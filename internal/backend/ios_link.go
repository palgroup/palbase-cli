package backend

// ios_link.go — `palbase ios link`
//
// The iOS symmetry of `palbase web link`: run in an Xcode project directory,
// ONE command wires the app to a Palbase PRODUCT without a Studio visit. The
// user answers ONE thing — which product (and only when they have several):
//
//  1. Pick the PRODUCT (a group). --group / your only product / a picker over
//     product names. The user never sees or picks an "environment" or a ref.
//  2. Resolve the product's PRODUCTION env-project (schema: exactly one per
//     group). That becomes the linked project ref (written to .palbase/config).
//  3. Auto-detect the app bundle id from ./*.xcodeproj (the MAIN app target —
//     *Tests/*UITests excluded; --bundle-id overrides). NEVER prompted — the
//     bundle id is the app's own identity, sent by the SDK at runtime.
//  4. Reuse the product's ios app, or register one (POST group apps).
//  5. Bind the bundle to PRODUCTION (PUT app binding).
//  6. Fetch the SPM codegen plugin's inputs (openapi.json + palbase-config.json)
//     into --out-dir via the same runPullSpec core `palbase spec` uses.
//  7. Print the manual Xcode wiring steps (SPM package + build plugin).
//
// `palbase ios use <branch>` then switches BRANCHES within that production
// project — the branch axis, orthogonal to the (hidden) env-project axis.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/spf13/cobra"
)

// iosGroupEnvRow mirrors the groups.environments tRPC row shape (env_preset /
// env_display_name may be JSON null — a null leaves the string field empty).
type iosGroupEnvRow struct {
	Ref            string `json:"ref"`
	EnvPreset      string `json:"env_preset"`
	EnvDisplayName string `json:"env_display_name"`
	Status         string `json:"status"`
}

// iosAppRow mirrors the apps.list / apps.create row shape.
type iosAppRow struct {
	ID          string  `json:"id"`
	Platform    string  `json:"platform"`
	DisplayName string  `json:"display_name"`
	DeletedAt   *string `json:"deleted_at"`
}

// iosLinkBinding is one (env × bundle id) pair the link bound. JSON tags feed
// the --json summary.
type iosLinkBinding struct {
	Env      string `json:"env"`
	BundleID string `json:"bundle_id"`
}

// iosLinkSummary is the --json output shape.
type iosLinkSummary struct {
	Group    string           `json:"group"`
	Ref      string           `json:"ref"` // the linked production env-project ref
	AppID    string           `json:"app_id"`
	Bindings []iosLinkBinding `json:"bindings"`
	OutDir   string           `json:"out_dir"`
}

// iosLinkDeps carries the injectable seams so runIOSLink is testable without a
// live Management API or tenant host: the REST transport (an httptest-backed
// *transport.Client in tests) plus the same four runPullSpec seams `palbase spec`'s
// tests stub.
type iosLinkDeps struct {
	rest        restDoer
	lookup      specTargetLookup
	fetch       remoteSpecFetch
	list        bindingLister
	cfgFetch    configArtifactFetch
	stdin       io.Reader
	interactive bool
}

