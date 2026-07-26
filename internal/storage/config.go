package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// config.go — read/generate config/storage.ts.
//
// The CLI owns config/storage.ts entirely: writeConfig regenerates the whole
// file from the bucket set (deterministic, buckets sorted by name) and
// readConfig parses that same generated shape back. The generated file is valid
// TypeScript importing defineStorage + bucket from @palbase/backend, so it
// type-checks and the deploy's br-pod evals it the same as a hand-written one.
//
// readConfig is tolerant: it parses each `name: bucket({ ... })` entry with a
// field-level regex over the file text. A user edit that keeps that shape
// round-trips; a free-form rewrite that the parser can't read yields a clear
// error (the command refuses rather than silently clobbering the file).

// bucketEntryRE matches one `name: bucket({ ...opts... })` entry inside the
// buckets object. The name is a bare identifier or quoted string; the body is
// the (non-nested) object literal passed to bucket(). bucket() bodies never
// nest braces (only scalars + a string array), so a non-greedy [^{}]* body is
// sufficient and avoids a full TS parser.
var bucketEntryRE = regexp.MustCompile(`(?:"([^"]+)"|([A-Za-z_$][\w$]*))\s*:\s*bucket\(\s*\{([^{}]*)\}\s*\)`)

// lineCommentRE strips `// ...` line comments before parsing so a comment that
// happens to contain a `name: bucket({ ... })`-shaped token is not mistaken for
// a real entry (the generated header comment references that shape on purpose).
var lineCommentRE = regexp.MustCompile(`(?m)//[^\n]*`)

