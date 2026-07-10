package backend

// ios_link.go — `palbase ios link`
//
// Native link wires a platform slot to a Palbase PRODUCT without inspecting or
// modifying Xcode. The user answers ONE thing — which product (and only when
// they have several):
//
//  1. Pick the PRODUCT (a group). --group / your only product / a picker over
//     product names. The user never sees or picks an "environment" or a ref.
//  2. Resolve the product's PRODUCTION env-project (schema: exactly one per
//     group). That becomes the linked project ref (written to .palbase/config).
//  3. Reuse the locally persisted platform app id, or register a new app.
//  4. Fetch the shared `.palbase/openapi.json` plus the platform config at
//     `.palbase/ios/palbase-config.json` or `.palbase/macos/...`.
//  5. Print the manual Xcode wiring steps (SPM package + build plugin).
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

// iosLinkSummary is the --json output shape.
type iosLinkSummary struct {
	Group     string `json:"group"`
	Ref       string `json:"ref"` // the linked production env-project ref
	AppID     string `json:"app_id"`
	ConfigDir string `json:"config_dir"`
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
	platform string // ios or macos; supplied by the command
	branch   string
	group    string
	appID    string // locally persisted platform app id; empty on first link
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

// newMacOSCmd exposes the same native Xcode registration flow for a distinct
// macOS app registration. App Attest is intentionally absent on macOS.
func newMacOSCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "macos",
		Short: "Wire a macOS app to a Palbase project",
	}
	cmd.AddCommand(newAppleLinkCmd(r, "macos"))
	return cmd
}

// newIOSLinkCmd builds `palbase ios link`.
func newIOSLinkCmd(r Resolvers) *cobra.Command {
	return newAppleLinkCmd(r, "ios")
}

func newAppleLinkCmd(r Resolvers, platform string) *cobra.Command {
	var groupFlag string
	var jsonOut bool
	next := "Run link again to refresh the macOS production config."
	if platform == "ios" {
		next = "Switch branches later with 'palbase ios use <branch>'."
	}

	cmd := &cobra.Command{
		Use:   "link",
		Short: fmt.Sprintf("Link this %s Xcode project to a Palbase product and fetch its SDK config", platform),
		Long: fmt.Sprintf(`Wire a %s app slot to a Palbase product in one command.
You pick ONE thing: your product. Local project files are left untouched.

  1. pick your product (or --group; auto-selected if you have only one)
  2. its production environment is resolved automatically
  3. reuses this checkout's linked %s app or registers a new one
  4. writes shared .palbase/openapi.json and the platform config under
     .palbase/%s/palbase-config.json

%s`, platform, platform, platform, next),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			stdout := cmd.OutOrStdout()
			// --json: the summary is the ONLY stdout so a script can parse it;
			// the human progress lines are suppressed.
			human := stdout
			if jsonOut {
				human = io.Discard
			}
			if err := auth.EnsureProjectConfigGitignored(".gitignore"); err != nil {
				return fmt.Errorf("update .gitignore: %w", err)
			}

			// Guard the REST() call like web link: constructing the tree with
			// zero-value Resolvers must not panic. Native link is REST-only —
			// it picks the PRODUCT (group) and binds/fetches over the Management
			// API; no project.list (env-project) picker.
			var rest restDoer
			if r.REST != nil {
				rest = r.REST()
			}
			// Branch = the linked branch, else main (same as `palbase spec`).
			branch := "main"
			persistedAppID := ""
			if cfg, cfgErr := auth.LoadProjectConfig(); cfgErr == nil {
				if cfg.DefaultEnv != "" {
					branch = cfg.DefaultEnv
				}
				if platform == "ios" {
					persistedAppID = cfg.IOSAppID
				} else {
					persistedAppID = cfg.MacOSAppID
				}
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
				platform: platform,
				branch:   branch,
				group:    groupFlag,
				appID:    persistedAppID,
			}, human)
			if err != nil {
				return err
			}
			// Persist the linked production ref + concrete platform app id. iOS
			// and macOS keep independent slots.
			cfg, _ := auth.LoadProjectConfig()
			if cfg == nil {
				cfg = &auth.ProjectConfig{}
			}
			cfg.Ref = summary.Ref
			if platform == "ios" {
				cfg.IOSAppID = summary.AppID
			} else {
				cfg.MacOSAppID = summary.AppID
			}
			if err := auth.SaveProjectConfig(cfg); err != nil {
				return fmt.Errorf("save .palbase/config.json: %w", err)
			}
			if jsonOut {
				fmt.Fprintln(stdout, renderJSON(summary))
				return nil
			}
			printAppleNextSteps(stdout, platform, summary.ConfigDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&groupFlag, "group", "", "Group id (defaults to your only group, or an interactive picker)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit a JSON summary instead of human output")
	return cmd
}

