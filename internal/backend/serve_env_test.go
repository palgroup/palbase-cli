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

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/studio"
)

// httptestServer starts a server and returns its URL.
func httptestServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func remoteEnvStudio(t *testing.T, status int, rows []map[string]string, gotInput *map[string]any) *studio.Client {
	t.Helper()
	url := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if gotInput != nil {
			*gotInput = decodeTRPCInput(t, r)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"json":{"message":"forbidden"}}}`))
			return
		}
		trpcOK(w, rows)
	})
	return studio.New(url, func(context.Context) (string, error) { return "tok", nil })
}

// appendRemoteEnv is serve's automatic env fetch. It asks for ONE thing — the
// ENVIRONMENT ref — and delivers the vars via a 0600 FILE, never via node.Env:
// dev-server treats an already-set process.env as highest priority, so an env
// var would BEAT .env.local and invert the intended precedence
// (shell env > .env.local > remote).
func TestAppendRemoteEnv_SendsOnlyTheRefAndWritesA0600File(t *testing.T) {
	var gotInput map[string]any
	sc := remoteEnvStudio(t, http.StatusOK, []map[string]string{
		{"key": "STRIPE_KEY", "value": "sk_live"},
		{"key": "FEATURE", "value": "on"},
	}, &gotInput)

	dir := t.TempDir()
	var out, errW bytes.Buffer
	env := appendRemoteEnv(context.Background(), sc, "app1prod", dir, []string{"BASE=1"}, &out, &errW)

	require.Equal(t, map[string]any{"ref": "app1prod"}, gotInput)
	require.NotContains(t, gotInput, "branch")

	file := filepath.Join(dir, "remote-env.json")
	require.Equal(t, []string{"BASE=1", "PALBASE_REMOTE_ENV_FILE=" + file}, env)
	for _, e := range env {
		require.NotContains(t, e, "sk_live", "a secret must never ride the child's env block")
	}

	info, err := os.Stat(file)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, map[string]string{"STRIPE_KEY": "sk_live", "FEATURE": "on"}, got)
	require.Contains(t, out.String(), "environment app1prod")
	require.Empty(t, errW.String())
}

// A fetch failure WARNS and continues: serve must still boot for local-only work.
func TestAppendRemoteEnv_FailureWarnsAndContinues(t *testing.T) {
	sc := remoteEnvStudio(t, http.StatusForbidden, nil, nil)
	var out, errW bytes.Buffer
	base := []string{"BASE=1"}
	env := appendRemoteEnv(context.Background(), sc, "app1prod", t.TempDir(), base, &out, &errW)
	require.Equal(t, base, env)
	require.Contains(t, errW.String(), "could not fetch remote env vars")
}

// No selection → no call at all, and a clear warning.
func TestAppendRemoteEnv_NoEnvironmentWarnsWithoutCalling(t *testing.T) {
	called := false
	url := httptestServer(t, func(http.ResponseWriter, *http.Request) { called = true })
	sc := studio.New(url, func(context.Context) (string, error) { return "tok", nil })

	var out, errW bytes.Buffer
	env := appendRemoteEnv(context.Background(), sc, "", t.TempDir(), []string{"BASE=1"}, &out, &errW)
	require.Equal(t, []string{"BASE=1"}, env)
	require.False(t, called)
	require.Contains(t, errW.String(), "no environment selected")
}

// The dev-server's identity contract (spec §7.4 / UAT SDK-007): the local
// runtime stamps job/webhook/worker metadata with projectId + environmentId. An
// unselected directory degrades to "local" instead of building a nonsense URL
// like https://.dev.palbase.studio.
func TestDevIdentity_DegradesToLocal(t *testing.T) {
	require.Equal(t, "local", devIdentity(""))
	require.Equal(t, "proj_1", devIdentity("proj_1"))
	require.Equal(t, "local", devEnvironmentRef(""))
	require.Equal(t, "app1prod", devEnvironmentRef("app1prod"))
}

// The embedded dev-server must read the CANONICAL identity env vars and must not
// resurrect the branch identity. This is a source-level lock on the JS the CLI
// ships: PALBASE_BRANCH used to become `environmentId`, which is exactly the
// "no Palbase branch identity is emitted" rule (SDK-007).
func TestDevServerJS_UsesProjectIdAndEnvironmentId_NotBranch(t *testing.T) {
	raw, err := devServerFS.ReadFile("devjs/dev-server.js")
	require.NoError(t, err)
	js := string(raw)

	require.Contains(t, js, "process.env.PALBASE_PROJECT_ID")
	require.Contains(t, js, "process.env.PALBASE_ENVIRONMENT_ID")
	require.Contains(t, js, "process.env.PALBASE_ENVIRONMENT_REF")

	require.NotContains(t, js, "PALBASE_BRANCH",
		"the dev server must not read a branch — environmentId names the runtime")
	require.NotContains(t, js, "PALBASE_PROJECT_REF",
		"the old ref env named an ENVIRONMENT while calling itself a project")

	// workerMeta must stamp the canonical pair.
	require.True(t, strings.Contains(js, "projectId: PROJECT_ID"), "workerMeta must carry the PROJECT id")
	require.True(t, strings.Contains(js, "environmentId: ENVIRONMENT_ID"), "workerMeta must carry the ENVIRONMENT id")
}
