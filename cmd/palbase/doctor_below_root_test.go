package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// DOCTOR IN A GRADLE DIRECTORY INSIDE A LINKED CHECKOUT SENDS PEOPLE TO THE ROOT
// (FR-016, FR-019).
//
// React Native's android/ and a native app's app/ module are Android checkouts
// to detection — each holds a Gradle file declaring the applicationId — so the
// section ran there with that directory as its root: "nothing under
// palbase/environments — `palbase link` here", "no palbase/environments/main
// here; `palbase link` here", and a link line saying "run `palbase link
// <project>` here". `palbase link` refuses every one of those directories and
// names the root; the build there reads the root's palbase/. And in app/ the
// module's own gradle.properties was read as the root file, so a line the
// plugin refuses (FR-203) was judged as a choice instead.

// belowRootLines are doctor's link line and its line in place of the Android
// section, in a Gradle directory inside the checkout linked at root.
func belowRootLines(root string) (link, here string) {
	where := "a Gradle directory inside the checkout linked at " + root + " — run `palbase doctor` there\n"
	return "  ✗ link       this directory is not linked, and is " + where, "  ✗ here       " + where
}

func TestDoctorInsideAReactNativeAndroidDirectorySendsToItsRoot(t *testing.T) {
	dir := t.TempDir()
	rnCheckoutIn(t, dir)
	linkedToAProject(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "android/gradle.properties", "palbase.env.debug=main\npalbase.env.release=main\n")
	root, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	link, here := belowRootLines(root)

	out := runDoctorIn(t, filepath.Join(dir, "android"))

	require.Contains(t, out, link)
	require.True(t, strings.HasSuffix(out, "android (app/build.gradle)\n"+here), out)
	require.NotContains(t, out, "`palbase link")
}

func TestDoctorInsideANativeAppModuleSendsToItsRoot(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, dir, "settings.gradle.kts", "include(\":app\")\n")
	androidCheckoutIn(t, dir)
	linkedToAProject(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.release=main\n")
	writeFileIn(t, dir, "app/gradle.properties", "palbase.env.debug=main\n")
	root, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	link, here := belowRootLines(root)

	out := runDoctorIn(t, filepath.Join(dir, "app"))

	require.Contains(t, out, link)
	require.True(t, strings.HasSuffix(out, "android (build.gradle.kts)\n"+here), out)
	require.NotContains(t, out, "`palbase link")
}

// A MODULE THAT IS NO APP — a library, no applicationId — gets no Android
// section, and its link line sends people to the root all the same: link
// refuses there too.
func TestDoctorInsideALibraryModuleSendsToItsRoot(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, dir, "settings.gradle.kts", "include(\":app\", \":core\")\n")
	androidCheckoutIn(t, dir)
	writeFileIn(t, dir, "core/build.gradle.kts", "plugins {\n    id(\"com.android.library\")\n}\n")
	linkedToAProject(t, dir)
	root, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	link, _ := belowRootLines(root)

	out := runDoctorIn(t, filepath.Join(dir, "core"))

	require.Contains(t, out, link)
	require.NotContains(t, out, "android (")
}

// NEGATIVE CONTROL: an app that is its own Gradle root inside a repository
// linked to the backend is a checkout of its own — link writes its palbase/
// there, and the plugin never reads the repository root two levels up — so
// doctor reads its section there, as before.
func TestDoctorInAnAppThatIsItsOwnGradleRootReadsItsOwnSection(t *testing.T) {
	dir := t.TempDir()
	linkedToAProject(t, dir)
	app := filepath.Join(dir, "apps", "android")
	writeFileIn(t, app, "settings.gradle.kts", "include(\":app\")\n")
	androidCheckoutIn(t, app)

	out := runDoctorIn(t, app)

	require.Contains(t, out, "  ✗ link       this directory is not linked — run `palbase link <project>` here\n")
	require.Contains(t, out, "android (app/build.gradle.kts)\n"+
		"  ✗ envs       nothing under palbase/environments — `palbase link` here writes one directory per environment\n")
	require.NotContains(t, out, "✗ here")
}
