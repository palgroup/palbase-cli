package project

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

// REST is the subset of the Management-API transport the project
// commands use. transport.Client satisfies it; tests substitute a stub.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// createCmd wires `palbase project create`. Project provisioning is
// async (Temporal): POST returns 202 with a workflow handle and the
// caller polls `project status <ref>`.
func createCmd(rest func() REST) *cobra.Command {
	var (
		orgID   string
		name    string
		tier    string
		region  string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "create <ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Create a new project (async — poll `project status <ref>`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			if orgID == "" {
				return fmt.Errorf("--org is required")
			}
			body := map[string]any{"ref": ref, "orgId": orgID, "name": name}
			if tier != "" {
				body["tier"] = tier
			}
			if region != "" {
				body["region"] = region
			}
			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			if err := rest().Do(cmd.Context(), http.MethodPost, "/api/v1/projects", body, &handle); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(handle)
			}
			fmt.Fprintf(os.Stdout, "✓ provisioning started for %s\n", ref)
			fmt.Fprintf(os.Stdout, "  workflow: %s\n", handle.WorkflowID)
			fmt.Fprintf(os.Stdout, "  poll:     palbase project status %s\n", ref)
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org", "", "Organization ID that owns the project (required)")
	cmd.Flags().StringVar(&name, "name", "", "Human-readable project name")
	cmd.Flags().StringVar(&tier, "tier", "", "Tier: free|pro|scale|enterprise (default free)")
	cmd.Flags().StringVar(&region, "region", "", "Region (default northeurope)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// statusCmd wires `palbase project status <ref>`. Reads the project row
// (GET /api/v1/projects/{ref}); the `status` field reflects provisioning
// progress.
func statusCmd(rest func() REST) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Show a project's provisioning/runtime status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			var row projectRow
			if err := rest().Do(cmd.Context(), http.MethodGet, "/api/v1/projects/"+ref, nil, &row); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(row)
			}
			fmt.Fprintf(os.Stdout, "ref:     %s\n", row.Ref)
			fmt.Fprintf(os.Stdout, "name:    %s\n", row.Name)
			fmt.Fprintf(os.Stdout, "tier:    %s\n", row.Tier)
			fmt.Fprintf(os.Stdout, "region:  %s\n", row.Region)
			fmt.Fprintf(os.Stdout, "status:  %s\n", row.Status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
