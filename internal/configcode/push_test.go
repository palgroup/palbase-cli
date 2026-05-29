package configcode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// flagsServer is a stateful mock of Studio's userFlags.system surface for
// push tests. It holds the project's flags in memory keyed by flag key,
// answers userFlags.system.list (GET) with the live set, and applies
// userFlags.system.put (POST) to that set while counting calls. This lets
// a single mock back the whole push flow — the conflict re-pull, the
// SET(s), and the post-apply refresh re-pull — and lets tests assert the
// exact number of SET mutations (the idempotency / conflict gates).
type flagsServer struct {
	mu       sync.Mutex
	flags    map[string]systemFlagRow
	putCalls int // number of userFlags.system.put mutations received
}

func newFlagsServer(seed ...systemFlagRow) *flagsServer {
	fs := &flagsServer{flags: map[string]systemFlagRow{}}
	for _, r := range seed {
		fs.flags[r.Key] = r
	}
	return fs
}

func (fs *flagsServer) client(t *testing.T) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fs.handle(t)))
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	})
}

func (fs *flagsServer) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/userFlags.system.list" && r.Method == http.MethodGet:
			fs.mu.Lock()
			rows := make([]systemFlagRow, 0, len(fs.flags))
			for _, row := range fs.flags {
				rows = append(rows, row)
			}
			fs.mu.Unlock()
			trpcOK(w, rows)

		case r.URL.Path == "/api/trpc/userFlags.system.put" && r.Method == http.MethodPost:
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			// tRPC POST envelope: { "json": <input> }.
			var env struct {
				JSON flagsPutInput `json:"json"`
			}
			require.NoError(t, json.Unmarshal(body, &env))
			in := env.JSON
			require.NotEmpty(t, in.Key, "put must carry a flag key")
			valRaw, err := json.Marshal(in.Value)
			require.NoError(t, err)
			fs.mu.Lock()
			fs.putCalls++
			fs.flags[in.Key] = systemFlagRow{
				Key:         in.Key,
				ValueType:   in.ValueType,
				Value:       json.RawMessage(valRaw),
				Description: in.Description,
			}
			fs.mu.Unlock()
			trpcOK(w, fs.flags[in.Key])

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}
}

func (fs *flagsServer) puts() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.putCalls
}

// writeFlagsConfig serializes rows to config/flags.toml under dir and
// returns the written bytes — the local file a push reads.
func writeFlagsConfig(t *testing.T, dir string, rows []systemFlagRow) []byte {
	t.Helper()
	body, err := serializeFlags(rows)
	require.NoError(t, err)
	configPath := filepath.Join(dir, ConfigDir)
	require.NoError(t, os.MkdirAll(configPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configPath, "flags.toml"), body, 0o644))
	return body
}

// seedState writes .palbase/state.json under dir recording hash as the
// flags module's last-pull baseline — the conflict-check reference.
func seedState(t *testing.T, dir, hash string) {
	t.Helper()
	st := newState()
	st.Modules["flags"] = ModuleState{Hash: hash}
	// Pre-seed an unrelated module so we can assert push doesn't clobber it.
	st.Modules["auth"] = ModuleState{Hash: "sha256:auth-untouched"}
	require.NoError(t, writeState(filepath.Join(dir, StateFile), st))
}

// loadStateForTest reads back .palbase/state.json.
func loadStateForTest(t *testing.T, dir string) *State {
	t.Helper()
	st, err := loadState(filepath.Join(dir, StateFile))
	require.NoError(t, err)
	return st
}

// serverHashFor pulls the mock's current flags and returns the hash a pull
// would have stored — used to seed a matching (non-conflicting) baseline.
func serverHashFor(t *testing.T, fs *flagsServer) string {
	t.Helper()
	body, err := flagsSerializer{}.Pull(context.Background(), "myproj", "", fs.client(t))
	require.NoError(t, err)
	return hashContent(body)
}

// --- Done-condition 1: round-trip a flags change --------------------

