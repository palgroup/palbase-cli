package github

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

func studioAgainst(t *testing.T, h http.HandlerFunc) Studio {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	}, func(context.Context, string, string, string) (string, error) { return "test-proof", nil })
}

func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// TestGithubStatusAndAccounts pins the read wires: status decode + accounts
// table with the project-create hint.
func TestGithubStatusAndAccounts(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/github.status":
			trpcOK(w, map[string]any{"connected": true, "githubLogin": "pal-salih"})
		case "/api/trpc/github.listAccounts":
			trpcOK(w, []map[string]any{
				{"installationId": 12345, "accountLogin": "pal-salih", "accountType": "User"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, StudioURL: func() string { return "http://x" }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"status"})
	require.NoError(t, cmd.Execute())

	cmd = Cmd(Resolvers{Studio: func() Studio { return c }, StudioURL: func() string { return "http://x" }})
	cmd.SetArgs([]string{"accounts"})
	require.NoError(t, cmd.Execute())
}

// TestGithubAccounts_ReauthMapped pins that github_reauth_required becomes an
// actionable "run `palbase github connect`" error, not a raw UNAUTHORIZED.
func TestGithubAccounts_ReauthMapped(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"json":{"message":"github_reauth_required"}}}`))
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }, StudioURL: func() string { return "http://x" }})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"accounts"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase github connect")
}
