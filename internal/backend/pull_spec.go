package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// pullSpecConfigEntry is the per-bundle-id config the Swift SPM plugin turns
// into the per-env Palbase-Info.plist (one dict per registered bundle id) —
// plain JSON so the plugin owns the plist serialization.
type pullSpecConfigEntry struct {
	AppID      string           `json:"app_id"`
	Identifier string           `json:"identifier"`
	EnvPreset  string           `json:"env_preset"`
	BaseURL    string           `json:"base_url"`
	APIKey     string           `json:"api_key"`
	OAuth      *oauthConfigJSON `json:"oauth,omitempty"`
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
// the artifacts SDK code generators consume (openapi.json, and with --app a
// bundle-id-keyed palbase-config.json) — the CLI does NOT generate client
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
		Long: `Fetch the deployed backend's openapi.json into --out-dir (default ./Palbase).
Run it after every deploy so the committed API contract stays current; the SDK
code generators (the iOS PalbaseCodegen plugin, @palbase/web's palbe-gen)
regenerate from it.

spec ONLY refreshes the API contract. The per-env runtime config
(palbase-config.json — base URLs + keys) is written once by 'palbase ios link'
at setup time and changes only when your app bindings do; re-run 'palbase ios
link' to refresh it, not spec.`,
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
				ref, branch, outDir, "",
				false, // spec: appID=="" so config isn't built; branch is a default, not a re-target demand
				os.Stdout,
			)
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to the linked .palbase/config.json; auto-picker in an interactive shell)")
	cmd.Flags().StringVar(&branchFlag, "branch", "", "Branch to fetch the spec from (defaults to the linked branch, else main)")
	cmd.Flags().StringVar(&outDir, "out-dir", "./Palbase", "Directory to write openapi.json")
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
// non-empty appID also emit the bundle-id-keyed palbase-config.json.
func runPullSpec(
	ctx context.Context,
	lookup specTargetLookup,
	fetch remoteSpecFetch,
	list bindingLister,
	cfgFetch configArtifactFetch,
	ref, branch, outDir, appID string,
	requireBranchApplied bool,
	w io.Writer,
) error {
	target, err := lookup(ctx, ref, branch)
	if err != nil {
		return err
	}

	specBytes, err := fetch(ctx, target.URL+"/openapi.json", target.APIKey, w)
	if err != nil {
		return err
	}
	if outDir == "" {
		outDir = "./Palbase"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	specPath := filepath.Join(outDir, "openapi.json")
	if err := os.WriteFile(specPath, specBytes, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", specPath, err)
	}
	fmt.Fprintf(w, "✓ wrote %s\n", specPath)

	if appID == "" {
		return nil
	}

	// The config artifacts resolve to the SAME branch the openapi came from, so
	// `palbase ios use <branch>` produces an openapi + config pair that both point
	// at that branch's pod. `spec` (config-less, appID=="") never reaches here.
	// The branch is a branch of THIS project (ref) only — other env-bindings live
	// in their own projects with their own branch namespaces, so branch is applied
	// solely to the ref binding and left at each other binding's default (main).
	configByBundle, branchApplied, err := buildPullSpecConfig(ctx, list, cfgFetch, appID, ref, branch, w)
	if err != nil {
		return err
	}
	// `ios use <branch>` is an EXPLICIT re-target: if the branch reached no env
	// (ref matches no binding, or the ref binding has no registered bundle id),
	// the config would silently point every env at main while openapi.json points
	// at <branch> — a mismatched pair reported as success. Fail loudly instead.
	// `ios link`/`spec` pass requireBranchApplied=false: their branch is a silent
	// default (main), not a user demand, and their ref routinely differs from the
	// app's env-project refs, so the branch legitimately applies to nothing.
	if requireBranchApplied && branch != "" && !branchApplied {
		return fmt.Errorf("branch %q requested but app %q has no environment bound to project ref %q (its binding is missing or has no registered bundle id) — nothing to re-target", branch, appID, ref)
	}
	data, err := json.MarshalIndent(configByBundle, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal palbase-config.json: %w", err)
	}
	cfgPath := filepath.Join(outDir, "palbase-config.json")
	if err := os.WriteFile(cfgPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	fmt.Fprintf(w, "✓ wrote %s (%d bundle id(s))\n", cfgPath, len(configByBundle))
	return nil
}

// buildPullSpecConfig resolves EVERY env the app is registered for (listBindings),
// fetches each binding's config artifact by its BARE project_ref (the SAME path
// emitIOSBundleKeyedPlist uses, including the merged OAuth block), and keys the
// result by bundle id. A binding with no registered bundle id is skipped with a
// warning; if none has one the call errors (nothing to write).
// buildPullSpecConfig returns (config-by-bundle, branchApplied, error). branchApplied
// is true iff a NON-empty branchName was actually stamped onto a written entry — i.e.
// a binding whose project_ref == ref survived the empty-identifier skip AND landed in
// the output. The caller uses it to fail loudly when a user EXPLICITLY asked to
// re-target a branch (`ios use`) but the branch reached no env (see runPullSpec).
func buildPullSpecConfig(
	ctx context.Context,
	list bindingLister,
	fetch configArtifactFetch,
	appID, ref, branchName string,
	w io.Writer,
) (map[string]pullSpecConfigEntry, bool, error) {
	bindings, err := list(ctx, appID)
	if err != nil {
		return nil, false, fmt.Errorf("list app %q bindings: %w", appID, err)
	}
	out := make(map[string]pullSpecConfigEntry)
	branchApplied := false
	for _, bnd := range bindings {
		if bnd.Identifier == "" {
			fmt.Fprintf(w, "skipping env %s: no registered bundle id (configure it in Studio → apps → bindings)\n", bnd.ProjectRef)
			continue
		}
		// The branch belongs to THIS project (ref) only. Apply it to the ref
		// binding so its base_url + key resolve to that branch's endpoint_ref;
		// every other env-binding is a separate project with its own branch
		// namespace, so it resolves at its own default (main) branch.
		bndBranch := ""
		isRefBinding := bnd.ProjectRef == ref
		if isRefBinding {
			bndBranch = branchName
		}
		art, err := fetch(ctx, appID, bnd.ProjectRef, bndBranch)
		if err != nil {
			return nil, false, fmt.Errorf("fetch config artifact for env %s: %w", bnd.ProjectRef, err)
		}
		if _, dup := out[art.Identifier]; dup {
			return nil, false, fmt.Errorf("two environments share the bundle id %q — each environment must register a distinct bundle id", art.Identifier)
		}
		entry := pullSpecConfigEntry{
			AppID:      art.AppID,
			Identifier: art.Identifier,
			EnvPreset:  art.EnvPreset,
			BaseURL:    art.BaseURL,
			APIKey:     art.APIKey,
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
		out[art.Identifier] = entry
		if isRefBinding && branchName != "" {
			branchApplied = true
		}
	}
	if len(out) == 0 {
		return nil, false, fmt.Errorf("app %q has no environment with a registered bundle id — register at least one bundle id in Studio (apps → bindings) before `palbase spec`", appID)
	}
	return out, branchApplied, nil
}
