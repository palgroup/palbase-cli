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
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

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
	names, err := environmentDirsIn(dir)
	if err != nil {
		// A DIRECTORY THAT CANNOT BE READ IS NOT AN EMPTY ONE. Saying "nothing
		// under it" would send a person to `palbase link`, which cannot read it
		// either — and every key below would be judged against a list of
		// directories nobody could read, so none is.
		return []DoctorLine{{
			Label: "envs",
			Detail: fmt.Sprintf("%s cannot be read (%s) — make it a directory you can read, then `palbase link` here",
				path.Dir(EnvDir("any")), readFailure(err)),
		}}
	}
	buildFile := AndroidCheckout(dir)
	gradleRoot := gradleRootOf(buildFile)
	lines := environmentDirLines(dir, names, project)
	lines = append(lines, environmentKeyLines(dir, gradleRoot, names)...)
	return append(lines, moduleKeyLines(dir, path.Dir(buildFile), gradleRoot)...)
}

// gradleRootOf is the Gradle root of the build whose file made the checkout an
// Android one, relative to the checkout: `android/` in a React Native or
// Flutter checkout (FR-015), the checkout itself otherwise. The plugin reads
// gradle.properties and local.properties from there.
func gradleRootOf(buildFile string) string {
	if strings.HasPrefix(buildFile, "android/") {
		return "android"
	}
	return "."
}

// environmentDirLines is one line per directory under palbase/environments —
// names, as environmentDirsIn read them: what the Gradle plugin reads from it,
// and what it would refuse.
func environmentDirLines(dir string, names []string, project bool) []DoctorLine {
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

// readJSONFile decodes file into v; found is false when there is no such file.
func readJSONFile(file string, v any) (found bool, err error) {
	raw, err := os.ReadFile(file)
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

// environmentKeyLines is one line per palbase.env key in the Gradle root's
// gradle.properties, then its local.properties — the two files a person writes
// a choice in — with the environment each names and whether a build can use it.
func environmentKeyLines(dir, gradleRoot string, names []string) []DoctorLine {
	var lines []DoctorLine
	for _, file := range []string{"gradle.properties", "local.properties"} {
		shown := path.Join(gradleRoot, file)
		keys, err := palbaseEnvKeysIn(filepath.Join(dir, filepath.FromSlash(shown)))
		if err != nil {
			lines = append(lines, unreadableKeysLine(shown, err))
			continue
		}
		for _, k := range keys {
			lines = append(lines, environmentKeyLine(k, shown, file == "local.properties", names))
		}
	}
	return lines
}

// environmentKeyLine says what one key picks, in the plugin's own words for
// where a choice came from (`palbase.env.debug in local.properties`).
func environmentKeyLine(k propertyKey, file string, personal bool, names []string) DoctorLine {
	label, head := keyLabel(k.key), keyHead(k, file)
	switch {
	case personal && k.key == envKeyPrefix:
		// Plugin 2.5.0 reads local.properties for palbase.env.<variant, flavor
		// or build type> alone, and says nothing about this line.
		return DoctorLine{Label: label, Detail: head + " — ignored: local.properties carries palbase.env.<build type> keys, not the global palbase.env"}
	case personal && releaseKey(k.key):
		// D-009: a forgotten `palbase.env.release=local` here built a
		// loopback, cleartext release APK, green.
		return DoctorLine{Label: label, Detail: head + " — ignored: local.properties reaches only debuggable builds, " +
			"and a release build is not one; put it in gradle.properties"}
	}
	// FIRST THE MISREAD, THEN THE REFUSAL IT CAUSES: `Übung` pasted in UTF-8
	// reads as `Ã` and a C1 control, which the plugin refuses as a control
	// character — true, and no help to the person who typed Übung.
	if typed := utf8Reading(k.value); typed != "" && slices.Contains(names, typed) {
		return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — %s is read as ISO-8859-1, so %s written in UTF-8 reads as "+
			"this; write it as %s=%s", head, file, envname.Label(typed), shownKey(k.key), envname.PropertiesValue(typed))}
	}
	if problem := refusedName(k.value); problem != "" {
		// No `palbase link` writes such a directory, so none is sent there.
		return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — a build refuses it: the name %s, and an environment "+
			"is one directory under palbase/environments", head, problem)}
	}
	if slices.Contains(names, k.value) {
		return DoctorLine{OK: true, Label: label, Detail: head}
	}
	for _, name := range names {
		if strings.EqualFold(name, k.value) {
			return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — %s differs only in case, and a build finds its "+
				"directory by the exact name: %s=%s", head, shownEnvDir(name), shownKey(k.key), envname.PropertiesValue(name))}
		}
	}
	cure := "`palbase link` here writes one directory per environment of the project"
	if k.value == localEnvName {
		cure = "`palbase start` in the backend, then `palbase link` here"
	}
	return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — no %s here; %s", head, shownEnvDir(k.value), cure)}
}

