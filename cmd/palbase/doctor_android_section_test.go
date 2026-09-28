package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// AN ANDROID CHECKOUT GETS ITS OWN SECTION (FR-019). Doctor probed the cloud,
// the login, the link, Docker, Node and Bun, and nothing an Android build reads:
// a missing contract, a keyless config, a release build with no environment
// all surfaced as a Gradle failure instead.
//
// (Not `doctor_android_test.go`: a `_android` suffix makes Go compile the file
// for GOOS=android only, and its tests would silently not exist here.)

// writeFileIn writes body at the slash path rel under dir.
func writeFileIn(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// androidCheckoutIn makes dir an Android app checkout: the Gradle file `palbase
// link` detects Android from.
func androidCheckoutIn(t *testing.T, dir string) {
	t.Helper()
	writeFileIn(t, dir, "app/build.gradle.kts", "android {\n    defaultConfig {\n        applicationId = \"com.example.todo\"\n    }\n}\n")
}

// Contracts as the stack serves them: with the roles inside, and from before
// they were.
const (
	withRoles    = `{"openapi":"3.1.0","paths":{},"x-palbase-roles":{"roles":[]}}`
	withoutRoles = `{"openapi":"3.1.0","paths":{}}`
)

func androidConfig(baseURL, key string) string {
	return fmt.Sprintf(`{"app_id":"prj_1","base_url":%q,"api_key":%q}`, baseURL, key)
}

// androidEnvironmentIn writes an environment's two files where `palbase link`
// does; an empty body leaves that file out.
func androidEnvironmentIn(t *testing.T, dir, env, config, contract string) {
	t.Helper()
	if config != "" {
		writeFileIn(t, dir, "palbase/environments/"+env+"/android-config.json", config)
	}
	if contract != "" {
		writeFileIn(t, dir, "palbase/environments/"+env+"/openapi.json", contract)
	}
}

func linkedToAProject(t *testing.T, dir string) {
	t.Helper()
	writeFileIn(t, dir, "palbase/project.json", `{"project":"prd_a","name":"todoapp"}`+"\n")
}

// EVERY ENVIRONMENT DIRECTORY IS CHECKED for what the Gradle plugin reads: both
// files, a key in the config, the roles inside the contract. Each problem names
// what ends it.
func TestDoctorChecksEachAndroidEnvironment(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "staging", androidConfig("https://staging.example", "pb_staging_cK"), "")
	androidEnvironmentIn(t, dir, "featureX", androidConfig("https://featurex.example", ""), withoutRoles)
	androidEnvironmentIn(t, dir, "local", "", withRoles)
	androidEnvironmentIn(t, dir, "broken", "{", withRoles)

	require.Contains(t, runDoctorIn(t, dir), "android (app/build.gradle.kts)\n"+
		"  ✗ broken     android-config.json cannot be read (unexpected end of JSON input) — `palbase link` here\n"+
		"  ✗ featureX   android-config.json has no api_key — `palbase link` here; "+
		"openapi.json carries no x-palbase-roles, which the Gradle plugin refuses — upgrade @palbase/backend, then `palbase push`, then `palbase link` here\n"+
		"  ✗ local      no android-config.json — `palbase start` in the backend, then `palbase link` here\n"+
		"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n"+
		"  ✗ staging    no openapi.json — `palbase push`, then `palbase link` here\n")
}

// A CHECKOUT LINKED TO A PROJECT PUSHES TO ONE OF ITS ENVIRONMENTS, so the
// cure names it — the same sentence `palbase link` prints (FR-014).
func TestDoctorNamesTheEnvironmentAContractIsPushedTo(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	linkedToAProject(t, dir)
	androidEnvironmentIn(t, dir, "staging", androidConfig("https://staging.example", "pb_staging_cK"), "")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ staging    no openapi.json — `palbase push --env staging`, then `palbase link` here\n")
}

func TestDoctorSaysAnAndroidCheckoutHoldsNoEnvironmentYet(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)

	require.Contains(t, runDoctorIn(t, dir), "android (app/build.gradle.kts)\n"+
		"  ✗ envs       nothing under palbase/environments — `palbase link` here writes one directory per environment\n")
}

