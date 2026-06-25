package testuser

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
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
	})
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

// TestTestUserCreate_HasFlags asserts the `test-user create` command exists
// under `test-user` with --scenario / --count / --json flags.
func TestTestUserCreate_HasFlags(t *testing.T) {
	parent := Cmd(Resolvers{Studio: func() Studio { return nil }})
	require.Equal(t, "test-user", parent.Name())

	var hasCreate bool
	for _, c := range parent.Commands() {
		if c.Name() == "create" {
			hasCreate = true
			for _, flag := range []string{"scenario", "count", "json"} {
				require.NotNil(t, c.Flags().Lookup(flag), "create must define --%s", flag)
			}
		}
	}
	require.True(t, hasCreate, "test-user must have a `create` subcommand")
}

// TestTestUserCreate_PlainJSON proves the no-scenario path calls
// testData.testUserCreate and that --json emits the creds+token.
func TestTestUserCreate_PlainJSON(t *testing.T) {
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
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	// The plain path sends count (default 1) + withTokens.
	require.EqualValues(t, 1, body["count"])
	require.Equal(t, true, body["withTokens"])

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
	require.Equal(t, "t1@x.dev", got.Users[0].Email)
	require.Equal(t, "pw1", got.Users[0].Password)
	require.Equal(t, "tok1", got.Users[0].AccessToken)
}

// TestTestUserCreate_PlainCountFlag proves --count is forwarded to the mint.
func TestTestUserCreate_PlainCountFlag(t *testing.T) {
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
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--count", "3"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	require.EqualValues(t, 3, body["count"])
}

// TestTestUserCreate_ScenarioJSON proves the --scenario path calls
// testData.runScenario with the scenario name and emits the minted user's
// creds+token (with inserted summary) as JSON.
func TestTestUserCreate_ScenarioJSON(t *testing.T) {
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/testData.runScenario", r.URL.Path)
		body = innerInput(t, r)
		trpcOK(w, map[string]any{
			"user": map[string]any{
				"id": "usr_s", "email": "s@x.dev", "password": "pws", "access_token": "toks",
			},
			"inserted": map[string]any{"todos": 2, "comments": 4},
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--scenario", "demo", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	// The scenario path sends the scenario name + ref.
	require.Equal(t, "demo", body["name"])
	require.Equal(t, "abcd1234", body["ref"])

	// --json emits the minted user's creds+token + the inserted summary.
	var got struct {
		User struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			Password    string `json:"password"`
			AccessToken string `json:"access_token"`
		} `json:"user"`
		Inserted map[string]int `json:"inserted"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "usr_s", got.User.ID)
	require.Equal(t, "toks", got.User.AccessToken)
	require.Equal(t, 2, got.Inserted["todos"])
	require.Equal(t, 4, got.Inserted["comments"])
}
