package backend

// web_link.go — `palbase web link` / `palbase web unlink`
//
// `palbase web link` wires a web project (package.json present) to a Palbase
// project:
//   1. Verifies package.json exists in the cwd.
//   2. Uses the project + environment this checkout is bound to
//      (`palbase link <project>` / `palbase link <ref>`; --project /
//      --environment narrow a SELECTION, and are refused once a link answers.
//      `palbase env use` is what this named, and it went at the v2 cutover).
//   3. Ensures .gitignore covers .palbase/config.json (the selection is
//      per-machine; generated inputs under .palbase remain trackable).
//   4. Fetches the SDK generator's committed inputs (Palbase/openapi.json +
//      Palbase/palbase-config.json via the webLinkArtifacts seam), then runs
//      @palbase/web's palbe-gen for the first palbe.gen.ts when installed.
//   5. Patches package.json: adds scripts.predev / scripts.prebuild running
//      `palbe-gen --soft || exit 0` via a BYTE-SPLICE editor — only the
//      inserted entries are new bytes, every other byte of the file (key
//      order, nested objects, indentation, `&&` in script values) is left
//      untouched. An existing script with a different value is warned about,
//      never clobbered.
//   6. Inserts `import '<rel-to-gen>';` into the detected entry file —
//      directive-prologue aware ('use client' stays first) and multiline-
//      import aware (never splices mid-statement).
//   7. Warns LOUDLY (exit 0) when .gitignore ignores the gen file itself —
//      that file must be committed; the rule is reported, not edited.
//
// `palbase web unlink` removes .palbase/config.json (+ dir if empty), leaving
// the gen file and scripts untouched.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// webArtifactsDir is the committed directory the SDK generators read — the
// counterpart of the .palbase/ directory the native platforms read (openapi.json +
// palbase-config.json under ./Palbase).
const webArtifactsDir = "Palbase"

// webLinkArtifacts is the artifact-fetch seam. It resolves (or creates) the
// concrete web app registration under the selected PROJECT, fetches its
// app-bound config/key for the selected ENVIRONMENT, then writes the two
// committed files @palbase/web's `palbe-gen` consumes offline:
//
//	Palbase/openapi.json          the API contract
//	Palbase/palbase-config.json   {app_id, base_url, api_key, kind}
//
// There is NO `branch` in that config: the base_url and the api_key already
// identify the Environment, so a branch field would be a second name for the
// same runtime — and the Palbase branch no longer exists.
//
// retarget records the selected Environment as this checkout's selection even
// when the Project is unchanged — what `web use` does and `web link` must not
// (a plain re-link keeps the environment the checkout already targets).
//
// The CLI does NOT generate the client — client codegen is the SDKs' job
// (`palbe-gen`, shipped in @palbase/web). Tests replace the seam with a stub.
var webLinkArtifacts = func(ctx context.Context, r Resolvers, sel selection.Selection, retarget bool, w io.Writer) error {
	if r.REST == nil {
		return fmt.Errorf("management API is unavailable")
	}
	rest := r.REST()
	persistedAppID, err := persistedAppIDFor("web", sel)
	if err != nil {
		return err
	}
	appID, err := resolveWebApp(ctx, rest, sel.ProjectID, persistedAppID, w)
	if err != nil {
		return err
	}
	if err := persistProjectAppSlot("web", appID, &sel, retarget); err != nil {
		return err
	}

	art, err := studioConfigArtifactFetch(rest, r.Endpoints().PublicHost)(ctx, appID, sel.EnvironmentRef())
	if err != nil {
		return fmt.Errorf("fetch app config: %w", err)
	}
	if art.Platform != "web" {
		return fmt.Errorf("app %s is %s, not web", appID, art.Platform)
	}
	spec, err := managedSpecFetch(rest)(ctx, sel.EnvironmentRef(), w)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(webArtifactsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(webArtifactsDir, "openapi.json"), spec, 0o644); err != nil {
		return err
	}
	cfg := map[string]any{
		"app_id":   art.AppID,
		"base_url": art.BaseURL,
		"api_key":  art.APIKey,
		"kind":     art.Kind,
	}
	if art.OAuth != nil {
		cfg["oauth"] = art.OAuth
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(webArtifactsDir, "palbase-config.json"), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ wrote %s/openapi.json + %s/palbase-config.json (commit them)\n", webArtifactsDir, webArtifactsDir)
	return nil
}

// resolveWebApp reuses the local web app id only when it still belongs to the
// selected PROJECT as a web app. Otherwise it registers a replacement instead of
// guessing another remote app.
func resolveWebApp(
	ctx context.Context,
	rest restDoer,
	projectID, persistedAppID string,
	w io.Writer,
) (string, error) {
	var rows []nativeAppRow
	if err := rest.Do(ctx, http.MethodGet,
		"/api/v2/projects/"+projectID+"/apps", nil, &rows); err != nil {
		return "", fmt.Errorf("list web apps: %w", err)
	}
	if persistedAppID != "" {
		for _, app := range rows {
			if app.ID != persistedAppID || app.DeletedAt != nil {
				continue
			}
			if app.Platform == "web" {
				fmt.Fprintf(w, "using linked web app %s (%s)\n", app.DisplayName, app.ID)
				return persistedAppID, nil
			}
		}
		fmt.Fprintf(w, "linked web app %s does not match the selected project and platform; registering a new one\n", persistedAppID)
	}

	name := "Web app"
	if cwd, err := os.Getwd(); err == nil && filepath.Base(cwd) != "." {
		name = filepath.Base(cwd)
	}
	var created nativeAppRow
	if err := rest.Do(ctx, http.MethodPost,
		"/api/v2/projects/"+projectID+"/apps",
		map[string]any{"platform": "web", "displayName": name}, &created); err != nil {
		return "", fmt.Errorf("create web app: %w", err)
	}
	fmt.Fprintf(w, "✓ registered web app %q (%s)\n", name, created.ID)
	return created.ID, nil
}

// webCmd holds the resolvers for the web command group.
type webCmd struct {
	r Resolvers
}

// webTypesCmd is the predev/prebuild hook value: the SDK's own generator
// (`palbe-gen`, shipped in @palbase/web's bin) regenerates palbe.gen.ts from
// the COMMITTED Palbase/ artifacts — offline, no CLI, no login. `--soft`
// swallows generator errors; `|| exit 0` covers a machine where @palbase/web
// isn't installed yet (command-not-found exits 127, which --soft alone
// cannot swallow).
const webTypesCmd = "palbe-gen --soft || exit 0"

// webTypesCmdFor keeps the automatic predev/prebuild regeneration pointed at
// the same file the one-shot link generation wrote. The default stays byte-for-
// byte compatible; a custom --out is single-quoted because package scripts run
// through a shell and the path is user-controlled.
func webTypesCmdFor(outFile string) string {
	if filepath.Clean(outFile) == "palbe.gen.ts" {
		return webTypesCmd
	}
	quoted := "'" + strings.ReplaceAll(outFile, "'", "'\"'\"'") + "'"
	return "palbe-gen --out " + quoted + " --soft || exit 0"
}

// newWebCmd builds the `palbase web` command group: link/unlink wire the
// project and use re-targets its Environment. The contract itself is refreshed
// by the shared `palbase spec`, and client generation lives in @palbase/web
// (`palbe-gen`), not here.
func newWebCmd(r Resolvers) *cobra.Command {
	wc := &webCmd{r: r}
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Wire a web project to a Palbase project",
	}
	cmd.AddCommand(wc.newWebLinkCmd(), wc.newWebUnlinkCmd(), wc.newWebUseCmd())
	return cmd
}