// REACT NATIVE AND FLUTTER keep the Gradle build under android/ (FR-015): the
// section is there too, named after the file that made it.
func TestDoctorFindsTheAndroidAppOfACrossPlatformCheckout(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, dir, "android/app/build.gradle", "android {\n    defaultConfig {\n        applicationId \"com.example.todo\"\n    }\n}\n")
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)

	require.Contains(t, runDoctorIn(t, dir), "android (android/app/build.gradle)\n"+
		"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n")
}

// NOT AN ANDROID CHECKOUT, NO SECTION — a config file is not the proof: a
// backend repository can hold one from a `link --platform android` run once.
func TestDoctorPrintsNoAndroidSectionWithoutAnAndroidApp(t *testing.T) {
	dir := t.TempDir()
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)

	require.NotContains(t, runDoctorIn(t, dir), "android (")
}

// TWO DIRECTORIES ONE DISK CANNOT TELL APART are named: only a case-sensitive
// disk can hold both, and a clone on macOS or Windows gets one of the two.
func TestDoctorNamesEnvironmentDirectoriesThatDifferOnlyInCase(t *testing.T) {
	dir := t.TempDir()
	if caseInsensitive(t, dir) {
		t.Skip("this disk ignores case, so it cannot hold two directories whose names differ only in case")
	}
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "Main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ Main       differs only in case from palbase/environments/main — a disk that ignores case (macOS, Windows) holds one of the two; rename one in the dashboard\n"+
			"  ✗ main       differs only in case from palbase/environments/Main — a disk that ignores case (macOS, Windows) holds one of the two; rename one in the dashboard\n")
}

// ONE DIRECTORY ON A MAC IS DECIDED BY THE LINK'S RULE (envname.SameDirectory,
// FR-003): a full case fold, which strings.EqualFold is not — `straße` and
// `STRASSE` are one directory on APFS, and a simple fold calls them two.
//
// AND ONLY ON A MAC (final review, Minor #5): Windows folds case one letter at
// a time, and keeps `straße` and `STRASSE` apart.
func TestDoctorNamesEnvironmentDirectoriesOnlyAFullCaseFoldMakesOne(t *testing.T) {
	dir := t.TempDir()
	if caseInsensitive(t, dir) {
		t.Skip("this disk ignores case, so it cannot hold two directories whose names differ only in case")
	}
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "straße", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "STRASSE", androidConfig("https://main.example", "pb_main_cK"), withRoles)

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ STRASSE    differs only in case from palbase/environments/\"straße\" — a disk that ignores case (macOS) holds one of the two; rename one in the dashboard\n"+
			"  ✗ \"straße\"   differs only in case from palbase/environments/STRASSE — a disk that ignores case (macOS) holds one of the two; rename one in the dashboard\n")
}

// TWO UNICODE FORMS OF ONE NAME ARE ONE DIRECTORY ON A MAC (final review, Minor
// #5): `café` with one code point and with two print alike, and the line says
// what differs. Every APFS volume ignores Unicode form, case-sensitive ones
// too, so only a disk that does not — Linux CI's — can hold the pair.
func TestDoctorNamesEnvironmentDirectoriesInTwoUnicodeForms(t *testing.T) {
	const composed, decomposed = "caf\u00e9", "cafe\u0301"
	dir := t.TempDir()
	if !holdsBoth(t, dir, composed, decomposed) {
		t.Skip("this disk ignores Unicode form, so it cannot hold two directories whose names differ only in it")
	}
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, composed, androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, decomposed, androidConfig("https://main.example", "pb_main_cK"), withRoles)

	out := runDoctorIn(t, dir)
	for name, other := range map[string]string{composed: decomposed, decomposed: composed} {
		require.Contains(t, out, "\""+name+"\"", out)
		require.Contains(t, out, " is palbase/environments/\""+other+"\" in another Unicode form — a disk that ignores "+
			"Unicode form (macOS) holds one of the two; rename one in the dashboard\n", out)
	}
	require.NotContains(t, out, "differs only in case")
}

