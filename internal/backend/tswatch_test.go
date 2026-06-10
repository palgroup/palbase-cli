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
func emptySpec() []byte {
	return []byte(`{"openapi":"3.1.0","info":{"title":"Palbase Backend","version":"1.0.0"},"paths":{},"components":{}}`)
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

// ── TestTypesWatch_SwiftLangError ─────────────────────────────────────────────
// --lang swift --watch → error "only supported for --lang ts"

func TestTypesWatch_SwiftLangError(t *testing.T) {
	t.Chdir(t.TempDir())
	r := deadStudioResolvers()
	cmd := newTypesCmd(r)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--ref", "abc123", "--lang", "swift", "--watch"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--watch is only supported for --lang ts")
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
	require4003Free(t) // local serve is down → initial pullTSTypes fails
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
