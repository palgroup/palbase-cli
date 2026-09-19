package backend

// strings_dir.go — palbase/strings/, the string table's home (D-1).
//
// One file per language beside a `_meta.json` that names the source language.
// The in-memory table is still stringsTable (strings_table.go): the merge does
// not care where a cell came from. What lives here is the disk — which entries
// ARE the table, how a directory is read and refused (v2 locale.ParseDir, rule
// for rule: D-19), how it is written (D-3, D-5), how the old single file moves
// in, and which format the project's own stack can read (D-21).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// metaFileName is the file whose presence MAKES the directory a table (D-2).
// Every write puts it last (D-5).
const metaFileName = "_meta.json"

// tableDirVersion is _meta.json's version; the SDK declares the same number
// when its stack reads the directory (D-21).
const tableDirVersion = 2

type tableLayout int

const (
	layoutNone tableLayout = iota
	layoutLegacy
	layoutDir
)

type tableMeta struct {
	Version int    `json:"version"`
	Source  string `json:"source"`
}

// errNoTableMeta: language files and no _meta.json, and no old file either.
var errNoTableMeta = fmt.Errorf("%s/%s is missing — it names the source language, and without it the language files "+
	"are not a table; start it with: palbase build --source <language>", StringsDir(), metaFileName)

// errBothTables is C-8's row 12: the table has one home. The build's read and
// the push's refusal (stringsTableRefusal) say the same sentence.
func errBothTables() error {
	return fmt.Errorf("%s and %s/ both exist — the table has one home: keep %s/ and delete %s "+
		"(a build moves the old file into the directory by itself while %s/%s does not exist)",
		StringsPath(), StringsDir(), StringsDir(), StringsPath(), StringsDir(), metaFileName)
}

// replaceTableFileFn is the seam D-5's order is measured through.
var replaceTableFileFn = replaceFileAtomically

// tableEntryName is v2 locale.TableEntry for a name directly under
// palbase/strings/: ends in `.json`, not hidden. Everything else there —
// Finder's .DS_Store, a README, an atomic write's `.fr.json-…` — is not the
// table: not read, not carried, not a reason to fail.
func tableEntryName(name string) bool {
	return name != "" && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".json")
}

// tableDirEntryNames lists the table entries of <dir>/palbase/strings/,
// sorted. No directory is no entries. A palbase/strings that is not a real
// directory, or an entry named like the table that is not a regular file, is
// refused: a translation that is not read is lost without a word (D-2).
func tableDirEntryNames(dir string) ([]string, error) {
	root := filepath.Join(dir, filepath.FromSlash(StringsDir()))
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", StringsDir(), err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory — the string table lives in a real directory", StringsDir())
	}
	list, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", StringsDir(), err)
	}
	var names []string
	for _, e := range list {
		if !tableEntryName(e.Name()) {
			continue
		}
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("%s/%s is not a regular file — a table file that is not read is a translation lost without a word", StringsDir(), e.Name())
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// readTableDirFiles reads every table entry; total size is NFR-001's.
func readTableDirFiles(dir string) (map[string][]byte, error) {
	names, err := tableDirEntryNames(dir)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(names))
	total := 0
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(StringsDir()), name))
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", StringsDir(), name, err)
		}
		total += len(raw)
		files[name] = raw
	}
	if total > maxStringsTableBytes {
		return nil, fmt.Errorf("%s/ is %d bytes, over the %d byte limit", StringsDir(), total, maxStringsTableBytes)
	}
	return files, nil
}

const jsonSpace = " \t\r\n"

// decodeTableObject is v2 locale.decodeObject: one JSON object, no unknown
// field, nothing but whitespace after it.
func decodeTableObject(raw []byte, into any) error {
	trimmed := bytes.TrimLeft(raw, jsonSpace)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("not a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if len(bytes.Trim(raw[dec.InputOffset():], jsonSpace)) != 0 {
		return errors.New("trailing content after the object")
	}
	return nil
}