// A DIRECTORY HOLDING NO PALBASE FILE IS NOT AN ENVIRONMENT (final review,
// Minor #8). It was reported as one missing both files, with `palbase link`
// as the cure — and a link leaves it exactly as it is, because it holds
// somebody's files.
func TestDoctorSaysADirectoryWithNoPalbaseFileIsNotAnEnvironment(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "palbase/environments/mine/NOTES.md", "mine\n")
	writeFileIn(t, dir, "palbase/environments/mine/.DS_Store", "noise")

	out := runDoctorIn(t, dir)
	require.Contains(t, out, "android (app/build.gradle.kts)\n"+
		"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n"+
		"  ✗ mine       holds no Palbase files — not an environment; move it aside\n")
	require.NotContains(t, out, "✗ mine       no android-config.json")
}

// AND ONE HOLDING NOTHING BUT A FILE BROWSER'S LEFTOVER IS AN ENVIRONMENT
// WITHOUT ITS FILES: `.DS_Store` is nobody's, so the cure is still the link.
func TestDoctorTakesADirectoryHoldingOnlyAFileBrowsersLeftoverForAnEmptyOne(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	writeFileIn(t, dir, "palbase/environments/staging/.DS_Store", "noise")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ staging    no android-config.json — `palbase link` here; no openapi.json — `palbase push`, then `palbase link` here\n")
}

// A DIRECTORY THAT CANNOT BE READ IS NOT AN EMPTY ONE: "nothing under it" would
// send a person to `palbase link`, which cannot read it either.
func TestDoctorSaysWhenTheEnvironmentsDirectoryCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	writeFileIn(t, dir, "palbase/environments", "not a directory\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out, "android (app/build.gradle.kts)\n"+
		"  ✗ envs       palbase/environments cannot be read (")
	require.Contains(t, out, ") — make it a directory you can read, then `palbase link` here\n")
	require.NotContains(t, out, "nothing under palbase/environments")
	require.NotContains(t, out, dir, "the reason is the system's, without the absolute path it carries")
}

// A CHECKOUT LINKED BY ADDRESS HAS NO ENVIRONMENT TO NAME in `push --env`: its
// record reads back without an error, and still names no project (FR-014).
func TestDoctorNamesNoEnvironmentForACheckoutLinkedByAddress(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	writeFileIn(t, dir, "palbase/project.json", `{"url":"https://stack.example"}`+"\n")
	androidEnvironmentIn(t, dir, "staging", androidConfig("https://staging.example", "pb_staging_cK"), "")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ staging    no openapi.json — `palbase push`, then `palbase link` here\n")
}

// NOTHING FROM DISK REACHES THE TERMINAL RAW. A directory's name is somebody
// else's text, and the system's error for a file inside it carries that name in
// its path: an escape byte there rewrote the line it was printed on (FR-006).
func TestDoctorPrintsNoControlCharacterFromAnEnvironmentDirectory(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	env := "evil\x1b[2J"
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "palbase", "environments", env, "android-config.json"), 0o755))
	androidEnvironmentIn(t, dir, env, "", withRoles)

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ \"evil\\x1b[2J\" android-config.json cannot be read (is a directory) — `palbase link` here\n")
	require.NotContains(t, out, "\x1b")
}

// holdsBoth reports whether the disk under dir keeps directories named a and b
// apart.
func holdsBoth(t *testing.T, dir, a, b string) bool {
	t.Helper()
	probe := filepath.Join(dir, "formprobe")
	require.NoError(t, os.Mkdir(probe, 0o755))
	defer func() { require.NoError(t, os.RemoveAll(probe)) }()
	require.NoError(t, os.Mkdir(filepath.Join(probe, a), 0o755))
	return os.Mkdir(filepath.Join(probe, b), 0o755) == nil
}

// caseInsensitive reports whether the disk under dir ignores letter case.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "caseprobe")
	require.NoError(t, os.Mkdir(probe, 0o755))
	defer func() { require.NoError(t, os.Remove(probe)) }()
	_, err := os.Stat(filepath.Join(dir, "CASEPROBE"))
	return err == nil
}

