// Package env wires `palbase env create|list|use|status|archive|wake|delete`
// over the Management API v2
// (`/api/v2/projects/{projectId}/environments{,/{environmentRef}}`).
//
// An Environment is the ISOLATED RUNTIME boundary: its own endpoint, database,
// API keys and secrets. It REPLACES the Palbase branch, which is gone as a
// resource — there is no `palbase branch`, no `--branch` runtime selector, and
// a Git branch is never a selector.
//
// `env create staging --from production` is the SAFE COPY: the new Environment
// starts from the source's schema and non-secret config. It copies NO production
// rows or secret plaintext and maps no Git branch. Row-data copy is unavailable
// here; secret assignment, Git mapping, and test users use explicit surfaces.
package env

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// REST is the Management-API transport subset the env commands use.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers carries the lazily-built REST client and the shared selection
// resolver (which owns --project / --environment and .palbase/config.json).
type Resolvers struct {
	REST      func() REST
	Selection func() *selection.Resolver
}

// Cmd returns the `palbase env` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Create, list, select, inspect, archive, wake, and delete environments",
		Long: `An environment is an isolated runtime: its own endpoint, database, API keys
and secrets. It replaces the Palbase branch — there is no branch resource and no
--branch selector.

  palbase env list
  palbase env create staging --from production
  palbase env use staging
  palbase env status
  palbase env archive staging
  palbase env wake staging
  palbase env delete staging`,
	}
	cmd.AddCommand(
		createCmd(r),
		listCmd(r),
		useCmd(r),
		statusCmd(r),
		lifecycleCmd(r, "archive", "Archive an environment (tear down compute; `env wake` reactivates it)"),
		lifecycleCmd(r, "wake", "Reactivate an archived environment"),
		deleteCmd(r),
	)
	return cmd
}

// envPath builds `/api/v2/projects/{projectId}/environments[/{ref}[/action]]`.
func envPath(projectID, ref, action string) string {
	p := "/api/v2/projects/" + projectID + "/environments"
	if ref != "" {
		p += "/" + ref
	}
	if action != "" {
		p += "/" + action
	}
	return p
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List the project's environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			projectID, err := r.Selection().ProjectID(ctx)
			if err != nil {
				return err
			}
			envs, err := selection.ListEnvironments(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), envs)
			}
			if len(envs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No environments.")
				return nil
			}
			// A failed resolve just means nothing is marked; listing must still work.
			selected := ""
			if sel, selErr := r.Selection().Resolve(ctx); selErr == nil {
				selected = sel.Environment.ID
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "\tSLUG\tREF\tKIND\tSTATUS\tGIT BRANCH")
			for _, e := range envs {
				marker := " "
				if e.ID == selected {
					marker = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					marker, e.Slug, e.Ref, e.Kind, e.Status, str(e.SourceGitBranch))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// useCmd selects the Environment this directory acts on. It reads the
// server-owned Project mode and Environment list, then writes one atomic local
// selection; it performs no server mutation.
func useCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "use <slug|ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Select the environment this directory acts on (no server mutation)",
		RunE: func(cmd *cobra.Command, args []string) error {
			want := args[0]
			resolver := r.Selection()
			cfg, err := selection.Load("")
			if err != nil {
				return err
			}
			if resolver.ProjectFlag != "" && resolver.ProjectFlag != cfg.ProjectID {
				return fmt.Errorf(
					"env use cannot switch projects from %s to %s — run `palbase project use %s` first",
					cfg.ProjectID, resolver.ProjectFlag, resolver.ProjectFlag,
				)
			}
			resolver.EnvironmentFlag = want
			sel, err := resolver.Resolve(cmd.Context())
			if err != nil {
				return err
			}
			if sel.ProjectID != cfg.ProjectID {
				return fmt.Errorf("resolved environment belongs to project %s, not linked project %s", sel.ProjectID, cfg.ProjectID)
			}
			selection.ApplySelection(cfg, sel)
			if err := selection.Save("", cfg); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), cfg)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ selected environment %s (%s)\n", sel.Environment.Slug, sel.EnvironmentRef())
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// environmentDetail is `GET .../environments/{ref}` — the row plus the fields
// only the single-Environment read carries (its Organization, that
// Organization's tier, and the endpoint URL).
type environmentDetail struct {
	selection.Environment
	OrganizationID string `json:"organization_id"`
	Tier           string `json:"tier"`
	URL            string `json:"url"`
}

func statusCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Args:  cobra.NoArgs,
		Short: "Show the selected environment (endpoint, status, git branch)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sel, err := r.Selection().Resolve(ctx)
			if err != nil {
				return err
			}
			var detail environmentDetail
			if err := r.REST().Do(ctx, http.MethodGet, envPath(sel.ProjectID, sel.EnvironmentRef(), ""), nil, &detail); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), detail)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "project:      %s\n", sel.ProjectID)
			fmt.Fprintf(out, "environment:  %s (%s)\n", detail.Slug, detail.Ref)
			fmt.Fprintf(out, "kind:         %s\n", detail.Kind)
			fmt.Fprintf(out, "status:       %s\n", detail.Status)
			fmt.Fprintf(out, "endpoint:     %s\n", detail.URL)
			fmt.Fprintf(out, "git branch:   %s\n", str(detail.SourceGitBranch))
			fmt.Fprintf(out, "repository:   %s\n", sel.RepositoryProvider)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// createCmd wires `palbase env create <name> --from <source>`.
