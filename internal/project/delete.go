package project

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// deleteCmd wires `palbase project delete <ref>`.
//
// Deletion is irreversible — it tears down the entire project stack. The
// server's project.delete mutation requires confirmRef == ref (anti-accidental-
// delete), and the CLI adds its own guard: an interactive prompt by default,
// skippable with --yes for scripted use.
func deleteCmd(studioFn func() *studio.Client) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Permanently delete a project and tear down its stack",
		Long: `Permanently delete a project and all its associated resources
(database, backend runtime, storage, API keys, secrets, …).

THIS IS IRREVERSIBLE. You will be prompted to re-type the project ref
to confirm. Pass --yes to skip the prompt in non-interactive environments.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]

			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(), "This will permanently delete project %q and all its resources.\n", ref)
				fmt.Fprintf(cmd.OutOrStdout(), "Type the project ref to confirm: ")
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Scan()
				typed := strings.TrimSpace(scanner.Text())
				if typed != ref {
					return fmt.Errorf("confirmation mismatch: expected %q, got %q — delete cancelled", ref, typed)
				}
			}

			// ponytail: out is nil — project.delete returns void, nothing to decode
			if err := studioFn().Mutation(cmd.Context(), "project.delete", map[string]any{
				"ref":        ref,
				"confirmRef": ref,
			}, nil); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ deleted project %s\n", ref)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation prompt (for scripted use)")
	return cmd
}
