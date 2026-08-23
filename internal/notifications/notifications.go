// Package notifications provides the `palbase notifications` subcommand group:
// providers / add / remove. These commands are GUIDED authoring for the
// notifications config-as-code surface — they read/write config/notifications.ts
// (the typed DSL from @palbase/backend) AND upload each provider's cert/key
// secret to a reserved encrypted env var, so an author declares providers
// without hand-writing TypeScript and without ever putting a secret in git.
//
// On `git push`, the deploy evals config/notifications.ts, resolves each enabled
// provider's reserved secret (PB_NOTIFICATIONS_<PROVIDER>_<FIELD>), and creates
// the missing providers via the PalNotify admin API (create-only; never deletes).
//
// The CLI is the SOLE author of config/notifications.ts: every write regenerates
// the whole file from the current provider set (deterministic template). The
// non-secret fields go in the file; the secret goes to env via the SAME env.set
// mutation `palbase secret set --file` uses (isSecret=true).
package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Resolvers carries the lazily-built Studio client, populated by
// PersistentPreRunE on the root command before any subcommand fires. The Studio
// client uploads provider secrets via the env.set mutation (the SAME path
// `palbase secret set` uses).
type Resolvers struct {
	// REST is where the senders live now that config/notifications.ts is gone.
	REST      func(*cobra.Command) (REST, error)
	Studio    func() *studio.Client
	Selection func() *selection.Resolver
}

// Cmd returns the `palbase notifications` parent command.
// providerEntry is one sender as the CLI assembles it before sending: whether it
// is on, and the non-secret fields it needs. Secrets never travel in it — they
// go to the vault and the stack reads them from there.
//
// It moved here when the config-file layer was deleted; this is the only place
// that builds one now.
type providerEntry struct {
	enabled bool
	fields  map[string]string
}

// MarshalJSON emits the shape the module reads. Written out rather than relying
// on struct tags because the fields are unexported — they were never meant to
// cross a package boundary and still are not.
func (e providerEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"enabled": e.enabled, "fields": e.fields})
}

// REST reaches the linked stack's management surface.
type REST interface {
	Do(ctx context.Context, method, path string, body []byte) (int, []byte, error)
}

const providersPath = "/v1/management/notifications/providers"

func call(r Resolvers, cmd *cobra.Command, method, path string, body []byte) ([]byte, error) {
	rest, err := r.REST(cmd)
	if err != nil {
		return nil, err
	}
	status, raw, err := rest.Do(cmd.Context(), method, path, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Description != "" {
			return nil, fmt.Errorf("%s: %s", e.Error, e.Description)
		}
		return nil, fmt.Errorf("the stack answered %d: %s", status, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "Manage this project's notification senders",
		Long: `The push/email/SMS senders this project delivers through.

  palbase notifications providers                 Show the catalog and what is configured.
  palbase notifications add <provider> [flags]    Configure a sender.
  palbase notifications remove <provider>         Stop delivering through one.

They live ON THE STACK and take effect immediately. They used to be declared in
config/notifications.ts, created on every deploy and never deleted when dropped
from the file — so "removed" and "still sending" were both true.

Provider SECRETS go to the vault, encrypted, and are never read back: the listing
says WHICH senders are configured, not with what.`,
	}
	cmd.AddCommand(providersCmd(r), addCmd(r), removeCmd(r))
	return cmd
}

// providersCmd lists the catalog + which providers are configured in config.
func providersCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "Show the catalog and what this stack has configured",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := call(r, cmd, http.MethodGet, providersPath, nil)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			configured := map[string]bool{}
			var live []struct {
				Provider string `json:"provider"`
				Name     string `json:"name"`
			}
			if json.Unmarshal(raw, &live) == nil {
				for _, p := range live {
					key := p.Provider
					if key == "" {
						key = p.Name
					}
					configured[key] = true
				}
			}
			fmt.Fprintln(out, "notification senders (● configured on this stack):")
			fmt.Fprintln(out)
			for _, spec := range catalog {
				mark := " "
				if configured[spec.name] {
					mark = "●"
				}
				fmt.Fprintf(out, "  %s %-18s %s\n", mark, spec.name, spec.channel)
			}
			return nil
		},
	}
}

