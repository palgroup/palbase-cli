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

All changes are applied to the branch's remote configuration via Studio tRPC.`,
	}
	cmd.AddCommand(
		setCmd(r.Studio),
		listCmd(r.Studio),
		removeCmd(r.Studio),
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
