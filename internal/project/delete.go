package project

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// deleteCmd wires `palbase project delete <projectId>`.
//
// DELETE /api/v2/projects/{projectId} is OWNER-only and takes
// `{"confirm_name": "<the project's NAME>"}` — the name, not the id: typing a
// name you can read off the dashboard is the anti-fat-finger gate, and it is
// what the server compares. 202: DeleteProjectWorkflow tears down every
// Environment underneath.
func deleteCmd(r Resolvers) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <projectId>",
		Args:  cobra.ExactArgs(1),
		Short: "Permanently delete a project and every environment under it",
		Long: `Permanently delete a project and ALL of its environments (databases,
backend runtimes, storage, API keys, secrets, apps).

THIS IS IRREVERSIBLE. You will be prompted to type the project's NAME to
confirm. Pass --yes to skip the prompt in a non-interactive shell.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]
			ctx := cmd.Context()

			// Read the name first: it is what the server's confirm_name compares,
			// and showing it makes the prompt meaningful ("delete todoapp?").
			detail, err := selection.GetProject(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}

			if !yes {
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "This will permanently delete project %q (%s) and every environment under it.\n", detail.Name, projectID)
				fmt.Fprintf(out, "Type the project name to confirm: ")
				scanner := bufio.NewScanner(cmd.InOrStdin())
				scanner.Scan()
				typed := strings.TrimSpace(scanner.Text())
				if typed != detail.Name {
					return fmt.Errorf("confirmation mismatch: expected %q, got %q — delete cancelled", detail.Name, typed)
				}
			}

			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			if err := r.REST().Do(ctx, http.MethodDelete, "/api/v2/projects/"+projectID,
				map[string]any{"confirm_name": detail.Name}, &handle); err != nil {
				return err
			}

			// The selection now points at a project that is being torn down. Drop it
			// rather than leaving every later command to fail with a 404.
			if cfg, cfgErr := selection.Load(""); cfgErr == nil && cfg.ProjectID == projectID {
				if rmErr := os.Remove(selection.ConfigPath("")); rmErr == nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "removed %s (it selected the deleted project)\n", selection.ConfigPath(""))
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ teardown started for %s (%s)\n", detail.Name, projectID)
			fmt.Fprintf(cmd.OutOrStdout(), "  workflow: %s\n", handle.WorkflowID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (for scripted use)")
	return cmd
}