// THE KEYS THAT PICK AN ENVIRONMENT are listed where the Gradle plugin reads
// them — the root gradle.properties, then local.properties — each with the
// environment it names, in the plugin's own words for where a choice came from.
func TestDoctorListsThePalbaseEnvKeys(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "featureX", androidConfig("https://featurex.example", "pb_featurex_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "org.gradle.jvmargs=-Xmx2g\npalbase.env.debug=main\npalbase.env.release=main\n")
	writeFileIn(t, dir, "local.properties", "sdk.dir=/opt/android-sdk\npalbase.env.debug = featureX\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n"+
			"  ✓ debug      → main (palbase.env.debug in gradle.properties)\n"+
			"  ✓ release    → main (palbase.env.release in gradle.properties)\n"+
			"  ✓ debug      → featureX (palbase.env.debug in local.properties)\n")
}

// A KEY THAT NAMES NO DIRECTORY fails the build that reads it. One whose name
// differs only in case finds its directory on this Mac and not on CI: the
// plugin matches the exact name.
func TestDoctorNamesAKeyWhoseEnvironmentIsNotHere(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "featureX", androidConfig("https://featurex.example", "pb_featurex_cK"), withRoles)
	writeFileIn(t, dir, "local.properties", "palbase.env.debug=Featurex\npalbase.env.staging=featureZ\npalbase.env.qa=local\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ debug      → Featurex (palbase.env.debug in local.properties) — palbase/environments/featureX differs only in case, "+
			"and a build finds its directory by the exact name: palbase.env.debug=featureX\n"+
			"  ✗ staging    → featureZ (palbase.env.staging in local.properties) — no palbase/environments/featureZ here; "+
			"`palbase link` here writes one directory per environment of the project\n"+
			"  ✗ qa         → local (palbase.env.qa in local.properties) — no palbase/environments/local here; "+
			"`palbase start` in the backend, then `palbase link` here\n")
}

// WHAT local.properties CANNOT CARRY is said, not listed as if it counted: it
// reaches debuggable builds only (a release build is not one), and it holds
// per-build-type keys, not the global one.
func TestDoctorSaysWhichKeysLocalPropertiesDoesNotReach(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "local.properties", "palbase.env.release=main\npalbase.env=main\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    → main (palbase.env.release in local.properties) — ignored: local.properties reaches only debuggable builds, "+
			"and a release build is not one; put it in gradle.properties\n"+
			"  ✗ any        → main (palbase.env in local.properties) — ignored: local.properties carries palbase.env.<build type> keys, "+
			"not the global palbase.env\n")
}

// A CROSS-PLATFORM CHECKOUT'S GRADLE BUILD IS android/, and so are its
// properties files.
func TestDoctorReadsTheKeysOfACrossPlatformCheckout(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, dir, "android/app/build.gradle", "android {\n    defaultConfig {\n        applicationId \"com.example.todo\"\n    }\n}\n")
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "android/gradle.properties", "palbase.env.release=main\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✓ release    → main (palbase.env.release in android/gradle.properties)\n")
}

// A KEY IS READ AS THE PLUGIN READS IT. Gradle and the plugin load these files
// through java.util.Properties — ISO-8859-1, \uXXXX escapes, a line continued
// by a trailing backslash — and the plugin trims the value it gets: `main  `
// selects main. `palbase link` hands over a name outside plain ASCII in those
// escapes, so the line it prints reads back as the directory it wrote.
func TestDoctorReadsTheKeysAsTheGradlePluginDoes(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "Zürich", androidConfig("https://zurich.example", "pb_zurich_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties",
		"palbase.env.debug=main  \n# palbase.env.qa=nothing\\\npalbase.env.release=ma\\\n    in\npalbase.env.staging=Z\\u00fcrich\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✓ debug      → main (palbase.env.debug in gradle.properties)\n"+
			"  ✓ release    → main (palbase.env.release in gradle.properties)\n"+
			"  ✓ staging    → \"Zürich\" (palbase.env.staging in gradle.properties)\n")
	require.NotContains(t, out, "palbase.env.qa", "a comment is not continued by a trailing backslash")
}

