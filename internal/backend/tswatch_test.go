package backend

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/stretchr/testify/require"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// watchCfg returns a minimal tsGeneratedConfig for watch loop tests.
func watchCfg() tsGeneratedConfig {
	return tsGeneratedConfig{URL: "http://localhost:4003", Branch: "main"}
}

// specBody returns a valid OpenAPI spec with the given title substituted into
// the fixture so different titles produce different SHA-256 hashes.
func specBody(title string) []byte {
	s := strings.Replace(tsFixtureOpenAPI, `"title":"t"`, `"title":"`+title+`"`, 1)
	return []byte(s)
}

// emptySpec returns an OpenAPI spec with paths:{} (0 operations).
func emptySpec() []byte { return emptySpecTitled("Palbase Backend") }

// emptySpecTitled is emptySpec with a controllable title — different titles
// produce different bytes (and SHA-256 hashes) while staying zero-op.
func emptySpecTitled(title string) []byte {
	return []byte(`{"openapi":"3.1.0","info":{"title":"` + title + `","version":"1.0.0"},"paths":{},"components":{}}`)
}

// sendTick sends to an unbuffered tick channel. The send blocks until the
// watchTSLoop goroutine receives the tick, ensuring sequential processing.
func sendTick(tick chan<- struct{}) { tick <- struct{}{} }

// ── TestWatchTSLoop_ChangeDetection ──────────────────────────────────────────
// Two different spec bodies → two writes. Same body twice → ONE write.

func TestWatchTSLoop_ChangeDetection(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	// Unbuffered: each sendTick blocks until the goroutine receives — guarantees
	// the tick is consumed (and the select branch entered) before we proceed.
	tick := make(chan struct{})
	cfg := watchCfg()
	var w bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	specA := specBody("vA")
	specB := specBody("vB")
	calls := [][]byte{specA, specA, specB}
	idx := 0
	fetchFn := func(_ context.Context, _ string) ([]byte, error) {
		b := calls[idx]
		idx++
		return b, nil
	}

	done := make(chan error, 1)
	go func() {
		done <- watchTSLoop(ctx, "http://localhost:4003/openapi.json", cfg, outFile, fetchFn, tick, &w)
	}()

	// Tick 1: specA → first emit (hash was empty).
	sendTick(tick)
	// Tick 2: specA again → no change.
	sendTick(tick)
	// Tick 3: specB → different hash → regen.
	sendTick(tick)
	// Cancel after all ticks are received. The goroutine finishes tick 3's
	// processing, loops back to select, and picks up ctx.Done before a 4th tick.
	cancel()
	require.NoError(t, <-done)

	out := w.String()
	require.Equal(t, 2, strings.Count(out, "regenerated"),
		"must regenerate exactly twice (first specA + specB, NOT the repeated specA)")

	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.Greater(t, len(body), 0)
}

// ── TestWatchTSLoop_ZeroOpMidWatch ───────────────────────────────────────────
// Good spec → write; then 0-op spec + existing file → file kept, warning printed.

func TestWatchTSLoop_ZeroOpMidWatch(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	tick := make(chan struct{})
	cfg := watchCfg()
	var w bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	specGood := specBody("good")
	specZero := emptySpec()
	calls := [][]byte{specGood, specZero}
	idx := 0
	fetchFn := func(_ context.Context, _ string) ([]byte, error) {
		b := calls[idx]
		idx++
		return b, nil
	}

	done := make(chan error, 1)
	go func() {
		done <- watchTSLoop(ctx, "http://localhost:4003/openapi.json", cfg, outFile, fetchFn, tick, &w)
	}()

	sendTick(tick) // tick 1: good spec → write
	sendTick(tick) // tick 2: zero-op spec → keep existing, warn
	cancel()
	require.NoError(t, <-done)

	out := w.String()
	require.Contains(t, out, "regenerated", "good spec must write")
	require.Contains(t, out, "warning: live spec has 0 operations — keeping existing",
		"zero-op must keep existing + warn")

	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.Contains(t, string(body), "__registerNamespaces(", "file must retain good content")
}

// ── TestWatchTSLoop_DownUpTransition ─────────────────────────────────────────
// Fetch errors twice (one "waiting…" print), then succeeds → regen.