//
// --from names the SOURCE ENVIRONMENT (spec §3.2). It never names a Git branch.
// The copy is unconditionally safe: schema + non-secret config only. Data copy,
// secret assignment, Git mapping and test-user creation deliberately are not
// flags on this command (locked spec §3.2).
func createCmd(r Resolvers) *cobra.Command {
	var (
		from    string
		async   bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "create <name> --from <environment>",
		Args:  cobra.ExactArgs(1),
		Short: "Create an environment from a source environment (schema + non-secret config)",
		Long: `Create a new environment in the selected project, copied from --from.

SAFE COPY: the new environment gets the source's SCHEMA and its
NON-SECRET config. It gets NO production rows, NO secret plaintext, and no Git
branch mapping.

Data copy is not available through this command. Secret assignment, Git mapping,
and test-user creation use their own explicit surfaces. --from is a source
ENVIRONMENT (slug or ref), never a Git branch.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := cmd.Context()
			if name != "staging" && name != "dev" && name != "preprod" {
				return fmt.Errorf("environment must be staging, dev, or preprod; got %q", name)
			}
			if from == "" {
				return fmt.Errorf("--from is required: name the source environment to copy schema + non-secret config from (e.g. --from production)")
			}
			projectID, err := r.Selection().ProjectID(ctx)
			if err != nil {
				return err
			}
			// The locked API body has exactly these two snake_case fields. The source is
			// a Project-scoped slug or ref; resolving it on the server avoids a TOCTOU
			// lookup and keeps cross-Project identifiers non-enumerating.
			body := map[string]any{
				"source_environment_ref": from,
				"name":                   name,
			}

			var handle struct {
				WorkflowID string `json:"workflowId"`
			}
			if err := r.REST().Do(ctx, http.MethodPost, envPath(projectID, "", ""), body, &handle); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			progress := cmd.ErrOrStderr()

			if async {
				if jsonOut {
					return encodeJSON(out, handle)
				}
				fmt.Fprintf(out, "✓ provisioning started for environment %s\n", name)
				fmt.Fprintf(out, "  workflow: %s\n", handle.WorkflowID)
				return nil
			}

			fmt.Fprintf(progress, "→ provisioning %s from %s (schema + non-secret config) ...\n", name, from)
			created, err := waitForEnvironmentCreate(ctx, r.REST, projectID, name, handle.WorkflowID)
			if err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(out, created)
			}
			fmt.Fprintf(out, "✓ environment %s is ready (%s)\n", created.Slug, created.Ref)
			fmt.Fprintf(out, "  select it: palbase env use %s\n", created.Slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Source ENVIRONMENT to copy schema + non-secret config from (required)")
	cmd.Flags().BoolVar(&async, "async", false, "Return the workflow handle immediately instead of waiting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// envCreateStatus is `GET .../environments/create-status` — the provisioning saga's
// own state. `Failure` is the reason it DIED (empty while it runs). Mirrors
// `project create`'s createStatus: the saga is the ONLY thing that can tell "still
// provisioning" apart from "died in the first second".
type envCreateStatus struct {
	Status          string `json:"status"`
	CurrentActivity string `json:"currentActivity"`
	Failure         string `json:"failure"`
}

// waitForEnvironmentCreate waits on the SAGA, not on a list that a dead create never
// fills. This is the same fix `palbase project create` got: watching the environments
// list cannot distinguish a create that is STILL running from one that DIED in its
// first second (both are "the env isn't there yet"), so a create killed non-retryably
// — "source_environment_ref required", a quota, a bad slug — used to sit here for the
// full 5-minute timeout and then blame the wait. The saga knows it failed; ask it.
//
// `rest` is the FACTORY, not a client: each tick re-resolves the credential, so a wait
// that outlives the current access token refreshes it instead of 401ing.
func waitForEnvironmentCreate(ctx context.Context, rest func() REST, projectID, slug, workflowID string) (selection.Environment, error) {
	deadline := time.Now().Add(envPollTimeout)
	ticker := time.NewTicker(envPollInterval)
	defer ticker.Stop()

	path := envPath(projectID, "", "create-status") + "?workflowId=" + url.QueryEscape(workflowID)

	var last envCreateStatus
	for {
		if err := rest().Do(ctx, http.MethodGet, path, nil, &last); err != nil {
			return selection.Environment{}, err
		}

		switch last.Status {
		case "COMPLETED":
			// The saga finished (its last steps land the env active). The server
			// allocated the ref, so the CLI finds the new env by the SLUG it chose
			// (unique per Project) rather than by a ref it cannot compute. A brief miss
			// is possible if the row is not visible yet — poll again, don't fail a
			// create that worked.
			env, err := environmentBySlug(ctx, rest(), projectID, slug)
			if err == nil {
				return env, nil
			}
			if !errors.Is(err, errEnvNotYetVisible) {
				return selection.Environment{}, err
			}
		case "FAILED", "TERMINATED", "CANCELED", "TIMED_OUT":
			// Dead, and it said why — in SECONDS, not after five minutes.
			reason := last.Failure
			if reason == "" {
				reason = fmt.Sprintf("provisioning %s (workflow %s)", strings.ToLower(last.Status), workflowID)
			}
			return selection.Environment{}, fmt.Errorf("environment create failed: %s", reason)
		}

		if time.Now().After(deadline) {
			// Honest: STILL RUNNING when we stopped looking is a different statement
			// from "it failed", and must not be dressed up as one.
			step := last.CurrentActivity
			if step == "" {
				step = last.Status
			}
			return selection.Environment{}, fmt.Errorf(
				"gave up waiting after %s — environment %q is STILL provisioning (workflow %s, at %s). It is not lost: run `palbase env list` once it finishes",
				envPollTimeout, slug, workflowID, step)
		}
		select {
		case <-ctx.Done():
			return selection.Environment{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// environmentBySlug reads the newly-created Environment by the slug the caller chose.
// A brief absence (the saga closed before its last write is visible to a fresh read) is
// errEnvNotYetVisible, retried on the next tick — never a failure.
func environmentBySlug(ctx context.Context, rest REST, projectID, slug string) (selection.Environment, error) {
	envs, err := selection.ListEnvironments(ctx, rest, projectID)
	if err != nil {
		return selection.Environment{}, err
	}
	for _, e := range envs {
		if e.Slug == slug {
			return e, nil
		}
	}
	return selection.Environment{}, errEnvNotYetVisible
}

// errEnvNotYetVisible: the saga completed but its Environment row is not readable yet.
var errEnvNotYetVisible = errors.New("environment not visible yet")

// Provisioning an Environment spins up a DB + pod + ingress (30-120s typical).
// vars, not consts, so tests can shrink the interval; production never reassigns.
var (
	envPollInterval = 2 * time.Second
	envPollTimeout  = 5 * time.Minute
)

// lifecycleCmd builds `palbase env archive|wake <slug>` — both POST to
// `.../environments/{ref}/<verb>` and print the workflow handle. Archive is
// explicit-reactivate: an archived Environment does NOT auto-wake.
func lifecycleCmd(r Resolvers, verb, short string) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   verb + " [slug|ref]",
		Args:  cobra.MaximumNArgs(1),
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			projectID, ref, err := targetEnv(ctx, r, args)
			if err != nil {
				return err
			}
			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			if err := r.REST().Do(ctx, http.MethodPost, envPath(projectID, ref, verb), nil, &handle); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), handle)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s started for environment %s\n", verb, ref)
			fmt.Fprintf(cmd.OutOrStdout(), "  workflow: %s\n", handle.WorkflowID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// deleteCmd wires `palbase env delete <slug>`. Teardown is async and
// IRREVERSIBLE; the server refuses to delete a production Environment on its own
// (deleting production means deleting the Project).
func deleteCmd(r Resolvers) *cobra.Command {
	var (
		yes     bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "delete <slug|ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Delete an environment and its entire stack (irreversible)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			projectID, ref, err := targetEnv(ctx, r, args)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(),
					"Delete environment %s and its entire stack (database, pod, endpoint, keys, secrets)? This is irreversible. [y/N]: ", ref)
				line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
					fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
					return nil
				}
			}
			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			if err := r.REST().Do(ctx, http.MethodDelete, envPath(projectID, ref, ""), nil, &handle); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), handle)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ teardown started for environment %s\n", ref)
			fmt.Fprintf(cmd.OutOrStdout(), "  workflow: %s\n", handle.WorkflowID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// targetEnv resolves which Environment a lifecycle verb acts on: the positional
// slug/ref when given, otherwise the SELECTED Environment.
func targetEnv(ctx context.Context, r Resolvers, args []string) (projectID, ref string, err error) {
	if len(args) == 0 {
		sel, selErr := r.Selection().Resolve(ctx)
		if selErr != nil {
			return "", "", selErr
		}
		return sel.ProjectID, sel.EnvironmentRef(), nil
	}
	projectID, err = r.Selection().ProjectID(ctx)
	if err != nil {
		return "", "", err
	}
	envs, err := selection.ListEnvironments(ctx, r.REST(), projectID)
	if err != nil {
		return "", "", err
	}
	target := findEnv(envs, args[0])
	if target == nil {
		return "", "", fmt.Errorf("no environment %q in this project — have: %s", args[0], slugsOf(envs))
	}
	return projectID, target.Ref, nil
}

func findEnv(envs []selection.Environment, want string) *selection.Environment {
	for i := range envs {
		if envs[i].Slug == want || envs[i].Ref == want || strings.EqualFold(envs[i].Name, want) {
			return &envs[i]
		}
	}
	return nil
}

func slugsOf(envs []selection.Environment) string {
	out := make([]string, 0, len(envs))
	for _, e := range envs {
		out = append(out, e.Slug)
	}
	return strings.Join(out, ", ")
}

func str(p *string) string {
	if p == nil || *p == "" {
		return "-"
	}
	return *p
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
