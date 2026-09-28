package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// rnCheckoutIn makes dir a React Native checkout: the whole Gradle build in
// android/ (FR-015), its app module declaring the applicationId.
func rnCheckoutIn(t *testing.T, dir string) {
	t.Helper()
	writeFileIn(t, dir, "android/settings.gradle", "include ':app'\n")
	writeFileIn(t, dir, "android/build.gradle", "buildscript {}\n")
	writeFileIn(t, dir, "android/app/build.gradle", "android {\n    defaultConfig {\n        applicationId \"com.example.todo\"\n    }\n}\n")
}

// The two things a copy in android/ does to a build (plugin 2.5.0, FR-205).
const (
	copyReadInstead = "  ✗ copy       android/palbase/ is a second copy: android/palbase/project.json makes android/ the checkout, " +
		"so a build reads it instead of palbase/ — delete it and commit that deletion\n"
	copyBothRefused = "  ✗ copy       android/palbase/ is a second copy: a build finds android/palbase/environments beside " +
		"palbase/environments and refuses — delete it and commit that deletion\n"
)

// A COPY AN OLDER CLI LEFT IN android/ IS NAMED AT THE ROOT (FR-016, FR-019).
//
// An older CLI linked wherever it ran, and in a React Native checkout android/
// was the only place it found Android: android/palbase/project.json may still
// be there. That file makes android/ a checkout of its own to the Gradle
// plugin, which then never looks above it — the build compiles the stale copy,
// green, while the root holds the current one (the D3a failure). The root's
// palbase/ is all doctor read, so it printed every line ✓: measured, the stale
// copy at https://STALE.example beside the root's https://new.example.
func TestDoctorNamesTheCopyAnEarlierLinkLeftInAndroid(t *testing.T) {
	dir := t.TempDir()
	rnCheckoutIn(t, dir)
	linkedToAProject(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://new.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "android/gradle.properties", "palbase.env.debug=main\npalbase.env.release=main\n")
	writeFileIn(t, dir, "android/palbase/project.json", `{"project":"prd_a","name":"todoapp"}`+"\n")
	writeFileIn(t, dir, "android/palbase/environments/main/android-config.json", androidConfig("https://STALE.example", "pb_stale_cK"))
	writeFileIn(t, dir, "android/palbase/environments/main/openapi.json", withRoles)

	require.Contains(t, runDoctorIn(t, dir), "android (android/app/build.gradle)\n"+copyReadInstead+
		"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n")
}

// WITHOUT project.json THE COPY IS FOUND BESIDE THE ROOT'S, and the plugin
// refuses the two ("more than one palbase/environments in reach") — doctor says
// which one to delete rather than leave the build's refusal to say it.
func TestDoctorNamesACopyInAndroidThatABuildRefuses(t *testing.T) {
	dir := t.TempDir()
	rnCheckoutIn(t, dir)
	linkedToAProject(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://new.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "android/palbase/environments/main/android-config.json", androidConfig("https://STALE.example", "pb_stale_cK"))

	require.Contains(t, runDoctorIn(t, dir), "android (android/app/build.gradle)\n"+copyBothRefused)
}

// NOT A SECOND COPY: one tree reached by two paths is read once by the plugin
// (distinctBy canonicalFile), and a palbase/ in android/ holding nothing a
// build reads changes nothing a build compiles. Neither gets a line — nor does
// a cross-platform checkout with no palbase/ in android/ at all.
func TestDoctorNamesNoCopyWhereABuildReadsNone(t *testing.T) {
	linked := t.TempDir()
	rnCheckoutIn(t, linked)
	linkedToAProject(t, linked)
	require.NoError(t, os.Symlink(filepath.Join("..", "palbase"), filepath.Join(linked, "android", "palbase")))

	linkedEnvs := t.TempDir()
	rnCheckoutIn(t, linkedEnvs)
	linkedToAProject(t, linkedEnvs)
	androidEnvironmentIn(t, linkedEnvs, "main", androidConfig("https://new.example", "pb_main_cK"), withRoles)
	require.NoError(t, os.MkdirAll(filepath.Join(linkedEnvs, "android", "palbase"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join("..", "..", "palbase", "environments"),
		filepath.Join(linkedEnvs, "android", "palbase", "environments")))

	empty := t.TempDir()
	rnCheckoutIn(t, empty)
	linkedToAProject(t, empty)
	androidEnvironmentIn(t, empty, "main", androidConfig("https://new.example", "pb_main_cK"), withRoles)
	writeFileIn(t, empty, "android/palbase/README", "notes\n")

	none := t.TempDir()
	rnCheckoutIn(t, none)
	linkedToAProject(t, none)
	androidEnvironmentIn(t, none, "main", androidConfig("https://new.example", "pb_main_cK"), withRoles)

	// The root linked, and nothing under its palbase/environments yet: the one
	// directory, reached through android/, is all a build could read.
	require.Contains(t, runDoctorIn(t, linked), "android (android/app/build.gradle)\n"+
		"  ✗ envs       nothing under palbase/environments — `palbase link` here writes one directory per environment\n")
	for name, dir := range map[string]string{"environments symlinked": linkedEnvs, "nothing read": empty, "no copy": none} {
		require.Contains(t, runDoctorIn(t, dir), "android (android/app/build.gradle)\n"+
			"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n", name)
	}
}
