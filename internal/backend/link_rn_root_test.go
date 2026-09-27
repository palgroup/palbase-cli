package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedGradleFile writes one Gradle file under the current directory, making its
// parent directories.
func seedGradleFile(t *testing.T, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
	writeFile(t, name, body)
}

// A REACT NATIVE OR FLUTTER ROOT KEEPS ITS WHOLE GRADLE BUILD IN android/ (FR-015),
// beside ios/ — the layout applePlatforms already reads. Detection looked at
// app/ and the root alone, so `palbase link` there found no Android at all
// (verification N2: "RN repo root -> None"), and the OAuth identifier, reading
// the same list, could never name the app either.
func TestAReactNativeRootIsAnAndroidCheckout(t *testing.T) {
	for _, file := range []string{
		"android/app/build.gradle",
		"android/app/build.gradle.kts",
		"android/build.gradle",
		"android/build.gradle.kts",
	} {
		t.Run(file, func(t *testing.T) {
			inScratchCheckout(t)
			seedGradleFile(t, file, "android {\n  defaultConfig {\n    applicationId \"com.rnapp\"\n  }\n}\n")

			assert.Equal(t, []string{"android"}, detectPlatforms("."))
			assert.Equal(t, []string{"com.rnapp"}, nativeIdentifiers("android"))
		})
	}
}

// AND A LINK THERE WRITES THE ANDROID CONFIG, at the root the build's Gradle
// root sits under (android/ is the Gradle root; the plugin looks one level up).
func TestALinkAtAReactNativeRootWritesTheAndroidConfig(t *testing.T) {
	inScratchCheckout(t)
	seedGradleFile(t, "android/app/build.gradle", "android {\n  defaultConfig {\n    applicationId \"com.rnapp\"\n  }\n}\n")
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		linkedEnv:    "main",
		product:      Product{ID: "prd_rn", Name: "rnapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.True(t, strings.HasPrefix(out.String(), "▸ android\n"), out.String())
	assert.FileExists(t, ConfigPath("main", "android"))
	assert.NoDirExists(t, filepath.Join("android", RootDir()))
}
