package project

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// createCmd wires `palbase project create <ref-seed>`.
//
// POST /api/v2/projects takes NO organizationId: the CLI convenience flow
// targets the caller's SERVER-SIDE default Organization (Personal for a new
// account). Choosing the payer explicitly is what the Organization-scoped API
// and Studio are for — spec §7.3 keeps Organization out of the CLI entirely.
//
// The positional arg is the ref SEED (4-13 lowercase alnum). The Project itself
// has no ref; the seed names its production Environment's ref (`<seed><slug>`).
// Provisioning is async (Temporal): the 202 carries a workflow handle.
func createCmd(r Resolvers) *cobra.Command {
	var (
		name          string
		region        string
		githubAccount string
		repoName      string
		async         bool
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "create <ref-seed>",
		Args:  cobra.ExactArgs(1),
		Short: "Create a project in your default organization (async)",
		Long: `Create a project. Provisioning is async: the command returns a workflow
handle and creates the project's production environment.

<ref-seed> is 4-13 lowercase alphanumerics. It is not the project's id — it
seeds the ref of the environments underneath it (the endpoint / DNS label /
API-key ref).

The project is created in your DEFAULT organization, which pays for it. There is
no --organization flag: pick the payer in Studio or the Organization-scoped API.

GitHub is OPTIONAL. Two modes:

  palbase mode (default — no GitHub flags):
      Deploy from your working directory with ` + "`palbase push`" + `.

  github mode (--github-account AND --repo):
      A GitHub repo is created and linked; deploys run on ` + "`git push`" + ` via webhook.

Pass both GitHub flags or neither — exactly one is an error.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			refSeed := args[0]
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			// The server rejects a lone GitHub field too (contract superRefine);
			// catching it here gives the actionable message instead of a 400.
			if (githubAccount != "") != (repoName != "") {
				return fmt.Errorf("use both --github-account and --repo for GitHub mode, or neither for palbase mode")
			}

			body := map[string]any{"ref": refSeed, "name": name}
			provider := selection.ProviderPalbase
			if githubAccount != "" {
				body["githubAccount"] = githubAccount
				body["repoName"] = repoName
				provider = selection.ProviderGitHub
			}
			if region != "" {
				body["region"] = region
			}

			var handle struct {
				WorkflowID     string `json:"workflowId"`
				RunID          string `json:"runId"`
				OrganizationID string `json:"organizationId"`
			}
			ctx := cmd.Context()
			if err := r.REST().Do(ctx, http.MethodPost, "/api/v2/projects", body, &handle); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			progress := cmd.ErrOrStderr()

			if async {
				if jsonOut {
					return encodeJSON(out, handle)
				}
				fmt.Fprintf(out, "✓ provisioning started (%s, seed %s, repository %s)\n", name, refSeed, provider)
				fmt.Fprintf(out, "  workflow: %s\n", handle.WorkflowID)
				fmt.Fprintln(out, "  next:     palbase project list   # then: palbase project use <projectId>")
				return nil
			}

			// The 202 carries NO project id: the saga writes the row. So we wait for
			// the production Environment the saga provisions — its ref starts with
			// the seed we chose, which is what makes it identifiable — and that
			// resolves the project id. Without this, `create` would hand the user a
			// workflow id and no way to name what it built.
			fmt.Fprintf(progress, "→ provisioning %s (project + production environment) ...\n", name)
			created, err := waitForProject(ctx, r.REST(), refSeed)
			if err != nil {
				return err
			}

			// The local Git invariant (spec §4): exactly ONE containing repository.
			// An ancestor repo (a monorepo) is reused; a standalone directory gets
			// `git init -b main`. Never a nested .git.
			if _, err := ensureGitRepo(gitRun, ".", progress); err != nil {
				return err
			}

			// Select what we just made: a fresh create leaves the directory ready to
			// use (UAT CLI-001 — config v2 is written by the create/link flow).
			cfg := &selection.Config{
				ProjectID:          created.ProjectID,
				EnvironmentID:      created.EnvironmentID,
				RepositoryProvider: provider,
			}
			if err := selection.Save("", cfg); err != nil {
				return err
			}
			if err := selection.EnsureGitignored(".gitignore"); err != nil {
				return fmt.Errorf("update .gitignore: %w", err)
			}

			if jsonOut {
				return encodeJSON(out, createdProject{
					ProjectID:          created.ProjectID,
					EnvironmentID:      created.EnvironmentID,
					EnvironmentRef:     created.EnvironmentRef,
					EnvironmentSlug:    created.EnvironmentSlug,
					RepositoryProvider: provider,
					WorkflowID:         handle.WorkflowID,
				})
			}
			fmt.Fprintf(out, "✓ created project %s (%s)\n", name, created.ProjectID)
			fmt.Fprintf(out, "  environment: %s (%s)\n", created.EnvironmentSlug, created.EnvironmentRef)
			fmt.Fprintf(out, "  repository:  %s\n", provider)
			fmt.Fprintf(out, "  selected in %s\n", selection.ConfigPath(""))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Human-readable project name (required)")
	cmd.Flags().StringVar(&githubAccount, "github-account", "", `GitHub target: "personal" or an installation id (omit for palbase mode)`)
	cmd.Flags().StringVar(&repoName, "repo", "", "GitHub repo name to create for this project (omit for palbase mode)")
	cmd.Flags().StringVar(&region, "region", "", "Region (default northeurope)")
	cmd.Flags().BoolVar(&async, "async", false, "Return the workflow handle immediately instead of waiting (nothing is selected)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// gitRun is the git seam `project create` uses to enforce the local repository
// invariant. A var so tests can drive it without forking a real git.
var gitRun gitRunner

// createdProject is `palbase project create --json`. It names everything the
// caller needs to act next: the project, its production environment, and the
// endpoint ref that environment answers on.
type createdProject struct {
	ProjectID          string `json:"project_id"`
	EnvironmentID      string `json:"environment_id"`
	EnvironmentRef     string `json:"environment_ref"`
	EnvironmentSlug    string `json:"environment_slug"`
	RepositoryProvider string `json:"repository_provider"`
	WorkflowID         string `json:"workflow_id"`
}

// Provisioning a project spins up a DB + pod + ingress + keys. vars, not consts,
// so tests can shrink the interval; production never reassigns them.
var (
	projectPollInterval = 2 * time.Second
	projectPollTimeout  = 10 * time.Minute
)

// waitForProject polls until the saga's production Environment is active, and
// returns the project it hangs under.
//
// The seed is the handle: `environments.ref` keeps the `<seed><envSlug>` wire
// format, so the Environment this create provisions is the one whose ref starts
// with the seed we passed. (This is exactly how the server authorizes its own
// create-status poll.) The Project row does not exist for the first part of the
// saga, so polling the project list alone would race.
func waitForProject(ctx context.Context, rest REST, refSeed string) (createdProject, error) {
	deadline := time.Now().Add(projectPollTimeout)
	ticker := time.NewTicker(projectPollInterval)
	defer ticker.Stop()

	seen := false
	for {
		projects := []selection.Project{}
		if err := rest.Do(ctx, http.MethodGet, "/api/v2/projects", nil, &projects); err != nil {
			return createdProject{}, err
		}
		for _, p := range projects {
			envs, err := selection.ListEnvironments(ctx, rest, p.ID)
			if err != nil {
				return createdProject{}, err
			}
			for _, e := range envs {
				if !strings.HasPrefix(e.Ref, refSeed) || !e.IsProduction {
					continue
				}
				seen = true
				if e.Status != "active" {
					continue
				}
				return createdProject{
					ProjectID:       p.ID,
					EnvironmentID:   e.ID,
					EnvironmentRef:  e.Ref,
					EnvironmentSlug: e.Slug,
				}, nil
			}
		}

		if time.Now().After(deadline) {
			if seen {
				return createdProject{}, fmt.Errorf("project %q is still provisioning after %s — check `palbase project list`", refSeed, projectPollTimeout)
			}
			return createdProject{}, fmt.Errorf("project %q did not appear after %s — provisioning failed or was rolled back; check Studio", refSeed, projectPollTimeout)
		}
		select {
		case <-ctx.Done():
			return createdProject{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
