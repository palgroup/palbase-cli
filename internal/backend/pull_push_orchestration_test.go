package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// minimalTarGz returns a base64 tar.gz holding a single file, so
// backend.pull's extractTarGzReplace step has something valid to unpack.
func minimalTarGz(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("export default {}\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "endpoints/hello.js", Mode: 0o644, Size: int64(len(body))}))
	_, err := tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// recordingStudio is a path-recording mock of the Studio tRPC surface the
// unified pull/push touch. It records the ORDER of tRPC paths so the
// orchestration tests can assert code→env→config (pull) and
// deploy→config (push) sequencing.
type recordingStudio struct {
	mu         sync.Mutex
	calls      []string
	archiveB64 string // backend.pull archive
	failConfig bool   // make the config tRPC list 500 (partial-fail-non-fatal)
}

func (rs *recordingStudio) client(t *testing.T) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		rs.mu.Lock()
		rs.calls = append(rs.calls, path)
		rs.mu.Unlock()

		ok := func(data any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": data}}})
		}

		switch {
		// NOTE: there is intentionally NO backend.status / backend.enable
		// handler — the CLI never enables (backend is the default). A call
		// to either would hit the default branch and fail the test.
		case path == "/api/trpc/backend.pull":
			ok(map[string]any{"version": "v1", "archive": rs.archiveB64, "size": 42})
		case path == "/api/trpc/env.pull":
			ok([]map[string]any{}) // no env vars
		case path == "/api/trpc/notifications.providers.list",
			path == "/api/trpc/userFlags.system.list",
			path == "/api/trpc/auth.providers.list",
			path == "/api/trpc/storage.buckets.list",
			path == "/api/trpc/documents.rules.list":
			if rs.failConfig {
				http.Error(w, `{"error":{"json":{"message":"module not provisioned"}}}`, http.StatusInternalServerError)
				return
			}
			// Shape doesn't matter much — empty is valid for each serializer.
			switch path {
			case "/api/trpc/auth.providers.list":
				ok(map[string]any{"configureAvailable": false, "providers": []any{}})
			case "/api/trpc/storage.buckets.list":
				ok(map[string]any{"buckets": []any{}})
			case "/api/trpc/documents.rules.list":
				ok(map[string]any{"rules": []any{}})
			default:
				ok([]any{})
			}
		default:
			t.Errorf("unexpected tRPC path: %s", path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) { return "tok", nil })
}

func (rs *recordingStudio) resolvers(t *testing.T) Resolvers {
	c := rs.client(t)
	return Resolvers{Studio: func() *studio.Client { return c }}
}

// chdirLinked makes a temp dir the cwd, seeds .palbase/config.json so
// resolveOrLinkRef finds the ref without prompting, and restores cwd.
func chdirLinked(t *testing.T, ref string) {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })
	require.NoError(t, os.MkdirAll(".palbase", 0o755))
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: ref, DefaultEnv: "main"}))
}

func (rs *recordingStudio) sequence() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]string, len(rs.calls))
	copy(out, rs.calls)
	return out
}

// indexOf returns the first index of path in calls, or -1.
func indexOf(calls []string, path string) int {
	for i, c := range calls {
		if c == path {
			return i
		}
	}
	return -1
}

// TestPull_Orchestration_CodeEnvConfig verifies `palbase pull` runs the
// three steps in order: code (backend.pull) → env (env.pull) → config-as-
// code (the *.list procedures). The CLI never enables/status-checks the
// backend (backend is the default) — those calls must NOT appear.
func TestPull_Orchestration_CodeEnvConfig(t *testing.T) {
	chdirLinked(t, "abc123")
	rs := &recordingStudio{archiveB64: minimalTarGz(t)}
	cmd := newPullCmd(rs.resolvers(t))
	cmd.SetArgs([]string{})
	require.NoError(t, cmd.Execute())

	calls := rs.sequence()
	// No enable/status — backend is the default, the CLI never gates on it.
	require.NotContains(t, calls, "/api/trpc/backend.enable")
	require.NotContains(t, calls, "/api/trpc/backend.status")
	iCode := indexOf(calls, "/api/trpc/backend.pull")
	iEnv := indexOf(calls, "/api/trpc/env.pull")
	iConfig := indexOf(calls, "/api/trpc/userFlags.system.list") // any config serializer
	require.GreaterOrEqual(t, iCode, 0, "code pull must run")
	require.GreaterOrEqual(t, iEnv, 0, "env pull must run")
	require.GreaterOrEqual(t, iConfig, 0, "config-as-code pull must run")
	// Ordering: code → env → config.
	require.Less(t, iCode, iEnv, "code before env")
	require.Less(t, iEnv, iConfig, "env before config")
}

