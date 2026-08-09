package db

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// trpcOK writes a tRPC success envelope (mirrors secret_test.go).
func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// studioAgainst spins an httptest server and returns a *studio.Client backed
// by it, with a static test token (mirrors secret_test.go).
func studioAgainst(t *testing.T, h http.HandlerFunc) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	}, func(context.Context, string, string, string) (string, error) { return "test-proof", nil })
}

// innerInput decodes the inner {"json":{...}} of a tRPC POST body.
func innerInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
	return outer.JSON
}

// chdirTemp creates a temp project dir, writes db/schema.ts with the given
// source, and chdirs into it (restoring on cleanup). The CLI's `db` commands
// read db/schema.ts and write db/migrations/* relative to cwd, so the test
// drives the REAL file-write path against a real (temp) filesystem.
func chdirTemp(t *testing.T, schemaSrc string) string {
	t.Helper()
	dir := t.TempDir()
	if schemaSrc != "<missing>" {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "schema.ts"), []byte(schemaSrc), 0o644))
	}
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}

// migSQLResponse is the shape the br-pod (via Studio backend.migrationSQL)
// returns: {sql, plan{...}}. All 9 DiffPlan fields are included so the CLI
// can decode addConstraints/dropConstraints/addIndexes/dropIndexes correctly.
func migSQLResponse(sql string, plan map[string][]string) map[string]any {
	get := func(k string) []string {
		if v, ok := plan[k]; ok {
			return v
		}
		return []string{}
	}
	body := map[string]any{
		"addTables":          get("addTables"),
		"addColumns":         get("addColumns"),
		"dropColumns":        get("dropColumns"),
		"dropTables":         get("dropTables"),
		"typeChanges":        get("typeChanges"),
		"nullabilityChanges": get("nullabilityChanges"),
		"addConstraints":     get("addConstraints"),
		"dropConstraints":    get("dropConstraints"),
		"addIndexes":         get("addIndexes"),
		"dropIndexes":        get("dropIndexes"),
		"enableRLS":          get("enableRLS"),
		"addPolicies":        get("addPolicies"),
		"changePolicies":     get("changePolicies"),
		"addForeignKeys":     get("addForeignKeys"),
		"unprotectedTables":  get("unprotectedTables"),
	}
	// Pass any key the caller supplied that the list above does not name. Without
	// this the helper SILENTLY DROPS an unknown field, so a test written for a
	// newly-added plan bucket passes against an empty plan and proves nothing —
	// the helper would hide exactly the class of bug these tests exist to catch
	// (a CLI struct missing the server's field).
	for k, v := range plan {
		if _, named := body[k]; !named {
			body[k] = v
		}
	}
	return map[string]any{"sql": sql, "plan": body}
}

// --- db diff -----------------------------------------------------------------

func TestDiff_WritesMigrationFile_WhenPlanNonEmpty(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({ todos: {} })")

	var sentSchema string
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/backend.migrationSQL", r.URL.Path)
		in := innerInput(t, r)
		require.Equal(t, "app1prod", in["ref"])
		sentSchema, _ = in["schema"].(string)
		trpcOK(w, migSQLResponse(
			"CREATE TABLE todos (id uuid);\nALTER TABLE todos ADD COLUMN done boolean;",
			map[string][]string{"addTables": {"todos"}, "addColumns": {"todos.done"}},
		))
	})

	var out, errOut strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "Add Todos!"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	// It POSTed the schema.ts SOURCE verbatim.
	require.Equal(t, "export default defineSchema({ todos: {} })", sentSchema)

	// Exactly one file under db/migrations, named <ts>_<sanitized-name>.sql.
	migDir := filepath.Join(dir, "db", "migrations")
	entries, err := os.ReadDir(migDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	name := entries[0].Name()
	require.True(t, strings.HasSuffix(name, "_add_todos.sql"), "filename %q must end _add_todos.sql (sanitized)", name)
	// Timestamp prefix: 8 digits + 'T' + 6 digits (e.g. 20260605T142233).
	require.Regexp(t, `^\d{8}T\d{6}_add_todos\.sql$`, name)

	body, err := os.ReadFile(filepath.Join(migDir, name))
	require.NoError(t, err)
	content := string(body)
	// Header comment + the SQL from the response.
	require.Contains(t, content, "-- palbase db diff: add_todos")
	require.Contains(t, content, "-- generated ")
	require.Contains(t, content, "CREATE TABLE todos (id uuid);")
	require.Contains(t, content, "ALTER TABLE todos ADD COLUMN done boolean;")

	// Stdout: path + summary line.
	require.Contains(t, out.String(), filepath.Join("db", "migrations", name))
	require.Contains(t, out.String(), "1 table(s) +")
	require.Contains(t, out.String(), "1 column(s) +")
	require.Contains(t, out.String(), "0 constraint(s)")
	require.Contains(t, out.String(), "0 index(es)")
	require.Contains(t, out.String(), "0 destructive")
}