// A NAME PASTED IN UTF-8 IS NOT THE NAME A BUILD READS: the file is decoded as
// ISO-8859-1, so `Zürich` arrives as `ZÃ¼rich`, a directory nobody has. The
// line says so, with the spelling that reads back.
func TestDoctorSaysANameWrittenInUTF8ReadsAsAnotherOne(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "Zürich", androidConfig("https://zurich.example", "pb_zurich_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.debug=Zürich\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ debug      → \"ZÃ¼rich\" (palbase.env.debug in gradle.properties) — gradle.properties is read as ISO-8859-1, "+
			"so \"Zürich\" written in UTF-8 reads as this; write it as palbase.env.debug=Z\\u00fcrich\n")
}

// A VALUE THAT IS NOT ONE DIRECTORY NAME is refused by the plugin before it
// looks for a directory — an empty one included, which does not mean unset —
// and no `palbase link` ever writes such a directory.
func TestDoctorSaysWhichValuesTheGradlePluginRefuses(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.debug=\npalbase.env.qa=../outside\npalbase.env.staging=.main\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ debug      → \"\" (palbase.env.debug in gradle.properties) — a build refuses it: the name is empty, "+
			"and an environment is one directory under palbase/environments\n"+
			"  ✗ qa         → \"../outside\" (palbase.env.qa in gradle.properties) — a build refuses it: the name contains a path separator, "+
			"and an environment is one directory under palbase/environments\n"+
			"  ✗ staging    → \".main\" (palbase.env.staging in gradle.properties) — a build refuses it: the name starts with a dot, "+
			"and an environment is one directory under palbase/environments\n")
}

// A MODULE'S OWN gradle.properties NEVER CHOOSES (FR-203): the plugin reads the
// root file every module shares, and refuses every build of a module whose own
// file holds a palbase.env line rather than ignore it in silence.
func TestDoctorSaysAModulesOwnGradlePropertiesIsRefused(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "app/gradle.properties", "android.useAndroidX=true\npalbase.env=main\npalbase.env.release=main\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ any        → main (palbase.env in app/gradle.properties) — every build of app/ is refused while it holds this line: "+
			"a module's own gradle.properties never chooses an environment; move it to gradle.properties\n"+
			"  ✗ release    → main (palbase.env.release in app/gradle.properties) — every build of app/ is refused while it holds this line: "+
			"a module's own gradle.properties never chooses an environment; move it to gradle.properties\n")

	rn := t.TempDir()
	writeFileIn(t, rn, "android/app/build.gradle", "android {\n    defaultConfig {\n        applicationId \"com.example.todo\"\n    }\n}\n")
	androidEnvironmentIn(t, rn, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, rn, "android/app/gradle.properties", "palbase.env.debug=main\n")

	require.Contains(t, runDoctorIn(t, rn),
		"  ✗ debug      → main (palbase.env.debug in android/app/gradle.properties) — every build of android/app/ is refused "+
			"while it holds this line: a module's own gradle.properties never chooses an environment; move it to android/gradle.properties\n")
}

// A FILE JAVA CANNOT LOAD stops a Gradle build before any environment is
// chosen: a hand-written Windows path in local.properties reads `\u` followed
// by "sers" as a broken escape.
func TestDoctorSaysWhenAPropertiesFileCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.debug=main\n")
	writeFileIn(t, dir, "local.properties", "sdk.dir=C:\\users\\me\\android\npalbase.env.debug=main\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✓ debug      → main (palbase.env.debug in gradle.properties)\n"+
			"  ✗ keys       local.properties cannot be read (malformed \\uxxxx encoding), and a Gradle build stops on it too — fix the file\n")
}

// NOTHING FROM A PROPERTIES FILE REACHES THE TERMINAL RAW: a key is somebody's
// text as much as a value is, and \uXXXX puts any character in either.
func TestDoctorPrintsNoControlCharacterFromAPropertiesFile(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.de\\u001bbug=ma\\u001b[2Jin\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ \"de\\x1bbug\" → \"ma\\x1b[2Jin\" (palbase.env.\"de\\x1bbug\" in gradle.properties) — a build refuses it: "+
			"the name contains a control character, and an environment is one directory under palbase/environments\n")
	require.NotContains(t, out, "\x1b")
}

// A RELEASE BUILD WITH NO ENVIRONMENT is refused by the plugin; doctor says so
// before Gradle does, with the environment to map it to — never `local`.
func TestDoctorSaysReleaseIsNotMapped(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.debug=local\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✓ debug      → local (palbase.env.debug in gradle.properties)\n"+
			"  ✗ release    not mapped in gradle.properties — a release build refuses to guess its environment; "+
			"map release to a cloud environment: palbase.env.release=main (or palbase { environment = \"<name>\" } in the build script)\n")
}