// iosLinkOpts is the resolved flag set runIOSLink acts on.
type iosLinkOpts struct {
	ref       string // resolved project ref (resolveOrLinkRef already ran)
	branch    string
	group     string
	app       string
	name      string
	bundleIDs []string // --bundle-id override (else auto-detected from ./*.xcodeproj)
	outDir    string
	suggested []string // bundle ids detected from ./*.xcodeproj
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

// newIOSLinkCmd builds `palbase ios link`.
func newIOSLinkCmd(r Resolvers) *cobra.Command {
	var groupFlag, appFlag, nameFlag, outDir string
	var bundleIDs []string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link this Xcode project to a Palbase product and fetch its SDK config",
		Long: `Wire the iOS app in the current directory to a Palbase product in one
command — no Studio visit needed. You pick ONE thing: your product.

  1. pick your product (or --group; auto-selected if you have only one)
  2. its production environment is resolved automatically
  3. the app bundle id is auto-detected from ./*.xcodeproj (--bundle-id overrides)
  4. registers the ios app, binds the bundle, and downloads the codegen plugin's
     inputs into --out-dir (openapi.json + palbase-config.json)

Switch branches later with 'palbase ios use <branch>'.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			stdout := cmd.OutOrStdout()
			// --json: the summary is the ONLY stdout so a script can parse it;
			// the human progress lines are suppressed.
			human := stdout
			if jsonOut {
				human = io.Discard
			}

			suggested := detectXcodeBundleIDs(".")
			if len(suggested) > 0 {
				fmt.Fprintf(human, "detected bundle id(s) in Xcode project: %s\n", strings.Join(suggested, ", "))
			}

			// Guard the REST() call like web link: constructing the tree with
			// zero-value Resolvers must not panic. `ios link` is REST-only now —
			// it picks the PRODUCT (group) and binds/fetches over the Management
			// API; no project.list (env-project) picker.
			var rest restDoer
			if r.REST != nil {
				rest = r.REST()
			}
			// Branch = the linked branch, else main (same as `palbase spec`).
			branch := "main"
			if cfg, cfgErr := auth.LoadProjectConfig(); cfgErr == nil && cfg.DefaultEnv != "" {
				branch = cfg.DefaultEnv
			}

			deps := iosLinkDeps{
				rest:        rest,
				lookup:      lookupSpecTarget(r),
				fetch:       fetchRemoteOpenAPISpec,
				list:        studioBindingLister(rest),
				cfgFetch:    studioConfigArtifactFetch(rest),
				stdin:       os.Stdin,
				interactive: isInteractive(),
			}
			summary, err := runIOSLink(ctx, deps, iosLinkOpts{
				branch:    branch,
				group:     groupFlag,
				app:       appFlag,
				name:      nameFlag,
				bundleIDs: bundleIDs,
				outDir:    outDir,
				suggested: suggested,
			}, human)
			if err != nil {
				return err
			}
			// Persist the linked production ref + ios app id so `palbase ios use
			// <branch>` and `palbase spec` know which project/app to act on without
			// re-resolving. This is what makes the product the "linked project".
			cfg, _ := auth.LoadProjectConfig()
			if cfg == nil {
				cfg = &auth.ProjectConfig{}
			}
			cfg.Ref = summary.Ref
			cfg.IOSAppID = summary.AppID
			_ = auth.SaveProjectConfig(cfg)
			if jsonOut {
				fmt.Fprintln(stdout, renderJSON(summary))
				return nil
			}
			printIOSNextSteps(stdout, summary.OutDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&groupFlag, "group", "", "Group id (defaults to your only group, or an interactive picker)")
	cmd.Flags().StringVar(&appFlag, "app", "", "App id (defaults to the group's first ios app; created when absent)")
	cmd.Flags().StringArrayVar(&bundleIDs, "bundle-id", nil, "App bundle id (overrides auto-detection from ./*.xcodeproj)")
	cmd.Flags().StringVar(&nameFlag, "name", "", "Display name for a newly registered app (default: current directory name)")
	cmd.Flags().StringVar(&outDir, "out-dir", "./.palbase", "Directory to write openapi.json + palbase-config.json")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit a JSON summary instead of human output")
	return cmd
}

// runIOSLink is the testable core: pick the PRODUCT (group) → resolve its
// production env-project → auto-detect the bundle → register/bind → fetch the
// plugin artifacts. Returns the summary the --json path serializes. The user is
// asked for ONE thing: which product (and only when they have more than one).
func runIOSLink(ctx context.Context, d iosLinkDeps, opts iosLinkOpts, w io.Writer) (*iosLinkSummary, error) {
	if opts.outDir == "" {
		opts.outDir = "./.palbase"
	}
	// `ios link` binds an app to a PRODUCT. A product is a group (the umbrella
	// that owns the product's environments) — so we pick the group, never an
	// environment. The user sees products, not the internal env-project split.
	grpID, err := pickIOSProduct(ctx, d, opts.group, w)
	if err != nil {
		return nil, err
	}

	// PRODUCTION is the target: a product's canonical environment is its
	// production env-project (schema guarantees exactly one per group). The user
	// never picks an environment — `ios use <branch>` switches within it later.
	prodRef, err := resolveProductionRef(ctx, d, grpID)
	if err != nil {
		return nil, err
	}

	// The app's bundle id is NOT prompted — it's the app's own identity, read
	// from ./*.xcodeproj (opts.suggested), and the SDK sends it on every request
	// (X-Palbase-Bundle) at runtime. Pick the MAIN app target's id: the shortest
	// non-test candidate (test/extension targets append a suffix to the app id).
	bundleID, err := pickAppBundleID(opts.bundleIDs, opts.suggested)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(w, "using bundle id %s (production %s)\n", bundleID, prodRef)

	appID, err := resolveIOSApp(ctx, d, grpID, opts.app, opts.name, w)
	if err != nil {
		return nil, err
	}

	// Bind the detected bundle to PRODUCTION only. Idempotent server-side for an
	// unchanged value; appID + projectRef ride in the PATH, identifier in the body.
	var res struct {
		OK bool `json:"ok"`
	}
	if err := d.rest.Do(ctx, http.MethodPut,
		"/api/v1/apps/"+appID+"/bindings/"+prodRef,
		map[string]any{"identifier": bundleID}, &res); err != nil {
		return nil, fmt.Errorf("bind production %s as %s: %w", prodRef, bundleID, err)
	}
	fmt.Fprintf(w, "✓ bound production %s as %s\n", prodRef, bundleID)

	// Fetch openapi.json + palbase-config.json for production — the exact
	// `palbase spec` core, so the two commands never drift on the artifacts.
	if err := runPullSpec(ctx, d.lookup, d.fetch, d.list, d.cfgFetch, prodRef, opts.branch, opts.outDir, appID, w); err != nil {
		return nil, err
	}

	return &iosLinkSummary{
		Group:    grpID,
		Ref:      prodRef,
		AppID:    appID,
		Bindings: []iosLinkBinding{{Env: prodRef, BundleID: bundleID}},
		OutDir:   opts.outDir,
	}, nil
}

// iosProjectRow is the GET /api/v1/projects/{ref} shape — we only need group_id.
type iosProjectRow struct {
	GroupID string `json:"group_id"`
}

// resolveProductionRef returns the group's production env-project ref (the schema
// guarantees exactly one env_preset='production' project per group). This is the
// ref an iOS app links to — the user never selects an environment.
func resolveProductionRef(ctx context.Context, d iosLinkDeps, grpID string) (string, error) {
	var envs []iosGroupEnvRow
	if err := d.rest.Do(ctx, http.MethodGet, "/api/v1/groups/"+grpID+"/environments", nil, &envs); err != nil {
		return "", fmt.Errorf("list environments: %w", err)
	}
	for _, e := range envs {
		if e.EnvPreset == "production" {
			return e.Ref, nil
		}
	}
	return "", fmt.Errorf("no production environment in this group — create the project's production environment in Studio first")
}

// pickAppBundleID chooses the app's bundle id WITHOUT prompting. An explicit
// --bundle-id flag wins. Otherwise it picks the MAIN app target from the ids
// detected in ./*.xcodeproj (opts.suggested): drop *Tests / *UITests targets,
// then take the SHORTEST remaining id (test/extension targets append a suffix to
// the app's own id, e.g. com.acme.app vs com.acme.app.widget). Only when nothing
// is detected does it error asking for --bundle-id.
func pickAppBundleID(flag []string, suggested []string) (string, error) {
	if len(flag) == 1 && flag[0] != "" {
		return flag[0], nil
	}
	if len(flag) > 1 {
		return "", fmt.Errorf("pass a single --bundle-id (the app's bundle id), not %d", len(flag))
	}
	var candidates []string
	for _, id := range suggested {
		low := strings.ToLower(id)
		if strings.HasSuffix(low, "tests") || strings.HasSuffix(low, "uitests") {
			continue // test/UITest targets are not the app
		}
		candidates = append(candidates, id)
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("could not detect the app bundle id from ./*.xcodeproj — pass --bundle-id <bundleId>")
	case 1:
		return candidates[0], nil
	default:
		// Multiple non-test targets → the main app is the shortest (others are
		// extensions/watch apps that suffix the app id).
		best := candidates[0]
		for _, c := range candidates[1:] {
			if len(c) < len(best) {
				best = c
			}
		}
		return best, nil
	}
}

// resolveIOSGroup derives the group from the ALREADY-SELECTED project ref — a
// project belongs to exactly one group (projects.group_id), so the user never
// picks a group: choosing the project IS choosing the group. `--group` stays as
// an explicit override for the rare case the ref lookup can't resolve it.
//
// The group is an internal umbrella (it owns a product's per-environment projects
// + the registered apps that bind across them); it is NOT a concept the user
// should navigate. `ios link` asks only for the project; `ios use` asks only for
// the branch.
func resolveIOSGroup(ctx context.Context, d iosLinkDeps, flag, ref string, w io.Writer) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if ref == "" {
		return "", fmt.Errorf("no project ref to resolve the group from — pass --ref <project> or --group <id>")
	}
	var proj iosProjectRow
	if err := d.rest.Do(ctx, http.MethodGet, "/api/v1/projects/"+ref, nil, &proj); err != nil {
		return "", fmt.Errorf("resolve group for project %q: %w", ref, err)
	}
	if proj.GroupID == "" {
		return "", fmt.Errorf("project %q has no group — pass --group <id>", ref)
	}
	return proj.GroupID, nil
}

