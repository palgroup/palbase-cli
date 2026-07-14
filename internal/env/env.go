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
// rows and NO secret plaintext (--with-data and --include-secret opt into each,
// explicitly), and it maps no Git branch unless --git-branch says so.
package env

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// useCmd selects the Environment this directory acts on. Local only — it
// rewrites `.palbase/config.json`'s environment_id and calls no mutation.
func useCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "use <slug|ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Select the environment this directory acts on (no server call)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			want := args[0]
			projectID, err := r.Selection().ProjectID(ctx)
			if err != nil {
				return err
			}
			envs, err := selection.ListEnvironments(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			var target *selection.Environment
			for i := range envs {
				if envs[i].Slug == want || envs[i].Ref == want || strings.EqualFold(envs[i].Name, want) {
					target = &envs[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("no environment %q in this project — have: %s", want, slugsOf(envs))
			}

			cfg, err := selection.Load("")
			if err != nil {
				return err
			}
			cfg.EnvironmentID = target.ID
			if err := selection.Save("", cfg); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), cfg)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ selected environment %s (%s)\n", target.Slug, target.Ref)
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
			if err := r.REST().Do(ctx, http.MethodGet, envPath(sel.ProjectID, sel.Ref(), ""), nil, &detail); err != nil {
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

// createCmd wires `palbase env create <slug> --from <source>`.
//
// --from names the SOURCE ENVIRONMENT (spec §3.2). It never names a Git branch.
// The copy is safe by default: schema + non-secret config only. Production rows
// (--with-data) and individual secret values (--include-secret KEY) are opt-in,
// one explicit flag at a time, because a staging environment that silently
// carries production PII or a live payment key is the failure this contract
// exists to prevent.
func createCmd(r Resolvers) *cobra.Command {
	var (
		from       string
		kind       string
		ref        string
		name       string
		gitBranch  string
		withData   bool
		secretKeys []string
		seedUsers  int
		async      bool
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:   "create <slug> --from <environment>",
		Args:  cobra.ExactArgs(1),
		Short: "Create an environment from a source environment (schema + non-secret config)",
		Long: `Create a new environment in the selected project, copied from --from.

SAFE COPY (the default): the new environment gets the source's SCHEMA and its
NON-SECRET config. It gets NO production rows, NO secret plaintext, and no Git
branch mapping.

  --with-data           also copy the source's table data (opt-in)
  --include-secret KEY  also copy ONE secret's value (repeatable, opt-in)
  --git-branch NAME     map a Git branch to auto-deploy into this environment

--from is a source ENVIRONMENT (slug or ref), never a Git branch.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			ctx := cmd.Context()
			if from == "" {
				return fmt.Errorf("--from is required: name the source environment to copy schema + non-secret config from (e.g. --from production)")
			}
			projectID, err := r.Selection().ProjectID(ctx)
			if err != nil {
				return err
			}
			envs, err := selection.ListEnvironments(ctx, r.REST(), projectID)
			if err != nil {
				return err
			}
			source := findEnv(envs, from)
			if source == nil {
				return fmt.Errorf("no source environment %q in this project — have: %s", from, slugsOf(envs))
			}
			if name == "" {
				name = slug
			}
			if ref == "" {
				ref = deriveRef(source.Ref, source.Slug, slug)
			}

			body := map[string]any{
				"sourceEnvironmentRef": source.Ref,
				"ref":                  ref,
				"name":                 name,
				"slug":                 slug,
				"kind":                 kind,
				"withData":             withData,
				"includeSecretKeys":    orEmpty(secretKeys),
				"testUserSeedCount":    seedUsers,
			}
			if gitBranch != "" {
				body["sourceGitBranch"] = gitBranch
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
				fmt.Fprintf(out, "✓ provisioning started for environment %s (%s)\n", slug, ref)
				fmt.Fprintf(out, "  workflow: %s\n", handle.WorkflowID)
				return nil
			}

			fmt.Fprintf(progress, "→ provisioning %s from %s (schema + non-secret config) ...\n", slug, source.Slug)
			created, err := waitForEnvironment(ctx, r.REST(), projectID, ref)
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
	cmd.Flags().StringVar(&kind, "kind", "staging", "Environment kind: staging | dev | preprod")
	cmd.Flags().StringVar(&ref, "ref", "", "Endpoint ref for the new environment (default: derived from the source's ref seed + slug)")
	cmd.Flags().StringVar(&name, "name", "", "Display name (default: the slug)")
	cmd.Flags().StringVar(&gitBranch, "git-branch", "", "Git branch that auto-deploys into this environment (never a runtime selector)")
	cmd.Flags().BoolVar(&withData, "with-data", false, "Also copy the source's table DATA (default: schema only — no production rows)")
	cmd.Flags().StringSliceVar(&secretKeys, "include-secret", nil, "Also copy this secret's value (repeatable; default: no secret plaintext is copied)")
	cmd.Flags().IntVar(&seedUsers, "seed-users", 0, "Seed N test users into the new environment")
	cmd.Flags().BoolVar(&async, "async", false, "Return the workflow handle immediately instead of waiting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// deriveRef builds the new Environment's ref from the source's. Refs keep the
// `<seed><envSlug>` wire format, so the seed is the source's ref minus its own
// slug suffix; the new ref is that seed plus this slug's first 3 chars (16-char
// ceiling: k8s namespace / pg-meta / storage). --ref overrides when a caller
// wants an exact value.
func deriveRef(sourceRef, sourceSlug, slug string) string {
	seed := sourceRef
	for n := len(sourceSlug); n > 0; n-- {
		if strings.HasSuffix(seed, sourceSlug[:n]) {
			seed = strings.TrimSuffix(seed, sourceSlug[:n])
			break
		}
	}
	suffix := slug
	if len(suffix) > 3 {
		suffix = suffix[:3]
	}
	ref := seed + suffix
	if len(ref) > 16 {
		ref = ref[:16]
	}
	return ref
}

// waitForEnvironment polls the project's Environment list until `ref` is active.
//
// Failure detection: the saga inserts the row, flips it to active on success,
// and REMOVES it on compensation. So a ref that VANISHES after we have seen it
// provisioning failed and rolled back — as opposed to never appearing, which on
// the first tick is just the row not being written yet.
func waitForEnvironment(ctx context.Context, rest REST, projectID, ref string) (selection.Environment, error) {
	deadline := time.Now().Add(envPollTimeout)
	ticker := time.NewTicker(envPollInterval)
	defer ticker.Stop()

	seen := false
	for {
		envs, err := selection.ListEnvironments(ctx, rest, projectID)
		if err != nil {
			return selection.Environment{}, err
		}
		var found *selection.Environment
		for i := range envs {
			if envs[i].Ref == ref {
				found = &envs[i]
				break
			}
		}
		switch {
		case found != nil && found.Status == "active":
			return *found, nil
		case found != nil:
			seen = true
		case seen:
			return selection.Environment{}, fmt.Errorf("environment %q failed to provision (the stack was rolled back) — check Studio for the reason", ref)
		}

		if time.Now().After(deadline) {
			return selection.Environment{}, fmt.Errorf("environment %q is still provisioning after %s — check `palbase env list`", ref, envPollTimeout)
		}
		select {
		case <-ctx.Done():
			return selection.Environment{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

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
		return sel.ProjectID, sel.Ref(), nil
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

// orEmpty keeps a nil slice out of the JSON body: `includeSecretKeys` is a
// strict array on the server and `null` is not one.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
