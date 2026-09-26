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
