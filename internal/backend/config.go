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

Faz 1 is READ-ONLY — pull mirrors server state into git-trackable TOML;
there is no push contract yet. Editing config/ does not change the server.`,
	}
	cmd.AddCommand(newConfigPullCmd(r))
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
