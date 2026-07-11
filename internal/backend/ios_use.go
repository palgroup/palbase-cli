package backend

// ios_use.go — `palbase ios use <branch>`
//
// Point the linked iOS project at a specific BRANCH of its environment. It
// re-fetches the SPM plugin inputs (openapi.json + palbase-config.json) for
// that branch and records the branch in .palbase/config.json. The shared spec
// is written to `.palbase/openapi.json`; iOS config always goes to
// `.palbase/ios/palbase-config.json`. There is ONE Xcode scheme — switching
// branch is a config swap the
// codegen plugin regenerates from on the next build; project files stay intact.
//
// Requires the project to have been `palbase ios link`'d (so an ios app is
// registered and its id is in .palbase/config.json). A stale registration is
// replaced; otherwise `use` only re-targets the existing wiring.

import (
	"fmt"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// newIOSUseCmd builds `palbase ios use <branch>`.
func newIOSUseCmd(r Resolvers) *cobra.Command {
	return newNativeUseCmd(r, "ios")
}

func newNativeUseCmd(r Resolvers, platform string) *cobra.Command {
	var refFlag string
	label := "iOS"
	if platform == "android" {
		label = "Android"
	}
	longHelp := `Re-target the linked iOS project at <branch> of its environment.

Writes the shared contract to .palbase/openapi.json, the iOS runtime config to
.palbase/ios/palbase-config.json, and records the branch in
.palbase/config.json. Rebuild in Xcode — the codegen plugin regenerates from the
new config; there is one scheme and no Xcode edit.

The project must already be linked with 'palbase ios link' (its ios app id is
read from .palbase/config.json). Run 'palbase ios link' first if it is not.`
	if platform == "android" {
		longHelp = `Re-target the linked Android project at <branch> of its environment.

Writes the shared contract to .palbase/openapi.json, the Android runtime config
to .palbase/android/palbase-config.json, and records the branch in
.palbase/config.json. Rebuild the app — the Gradle plugin regenerates from the
new config.

The project must already be linked with 'palbase android link' (its app id is
read from .palbase/config.json). Run 'palbase android link' first if it is not.`
	}
	cmd := &cobra.Command{
		Use:   "use <branch>",
		Args:  cobra.ExactArgs(1),
		Short: fmt.Sprintf("Point the %s project at a branch — refresh openapi + config for it", label),
		Long:  longHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			branch := args[0]
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// resolveOrLinkRef rides the tRPC studio client (project.list); apps +
			// groups ride the Management-API REST client.
			var sc *studio.Client
			if r.Studio != nil {
				sc = r.Studio()
			}
			var rest restDoer
			if r.REST != nil {
				rest = r.REST()
			}
			ref, err := resolveOrLinkRef(ctx, refFlag, sc, out)
			if err != nil {
				return err
			}

			linkedCfg, _ := auth.LoadProjectConfig()
			if linkedCfg == nil || projectAppID(linkedCfg, platform) == "" {
				return fmt.Errorf("no %s app linked — run `palbase %s link` first", platform, platform)
			}
			appID := projectAppID(linkedCfg, platform)
			grpID, err := resolveNativeGroup(ctx, nativeLinkDeps{rest: rest}, "", ref, out)
			if err != nil {
				return err
			}
			appID, err = resolveNativeApp(ctx, nativeLinkDeps{rest: rest}, grpID, platform, appID, "", out)
			if err != nil {
				return err
			}
			if err := persistProjectAppSlot(ref, platform, appID); err != nil {
				return err
			}
			// Fetch openapi + config FOR THE BRANCH. runPullSpec resolves the
			// branch's endpoint_ref for both the spec host and (via the
			// branch-threaded config artifact) the app-bound base_url + key.
			if err := runPullSpec(ctx,
				lookupSpecTarget(r), fetchRemoteOpenAPISpec,
				studioBindingLister(rest), studioConfigArtifactFetch(rest),
				ref, branch, ".palbase", ".palbase/"+platform, appID,
				out); err != nil {
				return err
			}

			// Record the active branch; the platform app id stays unchanged.
			cfg, _ := auth.LoadProjectConfig()
			if cfg == nil {
				cfg = &auth.ProjectConfig{Ref: ref}
			}
			cfg.DefaultEnv = branch
			if platform == "ios" {
				cfg.IOSAppID = appID
			} else {
				cfg.AndroidAppID = appID
			}
			if err := auth.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("save .palbase/config.json: %w", err)
			}

			if platform == "ios" {
				fmt.Fprintf(out, "✓ iOS project now targets branch %q\n", branch)
				fmt.Fprintln(out, "  rebuild in Xcode — the codegen plugin regenerates from the new config")
				fmt.Fprintf(out, "  ⚠ this build (and any archive) will connect to branch %q until you `palbase ios use <other>`\n", branch)
			} else {
				fmt.Fprintf(out, "✓ Android project now targets branch %q\n", branch)
				fmt.Fprintln(out, "  rebuild the app — the Gradle plugin regenerates from the new config")
				fmt.Fprintf(out, "  ⚠ this build will connect to branch %q until you `palbase android use <other>`\n", branch)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to the linked .palbase/config.json)")
	return cmd
}