// WITH NOTHING HERE A RELEASE BUILD MAY BUILD, the cure is the step that
// brings one: a link — of a project, when the checkout names none yet.
func TestDoctorSaysALocalOnlyCheckoutHasNoEnvironmentForRelease(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    not mapped in gradle.properties — a release build refuses to guess its environment; "+
			"link a project and map release to one of its environments\n")
}

func TestDoctorSendsALinkedCheckoutWithNoEnvironmentToLink(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	linkedToAProject(t, dir)

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    not mapped in gradle.properties — a release build refuses to guess its environment; "+
			"`palbase link` here, then map release to one of the project's environments\n")
}

// A RELEASE BUILD OF THIS MACHINE'S STACK is refused by the plugin (FR-206):
// `local` by name, and any environment whose address is loopback — what an
// older `link` wrote under main/ for a stack started here (B8).
func TestDoctorNamesALoopbackEnvironmentMappedToRelease(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "onbox", androidConfig("http://127.0.0.1:18865", "pb_onbox_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.release=local\npalbase.env.freeRelease=onbox\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ release    → local (palbase.env.release in gradle.properties) — local is the stack on this machine, "+
			"which a release build refuses; map release to a cloud environment: palbase.env.release=main\n"+
			"  ✗ freeRelease → onbox (palbase.env.freeRelease in gradle.properties) — onbox's base_url, http://127.0.0.1:18865, "+
			"is this machine, which a release build refuses; map freeRelease to a cloud environment: palbase.env.freeRelease=main\n")
	require.NotContains(t, out, "not mapped")
}

// A KEY'S VALUE IS A NAME, NEVER A PATH: doctor reads no file through one
// that is not a single directory name, and says it names no environment.
func TestDoctorReadsNoFileThroughAKeyThatIsAPath(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "palbase/outside/android-config.json", androidConfig("http://127.0.0.1:1", "pb_outside_cK"))
	writeFileIn(t, dir, "gradle.properties", "palbase.env.release=../outside\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ release    → \"../outside\" (palbase.env.release in gradle.properties) — a build refuses it: "+
			"the name contains a path separator, and an environment is one directory under palbase/environments\n")
	require.NotContains(t, out, "127.0.0.1:1", "palbase/outside/android-config.json was read through the key")
}

// THE GLOBAL KEY OF PLUGIN 2.3 reaches a release build no other key maps: it
// is a mapping, and a loopback one is refused like any other.
func TestDoctorNamesTheGlobalKeyWhenReleaseFallsToALoopbackEnvironment(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env=local\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ any        → local (palbase.env in gradle.properties) — a release build falls back to it, and local is the stack "+
			"on this machine, which a release build refuses; map release to a cloud environment: palbase.env.release=main\n")
	require.NotContains(t, out, "not mapped")
}

// PLAIN HTTP IS REFUSED TOO (FR-206): a build that is not debuggable refuses a
// base_url over http whatever its host, so neither the line for the key nor
// the environment it proposes instead may be one.
func TestDoctorSaysAPlainHTTPEnvironmentIsRefusedForRelease(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "lan", androidConfig("http://192.168.1.20:54321", "pb_lan_cK"), withRoles)
	androidEnvironmentIn(t, dir, "staging", androidConfig("https://staging.example", "pb_staging_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.release=lan\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    → lan (palbase.env.release in gradle.properties) — lan's base_url, http://192.168.1.20:54321, "+
			"is plain HTTP, which a release build refuses; map release to a cloud environment: palbase.env.release=staging\n")
}

// THE ENVIRONMENT PROPOSED FOR RELEASE IS ONE A RELEASE BUILD ACCEPTS: not an
// older link's loopback main/ (B8), and not a directory that is not an
// environment at all.
func TestDoctorProposesAnEnvironmentAReleaseBuildAccepts(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("http://127.0.0.1:54321", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "palbase/environments/mine/NOTES.md", "mine\n")
	androidEnvironmentIn(t, dir, "staging", androidConfig("https://staging.example", "pb_staging_cK"), withRoles)

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    not mapped in gradle.properties — a release build refuses to guess its environment; "+
			"map release to a cloud environment: palbase.env.release=staging (or palbase { environment = \"<name>\" } in the build script)\n")
}

