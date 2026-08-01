package backend

// native_link.go — the shared `palbase ios|macos|android link` core.
//
// Native link wires a platform slot to a Palbase PROJECT without inspecting or
// modifying platform project files:
//
//  1. Use the SELECTED project (`palbase project use`, or --project).
//  2. Register (or reuse) this checkout's platform app under that Project.
//     Apps are PROJECT-scoped: `apps.project_id` is singular.
//  3. Fetch the shared `.palbase/openapi.json` plus the platform config at
//     `.palbase/<platform>/palbase-config.json` for the SELECTED ENVIRONMENT.
//  4. Print the platform-specific SDK wiring steps.
//
// `palbase <platform> use <environment>` then re-targets the SAME app at another
// ENVIRONMENT of the same Project. It never selects a branch — there is none.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// nativeAppRow mirrors the apps list / create row shape.
type nativeAppRow struct {
	ID          string  `json:"id"`
	Platform    string  `json:"platform"`
	DisplayName string  `json:"display_name"`
	Identifier  *string `json:"identifier"`
	DeletedAt   *string `json:"deleted_at"`
}

// nativeLinkSummary is the --json output shape.
type nativeLinkSummary struct {
	ProjectID      string `json:"project_id"`
	EnvironmentRef string `json:"environment_ref"`
	AppID          string `json:"app_id"`
	ConfigDir      string `json:"config_dir"`
}

// nativeLinkDeps carries the injectable seams so runNativeLink is testable
// without a live Management API or tenant host.
type nativeLinkDeps struct {
	rest     restDoer
	lookup   specTargetLookup
	fetch    remoteSpecFetch
	list     bindingLister
	cfgFetch configArtifactFetch
	// publicHost binds config artifacts to the configured tenant DNS suffix.
	publicHost string
}

// nativeLinkOpts is the resolved context runNativeLink acts on.
type nativeLinkOpts struct {
	platform           string // ios, macos, or android
	projectID          string
	environmentID      string
	environmentRef     string
	repositoryProvider string
	appID              string // locally persisted platform app id; empty on first link
	identifier         string // bundle id / Android applicationId
}

// newIOSCmd builds the `palbase ios` command group.
func newIOSCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ios",
		Short: "Wire an iOS app to a Palbase project",
	}
	cmd.AddCommand(newIOSLinkCmd(r), newIOSUseCmd(r))
	return cmd
}

// newMacOSCmd exposes the same native registration flow for a distinct macOS
// app. App Attest is intentionally absent on macOS.
func newMacOSCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "macos",
		Short: "Wire a macOS app to a Palbase project",
	}
	cmd.AddCommand(newNativeLinkCmd(r, "macos"))
	return cmd
}

// newIOSLinkCmd builds `palbase ios link`.
func newIOSLinkCmd(r Resolvers) *cobra.Command {
	return newNativeLinkCmd(r, "ios")
}

