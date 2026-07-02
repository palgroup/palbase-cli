package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// doctorCmd is the environment triage verb: one command that answers "why is
// the CLI not working for me" — mode/endpoints, login state, headless PAT,
// project link, and the Node toolchain `serve`/`db types` need. Informative
// only (always exit 0): doctor diagnoses, the failing command still owns its
// error.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the CLI environment (mode, login, link, Node)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ok := func(label, detail string) { fmt.Fprintf(out, "  ✓ %-10s %s\n", label, detail) }
			bad := func(label, detail string) { fmt.Fprintf(out, "  ✗ %-10s %s\n", label, detail) }

			fmt.Fprintf(out, "palbase %s\n", Version)
			ok("mode", fmt.Sprintf("%s (source=%s) — studio %s, api %s",
				resolved.Mode, resolved.Source, resolved.Endpoints.Studio, resolved.Endpoints.PlatformAPI))

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if _, err := authClient.GetValidToken(ctx); err != nil {
				bad("login", fmt.Sprintf("not logged in (%v) — run `palbase login`", err))
			} else {
				ok("login", "session token valid")
			}

			if os.Getenv("PALBASE_ACCESS_TOKEN") != "" {
				ok("pat", "PALBASE_ACCESS_TOKEN set (headless credential)")
			} else {
				ok("pat", "not set (fine for interactive use; CI needs a Dashboard-issued PAT)")
			}

			if cfg, err := auth.LoadProjectConfig(); err == nil && cfg.Ref != "" {
				ok("link", fmt.Sprintf("%s (branch %s) via .palbase/config.json", cfg.Ref, cfg.DefaultEnv))
			} else {
				ok("link", "cwd not linked to a project (project commands need --ref or a linked dir)")
			}

			if node, err := exec.LookPath("node"); err != nil {
				bad("node", "not found on PATH — `palbase serve` and `palbase db types` need Node.js")
			} else {
				v, _ := exec.CommandContext(ctx, node, "--version").Output()
				ok("node", fmt.Sprintf("%s (%s)", strings.TrimSpace(string(v)), node))
			}
			return nil
		},
	}
}

// openCmd opens the Studio dashboard for the current mode in the browser —
// the CLI's escape hatch to the UI.
func openCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open",
		Short: "Open the Studio dashboard in your browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u := resolved.Endpoints.Studio
			fmt.Fprintf(cmd.OutOrStdout(), "Opening %s …\n", u)
			return auth.OpenURL(u)
		},
	}
}