// newWebLinkCmd builds `palbase web link`.
func (wc *webCmd) newWebLinkCmd() *cobra.Command {
	var entryFlag string
	var outFlag string

	cmd := &cobra.Command{
		Use:   "link",
		Args:  cobra.NoArgs,
		Short: "Link this web project to the selected Palbase project and generate typed SDK code",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ctx := cmd.Context()

			outFile := outFlag
			if outFile == "" {
				outFile = "palbe.gen.ts"
			}
			// 1. Must run where package.json exists.
			if _, err := os.Stat("package.json"); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("web link must run in the web app root (package.json not found)")
				}
				return err
			}

			// 2. Keep the per-machine selection out of git while leaving generated
			// SDK inputs under .palbase trackable.
			if err := selection.EnsureGitignored(".gitignore"); err != nil {
				return fmt.Errorf("update .gitignore: %w", err)
			}

			// 3. The committed SDK-generator inputs (openapi.json +
			// palbase-config.json under Palbase/). The CLI stops at the inputs —
			// generating palbe.gen.ts is @palbase/web's job (palbe-gen).
			//
			// A BINDING CHANGES WHERE THEY COME FROM AND NOTHING ELSE.
			//
			// This used to be written as an early return: a checkout bound to a
			// project handed the whole command to the direct path and stopped
			// (`if handled { return err }`). Everything below — the generator,
			// the predev/prebuild hooks, the entry import, the gitignore guard —
			// was skipped, and the command exited 0 having done none of it. That
			// is the harder kind of wrong: `web link` reported success for a
			// project it had not wired up. The binding is a fact about WHICH
			// project answers, not about which command the person ran.
			if bound, err := linkToBoundProject(cmd, webPlatform, out); bound {
				if err != nil {
					return err
				}
			} else {
				// The cloud path: the SELECTED project + environment
				// (--project / --environment override headlessly).
				sel, err := wc.r.resolve(ctx)
				if err != nil {
					return err
				}
				if err := webLinkArtifacts(ctx, wc.r, sel, false, out); err != nil {
					return fmt.Errorf("fetch artifacts: %w", err)
				}
			}

			// 4. Install @palbase/web when the project doesn't have it yet —
			// without the generator there is no palbe.gen.ts, and without that
			// steps 6-7b all skip, so `link` would have to be run AGAIN after a
			// manual install. Setup is this command's job; do it here.
			if _, err := os.Stat(palbeGenBin); err != nil {
				ensurePalbeWeb(ctx, out)
			}

			// 5. First generation via the SDK's generator (predev/prebuild
			// hooks regenerate on every build from here on).
			generated, err := runPalbeGen(ctx, outFile, out)
			if err != nil {
				return err
			}

			// 5. Patch package.json scripts. Always — predev/prebuild pick up
			// generation on the next build regardless of whether outFile
			// exists today.
			if err := patchPackageJSONScriptsWithCommand(
				"package.json", webTypesCmdFor(outFile), out,
			); err != nil {
				return fmt.Errorf("patch package.json: %w", err)
			}

			if generated {
				// 6. Wire import in entry file.
				if err := wireEntryImport(entryFlag, outFile, out); err != nil {
					return fmt.Errorf("wire entry import: %w", err)
				}

				// 7. For Next.js App Router layouts, ensure providers.tsx exists so
				// the browser bundle also imports and configures the generated client.
				// (The server layout import covers Server Components; the client bundle
				// has its OWN module graph and needs a separate import via a "use client"
				// component — without this, pb is not configured in the browser and every
				// pb call throws "Palbe is not configured".)
				if err := wireNextProviders(entryFlag, outFile, out); err != nil {
					return fmt.Errorf("wire providers.tsx: %w", err)
				}

				// 7b. For Next.js App Router layouts, ensure proxy.ts exists so
				// the session cookie refreshes BEFORE the RSC tree renders. Server
				// Components cannot write cookies, so without this every RSC render
				// re-refreshes from the same stale cookie token; two such refreshes
				// landing more than palauth's 30s rotation grace apart makes the
				// reuse detector revoke the whole token family (force logout).
				if err := wireNextProxy(entryFlag, out); err != nil {
					return fmt.Errorf("wire proxy.ts: %w", err)
				}
			} else {
				// outFile doesn't exist yet (SDK not installed): writing the
				// import now would leave the project unable to resolve it
				// until install. Defer instead of a "successful" link with a
				// dangling import.
				fmt.Fprintf(out, "\nnote: skipping the entry-file import until %s exists — after installing @palbase/web, re-run `palbase web link` to wire it in\n", outFile)
			}

			// 8. Gitignore guard for the GEN file (warn only, never edit the rule).
			checkGitignoreGuard(outFile, out)

			return nil
		},
	}
	cmd.Flags().StringVar(&entryFlag, "entry", "", "Entry file to wire the import into (auto-detected when absent)")
	cmd.Flags().StringVar(&outFlag, "out", "", "Gen file name (default: palbe.gen.ts)")
	return cmd
}

