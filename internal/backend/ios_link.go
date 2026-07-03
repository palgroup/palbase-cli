package backend

// ios_link.go — `palbase ios link`
//
// The iOS symmetry of `palbase web link`: run in an Xcode project directory,
// ONE command wires the app to a Palbase project without a Studio visit:
//
//  1. Best-effort detects candidate bundle ids from ./*.xcodeproj (used as
//     prompt suggestions — a missing pbxproj is fine, flags/prompt cover it).
//  2. resolveOrLinkRef — --ref / .palbase/config.json / interactive picker.
//  3. Resolves the group (--group, the account's only group, or a picker).
//  4. Reuses the group's first ios app, or registers one (apps.create).
//  5. Binds a bundle id per environment (apps.configureBinding) — from
//     --bundle-id <envRef>=<bundleId> flags, or a per-env prompt on a TTY.
//     An env without a bundle id is skipped; at least one must be bound.
//  6. Fetches the SPM codegen plugin's inputs into --out-dir via the same
//     runPullSpec core `palbase spec` uses (openapi.json +
//     palbase-config.json).
//  7. Prints the manual Xcode wiring steps (SPM package + build plugin).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/palgroup/palbase-cli/internal/apps"
	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// iosGroupRow mirrors the groups.mine tRPC row shape.
type iosGroupRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Plan string `json:"plan"`
}

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
	AppID    string           `json:"app_id"`
	Bindings []iosLinkBinding `json:"bindings"`
	OutDir   string           `json:"out_dir"`
}

// iosLinkDeps carries the injectable seams so runIOSLink is testable without a
// live Studio or tenant host: the tRPC transport (an httptest-backed
// *studio.Client in tests) plus the same four runPullSpec seams `palbase spec`'s
// tests stub.
type iosLinkDeps struct {
	studio      apps.Studio
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
	bundleIDs []string // raw --bundle-id values, <envRef>=<bundleId>
	outDir    string
	suggested []string // bundle ids detected from ./*.xcodeproj
}

// newIOSCmd builds the `palbase ios` command group.
func newIOSCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ios",
		Short: "Wire an iOS app to a Palbase project",
	}
	cmd.AddCommand(newIOSLinkCmd(r))
	return cmd
}

