package backend

import (
	"bytes"
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

// remoteEnvStudio spins an httptest Studio that answers env.pull with `rows`
// (or `status` != 200 for the failure path) and records the decoded tRPC
// input, so the test can assert serve reuses the exact `secret pull` procedure
// with the right ref/branch.
func remoteEnvStudio(t *testing.T, status int, rows []map[string]string, gotInput *map[string]any, called *bool) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		require.Equal(t, "/api/trpc/env.pull", r.URL.Path, "serve must reuse the secret-pull procedure (env.pull)")
		var outer struct {
			JSON map[string]any `json:"json"`
		}
		require.NoError(t, json.Unmarshal([]byte(r.URL.Query().Get("input")), &outer))
		*gotInput = outer.JSON
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"json":{"message":"forbidden"}}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"data": map[string]any{"json": rows}},
		})
	}))
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, nil)
}

// appendRemoteEnv is serve's automatic branch-env fetch: it must deliver the
// remote vars through a 0600 JSON file (PALBASE_REMOTE_ENV_FILE) — never
// directly in the spawn env, which would beat .env.local — and must NEVER
// block serve: any failure is exactly one warning line + env unchanged.
func TestAppendRemoteEnv_SuccessWritesFileAndAppendsEnv(t *testing.T) {
	var gotInput map[string]any
	var called bool
	sc := remoteEnvStudio(t, http.StatusOK, []map[string]string{
		{"key": "OPENAI_API_KEY", "value": "sk-remote"},
		{"key": "MULTI", "value": "line1\nline2"},
	}, &gotInput, &called)

	dir := t.TempDir()
	base := []string{"BASE=1"}
	var out, errW bytes.Buffer
	env := appendRemoteEnv(context.Background(), sc, "todoapp", "", dir, base, &out, &errW)

	require.True(t, called)
	require.Equal(t, "todoapp", gotInput["ref"])
	_, hasBranch := gotInput["branch"]
	require.False(t, hasBranch, "branch \"\" (main) must be omitted from the payload")

	// Env: base preserved + exactly the file pointer appended.
	require.Equal(t, []string{"BASE=1", "PALBASE_REMOTE_ENV_FILE=" + filepath.Join(dir, "remote-env.json")}, env)

	// File: 0600, JSON map of the pulled vars (values intact, incl. newlines).
	info, err := os.Stat(filepath.Join(dir, "remote-env.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "remote env file holds decrypted secrets — must be 0600")
	data, err := os.ReadFile(filepath.Join(dir, "remote-env.json"))
	require.NoError(t, err)
	var m map[string]string
	require.NoError(t, json.Unmarshal(data, &m))
	require.Equal(t, map[string]string{"OPENAI_API_KEY": "sk-remote", "MULTI": "line1\nline2"}, m)

	// One info line, values never logged.
	require.Equal(t, "loaded 2 remote env var(s) for branch main\n", out.String())
	require.NotContains(t, out.String(), "sk-remote")
	require.Empty(t, errW.String())
}

func TestAppendRemoteEnv_ThreadsBranch(t *testing.T) {
	var gotInput map[string]any
	var called bool
	sc := remoteEnvStudio(t, http.StatusOK, []map[string]string{}, &gotInput, &called)

	var out, errW bytes.Buffer
	env := appendRemoteEnv(context.Background(), sc, "todoapp", "qa", t.TempDir(), nil, &out, &errW)

	require.True(t, called)
	require.Equal(t, "qa", gotInput["branch"], "the active branch must reach env.pull — otherwise serve loads main's vars for a qa session")
	require.Len(t, env, 1) // still appends the (empty) file — 0 remote vars is a valid state
	require.Equal(t, "loaded 0 remote env var(s) for branch qa\n", out.String())
}

func TestAppendRemoteEnv_FailureWarnsAndContinues(t *testing.T) {
	var gotInput map[string]any
	var called bool
	sc := remoteEnvStudio(t, http.StatusForbidden, nil, &gotInput, &called) // env.pull needs project admin

	base := []string{"BASE=1"}
	var out, errW bytes.Buffer
	env := appendRemoteEnv(context.Background(), sc, "todoapp", "", t.TempDir(), base, &out, &errW)

	require.True(t, called)
	require.Equal(t, base, env, "on failure the spawn env must be untouched (no PALBASE_REMOTE_ENV_FILE)")
	require.Equal(t, 1, strings.Count(errW.String(), "\n"), "exactly ONE warning line")
	require.Contains(t, errW.String(), "warning: could not fetch remote env vars")
	require.Contains(t, errW.String(), "using local env only")
	require.Empty(t, out.String(), "no success line on failure")
}

func TestAppendRemoteEnv_UnlinkedProjectWarnsWithoutCalling(t *testing.T) {
	for _, ref := range []string{"", "local"} {
		t.Run("ref="+ref, func(t *testing.T) {
			var gotInput map[string]any
			var called bool
			sc := remoteEnvStudio(t, http.StatusOK, nil, &gotInput, &called)

			var out, errW bytes.Buffer
			env := appendRemoteEnv(context.Background(), sc, ref, "", t.TempDir(), []string{"BASE=1"}, &out, &errW)

			require.False(t, called, "no Studio round-trip for an unlinked project")
			require.Equal(t, []string{"BASE=1"}, env)
			require.Contains(t, errW.String(), "project not linked")
			require.Contains(t, errW.String(), "using local env only")
		})
	}
}
