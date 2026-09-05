package backend

// native_link.go — the shared `palbase ios|macos|android link` core.
//
// Native link wires a platform slot to a Palbase PROJECT without inspecting or
// modifying platform project files:
//
//  1. Use the SELECTED project (`palbase link`, or --project).
//  2. Register (or reuse) this checkout's platform app under that Project.
//     Apps are PROJECT-scoped: `apps.project_id` is singular.
//  3. Fetch one `.palbase/openapi/<env>.json` per environment plus the platform config at
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
	"sort"

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
	fetch    specFetch
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

  1. uses the selected project (palbase link <project>, or --project)
  2. reuses this checkout's linked %s app or registers a new one
  3. writes one .palbase/openapi/<env>.json per environment and the platform config under
     .palbase/%s/palbase-config.json for the SELECTED environment

%s`, platform, platform, platform, next),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			// The target first: a checkout bound to a project you run links to
			// THAT project, the same way login, push and spec already do.
			if handled, err := linkToBoundProject(cmd, platform, cmd.OutOrStdout()); handled {
				return err
			}
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
				fetch:      managedSpecFetch(rest),
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
		return nil, fmt.Errorf("applicationId is required for Android; pass --package-name")
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
	appID, err := resolveApp(ctx, d.rest, opts.projectID, opts.platform, linkedAppID, opts.identifier, w)
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
	// EVERY environment, not the one being linked — the same rule the direct
	// half has followed since app_environments.go was written. An app that holds
	// only the environment somebody linked last is an app whose address depends
	// on WHEN it was built, which is how a TestFlight build ends up pointed at
	// staging. The build configuration decides instead, and it cannot be
	// forgotten at run time.
	deps := cloudEnvDeps{
		fetch:      d.fetch,
		list:       d.list,
		cfgFetch:   d.cfgFetch,
		freshness:  studioSpecFreshness(d.rest, opts.projectID, opts.environmentRef),
		publicHost: d.publicHost,
	}
	if err := linkNativeEnvironments(
		ctx, deps, opts.platform, appID, opts.environmentRef, opts.projectID, w,
	); err != nil {
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

// resolveApp finds this checkout's app in the project, or registers one.
//
// ONE function, because there was only ever one behaviour. resolveNativeApp and
// resolveWebApp listed the same route, matched the same persisted id the same
// way, printed the same three sentences and created the same row. What differed
// was a platform string, an optional identifier, and which fallback name to use
// when the directory has none — three parameters wearing the disguise of two
// functions, and two places for one answer to drift.
func resolveApp(
	ctx context.Context,
	rest restDoer,
	projectID, platform, persistedAppID, identifier string,
	w io.Writer,
) (string, error) {
	var rows []nativeAppRow
	if err := rest.Do(ctx, http.MethodGet, "/api/v2/projects/"+projectID+"/apps", nil, &rows); err != nil {
		return "", fmt.Errorf("list %s apps: %w", platform, err)
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
		fmt.Fprintf(w, "linked %s app %s does not match the selected project and platform; registering a new one\n",
			platform, persistedAppID)
	}

	// The directory's name, and a platform-shaped fallback when it has none —
	// a registered app called "." helps nobody read the console later.
	name := platform + " app"
	if cwd, err := os.Getwd(); err == nil {
		if base := filepath.Base(cwd); base != "." && base != string(filepath.Separator) {
			name = base
		}
	}
	body := map[string]any{"platform": platform, "displayName": name}
	if identifier != "" {
		body["identifier"] = identifier
	}
	var created nativeAppRow
	if err := rest.Do(ctx, http.MethodPost, "/api/v2/projects/"+projectID+"/apps", body, &created); err != nil {
		return "", fmt.Errorf("create %s app: %w", platform, err)
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
	return "", fmt.Errorf("applicationId not found in the Android Gradle files; pass --package-name")
}

// printNativeNextSteps prints the platform-specific package/plugin wiring.
func printNativeNextSteps(w io.Writer, platform, outDir string) {
	if platform == "android" {
		fmt.Fprintf(w, `
next steps (Android app):
  1. add implementation("io.palbase:palbe:<version>")
  2. apply plugin id("io.palbase.codegen")
  3. commit .palbase/openapi/ and %s/palbase-config.json
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
  4. Commit .palbase/openapi/, %s/palbase-config.json, Palbase/Config/ and Palbase/Generated/
Then `+"`import Palbe`"+` and use `+"`pb`"+`. Re-run `+"`palbase %[1]s spec`"+` after every deploy to regenerate.
`, platform, outDir)
}
