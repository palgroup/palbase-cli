package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const tableKey = "Kartınız reddedildi ({{amount}} ₺)"

func fixtureTable() *stringsTable {
	return &stringsTable{
		Version: 1, Source: "tr", Locales: []string{"tr", "de", "en"},
		Strings: map[string]map[string]stringCell{
			tableKey: {
				"de": {Value: "", State: cellMissing},
				"en": {Value: "Your card was declined ({{amount}} ₺)", State: cellTranslated},
			},
			"Gözden geçirilecek": {"en": {Value: "To review", State: cellNeedsReview}, "tr": {Value: "elle yazılmış", State: cellTranslated}},
		},
	}
}

func TestMergeStringsTable_NewKeyGetsAMissingCellPerLanguage(t *testing.T) { // FR-017
	merged, dropped, err := mergeStringsTable(fixtureTable(), []string{tableKey, "Gözden geçirilecek", "Yeni"}, "")
	require.NoError(t, err)
	require.Empty(t, dropped)
	require.Equal(t, map[string]stringCell{"de": {State: cellMissing}, "en": {State: cellMissing}}, merged.Strings["Yeni"],
		"a new key gets a missing cell in every language but the source")
}

func TestMergeStringsTable_KeepsEveryExistingCell(t *testing.T) { // FR-018
	before := fixtureTable()
	merged, _, err := mergeStringsTable(before, []string{tableKey, "Gözden geçirilecek"}, "")
	require.NoError(t, err)
	require.Equal(t, fixtureTable().Strings[tableKey], merged.Strings[tableKey])
	// Every cell that was there is there unchanged — a needs_review cell and a
	// hand-written source cell are somebody's work…
	for loc, cell := range fixtureTable().Strings["Gözden geçirilecek"] {
		require.Equal(t, cell, merged.Strings["Gözden geçirilecek"][loc], loc)
	}
	// …and the language the row lacked gets its missing cell (FR-017/FR-020).
	require.Equal(t, stringCell{State: cellMissing}, merged.Strings["Gözden geçirilecek"]["de"])
}

func TestMergeStringsTable_DropsKeysTheCodeNoLongerUses(t *testing.T) { // FR-019
	merged, dropped, err := mergeStringsTable(fixtureTable(), []string{tableKey}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Gözden geçirilecek"}, dropped)
	require.NotContains(t, merged.Strings, "Gözden geçirilecek")
}

func TestMergeStringsTable_ANewLanguageGetsMissingCells(t *testing.T) { // FR-020
	tab := fixtureTable()
	tab.Locales = append(tab.Locales, "fr")
	merged, _, err := mergeStringsTable(tab, []string{tableKey}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"tr", "de", "en", "fr"}, merged.Locales)
	require.Equal(t, stringCell{State: cellMissing}, merged.Strings[tableKey]["fr"])
}

func TestWriteStringsTable_TheBytesAreD10sAndStable(t *testing.T) { // FR-021, D-10
	path := filepath.Join(t.TempDir(), filepath.FromSlash(StringsPath()))
	tab := &stringsTable{Version: 1, Source: "tr", Locales: []string{"tr", "en"}, Strings: map[string]map[string]stringCell{
		"İ": {"en": {State: cellMissing}}, "Z": {"en": {State: cellMissing}},
		"a": {"en": {Value: "<&>", State: cellTranslated}}, "Ç": {"en": {State: cellMissing}},
	}}
	require.NoError(t, writeStringsTable(path, tab))
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	body := string(first)
	require.True(t, strings.HasPrefix(body, "{\n  \"version\": 1,\n  \"source\": \"tr\",\n  \"locales\": [\n    \"tr\",\n    \"en\"\n  ],\n  \"strings\": {\n"))
	require.True(t, strings.HasSuffix(body, "}\n") && !strings.HasSuffix(body, "\n\n"), "one trailing newline")
	require.Contains(t, body, `"value": "<&>"`, "no HTML escaping")
	// UTF-8 byte order, as Go's sort.Strings — not JS's UTF-16 order.
	require.Less(t, strings.Index(body, `"Z"`), strings.Index(body, `"a"`))
	require.Less(t, strings.Index(body, `"a"`), strings.Index(body, `"Ç"`))
	require.Less(t, strings.Index(body, `"Ç"`), strings.Index(body, `"İ"`))

	info, err := os.Stat(path)
	require.NoError(t, err)
	again, _, err := readStringsTable(path)
	require.NoError(t, err)
	merged, _, err := mergeStringsTable(again, []string{"İ", "Z", "a", "Ç"}, "")
	require.NoError(t, err)
	require.NoError(t, writeStringsTable(path, merged))
	second, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, string(second), "a second run with the same code rewrote the table")
	info2, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, info.ModTime(), info2.ModTime(), "identical bytes must not be rewritten")
}