// moduleKeyLines is one line per palbase.env key in the app module's own
// gradle.properties — module, relative to the checkout — which the plugin
// refuses rather than ignore in silence (FR-203): it reads the root file every
// module shares, and every build of a module whose own file holds such a line
// fails. A module that is the Gradle root has no file of its own.
func moduleKeyLines(dir, module, gradleRoot string) []DoctorLine {
	if module == gradleRoot {
		return nil
	}
	shown := path.Join(module, "gradle.properties")
	keys, err := palbaseEnvKeysIn(filepath.Join(dir, filepath.FromSlash(shown)))
	if err != nil {
		return []DoctorLine{unreadableKeysLine(shown, err)}
	}
	var lines []DoctorLine
	for _, k := range keys {
		lines = append(lines, DoctorLine{Label: keyLabel(k.key), Detail: fmt.Sprintf("%s — every build of %s/ is refused "+
			"while it holds this line: a module's own gradle.properties never chooses an environment; move it to %s",
			keyHead(k, shown), module, path.Join(gradleRoot, "gradle.properties"))})
	}
	return lines
}

// unreadableKeysLine says that file — a .properties file the build reads —
// could not be loaded: Gradle loads it with the same reader, and stops there.
func unreadableKeysLine(file string, err error) DoctorLine {
	return DoctorLine{Label: "keys", Detail: fmt.Sprintf("%s cannot be read (%s), and a Gradle build stops on it too — "+
		"fix the file", file, readFailure(err))}
}

// keyLabel is a key's label: the build type, flavor or variant it is for, and
// `any` for the global palbase.env.
func keyLabel(key string) string {
	if key == envKeyPrefix {
		return "any"
	}
	return envname.Label(strings.TrimPrefix(key, envKeyPrefix+"."))
}

// keyHead is how a line opens: the environment the key names, and where it
// came from.
func keyHead(k propertyKey, file string) string {
	return fmt.Sprintf("→ %s (%s in %s)", envname.Label(k.value), shownKey(k.key), file)
}

// shownKey is a key as a line prints it. A key is the file's text as much as a
// value is, and \uXXXX puts any character in it: its suffix goes through
// envname.Label.
func shownKey(key string) string {
	suffix, ok := strings.CutPrefix(key, envKeyPrefix+".")
	if !ok {
		return key
	}
	return envKeyPrefix + "." + envname.Label(suffix)
}

// envKeyPrefix is the Gradle property an environment is chosen with: bare, the
// global of plugin 2.3, or suffixed with a build type, flavor or variant.
const envKeyPrefix = "palbase.env"

// releaseKey reports whether key picks the environment of a release build — the
// release build type itself, or a flavored variant of it (`freeRelease`).
func releaseKey(key string) bool {
	suffix, ok := strings.CutPrefix(key, envKeyPrefix+".")
	return ok && (suffix == "release" || strings.HasSuffix(suffix, "Release"))
}

