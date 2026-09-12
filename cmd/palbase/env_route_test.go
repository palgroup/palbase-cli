package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/config"
)

// `--env` RESOLVES THROUGH A ROUTE THAT ANSWERS — measured through the
// PRODUCTION client, not through a stubbed hook.
//
// WHY THE SEAM MATTERS MORE THAN THE ASSERTION. The flag's predecessors,
// `--project` and `--environment`, were retired because they resolved through
// `GET /api/v2/projects`, a route the cloud did not serve: they parsed, they
// were documented, and they selected NOTHING. A gate that replaced
// `backend.EnvironmentsOf` with a hand-written closure would measure the
// closure and re-open exactly that hole — the hook could be unwired and the
// test would stay green.
//
// So the seam is the one a real run uses: `PALBASE_PLATFORM_URL`
// (internal/config/config.go) points the management client at an httptest
// server, `PALBASE_ACCESS_TOKEN` (internal/auth) satisfies the credential, and
// `wireEnvironmentLookup` — the production wiring — does the asking.
func TestTheEnvFlagResolvesThroughTheProductionClient(t *testing.T) {
	var hits atomic.Int32
	var seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		seenPath = r.URL.Path
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": "prd_a", "name": "todoapp",
			"environments": []map[string]any{
				{"ref": "j06bwtuum", "name": "main", "status": "Running"},
				{"ref": "mu0028", "name": "staging", "status": "Running"},
			},
		}})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_PLATFORM_URL", srv.URL)
	t.Setenv("PALBASE_AUTH_URL", srv.URL)
	t.Setenv("PALBASE_ACCESS_TOKEN", "person-token")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "palbase"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "palbase", "project.json"),
		[]byte(`{"project":"prd_a","name":"todoapp"}`), 0o644))

	// The root command's own wiring runs: PersistentPreRunE binds the hooks.
	root := newRootCmd()
	root.SetArgs([]string{"doctor", "--env", "staging"})
	root.SetOut(os.Stderr)
	root.SetErr(os.Stderr)
	_ = root.Execute()

	require.Positive(t, hits.Load(),
		"`--env staging` resolved without asking the cloud anything — the route is not wired")
	require.Equal(t, "/api/v2/projects", seenPath,
		"the flag resolves through a different route than the one that answers")
	require.Equal(t, "staging", backend.SelectedEnvFlag,
		"the flag value never reached the resolver")
}

// AND THE HOOKS ARE BOUND BY THE PRODUCTION WIRING, all four of them.
//
// A variable with a reader and no writer is a dead wire; `TenantHost` in
// particular would surface as "no tenant host configured" on every address the
// resolver tried to build.
func TestTheProductionWiringBindsEveryHook(t *testing.T) {
	t.Setenv("PALBASE_PLATFORM_URL", "https://example.invalid")
	backend.EnvironmentsOf, backend.ProductOfRef, backend.TenantHost = nil, nil, ""

	root := newRootCmd()
	root.SetArgs([]string{"--help"})
	root.SetOut(os.Stderr)
	root.SetErr(os.Stderr)
	_ = root.Execute()

	// `--help` short-circuits before PersistentPreRunE, so the wiring is called
	// directly — the point is that ONE function binds all four, not that help
	// happens to run it.
	r, err := config.Resolve()
	require.NoError(t, err)
	resolved = r
	wireEnvironmentLookup()

	require.NotNil(t, backend.EnvironmentsOf, "EnvironmentsOf has a reader and no writer")
	require.NotNil(t, backend.ProductOfRef, "ProductOfRef has a reader and no writer")
	require.NotEmpty(t, backend.TenantHost, "TenantHost has a reader and no writer")
	require.False(t, strings.Contains(backend.TenantHost, "/"),
		"TenantHost is a host suffix, not a URL")
}