// iosProductRow mirrors the GET /api/v1/groups row shape (id/name/plan).
type iosProductRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Plan string `json:"plan"`
}

// pickIOSProduct resolves WHICH product to link to. A product is a group. With
// --group, that. With one product, auto-select it (no prompt). With several, an
// interactive picker over PRODUCT names — never over environments or refs. The
// user picks one thing: their app's product.
func pickIOSProduct(ctx context.Context, d iosLinkDeps, flag string, w io.Writer) (string, error) {
	if flag != "" {
		return flag, nil
	}
	var products []iosProductRow
	if err := d.rest.Do(ctx, http.MethodGet, "/api/v1/groups", nil, &products); err != nil {
		return "", fmt.Errorf("list your products: %w", err)
	}
	switch {
	case len(products) == 0:
		return "", fmt.Errorf("no products in your account — create a project at the Palbase Studio dashboard first")
	case len(products) == 1:
		fmt.Fprintf(w, "linking to %s\n", products[0].Name)
		return products[0].ID, nil
	case !d.interactive:
		var b strings.Builder
		for _, p := range products {
			fmt.Fprintf(&b, "\n  %s  %s", p.ID, p.Name)
		}
		return "", fmt.Errorf("multiple products — pass --group <id>:%s", b.String())
	}
	fmt.Fprintln(w, "Select a project:")
	for i, p := range products {
		fmt.Fprintf(w, "  %d) %s\n", i+1, p.Name)
	}
	fmt.Fprint(w, "Enter number: ")
	line, _ := bufio.NewReader(d.stdin).ReadString('\n')
	choice, convErr := strconv.Atoi(strings.TrimSpace(line))
	if convErr != nil || choice < 1 || choice > len(products) {
		return "", fmt.Errorf("invalid selection: %q", strings.TrimSpace(line))
	}
	fmt.Fprintf(w, "linking to %s\n", products[choice-1].Name)
	return products[choice-1].ID, nil
}

