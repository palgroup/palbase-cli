# Prototip, test matrisi ve eleştiriler — 2026-09-24

> Bu dosya ajan çıktılarından makineyle üretildi (2026-09-26). İçindeki `/private/tmp/...` yolları artık yok: macOS 2026-09-26'da geçici dizini temizledi. Prototip `proto-2.4/` altındaki yamalardan yeniden kurulur (birebir: 13 dosya, +1982/−110; 23+8+47 test yeşil).

## Prototip (plugin 2.4)

## Short answer

The plan works as written. I built it and ran it three ways:
- **Tests:** 69 tests pass (23 new resolver unit tests, and 46 TestKit tests: the 27 old ones plus 19 new).
- **Consumer builds:** the trial-based consumers build debug, release and featureX APKs on AGP 8.11.1 and on AGP 9.1.1. Each APK packs a different stack.
- **Real repos:** unchanged. `git status` is clean in palbackend-android-src, palbe-trial-android, palbackend-android and palbase-cli.

Sandbox git history (`git log` in `proto/src`):
- `3ffb61f`: baseline copy.
- `6f562aa`, `df8d526`: the prototype.
- `be14cf1`: baseline README and CHANGELOG. The 2.4 README and CHANGELOG edits are inside `df8d526`.

## DSL question (measured in `consumer-kts`, `consumer-groovy` and TestKit)

| Syntax | Result |
|---|---|
| (a) Kotlin `palbase { environment = "x" }` with **no import** | **Does not compile.** It binds to the PROJECT extension, so `environment` fails with `Function invocation 'environment(...)' expected / Unresolved reference ... receiver type mismatch`. The danger is proven: with no import, `release { palbase { packageName.set("com.silent.bind") } }` **compiled**, and the **debug** variant's code was generated into `generated/java/generatePalbaseDebug/com/silent/bind/PalbaseGenerated.kt`. So the inner block DOES silently bind to the project extension. |
| (b) the same line with `import io.palbase.gradle.palbase` | **Works** in `debug {}`, `release {}` and `create("featureX") {}`. The top-level `palbase { packageName.set(...) }` still binds to the project. `packageName` inside a build type now fails with `Unresolved reference: packageName`, so the silent-binding trap is closed. |
| (c) `extensions.configure<io.palbase.gradle.PalbaseBuildType> { environment = "x" }` | **Works** with no import, in debug, release and create(...). |
| Groovy `palbase { environment = 'x' }` | **Works** with no import, in debug, release and `featureX {}`. `packageName` there fails with `Could not set unknown property 'packageName' for extension 'palbase' of type io.palbase.gradle.PalbaseBuildType`. |

One extra finding, **not shipped**: I put the same extension function in package `org.gradle.kotlin.dsl`, which Kotlin build scripts import automatically. With that, (a) compiled and bound to the build type, with no import. I measured it and removed it. Neither AGP nor KGP ships classes in that package; I checked their jars.

## Files changed (`codegen-gradle/src/main/kotlin/io/palbase/gradle/`)

- **`EnvironmentResolver.kt`** (new) is the resolution logic, with no Gradle types in it. Its entry point is `resolve(variant, buildType): EnvironmentResolution`, which returns either `Selected(environment, origin)` or `Refused(reason)`.
  - It implements steps 1–8 exactly, then checks the name is one path segment.
  - Each source is read only when the resolution reaches it, so a source that cannot change the answer is never a cache input.
  - **Step 4, how I read it:** the DSL value is compared with the first of `palbase.env.<V>` / `palbase.env.<B>` in gradle.properties. So a variant key that the DSL would hide is also refused.
  - **Step 5:** `freeBenchmarkRelease` is resolved as `(freeRelease, release)`. It first checks `palbase.env.benchmarkRelease` and the benchmarkRelease DSL. The mapping only applies when the part after the prefix starts with a capital letter (`benchmarking` stays its own name).
- **`EnvironmentSources`** (in the same file) holds the lookups:
  - command line: `gradle.startParameter.projectProperties`;
  - `local.properties`: `providers.fileContents(<root>/local.properties)`, parsed with `Properties`;
  - DSL: a map filled in `finalizeDsl`;
  - gradle.properties and legacy `palbase.env`: `providers.gradleProperty`.
- **`PalbaseBuildType.kt`** (new) holds `abstract class PalbaseBuildType { abstract val environment: Property<String> }` and `fun BuildType.palbase(configure: Action<in PalbaseBuildType>)`, which is what the import in (b) brings in.
- **`AndroidVariantIntegration.kt`:**
  - `androidComponents.registerExtension(DslExtension.Builder("palbase").extendBuildTypeWith(PalbaseBuildType::class.java).build()) { NothingPerVariant }`. AGP adds the extension through `buildTypes.configureEach`, so debug and release get it too (checked in AGP bytecode).
  - `finalizeDsl` takes a snapshot of each build type's `environment`. `benchmarkRelease` needs release's value, which its own variant cannot see.
  - Roots: `environmentsDir.map { listOf(it) }.orElse(listOf(<module>/palbase/environments, <root>/palbase/environments))`. The task picks the first one that exists **when it runs**.
  - A refusal is stored on the task, not thrown during configuration. An unset release therefore never breaks `assembleDebug` or an IDE sync.
- **`GeneratePalbaseTask.kt`:**
  - `environment` is now `@Optional`. New: `environmentRefusal` (`@Input @Optional`), `environmentOrigin` and `variantName` (`@Internal`).
  - `environmentsDir` is replaced by `environmentRoots: ListProperty<Directory>`.
  - Order in the task action: no root → no-op; a refusal → fail and append "This checkout carries …"; then an **exact-name** match against the directory listing; then the log line `logger.lifecycle("Palbase: $variant → $env ($origin)")`. Codegen, asset, manifest, network config and validation are unchanged.
- **`PalbaseCodegenPlugin.kt` / `PalbaseExtension.kt`:** the global `palbase.env` convention and the `environmentsDir` convention are removed, and the docs updated. `packageName` still defaults to `io.palbase.generated`.
- **README.md / CHANGELOG.md** (copied into `proto/src`): I rewrote the environment section. The CHANGELOG entry is in Turkish, like the rest of that file.

## Measured behaviour

- **Consumer APK asset** (`unzip -p … assets/palbase/palbase-config.json`):
  - debug → `http://127.0.0.1:54321` (local);
  - featureX → `https://featurexref.palbase.studio`;
  - release → `https://8bbwb2pbm.palbase.studio` (main).
- **Log lines** from those builds:
  - `Palbase: debug → local (the default for debug)`
  - `Palbase: release → main (palbase { environment } in the \`release\` build type)`
  - `Palbase: featureX → featureX (from the build type name)`
- **Release unset:** `assembleDebug` succeeds. `testReleaseUnitTest` fails in `:app:generatePalbaseRelease` with "`release` has no environment … `palbase.env.release=<environment>` … or `buildTypes { release { palbase { environment = "<environment>" } } }` … This checkout carries featureX, local, main."
- **Missing environment:** `create("qa")` → "environment `qa` (from the build type name) has no directory …". `initWith(debug)` does not copy the `palbase { environment }` value.
- **2.3 compatibility:** with `palbase.env=main`, both debug and release compile main, with origin "palbase.env, the 2.3 global property".
- **Configuration cache and `-P`:** on Gradle 8.11.1, 8.13 and 9.5.0, adding or removing any `-P` flag invalidates the cache ("the set of Gradle properties has changed: 'probe.x' was removed"). So reading `startParameter.projectProperties` directly is safe.
- **AGP 9.1.1:** all three variants build, the build-type DSL works, and the cache is stored then reused.

### Birim testleri

Command:
cd /private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/39a3092a-b698-4c75-b79e-88fd2e465eed/scratchpad/spike/proto/src/codegen-gradle && JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home" ANDROID_HOME=/Users/erkutbas/Library/Android/sdk ../gradlew test --rerun-tasks

Result: EXIT=0, "BUILD SUCCESSFUL in 50s". Per test class:
- EnvironmentResolverTest (new, plain unit tests): tests="23" failures="0".
- PalbaseCodegenPluginTest (TestKit, AGP 8.10.1): tests="46" failures="0". That is the 27 existing tests, unchanged in intent, plus 19 new ones.
- codegen-engine: 46 tests, 0 failures.

The new TestKit tests cover:
- finding palbase/ at the checkout root with no palbase block;
- a module's own palbase/ beating the root one;
- palbase/ appearing after a cached configuration (read from the same cache entry);
- an unset release failing only its own task while debug builds;
- gradle.properties choosing per build type;
- the 2.3 global palbase.env still working;
- local.properties overriding gradle.properties, and editing it invalidating the cache;
- the command line beating local.properties, and dropping the flag reconfiguring even when the flag equals the gradle.properties value;
- the Kotlin DSL with the import, in debug, release and create("qa");
- the Kotlin DSL without the import failing with "Script compilation error" on `environment(`;
- extensions.configure with no import;
- the Groovy DSL;
- the DSL and gradle.properties disagreeing → refused;
- a custom build type compiling its namesake, and a missing one refused "(from the build type name)";
- exact-case matching (Main is not main);
- benchmarkRelease compiling release's environment;
- an invalid name ("../main") refused;
- a variant key for one flavor (paidDebug) beating the build-type rule;
- a library module.

Before my changes, the unmodified copy passed 27/27. Without ANDROID_HOME set, the first run failed 27/27 with "SDK location not found".

Mutation check (break the code on purpose, confirm a test fails):
- M2, choosing the root at configuration time: caught. Gradle re-configured instead of reusing ("the file system entry 'palbase/environments' has been created").
- M3, a case-insensitive isDirectory check: caught.
- M4, throwing the release refusal during configuration: caught.
- M1, reading local.properties with a plain File: NOT caught. AGP by itself already makes local.properties a cache input. I measured it with no Palbase plugin applied: "properties file …/local.properties has changed". So that test proves the behaviour, not which read API the plugin uses.

### Bilinen boşluklar