// A BUILD THAT MEASURES RELEASE DOES NOT MAP IT (FR-201 step 6):
// benchmarkRelease follows release's environment, never the other way round.
func TestDoctorSaysABenchmarkKeyDoesNotMapRelease(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.benchmarkRelease=main\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✓ benchmarkRelease → main (palbase.env.benchmarkRelease in gradle.properties)\n"+
			"  ✗ release    not mapped in gradle.properties — a release build refuses to guess its environment; "+
			"map release to a cloud environment: palbase.env.release=main (or palbase { environment = \"<name>\" } in the build script)\n")
}

// A base_url IS A FILE'S TEXT, and it reaches the line that names it: every
// rune that does not print is written out as an escape (FR-006).
func TestDoctorPrintsNoControlCharacterFromABaseURL(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "onbox", androidConfig("http://127.0.0.1:18865/\u202e\u0085", "pb_onbox_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.release=onbox\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out, "— onbox's base_url, http://127.0.0.1:18865/\\u202e\\u0085, is this machine, which a release build refuses; ")
	require.NotContains(t, out, "\u202e")
	require.NotContains(t, out, "\u0085")
}

// A gradle.properties GRADLE CANNOT LOAD stops every build before any variant
// has an environment: what a release build would read there is unknown, so
// doctor does not claim it reads nothing.
func TestDoctorSaysNothingOfReleaseWhenGradlePropertiesCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "sdk.dir=C:\\users\\me\\android\npalbase.env.release=main\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ keys       gradle.properties cannot be read (malformed \\uxxxx encoding), and a Gradle build stops on it too — fix the file\n")
	require.NotContains(t, out, "not mapped")
}

// THE SAME ANSWER ON EVERY DISK: a value that differs only in case from a
// loopback environment is that environment to a Mac, and no directory at all
// to Linux CI. Either way a release build cannot use it, and the cure is a
// cloud environment — never the loopback spelling a case fix would give.
func TestDoctorJudgesAReleaseKeyByTheDirectoryItMeans(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "onbox", androidConfig("http://127.0.0.1:18865", "pb_onbox_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.release=Onbox\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ release    → Onbox (palbase.env.release in gradle.properties) — onbox's base_url, http://127.0.0.1:18865, "+
			"is this machine, which a release build refuses; map release to a cloud environment: palbase.env.release=main\n")
	require.NotContains(t, out, "palbase.env.release=onbox")
}

// twoEnvironmentsCloud lists one project, todoapp, with two environments —
// the listing the resolver refuses to pick from when nothing selected one.
func twoEnvironmentsCloud() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/projects" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": "prd_a", "name": "todoapp",
			"environments": []map[string]any{
				{"ref": "mainref000", "name": "main", "status": "Running"},
				{"ref": "stagref000", "name": "staging", "status": "Running"},
			},
		}})
	})
}

// THE ENV LINE IS WHERE A VERB WOULD ACT, and in an Android checkout it reads
// as the environment the app builds — whose fix, `palbase env use`, changes
// nothing an APK compiles. There it says what it is.
func TestDoctorSaysTheEnvLineIsForVerbsInAnAndroidCheckout(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	linkedToAProject(t, dir)

	require.Contains(t, runDoctorAgainst(t, dir, twoEnvironmentsCloud(), "person-token"),
		"  ✗ env        todoapp has 2 environments and none is selected — verbs only; the build type picks the app's environment\n")
}

// NEGATIVE CONTROL: outside an Android checkout the line is the resolver's own,
// as before.
func TestDoctorLeavesTheEnvLineAloneOutsideAnAndroidCheckout(t *testing.T) {
	dir := t.TempDir()
	linkedToAProject(t, dir)

	require.Contains(t, runDoctorAgainst(t, dir, twoEnvironmentsCloud(), "person-token"),
		"  ✗ env        todoapp has 2 environments and none is selected:\n")
}