func TestWatchTSLoop_DownUpTransition(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	tick := make(chan struct{})
	cfg := watchCfg()
	var w bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type call struct {
		body []byte
		err  error
	}
	calls := []call{
		{nil, errors.New("connection refused")},
		{nil, errors.New("connection refused")},
		{specBody("up"), nil},
	}
	idx := 0
	fetchFn := func(_ context.Context, _ string) ([]byte, error) {
		c := calls[idx]
		idx++
		return c.body, c.err
	}

	done := make(chan error, 1)
	go func() {
		done <- watchTSLoop(ctx, "http://localhost:4003/openapi.json", cfg, outFile, fetchFn, tick, &w)
	}()

	sendTick(tick)
	sendTick(tick)
	sendTick(tick)
	cancel()
	require.NoError(t, <-done)

	out := w.String()
	require.Equal(t, 1, strings.Count(out, "waiting for palbase serve on :4003"),
		"must print waiting exactly once per down-transition")
	require.Contains(t, out, "regenerated")
}

// ── TestWatchTSLoop_Cancellation ─────────────────────────────────────────────
// cancel ctx → loop returns nil, "watch stopped" printed.

func TestWatchTSLoop_Cancellation(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	tick := make(chan struct{})
	cfg := watchCfg()
	var w bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	fetchFn := func(_ context.Context, _ string) ([]byte, error) {
		return specBody("x"), nil
	}

	done := make(chan error, 1)
	go func() {
		done <- watchTSLoop(ctx, "http://localhost:4003/openapi.json", cfg, outFile, fetchFn, tick, &w)
	}()

	cancel()
	require.NoError(t, <-done)
	require.Contains(t, w.String(), "watch stopped")
}

// ── TestWatchTSLoop_TickerClosed ─────────────────────────────────────────────
// A closed tick channel → loop returns nil, "watch stopped" printed.

func TestWatchTSLoop_TickerClosed(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	tick := make(chan struct{})
	cfg := watchCfg()
	var w bytes.Buffer
	ctx := context.Background()

	fetchFn := func(_ context.Context, _ string) ([]byte, error) {
		return specBody("x"), nil
	}

	done := make(chan error, 1)
	go func() {
		done <- watchTSLoop(ctx, "http://localhost:4003/openapi.json", cfg, outFile, fetchFn, tick, &w)
	}()

	close(tick)
	require.NoError(t, <-done)
	require.Contains(t, w.String(), "watch stopped")
}

// ── TestTypesWatch_FlagDispatch ───────────────────────────────────────────────
// --watch flag on the cobra command routes to the watch path (not one-shot).
// We verify by asserting that "watch stopped" is printed — this string is
// only emitted by watchTSLoop, never by the one-shot pullTSTypes path.
//
// Strategy: use --soft so that even if the initial generation fails (serve
// may or may not be on 4003), runTypesWatch still enters the watch loop.
// We pre-cancel the context so the loop exits immediately upon starting.

func TestTypesWatch_FlagDispatch(t *testing.T) {
	t.Chdir(t.TempDir())
	// Write a project config so resolveOrLinkRef returns without auto-linking.
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "abc123", DefaultEnv: "main"}))

	// Pre-cancel context: initial pullTSTypes will fail (context.Canceled),
	// --soft swallows it, then the loop's first select fires ctx.Done immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	r := deadStudioResolvers()
	cmd := newTypesCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--ref", "abc123", "--watch", "--soft"})

	err := cmd.ExecuteContext(ctx)
	require.NoError(t, err)
	require.Contains(t, out.String(), "watch stopped",
		"--watch must enter the watch loop (not the one-shot path)")
}

// ── TestTypesWatch_SoftInitialFailure ────────────────────────────────────────
// --watch --soft with initial failure (no serve, no Studio) must NOT return an
// error; it must print the soft warning and then enter the watch loop (which
// we cancel immediately).

