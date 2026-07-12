// Package branch wires `palbase branch ...` subcommands over the
// Management API REST transport. Branches are a paid-plan capability
// (Track A · Feature 1): create starts the orchestrator's
// CreateBranchWorkflow; list shows the project's branches with URLs;
// switch sets the locally-active branch the dev/deploy commands target.
package branch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

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
// (.palbase/config.json in the project dir). Branch is always scoped to a project.
func linkedRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	cfg, err := auth.LoadProjectConfig()
	if err != nil || cfg.Ref == "" {
		return "", fmt.Errorf("not linked to a project — run from a project directory or pass --ref")
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
		deleteCmd(r.REST),
		lifecycleCmd(r.REST, "archive", "hibernate", "Archive a branch (tear down compute; reactivate with wake)"),
		lifecycleCmd(r.REST, "wake", "wake", "Wake a sleeping branch or reactivate an archived one (re-provision + restore)"),
	)
	return cmd
}

// lifecycleCmd builds `palbase branch archive|wake <name>` — both POST to
// /api/v1/projects/:ref/branches/:name/<pathAction> and print the workflow
// handle. The command name is the user-facing verb (archive); pathAction is the
// wire contract and stays "hibernate" — the server route + 'hibernated' status
// value are API surface, renamed only in UI/CLI copy. archive refuses "main"
// client-side (the server also refuses).
func lifecycleCmd(rest func() REST, verb, pathAction, short string) *cobra.Command {
	var (
		ref     string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   verb + " <name>",
		Args:  cobra.ExactArgs(1),
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if verb == "archive" && name == "main" {
				return fmt.Errorf("cannot archive the main branch")
			}
			projectRef, err := linkedRef(ref)
			if err != nil {
				return err
			}
			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			path := "/api/v1/projects/" + projectRef + "/branches/" + name + "/" + pathAction
			if err := rest().Do(cmd.Context(), http.MethodPost, path, nil, &handle); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(handle)
			}
			fmt.Fprintf(os.Stdout, "✓ branch %q %s started on %s\n", name, verb, projectRef)
			fmt.Fprintf(os.Stdout, "  workflow: %s\n", handle.WorkflowID)
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Project ref (defaults to the linked project)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
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

// branchPollInterval / branchPollTimeout bound the sync-wait loop: how often
// the CLI polls the branch list and how long it waits before giving up.
// Provisioning a branch spins up a DB + pod + ingress (30–120s typical), so
// the interval is tight enough to feel live and the ceiling generous enough
// not to false-fail. They are vars (not consts) so tests can shrink the
// interval — production never reassigns them.
var (
	branchPollInterval = 2 * time.Second
	branchPollTimeout  = 5 * time.Minute
)

// createCmd wires `palbase branch create <name>`. A branch is a full isolated
// stack (its own DB + pod + URL), so the command (a) confirms before spending
// those resources and (b) waits for provisioning to finish by default,
// printing the live URL when it's ready. `--async` returns the 202 workflow
// handle immediately (CI / scripted use); `--yes` skips the prompt.
// Free tier → 403 server-side (branches need Pro+).
func createCmd(rest func() REST) *cobra.Command {
	var (
		ref      string
		kind     string
		forkFrom string
		noDeploy bool
		async    bool
		yes      bool
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Create a branch (waits for provisioning; --async returns immediately)",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			out := cmd.OutOrStdout()
			projectRef, err := linkedRef(ref)
			if err != nil {
				return err
			}

			// Confirm before provisioning — a branch is a real isolated stack
			// (DB + pod + URL), not a cheap pointer. --yes skips; a
			// non-interactive caller without --yes must opt in explicitly.
			if !yes {
				if !isInteractive() {
					return fmt.Errorf("creating a branch provisions an isolated stack (DB+pod+URL) — pass --yes to confirm in a non-interactive shell")
				}
				fmt.Fprintf(out, "Create branch %q on %s? This provisions a new isolated stack (DB, pod, URL). [y/N]: ", name, projectRef)
				reader := bufio.NewReader(cmd.InOrStdin())
				line, _ := reader.ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
					fmt.Fprintln(out, "Aborted.")
					return nil
				}
			}

			body := map[string]any{
				"branchName": name,
				"kind":       kind,
				"deploy":     !noDeploy,
			}
			if forkFrom != "" {
				body["forkFrom"] = forkFrom
			}
			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			path := "/api/v1/projects/" + projectRef + "/branches"
			if err := rest().Do(cmd.Context(), http.MethodPost, path, body, &handle); err != nil {
				return err
			}

			// --async: don't wait — hand back the workflow handle (the old
			// default; kept for CI loops and scripts).
			if async {
				if jsonOut {
					return encodeJSONTo(out, handle)
				}
				fmt.Fprintf(out, "✓ branch %q provisioning started on %s\n", name, projectRef)
				fmt.Fprintf(out, "  workflow: %s\n", handle.WorkflowID)
				fmt.Fprintf(out, "  list:     palbase branch list\n")
				return nil
			}

			// Default: wait for the stack to come up, then show its URL.
			if !jsonOut {
				fmt.Fprintf(out, "→ provisioning branch %q on %s (DB + pod + URL) ...\n", name, projectRef)
			}
			row, err := waitForBranchReady(cmd.Context(), rest(), projectRef, name)
			if err != nil {
				return err
			}
			if jsonOut {
				return encodeJSONTo(out, row)
			}
			fmt.Fprintf(out, "✓ branch %q is ready on %s\n", name, projectRef)
			if row.URL != "" {
				fmt.Fprintf(out, "  url: %s\n", row.URL)
			} else {
				fmt.Fprintf(out, "  run `palbase branch list` for its URL\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Project ref (defaults to the linked project)")
	cmd.Flags().StringVar(&kind, "kind", "staging", "Branch kind: staging|dev|qa|preview")
	cmd.Flags().StringVar(&forkFrom, "fork-from", "", "Fork from a parent branch (copies its code; schema-only DB)")
	cmd.Flags().BoolVar(&noDeploy, "no-deploy", false, "Provision the stack without auto-deploying the backend pod")
	cmd.Flags().BoolVar(&async, "async", false, "Return the workflow handle immediately instead of waiting for provisioning")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// waitForBranchReady polls the project's branch list until the named branch
// reaches status "active" (provisioning done → URL serves), surfacing it.
//
// Failure detection: the orchestrator inserts the row as "creating", flips it
// to "active" on success, and (on compensation) to "deleted" — and listBranches
// filters "deleted" out. So a branch that DISAPPEARS after we've seen it
// "creating" provisioned-and-failed. We track seenCreating to avoid mistaking
// the first-poll race (row not inserted yet) for that failure.
func waitForBranchReady(ctx context.Context, rest REST, projectRef, name string) (branchRow, error) {
	deadline := time.Now().Add(branchPollTimeout)
	ticker := time.NewTicker(branchPollInterval)
	defer ticker.Stop()

	path := "/api/v1/projects/" + projectRef + "/branches"
	seenCreating := false
	for {
		var rows []branchRow
		if err := rest.Do(ctx, http.MethodGet, path, nil, &rows); err != nil {
			return branchRow{}, err
		}
		var found *branchRow
		for i := range rows {
			if rows[i].Name == name {
				found = &rows[i]
				break
			}
		}
		switch {
		case found != nil && found.Status == "active":
			return *found, nil
		case found != nil:
			// still "creating" (or another transient state) — keep waiting.
			seenCreating = true
		case seenCreating:
			// row vanished after we saw it provisioning → compensation ran.
			return branchRow{}, fmt.Errorf("branch %q provisioning failed (the stack was rolled back) — check the Studio dashboard", name)
		}

		if time.Now().After(deadline) {
			return branchRow{}, fmt.Errorf("branch %q still provisioning after %s — check `palbase branch list` for its status", name, branchPollTimeout)
		}
		select {
		case <-ctx.Done():
			return branchRow{}, ctx.Err()
		case <-ticker.C:
		}
	}
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
// active branch in the project config so subsequent `palbase serve`/`palbase
// push` target that branch's endpoint_ref. No server call.
func switchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "switch <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Set the locally-active branch for serve (no server call)",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := auth.LoadProjectConfig()
			if err != nil || cfg.Ref == "" {
				return fmt.Errorf("not linked to a project — run from a project directory or pass --ref")
			}
			cfg.DefaultEnv = name
			if err := auth.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
			}
			fmt.Fprintf(os.Stdout, "✓ switched to branch %q (project %s)\n", name, cfg.Ref)
			fmt.Fprintln(os.Stdout, "  `palbase serve` now targets this branch.")
			return nil
		},
	}
	return cmd
}

