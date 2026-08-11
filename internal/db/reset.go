package db

// reset.go — `palbase db reset`: throw the schema away and rebuild it from
// db/schema.ts.
//
// WHY it exists: the Studio SQL editor is read-only, so a developer who wanted to
// start their schema over had exactly one option — delete the environment and
// create a new one. That burns the ref permanently, mints new API keys and
// re-points every client. This is the verb that was missing.
//
// The server only empties. What puts the schema back is the declaration, planned
// against the database that was just cleared — which is what makes that plan
// purely additive, and never a destructive one to approve.

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// resetResult is what the reset reports back.
type resetResult struct {
	DroppedObjects    int `json:"droppedObjects"`
	AppliedMigrations int `json:"appliedMigrations"`
	// Migrations is what the server replayed, and it is always empty: there are no
	// migration files. Kept because the response shape is the server's, not this
	// client's, to redefine.
	Migrations []string `json:"migrations"`
}

// dbReset calls Studio's backend.dbReset for ONE environment.
//
// confirmRef is what the user ACTUALLY TYPED — never a value this CLI filled in
// for them. On production the server requires it to equal the ref, so the real
// gate lives there: a patched or spoofed client that skips the local prompt sends
// no confirmation and the server refuses. Auto-filling it here would have made the
// server's production rule depend on this binary behaving.
func dbReset(ctx context.Context, c *studio.Client, ref string, confirmRef string) (resetResult, error) {
	input := map[string]any{"ref": ref, "confirmRef": confirmRef}
	var resp resetResult
	// Studio blocks on the reset Job for up to 330s; the client's 120s default
	// would abandon a reset that is still running and tell the user it failed.
	if err := c.WithTimeout(studio.JobCallTimeout).Mutation(ctx, "backend.dbReset", input, &resp); err != nil {
		return resetResult{}, fmt.Errorf("backend.dbReset: %w", err)
	}
	return resp, nil
}

func resetCmd(r Resolvers) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Drop the selected environment's schema and rebuild it from db/schema.ts",
		Long: `Empty the SELECTED environment's public schema, then rebuild it from
db/schema.ts.

THIS DELETES ALL DATA in the environment's own tables. Auth users, storage objects
and the other module schemas are NOT touched, and the environment keeps its ref,
URL and API keys — this is a schema reset, not a teardown.

Use it when you want to start the schema over: edit db/schema.ts to whatever you
actually want, then reset so the database matches it exactly.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			ref := sel.EnvironmentRef()

			out := cmd.OutOrStdout()
			isProd := sel.Environment.IsProduction

			// Spell out BOTH halves — what dies and what survives. "Deletes all
			// data" alone reads as "deletes the environment", which is the wrong
			// mental model (the ref, URL and keys are untouched) and pushes people
			// toward the far more destructive env delete.
			fmt.Fprintln(out, "⚠  DESTRUCTIVE — this deletes ALL DATA in this environment's own tables.")
			fmt.Fprintln(out)
			if isProd {
				fmt.Fprintf(out, "Environment: %s  ** PRODUCTION **\n", ref)
			} else {
				fmt.Fprintf(out, "Environment: %s\n", ref)
			}
			fmt.Fprintln(out, "Rebuild:     from db/schema.ts, by planning against the emptied database")
			fmt.Fprintln(out)
			fmt.Fprintln(out, "  Dropped:  every table, view, sequence, routine and enum in the public")
			fmt.Fprintln(out, "            schema — and every row in them. This cannot be undone.")
			fmt.Fprintln(out, "  Kept:     the environment itself (same ref, URL and API keys), your auth")
			fmt.Fprintln(out, "            users, storage objects, and the other module schemas.")
			fmt.Fprintln(out)

			// Production refuses --yes. This is a nicer error, NOT the gate: the
			// server independently requires confirm_ref to equal the ref on
			// production, and this CLI only ever forwards what the user typed — so a
			// patched client that skips this branch still gets refused server-side.
			// (is_production itself comes from the server on every Resolve, not from
			// the local config, which only records WHICH environment is selected.)
			if isProd && yes {
				return fmt.Errorf(
					"refusing --yes on production (%s): resetting production requires typing the ref.\n"+
						"For automation use the Management API: POST /api/v2/projects/{projectId}/environments/%s/database/reset "+
						"with a token holding the database:reset scope", ref, ref)
			}

			// typed is forwarded to the server as confirm_ref. It stays EMPTY under
			// --yes: off production the server does not ask for it, and on production
			// an empty confirmation is exactly what must be refused — by the server,
			// not by this branch.
			var typed string
			if !yes {
				// Typing the ref back — not a bare y/N — because the blast radius is
				// every row in the environment and the wrong terminal is an easy
				// mistake to make. This is the same posture `env delete` takes.
				fmt.Fprintf(out, "Type the environment ref (%s) to confirm: ", ref)
				line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				typed = strings.TrimSpace(line)
				if typed != ref {
					fmt.Fprintln(out, "Aborted.")
					return nil
				}
			}

			// The server empties the schema and replays nothing: there are no
			// migration files to replay. What rebuilds it is the declaration —
			// planned against the now-empty database, which makes the plan purely
			// additive and therefore never a destructive one to approve.
			res, err := dbReset(cmd.Context(), r.Studio(), ref, typed)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "✓ reset %s — dropped %d object(s)\n", ref, res.DroppedObjects)

			schema, err := readSchema()
			if err != nil {
				// A project with no declaration has nothing to rebuild, and saying so
				// is better than leaving the operator to guess why the schema is empty.
				fmt.Fprintln(out, "  no db/schema.ts — the schema is now EMPTY")
				return nil
			}

			fmt.Fprintln(out, "  rebuilding from db/schema.ts…")
			plan, err := computeSchemaPlan(cmd.Context(), r.Studio(), ref, schema)
			if err != nil {
				return err
			}
			if plan.empty() {
				fmt.Fprintln(out, "  db/schema.ts declares nothing — the schema is now EMPTY")
				return nil
			}
			applied, err := sendApply(cmd.Context(), r.Studio(), ref, schema, plan, newPlanID(plan.ToFingerprint), nil)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "✓ rebuilt from db/schema.ts (%s)\n", applied.PlanID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	return cmd
}
