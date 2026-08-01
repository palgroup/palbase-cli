package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/apps"
)

// pullSpecConfigEntry is the single-ENVIRONMENT config the SDK code generators
// consume (the Swift SPM plugin turns it into Palbase-Info.plist; the Gradle
// plugin and @palbase/web read it directly).
//
// It names the Environment — `environment_ref` — and carries NO branch. The URL
// and the API key already identify the Environment; a `branch` field would be a
// second, drift-prone name for the same runtime, and the Palbase branch no
// longer exists.
type pullSpecConfigEntry struct {
	AppID          string                    `json:"app_id"`
	EnvironmentRef string                    `json:"environment_ref"`
	Kind           string                    `json:"kind"`
	BaseURL        string                    `json:"base_url"`
	APIKey         string                    `json:"api_key"`
	OAuth          *oauthConfigJSON          `json:"oauth,omitempty"`
	Integrity      *apps.IntegrityConfig     `json:"integrity,omitempty"`
	Notifications  *apps.NotificationsConfig `json:"notifications,omitempty"`
}

// oauthConfigJSON mirrors apps.OAuthConfig field-for-field so the emitted JSON's
// `oauth` block decodes identically to the plist's.
type oauthConfigJSON struct {
	Apple  *oauthAppleJSON  `json:"apple,omitempty"`
	Google *oauthGoogleJSON `json:"google,omitempty"`
}

type oauthAppleJSON struct {
	Enabled bool `json:"enabled"`
}

type oauthGoogleJSON struct {
	Enabled     bool   `json:"enabled"`
	ClientID    string `json:"client_id"`
	RedirectURI string `json:"redirect_uri"`
}

// nativeArtifactsDir is the committed directory the NATIVE SDK generators read:
// the shared openapi.json plus one per-platform slot (.palbase/ios, /macos,
// /android). The web SDK reads its own directory instead — webArtifactsDir.
const nativeArtifactsDir = ".palbase"

// newNativeSpecCmd (`palbase ios|macos|android spec`) fetches ONLY the artifact
// the SDK code generators consume: the SELECTED ENVIRONMENT's openapi.json.
// Client generation itself is the SDKs' job — for Apple that means the SDK's own
// swiftgen, which this command then runs over the refreshed spec.
//
// spec NEVER probes a local server on :4003 — it fetches the REMOTE
// spec via the wake-aware fetch.
func newNativeSpecCmd(r Resolvers, platform string) *cobra.Command {
	var outDir string
	regen := `A checkout with an Apple platform slot (` + nativeArtifactsDir + `/ios or ` + nativeArtifactsDir + `/macos) then
regenerates Palbase/Generated/ — PalbaseGenerated.swift + Palbase-Info.plist —
using the generator from the palbackend-ios checkout SwiftPM resolved for this
project. Commit the result.`
	if platform == "android" {
		regen = `The Gradle plugin regenerates from the refreshed spec on the next build.`
	}
	cmd := &cobra.Command{
		Use:   "spec",
		Args:  cobra.NoArgs,
		Short: "Refresh openapi.json for the selected environment (and regenerate the committed client)",
		Long: `Fetch the SELECTED environment's openapi.json into --out-dir (default ./` + nativeArtifactsDir + `).
Run it after every deploy so the committed API contract stays current.

` + regen + `

spec does NOT write the per-environment runtime config (palbase-config.json —
base URL + key). That comes from ` + "`palbase " + platform + " link`" + ` and is re-written by
` + "`palbase " + platform + " use <environment>`" + `.

Override the target with the global --project / --environment flags.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sel, err := r.resolve(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// spec fetches the API contract ONLY (empty appID → no config).
			if err := runPullSpec(
				cmd.Context(),
				lookupSpecTarget(r),
				fetchRemoteOpenAPISpec,
				studioBindingLister(r.REST()),
				studioConfigArtifactFetch(r.REST(), r.Endpoints().PublicHost),
				sel.EnvironmentRef(), r.Endpoints().PublicHost, outDir, "", "",
				out,
			); err != nil {
				return err
			}
			if platform == "android" {
				return nil
			}
			return generateAppleClient(outDir, out)
		},
	}
	cmd.Flags().StringVar(&outDir, "out-dir", "./"+nativeArtifactsDir, "Directory to write openapi.json")
	return cmd
}

// newWebSpecCmd (`palbase web spec`) refreshes the contract in the directory
// @palbase/web's palbe-gen reads — Palbase/, never .palbase/ — and emits no
// client: generating palbe.gen.ts is palbe-gen's job, run by the predev/prebuild
// hook `palbase web link` installs (or `npx palbe-gen` by hand).
func newWebSpecCmd(r Resolvers) *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "spec",
		Args:  cobra.NoArgs,
		Short: "Refresh openapi.json for the selected environment",
		Long: `Fetch the SELECTED environment's openapi.json into --out-dir (default ./` + webArtifactsDir + `).
Run it after every deploy so the committed API contract stays current.

Regenerating the typed client from it is ` + "`palbe-gen`" + `'s job (shipped in
@palbase/web): the predev/prebuild hook ` + "`palbase web link`" + ` installs runs it on
every dev run and build, and ` + "`npx palbe-gen`" + ` does it on demand.

spec does NOT write the per-environment runtime config (palbase-config.json —
base URL + key). That comes from ` + "`palbase web link`" + ` and is re-written by
` + "`palbase web use <environment>`" + `.

Override the target with the global --project / --environment flags.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sel, err := r.resolve(cmd.Context())
			if err != nil {
				return err
			}
			return runPullSpec(
				cmd.Context(),
				lookupSpecTarget(r),
				fetchRemoteOpenAPISpec,
				studioBindingLister(r.REST()),
				studioConfigArtifactFetch(r.REST(), r.Endpoints().PublicHost),
				sel.EnvironmentRef(), r.Endpoints().PublicHost, outDir, "", "",
				cmd.OutOrStdout(),
			)
		},
	}
	cmd.Flags().StringVar(&outDir, "out-dir", "./"+webArtifactsDir, "Directory to write openapi.json")
	return cmd
}

