// Package egress provides the `palbase egress` subcommand group: list / add /
// remove. It is GUIDED authoring for the outbound-HTTP allowlist config-as-code
// surface — the same shape `palbase storage` and `palbase flags` have — so an
// author declares hosts without hand-writing TypeScript and without guessing the
// deploy's fail-closed host rules.
//
// config/egress.ts was the one module config surface with NO command: it had to
// be hand-written, and a host the deploy rejects (a URL, a port, an IP literal,
// an internal name) only surfaced as a FAILED deploy. These commands run the same
// checks locally, so the mistake is caught at authoring time.
//
// The CLI is the SOLE author of config/egress.ts: every write regenerates the
// whole file from the current host set. A hand-edit that keeps the generated
// `defineEgress({ hosts: [...] })` shape still round-trips.
//
// No network, no secrets — pure local file authoring. The allowlist is enforced
// at deploy (artifact manifest → isolate → tenantFetch), not from here.
package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// configPath is the project-relative path the deploy evals.
const configPath = "config/egress.ts"

// hostEntryRE pulls the quoted host strings out of the hosts: [ ... ] array.
var hostEntryRE = regexp.MustCompile(`"([^"]*)"`)

// hostsArrayRE locates the hosts array body. Line comments are stripped first so
// a commented-out host is not read back as a real one.
var hostsArrayRE = regexp.MustCompile(`\bhosts\s*:\s*\[([^\]]*)\]`)

var lineCommentRE = regexp.MustCompile(`(?m)//[^\n]*`)

// Cmd returns the `palbase egress` parent command.
// REST reaches the linked stack's management surface.
type REST interface {
	Do(ctx context.Context, method, path string, body []byte) (int, []byte, error)
}

// Resolvers carries the lazily-built dependency; resolving announces the target.
type Resolvers struct {
	REST func(*cobra.Command) (REST, error)
}

const endpoint = "/v1/management/egress"

// fence is the wire shape of the allowlist.
type fence struct {
	Hosts     []string `json:"hosts"`
	TimeoutMs *int     `json:"timeout_ms,omitempty"`
}

func read(r Resolvers, cmd *cobra.Command) (fence, REST, error) {
	rest, err := r.REST(cmd)
	if err != nil {
		return fence{}, nil, err
	}
	status, raw, err := rest.Do(cmd.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		return fence{}, nil, err
	}
	if status >= 400 {
		return fence{}, nil, fmt.Errorf("the stack answered %d reading the fence: %s", status, strings.TrimSpace(string(raw)))
	}
	var f fence
	if len(raw) > 0 && json.Unmarshal(raw, &f) != nil {
		return fence{}, nil, fmt.Errorf("the stack's fence is not readable: %s", strings.TrimSpace(string(raw)))
	}
	return f, rest, nil
}

func write(rest REST, cmd *cobra.Command, f fence) error {
	if f.Hosts == nil {
		// An explicitly empty list is a VALUE — "call nothing" — and must travel
		// as one. Sending null would read as "nobody has said", which is the
		// state a deliberately closed fence must not collapse into.
		f.Hosts = []string{}
	}
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	status, raw, err := rest.Do(cmd.Context(), http.MethodPut, endpoint, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Description != "" {
			return fmt.Errorf("%s: %s", e.Error, e.Description)
		}
		return fmt.Errorf("the stack refused the fence (%d): %s", status, strings.TrimSpace(string(raw)))
	}
	return nil
}

// Cmd builds `palbase egress`.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Manage the outbound-HTTP allowlist",
		Long: `Declare the external hosts your backend may fetch().

  palbase egress list             Show the hosts this stack allows.
  palbase egress add <host>       Allow an external host.
  palbase egress remove <host>    Stop allowing a host.

The backend has NO ambient network: a host that is not on this list is refused at
runtime. Hosts are bare hostnames — https on :443 only, no scheme, port, path or
wildcard. A leading dot (".example.com") also covers subdomains.

THE LIST LIVES ON THE STACK, not in the source tree. It used to be
config/egress.ts, applied on every push, which meant the panel could not change
it and two writers could disagree about it. It is set here and stamped into the
artifact by the next deploy — so a change made now takes effect when you push.`,
	}
	cmd.AddCommand(listCmd(r), addCmd(r), removeCmd(r))
	return cmd
}

func listCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use: "list", Args: cobra.NoArgs, Short: "Show the hosts this stack allows",
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, _, err := read(r, cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(f.Hosts) == 0 {
				fmt.Fprintln(out, "no outbound hosts allowed (the backend cannot make external calls)")
				fmt.Fprintln(out, "  allow one: palbase egress add api.example.com")
				return nil
			}
			hosts := append([]string(nil), f.Hosts...)
			sort.Strings(hosts)
			fmt.Fprintln(out, "hosts this stack allows:")
			for _, h := range hosts {
				suffix := ""
				if strings.HasPrefix(h, ".") {
					suffix = "   (and subdomains)"
				}
				fmt.Fprintf(out, "  %s%s\n", h, suffix)
			}
			return nil
		},
	}
}

func addCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use: "add <host>", Args: cobra.ExactArgs(1), Short: "Allow an external host",
		Long: `Add a host to the outbound-HTTP allowlist.

  palbase egress add api.example.com     Allow exactly that host.
  palbase egress add .example.com        Allow it and every subdomain.

It is stored on the stack immediately and stamped into the artifact by the next
deploy.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			host := strings.ToLower(strings.TrimSpace(args[0]))
			// Checked HERE as well as on the stack, because a security allowlist
			// is never best-effort and finding out through a failed deploy is a
			// bad way to learn the rules.
			if err := validateHost(host); err != nil {
				return fmt.Errorf("host %q: %w", args[0], err)
			}
			f, rest, err := read(r, cmd)
			if err != nil {
				return err
			}
			for _, h := range f.Hosts {
				if h == host {
					fmt.Fprintf(cmd.OutOrStdout(), "%s is already allowed\n", host)
					return nil
				}
			}
			f.Hosts = append(f.Hosts, host)
			sort.Strings(f.Hosts)
			if err := write(rest, cmd, f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s allowed — effective on the next deploy\n", host)
			return nil
		},
	}
}

func removeCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use: "remove <host>", Args: cobra.ExactArgs(1), Short: "Stop allowing a host",
		RunE: func(cmd *cobra.Command, args []string) error {
			host := strings.ToLower(strings.TrimSpace(args[0]))
			f, rest, err := read(r, cmd)
			if err != nil {
				return err
			}
			kept := make([]string, 0, len(f.Hosts))
			found := false
			for _, h := range f.Hosts {
				if h == host {
					found = true
					continue
				}
				kept = append(kept, h)
			}
			if !found {
				fmt.Fprintf(cmd.OutOrStdout(), "%s was not allowed\n", host)
				return nil
			}
			f.Hosts = kept
			if err := write(rest, cmd, f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s removed — effective on the next deploy\n", host)
			return nil
		},
	}
}

// validateHost mirrors the backend's validateEgressHost (modules/backend
// internal/runtime/egress_config.go) so the CLI rejects locally exactly what the
// deploy would reject — a security allowlist is never best-effort, and finding
// out via a failed deploy is a bad way to learn the rules.
func validateHost(h string) error {
	if h == "" {
		return fmt.Errorf("empty host")
	}
	if h != strings.ToLower(h) {
		return fmt.Errorf("must be lowercase")
	}
	bare := strings.TrimPrefix(h, ".")
	if strings.ContainsAny(bare, "/:@ *?#") {
		return fmt.Errorf("hostname only — no scheme, port, path, or wildcard (use %q, not a URL)", "api.example.com")
	}
	if net.ParseIP(bare) != nil {
		return fmt.Errorf("IP literals not allowed — use a hostname")
	}
	for _, r := range bare {
		if r > 127 {
			return fmt.Errorf("non-ASCII not allowed (use the punycode host)")
		}
	}
	if !strings.Contains(bare, ".") {
		return fmt.Errorf("single-label host not allowed")
	}
	labels := strings.Split(bare, ".")
	tld := labels[len(labels)-1]
	hasAlpha := false
	for _, r := range tld {
		if r >= 'a' && r <= 'z' {
			hasAlpha = true
			break
		}
	}
	if !hasAlpha {
		return fmt.Errorf("top-level label must be alphabetic (rejects short-form/decimal/hex IP literals)")
	}
	if bare == "localhost" {
		return fmt.Errorf("internal host not allowed")
	}
	for _, bad := range []string{".svc", ".cluster.local", ".internal", ".localhost"} {
		if strings.HasSuffix(bare, bad) {
			return fmt.Errorf("internal host not allowed")
		}
	}
	return nil
}
