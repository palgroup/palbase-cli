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

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
)

// localTSSpecServer serves the shared TS OpenAPI fixture as the local serve
// stand-in (see localServeStub), so the `--env auto` probe takes the LOCAL path.
func localTSSpecServer(t *testing.T) {
	t.Helper()
	localServeStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tsFixtureOpenAPI))
	}))
}

// tsTypesStudio is the remote-side rig for the ts codegen tests: one httptest
// server playing the Studio tRPC (apikey.reveal), the deployed tenant host
// (/openapi.json → the shared TS fixture) and palauth's public
// /auth/oauth/providers (google enabled). Mirrors codegenURLStudio.
type tsTypesStudio struct{ srvURL string }

func (ts *tsTypesStudio) resolvers(t *testing.T) Resolvers {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/openapi.json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tsFixtureOpenAPI))
			return
		}
		if strings.Contains(r.URL.Path, "/auth/oauth/providers") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"providers":{"google":{"enabled":true,"client_id":"123-abc.apps.googleusercontent.com"}}}`))
			return
		}
		// tRPC apikey.reveal → remote endpoint_ref + publishable key.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{
			"endpointRef":    "erkut1230qe6um",
			"publishableKey": "pb_erkut1230qe6um_ctest",
		}}}})
	}))
	t.Cleanup(srv.Close)
	ts.srvURL = srv.URL
	c := studio.New(srv.URL, func(_ context.Context) (string, error) { return "tok", nil })
	return Resolvers{
		Studio:    func() *studio.Client { return c },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}
}

// TestTypesTS_DefaultOut_LocalPath runs the full `palbase types` command (no
// flags beyond the linked cwd) with a local `palbase serve` stub up: the
// default --lang is ts, the default --out is the FILE palbe.gen.ts, the
// default --env auto takes the LOCAL path (embedding the localhost URL, no
// studio round-trip, no oauth block), and the branch comes from the linked
// .palbase/config.json DefaultEnv.
func TestTypesTS_DefaultOut_LocalPath(t *testing.T) {
	t.Chdir(t.TempDir())
	localTSSpecServer(t)
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "abc123", DefaultEnv: "staging"}))

	// The local path must not touch Studio at all — point it at a dead host so
	// any accidental remote round-trip fails loudly.
	dead := studio.New("http://127.0.0.1:1", func(_ context.Context) (string, error) { return "tok", nil })
	r := Resolvers{
		Studio:    func() *studio.Client { return dead },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}

	cmd := newTypesCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	require.NoError(t, cmd.Execute())

	body, err := os.ReadFile("palbe.gen.ts")
	require.NoError(t, err, "default --out must be the FILE palbe.gen.ts in the cwd")
	content := string(body)
	require.Contains(t, content, "__registerNamespaces(")
	require.Contains(t, content, "url: 'http://localhost:4003'")
	require.Contains(t, content, "branch: 'staging'", "branch must come from .palbase/config.json DefaultEnv")
	require.NotContains(t, content, "oauth:", "local path must skip oauth discovery")
	require.Contains(t, out.String(), "✓ wrote palbe.gen.ts")
}

// TestTypesTS_AutoRemoteFallback pins the --env auto fallback: with no local
// serve on 4003, codegen resolves the branch target via apikey.reveal, fetches
// the deployed spec wake-aware, embeds the remote tenant URL + publishable
// key, and fetches the oauth providers best-effort.
func TestTypesTS_AutoRemoteFallback(t *testing.T) {
	localServeDown(t)
	rig := &tsTypesStudio{}
	r := rig.resolvers(t)
	restore := redirectHostTo(t, "erkut1230qe6um.dev.palbase.studio", rig.srvURL)
	defer restore()

	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	var w strings.Builder
	require.NoError(t, pullTSTypes(
		context.Background(), r.Studio(), r.Endpoints(),
		"erkut1230qe6u", "main", "auto", outFile, &w,
	))

	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	content := string(body)
	require.Contains(t, content, "url: 'https://erkut1230qe6um.dev.palbase.studio'")
	require.Contains(t, content, "apiKey: 'pb_erkut1230qe6um_ctest'")
	require.Contains(t, content, "google: { enabled: true, clientId: '123-abc.apps.googleusercontent.com' }",
		"remote path must fetch oauth providers")
	require.Contains(t, content, "__registerNamespaces(")
}

// TestTypesTS_LocalForced_ServeDown pins --env local strictness: when serve is
// down the command errors (no silent remote fallback).
func TestTypesTS_LocalForced_ServeDown(t *testing.T) {
	localServeDown(t)
	rig := &tsTypesStudio{}
	r := rig.resolvers(t)

	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	err := pullTSTypes(
		context.Background(), r.Studio(), r.Endpoints(),
		"erkut1230qe6u", "main", "local", outFile, &strings.Builder{},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "localhost:4003")
	_, statErr := os.Stat(outFile)
	require.True(t, os.IsNotExist(statErr), "--env local serve-down must not write output")
}

// TestTypesTS_SoftFlag pins the --soft contract for predev/prebuild hooks:
// with no local serve AND an unreachable Studio, --soft turns the failure into
// a `warning: codegen skipped (...)` line + exit 0; without --soft the same
// scenario stays a hard error.
func TestTypesTS_SoftFlag(t *testing.T) {
	localServeDown(t)

	dead := studio.New("http://127.0.0.1:1", func(_ context.Context) (string, error) { return "tok", nil })
	r := Resolvers{
		Studio:    func() *studio.Client { return dead },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}

	t.Run("soft swallows the failure", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cmd := newTypesCmd(r)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--ref", "abc123", "--soft"})
		require.NoError(t, cmd.Execute(), "--soft must exit 0 on any failure")
		require.Contains(t, out.String(), "warning: codegen skipped (")
		_, statErr := os.Stat("palbe.gen.ts")
		require.True(t, os.IsNotExist(statErr))
	})

	t.Run("without soft the failure is hard", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cmd := newTypesCmd(r)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs([]string{"--ref", "abc123"})
		require.Error(t, cmd.Execute())
	})
}

// deadStudioResolvers points the Studio client at a closed port so any remote
// round-trip fails loudly — used by tests whose code path must never leave
// the loopback.
func deadStudioResolvers() Resolvers {
	dead := studio.New("http://127.0.0.1:1", func(_ context.Context) (string, error) { return "tok", nil })
	return Resolvers{
		Studio:    func() *studio.Client { return dead },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}
}

// TestTypesTS_ZeroOps_KeepsExistingFile pins the zero-op overwrite guard: a
// live spec with 0 operations almost always means the backend's controller
// metadata extraction broke (the "zero endpoints collected" failure class),
// not that the app has no endpoints — it must NOT clobber a previously good
// palbe.gen.ts. Warn + exit 0 so a predev hook doesn't fail the build.
func TestTypesTS_ZeroOps_KeepsExistingFile(t *testing.T) {
	localSpecServer(t) // empty spec (paths:{}) on 4003 → 0 operations
	r := deadStudioResolvers()

	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	const previous = "// previously generated good client\n"
	require.NoError(t, os.WriteFile(outFile, []byte(previous), 0o644))

	var w strings.Builder
	require.NoError(t, pullTSTypes(
		context.Background(), r.Studio(), r.Endpoints(),
		"abc123", "main", "local", outFile, &w,
	), "zero-op keep must be a SUCCESS exit (predev hooks)")

	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.Equal(t, previous, string(body), "existing palbe.gen.ts must not be overwritten by an empty client")
	require.Contains(t, w.String(), "warning: live spec has 0 operations — keeping existing")
	require.Contains(t, w.String(), "fix your controllers and rerun")
	require.NotContains(t, w.String(), "✓ wrote")
}

// TestTypesTS_ZeroOps_NoExistingFile_WritesWithWarning pins the other half of
// the guard: with nothing to protect, the (empty) client IS written — a fresh
// project should still get a compilable module — but with a loud warning.
func TestTypesTS_ZeroOps_NoExistingFile_WritesWithWarning(t *testing.T) {
	localSpecServer(t) // empty spec (paths:{}) on 4003 → 0 operations
	r := deadStudioResolvers()

	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	var w strings.Builder
	require.NoError(t, pullTSTypes(
		context.Background(), r.Studio(), r.Endpoints(),
		"abc123", "main", "local", outFile, &w,
	))

	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.Contains(t, string(body), "__configure(")
	require.Contains(t, w.String(), "warning: live spec has 0 operations")
	require.Contains(t, w.String(), "✓ wrote")
}

// TestTypesTS_AutoFallback_LocalHTTPError pins the fallback wording split: a
// serve that ANSWERED with an HTTP error is a different situation from
// nothing listening — the auto-fallback print must say "responded with an
// error (HTTP <code>)" instead of the misleading "not found".
func TestTypesTS_AutoFallback_LocalHTTPError(t *testing.T) {
	localServeStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	rig := &tsTypesStudio{}
	r := rig.resolvers(t)
	restore := redirectHostTo(t, "erkut1230qe6um.dev.palbase.studio", rig.srvURL)
	defer restore()

	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	var w strings.Builder
	require.NoError(t, pullTSTypes(
		context.Background(), r.Studio(), r.Endpoints(),
		"erkut1230qe6u", "main", "auto", outFile, &w,
	))

	require.Contains(t, w.String(), "local serve responded with an error (HTTP 500)")
	require.NotContains(t, w.String(), "local spec not found")

	// And it still fell back to the deployed spec.
	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.Contains(t, string(body), "url: 'https://erkut1230qe6um.dev.palbase.studio'")
}
