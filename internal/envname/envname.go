// Package envname is the one rule for what an environment's NAME may be.
//
// THE NAME IS SOMEBODY ELSE'S TEXT. Until the control plane stores slugs, any
// member of an organisation can name an environment with any 1–64 characters,
// and every teammate's `palbase link` turns that name into a directory:
// `palbase/environments/<name>/`. A name that is not one directory is a path
// the CLI was never meant to write — measured: `../../gradle` wrote into the
// checkout's own gradle/, `..` wrote the retired layout's marker so every later
// link refused the checkout, and a longer run of `../` wrote outside the
// checkout altogether.
//
// A LEAF PACKAGE, so every command that reads or prints a name can ask the same
// question without importing the link.
package envname

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// CheckDir says why name cannot be ONE directory under palbase/environments,
// or nil when it can.
//
// THIS IS THE LOOSE RULE, AND DELIBERATELY SO (D-011). Names the control plane
// lists today — `Production`, `featureX`, a name with a space — are each one
// directory and keep working; the boundary is the path, not a style. Every
// rune must be printable (unicode.IsPrint), which is wider than the control
// characters alone: a C1 byte or a bidi override in a directory name reaches
// the terminal in every "wrote …" line after it.
//
// ONE DIRECTORY ON EVERY TEAMMATE'S DISK, Windows included — this CLI ships for
// it (.goreleaser.yml). Windows drops a trailing dot or space, so `main.` is
// the directory `main` there and its config lands over main's; `CON` or
// `nul.json` is a device, not a directory; and `<>:"|?*` cannot be in a name at
// all.
//
// The error completes the sentence "the name …".
func CheckDir(name string) error {
	switch {
	case name == "":
		return errors.New("is empty")
	case name == "." || name == "..":
		return errors.New(`is "." or ".."`)
	case strings.ContainsAny(name, `/\`):
		return errors.New("contains a path separator")
	case strings.HasPrefix(name, "."):
		return errors.New("starts with a dot")
	case !utf8.ValidString(name):
		return errors.New("is not UTF-8")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("contains the non-printing character %U", r)
		}
	}
	switch {
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return errors.New("ends with a dot or a space, which Windows drops")
	case strings.ContainsAny(name, windowsForbidden):
		return fmt.Errorf("contains a character Windows does not allow in a directory name (%s)", windowsForbidden)
	case windowsDevice(name):
		return errors.New("is a device name on Windows")
	}
	return nil
}

// SameDirectory says whether a and b are one directory on a disk that ignores
// letter case and Unicode normalisation — APFS, every Mac's. `Staging` and
// `staging` are, and so are `café` written with one code point and with two:
// two environments named so wrote into one directory, and the app built one
// environment's address under the other's name (FR-003).
//
// THE FOLD IS FULL, NOT SIMPLE — measured on this Mac's APFS (`mkdir a; test
// -d b`): `straße` and `STRASSE` are one directory there (ß folds to "ss"),
// and so are `ﬁle` and `file` (the ﬁ ligature folds to "fi"). strings.EqualFold
// only does simple case folding and gets both wrong. `İzmir` and `izmir` stay
// different, which APFS agrees with — İ folds to "i" plus a combining dot, not
// to plain "i".
func SameDirectory(a, b string) bool {
	return sameDirectoryKey(a) == sameDirectoryKey(b)
}

// sameDirectoryKey is APFS's own comparison key: decompose (NFD), fold every
// letter's case in full, then recompose (NFC) so the encoding does not matter
// either.
func sameDirectoryKey(s string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFD.String(s)))
}

// windowsForbidden are the characters a Windows directory name cannot hold,
// beside the separators and the control characters refused before them.
const windowsForbidden = `<>:"|?*`

// windowsDevice says whether Windows opens name as a device, not a directory:
// CON, PRN, AUX, NUL, CONIN$, CONOUT$, and COM or LPT with a digit 1–9 or a
// superscript ¹²³ — in any case, and with anything after a dot (`nul.json` is
// NUL). The list is the one Go's own filepath.IsLocal refuses on Windows.
func windowsDevice(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.TrimRight(base, " ")
	for _, device := range []string{"CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$"} {
		if strings.EqualFold(base, device) {
			return true
		}
	}
	if len(base) < 4 || (!strings.EqualFold(base[:3], "COM") && !strings.EqualFold(base[:3], "LPT")) {
		return false
	}
	switch base[3:] {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}