// refusedName says why the Gradle plugin refuses name as an environment before
// it looks for a directory — its one-segment rule (EnvironmentResolver
// .requireOneSegment, 2.5.0) — or "" when it does not. The words complete "the
// name …".
func refusedName(name string) string {
	switch {
	case name == "":
		return "is empty"
	case strings.ContainsAny(name, `/\`):
		return "contains a path separator"
	case strings.HasPrefix(name, "."):
		return "starts with a dot"
	}
	for _, r := range name {
		// Java's Character.isISOControl: C0, DEL and C1.
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return "contains a control character"
		}
	}
	return ""
}

// utf8Reading is the name value was typed as, when a file holding it in UTF-8
// was read as ISO-8859-1 — one letter per byte, as Gradle and the plugin read
// it — or "" when value reads the same either way.
func utf8Reading(value string) string {
	raw := make([]byte, 0, len(value))
	for _, r := range value {
		if r > 0xff {
			return ""
		}
		raw = append(raw, byte(r))
	}
	if !utf8.Valid(raw) || string(raw) == value {
		return ""
	}
	return string(raw)
}

// propertyKey is one key of a .properties file and the value it holds.
type propertyKey struct{ key, value string }

// palbaseEnvKeysIn is every palbase.env key in the .properties file, in file
// order, each with the value a build uses: the file read as Gradle and the
// plugin read it (loadProperties), a key written twice holding its last value,
// and the value trimmed as the plugin trims it (pluginTrim) — `main ` selects
// main. No file is no keys; one that cannot be loaded is an error.
func palbaseEnvKeysIn(file string) ([]propertyKey, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	props, err := loadProperties(raw)
	if err != nil {
		return nil, err
	}
	var keys []propertyKey
	for _, p := range props {
		if p.key == envKeyPrefix || strings.HasPrefix(p.key, envKeyPrefix+".") {
			keys = append(keys, propertyKey{key: p.key, value: pluginTrim(p.value)})
		}
	}
	return keys, nil
}

// pluginTrim is the value the plugin uses: it trims every value it reads with
// Kotlin's trim(), which drops every Unicode space separator and the ASCII
// controls Java calls whitespace from both ends — a no-break space included,
// a next-line control not.
func pluginTrim(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f:
			return true
		}
		return unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp)
	})
}

// errMalformedEscape is Java's refusal of a `\u` not followed by four hex
// digits: Properties.load throws, and so does every build that loads the file.
var errMalformedEscape = errors.New(`malformed \uxxxx encoding`)

// loadProperties is java.util.Properties.load(InputStream), the reader Gradle,
// AGP and the plugin load these files with: every byte one ISO-8859-1 letter;
// a line ending in an odd number of backslashes continued on the next, whose
// leading blanks go; `#` and `!` comment lines, never continued; the key ending
// at the first unescaped `=`, `:` or blank; \uXXXX and \t \n \r \f escapes,
// any other escaped letter standing for itself. The properties are in the order
// their keys first appear; a key written again holds its last value.
func loadProperties(raw []byte) ([]propertyKey, error) {
	var props []propertyKey
	at := map[string]int{}
	for _, line := range logicalLines(raw) {
		keyEnd, valueStart, separated, escaped := len(line), len(line), false, false
		for i, c := range line {
			if !escaped && (c == '=' || c == ':') {
				keyEnd, valueStart, separated = i, i+1, true
				break
			}
			if !escaped && (c == ' ' || c == '\t' || c == '\f') {
				keyEnd, valueStart = i, i+1
				break
			}
			escaped = c == '\\' && !escaped
		}
		for ; valueStart < len(line); valueStart++ {
			c := line[valueStart]
			if c == ' ' || c == '\t' || c == '\f' {
				continue
			}
			if !separated && (c == '=' || c == ':') {
				separated = true
				continue
			}
			break
		}
		key, err := unescapeProperties(line[:keyEnd])
		if err != nil {
			return nil, err
		}
		value, err := unescapeProperties(line[valueStart:])
		if err != nil {
			return nil, err
		}
		if i, seen := at[key]; seen {
			props[i].value = value
			continue
		}
		at[key] = len(props)
		props = append(props, propertyKey{key: key, value: value})
	}
	return props, nil
}

// logicalLines is Properties' LineReader: the file's logical lines, blank and
// comment lines dropped, continuations joined, escapes still in place.
func logicalLines(raw []byte) [][]byte {
	var lines [][]byte
	var line []byte
	skipBlanks, continued, escaped := true, false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if skipBlanks {
			if c == ' ' || c == '\t' || c == '\f' || (!continued && (c == '\r' || c == '\n')) {
				continue
			}
			skipBlanks, continued = false, false
		}
		if len(line) == 0 && (c == '#' || c == '!') {
			for i+1 < len(raw) && raw[i+1] != '\r' && raw[i+1] != '\n' {
				i++
			}
			skipBlanks = true
			continue
		}
		if c != '\r' && c != '\n' {
			line = append(line, c)
			escaped = c == '\\' && !escaped
			continue
		}
		if len(line) == 0 {
			skipBlanks = true
			continue
		}
		if i+1 == len(raw) {
			break // the end of the file ends the line, as below
		}
		if escaped {
			// The backslash is not part of the line; the next one's leading
			// blanks are skipped, and a CR's LF goes with it.
			line = line[:len(line)-1]
			skipBlanks, continued, escaped = true, true, false
			if c == '\r' && raw[i+1] == '\n' {
				i++
			}
			continue
		}
		lines = append(lines, line)
		line, skipBlanks = nil, true
	}
	if len(line) > 0 {
		if escaped {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
	}
	return lines
}

// unescapeProperties is Properties' loadConvert on one key or value: ISO-8859-1
// letters and escapes into the text they stand for, in UTF-16 units as Java
// holds them, so two \u escapes make one character above U+FFFF.
func unescapeProperties(in []byte) (string, error) {
	units := make([]uint16, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c != '\\' || i+1 == len(in) {
			units = append(units, uint16(c))
			continue
		}
		i++
		switch c = in[i]; c {
		case 'u':
			if i+4 >= len(in) {
				return "", errMalformedEscape
			}
			unit, err := strconv.ParseUint(string(in[i+1:i+5]), 16, 16)
			if err != nil {
				return "", errMalformedEscape
			}
			units = append(units, uint16(unit))
			i += 4
		case 't':
			units = append(units, '\t')
		case 'n':
			units = append(units, '\n')
		case 'r':
			units = append(units, '\r')
		case 'f':
			units = append(units, '\f')
		default:
			units = append(units, uint16(c))
		}
	}
	return string(utf16.Decode(units)), nil
}