func TestTypesWatch_SoftInitialFailure(t *testing.T) {
	localServeDown(t) // local serve is down → initial pullTSTypes fails
	t.Chdir(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := deadStudioResolvers()
	cmd := newTypesCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--ref", "abc123", "--watch", "--soft"})

	done := make(chan error, 1)
	go func() {
		done <- cmd.ExecuteContext(ctx)
	}()

	cancel()
	err := <-done
	require.NoError(t, err, "--watch --soft must exit 0 even on initial failure")
	require.Contains(t, out.String(), "warning: codegen skipped (", "soft warning must be printed")
	require.Contains(t, out.String(), "watch stopped", "must enter the watch loop after soft-failure")
}

// ── tick-driven loop rig ─────────────────────────────────────────────────────

// fetchResult is one scripted fetchFn return for runWatchTicks.
type fetchResult struct {
	body []byte
	err  error
}

// runWatchTicks drives watchTSLoop through the scripted fetch results (one per
// tick) against outFile, then cancels and returns the loop's full output.
func runWatchTicks(t *testing.T, outFile string, results []fetchResult) string {
	t.Helper()
	tick := make(chan struct{})
	var w bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	idx := 0
	fetchFn := func(_ context.Context, _ string) ([]byte, error) {
		r := results[idx]
		idx++
		return r.body, r.err
	}

	done := make(chan error, 1)
	go func() {
		done <- watchTSLoop(ctx, "http://localhost:4003/openapi.json", watchCfg(), outFile, fetchFn, tick, &w)
	}()

	for range results {
		sendTick(tick)
	}
	cancel()
	require.NoError(t, <-done)
	return w.String()
}

// ── TestWatchTSLoop_BadSpecWarnDedup ─────────────────────────────────────────
// The SAME zero-op body across 3 ticks warns ONCE — no 1s warn-spam while the
// broken spec stands unchanged.

func TestWatchTSLoop_BadSpecWarnDedup(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	require.NoError(t, os.WriteFile(outFile, []byte("// previously good client\n"), 0o644))

	zero := emptySpec()
	out := runWatchTicks(t, outFile, []fetchResult{{zero, nil}, {zero, nil}, {zero, nil}})

	require.Equal(t, 1, strings.Count(out, "warning: live spec has 0 operations"),
		"unchanged zero-op body must warn exactly once, not every tick")
	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.Equal(t, "// previously good client\n", string(body))
}

// ── TestWatchTSLoop_BadSpecChangedRewarns ────────────────────────────────────
// A DIFFERENT zero-op body is a new occurrence → warns again.

func TestWatchTSLoop_BadSpecChangedRewarns(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	require.NoError(t, os.WriteFile(outFile, []byte("// previously good client\n"), 0o644))

	out := runWatchTicks(t, outFile, []fetchResult{
		{emptySpecTitled("A"), nil},
		{emptySpecTitled("B"), nil}, // different bytes, still zero-op
	})

	require.Equal(t, 2, strings.Count(out, "warning: live spec has 0 operations"),
		"a changed (still bad) body is a new occurrence and must warn again")
}

// ── TestWatchTSLoop_BadGoodBadResets ─────────────────────────────────────────
// zero-op (warn) → good (regen, resets the warn-dedup state) → the SAME
// zero-op body again → warns anew (proves the reset).

func TestWatchTSLoop_BadGoodBadResets(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	require.NoError(t, os.WriteFile(outFile, []byte("// previously good client\n"), 0o644))

	zero := emptySpec()
	out := runWatchTicks(t, outFile, []fetchResult{
		{zero, nil},             // warn 1
		{specBody("good"), nil}, // regen — resets bad-hash state
		{zero, nil},             // same bad body, but post-reset → warn 2
	})

	require.Equal(t, 2, strings.Count(out, "warning: live spec has 0 operations"),
		"a successful emit must reset the warn-dedup state")
	require.Equal(t, 1, strings.Count(out, "regenerated"))
	body, err := os.ReadFile(outFile)
	require.NoError(t, err)
	require.Contains(t, string(body), "__registerNamespaces(", "zero-op must keep the regenerated good content")
}

// ── TestWatchTSLoop_UnparseableWarnDedup ─────────────────────────────────────
// Same unparseable body twice → one "failed to parse" warning (same dedup rail
// as the zero-op guard).

func TestWatchTSLoop_UnparseableWarnDedup(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	garbage := []byte("not json at all")
	out := runWatchTicks(t, outFile, []fetchResult{{garbage, nil}, {garbage, nil}})

	require.Equal(t, 1, strings.Count(out, "warning: failed to parse spec"),
		"unchanged unparseable body must warn exactly once")
}

// ── TestWatchTSLoop_HTTPErrorDistinctMessage ─────────────────────────────────
// A serve that ANSWERED with an HTTP error prints the httpStatusError wording
// (pullTSTypes parity), once per transition — not the misleading "waiting".

func TestWatchTSLoop_HTTPErrorDistinctMessage(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	httpErr := httpStatusError{status: 500, err: errors.New("fetch: 500 boom")}
	out := runWatchTicks(t, outFile, []fetchResult{
		{nil, httpErr},
		{nil, httpErr},
		{specBody("recovered"), nil},
	})

	require.Equal(t, 1, strings.Count(out, "local serve responded with an error (HTTP 500)"),
		"HTTP-error down state must print its own message once per transition")
	require.NotContains(t, out, "waiting for palbase serve",
		"an answering-but-erroring serve is not 'down' — wording must split")
	require.Contains(t, out, "regenerated")
}

// ── TestWatchTSLoop_DownUpDownSecondWaiting ──────────────────────────────────
// down → up (regen) → down again: the second down-transition prints "waiting"
// again (the transition state resets on a successful fetch).

func TestWatchTSLoop_DownUpDownSecondWaiting(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "palbe.gen.ts")
	out := runWatchTicks(t, outFile, []fetchResult{
		{nil, errors.New("connection refused")},
		{specBody("up"), nil},
		{nil, errors.New("connection refused")},
	})

	require.Equal(t, 2, strings.Count(out, "waiting for palbase serve on :4003"),
		"each down-transition must print waiting once")
	require.Equal(t, 1, strings.Count(out, "regenerated"))
}