// addCmd configures a single provider: it (1) uploads the provider's secret(s)
// to the reserved env key via env.set (isSecret=true), then (2) writes/updates
// the provider's non-secret fields in config/notifications.ts (enabled:true).
func addCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <provider> [flags]",
		Short: "Configure a provider: upload its secret + write its config entry",
		Long: `Configure a notification provider. The provider's NON-SECRET fields are passed
as flags and written to config/notifications.ts; its SECRET (cert/key/api-key) is
read from a file (or prompted, hidden) and uploaded to a reserved encrypted env
var (PB_NOTIFICATIONS_<PROVIDER>_<FIELD>) — never written to git.

Examples:
  palbase notifications add apns --team-id T --key-id K --bundle-id com.acme.app --p8-file AuthKey.p8
  palbase notifications add fcm --service-account-file service-account.json
  palbase notifications add sendgrid --from-domain mail.acme.com            (prompts for API key)
  palbase notifications add twilio --account-sid AC.. --messaging-sid MG..  (prompts for auth token)

Run ` + "`palbase notifications providers`" + ` to see every provider's flags.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			spec := specByName(name)
			if spec == nil {
				return fmt.Errorf("unknown provider %q — run `palbase notifications providers` to list them", name)
			}

			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			ref := sel.EnvironmentRef()

			// 1. Collect non-secret fields from flags; validate required ones.
			entry := providerEntry{enabled: true, fields: map[string]string{}}
			for _, f := range spec.fields {
				raw, _ := cmd.Flags().GetString(f.flag)
				if f.isBool {
					// Bool flags are tri-state here: present → value, absent → unset.
					if cmd.Flags().Changed(f.flag) {
						bv, _ := cmd.Flags().GetBool(f.flag)
						entry.fields[f.name] = strconv.FormatBool(bv)
					}
					continue
				}
				if raw == "" {
					if f.required {
						return fmt.Errorf("provider %q requires --%s (%s)", name, f.flag, f.help)
					}
					continue
				}
				if f.isInt {
					if _, perr := strconv.Atoi(raw); perr != nil {
						return fmt.Errorf("--%s must be a number (got %q)", f.flag, raw)
					}
				}
				entry.fields[f.name] = raw
			}
			if verr := validateProvider(spec, entry); verr != nil {
				return verr
			}

			// 2. Resolve + upload each secret to its reserved env key FIRST. If the
			// upload fails we never write the config (so config never references a
			// secret that isn't set). Order: secrets, then config.
			out := cmd.OutOrStdout()
			for _, s := range spec.secrets {
				value, serr := resolveSecretValue(cmd, name, s)
				if serr != nil {
					return serr
				}
				reserved := reservedSecretKey(name, s.name)
				if uerr := r.Studio().Mutation(cmd.Context(), "env.set", map[string]any{
					"ref":      ref,
					"key":      reserved,
					"value":    value,
					"isSecret": true,
				}, nil); uerr != nil {
					return fmt.Errorf("upload secret %s: %w", reserved, uerr)
				}
				fmt.Fprintf(out, "✓ uploaded secret %s (encrypted)\n", reserved)
			}

			// 3. Tell the stack. No file, no deploy to wait for.
			body, err := json.Marshal(map[string]any{"provider": name, "config": entry})
			if err != nil {
				return err
			}
			if _, err := call(r, cmd, http.MethodPost, providersPath, body); err != nil {
				return err
			}
			fmt.Fprintf(out, "✓ provider %q configured — delivering now\n", name)
			return nil
		},
	}
	// Register a flag for every catalog field + every secret file across all
	// providers. cobra ignores flags a given provider doesn't use; the RunE only
	// reads the ones in that provider's spec.
	registerProviderFlags(cmd)
	return cmd
}

// validateProvider enforces cross-field rules the flat required-check can't:
// twilio needs one of fromNumber / messagingServiceSid.
func validateProvider(spec *providerSpec, entry providerEntry) error {
	if spec.name == "twilio" {
		if entry.fields["fromNumber"] == "" && entry.fields["messagingServiceSid"] == "" {
			return fmt.Errorf("provider \"twilio\" requires one of --from-number or --messaging-sid")
		}
	}
	return nil
}

// registerProviderFlags declares every field flag + every secret `--<flag>-file`
// flag once on the add command. Names are unique across the catalog (verified by
// test), so a single shared flag set works for all providers.
func registerProviderFlags(cmd *cobra.Command) {
	seen := map[string]bool{}
	for _, spec := range catalog {
		for _, f := range spec.fields {
			if seen[f.flag] {
				continue
			}
			seen[f.flag] = true
			if f.isBool {
				cmd.Flags().Bool(f.flag, false, f.help)
			} else {
				cmd.Flags().String(f.flag, "", f.help)
			}
		}
		for _, s := range spec.secrets {
			fileFlag := s.flag + "-file"
			if !seen[fileFlag] {
				seen[fileFlag] = true
				cmd.Flags().String(fileFlag, "", s.help+" (file path)")
			}
		}
	}
}

// resolveSecretValue gets a provider secret's value: from its `--<flag>-file`
// flag if given, else (when the secret allows prompting) an interactive hidden
// prompt. A secret that can't be sourced is a hard error.
func resolveSecretValue(cmd *cobra.Command, provider string, s secretField) (string, error) {
	fileFlag := s.flag + "-file"
	path, _ := cmd.Flags().GetString(fileFlag)
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --%s %q: %w", fileFlag, path, err)
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return "", fmt.Errorf("--%s %q is empty", fileFlag, path)
		}
		return string(data), nil
	}
	if s.prompt {
		return promptHidden(cmd, fmt.Sprintf("%s %s: ", provider, s.help))
	}
	return "", fmt.Errorf("provider %q requires --%s (%s)", provider, fileFlag, s.help)
}

// promptHidden reads a secret from the terminal without echoing it. Falls back to
// a clear error when stdin is not a TTY (so a non-interactive run fails loudly
// rather than hanging or reading a blank value).
func promptHidden(cmd *cobra.Command, label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("no value provided and stdin is not a terminal — pass the secret via its --*-file flag")
	}
	fmt.Fprint(cmd.OutOrStdout(), label)
	data, err := term.ReadPassword(fd)
	fmt.Fprintln(cmd.OutOrStdout())
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", fmt.Errorf("empty value — aborting")
	}
	return value, nil
}

// removeCmd disables + drops a provider from config/notifications.ts. The live
// provider stays until removed in Studio (the deploy is upsert-only / never
// auto-deletes). The reserved secret is NOT deleted here (use `palbase secret
// remove <key>` if you want to purge it).
func removeCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <provider>",
		Short: "Stop delivering through one sender",
		Long: `Remove a sender from the stack.

It stops delivering. This used to drop the entry from config/notifications.ts and
leave the live provider in place, so "removed" and "still sending" were both true
at once.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := call(r, cmd, http.MethodDelete, providersPath+"/"+url.PathEscape(args[0]), nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ provider %q removed\n", args[0])
			return nil
		},
	}
}

// firstSecretName returns a provider's first secret field name (for the remove
// hint). Every provider has at least one secret in the catalog.
func firstSecretName(provider string) string {
	if spec := specByName(provider); spec != nil && len(spec.secrets) > 0 {
		return spec.secrets[0].name
	}
	return ""
}

// sortedProviderNames returns the catalog provider names sorted (test helper +
// stable iteration where needed).
func sortedProviderNames() []string {
	names := make([]string, 0, len(catalog))
	for _, s := range catalog {
		names = append(names, s.name)
	}
	sort.Strings(names)
	return names
}
