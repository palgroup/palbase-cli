package backend

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// emitAndWriteTS parses specBytes and writes the TypeScript client to outFile.
// Returns (nOps, err). The zero-op guard is NOT applied here — the caller
// decides what to do with 0 ops (pullTSTypes and watchTSLoop have different
// policies). The caller is responsible for creating parent directories.
func emitAndWriteTS(specBytes []byte, cfg tsGeneratedConfig, outFile string) (int, error) {
	ops, err := parseOpenAPIForSwift(specBytes)
	if err != nil {
		return 0, err
	}
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

// runTypesWatch is the watch-mode entry point called from newTypesCmd.
// It performs the initial generation (via pullTSTypes), then enters watchTSLoop
// with a real 1-second ticker and fetchLocalOpenAPISpec as the fetch function.
//
// Initial failure policy:
//   - Hard error (softFlag=false): if pullTSTypes fails, return the error and
//     do NOT start the watch loop.
//   - Soft mode (softFlag=true): if pullTSTypes fails, print the soft warning
//     and CONTINUE into the watch loop — the whole point is to wait for
//     `palbase serve` to come up.
func runTypesWatch(
	ctx context.Context,
	r Resolvers,
	ref, branch, env, outFile string,
	softFlag bool,
	w io.Writer,
) error {
	// Initial generation.
	if err := pullTSTypes(ctx, r.Studio(), r.Endpoints(), ref, branch, env, outFile, w); err != nil {
		if !softFlag {
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

// watchTSLoop implements the `palbase types --watch` inner loop.
//
// It polls localSpecURL (always http://localhost:4003/openapi.json) using the
// injected fetchFn. On each tick:
//   - If fetch fails: print "waiting for palbase serve on :4003…" ONCE per
//     down-transition (suppress repeated prints while serve stays down).
//   - If fetch succeeds: SHA-256 the body; if the hash differs from the last
//     EMITTED hash → run emitAndWriteTS + print regenerated message.
//     Zero-op guard: if 0 ops and existing file → keep + warn once per occurrence.
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
	var hashSet bool  // true once we have emitted at least once
	serveWasDown := false

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
			if !serveWasDown {
				fmt.Fprintln(w, "waiting for palbase serve on :4003…")
				serveWasDown = true
			}
			continue
		}

		// Serve is (back) up.
		serveWasDown = false

		h := sha256.Sum256(specBytes)
		if hashSet && h == lastEmittedHash {
			// No change — skip.
			continue
		}

		// Parse and apply zero-op guard.
		ops, err := parseOpenAPIForSwift(specBytes)
		if err != nil {
			fmt.Fprintf(w, "warning: failed to parse spec: %v\n", err)
			continue
		}
		if len(ops) == 0 {
			if existing, readErr := os.ReadFile(outFile); readErr == nil && len(existing) > 0 {
				fmt.Fprintf(w, "warning: live spec has 0 operations — keeping existing %s (fix your controllers and rerun)\n", outFile)
				// Do NOT update lastEmittedHash — let it retry next tick.
				continue
			}
		}

		// Write the file.
		nOps, err := emitAndWriteTS(specBytes, cfg, outFile)
		if err != nil {
			fmt.Fprintf(w, "warning: codegen error: %v\n", err)
			continue
		}

		lastEmittedHash = h
		hashSet = true
		fmt.Fprintf(w, "regenerated %s (%d operations)\n", outFile, nOps)
	}
}
