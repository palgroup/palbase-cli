package backend

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const dirMetaTR = "{\n  \"version\": 2,\n  \"source\": \"tr\"\n}\n"

func writeRel(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

func readRel(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

func TestStringsDirIsTheStacks(t *testing.T) {
	require.Equal(t, "palbase/strings", StringsDir())
}

func TestTableEntryName(t *testing.T) { // D-2, v2 locale.TableEntry
	for name, want := range map[string]bool{
		"_meta.json": true, "en-US.json": true, ".DS_Store": false, "._fr.json": false,
		".fr.json-123": false, "README.md": false, "": false,
	} {
		require.Equal(t, want, tableEntryName(name), name)
	}
}

func TestParseTableDir_CarriesTheStacksRefusals(t *testing.T) { // FR-014, D-19 — v2 dir_test.go's cases
	for _, tc := range []struct {
		name   string
		files  map[string]string
		wantIn string
	}{
		{"meta yok", map[string]string{"en.json": `{}`}, "_meta.json is missing"},
		{"sürüm 1", map[string]string{metaFileName: `{"version":1,"source":"tr"}`}, "version 1 is not supported"},
		{"tanınmayan meta alanı", map[string]string{metaFileName: `{"version":2,"source":"tr","locales":["tr"]}`}, `unknown field "locales"`},
		{"kaynak yok", map[string]string{metaFileName: `{"version":2}`}, "source is required"},
		{"kanonik olmayan kaynak", map[string]string{metaFileName: `{"version":2,"source":"TR"}`}, "not canonical"},
		{"meta sonrası çöp", map[string]string{metaFileName: `{"version":2,"source":"tr"}}`}, "trailing content"},
		{"meta nesne değil", map[string]string{metaFileName: `null`}, "not a JSON object"},
		{"etiket olmayan ad", map[string]string{metaFileName: dirMetaTR, "english.json": `{}`}, "is not a language tag"},
		{"kanonik olmayan ad", map[string]string{metaFileName: dirMetaTR, "en-us.json": `{}`}, "name the file en-US.json"},
		{"kaynak dilin dosyası", map[string]string{metaFileName: dirMetaTR, "tr.json": `{}`}, "is the source language"},
		{"bilinmeyen durum", map[string]string{metaFileName: dirMetaTR, "en.json": `{"k":{"value":"x","state":"done"}}`}, `unknown state "done"`},
		{"değerli missing", map[string]string{metaFileName: dirMetaTR, "en.json": `{"k":{"value":"x","state":"missing"}}`}, "missing but carries a value"},
		{"tanınmayan hücre alanı", map[string]string{metaFileName: dirMetaTR, "en.json": `{"k":{"value":"x","state":"translated","note":"n"}}`}, `unknown field "note"`},
		{"dil dosyası dizi", map[string]string{metaFileName: dirMetaTR, "en.json": `[]`}, "not a JSON object"},
		{"dil dosyası sonrası çöp", map[string]string{metaFileName: dirMetaTR, "en.json": `{"k":{"value":"x","state":"translated"}}}`}, "trailing content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			for name, body := range tc.files {
				files[name] = []byte(body)
			}
			_, err := parseTableDir(files)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantIn)
			require.Contains(t, err.Error(), "palbase/strings/")
		})
	}
}

func TestTableDirBodies_TheBytesAreD3s(t *testing.T) { // D-3, FR-004
	tab := &stringsTable{Version: 1, Source: "tr", Locales: []string{"tr", "de", "en"}, Strings: map[string]map[string]stringCell{
		"Zeta <b>": {"en": {Value: "Zeta & <b>", State: cellTranslated}, "de": {State: cellMissing}},
		"Ağ":       {"en": {Value: "Net", State: cellNeedsReview}},
	}}
	bodies, err := tableDirBodies(tab)
	require.NoError(t, err)
	require.Equal(t, dirMetaTR, string(bodies[metaFileName]))
	require.Equal(t, "{\n  \"Ağ\": {\n    \"value\": \"Net\",\n    \"state\": \"needs_review\"\n  },\n"+
		"  \"Zeta <b>\": {\n    \"value\": \"Zeta & <b>\",\n    \"state\": \"translated\"\n  }\n}\n", string(bodies["en.json"]))
	// A cell the row lacks is written as missing: every key in every file (D-3).
	require.Contains(t, string(bodies["de.json"]), "\"Ağ\": {\n    \"value\": \"\",\n    \"state\": \"missing\"\n  }")
	empty, err := tableDirBodies(&stringsTable{Version: 1, Source: "tr", Locales: []string{"tr", "fr"}, Strings: map[string]map[string]stringCell{}})
	require.NoError(t, err)
	require.Equal(t, "{}\n", string(empty["fr.json"]))
}