func TestDiff_DestructivePrintsWarning(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"-- DESTRUCTIVE:\nALTER TABLE todos DROP COLUMN legacy;\nDROP TABLE stale;",
			map[string][]string{"dropColumns": {"todos.legacy"}, "dropTables": {"stale"}},
		))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "--name", "drop_stuff"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	entries, err := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	require.NoError(t, err)
	require.Len(t, entries, 1)

	o := out.String()
	require.Contains(t, o, "2 destructive")
	// A loud warning that the migration drops data.
	require.Contains(t, strings.ToUpper(o), "WARNING")
	require.Contains(t, strings.ToLower(o), "drop")
}

func TestDiff_NoOp_WhenPlanEmpty(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse("", nil)) // all arrays empty
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "noop"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	// In sync → wrote NOTHING (no db/migrations dir created).
	_, err := os.Stat(filepath.Join(dir, "db", "migrations"))
	require.True(t, os.IsNotExist(err), "db/migrations must not be created when schema is in sync")
	require.Contains(t, out.String(), "in sync")
}

// TestDiff_WritesMigrationFile_WhenOnlyConstraints verifies that a plan with
// ONLY addConstraints populated is NOT treated as empty — the CLI must write a
// migration file even when the only change is a new constraint.
func TestDiff_WritesMigrationFile_WhenOnlyConstraints(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({ todos: {} })")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"ALTER TABLE todos ADD CONSTRAINT todos_title_not_empty CHECK (title <> '');",
			map[string][]string{"addConstraints": {"todos.todos_title_not_empty"}},
		))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "add_constraint"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	// Migration file MUST be written — constraint-only is NOT in sync.
	entries, err := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "constraint-only diff must write a migration file")

	body, _ := os.ReadFile(filepath.Join(dir, "db", "migrations", entries[0].Name()))
	require.Contains(t, string(body), "ALTER TABLE todos ADD CONSTRAINT")

	// Summary must show 1 constraint, not "in sync".
	o := out.String()
	require.NotContains(t, o, "in sync")
	require.Contains(t, o, "1 constraint(s)")
	require.Contains(t, o, "0 destructive")
}

// TestDiff_WritesMigrationFile_WhenOnlyPolicies verifies that adding a NEW RLS
// policy to an EXISTING table (the live table already exists, the user just
// added a policy in schema.ts) is NOT treated as "in sync". The backend emits
// the CREATE POLICY SQL and populates plan.addPolicies; before the fix the CLI's
// diffPlan struct lacked enableRLS/addPolicies/changePolicies, so JSON unmarshal
// dropped them and empty() wrongly returned true → "schema in sync — no
// migration needed" (a real bug a Penny dev and a membership-RLS test both hit).
func TestDiff_WritesMigrationFile_WhenOnlyPolicies(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({ documents: {} })")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"CREATE POLICY doc_owner_select ON documents AS PERMISSIVE FOR SELECT TO authenticated USING (user_id = (select auth.uid()));",
			// addPolicies ALONE (no enableRLS) — RLS already on, the user just
			// added a new policy to the existing table. Isolating addPolicies
			// proves empty() checks THAT field, not just enableRLS.
			map[string][]string{
				"addPolicies": {"documents.doc_owner_select"},
			},
		))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "add_policy"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	// Migration file MUST be written — a policy-only diff is NOT in sync.
	entries, err := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "policy-only diff must write a migration file")

	body, _ := os.ReadFile(filepath.Join(dir, "db", "migrations", entries[0].Name()))
	require.Contains(t, string(body), "CREATE POLICY doc_owner_select")

	require.NotContains(t, out.String(), "in sync")
}

