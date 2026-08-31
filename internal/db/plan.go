package db

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/spf13/cobra"
)

// schemaPlan is the stack's SchemaPlan: what it would take to make the database
// match db/public.ts, and what that would cost.
type schemaPlan struct {
	InSync      bool                `json:"in_sync"`
	Changes     []string            `json:"changes"`
	Destructive []destructiveChange `json:"destructive"`
	Unsupported []string            `json:"unsupported"`
	// Incompatible is what a push of this change would be refused for, with the
	// way out named. Empty when the push would be accepted.
	Incompatible []string `json:"incompatible"`
}

type destructiveChange struct {
	Kind    string `json:"kind"`
	Table   string `json:"table"`
	Column  string `json:"column,omitempty"`
	Rows    int64  `json:"rows"`
	NonNull int64  `json:"non_null,omitempty"`
}

// exitCodeChanges is what --detailed-exitcode reports when the plan is not
// empty. 2 rather than 1 is the convention CI pipelines already expect from
// planners: 0 = in sync, 2 = would change, anything else = could not plan.
const exitCodeChanges = 2

type changesError struct{}

func (changesError) Error() string { return "" }
func (changesError) ExitCode() int { return exitCodeChanges }

// DeliberateExitStatus marks this as a status somebody CHOSE, told apart from
// `*exec.ExitError`, which carries one by accident and must still print why.
func (changesError) DeliberateExitStatus() {}

func planCmd() *cobra.Command {
	var detailedExitCode bool
	cmd := &cobra.Command{
		Use:   "plan",
		Args:  cobra.NoArgs,
		Short: "Show what it would take to make the local database match db/public.ts",
		Long: `Plan db/public.ts against the local stack's database. Applies nothing.

The plan is computed by the stack against its database as it is right now, so it
covers what a text diff cannot: type changes, policies, constraints.

With --detailed-exitcode this exits 0 when the schema is in sync and 2 when the
plan would change something.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stack, err := openLocal(cmd)
			if err != nil {
				return err
			}
			sources, err := readSchema()
			if err != nil {
				return err
			}

			plan, err := computePlan(cmd.Context(), stack, sources)
			if err != nil {
				return err
			}
			renderPlan(cmd.OutOrStdout(), plan)
			if detailedExitCode && !plan.InSync {
				return changesError{}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&detailedExitCode, "detailed-exitcode",
		false, "exit 0 when in sync and 2 when the plan would change something")
	return cmd
}

func computePlan(ctx context.Context, stack local, sources []backend.SchemaSource) (schemaPlan, error) {
	payload, err := schemaBody(sources)
	if err != nil {
		return schemaPlan{}, err
	}
	status, body, err := stack.post(ctx, "/v1/management/schema/plan", "application/json", payload)
	if err != nil {
		return schemaPlan{}, err
	}
	if status != http.StatusOK {
		return schemaPlan{}, apiError(status, body)
	}
	var plan schemaPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		return schemaPlan{}, fmt.Errorf("read the plan: %w", err)
	}
	return plan, nil
}

// renderPlan writes the plan for a person about to decide something.
//
// Destructive changes carry their row counts, and that is the whole design: "this
// drops a column" is a shrug, "this drops a column with 41,908 values in it" is a
// decision.
func renderPlan(w io.Writer, plan schemaPlan) {
	if plan.InSync && len(plan.Changes) == 0 {
		fmt.Fprintln(w, "✓ the database matches db/public.ts")
		return
	}
	for _, change := range plan.Changes {
		fmt.Fprintf(w, "  %s\n", change)
	}
	for _, drop := range plan.Destructive {
		fmt.Fprintf(w, "  ⚠ %s\n", describeDrop(drop))
	}
	if len(plan.Unsupported) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "not applied by this rail — change these yourself:")
		for _, item := range plan.Unsupported {
			fmt.Fprintf(w, "  %s\n", item)
		}
	}
	if len(plan.Incompatible) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "this change would be refused at push:")
		for _, item := range plan.Incompatible {
			fmt.Fprintf(w, "  %s\n", item)
		}
	}
	if len(plan.Destructive) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "the ⚠ changes take data away — `palbase db apply --approve` runs them too")
	}
}

func describeDrop(d destructiveChange) string {
	if d.Kind == "column" {
		if d.NonNull > 0 {
			return fmt.Sprintf("drop %s.%s — %d value(s) in %d row(s)", d.Table, d.Column, d.NonNull, d.Rows)
		}
		return fmt.Sprintf("drop %s.%s — %d row(s)", d.Table, d.Column, d.Rows)
	}
	return fmt.Sprintf("drop table %s — %d row(s)", d.Table, d.Rows)
}