// resolveIOSApp picks the app: --app verbatim, the group's first (live) ios
// app, or a fresh apps.create registration named --name / the cwd dir name.
func resolveIOSApp(ctx context.Context, d iosLinkDeps, grpID, appFlag, nameFlag string, w io.Writer) (string, error) {
	if appFlag != "" {
		return appFlag, nil
	}
	existing, err := resolveExistingIOSApp(ctx, d.rest, grpID, w)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}
	name := nameFlag
	if name == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		name = filepath.Base(cwd)
	}
	var created iosAppRow
	if err := d.rest.Do(ctx, http.MethodPost, "/api/v1/groups/"+grpID+"/apps", map[string]any{
		"platform": "ios",
		"name":     name,
	}, &created); err != nil {
		return "", fmt.Errorf("create app: %w", err)
	}
	fmt.Fprintf(w, "✓ registered ios app %q (%s)\n", name, created.ID)
	return created.ID, nil
}

// resolveExistingIOSApp finds the group's first live ios app WITHOUT creating
// one — the read-only half of resolveIOSApp. `palbase spec` uses it to
// auto-resolve --app (it fetches artifacts, it must NOT register apps — that's
// `ios link`'s job). Returns "" (not an error) when the group has no ios app,
// so spec can fall back to the openapi-only path.
func resolveExistingIOSApp(ctx context.Context, rest restDoer, grpID string, w io.Writer) (string, error) {
	var rows []iosAppRow
	if err := rest.Do(ctx, http.MethodGet, "/api/v1/groups/"+grpID+"/apps", nil, &rows); err != nil {
		return "", fmt.Errorf("list apps: %w", err)
	}
	for _, a := range rows {
		if a.Platform == "ios" && a.DeletedAt == nil {
			fmt.Fprintf(w, "using ios app %s (%s)\n", a.DisplayName, a.ID)
			return a.ID, nil
		}
	}
	return "", nil
}