// runIOSLink is the testable core: pick the PRODUCT (group) → resolve its
// production env-project → reuse/create the platform app → fetch the shared
// spec and platform config. It never reads Xcode files or mutates bindings.
func runIOSLink(ctx context.Context, d iosLinkDeps, opts iosLinkOpts, w io.Writer) (*iosLinkSummary, error) {
	if opts.platform != "ios" && opts.platform != "macos" {
		return nil, fmt.Errorf("native link platform must be ios or macos")
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

	appID, err := resolveAppleApp(ctx, d, grpID, opts.platform, opts.appID, w)
	if err != nil {
		return nil, err
	}
	// Persist the concrete app immediately. If a later config/spec fetch fails,
	// the next run can reuse this exact registration instead of creating another.
	if err := persistProjectAppSlot(prodRef, opts.platform, appID); err != nil {
		return nil, err
	}

	configDir := filepath.Join(".palbase", opts.platform)
	if err := runPullSpec(
		ctx, d.lookup, d.fetch, d.list, d.cfgFetch,
		prodRef, opts.branch, ".palbase", configDir, appID, w,
	); err != nil {
		return nil, err
	}

	return &iosLinkSummary{
		Group:     grpID,
		Ref:       prodRef,
		AppID:     appID,
		ConfigDir: configDir,
	}, nil
}

// persistProjectAppSlot records one concrete app registration while preserving
// every sibling platform slot. It intentionally runs before remote artifact
// fetches so a retry can reuse an app that was already created server-side.
func persistProjectAppSlot(ref, platform, appID string) error {
	cfg, _ := auth.LoadProjectConfig()
	if cfg == nil {
		cfg = &auth.ProjectConfig{}
	}
	cfg.Ref = ref
	switch platform {
	case "ios":
		cfg.IOSAppID = appID
	case "macos":
		cfg.MacOSAppID = appID
	case "web":
		cfg.WebAppID = appID
	default:
		return fmt.Errorf("unsupported app slot %q", platform)
	}
	if err := auth.SaveProjectConfig(cfg); err != nil {
		return fmt.Errorf("save .palbase/config.json: %w", err)
	}
	return nil
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

// resolveIOSGroup derives the product group from an already linked project ref.
// `ios link` selects the product directly; `ios use` supplies its stored ref and
// asks the user only for the branch. The optional flag remains an internal seam
// for callers that already know the group id.
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
	fmt.Fprintln(w, "Select a product:")
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

// resolveAppleApp reuses the locally persisted app id only when it still belongs
// to the selected product and platform. A missing, deleted, or mismatched id is
// replaced with a fresh platform app; remote apps are never guessed or mutated.
func resolveAppleApp(
	ctx context.Context,
	d iosLinkDeps,
	grpID, platform, persistedAppID string,
	w io.Writer,
) (string, error) {
	var rows []iosAppRow
	if err := d.rest.Do(ctx, http.MethodGet, "/api/v1/groups/"+grpID+"/apps", nil, &rows); err != nil {
		return "", fmt.Errorf("list apps: %w", err)
	}
	if persistedAppID != "" {
		for _, app := range rows {
			if app.ID != persistedAppID || app.DeletedAt != nil {
				continue
			}
			if app.Platform == platform {
				fmt.Fprintf(w, "using linked %s app %s (%s)\n", platform, app.DisplayName, app.ID)
				return persistedAppID, nil
			}
		}
		fmt.Fprintf(w, "linked %s app %s does not match the selected product and platform; registering a new one\n", platform, persistedAppID)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	name := filepath.Base(cwd)
	var created iosAppRow
	if err := d.rest.Do(ctx, http.MethodPost, "/api/v1/groups/"+grpID+"/apps", map[string]any{
		"platform": platform,
		"name":     name,
	}, &created); err != nil {
		return "", fmt.Errorf("create app: %w", err)
	}
	fmt.Fprintf(w, "✓ registered %s app %q (%s)\n", platform, name, created.ID)
	return created.ID, nil
}

// printIOSNextSteps prints the manual Xcode wiring the CLI cannot do itself
// (SPM package + build-tool plugin; Apple exposes no CLI for either).
func printAppleNextSteps(w io.Writer, platform, outDir string) {
	fmt.Fprintf(w, `
next steps (%s Xcode target):
  1. File ▸ Add Package Dependencies… → https://github.com/palgroup/palbackend-ios
  2. Add the "Palbe" library to your app target
  3. Target ▸ Build Phases ▸ Run Build Tool Plug-ins → add "PalbaseCodegen"
  4. Commit .palbase/openapi.json and %s/palbase-config.json
Build the app — the plugin generates PalbaseGenerated.swift + Palbase-Info.plist; then `+"`import Palbe`"+` and use `+"`pb`"+`.
`, platform, outDir)
}
