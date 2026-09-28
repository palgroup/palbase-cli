package backend

import (
	"fmt"
	"io"

	"github.com/palgroup/palbase-cli/internal/envname"
)

// palbaseAndroidVersion is the Android SDK release `palbase link` tells an app to
// build with.
//
// ONE CONSTANT, BECAUSE THEY SHIP AS ONE. The `io.palbase.codegen` plugin and
// `io.palbase:palbe` are published together at a single version (the Android
// SDK's publish.sh; the plugin's POM pins the engine at its own version), and
// the plugin generates code against that version's runtime — two numbers here
// could only disagree. The Android SDK's release bumps it. It is 2.5.0 because
// the per-build-type keys printed below are 2.5's: 2.3 reads only the global
// `palbase.env`, and so do 2.4.0 and 2.4.1, which were never published to the
// repository below.
const palbaseAndroidVersion = "2.5.0"

// palbaseAndroidRepository is the Maven repository both are served from, and
// the only one a build needs besides google() and mavenCentral().
const palbaseAndroidRepository = "https://palgroup.github.io/palbackend-android/"

// printAndroidSetup tells an Android developer what their Gradle build needs to
// use what this link wrote (FR-013) — the Android twin of
// printEnvironmentSelectionSnippet.
//
// THE ENVIRONMENT IS THE ONE THIS LINK READ FROM, never a literal. A project
// made in the panel names its first environment `production`; a `main` printed
// here would be a build that finds no directory, and the plugin's default for
// debug is `local`, which a checkout linked to the cloud does not carry.
//
// IN THE ROOT gradle.properties, SPELLED THE WAY THAT FILE IS READ. The plugin
// reads the keys from the project's own gradle.properties and refuses a
// `palbase.env` line in a module's; and it decodes that file as ISO-8859-1
// through java.util.Properties, so the name is written in the file's escapes
// (envname.PropertiesValue) — a slug prints as it is.
//
// NOT A RELEASE WHEN IT IS THIS MACHINE. A link to the stack `palbase start`
// runs has `local` as its only environment, and a release build refuses a
// loopback address (the plugin, FR-206). Printing `palbase.env.release=local`
// would be handing over a line the build rejects, so the line says why there is
// none instead — as a comment, harmless if it is pasted. The release then
// compiles nothing until one is chosen, and its generation says how.
func printAndroidSetup(w io.Writer, env string) {
	value := envname.PropertiesValue(env)
	release := "palbase.env.release=" + value
	if env == localEnvName {
		release = "# palbase.env.release — none here: local is the stack on this machine, which a release build " +
			"refuses; link a project and map release to one of its environments"
	}
	fmt.Fprintf(w, `
Add Palbase to the Gradle build (Kotlin DSL shown):

  settings.gradle.kts, in pluginManagement { repositories { } } and in
  dependencyResolutionManagement { repositories { } }:
      maven("%[1]s")

  build.gradle.kts of the project:
      plugins {
          id("org.jetbrains.kotlin.plugin.serialization") version "<your Kotlin version>" apply false
          id("io.palbase.codegen") version "%[2]s" apply false
      }

  build.gradle.kts of the app module:
      plugins {
          id("org.jetbrains.kotlin.plugin.serialization")
          id("io.palbase.codegen")
      }
      dependencies {
          implementation("io.palbase:palbe:%[2]s")
      }

  gradle.properties of the project, not a module's own — the environment
  each build type compiles:
      palbase.env.debug=%[3]s
      %[4]s
`, palbaseAndroidRepository, palbaseAndroidVersion, value, release)
}