- Android Studio sync, and how the editor handles `import io.palbase.gradle.palbase`, are not tested. Only command-line Gradle was run. Command-line configuration does not fail when release is unset (`assembleDebug` passes), but I did not run an IDE sync.
- Nothing ran on a device or emulator. I checked only the `palbase/palbase-config.json` inside each APK (with unzip) and that the app compiled against the generated client. The palbe runtime reading that file on a device was not exercised.
- Versions tested: AGP 8.10.1 (TestKit, Gradle 8.11.1), AGP 8.11.1 (consumer, Gradle 8.13) and AGP 9.1.1 (consumer, Gradle 9.3.1, built-in Kotlin). Not tested: AGP below 8.10, AGP 9.3.x, com.android.test and dynamic-feature modules, and Gradle Isolated Projects. The plugin reads `project.rootDir`, not the `project.isolated` API.
- This is a deliberate breaking change for checkouts that never set `palbase.env`: in 2.3 their release silently compiled `local`. With 2.4 and a linked checkout, `assembleRelease`, `assemble`, `build`, `check` and `testReleaseUnitTest` all stop at `generatePalbaseRelease` (measured with testReleaseUnitTest) until release is set. The CLI (`palbase link`) does not yet write a release choice such as `palbase.env.release=main`. That is not implemented.
- Behaviour changes that follow from the spec's order: (1) a custom build type now compiles its own name even when legacy `palbase.env=main` is set (step 6 comes before step 7); (2) `-Ppalbase.env=X` on the command line is now step 7, so gradle.properties, local.properties, the DSL and the build-type-name rule all beat it. The 2.3 error text told users to pass `-Ppalbase.env=`; the new text suggests `-Ppalbase.env.<variant>=`.
- How I read the spec, for you to confirm: (a) the step-4 conflict compares the DSL with the first of palbase.env.<V>/<B> found in gradle.properties, so a variant key the DSL would hide is also refused; (b) 'gradle.properties' means `providers.gradleProperty`, which also picks up ~/.gradle/gradle.properties and ORG_GRADLE_PROJECT_ variables, so a personal ~/.gradle value counts as committed and can trigger the conflict refusal; (c) `-Dorg.gradle.project.palbase.env.X` does not count as command line and ranks at step 4.
- When no palbase/environments exists anywhere, every refusal (unset release, a conflict, an invalid name) is a silent no-op, as spec A says ('None → no-op, as today').
- The `Palbase: <variant> → <env> (<origin>)` line is printed only when the task actually runs, not when it is UP-TO-DATE or loaded from the build cache. That matches 'one line per generation'.
- Two changes go beyond spec D ('everything else unchanged'): (1) the environment directory must match the name exactly, so on macOS `Main` no longer matches `main` (this avoids passing locally and failing on Linux CI); (2) the task's public API changed: `GeneratePalbaseTask.environmentsDir` was removed in favour of `environmentRoots`, `environment` is now @Optional, and there are new properties. Code that configures this task directly will break at compile time and at runtime.
- Kotlin DSL option (a), with no import, cannot work in the shipped design. It would only work by putting the extension function in package org.gradle.kotlin.dsl. I measured that it works, but it is not shipped because it is unconventional.
- I did not build the full real repository: no dokka, publishing, apiValidation or the root build. Only the codegen-gradle included build (compile and test) and its codegen-engine dependency ran. No file maven repo was produced in proto/repo; consumers use it through includeBuild only.

## Test matrisi, 1. tur

| ID | Beklenen | Gözlenen | Geçti |
|---|---|---|---|
| M1 | assembleDebug compiles main | debug compiled main; the APK asset carries main.example.com | ✅ |
| M2 | assembleRelease compiles main | The unsigned release APK carries main | ✅ |
| M3 | assembleFeatureX compiles featureX through the build-type-name rule, and mylib resolves through matchingFallbacks | featureX compiled featureX. mylib (which has no featureX build type) was consumed as its debug variant. | ✅ |
| M4 | assembleFeatureY compiles featureY | featureY compiled featureY | ✅ |
| M5 | assembleFeatureProfileUpdate compiles feature-profile-update, set through the DSL | The DSL value was used | ✅ |
| M6 | palbase.env.debug=featureX in local.properties makes debug compile featureX | local.properties beat gradle.properties | ✅ |
| M7 | -Ppalbase.env.debug=featureY, with M6's local.properties still in place, compiles featureY | The command line beat local.properties | ✅ |
| M8 | create("qa") with no palbase/environments/qa makes assembleQa fail with a clear message and produce no APK | The build failed in generatePalbaseQa. No qa APK and no qa asset were produced. | ✅ |
| M9 | With palbase.env.release removed (and no legacy palbase.env), assembleRelease fails with a message naming both ways to set it | The build failed with the message. assembleDebug still succeeded with release unset. | ✅ |
| M10 | No block is needed (root discovery), and the 2.3 block palbase { environmentsDir.set(rootProject...dir("palbase/environments")) } still works | With no block, M1–M9 all found the root palbase/. With the legacy block added (and the import line present), debug, release and featureX were identical. | ✅ |
| M11 | A 2.3-style consumer (only palbase.env=main, only debug/release, block present, no import) compiles main for both debug and release | Both compiled main, with origin 'the 2.3 global property' | ✅ |
| M12 | The configuration cache is reused on the second run. A local.properties change regenerates the new env. A new featureZ dir plus create("featureZ") is picked up. | All held. Also: editing featureZ's config under a REUSED cache entry regenerated it, and deleting the featureZ dir under a reused entry failed (no stale APK). | ✅ |
| M13 | The featureX APK is debuggable, signed with the debug key, and in installable format | It is debuggable, signed with the Android Debug cert (same SHA-256 as ~/.android/debug.keystore) using v2, and zipalign verification succeeds. The auth-callback host in the manifest is also featurex. | ✅ |
| M14 | freeFeatureX compiles featureX; palbase.env.freeDebug=featureY makes freeDebug compile featureY while paidDebug compiles main | As expected. paidFeatureX compiled featureX and paidRelease compiled main. | ✅ |
| M15 | benchmarkRelease (initWith release, simulating baseline-profile) resolves as release and compiles main | benchmarkRelease and nonMinifiedRelease both followed release. With only a DSL value on release they followed it, and with release unset they refused. | ✅ |
| M16 | Find which DSL syntax compiles and works in debug{} and create("x"){}, and whether an inner palbase{} ever binds to the project extension | (a) with no import does NOT compile. (b) with the import works. (c) works with no import. Groovy works with no import. With no import, an inner palbase{} that sets a PROJECT property (packageName) compiles and silently binds to the project extension; with the import, or in Groovy, that fails. | ✅ |
| M17 | The prototype plugin's own tests pass | All pass (run on an unmodified rsync copy of proto/src, source diff identical, proto HEAD df8d526) | ✅ |
| M18 | The generated client compiles against palbe 2.3.0 for every variant | After clean assemble, compile<Variant>Kotlin succeeded for every variant. Every APK's dex holds 1549 io.palbase.generated classes. palbe 2.3.0 was resolved from the local file repo. | ✅ |
| X1-extra | (Not in the matrix) The plugin is applied only in a LIBRARY module (the client lives in :mylib), and the app has featureX/featureY that mylib lacks | SILENT WRONG ENVIRONMENT: the featureX and featureY APKs carry main, because mylib's debug variant is consumed via matchingFallbacks and the build-type-name rule never sees featureX. The only log line is from :mylib debug. Adding create("featureX") to mylib fixed featureX (featureY stayed main). | ❌ |
| X2-extra | (Not in the matrix) The plugin is applied in BOTH app and mylib | The debug-type builds succeeded. The app's asset won (featureY APK = featurey), but io.palbase.generated is generated in both modules and there was no duplicate-class failure in debug-type dexing. This is pre-existing (same in 2.3) and not specific to environments. Release/R8 not tested. | ✅ |
| X3-extra | (Not in the matrix) Other resolver edges | -Ppalbase.env.featureX=main overrides the name rule. A flavor-only key (palbase.env.free) is silently ignored, as the spec says (only <V>/<B> keys). A hyphenated build type create("feature-profile-update") is accepted by AGP 8.11.1 and the name rule compiles it. '../main', 'main/x' and '.git' are refused; 'Main' is refused (exact case). local.properties masks a DSL-vs-gradle.properties conflict. | ✅ |

### Kanıtlar

**M1**

```
./gradlew assembleDebug assembleRelease assembleFeatureX assembleFeatureY assembleFeatureProfileUpdate -> EXIT=0
"Palbase: debug → main (palbase.env.debug in gradle.properties)"
app/build/outputs/apk/debug/app-debug.apk  "base_url":"https://main.example.com"
```

**M2**

```
"Palbase: release → main (palbase.env.release in gradle.properties)"
app/build/outputs/apk/release/app-release-unsigned.apk  "base_url":"https://main.example.com"
```

**M3**

```
"Palbase: featureX → featureX (from the build type name)"
app-featureX.apk  "base_url":"https://featurex.example.com"
dependencyInsight --configuration featureXRuntimeClasspath --dependency :mylib -> "project :mylib  Variant debugRuntimeElements ... BuildTypeAttr | debug | featureX"
```

**M4**

```
"Palbase: featureY → featureY (from the build type name)"
app-featureY.apk  "base_url":"https://featurey.example.com"
```

**M5**

```
"Palbase: featureProfileUpdate → feature-profile-update (palbase { environment } in the `featureProfileUpdate` build type)"
app-featureProfileUpdate.apk  "base_url":"https://feature-profile-update.example.com"
```

**M6**

```
"Palbase: debug → featureX (palbase.env.debug in local.properties)"
app-debug.apk  "base_url":"https://featurex.example.com"
```

**M7**

```
./gradlew assembleDebug -Ppalbase.env.debug=featureY -> EXIT=0
"Palbase: debug → featureY (-Ppalbase.env.debug on the command line)"
app-debug.apk  "base_url":"https://featurey.example.com"
```

**M8**

```
EXIT=1 "Execution failed for task ':app:generatePalbaseQa'. > Palbase: environment `qa` (from the build type name) has no directory — .../consumer/palbase/environments/qa does not exist, and this checkout carries feature-profile-update, featureX, featureY, main. Run `palbase link` to write it, or choose one ..."
ls app/build/outputs/apk/qa -> No such file or directory
```

**M9**

```
EXIT=1 "Execution failed for task ':app:generatePalbaseRelease'. > Palbase: `release` has no environment, and a release never gets one by default ... `palbase.env.release=<environment>` in gradle.properties, or `buildTypes { release { palbase { environment = "<environment>" } } }` in the build script (Kotlin DSL: `import io.palbase.gradle.palbase`). This checkout carries feature-profile-update, featureX, featureY, main."
Then assembleDebug -> EXIT=0 "Palbase: debug → main"
```

**M10**

```
M10c (--rerun-tasks, with the block): "Palbase: debug → main (palbase.env.debug in gradle.properties)" / "Palbase: release → main ..." / "Palbase: featureX → featureX (from the build type name)"; APKs main/featurex/main.
Side probe M10b: the block pointed at a missing "elsewhere/environments" -> no-op, then Kotlin 'Unresolved reference HelloGreetQuery' (explicit dir wins, no fallback to discovery).
```

**M11**

```
gradle.properties: palbase.env=main only.
"Palbase: debug → main (palbase.env, the 2.3 global property)"
"Palbase: release → main (palbase.env, the 2.3 global property)"
app-debug.apk / app-release-unsigned.apk  "base_url":"https://main.example.com"
-Ppalbase.env=featureX -> "Palbase: debug → featureX (palbase.env, the 2.3 global property)"
```

**M12**

```
M12a "Configuration cache entry stored." / M12b "Configuration cache entry reused."
M12c "configuration cache cannot be reused because properties file .../local.properties has changed." "Palbase: debug → featureY (palbase.env.debug in local.properties)" apk featurey
M12e "Palbase: featureZ → featureZ (from the build type name)" apk featurez.example.com
M12g (Reusing configuration cache, config edited) apk "https://featurez2.example.com"
M12n (Reusing configuration cache, dir removed) EXIT=1 "environment `featureZ` ... has no directory"
M12k gradle.properties edit -> "cannot be reused because file 'gradle.properties' has changed" -> featurex
-P toggle: "the set of Gradle properties has changed: 'palbase.env.debug' was added" / "...was removed" -> featurey then main
```

**M13**

```
aapt2 dump badging -> "application-debuggable"
apksigner verify --print-certs -v -> "Verifies" "Verified using v2 scheme (APK Signature Scheme v2): true" "Signer #1 certificate DN: C=US, O=Android, CN=Android Debug" SHA-256 021aafa2...394c (keytool on debug.keystore: 02:1A:AF:A2:...:39:4C)
zipalign -c -P 16 -v 4 -> "Verification successful"
manifest: android:host="featurex.example.com" on io.palbase.auth.PalbaseAuthCallbackActivity
NOT installed on a device
```

