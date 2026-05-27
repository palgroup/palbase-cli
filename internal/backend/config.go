package backend

// `palbase backend config pull` — config-as-code Faz 1 (read-only).
//
// Pulls each module's current config from Studio's tRPC GET APIs into
// declarative TOML under config/ + a .palbase/state.json mirror. The
// per-module serialization lives in the configcode package; this file
// is just the cobra wiring (mirrors the backend group's other commands).
//
// READ-ONLY: there is no push contract yet. The command prints that
// caveat so users don't edit config/ expecting it to deploy (Faz 2).

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/palgroup/palbase-cli/internal/configcode"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// newConfigCmd builds the `palbase backend config` group. Wired into the
// backend command tree alongside init/dev/deploy/… in backend.go.
func newConfigCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Config-as-code: pull module config into config/*.toml",
		Long: `Manage your project's platform config as declarative files.

  palbase backend config pull   Pull current server config into config/*.toml.
  palbase backend config push   Apply a local config/*.toml back to the server.

pull mirrors server state into git-trackable TOML; push applies it back.
Faz 2 push supports the flags module only (others arrive in Faz 3).`,
	}
	cmd.AddCommand(newConfigPullCmd(r))
	cmd.AddCommand(newConfigPushCmd(r))
	return cmd
}

func newConfigPullCmd(r Resolvers) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Pull module config into config/*.toml (read-only mirror)",
		Long: `Fetch each module's current configuration from Studio and write
declarative TOML files under config/ (auth, storage, documents, flags,
notifications), plus a .palbase/state.json mirror.

READ-ONLY: this is a mirror of server state, not a push contract (Faz 2).
Secrets are written as references (@secret/<NAME>), never values.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ref, err := resolveOrLinkRef(ctx, refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			fmt.Printf("→ pulling config for %s ...\n", ref)
			results, err := configcode.Pull(ctx, cwd, ref, r.Studio())
			if err != nil {
				return fmt.Errorf("config pull: %w", err)
			}
			for _, res := range results {
				if res.Err != nil {
					// Non-fatal: this module isn't provisioned for the
					// tenant (e.g. storage tables not created). Warn and
					// keep the rest.
					fmt.Fprintf(os.Stderr, "  ⚠ %s skipped: %v\n", res.Module, res.Err)
					continue
				}
				fmt.Printf("  config/%s (%d bytes)\n", res.Filename, res.Bytes)
			}
			fmt.Printf("  %s\n", configcode.StateFile)
			fmt.Println("✓ config pull complete (read-only mirror — no push contract yet)")
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	return cmd
}

// newConfigPushCmd builds `palbase backend config push` (config-as-code
// Faz 2/3a). It applies local config/*.toml back to the server via each
// module's SET tRPC, gated by client-side state_version conflict detection
// (re-pull + hash-compare against .palbase/state.json).
//
//	config push              → ALL push-capable modules, ordered, with an
//	                           all-or-nothing pre-validate (Faz 3a).
//	config push --module X   → that single module only (back-compat).
//
// Push is idempotent (only changed entries are SET) and conflict-gated
// (an out-of-band dashboard edit since the last pull aborts with no SET).
func newConfigPushCmd(r Resolvers) *cobra.Command {
	var refFlag, moduleFlag string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Apply local config/*.toml back to the server (all modules, or --module X)",
		Long: `Apply local config module files back to the server via their SET tRPC.

With no flag, push applies EVERY push-capable module in one ordered batch:
it first re-pulls + hash-compares each module against .palbase/state.json
(the last pull) and, if ANY module changed out-of-band, ABORTS the whole
batch before a single change — run "config pull" to reconcile first. If all
modules validate, it applies them in order; a mid-batch failure leaves the
already-applied modules in place (no rollback — Faz 3b) and reports which.

--module X restricts the push to a single module (same conflict gate).

Push is idempotent: re-pushing an unchanged file is a no-op.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ref, err := resolveOrLinkRef(ctx, refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			// --module restricts to one module; absent → push all (Faz 3a).
			if cmd.Flags().Changed("module") {
				return runConfigPushOne(ctx, cwd, ref, moduleFlag, r.Studio())
			}
			return runConfigPushAll(ctx, cwd, ref, r.Studio())
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&moduleFlag, "module", "", "Restrict push to a single module (default: all modules)")
	return cmd
}

// runConfigPushOne pushes a single module (the --module path).
func runConfigPushOne(ctx context.Context, cwd, ref, module string, sc *studio.Client) error {
	fmt.Printf("→ pushing %s config for %s ...\n", module, ref)
	res, err := configcode.Push(ctx, cwd, ref, module, sc)
	if err != nil {
		if errors.Is(err, configcode.ErrStateConflict) {
			return fmt.Errorf("push aborted: %w", err)
		}
		if errors.Is(err, configcode.ErrPushNotImplemented) {
			return fmt.Errorf("push: %w", err)
		}
		return fmt.Errorf("config push: %w", err)
	}
	printPushResult(res)
	return nil
}

// runConfigPushAll pushes every push-capable module in one ordered,
// pre-validated batch (the argument-less path).
func runConfigPushAll(ctx context.Context, cwd, ref string, sc *studio.Client) error {
	fmt.Printf("→ pushing all config modules for %s ...\n", ref)
	result, err := configcode.PushAll(ctx, cwd, ref, sc)

	// Report whatever applied BEFORE surfacing an error: on a mid-batch
	// failure the prior modules really did change the server.
	for _, res := range result.Applied {
		printPushResult(res)
	}
	for _, m := range result.Skipped {
		fmt.Fprintf(os.Stderr, "  - %s skipped (no local config/ file)\n", m)
	}

	if err != nil {
		if errors.Is(err, configcode.ErrStateConflict) {
			// Pre-validate abort: nothing was applied.
			return fmt.Errorf("push aborted, no changes applied (%s): %w", result.FailedModule, err)
		}
		return fmt.Errorf("config push %s failed (prior modules applied): %w", result.FailedModule, err)
	}
	if len(result.Applied) == 0 && len(result.Skipped) > 0 {
		fmt.Println("✓ nothing to push (no push-capable module had a local config file)")
	}
	return nil
}

// printPushResult renders one module's apply outcome (shared by the
// single-module and all-modules paths).
func printPushResult(res configcode.PushResult) {
	if res.Set == 0 {
		fmt.Printf("✓ %s already up to date (no changes)\n", res.Module)
	} else {
		fmt.Printf("✓ %s: applied %d change(s) from config/%s\n", res.Module, res.Set, res.Filename)
	}
	if len(res.Orphans) > 0 {
		fmt.Fprintf(os.Stderr, "  ⚠ %d server entr(y/ies) not in config/%s removed/left per module policy: %v\n",
			len(res.Orphans), res.Filename, res.Orphans)
	}
	if len(res.Ignored) > 0 {
		fmt.Fprintf(os.Stderr, "  ⚠ %d entr(y/ies) in config/%s did not fully round-trip (see module notes): %v\n",
			len(res.Ignored), res.Filename, res.Ignored)
	}
}
