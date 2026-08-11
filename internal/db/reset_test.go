package db

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// writeMigrations drops the given <name>: <sql> pairs into db/migrations under cwd.
func writeMigrations(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	migDir := filepath.Join(dir, "db", "migrations")
	require.NoError(t, os.MkdirAll(migDir, 0o755))
	for name, sql := range files {
		require.NoError(t, os.WriteFile(filepath.Join(migDir, name), []byte(sql), 0o644))
	}
}

func resetOKResponse(dropped, applied int, stems ...string) map[string]any {
	if stems == nil {
		stems = []string{}
	}
	return map[string]any{
		"droppedObjects":    dropped,
		"appliedMigrations": applied,
		"migrations":        stems,
	}
}

// TestReset_ShipsMigrationsInOrder is the core contract: every committed migration is
// sent, with its FILENAME intact and in lexicographic (= chronological, given the
// timestamped stems) order — the same order the server replays them in. A reordering
// here would replay an ALTER before its CREATE.
func TestReset_ShipsMigrationsInOrder(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")
	writeMigrations(t, dir, map[string]string{
		"20260202T000000_add_done.sql": "ALTER TABLE todos ADD done bool",
		"20260101T000000_init.sql":     "CREATE TABLE todos ()",
		"notes.txt":                    "not a migration",
	})

	var sent []any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/backend.dbReset", r.URL.Path)
		in := innerInput(t, r)
		require.Equal(t, "app1prod", in["ref"])
		sent, _ = in["migrations"].([]any)
		trpcOK(w, resetOKResponse(3, 2, "20260101T000000_init", "20260202T000000_add_done"))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Len(t, sent, 2, "only .sql files are migrations")
	first := sent[0].(map[string]any)
	second := sent[1].(map[string]any)
	require.Equal(t, "20260101T000000_init.sql", first["name"], "migrations must be ordered by stem")
	require.Equal(t, "CREATE TABLE todos ()", first["sql"])
	require.Equal(t, "20260202T000000_add_done.sql", second["name"])

	require.Contains(t, out.String(), "dropped 3 object(s), replayed 2 migration(s)")
}

// TestReset_WithoutConfirmationSendsNothing is the guard on the destructive prompt:
// anything other than the exact ref aborts BEFORE the request. A y/N typo on the
// wrong terminal would otherwise wipe an environment's data.
//
// MUTATION GUARD: dropping the confirmation branch turns this RED.
func TestReset_WithoutConfirmationSendsNothing(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")
	writeMigrations(t, dir, map[string]string{"20260101T000000_init.sql": "CREATE TABLE t ()"})

	called := false
	c := studioAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		trpcOK(w, resetOKResponse(0, 0))
	})

	for _, answer := range []string{"y\n", "yes\n", "\n", "app1pro\n", "APP1PROD\n"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			called = false
			var out strings.Builder
			cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
			cmd.SetArgs([]string{"reset"})
			cmd.SetIn(strings.NewReader(answer))
			cmd.SetOut(&out)
			cmd.SilenceUsage = true
			require.NoError(t, cmd.Execute())

			require.False(t, called, "a mistyped confirmation must never reach the server")
			require.Contains(t, out.String(), "Aborted.")
		})
	}
}

// TestReset_TypedRefConfirms proves the prompt accepts the real ref — the guard must
// not be so strict that the command is unusable.
func TestReset_TypedRefConfirms(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")
	writeMigrations(t, dir, map[string]string{"20260101T000000_init.sql": "CREATE TABLE t ()"})

	called := false
	c := studioAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		trpcOK(w, resetOKResponse(1, 1, "20260101T000000_init"))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.True(t, called, "typing the ref must confirm the reset")
	require.Contains(t, out.String(), "✓ reset app1prod")
}

// TestReset_NoMigrationsWarnsTheSchemaWillBeEmpty — resetting a project with no
// committed migrations is legitimate (that is how you start over), but it leaves an
// EMPTY schema. Saying so is the difference between a deliberate act and a surprise.
func TestReset_NoMigrationsWarnsTheSchemaWillBeEmpty(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")

	var sent []any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		in := innerInput(t, r)
		sent, _ = in["migrations"].([]any)
		trpcOK(w, resetOKResponse(4, 0))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Empty(t, sent, "a missing db/migrations must send [] rather than fail")
	require.Contains(t, out.String(), "the schema will be left EMPTY")
	require.Contains(t, out.String(), "The schema is now EMPTY")
}