**M14**

```
"Palbase: freeFeatureX → featureX (from the build type name)"
"Palbase: freeDebug → featureY (palbase.env.freeDebug in gradle.properties)"
"Palbase: paidDebug → main (palbase.env.debug in gradle.properties)"
app-free-debug.apk featurey / app-free-featureX.apk featurex / app-paid-debug.apk main / app-paid-featureX.apk featurex / app-paid-release-unsigned.apk main
```

**M15**

```
"Palbase: benchmarkRelease → main (palbase.env.release in gradle.properties, as `benchmarkRelease` builds as `release`)"
"Palbase: nonMinifiedRelease → main (...as `nonMinifiedRelease` builds as `release`)"
DSL on release only: "Palbase: benchmarkRelease → featureY (palbase { environment } in the `release` build type, as `benchmarkRelease` builds as `release`)"
release unset: EXIT=1 "Palbase: `release` has no environment ... (`benchmarkRelease` ..."
```

**M16**

```
(a) no import, debug{ palbase { environment = "featureX" } } -> "build.gradle.kts:25:23: Function invocation 'environment(...)' expected" / "Unresolved reference. None of the following candidates is applicable because of receiver type mismatch"
(a2) no import, debug{ palbase { packageName.set("com.silent.bind") } } + dslProbe{...bind2} -> script COMPILED, generated app/build/generated/java/generatePalbaseDebug/com/silent/bind2/PalbaseGenerated.kt
(b) import io.palbase.gradle.palbase -> "Palbase: debug → featureX (palbase { environment } in the `debug` build type)" "Palbase: dslProbe → featureY (... `dslProbe` build type)"; APKs featurex/featurey
(b2) import + packageName inside debug -> "build.gradle.kts:27:23: Unresolved reference: packageName"
(c) extensions.configure<io.palbase.gradle.PalbaseBuildType> { environment = "featureX" } no import -> debug featurex, dslProbe featurey, featureProfileUpdate feature-profile-update
Groovy build.gradle debug { palbase { environment = 'featureX' } }, dslProbe { initWith debug; palbase { environment = 'featureY' } } -> APKs featurex/featurey/feature-profile-update; packageName inside a build type -> "Could not set unknown property 'packageName' for extension 'palbase' of type io.palbase.gradle.PalbaseBuildType."
Conflict rule: DSL featureX + gradle.properties palbase.env.debug=main -> EXIT=1 "the `debug` build type names two environments in two committed places"; same value in both places -> accepted
```

**M17**

```
cd consumer/_m17/src/codegen-gradle && ../gradlew test -> EXIT=0 "BUILD SUCCESSFUL in 1m 8s"
io.palbase.gradle.PalbaseCodegenPluginTest tests="46" failures="0" errors="0"
io.palbase.gradle.EnvironmentResolverTest tests="23" failures="0" errors="0"
codegen-engine suites: 3+9+2+6+5+9+5+7 = 46 tests, 0 failures
```

**M18**

```
./gradlew clean assemble -> EXIT=0; "> Task :app:compileDebugKotlin" ":app:compileFeatureProfileUpdateKotlin" ":app:compileFeatureXKotlin" ":app:compileFeatureYKotlin" ":app:compileReleaseKotlin" (flavor/benchmark variants also compiled in M14/M15)
dexdump: debug/featureX/featureY/featureProfileUpdate/release = 1549 'Lio/palbase/generated/' classes each (incl. HelloGreetQuery)
dependencyInsight -> "io.palbase:palbe:2.3.0"
```

**X1-extra**

```
X1: "Palbase: debug → main (palbase.env.debug in gradle.properties)" (only mylib tasks: ":mylib:generatePalbaseDebug")
app-featureX.apk  "base_url":"https://main.example.com"
app-featureY.apk  "base_url":"https://main.example.com"
X1b (mylib declares featureX): app-featureX.apk featurex, app-featureY.apk "https://main.example.com"
```

**X2-extra**

```
X2 EXIT=0; app-featureX.apk featurex, app-featureY.apk featurey; ":app:checkFeatureYDuplicateClasses UP-TO-DATE"
```

**X3-extra**

```
"Palbase: featureX → main (-Ppalbase.env.featureX on the command line)"
palbase.env.free=featureY -> "Palbase: freeDebug → main (palbase.env.debug in gradle.properties)"
"Palbase: feature-profile-update → feature-profile-update (from the build type name)" apk feature-profile-update.example.com
"environment `../main` (-Ppalbase.env.debug on the command line) contains a path separator" / "`.git` ... starts with `.`" / "`Main` ... has no directory"
conflict + local.properties palbase.env.debug=featureY -> EXIT=0 "Palbase: debug → featureY (palbase.env.debug in local.properties)"
```

### DSL kararı

Kotlin DSL: `import io.palbase.gradle.palbase` at the top of the script, then `palbase { environment = "x" }` inside debug {} / release {} / create("x") {}. The no-import alternative is `extensions.configure<io.palbase.gradle.PalbaseBuildType> { environment = "x" }`. Groovy: `palbase { environment = 'x' }` with no import. Syntax (a), with no import in Kotlin, does NOT compile. The import-free fallback that works the same in both languages is `palbase.env.<buildType>=<env>` in gradle.properties.

### Test edilmeyenler

- Android Studio: Gradle sync and how the editor handles `import io.palbase.gradle.palbase`. Only command-line Gradle ran. Configuration with release unset does succeed on the command line (assembleDebug passed).
- Installing or running any APK on a device or emulator; the palbe runtime reading palbase/palbase-config.json on a device. Only APK contents were checked (unzip, aapt2, apksigner, zipalign, dexdump).
- The real androidx.baselineprofile plugin. benchmarkRelease/nonMinifiedRelease were simulated with create(...){initWith(release)} as the matrix says. The plugin was not downloaded.
- A signed release APK and minified/R8 release (isMinifyEnabled=false; only the unsigned release APK was checked). X2 (plugin in both modules) was not tested in release/R8.
- AGP 9.x and AGP below 8.11.1 in this fresh consumer. Here: AGP 8.11.1, Gradle 8.13, Kotlin 2.2.21, JBR 21. The plugin's TestKit suite ran on AGP 8.10.1 / Gradle 8.11.1.
- ~/.gradle/gradle.properties and ORG_GRADLE_PROJECT_ env vars as sources (the user's global Gradle config was deliberately not modified).
- `palbase link` actually writing the environment directories: the files were hand-copied from palbe-trial-android/palbase/environments/main with distinct base_url values.
- com.android.dynamic-feature, com.android.test, Gradle Isolated Projects, and Windows/Linux file systems.

## Result

All 18 matrix cases (M1–M18) **PASS** on a fresh consumer:
- **Consumer:** Kotlin DSL, AGP 8.11.1, Gradle 8.13, compileSdk 36, minSdk 26.
- **Modules:** `:app` with no `palbase {}` block, and `:mylib`, a library without the feature build types.
- **Environments:** 4 in root `palbase/environments`, each with its own `base_url`.

Every claim is backed by the `base_url` inside the APK and the `Palbase: <variant> → <env> (<origin>)` log line.

The real repos are unchanged. `git status` is empty in palbase-cli (20e5d7e), palbackend-android-src, palbackend-android and palbe-trial-android. The prototype was not edited: proto/src is still at HEAD df8d526 with a clean status.

## One gap the plan does not cover (X1, measured, outside the matrix)

**Setup:** the codegen plugin is applied in a **library** module (for example a `:data` module that holds the client), and the app has build types the library lacks (`featureX`).

**What happens:**
- The app's `featureX` consumes the library's `debug` variant through `matchingFallbacks`.
- So the featureX APK **silently carries `main`**, the library's debug environment. Measured: `app-featureX.apk "base_url":"https://main.example.com"`.
- The only log line is `Palbase: debug → main`, printed from `:mylib`.
- The build-type-name rule does not cross module boundaries.

**What fixes it:** declaring `create("featureX")` in the library too (measured). Applying the plugin only to the application module, as the matrix does, is correct.

**Options before shipping:**
- document "apply the plugin in the app module, or mirror the build types in the library"; or
- make the plugin warn when a library applies it.

## Other observations (all measured)

- **DSL:**
  - Kotlin needs `import io.palbase.gradle.palbase`. With no import, `palbase { environment = ... }` is a compile error ("Function invocation 'environment(...)' expected").
  - With no import, a *project* property inside a build type (for example `packageName.set(..)`) compiles and silently binds to the project extension. Measured: debug code was generated into `com/silent/bind2`.
  - The import, the `extensions.configure<PalbaseBuildType>` form, and Groovy all close that trap.
- **Conflict rule:** the DSL and gradle.properties disagreeing is refused. But a `local.properties` or `-P` value hides the conflict on that machine, by design of the order.
- **Silently ignored keys:** keys other than `<variant>` and `<buildType>` are ignored with no warning. `palbase.env.free` (a flavor alone) and typos like `palbase.env.featurX` are both ignored.
- **Wrong explicit directory:** `environmentsDir` set to a missing directory is a no-op. It then fails later as Kotlin "Unresolved reference" errors. That is the same as 2.3, and it is noisy rather than silent.
- **Hyphenated build types:** AGP 8.11.1 accepts `create("feature-profile-update")`, and the name rule compiles that environment.
- **Plugin in both app and library (X2):** debug-type builds succeed. The app's asset wins, but `io.palbase.generated` exists in both modules. This is pre-existing and not about environments.
- **Configuration cache:** it is invalidated correctly by edits to local.properties and gradle.properties and by adding or removing a `-P` flag. Environment file edits and deleting an environment directory are picked up even when the cached entry is **reused**.

## Where things are

- Consumer project: `/private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/39a3092a-b698-4c75-b79e-88fd2e465eed/scratchpad/spike/consumer/`. It is a sandbox-local git repo, left at its baseline commit f90cb60 with local.properties holding only sdk.dir.
- Logs for every run: `.../spike/consumer/_evidence/*.log`, plus the helpers `g.sh`, `apk.sh` and `dsl.py`.
- M17 test copy: `.../spike/consumer/_m17/`. Its results are in `_m17/src/codegen-gradle/build/test-results`, and the run log is `_m17/m17.log`.
- JDK: `/Applications/Android Studio.app/Contents/jbr/Contents/Home` (JBR 21.0.9). SDK: `/Users/erkutbas/Library/Android/sdk`, build-tools 36.0.0.

## Düzeltme turu 1 (X1)

# X1: the plugin in a library module — what went wrong, the fix, and the spec change

## Verdict

**The spec is the problem, not the plugin or the test setup.** The build-type-name rule (B.6), and in fact all of spec B, only work for build types declared in the module that applies the plugin. A library can't compile an environment for a build type it doesn't have. I did not fake it. The prototype now says so loudly instead of shipping the wrong environment silently. The spec needs one added rule, given at the end.

## Why it happens (measured)

- **The library has no variant for the app's extra build types.** With the plugin only in `:mylib`, `:mylib:tasks --group palbase` lists just `generatePalbaseDebug` and `generatePalbaseRelease`.
- **AGP swaps in the library's debug variant.** `:app:dependencyInsight --configuration featureXRuntimeClasspath --dependency :mylib` shows `Variant debugRuntimeElements … BuildTypeAttr | debug | featureX`. `matchingFallbacks` puts `:mylib`'s debug output, including its generated client and `palbase-config.json`, into the featureX APK.
- **The library never learns which app build type pulled it in.** No Palbase setting can reach a variant that doesn't exist, whether it's `palbase.env.featureX`, the DSL, or the build-type name.
- **It isn't a test-setup mistake.** Keeping the generated client in a library module is a normal Android architecture.

Reproduced with the unchanged prototype:
```
== X1-repro-before: EXIT=0
Palbase: debug → main (palbase.env.debug in gradle.properties)
app-featureX.apk "base_url":"https://main.example.com"
app-featureY.apk "base_url":"https://main.example.com"
```

## Fix in the prototype (sandbox commit `80fdab5` in `proto/src`)

It is a **warning, not a failure**. The library task that builds `debug` also serves the app's correct `debug` build, so failing it would break a good build. Failing only the featureX build would mean wiring tasks across projects or hooking the task graph. I judged that too invasive and did not build it.

- **New `LibraryFallbackCheck.kt`:**
  - Only runs when the plugin is in a library.
  - After all projects are configured, it finds the app modules that depend on the library, directly or through other modules.
  - For each app build type B the library lacks, it takes the first `matchingFallbacks` entry the library has. It compares the environment that fallback compiled with what the same resolver says B would select.
  - If they differ, it logs a warning. It also adds a note to the fallback's own generation line, so the note still shows when the configuration cache is reused (the warning doesn't).
  - It stays quiet when there's no usable fallback (AGP already fails loudly then), when the fallback is itself refused, and when both answers agree.
  - `palbase.env.<B>=<fallback env>` makes them agree, which is how a team says "featureX uses main on purpose".
