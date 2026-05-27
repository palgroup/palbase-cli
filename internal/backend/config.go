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
	"errors"
	"fmt"
	"os"

	"github.com/palgroup/palbase-cli/internal/configcode"
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
// Faz 2). It applies a single module's local config/*.toml back to the
// server via that module's SET tRPC, gated by client-side state_version
// conflict detection (re-pull + hash-compare against .palbase/state.json).
//
// Faz 2 supports the flags module only (--module flags, the default).
// Push is UPSERT-only: server flags absent from the local file are NOT
// deleted (reported as a warning) — destructive sync is Faz 3.
func newConfigPushCmd(r Resolvers) *cobra.Command {
	var refFlag, moduleFlag string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Apply a local config/*.toml back to the server (flags only — Faz 2)",
		Long: `Apply a local config module file back to the server via its SET tRPC.

Before applying, push re-pulls the module's current server state and
compares it to the hash recorded in .palbase/state.json at the last pull.
If the server changed out-of-band since then (e.g. a dashboard edit), the
push is REJECTED with no change — run "config pull" to reconcile first.

Push is UPSERT-only and idempotent: it sets only entries that differ from
the server, so re-pushing an unchanged file is a no-op. Server entries
absent from the local file are NOT deleted (Faz 3).

Faz 2 supports the flags module only.`,
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

			fmt.Printf("→ pushing %s config for %s ...\n", moduleFlag, ref)
			res, err := configcode.Push(ctx, cwd, ref, moduleFlag, r.Studio())
			if err != nil {
				if errors.Is(err, configcode.ErrStateConflict) {
					return fmt.Errorf("push aborted: %w", err)
				}
				if errors.Is(err, configcode.ErrPushNotImplemented) {
					return fmt.Errorf("push: %w", err)
				}
				return fmt.Errorf("config push: %w", err)
			}

			if res.Set == 0 {
				fmt.Printf("✓ %s already up to date (no changes)\n", res.Module)
			} else {
				fmt.Printf("✓ %s: applied %d change(s) from config/%s\n", res.Module, res.Set, res.Filename)
			}
			if len(res.Orphans) > 0 {
				fmt.Fprintf(os.Stderr, "  ⚠ %d server flag(s) not in config/%s left untouched (push is upsert-only; deletion is Faz 3): %v\n",
					len(res.Orphans), res.Filename, res.Orphans)
			}
			if len(res.Ignored) > 0 {
				fmt.Fprintf(os.Stderr, "  ⚠ variants on %d flag(s) were NOT pushed (the SET API has no variants field; Faz 3): %v\n",
					len(res.Ignored), res.Ignored)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json)")
	cmd.Flags().StringVar(&moduleFlag, "module", "flags", "Module to push (Faz 2: flags only)")
	return cmd
}
