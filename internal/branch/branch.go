// Package branch wires `palbase branch ...` subcommands over the
// Management API REST transport. Branches are a paid-plan capability
// (Track A · Feature 1): create starts the orchestrator's
// CreateBranchWorkflow; list shows the project's branches with URLs;
// switch sets the locally-active branch the dev/deploy commands target.
package branch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// REST is the subset of the Management-API transport the branch commands
// use. transport.Client satisfies it; tests substitute a stub.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers lets the cobra wiring read the lazily-built REST client from
// main.go's PersistentPreRunE — mirrors project.Resolvers' pattern.
type Resolvers struct {
	REST func() REST
}

// linkedRef resolves the project ref the branch commands act on: an
// explicit --ref override wins, otherwise the locally-linked project
// (`palbase backend init` / link). Branch is always scoped to a project.
func linkedRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	cfg, err := auth.LoadProjectConfig()
	if err != nil || cfg.Ref == "" {
		return "", fmt.Errorf("not linked to a project — run `palbase backend init` or pass --ref")
	}
	return cfg.Ref, nil
}

// Cmd returns the `palbase branch` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "branch",
		Short: "Create, list, and switch project branches (Pro+)",
	}
	cmd.AddCommand(
		createCmd(r.REST),
		listCmd(r.REST),
		switchCmd(),
	)
	return cmd
}

type branchRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	EndpointRef string `json:"endpoint_ref"`
	Status      string `json:"status"`
	Ephemeral   bool   `json:"ephemeral"`
	URL         string `json:"url"`
}

// createCmd wires `palbase branch create <name>`. Branch provisioning is
// async (Temporal): POST returns 202 with a workflow handle; the branch
// URL serves once the stack is up. Free tier → 403 (branches need Pro+).
func createCmd(rest func() REST) *cobra.Command {
	var (
		ref      string
		kind     string
		noDeploy bool
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Create a branch (async — its URL serves once provisioned)",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			projectRef, err := linkedRef(ref)
			if err != nil {
				return err
			}
			body := map[string]any{
				"branchName": name,
				"kind":       kind,
				"deploy":     !noDeploy,
			}
			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			path := "/api/v1/projects/" + projectRef + "/branches"
			if err := rest().Do(cmd.Context(), http.MethodPost, path, body, &handle); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(handle)
			}
			fmt.Fprintf(os.Stdout, "✓ branch %q provisioning started on %s\n", name, projectRef)
			fmt.Fprintf(os.Stdout, "  workflow: %s\n", handle.WorkflowID)
			fmt.Fprintf(os.Stdout, "  list:     palbase branch list\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Project ref (defaults to the linked project)")
	cmd.Flags().StringVar(&kind, "kind", "staging", "Branch kind: staging|dev|qa|preview")
	cmd.Flags().BoolVar(&noDeploy, "no-deploy", false, "Provision the stack without auto-deploying the backend pod")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// listCmd wires `palbase branch list`.
func listCmd(rest func() REST) *cobra.Command {
	var (
		ref     string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's branches",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectRef, err := linkedRef(ref)
			if err != nil {
				return err
			}
			var rows []branchRow
			path := "/api/v1/projects/" + projectRef + "/branches"
			if err := rest().Do(cmd.Context(), http.MethodGet, path, nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No branches yet — create one with `palbase branch create <name>`.")
				return nil
			}
			active := activeBranchName()
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "\tNAME\tSTATUS\tURL")
			for _, b := range rows {
				marker := " "
				if b.Name == active {
					marker = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", marker, b.Name, b.Status, b.URL)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Project ref (defaults to the linked project)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// switchCmd wires `palbase branch switch <name>`. Local-only: it records the
// active branch in the project config so subsequent `palbase backend
// dev`/`deploy` target that branch's endpoint_ref. No server call.
func switchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "switch <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Set the locally-active branch for dev/deploy (no server call)",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := auth.LoadProjectConfig()
			if err != nil || cfg.Ref == "" {
				return fmt.Errorf("not linked to a project — run `palbase backend init` first")
			}
			cfg.DefaultEnv = name
			if err := auth.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
			}
			fmt.Fprintf(os.Stdout, "✓ switched to branch %q (project %s)\n", name, cfg.Ref)
			fmt.Fprintln(os.Stdout, "  `palbase backend dev`/`deploy` now target this branch.")
			return nil
		},
	}
	return cmd
}

// activeBranchName returns the locally-active branch (ProjectConfig.DefaultEnv),
// or "" if not linked. Used only to mark the active row in `branch list`.
func activeBranchName() string {
	cfg, err := auth.LoadProjectConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.DefaultEnv
}

func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