- **Isolated Projects:** the plugin class now takes an injected `BuildFeatures`, and the check is skipped when Isolated Projects is on, because it reads another project's model.
- **Other changes:**
  - `GeneratePalbaseTask` has a new `@Internal alsoPackedInto: ListProperty<String>`.
  - `PalbaseCodegenPlugin` is now an `abstract class` with an `@Inject` constructor.
  - `AndroidVariantIntegration` records each build type's name and resolution.
  - README and CHANGELOG (in Turkish, like the rest of that file) have a section on the library rule.

## Results after the fix

**X1, plugin only in `:mylib`**, same APKs as before, now loud:
```
Palbase: `:app` build type `featureX` is not declared in `:mylib`, so AGP packs `:mylib`'s `debug` build into it (matchingFallbacks) — … `main` for `debug` (palbase.env.debug in gradle.properties). `featureX` itself would select `featureX` (from the build type name) … Declare it in `:mylib` too (`buildTypes { create("featureX") { initWith(getByName("debug")) } }`), or, if `main` is what `featureX` should use, say so: `palbase.env.featureX=main` in gradle.properties.
Palbase: debug → main (palbase.env.debug in gradle.properties) — ALSO packed into `:app` featureProfileUpdate, `:app` featureX, `:app` featureY (matchingFallbacks), which would select another environment: declare those build types in this library
```

| Check | Result |
|---|---|
| Configuration cache | First run: "Configuration cache entry stored." with the warning. Reused run: "Configuration cache entry reused." The warning is gone but the `ALSO packed into` line still prints. |
| Adding `palbase.env.featureX=main` | Warnings only for featureProfileUpdate and featureY. The featureX warning is gone. |
| X1b: `create("featureX")` added to `:mylib` | `Palbase: featureX → featureX (from the build type name)`. featureX APK carries `https://featurex.example.com`. featureY still warns. |
| Isolated Projects (`-Dorg.gradle.unsafe.isolated-projects=true`) | Check skipped: the `ALSO packed into` suffix is gone. The only two problems reported come from `codegen-engine/build.gradle.kts:51` (`rootProject.layout`). That file is identical to the real repo and unchanged since the baseline commit. |
| Regression: normal consumer, plugin in `:app` | EXIT=0, zero warnings. debug and release carry main; featureX, featureY and feature-profile-update each carry their own. |
| AGP 9.1.1 / Gradle 9.3.1, original consumer | EXIT=0. debug is `127.0.0.1`, featureX is `featurexref`, release is `8bbwb2pbm`. |
| AGP 9.1.1, new library copy `consumer-agp9-lib` | Warning and the `ALSO packed into` line both appear. After `create("featureX")` in `:lib`, the featureX APK carries `https://featurexref.palbase.studio`. |

**Unit tests** (`../gradlew test --rerun-tasks` in `proto/src/codegen-gradle`): EXIT=0, "BUILD SUCCESSFUL in 57s".
- `LibraryFallbackCheckTest`: 8 tests, 0 failures (new).
- `PalbaseCodegenPluginTest`: 47 tests, 0 failures. That is the previous 46 plus a new two-module TestKit test covering the warning, the line after configuration-cache reuse, and the acknowledgement.
- `EnvironmentResolverTest`: 23 tests, 0 failures.
- codegen-engine: 46 tests, 0 failures.

**Mutation check:** turning the check off (`&& false`) makes the new TestKit test fail with `AssertionFailedError at PalbaseCodegenPluginTest.kt:761`.

**Real repos are untouched:** palbase-cli (`20e5d7e`), palbackend-android-src (`e72f704`), palbackend-android (`d840817`) and palbe-trial-android (`acedb85`) each report 0 changes. The consumer's tracked files are back at baseline `f90cb60`.

## What the spec should say (add to B)

> Resolution is per variant **of the module that applies `io.palbase.codegen`**. Apply it in the module whose code uses the client, and declare in that module every build type that should compile its own environment. If that module is a library, an app build type the library lacks gets the library's `matchingFallbacks` variant and that variant's environment. No rule can change this, because AGP never creates that variant in the library. The plugin **warns** and doesn't fail, since the same library task also serves correct builds. It warns at configuration time and adds a note to the fallback's generation line. To silence it, either declare the build type in the library or set `palbase.env.<appBuildType>=<fallback's environment>`. This check is not done under Isolated Projects. Applying the plugin to both the app and a library is not supported (the generated package ends up in both).

## Still open / not tested

