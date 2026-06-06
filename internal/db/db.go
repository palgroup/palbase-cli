// Package db provides the `palbase db` subcommand group: diff / check.
//
// These commands implement the config-as-code migration workflow. The
// authoritative schema lives in db/schema.ts; the live database may have
// drifted from it (someone added a column directly, or a teammate pushed a
// schema change). `db diff` asks Studio (which proxies the per-project
// br-pod's POST /internal/migration-sql/{envId}) for the migration SQL that
// reconciles the live DB to the declared schema, and writes it to
// db/migrations/<ts>_<name>.sql. `db check` is the read-only gate the
// pre-push hook keys on: it exits non-zero when the schema has drifted but no
// migration has been generated yet.
//
// The diff is computed SERVER-side: the br-pod is the only thing that can
// reach the tenant DB, so the CLI never touches the database. It ships the
// db/schema.ts SOURCE STRING and gets back {sql, plan}. The plan's five
// string arrays (added/dropped tables+columns, type changes) drive the
// summary line, the destructive warning, and the check gate's drift report.
package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// Resolvers carries the lazily-built Studio client, populated by
// PersistentPreRunE on the root command before any subcommand fires
// (mirrors secret.Resolvers).
type Resolvers struct {
	Studio func() *studio.Client
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
}

// empty reports whether the live DB already matches the declared schema.
func (p diffPlan) empty() bool {
	return len(p.AddTables) == 0 &&
		len(p.AddColumns) == 0 &&
		len(p.DropColumns) == 0 &&
		len(p.DropTables) == 0 &&
		len(p.TypeChanges) == 0
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
		Short: "Manage the project's database schema migrations",
		Long: `Commands to keep db/schema.ts and the live database in sync.

  palbase db diff -f <name>   Generate a migration from db/schema.ts vs the live DB.
  palbase db check            Fail (non-zero) if the schema has drifted but no
                              migration was generated yet (pre-push gate).

The diff is computed server-side: db/schema.ts is sent to Palbase, which diffs
it against the deployed branch's database and returns the migration SQL.`,
	}
	cmd.AddCommand(
		diffCmd(r.Studio),
		checkCmd(r.Studio),
	)
	return cmd
}

// projectRef resolves the linked project ref (mirrors secret.projectRef).
// Order: 1) --ref flag override, 2) .palbase/config.json, 3) error.
func projectRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	cfg, err := auth.LoadProjectConfig()
	if err != nil {
		if os.IsNotExist(errors.Unwrap(err)) || strings.Contains(err.Error(), "not linked") {
			return "", fmt.Errorf("project not linked — pass --ref or run from a project directory")
		}
		return "", err
	}
	if cfg.Ref == "" {
		return "", fmt.Errorf("project not linked — pass --ref or run from a project directory")
	}
	return cfg.Ref, nil
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

// migrationSQL calls Studio's backend.migrationSQL for the given ref+branch,
// shipping the declared schema source and decoding {sql, plan}.
func migrationSQL(ctx context.Context, c *studio.Client, ref, branch, schema string) (migrationSQLResult, error) {
	input := map[string]any{"ref": ref, "schema": schema}
	if branch != "" {
		input["branch"] = branch
	}
	var resp migrationSQLResult
	if err := c.Mutation(ctx, "backend.migrationSQL", input, &resp); err != nil {
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

func diffCmd(studioFn func() *studio.Client) *cobra.Command {
	var (
		refFlag    string
		branchFlag string
		nameFlag   string
	)
	cmd := &cobra.Command{
		Use:   "diff -f <name>",
		Short: "Generate a migration from db/schema.ts vs the live database",
		Long: `Diff db/schema.ts against the deployed branch's database and, if they differ,
write the reconciling migration SQL to db/migrations/<timestamp>_<name>.sql.

When the schema is already in sync, nothing is written. When the migration
drops data (columns or tables), a warning is printed — review before pushing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := sanitizeName(nameFlag)
			if name == "" {
				return fmt.Errorf("-f/--name is required (a short migration name, e.g. -f add_todos)")
			}

			ref, err := projectRef(refFlag)
			if err != nil {
				return err
			}
			schema, err := readSchema()
			if err != nil {
				return err
			}

			resp, err := migrationSQL(cmd.Context(), studioFn(), ref, branchFlag, schema)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if resp.Plan.empty() {
				fmt.Fprintln(out, "✓ schema in sync — no migration needed")
				return nil
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
			fmt.Fprintf(out, "  %d table(s) +, %d column(s) +, %d destructive\n",
				len(resp.Plan.AddTables), len(resp.Plan.AddColumns), destructive)
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
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to diff against (defaults to the project's default branch)")
	cmd.Flags().StringVarP(&nameFlag, "name", "f", "", "Migration name (sanitized to [a-z0-9_]) — required")
	return cmd
}

func checkCmd(studioFn func() *studio.Client) *cobra.Command {
	var (
		refFlag    string
		branchFlag string
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Fail (non-zero) if db/schema.ts has drifted from the live database",
		Long: `Compare db/schema.ts against the deployed branch's database. Exits 0 when they
match; exits non-zero (printing the drift) when they differ. This is the gate
the pre-push hook uses to stop a push that would deploy unmigrated schema
changes — run ` + "`palbase db diff -f <name>`" + ` to generate the migration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := projectRef(refFlag)
			if err != nil {
				return err
			}
			schema, err := readSchema()
			if err != nil {
				return err
			}

			resp, err := migrationSQL(cmd.Context(), studioFn(), ref, branchFlag, schema)
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
			report("+ table   ", resp.Plan.AddTables)
			report("+ column  ", resp.Plan.AddColumns)
			report("~ type    ", resp.Plan.TypeChanges)
			report("- column  ", resp.Plan.DropColumns)
			report("- table   ", resp.Plan.DropTables)
			fmt.Fprintln(errOut, "run `palbase db diff -f <name>` to generate a migration")
			return fmt.Errorf("schema drift: migration needed")
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to check against (defaults to the project's default branch)")
	return cmd
}
