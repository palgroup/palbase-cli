// Package secret provides the `palbase secret` subcommand group:
// set / list / remove. These commands manage a branch's remote env vars
// via Studio tRPC (user JWT → Studio env.* → control-pg). NOT
// dev-server-local.
package secret

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// Resolvers carries the lazily-built Studio client, populated by
// PersistentPreRunE on the root command before any subcommand fires.
type Resolvers struct {
	Studio func() *studio.Client
}

// Cmd returns the `palbase secret` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage a branch's remote env vars and secrets",
		Long: `Commands to manage environment variables for a Palbase project branch.

  palbase secret set KEY=value          Set a plain env var.
  palbase secret set KEY=value --secret Mark as encrypted secret (value masked in list).
  palbase secret list                   List all env vars (secrets shown masked).
  palbase secret remove KEY             Delete an env var.
  palbase secret pull                   Write the branch's env vars (decrypted) to .env.local.
  palbase secret push                   Push local .env.local changes back to the branch.

All changes are applied to the branch's remote configuration via Studio tRPC.`,
	}
	cmd.AddCommand(
		setCmd(r.Studio),
		listCmd(r.Studio),
		removeCmd(r.Studio),
		pullCmd(r.Studio),
		pushCmd(r.Studio),
	)
	return cmd
}

// reservedKeyPrefix is the env-var namespace owned by the platform's managed
// provider secrets (`palbase notifications add` uploads cert/key/api-key secrets
// under PB_NOTIFICATIONS_*). A hand-set `palbase secret set PB_*` is REFUSED so a
// user can't shadow or collide with a managed secret — they're meant to be set
// via the guided `notifications` commands, which derive the exact key.
const reservedKeyPrefix = "PB_"

// guardReservedKey refuses a key under the reserved PB_ namespace. Returns a
// clear error pointing the user at the right command.
func guardReservedKey(key string) error {
	if strings.HasPrefix(key, reservedKeyPrefix) {
		return fmt.Errorf("key %q is in the reserved %s* namespace (managed provider secrets) — set provider secrets with `palbase notifications add <provider>`, not `secret set`", key, reservedKeyPrefix)
	}
	return nil
}

