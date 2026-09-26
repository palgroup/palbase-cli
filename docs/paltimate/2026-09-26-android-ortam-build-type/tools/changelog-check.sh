#!/usr/bin/env bash
# Every 2.4 behaviour change is named in the CHANGELOG section that is being
# released — and every message it quotes is one the plugin really prints.
# usage (repo root): changelog-check.sh [heading]   (default: "## Yayınlanmamış")
heading="${1:-## Yayınlanmamış}"
src=codegen-gradle/src/main/kotlin/io/palbase/gradle
section="$(awk -v h="$heading" 'index($0,h)==1{p=1;next} p&&/^## /{exit} p' CHANGELOG.md)"
missing=0
need() { # need <what> <text> [<source file that must print it>]
  if ! grep -qF -- "$2" <<<"$section"; then echo "missing: $1 — \`$2\`"; missing=$((missing + 1)); fi
  if [[ -n "${3:-}" ]] && ! grep -qF -- "$2" "$src/$3"; then echo "not in $3: \`$2\`"; missing=$((missing + 1)); fi
}
need "release is refused"                "a release never gets one by default"               EnvironmentResolver.kt
need "AGP 8 test/check reach release"    "testReleaseUnitTest"
need "custom build type compiles itself" "from the build type name"                          EnvironmentResolver.kt
need "legacy palbase.env before the name" "the 2.3 global property"                          EnvironmentResolver.kt
need "-Ppalbase.env ranking"             "-Ppalbase.env.<variant|flavor|build type>"
need "overrides are labelled"            "ORG_GRADLE_PROJECT_*"                              EnvironmentResolver.kt
need "local.properties, debuggable only" "in local.properties is ignored for"                EnvironmentResolver.kt
need "loopback refused when shippable"   "is not debuggable, and environment"                GeneratePalbaseTask.kt
need "exact names"                       "differs from it only"                              GeneratePalbaseTask.kt
need "two roots refused"                 "has more than one palbase/environments in reach"   GeneratePalbaseTask.kt
need "no root, one line"                 "nothing generated: no palbase/environments found"  GeneratePalbaseTask.kt
need "module gradle.properties refused"  "a module's own gradle.properties never"          AndroidVariantIntegration.kt
need "library fallback fails"            "pre<Variant>Build"
need "Isolated Projects"                 "Isolated Projects is on"                           AndroidVariantIntegration.kt
need "task API removed"                  "GeneratePalbaseTask.environmentsDir"
need "extension convention gone"         "PalbaseExtension.environmentsDir"
need "plugin constructor"                "@Inject constructor(BuildFeatures)"
need "Gradle floor"                      "Gradle 8.11.1"
need "AGP floor"                         "AGP 8.10.1"
need "JDK floor"                         "JDK 17"
need "packaged environment name"         "palbase_environment"
need "generation line"                   "Palbase: <variant> → <ortam> (<köken>) [<kök>]"
if (( missing )); then echo "$missing missing"; exit 1; fi
echo "the section names every 2.4 behaviour change"
