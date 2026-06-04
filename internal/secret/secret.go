// Package secret provides the `palbase secret` subcommand group:
// set / list / remove. These commands manage a branch's remote env vars
// via Studio tRPC (user JWT → Studio env.* → control-pg). NOT
// dev-server-local.
package secret

import (
	"errors"
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

// projectRef resolves the linked project ref. Order:
//  1. --ref flag override
//  2. .palbase/config.json in the project directory
//  3. error if neither is available
func projectRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	cfg, err := auth.LoadProjectConfig()
	if err != nil {
		if os.IsNotExist(errors.Unwrap(err)) || strings.Contains(err.Error(), "not linked") {
			return "", fmt.Errorf("project not linked — pass --ref or run from a project directory")
		}
		return "", err
	}
	if cfg.Ref == "" {
		return "", fmt.Errorf("project not linked — pass --ref or run from a project directory")
	}
	return cfg.Ref, nil
}

func setCmd(studioFn func() *studio.Client) *cobra.Command {
	var (
		refFlag  string
		isSecret bool
	)
	cmd := &cobra.Command{
		Use:   "set <KEY=value>",
		Short: "Set an env var (use --secret to mark as encrypted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parts := strings.SplitN(args[0], "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("argument must be in KEY=value format (got %q)", args[0])
			}
			key, value := parts[0], parts[1]
			if key == "" {
				return fmt.Errorf("key must not be empty")
			}

			ref, err := projectRef(refFlag)
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
			ref, err := projectRef(refFlag)
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
			ref, err := projectRef(refFlag)
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
		refFlag    string
		inFlag     string
		secretKeys []string
		dryRun     bool
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
			ref, err := projectRef(refFlag)
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
			// secret-ness. Secrets come back masked (value nil), so a secret's
			// value always counts as "changed" and is re-pushed (we can't compare
			// a masked value) — acceptable, the upsert is idempotent.
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
			markSecret := map[string]bool{}
			for _, k := range secretKeys {
				markSecret[strings.TrimSpace(k)] = true
			}

			pushed := 0
			for _, key := range sortedKeys(local) {
				val := local[key]
				_, existsRemote := remoteSecret[key]
				isSecret := remoteSecret[key] || markSecret[key]
				// Skip plain keys whose remote value already matches (no-op push).
				if existsRemote && !isSecret {
					if cur, ok := remotePlain[key]; ok && cur == val {
						continue
					}
				}
				if dryRun {
					fmt.Fprintf(cmd.OutOrStdout(), "would push %s%s\n", key, secretTag(isSecret))
					pushed++
					continue
				}
				if err := studioFn().Mutation(cmd.Context(), "env.set", map[string]any{
					"ref": ref, "key": key, "value": val, "isSecret": isSecret,
				}, nil); err != nil {
					return fmt.Errorf("env.set %s: %w", key, err)
				}
				pushed++
			}
			verb := "pushed"
			if dryRun {
				verb = "would push"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s %d changed var(s) from %s\n", verb, pushed, inPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVarP(&inFlag, "in", "i", "", "Input dotenv file (default .env.local)")
	cmd.Flags().StringSliceVar(&secretKeys, "secret", nil, "Mark these NEW keys as encrypted secrets (comma-separated)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without writing")
	return cmd
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
			ref, err := projectRef(refFlag)
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
