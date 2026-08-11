package db

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/studio"
)

// schemaPlan is the planner's result. The plan is computed server-side against the
// environment's LIVE database: the declared schema is built for real in a throwaway
// shadow database and the difference is taken by Postgres itself, so it covers the
// things a hand-rolled differ cannot model — type changes, triggers, views, policies.
type schemaPlan struct {
	SQL             string           `json:"sql"`
	FromFingerprint string           `json:"fromFingerprint"`
	ToFingerprint   string           `json:"toFingerprint"`
	Findings        []planFinding    `json:"findings"`
	Destructive     *planDestructive `json:"destructive"`
}

type planFinding struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type planDestructive struct {
	Drops       []planDrop `json:"drops"`
	HasDataLoss bool       `json:"hasDataLoss"`
}

type planDrop struct {
	Kind     string `json:"kind"`
	Table    string `json:"table"`
	Column   string `json:"column,omitempty"`
	RowCount int64  `json:"rowCount"`
	NonNull  int64  `json:"nonNull,omitempty"`
}

func (p schemaPlan) empty() bool { return strings.TrimSpace(p.SQL) == "" }

func (p schemaPlan) hasError() bool {
	for _, f := range p.Findings {
		if f.Level == "error" {
			return true
		}
	}
	return false
}

// computeSchemaPlan asks Studio to plan the schema against the selected environment.
func computeSchemaPlan(ctx context.Context, c *studio.Client, ref, schema string) (schemaPlan, error) {
	input := map[string]any{"ref": ref, "schema": schema}
	var resp schemaPlan
	// The planner builds the declared schema in a real database before it can diff
	// anything, so it runs longer than a plain differ; the client's default timeout
	// would report a failure while the Job was still working.
	if err := c.WithTimeout(studio.JobCallTimeout).Mutation(ctx, "backend.schemaPlan", input, &resp); err != nil {
		return schemaPlan{}, fmt.Errorf("backend.schemaPlan: %w", err)
	}
	return resp, nil
}

// exitCodeChanges is what `--detailed-exitcode` reports when a plan would change
// something. It mirrors the convention CI pipelines already expect from planners:
// 0 = no changes, 2 = changes, non-zero-non-2 = the plan could not be produced.
const exitCodeChanges = 2

// changesError carries the detailed exit code out of RunE without printing anything.
type changesError struct{}

func (changesError) Error() string { return "" }

// ExitCode lets the root command translate this into a process exit status.
func (changesError) ExitCode() int { return exitCodeChanges }

func planCmd(r Resolvers) *cobra.Command {
	var detailedExitCode bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what it would take to make the live database match db/schema.ts",
		Long: `Plan db/schema.ts against the SELECTED environment's live database.

The plan is computed on the server: your declared schema is built for real in a
throwaway database and Postgres itself reports the difference, so the plan covers
everything the schema DSL can express — including type changes and policies.

Nothing is applied and nothing is written to your repository.

With --detailed-exitcode the command exits 0 when the schema is in sync and 2 when
the plan would change something, so CI can branch on it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			schema, err := readSchema()
			if err != nil {
				return err
			}

			plan, err := computeSchemaPlan(cmd.Context(), r.Studio(), sel.EnvironmentRef(), schema)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			renderPlan(out, plan)

			if detailedExitCode && !plan.empty() {
				return changesError{}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&detailedExitCode, "detailed-exitcode", false,
		"exit 0 when the schema is in sync and 2 when the plan would change something")
	return cmd
}

// renderPlan prints the plan for a person deciding whether to apply it.
//
// The findings come first and the errors are named as such, because the numbers are
// the decision: "drops notes" and "drops notes, 431 rows" are not the same choice,
// and a plan whose destructive lines scrolled past the SQL would be approved by
// someone who never read them.
func renderPlan(out io.Writer, plan schemaPlan) {
	if plan.empty() {
		fmt.Fprintln(out, "✓ schema in sync — nothing to apply")
		return
	}

	if plan.Destructive != nil && len(plan.Destructive.Drops) > 0 {
		drops := append([]planDrop(nil), plan.Destructive.Drops...)
		sort.Slice(drops, func(i, j int) bool {
			if drops[i].Table != drops[j].Table {
				return drops[i].Table < drops[j].Table
			}
			return drops[i].Column < drops[j].Column
		})
		for _, d := range drops {
			target := d.Table
			if d.Column != "" {
				target = d.Table + "." + d.Column
			}
			// A column drop destroys the values that are THERE, always — not one per
			// row, and not conditionally. Falling back to the table's row count when
			// NonNull is zero is how "nothing is lost" got displayed as "168 rows":
			// the overstated warning people learn to skip, printed over the server's
			// correct finding. Measured live on todoapp, 2026-08-11.
			lost := d.RowCount
			if d.Kind == "column" {
				lost = d.NonNull
			}
			if lost > 0 {
				fmt.Fprintf(out, "  - %-40s DROP  ⚠ %d row(s)\n", target, lost)
			} else {
				fmt.Fprintf(out, "  - %-40s DROP\n", target)
			}
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, strings.TrimRight(plan.SQL, "\n"))
	fmt.Fprintln(out)

	if len(plan.Findings) > 0 {
		for _, f := range plan.Findings {
			marker := "!"
			if f.Level == "error" {
				marker = "✗"
			}
			fmt.Fprintf(out, "  %s %s\n", marker, f.Message)
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintf(out, "  from %s  to %s\n", short(plan.FromFingerprint), short(plan.ToFingerprint))
	if plan.hasError() {
		fmt.Fprintln(out, "  this plan needs approval before it can run in production")
	}
}

// short renders a fingerprint at a length a person can compare at a glance.
func short(fingerprint string) string {
	if len(fingerprint) <= 12 {
		return fingerprint
	}
	return fingerprint[:12] + "…"
}
