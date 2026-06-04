package backend

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
)

// localSpecServer stands up a stub `palbase serve` on localhost:4003 (the
// fixed local-codegen port) serving an empty OpenAPI spec, so generateIOSAuto
// takes the LOCAL-spec path. Returns a cleanup. Skips the test if 4003 is
// already bound (a real serve running) — we don't want to fight it.
func localSpecServer(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:4003")
	if err != nil {
		t.Skipf("localhost:4003 unavailable (%v) — skipping local-spec path test", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openapi":"3.1.0","info":{"title":"Palbase Backend","version":"1.0.0"},"paths":{},"components":{}}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// redirectHostTo installs a temporary RoundTripper on defaultHTTPClient that
// rewrites requests to `host` (the hard-coded remote tenant host) to `targetURL`
// (the httptest mock), leaving every other request untouched. Returns a restore
// func. Used to exercise the serve-down deployed-spec fetch without a real host.
func redirectHostTo(t *testing.T, host, targetURL string) func() {
	t.Helper()
	u, err := url.Parse(targetURL)
	require.NoError(t, err)
	prev := defaultHTTPClient.Transport
	base := prev
	if base == nil {
		base = http.DefaultTransport
	}
	defaultHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == host {
			req = req.Clone(req.Context())
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
		}
		return base.RoundTrip(req)
	})
	return func() { defaultHTTPClient.Transport = prev }
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// codegenURLStudio mocks apikey.reveal so lookupBackendTarget resolves a
// REMOTE backend target (endpointRef erkut1230qe6um) plus serves an empty
// OpenAPI spec at target.URL/openapi.json (the deployed-spec fallback path).
type codegenURLStudio struct{ srvURL string }

func (cs *codegenURLStudio) resolvers(t *testing.T) Resolvers {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deployed-project OpenAPI spec (used when no local `palbase serve`).
		if strings.HasSuffix(r.URL.Path, "/openapi.json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","info":{"title":"Palbase Backend","version":"1.0.0"},"paths":{},"components":{}}`))
			return
		}
		// palauth oauth providers — none configured.
		if strings.Contains(r.URL.Path, "/auth/oauth/providers") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// tRPC apikey.reveal → remote endpoint_ref + publishable key.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{
			"endpointRef":    "erkut1230qe6um",
			"publishableKey": "pb_erkut1230qe6um_ctest",
			"keys":           []any{},
		}}}})
	}))
	t.Cleanup(srv.Close)
	cs.srvURL = srv.URL
	c := studio.New(srv.URL, func(_ context.Context) (string, error) { return "tok", nil })
	return Resolvers{
		Studio:    func() *studio.Client { return c },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}
}

// TestGenerateIOSAuto_ServeUp_EmbedsLANIP pins Bug B's fix: when a local
// `palbase serve` is reachable on 4003, the embedded runtime URL points at the
// dev machine's LAN IP:4003 so both the simulator and a same-network physical
// device reach the local backend. The remote host is used ONLY when serve is down.
func TestGenerateIOSAuto_ServeUp_EmbedsLANIP(t *testing.T) {
	// writeSwiftGenerated appends ".palbase/config.json" to ./.gitignore
	// (relative to cwd). Run in a temp cwd so that stray file lands in the
	// sandbox, not in the repo (a leftover internal/backend/.gitignore trips
	// goreleaser's dirty-tree check at release time).
	t.Chdir(t.TempDir())

	localSpecServer(t) // local `palbase serve` is up → local path
	cs := &codegenURLStudio{}
	r := cs.resolvers(t)

	outFile := filepath.Join(t.TempDir(), "PalbaseGenerated.swift")
	require.NoError(t, generateIOSAuto(
		context.Background(), r.Studio(), r.Endpoints(),
		"erkut1230qe6u", "main", outFile, &strings.Builder{},
	))

	jsonBytes, err := os.ReadFile(strings.TrimSuffix(outFile, ".swift") + ".json")
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &cfg))

	url, _ := cfg["url"].(string)
	require.Contains(t, url, ":4003", "serve-up: URL must target the local serve port")
	require.NotContains(t, url, "dev.palbase.studio", "serve-up: URL must NOT be the remote tenant host")
	// The host is whatever outboundLANIP() resolved — LAN IP normally, or
	// "localhost" in an offline sandbox. Both are valid serve-up targets.
	expected := "http://" + outboundLANIP() + ":4003"
	require.Equal(t, expected, url)

	_, hasSource := cfg["source"]
	require.False(t, hasSource, "PalbaseGenerated.json must not carry a 'source' key")
}

// TestGenerateIOSAuto_ServeDown_EmbedsRemote pins the fallback: with no local
// serve on 4003, codegen embeds the remote tenant host.
func TestGenerateIOSAuto_ServeDown_EmbedsRemote(t *testing.T) {
	// Ensure 4003 is free so the local probe fails and we take the remote path.
	if ln, err := net.Listen("tcp", "127.0.0.1:4003"); err == nil {
		_ = ln.Close()
	} else {
		t.Skip("4003 is held by a real serve — cannot test the serve-down path")
	}
	t.Chdir(t.TempDir())
	cs := &codegenURLStudio{}
	r := cs.resolvers(t)

	// The serve-down path fetches the deployed spec from target.URL
	// (https://erkut1230qe6um.dev.palbase.studio/openapi.json). lookupBackendTarget
	// hard-codes that real host, so redirect just that host's requests to the
	// mock server for the duration of this test (target.URL itself is unchanged —
	// we still assert the embedded url is the real remote host below).
	restore := redirectHostTo(t, "erkut1230qe6um.dev.palbase.studio", cs.srvURL)
	defer restore()

	outFile := filepath.Join(t.TempDir(), "PalbaseGenerated.swift")
	require.NoError(t, generateIOSAuto(
		context.Background(), r.Studio(), r.Endpoints(),
		"erkut1230qe6u", "main", outFile, &strings.Builder{},
	))

	jsonBytes, err := os.ReadFile(strings.TrimSuffix(outFile, ".swift") + ".json")
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &cfg))

	url, _ := cfg["url"].(string)
	require.Equal(t, "https://erkut1230qe6um.dev.palbase.studio", url,
		"serve-down: URL must be the remote tenant host")
	require.NotContains(t, url, "4003")
}
