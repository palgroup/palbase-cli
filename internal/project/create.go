package project

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/transport"
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
				ProjectID      string `json:"projectId"`
				WorkflowID     string `json:"workflowId"`
				RunID          string `json:"runId"`
				OrganizationID string `json:"organizationId"`
			}
			ctx := cmd.Context()
			if err := r.REST().Do(ctx, http.MethodPost, "/api/v2/projects", body, &handle); err != nil {
				return err
			}
			// The 202 NAMES the Project it is creating: the server mints the id before
			// it starts the saga. Everything after this addresses that one id. An empty
			// one is a server/contract break, not something to paper over by searching
			// for the project — that search is exactly what this command stopped doing.
			if handle.ProjectID == "" {
				return fmt.Errorf("server accepted the create but returned no project id — upgrade the CLI or report this (workflow %s)", handle.WorkflowID)
			}

			out := cmd.OutOrStdout()
			progress := cmd.ErrOrStderr()

			if async {
				if jsonOut {
					return encodeJSON(out, handle)
				}
				fmt.Fprintf(out, "✓ provisioning started (%s, seed %s, repository %s)\n", name, refSeed, provider)
				fmt.Fprintf(out, "  project:  %s\n", handle.ProjectID)
				fmt.Fprintf(out, "  workflow: %s\n", handle.WorkflowID)
				fmt.Fprintf(out, "  next:     palbase project use %s   # once provisioning finishes\n", handle.ProjectID)
				return nil
			}

			// Wait on the SAGA — one call per tick — so a create that dies says why.
			fmt.Fprintf(progress, "→ provisioning %s (project + production environment) ...\n", name)
			created, err := waitForProject(ctx, r.REST, handle.ProjectID, refSeed, handle.WorkflowID)
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
// Environment ref that the runtime answers on.
type createdProject struct {
	ProjectID          string `json:"project_id"`
	EnvironmentID      string `json:"environment_id"`
	EnvironmentRef     string `json:"environment_ref"`
	EnvironmentSlug    string `json:"environment_slug"`
	RepositoryProvider string `json:"repository_provider"`
	WorkflowID         string `json:"workflow_id"`
}

// Creating a Project also provisions its initial production Environment (DB,
// runtime, ingress, and keys). vars, not consts, so tests can shrink the
// interval; production never reassigns them.
var (
	projectPollInterval = 2 * time.Second
	projectPollTimeout  = 10 * time.Minute
)

// createStatus is `GET /api/v2/projects/create-status` — the provisioning saga's
// own state. `Failure` is the reason it DIED (empty while it runs).
type createStatus struct {
	Status          string `json:"status"`
	CurrentActivity string `json:"currentActivity"`
	Failure         string `json:"failure"`
}

// waitForProject waits on the SAGA, not on a list that may never fill: exactly
// ONE request per tick, whatever the account holds.
//
// It polls the saga because the saga is the only thing that knows the create
// FAILED. Watching the project's environments cannot tell "still provisioning"
// apart from "died in the first second" — both are an empty list — so a create
// killed non-retryably by a quota ("project quota exceeded: 77/20") used to sit
// here silently for the full ten-minute timeout and then blame the wait.
//
// `rest` is the FACTORY, not a client: each tick re-resolves the credential, so a
// wait that outlives the current access token refreshes it instead of 401ing.
func waitForProject(ctx context.Context, rest func() REST, projectID, refSeed, workflowID string) (createdProject, error) {
	deadline := time.Now().Add(projectPollTimeout)
	ticker := time.NewTicker(projectPollInterval)
	defer ticker.Stop()

	path := fmt.Sprintf("/api/v2/projects/create-status?ref=%s&workflowId=%s",
		url.QueryEscape(refSeed), url.QueryEscape(workflowID))

	var last createStatus
	for {
		if err := rest().Do(ctx, http.MethodGet, path, nil, &last); err != nil {
			return createdProject{}, err
		}

		switch last.Status {
		case "COMPLETED":
			// The saga is done (its last step is MarkActive), so the Project and
			// its production Environment exist. Read them — and if the row is not
			// visible for a beat, poll again rather than fail a create that worked.
			created, err := productionEnvironment(ctx, rest(), projectID)
			if err == nil {
				return created, nil
			}
			if !errors.Is(err, errEnvNotYetVisible) {
				return createdProject{}, err
			}
		case "FAILED", "TERMINATED", "CANCELED", "TIMED_OUT":
			// It is dead, and it said why. Nothing to wait for.
			reason := last.Failure
			if reason == "" {
				reason = fmt.Sprintf("provisioning %s (workflow %s)", strings.ToLower(last.Status), workflowID)
			}
			return createdProject{}, fmt.Errorf("project create failed: %s", reason)
		}

		if time.Now().After(deadline) {
			// Honest: the saga was STILL RUNNING when we stopped looking. That is
			// a different statement from "it failed", and it must not be dressed
			// up as one.
			step := last.CurrentActivity
			if step == "" {
				step = last.Status
			}
			return createdProject{}, fmt.Errorf(
				"gave up waiting after %s — the project is STILL provisioning (workflow %s, at %s). It is not lost: run `palbase project use %s` once it finishes",
				projectPollTimeout, workflowID, step, projectID)
		}
		select {
		case <-ctx.Done():
			return createdProject{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// productionEnvironment reads the Environment the finished saga created. A 404 is
// still possible for a beat (the workflow closes before its last write is visible
// to a fresh read), so it is retried on the same tick, never treated as failure.
func productionEnvironment(ctx context.Context, rest REST, projectID string) (createdProject, error) {
	envs, err := selection.ListEnvironments(ctx, rest, projectID)
	if err != nil && !isNotFound(err) {
		return createdProject{}, err
	}
	for _, e := range envs {
		if !e.IsProduction {
			continue
		}
		return createdProject{
			ProjectID:       projectID,
			EnvironmentID:   e.ID,
			EnvironmentRef:  e.Ref,
			EnvironmentSlug: e.Slug,
		}, nil
	}
	return createdProject{}, errEnvNotYetVisible
}

// errEnvNotYetVisible: the saga completed but its production Environment is not
// readable yet — poll again rather than fail a create that succeeded.
var errEnvNotYetVisible = errors.New("production environment not visible yet")

// isNotFound reports whether err is the API's 404.
func isNotFound(err error) bool {
	var apiErr *transport.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
