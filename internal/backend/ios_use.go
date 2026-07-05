package backend

// ios_use.go — `palbase ios use <branch>`
//
// Point the linked iOS project at a specific BRANCH of its environment. It
// re-fetches the SPM plugin inputs (openapi.json + palbase-config.json) for
// that branch and records the branch as the project's active target in
// .palbase/config.json, so a later `palbase spec` refreshes from the same
// branch. There is ONE Xcode scheme — switching branch is a config swap the
// codegen plugin regenerates from on the next build; the CLI never touches the
// .xcodeproj / .xcscheme / .xcconfig.
//
// Requires the project to have been `palbase ios link`'d (so an ios app is
// registered and its id is in .palbase/config.json). `use` never registers or
// binds — that is link's job; it only re-targets an existing wiring.

import (
	"fmt"
	"os"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// newIOSUseCmd builds `palbase ios use <branch>`.
func newIOSUseCmd(r Resolvers) *cobra.Command {
	var refFlag, groupFlag, appFlag, outDir string
	cmd := &cobra.Command{
		Use:   "use <branch>",
		Args:  cobra.ExactArgs(1),
		Short: "Point the iOS project at a branch — refresh openapi + config for it",
		Long: `Re-target the linked iOS project at <branch> of its environment.

Fetches openapi.json + palbase-config.json for that branch into ./.palbase and
records it as the active target in .palbase/config.json, so a later
'palbase spec' refreshes from the same branch. Rebuild in Xcode — the codegen
plugin regenerates from the new config; there is one scheme and no Xcode edit.

The project must already be linked with 'palbase ios link' (its ios app id is
read from .palbase/config.json). Run 'palbase ios link' first if it is not.`,
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

			// The ios app id: --app wins, else the linked project config
			// (written by `ios link`), else resolve it read-only from the group.
			appID := appFlag
			if appID == "" {
				if cfg, cfgErr := auth.LoadProjectConfig(); cfgErr == nil {
					appID = cfg.IOSAppID
				}
			}
			if appID == "" {
				if rest == nil {
					return fmt.Errorf("no ios app linked — run `palbase ios link` first, or pass --app <appId>")
				}
				// Group derives from the resolved project ref — no group prompt.
				grpID, gerr := resolveIOSGroup(ctx, iosLinkDeps{rest: rest, stdin: os.Stdin, interactive: isInteractive()}, groupFlag, ref, out)
				if gerr != nil {
					return gerr
				}
				appID, err = resolveExistingIOSApp(ctx, rest, grpID, out)
				if err != nil {
					return err
				}
				if appID == "" {
					return fmt.Errorf("no ios app registered in this group — run `palbase ios link` first")
				}
			}

			if outDir == "" {
				outDir = "./.palbase"
			}
			// Fetch openapi + config FOR THE BRANCH. runPullSpec resolves the
			// branch's endpoint_ref for both the spec host and (via the
			// branch-threaded config artifact) every bundle-id entry's base_url +
			// key, so the whole ./.palbase pair points at <branch>.
			if err := runPullSpec(ctx,
				lookupSpecTarget(r), fetchRemoteOpenAPISpec,
				studioBindingLister(rest), studioConfigArtifactFetch(rest),
				ref, branch, outDir, appID,
				out); err != nil {
				return err
			}

			// Record the active target so `palbase spec` (and a re-run of `use`)
			// default to this branch. Persist the app id too if we resolved it.
			cfg, _ := auth.LoadProjectConfig()
			if cfg == nil {
				cfg = &auth.ProjectConfig{Ref: ref}
			}
			cfg.DefaultEnv = branch
			if cfg.IOSAppID == "" {
				cfg.IOSAppID = appID
			}
			if err := auth.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("save .palbase/config.json: %w", err)
			}

			fmt.Fprintf(out, "✓ iOS project now targets branch %q\n", branch)
			fmt.Fprintln(out, "  rebuild in Xcode — the codegen plugin regenerates from the new config")
			fmt.Fprintf(out, "  ⚠ this build (and any archive) will connect to branch %q until you `palbase ios use <other>`\n", branch)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to the linked .palbase/config.json)")
	cmd.Flags().StringVar(&groupFlag, "group", "", "Group id (only used if no ios app id is linked yet)")
	cmd.Flags().StringVar(&appFlag, "app", "", "App id (defaults to the linked project's ios app)")
	cmd.Flags().StringVar(&outDir, "out-dir", "./.palbase", "Directory to write openapi.json + palbase-config.json")
	return cmd
}