// parseTableDir is v2 locale.ParseDir, rule for rule and message for message
// (D-19), into the in-memory table.
func parseTableDir(files map[string][]byte) (*stringsTable, error) {
	metaRaw, ok := files[metaFileName]
	if !ok {
		return nil, errNoTableMeta
	}
	var m tableMeta
	if err := decodeTableObject(metaRaw, &m); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", StringsDir(), metaFileName, err)
	}
	if m.Version != tableDirVersion {
		return nil, fmt.Errorf("%s/%s: version %d is not supported (only %d)", StringsDir(), metaFileName, m.Version, tableDirVersion)
	}
	if m.Source == "" {
		return nil, fmt.Errorf("%s/%s: source is required", StringsDir(), metaFileName)
	}
	if c, ok := canonicalLocale(m.Source); !ok {
		return nil, fmt.Errorf("%s/%s: source %q is not a language tag", StringsDir(), metaFileName, m.Source)
	} else if c != m.Source {
		return nil, fmt.Errorf("%s/%s: source %q is not canonical (want %q)", StringsDir(), metaFileName, m.Source, c)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		if name != metaFileName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	t := &stringsTable{Version: 1, Source: m.Source, Strings: map[string]map[string]stringCell{}}
	locales := []string{m.Source}
	for _, name := range names {
		stem, _ := strings.CutSuffix(name, ".json")
		if c, ok := canonicalLocale(stem); !ok {
			return nil, fmt.Errorf("%s/%s: %q is not a language tag", StringsDir(), name, stem)
		} else if c != stem {
			return nil, fmt.Errorf("%s/%s: %q is not canonical (name the file %s.json)", StringsDir(), name, stem, c)
		}
		if stem == m.Source {
			return nil, fmt.Errorf("%s/%s: %s is the source language — its text is the key itself and has no file", StringsDir(), name, stem)
		}
		var cells map[string]stringCell
		if err := decodeTableObject(files[name], &cells); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", StringsDir(), name, err)
		}
		for key, c := range cells {
			switch c.State {
			case cellTranslated, cellNeedsReview:
			case cellMissing:
				if c.Value != "" {
					return nil, fmt.Errorf("%s/%s: key %q is missing but carries a value", StringsDir(), name, key)
				}
			default:
				return nil, fmt.Errorf("%s/%s: key %q has unknown state %q", StringsDir(), name, key, c.State)
			}
			if t.Strings[key] == nil {
				t.Strings[key] = map[string]stringCell{}
			}
			t.Strings[key][stem] = c
		}
		locales = append(locales, stem)
	}
	t.Locales = orderedLocales(m.Source, locales)
	return t, nil
}

// readTable reads whichever table the checkout has (D-2, D-4). droppedLocales
// is the old file's lenient rule (the previous run's D-18) and is only ever
// non-empty for layoutLegacy.
func readTable(cwd string) (*stringsTable, tableLayout, []string, error) {
	files, err := readTableDirFiles(cwd)
	if err != nil {
		return nil, layoutNone, nil, err
	}
	legacyPath := filepath.Join(cwd, filepath.FromSlash(StringsPath()))
	_, lerr := os.Lstat(legacyPath)
	hasLegacy := lerr == nil
	_, hasMeta := files[metaFileName]
	switch {
	case hasLegacy && hasMeta:
		return nil, layoutNone, nil, errBothTables()
	case hasLegacy:
		t, dropped, err := readStringsTable(legacyPath)
		if err != nil {
			return nil, layoutNone, nil, err
		}
		// Language files without _meta.json are a move that did not finish; the
		// move rewrites the old file's languages. One the old file does not
		// have would enter the table unread once _meta.json lands (FR-002).
		for name := range files {
			stem, _ := strings.CutSuffix(name, ".json")
			known := false
			for _, loc := range t.Locales {
				if loc == stem {
					known = true
				}
			}
			if !known {
				return nil, layoutNone, nil, fmt.Errorf("%s/%s is beside %s without %s, and %s is not a language of %s — "+
					"move its translations into %s or delete it, then build again",
					StringsDir(), name, StringsPath(), metaFileName, stem, StringsPath(), StringsPath())
			}
		}
		return t, layoutLegacy, dropped, nil
	case len(files) > 0:
		t, err := parseTableDir(files)
		if err != nil {
			return nil, layoutNone, nil, err
		}
		return t, layoutDir, nil, nil
	}
	return nil, layoutNone, nil, nil
}

// adoptTableDir is FR-011's way out of a directory without _meta.json: the
// language files are read as a table of `source` — every rule applies — and
// the next write puts _meta.json in place.
func adoptTableDir(cwd, source string) (*stringsTable, error) {
	c, ok := canonicalLocale(source)
	if !ok {
		return nil, fmt.Errorf("--source %q is not a language tag", source)
	}
	files, err := readTableDirFiles(cwd)
	if err != nil {
		return nil, err
	}
	meta, err := encodeTableFile(tableMeta{Version: tableDirVersion, Source: c})
	if err != nil {
		return nil, err
	}
	files[metaFileName] = meta
	return parseTableDir(files)
}