func TestMergeStringsTable_NoTableAndNoSourceWritesNothing(t *testing.T) { // FR-022's merge half
	merged, dropped, err := mergeStringsTable(nil, []string{"Merhaba"}, "")
	require.NoError(t, err)
	require.Nil(t, merged)
	require.Nil(t, dropped)
}

func TestMergeStringsTable_SourceStartsATableInCanonicalForm(t *testing.T) { // FR-023
	merged, _, err := mergeStringsTable(nil, []string{"Merhaba"}, "TR")
	require.NoError(t, err)
	require.Equal(t, "tr", merged.Source)
	require.Equal(t, []string{"tr"}, merged.Locales)
	require.Equal(t, map[string]stringCell{}, merged.Strings["Merhaba"])
	_, _, err = mergeStringsTable(nil, []string{"Merhaba"}, "not a tag!!")
	require.ErrorContains(t, err, "not a language tag")
}

func TestMergeStringsTable_ADifferentSourceIsRefusedNamingBoth(t *testing.T) { // FR-024
	_, _, err := mergeStringsTable(fixtureTable(), []string{tableKey}, "en")
	require.Error(t, err)
	require.Contains(t, err.Error(), "en")
	require.Contains(t, err.Error(), "source language is tr")
	merged, _, err := mergeStringsTable(fixtureTable(), []string{tableKey}, "tr")
	require.NoError(t, err, "the SAME source is not a conflict")
	require.Equal(t, "tr", merged.Source)
}

func TestWriteStringsTable_RefusesWhatTheStackRefuses(t *testing.T) { // FR-025
	path := filepath.Join(t.TempDir(), filepath.FromSlash(StringsPath()))
	bad := &stringsTable{Version: 1, Source: "tr", Locales: []string{"tr", "en"},
		Strings: map[string]map[string]stringCell{"k": {"en": {Value: "x", State: "draft"}}}}
	require.ErrorContains(t, writeStringsTable(path, bad), "draft")
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "a refused table must not be written")

	huge := &stringsTable{Version: 1, Source: "tr", Locales: []string{"tr"}, Strings: map[string]map[string]stringCell{
		strings.Repeat("x", maxStringsTableBytes): {},
	}}
	require.ErrorContains(t, writeStringsTable(path, huge), "byte limit")
}

// v2's TestParseRefuses, case for case (v2/internal/platform/locale/table_test.go:34):
// the stack and the build refuse the same tables for the same named reason.
func TestParseStringsTable_CarriesTheStacksRefusals(t *testing.T) { // FR-025, D-17
	for _, tc := range []struct{ name, body, wantIn string }{
		{"tanınmayan üst alan", `{"version":1,"source":"tr","locales":["tr"],"strings":{},"extra":1}`, "extra"},
		{"desteklenmeyen sürüm", `{"version":2,"source":"tr","locales":["tr"],"strings":{}}`, "version"},
		{"kaynak dil locales içinde değil", `{"version":1,"source":"fr","locales":["tr","en"],"strings":{}}`, "source"},
		{"hücre dili locales dışında", `{"version":1,"source":"tr","locales":["tr"],"strings":{"k":{"zz":{"value":"x","state":"translated"}}}}`, "zz"},
		{"value string değil", `{"version":1,"source":"tr","locales":["tr","en"],"strings":{"k":{"en":{"value":7,"state":"translated"}}}}`, "value"},
		{"tanınmayan state", `{"version":1,"source":"tr","locales":["tr","en"],"strings":{"k":{"en":{"value":"x","state":"draft"}}}}`, "draft"},
		{"missing ama değer taşıyor", `{"version":1,"source":"tr","locales":["tr","en"],"strings":{"k":{"en":{"value":"x","state":"missing"}}}}`, "missing"},
		{"kanonik olmayan kaynak", `{"version":1,"source":"TR","locales":["TR"],"strings":{}}`, "canonical"},
		{"sondaki çöp", `{"version":1,"source":"tr","locales":["tr"],"strings":{}}  GARBAGE`, "trailing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseStringsTable([]byte(tc.body), false)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantIn)
			require.Contains(t, err.Error(), StringsPath(), "the refusal names the file")
		})
	}
	_, _, err := parseStringsTable(make([]byte, maxStringsTableBytes+1), false)
	require.ErrorContains(t, err, "byte limit")
}

// v2's TestCanonical cases (chain_test.go:8) plus the aliases the stack applies.
func TestCanonicalLocale_IsTheStacksRule(t *testing.T) { // D-17
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"tr", "tr", true}, {"tr-tr", "tr-TR", true}, {"  en-US  ", "en-US", true},
		{"*", "", false}, {"", "", false}, {"xyz", "", false}, {"mul", "", false}, {"und", "", false},
		{strings.Repeat("x", 65), "", false}, {"und-US", "", false}, {"mul-TR", "", false},
		{"iw", "he", true}, {"tl", "fil", true},
	} {
		got, ok := canonicalLocale(tc.in)
		require.Equal(t, tc.want, got, tc.in)
		require.Equal(t, tc.ok, ok, tc.in)
	}
}

