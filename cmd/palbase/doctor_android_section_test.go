package main

import (
	"fmt"
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
