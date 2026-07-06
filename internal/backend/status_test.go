package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// ptr is a tiny helper for the nullable lastDeploy fields.
func ptr[T any](v T) *T { return &v }

// TestFormatLastDeploy is the pure-function lock on the visibility surface: a
// server-side deploy failure that only lived in logs must render as a human
// line. Mutation-lock (M5): delete the `formatLastDeploy(...)` print call in
// newStatusCmd and TestStatus_PrintsLastDeploy goes RED.
func TestFormatLastDeploy(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	threeMinAgo := now.Add(-3 * time.Minute).Format(time.RFC3339)
	cases := []struct {
		name    string
		in      *lastDeploy
		want    string // substrings that MUST appear
		notWant string // "" = no negative assertion
	}{
		{
			name:    "nil renders nothing",
			in:      nil,
			want:    "",
			notWant: "last deploy",
		},
		{
			name: "failed carries the server error to the terminal",
			in: &lastDeploy{
				Status:    "failed",
				Error:     ptr("controller metadata extraction failed: @Query(zodSchema)…"),
				Branch:    ptr("main"),
				UpdatedAt: ptr(threeMinAgo),
			},
			want: "last deploy: FAILED (main, 3m ago)\n  error: controller metadata extraction failed: @Query(zodSchema)…\n",
		},
		{
			name: "succeeded with a warning error reads as succeeded-with-warnings",
			in: &lastDeploy{
				Status:    "succeeded",
				Error:     ptr("zero endpoints collected (deploy unaffected)"),
				Branch:    ptr("staging"),
				UpdatedAt: ptr(threeMinAgo),
			},
			want: "last deploy: SUCCEEDED WITH WARNINGS (staging, 3m ago)\n  error: zero endpoints collected (deploy unaffected)\n",
		},
		{
			name: "clean success prints no error line, defaults branch to main",
			in: &lastDeploy{
				Status:    "succeeded",
				Version:   ptr("sha-abc123"),
				UpdatedAt: ptr(threeMinAgo),
			},
			want:    "last deploy: SUCCEEDED (main, 3m ago)\n",
			notWant: "error:",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatLastDeploy(c.in, now)
			if c.want == "" {
				require.Empty(t, got)
			} else {
				require.Equal(t, c.want, got)
			}
			if c.notWant != "" {
				require.NotContains(t, got, c.notWant)
			}
		})
	}
}

func TestHumanizeAgo(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{3 * time.Minute, "3m ago"},
		{2 * time.Hour, "2h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, humanizeAgo(c.d))
	}
}

// TestStatus_PrintsLastDeploy_AndSendsBranch is the end-to-end lock: it drives
// newStatusCmd against a fake tRPC backend.status, asserts the FAILED lastDeploy
// block reaches stdout (centauri), AND that the active branch from the on-disk
// config is sent in the query input (F14).
func TestStatus_PrintsLastDeploy_AndSendsBranch(t *testing.T) {
	// A cwd linked to ref=todoapp on the "staging" branch.
	dir := t.TempDir()
	require.NoError(t, auth.SaveProjectConfigIn(dir, &auth.ProjectConfig{Ref: "todoapp", DefaultEnv: "staging"}))
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(dir))

	var gotInput map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// tRPC query carries the input as ?input={"json":{...}}.
		raw := r.URL.Query().Get("input")
		decoded, _ := url.QueryUnescape(raw)
		var env struct {
			JSON map[string]any `json:"json"`
		}
		_ = json.Unmarshal([]byte(decoded), &env)
		gotInput = env.JSON

		resp := map[string]any{
			"result": map[string]any{"data": map[string]any{"json": map[string]any{
				"ref":           "todoapp",
				"activeVersion": "sha-abc123",
				"lastDeploy": map[string]any{
					"status":    "failed",
					"error":     "controller metadata extraction failed: @Query(zodSchema)…",
					"branch":    "staging",
					"updatedAt": time.Now().Add(-3 * time.Minute).Format(time.RFC3339),
				},
			}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	out := captureStdout(t, func() {
		cmd := newStatusCmd(Resolvers{Studio: func() *studio.Client { return studio.New(srv.URL, nil) }})
		cmd.SetArgs([]string{})
		cmd.SilenceUsage = true
		require.NoError(t, cmd.Execute())
	})

	// F14: the active branch from config reached the server.
	require.Equal(t, "staging", gotInput["branch"], "status must send the active branch")
	require.Equal(t, "todoapp", gotInput["ref"])

	// Centauri: the failed deploy + its server-side error are on the terminal.
	require.Contains(t, out, "last deploy: FAILED (staging, 3m ago)")
	require.Contains(t, out, "error: controller metadata extraction failed: @Query(zodSchema)…")
}

// captureStdout swaps os.Stdout for a pipe, runs fn, and returns what it wrote.
// The status command prints via fmt.Printf to the real os.Stdout, so cmd.SetOut
// alone would not capture the lastDeploy line.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig
	b, _ := io.ReadAll(r)
	_ = r.Close()
	return string(b)
}
