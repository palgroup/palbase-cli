// PROOF HARNESS ONLY: io.palbase — plugin marker, plugin, engine and runtime —
// from the Test repositories T030's dry run filled, as a consumer resolves the
// public tree after publish.sh. No includeBuild.
beforeSettings {
    pluginManagement {
        repositories {
            maven {
                url = uri("file:///Users/erkutbas/Github_Pallasite/palbackend-android-src/codegen-gradle/build/test-repository")
                content { includeGroupByRegex("io\\.palbase.*") }
            }
            maven {
                url = uri("file:///Users/erkutbas/Github_Pallasite/palbackend-android-src/build/test-repository")
                content { includeGroupByRegex("io\\.palbase.*") }
            }
        }
    }
    dependencyResolutionManagement {
        repositories {
            maven {
                url = uri("file:///Users/erkutbas/Github_Pallasite/palbackend-android-src/build/test-repository")
                content { includeGroup("io.palbase") }
            }
        }
    }
}