// specTargetLookup resolves a backend target (URL + publishable key) for an
// ENVIRONMENT ref. Injected so runPullSpec is testable without a live server.
type specTargetLookup func(ctx context.Context, environmentRef string) (backendTarget, error)

// remoteSpecFetch fetches the openapi.json bytes from a remote tenant host.
type remoteSpecFetch func(ctx context.Context, specURL, apiKey string, w io.Writer) ([]byte, error)

// lookupSpecTarget binds the production lookupBackendTarget to the resolvers.
func lookupSpecTarget(r Resolvers) specTargetLookup {
	return func(ctx context.Context, environmentRef string) (backendTarget, error) {
		return lookupBackendTarget(ctx, r.Studio(), r.Endpoints(), environmentRef)
	}
}

// runPullSpec is the testable core: resolve the Environment's remote target,
// fetch openapi.json (wake-aware, REMOTE only), write it, and with a non-empty
// appID also emit that app's palbase-config.json for the SAME Environment.
func runPullSpec(
	ctx context.Context,
	lookup specTargetLookup,
	fetch remoteSpecFetch,
	list bindingLister,
	cfgFetch configArtifactFetch,
	environmentRef, publicHost, specOutDir, configOutDir, appID string,
	w io.Writer,
) error {
	target, err := lookup(ctx, environmentRef)
	if err != nil {
		return err
	}

	specURL := target.URL + "/openapi.json"
	specKey := target.APIKey
	var entry *pullSpecConfigEntry
	if appID != "" {
		entry, err = buildPullSpecConfig(ctx, list, cfgFetch, appID, environmentRef, publicHost)
		if err != nil {
			return err
		}
		// App links fetch with their app-bound key.
		specURL = strings.TrimRight(entry.BaseURL, "/") + "/openapi.json"
		specKey = entry.APIKey
	}

	specBytes, err := fetch(ctx, specURL, specKey, w)
	if err != nil {
		return err
	}
	if specOutDir == "" {
		specOutDir = "./.palbase"
	}
	if err := os.MkdirAll(specOutDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", specOutDir, err)
	}
	specPath := filepath.Join(specOutDir, "openapi.json")
	if err := os.WriteFile(specPath, specBytes, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", specPath, err)
	}
	fmt.Fprintf(w, "✓ wrote %s\n", specPath)

	if appID != "" {
		if configOutDir == "" {
			configOutDir = specOutDir
		}
		if err := os.MkdirAll(configOutDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", configOutDir, err)
		}

		// SINGLE-environment config: the ONE active target the CLI selected. The
		// SDK reads a flat {environment_ref, base_url, api_key, ...} object.
		data, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal palbase-config.json: %w", err)
		}
		cfgPath := filepath.Join(configOutDir, "palbase-config.json")
		if err := os.WriteFile(cfgPath, append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", cfgPath, err)
		}
		fmt.Fprintf(w, "✓ wrote %s (%s)\n", cfgPath, entry.BaseURL)
	}

	// Client generation is NOT done here: this core is platform-agnostic, and
	// each platform's spec/link/use command owns the regeneration that follows
	// it (generateAppleClient for Apple, palbe-gen for web, the Gradle plugin
	// for Android). Every one of those callers regenerates right after this
	// returns, so none of them leaves a spec on disk that the committed client
	// no longer reflects.
	return nil
}

// buildPullSpecConfig fetches the config artifact for the ONE selected
// Environment — the binding whose environment_ref == environmentRef.
func buildPullSpecConfig(
	ctx context.Context,
	list bindingLister,
	fetch configArtifactFetch,
	appID, environmentRef, publicHost string,
) (*pullSpecConfigEntry, error) {
	bindings, err := list(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("list app %q bindings: %w", appID, err)
	}
	bound := false
	for i := range bindings {
		if bindings[i].EnvironmentRef == environmentRef {
			bound = true
			break
		}
	}
	if !bound {
		return nil, fmt.Errorf("app %q is not bound to environment %q — run the platform link command again", appID, environmentRef)
	}
	art, err := fetch(ctx, appID, environmentRef)
	if err != nil {
		return nil, fmt.Errorf("fetch config artifact for %s: %w", environmentRef, err)
	}
	if err := apps.ValidateConfigArtifact(art, appID, environmentRef, publicHost); err != nil {
		return nil, err
	}
	entry := &pullSpecConfigEntry{
		AppID:          art.AppID,
		EnvironmentRef: art.EnvironmentRef,
		Kind:           art.Kind,
		BaseURL:        art.BaseURL,
		APIKey:         art.APIKey,
		Integrity:      art.Integrity,
		Notifications:  art.Notifications,
	}
	if art.OAuth != nil {
		oc := &oauthConfigJSON{}
		if art.OAuth.Apple != nil {
			oc.Apple = &oauthAppleJSON{Enabled: art.OAuth.Apple.Enabled}
		}
		if art.OAuth.Google != nil {
			oc.Google = &oauthGoogleJSON{
				Enabled:     art.OAuth.Google.Enabled,
				ClientID:    art.OAuth.Google.ClientID,
				RedirectURI: art.OAuth.Google.RedirectURI,
			}
		}
		if oc.Apple != nil || oc.Google != nil {
			entry.OAuth = oc
		}
	}
	return entry, nil
}