// palbeGenBin locates the project-local palbe-gen binary @palbase/web ships.
// A var so tests can point it at a stub.
var palbeGenBin = filepath.Join("node_modules", ".bin", "palbe-gen")

// projectPackageManagers maps a lockfile to the argv that ADDS a dependency in
// the manager that wrote it. Order matters only for reporting — a project has
// one lockfile. npm is the fallback for a project that has none yet.
//
// Unlike installNodeDeps (backend.go), which may assume npm because it runs in
// a Palbase-scaffolded project, `web link` runs in the USER's app: running npm
// in a pnpm workspace would drop a package-lock.json next to pnpm-lock.yaml and
// desync the tree.
var projectPackageManagers = []struct {
	lockfile string
	argv     []string
}{
	{"pnpm-lock.yaml", []string{"pnpm", "add"}},
	{"yarn.lock", []string{"yarn", "add"}},
	{"bun.lockb", []string{"bun", "add"}},
	{"bun.lock", []string{"bun", "add"}},
	{"package-lock.json", []string{"npm", "install"}},
}

// addDepArgv picks the install command for the project in the cwd.
func addDepArgv() []string {
	for _, pm := range projectPackageManagers {
		if _, err := os.Stat(pm.lockfile); err == nil {
			return pm.argv
		}
	}
	return []string{"npm", "install"}
}

// ensurePalbeWeb installs @palbase/web when the project doesn't have it yet, so
// `web link` is ONE command instead of link → install → link. Best-effort: a
// failed install only means the caller falls back to the manual follow-up, so
// it warns and returns rather than failing the link.
//
// `latest` is deliberate, matching how the backend template pins @palbase/backend:
// a version baked into this binary goes stale the next time the SDK majors.
//
// A var so tests can stub it — the real one shells out to a package manager.
var ensurePalbeWeb = func(ctx context.Context, w io.Writer) {
	argv := addDepArgv()
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(w, "  %s not found in PATH — install @palbase/web yourself, then re-run `palbase web link`\n", argv[0])
		return
	}
	fmt.Fprintf(w, "  installing @palbase/web (%s) ...\n", strings.Join(argv, " "))
	c := exec.CommandContext(ctx, bin, append(argv[1:], webPkg+"@latest")...)
	c.Stdout = w
	c.Stderr = w
	if err := c.Run(); err != nil {
		fmt.Fprintf(w, "  warning: could not install %s (%v) — install it yourself, then re-run `palbase web link`\n", webPkg, err)
	}
}

// webPkg is the client SDK `web link` generates against.
const webPkg = "@palbase/web"

// runPalbeGen runs the SDK's generator for palbe.gen.ts when @palbase/web is
// installed; when it isn't, it prints the follow-up instead of failing the
// caller. The returned bool reports whether outFile now exists — callers MUST
// NOT wire an import to it when it doesn't, or the command "succeeds" while
// leaving a dangling import the project can't resolve.
//
// Installing the SDK is NOT done here: `web link` (the setup command) does it
// on its own path. `web use` only flips the target environment, and silently
// adding a dependency during that is a side effect nobody asked for.
func runPalbeGen(ctx context.Context, outFile string, w io.Writer) (bool, error) {
	if _, err := os.Stat(palbeGenBin); err != nil {
		fmt.Fprintf(w, "  %s not installed — install it, then run `npx palbe-gen`\n", webPkg)
		return false, nil
	}
	c := exec.CommandContext(ctx, palbeGenBin, "--out", outFile)
	c.Stdout = w
	c.Stderr = w
	if err := c.Run(); err != nil {
		return false, fmt.Errorf("palbe-gen: %w", err)
	}
	return true, nil
}