// TestPull_Orchestration_ConfigFailureNonFatal: a config-as-code failure
// must NOT fail the overall pull (code+env already succeeded).
func TestPull_Orchestration_ConfigFailureNonFatal(t *testing.T) {
	chdirLinked(t, "abc123")
	rs := &recordingStudio{archiveB64: minimalTarGz(t), failConfig: true}
	cmd := newPullCmd(rs.resolvers(t))
	cmd.SetArgs([]string{})
	require.NoError(t, cmd.Execute(), "config failure must be non-fatal for pull")
}

// TestPush_Orchestration_DeployThenConfig verifies `palbase push` deploys
// code (backend.deploy) BEFORE pushing config (the PushAll list/SET
// procedures), and never enables/status-checks the backend. A prior config
// pull seeds .palbase/state.json so PushAll's conflict gate has a clean
// baseline.
func TestPush_Orchestration_DeployThenConfig(t *testing.T) {
	chdirLinked(t, "abc123")
	rs := &recordingStudioPush{}
	r := rs.resolvers(t)

	// Seed config/*.toml + a matching .palbase/state.json baseline by
	// running the config pull against the same mock first — a realistic
	// pull→push, so PushAll's pre-validate sees no drift.
	require.NoError(t, runConfigPull(context.Background(), mustGetwd(t), "abc123", r.Studio(), &bytes.Buffer{}))
	rs.reset() // drop the pull's recorded calls; we only assert the push order

	cmd := newPushCmd(r)
	cmd.SetArgs([]string{"--no-types"}) // skip the types HTTP fetch
	require.NoError(t, cmd.Execute())

	calls := rs.sequence()
	require.NotContains(t, calls, "/api/trpc/backend.enable")
	require.NotContains(t, calls, "/api/trpc/backend.status")
	iDeploy := indexOf(calls, "/api/trpc/backend.deploy")
	iConfig := indexOf(calls, "/api/trpc/userFlags.system.list")
	require.GreaterOrEqual(t, iDeploy, 0, "deploy must run")
	require.GreaterOrEqual(t, iConfig, 0, "config push (re-pull/validate) must run")
	require.Less(t, iDeploy, iConfig, "deploy (code) before config push")
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return wd
}

// recordingStudioPush answers the deploy + config-push (PushAll) surface
// and records tRPC call order. PushAll's pre-validate re-pulls + hash-
// compares against state.json, so the test seeds a baseline via an initial
// config pull (see the test). reset() clears the recorded calls so the
// post-seed push sequence can be asserted in isolation.
type recordingStudioPush struct {
	mu    sync.Mutex
	calls []string
}

func (rs *recordingStudioPush) sequence() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]string, len(rs.calls))
	copy(out, rs.calls)
	return out
}

func (rs *recordingStudioPush) reset() {
	rs.mu.Lock()
	rs.calls = nil
	rs.mu.Unlock()
}

func (rs *recordingStudioPush) resolvers(t *testing.T) Resolvers {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		rs.mu.Lock()
		rs.calls = append(rs.calls, path)
		rs.mu.Unlock()
		ok := func(data any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": data}}})
		}
		switch path {
		// No backend.status/enable — backend is the default, push never gates.
		case "/api/trpc/backend.deploy":
			ok(map[string]any{"version": "v2", "files": 1})
		// config-as-code pull (seed) + PushAll (validate). Empty server →
		// header-only TOML, zero SET, no conflict against the seeded baseline.
		case "/api/trpc/userFlags.system.list",
			"/api/trpc/notifications.providers.list":
			ok([]any{})
		case "/api/trpc/auth.providers.list":
			ok(map[string]any{"configureAvailable": false, "providers": []any{}})
		case "/api/trpc/storage.buckets.list":
			ok(map[string]any{"buckets": []any{}})
		case "/api/trpc/documents.rules.list":
			ok(map[string]any{"rules": []any{}})
		default:
			t.Errorf("unexpected push tRPC path: %s", path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := studio.New(srv.URL, func(_ context.Context) (string, error) { return "tok", nil })
	return Resolvers{Studio: func() *studio.Client { return c }}
}
