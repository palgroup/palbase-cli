// PROOF HARNESS ONLY — never part of a migrated checkout. The plugin comes from
// the scratch clone (includeBuild), io.palbase libraries from local file
// repositories: 2.5.0 from the scratch Test repository (T030's dry run), 2.3.0
// from the local copy of the public distribution repo.
beforeSettings {
    pluginManagement {
        includeBuild("/Users/erkutbas/Github_Pallasite/palbackend-android-src/codegen-gradle")
    }
    dependencyResolutionManagement {
        repositories {
            maven {
                url = uri("file:///Users/erkutbas/Github_Pallasite/palbackend-android-src/build/test-repository")
                content { includeGroup("io.palbase") }
            }
            maven {
                url = uri("file:///Users/erkutbas/Github_Pallasite/palbackend-android")
                content { includeGroup("io.palbase") }
            }
        }
    }
}
