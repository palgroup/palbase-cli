package testuser

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// trpcOK writes a tRPC success envelope ({result:{data:{json:...}}}).
func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// studioAgainst spins an httptest server and returns a *studio.Client backed
// by it (mirrors apps_test.go / secret_test.go).
func studioAgainst(t *testing.T, h http.HandlerFunc) Studio {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	}, func(context.Context, string, string, string) (string, error) { return "test-proof", nil })
}

// innerInput decodes the inner {"json":{...}} payload from a tRPC POST body.
func innerInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
	return outer.JSON
}

// TestTestUserSurface pins the subcommand set and the create flags. The command
// group is the whole lifecycle now, not just minting.
func TestTestUserSurface(t *testing.T) {
	t.Chdir(t.TempDir())
	parent := Cmd(Resolvers{Studio: func() Studio { return nil }})
	require.Equal(t, "test-user", parent.Name())

	subs := map[string]*cobra.Command{}
	for _, c := range parent.Commands() {
		subs[c.Name()] = c
	}
	for _, name := range []string{"create", "list", "templates", "clone", "delete"} {
		require.Contains(t, subs, name, "test-user must have a `%s` subcommand", name)
	}

	for _, flag := range []string{"template", "count", "json"} {
		require.NotNil(t, subs["create"].Flags().Lookup(flag), "create must define --%s", flag)
	}
	// --scenario is gone: templates come from config/test-users.ts now, and the
	// old scenario format had no way to author a scenario in the first place.
	require.Nil(t, subs["create"].Flags().Lookup("scenario"), "--scenario must be gone")

	for _, flag := range []string{"email", "password", "set", "json"} {
		require.NotNil(t, subs["clone"].Flags().Lookup(flag), "clone must define --%s", flag)
	}
}