// TestFlagsPush_RoundTripsChange edits flags.toml (feature_x false→true),
// pushes, and asserts the server reflects it via exactly one SET. This is
// the core round-trip done-condition.
func TestFlagsPush_RoundTripsChange(t *testing.T) {
	fs := newFlagsServer(row("feature_x", "bool", "false", "", ""))
	c := fs.client(t)

	dir := t.TempDir()
	// Baseline state.json matches current server (feature_x=false) — no conflict.
	seedState(t, dir, serverHashFor(t, fs))
	// Local file flips feature_x to true.
	writeFlagsConfig(t, dir, []systemFlagRow{row("feature_x", "bool", "true", "", "")})

	res, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.NoError(t, err)
	require.Equal(t, 1, res.Set, "one flag changed → one SET")
	require.Equal(t, 1, fs.puts(), "exactly one userFlags.system.put")

	// Server now reflects the pushed value.
	fs.mu.Lock()
	got := fs.flags["feature_x"]
	fs.mu.Unlock()
	require.JSONEq(t, "true", string(got.Value))

	// state.json refreshed to the new server hash; auth untouched.
	st := loadStateForTest(t, dir)
	require.Equal(t, serverHashFor(t, fs), st.Modules["flags"].Hash)
	require.Equal(t, "sha256:auth-untouched", st.Modules["auth"].Hash)
}

// TestFlagsPush_CreatesNewFlag: a flag present locally but absent
// server-side is created (one SET).
func TestFlagsPush_CreatesNewFlag(t *testing.T) {
	fs := newFlagsServer() // empty server
	c := fs.client(t)

	dir := t.TempDir()
	seedState(t, dir, serverHashFor(t, fs)) // baseline = empty server
	writeFlagsConfig(t, dir, []systemFlagRow{row("brand_new", "string", `"hi"`, "", "")})

	res, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.NoError(t, err)
	require.Equal(t, 1, res.Set)
	require.Equal(t, 1, fs.puts())
}

// --- Done-condition 2: stale-state conflict → rejected, no SET ------

// TestFlagsPush_ConflictRejected: the server changed since the last pull
// (state.json hash ≠ current server hash), so push must reject with
// ErrStateConflict and make ZERO SET calls.
func TestFlagsPush_ConflictRejected(t *testing.T) {
	fs := newFlagsServer(row("feature_x", "bool", "false", "", ""))
	c := fs.client(t)

	dir := t.TempDir()
	// Stale baseline: a hash that does NOT match the current server state
	// (simulates a dashboard edit landing after the last pull).
	seedState(t, dir, "sha256:stale-from-an-older-pull")
	writeFlagsConfig(t, dir, []systemFlagRow{row("feature_x", "bool", "true", "", "")})

	_, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.ErrorIs(t, err, ErrStateConflict)
	require.Equal(t, 0, fs.puts(), "conflict must abort before any SET")
}

// TestFlagsPush_NoBaselineRejected: a project that never pulled this
// module (no flags entry in state.json) has no baseline to compare, so a
// non-empty server must be treated as a conflict — no SET.
func TestFlagsPush_NoBaselineRejected(t *testing.T) {
	fs := newFlagsServer(row("feature_x", "bool", "false", "", ""))
	c := fs.client(t)

	dir := t.TempDir()
	// state.json with NO flags entry.
	st := newState()
	require.NoError(t, writeState(filepath.Join(dir, StateFile), st))
	writeFlagsConfig(t, dir, []systemFlagRow{row("feature_x", "bool", "true", "", "")})

	_, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.ErrorIs(t, err, ErrStateConflict)
	require.Equal(t, 0, fs.puts())
}

// --- Done-condition 3: idempotent → same push twice = no-op ---------

// TestFlagsPush_Idempotent: pushing a file that already matches the server
// makes ZERO SET calls (asserted directly, not just "no error").
func TestFlagsPush_Idempotent(t *testing.T) {
	fs := newFlagsServer(
		row("feature_x", "bool", "true", "", "Checkout"),
		row("max_items", "number", "42", "", ""),
	)
	c := fs.client(t)

	dir := t.TempDir()
	seedState(t, dir, serverHashFor(t, fs))
	// Local file = exactly what the server already holds.
	writeFlagsConfig(t, dir, []systemFlagRow{
		row("feature_x", "bool", "true", "", "Checkout"),
		row("max_items", "number", "42", "", ""),
	})

	res, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.NoError(t, err)
	require.Equal(t, 0, res.Set, "no changes → no SET")
	require.Equal(t, 0, fs.puts(), "idempotent push must issue zero mutations")
}