// encodeTableFile is D-3's bytes: fields in declaration order, map keys in
// byte order (encoding/json sorts as sort.Strings does), two spaces, no HTML
// escaping, one trailing newline.
func encodeTableFile(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tableDirBodies renders the table as files: _meta.json and one file per
// language but the source, EVERY key in every file (D-3) — a cell the row
// lacks is written missing. What the stack would refuse is never returned.
func tableDirBodies(t *stringsTable) (map[string][]byte, error) {
	bodies := map[string][]byte{}
	meta, err := encodeTableFile(tableMeta{Version: tableDirVersion, Source: t.Source})
	if err != nil {
		return nil, err
	}
	bodies[metaFileName] = meta
	for _, loc := range t.Locales {
		if loc == t.Source {
			continue
		}
		cells := make(map[string]stringCell, len(t.Strings))
		for key, row := range t.Strings {
			c, ok := row[loc]
			if !ok {
				c = stringCell{State: cellMissing}
			}
			cells[key] = c
		}
		body, err := encodeTableFile(cells)
		if err != nil {
			return nil, err
		}
		bodies[loc+".json"] = body
	}
	if _, err := parseTableDir(bodies); err != nil {
		return nil, fmt.Errorf("refusing to write a table the stack would refuse: %w", err)
	}
	return bodies, nil
}

// writeTableDir lands the table in palbase/strings/ — language files first,
// _meta.json LAST (D-5), each only when its bytes differ. It never deletes a
// language file: removing a language is deleting its file, somebody's choice.
func writeTableDir(cwd string, t *stringsTable) (bool, error) {
	bodies, err := tableDirBodies(t)
	if err != nil {
		return false, err
	}
	root := filepath.Join(cwd, filepath.FromSlash(StringsDir()))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", StringsDir(), err)
	}
	names := make([]string, 0, len(bodies))
	for name := range bodies {
		if name != metaFileName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	names = append(names, metaFileName)
	changed := false
	for _, name := range names {
		p := filepath.Join(root, name)
		if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, bodies[name]) {
			continue
		}
		if err := replaceTableFileFn(p, bodies[name]); err != nil {
			return changed, fmt.Errorf("write %s/%s: %w", StringsDir(), name, err)
		}
		changed = true
	}
	return changed, nil
}

// migrateLegacyTable moves the old file's table into the directory (FR-002):
// the whole directory is written first, _meta.json last, and only then is the
// old file deleted. A failure before _meta.json leaves the old file the table;
// a failure after it leaves both, which the next build names (FR-003).
func migrateLegacyTable(cwd string, t *stringsTable) error {
	if _, err := writeTableDir(cwd, t); err != nil {
		return err
	}
	legacy := filepath.Join(cwd, filepath.FromSlash(StringsPath()))
	if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s/ is written but %s could not be deleted (%v) — delete it by hand; until then the table has two homes",
			StringsDir(), StringsPath(), err)
	}
	return nil
}

// placeholderPattern is the SDK's interpolate pattern (i18n.ts), with JS's
// `\s` spelled out — RE2's `\s` is ASCII only, JS's is not (D-10).
var placeholderPattern = regexp.MustCompile(
	`\{\{[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*` +
		`([A-Za-z_][A-Za-z0-9_]*)` +
		`[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*\}\}`)

// placeholderNames is the set of names interpolate would replace: a match
// right after `{` or right before `}` is triple braces, left as written.
func placeholderNames(text string) map[string]struct{} {
	names := map[string]struct{}{}
	for _, m := range placeholderPattern.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[0], m[1]
		if (start > 0 && text[start-1] == '{') || (end < len(text) && text[end] == '}') {
			continue
		}
		names[text[m[2]:m[3]]] = struct{}{}
	}
	return names
}

// placeholderDiff is D-10's parity: names the value drops, names it adds;
// both sorted, nil when none.
func placeholderDiff(key, value string) (dropped, added []string) {
	want, got := placeholderNames(key), placeholderNames(value)
	for n := range want {
		if _, ok := got[n]; !ok {
			dropped = append(dropped, n)
		}
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			added = append(added, n)
		}
	}
	sort.Strings(dropped)
	sort.Strings(added)
	return dropped, added
}

// sdkStringsTable reads what the project's INSTALLED @palbase/backend declares
// its stack reads (D-21): the stack a project runs is the one its installed SDK
// names, so the table is written in a format that stack can read. installed is
// "" when the SDK is not installed or declares no version.
func sdkStringsTable(projectDir string) (declared int, installed string) {
	data, err := os.ReadFile(filepath.Join(projectDir, "node_modules", "@palbase", "backend", "package.json"))
	if err != nil {
		return 0, ""
	}
	var pkg struct {
		Version string `json:"version"`
		Palbase struct {
			StringsTable int `json:"stringsTable"`
		} `json:"palbase"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return 0, ""
	}
	return pkg.Palbase.StringsTable, pkg.Version
}