// TestReset_ServerFailureSurfaces keeps a failed reset an ERROR. Reporting success
// after a failed replay would tell the user their schema was rebuilt when it may be
// empty.
func TestReset_ServerFailureSurfaces(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")
	writeMigrations(t, dir, map[string]string{"20260101T000000_init.sql": "CREATE TABLE t ()"})

	c := studioAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"json":{"message":"db_reset_failed: migration 1 failed","code":-32603}}}`))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "backend.dbReset")
}

// TestReset_IsRegisteredUnderDB locks the command surface: `palbase db reset` exists
// and takes no positional args (the target is the SELECTED environment, never a ref
// typed on the command line — that is how you reset the wrong environment).
func TestReset_IsRegisteredUnderDB(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
	}
	require.Contains(t, names, "reset")

	for _, c := range cmd.Commands() {
		if c.Name() == "reset" {
			require.Error(t, c.Args(c, []string{"some-ref"}),
				"reset must not accept a ref argument — it acts on the selected environment")
		}
	}
}

func TestReset_ProductionRefusesTheYesFlag(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")
	writeMigrations(t, dir, map[string]string{"20260101T000000_init.sql": "CREATE TABLE t ()"})

	called := false
	c := studioAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		trpcOK(w, resetOKResponse(0, 0))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset", "--yes"})
	cmd.SetOut(&out)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	require.Error(t, err, "--yes must not be enough to reset production")
	require.Contains(t, err.Error(), "production")
	require.Contains(t, err.Error(), "database:reset", "point the user at the automation path")
	require.False(t, called, "nothing may be dispatched")
}

// TestReset_WarnsWhatDiesAndWhatSurvives — "deletes all data" alone reads as
// "deletes the environment", which is the wrong mental model (the ref, URL and
// keys survive) and pushes people toward the far more destructive env delete. The
// prompt has to state both halves, and mark production as production.
func TestReset_WarnsWhatDiesAndWhatSurvives(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")
	writeMigrations(t, dir, map[string]string{"20260101T000000_init.sql": "CREATE TABLE t ()"})

	c := studioAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		trpcOK(w, resetOKResponse(1, 1, "20260101T000000_init"))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	report := out.String()
	require.Contains(t, report, "DESTRUCTIVE")
	require.Contains(t, report, "PRODUCTION", "a production target must be marked as such")
	require.Contains(t, report, "Dropped:")
	require.Contains(t, report, "cannot be undone")
	require.Contains(t, report, "Kept:")
	require.Contains(t, report, "API keys", "say the environment itself survives")
}

// TestReset_SendsConfirmRef — the server REQUIRES confirm_ref to match on
// production; a CLI that omitted it would fail every production reset with a
// confusing validation error after the user had already typed the ref.
func TestReset_SendsConfirmRef(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")
	writeMigrations(t, dir, map[string]string{"20260101T000000_init.sql": "CREATE TABLE t ()"})

	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		got = innerInput(t, r)
		trpcOK(w, resetOKResponse(1, 1, "20260101T000000_init"))
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "app1prod", got["confirmRef"])
}

// TestReset_ConfirmRefIsWhatTheUserTyped_NotAutoFilled is the anti-spoofing lock.
// The server's production rule (confirm_ref must equal the ref) is only worth
// anything if this CLI cannot satisfy it on the user's behalf. Under --yes there
// is no typed confirmation, so an EMPTY confirm_ref must go on the wire — the
// server then refuses production regardless of what a patched client believes
// about is_production.
//
// MUTATION GUARD: auto-filling confirmRef with the ref turns this RED.
func TestReset_ConfirmRefIsWhatTheUserTyped_NotAutoFilled(t *testing.T) {
	dir := chdirTemp(t, "export default defineSchema({})")
	writeMigrations(t, dir, map[string]string{"20260101T000000_init.sql": "CREATE TABLE t ()"})

	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		got = innerInput(t, r)
		trpcOK(w, resetOKResponse(1, 1, "20260101T000000_init"))
	})

	// A NON-production environment, so --yes is allowed to reach the server at all.
	fake := selectiontest.New(t)
	fake.Environments["proj_1"] = []selection.Environment{
		selectiontest.Env("env_dev", "proj_1", "app1dev", "dev", "dev", false),
	}
	selectiontest.WriteConfig(t, ".", &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_dev",
		RepositoryProvider: selection.ProviderPalbase,
	})
	resolver := fake.Resolver()

	var out strings.Builder
	cmd := Cmd(Resolvers{
		Studio:    func() *studio.Client { return c },
		Selection: func() *selection.Resolver { return resolver },
	})
	cmd.SetArgs([]string{"reset", "--yes"})
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "", got["confirmRef"],
		"--yes types nothing, so nothing may be claimed as a confirmation")
}
