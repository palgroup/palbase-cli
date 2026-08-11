package db

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// trpcOK writes a tRPC success envelope (mirrors secret_test.go).
func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// studioAgainst spins an httptest server and returns a *studio.Client backed
// by it, with a static test token (mirrors secret_test.go).
func studioAgainst(t *testing.T, h http.HandlerFunc) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	}, func(context.Context, string, string, string) (string, error) { return "test-proof", nil })
}

// innerInput decodes the inner {"json":{...}} of a tRPC POST body.
func innerInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
	return outer.JSON
}

// chdirTemp creates a temp project dir, writes db/schema.ts with the given
// source, and chdirs into it (restoring on cleanup). The CLI's `db` commands
// read db/schema.ts and write db/migrations/* relative to cwd, so the test
// drives the REAL file-write path against a real (temp) filesystem.
func chdirTemp(t *testing.T, schemaSrc string) string {
	t.Helper()
	dir := t.TempDir()
	if schemaSrc != "<missing>" {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "schema.ts"), []byte(schemaSrc), 0o644))
	}
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}
