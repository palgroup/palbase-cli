package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// deploysREST spins an httptest server serving the deployments list route and
// returns a real *transport.Client bound to it, so the {data,request_id}
// envelope unwrap matches production.
func deploysREST(t *testing.T, h http.HandlerFunc) *transport.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(srv.URL, key, "pat_test")
}

// TestDeploys_RendersFailedAndWarningRows is the end-to-end lock on the deploys
// rewrite: it drives newDeploysCmd against a fake REST /deployments list and
// asserts the FAILED row carries its server-side error into the NOTE column,
// the succeeded-with-error row reads as WARN (not a clean success), and rows
// from a NON-main branch appear (all-branches — no branch hides a failure).
//
// Mutation-lock (M5): delete the `deployNote(d)` argument (or the Error branch
// inside deployNote) and this goes RED — the error/warning text vanishes from
// the table.
func TestDeploys_RendersFailedAndWarningRows(t *testing.T) {
	chdirLinked(t, "todoapp")

	var gotPath string
	rest := deploysREST(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"deployments": []map[string]any{
				{
					"status":        "failed",
					"version":       nil,
					"branch":        "main",
					"trigger":       "github_push",
					"error":         "controller metadata extraction failed: @Query(zodSchema)…\nsecond line is dropped",
					"commitMessage": "add query endpoint",
					"createdAt":     "2026-07-06T09:58:11Z",
				},
				{
					"status":        "succeeded",
					"version":       "a1b2c3d",
					"branch":        "staging",
					"trigger":       "cli",
					"error":         "zero endpoints collected (deploy unaffected)",
					"commitMessage": "deploy via cli",
					"createdAt":     "2026-07-06T10:00:01Z",
				},
			}},
			"request_id": "req_x",
		})
	})

	out := captureStdout(t, func() {
		cmd := newDeploysCmd(Resolvers{
			// cwd is linked → resolveOrLinkRef returns from config without a
			// Studio call, but r.Studio() is evaluated as an argument, so it must
			// be non-nil. It is never dialed.
			Studio: func() *studio.Client { return studio.New("http://unused", nil) },
			REST:   func() REST { return rest },
		})
		cmd.SetArgs([]string{})
		cmd.SilenceUsage = true
		require.NoError(t, cmd.Execute())
	})

	// Reads the control-pg list route (NOT Store A backend.versions).
	require.Equal(t, "/api/v1/projects/todoapp/deployments?limit=20", gotPath)

	// The FAILED row carries its server-side error's FIRST line into NOTE.
	require.Contains(t, out, "FAILED")
	require.Contains(t, out, "controller metadata extraction failed: @Query(zodSchema)…")
	require.NotContains(t, out, "second line is dropped", "only the first error line goes in the table")

	// succeeded + a non-empty error reads as WARN, not a clean success.
	require.Contains(t, out, "WARN")
	require.Contains(t, out, "zero endpoints collected (deploy unaffected)")

	// All-branches: the non-main (staging) row is present.
	require.Contains(t, out, "staging")
	require.Contains(t, out, "main")
}

// TestDeploys_EmptyList locks the empty-state message (no rows → an actionable
// hint, not the old "(no versions)" that hid the centauri failure).
func TestDeploys_EmptyList(t *testing.T) {
	chdirLinked(t, "todoapp")
	rest := deploysREST(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       map[string]any{"deployments": []map[string]any{}},
			"request_id": "req_x",
		})
	})
	out := captureStdout(t, func() {
		cmd := newDeploysCmd(Resolvers{
			// cwd is linked → resolveOrLinkRef returns from config without a
			// Studio call, but r.Studio() is evaluated as an argument, so it must
			// be non-nil. It is never dialed.
			Studio: func() *studio.Client { return studio.New("http://unused", nil) },
			REST:   func() REST { return rest },
		})
		cmd.SetArgs([]string{})
		cmd.SilenceUsage = true
		require.NoError(t, cmd.Execute())
	})
	require.Contains(t, out, "no deploy attempts yet")
	require.NotContains(t, out, "no versions", "the Store-A empty message is retired")
}

// TestTruncateNote pins the ~100-char clamp used for the NOTE column.
func TestTruncateNote(t *testing.T) {
	require.Equal(t, "short", truncateNote("short", 100))
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateNote(string(long), 100)
	require.Equal(t, 100, len([]rune(got)), "99 kept chars + 1 ellipsis rune")
	require.True(t, []rune(got)[99] == '…', "truncated note ends with an ellipsis")
}
