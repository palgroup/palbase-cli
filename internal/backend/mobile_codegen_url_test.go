package backend

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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

// TestGenerateIOSAuto_EmbedsRemoteURL_NotLocalhost pins the core fix: the
// app config's runtime base URL must ALWAYS be the remote tenant host
// (https://<endpointRef>.<publicHost>), never the local `palbase serve`
// address — the app runs on a device/simulator that cannot reach the
// developer's localhost:4003. A local serve only provides a fresher SPEC.
//
// This test runs without a local serve listening on 4003, so codegen takes
// the deployed-spec fallback; either way the embedded URL must be remote.
func TestGenerateIOSAuto_EmbedsRemoteURL_NotLocalhost(t *testing.T) {
	localSpecServer(t) // local `palbase serve` is up → local-spec path
	cs := &codegenURLStudio{}
	r := cs.resolvers(t)

	outFile := filepath.Join(t.TempDir(), "PalbaseGenerated.swift")
	err := generateIOSAuto(
		context.Background(),
		r.Studio(),
		r.Endpoints(),
		"erkut1230qe6u", // bare ref
		"main",
		outFile,
		&strings.Builder{},
	)
	require.NoError(t, err)

	// The JSON sidecar carries the runtime config the SDK reads at launch.
	jsonBytes, err := os.ReadFile(strings.TrimSuffix(outFile, ".swift") + ".json")
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(jsonBytes, &cfg))

	url, _ := cfg["url"].(string)
	require.Equal(t, "https://erkut1230qe6um.dev.palbase.studio", url,
		"app config URL must be the remote tenant host")
	require.NotContains(t, url, "localhost",
		"app config URL must NEVER be localhost — the app cannot reach the dev machine")
	require.NotContains(t, url, "4003",
		"app config URL must NEVER be the local serve port")
}
