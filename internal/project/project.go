// Package project wires `palbase project create|list|use|status|delete` over
// the Management API v2 (`/api/v2/projects*`, Authorization: DPoP <pat> +
// per-request proof).
//
// A Project is the SaaS control-plane aggregate and CLI link anchor — members,
// apps, ONE repository, and its Environment set. It carries no runtime ref,
// tier, or region: entitlement lives on its Organization and runtime identity
// lives only on its Environments.
//
// Organization is NOT a CLI context (spec §7.3): create targets the caller's
// server-side default Organization, and there is no `--organization`.
package project

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// REST is the Management-API transport subset the project commands use.
// *transport.Client satisfies it; tests substitute a stub.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers lets the cobra wiring read the lazily-built REST client and the
// shared selection resolver from main.go's PersistentPreRunE.
type Resolvers struct {
	REST      func() REST
	Selection func() *selection.Resolver
}

// Cmd returns the `palbase project` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Create, list, select, inspect, and delete Palbase projects",
	}
	cmd.AddCommand(
		createCmd(r),
		listCmd(r),
		useCmd(r),
		statusCmd(r),
		connectRepoCmd(r),
		disconnectRepoCmd(r),
		deleteCmd(r),
	)
	return cmd
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List the projects you can see",
		RunE: func(cmd *cobra.Command, args []string) error {
			var rows []selection.Project
			if err := r.REST().Do(cmd.Context(), http.MethodGet, "/api/v2/projects", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No projects yet — create one with `palbase project create`.")
				return nil
			}
			selected, _ := r.Selection().ProjectID(cmd.Context())
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "\tID\tNAME\tCREATED")
			for _, p := range rows {
				marker := " "
				if p.ID == selected {
					marker = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", marker, p.ID, p.Name, day(p.CreatedAt))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// useCmd selects a Project for this directory and writes config v2. It also
// selects the Project's production Environment, so a fresh `project use` is
// immediately usable — a Project always has exactly one production Environment.
func useCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "use <projectId>",
		Args:  cobra.ExactArgs(1),
		Short: "Select the project this directory acts on (writes .palbase/config.json)",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]
			ctx := cmd.Context()

			detail, err := selection.GetProject(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			envs, err := selection.ListEnvironments(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			target, ok := selection.DefaultEnvironment(envs)
			if !ok {
				return fmt.Errorf("project %s has no visible environment yet — it may still be provisioning (`palbase project status %s`)", projectID, projectID)
			}
			provider, err := detail.RepositoryProvider()
			if err != nil {
				return err
			}

			cfg, _ := selection.Load("")
			if cfg == nil {
				cfg = &selection.Config{}
			}
			selection.ApplySelection(cfg, selection.Selection{
				ProjectID: projectID, Environment: target, RepositoryProvider: provider,
			})
			if err := selection.Save("", cfg); err != nil {
				return err
			}
			if err := selection.EnsureGitignored(".gitignore"); err != nil {
				return fmt.Errorf("update .gitignore: %w", err)
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), cfg)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ selected project %s (%s)\n", detail.Name, projectID)
			fmt.Fprintf(cmd.OutOrStdout(), "  environment: %s (%s)\n", target.Slug, target.Ref)
			fmt.Fprintf(cmd.OutOrStdout(), "  repository:  %s\n", provider)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// statusCmd reports the SELECTED project (or --project): the product, its
// repository, and every Environment under it. It is the "where am I" command.
func statusCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Args:  cobra.NoArgs,
		Short: "Show the selected project, its repository, and its environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			projectID, err := r.Selection().ProjectID(ctx)
			if err != nil {
				return err
			}
			detail, err := selection.GetProject(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			envs, err := selection.ListEnvironments(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			provider, err := detail.RepositoryProvider()
			if err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), struct {
					Project      selection.ProjectDetail `json:"project"`
					Environments []selection.Environment `json:"environments"`
				}{detail, envs})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "project:     %s (%s)\n", detail.Name, detail.ID)
			fmt.Fprintf(out, "role:        %s\n", detail.Role)
			fmt.Fprintf(out, "repository:  %s", provider)
			if detail.GithubRepo != "" {
				fmt.Fprintf(out, " (%s)", detail.GithubRepo)
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, "environments:")
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "  SLUG\tREF\tKIND\tSTATUS")
			for _, e := range envs {
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", e.Slug, e.Ref, e.Kind, e.Status)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// day trims an RFC3339 timestamp to its date. A value we cannot parse is shown
// verbatim rather than as a bogus zero-date.
func day(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