// printIOSNextSteps prints the manual Xcode wiring the CLI cannot do itself
// (SPM package + build-tool plugin; Apple exposes no CLI for either).
func printIOSNextSteps(w io.Writer, outDir string) {
	fmt.Fprintf(w, `
next steps (Xcode):
  1. File ▸ Add Package Dependencies… → https://github.com/palgroup/palbackend-ios
  2. Add the "Palbe" library to your app target
  3. Target ▸ Build Phases ▸ Run Build Tool Plug-ins → add "PalbaseCodegen"
  4. Commit the %s directory (openapi.json + palbase-config.json)
Build the app — the plugin generates PalbaseGenerated.swift + Palbase-Info.plist; then `+"`import Palbe`"+` and use `+"`pb`"+`.
`, outDir)
}

// ── Xcode bundle-id detection ────────────────────────────────────────────────

// pbxBundleIDRe matches PRODUCT_BUNDLE_IDENTIFIER assignments in a
// project.pbxproj, quoted or bare.
var pbxBundleIDRe = regexp.MustCompile(`PRODUCT_BUNDLE_IDENTIFIER\s*=\s*"?([^"';\n]+?)"?\s*;`)

// parsePBXBundleIDs extracts the distinct PRODUCT_BUNDLE_IDENTIFIER values
// from project.pbxproj content, deduped and sorted. Values referencing a build
// setting ($(...)) are dropped — they cannot be resolved without a build.
func parsePBXBundleIDs(content string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range pbxBundleIDRe.FindAllStringSubmatch(content, -1) {
		id := strings.TrimSpace(m[1])
		if id == "" || strings.Contains(id, "$(") || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// detectXcodeBundleIDs best-effort scans dir's *.xcodeproj for bundle ids to
// suggest at the per-env prompt. No Xcode project → nil (bundle ids then come
// from --bundle-id flags or the prompt).
func detectXcodeBundleIDs(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.xcodeproj", "project.pbxproj"))
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		for _, id := range parsePBXBundleIDs(string(data)) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}
