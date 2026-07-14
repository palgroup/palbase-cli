package backend

// ios_use.go — `palbase ios|android use <environment>`
//
// Point the linked native project at another ENVIRONMENT of the same Project:
// re-fetch the codegen inputs (openapi.json + palbase-config.json) for it and
// record the selection in .palbase/config.json.
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
	rebuild := "rebuild in Xcode — the codegen plugin regenerates from the new config"
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

Writes the shared contract to .palbase/openapi.json, the %s runtime config to
.palbase/%s/palbase-config.json, and records the environment in
.palbase/config.json. %s.

The project must already be linked with 'palbase %s link' (its app id is read
from .palbase/config.json).`, label, label, platform, rebuild, platform),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// The positional arg IS the environment: resolve through the shared
			// resolver so a slug, a ref, or a display name all work.
			resolver := r.Selection()
			if resolver == nil {
				return fmt.Errorf("no project selected — run `palbase project use <projectId>`")
			}
			resolver.EnvironmentFlag = args[0]
			sel, err := resolver.Resolve(ctx)
			if err != nil {
				return err
			}

			cfg, err := selection.Load("")
			if err != nil {
				return err
			}
			appID := cfg.AppID(platform)
			if appID == "" {
				return fmt.Errorf("no %s app linked — run `palbase %s link` first", platform, platform)
			}

			rest := r.REST()
			appID, err = resolveNativeApp(ctx, nativeLinkDeps{rest: rest}, sel.ProjectID, platform, appID, "", out)
			if err != nil {
				return err
			}
			if err := persistProjectAppSlot(platform, appID); err != nil {
				return err
			}

			if err := runPullSpec(ctx,
				lookupSpecTarget(r), fetchRemoteOpenAPISpec,
				studioBindingLister(rest), studioConfigArtifactFetch(rest),
				sel.Ref(), ".palbase", ".palbase/"+platform, appID,
				out); err != nil {
				return err
			}

			// Record the environment as THE selection: a build made now connects to
			// it, so the local selection and the baked config must not disagree.
			cfg, err = selection.Load("")
			if err != nil {
				return err
			}
			cfg.EnvironmentID = sel.Environment.ID
			if err := selection.Save("", cfg); err != nil {
				return fmt.Errorf("save %s: %w", selection.ConfigPath(""), err)
			}

			fmt.Fprintf(out, "✓ %s project now targets environment %s (%s)\n", label, sel.Environment.Slug, sel.Ref())
			fmt.Fprintf(out, "  %s\n", rebuild)
			fmt.Fprintf(out, "  ⚠ this build (and any archive) connects to %s until you `palbase %s use <other>`\n",
				sel.Environment.Slug, platform)
			return nil
		},
	}
	return cmd
}
