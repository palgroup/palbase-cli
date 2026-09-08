package backend

import (
	"path/filepath"

	"github.com/spf13/cobra"
)

// nativeArtifactsDir is the committed directory the NATIVE SDK generators read:
// the per-environment contracts under openapi/ plus one per-platform slot (.palbase/ios, /macos,
// /android). The web SDK reads its own directory instead — webArtifactsDir.
const nativeArtifactsDir = ".palbase"

// webArtifactsDir is the committed directory the WEB SDK generator reads —
// openapi.json + palbase-config.json under ./Palbase. It lived in web_link.go
// until the `palbase web` command group was retired (FR-009); the directory
// outlived the command because `link`, `spec` and `pull` all write into it.
const webArtifactsDir = "Palbase"

// linkedPlatforms reports which platforms this checkout is linked for, read from
// the COMMITTED slot files rather than from `.palbase/config.json` (which is
// per-machine and gitignored, so a fresh clone has none) and rather than from a
// guess about the directory layout.
//
// This is what makes ONE `spec` correct: the command does not ask you which
// platform you are on, and it does not sniff a sibling directory relative to a
// --out-dir that may have moved. It reads the same files the link commands
// wrote and the repo carries.
func linkedPlatforms() (web bool, apple bool, android bool) {
	web = isRegularFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
	apple = (isRegularFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json")) ||
		isRegularFile(filepath.Join(nativeArtifactsDir, "macos", "palbase-config.json"))) &&
		hasAppleProject(".")
	android = isRegularFile(filepath.Join(nativeArtifactsDir, "android", "palbase-config.json"))
	return web, apple, android
}

// hasAppleProject reports whether THIS checkout is one an Apple client can be
// generated in — that is, whether it holds an Xcode project at all.
//
// A CONFIG FILE IS NOT THE PROOF. `apple` used to be "the ios slot file exists",
// and a backend repository can hold that file legitimately: somebody ran
// `palbase link --platform ios` there once and committed it. Measured on a real
// customer repo (08.09.2026, centauri): every `palbase push` ended with
//
//	the push landed, but the client could not be regenerated: … the
//	palbackend-ios checkout is not resolved for this project yet
//
// on every push, forever — because the app lives in a SEPARATE repository and
// this one has no Xcode project to resolve. The generator was being asked to run
// where it can never run.
//
// The distinction this restores: no Xcode project means the client is generated
// SOMEWHERE ELSE and nothing is wrong here; an Xcode project that is not yet
// resolved is a real error and still says "build once in Xcode".
func hasAppleProject(root string) bool {
	for _, ext := range []string{"*.xcodeproj", "*.xcworkspace"} {
		if m, _ := filepath.Glob(filepath.Join(root, ext)); len(m) > 0 {
			return true
		}
	}
	return false
}

// newSpecCmd (`palbase spec`) fetches ONLY the artifact the SDK code generators
// consume: the SELECTED ENVIRONMENT's openapi.json. It writes it wherever this
// checkout's linked platforms read it from, and then triggers the ONE generator
// the CLI owns — the SDK's swiftgen, for an Apple checkout.
//
// It is a single command because fetching the contract is a single act. The
// platforms differ only in (a) which directory their generator reads and (b)
// who regenerates afterwards, and both of those are facts about the checkout,
// not questions for the caller: web's palbe-gen runs from the predev/prebuild
// hook, Android's Gradle plugin runs at build time, and only Apple has no build
// step of its own — which is why the CLI runs swiftgen here and nowhere else.
//
// spec NEVER probes a local server on :4003 — it fetches the REMOTE
// spec via the wake-aware fetch.
func newSpecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec",
		Args:  cobra.NoArgs,
		Short: "Refresh openapi.json for the selected environment (and regenerate the committed Swift client)",
		Long: `Fetch the SELECTED environment's openapi.json into every directory this
checkout's linked platforms read it from. Run it after every deploy so the
committed API contract stays current.

  web      → ` + webArtifactsDir + `/openapi.json   (` + "`palbe-gen`" + ` regenerates palbe.gen.ts from it,
                                    via the predev/prebuild hook or by hand)
  ios/macos→ ` + nativeArtifactsDir + `/openapi/<env>.json  ONE PER ENVIRONMENT, then regenerates
                                    Palbase/Generated/ — PalbaseGenerated.swift +
                                    Palbase-Info.plist — using the generator from the
                                    palbackend-ios checkout SwiftPM resolved for this
                                    project. Commit the result.
  android  → ` + nativeArtifactsDir + `/openapi/<env>.json  (the Gradle plugin regenerates on the next build)

Which of those run is read from the COMMITTED slot files the link commands
wrote (` + webArtifactsDir + `/palbase-config.json, ` + nativeArtifactsDir + `/<platform>/palbase-config.json),
so a fresh clone behaves the same as the machine that linked it.

spec does NOT write the per-environment runtime config (palbase-config.json —
base URL + key). Run ` + "`palbase link <ref>`" + ` to refresh that configuration.

This acts on the project this checkout is bound to. There is one addressing
mechanism — run ` + "`palbase link <ref>`" + ` to point the checkout at another
project.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := PrintTargetFor(cmd); err != nil {
				return err
			}
			return RefreshSpec(cmd.Context(), cmd.OutOrStdout())
		},
	}
	return cmd
}
