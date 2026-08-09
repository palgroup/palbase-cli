package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	apple = isRegularFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json")) ||
		isRegularFile(filepath.Join(nativeArtifactsDir, "macos", "palbase-config.json"))
	android = isRegularFile(filepath.Join(nativeArtifactsDir, "android", "palbase-config.json"))
	return web, apple, android
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
func newSpecCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec",
		Args:  cobra.NoArgs,
		Short: "Refresh openapi.json for the selected environment (and regenerate the committed Swift client)",
		Long: `Fetch the SELECTED environment's openapi.json into every directory this
checkout's linked platforms read it from. Run it after every deploy so the
committed API contract stays current.

  web      → ` + webArtifactsDir + `/openapi.json   (` + "`palbe-gen`" + ` regenerates palbe.gen.ts from it,
                                    via the predev/prebuild hook or by hand)
  ios/macos→ ` + nativeArtifactsDir + `/openapi.json  then regenerates Palbase/Generated/ —
                                    PalbaseGenerated.swift + Palbase-Info.plist — using
                                    the generator from the palbackend-ios checkout
                                    SwiftPM resolved for this project. Commit the result.
  android  → ` + nativeArtifactsDir + `/openapi.json  (the Gradle plugin regenerates on the next build)

Which of those run is read from the COMMITTED slot files the link commands
wrote (` + webArtifactsDir + `/palbase-config.json, ` + nativeArtifactsDir + `/<platform>/palbase-config.json),
so a fresh clone behaves the same as the machine that linked it.

spec does NOT write the per-environment runtime config (palbase-config.json —
base URL + key). That comes from ` + "`palbase <platform> link`" + ` and is re-written by
` + "`palbase <platform> use <environment>`" + `.

Override the target with the global --project / --environment flags.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sel, err := r.resolve(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			web, apple, android := linkedPlatforms()
			if !web && !apple && !android {
				return fmt.Errorf(
					"this directory is not linked to a Palbase project — run `palbase web link`, `palbase ios link`, `palbase macos link` or `palbase android link` first")
			}

			// Apple regenerates from this refresh, so verify it CAN before any
			// file moves — a spec written next to a client that cannot be
			// re-emitted is exactly the drift this command exists to prevent.
			if apple {
				if err := preflightAppleGenerator(nativeArtifactsDir); err != nil {
					return err
				}
			}

			// One fetch, written to each linked platform's directory. Native
			// platforms share one directory, so this is at most two writes.
			dirs := []string{}
			if web {
				dirs = append(dirs, webArtifactsDir)
			}
			if apple || android {
				dirs = append(dirs, nativeArtifactsDir)
			}
			for _, dir := range dirs {
				// spec fetches the API contract ONLY (empty appID → no config).
				if err := runPullSpec(
					cmd.Context(),
					lookupSpecTarget(r),
					fetchRemoteOpenAPISpec,
					studioBindingLister(r.REST()),
					studioConfigArtifactFetch(r.REST(), r.Endpoints().PublicHost),
					studioSpecFreshness(r.REST(), sel.ProjectID, sel.EnvironmentRef()),
					sel.EnvironmentRef(), r.Endpoints().PublicHost, dir, "", "",
					out,
				); err != nil {
					return err
				}
			}

			// Apple is the only platform with no build-time generator of its own.
			if apple {
				return generateAppleClient(nativeArtifactsDir, out)
			}
			return nil
		},
	}
	return cmd
}

// specTargetLookup resolves a backend target (URL + publishable key) for an
// ENVIRONMENT ref. Injected so runPullSpec is testable without a live server.
type specTargetLookup func(ctx context.Context, environmentRef string) (backendTarget, error)

// remoteSpecFetch fetches the openapi.json bytes from a remote tenant host.
type remoteSpecFetch func(ctx context.Context, specURL, apiKey string, w io.Writer) ([]byte, error)

// specFreshness resolves the deploy identity the origin is EXPECTED to be
// serving — the newest deployment the platform records as succeeded. Injected so
// runPullSpec is testable without Studio; nil disables the check entirely (only
// tests pass nil — every real caller supplies it, or a contract could be written
// unverified through a path nobody thought to wire).
type specFreshness func(ctx context.Context) (string, error)

// specWaitTimeout bounds how long `palbase spec` waits for the origin to serve
// the deploy it is supposed to be serving. The runtime's own bound is
// ACTIVE_RECHECK_MS (10s by default) — a warm isolate worker re-reads the ACTIVE
// pointer once per that window — so this is that window with room for a cold
// artifact fetch: long enough that the normal case always converges, short
// enough that a genuinely wedged origin still fails inside a person's attention
// span.
const specWaitTimeout = 45 * time.Second

// specWaitInterval paces the re-fetch. Each tick costs one small GET, and the
// thing being waited for moves on a ~10s boundary, so polling faster would only
// add noise.
const specWaitInterval = 2 * time.Second

// specDocVersion reads the deploy identity out of an OpenAPI document — the
// x-palbase-deploy extension the runtime stamps in (openapiSpecFromRoutes).
//
// Empty means the document does not name its deploy: a runtime older than the
// stamp, or an unparseable body. Both are "cannot verify", never "verified" and
// never "stale" — the extension is omitted rather than defaulted precisely so
// absence needs no placeholder to recognise.
func specDocVersion(b []byte) string {
	var doc struct {
		Deploy string `json:"x-palbase-deploy"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return ""
	}
	return doc.Deploy
}