func newNativeLinkCmd(r Resolvers, platform string) *cobra.Command {
	var jsonOut bool
	var packageName string
	next := "Run link again to refresh the macOS config."
	if platform == "ios" || platform == "android" {
		next = fmt.Sprintf("Switch environments later with 'palbase %s use <environment>'.", platform)
	}
	projectKind := "Xcode"
	if platform == "android" {
		projectKind = "Android"
	}

	cmd := &cobra.Command{
		Use:   "link",
		Args:  cobra.NoArgs,
		Short: fmt.Sprintf("Link this %s %s project to the selected Palbase project and fetch its SDK config", platform, projectKind),
		Long: fmt.Sprintf(`Wire a %s app slot to the SELECTED Palbase project.
Local project files are left untouched.

  1. uses the selected project (palbase project use <projectId>, or --project)
  2. reuses this checkout's linked %s app or registers a new one
  3. writes shared .palbase/openapi.json and the platform config under
     .palbase/%s/palbase-config.json for the SELECTED environment

%s`, platform, platform, platform, next),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if platform == "android" && packageName == "" {
				var err error
				packageName, err = detectAndroidApplicationID(".")
				if err != nil {
					return err
				}
			}
			stdout := cmd.OutOrStdout()
			// --json: the summary is the ONLY stdout so a script can parse it; the
			// human progress lines are suppressed.
			human := stdout
			if jsonOut {
				human = io.Discard
			}
			if err := selection.EnsureGitignored(".gitignore"); err != nil {
				return fmt.Errorf("update .gitignore: %w", err)
			}

			sel, err := r.resolve(ctx)
			if err != nil {
				return err
			}
			var rest restDoer
			if r.REST != nil {
				rest = r.REST()
			}
			persistedAppID, err := persistedAppIDFor(platform, sel)
			if err != nil {
				return err
			}

			deps := nativeLinkDeps{
				rest:       rest,
				lookup:     lookupSpecTarget(r),
				fetch:      fetchRemoteOpenAPISpec,
				list:       studioBindingLister(rest),
				cfgFetch:   studioConfigArtifactFetch(rest, r.Endpoints().PublicHost),
				publicHost: r.Endpoints().PublicHost,
			}
			summary, err := runNativeLink(ctx, deps, nativeLinkOpts{
				platform:           platform,
				projectID:          sel.ProjectID,
				environmentID:      sel.Environment.ID,
				environmentRef:     sel.EnvironmentRef(),
				repositoryProvider: sel.RepositoryProvider,
				appID:              persistedAppID,
				identifier:         packageName,
			}, human)
			if err != nil {
				return err
			}
			if jsonOut {
				fmt.Fprintln(stdout, renderJSON(summary))
				return nil
			}
			printNativeNextSteps(stdout, platform, summary.ConfigDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit a JSON summary instead of human output")
	if platform == "android" {
		cmd.Flags().StringVar(&packageName, "package-name", "", "Android applicationId (auto-detected from app/build.gradle[.kts])")
	}
	return cmd
}

// runNativeLink is the testable core: reuse/create the platform app under the
// selected Project, then fetch the spec and config for the selected Environment.
func runNativeLink(ctx context.Context, d nativeLinkDeps, opts nativeLinkOpts, w io.Writer) (*nativeLinkSummary, error) {
	if opts.platform != "ios" && opts.platform != "macos" && opts.platform != "android" {
		return nil, fmt.Errorf("native link platform must be ios, macos, or android")
	}
	if opts.platform == "android" && opts.identifier == "" {
		return nil, fmt.Errorf("Android applicationId is required; pass --package-name")
	}

	linkedAppID := opts.appID
	if linkedAppID == "" {
		// Fresh clone: `.palbase/config.json` is gitignored (it is CLI state),
		// but the COMMITTED platform slot already names the app this checkout
		// is linked to. Without this, the first link on a teammate's machine
		// registers a SECOND app for the same project and rewrites the
		// committed api_key.
		linkedAppID = appIDFromPlatformSlot(opts.platform)
	}
	appID, err := resolveNativeApp(ctx, d, opts.projectID, opts.platform, linkedAppID, opts.identifier, w)
	if err != nil {
		return nil, err
	}
	// Persist the concrete app immediately. If a later config/spec fetch fails,
	// the next run reuses this exact registration instead of creating another.
	sel := selection.Selection{
		ProjectID: opts.projectID,
		Environment: selection.Environment{
			ID: opts.environmentID, Ref: opts.environmentRef,
		},
		RepositoryProvider: opts.repositoryProvider,
	}
	if err := persistProjectAppSlot(opts.platform, appID, &sel, false); err != nil {
		return nil, err
	}

	configDir := filepath.Join(".palbase", opts.platform)
	if err := runPullSpec(
		ctx, d.lookup, d.fetch, d.list, d.cfgFetch,
		opts.environmentRef, d.publicHost, ".palbase", configDir, appID, w,
	); err != nil {
		return nil, err
	}
	// Emit the committed Swift client from the spec+config just written. A no-op
	// on an Android-only checkout (no Apple slot), where the Gradle plugin
	// generates instead.
	if err := generateAppleClient(".palbase", w); err != nil {
		return nil, err
	}

	return &nativeLinkSummary{
		ProjectID:      opts.projectID,
		EnvironmentRef: opts.environmentRef,
		AppID:          appID,
		ConfigDir:      configDir,
	}, nil
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
	var slot struct {
		AppID string `json:"app_id"`
	}
	if err := json.Unmarshal(data, &slot); err != nil {
		return ""
	}
	return slot.AppID
}

// persistedAppIDFor returns the locally-remembered app registration for
// platform, but only when the local config still selects the SAME project as
// sel. A slot left over from no selection or a different project must never
// be handed to the remote API as if it already belonged to the project being
// linked — resolveNativeApp/resolveWebApp fall back to registering a fresh
// app instead.
//
// The returned error is non-nil ONLY when `.palbase/config.json` exists but
// failed to load for a reason OTHER than "nothing selected yet" (corrupt
// JSON, unsupported version — selection.ErrNotSelected is NOT an error here,
// it is the ordinary first-link case). Callers MUST check it and return
// before registering anything remotely: register-then-persist is how a
// config-less directory used to orphan an app (persistProjectAppSlot failed
// AFTER the remote create), and a genuinely broken config hits that exact
// same trap — persistProjectAppSlot's Load("") fails there too, just with an
// error persistProjectAppSlot correctly refuses to paper over. Gating here,
// before any remote call, closes that corner instead of merely surfacing it.
func persistedAppIDFor(platform string, sel selection.Selection) (string, error) {
	cfg, err := selection.Load("")
	if err != nil {
		var notSelected selection.ErrNotSelected
		if errors.As(err, &notSelected) {
			return "", nil
		}
		return "", err
	}
	if cfg.ProjectID != sel.ProjectID {
		return "", nil
	}
	return cfg.AppID(platform), nil
}

// persistProjectAppSlot records one platform's app registration in
// `.palbase/config.json`. A cross-Project move clears every foreign app slot;
// within one Project, sibling slots remain intact.
//
// The directory may genuinely have no config yet — `--project X` resolves the
// Selection without ever touching disk (selection.Resolver.Resolve), so a
// config-less directory is the ORDINARY first-link case, not an error. Only a
// config that failed to load for some OTHER reason (corrupt JSON, wrong
// version) must still fail loudly: the caller already registered the app
// remotely by this point, and silently discarding a real config error would
// paper over a broken checkout instead of surfacing it.
func persistProjectAppSlot(platform, appID string, sel *selection.Selection, retarget bool) error {
	cfg, err := selection.Load("")
	if err != nil {
		var notSelected selection.ErrNotSelected
		if !errors.As(err, &notSelected) {
			return err
		}
		cfg = &selection.Config{}
	}
	if sel != nil {
		projectChanged := cfg.ProjectID != sel.ProjectID
		if projectChanged || retarget {
			if sel.ProjectID == "" || sel.Environment.ID == "" || sel.RepositoryProvider == "" {
				return fmt.Errorf("cannot persist an incomplete Project/Environment selection")
			}
			selection.ApplySelection(cfg, *sel)
		} else if sel.RepositoryProvider != "" {
			// Repository mode is server-owned and may have changed since link.
			cfg.RepositoryProvider = sel.RepositoryProvider
		}
	}
	if err := cfg.SetAppID(platform, appID); err != nil {
		return err
	}
	if err := selection.Save("", cfg); err != nil {
		return fmt.Errorf("save %s: %w", selection.ConfigPath(""), err)
	}
	return nil
}

// resolveNativeApp reuses the locally persisted app id only when it still
// belongs to the selected PROJECT and platform. A missing, deleted, or
// mismatched id is replaced with a fresh registration; remote apps are never
// guessed or mutated.
func resolveNativeApp(
	ctx context.Context,
	d nativeLinkDeps,
	projectID, platform, persistedAppID, identifier string,
	w io.Writer,
) (string, error) {
	var rows []nativeAppRow
	if err := d.rest.Do(ctx, http.MethodGet, "/api/v2/projects/"+projectID+"/apps", nil, &rows); err != nil {
		return "", fmt.Errorf("list apps: %w", err)
	}
	if persistedAppID != "" {
		for _, app := range rows {
			if app.ID != persistedAppID || app.DeletedAt != nil {
				continue
			}
			if app.Platform == platform && (identifier == "" || derefStr(app.Identifier) == identifier) {
				fmt.Fprintf(w, "using linked %s app %s (%s)\n", platform, app.DisplayName, app.ID)
				return persistedAppID, nil
			}
		}
		fmt.Fprintf(w, "linked %s app %s does not match the selected project and platform; registering a new one\n", platform, persistedAppID)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	name := filepath.Base(cwd)
	body := map[string]any{"platform": platform, "displayName": name}
	if identifier != "" {
		body["identifier"] = identifier
	}
	var created nativeAppRow
	if err := d.rest.Do(ctx, http.MethodPost, "/api/v2/projects/"+projectID+"/apps", body, &created); err != nil {
		return "", fmt.Errorf("create app: %w", err)
	}
	fmt.Fprintf(w, "✓ registered %s app %q (%s)\n", platform, name, created.ID)
	return created.ID, nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

var androidApplicationIDPattern = regexp.MustCompile(`(?m)applicationId\s*(?:=\s*)?["']([^"']+)["']`)

func detectAndroidApplicationID(root string) (string, error) {
	candidates := []string{
		filepath.Join(root, "app", "build.gradle.kts"),
		filepath.Join(root, "app", "build.gradle"),
		filepath.Join(root, "build.gradle.kts"),
		filepath.Join(root, "build.gradle"),
	}
	for _, candidate := range candidates {
		contents, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		match := androidApplicationIDPattern.FindSubmatch(contents)
		if len(match) == 2 {
			return string(match[1]), nil
		}
	}
	return "", fmt.Errorf("Android applicationId not found; pass --package-name")
}

// printNativeNextSteps prints the platform-specific package/plugin wiring.
func printNativeNextSteps(w io.Writer, platform, outDir string) {
	if platform == "android" {
		fmt.Fprintf(w, `
next steps (Android app):
  1. add implementation("io.palbase:palbe:<version>")
  2. apply plugin id("io.palbase.codegen")
  3. commit .palbase/openapi.json and %s/palbase-config.json
  4. call Palbase.initialize(this), then import io.palbase.pb
`, outDir)
		return
	}
	fmt.Fprintf(w, `
next steps (%s Xcode target):
  1. File ▸ Add Package Dependencies… → https://github.com/palgroup/palbackend-ios
  2. Add the "Palbe" library to your app target
  3. Drag the Palbase folder into your app target (folder reference) — its
     PalbaseGenerated.swift compiles and Palbase-Info.plist ships as a resource
  4. Commit .palbase/openapi.json, %s/palbase-config.json and Palbase/Generated/
Then `+"`import Palbe`"+` and use `+"`pb`"+`. Re-run `+"`palbase %[1]s spec`"+` after every deploy to regenerate.
`, platform, outDir)
}