// newWebUnlinkCmd builds `palbase web unlink`.
func (wc *webCmd) newWebUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Detach this web project from its Palbase project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			// THE SELECTION, not `.palbase/config.json`.
			//
			// Those were the same file until the selection moved out of the way of
			// the EVALUATED CONFIG DOCUMENT — the flags, buckets and secret names
			// `palbase build` writes there from config/*.ts. This line was left
			// behind, so `web unlink` deleted a build output: `palbase plan` would
			// then report "nothing declared" for config and secrets about a project
			// that declares plenty, which is a wrong answer rather than an error.
			cfgPath := selection.ConfigPath("")
			if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove the selection: %w", err)
			}

			// Remove .palbase/ if empty.
			if entries, err := os.ReadDir(".palbase"); err == nil && len(entries) == 0 {
				_ = os.Remove(".palbase")
			}

			// unlink doesn't know the --out the link used, so speak generically.
			fmt.Fprintf(out, "✓ unlinked — removed %s\n", cfgPath)
			fmt.Fprintln(out, "  left in place (remove manually if you are dropping Palbase for good):")
			fmt.Fprintln(out, "    - the generated client file and its entry-file import")
			fmt.Fprintln(out, "    - the predev/prebuild scripts in package.json")
			fmt.Fprintln(out, "    - app/providers.tsx (Next.js App Router only)")
			fmt.Fprintln(out, "  re-link with `palbase web link`")
			return nil
		},
	}
}

// ── package.json byte-splice editing ─────────────────────────────────────────
//
// The editor NEVER round-trips the file through Go data structures: it locates
// the byte range of the `scripts` object with json.Decoder.InputOffset() and
// splices the new entries into the ORIGINAL bytes. Everything outside the
// splice — key order at every depth, nested objects, indentation quirks,
// `&&` in script values — stays byte-identical.

// orderedKV is a single key-value pair in a JSON object, preserving insertion
// order. Used only to INSPECT the existing scripts (which hooks are present,
// with what value) — never to re-serialise the file.
type orderedKV struct {
	key string
	raw json.RawMessage
}

// parseOrderedObject parses a JSON object into a slice of key-value pairs,
// preserving key order.
func parseOrderedObject(data []byte) ([]orderedKV, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("expected '{', got %v", tok)
	}

	var pairs []orderedKV
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key, got %T", keyTok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		pairs = append(pairs, orderedKV{key: key, raw: raw})
	}
	return pairs, nil
}

// scriptsLocation is the byte geography of package.json the splice needs.
type scriptsLocation struct {
	found    bool
	valStart int // offset of the '{' opening the scripts value
	valEnd   int // offset just past the '}' closing the scripts value
	topEnd   int // offset of the top-level closing '}'
}

// locatePackageJSONScripts walks the top-level object with json.Decoder and
// records the exact byte range of the `scripts` value (when present) plus the
// top-level closing brace, using InputOffset() — no re-serialisation.
func locatePackageJSONScripts(data []byte) (scriptsLocation, error) {
	var loc scriptsLocation
	dec := json.NewDecoder(bytes.NewReader(data))

	tok, err := dec.Token()
	if err != nil {
		return loc, err
	}
	if tok != json.Delim('{') {
		return loc, fmt.Errorf("expected a top-level object")
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return loc, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return loc, fmt.Errorf("expected object key, got %T", keyTok)
		}
		afterKey := int(dec.InputOffset()) // just past the key's closing quote
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return loc, err
		}
		if key == "scripts" && !loc.found {
			// Value starts at the first non-(whitespace|colon) byte after the key.
			start := afterKey
			for start < len(data) {
				if data[start] == ':' || data[start] == ' ' || data[start] == '\t' || data[start] == '\n' || data[start] == '\r' {
					start++
					continue
				}
				break
			}
			loc.found = true
			loc.valStart = start
			loc.valEnd = int(dec.InputOffset()) // just past the value
		}
	}
	if _, err := dec.Token(); err != nil { // top-level '}'
		return loc, err
	}
	loc.topEnd = int(dec.InputOffset()) - 1 // offset OF the closing '}'
	return loc, nil
}

// patchPackageJSONScripts adds scripts.predev / scripts.prebuild via byte
// splice. Hooks already set to a different value are warned about and left
// untouched; when nothing is missing the file is not rewritten at all.
func patchPackageJSONScripts(pkgPath string, w io.Writer) error {
	return patchPackageJSONScriptsWithCommand(pkgPath, webTypesCmd, w)
}

func patchPackageJSONScriptsWithCommand(pkgPath, typesCmd string, w io.Writer) error {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return err
	}

	loc, err := locatePackageJSONScripts(data)
	if err != nil {
		return fmt.Errorf("parse package.json: %w", err)
	}

	if !loc.found {
		return os.WriteFile(pkgPath, spliceNewScriptsObject(data, loc.topEnd, typesCmd), 0o644)
	}

	pairs, err := parseOrderedObject(data[loc.valStart:loc.valEnd])
	if err != nil {
		return fmt.Errorf("parse package.json scripts: %w", err)
	}

	var missing []string
	for _, hook := range []string{"predev", "prebuild"} {
		present := false
		for _, kv := range pairs {
			if kv.key != hook {
				continue
			}
			present = true
			var existing string
			_ = json.Unmarshal(kv.raw, &existing)
			if existing != typesCmd {
				fmt.Fprintf(w, "warning: scripts.%s is already set to %q — skipping (suggested value: %q)\n", hook, existing, typesCmd)
			}
			break
		}
		if !present {
			missing = append(missing, hook)
		}
	}
	if len(missing) == 0 {
		return nil // byte-identical: don't touch the file
	}

	return os.WriteFile(pkgPath, spliceScriptEntries(data, loc, missing, typesCmd), 0o644)
}

