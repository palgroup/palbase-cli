// Package db provides the `palbase db` subcommand group: diff / check.
//
// These commands implement the config-as-code migration workflow. The
// authoritative schema lives in db/schema.ts; the live database may have
// drifted from it (someone added a column directly, or a teammate pushed a
// schema change). `db diff` asks Studio (which proxies the orchestrator's
// POST /internal/migration-sql/{ref}) for the migration SQL that
// reconciles the live DB to the declared schema, and writes it to
// db/migrations/<ts>_<name>.sql. `db check` is the read-only gate the
// pre-push hook keys on: it exits non-zero when the schema has drifted but no
// migration has been generated yet.
//
// The diff is computed SERVER-side: only the cluster can reach the tenant DB
// (the orchestrator runs the tenant's own backend-runtime image as a throwaway
// `--migration-sql` Job), so the CLI never touches the database. It ships the
// db/schema.ts SOURCE STRING and gets back {sql, plan}. The plan's five
// string arrays (added/dropped tables+columns, type changes) drive the
// summary line, the destructive warning, and the check gate's drift report.
package db

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// Resolvers carries the lazily-built Studio client, populated by
// PersistentPreRunE on the root command before any subcommand fires
// (mirrors secret.Resolvers).
type Resolvers struct {
	Studio    func() *studio.Client
	Selection func() *selection.Resolver
}

// diffPlan mirrors the br-pod's config-as-code planner output, decoded from
// Studio's backend.migrationSQL. Every entry is a dotted identifier
// ("todos.done", "stale"); all-empty means the declared schema is in sync
// with the live DB. Field tags match the tRPC JSON exactly.
type diffPlan struct {
	AddTables   []string `json:"addTables"`
	AddColumns  []string `json:"addColumns"`
	DropColumns []string `json:"dropColumns"`
	DropTables  []string `json:"dropTables"`
	TypeChanges []string `json:"typeChanges"`
	// Constraint and index operations — non-data-losing but still require a
	// migration file. Field tags match the server's DiffPlan JSON exactly.
	AddConstraints  []string `json:"addConstraints"`
	DropConstraints []string `json:"dropConstraints"`
	AddIndexes      []string `json:"addIndexes"`
	DropIndexes     []string `json:"dropIndexes"`
	// RLS additions — an rls-only diff (table already exists, the user just added
	// rls:true or a new policy) is a real change. Without these fields the JSON
	// unmarshal silently DROPPED them, so empty() returned true and `db diff`
	// printed "schema in sync" even though the backend generated CREATE POLICY
	// SQL. Tags must match the server DiffPlan JSON exactly.
	EnableRLS      []string `json:"enableRLS"`
	AddPolicies    []string `json:"addPolicies"`
	ChangePolicies []string `json:"changePolicies"`
	// AddForeignKeys — an FK-only diff (table already exists, the user just added
	// a .references()/.referencesAuthUser()) is a real change. Without this field
	// the JSON unmarshal silently DROPPED the server's addForeignKeys, so empty()
	// returned true and `db diff` printed "schema in sync" even though the backend
	// generated ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY SQL (the same class
	// as the RLS-dropped-in-CLI regression). Tag must match the server DiffPlan.
	AddForeignKeys []string `json:"addForeignKeys"`
}

// empty reports whether the live DB already matches the declared schema.
func (p diffPlan) empty() bool {
	return len(p.AddTables) == 0 &&
		len(p.AddColumns) == 0 &&
		len(p.DropColumns) == 0 &&
		len(p.DropTables) == 0 &&
		len(p.TypeChanges) == 0 &&
		len(p.AddConstraints) == 0 &&
		len(p.DropConstraints) == 0 &&
		len(p.AddIndexes) == 0 &&
		len(p.DropIndexes) == 0 &&
		len(p.EnableRLS) == 0 &&
		len(p.AddPolicies) == 0 &&
		len(p.ChangePolicies) == 0 &&
		len(p.AddForeignKeys) == 0
}

// hasDestructive reports whether applying the migration would drop data
// (dropped columns or tables).
func (p diffPlan) hasDestructive() bool {
	return len(p.DropColumns) > 0 || len(p.DropTables) > 0
}

// migrationSQLResult is the full backend.migrationSQL response: the migration
// DDL plus the structured plan. Matches Studio's MigrationSQLResult return.
type migrationSQLResult struct {
	Sql  string   `json:"sql"`
	Plan diffPlan `json:"plan"`
}

