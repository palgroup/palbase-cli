package configcode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// studioAgainst spins an httptest server returning a tRPC success
// envelope for any query, decoded from the per-path handler. Mirrors the
// secret package's test helper.
func studioAgainst(t *testing.T, h http.HandlerFunc) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	})
}

func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// pathAwareStudio returns a Studio client whose mock answers EVERY
// registered serializer's tRPC path with a correctly-shaped response.
// Pull() iterates all registered serializers (auth, documents, flags,
// storage, …), each expecting a different response shape; a single-shape
// mock would 404/decode-fail on the first non-flags call. flagsRows lets
// a caller seed the flags payload (the module the Pull tests assert on);
// other modules get minimal valid empty bodies.
func pathAwareStudio(t *testing.T, flagsRows []systemFlagRow) *studio.Client {
	t.Helper()
	return studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/userFlags.system.list":
			trpcOK(w, flagsRows)
		case "/api/trpc/auth.providers.list":
			trpcOK(w, map[string]any{"configureAvailable": false, "providers": []any{}})
		case "/api/trpc/storage.buckets.list":
			trpcOK(w, map[string]any{"buckets": []any{}})
		case "/api/trpc/documents.rules.list":
			trpcOK(w, map[string]any{"rules": []any{}})
		case "/api/trpc/notifications.providers.list":
			trpcOK(w, []any{})
		default:
			// A newly-registered serializer hit an unmocked path — fail
			// loudly so the mock stays in sync with the registry.
			t.Errorf("unmocked tRPC path: %s", r.URL.Path)
			http.Error(w, "unmocked path", http.StatusNotFound)
		}
	})
}

// TestSerializers_SortedAndFlagsRegistered confirms the registry exposes
// the flags serializer and Serializers() returns Name-sorted order.
func TestSerializers_SortedAndFlagsRegistered(t *testing.T) {
	sers := Serializers()
	require.NotEmpty(t, sers)

	var hasFlags bool
	for i, s := range sers {
		if i > 0 {
			require.LessOrEqual(t, sers[i-1].Name(), s.Name(), "serializers must be Name-sorted")
		}
		if s.Name() == "flags" {
			hasFlags = true
			require.Equal(t, "flags.toml", s.Filename())
		}
	}
	require.True(t, hasFlags, "flags serializer must be registered")
}

// TestRegister_DuplicatePanics asserts a duplicate Name() is rejected at
// registration so a parallel subagent's copy-paste slip surfaces loudly.
func TestRegister_DuplicatePanics(t *testing.T) {
	require.Panics(t, func() { Register(flagsSerializer{}) })
}

// TestFlagsSerializer_Pull drives the flags serializer end-to-end against
// a mock tRPC server (no live API), asserting it queries the right
// procedure and returns expected TOML.
func TestFlagsSerializer_Pull(t *testing.T) {
	var calledPath string
	rows := []systemFlagRow{row("feature_x", "bool", "true", "", "")}
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		calledPath = r.URL.Path
		require.Equal(t, http.MethodGet, r.Method)
		trpcOK(w, rows)
	})

	body, err := flagsSerializer{}.Pull(context.Background(), "myproj", c)
	require.NoError(t, err)
	// Must match the root router's mount key (router.ts:27 `userFlags:`),
	// which is camelCase — NOT the hyphenated module name.
	require.Equal(t, "/api/trpc/userFlags.system.list", calledPath)
	require.Contains(t, string(body), "feature_x")
	require.Contains(t, string(body), `type = "bool"`)
}

// TestPull_WritesConfigAndState runs the full Pull against a temp dir +
// mock server: every registered module writes a file, state.json mirrors
// each module's hash. The stub modules are NOT registered (only flags is)
// so this exercises the live path with the reference impl.
func TestPull_WritesConfigAndState(t *testing.T) {
	rows := []systemFlagRow{row("alpha", "bool", "true", "", "")}
	c := pathAwareStudio(t, rows)

	dir := t.TempDir()
	results, err := Pull(context.Background(), dir, "myproj", c)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// config/flags.toml exists and parses.
	flagsPath := filepath.Join(dir, ConfigDir, "flags.toml")
	raw, err := os.ReadFile(flagsPath)
	require.NoError(t, err)
	var doc flagsDoc
	require.NoError(t, toml.Unmarshal(raw, &doc))
	require.Contains(t, doc.Flags, "alpha")

	// state.json exists, mirrors the flags hash + placeholder version.
	statePath := filepath.Join(dir, StateFile)
	stateRaw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var st State
	require.NoError(t, json.Unmarshal(stateRaw, &st))
	require.EqualValues(t, 0, st.StateVersion, "Faz 1 placeholder")
	flagsState, ok := st.Modules["flags"]
	require.True(t, ok)
	require.Equal(t, hashContent(raw), flagsState.Hash, "state hash must match file content")
}

