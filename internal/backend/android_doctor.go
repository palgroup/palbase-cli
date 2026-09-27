package backend

// android_doctor.go — `palbase doctor`'s Android section (FR-019).
//
// An Android build picks its environment by build type, from files `palbase
// link` wrote and keys a person wrote. Neither reached doctor: it probed the
// cloud, the login, the link and the toolchains, and a keyless config, a
// missing contract or a release build with no environment surfaced as a Gradle
// failure instead. This reads what the Gradle plugin reads and says what a
// build would trip on, naming the command that ends it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/palgroup/palbase-cli/internal/envname"
)

// DoctorLine is one row `palbase doctor` prints: whether it passed, a short
// label, and a detail that names the next command when it did not.
type DoctorLine struct {
	OK     bool
	Label  string
	Detail string
}

// AndroidCheckout names the Gradle file that makes dir an Android app checkout
// — the one `palbase link` detects Android from — or "" when none does.
//
// A CONFIG FILE IS NOT THE PROOF (hasAppleProject says why): a backend
// repository can hold an android-config.json from one `link --platform
// android`, and a section about Gradle keys there would be noise.
func AndroidCheckout(dir string) string {
	file, _, err := androidApplicationFile(dir)
	if err != nil {
		return ""
	}
	return file
}

// AndroidDoctor is doctor's Android section for the Android checkout at dir.
// project says whether dir is linked to a project, which decides what brings a
// missing contract in.
func AndroidDoctor(dir string, project bool) []DoctorLine {
	return environmentDirLines(dir, project)
}

// environmentDirLines is one line per directory under palbase/environments:
// what the Gradle plugin reads from it, and what it would refuse.
func environmentDirLines(dir string, project bool) []DoctorLine {
	envRoot := path.Dir(EnvDir("any"))
	names, err := environmentDirsIn(dir)
	if err != nil {
		// A DIRECTORY THAT CANNOT BE READ IS NOT AN EMPTY ONE. Saying "nothing
		// under it" would send a person to `palbase link`, which cannot read it
		// either.
		return []DoctorLine{{
			Label: "envs",
			Detail: fmt.Sprintf("%s cannot be read (%s) — make it a directory you can read, then `palbase link` here",
				envRoot, readFailure(err)),
		}}
	}
	if len(names) == 0 {
		return []DoctorLine{{
			Label:  "envs",
			Detail: "nothing under palbase/environments — `palbase link` here writes one directory per environment",
		}}
	}
	lines := make([]DoctorLine, 0, len(names))
	for _, name := range names {
		problems := environmentProblems(dir, name, project)
		// ONE DIRECTORY ON A MAC, whatever the spelling: the rule link and the
		// sweep use (envname.SameDirectory, FR-003) — a full case fold and the
		// Unicode form, which strings.EqualFold does not see.
		for _, other := range names {
			if other != name && envname.SameDirectory(other, name) {
				problems = append(problems, twinProblem(name, other))
			}
		}
		if len(problems) == 0 {
			lines = append(lines, DoctorLine{OK: true, Label: envname.Label(name),
				Detail: "android-config.json with an api_key, openapi.json with x-palbase-roles"})
			continue
		}
		lines = append(lines, DoctorLine{Label: envname.Label(name), Detail: strings.Join(problems, "; ")})
	}
	return lines
}

// twinProblem is what the line for name says about other, a directory a disk
// that ignores letter case and Unicode form takes for the same one.
func twinProblem(name, other string) string {
	if norm.NFC.String(name) == norm.NFC.String(other) {
		// The two print alike: the difference is how an accented letter is
		// encoded, which is not a difference of case.
		return fmt.Sprintf("is %s in another Unicode form — a disk that ignores Unicode form (macOS) "+
			"holds one of the two; rename one in the dashboard", shownEnvDir(other))
	}
	// WINDOWS FOLDS CASE ONE LETTER AT A TIME, which is what strings.EqualFold
	// does: `Main` and `main` are one directory there too. A pair only the full
	// fold joins — `straße` and `STRASSE` — is one on a Mac alone.
	disks := "macOS"
	if strings.EqualFold(name, other) {
		disks = "macOS, Windows"
	}
	return fmt.Sprintf("differs only in case from %s — a disk that ignores case "+
		"(%s) holds one of the two; rename one in the dashboard", shownEnvDir(other), disks)
}

