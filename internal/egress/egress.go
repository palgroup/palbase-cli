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
	"fmt"
	"net"
	"os"
	"path/filepath"
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
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "Manage the outbound-HTTP allowlist (config/egress.ts)",
		Long: `Declare the external hosts your backend may fetch().

  palbase egress list             Show the hosts declared in config/egress.ts.
  palbase egress add <host>       Allow an external host.
  palbase egress remove <host>    Stop allowing a host.

The backend has NO ambient network: a host that is not on this list is refused at
runtime, and no config/egress.ts at all means no outbound calls. Hosts are bare
hostnames — https on :443 only, no scheme, port, path or wildcard. A leading dot
(".example.com") also covers subdomains.

config/egress.ts is git-authoritative: commit it and ` + "`git push`" + ` to deploy.`,
	}
	cmd.AddCommand(listCmd(), addCmd(), removeCmd())
	return cmd
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

// readConfig parses config/egress.ts into the declared host list. A missing file
// is NOT an error — it means "no hosts yet" (add creates the file). A
// present-but-unparseable file IS an error so we never clobber something we
// don't understand.
func readConfig() ([]string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	return parseConfig(string(data))
}

func parseConfig(src string) ([]string, error) {
	if !strings.Contains(src, "defineEgress") {
		return nil, fmt.Errorf("%s does not look like a defineEgress() config (no defineEgress call found) — refusing to overwrite; remove or fix it manually", configPath)
	}
	src = lineCommentRE.ReplaceAllString(src, "")
	m := hostsArrayRE.FindStringSubmatch(src)
	if m == nil {
		return nil, nil
	}
	var hosts []string
	for _, hm := range hostEntryRE.FindAllStringSubmatch(m[1], -1) {
		hosts = append(hosts, hm[1])
	}
	return hosts, nil
}

func writeConfig(hosts []string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(configPath), err)
	}
	return os.WriteFile(configPath, []byte(generateConfig(hosts)), 0o644)
}

// generateConfig renders the full config/egress.ts source. Deterministic: hosts
// sorted, one per line, so a diff shows exactly what changed.
func generateConfig(hosts []string) string {
	sorted := append([]string(nil), hosts...)
	sort.Strings(sorted)

	var b strings.Builder
	b.WriteString("// Generated + maintained by `palbase egress`. Edit via the CLI, or by hand\n")
	b.WriteString("// keeping the `defineEgress({ hosts: [...] })` shape. The backend has NO\n")
	b.WriteString("// ambient network: a host missing here is refused at runtime.\n")
	b.WriteString("import { defineEgress } from \"@palbase/backend\";\n\n")
	b.WriteString("export default defineEgress({\n")
	b.WriteString("  hosts: [\n")
	for _, h := range sorted {
		fmt.Fprintf(&b, "    %q,\n", h)
	}
	b.WriteString("  ],\n")
	b.WriteString("});\n")
	return b.String()
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "Show the hosts declared in config/egress.ts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, err := readConfig()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(hosts) == 0 {
				fmt.Fprintln(out, "no outbound hosts allowed (the backend cannot make external calls)")
				fmt.Fprintln(out, "  allow one: palbase egress add api.example.com")
				return nil
			}
			sort.Strings(hosts)
			fmt.Fprintf(out, "hosts allowed in %s:\n", configPath)
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

func addCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <host>",
		Args:  cobra.ExactArgs(1),
		Short: "Allow an external host in config/egress.ts",
		Long: `Add a host to the outbound-HTTP allowlist.

  palbase egress add api.example.com     Allow exactly that host.
  palbase egress add .example.com        Allow it and every subdomain.

Commit config/egress.ts and ` + "`git push`" + ` to apply.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			host := strings.ToLower(strings.TrimSpace(args[0]))
			if err := validateHost(host); err != nil {
				return fmt.Errorf("host %q: %w", args[0], err)
			}
			hosts, err := readConfig()
			if err != nil {
				return err
			}
			for _, h := range hosts {
				if h == host {
					fmt.Fprintf(cmd.OutOrStdout(), "host %q already allowed in %s\n", host, configPath)
					return nil
				}
			}
			hosts = append(hosts, host)
			if err := writeConfig(hosts); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ allowed host %q in %s\n", host, configPath)
			fmt.Fprintf(out, "  commit %s and `git push` to deploy\n", configPath)
			return nil
		},
	}
}

func removeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <host>",
		Args:  cobra.ExactArgs(1),
		Short: "Stop allowing a host in config/egress.ts",
		RunE: func(cmd *cobra.Command, args []string) error {
			host := strings.ToLower(strings.TrimSpace(args[0]))
			hosts, err := readConfig()
			if err != nil {
				return err
			}
			kept := make([]string, 0, len(hosts))
			found := false
			for _, h := range hosts {
				if h == host {
					found = true
					continue
				}
				kept = append(kept, h)
			}
			if !found {
				return fmt.Errorf("host %q is not in %s", host, configPath)
			}
			if err := writeConfig(kept); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ removed host %q from %s\n", host, configPath)
			fmt.Fprintln(out, "  the backend can no longer call it once you commit + `git push`")
			return nil
		},
	}
}
