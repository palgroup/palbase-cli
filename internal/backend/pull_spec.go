package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/selection"
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
base URL + key). That comes from ` + "`palbase <platform> link`" + ` and is re-written by
` + "`palbase <platform> use <environment>`" + `.

The global --project / --environment flags select a CLOUD environment. In a
checkout linked to a project they do not apply and are REFUSED — run
` + "`palbase link <ref>`" + ` to point the checkout at another project.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			// TARGET-RELATIVE, like login and push: a checkout linked to a stack
			// refreshes from THAT stack. One verb either way — a person should
			// not have to remember which kind of project they are standing in.
			if target, err := ReadTarget(); err == nil {
				if err := refuseCloudSelectionFlags(cmd, target); err != nil {
					return err
				}
				if _, err := PrintTargetFor(cmd); err != nil {
					return err
				}
				return RefreshSpec(cmd.Context(), out)
			}

			sel, err := r.resolve(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", sel.Describe())

			web, apple, android := linkedPlatforms()
			if !web && !apple && !android {
				// NAME A CURE THAT EXISTS. This used to advise `palbase web link`
				// and its three siblings; the platform command groups were
				// retired (FR-009), so the advice sent the reader to
				// `unknown command "web" for "palbase"` — a refusal whose only
				// remedy could not be typed.
				return errors.New(
					"this directory is not linked to a Palbase project — run `palbase link` first, " +
						"which detects what this checkout is and writes the slots this command reads")
			}

			// Apple regenerates from this refresh, so verify it CAN before any
			// file moves — a spec written next to a client that cannot be
			// re-emitted is exactly the drift this command exists to prevent.
			if apple {
				if err := preflightAppleGenerator(nativeArtifactsDir); err != nil {
					return err
				}
			}

			deps := cloudEnvDeps{
				fetch:      managedSpecFetch(r.REST()),
				list:       studioBindingLister(r.REST()),
				cfgFetch:   studioConfigArtifactFetch(r.REST(), r.Endpoints().PublicHost),
				freshness:  studioSpecFreshness(r.REST(), sel.ProjectID, sel.EnvironmentRef()),
				publicHost: r.Endpoints().PublicHost,
			}

			// NATIVE: EVERY environment refreshes, not just the selected one.
			//
			// The checkout carries them all and the build configuration picks
			// one, so refreshing only the selected environment would leave the
			// OTHER configurations compiling against a contract from whenever
			// they were last linked — and the person building them has no way to
			// tell. `spec` still does not touch the runtime config; it rewrites
			// contracts and re-emits the clients built from them.
			if apple || android {
				platform := "ios"
				if !apple {
					platform = "android"
				}
				// The COMMITTED slot names the app, so a fresh clone refreshes
				// without ever having run `link` on this machine.
				//
				// AND THE FALLBACK BELOW READS A FILE NOTHING WRITES ANY MORE.
				// `.palbase/selection.json` lost its last producer when `clone`
				// stopped taking a management project id, and it left the
				// addressing chain with the second mechanism (FR-013). It is
				// kept for one reason: a checkout an OLDER CLI left behind still
				// has one, and refusing those would strand them. It is a
				// migration affordance, not a live mechanism — nothing here
				// should grow to depend on it.
				appID := appIDFromPlatformSlot(platform)
				if appID == "" {
					cfg, err := selection.Load("")
					if err != nil {
						return err
					}
					appID = cfg.AppID(platform)
				}
				if appID == "" {
					return fmt.Errorf(
						"no %s app linked — run `palbase link` first (it writes .palbase/%s/palbase-config.json, "+
							"which is where this command reads the app from)", platform, platform)
				}
				if err := linkNativeEnvironments(
					cmd.Context(), deps, platform, appID, sel.EnvironmentRef(), sel.ProjectID, out,
				); err != nil {
					return err
				}
			}

			// WEB reads ONE contract from its own directory: `palbe-gen` takes a
			// single openapi.json and the web SDK has no per-environment build
			// configuration to select between them.
			if web {
				if err := runPullSpec(
					cmd.Context(), deps.fetch, deps.freshness,
					sel.EnvironmentRef(), webArtifactsDir, out,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
	return cmd
}

// specFetch reads an Environment's OpenAPI contract.
//
// THE REF IS THE WHOLE SELECTOR. It used to be a URL plus a key, built from the
// tenant origin — and that could not work on v2: a stack serves its document at
// `/admin/openapi.json` for a `service_role` key ONLY, while the CLI holds the
// publishable one. The contract now comes from the Management API, which holds
// the service credential and never hands it out (see managed_spec.go).

type specFetch func(ctx context.Context, environmentRef string, w io.Writer) ([]byte, error)

// specFreshness resolves what the PLATFORM'S LEDGER knows: the newest
// deployment it records as succeeded, and the recent identities it has seen at
// all. Injected so runPullSpec is testable without Studio; nil disables the
// check entirely (only tests pass nil — every real caller supplies it, or a
// contract could be written unverified through a path nobody thought to wire).
//
// THE SECOND RETURN IS WHAT MAKES THE CHECK HONEST. "Served != newest" has two
// causes and they call for opposite actions: the origin lagging a deploy it is
// about to pick up (wait), or the LEDGER lagging a deploy that never went
// through it (write). A push straight at a stack — a linked checkout, a
// self-hosted stack, a port-forwarded push — never reaches the ledger, so its
// newest row is the old one and the origin is AHEAD. Only the known set tells
// the two apart.
type specFreshness func(ctx context.Context) (expected string, known []string, err error)

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
	fetch specFetch,
	environmentRef string,
	freshness specFreshness,
	w io.Writer,
) ([]byte, string, error) {
	deadline := time.Now().Add(specWaitTimeout)
	announced := false
	for {
		specBytes, err := fetch(ctx, environmentRef, w)
		if err != nil {
			return nil, "", err
		}
		if freshness == nil {
			return specBytes, specDocVersion(specBytes), nil
		}
		// Re-resolved every round on purpose: a deploy that finishes WHILE we
		// wait moves the expectation forward, and pinning the first answer would
		// leave us waiting for a version that is already superseded.
		expected, known, ferr := freshness(ctx)
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
		// THE LEDGER, NOT THE ORIGIN, IS THE ONE BEHIND.
		//
		// A deploy the ledger has never recorded cannot be an OLDER one: the
		// wait loop would sit out its whole deadline and then refuse, and the
		// command would be lost for as long as that stack keeps being pushed to
		// directly. Measured live on `centauri` 25.08.2026 — the stack served an
		// artifact written ten hours AFTER the ledger's newest row.
		if !slices.Contains(known, served) {
			fmt.Fprintf(w, "  note: this origin serves deploy %s, which this platform has no record of "+
				"(a stack pushed to directly) — freshness UNVERIFIED\n", short(served))
			return specBytes, served, nil
		}
		if time.Now().After(deadline) {
			return nil, "", fmt.Errorf(
				"environment %s is still serving deploy %s but the latest successful deploy is %s — nothing was written; "+
					"wait for the rollout to finish and run `palbase spec` again",
				environmentRef, served, expected)
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
	return func(ctx context.Context) (string, []string, error) {
		var resp struct {
			Deployments []deployRow `json:"deployments"`
		}
		if err := rest.Do(ctx, http.MethodGet,
			DeploymentsPath(projectID, environmentRef)+"?limit=20", nil, &resp); err != nil {
			return "", nil, err
		}
		expected := ""
		known := make([]string, 0, len(resp.Deployments))
		// EVERY identity the ledger has seen, not just the succeeded ones: a
		// failed row still proves the ledger KNOWS that push, which is the only
		// question the known set answers.
		for _, d := range resp.Deployments { // newest first
			if d.Version == nil || *d.Version == "" {
				continue
			}
			known = append(known, *d.Version)
			if expected == "" && d.Status == "succeeded" {
				expected = *d.Version
			}
		}
		// Never deployed (or every attempt failed): there is nothing to compare
		// against, which is not the same as a mismatch.
		return expected, known, nil
	}
}

// runPullSpec writes ONE Environment's contract into dir.
//
// THE WEB HALF. A native checkout carries every environment and picks one at
// build time (cloud_environments.go); the web SDK has no build configuration to
// select between them and `palbe-gen` takes a single openapi.json, so for the
// web the selected Environment IS the contract.
func runPullSpec(
	ctx context.Context,
	fetch specFetch,
	freshness specFreshness,
	environmentRef, outDir string,
	w io.Writer,
) error {
	specBytes, servedVersion, err := fetchFreshSpec(ctx, fetch, environmentRef, freshness, w)
	if err != nil {
		return err
	}
	if outDir == "" {
		outDir = "./.palbase"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	path := filepath.Join(outDir, "openapi.json")
	if err := os.WriteFile(path, specBytes, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Name the deploy on the success line. "wrote openapi.json" alone is what let
	// a contract from the previous deploy sit on disk looking finished; the
	// identity is the one thing that makes the line checkable.
	if servedVersion != "" {
		fmt.Fprintf(w, "✓ wrote %s (deploy %s)\n", path, servedVersion)
	} else {
		fmt.Fprintf(w, "✓ wrote %s\n", path)
	}
	return nil
}

// appIDFromPlatformSlot reads the app id out of the committed platform config
// (`.palbase/<platform>/palbase-config.json`). Returns "" when the file is
// absent or carries no id — the genuine first-link case, where registering a
// new app is correct.
func appIDFromPlatformSlot(platform string) string {
	data, err := os.ReadFile(filepath.Join(".palbase", platform, "palbase-config.json"))
	if err != nil {
		return ""
	}
	// TWO SHAPES, ONE ANSWER. The web slot is a flat object with `app_id`; a
	// native slot carries every environment and each names the app. Reading only
	// the flat field made a fresh clone look UNLINKED after the native config
	// went multi-environment — and an unlinked clone registers a SECOND app for
	// the same project and rewrites the committed key.
	var slot struct {
		AppID        string                    `json:"app_id"`
		Default      string                    `json:"default_environment"`
		Environments map[string]appEnvironment `json:"environments"`
	}
	if err := json.Unmarshal(data, &slot); err != nil {
		return ""
	}
	if slot.AppID != "" {
		return slot.AppID
	}
	if e, ok := slot.Environments[slot.Default]; ok && e.AppID != "" && e.AppID != projectAppID {
		return e.AppID
	}
	// The default may be the LOCAL stack, whose app id is the stack's own
	// identity rather than a registration in the cloud. Deterministic order so
	// two runs of the same checkout never disagree.
	names := make([]string, 0, len(slot.Environments))
	for name := range slot.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if e := slot.Environments[name]; e.AppID != "" && e.AppID != projectAppID {
			return e.AppID
		}
	}
	return ""
}