// spliceScriptEntries inserts the missing hook entries at the END of the
// existing scripts object (right after its last entry), leaving every other
// byte alone.
func spliceScriptEntries(data []byte, loc scriptsLocation, hooks []string, typesCmd string) []byte {
	keyIndent := indentOfLineAt(data, loc.valStart)
	entryIndent := scriptsEntryIndent(data, loc, keyIndent)

	last := lastNonWS(data, loc.valStart+1, loc.valEnd-1)
	var b strings.Builder
	if last < 0 {
		// Empty scripts object: open it across lines.
		for i, h := range hooks {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n" + entryIndent + encodeJSONString(h) + ": " + encodeJSONString(typesCmd))
		}
		b.WriteString("\n" + keyIndent)
		return splice(data, loc.valStart+1, b.String())
	}
	for _, h := range hooks {
		b.WriteString(",\n" + entryIndent + encodeJSONString(h) + ": " + encodeJSONString(typesCmd))
	}
	return splice(data, last+1, b.String())
}

// spliceNewScriptsObject inserts a whole `"scripts": {...}` block before the
// top-level closing brace (stable choice: scripts lands last, like the
// previous appended-at-end behaviour).
func spliceNewScriptsObject(data []byte, topEnd int, typesCmd string) []byte {
	unit := topUnitIndent(data)
	entryIndent := unit + unit

	var inner strings.Builder
	for i, h := range []string{"predev", "prebuild"} {
		if i > 0 {
			inner.WriteString(",")
		}
		inner.WriteString("\n" + entryIndent + encodeJSONString(h) + ": " + encodeJSONString(typesCmd))
	}
	block := `"scripts": {` + inner.String() + "\n" + unit + "}"

	brace := bytes.IndexByte(data, '{')
	last := lastNonWS(data, brace+1, topEnd)
	if last < 0 {
		// Empty top-level object.
		return splice(data, brace+1, "\n"+unit+block+"\n")
	}
	return splice(data, last+1, ",\n"+unit+block)
}

// splice returns data with text inserted at offset `at`.
func splice(data []byte, at int, text string) []byte {
	out := make([]byte, 0, len(data)+len(text))
	out = append(out, data[:at]...)
	out = append(out, text...)
	out = append(out, data[at:]...)
	return out
}

// lastNonWS returns the index of the last non-whitespace byte in data[from:to),
// or -1 when the range is all whitespace.
func lastNonWS(data []byte, from, to int) int {
	for i := to - 1; i >= from; i-- {
		if data[i] == ' ' || data[i] == '\t' || data[i] == '\n' || data[i] == '\r' {
			continue
		}
		return i
	}
	return -1
}

// indentOfLineAt returns the leading whitespace of the line containing offset.
func indentOfLineAt(data []byte, off int) string {
	lineStart := bytes.LastIndexByte(data[:off], '\n') + 1
	i := lineStart
	for i < len(data) && (data[i] == ' ' || data[i] == '\t') {
		i++
	}
	return string(data[lineStart:i])
}

// scriptsEntryIndent detects the indentation of the scripts object's entries
// from its first entry line; falls back to the key line's indent + 2 spaces.
func scriptsEntryIndent(data []byte, loc scriptsLocation, keyIndent string) string {
	inner := data[loc.valStart+1 : loc.valEnd-1]
	if nl := bytes.IndexByte(inner, '\n'); nl >= 0 {
		j := nl + 1
		k := j
		for k < len(inner) && (inner[k] == ' ' || inner[k] == '\t') {
			k++
		}
		if k > j && k < len(inner) {
			return string(inner[j:k])
		}
	}
	return keyIndent + "  "
}

// topUnitIndent detects the file's top-level indent unit from the first key
// line; defaults to two spaces (npm's package.json style).
func topUnitIndent(data []byte) string {
	brace := bytes.IndexByte(data, '{')
	if brace < 0 {
		return "  "
	}
	nl := bytes.IndexByte(data[brace:], '\n')
	if nl < 0 {
		return "  "
	}
	i := brace + nl + 1
	j := i
	for j < len(data) && (data[j] == ' ' || data[j] == '\t') {
		j++
	}
	if j > i {
		return string(data[i:j])
	}
	return "  "
}

// encodeJSONString encodes s as a JSON string WITHOUT HTML escaping, so
// `&&`/`<`/`>` in script values stay readable.
func encodeJSONString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

// ── entry file import wiring ──────────────────────────────────────────────────

// autoEntryPaths is the ordered list of entry files to probe when --entry is
// not given. .jsx variants sit right after their .tsx counterpart — .tsx
// wins when a project somehow has both, but a plain-JS App Router layout is
// still found without needing --entry.
var autoEntryPaths = []string{
	"app/layout.tsx",
	"app/layout.jsx",
	"src/app/layout.tsx",
	"src/app/layout.jsx",
	"src/main.tsx",
	"src/main.ts",
	"main.tsx",
}

// wireEntryImport inserts `import '<rel>';` into the detected (or explicit)
// entry file. Idempotent on EXACT module-specifier match (a path ending in
// /<stem>), directive-prologue aware, and multiline-import aware.
func wireEntryImport(entryFlag, outFile string, w io.Writer) error {
	stem := strings.TrimSuffix(filepath.Base(outFile), ".ts") // e.g. "palbe.gen"

	entryPath := entryFlag
	if entryPath == "" {
		for _, candidate := range autoEntryPaths {
			if _, err := os.Stat(candidate); err == nil {
				entryPath = candidate
				break
			}
		}
	}

	if entryPath == "" {
		// Unknown layout — print manual instruction, exit 0.
		fmt.Fprintf(w, "note: could not detect an entry file — add the following import manually:\n")
		fmt.Fprintf(w, "  import './%s';\n", strings.TrimSuffix(outFile, ".ts"))
		return nil
	}

	// Compute the POSIX relative import path from the entry file's dir to the
	// gen file, extension stripped, with a leading ./ or ../.
	rel, err := filepath.Rel(filepath.Dir(entryPath), outFile)
	if err != nil {
		return fmt.Errorf("rel path from %s to %s: %w", filepath.Dir(entryPath), outFile, err)
	}
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".ts")
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	importLine := fmt.Sprintf("import '%s';", rel)

	body, err := os.ReadFile(entryPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", entryPath, err)
	}
	lines := strings.Split(string(body), "\n")

	// Idempotency: skip only on an EXACT specifier match — a quoted path that
	// IS the gen module ('./palbe.gen', '../palbe.gen'), not any substring
	// (e.g. './palbe.gen.extra' must NOT suppress the insert).
	for _, line := range lines {
		if lineImportsGen(line, stem) {
			return nil
		}
	}

	insertAt := entryImportInsertIndex(lines)
	newLines := make([]string, 0, len(lines)+1)
	newLines = append(newLines, lines[:insertAt]...)
	newLines = append(newLines, importLine)
	newLines = append(newLines, lines[insertAt:]...)
	return os.WriteFile(entryPath, []byte(strings.Join(newLines, "\n")), 0o644)
}

