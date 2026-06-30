package backend

// web_link.go — `palbase web link` / `palbase web unlink`
//
// `palbase web link` wires a web project (package.json present) to a Palbase
// project:
//   1. Verifies package.json exists in the cwd.
//   2. resolveOrLinkRef — writes/updates .palbase/config.json (a --ref that
//      differs from the existing link re-links, keeping the active branch).
//   3. Ensures .gitignore covers .palbase/ (the link file is per-machine).
//   4. Runs TS codegen (via webLinkCodegen seam) → --out (default palbe.gen.ts).
//   5. Patches package.json: adds scripts.predev / scripts.prebuild running
//      `palbase types --soft || exit 0` via a BYTE-SPLICE editor — only the
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
	"os"
	"path/filepath"
	"strings"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// webLinkCodegenEnv is the env value passed to pullTSTypes by the production
// webLinkCodegen closure. Exposed as a constant so tests can assert the env
// without stubbing the entire seam or requiring a real network.
//
// "remote" is the correct default for a linked platform project: "auto" tries
// localhost:4003 first and will emit whatever app happens to be at that port —
// or an empty client — if no local `palbase serve` is running. A linked
// project's source of truth is the deployed backend, not the local machine.
// Users who want local-first iteration can run `palbase types --env local` or
// keep `palbase serve` running separately; the prebuild hook also pins
// --env remote so CI/builds never regress to a localhost URL.
const webLinkCodegenEnv = "remote"

// webLinkCodegen is the codegen seam. Tests replace it with a stub; production
// calls pullTSTypes via the real ts codegen pipeline (env webLinkCodegenEnv).
var webLinkCodegen = func(ctx context.Context, r Resolvers, ref, outFile string, w io.Writer) error {
	// branch: load from .palbase/config.json DefaultEnv, or default to "main".
	branch := "main"
	if cfg, err := auth.LoadProjectConfig(); err == nil && cfg.DefaultEnv != "" {
		branch = cfg.DefaultEnv
	}
	return pullTSTypes(ctx, r.Studio(), r.Endpoints(), ref, branch, webLinkCodegenEnv, outFile, w)
}

// webCmd holds the resolvers for the web command group.
type webCmd struct {
	r Resolvers
}

// webTypesCmd is the predev/prebuild hook value. --env remote pins codegen to
// the deployed backend so repeated builds never regress to a localhost URL
// (the "auto" default tries localhost:4003 first and can pick up whatever
// happens to be running there). `|| exit 0` covers a machine without the CLI
// installed (command-not-found exits 127, which --soft alone cannot swallow).
const webTypesCmd = "palbase types --env remote --soft || exit 0"

// newWebCmd builds the `palbase web` command group.
func newWebCmd(r Resolvers) *cobra.Command {
	wc := &webCmd{r: r}
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Wire a web project to a Palbase project",
	}
	cmd.AddCommand(wc.newWebLinkCmd(), wc.newWebUnlinkCmd())
	return cmd
}

// newWebLinkCmd builds `palbase web link`.
func (wc *webCmd) newWebLinkCmd() *cobra.Command {
	var refFlag string
	var entryFlag string
	var outFlag string

	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link this web project to a Palbase project and generate typed SDK code",
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

			// 2. Resolve or link the project ref.
			// Guard the Studio() call: in tests noopResolvers sets Studio=nil
			// and the func would panic. When --ref is provided, resolveOrLinkRef
			// returns before touching the studio client anyway.
			var sc *studio.Client
			if wc.r.Studio != nil {
				sc = wc.r.Studio()
			}
			ref, err := resolveOrLinkRef(ctx, refFlag, sc, out)
			if err != nil {
				return err
			}
			// resolveOrLinkRef only writes .palbase/config.json when it goes
			// through the picker path (no override). Materialise the link when
			// missing, and RE-LINK when an explicit --ref differs from the
			// existing link (keeping DefaultEnv — the active branch is a local
			// choice the new ref shouldn't reset).
			cfg, cfgErr := auth.LoadProjectConfig()
			switch {
			case cfgErr != nil:
				if saveErr := auth.SaveProjectConfig(&auth.ProjectConfig{Ref: ref, DefaultEnv: "main"}); saveErr != nil {
					return fmt.Errorf("save .palbase/config.json: %w", saveErr)
				}
				fmt.Fprintf(out, "✓ Linked to %s\n", ref)
			case refFlag != "" && cfg.Ref != ref:
				prev := cfg.Ref
				cfg.Ref = ref
				if saveErr := auth.SaveProjectConfig(cfg); saveErr != nil {
					return fmt.Errorf("save .palbase/config.json: %w", saveErr)
				}
				fmt.Fprintf(out, "✓ Re-linked to %s (was %s)\n", ref, prev)
			}

			// 3. Keep the per-machine link file out of git (same as mobile link).
			if err := ensureGitignored(".gitignore", ".palbase/"); err != nil {
				return fmt.Errorf("update .gitignore: %w", err)
			}

			// 4. First codegen.
			if err := webLinkCodegen(ctx, wc.r, ref, outFile, out); err != nil {
				return fmt.Errorf("codegen: %w", err)
			}

			// 5. Patch package.json scripts.
			if err := patchPackageJSONScripts("package.json", out); err != nil {
				return fmt.Errorf("patch package.json: %w", err)
			}

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

			// 8. Gitignore guard for the GEN file (warn only, never edit the rule).
			checkGitignoreGuard(outFile, out)

			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "", "Project ref (skips interactive picker; required in non-interactive shells)")
	cmd.Flags().StringVar(&entryFlag, "entry", "", "Entry file to wire the import into (auto-detected when absent)")
	cmd.Flags().StringVar(&outFlag, "out", "", "Gen file name (default: palbe.gen.ts)")
	return cmd
}

