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
// readConfig is tolerant: it locates each `key: flag({ ... })` entry and splits
// the body into top-level fields with a small string-aware scanner. A user edit
// that keeps that shape round-trips; a free-form rewrite that the parser can't
// read yields a clear error (the command refuses rather than silently clobbering
// the file).
//
// The scanner exists because of `type: "json"`, whose default is an object
// literal: its braces, commas and quoted keys all appear inside a value, so a
// regex either stops at the first `}` or reads the object's own keys as the
// flag's fields.

// entryKeyRE matches the `key:` that must sit immediately before a `flag(` call
// — a bare identifier or a quoted string. It is anchored at the end so it is
// applied to the text PRECEDING a located `flag(`; the call's body is then read
// by brace-matching rather than by regex, because a json flag's default is an
// object literal and nested braces are exactly what a regex body cannot bound.
var entryKeyRE = regexp.MustCompile(`(?:"([^"]+)"|([A-Za-z_$][\w$]*))\s*:\s*$`)

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
// defensive check so we don't parse an unrelated file); each entry's options are
// then read field-by-field.
func parseConfig(src string) (map[string]flagDef, error) {
	if !strings.Contains(src, "defineFlags") {
		return nil, fmt.Errorf("%s does not look like a defineFlags() config (no defineFlags call found) — refusing to overwrite; remove or fix it manually", configPath)
	}
	// Strip line comments first so commented-out or header text never parses as a
	// real entry.
	src = stripComments(src)

	flags := map[string]flagDef{}
	for i := 0; ; {
		off := strings.Index(src[i:], "flag(")
		if off < 0 {
			break
		}
		call := i + off
		i = call + len("flag(")

		// The key must sit immediately before the call. Only a bounded window of
		// preceding text can hold one (a key is at most 128 chars), so the
		// lookbehind stays O(1) per entry instead of rescanning the whole file.
		km := entryKeyRE.FindStringSubmatch(src[max(0, call-256):call])
		if km == nil {
			continue
		}
		key := km[1]
		if key == "" {
			key = km[2]
		}

		// flag( <ws> { ... } <ws> ) — brace-match the body.
		open := i
		for open < len(src) && isSpace(src[open]) {
			open++
		}
		if open >= len(src) || src[open] != '{' {
			continue
		}
		end := spanGroup(src, open)
		if end < 0 {
			continue
		}
		def, err := parseFlagBody(key, src[open+1:end-1])
		if err != nil {
			return nil, err
		}
		flags[key] = def
		i = end
	}
	return flags, nil
}

var (
	typeValueRE     = regexp.MustCompile(`^"(boolean|number|string|json)"$`)
	numberValueRE   = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)
	stringValueRE   = regexp.MustCompile(`^"(?:[^"\\]|\\.)*"$`)
	variantsValueRE = regexp.MustCompile(`^\[([^\]]*)\]$`)
	variantElemRE   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// parseFlagBody reads the fields out of one flag({...}) body. `type` + `default`
// are required (the generator always emits them); variants + description are
// optional. The default's literal is kept VERBATIM so a round-trip through the
// generated file is lossless, and its shape is checked against the declared type
// — so a body the CLI would re-emit as something @palbase/backend rejects at
// deploy time is refused here instead.
func parseFlagBody(key, body string) (flagDef, error) {
	fields, err := splitFields(body)
	if err != nil {
		return flagDef{}, fmt.Errorf("flag %q: %v in %s", key, err, configPath)
	}
	def := flagDef{Key: key}

	tm := typeValueRE.FindStringSubmatch(fields["type"])
	if tm == nil {
		return flagDef{}, fmt.Errorf("flag %q: missing or invalid type in %s", key, configPath)
	}
	def.Type = tm[1]

	lit, ok := fields["default"]
	if !ok {
		return flagDef{}, fmt.Errorf("flag %q: missing default in %s", key, configPath)
	}
	if !defaultLiteralMatchesType(def.Type, lit) {
		return flagDef{}, fmt.Errorf("flag %q: default %s does not match type %q in %s", key, lit, def.Type, configPath)
	}
	def.DefaultLiteral = lit

	if raw, ok := fields["variants"]; ok {
		vm := variantsValueRE.FindStringSubmatch(raw)
		if vm == nil {
			return flagDef{}, fmt.Errorf("flag %q: variants must be an array of strings in %s", key, configPath)
		}
		for _, em := range variantElemRE.FindAllStringSubmatch(vm[1], -1) {
			unq, err := strconv.Unquote(`"` + em[1] + `"`)
			if err != nil {
				unq = em[1]
			}
			def.Variants = append(def.Variants, unq)
		}
	}

	if raw, ok := fields["description"]; ok {
		if !stringValueRE.MatchString(raw) {
			return flagDef{}, fmt.Errorf("flag %q: description must be a string in %s", key, configPath)
		}
		unq, err := strconv.Unquote(raw)
		if err != nil {
			unq = strings.Trim(raw, `"`)
		}
		def.Description = unq
	}

	return def, nil
}

