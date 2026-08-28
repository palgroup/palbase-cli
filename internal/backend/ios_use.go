package backend

// ios_use.go — `palbase ios|android use <environment>`
//
// Point the linked native project at another ENVIRONMENT of the same Project:
// re-fetch the codegen inputs (one contract per environment + palbase-config.json) and
// record the selection in .palbase/selection.json.
//
// It selects an ENVIRONMENT, never a branch. There is ONE Xcode scheme / Gradle
// module — switching environments is a config swap the codegen plugin
// regenerates from on the next build; project files stay intact.

import (
	"fmt"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// newIOSUseCmd builds `palbase ios use <environment>`.
func newIOSUseCmd(r Resolvers) *cobra.Command {
	return newNativeUseCmd(r, "ios")
}

func newNativeUseCmd(r Resolvers, platform string) *cobra.Command {
	label := "iOS"
	// The iOS build-tool plugin was retired: the generated client is written to
	// Palbase/Generated/ by THIS command and committed, so nothing regenerates
	// at build time.
	rebuild := "commit the refreshed Palbase/Generated/ output alongside .palbase/"
	if platform == "android" {
		label = "Android"
		rebuild = "rebuild the app — the Gradle plugin regenerates from the new config"
	}
	cmd := &cobra.Command{
		Use:   "use <environment>",
		Args:  cobra.ExactArgs(1),
		Short: fmt.Sprintf("Point the %s project at an environment — refresh openapi + config for it", label),
		Long: fmt.Sprintf(`Re-target the linked %s project at <environment> (a slug or ref) of the
selected project.

Writes one contract per environment to .palbase/openapi/, the %s runtime config to
.palbase/%s/palbase-config.json, and records the environment in
.palbase/selection.json. %s.

The project must already be linked with 'palbase %s link' (its app id is read
from .palbase/selection.json, falling back to the committed platform slot).`, label, label, platform, rebuild, platform),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// A project you run has one environment; switching is a cloud verb.
			if err := refuseUseOnBoundProject(platform); err != nil {
				return err
			}

			// The positional arg IS the environment: resolve its exact slug or ref
			// through the shared resolver.
			resolver := r.Selection()
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
					"%s use cannot switch projects from %s to %s — run `palbase link %s` first",
					platform, cfg.ProjectID, resolver.ProjectFlag, resolver.ProjectFlag,
				)
			}
			resolver.EnvironmentFlag = args[0]
			sel, err := resolver.Resolve(ctx)
			if err != nil {
				return err
			}
			if sel.ProjectID != cfg.ProjectID {
				return fmt.Errorf("resolved environment belongs to project %s, not linked project %s", sel.ProjectID, cfg.ProjectID)
			}
			appID := cfg.AppID(platform)
			if appID == "" {
				return fmt.Errorf("no %s app linked — run `palbase %s link` first", platform, platform)
			}
			// Re-targeting rewrites the config the committed Swift client embeds,
			// so it must be re-emitted. Check that it can be before moving
			// anything (link is exempt: on a first link the SDK package is not
			// added yet, and having no client to regenerate is normal there).
			if err := preflightAppleGenerator(".palbase"); err != nil {
				return err
			}

			rest := r.REST()
			appID, err = resolveNativeApp(ctx, nativeLinkDeps{rest: rest}, sel.ProjectID, platform, appID, "", out)
			if err != nil {
				return err
			}
			if err := persistProjectAppSlot(platform, appID, &sel, true); err != nil {
				return err
			}

			// `use` moves the DEFAULT, and rewrites every environment beside it:
			// the checkout carries them all, so the answer to "which one does a
			// plain build get" is the only thing that changes here.
			deps := cloudEnvDeps{
				fetch:      managedSpecFetch(rest),
				list:       studioBindingLister(rest),
				cfgFetch:   studioConfigArtifactFetch(rest, r.Endpoints().PublicHost),
				freshness:  studioSpecFreshness(rest, sel.ProjectID, sel.EnvironmentRef()),
				publicHost: r.Endpoints().PublicHost,
			}
			if err := linkNativeEnvironments(
				ctx, deps, platform, appID, sel.EnvironmentRef(), sel.ProjectID, out,
			); err != nil {
				return err
			}

			fmt.Fprintf(out, "✓ %s project now targets environment %s (%s)\n", label, sel.Environment.Slug, sel.EnvironmentRef())
			fmt.Fprintf(out, "  %s\n", rebuild)
			fmt.Fprintf(out, "  ⚠ this build (and any archive) connects to %s until you `palbase %s use <other>`\n",
				sel.Environment.Slug, platform)
			return nil
		},
	}
	return cmd
}