// Cmd returns the `palbase db` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Manage the selected environment's database schema migrations",
		Long: `Commands to keep db/schema.ts and the live database in sync.

  palbase db diff -f <name>   Generate a migration from db/schema.ts vs the live DB.
  palbase db check            Fail (non-zero) if the schema has drifted but no
                              migration was generated yet (pre-push gate).
  palbase db reset            Drop the schema and replay db/migrations (DESTRUCTIVE).

The diff is computed server-side: db/schema.ts is sent to Palbase, which diffs
it against the SELECTED environment's database and returns the migration SQL.`,
	}
	cmd.AddCommand(
		diffCmd(r),
		checkCmd(r),
		resetCmd(r),
	)
	return cmd
}

// schemaPath is the project-relative path to the declared schema source.
const schemaPath = "db/schema.ts"

// readSchema reads db/schema.ts (relative to cwd) as a string, returning a
// clear error when it is missing — the diff is meaningless without it.
func readSchema() (string, error) {
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no %s in this directory — run from a project with a declared schema", schemaPath)
		}
		return "", fmt.Errorf("read %s: %w", schemaPath, err)
	}
	return string(data), nil
}

// migrationSQL calls Studio's backend.migrationSQL for ONE ENVIRONMENT, shipping
// the declared schema source and decoding {sql, plan}. ref is the Environment's
// ref — the database it diffs against is that environment's.
func migrationSQL(ctx context.Context, c *studio.Client, ref, schema string) (migrationSQLResult, error) {
	input := map[string]any{"ref": ref, "schema": schema}
	var resp migrationSQLResult
	// Same Job rail as db reset: Studio blocks up to 330s on a cold differ, so the
	// client's 120s default would report a timeout while the Job is still running.
	if err := c.WithTimeout(studio.JobCallTimeout).Mutation(ctx, "backend.migrationSQL", input, &resp); err != nil {
		return migrationSQLResult{}, fmt.Errorf("backend.migrationSQL: %w", err)
	}
	return resp, nil
}

// nameSanitizer collapses any run of disallowed characters into a single
// underscore so a free-form --name ("Add Todos!") becomes a safe filename
// slug ("add_todos"). Only [a-z0-9_] survive.
var nameSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeName lowercases and slugifies a migration name to [a-z0-9_],
// trimming leading/trailing underscores. Returns "" for an all-junk name so
// the caller can reject it.
func sanitizeName(raw string) string {
	s := nameSanitizer.ReplaceAllString(strings.ToLower(raw), "_")
	return strings.Trim(s, "_")
}

// unpushedMigrations returns db/migrations/*.sql files that git reports as
// untracked, modified, or committed-but-not-yet-pushed (ahead of upstream) —
// i.e. migrations the deployed branch's live DB has almost certainly NOT applied
// yet. Best-effort: any git error (not a repo, no upstream) yields an empty
// list so `db diff` never blocks on git plumbing.
func unpushedMigrations() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		if !strings.HasPrefix(p, "db/migrations/") || !strings.HasSuffix(p, ".sql") {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	// Untracked or modified working-tree migration files.
	if b, err := exec.Command("git", "status", "--porcelain", "--", "db/migrations").Output(); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if len(line) < 4 {
				continue
			}
			add(line[3:]) // strip the 2-char XY status + space
		}
	}

	// Committed but not yet pushed (ahead of the tracked upstream).
	if b, err := exec.Command("git", "diff", "--name-only", "@{u}..HEAD", "--", "db/migrations").Output(); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			add(line)
		}
	}

	sort.Strings(out)
	return out
}

