package backend

// strings_table.go — the string table in memory, and its OLD single-file form
// (palbase/strings.json) that strings_dir.go moves into palbase/strings/.
//
// `palbase build` owns this file's SHAPE and nothing else: the keys come from
// the t() calls the scanner found, the translations from whoever filled them
// in. So a build MERGES — it adds the keys the code gained, removes the ones it
// lost, and never touches a cell somebody wrote (FR-017..FR-019).
//
// THE STACK VALIDATES THE SAME FILE (v2 internal/platform/locale.Parse), and a
// table the stack refuses is a deploy that fails after this command said OK.
// So the rules below are that function's, the tags are canonicalised by the
// same library at the same version (D-17), and nothing is written that would
// not pass them (FR-025).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/language"
)

// maxStringsTableBytes is v2's locale.MaxTableBytes (NFR-002).
const maxStringsTableBytes = 8 << 20

// maxLocaleTagBytes is v2's maxTagLen.
const maxLocaleTagBytes = 64

const (
	cellTranslated  = "translated"
	cellNeedsReview = "needs_review"
	cellMissing     = "missing"
)

type stringCell struct {
	Value string `json:"value"`
	State string `json:"state"`
}

// stringsTable is the file, field for field. The field ORDER is the order the
// file is written in (D-10): encoding/json writes struct fields as declared.
type stringsTable struct {
	Version int                              `json:"version"`
	Source  string                           `json:"source"`
	Locales []string                         `json:"locales"`
	Strings map[string]map[string]stringCell `json:"strings"`
}

// canonicalLocale is v2's locale.Canonical, rule for rule (D-17).
func canonicalLocale(tag string) (string, bool) {
	t := strings.TrimSpace(tag)
	if t == "" || t == "*" || len(t) > maxLocaleTagBytes {
		return "", false
	}
	parsed, err := language.Parse(t)
	if err != nil {
		return "", false
	}
	c := parsed.String()
	base := c
	if i := strings.IndexByte(c, '-'); i > 0 {
		base = c[:i]
	}
	switch base {
	case "", "mul", "und":
		return "", false
	}
	return c, true
}

// parseStringsTable applies v2's locale.Parse rules. lenient relaxes exactly
// ONE of them (D-18): a cell in a language that is no longer in `locales` — a
// canonical tag, removed on purpose — is dropped and its language returned,
// instead of refusing the file; removing a language leaves the file invalid
// until a build writes it back (FR-032).
func parseStringsTable(raw []byte, lenient bool) (*stringsTable, []string, error) {
	if len(raw) > maxStringsTableBytes {
		return nil, nil, fmt.Errorf("%s is %d bytes, over the %d byte limit", StringsPath(), len(raw), maxStringsTableBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var t stringsTable
	if err := dec.Decode(&t); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", StringsPath(), err)
	}
	if dec.More() {
		return nil, nil, fmt.Errorf("%s: trailing content after the table object", StringsPath())
	}
	if t.Version != 1 {
		return nil, nil, fmt.Errorf("%s: version %d is not supported (only 1)", StringsPath(), t.Version)
	}
	if t.Source == "" {
		return nil, nil, fmt.Errorf("%s: source is required", StringsPath())
	}
	if c, ok := canonicalLocale(t.Source); !ok {
		return nil, nil, fmt.Errorf("%s: source %q is not a language tag", StringsPath(), t.Source)
	} else if c != t.Source {
		return nil, nil, fmt.Errorf("%s: source %q is not canonical (want %q)", StringsPath(), t.Source, c)
	}
	known := make(map[string]struct{}, len(t.Locales))
	for _, l := range t.Locales {
		if c, ok := canonicalLocale(l); !ok {
			return nil, nil, fmt.Errorf("%s: locales carries %q, which is not a language tag", StringsPath(), l)
		} else if c != l {
			return nil, nil, fmt.Errorf("%s: locales carries %q, which is not canonical (want %q)", StringsPath(), l, c)
		}
		known[l] = struct{}{}
	}
	if _, ok := known[t.Source]; !ok {
		return nil, nil, fmt.Errorf("%s: source %q is not in locales", StringsPath(), t.Source)
	}
	droppedSet := map[string]struct{}{}
	for key, cells := range t.Strings {
		for loc, c := range cells {
			if _, ok := known[loc]; !ok {
				// Only a REMOVED language is dropped: a canonical tag that is simply
				// no longer in `locales`. A cell under `en-us` while `locales` says
				// `en-US`, or under something that is not a tag at all, is a
				// hand-written translation filed under a typo — dropping it would
				// delete somebody's work, so it is refused by name like every other
				// violation (FR-025).
				if c, isTag := canonicalLocale(loc); !lenient || !isTag || c != loc {
					return nil, nil, fmt.Errorf("%s: key %q carries locale %q which is not in locales", StringsPath(), key, loc)
				}
				delete(cells, loc)
				droppedSet[loc] = struct{}{}
				continue
			}
			switch c.State {
			case cellTranslated, cellNeedsReview:
			case cellMissing:
				if c.Value != "" {
					return nil, nil, fmt.Errorf("%s: key %q locale %q is missing but carries a value", StringsPath(), key, loc)
				}
			default:
				return nil, nil, fmt.Errorf("%s: key %q locale %q has unknown state %q", StringsPath(), key, loc, c.State)
			}
		}
	}
	if t.Strings == nil {
		t.Strings = map[string]map[string]stringCell{}
	}
	dropped := make([]string, 0, len(droppedSet))
	for loc := range droppedSet {
		dropped = append(dropped, loc)
	}
	sort.Strings(dropped)
	return &t, dropped, nil
}

// readStringsTable reads the checkout's table. No file is (nil, nil, nil): a
// project that has not started one yet (FR-022).
func readStringsTable(path string) (*stringsTable, []string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", StringsPath(), err)
	}
	return parseStringsTable(raw, true)
}