// TestTestUserCreate_PlainJSON proves the no-template path calls
// testData.testUserCreate and that --json emits the creds+token.
func TestTestUserCreate_PlainJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/testData.testUserCreate", r.URL.Path)
		body = innerInput(t, r)
		trpcOK(w, map[string]any{
			"users": []map[string]any{
				{"id": "usr_1", "email": "t1@x.dev", "password": "pw1", "accessToken": "tok1"},
			},
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"create", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	// The plain path sends count (default 1) + withTokens.
	require.EqualValues(t, 1, body["count"])
	require.Equal(t, true, body["withTokens"])
	// The mint targets the SELECTED ENVIRONMENT by ref, and carries no branch:
	// each environment verifies tokens against its OWN auth, so the environment
	// IS the isolation boundary a minted token is scoped to.
	require.Equal(t, "app1prod", body["ref"])
	require.NotContains(t, body, "branch", "the Palbase branch is gone — the environment is the target")

	// --json emits the creds+token verbatim (scriptable).
	var got struct {
		Users []struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			Password    string `json:"password"`
			AccessToken string `json:"accessToken"`
		} `json:"users"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Len(t, got.Users, 1)
	require.Equal(t, "usr_1", got.Users[0].ID)
	require.Equal(t, "tok1", got.Users[0].AccessToken)
}

// TestTestUserCreate_PlainCountFlag proves --count is forwarded to the mint.
func TestTestUserCreate_PlainCountFlag(t *testing.T) {
	t.Chdir(t.TempDir())
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/testData.testUserCreate", r.URL.Path)
		body = innerInput(t, r)
		trpcOK(w, map[string]any{"users": []map[string]any{
			{"id": "usr_1", "email": "a@x.dev", "password": "pw", "accessToken": ""},
			{"id": "usr_2", "email": "b@x.dev", "password": "pw", "accessToken": ""},
			{"id": "usr_3", "email": "c@x.dev", "password": "pw", "accessToken": ""},
		}})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"create", "--count", "3"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	require.EqualValues(t, 3, body["count"])
}

// TestTestUserCreate_CountAndTemplateMutuallyExclusive asserts that combining
// --count with --template is rejected before any network call is made.
func TestTestUserCreate_CountAndTemplateMutuallyExclusive(t *testing.T) {
	t.Chdir(t.TempDir())
	called := false
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		trpcOK(w, map[string]any{})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"create", "--template", "demo", "--count", "2"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--count cannot be combined with --template")
	require.False(t, called, "transport must not be called when flag validation fails")
}

// TestTestUserCreate_TemplateJSON proves the --template path calls
// testData.createFromTemplate and emits the minted user's creds+token plus the
// per-table inserted summary.
func TestTestUserCreate_TemplateJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/testData.createFromTemplate", r.URL.Path)
		body = innerInput(t, r)
		trpcOK(w, map[string]any{
			"user": map[string]any{
				"id": "usr_s", "email": "s@x.dev", "password": "pws", "accessToken": "toks",
			},
			"inserted": map[string]any{"lists": 2, "todos": 4},
			"existing": false,
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"create", "--template", "demo", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, "demo", body["name"])
	require.Equal(t, "app1prod", body["ref"])

	var got materializeResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "usr_s", got.User.ID)
	require.Equal(t, "toks", got.User.AccessToken)
	require.Equal(t, 2, got.Inserted["lists"])
	require.Equal(t, 4, got.Inserted["todos"])
}

// TestTestUserList queries the environment's test users.
func TestTestUserList(t *testing.T) {
	t.Chdir(t.TempDir())
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "testData.testUsers")
		trpcOK(w, map[string]any{"users": []map[string]any{
			{"id": "usr_1", "email": "a@test.invalid"},
			{"id": "usr_2", "email": nil},
		}})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"list"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Contains(t, out.String(), "usr_1")
	require.Contains(t, out.String(), "a@test.invalid")
	// A user with no readable e-mail still lists, with a placeholder.
	require.Contains(t, out.String(), "usr_2")
}

// TestTestUserTemplates lists what config/test-users.ts declares.
func TestTestUserTemplates(t *testing.T) {
	t.Chdir(t.TempDir())
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "testData.listTemplates")
		trpcOK(w, []map[string]any{
			{"name": "demo", "email": "demo@test.local", "tables": []string{"lists", "todos"}},
			{"name": "heavy", "email": nil, "tables": []string{}},
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"templates"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Contains(t, out.String(), "demo")
	require.Contains(t, out.String(), "demo@test.local")
	require.Contains(t, out.String(), "lists, todos")
	require.Contains(t, out.String(), "heavy")
}

// TestTestUserTemplates_EmptyPointsAtConfig: with nothing declared, the CLI
// says where templates come from rather than printing an empty table.
func TestTestUserTemplates_Empty(t *testing.T) {
	t.Chdir(t.TempDir())
	c := studioAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		trpcOK(w, []map[string]any{})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"templates"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "No templates on this stack")
}

// TestTestUserClone forwards the source, the fixed credentials and the parsed
// --set overrides.
func TestTestUserClone(t *testing.T) {
	t.Chdir(t.TempDir())
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/testData.cloneTestUser", r.URL.Path)
		body = innerInput(t, r)
		trpcOK(w, map[string]any{
			"user":     map[string]any{"id": "usr_c", "email": "c@x.dev", "password": "pw", "accessToken": "tok"},
			"inserted": map[string]any{"lists": 1},
			"existing": false,
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{
		"clone", "usr_src",
		"--email", "clone@test.local",
		"--password", "clone-password-1",
		"--set", "profiles.display_name=Copy",
		"--set", "profiles.age=41",
		"--set", "lists.archived_at=null",
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, "usr_src", body["sourceUserId"])
	require.Equal(t, "clone@test.local", body["email"])
	require.Equal(t, "clone-password-1", body["password"])

	overrides, ok := body["overrides"].(map[string]any)
	require.True(t, ok, "overrides must be sent as a table→column map")
	profiles := overrides["profiles"].(map[string]any)
	require.Equal(t, "Copy", profiles["display_name"])
	// A numeric value must arrive typed, not as the string "41".
	require.EqualValues(t, 41, profiles["age"])
	// And `null` must arrive as JSON null, so the column is actually cleared.
	lists := overrides["lists"].(map[string]any)
	require.Contains(t, lists, "archived_at")
	require.Nil(t, lists["archived_at"])
}

// TestTestUserClone_NoCredentials omits both fields so the server generates a
// pair, rather than sending empty strings it would try to mint with.
func TestTestUserClone_NoCredentials(t *testing.T) {
	t.Chdir(t.TempDir())
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		body = innerInput(t, r)
		trpcOK(w, map[string]any{
			"user":     map[string]any{"id": "usr_c", "email": "c@x.dev", "password": "pw", "accessToken": ""},
			"inserted": map[string]any{},
			"existing": false,
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"clone", "usr_src"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.NotContains(t, body, "email")
	require.NotContains(t, body, "password")
	require.NotContains(t, body, "overrides")
}

// TestTestUserClone_HalfCredentialPair rejects before the network call.
func TestTestUserClone_HalfCredentialPair(t *testing.T) {
	t.Chdir(t.TempDir())
	called := false
	c := studioAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		trpcOK(w, map[string]any{})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"clone", "usr_src", "--email", "x@test.local"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be given together")
	require.False(t, called, "transport must not be called when flag validation fails")
}

// TestTestUserDelete purges by id.
func TestTestUserDelete(t *testing.T) {
	t.Chdir(t.TempDir())
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/testData.deleteTestUser", r.URL.Path)
		body = innerInput(t, r)
		trpcOK(w, map[string]any{"ok": true})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"delete", "usr_gone"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, "usr_gone", body["userId"])
	require.Equal(t, "app1prod", body["ref"])
	require.Contains(t, out.String(), "usr_gone")
}

// TestParseSets covers the --set grammar directly, including the malformed
// forms that must be rejected rather than silently dropped.
func TestParseSets(t *testing.T) {
	t.Run("groups by table and types values", func(t *testing.T) {
		got, err := parseSets([]string{
			"profiles.display_name=Copy",
			"profiles.age=41",
			"profiles.active=true",
			"lists.archived_at=null",
			`lists.meta={"a":1}`,
		})
		require.NoError(t, err)
		require.Equal(t, "Copy", got["profiles"]["display_name"])
		require.EqualValues(t, 41, got["profiles"]["age"])
		require.Equal(t, true, got["profiles"]["active"])
		require.Nil(t, got["lists"]["archived_at"])
		require.Equal(t, map[string]any{"a": float64(1)}, got["lists"]["meta"])
	})

	t.Run("keeps a value that only looks like JSON as a string", func(t *testing.T) {
		got, err := parseSets([]string{"profiles.bio=hello world"})
		require.NoError(t, err)
		require.Equal(t, "hello world", got["profiles"]["bio"])
	})

	t.Run("accepts an empty value", func(t *testing.T) {
		got, err := parseSets([]string{"profiles.bio="})
		require.NoError(t, err)
		require.Equal(t, "", got["profiles"]["bio"])
	})

	t.Run("rejects a missing =", func(t *testing.T) {
		_, err := parseSets([]string{"profiles.display_name"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "table.column=value")
	})

	t.Run("rejects a missing table qualifier", func(t *testing.T) {
		_, err := parseSets([]string{"display_name=Copy"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "table and a column")
	})

	t.Run("nil for no sets", func(t *testing.T) {
		got, err := parseSets(nil)
		require.NoError(t, err)
		require.Nil(t, got)
	})
}