// readConfig parses config/storage.ts into a name->bucketDef map. A missing file
// is NOT an error — it means "no buckets yet" (the add command creates the
// file). A present-but-unparseable file IS an error so we never clobber a
// hand-authored config we don't understand.
func readConfig() (map[string]bucketDef, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bucketDef{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	return parseConfig(string(data))
}

// parseConfig extracts bucket entries from config/storage.ts source text. It
// requires the file to look like a defineStorage({ buckets: { ... } }) config
// (a defensive check so we don't parse an unrelated file); each matched entry's
// options are read field-by-field.
func parseConfig(src string) (map[string]bucketDef, error) {
	if !strings.Contains(src, "defineStorage") {
		return nil, fmt.Errorf("%s does not look like a defineStorage() config (no defineStorage call found) — refusing to overwrite; remove or fix it manually", configPath)
	}
	// Strip line comments first so commented-out or header text never parses as
	// a real entry.
	src = lineCommentRE.ReplaceAllString(src, "")
	buckets := map[string]bucketDef{}
	for _, m := range bucketEntryRE.FindAllStringSubmatch(src, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		def, err := parseBucketBody(name, m[3])
		if err != nil {
			return nil, err
		}
		buckets[name] = def
	}
	return buckets, nil
}

var (
	publicFieldRE = regexp.MustCompile(`\bpublic\s*:\s*(true|false)\b`)
	// The SDK's fileSizeLimit accepts BOTH a human string ("25MB") and a bare byte
	// count, and the docs teach the string form — so both must parse here. Matching
	// only digits silently dropped a `"25MB"` limit on the next rewrite (the file is
	// fully regenerated), leaving the bucket unlimited on the following deploy.
	sizeFieldRE   = regexp.MustCompile(`\bfileSizeLimit\s*:\s*("(?:[^"\\]|\\.)*"|\d+)`)
	mimeFieldRE   = regexp.MustCompile(`\ballowedMimeTypes\s*:\s*\[([^\]]*)\]`)
	mimeElementRE = regexp.MustCompile(`"([^"]*)"`)
)

// parseBucketBody reads the scalar fields out of one bucket({...}) body.
func parseBucketBody(name, body string) (bucketDef, error) {
	def := bucketDef{Name: name}
	if m := publicFieldRE.FindStringSubmatch(body); m != nil {
		def.Public = m[1] == "true"
	}
	if m := sizeFieldRE.FindStringSubmatch(body); m != nil {
		n, err := parseSizeLiteral(m[1])
		if err != nil {
			return bucketDef{}, fmt.Errorf("bucket %q: bad fileSizeLimit %q: %w", name, m[1], err)
		}
		def.FileSizeLimit = &n
		// Keep the raw TS literal so the generator re-emits it verbatim — a
		// hand-written `"25MB"` survives a CLI rewrite unchanged (same lossless
		// round-trip the flags parser gets from its verbatim default literal).
		def.SizeLiteral = m[1]
	}
	if m := mimeFieldRE.FindStringSubmatch(body); m != nil {
		for _, em := range mimeElementRE.FindAllStringSubmatch(m[1], -1) {
			def.AllowedMimeTypes = append(def.AllowedMimeTypes, em[1])
		}
	}
	return def, nil
}

// writeConfig regenerates config/storage.ts from the bucket set and writes it
// (creating the config/ dir if needed). Buckets are emitted sorted by name so
// the file is deterministic (no spurious diffs on reorder).
func writeConfig(buckets map[string]bucketDef) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(configPath), err)
	}
	if err := os.WriteFile(configPath, []byte(generateConfig(buckets)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	return nil
}

// generateConfig renders the full config/storage.ts source for a bucket set.
// Deterministic: buckets sorted by name, fields emitted in a fixed order, only
// non-default fields written (public:false / no limit / no mimes are omitted to
// match the SDK defaults and keep the file minimal). An empty set still emits a
// valid `defineStorage({ buckets: {} })` so a subsequent read parses cleanly.
func generateConfig(buckets map[string]bucketDef) string {
	names := make([]string, 0, len(buckets))
	for n := range buckets {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("// Generated + maintained by `palbase storage`. Edit via the CLI,\n")
	b.WriteString("// or by hand keeping each entry's `bucket({ ... })` shape. Config-as-code:\n")
	b.WriteString("// commit this file and `git push` — the deploy creates/updates the buckets.\n")
	b.WriteString("import { defineStorage, bucket } from \"@palbase/backend\";\n\n")
	b.WriteString("export default defineStorage({\n")
	b.WriteString("  buckets: {\n")
	for _, name := range names {
		b.WriteString("    ")
		b.WriteString(tsKey(name))
		b.WriteString(": bucket(")
		b.WriteString(renderOpts(buckets[name]))
		b.WriteString("),\n")
	}
	b.WriteString("  },\n")
	b.WriteString("});\n")
	return b.String()
}

// renderOpts renders the bucket({...}) options literal. Returns "{}" when every
// option is at its default (public false, no limit, any mime) so a bare bucket
// stays compact. Otherwise emits only the non-default fields.
func renderOpts(def bucketDef) string {
	var parts []string
	if def.Public {
		parts = append(parts, "public: true")
	}
	if def.FileSizeLimit != nil {
		lit := def.SizeLiteral
		if lit == "" {
			lit = strconv.FormatInt(*def.FileSizeLimit, 10)
		}
		parts = append(parts, "fileSizeLimit: "+lit)
	}
	if len(def.AllowedMimeTypes) > 0 {
		quoted := make([]string, len(def.AllowedMimeTypes))
		for i, m := range def.AllowedMimeTypes {
			quoted[i] = strconv.Quote(m)
		}
		parts = append(parts, "allowedMimeTypes: ["+strings.Join(quoted, ", ")+"]")
	}
	if len(parts) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// identRE matches a bare JS identifier safe to use as an unquoted object key.
var identRE = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)

// tsKey returns the bucket name as an object key: bare when it's a valid JS
// identifier, quoted otherwise (e.g. "user-uploads" needs quoting).
func tsKey(name string) string {
	if identRE.MatchString(name) {
		return name
	}
	return strconv.Quote(name)
}