// newIOSLinkCmd builds `palbase ios link`.
func newIOSLinkCmd(r Resolvers) *cobra.Command {
	var refFlag, groupFlag, appFlag, nameFlag, outDir string
	var bundleIDs []string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "link",
		Short: "Register this Xcode project's app, bind bundle ids and fetch SDK config",
		Long: `Wire the iOS app in the current directory to a Palbase project in one
command — no Studio visit needed:

  1. registers an ios app under your group when none exists
  2. binds a bundle id per environment
  3. downloads the SPM codegen plugin's inputs into --out-dir
     (openapi.json + palbase-config.json)

Bundle ids come from --bundle-id <envRef>=<bundleId> flags (repeatable), or an
interactive per-env prompt seeded with the ids detected in ./*.xcodeproj. An
environment without a bundle id is skipped; at least one must be bound.`,
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

			// Guard the Studio() call like web link: constructing the tree with
			// zero-value Resolvers must not panic.
			var sc *studio.Client
			if r.Studio != nil {
				sc = r.Studio()
			}
			ref, err := resolveOrLinkRef(ctx, refFlag, sc, human)
			if err != nil {
				return err
			}
			// Branch = the linked branch, else main (same as `palbase spec`).
			branch := "main"
			if cfg, cfgErr := auth.LoadProjectConfig(); cfgErr == nil && cfg.DefaultEnv != "" {
				branch = cfg.DefaultEnv
			}

			deps := iosLinkDeps{
				studio:      sc,
				lookup:      lookupSpecTarget(r),
				fetch:       fetchRemoteOpenAPISpec,
				list:        studioBindingLister(sc),
				cfgFetch:    studioConfigArtifactFetch(sc),
				stdin:       os.Stdin,
				interactive: isInteractive(),
			}
			summary, err := runIOSLink(ctx, deps, iosLinkOpts{
				ref:       ref,
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
			if jsonOut {
				fmt.Fprintln(stdout, renderJSON(summary))
				return nil
			}
			printIOSNextSteps(stdout, summary.OutDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (defaults to .palbase/config.json; required in non-interactive shells)")
	cmd.Flags().StringVar(&groupFlag, "group", "", "Group id (defaults to your only group, or an interactive picker)")
	cmd.Flags().StringVar(&appFlag, "app", "", "App id (defaults to the group's first ios app; created when absent)")
	cmd.Flags().StringArrayVar(&bundleIDs, "bundle-id", nil, "Per-env bundle id as <envRef>=<bundleId> (repeatable)")
	cmd.Flags().StringVar(&nameFlag, "name", "", "Display name for a newly registered app (default: current directory name)")
	cmd.Flags().StringVar(&outDir, "out-dir", "./Palbase", "Directory to write openapi.json + palbase-config.json")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit a JSON summary instead of human output")
	return cmd
}

// runIOSLink is the testable core: resolve group → app → per-env bundle ids,
// bind each, then fetch the plugin artifacts via runPullSpec. Returns the
// summary the --json path serializes.
func runIOSLink(ctx context.Context, d iosLinkDeps, opts iosLinkOpts, w io.Writer) (*iosLinkSummary, error) {
	if opts.outDir == "" {
		opts.outDir = "./Palbase"
	}
	// ONE buffered reader for every interactive read — mixing per-prompt
	// readers over the same stdin would swallow buffered lines between them.
	reader := bufio.NewReader(d.stdin)

	grpID, err := resolveIOSGroup(ctx, d, opts.group, reader, w)
	if err != nil {
		return nil, err
	}
	appID, err := resolveIOSApp(ctx, d, grpID, opts.app, opts.name, w)
	if err != nil {
		return nil, err
	}

	var envs []iosGroupEnvRow
	if err := d.studio.Query(ctx, "groups.environments", map[string]any{"grpId": grpID}, &envs); err != nil {
		return nil, fmt.Errorf("groups.environments: %w", err)
	}
	if len(envs) == 0 {
		return nil, fmt.Errorf("group %s has no environments — create one at the Palbase Studio dashboard first", grpID)
	}
	flagIDs, err := parseBundleIDFlags(opts.bundleIDs)
	if err != nil {
		return nil, err
	}
	// Typo guard: a --bundle-id for an env that doesn't exist would otherwise
	// be silently ignored (the loop below only visits real envs).
	for env := range flagIDs {
		if !envKnown(envs, env) {
			return nil, fmt.Errorf("--bundle-id: unknown environment %q (environments: %s)", env, envRefList(envs))
		}
	}

	bindings := resolveIOSBindings(d, envs, flagIDs, opts.suggested, reader, w)
	if len(bindings) == 0 {
		return nil, fmt.Errorf("no environments bound — pass --bundle-id <envRef>=<bundleId> (environments: %s)", envRefList(envs))
	}

	// Bind each (env × bundle id). configureBinding is idempotent server-side
	// for an unchanged value; a real conflict surfaces as the server's error.
	for _, b := range bindings {
		var res struct {
			OK bool `json:"ok"`
		}
		if err := d.studio.Mutation(ctx, "apps.configureBinding", map[string]any{
			"appId":      appID,
			"projectRef": b.Env,
			"identifier": b.BundleID,
		}, &res); err != nil {
			return nil, fmt.Errorf("bind env %s as %s: %w", b.Env, b.BundleID, err)
		}
		fmt.Fprintf(w, "✓ bound env %s as %s\n", b.Env, b.BundleID)
	}

	// Fetch openapi.json + palbase-config.json — the exact `palbase spec` core, so
	// the two commands can never drift on the artifact contents.
	if err := runPullSpec(ctx, d.lookup, d.fetch, d.list, d.cfgFetch, opts.ref, opts.branch, opts.outDir, appID, w); err != nil {
		return nil, err
	}

	return &iosLinkSummary{Group: grpID, AppID: appID, Bindings: bindings, OutDir: opts.outDir}, nil
}

// resolveIOSGroup picks the group: --group verbatim, the account's only group,
// an interactive picker (pickProject's pattern), or an actionable error listing
// the choices for non-interactive callers.
func resolveIOSGroup(ctx context.Context, d iosLinkDeps, flag string, reader *bufio.Reader, w io.Writer) (string, error) {
	if flag != "" {
		return flag, nil
	}
	var groups []iosGroupRow
	if err := d.studio.Query(ctx, "groups.mine", nil, &groups); err != nil {
		return "", fmt.Errorf("groups.mine: %w", err)
	}
	switch {
	case len(groups) == 0:
		return "", fmt.Errorf("no groups in your account — create a project at the Palbase Studio dashboard first")
	case len(groups) == 1:
		fmt.Fprintf(w, "using group %s (%s)\n", groups[0].Name, groups[0].ID)
		return groups[0].ID, nil
	case !d.interactive:
		var b strings.Builder
		for _, g := range groups {
			fmt.Fprintf(&b, "\n  %s  %s", g.ID, g.Name)
		}
		return "", fmt.Errorf("multiple groups in your account — pass --group <id>:%s", b.String())
	}
	fmt.Fprintln(w, "Select a group:")
	for i, g := range groups {
		fmt.Fprintf(w, "  %d) %s (%s)\n", i+1, g.Name, g.ID)
	}
	fmt.Fprint(w, "Enter number: ")
	line, readErr := readTrimmedLine(reader)
	if readErr != nil && line == "" {
		return "", fmt.Errorf("invalid selection: %w", readErr)
	}
	choice, convErr := strconv.Atoi(line)
	if convErr != nil || choice < 1 || choice > len(groups) {
		return "", fmt.Errorf("invalid selection: %q", line)
	}
	return groups[choice-1].ID, nil
}

// resolveIOSApp picks the app: --app verbatim, the group's first (live) ios
// app, or a fresh apps.create registration named --name / the cwd dir name.
func resolveIOSApp(ctx context.Context, d iosLinkDeps, grpID, appFlag, nameFlag string, w io.Writer) (string, error) {
	if appFlag != "" {
		return appFlag, nil
	}
	existing, err := resolveExistingIOSApp(ctx, d.studio, grpID, w)
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
	if err := d.studio.Mutation(ctx, "apps.create", map[string]any{
		"groupId":     grpID,
		"platform":    "ios",
		"displayName": name,
	}, &created); err != nil {
		return "", fmt.Errorf("apps.create: %w", err)
	}
	fmt.Fprintf(w, "✓ registered ios app %q (%s)\n", name, created.ID)
	return created.ID, nil
}

// resolveExistingIOSApp finds the group's first live ios app WITHOUT creating
// one — the read-only half of resolveIOSApp. `palbase spec` uses it to
// auto-resolve --app (it fetches artifacts, it must NOT register apps — that's
// `ios link`'s job). Returns "" (not an error) when the group has no ios app,
// so spec can fall back to the openapi-only path.
func resolveExistingIOSApp(ctx context.Context, studio apps.Studio, grpID string, w io.Writer) (string, error) {
	var rows []iosAppRow
	if err := studio.Query(ctx, "apps.list", map[string]any{"groupId": grpID}, &rows); err != nil {
		return "", fmt.Errorf("apps.list: %w", err)
	}
	for _, a := range rows {
		if a.Platform == "ios" && a.DeletedAt == nil {
			fmt.Fprintf(w, "using ios app %s (%s)\n", a.DisplayName, a.ID)
			return a.ID, nil
		}
	}
	return "", nil
}

// resolveIOSBindings decides each env's bundle id: the --bundle-id flag wins;
// otherwise a TTY prompts (empty input skips the env) and a non-TTY skips with
// a warning. The caller enforces "at least one bound".
func resolveIOSBindings(d iosLinkDeps, envs []iosGroupEnvRow, flagIDs map[string]string, suggested []string, reader *bufio.Reader, w io.Writer) []iosLinkBinding {
	var out []iosLinkBinding
	for _, e := range envs {
		id := flagIDs[e.Ref]
		switch {
		case id != "":
		case d.interactive:
			id = promptBundleID(reader, e, suggested, w)
			if id == "" {
				fmt.Fprintf(w, "skipping env %s (no bundle id entered)\n", e.Ref)
				continue
			}
		default:
			fmt.Fprintf(w, "warning: skipping env %s — no --bundle-id %s=<bundleId> given\n", e.Ref, e.Ref)
			continue
		}
		out = append(out, iosLinkBinding{Env: e.Ref, BundleID: id})
	}
	return out
}

// promptBundleID asks for one env's bundle id on a TTY, showing the
// pbxproj-detected ids as suggestions. Empty input means "skip this env".
func promptBundleID(reader *bufio.Reader, e iosGroupEnvRow, suggested []string, w io.Writer) string {
	label := e.Ref
	switch {
	case e.EnvDisplayName != "":
		label = fmt.Sprintf("%s (%s)", e.EnvDisplayName, e.Ref)
	case e.EnvPreset != "":
		label = fmt.Sprintf("%s (%s)", e.Ref, e.EnvPreset)
	}
	if len(suggested) > 0 {
		fmt.Fprintf(w, "bundle id for %s [detected: %s] (empty to skip): ", label, strings.Join(suggested, ", "))
	} else {
		fmt.Fprintf(w, "bundle id for %s (empty to skip): ", label)
	}
	line, _ := readTrimmedLine(reader)
	return line
}

// readTrimmedLine reads one line (trimmed) from the shared interactive reader.
// EOF with partial content still returns the content.
func readTrimmedLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	return strings.TrimSpace(line), err
}

// parseBundleIDFlags parses repeated --bundle-id <envRef>=<bundleId> values.
func parseBundleIDFlags(vals []string) (map[string]string, error) {
	out := make(map[string]string, len(vals))
	for _, v := range vals {
		env, id, ok := strings.Cut(v, "=")
		env, id = strings.TrimSpace(env), strings.TrimSpace(id)
		if !ok || env == "" || id == "" {
			return nil, fmt.Errorf("--bundle-id %q: expected <envRef>=<bundleId>", v)
		}
		if prev, dup := out[env]; dup && prev != id {
			return nil, fmt.Errorf("--bundle-id: environment %q given twice with different bundle ids (%q, %q)", env, prev, id)
		}
		out[env] = id
	}
	return out, nil
}

func envKnown(envs []iosGroupEnvRow, ref string) bool {
	for _, e := range envs {
		if e.Ref == ref {
			return true
		}
	}
	return false
}

func envRefList(envs []iosGroupEnvRow) string {
	refs := make([]string, len(envs))
	for i, e := range envs {
		refs[i] = e.Ref
	}
	return strings.Join(refs, ", ")
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