// defaultLiteralMatchesType reports whether a raw default literal has the shape
// the declared type requires. A "json" default must be a single balanced object
// literal — PalFlags' `object` value type accepts a JSON object only (not an
// array, not a scalar), so an array literal is rejected here too.
func defaultLiteralMatchesType(flagType, lit string) bool {
	switch flagType {
	case "boolean":
		return lit == "true" || lit == "false"
	case "number":
		return numberValueRE.MatchString(lit)
	case "string":
		return stringValueRE.MatchString(lit)
	case "json":
		return len(lit) > 0 && lit[0] == '{' && spanGroup(lit, 0) == len(lit)
	default:
		return false
	}
}

// splitFields splits a flag({...}) body into its TOP-LEVEL `name: value` fields,
// each value kept as the raw source literal.
//
// This is a scanner and not a set of regexes over the body text for one reason:
// a json flag's default is an object literal that can contain commas, braces and
// the very field names we are looking for. `default: {"description":"x"}` read by
// a `description\s*:\s*"..."` regex yields "x" as the FLAG's description; read
// here, the object is one opaque value and cannot be confused for a field.
func splitFields(body string) (map[string]string, error) {
	fields := map[string]string{}
	for _, part := range splitTopLevel(body, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		seps := topLevelIndexes(part, ':')
		if len(seps) == 0 {
			return nil, fmt.Errorf("field %q is not `name: value`", part)
		}
		name := strings.Trim(strings.TrimSpace(part[:seps[0]]), `"'`)
		fields[name] = strings.TrimSpace(part[seps[0]+1:])
	}
	return fields, nil
}

// splitTopLevel splits src on sep, ignoring separators inside string literals or
// {…} / […] groups.
func splitTopLevel(src string, sep byte) []string {
	idx := topLevelIndexes(src, sep)
	out := make([]string, 0, len(idx)+1)
	prev := 0
	for _, i := range idx {
		out = append(out, src[prev:i])
		prev = i + 1
	}
	return append(out, src[prev:])
}

// topLevelIndexes returns the offsets of every sep byte in src that is not inside
// a string literal or a {…} / […] group. An unterminated group ends the scan —
// the caller then fails on the field shape rather than reading past it.
func topLevelIndexes(src string, sep byte) []int {
	var out []int
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			i = scanStringEnd(src, i) - 1
		case '{', '[':
			end := spanGroup(src, i)
			if end < 0 {
				return out
			}
			i = end - 1
		case sep:
			out = append(out, i)
		}
	}
	return out
}

// spanGroup returns the offset just past the {…} or […] group starting at src[i],
// or -1 if it is unterminated. String literals are skipped as units so a brace
// inside a string never counts toward the depth.
func spanGroup(src string, i int) int {
	if i >= len(src) {
		return -1
	}
	opener := src[i]
	var closer byte
	switch opener {
	case '{':
		closer = '}'
	case '[':
		closer = ']'
	default:
		return -1
	}
	depth := 0
	for j := i; j < len(src); j++ {
		switch c := src[j]; c {
		case '"', '\'', '`':
			j = scanStringEnd(src, j) - 1
		case opener:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return j + 1
			}
		}
	}
	return -1
}

// scanStringEnd returns the offset just past the string literal starting at
// src[i] (a quote byte), honouring backslash escapes. An unterminated literal
// runs to the end of src.
func scanStringEnd(src string, i int) int {
	quote := src[i]
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++ // skip the escaped byte
		case quote:
			return j + 1
		}
	}
	return len(src)
}

// stripComments removes `// ...` line comments so a commented-out entry (or the
// generated header, which quotes the `flag({ ... })` shape on purpose) never
// parses as a real flag.
//
// It skips string literals, which the regex it replaced could not: a `//` inside
// a string — `description: "see http://x"`, or a URL in a json default — used to
// be truncated mid-literal, and for an object default that also unbalanced the
// braces and dropped the whole entry.
func stripComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			end := scanStringEnd(src, i)
			b.WriteString(src[i:end])
			i = end - 1
		case '/':
			if i+1 < len(src) && src[i+1] == '/' {
				nl := strings.IndexByte(src[i:], '\n')
				if nl < 0 {
					return b.String()
				}
				i += nl - 1 // leave the newline for the next iteration to write
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
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