// entryImportInsertIndex returns the line index BEFORE which the generated
// import is inserted: after the last top-of-file import statement (consuming
// multiline imports whole), or — when there are no imports — right after the
// directive prologue ('use client' & friends must stay the first statement).
func entryImportInsertIndex(lines []string) int {
	i := 0
	// Directive prologue: directives, blank lines, line comments.
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "//") || isDirectiveLine(t) {
			i++
			continue
		}
		break
	}
	insertAt := i // no imports → insert right after the prologue

	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "//") {
			i++
			continue
		}
		if !isImportStart(t) {
			break // first real (non-import) code line
		}
		// Consume the whole statement: an unbalanced `import {` continues
		// until a line containing from '…'/from "…" or ending '; / ";.
		j := i
		for j < len(lines)-1 && !importStatementComplete(strings.TrimSpace(lines[j])) {
			j++
		}
		insertAt = j + 1
		i = j + 1
	}
	return insertAt
}

// isImportStart reports whether a trimmed line begins an import statement.
func isImportStart(t string) bool {
	return strings.HasPrefix(t, "import ") ||
		strings.HasPrefix(t, "import{") ||
		strings.HasPrefix(t, "import'") ||
		strings.HasPrefix(t, "import\"")
}

// importStatementComplete reports whether a trimmed line completes an import
// statement: it carries the from-clause, or terminates a (side-effect) import.
func importStatementComplete(t string) bool {
	if strings.Contains(t, "from '") || strings.Contains(t, "from \"") {
		return true
	}
	if strings.HasSuffix(t, "';") || strings.HasSuffix(t, "\";") {
		return true
	}
	// Bare side-effect import without semicolon: import './x'
	return strings.HasSuffix(t, "'") || strings.HasSuffix(t, "\"")
}

// isDirectiveLine reports whether a trimmed line is a module directive that
// must stay at the very top of the file.
func isDirectiveLine(t string) bool {
	for _, d := range []string{"use client", "use server", "use strict"} {
		for _, q := range []string{"'", `"`} {
			base := q + d + q
			if t == base || t == base+";" {
				return true
			}
		}
	}
	return false
}

// lineImportsGen reports whether the line imports the generated module: an
// import-ish line whose quoted specifier equals, or path-ends with, the gen
// stem. Exact match only — './palbe.gen.extra' does not count.
func lineImportsGen(line, stem string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "import") && !strings.Contains(t, "from ") {
		return false
	}
	for _, q := range quotedStrings(t) {
		if q == stem || q == "./"+stem || strings.HasSuffix(q, "/"+stem) {
			return true
		}
	}
	return false
}

// quotedStrings extracts the contents of '…' and "…" substrings of s.
func quotedStrings(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\'' && c != '"' {
			continue
		}
		j := strings.IndexByte(s[i+1:], c)
		if j < 0 {
			break
		}
		out = append(out, s[i+1:i+1+j])
		i = i + 1 + j
	}
	return out
}

// ── gitignore guard ───────────────────────────────────────────────────────────

// checkGitignoreGuard prints a loud warning if .gitignore would ignore the gen
// file. It reports the rule, never edits it.
func checkGitignoreGuard(outFile string, w io.Writer) {
	data, err := os.ReadFile(".gitignore")
	if err != nil {
		return
	}
	content := string(data)
	basename := filepath.Base(outFile)
	ext := filepath.Ext(basename) // e.g. ".ts"
	stem := strings.TrimSuffix(basename, ext)

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Match: exact filename, *.gen.ts pattern, or exact out path.
		if line == basename || line == outFile {
			printGitignoreWarning(w, outFile)
			return
		}
		// *.gen.ts style glob: *.gen.<ext>
		if strings.HasPrefix(line, "*.") {
			suffix := line[1:] // e.g. ".gen.ts"
			if strings.HasSuffix(basename, suffix) {
				printGitignoreWarning(w, outFile)
				return
			}
		}
		// stem.* style: if the line is "palbe.gen.*" — treat generously.
		if strings.Contains(line, "*") && strings.HasPrefix(line, stem+".") {
			printGitignoreWarning(w, outFile)
			return
		}
	}
}

func printGitignoreWarning(w io.Writer, outFile string) {
	fmt.Fprintf(w, "\nWARNING: .gitignore appears to ignore %s — this file must be committed\n", outFile)
	fmt.Fprintf(w, "         (it is the typed client your app imports, not a build artifact)\n")
	fmt.Fprintf(w, "         Remove the matching .gitignore rule or force-add the file:\n")
	fmt.Fprintf(w, "           git add -f %s\n\n", outFile)
}