- **Warning, not failure.** This goes against the plan's "never fall back" principle. A featureX APK built through a library still carries the fallback's environment, just no longer silently. If you want a hard failure, the options are failing the fallback task, which also breaks the app's debug build, or cross-project task wiring, which breaks Isolated Projects. Neither is implemented.
- **The configuration-time warning disappears when the configuration cache is reused.** Only the generation-line note remains, and that too is hidden when the task is UP-TO-DATE.
- **Flavors in the app are approximated.** For an app build type, the check only reads the `palbase.env.<buildType>` keys, not `palbase.env.<appVariant>` ones. So a flavor-specific key like `palbase.env.freeFeatureX` doesn't count. It also doesn't read the app's own `palbase { environment }` DSL. That DSL only exists when the app applies the plugin too, which is an unsupported layout anyway.
- **Consumer types not covered:** `com.android.dynamic-feature` and `com.android.test` consumers aren't checked.
- **The AGP types must share a class loader.** If the app loads AGP in a different class loader from the library, the check silently can't read the app's build types.
- **Not tested:** Android Studio sync, a device, release/R8 in the library layout, and a library flavor setup in a real consumer (it's only unit-tested).
- **Also still open:** the earlier known gaps, such as `palbase link` not yet writing a release choice.

## Files
- `/private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/39a3092a-b698-4c75-b79e-88fd2e465eed/scratchpad/spike/proto/src/codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt`
- `…/spike/proto/src/codegen-gradle/src/main/kotlin/io/palbase/gradle/{AndroidVariantIntegration.kt, GeneratePalbaseTask.kt, PalbaseCodegenPlugin.kt, EnvironmentResolver.kt}`
- `…/spike/proto/src/codegen-gradle/src/test/kotlin/io/palbase/gradle/{LibraryFallbackCheckTest.kt, PalbaseCodegenPluginTest.kt}`
- `…/spike/proto/src/{README.md, CHANGELOG.md}`
- Logs:
  - consumer runs: `…/spike/consumer/_evidence/{X1-repro-before, X1-after, X1-after2, X1-after2-reuse, X1-cc1, X1-cc2, X1-ack, X1b-after, X1-ip, REG-after}.log`
  - X1 build files: `…/spike/consumer/_evidence/x1files/`
  - AGP 9 runs: `…/spike/proto/{consumer-agp9-after, consumer-agp9-lib, consumer-agp9-lib-b}.log`
  - tests: `…/spike/proto/{test-final3, fb-mut}.log`
- New AGP 9 library consumer: `…/spike/proto/consumer-agp9-lib/`

## Eleştiri — review:agp-breaker

### [blocker] When the plugin sits in a library module, the app's custom build type silently gets the library's release environment

**Senaryo.** `:lib` (com.android.library, a module type the plugin explicitly supports) applies io.palbase.codegen. `:app` does not, and declares `create("staging") { initWith(debug); matchingFallbacks += "release" }`. gradle.properties sets `palbase.env.release=prod` and `palbase.env.staging=staging`. Running `./gradlew :app:assembleStaging` builds `:lib:generatePalbaseRelease`. The staging APK ships the PROD config, and the lib's Kotlin is compiled against the prod contract. `palbase.env.staging` has no effect: the library has no staging variant, and step 6 (build-type name) never sees 'staging'. The only hint is a log line that reads `release → prod`.

**Kanıt.** Real 2.3.0 plugin, `:app:assembleStaging -m` → `:lib:generatePalbaseRelease SKIPPED` (dry run). Proto 2.4 snapshot, fixture review-agp/t2: `> Task :lib:generatePalbaseRelease` / `Palbase: release → prod (palbase.env.release in gradle.properties)`. Then `unzip -p app-staging.apk assets/palbase/palbase-config.json` → `APK asset base_url = https://prod.envprobe.palbase.studio`. AGP picks the library variant through matchingFallbacks, so no per-module rule inside the library can know the consuming app's build type.

**Düzeltme.** Decide how libraries work before shipping. Options: (a) the application module owns environment selection and the config asset; a library variant writes an environment stamp (e.g. an asset `palbase/palbase-env-<module>.json`), and the app-level task FAILS when a merged library stamp differs from the app variant's environment. (b) In com.android.library, refuse per-build-type resolution unless the library declares the same build types as its app, and say so in the refusal. At minimum, document the rule and add a TestKit case for app-staging + lib-with-fallback.

### [major] Product flavors are ignored, and the release refusal steers users to the setting that makes stagingRelease ship prod

**Senaryo.** The usual Android idiom picks the environment with flavors: `flavorDimensions("env"); productFlavors { staging; prod }`. The user follows the refusal text and sets `palbase.env.release=prod` (or `release { palbase { environment = "prod" } }`). `stagingRelease` then compiles PROD, and stagingDebug/prodDebug both compile `local`. No step of the plan reads a flavor name, so a flavor called `staging` never selects `palbase/environments/staging`.

**Kanıt.** Proto fixture t1: `Palbase: stagingRelease → prod (palbase { environment } in the \`release\` build type)`, asset `ASSET app stagingRelease base_url = https://prod.envprobe.palbase.studio`. Refusal for the unconfigured flavored variant: "`release` (variant `stagingRelease`) has no environment ... Commit the choice ... `palbase.env.release=<environment>` in gradle.properties". AGP API supports the fix: `DslExtension.Builder.extendProductFlavorWith` / `VariantExtensionConfig.productFlavorsExtensions` exist in 8.10.1 and 9.3.2. Probe review-agp/flavdsl shows `productFlavors { create("staging") { palbase { environment = "staging" } } }` works with the same import: `PROBE variant=stagingRelease buildType=release ... dsl=prod flavorDsl=[staging]`.

**Düzeltme.** Add a flavor level: `palbase.env.<flavor>` plus a `palbase { environment }` on product flavors (extendProductFlavorWith). Refuse when the flavor and build type both name different environments. Variant-level keys should beat both. At minimum, in a flavored module refuse `palbase.env.<buildType>` unless every variant resolves the same way, and change the refusal text to suggest the variant keys.

### [major] The README's CI flag `-Ppalbase.env=X` on the command line is silently outranked by committed per-build-type keys

**Senaryo.** A CI job follows the README ('a CI job passes the environment it releases': `./gradlew assembleRelease -Ppalbase.env=staging`), while gradle.properties commits `palbase.env.release=prod`. Legacy `palbase.env` is step 7, below steps 3-6, so the release build compiles PROD although the command line explicitly asked for staging. For a custom build type `qa`, step 6 wins and the explicit flag is ignored (loud there, because `qa` has no directory).

**Kanıt.** Proto fixture t3, `-Ppalbase.env=staging`: `Palbase: debug → staging (palbase.env, the 2.3 global property)` while `ASSET app release base_url = https://prod.envprobe.palbase.studio`. On the rerun the release task was UP-TO-DATE, so no release line was printed at all. For qa: "environment `qa` (from the build type name) has no directory". README.md lines 94-106 document `-Ppalbase.env=main` as the per-build or CI selector.

**Düzeltme.** Treat `palbase.env` given on the COMMAND LINE (gradle.startParameter.projectProperties) as step 1, right after the per-variant and per-build-type CLI keys. Alternatively, FAIL when it is present on the command line but a lower step chose something else, naming `-Ppalbase.env.<buildType>`. Only a legacy `palbase.env` from a file should rank at step 7.

### [major] Step 4 ('gradle.properties') mixes CI and user overrides with committed values and ignores module-level files

**Senaryo.** (1) CI sets `ORG_GRADLE_PROJECT_palbase.env.release=staging` (or `-Dorg.gradle.project...`, or the user's ~/.gradle/gradle.properties), and the build type commits `release { palbase { environment = "prod" } }`. The build refuses with a false message saying gradle.properties contains palbase.env.release=staging, though the checkout has no gradle.properties. (2) Without the DSL, the env-var value silently beats the committed gradle.properties value and is still labelled 'in gradle.properties'. (3) `palbase.env.debug=main` in app/gradle.properties (module-level) is ignored and debug silently builds `local`. (4) A typo such as `palbase.env.debgu=main` is silently ignored.

**Kanıt.** Probe ccprobe: `gradle.startParameter.projectProperties` does NOT contain ORG_GRADLE_PROJECT_ / -Dorg.gradle.project / gradle.properties values, while `providers.gradleProperty` contains all of them (`startParam=null gradleProperty=envvar`). Probe ccprobe2: in :app, `providers.gradleProperty=null project.findProperty=from-app-gradle-properties` on both Gradle 8.13 and 9.5.0. Proto t4: "the `release` build type names two environments in two committed places — ... `palbase.env.release=staging` in gradle.properties", with an env-var-only override; without the DSL: `Palbase: release → staging (palbase.env.release in gradle.properties)`. Proto t5: `Palbase: debug → local (the default for debug)` with app/gradle.properties=`palbase.env.debug=main`; typo run: `generatePalbaseDebug UP-TO-DATE`, asset local.

**Düzeltme.** Define step 4 as the committed root gradle.properties FILE, read via providers.fileContents. Treat a providers.gradleProperty value that differs from that file as an override: rank it with step 1 and label it 'Gradle property (command line / environment / user gradle.properties)'. Refuse `palbase.env.*` in a module-level gradle.properties, or read it explicitly. Enumerate `providers.gradlePropertiesPrefixedBy("palbase.env.")` (verified to list `palbase.env.relase=typo`) plus the local.properties keys, and FAIL on keys that name no variant or build type.

### [major] A forgotten local.properties override outranks the committed release choice and ships a loopback/cleartext release

**Senaryo.** A developer sets `palbase.env.release=local` in local.properties to test a minified build and forgets it. The committed `palbase.env.release=prod` is overridden (step 2 beats step 4), so `assembleRelease`/`bundleRelease` on that machine produce a release APK pointing at http://127.0.0.1 with the cleartext network-security-config. That defeats the plan's goal that a release never gets an environment nobody chose.

**Kanıt.** Proto fixture t14: `Palbase: release → local (palbase.env.release in local.properties)`, then `unzip -p app-release-unsigned.apk assets/palbase/palbase-config.json` → `release APK base_url = http://127.0.0.1:54321`, BUILD SUCCESSFUL (only a WARN about 127.0.0.1). The AGP 8.10.1 API exposes `com.android.build.api.variant.Component.getDebuggable()` (javap), so the plugin can tell debuggable variants apart.

**Düzeltme.** For non-debuggable variants (variant.debuggable == false): refuse a loopback/http base_url, and either ignore local.properties overrides or require them to be repeated on the command line. At minimum, turn the origin into a WARN when a non-debuggable variant is resolved from local.properties.

### [major] Root discovery (A): a stale module copy silently shadows the root, and RN/Flutter-style layouts still produce a silent no-op

**Senaryo.** (1) `palbase link` writes to os.Getwd(), and PlaneOf() accepts app/ because app/build.gradle.kts exists. Running link once from app/ leaves app/palbase/environments. Module-first discovery then uses that stale copy forever, even after a fresh link at the root. (2) RN, Flutter or Capacitor layout: palbase/ at the repo root, Gradle root at repo/android. Neither candidate exists, the build is green, and the APK has no palbase config. Nothing is logged, so it fails only at runtime. The CLI itself names 'an RN app has web and android side by side' (layout.go).

**Kanıt.** CLI source: internal/backend/link_artifacts.go:141 `root, err := os.Getwd()`; planes.go:58 accepts any dir with build.gradle(.kts). Proto t6: `Palbase: debug → local (the default for debug)` → `ASSET app debug base_url = https://STALE-module-copy.envprobe.palbase.studio`; the line does not say which root was used. Proto t8 (`<repo>/palbase/environments`, `<repo>/android/settings.gradle.kts`): `> Task :app:generatePalbaseDebug`, `BUILD SUCCESSFUL`, `APK palbase entries: 0`.

**Düzeltme.** FAIL when both module and root candidates exist, naming both. Otherwise print the chosen root in the lifecycle line. Also search upward from rootDir to the checkout root (the directory holding palbase/project.json, bounded by .git). When nothing is found, log a lifecycle line listing the searched paths, not a silent return.

### [major] Release refusal-by-default breaks `./gradlew test`, `check` and `build` for every project that has not chosen a release environment

**Senaryo.** Two cases: a 2.3 consumer with no `palbase.env`, or a fresh checkout linked only to `local`, upgrades to 2.4. `./gradlew test` (the usual PR CI command) fails, because testReleaseUnitTest compiles the release variant and so runs generatePalbaseRelease, which refuses. assembleDebug and IDE sync keep working, because the refusal is deferred to task execution.

**Kanıt.** Real 2.3.0 plugin, `-m` task graphs: `./gradlew test → :app:generatePalbaseDebug :app:generatePalbaseRelease`; same for `check` and `build`; `lint`/`connectedCheck`/`assembleDebug` → debug only. Proto t9: `assembleDebug` → BUILD SUCCESSFUL; `test` → "Execution failed for task ':app:generatePalbaseRelease'. > Palbase: `release` has no environment ..." BUILD FAILED.

**Düzeltme.** Accept this breakage deliberately and communicate it: a changelog migration note, and `palbase link` offering to write `palbase.env.release=<env>` into gradle.properties. Keep the refusal inside the task (the proto does; verified it does not break assembleDebug). Do not move it into onVariants: every variant is configured on every build, as shown by probe output listing debug/release/featureX during `help`.

### [minor] Kotlin DSL without the import silently binds the inner `palbase {}` to the PROJECT extension

**Senaryo.** Without `import io.palbase.gradle.palbase`, `debug { palbase { ... } }` resolves to the project-level `palbase` accessor. `environment = "x"` then fails with a misleading error ('Function invocation environment(...) expected' plus exec-task candidates). Any property the project extension has compiles and applies to ALL variants: `debug { palbase { packageName.set(...) } }` changed the package for release too. If `environment` were ever added to the project extension, syntax (a) would compile and apply one environment globally.

**Kanıt.** Probe kts-a (AGP 8.10.1/Gradle 8.13, AGP 9.3.2/Gradle 9.5.0): `Line 7: debug { palbase { environment = "dbgA" } } ^ Function invocation 'environment(...)' expected`. Probe kts-a-pkg: `PROBE variant=release ... projectPkg=set.inside.debug.block`. Probe kts-a-projenv (project extension given `var environment`): compiles, `dsl=null projectEnv=fxA` for debug, release and featureX.

**Düzeltme.** Add a trap on the project extension: `@Deprecated("...add import io.palbase.gradle.palbase...", level = DeprecationLevel.ERROR) var environment: String?`. Verified in plugin-trap: without the import the compile error prints the exact guidance ('Using environment: String? is an error. The environment is chosen per build type: ... add import io.palbase.gradle.palbase'). With the import, (b) still works (dsl=dbgA/fxA), and Groovy is unaffected (dsl=dbgG/fxG/fyG). Never add a real `environment` to the project extension.

### [minor] The only record of the resolved environment is a log line that disappears on UP-TO-DATE, FROM-CACHE and configuration-cache hits

**Senaryo.** The task is @CacheableTask, and the 'Palbase: <variant> → <env> (<origin>)' line is printed from the task action. On incremental, cached or CC-reused builds nothing is printed. This includes the t3 build where debug went to staging and release silently to prod, and the t5 typo build.

**Kanıt.** t3 rerun printed only `Palbase: debug → staging ...` (release was UP-TO-DATE). t5 typo run: `> Task :app:generatePalbaseDebug UP-TO-DATE`, no line. t9 CC rerun: `Reusing configuration cache.` + `generatePalbaseDebug UP-TO-DATE`, no line.

**Düzeltme.** Put the environment and origin into the outputs, e.g. a `palbase_environment` field in the packed asset and/or a generated `PalbaseGenerated.ENVIRONMENT` constant, so it can be checked in the APK. Keep the log line as a convenience.

### [minor] Naming conventions: a plain `benchmark` build type and AGP-reserved names

**Senaryo.** The pre-baseline-profile Macrobenchmark template creates `benchmark` (initWith release, matchingFallbacks=[release]). Step 5 needs `benchmark<X>`, so this resolves to env `benchmark` and refuses. Environments named test*, androidTest* or lint can never be chosen by build-type name, because AGP rejects those build types. `main` and `local` are accepted.

**Kanıt.** Proto t11: `Palbase: benchmarkRelease → prod (..., as \`benchmarkRelease\` builds as \`release\`)`, and `generatePalbaseBenchmark FAILED: environment \`benchmark\` (from the build type name) has no directory`. AGP 8.10.1 probes: 'BuildType names cannot start with 'test'' (test, testing, testFixtures), 'cannot start with 'androidTest'', 'BuildType names cannot be lint'; `main` → `PROBE variant=main buildType=main`, `local` → `PROBE variant=local`.

**Düzeltme.** In step 5, also map a build type whose DSL `matchingFallbacks` is non-empty and that has no own setting to its first fallback (read in finalizeDsl), or document it. Document that test*/androidTest*/lint environments need a key.

### Karar

## Verdict: not 100% safe as written. Do not ship 2.4 on this plan unchanged.

The core mechanism works. The plan still has one blocker and six major gaps: in each, a variant silently compiles the wrong environment, or a normal project breaks. All were reproduced against real AGP builds, and most also against a snapshot of the proto 2.4 implementation (copied at 16:45 into `review-agp/proto-snap`, so it may have changed since).

### DSL syntax question (tested on AGP 8.10.1 + Gradle 8.13 and AGP 9.3.2 + Gradle 9.5.0)
| Syntax | `debug { }` | `create("featureX") { }` |
|---|---|---|
| (a) KTS `palbase { environment = "x" }`, no import | **fails to compile** (see note) | **fails to compile** |
| (b) KTS, plus `import io.palbase.gradle.palbase` | works: `dsl=dbgA` | works: `dsl=fxA` |
| (c) KTS `extensions.configure<io.palbase.gradle.PalbaseBuildType> { ... }` | works: `dsl=dbgC` | works: `dsl=fxC` |
| Groovy `palbase { environment = 'x' }`, no import | works | works, for both `create('featureX')` and `featureY {}` |

Note on (a): the inner `palbase {}` binds to the **project** extension. It only fails because that extension has no `environment`. `packageName.set(...)` written inside `debug {}` compiled and changed every variant. The fix is the `@Deprecated(level = ERROR)` trap described in the issues, which I verified gives the exact guidance.

The proto's Groovy form and its `Property<String>`-based (b) also work. The recommended answer is (b), with (c) as the no-import alternative; the gradle.properties fallback is not needed.

### What held up (verified)
- **Configuration cache:**
  - A `-P` change invalidates it ("the value of 'palbase.env.release' was changed").
  - A root `palbase/` created after the first run is picked up.
  - A `local.properties` edit invalidates it ("file 'local.properties' has changed").
- **Isolated projects:** `rootProject.layout` is a violation ("Project ':app' cannot access 'Project.layout' functionality on another project ':'"). `rootDir` and `isolated.rootProject` are clean, and the proto uses `rootDir`.
- **Deferred refusal:** refusing inside the task keeps `assembleDebug` and IDE sync alive.
- **Test components:** they get no generate task. `testReleaseUnitTest` reuses `generatePalbaseRelease`, which is correct but causes issue 7.
- **Plugin order:** applying the palbase plugin before AGP, or after the `android {}` block, still works.
- **AGP 9.3.2:** the proto resolves correctly (debug → local, release → prod, staging → staging).
- **`variant.buildType`:** it is `@Nullable` in the API, but unreachable here, because the plugin only hooks `com.android.application` and `com.android.library`. The proto's `?: variant.name` fallback would be wrong if the KMP library plugin were ever hooked.

### Must-fix before 2.4
1. **Library modules:** decide library semantics (issue 1).
2. **Flavors:** add a flavor level (2).
3. **Command-line `-Ppalbase.env`:** move it to step 1 (3).
4. **Override sources:** separate the committed gradle.properties file from Gradle-property overrides, and detect typo keys (4).
5. **Non-debuggable variants:** do not let `local.properties` or loopback configs reach them (5).
6. **Root discovery:** FAIL on an ambiguous or missing root instead of a silent no-op (6).
7. **Release refusal:** plan the migration for the `./gradlew test` breakage it causes (7).

### Not tested (said explicitly)
- Android Studio sync and the Build Variants picker.
- The real `androidx.baselineprofile` plugin (not in the offline cache); its naming was simulated with hand-made build types.
- Dynamic-feature modules (the plugin does not hook them; source only).
- Multiple app modules sharing a root config that has an FCM `package_name`.
- Duplicate generated classes when both lib and app apply the plugin (needs a Kotlin and palbe compile).
- AGP older than 8.10.1 (only 8.10.1, 8.11.1, 9.1.1 and 9.3.2 are cached).

### Where things are
- All work is in `/private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/39a3092a-b698-4c75-b79e-88fd2e465eed/scratchpad/spike/review-agp/`:
  - probe plugin in `plugin/`, `plugin-trap/` and `plugin-flavor/`
  - fixtures `t1`–`t15`, `libfb`, `single`, `kts-*`, `agp9-*`, `btn-*`
  - Gradle/property probes `ccprobe*` and `ipprobe`
- No real repo was modified; `git status` is clean for all four.

## Eleştiri — review:flow-breaker

### [blocker] debug → "local" default fails the first build: link almost never writes a usable local/

**Senaryo.** The four setups below all end in a failing debug build under plan B.8:
(a) Zero-project user, cloud only (login → project create → link): no local/ is written, so debug fails.
(b) The user ran `palbase start` in the backend checkout (linked to project "myapp"), then `palbase link myapp` in ~/AndroidStudioProjects/MyApp. The stack is registered under group "myapp", but link looks it up under "myapp" derived from the APP DIRECTORY name → no local/ → debug fails.
(c) The stack is registered but stopped, or this machine has no credential: local/android-config.json is committed with api_key "" and no openapi.json → debug fails with "Run `palbase link`".
(d) Monorepo: `palbase start` then `palbase link` writes the stack as main/, `palbase spec` writes local/openapi.json only, and debug fails.

**Kanıt.** Probe L2 (real binary 0.71.2, isolated HOME):
- stack registered under "todoapp" → link wrote only `main`
- stack registered under "myapplication" (checkout dir) → `wrote palbase/environments/local/android-config.json`

Probe L3: "local: http://127.0.0.1:18767 is registered but this machine holds no credential …"; local/android-config.json has "api_key": "". Then probe G-D: "Palbase Android inputs are incomplete for environment `local`. Run `palbase link`…"

Probe L6 (simulated start record): link printed `wrote palbase/environments/main/android-config.json`; spec printed `✓ wrote palbase/environments/local/openapi.json`; probe G-I then failed with the same incomplete-inputs error.

Probe G-B: "environment `local` has no directory … this checkout carries main".

Code:
- project_link.go:837 builds the target with no Name/Project
- app_environments.go:656 LookupLocalStack(groupOf(primary)), and :693-714 groupOf falls back to the checkout's base name
- start.go:670-684 groupName uses the linked project's name
- project_link.go:876-882 linkedEnv=soleEnvName "main" for a start or loopback stack
- stack_spec.go:103-106 writes "local" for the same stack
- app_environments.go:664, 672 write keyless entries
- social_link.go:293-299 passes keyless non-default entries through to the write

**Düzeltme.** CLI:
- In runLinkPrepared, set target.Name/Project from o.product (or look up by product name) so groupOf matches start's groupName.
- Name the start/loopback stack `local` in link (followStart and loopback paths), matching spec.
- Never write a local/ that has no key or no contract, or write both files or neither.
- Consider gitignoring palbase/environments/local/: it carries this machine's port.

Plugin:
- When debug resolves to a missing or incomplete local/, the error must list the environments that exist and give the exact fix line (`palbase.env.debug=main` in gradle.properties or local.properties), plus `palbase start && palbase link`. Never say "Run palbase link" when link cannot help.
- Decide explicitly whether debug should default to the checkout's default environment when local/ is absent. As written, every cloud-only user fails on the first Run.

### [blocker] Environment names are unvalidated and become filesystem paths: path traversal outside the checkout and the link stage

**Senaryo.** Any org member creates an environment named `../../../../../../../../../tmp/pwned` (37 chars, allowed by `z.string().min(1).max(64)`) through `palbase env create`, the panel, or a panel rename. Every teammate's next `palbase link` in an app checkout then:
- writes android-config.json, openapi.json and web-config.json (and on Apple, Palbase-Info.plist and PalbaseGenerated.swift) to /tmp/pwned/;
- with `../../gradle`, writes straight into the REAL checkout's gradle/ through the stage's symlink, bypassing staging and rollback;
- with `../../app/src/main/assets`, writes into app sources, and those are published because `app` is in the mutable set.

**Kanıt.** CONFIRMED by code reading plus a path emulation. Go is not installed on this machine, so I emulated `path.Join` with Python's posixpath.normpath, which has the same lexical semantics; the emulation output is below. I did not drive the real binary with hostile names: cloud names need the cloud, and TenantHost cannot be overridden.

Code chain:
- layout.go:60 EnvDir = path.Join("palbase","environments",env), with no check
- layout.go:79-81 ConfigPath
- app_environments.go:103-113 MkdirAll + WriteFile relative to cwd = stage
- link_artifacts.go:136-138 stage = os.MkdirTemp("", …) in $TMPDIR
- link_artifacts.go:227-231 non-mutable top-level dirs are SYMLINKED into the stage
- link_artifacts.go:195 mutable includes "app"

Server side: cloud-lifecycle.ts:58-63 CreateEnvironmentBody name z.string().min(1).max(64); panel.ts:405-409 and :243 (rename) are the same; cli.controller.ts:474-491 environmentSlug returns the trimmed name verbatim.

Emulation output:
- '../../gradle' → 'gradle/android-config.json'
- '../../../../../../../../../tmp/pwned' → abs=/tmp/pwned/android-config.json

**Düzeltme.** Server:
- Enforce a slug at create and rename, e.g. ^[a-z][a-z0-9-]{0,38}$ (the studio already defines environmentSlugSchema in services/environment.ts).
- Make it unique per product, case-insensitively.
- Reserve local, main, debug, release, lint, test*.
- Store the slug separately from the display name, and have the CLI listing return the slug.

CLI: EnvDir/ConfigPath/SpecPath must refuse any name that is not exactly one clean segment (no '/', '\\', '..', leading '.', no NUL, and not equal to itself after path.Clean). The refusal happens before any write, and the environment is reported as skipped.

Plugin: the same one-segment check (already in the plan).

### [major] Name-to-directory mapping is not injective or stable: duplicate `main`, case twins, rename flips, `local` clash

**Senaryo.** (1) Product "todoapp": its first environment's row is named "todoapp" and is listed as "main". A user runs `palbase env create main`, and two environments are now both "main". In gatherEnvironments the second one overwrites the first (map keyed by name), so palbase/environments/main/ carries environment #2's URL and key. But `palbase push --env main` picks environment #1 (first EqualFold match). The app and the push target diverge.
(2) The product is renamed in the panel. The first environment's name no longer equals the product name, so its directory flips from main/ to todoapp/, and `palbase.env.release=main` keeps building the stale main/.
(3) Environments `Staging` and `staging` exist: on APFS they are one directory holding the last writer's config.
(4) A cloud environment named `local` is overwritten by this machine's stack entry.
(5) An environment renamed Staging→staging: the Apple sweep compares `wanted[e.Name()]` exactly, sees the on-disk "Staging" as unwanted, and RemoveAll deletes the directory that was just written. By reading; an Android sweep copied from it inherits this.

**Kanıt.** cli.controller.ts:100 and :474-491 (index 0 && name == productName → "main", otherwise the trimmed name)
panel.controller.ts:357-380 (product rename updates only cloud_products.name) and :600-610 (environment rename)
app_environments.go:642 envs.Environments[name]=… (last wins); :664-684 local overwrites the key
environments.go:328-335 resolveNamed takes the first EqualFold match
app_environments.go:162-223 removeStaleEnvironmentDirs uses exact-case `wanted`

FS probe (real APFS): writing Staging and then staging produced one directory `Staging` containing {"env":"staging"}.

**Düzeltme.** The server slug from the path-traversal blocker must be unique case-insensitively, immutable, and independent of the product name. Drop the "index 0 == product name → main" heuristic and store `main` as the first environment's slug at creation.

CLI: refuse a link when two environments map to the same directory (case-folded). Make the sweep compare names case-insensitively on case-insensitive filesystems and never delete a directory that is being written in the same run.

### [major] Plan step B.6 (build-type name = env) runs before B.7 (legacy global): breaks 2.3 consumers, including the user's own test project

**Senaryo.** The user's zero-project (/Users/erkutbas/AndroidStudioProjects/MyApplicationPalbaseAndroidSdkTest) has AGP 9.1.1, build types featureX and featureY, `palbase.env=main`, and only palbase/environments/main/. With 2.3.0 all variants build against main. Under 2.4:
- featureX resolves to featureX/ (B.6) before the global palbase.env=main (B.7) is consulted, so it FAILS.
- `./gradlew build` runs generatePalbase for every variant, so the whole build fails.
- Any 2.3 consumer with a custom build type (qa, staging, profile) and a global palbase.env breaks on upgrade.

**Kanıt.** Probe G-J, on a sandbox copy of the user's project (AGP 9.1.1, Gradle 9.3.1, plugin 2.3.0), after changing bare `featureX {` to create("featureX"): `> Task :app:generatePalbaseFeatureX … BUILD SUCCESSFUL`; generated/palbase/featureX/assets/palbase/palbase-config.json exists; `compileFeatureXKotlin` succeeded.

`./gradlew build --dry-run` lists generatePalbaseDebug, FeatureX, FeatureY and Release. On AGP 8.11.1, `./gradlew test --dry-run` also includes generatePalbaseRelease.

The bare `featureX {` in the original file fails to compile: "Unresolved reference 'featureX'".

**Düzeltme.** Either:
- put the legacy global palbase.env ahead of the build-type-name rule (B.6 applies only when no global is set), or
- ship this as a major version with a migration note.

In addition:
- The B.6 failure message must name the three ways out: create the environment, map it with `palbase.env.<buildType>=<env>`, or use the DSL.
- Printed or README snippets must use `create("featureX") { … }` for .kts.
- Decide whether B.8's release refusal should fire during `./gradlew build` / AGP 8 `test` on projects that never release; at minimum the message must give the exact line to add.

### [major] No Android sweep: a deleted or renamed environment keeps building silently against a dead tenant

**Senaryo.** Environment `featurex` is deleted in the panel, or renamed. `palbase link` in an Android-only checkout leaves palbase/environments/featurex/ untouched and says nothing. Under the plan, build type `featurex` (or `featureX` on macOS) still resolves that directory and ships a client pointed at a deleted tenant. The failure only appears at runtime.

**Kanıt.** project_link.go:1072-1083: generateForEnvironmentsAt, which calls removeStaleEnvironmentDirs, runs only `if apple`.

Probe L4 (real binary, Android checkout carrying a stale featurex/): output was `▸ android / wrote palbase/environments/main/android-config.json / linked to … / commit palbase/`, and featurex/ still exists.

Probe G-E: `-Ppalbase.env=featurex` → BUILD SUCCESSFUL, asset base_url `https://deadref00.palbase.studio`.

**Düzeltme.** Run the sweep for every platform that writes per-environment artifacts (android, web). Reuse the existing keep list (every listed environment, including Failed/Deleting ones, plus `local`).
- Make the comparison case-aware (see the name-mapping issue).
- Delete only directories whose files are all CLI-owned.
- Print the removal, and add that any build type mapped to that environment will now fail.

### [major] A release variant can ship a loopback, cleartext config (self-host or started stack is written as main/)

**Senaryo.** The user links a local or loopback stack (`palbase link http://localhost:54321`, or `palbase start` then `palbase link`). The CLI writes it as palbase/environments/main/. Plan B.8 forces the user to map release, they set `palbase.env.release=main` (the only directory there), and the release APK is built with base_url http://127.0.0.1:18765 plus a network-security-config permitting cleartext to 127.0.0.1 and 10.0.2.2. The plugin only warns.

**Kanıt.** Probe L1 (real binary): `wrote palbase/environments/main/android-config.json` with "base_url": "http://127.0.0.1:18765".

Probe G-C: `-Ppalbase.env=main :app:generatePalbaseRelease` → warning "base_url points at 127.0.0.1 …", then BUILD SUCCESSFUL. The release res contains palbase_network_security_config.xml with cleartextTrafficPermitted="true" for 127.0.0.1 and 10.0.2.2.

Code: GeneratePalbaseTask.kt:201-219 (loopback accepted), :343-370 (warn only); project_link.go:876-882 (loopback link → "main").

**Düzeltme.** Plugin: refuse an http/loopback base_url for non-debuggable variants, reading the build type's isDebuggable through the plan's DslExtension or finalizeDsl hook.

CLI: name loopback and self-host stacks `local` (or refuse `main` for a loopback address) so release can never be mapped to them by accident.

### [major] Zero-project flow: link gives Android nothing to act on, and a never-pushed backend yields a misleading build error

**Senaryo.** A fresh Android Studio project runs `palbase link myapp`. The output never mentions Gradle: no repositories, no plugin id or version, no palbe dependency, no serialization plugin, and no build-type mapping. If the backend has not been pushed yet, link writes only android-config.json ("the link is recorded; palbase spec fills the contract in"). The build then fails with "Run `palbase link`", which cannot help; the real cure is `palbase push`.

**Kanıt.** Probe L1 output, in full: `▸ android / remembered this stack's key … / wrote palbase/environments/main/android-config.json / linked to http://127.0.0.1:18765 (project) / commit palbase/`. Apple gets printEnvironmentSelectionSnippet (project_link.go:1032-1047); Android gets nothing. A grep of the CLI finds no `io.palbase.codegen` or `palbase.env` anywhere.

Probe L5: "no contract yet: … nothing is deployed yet … ends with `palbase push`", and palbase/environments/main/ has only android-config.json.

Probe G-H: "Palbase Android inputs are incomplete for environment `main`. Run `palbase link` to write …/openapi.json" (GeneratePalbaseTask.kt:120-127).

Probe G-A (2.3.0, root palbase/, no block): BUILD SUCCESSFUL, with no palbase-config.json in the generated assets.

**Düzeltme.** CLI link, when android is detected, prints:
- the settings repositories
- plugins { id("org.jetbrains.kotlin.plugin.serialization"); id("io.palbase.codegen") version X }
- implementation("io.palbase:palbe:X")
- the environments it wrote, and the build-type mapping each would take (debug→local|missing, release→must set), with the exact `palbase.env.<bt>=` lines

Plugin: when openapi.json is missing but android-config.json exists, say "push a backend first (`palbase push --env <env>`), then `palbase link`".

The plan's root discovery (A) fixes the G-A no-op; keep it.

### [major] Case-insensitive resolution: build type `featureX` finds `featurex/` on macOS and fails on Linux CI

**Senaryo.** Environment slugs are lowercase (the studio slug regex is ^[a-z][a-z0-9-]…); build types are camelCase by convention. Build type featureX with directory featurex/ resolves on the developer's APFS Mac (File.isDirectory is case-insensitive) and passes. CI on Linux, a case-sensitive filesystem, fails with the no-directory refusal, or resolves differently.

**Kanıt.** Probe G-G (real APFS, plugin 2.3.0): `-Ppalbase.env=featureX` with only palbase/environments/featurex/ on disk → BUILD SUCCESSFUL.

Code: GeneratePalbaseTask.kt:98-99 root.resolve(selected).isDirectory.

The Linux half was NOT tested: no case-sensitive volume or container was available.

**Düzeltme.** Resolve by listing root.listFiles() and requiring an exact String match. On a case-only mismatch, fail with "build type featureX found directory featurex; names must match exactly (palbase.env.featureX=featurex)".

### [major] Versioning and release tooling contradict "plugin 2.4 + palbe 2.3.0"; the SDK's own release gate breaks under B.8

**Senaryo.** Plugin-only 2.4 cannot be published: publish.sh ships all 11 artifacts under one PALBE_VERSION and exits unless 11 were copied. The README tells consumers the two must be the same version. Separately, the SDK repo builds consumer-release (release, minified) and lintRelease against sample/palbase/environments, which holds only `local`. Today release defaults to local; under B.8 release is unmapped and the gate fails.

**Kanıt.** palbackend-android-src README.md:65 "The library and the plugin must be the SAME version"; scripts/publish.sh:119-120 "expected 11 artifact directories"; consumer-release/build.gradle.kts:50-52; sample/palbase/environments contains only `local`; README gate `./gradlew check lintRelease :consumer-release:assembleRelease`; settings.gradle.kts:2 includeBuild("codegen-gradle"); PalbaseCodegenPluginTest.kt:49 and :82-89 assert the local default; distribution/README.md:54, 66, 105-117 pin 2.3.0 and document palbase.env plus the environmentsDir block; trial gradle.properties has `palbase.env=main`.

Compatibility itself: `git diff v2.3.0..HEAD` does not touch codegen-engine, shared or codegen-gradle, and the runtime reads only the asset (GeneratedConfigLoader.kt:15). A resolution-only 2.4 therefore generates identical code. I did not build or run a 2.4 plugin.

**Düzeltme.** Release everything as 2.4.0 (the runtime is unchanged) rather than mixing versions, or change the README rule deliberately.

Add `palbase.env.release=local` (or a build-type DSL entry) to the SDK repo for consumer-release and sample, and update the plugin tests, distribution/README.md, the trial app and the user's test project.

### [major] D-036 (env name = git branch name) collides with the one-segment and build-type rules

**Senaryo.** Under D-036, a non-production environment deploys from the branch equal to its slug. Branches are typically `feature/login`. Because the slug is the raw name today, the environment has to be named `feature/login`. The CLI then writes nested palbase/environments/feature/login/ (breaking Xcode's two-level pattern too), the plan's plugin rejects it (one segment), and AGP rejects it as a build type. `test-login`, `testing`, `main` and `lint` can never be build types.

**Kanıt.** decisions.md:656-669 (D-036: "Diğer her environment → kendi `slug`'ı" — every other environment deploys its own slug); cli.controller.ts:474-491 (slug == trimmed name); layout.go:23-33 (flat directory required by Xcode).

Probe G-F (AGP 8.11.1):
- 'feature/x' → "Configuration name … must not contain … /"
- 'test-login' → "BuildType names cannot start with 'test'"
- 'main' → "Multiple entries with same key: main"
- 'Feature X' → "cannot contain whitespace"
- 'feature.x' → "Directory should not contain '.'"
- 'lint' → "BuildType names cannot be lint"

**Düzeltme.** Define one canonical slug grammar that satisfies DNS, the directory, Xcode and AGP: lowercase, [a-z][a-z0-9-]*, not starting with `test`, not main/lint/local/debug/release.
- Add a deterministic branch→slug function for D-036 (feature/login → feature-login).
- Have the server return slug and display name separately; the CLI uses the slug for directories.
- Document that B.6 matches the slug exactly (build type `feature-login` is legal; Kotlin DSL create("feature-login")).

### [minor] doctor and status are blind to Android and point app developers at the wrong knob

**Senaryo.** In an Android checkout of a two-environment project, `palbase doctor` prints ✗ env "has 2 environments and none is selected". The fix it implies (`palbase env use`) does not affect which environment the APK compiles. `palbase status` never checks the Android app key: key drift reads only ios configs. Nothing reports a keyless local/, a missing openapi.json, an unmapped release, or a stale environment directory.

**Kanıt.** cmd/palbase/doctor.go:148-216 (no Android probes) and :248-264 (env line from the resolver); status_project.go:236 and :320 readAppEnvironments("ios") only.

**Düzeltme.** doctor: an Android section that
- lists palbase/environments/* with completeness (both files, non-empty key, x-palbase-roles);
- parses gradle.properties and local.properties palbase.env.* keys and shows the build-type → environment mapping the plugin would use;
- flags an unmapped release, a loopback environment mapped to release, directories not in the project, and case-only collisions.

status: check key drift for every linked platform. For app checkouts, annotate the env line: "verbs only; the app's environment is chosen by its build type".

### [minor] Environments modeled as flavors, applicationIdSuffix and OAuth/FCM package checks are not covered by the plan

**Senaryo.** Many Android apps model environments as productFlavors (dev/staging/prod) and use applicationIdSuffix per build type. The plan keys only on variant and build type (no palbase.env.<flavor>). Once the gradle file contains productFlavors or applicationIdSuffix, link's nativeIdentifiers returns nil, so any environment with an Android OAuth client makes link refuse until project.json carries oauth.android. The plugin's FCM package check fails for suffixed variants.

**Kanıt.** social_link.go:234-259 (nil when androidVariantConfiguration matches); social_link.go:167-172 (refusal "needs application_key and variant"); GeneratePalbaseTask.kt:256-263 (FCM package_name must equal the variant's applicationId). Not reproduced: the fake stack had no OAuth or FCM configured.

**Düzeltme.** Add a flavor-level key (palbase.env.<flavor>, between variant and build type) or document the variant keys. Have link explain the OAuth selection requirement when it sees suffixes or flavors, and document per-environment OAuth/FCM clients for suffixed application ids.

### [minor] `palbase env create` with a space in the name cannot be confirmed interactively

**Senaryo.** `palbase env create "Feature X"` prompts "Type the name to confirm". fmt.Fscanln reads `Feature`, then errors on ` X`, so the command aborts. Only `--yes` gets through. The server accepts the name.

**Kanıt.** internal/env/env.go:233-240 (Fscanln into a single string). By reading; not run, because it needs a linked cloud project.

**Düzeltme.** Moot once the server enforces a slug. Until then, validate the name client-side against the same grammar before prompting.

### Karar

## Verdict: NOT CORRECT END-TO-END as written. The plugin-side resolution order holds up; the flow around it does not.

I found 3 blockers. Each one was either reproduced with the real `palbase` 0.71.2 binary, the real 2.3.0 plugin and AGP 8.11.1 / 9.1.1, or confirmed by reading the code with file:line.
1. **The default `debug → "local"` fails on the first build of the zero-project flow.**
   - `link` writes `local/` only when a `palbase start` stack is registered under the **app checkout's directory name**. `start` registers it under the **project name**, so the two never meet.
   - When `local/` is written, it is often keyless and has no contract.
   - For the same started stack, `link` calls it `main` and `palbase spec` calls it `local`.
2. **Environment names reach the filesystem unvalidated.** This allows path traversal outside the checkout and the link stage, nested directories for `feature/x`, APFS case-twin collapse, and a duplicate `main`.
3. **The CLI change list is incomplete.** Items 1 and 2 need fixes in the CLI and on the server, not in the plugin.

The majors are:
- Resolution step B.6 comes before B.7. That breaks 2.3 consumers, and it breaks the user's own AGP 9 test project.
- There is no Android sweep.
- A release variant can ship a loopback cleartext config.
- `link` prints nothing Android-specific.
- A backend that was never pushed makes the build fail with the wrong advice.
- Case-insensitive resolution on APFS passes on the Mac and fails on Linux CI.
- The version and release tooling contradicts "plugin 2.4 + palbe 2.3.0".

### 1. Zero-project onboarding: the exact sequence today
By reading. I did not run login, create, init, push or a cloud link: no account use and no billable resources.

1. `palbase login`
2. `palbase project create myapp` (internal/project/project.go:130-160)
   - Creates the product and its first environment. That environment's row is named `myapp`, and the CLI lists it as **main** (cli.controller.ts:474-491).
   - Prints `palbase link myapp`.
3. Backend, in its own directory:
   - `palbase init` (npm scaffold)
   - `palbase link myapp`: writes only `palbase/project.json`
   - `palbase push` (needs bun). Without this there is no contract.
4. In the Android Studio root: `palbase link myapp`
   - Detects Android through the literal `applicationId` in `app/build.gradle(.kts)` (planes.go:326-346). A KMP `composeApp/` layout or a non-literal id is not detected, and link then runs backend-only.
   - Writes `palbase/project.json` and `palbase/environments/<every env>/{android-config.json, openapi.json}`.
   - Prints **no Gradle lines**.
5. Hand-wire Gradle: repositories, the serialization plugin, `io.palbase.codegen`, `io.palbase:palbe`. With 2.3 you also need the `palbase{environmentsDir}` block and `palbase.env=main`.

Where it breaks (reproduced):
- **Probe G-A:** without the block, 2.3 is a silent no-op: `BUILD SUCCESSFUL`, and the generated assets directory is empty.
- **Probe G-B:** debug without the property fails with "environment `local` has no directory … this checkout carries main. Run `palbase link` to write it".
- **Probes L5 / G-H:** before the first push, link writes only `android-config.json`, and the build fails with "inputs are incomplete … Run `palbase link`". The fix is actually `palbase push`.
- **Probe L1:** `link` output is just `▸ android / wrote …/android-config.json / linked to … / commit palbase/`.

`local/` is not written without a registered stack. When it is written for a stopped or foreign stack, `api_key` is `""` and there is no `openapi.json` (probes L3 / G-D).

### 2. Environment names
The server accepts any 1–64 characters, with no uniqueness check:
- cloud-lifecycle.ts:58-63
- panel.ts:405-409
- the rename endpoint, panel.ts:243

The directory is the trimmed name, used verbatim (cli.controller.ts:474-491, project_link.go:580-587, layout.go:60). The panel has no slug field.

AGP 8.11.1 legality (probe G-F, real runs):

| Name | Result in AGP 8.11.1 |
|---|---|
| `featureX`, `feature-x`, `feature_x`, `local`, `staging`, `Staging` | Configure fine |
| `test-login`, `testLogin` | Refused: "BuildType names cannot start with 'test'" |
| `main` | Refused: "Multiple entries with same key: main" |
| `lint` | Refused |
| `feature.x` | Refused: "Directory should not contain '.'" |
| `feature/x` | Refused: Configuration name must not contain '/' |
| `Feature X` | Refused: "cannot contain whitespace" |

- **`debug` / `release` as environment names:** never auto-selected under B.6 or B.8.
- **`local`:** clashes with the machine stack (app_environments.go:642 vs 664-684).
- **Case twins:** `Staging` + `staging` collapse into one directory holding the second one's config (FS probe).
- **Path traversal: CONFIRMED by reading plus a path emulation.** Go is not installed on this machine, so I emulated `path.Join` in Python rather than running it. `../../../../../../../../../tmp/pwned` (37 characters) resolves to `/tmp/pwned/android-config.json`. `../../gradle` writes into the real checkout through the stage's symlinked `gradle/` (link_artifacts.go:227-231).
- **D-036 conflict:** D-036 makes the environment name equal the git branch name, and branch names usually contain `/`. That collides with the plan's one-segment rule.

### 3. Lifecycle
- **Confirmed:** the sweep runs only under `if apple` (project_link.go:1072-1083).
- **Probe L4:** a stale `featurex/` survives `link` without a word.
- **Probe G-E:** selecting it builds successfully against `https://deadref00…`.
- **Product rename** flips the first environment's directory from `main` to the old product name (panel.controller.ts:357-380 together with environmentSlug).
- **Environment rename** leaves the old directory behind.

### 4. Versioning
- **Runtime:** it reads only the asset `palbase/palbase-config.json` (GeneratedConfigLoader.kt:15).
- **Generated code:** `git diff v2.3.0..HEAD` does not touch codegen-engine, shared or codegen-gradle, so a 2.4 plugin that only changes resolution generates the same Kotlin. This is structurally compatible, but I did not build a 2.4 plugin.
- **Conflicts with the plan:**
  - README.md:65 says plugin and library "must be the SAME version".
  - publish.sh:119-120 refuses unless all 11 artifacts ship under one version.
  - consumer-release/build.gradle.kts:50-52 points at `sample/palbase/environments`, which has only `local`, and the SDK's own gate runs `lintRelease :consumer-release:assembleRelease`. Under B.8 that gate fails.
- **Must change:**
  - PalbaseCodegenPluginTest.kt:49 and :82-89 (they assert the local default)
  - distribution/README.md:54, 66, 105-117
  - trial app `palbase.env=main` plus the `palbase{}` block
- **The user's AGP 9 test project** (build types featureX/featureY, only `main`, `palbase.env=main`) builds with 2.3.0 (probe G-J). Under 2.4, B.6 fails it. Its bare `featureX {}` does not compile in Kotlin DSL at all.

### 5. CLI change list
Beyond what the plan lists (name validation, Android sweep, printing Gradle lines, doctor checks), the CLI also needs to:
- Fix the local-stack group key (use the product name).
- Name a started or loopback stack `local` consistently across `link` and `spec`.
- Stop committing keyless or contract-less `local/`.
- Validate names on the server with a slug that is unique and case-insensitive, reserving `local`/`main`/`debug`/`release`/`test*`/`lint`.
- Refuse traversal in `EnvDir`.
- Make the sweep case-aware.
- Check the Android config in `status`: key drift reads only `ios`.
- Stop `doctor`'s `env` line pointing Android developers at `env use`.

On the plugin side:
- Refuse loopback in non-debuggable variants.
- Match directory names exactly, not through `File.isDirectory`.
- Improve the error messages.

### Evidence and scope
- **Sandbox:** `/private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/39a3092a-b698-4c75-b79e-88fd2e465eed/scratchpad/spike/review-flow/` (MyApplication, FreshApp, StartApp, Agp9App, stack/fake_stack.py, home/).
- **Setup:** a fake loopback stack in Python, an isolated `HOME`, and the plugin from the file maven repo at /Users/erkutbas/Github_Pallasite/palbackend-android.
- **Real repos:** unmodified (`git status` clean on all five). I started two sandbox-only Gradle daemons with a 15-minute idle timeout and stopped nothing.
- **Not tested:**
  - Go unit tests (no Go toolchain here)
  - a cloud link with hostile names (TenantHost cannot be overridden)
  - `palbase start` (no Docker; I simulated its machine record)
  - Linux CI case-sensitivity
  - the 2.4 DSL syntax question, which belongs to another reviewer's role
  - a compiled 2.4 plugin
