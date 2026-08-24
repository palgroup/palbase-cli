package logs

// The one-shot path, end to end against a project that answers on the wire.
//
// These used to drive a fake Studio and assert a tRPC input document. That
// transport is gone — the lines come from the plane's panel surface now, which
// reads the same ClickHouse the panel's log screen does — so what is worth
// pinning is the PATH, the query parameters a person's flags turn into, and the
// order lines reach the terminal in.

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// runLogs drives `palbase logs` against a fake project and returns what the
// server was asked plus what the terminal saw.
func runLogs(t *testing.T, entries []map[string]any, args ...string) (url.Values, string) {
	t.Helper()
	t.Chdir(t.TempDir())
	f := selectiontest.New(t)
	selectiontest.WriteConfig(t, ".", nil)

	var got url.Values
	f.Handle("GET /v1/panel/environments/app1prod/logs",
		func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Query()
			selectiontest.WriteOK(w, http.StatusOK, map[string]any{"entries": entries})
		})

	rest := f.REST()
	resolver := f.Resolver()
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute())
	return got, out.String()
}

// Every flag a person can type reaches the store as a parameter. A filter the
// server never sees is worse than one that does not exist: the command answers,
// the lines look plausible, and nobody learns that `--level error` showed them
// everything.
func TestEveryFilterTravels(t *testing.T) {
	got, _ := runLogs(t, nil,
		"--level", "error,warn", "-q", "timeout", "--limit", "50",
		"--since", "15m", "--source", "runtime")

	require.Equal(t, "error,warn", got.Get("level"))
	require.Equal(t, "timeout", got.Get("search"))
	require.Equal(t, "50", got.Get("limit"))
	require.Equal(t, "900", got.Get("window_seconds"))
	require.Equal(t, "runtime", got.Get("source"))
}

// The store answers newest-first; a terminal reads oldest-first. Printing the
// wire order would put the newest line at the top and every follow-up below it.
func TestNewestFirstOnTheWireIsOldestFirstOnTheScreen(t *testing.T) {
	_, out := runLogs(t, []map[string]any{
		{"timestamp": "2026-07-02T10:00:02Z", "severity": "error", "source": "runtime", "body": "newest"},
		{"timestamp": "2026-07-02T10:00:01Z", "severity": "info", "source": "runtime", "body": "oldest"},
	})
	require.Less(t, bytes.Index([]byte(out), []byte("oldest")), bytes.Index([]byte(out), []byte("newest")),
		"lines were printed newest-first:\n%s", out)
}

// A window nobody named is an hour — the panel's default too, so the two
// screens answer the same question when neither is told otherwise.
func TestTheDefaultWindowIsAnHour(t *testing.T) {
	got, _ := runLogs(t, nil)
	require.Equal(t, "3600", got.Get("window_seconds"))
	require.Empty(t, got.Get("level"), "an unset filter must not travel as an empty one")
	require.Empty(t, got.Get("search"))
	require.Empty(t, got.Get("source"))
}

// An empty window is a state, and saying so beats printing nothing: somebody
// looking at a blank terminal cannot tell it from a broken command.
func TestNoLinesSaysSo(t *testing.T) {
	_, out := runLogs(t, nil)
	require.Contains(t, out, "no log lines")
}

func TestParseWindow(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 3600},
		{"15m", 900},
		{"2h", 7200},
		{"1h30m", 5400},
		{"7d", 604800}, // Go's ParseDuration refuses `d`; people write it anyway.
	} {
		got, err := parseWindow(tc.in)
		require.NoErrorf(t, err, "--since %q", tc.in)
		require.Equalf(t, tc.want, got, "--since %q", tc.in)
	}

	for _, bad := range []string{"soon", "0", "-5m", "0d", "d", "15"} {
		_, err := parseWindow(bad)
		require.Errorf(t, err, "--since %q was accepted", bad)
		require.Contains(t, err.Error(), "15m, 2h or 7d")
	}
}

// The poll-boundary invariant: lines sharing the last timestamp are neither
// re-printed nor dropped across polls. The store answers a window rather than a
// cursor, so a follower re-reads overlapping lines every time and this is what
// keeps the overlap from reaching the screen twice.
func TestFollowCursor_Dedup(t *testing.T) {
	l := func(ts, msg string) logLine { return logLine{Timestamp: ts, Message: msg} }

	c := newFollowCursor([]logLine{l("t1", "a"), l("t2", "b")})

	fresh := c.fresh([]logLine{l("t2", "b"), l("t2", "c"), l("t3", "d")})
	require.Len(t, fresh, 2)
	require.Equal(t, "c", fresh[0].Message, "same-ts sibling must NOT be dropped")
	require.Equal(t, "d", fresh[1].Message)

	require.Empty(t, c.fresh([]logLine{l("t2", "c"), l("t3", "d")}))
	require.Empty(t, c.fresh([]logLine{l("t1", "a")}))
}