func setCmd(studioFn func() *studio.Client) *cobra.Command {
	var (
		refFlag  string
		isSecret bool
		fileFlag string
		plain    bool
	)
	cmd := &cobra.Command{
		Use:   "set <KEY=value> | set <KEY> --file <path>",
		Short: "Set an env var (use --secret to mark encrypted; --file for a multi-line cert/key)",
		Long: `Set an env var on the branch. Two forms:

  palbase secret set API_URL=https://...           inline value (plain by default)
  palbase secret set API_URL=https://... --secret  inline value, marked encrypted
  palbase secret set APNS_P8 --file AuthKey.p8      value from a file (multi-line
                                                    certs/keys/JSON), encrypted by
                                                    default — pass --plain to opt out

The --file form takes a bare KEY arg (no =value) and reads the file's contents
verbatim as the value, so PEM keys and service-account JSON survive intact. The
file itself stays local (gitignored); only its encrypted value is uploaded.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var key, value string

			if fileFlag != "" {
				// --file form: the arg is a bare KEY (no =value), value comes from the file.
				key = args[0]
				if strings.Contains(key, "=") {
					return fmt.Errorf("with --file, pass a bare KEY (got %q) — the value is read from the file", key)
				}
				data, err := os.ReadFile(fileFlag)
				if err != nil {
					return fmt.Errorf("read --file %q: %w", fileFlag, err)
				}
				value = string(data)
				// A file value (cert/key/JSON) is a secret unless explicitly --plain.
				if !plain {
					isSecret = true
				}
			} else {
				// Inline KEY=value form.
				parts := strings.SplitN(args[0], "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("argument must be in KEY=value format (got %q) — or use --file for a file value", args[0])
				}
				key, value = parts[0], parts[1]
			}
			if key == "" {
				return fmt.Errorf("key must not be empty")
			}
			if err := guardReservedKey(key); err != nil {
				return err
			}

			ref, err := auth.ResolveProjectRef(refFlag)
			if err != nil {
				return err
			}

			if err := studioFn().Mutation(cmd.Context(), "env.set", map[string]any{
				"ref":      ref,
				"key":      key,
				"value":    value,
				"isSecret": isSecret,
			}, nil); err != nil {
				return fmt.Errorf("env.set: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ set %s\n", key)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().BoolVar(&isSecret, "secret", false, "Mark value as encrypted secret (masked in list)")
	cmd.Flags().StringVar(&fileFlag, "file", "", "Read the value from a file (multi-line certs/keys/JSON); encrypted by default")
	cmd.Flags().BoolVar(&plain, "plain", false, "With --file, store the value as a plain (non-secret) env var")
	return cmd
}

// envVar mirrors the tRPC env.list output row.
type envVar struct {
	Key       string  `json:"key"`
	IsSecret  bool    `json:"isSecret"`
	Value     *string `json:"value"`
	UpdatedAt string  `json:"updatedAt"`
}

func listCmd(studioFn func() *studio.Client) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all env vars for the branch (secrets shown masked)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := auth.ResolveProjectRef(refFlag)
			if err != nil {
				return err
			}

			var rows []envVar
			if err := studioFn().Query(cmd.Context(), "env.list", map[string]any{"ref": ref}, &rows); err != nil {
				return fmt.Errorf("env.list: %w", err)
			}

			out := cmd.OutOrStdout()
			if len(rows) == 0 {
				fmt.Fprintln(out, "(no env vars)")
				return nil
			}
			return printEnvTable(out, rows)
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	return cmd
}

// pulledVar is one decrypted env var returned by Studio's env.pull.
type pulledVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func pullCmd(studioFn func() *studio.Client) *cobra.Command {
	var (
		refFlag string
		outFlag string
		force   bool
	)
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Write the branch's env vars (decrypted) to .env.local",
		Long: `Fetch every env var for the branch (plain + decrypted secrets) and write
them to a local dotenv file (default .env.local) for local development.

Existing keys in the file are UPDATED from remote; keys present only locally are
KEPT (local-only overrides survive a pull). Pass --force to overwrite the file
with exactly the remote set instead of merging.

The file is gitignored by the scaffold — secrets never enter git.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := auth.ResolveProjectRef(refFlag)
			if err != nil {
				return err
			}
			outPath := outFlag
			if outPath == "" {
				outPath = ".env.local"
			}

			var remote []pulledVar
			if err := studioFn().Query(cmd.Context(), "env.pull", map[string]any{"ref": ref}, &remote); err != nil {
				return fmt.Errorf("env.pull: %w", err)
			}

			// Merge into the existing file unless --force. Remote wins on shared
			// keys; local-only keys are preserved (developer overrides). Order:
			// existing keys keep their position, new remote keys appended sorted.
			merged, order := loadDotenv(outPath, force)
			for _, v := range remote {
				if _, seen := merged[v.Key]; !seen {
					order = append(order, v.Key)
				}
				merged[v.Key] = v.Value
			}

			if err := writeDotenv(outPath, merged, order); err != nil {
				return fmt.Errorf("write %s: %w", outPath, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ wrote %d var(s) to %s\n", len(remote), outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVarP(&outFlag, "out", "o", "", "Output dotenv file (default .env.local)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite the file with exactly the remote set (no merge)")
	return cmd
}

func pushCmd(studioFn func() *studio.Client) *cobra.Command {
	var (
		refFlag      string
		inFlag       string
		secretKeys   []string
		plainKeys    []string
		includeNew   bool
		forceSecrets bool
		dryRun       bool
	)
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push local .env.local changes back to the branch",
		Long: `Read a local dotenv file (default .env.local) and upsert every var to the
branch's remote config — the inverse of ` + "`secret pull`" + `.

Only keys whose value DIFFERS from remote (or are new) are written, so a push
after a pull is a no-op. A key's secret-ness is preserved from remote; mark NEW
keys as secret with --secret KEY1,KEY2. Push never DELETES remote keys that are
absent locally (use ` + "`secret remove`" + ` for that).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := auth.ResolveProjectRef(refFlag)
			if err != nil {
				return err
			}
			inPath := inFlag
			if inPath == "" {
				inPath = ".env.local"
			}
			local, _ := loadDotenv(inPath, false)
			if len(local) == 0 {
				return fmt.Errorf("%s has no env vars to push", inPath)
			}

			// Current remote state: which keys exist + their plain values +
			// secret-ness. Secret values come back MASKED (value nil), so we
			// can't diff them — they are NOT re-pushed unless --force-secrets.
			var remote []envVar
			if err := studioFn().Query(cmd.Context(), "env.list", map[string]any{"ref": ref}, &remote); err != nil {
				return fmt.Errorf("env.list: %w", err)
			}
			remotePlain := map[string]string{}
			remoteSecret := map[string]bool{}
			for _, r := range remote {
				remoteSecret[r.Key] = r.IsSecret
				if r.Value != nil {
					remotePlain[r.Key] = *r.Value
				}
			}
			// Explicit per-key classification for NEW keys. Empty tokens are
			// dropped so a stray `--secret ""` can't create a phantom slot.
			markSecret := toSet(secretKeys)
			markPlain := toSet(plainKeys)
			for k := range markSecret {
				if markPlain[k] {
					return fmt.Errorf("key %q passed to both --secret and --plain", k)
				}
			}

			// Classify every local key into a push plan. SECURITY (review
			// findings 1+2+3):
			//   • New keys (not on remote) NEVER auto-upload. They are
			//     credentials by default: secret unless the user says --plain,
			//     and they only push when the user opts in (--include-new /
			//     explicit --secret/--plain classification). This stops a
			//     local-only var from silently leaking to remote, and stops a
			//     new credential from being stored in plaintext.
			//   • Remote SECRET keys are skipped (masked, can't diff) unless
			//     --force-secrets — so a stale local pull can't clobber a secret
			//     someone rotated since.
			//   • Remote PLAIN keys push only when the value actually changed.
			type plan struct {
				key      string
				isSecret bool
				reason   string // shown in dry-run / confirmation
			}
			var toPush []plan
			var newUnclassified []string
			var skippedSecrets []string
			var skippedReserved []string
			for _, key := range sortedKeys(local) {
				val := local[key]
				_, existsRemote := remoteSecret[key]

				// Never push a reserved provider-secret key — it's managed by
				// `palbase notifications add` (which sets it directly), so a stale
				// .env.local copy must not clobber it.
				if guardReservedKey(key) != nil {
					skippedReserved = append(skippedReserved, key)
					continue
				}

				if !existsRemote {
					// New key. Default secret unless explicitly --plain.
					classifiedPlain := markPlain[key]
					classifiedSecret := markSecret[key]
					if !includeNew && !classifiedPlain && !classifiedSecret {
						newUnclassified = append(newUnclassified, key)
						continue
					}
					isSecret := !classifiedPlain // default secret; --plain opts out
					if classifiedSecret {
						isSecret = true
					}
					toPush = append(toPush, plan{key, isSecret, "new"})
					continue
				}

				if remoteSecret[key] {
					// Existing secret — masked, can't diff. Skip unless forced.
					if !forceSecrets {
						skippedSecrets = append(skippedSecrets, key)
						continue
					}
					toPush = append(toPush, plan{key, true, "secret (forced)"})
					continue
				}

				// Existing plain key — push only if the value changed.
				if cur, ok := remotePlain[key]; ok && cur == val {
					continue
				}
				toPush = append(toPush, plan{key, false, "changed"})
			}

			// New keys that weren't classified are a hard stop, not a silent
			// plaintext upload — surface them and tell the user how to proceed.
			if len(newUnclassified) > 0 {
				out := cmd.ErrOrStderr()
				fmt.Fprintf(out, "Refusing to push %d new key(s) not yet on remote (would be credentials):\n", len(newUnclassified))
				for _, k := range newUnclassified {
					fmt.Fprintf(out, "  %s\n", k)
				}
				fmt.Fprintln(out, "Classify each, then re-run:")
				fmt.Fprintln(out, "  --secret KEY[,KEY]   encrypt these new keys")
				fmt.Fprintln(out, "  --plain  KEY[,KEY]   store these new keys in plaintext")
				fmt.Fprintln(out, "  --include-new        include all new keys (defaults to SECRET; --plain to opt out per key)")
				return fmt.Errorf("%d new key(s) need classification", len(newUnclassified))
			}

			for _, k := range skippedSecrets {
				fmt.Fprintf(cmd.OutOrStdout(), "· skipped %s (existing secret — masked; pass --force-secrets to overwrite)\n", k)
			}
			for _, k := range skippedReserved {
				fmt.Fprintf(cmd.OutOrStdout(), "· skipped %s (reserved %s* namespace — managed by `palbase notifications add`)\n", k, reservedKeyPrefix)
			}

			pushed := 0
			for _, p := range toPush {
				if dryRun {
					fmt.Fprintf(cmd.OutOrStdout(), "would push %s%s [%s]\n", p.key, secretTag(p.isSecret), p.reason)
					pushed++
					continue
				}
				if err := studioFn().Mutation(cmd.Context(), "env.set", map[string]any{
					"ref": ref, "key": p.key, "value": local[p.key], "isSecret": p.isSecret,
				}, nil); err != nil {
					return fmt.Errorf("env.set %s: %w", p.key, err)
				}
				pushed++
			}
			verb := "pushed"
			if dryRun {
				verb = "would push"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s %d var(s) from %s\n", verb, pushed, inPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVarP(&inFlag, "in", "i", "", "Input dotenv file (default .env.local)")
	cmd.Flags().StringSliceVar(&secretKeys, "secret", nil, "NEW keys to encrypt (comma-separated)")
	cmd.Flags().StringSliceVar(&plainKeys, "plain", nil, "NEW keys to store in plaintext (comma-separated)")
	cmd.Flags().BoolVar(&includeNew, "include-new", false, "Include all new (not-on-remote) keys; each defaults to SECRET unless --plain")
	cmd.Flags().BoolVar(&forceSecrets, "force-secrets", false, "Re-upload existing secret keys (overwrites remote; use after rotating locally)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without writing")
	return cmd
}

// toSet builds a set from a flag slice, dropping empty/whitespace tokens so a
// stray `--secret ""` can't create a phantom classification.
func toSet(items []string) map[string]bool {
	s := map[string]bool{}
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it != "" {
			s[it] = true
		}
	}
	return s
}

func secretTag(isSecret bool) string {
	if isSecret {
		return " (secret)"
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// loadDotenv reads an existing dotenv file into a map + key order. When force is
// true (or the file is absent) it returns empty, so the caller writes only the
// remote set. Lines that aren't KEY=value (comments, blanks) are dropped on
// rewrite — the file is a generated dev artifact, not hand-curated.
func loadDotenv(path string, force bool) (map[string]string, []string) {
	vars := map[string]string{}
	order := []string{}
	if force {
		return vars, order
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return vars, order
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"`)
		if _, seen := vars[key]; !seen {
			order = append(order, key)
		}
		vars[key] = val
	}
	return vars, order
}

// writeDotenv writes key=value lines in `order`. Values containing whitespace or
// special chars are double-quoted so dotenv parsers read them back intact.
func writeDotenv(path string, vars map[string]string, order []string) error {
	var b strings.Builder
	b.WriteString("# Generated by `palbase secret pull` — do not commit (gitignored).\n")
	for _, key := range order {
		val := vars[key]
		if strings.ContainsAny(val, " \t\"'#") || val == "" {
			val = `"` + strings.ReplaceAll(val, `"`, `\"`) + `"`
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(val)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// printEnvTable writes a tabular view of env vars to w. Secrets are
// masked; plain values are shown inline.
func printEnvTable(w io.Writer, rows []envVar) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tVALUE\tUPDATED")
	for _, v := range rows {
		display := ""
		if v.IsSecret {
			display = "(secret)"
		} else if v.Value != nil {
			display = *v.Value
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", v.Key, display, v.UpdatedAt)
	}
	return tw.Flush()
}

func removeCmd(studioFn func() *studio.Client) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "remove <KEY>",
		Short: "Delete an env var",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			ref, err := auth.ResolveProjectRef(refFlag)
			if err != nil {
				return err
			}

			if err := studioFn().Mutation(cmd.Context(), "env.delete", map[string]any{
				"ref": ref,
				"key": key,
			}, nil); err != nil {
				return fmt.Errorf("env.delete: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ removed %s\n", key)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	return cmd
}