// TestFlagsPush_IdempotentAfterRoundTrip: push a change, then push the
// SAME file again — the second push is a no-op (the round-trip refreshed
// the baseline so there's no conflict, and the diff is empty).
func TestFlagsPush_IdempotentAfterRoundTrip(t *testing.T) {
	fs := newFlagsServer(row("feature_x", "bool", "false", "", ""))
	c := fs.client(t)

	dir := t.TempDir()
	seedState(t, dir, serverHashFor(t, fs))
	writeFlagsConfig(t, dir, []systemFlagRow{row("feature_x", "bool", "true", "", "")})

	_, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.NoError(t, err)
	require.Equal(t, 1, fs.puts())

	// Second push, unchanged file.
	res, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.NoError(t, err)
	require.Equal(t, 0, res.Set)
	require.Equal(t, 1, fs.puts(), "second push must add no mutations")
}

// --- Upsert-only orphan handling ------------------------------------

// TestFlagsPush_OrphansLeftUntouched: a flag on the server but absent from
// the local file is NOT deleted — reported as an orphan, no DELETE call
// (the mock would fail on an unexpected DELETE).
func TestFlagsPush_OrphansLeftUntouched(t *testing.T) {
	fs := newFlagsServer(
		row("keep_me", "bool", "true", "", ""),
		row("server_only", "string", `"x"`, "", ""),
	)
	c := fs.client(t)

	dir := t.TempDir()
	seedState(t, dir, serverHashFor(t, fs))
	// Local file omits server_only; keep_me unchanged.
	writeFlagsConfig(t, dir, []systemFlagRow{row("keep_me", "bool", "true", "", "")})

	res, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.NoError(t, err)
	require.Equal(t, 0, res.Set, "keep_me unchanged → no SET")
	require.Equal(t, []string{"server_only"}, res.Orphans)
	require.Equal(t, 0, fs.puts())

	// server_only still on the server.
	fs.mu.Lock()
	_, stillThere := fs.flags["server_only"]
	fs.mu.Unlock()
	require.True(t, stillThere, "orphan must NOT be deleted")
}

// TestFlagsPush_VariantsIgnored: a local flag declaring variants reports
// the flag in Ignored (the SET API can't push variants), so the omission
// is visible instead of silent. The non-variant fields still upsert.
func TestFlagsPush_VariantsIgnored(t *testing.T) {
	fs := newFlagsServer() // empty server
	c := fs.client(t)

	dir := t.TempDir()
	seedState(t, dir, serverHashFor(t, fs))
	writeFlagsConfig(t, dir, []systemFlagRow{
		row("ab_flag", "bool", "false", `[{"value":false,"weight":50},{"value":true,"weight":50}]`, ""),
	})

	res, err := Push(context.Background(), dir, "myproj", "", "flags", c)
	require.NoError(t, err)
	require.Equal(t, 1, res.Set, "the flag itself is still created")
	require.Equal(t, []string{"ab_flag"}, res.Ignored, "variants must be flagged as not pushed")
}

// --- Module gating --------------------------------------------------

// TestPush_UnsupportedModule: a module with a pull serializer but no push
// support returns ErrPushNotImplemented (Faz 3 sentinel), no work done.
func TestPush_UnsupportedModule(t *testing.T) {
	fs := newFlagsServer()
	c := fs.client(t)
	dir := t.TempDir()
	_, err := Push(context.Background(), dir, "myproj", "", "auth", c)
	require.ErrorIs(t, err, ErrPushNotImplemented)
}

// TestPush_UnknownModule: an unregistered module name is a plain error
// (not the not-implemented sentinel).
func TestPush_UnknownModule(t *testing.T) {
	fs := newFlagsServer()
	c := fs.client(t)
	dir := t.TempDir()
	_, err := Push(context.Background(), dir, "myproj", "", "does-not-exist", c)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPushNotImplemented)
}

// TestFlagsImplementsModulePusher is a compile-time + runtime guard that
// the flags serializer satisfies ModulePusher and others do not.
func TestFlagsImplementsModulePusher(t *testing.T) {
	_, ok := pusherFor("flags")
	require.True(t, ok, "flags must implement ModulePusher")
	_, ok = pusherFor("auth")
	require.False(t, ok, "auth must NOT implement ModulePusher (Faz 3)")
}
