package db

import (
	"net/http"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

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

func TestReset_WithoutConfirmationSendsNothing(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")

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
	chdirTemp(t, "export default defineSchema({})")

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

func TestReset_ServerFailureSurfaces(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")

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
	chdirTemp(t, "export default defineSchema({})")

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
	chdirTemp(t, "export default defineSchema({})")

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

// resetRail answers the three calls a reset now makes and records each one's input
// separately. A fake that answered them all alike handed the test whichever call
// came last, which is how the confirm_ref assertions started reading the apply's
// input instead of the reset's.
func resetRail(t *testing.T, plan map[string]any) (*studio.Client, map[string]map[string]any) {
	t.Helper()
	seen := map[string]map[string]any{}
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "databaseReset"), strings.Contains(r.URL.Path, "dbReset"):
			seen["reset"] = innerInput(t, r)
			trpcOK(w, resetOKResponse(1, 0))
		case strings.Contains(r.URL.Path, "schemaPlan"):
			seen["plan"] = innerInput(t, r)
			trpcOK(w, plan)
		case strings.Contains(r.URL.Path, "schemaApply"):
			seen["apply"] = innerInput(t, r)
			trpcOK(w, map[string]any{"planId": "pl_1", "statementsApplied": 1})
		default:
			seen[r.URL.Path] = innerInput(t, r)
			trpcOK(w, map[string]any{})
		}
	})
	return c, seen
}

// emptyPlan is what a reset of a project whose declaration is empty produces.
var emptyPlan = map[string]any{"sql": "", "fromFingerprint": "a", "toFingerprint": "b", "findings": []any{}, "destructive": nil}

// TestReset_SendsConfirmRef — the server REQUIRES confirm_ref to match on
// production; a CLI that omitted it would fail every production reset with a
// confusing validation error after the user had already typed the ref.
func TestReset_SendsConfirmRef(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")
	c, seen := resetRail(t, emptyPlan)

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "app1prod", seen["reset"]["confirmRef"])
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
	chdirTemp(t, "export default defineSchema({})")
	c, seen := resetRail(t, emptyPlan)

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

	require.Equal(t, "", seen["reset"]["confirmRef"],
		"--yes types nothing, so nothing may be claimed as a confirmation")
}

// A reset used to replay db/migrations. There are none, so what rebuilds the schema
// is the declaration — planned against the database the reset just emptied, which
// makes the plan purely additive and therefore never one to approve.
//
// MUTATION GUARD: dropping the plan+apply after the reset leaves the database EMPTY
// and turns this RED.
func TestReset_RebuildsFromTheDeclaration(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")
	c, seen := resetRail(t, map[string]any{
		"sql":             `create table "public"."todos" (id uuid primary key);`,
		"fromFingerprint": "a", "toFingerprint": "b",
		"findings": []any{}, "destructive": nil,
	})

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.NotNil(t, seen["plan"], "the declaration was never planned — the schema would stay empty")
	require.NotNil(t, seen["apply"], "the plan was never applied — the schema would stay empty")

	// The order is the property: planning before the drop would describe the schema
	// that is about to be destroyed.
	require.Contains(t, out.String(), "reset app1prod")
	require.Contains(t, out.String(), "rebuilding from db/schema.ts")

	// The declaration must be what travels, not a migration list.
	require.Equal(t, "export default defineSchema({})", seen["plan"]["schema"])
}

// A reset must never ship migration files: there are none, and a client still
// sending them would be describing a rail the server no longer has.
func TestReset_ShipsNoMigrations(t *testing.T) {
	chdirTemp(t, "export default defineSchema({})")
	c, seen := resetRail(t, emptyPlan)

	var out strings.Builder
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"reset"})
	cmd.SetIn(strings.NewReader("app1prod\n"))
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	migrations, present := seen["reset"]["migrations"]
	if present && migrations != nil {
		if list, ok := migrations.([]any); ok && len(list) > 0 {
			t.Fatalf("the reset shipped %d migration(s): %v", len(list), list)
		}
	}
}
