package backend

// config-as-code orchestration, folded into the unified `palbase pull`
// and `palbase push` verbs (CLI-1 flat redesign). There is no longer a
// `config` subcommand for config-as-code — `palbase config` is the CLI
// mode/login config (main.go), a separate concern. The per-module
// serialization lives in the configcode package; these helpers are the
// reporting wrappers the pull/push commands call after the code/env step.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/palgroup/palbase-cli/internal/configcode"
	"github.com/palgroup/palbase-cli/internal/studio"
)

// runConfigPull pulls every module's current config into config/*.toml +
// .palbase/state.json. Partial failure (a module not provisioned for the
// tenant) is non-fatal — it warns and keeps the rest, so a project that
// hasn't enabled a given module still pulls cleanly. Returns an error only
// for a hard failure of the whole pull.
func runConfigPull(ctx context.Context, cwd, ref, branch string, sc *studio.Client, out io.Writer) error {
	fmt.Fprintln(out, "→ pulling config (config/*.toml) ...")
	results, err := configcode.Pull(ctx, cwd, ref, branch, sc)
	if err != nil {
		return fmt.Errorf("config pull: %w", err)
	}
	for _, res := range results {
		if res.Err != nil {
			// Non-fatal: module isn't provisioned for the tenant. Warn,
			// keep the rest — never abort the unified pull over config.
			fmt.Fprintf(out, "  ⚠ %s skipped: %v\n", res.Module, res.Err)
			continue
		}
		fmt.Fprintf(out, "  config/%s (%d bytes)\n", res.Filename, res.Bytes)
	}
	return nil
}

// runConfigPush applies every push-capable module's config/*.toml back to
// the server in one ordered, pre-validated batch (configcode.PushAll). On
// a mid-batch failure the already-applied modules are reported before the
// error is surfaced (they really changed the server — no rollback).
func runConfigPush(ctx context.Context, cwd, ref, branch string, sc *studio.Client, out io.Writer) error {
	fmt.Fprintln(out, "→ pushing config (config/*.toml) ...")
	result, err := configcode.PushAll(ctx, cwd, ref, branch, sc)

	for _, res := range result.Applied {
		printPushResult(res, out)
	}
	for _, m := range result.Skipped {
		fmt.Fprintf(out, "  - %s config skipped (no local config/ file)\n", m)
	}

	if err != nil {
		if errors.Is(err, configcode.ErrStateConflict) {
			// Pre-validate abort: nothing was applied.
			return fmt.Errorf("config push aborted, no changes applied (%s): %w", result.FailedModule, err)
		}
		return fmt.Errorf("config push %s failed (prior modules applied): %w", result.FailedModule, err)
	}
	return nil
}

// printPushResult renders one module's apply outcome.
func printPushResult(res configcode.PushResult, out io.Writer) {
	if res.Set == 0 {
		fmt.Fprintf(out, "  ✓ %s config already up to date\n", res.Module)
	} else {
		fmt.Fprintf(out, "  ✓ %s: applied %d config change(s) from config/%s\n", res.Module, res.Set, res.Filename)
	}
	if len(res.Orphans) > 0 {
		fmt.Fprintf(out, "    ⚠ %d server entr(y/ies) not in config/%s removed/left per module policy: %v\n",
			len(res.Orphans), res.Filename, res.Orphans)
	}
	if len(res.Ignored) > 0 {
		fmt.Fprintf(out, "    ⚠ %d entr(y/ies) in config/%s did not fully round-trip (see module notes): %v\n",
			len(res.Ignored), res.Filename, res.Ignored)
	}
}