func TestReadStringsTable_DropsALanguageRemovedFromLocales(t *testing.T) { // FR-032, D-18
	path := filepath.Join(t.TempDir(), filepath.FromSlash(StringsPath()))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"source":"tr","locales":["tr","en"],"strings":{"k":{"en":{"value":"E","state":"translated"},"de":{"value":"D","state":"translated"}}}}`), 0o644))
	tab, droppedLocales, err := readStringsTable(path)
	require.NoError(t, err)
	require.Equal(t, []string{"de"}, droppedLocales)
	require.Equal(t, map[string]stringCell{"en": {Value: "E", State: cellTranslated}}, tab.Strings["k"])
	// Every other rule is still strict on read.
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"source":"tr","locales":["tr","en"],"strings":{"k":{"en":{"value":"x","state":"draft"}}}}`), 0o644))
	_, _, err = readStringsTable(path)
	require.ErrorContains(t, err, "draft")
}

func TestReadStringsTable_NoFileIsNoTable(t *testing.T) {
	tab, dropped, err := readStringsTable(filepath.Join(t.TempDir(), filepath.FromSlash(StringsPath())))
	require.NoError(t, err)
	require.Nil(t, tab)
	require.Nil(t, dropped)
}

// D-18 drops a REMOVED language only. A translation filed under a mis-cased or
// invalid tag is somebody's work: refused by name, never silently deleted
// (review of T005, I-2 — measured: "Hello" under `en-us` became `missing`).
func TestReadStringsTable_RefusesAMisfiledTranslation(t *testing.T) {
	path := filepath.Join(t.TempDir(), filepath.FromSlash(StringsPath()))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	for _, loc := range []string{"en-us", "EN-US", "not a tag"} {
		body := `{"version":1,"source":"tr","locales":["tr","en-US"],"strings":{"k":{"` + loc + `":{"value":"Hello","state":"translated"}}}}`
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
		_, _, err := readStringsTable(path)
		require.ErrorContains(t, err, loc, "a translation under %q was dropped instead of refused", loc)
	}
}

func TestMergeStringsTable_OrdersAndDedupesLocales(t *testing.T) { // D-10
	tab := fixtureTable()
	tab.Locales = []string{"tr", "en", "de", "en"}
	merged, _, err := mergeStringsTable(tab, []string{tableKey}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"tr", "de", "en"}, merged.Locales)
}

func TestMergeStringsTable_TheSameSourceSpelledOtherwiseIsNoConflict(t *testing.T) { // FR-024
	merged, _, err := mergeStringsTable(fixtureTable(), []string{tableKey}, "TR")
	require.NoError(t, err)
	require.Equal(t, "tr", merged.Source)
}

func TestMergeStringsTable_DroppedKeysComeBackSorted(t *testing.T) { // FR-019
	tab := fixtureTable()
	tab.Strings["b"] = map[string]stringCell{}
	tab.Strings["a"] = map[string]stringCell{}
	_, dropped, err := mergeStringsTable(tab, []string{tableKey}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"Gözden geçirilecek", "a", "b"}, dropped)
}

// The table is a file somebody commits: it is rewritten the way target.go
// rewrites one — through a symlink, keeping its mode, and a read-only file is
// not written (review of T005, I-1).
func TestWriteStringsTable_RewritesTheFileTheWayTheCheckoutOwnsIt(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "shared", "strings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(real), 0o755))
	require.NoError(t, os.WriteFile(real, []byte("{}\n"), 0o600))
	path := filepath.Join(dir, filepath.FromSlash(StringsPath()))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.Symlink(real, path))

	require.NoError(t, writeStringsTable(path, fixtureTable()))
	fi, err := os.Lstat(path)
	require.NoError(t, err)
	require.NotZero(t, fi.Mode()&os.ModeSymlink, "the symlink was replaced by a copy")
	info, err := os.Stat(real)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the file's own mode was not kept")
	body, err := os.ReadFile(real)
	require.NoError(t, err)
	require.Contains(t, string(body), tableKey)

	if os.Geteuid() == 0 {
		return // root writes a 0444 file; the read-only half cannot be built
	}
	require.NoError(t, os.Chmod(real, 0o444))
	t.Cleanup(func() { _ = os.Chmod(real, 0o644) })
	tab := fixtureTable()
	tab.Strings["Yeni"] = map[string]stringCell{}
	require.Error(t, writeStringsTable(path, tab), "a read-only table was overwritten")
	after, err := os.ReadFile(real)
	require.NoError(t, err)
	require.Equal(t, string(body), string(after))
}
