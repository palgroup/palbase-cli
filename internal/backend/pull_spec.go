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

	"github.com/palgroup/palbase-cli/internal/auth"
)

// pullSpecConfigEntry is the single active-env config the Swift SPM plugin turns
// into Palbase-Info.plist — plain JSON so the plugin owns serialization.
type pullSpecConfigEntry struct {
	AppID     string           `json:"app_id"`
	EnvPreset string           `json:"env_preset"`
	BaseURL   string           `json:"base_url"`
	APIKey    string           `json:"api_key"`
	OAuth     *oauthConfigJSON `json:"oauth,omitempty"`
}

// oauthConfigJSON mirrors apps.OAuthConfig field-for-field so the emitted JSON's
// `oauth` block decodes identically to the plist's. Kept local (not a reuse of
// apps.OAuthConfig directly) only so the JSON shape this command commits to is
// explicit and self-documenting.
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

// newSpecCmd (`palbase spec`) is the codegen-split fetcher: it downloads ONLY
// the artifacts SDK code generators consume (openapi.json and platform config)
// — the CLI does NOT generate client
// code; that is the SDKs' job. Today the PalbaseCodegen SPM build-tool plugin
// generates Swift offline over these committed files on every Xcode build.
//
// spec NEVER probes a local `palbase serve` on :4003 — it fetches the
// REMOTE spec for the resolved --ref via the wake-aware fetch. (A future
// --local opt-in could add a serve probe; for now, remote only.)
func newSpecCmd(r Resolvers) *cobra.Command {
	var refFlag, branchFlag, outDir string
	cmd := &cobra.Command{
		Use:   "spec",
		Args:  cobra.NoArgs,
		Short: "Refresh openapi.json — the API contract the SDK code generators consume",
		Long: `Fetch the deployed backend's openapi.json into --out-dir (default ./.palbase).
Run it after every deploy so the committed API contract stays current; the SDK
code generators (the iOS PalbaseCodegen plugin, @palbase/web's palbe-gen)
regenerate from it.

spec ONLY refreshes the API contract. The per-env runtime config
(palbase-config.json — base URLs + keys) is written once by 'palbase ios link'
at setup time and changes only when your app bindings do; re-run 'palbase ios
link' to refresh it, not spec. Native runtime config lives in fixed
.palbase/ios and .palbase/macos slots.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ref, err := resolveOrLinkRef(cmd.Context(), refFlag, r.Studio(), os.Stdout)
			if err != nil {
				return err
			}
			branch := branchFlag
			if branch == "" {
				if cfg, err := auth.LoadProjectConfig(); err == nil && cfg.DefaultEnv != "" {
					branch = cfg.DefaultEnv
				} else {
					branch = "main"
				}
			}
			// spec fetches the API contract ONLY (empty appID → no config).
			// palbase-config.json is `ios link`'s responsibility.
			// resolveOrLinkRef + lookupSpecTarget ride the tRPC studio client
			// (project.list / apikey.reveal); the app bindings + config artifact
			// ride the Management-API REST client. spec passes appID="", so the
			// binding/config seams are never actually reached — but they still
			// construct here.
			return runPullSpec(
				cmd.Context(),
				lookupSpecTarget(r),
				fetchRemoteOpenAPISpec,
				studioBindingLister(r.REST()),
				studioConfigArtifactFetch(r.REST()),
				ref, branch, outDir, "", "",
				os.Stdout,
			)
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to the linked .palbase/config.json; auto-picker in an interactive shell)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to fetch the spec from (defaults to the linked branch, else main)")
	cmd.Flags().StringVar(&outDir, "out-dir", "./.palbase", "Directory to write openapi.json")
	return cmd
}

// specTargetLookup resolves a backend target (URL + publishable key) for an
// (ref, branch). Injected so runPullSpec is testable without a live tRPC server.
type specTargetLookup func(ctx context.Context, ref, branch string) (backendTarget, error)

// remoteSpecFetch fetches the openapi.json bytes from a remote tenant host. Its
// signature matches fetchRemoteOpenAPISpec so the production wire is a direct
// reference; tests inject a stub.
type remoteSpecFetch func(ctx context.Context, specURL, apiKey string, w io.Writer) ([]byte, error)

// lookupSpecTarget binds the production lookupBackendTarget to the resolvers.
func lookupSpecTarget(r Resolvers) specTargetLookup {
	return func(ctx context.Context, ref, branch string) (backendTarget, error) {
		return lookupBackendTarget(ctx, r.Studio(), r.Endpoints(), ref, branch)
	}
}

// runPullSpec is the testable core: resolve the remote target, fetch
// openapi.json (wake-aware, REMOTE only — no :4003 probe), write it, and with a
// non-empty appID also emit its flat app-bound palbase-config.json. App-bound
// fetches use the artifact key; runtime bundle/origin headers are metadata.
func runPullSpec(
	ctx context.Context,
	lookup specTargetLookup,
	fetch remoteSpecFetch,
	list bindingLister,
	cfgFetch configArtifactFetch,
	ref, branch, specOutDir, configOutDir, appID string,
	w io.Writer,
) error {
	target, err := lookup(ctx, ref, branch)
	if err != nil {
		return err
	}

	specURL := target.URL + "/openapi.json"
	specKey := target.APIKey
	var entry *pullSpecConfigEntry
	if appID != "" {
		entry, err = buildPullSpecConfig(ctx, list, cfgFetch, appID, ref, branch)
		if err != nil {
			return err
		}
		// App links fetch with their app-bound key. Runtime bundle/origin
		// metadata is intentionally absent from CLI artifact requests.
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

	// SINGLE-env config: the config is for the ONE active target — the linked
	// env-project (ref) at the requested branch. `ios link` sets it to production;
	// `ios use <branch>` re-fetches it for that branch. The SDK reads a flat
	// {base_url, api_key, ...} object for the platform slot.
	// `spec` (config-less, appID=="") never reaches here.
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

// buildPullSpecConfig fetches the config artifact for the ONE active env — the
// binding whose project_ref == ref, at branchName — and returns it as a single
// flat entry. The active target is what the CLI already selected (`ios link` →
// production, `ios use` → the branch).
func buildPullSpecConfig(
	ctx context.Context,
	list bindingLister,
	fetch configArtifactFetch,
	appID, ref, branchName string,
) (*pullSpecConfigEntry, error) {
	bindings, err := list(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("list app %q bindings: %w", appID, err)
	}
	var refBinding *AppBinding
	for i := range bindings {
		if bindings[i].ProjectRef == ref {
			refBinding = &bindings[i]
			break
		}
	}
	if refBinding == nil {
		return nil, fmt.Errorf("app %q is not bound to project ref %q — run the platform link command again", appID, ref)
	}
	art, err := fetch(ctx, appID, ref, branchName)
	if err != nil {
		return nil, fmt.Errorf("fetch config artifact for %s: %w", ref, err)
	}
	entry := &pullSpecConfigEntry{
		AppID:     art.AppID,
		EnvPreset: art.EnvPreset,
		BaseURL:   art.BaseURL,
		APIKey:    art.APIKey,
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
