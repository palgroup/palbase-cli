package backend

// web_wiring.go — WHAT `palbase link` DOES FOR A WEB CHECKOUT.
//
// These steps used to live behind `palbase web link`. That command was retired
// (FR-009) and the wiring went unreachable with it: 1.208 lines that nothing
// could call, kept looking alive by their own tests. The artifacts still got
// written, so a web project linked "successfully" and then had no generated
// client, no regeneration on the next build, and — in a Next App Router app —
// no configured client in the browser bundle at all.
//
// One link, every platform. An Apple checkout gets its xcconfigs and its Swift
// client; an Android checkout gets its slot; a web checkout gets this. The
// platform is DETECTED (or named with --platform), and what each one needs is
// the platform's business, not a separate command's.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
		fmt.Fprintf(w, "  %s not found in PATH — install @palbase/web yourself, then re-run `palbase link`\n", argv[0])
		return
	}
	fmt.Fprintf(w, "  installing @palbase/web (%s) ...\n", strings.Join(argv, " "))
	c := exec.CommandContext(ctx, bin, append(argv[1:], webPkg+"@latest")...)
	c.Stdout = w
	c.Stderr = w
	if err := c.Run(); err != nil {
		fmt.Fprintf(w, "  warning: could not install %s (%v) — install it yourself, then re-run `palbase link`\n", webPkg, err)
	}
}

// webPkg is the client SDK `palbase link` generates against.
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
		if err := os.WriteFile(pkgPath, spliceNewScriptsObject(data, loc.topEnd, typesCmd), 0o644); err != nil {
			return err
		}
		announceModified(w, pkgPath)
		return nil
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

	if err := os.WriteFile(pkgPath, spliceScriptEntries(data, loc, missing, typesCmd), 0o644); err != nil {
		return err
	}
	announceModified(w, pkgPath)
	return nil
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
	if err := os.WriteFile(entryPath, []byte(strings.Join(newLines, "\n")), 0o644); err != nil {
		return err
	}
	announceModified(w, entryPath)
	return nil
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
		announceModified(w, providersPath)
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

// announceModified names a file `web link` EDITED IN PLACE.
//
// The command already said `✓ wrote …` for every file it created, and said
// nothing at all for the three it opens and changes: package.json (predev/
// prebuild spliced into scripts), the entry file (app/layout.tsx and friends,
// one import line), and an existing providers component (one more import).
// So a person ran one command and `git status` came back with edits to files
// they had written themselves, with nothing in the output saying which of them
// Palbase had touched or why. `✓` and `~` are the two events kept apart on
// purpose — created vs changed — because looking for a NEW file when a file was
// edited is the wrong search.
//
// Generated artifacts (Palbase/openapi.json, Palbase/palbase-config.json,
// palbe.gen.ts) are deliberately NOT announced here: they carry their own
// `✓ wrote` line and nobody hand-edits them, so calling them "modified" would
// bury the three lines that are actually about the reader's own code.
func announceModified(w io.Writer, path string) {
	fmt.Fprintf(w, "~ modified %s\n", path)
}

// webTypesCmd is the predev/prebuild hook value: the SDK's own generator
// (`palbe-gen`, shipped in @palbase/web's bin) regenerates palbe.gen.ts from
// the COMMITTED Palbase/ artifacts — offline, no CLI, no login. `--soft`
// swallows generator errors; `|| exit 0` covers a machine where @palbase/web
// isn't installed yet (command-not-found exits 127, which --soft alone
// cannot swallow).
const webTypesCmd = "palbe-gen --soft || exit 0"

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

// wireWebProject does for a web checkout what the xcconfig + Swift generator
// steps do for an Apple one: it turns written artifacts into a project that
// actually compiles against them.
//
// Artifacts alone are inert. `Palbase/openapi.json` is a contract nobody reads
// until `palbe-gen` turns it into `palbe.gen.ts`; that file is imported by
// nothing until the entry file imports it; and in a Next App Router app the
// server layout's import does not reach the BROWSER bundle at all, so `pb`
// stays unconfigured and every call throws. Each step below closes one of those
// gaps, and skipping any of them produces a link that reports success and a
// project that does not work.
//
// Nothing here is silent: every file created or modified is announced, an
// existing script with a different value is warned about rather than
// overwritten, and the `.gitignore` rule that would hide the generated file is
// reported and never edited.
func wireWebProject(ctx context.Context, entryFlag, outFlag string, w io.Writer) error {
	outFile := outFlag
	if outFile == "" {
		outFile = "palbe.gen.ts"
	}
	// A PRECONDITION, not a decision. `runLink` refuses `--platform web` in a
	// directory with no package.json BEFORE it writes anything (see
	// refuseUnsupportedPlatforms), so this branch is the guard for a caller that
	// skipped it rather than a policy about what a link means.
	if _, err := os.Stat("package.json"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("the web wiring needs a package.json in the current directory")
		}
		return err
	}

	// The generator ships in @palbase/web. Without it there is no client, and
	// every step after this one would skip — which would mean linking twice.
	if _, err := os.Stat(palbeGenBin); err != nil {
		ensurePalbeWeb(ctx, w)
	}

	generated, err := runPalbeGen(ctx, outFile, w)
	if err != nil {
		return err
	}

	// ALWAYS, even when generation could not run: predev/prebuild pick the
	// generation up on the next build, which is what makes the contract follow
	// a deploy without anybody re-running a command.
	if err := patchPackageJSONScriptsWithCommand("package.json", webTypesCmdFor(outFile), w); err != nil {
		return fmt.Errorf("patch package.json: %w", err)
	}

	if !generated {
		// An import of a file that does not exist is a project that does not
		// build. Defer it rather than report a link that broke the app.
		fmt.Fprintf(w, "\nnote: skipping the entry-file import until %s exists — "+
			"after installing @palbase/web, re-run `palbase link` to wire it in\n", outFile)
		checkGitignoreGuard(outFile, w)
		return nil
	}

	if err := wireEntryImport(entryFlag, outFile, w); err != nil {
		return fmt.Errorf("wire entry import: %w", err)
	}
	// The server layout and the browser bundle are separate module graphs: the
	// layout's import configures Server Components only, and without a "use
	// client" provider every pb call in the browser throws "Palbe is not
	// configured".
	if err := wireNextProviders(entryFlag, outFile, w); err != nil {
		return fmt.Errorf("wire providers.tsx: %w", err)
	}
	// And the session cookie has to refresh BEFORE the RSC tree renders: Server
	// Components cannot write cookies, so two refreshes from the same stale
	// token more than palauth's 30s rotation grace apart make the reuse
	// detector revoke the whole family — a silent forced logout.
	if err := wireNextProxy(entryFlag, w); err != nil {
		return fmt.Errorf("wire proxy.ts: %w", err)
	}

	checkGitignoreGuard(outFile, w)
	return nil
}

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
