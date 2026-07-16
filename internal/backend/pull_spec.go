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

// newSpecCmd (`palbase spec`) fetches ONLY the artifact the SDK code generators
// consume: the SELECTED ENVIRONMENT's openapi.json. The CLI does not generate
// client code — that is the SDKs' job.
//
// spec NEVER probes a local `palbase serve` on :4003 — it fetches the REMOTE
// spec via the wake-aware fetch.
func newSpecCmd(r Resolvers) *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "spec",
		Args:  cobra.NoArgs,
		Short: "Refresh openapi.json — the API contract the SDK code generators consume",
		Long: `Fetch the SELECTED environment's openapi.json into --out-dir (default ./.palbase).
Run it after every deploy so the committed API contract stays current; the SDK
code generators (the iOS PalbaseCodegen plugin, @palbase/web's palbe-gen, the
Android Gradle plugin) regenerate from it.

spec ONLY refreshes the API contract. The per-environment runtime config
(palbase-config.json — base URL + key) is written by the platform link commands
and re-written by ` + "`palbase ios|android use <environment>`" + `.

Override the target with the global --project / --environment flags.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sel, err := r.resolve(cmd.Context())
			if err != nil {
				return err
			}
			// spec fetches the API contract ONLY (empty appID → no config).
			return runPullSpec(
				cmd.Context(),
				lookupSpecTarget(r),
				fetchRemoteOpenAPISpec,
				studioBindingLister(r.REST()),
				studioConfigArtifactFetch(r.REST()),
				sel.EnvironmentRef(), outDir, "", "",
				cmd.OutOrStdout(),
			)
		},
	}
	cmd.Flags().StringVar(&outDir, "out-dir", "./.palbase", "Directory to write openapi.json")
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
	environmentRef, specOutDir, configOutDir, appID string,
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
		entry, err = buildPullSpecConfig(ctx, list, cfgFetch, appID, environmentRef)
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

	if appID == "" {
		return nil
	}
	if configOutDir == "" {
		configOutDir = specOutDir
	}
	if err := os.MkdirAll(configOutDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", configOutDir, err)
	}

	// SINGLE-environment config: the ONE active target the CLI selected. The SDK
	// reads a flat {environment_ref, base_url, api_key, ...} object.
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal palbase-config.json: %w", err)
	}
	cfgPath := filepath.Join(configOutDir, "palbase-config.json")
	if err := os.WriteFile(cfgPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	fmt.Fprintf(w, "✓ wrote %s (%s)\n", cfgPath, entry.BaseURL)
	return nil
}

// buildPullSpecConfig fetches the config artifact for the ONE selected
// Environment — the binding whose environment_ref == environmentRef.
func buildPullSpecConfig(
	ctx context.Context,
	list bindingLister,
	fetch configArtifactFetch,
	appID, environmentRef string,
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
	if err := apps.ValidateConfigArtifact(art, appID, environmentRef); err != nil {
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