// ── Next.js providers.tsx wiring ─────────────────────────────────────────────

// nextAppLayouts lists the App Router layout paths that indicate a Next.js
// project. When the detected entry file is one of these, we also wire up
// providers.tsx (or .jsx) in the same directory so the CLIENT bundle imports
// and configures the gen file. Both .tsx and .jsx are listed — a plain-JS App
// Router project must get the same wiring, not a silent skip.
//
// Background: app/layout.tsx is a Server Component. Its module graph configures
// pb for the server. The browser's client-component bundle has a SEPARATE module
// graph — it never executes the layout's top-level imports. Without a "use client"
// component that also imports palbe.gen, pb.auth/pb.backend/etc. throw
// "Palbe is not configured" in every client component.
var nextAppLayouts = map[string]bool{
	"app/layout.tsx":     true,
	"app/layout.jsx":     true,
	"src/app/layout.tsx": true,
	"src/app/layout.jsx": true,
}

// providersFileFor names the providers companion for entryPath's own App
// Router directory, matched to its extension: a .jsx layout gets a plain
// providers.jsx (no TS type syntax it can't parse), a .tsx layout the usual
// providers.tsx.
func providersFileFor(appDir, entryPath string) (path string, typescript bool) {
	if strings.HasSuffix(entryPath, ".jsx") {
		return filepath.Join(appDir, "providers.jsx"), false
	}
	return filepath.Join(appDir, "providers.tsx"), true
}

// firstLineIsUseClientDirective reports whether lines[0] (trimmed) is exactly
// the 'use client' directive. It is the ONLY place splicing a bare import
// into an EXISTING, unrecognized file is safe: React requires the directive
// to be a file's first statement, so if it is there the file is provably a
// Client Component. Anything else (a Server Component, or a file we can't
// otherwise reason about) must be left alone — setupPalbeNext()/the gen
// import silently no-op without `document`, so a wrong injection wouldn't
// even fail loudly, it would just resurface as "Palbase is not configured"
// with no trace of why.
func firstLineIsUseClientDirective(lines []string) bool {
	if len(lines) == 0 {
		return false
	}
	t := strings.TrimSpace(lines[0])
	return t == `'use client'` || t == `'use client';` || t == `"use client"` || t == `"use client";`
}

// wireNextProviders creates <appDir>/providers.tsx (or .jsx) when the project
// is a Next.js App Router app, so the CLIENT bundle also imports and
// configures the generated client. It is idempotent (a providers file that
// already imports the gen stem is left untouched) and NEVER overwrites an
// existing providers file — that file is, more often than not, the project's
// OWN providers (React Query / theme / i18n and friends), and overwriting it
// is irrecoverable data loss. When one already exists but isn't wired up, a
// single import line is spliced in — ONLY when the file is provably a Client
// Component (see firstLineIsUseClientDirective); otherwise nothing is
// written and the user is told exactly what to add.
func wireNextProviders(entryFlag, outFile string, w io.Writer) error {
	// Determine the entry file that was (or would be) used by wireEntryImport.
	entryPath := entryFlag
	if entryPath == "" {
		for _, candidate := range autoEntryPaths {
			if _, err := os.Stat(candidate); err == nil {
				entryPath = candidate
				break
			}
		}
	}
	if !nextAppLayouts[entryPath] {
		return nil // not an App Router project — nothing to do
	}

	appDir := filepath.Dir(entryPath) // "app" or "src/app"
	providersPath, typescript := providersFileFor(appDir, entryPath)
	genStem := strings.TrimSuffix(filepath.Base(outFile), ".ts") // e.g. "palbe.gen"

	// Compute the relative import path from appDir to the gen file.
	rel, err := filepath.Rel(appDir, outFile)
	if err != nil {
		return fmt.Errorf("rel path from %s to %s: %w", appDir, outFile, err)
	}
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".ts")
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	importLine := fmt.Sprintf("import '%s'; // configures pb in the client bundle", rel)

	existing, readErr := os.ReadFile(providersPath)
	if readErr == nil {
		// The file already exists — NEVER overwrite it.
		lines := strings.Split(string(existing), "\n")
		for _, line := range lines {
			if lineImportsGen(line, genStem) {
				return nil // already wired, idempotent
			}
		}
		if !firstLineIsUseClientDirective(lines) {
			fmt.Fprintf(w, "\nNOTE: %s already exists and its first line is not 'use client' — Palbase left it untouched to avoid corrupting it.\n", providersPath)
			fmt.Fprintf(w, "      Add this import to a 'use client' component rendered inside your root layout:\n")
			fmt.Fprintf(w, "        %s\n\n", importLine)
			return nil
		}
		insertAt := entryImportInsertIndex(lines)
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:insertAt]...)
		newLines = append(newLines, importLine)
		newLines = append(newLines, lines[insertAt:]...)
		if err := os.WriteFile(providersPath, []byte(strings.Join(newLines, "\n")), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "✓ added the Palbase import to existing %s\n", providersPath)
		return nil
	}
	if !os.IsNotExist(readErr) {
		return fmt.Errorf("read %s: %w", providersPath, readErr)
	}

	// No existing providers file — write ours from scratch.
	childrenParam := "{ children }"
	if typescript {
		childrenParam = "{ children }: { children: React.ReactNode }"
	}
	content := fmt.Sprintf(
		"'use client';\n%s\nimport { setupPalbeNext } from '@palbase/web/next/client';\n\nsetupPalbeNext(); // switches session storage to cookies\n\nexport function Providers(%s) {\n  return children;\n}\n",
		importLine, childrenParam,
	)
	if err := os.WriteFile(providersPath, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ wrote %s — render <Providers> inside your root layout\n", providersPath)
	return nil
}