// newWebUnlinkCmd builds `palbase web unlink`.
func (wc *webCmd) newWebUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Detach this web project from its Palbase project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			cfgPath := filepath.Join(".palbase", "config.json")
			if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove project config: %w", err)
			}

			// Remove .palbase/ if empty.
			if entries, err := os.ReadDir(".palbase"); err == nil && len(entries) == 0 {
				_ = os.Remove(".palbase")
			}

			// unlink doesn't know the --out the link used, so speak generically.
			fmt.Fprintln(out, "✓ unlinked — removed .palbase/config.json")
			fmt.Fprintln(out, "  generated client file and package.json scripts left in place — re-link with `palbase web link`")
			return nil
		},
	}
}

// The .palbase/ gitignore append reuses ensureGitignored (backend.go) — the
// shared idempotent helper that creates/appends the entry when missing.

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
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return err
	}

	loc, err := locatePackageJSONScripts(data)
	if err != nil {
		return fmt.Errorf("parse package.json: %w", err)
	}

	if !loc.found {
		return os.WriteFile(pkgPath, spliceNewScriptsObject(data, loc.topEnd), 0o644)
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
			if existing != webTypesCmd {
				fmt.Fprintf(w, "warning: scripts.%s is already set to %q — skipping (suggested value: %q)\n", hook, existing, webTypesCmd)
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

	return os.WriteFile(pkgPath, spliceScriptEntries(data, loc, missing), 0o644)
}

// spliceScriptEntries inserts the missing hook entries at the END of the
// existing scripts object (right after its last entry), leaving every other
// byte alone.
func spliceScriptEntries(data []byte, loc scriptsLocation, hooks []string) []byte {
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
			b.WriteString("\n" + entryIndent + encodeJSONString(h) + ": " + encodeJSONString(webTypesCmd))
		}
		b.WriteString("\n" + keyIndent)
		return splice(data, loc.valStart+1, b.String())
	}
	for _, h := range hooks {
		b.WriteString(",\n" + entryIndent + encodeJSONString(h) + ": " + encodeJSONString(webTypesCmd))
	}
	return splice(data, last+1, b.String())
}

// spliceNewScriptsObject inserts a whole `"scripts": {...}` block before the
// top-level closing brace (stable choice: scripts lands last, like the
// previous appended-at-end behaviour).
func spliceNewScriptsObject(data []byte, topEnd int) []byte {
	unit := topUnitIndent(data)
	entryIndent := unit + unit

	var inner strings.Builder
	for i, h := range []string{"predev", "prebuild"} {
		if i > 0 {
			inner.WriteString(",")
		}
		inner.WriteString("\n" + entryIndent + encodeJSONString(h) + ": " + encodeJSONString(webTypesCmd))
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
// not given.
var autoEntryPaths = []string{
	"app/layout.tsx",
	"src/app/layout.tsx",
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

// nextAppDirs lists the App Router layout paths that indicate a Next.js project.
// When the detected entry file is one of these, we also create providers.tsx in
// the same directory so the CLIENT bundle imports and configures the gen file.
//
// Background: app/layout.tsx is a Server Component. Its module graph configures
// pb for the server. The browser's client-component bundle has a SEPARATE module
// graph — it never executes the layout's top-level imports. Without a "use client"
// component that also imports palbe.gen, pb.auth/pb.backend/etc. throw
// "Palbe is not configured" in every client component.
var nextAppLayouts = map[string]bool{
	"app/layout.tsx":     true,
	"src/app/layout.tsx": true,
}

// wireNextProviders creates app/providers.tsx (or src/app/providers.tsx) when
// the project is a Next.js App Router app. It is idempotent: a providers file
// that already imports the gen stem is left untouched.
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
	providersPath := filepath.Join(appDir, "providers.tsx")
	genStem := strings.TrimSuffix(filepath.Base(outFile), ".ts") // e.g. "palbe.gen"

	// Idempotency: if providers.tsx already imports the gen file, skip.
	if existing, err := os.ReadFile(providersPath); err == nil {
		for _, line := range strings.Split(string(existing), "\n") {
			if lineImportsGen(line, genStem) {
				return nil // already configured
			}
		}
	}

	// Compute the relative import path from appDir to the gen file.
	rel, err := filepath.Rel(appDir, outFile)
	if err != nil {
		return fmt.Errorf("rel path from %s to %s: %w", appDir, outFile, err)
	}
	rel = strings.TrimSuffix(filepath.ToSlash(rel), ".ts")
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}

	content := fmt.Sprintf("'use client';\nimport '%s'; // configures pb in the client bundle\nimport { setupPalbeNext } from '@palbase/web/next/client';\n\nsetupPalbeNext(); // switches session storage to cookies\n\nexport function Providers({ children }: { children: React.ReactNode }) {\n  return children;\n}\n", rel)
	if err := os.WriteFile(providersPath, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "✓ wrote %s — render <Providers> inside your root layout\n", providersPath)
	return nil
}
