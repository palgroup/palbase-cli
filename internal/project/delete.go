package project

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// deleteCmd wires `palbase project delete <ref>` over the Management API
// (DELETE /api/v1/projects/{ref}) — the same DPoP/PAT transport every other
// project verb uses, so delete works headless too.
//
// Deletion is irreversible — it tears down the entire project stack. The
// server requires confirm_ref == ref (anti-accidental-delete), and the CLI
// adds its own guard: an interactive prompt by default, skippable with --yes
// for scripted use.
func deleteCmd(rest func() REST) *cobra.Command {
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

			if err := rest().Do(cmd.Context(), http.MethodDelete, "/api/v1/projects/"+ref,
				map[string]any{"confirm_ref": ref}, nil); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ deleted project %s\n", ref)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation prompt (for scripted use)")
	return cmd
}