// deleteCmd wires `palbase branch delete <name>`. Teardown is async (Temporal)
// and IRREVERSIBLE — without --yes it prompts for confirmation. Refuses "main"
// client-side for fast feedback (the server also refuses the default branch).
func deleteCmd(rest func() REST) *cobra.Command {
	var (
		ref     string
		yes     bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Args:  cobra.ExactArgs(1),
		Short: "Delete a branch and its entire stack (irreversible)",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "main" {
				return fmt.Errorf("cannot delete the main branch")
			}
			projectRef, err := linkedRef(ref)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(os.Stdout, "Delete branch %q on %s and its entire stack (DB, pod, URL)? This is irreversible. [y/N]: ", name, projectRef)
				reader := bufio.NewReader(cmd.InOrStdin())
				line, _ := reader.ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
					fmt.Fprintln(os.Stdout, "Aborted.")
					return nil
				}
			}
			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			path := "/api/v1/projects/" + projectRef + "/branches/" + name
			if err := rest().Do(cmd.Context(), http.MethodDelete, path, nil, &handle); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(handle)
			}
			fmt.Fprintf(os.Stdout, "✓ branch %q teardown started on %s\n", name, projectRef)
			fmt.Fprintf(os.Stdout, "  workflow: %s\n", handle.WorkflowID)
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Project ref (defaults to the linked project)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// isInteractive reports whether stdin is a TTY. The create confirmation
// prompt fires only when interactive; a piped/CI shell must pass --yes.
// (Duplicated from the backend package's unexported helper — a 4-line stdlib
// idiom isn't worth exporting across a package boundary.) It's a var so tests
// can force interactivity to exercise the prompt's decline path.
var isInteractive = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
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
	return encodeJSONTo(os.Stdout, v)
}

// encodeJSONTo writes indented JSON to an explicit writer, so commands that
// thread cmd.OutOrStdout() (create) emit to a test-capturable sink.
func encodeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