// TestPull_Deterministic asserts two pulls of identical server state
// produce byte-identical files (config + state) — the diff-stability
// contract.
func TestPull_Deterministic(t *testing.T) {
	rows := []systemFlagRow{
		row("b", "bool", "true", "", ""),
		row("a", "string", `"x"`, "", ""),
	}
	c := pathAwareStudio(t, rows)

	read := func(dir string) (string, string) {
		f, err := os.ReadFile(filepath.Join(dir, ConfigDir, "flags.toml"))
		require.NoError(t, err)
		s, err := os.ReadFile(filepath.Join(dir, StateFile))
		require.NoError(t, err)
		return string(f), string(s)
	}

	d1 := t.TempDir()
	_, err := Pull(context.Background(), d1, "myproj", c)
	require.NoError(t, err)
	d2 := t.TempDir()
	_, err = Pull(context.Background(), d2, "myproj", c)
	require.NoError(t, err)

	f1, s1 := read(d1)
	f2, s2 := read(d2)
	require.Equal(t, f1, f2, "flags.toml must be deterministic")
	require.Equal(t, s1, s2, "state.json must be deterministic")
}

// TestPull_PartialFailureIsNonFatal: one module's tRPC 500 (e.g. tenant
// hasn't provisioned that module's tables) must NOT abort the whole pull.
// The other modules still write; the failing module gets an Err result.
func TestPull_PartialFailureIsNonFatal(t *testing.T) {
	rows := []systemFlagRow{row("alpha", "bool", "true", "", "")}
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/userFlags.system.list":
			trpcOK(w, rows)
		case "/api/trpc/auth.providers.list":
			trpcOK(w, map[string]any{"configureAvailable": false, "providers": []any{}})
		case "/api/trpc/documents.rules.list":
			trpcOK(w, map[string]any{"rules": []any{}})
		case "/api/trpc/notifications.providers.list":
			trpcOK(w, []any{})
		case "/api/trpc/storage.buckets.list":
			// Simulate the live centauri failure: buckets table absent.
			http.Error(w, `{"error":{"json":{"message":"relation \"buckets\" does not exist"}}}`, http.StatusInternalServerError)
		default:
			t.Errorf("unmocked path: %s", r.URL.Path)
		}
	})

	dir := t.TempDir()
	results, err := Pull(context.Background(), dir, "myproj", c)
	require.NoError(t, err, "partial failure must not abort the whole pull")

	// flags/auth/documents wrote files; storage is an Err result, no file.
	require.FileExists(t, filepath.Join(dir, ConfigDir, "flags.toml"))
	require.NoFileExists(t, filepath.Join(dir, ConfigDir, "storage.toml"))

	var storageErr error
	for _, r := range results {
		if r.Module == "storage" {
			storageErr = r.Err
		}
	}
	require.Error(t, storageErr, "storage must report its failure via Err")

	// state.json still written, omitting the failed module.
	statePath := filepath.Join(dir, StateFile)
	require.FileExists(t, statePath)
	var st State
	stateRaw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(stateRaw, &st))
	require.Contains(t, st.Modules, "flags")
	require.NotContains(t, st.Modules, "storage")
}

// TestState_Marshal asserts the state mirror serializes with a trailing
// newline + sorted keys (stable).
func TestState_Marshal(t *testing.T) {
	s := newState()
	s.Modules["flags"] = ModuleState{Hash: "sha256:abc"}
	s.Modules["auth"] = ModuleState{Hash: "sha256:def"}
	b, err := s.marshal()
	require.NoError(t, err)
	require.True(t, b[len(b)-1] == '\n')
	// encoding/json sorts map keys: "auth" before "flags".
	require.Less(t, indexOf(string(b), "auth"), indexOf(string(b), "flags"))
	require.Equal(t, []string{"auth", "flags"}, s.ModuleNames())
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