func TestWriteTableDir_MetaLastAndIdenticalFilesUntouched(t *testing.T) { // D-5, FR-004
	dir := t.TempDir()
	var order []string
	prev := replaceTableFileFn
	replaceTableFileFn = func(path string, body []byte) error {
		order = append(order, filepath.Base(path))
		return replaceFileAtomically(path, body)
	}
	t.Cleanup(func() { replaceTableFileFn = prev })
	tab := &stringsTable{Version: 1, Source: "tr", Locales: []string{"tr", "de", "en"}, Strings: map[string]map[string]stringCell{
		"Merhaba": {"en": {Value: "Hello", State: cellTranslated}, "de": {State: cellMissing}},
	}}
	changed, err := writeTableDir(dir, tab)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, []string{"de.json", "en.json", metaFileName}, order, "_meta.json is written LAST")
	order = nil
	changed, err = writeTableDir(dir, tab)
	require.NoError(t, err)
	require.False(t, changed)
	require.Empty(t, order, "identical bytes are not rewritten")
}

func TestReadTable_EveryLayout(t *testing.T) { // D-2, D-4
	legacy := `{"version":1,"source":"tr","locales":["tr","en"],"strings":{"Merhaba":{"en":{"value":"Hello (legacy)","state":"translated"}}}}`
	t.Run("hiçbiri", func(t *testing.T) {
		tab, layout, _, err := readTable(t.TempDir())
		require.NoError(t, err)
		require.Nil(t, tab)
		require.Equal(t, layoutNone, layout)
	})
	t.Run("eski dosya", func(t *testing.T) {
		dir := t.TempDir()
		writeRel(t, dir, "palbase/strings.json", legacy)
		tab, layout, _, err := readTable(dir)
		require.NoError(t, err)
		require.Equal(t, layoutLegacy, layout)
		require.Equal(t, "Hello (legacy)", tab.Strings["Merhaba"]["en"].Value)
	})
	t.Run("dizin", func(t *testing.T) {
		dir := t.TempDir()
		writeRel(t, dir, "palbase/strings/_meta.json", dirMetaTR)
		writeRel(t, dir, "palbase/strings/en.json", `{"Merhaba":{"value":"Hello","state":"translated"}}`)
		writeRel(t, dir, "palbase/strings/.DS_Store", "junk")
		tab, layout, _, err := readTable(dir)
		require.NoError(t, err)
		require.Equal(t, layoutDir, layout)
		require.Equal(t, []string{"tr", "en"}, tab.Locales)
	})
	t.Run("ikisi birden", func(t *testing.T) {
		dir := t.TempDir()
		writeRel(t, dir, "palbase/strings.json", legacy)
		writeRel(t, dir, "palbase/strings/_meta.json", dirMetaTR)
		_, _, _, err := readTable(dir)
		require.ErrorContains(t, err, "palbase/strings.json and palbase/strings/ both exist")
	})
	t.Run("eski dosya ve kalıntı", func(t *testing.T) {
		dir := t.TempDir()
		writeRel(t, dir, "palbase/strings.json", legacy)
		writeRel(t, dir, "palbase/strings/en.json", `{"Merhaba":{"value":"Hello (leftover)","state":"translated"}}`)
		tab, layout, _, err := readTable(dir)
		require.NoError(t, err)
		require.Equal(t, layoutLegacy, layout, "without _meta.json the old file is the table")
		require.Equal(t, "Hello (legacy)", tab.Strings["Merhaba"]["en"].Value)
	})
	t.Run("eski tabloda olmayan dilin kalıntısı", func(t *testing.T) {
		dir := t.TempDir()
		writeRel(t, dir, "palbase/strings.json", legacy)
		writeRel(t, dir, "palbase/strings/fr.json", `{}`)
		_, _, _, err := readTable(dir)
		require.ErrorContains(t, err, "palbase/strings/fr.json is beside palbase/strings.json without _meta.json, and fr is not a language of palbase/strings.json")
	})
	t.Run("meta'sız dizin", func(t *testing.T) {
		dir := t.TempDir()
		writeRel(t, dir, "palbase/strings/en.json", `{}`)
		_, _, _, err := readTable(dir)
		require.ErrorIs(t, err, errNoTableMeta)
		require.ErrorContains(t, err, "palbase build --source <language>")
	})
	t.Run("sembolik bağ", func(t *testing.T) {
		dir := t.TempDir()
		writeRel(t, dir, "palbase/strings/_meta.json", dirMetaTR)
		writeRel(t, dir, "elsewhere.json", `{}`)
		require.NoError(t, os.Symlink(filepath.Join(dir, "elsewhere.json"), filepath.Join(dir, "palbase", "strings", "fr.json")))
		_, _, _, err := readTable(dir)
		require.ErrorContains(t, err, "palbase/strings/fr.json is not a regular file")
	})
	t.Run("dizin değil", func(t *testing.T) {
		dir := t.TempDir()
		writeRel(t, dir, "palbase/strings", "a file")
		_, _, _, err := readTable(dir)
		require.ErrorContains(t, err, "palbase/strings is not a directory")
	})
}

