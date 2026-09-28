package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE READER IS JAVA'S. Gradle, AGP and the Palbase plugin load gradle.properties
// and local.properties through java.util.Properties.load(InputStream), and a
// doctor that read them any other way would call a working key broken, or a
// broken one fine. Every expectation below was measured with that method on
// the same bytes (OpenJDK, 2026-09-28): a comment is never continued, a line
// that a continuation leaves empty makes the next one a comment, a lone `\` at
// the end of the file is dropped, and the value keeps its trailing blanks —
// trimming them is the plugin's step, not the reader's.
func TestLoadPropertiesReadsAFileAsJavaDoes(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want []propertyKey
	}{
		{"equals", "a=b\n", []propertyKey{{"a", "b"}}},
		{"blanks around the separator", "  palbase.env.debug = main  \n", []propertyKey{{"palbase.env.debug", "main  "}}},
		{"colon", "palbase.env.debug:main", []propertyKey{{"palbase.env.debug", "main"}}},
		{"blank", "palbase.env.debug main", []propertyKey{{"palbase.env.debug", "main"}}},
		{"tab and blank", "palbase.env.debug\t=\t main", []propertyKey{{"palbase.env.debug", "main"}}},
		{"second separator is the value's", "palbase.env.debug==main", []propertyKey{{"palbase.env.debug", "=main"}}},
		{"continuation", "palbase.env.debug=ma\\\n   in\n", []propertyKey{{"palbase.env.debug", "main"}}},
		{"continuation over CRLF", "palbase.env.debug=ma\\\r\n   in\r\n", []propertyKey{{"palbase.env.debug", "main"}}},
		{"a comment is not continued", "# palbase.env.debug=x\\\npalbase.env.release=y\n", []propertyKey{{"palbase.env.release", "y"}}},
		{"bang comment, no final newline", "! c\npalbase.env.qa=z", []propertyKey{{"palbase.env.qa", "z"}}},
		{"unicode escape", "palbase.env.debug=Z\\u00fcrich", []propertyKey{{"palbase.env.debug", "Zürich"}}},
		{"unicode escape, upper hex", "palbase.env.debug=Z\\u00FCrich", []propertyKey{{"palbase.env.debug", "Zürich"}}},
		{"escaped leading blank", "palbase.env.debug=\\ main", []propertyKey{{"palbase.env.debug", " main"}}},
		{"escaped separator in the key", "palbase.env.de\\=bug=x", []propertyKey{{"palbase.env.de=bug", "x"}}},
		{"escaped blank in the key", "palbase.env.de\\ bug=x", []propertyKey{{"palbase.env.de bug", "x"}}},
		{"tab escape", "palbase.env.debug=a\\tb", []propertyKey{{"palbase.env.debug", "a\tb"}}},
		{"UTF-8 bytes read as ISO-8859-1", "palbase.env.debug=Z\xc3\xbcrich", []propertyKey{{"palbase.env.debug", "Z\u00c3\u00bcrich"}}},
		{"lone backslash at the end of the file", "palbase.env.debug=x\\", []propertyKey{{"palbase.env.debug", "x"}}},
		{"escaped backslash", "palbase.env.debug=x\\\\", []propertyKey{{"palbase.env.debug", "x\\"}}},
		{"surrogate pair", "palbase.env.debug=\\uD83D\\uDE00", []propertyKey{{"palbase.env.debug", "😀"}}},
		{"an emptied line makes the next a comment", "\\\n#notcomment=1\n", nil},
		{"last value wins", "palbase.env.debug=a\npalbase.env.debug=b\n", []propertyKey{{"palbase.env.debug", "b"}}},
		{"continuation into an empty line", "palbase.env.debug=x\\\n\n  y\n", []propertyKey{{"palbase.env.debug", "x"}, {"y", ""}}},
		{"CR line ends", "a=1\rb=2\r", []propertyKey{{"a", "1"}, {"b", "2"}}},
		{"no value", "palbase.env.debug", []propertyKey{{"palbase.env.debug", ""}}},
		{"hash inside a value", "palbase.env.debug=a#b", []propertyKey{{"palbase.env.debug", "a#b"}}},
		{"continuation at the end of the file", "palbase.env.debug=\\\n", []propertyKey{{"palbase.env.debug", ""}}},
		{"blank and comment lines", "   \n\t\f\n  # x\n\t! y\npalbase.env.debug=m\n", []propertyKey{{"palbase.env.debug", "m"}}},
		{"a continued line is no comment", "palbase.env.debug=a\\\n  # b\n", []propertyKey{{"palbase.env.debug", "a# b"}}},
		{"continuation over CR", "palbase.env.debug=a\\\r  b\n", []propertyKey{{"palbase.env.debug", "ab"}}},
		{"escaped backslash ends no line", "palbase.env.debug=x\\\\\nk=v", []propertyKey{{"palbase.env.debug", "x\\"}, {"k", "v"}}},
		{"two emptied lines", "\\\n\\\n", []propertyKey{{"", ""}}},
		{"continued key", "p\\\n  q=r", []propertyKey{{"pq", "r"}}},
		{"unknown escapes are the letter", "palbase.env.debug=\\q\\\"", []propertyKey{{"palbase.env.debug", "q\""}}},
		{"odd backslashes continue", "x=y\\\\\\\nz", []propertyKey{{"x", "y\\z"}}},
	} {
		got, err := loadProperties([]byte(c.raw))
		require.NoError(t, err, c.name)
		require.Equal(t, c.want, got, c.name)
	}

	for _, raw := range []string{"palbase.env.debug=\\u00", "palbase.env.debug=\\u00zz", "palbase.env.debug=\\u00e", "sdk.dir=C:\\users\\me"} {
		_, err := loadProperties([]byte(raw))
		require.EqualError(t, err, `malformed \uxxxx encoding`, raw)
	}
}

// THE VALUE A BUILD USES is trimmed as Kotlin's trim() trims — every Unicode
// space and the ASCII controls Java calls whitespace — and nothing else: a
// no-break space is trimmed (Kotlin counts it), a next-line control is not.
func TestPluginTrimIsKotlinsTrim(t *testing.T) {
	require.Equal(t, "main", pluginTrim(" \t\f\v\x1c\x1f\u00a0\u2007\u3000main\u2028\u2029 "))
	require.Equal(t, "\u0085main\u0085", pluginTrim("\u0085main\u0085"))
}

// A NAME IS NEVER A PATH, even one somebody put in the list: loopbackEnvironment
// reads a config only through a name that is one directory (envname.CheckDir)
// and that the directory listing returned exactly — `../outside` is neither,
// and a spelling that differs in case is not the second.
func TestLoopbackEnvironmentReadsNoFileThroughAPath(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"palbase/outside", "palbase/environments/onbox"} {
		file := filepath.Join(dir, filepath.FromSlash(rel), "android-config.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(`{"base_url":"http://127.0.0.1:1"}`), 0o644))
	}

	require.Empty(t, loopbackEnvironment(dir, "../outside", []string{"../outside"}))
	require.Empty(t, loopbackEnvironment(dir, "Onbox", []string{"onbox"}))
	require.Equal(t, "onbox's base_url, http://127.0.0.1:1, is this machine", loopbackEnvironment(dir, "onbox", []string{"onbox"}))
	require.Equal(t, "LOCAL is the stack on this machine", loopbackEnvironment(dir, "LOCAL", nil))
}
