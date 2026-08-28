package backend

// web_use.go — `palbase web use <environment>`
//
// Point the linked web project at another ENVIRONMENT of the same Project:
// re-fetch the codegen inputs (Palbase/openapi.json + Palbase/palbase-config.json)
// for it, record the selection in .palbase/selection.json, and regenerate the
// typed client from the refreshed contract.
//
// selection.json, NOT config.json. The two are different files with different
// owners — `.palbase/config.json` is the EVALUATED config document a push
// carries — and selection/config.go:62 records what the collision cost.
//
// It selects an ENVIRONMENT, never a branch. There is ONE web app — switching
// environments is a config swap, so nothing in the app's own files moves.

import (
	"fmt"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// newWebUseCmd builds `palbase web use <environment>`.
func (wc *webCmd) newWebUseCmd() *cobra.Command {
	var outFlag string

	cmd := &cobra.Command{
		Use:   "use <environment>",
		Args:  cobra.ExactArgs(1),
		Short: "Point the web project at an environment — refresh openapi + config for it",
		Long: `Re-target the linked web project at <environment> (a slug or ref) of the
selected project.

Writes the contract to ` + webArtifactsDir + `/openapi.json, the runtime config to
` + webArtifactsDir + `/palbase-config.json, and records the environment in
.palbase/selection.json. Commit the refreshed ` + webArtifactsDir + `/ files alongside the
regenerated client.

The project must already be linked with 'palbase web link' (its app id is read
from .palbase/selection.json).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A project you run has one environment; switching is a cloud verb.
			if err := refuseUseOnBoundProject("web"); err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			outFile := outFlag
			if outFile == "" {
				outFile = "palbe.gen.ts"
			}

			// The positional arg IS the environment: resolve its exact slug or ref
			// through the shared resolver.
			resolver := wc.r.Selection()
			if resolver == nil {
				// NOT `--environment <ref>`, which is what this offered: the
				// flag narrows an already-selected project and cannot name one.
				return fmt.Errorf(
					"no project selected — run `palbase link <project>`, or `palbase link <ref>` for one environment of it")
			}
			cfg, err := selection.Load("")
			if err != nil {
				return err
			}
			if resolver.ProjectFlag != "" && resolver.ProjectFlag != cfg.ProjectID {
				return fmt.Errorf(
					"web use cannot switch projects from %s to %s — run `palbase link %s` first",
					cfg.ProjectID, resolver.ProjectFlag, resolver.ProjectFlag,
				)
			}
			if cfg.AppID("web") == "" {
				return fmt.Errorf("no web app linked — run `palbase web link` first")
			}
			resolver.EnvironmentFlag = args[0]
			sel, err := resolver.Resolve(ctx)
			if err != nil {
				return err
			}
			if sel.ProjectID != cfg.ProjectID {
				return fmt.Errorf("resolved environment belongs to project %s, not linked project %s", sel.ProjectID, cfg.ProjectID)
			}

			// Same seam `web link` fetches through — it resolves the app, writes
			// both committed artifacts, and (with retarget) records the new
			// Environment as this checkout's selection.
			if err := webLinkArtifacts(ctx, wc.r, sel, true, out); err != nil {
				return fmt.Errorf("fetch artifacts: %w", err)
			}

			// The generated client embeds the environment's URL and key, so it is
			// STALE the moment the config moves: regenerate now rather than leave
			// an app that still talks to the previous environment.
			generated, err := runPalbeGen(ctx, outFile, out)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "✓ web project now targets environment %s (%s)\n", sel.Environment.Slug, sel.EnvironmentRef())
			if !generated {
				fmt.Fprintf(out, "  ⚠ %s still points at the previous environment — install @palbase/web, then run `npx palbe-gen`\n", outFile)
			}
			fmt.Fprintf(out, "  ⚠ every build connects to %s until you `palbase web use <other>`\n", sel.Environment.Slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&outFlag, "out", "", "Gen file to regenerate (default: palbe.gen.ts)")
	return cmd
}