// orderedLocales is D-10's order: the source language first, the rest in byte
// order, each once.
func orderedLocales(source string, locales []string) []string {
	rest := make([]string, 0, len(locales))
	seen := map[string]struct{}{source: {}}
	for _, l := range locales {
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		rest = append(rest, l)
	}
	sort.Strings(rest)
	return append([]string{source}, rest...)
}

// mergeStringsTable is the build's whole decision about the table.
//
//   - no table and no --source: (nil, nil, nil) — nothing is written (FR-022)
//   - no table and --source: a new table in that language (FR-023)
//   - a table and a DIFFERENT --source: refused, both named (FR-024) — every key
//     is a sentence in the source language, so changing it is a migration
//   - a key the code still uses keeps every cell it has (FR-018)
//   - a new key, or a language new to `locales`, gets a `missing` cell in every
//     language but the source (FR-017, FR-020)
//   - a key the code no longer uses is dropped and returned (FR-019)
func mergeStringsTable(existing *stringsTable, keys []string, sourceFlag string) (*stringsTable, []string, error) {
	flag := ""
	if sourceFlag != "" {
		c, ok := canonicalLocale(sourceFlag)
		if !ok {
			return nil, nil, fmt.Errorf("--source %q is not a language tag", sourceFlag)
		}
		flag = c
	}
	base := existing
	if base == nil {
		if flag == "" {
			return nil, nil, nil
		}
		base = &stringsTable{Version: 1, Source: flag, Locales: []string{flag}, Strings: map[string]map[string]stringCell{}}
	} else if flag != "" && flag != base.Source {
		return nil, nil, fmt.Errorf("--source %s conflicts with the table, whose source language is %s — every key in it is a "+
			"%s sentence, so changing the source language is a migration of every key, not a flag",
			flag, base.Source, base.Source)
	}

	locales := orderedLocales(base.Source, base.Locales)
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	merged := &stringsTable{
		Version: 1,
		Source:  base.Source,
		Locales: locales,
		Strings: make(map[string]map[string]stringCell, len(want)),
	}
	for k := range want {
		cells := make(map[string]stringCell, len(locales))
		for loc, c := range base.Strings[k] {
			cells[loc] = c
		}
		// A key with nothing but whitespace is its own translation in every
		// language (FR-005): no person or model has anything to add, and a
		// `missing` cell would be a sentence the stack refuses to translate.
		blank := strings.TrimSpace(k) == ""
		for _, loc := range locales {
			if loc == base.Source {
				continue
			}
			c, ok := cells[loc]
			if blank && (!ok || c.State == cellMissing) {
				cells[loc] = stringCell{Value: k, State: cellTranslated}
				continue
			}
			if !ok {
				cells[loc] = stringCell{Value: "", State: cellMissing}
			}
		}
		merged.Strings[k] = cells
	}
	var dropped []string
	for k := range base.Strings {
		if _, ok := want[k]; !ok {
			dropped = append(dropped, k)
		}
	}
	sort.Strings(dropped)
	return merged, dropped, nil
}

// encodeStringsTable writes D-10's bytes: fields in declaration order, map keys
// in byte order (encoding/json sorts them exactly as sort.Strings does), two
// spaces, no HTML escaping — `<`, `>` and `&` are text somebody wrote.
func encodeStringsTable(t *stringsTable) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(t); err != nil {
		return nil, fmt.Errorf("encode %s: %w", StringsPath(), err)
	}
	return buf.Bytes(), nil
}

// writeStringsTable lands the table — and only a table the stack would accept
// (FR-025). Identical bytes are not rewritten: a build that changes nothing
// leaves the file, and `git status`, alone (FR-021).
func writeStringsTable(path string, t *stringsTable) error {
	body, err := encodeStringsTable(t)
	if err != nil {
		return err
	}
	if _, _, err := parseStringsTable(body, false); err != nil {
		return fmt.Errorf("refusing to write a table the stack would refuse: %w", err)
	}
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, body) {
		return nil
	}
	// The file somebody commits, rewritten the ONE way this package rewrites
	// such a file (target.go): whole or not at all, through a symlink, keeping
	// its mode, refusing one that is read-only.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := replaceFileAtomically(path, body); err != nil {
		return fmt.Errorf("write %s: %w", StringsPath(), err)
	}
	return nil
}
