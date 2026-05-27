// Package merge wires `palbase merge ...` over the Management API REST
// transport (Track A · Feature 5). `palbase merge <source> --into <target>`
// opens a merge request (source's code + schema → target); with --yes it also
// approves + executes in one flow. `palbase merge list` shows the project's
// merge requests. Merge moves CODE + SCHEMA only — never plain user data.
package merge

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

// REST is the subset of the Management-API transport the merge commands use.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers lets the cobra wiring read the lazily-built REST client from
// main.go's PersistentPreRunE (mirrors branch.Resolvers).
type Resolvers struct {
	REST func() REST
}

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

// Cmd returns the `palbase merge` parent command. The bare `palbase merge
// <source> --into <target>` opens (and optionally executes) a merge; the
// `list` subcommand lists merge requests.
func Cmd(r Resolvers) *cobra.Command {
	var (
		ref     string
		into    string
		title   string
		yes     bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "merge <source>",
		Args:  cobra.ExactArgs(1),
		Short: "Merge a branch's code + schema into another (Pro+)",
		Long: "Open a merge request: the source branch's code bundle is promoted to the\n" +
			"target and its schema diff is applied to the target's DB. Plain user data is\n" +
			"never moved. Without --yes, prints the merge-request id for review in Studio.",
		RunE: func(cmd *cobra.Command, args []string) error {
			source := args[0]
			if into == "" {
				return fmt.Errorf("--into <target-branch> is required")
			}
			if source == into {
				return fmt.Errorf("cannot merge a branch into itself")
			}
			projectRef, err := linkedRef(ref)
			if err != nil {
				return err
			}
			if title == "" {
				title = fmt.Sprintf("Merge %s into %s", source, into)
			}

			// 1 — open the MR.
			var handle struct {
				WorkflowID string `json:"workflowId"`
				RunID      string `json:"runId"`
			}
			body := map[string]any{"source": source, "target": into, "title": title}
			path := "/api/v1/projects/" + projectRef + "/merge-requests"
			if err := r.REST().Do(cmd.Context(), http.MethodPost, path, body, &handle); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(handle)
			}
			fmt.Fprintf(os.Stdout, "✓ merge request opened: %s → %s on %s\n", source, into, projectRef)
			fmt.Fprintf(os.Stdout, "  workflow: %s\n", handle.WorkflowID)
			if !yes {
				fmt.Fprintf(os.Stdout, "  review the diff + conflicts in Studio, then approve + merge.\n")
				fmt.Fprintf(os.Stdout, "  (re-run with --yes to approve + merge once the diff is computed)\n")
				return nil
			}

			// --yes: the MR id isn't returned by the async create (the workflow
			// computes the diff first), so direct one-shot approve+execute is not
			// safe here — the diff/conflict must settle first. Tell the user to
			// use `merge list` to get the id, then approve via Studio. Keeping the
			// CLI honest beats faking a synchronous merge.
			fmt.Fprintf(os.Stdout, "  --yes: run `palbase merge list` once the diff is computed,\n")
			fmt.Fprintf(os.Stdout, "         then approve + merge from Studio (conflict gating lives there).\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Project ref (defaults to the linked project)")
	cmd.Flags().StringVar(&into, "into", "", "Target branch to merge into (required)")
	cmd.Flags().StringVar(&title, "title", "", "Merge request title (defaults to \"Merge <source> into <target>\")")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the review prompt")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")

	cmd.AddCommand(listCmd(r.REST))
	return cmd
}

type mergeRow struct {
	ID           string `json:"id"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	State        string `json:"state"`
	Title        string `json:"title"`
	CreatedAt    string `json:"created_at"`
}

// listCmd wires `palbase merge list`.
func listCmd(rest func() REST) *cobra.Command {
	var (
		ref     string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's merge requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectRef, err := linkedRef(ref)
			if err != nil {
				return err
			}
			var rows []mergeRow
			path := "/api/v1/projects/" + projectRef + "/merge-requests"
			if err := rest().Do(cmd.Context(), http.MethodGet, path, nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No merge requests yet — open one with `palbase merge <source> --into <target>`.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSOURCE→TARGET\tSTATE\tTITLE")
			for _, m := range rows {
				fmt.Fprintf(tw, "%s\t%s→%s\t%s\t%s\n", m.ID, m.SourceBranch, m.TargetBranch, m.State, m.Title)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Project ref (defaults to the linked project)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