// TestDiff_WritesMigrationFile_WhenOnlyForeignKeys verifies that a plan with
// ONLY addForeignKeys populated is NOT treated as "in sync" — a .references()
// added to a column of an EXISTING table (the live table exists, the user just
// added the FK in schema.ts) is a real change. The backend emits the
// ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY SQL and populates
// plan.addForeignKeys; before the fix the CLI's diffPlan struct lacked
// addForeignKeys, so JSON unmarshal dropped it and empty() wrongly returned true
// → "schema in sync — no migration needed" (the Penny FK bug, same class as the
// RLS-dropped-in-CLI regression). Mutation: drop the len(p.AddForeignKeys)==0
// clause from empty() → this test goes RED (no migration written, "in sync").
func TestDiff_WritesMigrationFile_WhenOnlyForeignKeys(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({ todos: {} })")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"ALTER TABLE todos ADD CONSTRAINT todos_user_id_fkey FOREIGN KEY (user_id) REFERENCES auth.users(id) ON DELETE CASCADE;",
			// addForeignKeys ALONE — isolating it proves empty() checks THAT field.
			map[string][]string{"addForeignKeys": {"todos.todos_user_id_fkey"}},
		))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "add_fk"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	// Migration file MUST be written — an FK-only diff is NOT in sync.
	entries, err := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "fk-only diff must write a migration file")

	body, _ := os.ReadFile(filepath.Join(dir, "db", "migrations", entries[0].Name()))
	require.Contains(t, string(body), "ADD CONSTRAINT todos_user_id_fkey FOREIGN KEY (user_id) REFERENCES auth.users(id)")

	o := out.String()
	require.NotContains(t, o, "in sync")
	require.Contains(t, o, "1 foreign key(s)")
	require.Contains(t, o, "0 destructive")
}

// TestDiff_WritesMigrationFile_WhenOnlyIndexes verifies that a plan with ONLY
// addIndexes populated is NOT treated as empty.
func TestDiff_WritesMigrationFile_WhenOnlyIndexes(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({ todos: {} })")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"CREATE INDEX todos_title_idx ON todos (title);",
			map[string][]string{"addIndexes": {"todos.todos_title_idx"}},
		))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "add_index"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	// Migration file MUST be written — index-only is NOT in sync.
	entries, err := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "index-only diff must write a migration file")

	body, _ := os.ReadFile(filepath.Join(dir, "db", "migrations", entries[0].Name()))
	require.Contains(t, string(body), "CREATE INDEX todos_title_idx")

	// Summary must show 1 index, not "in sync".
	o := out.String()
	require.NotContains(t, o, "in sync")
	require.Contains(t, o, "1 index(es)")
	require.Contains(t, o, "0 destructive")
}

// TestDiff_DropConstraint_IsNotDestructive verifies that dropping a constraint
// does NOT trigger the destructive warning — DROP CONSTRAINT loses no rows.
func TestDiff_DropConstraint_IsNotDestructive(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({ todos: {} })")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"ALTER TABLE todos DROP CONSTRAINT todos_title_not_empty;",
			map[string][]string{"dropConstraints": {"todos.todos_title_not_empty"}},
		))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "drop_constraint"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	// Migration file IS written.
	entries, err := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	require.NoError(t, err)
	require.Len(t, entries, 1)

	o := out.String()
	// 0 destructive — dropping a constraint doesn't drop data.
	require.Contains(t, o, "0 destructive")
	// No WARNING banner.
	require.NotContains(t, strings.ToUpper(o), "WARNING")
}

func TestDiff_RequiresName(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call Studio when -f/--name is missing")
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SilenceUsage = true
	require.Error(t, cmd.Execute())
}

func TestDiff_ErrorsWhenSchemaMissing(t *testing.T) {
	chdirTemp(t, "<missing>") // no db/schema.ts
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call Studio when db/schema.ts is absent")
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "x"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SilenceUsage = true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "db/schema.ts")
}

// --- db check ----------------------------------------------------------------

func TestCheck_NilWhenInSync(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/backend.migrationSQL", r.URL.Path)
		trpcOK(w, migSQLResponse("", nil))
	})
	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"check"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "in sync")
}

// --- renderCheck (unprotected tables report) ----------------------------
//
// A table that exists without row security is invisible to a DIFF — nobody
// declared a change, so there is nothing to compare. It is exactly the table
// an upgrade to fail-closed defaults (@palbase/backend 16) leaves behind, and
// the only reason the tenant would ever learn about it is this report.

func TestCheckReportsUnprotectedTables(t *testing.T) {
	plan := diffPlan{UnprotectedTables: []string{"todos", "zthreads"}}

	out := renderCheck(plan)

	require.Contains(t, out, "todos")
	require.Contains(t, out, "zthreads")
	require.Contains(t, strings.ToLower(out), "row security")
}