// ── Next.js proxy.ts wiring ──────────────────────────────────────────────────

// webConfigArtifact is the subset of Palbase/palbase-config.json (already
// committed by webLinkArtifacts, step 4, before this runs) that
// wireNextProxy needs. base_url and api_key are the SAME two published
// values palbe.gen.ts itself is configured with — api_key is always the
// publishable (anon) key, never service_role (see @palbase/web/next/proxy's
// own doc comment: a service_role key has no business in a request-path bundle).
type webConfigArtifact struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// proxyPathFor names the proxy.ts (or .js) file Next.js recognizes by
// convention: at the project root, or inside src/ when the project uses a
// src/ layout — NEVER inside app/ (confirmed against the Next 16.2.9 source:
// build/index.js gates PROXY_FILENAME on isAtConventionLevel, i.e.
// normalizedFileDir === '/' || === '/src').
// Extension matches entryPath's own, same reasoning as providersFileFor: a
// .jsx project must not get a file full of TypeScript type syntax it can't
// parse (import type NextRequest, the request: NextRequest annotation).
func proxyPathFor(entryPath string) (path string, typescript bool) {
	name, typescript := "proxy.ts", true
	if strings.HasSuffix(entryPath, ".jsx") {
		name, typescript = "proxy.js", false
	}
	if strings.HasPrefix(entryPath, "src/") {
		return filepath.Join("src", name), typescript
	}
	return name, typescript
}

// existingProxyCandidates are every path Next.js would recognize as the ONE
// request-interception handler, at both possible convention levels. The
// deprecated pre-16 `middleware` names are still listed BECAUSE Next still
// reads them: a project carrying one must not silently end up with two
// handlers.
var existingProxyCandidates = []string{
	"proxy.ts", "proxy.js",
	filepath.Join("src", "proxy.ts"), filepath.Join("src", "proxy.js"),
	"middleware.ts", "middleware.js",
	filepath.Join("src", "middleware.ts"), filepath.Join("src", "middleware.js"),
}

// wireNextProxy writes proxy.ts (or .js) for a Next.js App Router project so
// the session cookie refreshes BEFORE the RSC tree renders — see
// @palbase/web/next/proxy's package doc for why this is required, not
// optional, for any session-bearing RSC app.
//
// `proxy` is the Next 16 file convention; `middleware` is deprecated and warns
// on every dev/build, so the generated file never uses it.
//
// It NEVER overwrites an existing proxy/middleware file. Unlike providers.tsx,
// there is no safe auto-merge here: Next reads exactly ONE handler export per
// file, so splicing palbeProxy into unknown existing logic would mean guessing
// how to combine two response values — instead, an existing file is left
// byte-identical and the user is told exactly what to add.
func wireNextProxy(entryFlag string, w io.Writer) error {
	entryPath := entryFlag
	if entryPath == "" {
		for _, candidate := range autoEntryPaths {
			if _, err := os.Stat(candidate); err == nil {
				entryPath = candidate
				break
			}
		}
	}
	if !nextAppLayouts[entryPath] {
		return nil // not an App Router project — nothing to do
	}

	for _, existing := range existingProxyCandidates {
		if _, err := os.Stat(existing); err == nil {
			fmt.Fprintf(w, "\nNOTE: %s already exists — Palbase left it untouched.\n", existing)
			fmt.Fprintf(w, "      Without this, RSC session refresh cannot persist (Server Components\n")
			fmt.Fprintf(w, "      can't write cookies) and users get force-logged-out everywhere. Wire it\n")
			fmt.Fprintf(w, "      in by hand — see the @palbase/web/next/proxy package doc for the\n")
			fmt.Fprintf(w, "      exact call (palbeProxy(request, { url, apiKey })).\n")
			if strings.HasPrefix(filepath.Base(existing), "middleware.") {
				fmt.Fprintf(w, "      Next 16 also deprecated that filename: rename it to proxy%s and its\n", filepath.Ext(existing))
				fmt.Fprintf(w, "      export to `proxy` (`npx @next/codemod middleware-to-proxy .`).\n")
			}
			fmt.Fprintln(w)
			return nil
		}
	}

	cfgPath := filepath.Join(webArtifactsDir, "palbase-config.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", cfgPath, err)
	}
	var art webConfigArtifact
	if err := json.Unmarshal(raw, &art); err != nil {
		return fmt.Errorf("parse %s: %w", cfgPath, err)
	}

	proxyPath, typescript := proxyPathFor(entryPath)
	requestParam, importType := "request", ""
	if typescript {
		requestParam = "request: NextRequest"
		importType = "import type { NextRequest } from 'next/server';\n"
	}
	content := fmt.Sprintf(
		"import { palbeProxy } from '@palbase/web/next/proxy';\n%s\nexport function proxy(%s) {\n  return palbeProxy(request, {\n    url: %s,\n    apiKey: %s,\n  });\n}\n\n"+
			"// `config` MUST be declared here, in this file: Next reads it off THIS\n"+
			"// file's AST (export const + literal initializer). A re-exported or\n"+
			"// imported config is invisible and silently degrades to a catch-all.\n"+
			"export const config = { matcher: ['/((?!_next/static|_next/image|favicon.ico).*)'] };\n",
		importType, requestParam, encodeJSONString(art.BaseURL), encodeJSONString(art.APIKey),
	)
	// MkdirAll(".", …) is a no-op when the project has no src/ layout.
	if err := os.MkdirAll(filepath.Dir(proxyPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(proxyPath, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ wrote %s — required for RSC session refresh to persist\n", proxyPath)
	return nil
}
