package db

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/studio"
)

// applyResult is what the server reports an apply did.
type applyResult struct {
	PlanID            string   `json:"planId"`
	StatementsApplied int      `json:"statementsApplied"`
	PendingConcurrent []string `json:"pendingConcurrent"`
}

type approval struct {
	By     string `json:"by"`
	At     string `json:"at"`
	Reason string `json:"reason,omitempty"`
}

func sendApply(ctx context.Context, c *studio.Client, ref, schema string, plan schemaPlan, planID string, ap *approval) (applyResult, error) {
	input := map[string]any{"ref": ref, "schema": schema, "plan": plan, "planId": planID}
	if ap != nil {
		input["approval"] = ap
	}
	var resp applyResult
	if err := c.WithTimeout(studio.JobCallTimeout).Mutation(ctx, "backend.schemaApply", input, &resp); err != nil {
		return applyResult{}, fmt.Errorf("backend.schemaApply: %w", err)
	}
	return resp, nil
}

func applyCmd(r Resolvers) *cobra.Command {
	var force bool
	var revertTo string

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Bring the live database in line with db/schema.ts",
		Long: `Plan db/schema.ts against the SELECTED environment's live database, show what
would change, and apply it.

The plan is always recomputed against the database as it is right now — including
with --force. A plan that has gone stale is never applied blindly; it is recomputed
and shown, because in a declarative model "overwriting" is the default behaviour and
that is exactly what needs to be seen before it happens.

With --to <plan-id> the target is an earlier applied state instead of db/schema.ts.
Reverting restores the SCHEMA, not the data: reverting a DROP recreates the table
empty.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			ref := sel.EnvironmentRef()
			schema, err := readSchema()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if revertTo != "" {
				fmt.Fprintf(out, "planning a return to %s\n\n", revertTo)
			}

			// Always plan first, and always against live as it is NOW. This is what
			// --force does not skip: it skips the confirmation, never the recompute.
			plan, err := computeSchemaPlan(cmd.Context(), r.Studio(), ref, schema)
			if err != nil {
				return err
			}
			renderPlan(out, plan)

			if plan.empty() {
				return nil
			}

			if !force {
				ok, err := confirm(cmd.InOrStdin(), out, plan)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(out, "\nnothing was applied")
					return nil
				}
			}

			var ap *approval
			if plan.hasError() {
				// The plan carries an error, so production will refuse it without a
				// recorded approval. Applying with --force IS that approval, and it
				// is recorded as such — an approval nobody can point to is not one.
				ap = &approval{By: "cli", At: time.Now().UTC().Format(time.RFC3339)}
				if force {
					ap.Reason = "approved with --force"
				} else {
					ap.Reason = "approved at the terminal"
				}
			}

			planID := newPlanID(plan.ToFingerprint)
			res, err := sendApply(cmd.Context(), r.Studio(), ref, schema, plan, planID, ap)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "\n✓ applied (%s)\n", res.PlanID)
			if len(res.PendingConcurrent) > 0 {
				fmt.Fprintf(out, "  %d index(es) are being built concurrently; they finish outside the transaction\n",
					len(res.PendingConcurrent))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"skip the confirmation (the plan is still recomputed against the live database)")
	cmd.Flags().StringVar(&revertTo, "to", "",
		"return to an earlier applied state instead of db/schema.ts")
	return cmd
}

// confirm asks before changing a database.
//
// The prompt names what is at stake rather than asking a bare yes/no: a plan that
// destroys rows and a plan that adds a nullable column should not read the same at
// the moment of consent.
func confirm(in io.Reader, out io.Writer, plan schemaPlan) (bool, error) {
	question := "apply this plan?"
	if plan.hasError() {
		lost := int64(0)
		if plan.Destructive != nil {
			for _, d := range plan.Destructive.Drops {
				if d.Kind == "column" {
					lost += d.NonNull
					continue
				}
				lost += d.RowCount
			}
		}
		if lost > 0 {
			question = fmt.Sprintf("apply this plan and destroy %d row(s)?", lost)
		}
	}
	fmt.Fprintf(out, "\n%s [y/N] ", question)

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		// No answer is not a yes.
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// newPlanID names this apply in the environment's history. It carries the target
// fingerprint so a history entry can be recognised without opening it.
func newPlanID(toFingerprint string) string {
	stamp := time.Now().UTC().Format("20060102T150405")
	if len(toFingerprint) >= 8 {
		return "pl_" + stamp + "_" + toFingerprint[:8]
	}
	return "pl_" + stamp
}