// holdsNoPalbaseFile reports whether an environment directory holds
// something, and nothing this CLI writes there — somebody's directory, not an
// environment missing its files: `palbase link` leaves it exactly as it is.
// A file browser's leftover is nobody's, and counts for neither.
func holdsNoPalbaseFile(dir, name string) bool {
	entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(EnvDir(name))))
	if err != nil {
		return false
	}
	somebodys := false
	for _, e := range entries {
		switch {
		case e.Type().IsRegular() && isFileBrowserNoise(e.Name()):
		case e.Type().IsRegular() && isGeneratedEnvironmentFile(e.Name()):
			return false
		default:
			somebodys = true
		}
	}
	return somebodys
}

// environmentDirsIn is the environment directories under dir, by their exact
// names on disk, sorted. No palbase/environments at all is no directories; one
// that cannot be read is an error, not an empty list.
func environmentDirsIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, filepath.Dir(filepath.FromSlash(EnvDir("any")))))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// environmentProblems is what the Gradle plugin would refuse in name's
// directory: the config it packages, and the contract it generates from.
func environmentProblems(dir, name string, project bool) []string {
	// NOT AN ENVIRONMENT AT ALL (final review, Minor #8): naming the two files
	// it lacks would send the reader to a link that cannot change it.
	if holdsNoPalbaseFile(dir, name) {
		return []string{"holds no Palbase files — not an environment; move it aside"}
	}
	// `local/` comes from the stack `palbase start` runs here, and from nothing
	// else: a link alone cannot fill it.
	configCure := "`palbase link` here"
	if name == localEnvName {
		configCure = "`palbase start` in the backend, then `palbase link` here"
	}
	var problems []string
	var config appEnvironment
	switch found, err := readJSONFile(filepath.Join(dir, filepath.FromSlash(ConfigPath(name, "android"))), &config); {
	case !found:
		problems = append(problems, "no android-config.json — "+configCure)
	case err != nil:
		problems = append(problems, fmt.Sprintf("android-config.json cannot be read (%s) — %s", readFailure(err), configCure))
	case config.APIKey == "":
		problems = append(problems, "android-config.json has no api_key — "+configCure)
	}

	// THE FIELD, NOT THE TEXT. The plugin reads `x-palbase-roles` as a key of
	// the document; a contract that carries the string somewhere else — an
	// example body, a schema property — still has none (Roles.kt).
	var contract map[string]json.RawMessage
	switch found, err := readJSONFile(filepath.Join(dir, filepath.FromSlash(SpecPath(name))), &contract); {
	case !found:
		problems = append(problems, "no openapi.json — "+contractCure(name, project))
	case err != nil:
		problems = append(problems, fmt.Sprintf("openapi.json cannot be read (%s) — %s", readFailure(err), contractCure(name, project)))
	case contract["x-palbase-roles"] == nil:
		problems = append(problems, "openapi.json carries no x-palbase-roles, which the Gradle plugin refuses — "+
			"upgrade @palbase/backend, then "+contractCure(name, project))
	}
	return problems
}

// readJSONFile decodes the file at path into v; found is false when there is
// no such file.
func readJSONFile(path string, v any) (found bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return true, json.Unmarshal(raw, v)
}

// readFailure is how a doctor line says why something could not be read.
//
// THE REASON WITHOUT THE PATH. The system's error carries the absolute path it
// failed on, and that path holds an environment directory's name as it is on
// disk — somebody else's text, which the line has already named through
// envname.Label. And every rune that does not print is written out as an
// escape, so neither a name nor a file's content reaches the terminal raw.
func readFailure(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	var b strings.Builder
	for _, r := range err.Error() {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			continue
		}
		quoted := strconv.QuoteRuneToASCII(r)
		b.WriteString(quoted[1 : len(quoted)-1])
	}
	return b.String()
}
