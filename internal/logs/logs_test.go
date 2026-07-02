package logs

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
	})
}

func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// TestLogs_SearchWire pins the one-shot path: input carries ref/level-array/
// q/limit + BACKWARD, and the BACKWARD result is printed oldest-first.
func TestLogs_SearchWire(t *testing.T) {
	var gotInput string
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/logs.entries.search", r.URL.Path)
		gotInput = r.URL.Query().Get("input")
		trpcOK(w, map[string]any{
			"lines": []map[string]any{
				{"timestamp": "2026-07-02T10:00:02Z", "source": "backend", "level": "error", "message": "newest"},
				{"timestamp": "2026-07-02T10:00:01Z", "source": "backend", "level": "info", "message": "oldest"},
			},
			"next_cursor": nil,
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--ref", "todoappm8p6z", "--level", "error,warn", "-q", "timeout", "--limit", "50"})
	require.NoError(t, cmd.Execute())

	require.Contains(t, gotInput, `"ref":"todoappm8p6z"`)
	require.Contains(t, gotInput, `"level":["error","warn"]`)
	require.Contains(t, gotInput, `"q":"timeout"`)
	require.Contains(t, gotInput, `"limit":50`)
	require.Contains(t, gotInput, `"direction":"BACKWARD"`)

	s := out.String()
	require.Less(t, bytes.Index(out.Bytes(), []byte("oldest")), bytes.Index(out.Bytes(), []byte("newest")),
		"BACKWARD result must be printed oldest-first: %s", s)
}

// TestFollowCursor_Dedup pins the poll-boundary invariant: lines sharing the
// last timestamp are neither re-printed nor dropped across polls.
func TestFollowCursor_Dedup(t *testing.T) {
	l := func(ts, msg string) logLine { return logLine{Timestamp: ts, Message: msg} }

	c := newFollowCursor([]logLine{l("t1", "a"), l("t2", "b")})

	// Poll returns the boundary line again + one same-ts sibling + one newer.
	fresh := c.fresh([]logLine{l("t2", "b"), l("t2", "c"), l("t3", "d")})
	require.Len(t, fresh, 2)
	require.Equal(t, "c", fresh[0].Message, "same-ts sibling must NOT be dropped")
	require.Equal(t, "d", fresh[1].Message)

	// Next poll repeating everything yields nothing new.
	require.Empty(t, c.fresh([]logLine{l("t2", "c"), l("t3", "d")}))

	// Older-than-cursor lines are skipped.
	require.Empty(t, c.fresh([]logLine{l("t1", "a")}))
}
