package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
	return studio.New(
		url,
		func(context.Context) (string, error) { return "tok", nil },
		func(context.Context, string, string, string) (string, error) { return "proof", nil },
	)
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
	sc := studio.New(
		url,
		func(context.Context) (string, error) { return "tok", nil },
		func(context.Context, string, string, string) (string, error) { return "proof", nil },
	)

	var out, errW bytes.Buffer
	env := appendRemoteEnv(context.Background(), sc, "", t.TempDir(), []string{"BASE=1"}, &out, &errW)
	require.Equal(t, []string{"BASE=1"}, env)
	require.False(t, called)
	require.Contains(t, errW.String(), "no environment selected")
}

// The embedded dev-server must read only the CANONICAL identity env vars. This
// is a source-level lock on the JS the CLI ships and enforces the rule that no
// retired branch identity is emitted (SDK-007).
func TestDevServerJS_UsesOnlyCanonicalIdentityEnvVars(t *testing.T) {
	raw, err := devServerFS.ReadFile("devjs/dev-server.js")
	require.NoError(t, err)
	js := string(raw)

	require.Contains(t, js, "process.env.PALBASE_ENVIRONMENT_ID")
	require.Contains(t, js, "process.env.PALBASE_ENVIRONMENT_REF")

	// Exact allowlisting keeps retired identity aliases from reappearing without
	// preserving those aliases in the CLI's own source tree.
	identityEnvPattern := regexp.MustCompile(`process\.env\.(PALBASE_(?:PROJECT|ENVIRONMENT|BRANCH)[A-Z_]*)`)
	identityEnvVars := make(map[string]struct{})
	for _, match := range identityEnvPattern.FindAllStringSubmatch(js, -1) {
		identityEnvVars[match[1]] = struct{}{}
	}
	require.Equal(t, map[string]struct{}{
		"PALBASE_ENVIRONMENT_ID":  {},
		"PALBASE_ENVIRONMENT_REF": {},
	}, identityEnvVars)

	// workerMeta must stamp only the runtime Environment identity.
	require.NotContains(t, js, "projectId:")
	require.True(t, strings.Contains(js, "environmentId: ENVIRONMENT_ID"), "workerMeta must carry the ENVIRONMENT id")
}