// ── TestTypesWatch_SignalContextWiring ───────────────────────────────────────
// The --watch dispatch must wrap the command ctx with watchSignalContext
// (SIGINT/SIGTERM → clean "watch stopped" exit instead of a hard kill).
// Real signal delivery is hazardous in a test runner, so we stub the seam and
// assert it is invoked; the loop's ctx-cancel behaviour itself is pinned by
// TestWatchTSLoop_Cancellation.

func TestTypesWatch_SignalContextWiring(t *testing.T) {
	t.Chdir(t.TempDir())
	called := false
	orig := watchSignalContext
	watchSignalContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
		called = true
		return context.WithCancel(ctx)
	}
	t.Cleanup(func() { watchSignalContext = orig })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: the loop exits immediately after entering

	r := deadStudioResolvers()
	cmd := newTypesCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--ref", "abc123", "--watch", "--soft"})

	require.NoError(t, cmd.ExecuteContext(ctx))
	require.True(t, called, "--watch dispatch must wrap the ctx with watchSignalContext (SIGINT/SIGTERM)")
	require.Contains(t, out.String(), "watch stopped")
}

// ── TestTypesWatch_LocalEnvServeDownEntersLoop ───────────────────────────────
// --env local --watch with serve down: the initial generation failure is
// treated as soft (no --soft needed) — the loop exists to wait for serve.

func TestTypesWatch_LocalEnvServeDownEntersLoop(t *testing.T) {
	localServeDown(t)
	t.Chdir(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: loop exits immediately

	r := deadStudioResolvers()
	cmd := newTypesCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--ref", "abc123", "--env", "local", "--watch"})

	require.NoError(t, cmd.ExecuteContext(ctx),
		"--env local --watch must enter the loop on initial serve-down (implicit soft)")
	require.Contains(t, out.String(), "warning: codegen skipped (")
	require.Contains(t, out.String(), "watch stopped")
}

// ── TestTypesWatch_RemoteEnvNote ─────────────────────────────────────────────
// --env remote --watch prints a one-line note that the watch loop follows the
// local serve and rewrites the file with the localhost config.

func TestTypesWatch_RemoteEnvNote(t *testing.T) {
	t.Chdir(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: loop exits immediately

	r := deadStudioResolvers()
	cmd := newTypesCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--ref", "abc123", "--env", "remote", "--watch", "--soft"})

	require.NoError(t, cmd.ExecuteContext(ctx))
	require.Contains(t, out.String(), "note: --watch follows the local `palbase serve`",
		"--env remote + --watch must warn that the loop rewrites with the localhost config")
	require.Contains(t, out.String(), "watch stopped")
}
