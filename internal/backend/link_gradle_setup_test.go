package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gradleSetup is the block an Android link prints, for a checkout whose
// default environment is `env` and whose release line is `release`.
//
// The version is spelled out rather than read from the constant: it is what a
// person pastes into their build, and a bump is a decision the diff of this
// file should show.
func gradleSetup(env, release string) string {
	return `
Add Palbase to the Gradle build (Kotlin DSL shown):

  settings.gradle.kts, in pluginManagement { repositories { } } and in
  dependencyResolutionManagement { repositories { } }:
      maven("https://palgroup.github.io/palbackend-android/")

  build.gradle.kts of the project:
      plugins {
          id("org.jetbrains.kotlin.plugin.serialization") version "<your Kotlin version>" apply false
          id("io.palbase.codegen") version "2.5.0" apply false
      }

  build.gradle.kts of the app module:
      plugins {
          id("org.jetbrains.kotlin.plugin.serialization")
          id("io.palbase.codegen")
      }
      dependencies {
          implementation("io.palbase:palbe:2.5.0")
      }

  gradle.properties of the project, not a module's own — the environment
  each build type compiles:
      palbase.env.debug=` + env + `
      ` + release + `
`
}

// AN ANDROID LINK SAYS HOW THE BUILD USES WHAT IT WROTE (FR-013).
//
// Apple got its three lines printed and Android got nothing: no repository, no
// plugin id, no dependency, and no word about which environment a build
// compiles (verification B5, L1 output in full: `▸ android / wrote …/
// android-config.json / linked to … / commit palbase/`). The environment is the
// one this link read from, as the listing names it — a project made in the
// panel names its first environment `production`, and a literal `main` here
// would be a build that finds no directory.
func TestAnAndroidLinkPrintsTheGradleSetupWithTheListedEnvironment(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	production := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"prodref000": production.URL})
	o := linkOpts{
		url:          production.URL,
		platforms:    []string{"android"},
		linkedEnv:    "production",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "production", Ref: "prodref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Contains(t, out.String(), "wrote palbase/environments/production/openapi.json\n"+
		gradleSetup("production", "palbase.env.release=production")+"\nlinked to todoapp (prd_a)\n")
}

// THE STACK ON THIS MACHINE IS NEVER A RELEASE. A link to it by address has
// `local` as its only environment, and a release build refuses a loopback
// address: the line that would map release to it is not printed as a setting.
func TestALinkToThisMachinesStackPrintsNoReleaseMapping(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	stack := stackServing(t, linkKeyLocal, nil)
	linkedAs(t, stack.URL, "operator")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL, platforms: []string{"android"}}, &out), out.String())

	assert.Contains(t, out.String(), gradleSetup("local",
		"# palbase.env.release — none here: local is the stack on this machine, which a release build refuses; "+
			"link a project and map release to one of its environments"))
	assert.NotContains(t, out.String(), "palbase.env.release=")
}

// ONLY WHERE ANDROID WAS WRITTEN. A checkout with no client binds the backend
// and has no Gradle build to set up.
func TestALinkWithNoAndroidClientPrintsNoGradleSetup(t *testing.T) {
	inScratchCheckout(t)
	stack := stackServing(t, linkKeyLocal, nil)
	linkedAs(t, stack.URL, "operator")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())

	assert.NotContains(t, out.String(), "Gradle")
	assert.NotContains(t, out.String(), "palbase.env.")
}

// THE LINES ARE PASTED INTO A FILE GRADLE READS AS ISO-8859-1. A name the
// panel allows today — `Zürich EU` — pasted as printed in UTF-8 reads back as
// `ZÃ¼rich EU`, a directory nobody has, and Label's quotes would become part
// of the value; the name is spelled in the file's own escapes instead, which
// also keeps the printed line plain ASCII.
func TestTheGradlePropertiesLinesSpellTheNameTheWayGradleReadsIt(t *testing.T) {
	var out strings.Builder
	printAndroidSetup(&out, "Z\u00fcrich EU")

	assert.Equal(t, gradleSetup(`Z\u00fcrich EU`, `palbase.env.release=Z\u00fcrich EU`), out.String())
}
