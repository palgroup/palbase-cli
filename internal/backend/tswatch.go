package backend

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// emitAndWriteTS emits the TypeScript client for already-parsed ops and writes
// it to outFile. Returns (nOps, err). The zero-op guard is NOT applied here —
// the caller decides what to do with 0 ops (pullTSTypes and watchTSLoop have
// different policies). Parent directories are created as needed.
func emitAndWriteTS(ops []swiftOp, cfg tsGeneratedConfig, outFile string) (int, error) {
	if dir := filepath.Dir(outFile); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	tsOut := emitTypeScript(ops, cfg)
	if err := os.WriteFile(outFile, []byte(tsOut), 0o644); err != nil {
		return 0, fmt.Errorf("write %s: %w", outFile, err)
	}
	return len(ops), nil
}

// fetchFuncType is the signature for the injected fetch function in watchTSLoop.
// It should return the raw spec bytes (as fetchLocalOpenAPISpec does) or an error.
type fetchFuncType func(ctx context.Context, url string) ([]byte, error)

// watchSignalContext wraps the watch context with SIGINT/SIGTERM cancellation
// (mirrors `palbase serve`'s signal.NotifyContext) so a real Ctrl-C cancels
// the loop's ctx → clean "watch stopped" exit 0, instead of a hard kill that
// could truncate palbe.gen.ts mid-WriteFile and leak the ticker goroutine.
// Package-level var as a test seam: delivering a real SIGINT in a test is
// hazardous (a mistimed signal kills the test runner), so tests stub this to
// assert the dispatch wraps with it and drive cancellation through the
// returned ctx — the loop's ctx-cancel path itself is covered by
// TestWatchTSLoop_Cancellation.
var watchSignalContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
}