func diffCmd(r Resolvers) *cobra.Command {
	var nameFlag string
	cmd := &cobra.Command{
		Use:   "diff -f <name>",
		Short: "Generate a migration from db/schema.ts vs the live database",
		Long: `Diff db/schema.ts against the SELECTED environment's database and, if they differ,
write the reconciling migration SQL to db/migrations/<timestamp>_<name>.sql.

When the schema is already in sync, nothing is written. When the migration
drops data (columns or tables), a warning is printed — review before pushing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := sanitizeName(nameFlag)
			if name == "" {
				return fmt.Errorf("-f/--name is required (a short migration name, e.g. -f add_todos)")
			}

			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			ref := sel.EnvironmentRef()
			schema, err := readSchema()
			if err != nil {
				return err
			}

			resp, err := migrationSQL(cmd.Context(), r.Studio(), ref, schema)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if resp.Plan.empty() {
				fmt.Fprintln(out, "✓ schema in sync — no migration needed")
				return nil
			}

			// The diff is computed against the LIVE (applied) database. If there
			// are migration files locally that haven't been deployed yet, the live
			// DB doesn't reflect them — so this diff may RE-generate their changes,
			// producing a duplicate migration. Warn (don't block: a hand-written
			// or already-pushed migration is fine), so the user pushes pending
			// migrations before generating the next one.
			if pending := unpushedMigrations(); len(pending) > 0 {
				fmt.Fprintf(out, "⚠ %d local migration(s) may not be deployed yet:\n", len(pending))
				for _, m := range pending {
					fmt.Fprintf(out, "    %s\n", m)
				}
				fmt.Fprintln(out, "  This diff is computed against the LIVE database, which does NOT")
				fmt.Fprintln(out, "  reflect un-deployed migrations — the new file may duplicate their")
				fmt.Fprintln(out, "  changes. Commit + `git push` pending migrations first, then re-run.")
			}

			migDir := filepath.Join("db", "migrations")
			if err := os.MkdirAll(migDir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", migDir, err)
			}
			ts := time.Now().UTC().Format("20060102T150405")
			filename := fmt.Sprintf("%s_%s.sql", ts, name)
			relPath := filepath.Join(migDir, filename)

			var b strings.Builder
			fmt.Fprintf(&b, "-- palbase db diff: %s\n", name)
			fmt.Fprintf(&b, "-- generated %s\n\n", ts)
			b.WriteString(resp.Sql)
			if !strings.HasSuffix(resp.Sql, "\n") {
				b.WriteByte('\n')
			}
			if err := os.WriteFile(relPath, []byte(b.String()), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", relPath, err)
			}

			destructive := len(resp.Plan.DropColumns) + len(resp.Plan.DropTables)
			fmt.Fprintf(out, "✓ wrote %s\n", relPath)
			fmt.Fprintf(out, "  %d table(s) +, %d column(s) +, %d constraint(s), %d index(es), %d foreign key(s), %d destructive\n",
				len(resp.Plan.AddTables), len(resp.Plan.AddColumns),
				len(resp.Plan.AddConstraints)+len(resp.Plan.DropConstraints),
				len(resp.Plan.AddIndexes)+len(resp.Plan.DropIndexes),
				len(resp.Plan.AddForeignKeys),
				destructive)
			if resp.Plan.hasDestructive() {
				fmt.Fprintln(out, "  WARNING: this migration DROPS data (columns/tables) — review the SQL before pushing.")
				for _, c := range resp.Plan.DropColumns {
					fmt.Fprintf(out, "    drop column %s\n", c)
				}
				for _, tbl := range resp.Plan.DropTables {
					fmt.Fprintf(out, "    drop table %s\n", tbl)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&nameFlag, "name", "f", "", "Migration name (sanitized to [a-z0-9_]) — required")
	return cmd
}

func checkCmd(r Resolvers) *cobra.Command {

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Fail (non-zero) if db/schema.ts has drifted from the live database",
		Long: `Compare db/schema.ts against the deployed branch's database. Exits 0 when they
match; exits non-zero (printing the drift) when they differ. This is the gate
the pre-push hook uses to stop a push that would deploy unmigrated schema
changes — run ` + "`palbase db diff -f <name>`" + ` to generate the migration.`,
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

			resp, err := migrationSQL(cmd.Context(), r.Studio(), ref, schema)
			if err != nil {
				return err
			}

			if resp.Plan.empty() {
				fmt.Fprintln(cmd.OutOrStdout(), "✓ schema in sync")
				return nil
			}

			// Drift detail goes to STDERR so the pre-push hook can show it while
			// keeping STDOUT clean; the non-nil error makes the process exit
			// non-zero, which is what the hook keys on.
			errOut := cmd.ErrOrStderr()
			fmt.Fprintln(errOut, "✗ schema has drifted from the live database:")
			report := func(label string, items []string) {
				for _, it := range items {
					fmt.Fprintf(errOut, "  %s %s\n", label, it)
				}
			}
			report("+ table      ", resp.Plan.AddTables)
			report("+ column     ", resp.Plan.AddColumns)
			report("~ type       ", resp.Plan.TypeChanges)
			report("- column     ", resp.Plan.DropColumns)
			report("- table      ", resp.Plan.DropTables)
			report("+ constraint ", resp.Plan.AddConstraints)
			report("- constraint ", resp.Plan.DropConstraints)
			report("+ index      ", resp.Plan.AddIndexes)
			report("- index      ", resp.Plan.DropIndexes)
			report("+ foreign key", resp.Plan.AddForeignKeys)
			// RLS is drift like any other, and empty() already counts it — but these
			// three were never REPORTED. An RLS-only diff therefore printed the
			// "schema has drifted" header with NOTHING under it and exited non-zero,
			// which is the least actionable failure a gate can produce. Observed live
			// after a db reset, where the committed migrations grant a policy to
			// fewer roles than db/schema.ts declares.
			report("+ rls        ", resp.Plan.EnableRLS)
			report("+ policy     ", resp.Plan.AddPolicies)
			report("~ policy     ", resp.Plan.ChangePolicies)
			fmt.Fprintln(errOut, "run `palbase db diff -f <name>` to generate a migration")
			return fmt.Errorf("schema drift: migration needed")
		},
	}
	return cmd
}