// An empty list must not print a scary heading with nothing under it.
func TestCheckSaysNothingWhenAllProtected(t *testing.T) {
	out := renderCheck(diffPlan{})
	require.NotContains(t, strings.ToLower(out), "row security")
}

// TestCheck_WarnsAboutUnprotectedTables_EvenWhenSchemaInSync locks the CLI
// side of the fix defensively: even if the server ever sent unprotectedTables
// with every other field empty, `db check` must still name the table rather
// than silently swallowing it because empty() is true. In today's real server
// computation this combination cannot actually occur (see
// diffPlan.UnprotectedTables's doc comment — a table never arrives here
// without also tripping DropTables or EnableRLS in the same diff pass), so
// this test drives the scenario through a hand-built HTTP response rather
// than a real deploy — the CLI's rendering must not depend on that server
// invariant holding forever. It must still exit 0 (an existing unprotected
// table is not new drift to block a push over) but now names the table on
// stdout regardless of which branch fires.
func TestCheck_WarnsAboutUnprotectedTables_EvenWhenSchemaInSync(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		resp := migSQLResponse("", nil)
		resp["plan"].(map[string]any)["unprotectedTables"] = []string{"todos"}
		trpcOK(w, resp)
	})
	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"check"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	o := out.String()
	require.Contains(t, o, "in sync")
	require.Contains(t, o, "todos")
	require.Contains(t, strings.ToLower(o), "row security")
}

func TestCheck_ErrorsWhenDrift(t *testing.T) {
	chdirTemp(t, "export default defineSchema({ todos: {} })")
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"CREATE TABLE todos (id uuid);",
			map[string][]string{
				"addTables":   {"todos"},
				"dropColumns": {"old.gone"},
				"typeChanges": {"users.age"},
			},
		))
	})
	var stdout, stderr strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"check"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceUsage = true
	err := cmd.Execute()
	// Non-zero exit (the pre-push hook keys on this) — RunE returns error.
	require.Error(t, err)
	// Drift detail goes to STDERR with the remediation hint.
	e := stderr.String()
	require.Contains(t, e, "todos")
	require.Contains(t, e, "old.gone")
	require.Contains(t, e, "users.age")
	require.Contains(t, e, "palbase db diff")
}

// TestDiff_WritesMigrationFile_WhenOnlyNullability verifies that a plan with
// ONLY nullabilityChanges populated is NOT treated as "in sync". A NOT NULL →
// nullable() edit on an EXISTING column is a real change: the backend now emits
// the ALTER TABLE ... DROP NOT NULL and populates plan.nullabilityChanges.
// Before the fix the differ never compared nullability at all, so the plan was
// EMPTY and `db diff` printed "schema in sync — no migration needed" — the live
// column kept its NOT NULL and every INSERT that relied on the new nullability
// failed in production (the reported credit-ledger bug: Stripe took the money,
// the credit row never inserted). Mutation: drop the
// len(p.NullabilityChanges)==0 clause from empty() → this test goes RED.
func TestDiff_WritesMigrationFile_WhenOnlyNullability(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({ credits: {} })")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"ALTER TABLE credits ALTER COLUMN palai_project_id DROP NOT NULL;",
			// nullabilityChanges ALONE — isolating it proves empty() checks THAT
			// field, and that the JSON tag matches the server's exactly.
			map[string][]string{"nullabilityChanges": {"credits.palai_project_id"}},
		))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"diff", "-f", "relax_credits"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	entries, err := os.ReadDir(filepath.Join(dir, "db", "migrations"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "nullability-only diff must write a migration file")

	body, _ := os.ReadFile(filepath.Join(dir, "db", "migrations", entries[0].Name()))
	require.Contains(t, string(body), "DROP NOT NULL")

	require.NotContains(t, out.String(), "in sync")
	require.Contains(t, out.String(), "1 nullability", "the summary must count it")
}

// TestCheck_NamesNullabilityDrift locks that `db check` NAMES the drift it
// exits non-zero for. A gate that prints "schema has drifted" with nothing
// under it is the least actionable failure it can produce — the same lesson the
// RLS report lines were added for.
func TestCheck_NamesNullabilityDrift(t *testing.T) {
	chdirTemp(t, "export default defineSchema({ credits: {} })")

	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		trpcOK(w, migSQLResponse(
			"ALTER TABLE credits ALTER COLUMN meter SET NOT NULL;",
			map[string][]string{"nullabilityChanges": {"credits.meter"}},
		))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"check"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.Error(t, cmd.Execute(), "drift must exit non-zero")
	require.Contains(t, out.String(), "~ nullability credits.meter")
}
