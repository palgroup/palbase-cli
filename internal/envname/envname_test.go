package envname

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// THE NAMES PEOPLE ALREADY USE STAY LEGAL (D-011). This gate is the security
// boundary, not a style rule: `Production`, `featureX` and a name with a space
// in it are each one directory, and they must keep linking until the control
// plane stores slugs.
func TestCheckDirAcceptsEveryNameThatIsOneDirectory(t *testing.T) {
	for _, name := range []string{"main", "local", "Production", "featureX", "feature-profile-update", "Üretim ortamı", "v1.2"} {
		require.NoError(t, CheckDir(name), "%q", name)
	}
}

// EVERY NAME THAT IS NOT EXACTLY ONE DIRECTORY IS REFUSED, and the reason
// completes the sentence "the name …" that the link prints beside it.
func TestCheckDirRefusesEveryNameThatIsNotOneDirectory(t *testing.T) {
	for name, why := range map[string]string{
		"":                       "is empty",
		".":                      `is "." or ".."`,
		"..":                     `is "." or ".."`,
		"../../gradle":           "contains a path separator",
		"feature/login":          "contains a path separator",
		`..\..\gradle`:           "contains a path separator",
		".hidden":                "starts with a dot",
		"a\x00b":                 "contains the non-printing character U+0000",
		"evil\x1b]0;owned\a":     "contains the non-printing character U+001B",
		"tab\there":              "contains the non-printing character U+0009",
		"del\x7f":                "contains the non-printing character U+007F",
		"csi\xc2\x9b31m":         "contains the non-printing character U+009B",
		"rtl\xe2\x80\xaeexe.txt": "contains the non-printing character U+202E",
		"csi\x9b31m":             "is not UTF-8",
	} {
		err := CheckDir(name)
		require.Error(t, err, "%q was accepted", name)
		require.Equal(t, why, err.Error(), "%q", name)
	}
}

// A NAME WINDOWS WOULD KEEP AS ANOTHER ONE IS NOT ONE DIRECTORY EITHER. This
// CLI ships for Windows (.goreleaser.yml): there `main.` and `main ` are the
// directory `main` — a member's environment written over main's config — and
// CON or NUL is a device, not a directory.
func TestCheckDirRefusesWhatWindowsCannotKeepAsOneDirectory(t *testing.T) {
	for name, why := range map[string]string{
		"main.":     "ends with a dot or a space, which Windows drops",
		"main ":     "ends with a dot or a space, which Windows drops",
		"CON":       "is a device name on Windows",
		"nul.json":  "is a device name on Windows",
		"com1":      "is a device name on Windows",
		"LPT9.log":  "is a device name on Windows",
		"COM²":      "is a device name on Windows",
		"conout$":   "is a device name on Windows",
		"feature:x": `contains a character Windows does not allow in a directory name (<>:"|?*)`,
		"a*b":       `contains a character Windows does not allow in a directory name (<>:"|?*)`,
	} {
		err := CheckDir(name)
		require.Error(t, err, "%q was accepted", name)
		require.Equal(t, why, err.Error(), "%q", name)
	}
	for _, name := range []string{"Production", "Üretim ortamı", "v1.2", "console", "nullable", "com10"} {
		require.NoError(t, CheckDir(name), "%q", name)
	}
}

// ONE DIRECTORY ON A MAC IS ONE NAME HERE. APFS ignores letter case and Unicode
// normalisation alike, so `Staging` and `staging` — and `café` composed and
// decomposed — are one directory, and two environments named so wrote into
// one (FR-003). APFS also uses FULL case folding, not simple: `straße` and
// `STRASSE` are one directory there (ß folds to "ss"), and so are `ﬁle`
// and `file` (the ﬁ ligature folds to "fi") — measured on this Mac's APFS
// (`mkdir a; test -d b`). `İzmir` and `izmir` stay DIFFERENT, which APFS
// agrees with (İ folds to "i" plus a combining dot, not to plain "i").
func TestSameDirectoryIsWhatAMacTakesForOneName(t *testing.T) {
	const composed, decomposed = "caf\u00e9", "cafe\u0301"
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"Staging", "staging", true},
		{composed, decomposed, true},
		{"CAF\u00c9", decomposed, true},
		{"main", "main", true},
		{"main", "main2", false},
		{"feature-x", "featurex", false},
		{"stra\u00dfe", "STRASSE", true},
		{"\uFB01le", "file", true},
		{"\u0130zmir", "izmir", false},
	} {
		require.Equal(t, c.same, SameDirectory(c.a, c.b), "%q and %q", c.a, c.b)
	}
}

// A NAME PRINTS AS IT IS WHEN IT IS ONE PLAIN WORD — every slug is — and
// quoted and escaped otherwise, so no byte a member typed into a name reaches
// a teammate's terminal raw (FR-006).
func TestLabelQuotesEveryNameThatIsNotOnePlainWord(t *testing.T) {
	for name, want := range map[string]string{
		"main":                   "main",
		"Production":             "Production",
		"feature-profile-update": "feature-profile-update",
		"staging_2":              "staging_2",
		"Feature X":              `"Feature X"`,
		"evil\x1b]0;owned\a":     `"evil\x1b]0;owned\a"`,
		"tab\there":              `"tab\there"`,
		"":                       `""`,
	} {
		require.Equal(t, want, Label(name), "%q", name)
	}
}

// A NAME INSIDE A SUGGESTED COMMAND MUST BE SAFE FOR THE SHELL, NOT JUST FOR
// THE EYE (fix round 1, security-relevant). Label's %q is Go's quoting, not a
// shell's: CheckDir admits $, `, (, ), and ; and %q leaves every one of them
// alone, so `palbase push --env "x$(id)"` pasted into sh/zsh RUNS `id`. POSIX
// single-quoting has exactly one escape — close the quote, an escaped quote,
// reopen it — and that is enough on its own: nothing else is special inside
// single quotes.
func TestShellWordQuotesEveryNameThatIsNotOnePlainWord(t *testing.T) {
	for name, want := range map[string]string{
		"main":                   "main",
		"feature-profile-update": "feature-profile-update",
		"staging_2":              "staging_2",
		"x$(id)":                 `'x$(id)'`,
		"it's":                   `'it'\''s'`,
		"Feature X":              `'Feature X'`,
	} {
		require.Equal(t, want, ShellWord(name), "%q", name)
	}
}

// A CONTROL BYTE MUST NOT SURVIVE SHELLWORD EITHER, NOT JUST THE SHELL'S
// METACHARACTERS (T008 review, security-relevant). Single-quoting protects
// the SHELL a pasted command might run in; it does nothing for the TERMINAL
// this line is printed to right now — an OSC sequence or a bell inside a name
// rewrites the terminal or rings it the instant the line is shown, quotes or
// no quotes. So every rune that fails unicode.IsPrint must come out as its
// own visible Go-style escape, as literal text inside the quotes.
func TestShellWordEscapesEveryNonPrintingRuneEvenInsideTheQuotes(t *testing.T) {
	got := ShellWord("evil\x1b]0;owned\a")
	require.NotContains(t, got, "\x1b", "a raw ESC byte survived: %q", got)
	require.NotContains(t, got, "\a", "a raw BEL byte survived: %q", got)
	require.True(t, len(got) >= 2 && got[0] == '\'' && got[len(got)-1] == '\'',
		"does not start and end with a quote: %q", got)
	require.Contains(t, got, `\x1b`, "the escape byte is not shown as text: %q", got)
}