func TestMigrateLegacyTable_IntoAnExistingDirectory(t *testing.T) { // FR-002, D-5
	dir := t.TempDir()
	writeRel(t, dir, "palbase/strings.json", `{"version":1,"source":"tr","locales":["tr","en"],"strings":{"Merhaba":{"en":{"value":"Hello","state":"translated"}}}}`)
	writeRel(t, dir, "palbase/strings/.DS_Store", "junk")
	tab, layout, _, err := readTable(dir)
	require.NoError(t, err)
	require.Equal(t, layoutLegacy, layout)
	require.NoError(t, migrateLegacyTable(dir, tab))
	require.Equal(t, dirMetaTR, readRel(t, dir, "palbase/strings/_meta.json"))
	require.Contains(t, readRel(t, dir, "palbase/strings/en.json"), `"value": "Hello"`)
	_, err = os.Stat(filepath.Join(dir, "palbase", "strings.json"))
	require.True(t, os.IsNotExist(err), "the old file is deleted once the directory is complete")
	require.Equal(t, "junk", readRel(t, dir, "palbase/strings/.DS_Store"), "a file that is not the table is left alone")
}

func TestPlaceholderDiff_IsInterpolatesOwnRule(t *testing.T) { // D-10
	for _, tc := range []struct {
		key, value     string
		dropped, added []string
	}{
		{"Kart {{amount}} ₺", "Card {{amount}} ₺", nil, nil},
		{"Kart {{amount}} ₺", "Card {{ amount }} ₺", nil, nil},
		{"Kart {{amount}} ₺", "Card ₺", []string{"amount"}, nil},
		{"Kart", "Card {{amount}}", nil, []string{"amount"}},
		{"{{a}} ve {{a}}", "{{a}}", nil, nil},
		{"{{{raw}}}", "raw", nil, nil},
		{"{{a.b}}", "x", nil, nil},
		{"{{ ad}}", "{{ad}}", nil, nil},
		{"{{b}} {{a}}", "", []string{"a", "b"}, nil},
	} {
		dropped, added := placeholderDiff(tc.key, tc.value)
		require.Equal(t, tc.dropped, dropped, "%q → %q", tc.key, tc.value)
		require.Equal(t, tc.added, added, "%q → %q", tc.key, tc.value)
	}
}

func TestSdkStringsTable_ReadsTheInstalledDeclaration(t *testing.T) { // D-21
	dir := t.TempDir()
	declared, installed := sdkStringsTable(dir)
	require.Equal(t, 0, declared)
	require.Equal(t, "", installed)
	writeRel(t, dir, "node_modules/@palbase/backend/package.json", `{"version":"41.2.0"}`)
	declared, installed = sdkStringsTable(dir)
	require.Equal(t, 0, declared)
	require.Equal(t, "41.2.0", installed)
	writeRel(t, dir, "node_modules/@palbase/backend/package.json", `{"version":"41.3.0","palbase":{"stringsTable":2}}`)
	declared, installed = sdkStringsTable(dir)
	require.Equal(t, 2, declared)
	require.Equal(t, "41.3.0", installed)
}

func TestTableDirEntryNames_Sorted(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"en.json", "_meta.json", "de.json", ".DS_Store", "notes.md"} {
		writeRel(t, dir, "palbase/strings/"+n, "{}")
	}
	names, err := tableDirEntryNames(dir)
	require.NoError(t, err)
	require.True(t, sort.StringsAreSorted(names))
	require.Equal(t, []string{"_meta.json", "de.json", "en.json"}, names)
	require.False(t, strings.Contains(strings.Join(names, ","), "DS_Store"))
}
