package db

// reset.go — `palbase db reset`: throw the schema away and replay the committed
// migrations.
//
// WHY it exists: migrations here are forward-only (no .down.sql) and the Studio SQL
// editor is read-only, so a developer who wanted to start their schema over had
// exactly one option — delete the environment and create a new one. That burns the
// ref permanently, mints new API keys and re-points every client. This is the verb
// that was missing.
//
// The migrations are read from the LOCAL checkout and shipped to the server, mirroring
// `db diff` (which ships db/schema.ts the same way). That is deliberate: "reset" means
// "rebuild this database from the migrations I have committed", so the files on disk
// are the source of truth, not whatever the last deploy happened to apply.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// resetMigration is one file on its way to the reset Job. The name is the identity
// the server records in palbase_schema_migrations, so it travels as the plain
// <stem>.sql filename — the runtime rejects a path rather than sanitising it.
type resetMigration struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// resetResult is what the reset reports back.
type resetResult struct {
	DroppedObjects    int      `json:"droppedObjects"`
	AppliedMigrations int      `json:"appliedMigrations"`
	Migrations        []string `json:"migrations"`
}

// readMigrations loads db/migrations/*.sql in lexicographic order — the SAME order
// the server applies them in (the timestamped stems make that chronological).
//
// A missing directory is NOT an error: a project whose schema was never migrated can
// still be reset (it just replays nothing), and failing here would block the one
// command that fixes a wedged schema.
func readMigrations() ([]resetMigration, error) {
	dir := filepath.Join("db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []resetMigration{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make([]resetMigration, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, name), err)
		}
		out = append(out, resetMigration{Name: name, SQL: string(data)})
	}
	return out, nil
}

// dbReset calls Studio's backend.dbReset for ONE environment.
//
// confirmRef is what the user ACTUALLY TYPED — never a value this CLI filled in
// for them. On production the server requires it to equal the ref, so the real
// gate lives there: a patched or spoofed client that skips the local prompt sends
// no confirmation and the server refuses. Auto-filling it here would have made the
// server's production rule depend on this binary behaving.
func dbReset(ctx context.Context, c *studio.Client, ref string, migrations []resetMigration, confirmRef string) (resetResult, error) {
	input := map[string]any{"ref": ref, "migrations": migrations, "confirmRef": confirmRef}
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
		Short: "Drop the selected environment's schema and replay its migrations",
		Long: `Empty the SELECTED environment's public schema and replay db/migrations/*.sql
from this checkout, in order.

THIS DELETES ALL DATA in the environment's own tables. Auth users, storage objects
and the other module schemas are NOT touched, and the environment keeps its ref,
URL and API keys — this is a schema reset, not a teardown.

Use it when you want to start the schema over: delete the old migrations, write
new ones, and reset so the database matches them exactly.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			ref := sel.EnvironmentRef()

			migrations, err := readMigrations()
			if err != nil {
				return err
			}

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
			if len(migrations) == 0 {
				fmt.Fprintln(out, "Migrations:  none found in db/migrations — the schema will be left EMPTY")
			} else {
				fmt.Fprintf(out, "Migrations:  %d file(s) will be replayed from db/migrations\n", len(migrations))
			}
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

			res, err := dbReset(cmd.Context(), r.Studio(), ref, migrations, typed)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "✓ reset %s\n", ref)
			fmt.Fprintf(out, "  dropped %d object(s), replayed %d migration(s)\n",
				res.DroppedObjects, res.AppliedMigrations)
			if len(migrations) == 0 {
				fmt.Fprintln(out, "  The schema is now EMPTY — generate a migration with `palbase db diff -f <name>`.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	return cmd
}
