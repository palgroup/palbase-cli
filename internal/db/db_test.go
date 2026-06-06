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
	})
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
// returns: {sql, plan{addTables,addColumns,dropColumns,dropTables,typeChanges}}.
func migSQLResponse(sql string, plan map[string][]string) map[string]any {
	get := func(k string) []string {
		if v, ok := plan[k]; ok {
			return v
		}
		return []string{}
	}
	return map[string]any{
		"sql": sql,
		"plan": map[string]any{
			"addTables":   get("addTables"),
			"addColumns":  get("addColumns"),
			"dropColumns": get("dropColumns"),
			"dropTables":  get("dropTables"),
			"typeChanges": get("typeChanges"),
		},
	}
}

// --- db diff -----------------------------------------------------------------

func TestDiff_WritesMigrationFile_WhenPlanNonEmpty(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({ todos: {} })")

	var sentSchema string
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/backend.migrationSQL", r.URL.Path)
		in := innerInput(t, r)
		require.Equal(t, "myproj", in["ref"])
		sentSchema, _ = in["schema"].(string)
		trpcOK(w, migSQLResponse(
			"CREATE TABLE todos (id uuid);\nALTER TABLE todos ADD COLUMN done boolean;",
			map[string][]string{"addTables": {"todos"}, "addColumns": {"todos.done"}},
		))
	})

	var out, errOut strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"diff", "--ref", "myproj", "-f", "Add Todos!"})
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
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"diff", "--ref", "myproj", "--name", "drop_stuff"})
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
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"diff", "--ref", "myproj", "-f", "noop"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	// In sync → wrote NOTHING (no db/migrations dir created).
	_, err := os.Stat(filepath.Join(dir, "db", "migrations"))
	require.True(t, os.IsNotExist(err), "db/migrations must not be created when schema is in sync")
	require.Contains(t, out.String(), "in sync")
}

func TestDiff_RequiresName(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call Studio when -f/--name is missing")
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"diff", "--ref", "myproj"})
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
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"diff", "--ref", "myproj", "-f", "x"})
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
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"check", "--ref", "myproj"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "in sync")
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
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"check", "--ref", "myproj"})
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
