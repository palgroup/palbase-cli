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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/transport"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// runLogs drives `palbase logs` against a fake project and returns what the
// server was asked plus what the terminal saw.
func runLogs(t *testing.T, entries []map[string]any, args ...string) (url.Values, string) {
	t.Helper()
	t.Chdir(t.TempDir())
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/panel/environments/app1prod/logs" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
	}))
	t.Cleanup(srv.Close)

	// THE ADDRESS PATH, which is the one that ships. These tests used to reach
	// `showCloud` through the selection resolver; FR-013 retired that, and the
	// route it exercised is still live — a linked checkout whose address IS a
	// cloud ref takes it. So the rig binds the checkout instead of selecting it.
	// `backend.RootDir()`, never the string: this rig binds a checkout the way
	// `link` does, and a spelled-out directory would keep building the old one.
	if err := os.MkdirAll(backend.RootDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backend.RootDir(), "project.json"),
		[]byte(`{"url":"https://app1prod.palbase.studio"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rest := transport.New(srv.URL, "tok_test")
	cmd := Cmd(Resolvers{
		REST:     func() REST { return rest },
		CloudRef: func(string) (string, bool) { return "app1prod", true },
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

// followAnswers answers the follow loop's reads in order, repeating the last
// answer. An answer is an entries document or an error.
//
// A STUB RATHER THAN THE httptest SERVER, deliberately: a named transient on a
// GET is swallowed INSIDE transport.Do, so a 503 written on the wire would be
// retried by the transport and never reach this loop at all. What reaches the
// loop is what the transport hands back after its own budget — which is the
// shape these answers carry.
type followAnswers struct {
	answers []any
	calls   int
	paths   []string
	onCall  func(n int)
}

func (f *followAnswers) Do(_ context.Context, method, path string, _ any, out any) error {
	f.calls++
	f.paths = append(f.paths, path)
	if f.onCall != nil {
		f.onCall(f.calls)
	}
	if method != http.MethodGet {
		return fmt.Errorf("unexpected method %s", method)
	}
	if !strings.HasPrefix(path, "/v1/panel/environments/app1prod/logs?") {
		return fmt.Errorf("unexpected path %s", path)
	}
	i := f.calls - 1
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	if err, ok := f.answers[i].(error); ok {
		return err
	}
	raw, err := json.Marshal(map[string]any{"entries": f.answers[i]})
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// runFollow drives `palbase logs --follow` against a stub and cancels the
// command's context once the stub has been asked cancelAfter times. A
// cancelAfter nobody reaches leaves the loop to end on its own — which is how
// the real-failure case is measured.
func runFollow(t *testing.T, stub *followAnswers, cancelAfter int) (string, string, error) {
	t.Helper()
	t.Chdir(t.TempDir())

	prev := followInterval
	followInterval = time.Millisecond
	t.Cleanup(func() { followInterval = prev })

	if err := os.MkdirAll(backend.RootDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backend.RootDir(), "project.json"),
		[]byte(`{"url":"https://app1prod.palbase.studio"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stub.onCall = func(n int) {
		if cancelAfter > 0 && n >= cancelAfter {
			cancel()
		}
	}

	cmd := Cmd(Resolvers{
		REST:     func() REST { return stub },
		CloudRef: func(string) (string, bool) { return "app1prod", true },
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--follow"})
	err := cmd.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

// A WAKING TENANT MUST NOT KILL THE TAIL (FR-012).
//
// Measured: `read` failing ended the loop with `return err`, so a tenant that
// woke up or was swapped underneath a follower took the follower with it — and
// the person saw the platform's own transient state as the reason their tail
// died.
func TestFollowReconnectsWhenTheAnswerIsANamedTransient(t *testing.T) {
	gaveUp := fmt.Errorf(
		"the environment is still starting after 4m0s — it usually answers within a minute; run the same command again: %w",
		&transport.APIError{Code: "tenant_unreachable", Status: 503})
	stub := &followAnswers{answers: []any{
		[]map[string]any{{"timestamp": "2026-07-02T10:00:01Z", "severity": "info", "source": "runtime", "body": "before"}},
		gaveUp,
		[]map[string]any{
			{"timestamp": "2026-07-02T10:00:02Z", "severity": "info", "source": "runtime", "body": "after"},
			{"timestamp": "2026-07-02T10:00:01Z", "severity": "info", "source": "runtime", "body": "before"},
		},
	}}
	out, errOut, err := runFollow(t, stub, 3)

	require.NoError(t, err, "a named transient ended the tail")
	require.Equal(t, 3, stub.calls, "the loop did not read again after the transient")
	require.Contains(t, out, "before")
	require.Contains(t, out, "after", "the lines after the reconnection were never printed")
	// THE NOTICE GOES TO STDERR, not to the stream. With --json, stdout is a
	// document per line and a notice printed there would corrupt it.
	require.Contains(t, errOut, "tenant_unreachable")
	require.NotContains(t, out, "tenant_unreachable")
}

// AND A REAL FAILURE STILL ENDS IT. A 404 is the plane answering, not the
// platform starting; retrying it forever would hide the cause behind a tail
// that never prints anything again.
func TestFollowStillExitsOnARealFailure(t *testing.T) {
	stub := &followAnswers{answers: []any{
		[]map[string]any{{"timestamp": "2026-07-02T10:00:01Z", "severity": "info", "source": "runtime", "body": "before"}},
		&transport.APIError{Code: "not_found", Status: 404},
	}}
	_, _, err := runFollow(t, stub, 0)

	require.Error(t, err, "a real failure was retried instead of reported")
	require.Contains(t, err.Error(), "not_found")
	require.Equal(t, 2, stub.calls)
}
