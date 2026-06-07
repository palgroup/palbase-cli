package flags

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// config.go — read/generate config/flags.ts.
//
// The CLI owns config/flags.ts entirely: writeConfig regenerates the whole file
// from the flag set (deterministic, flags sorted by key) and readConfig parses
// that same generated shape back. The generated file is valid TypeScript
// importing defineFlags + flag from @palbase/backend, so it type-checks and the
// deploy's br-pod evals it the same as a hand-written one.
//
// readConfig is tolerant: it parses each `key: flag({ ... })` entry with a
// field-level regex over the file text. A user edit that keeps that shape
// round-trips; a free-form rewrite that the parser can't read yields a clear
// error (the command refuses rather than silently clobbering the file).

// flagEntryRE matches one `key: flag({ ...opts... })` entry inside the flags
// object. The key is a bare identifier or quoted string; the body is the
// (non-nested) object literal passed to flag(). flag() bodies never nest braces
// (only scalars + a string array), so a non-greedy [^{}]* body is sufficient and
// avoids a full TS parser.
var flagEntryRE = regexp.MustCompile(`(?:"([^"]+)"|([A-Za-z_$][\w$]*))\s*:\s*flag\(\s*\{([^{}]*)\}\s*\)`)

// lineCommentRE strips `// ...` line comments before parsing so a comment that
// happens to contain a `key: flag({ ... })`-shaped token is not mistaken for a
// real entry (the generated header comment references that shape on purpose).
var lineCommentRE = regexp.MustCompile(`(?m)//[^\n]*`)

// readConfig parses config/flags.ts into a key->flagDef map. A missing file is
// NOT an error — it means "no flags yet" (the add command creates the file). A
// present-but-unparseable file IS an error so we never clobber a hand-authored
// config we don't understand.
func readConfig() (map[string]flagDef, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]flagDef{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	return parseConfig(string(data))
}

// parseConfig extracts flag entries from config/flags.ts source text. It
// requires the file to look like a defineFlags({ flags: { ... } }) config (a
// defensive check so we don't parse an unrelated file); each matched entry's
// options are read field-by-field.
func parseConfig(src string) (map[string]flagDef, error) {
	if !strings.Contains(src, "defineFlags") {
		return nil, fmt.Errorf("%s does not look like a defineFlags() config (no defineFlags call found) — refusing to overwrite; remove or fix it manually", configPath)
	}
	// Strip line comments first so commented-out or header text never parses as a
	// real entry.
	src = lineCommentRE.ReplaceAllString(src, "")
	flags := map[string]flagDef{}
	for _, m := range flagEntryRE.FindAllStringSubmatch(src, -1) {
		key := m[1]
		if key == "" {
			key = m[2]
		}
		def, err := parseFlagBody(key, m[3])
		if err != nil {
			return nil, err
		}
		flags[key] = def
	}
	return flags, nil
}

var (
	typeFieldRE     = regexp.MustCompile(`\btype\s*:\s*"(boolean|number|string)"`)
	defaultFieldRE  = regexp.MustCompile(`\bdefault\s*:\s*(true|false|-?\d+(?:\.\d+)?|"(?:[^"\\]|\\.)*")`)
	variantsFieldRE = regexp.MustCompile(`\bvariants\s*:\s*\[([^\]]*)\]`)
	variantElemRE   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	descFieldRE     = regexp.MustCompile(`\bdescription\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// parseFlagBody reads the scalar fields out of one flag({...}) body. The `type`
// + `default` are required (the generator always emits them); variants +
// description are optional.
func parseFlagBody(key, body string) (flagDef, error) {
	def := flagDef{Key: key}

	m := typeFieldRE.FindStringSubmatch(body)
	if m == nil {
		return flagDef{}, fmt.Errorf("flag %q: missing or invalid type in config/flags.ts", key)
	}
	def.Type = m[1]

	dm := defaultFieldRE.FindStringSubmatch(body)
	if dm == nil {
		return flagDef{}, fmt.Errorf("flag %q: missing default in config/flags.ts", key)
	}
	// dm[1] is the raw TS literal (true / 42 / "light"). Store it verbatim — the
	// generator re-emits it verbatim, so a round-trip is lossless.
	def.DefaultLiteral = dm[1]

	if vm := variantsFieldRE.FindStringSubmatch(body); vm != nil {
		for _, em := range variantElemRE.FindAllStringSubmatch(vm[1], -1) {
			unq, err := strconv.Unquote(`"` + em[1] + `"`)
			if err != nil {
				unq = em[1]
			}
			def.Variants = append(def.Variants, unq)
		}
	}

	if dm := descFieldRE.FindStringSubmatch(body); dm != nil {
		unq, err := strconv.Unquote(`"` + dm[1] + `"`)
		if err != nil {
			unq = dm[1]
		}
		def.Description = unq
	}

	return def, nil
}

// writeConfig regenerates config/flags.ts from the flag set and writes it
// (creating the config/ dir if needed). Flags are emitted sorted by key so the
// file is deterministic (no spurious diffs on reorder).
func writeConfig(flags map[string]flagDef) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(configPath), err)
	}
	if err := os.WriteFile(configPath, []byte(generateConfig(flags)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	return nil
}

// generateConfig renders the full config/flags.ts source for a flag set.
// Deterministic: flags sorted by key, fields emitted in a fixed order (type,
// default, variants, description), only present optional fields written. An
// empty set still emits a valid `defineFlags({ flags: {} })` so a subsequent
// read parses cleanly.
func generateConfig(flags map[string]flagDef) string {
	keys := make([]string, 0, len(flags))
	for k := range flags {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("// Generated + maintained by `palbase flags`. Edit via the CLI, or by hand\n")
	b.WriteString("// keeping each entry's `flag({ ... })` shape. Config-as-code: commit this\n")
	b.WriteString("// file and `git push` — the deploy upserts the flag definitions.\n")
	b.WriteString("import { defineFlags, flag } from \"@palbase/backend\";\n\n")
	b.WriteString("export default defineFlags({\n")
	b.WriteString("  flags: {\n")
	for _, key := range keys {
		b.WriteString("    ")
		b.WriteString(tsKey(key))
		b.WriteString(": flag(")
		b.WriteString(renderOpts(flags[key]))
		b.WriteString("),\n")
	}
	b.WriteString("  },\n")
	b.WriteString("});\n")
	return b.String()
}

// renderOpts renders the flag({...}) options literal. type + default are always
// present; variants + description only when set.
func renderOpts(def flagDef) string {
	parts := []string{
		fmt.Sprintf("type: %q", def.Type),
		"default: " + def.DefaultLiteral,
	}
	if len(def.Variants) > 0 {
		quoted := make([]string, len(def.Variants))
		for i, v := range def.Variants {
			quoted[i] = strconv.Quote(v)
		}
		parts = append(parts, "variants: ["+strings.Join(quoted, ", ")+"]")
	}
	if def.Description != "" {
		parts = append(parts, "description: "+strconv.Quote(def.Description))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// identRE matches a bare JS identifier safe to use as an unquoted object key.
var identRE = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)

// tsKey returns the flag key as an object key: bare when it's a valid JS
// identifier, quoted otherwise.
func tsKey(key string) string {
	if identRE.MatchString(key) {
		return key
	}
	return strconv.Quote(key)
}