// fetchFreshSpec fetches the contract and, when the expected deploy identity is
// knowable, keeps fetching until the origin actually serves it.
//
// Why this exists: a deploy reports success the moment ACTIVE.json flips, but a
// WARM isolate worker re-reads that pointer only once per ACTIVE_RECHECK_MS
// window, so for up to ~10s the origin still serves the PREVIOUS artifact — and
// the previous artifact carries the previous baked contract. Writing that to
// disk is how codegen silently emitted a client for 28 routes right after a
// deploy that shipped 29, and reported success doing it.
//
// Three outcomes, each honest about what it knows:
//   - served == expected     → write it.
//   - served has no identity → write it, and SAY the check could not run.
//   - still different at the → return an error and write NOTHING. A contract
//     deadline                 nobody can trust is worse on disk than absent:
//     on disk it becomes a committed client.
func fetchFreshSpec(
	ctx context.Context,
	fetch remoteSpecFetch,
	specURL, specKey string,
	freshness specFreshness,
	w io.Writer,
) ([]byte, string, error) {
	deadline := time.Now().Add(specWaitTimeout)
	announced := false
	for {
		specBytes, err := fetch(ctx, specURL, specKey, w)
		if err != nil {
			return nil, "", err
		}
		if freshness == nil {
			return specBytes, specDocVersion(specBytes), nil
		}
		// Re-resolved every round on purpose: a deploy that finishes WHILE we
		// wait moves the expectation forward, and pinning the first answer would
		// leave us waiting for a version that is already superseded.
		expected, ferr := freshness(ctx)
		if ferr != nil {
			fmt.Fprintf(w, "  warning: could not verify spec freshness (%v) — the written contract may predate your latest deploy\n", ferr)
			return specBytes, specDocVersion(specBytes), nil
		}
		served := specDocVersion(specBytes)
		if expected == "" || served == expected {
			return specBytes, served, nil
		}
		if served == "" {
			fmt.Fprintln(w, "  note: this origin serves a contract with no deploy identity — freshness UNVERIFIED")
			return specBytes, "", nil
		}
		if time.Now().After(deadline) {
			return nil, "", fmt.Errorf(
				"%s is still serving deploy %s but the latest successful deploy is %s — nothing was written; "+
					"wait for the rollout to finish and run `palbase spec` again",
				specURL, served, expected)
		}
		if !announced {
			fmt.Fprintf(w, "  waiting for the origin to serve deploy %s (it is still on %s)…\n", expected, served)
			announced = true
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(specWaitInterval):
		}
	}
}

// studioSpecFreshness resolves the newest SUCCEEDED deployment's version from
// the same deployments route `palbase deploys` reads. The environment's deploy
// history is the only place that knows which deploy the origin OUGHT to be
// serving — the origin itself will happily report the previous one.
func studioSpecFreshness(rest restDoer, projectID, environmentRef string) specFreshness {
	return func(ctx context.Context) (string, error) {
		var resp struct {
			Deployments []deployRow `json:"deployments"`
		}
		if err := rest.Do(ctx, http.MethodGet,
			DeploymentsPath(projectID, environmentRef)+"?limit=20", nil, &resp); err != nil {
			return "", err
		}
		for _, d := range resp.Deployments { // newest first
			if d.Status == "succeeded" && d.Version != nil && *d.Version != "" {
				return *d.Version, nil
			}
		}
		// Never deployed (or every attempt failed): there is nothing to compare
		// against, which is not the same as a mismatch.
		return "", nil
	}
}

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
	freshness specFreshness,
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

	specBytes, servedVersion, err := fetchFreshSpec(ctx, fetch, specURL, specKey, freshness, w)
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
	// Name the deploy on the success line. "wrote openapi.json" alone is what let
	// a contract from the previous deploy sit on disk looking finished; the
	// identity is the one thing that makes the line checkable.
	if servedVersion != "" {
		fmt.Fprintf(w, "✓ wrote %s (deploy %s)\n", specPath, servedVersion)
	} else {
		fmt.Fprintf(w, "✓ wrote %s\n", specPath)
	}

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