// runTypesWatch is the watch-mode entry point called from newTypesCmd.
// It performs the initial generation (via pullTSTypes), then enters watchTSLoop
// with a real 1-second ticker and fetchLocalOpenAPISpec as the fetch function.
//
// Initial failure policy:
//   - Hard error (softFlag=false, env != "local"): if pullTSTypes fails,
//     return the error and do NOT start the watch loop.
//   - Soft mode (softFlag=true): if pullTSTypes fails, print the soft warning
//     and CONTINUE into the watch loop — the whole point is to wait for
//     `palbase serve` to come up.
//   - env="local": an initial failure is ALWAYS treated as soft. --env local's
//     only failure mode is the local serve being down/erroring, and the watch
//     loop exists precisely to wait for it (matches the --watch help text:
//     "--watch handles the case where serve is not yet up").
func runTypesWatch(
	ctx context.Context,
	r Resolvers,
	ref, branch, env, outFile string,
	softFlag bool,
	w io.Writer,
) error {
	if env == "remote" {
		fmt.Fprintln(w, "note: --watch follows the local `palbase serve` — after the initial remote generation, spec changes on :4003 rewrite the file with the localhost config")
	}

	// Initial generation.
	if err := pullTSTypes(ctx, r.Studio(), r.Endpoints(), ref, branch, env, outFile, w); err != nil {
		if !softFlag && env != "local" {
			return err
		}
		fmt.Fprintf(w, "warning: codegen skipped (%v)\n", err)
	}

	// Watch loop: local spec URL is always localhost:4003.
	const localSpecURL = "http://localhost:4003/openapi.json"

	// Build the local cfg for the loop: URL is always localhost (no apiKey, no
	// OAuth — serve's spec is unauthenticated). Branch comes from the caller.
	cfg := tsGeneratedConfig{
		URL:    "http://localhost:4003",
		Branch: branch,
	}

	// Real ticker: fire every second.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	// Convert time.Ticker to chan struct{} by bridging via a goroutine.
	tickCh := make(chan struct{})
	go func() {
		defer close(tickCh)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ticker.C:
				if !ok {
					return
				}
				select {
				case tickCh <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	fetchFn := func(ctx context.Context, url string) ([]byte, error) {
		return fetchLocalOpenAPISpec(ctx, url)
	}
	return watchTSLoop(ctx, localSpecURL, cfg, outFile, fetchFn, tickCh, w)
}

// watchTSLoop implements the `palbase web gen --watch` inner loop.
//
// It polls localSpecURL (always http://localhost:4003/openapi.json) using the
// injected fetchFn. On each tick:
//   - Fetch fails: print a down message ONCE per transition — "waiting for
//     palbase serve on :4003…" when nothing is listening, or "local serve
//     responded with an error (HTTP n)" when serve answered with an error
//     (mirrors pullTSTypes' httpStatusError wording split). The message is
//     reprinted only when it CHANGES (up→down, or refused↔HTTP-error).
//   - Fetch succeeds: SHA-256 the body. Hash equals the last EMITTED hash →
//     no change, skip. Hash equals the last WARNED-bad hash → the same broken
//     body, stay silent (no re-parse, no warn-spam every tick). Otherwise
//     parse + emit + write. A bad body (unparseable, or 0 ops with an
//     existing file to protect) warns once and records its hash; a DIFFERENT
//     bad body is a new occurrence and warns again. A successful emit resets
//     the bad-hash state, so the same bad body reappearing later warns anew.
//
// Hash state starts EMPTY intentionally: the first successful local fetch
// always regenerates. This is correct because the initial pullTSTypes call
// (before this loop starts) may have used the REMOTE spec (env auto with
// serve down) — the local spec is likely different. Even if pullTSTypes used
// the local spec, an unconditional first write is harmless and simplifies the
// state machine.
//
// Exits cleanly (nil) on ctx cancellation, printing "watch stopped".
func watchTSLoop(
	ctx context.Context,
	localSpecURL string,
	cfg tsGeneratedConfig,
	outFile string,
	fetchFn fetchFuncType,
	tick <-chan struct{},
	w io.Writer,
) error {
	var lastEmittedHash [32]byte
	var hashSet bool // true once we have emitted at least once
	var lastBadHash [32]byte
	var badHashSet bool // true while the last warned-about bad body stands
	lastDownMsg := ""   // last printed down message; "" = serve was up

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(w, "watch stopped")
			return nil
		case _, ok := <-tick:
			if !ok {
				// Ticker channel closed — treat as cancellation.
				fmt.Fprintln(w, "watch stopped")
				return nil
			}
		}

		specBytes, err := fetchFn(ctx, localSpecURL)
		if err != nil {
			msg := "waiting for palbase serve on :4003…"
			var hs httpStatusError
			if errors.As(err, &hs) {
				msg = fmt.Sprintf("local serve responded with an error (HTTP %d) — fix `palbase serve`", hs.status)
			}
			if msg != lastDownMsg {
				fmt.Fprintln(w, msg)
				lastDownMsg = msg
			}
			continue
		}

		// Serve is (back) up.
		lastDownMsg = ""

		h := sha256.Sum256(specBytes)
		if hashSet && h == lastEmittedHash {
			continue // no change since the last emit
		}
		if badHashSet && h == lastBadHash {
			continue // same bad body we already warned about — stay silent
		}

		ops, err := parseOpenAPIForSwift(specBytes)
		if err != nil {
			fmt.Fprintf(w, "warning: failed to parse spec: %v\n", err)
			lastBadHash, badHashSet = h, true
			continue
		}
		if len(ops) == 0 {
			if existing, readErr := os.ReadFile(outFile); readErr == nil && len(existing) > 0 {
				fmt.Fprintf(w, "warning: live spec has 0 operations — keeping existing %s (fix your controllers and rerun)\n", outFile)
				lastBadHash, badHashSet = h, true
				continue
			}
		}

		nOps, err := emitAndWriteTS(ops, cfg, outFile)
		if err != nil {
			// Environmental write failure (disk) — not spec-dependent, so do
			// not record a bad hash; the next tick retries.
			fmt.Fprintf(w, "warning: codegen error: %v\n", err)
			continue
		}

		lastEmittedHash, hashSet = h, true
		badHashSet = false // a good emit resets the warn-dedup state
		fmt.Fprintf(w, "regenerated %s (%d operations)\n", outFile, nOps)
	}
}
