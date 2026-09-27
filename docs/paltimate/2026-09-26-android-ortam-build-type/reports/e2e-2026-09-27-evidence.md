# E2E evidence: io.palbase.codegen 2.5.0 picks the Palbase environment per Android build type

- Date: 2026-09-27 (Mac, Darwin 25.5.0, case-insensitive APFS)
- App: /Users/erkutbas/AndroidStudioProjects/PalbaseBuildTypeE2E (git HEAD d5fedf6 "E2E skeleton")
- Local stack: palbase start, group palbasebuildtypee2e, http://127.0.0.1:62096 (containers palbase-palbasebuildtypee2e-{envoy,runtime,palsvc,postgres} Up)
- Cloud: project penny (proj_69zltx2ym), env main, https://na1m7lt2m.palbase.studio
- CLI: /Users/erkutbas/Github_Pallasite/palbase-e2e-buildtype/bin/palbase (wave 1 build)
- Env for every Gradle run: JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home" ANDROID_HOME=/Users/erkutbas/Library/Android/sdk
- api_key values below are shortened to first 14 chars + "..." (not decisive; everything else is raw).

## Starting state (before any Gradle run)

```
$ git status --short
 M .gitignore
?? palbase/
$ git diff .gitignore   -> +palbase/environments/local/
$ find palbase -type f
palbase/environments/local/android-config.json   {"base_url": "http://127.0.0.1:62096", ...}
palbase/environments/local/openapi.json
palbase/environments/main/android-config.json    {"base_url": "https://na1m7lt2m.palbase.studio", ...}
palbase/environments/main/openapi.json
palbase/project.json                             {"project": "proj_69zltx2ym", "name": "penny"}
$ cat gradle.properties (palbase keys)
palbase.env.release=main
palbase.env.staging=main
```

## Part A — AGP 8.11.1 / Gradle 8.13

### A1 (first attempt) — FAILED on plugin resolution (test-app setup, not the plugin)

```
$ ./gradlew --console=plain :app:assembleDebug :app:assembleRelease :app:assembleStaging :app:assembleFeatureDev
* What went wrong:
A problem occurred configuring project ':app'.
> Could not resolve all artifacts for configuration 'classpath'.
   > Could not find io.palbase:palbase-codegen-engine:2.5.0.
     Searched in the following locations:
       - file:/Users/erkutbas/Github_Pallasite/palbackend-android-src/codegen-gradle/build/test-repository/io/palbase/palbase-codegen-engine/2.5.0/palbase-codegen-engine-2.5.0.pom
       - https://dl.google.com/... - https://repo.maven.apache.org/... - https://plugins.gradle.org/...
     Required by:
         project :app > io.palbase.codegen:io.palbase.codegen.gradle.plugin:2.5.0 > io.palbase:codegen-gradle:2.5.0
BUILD FAILED in 5s
```

Cause: settings.gradle.kts `pluginManagement` listed only codegen-gradle/build/test-repository; the plugin's
module pins io.palbase:palbase-codegen-engine:2.5.0, which lives in palbackend-android-src/build/test-repository
(the T030 AGP 9 reference lists both). Fix: settings.gradle.kts repository blocks replaced with the reference's
(both Test repositories in pluginManagement, io.palbase excluded from google/mavenCentral/gradlePluginPortal).
rootProject.name and include(":app") unchanged.

### A1 — PASS (after the settings fix)

```
$ ./gradlew --console=plain :app:assembleDebug :app:assembleRelease :app:assembleStaging :app:assembleFeatureDev
> Task :app:generatePalbaseDebug
Palbase: debug → local (the default for debug) [palbase/environments]
Palbase: base_url points at 127.0.0.1, which on a device or emulator means THAT DEVICE. Use http://10.0.2.2:<port> from an emulator, or run `palbase start --lan` and use the machine's LAN address from a physical device.
> Task :app:generatePalbaseRelease
Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]
> Task :app:generatePalbaseStaging
Palbase: staging → main (palbase.env.staging in gradle.properties) [palbase/environments]
> Task :app:generatePalbaseFeatureDev
Palbase: featureDev → local (palbase { environment } in the `featureDev` build type) [palbase/environments]
Palbase: base_url points at 127.0.0.1, which on a device or emulator means THAT DEVICE. Use http://10.0.2.2:<port> ...
Unable to strip the following libraries, packaging them as they are: libdatastore_shared_counter.so, libjnidispatch.so, libpalbe_mls.so. (x4, AGP, unrelated)
BUILD SUCCESSFUL in 25s
166 actionable tasks: 166 executed
APKs: app/build/outputs/apk/{debug/app-debug.apk, featureDev/app-featureDev.apk, release/app-release-unsigned.apk, staging/app-staging-unsigned.apk}
```

### A2 — PASS: APK assets + manifests (AGP 8.11.1)

```
$ unzip -p app/build/outputs/apk/debug/app-debug.apk assets/palbase/palbase-config.json
{"app_id":"project","base_url":"http://127.0.0.1:62096","api_key":"pb_project_ca8...","sealed_root":"q9j4u2ykiop/k+o35wkznq8JBhmSEco3OZWxArmuj9s=","palbase_environment":"local"}
$ unzip -p app/build/outputs/apk/featureDev/app-featureDev.apk assets/palbase/palbase-config.json
{"app_id":"project","base_url":"http://127.0.0.1:62096","api_key":"pb_project_ca8...","sealed_root":"q9j4u2ykiop/k+o35wkznq8JBhmSEco3OZWxArmuj9s=","palbase_environment":"local"}
$ unzip -p app/build/outputs/apk/release/app-release-unsigned.apk assets/palbase/palbase-config.json
{"app_id":"project","base_url":"https://na1m7lt2m.palbase.studio","api_key":"pb_project_cQl...","palbase_environment":"main"}
$ unzip -p app/build/outputs/apk/staging/app-staging-unsigned.apk assets/palbase/palbase-config.json
{"app_id":"project","base_url":"https://na1m7lt2m.palbase.studio","api_key":"pb_project_cQl...","palbase_environment":"main"}

$ aapt2 (build-tools 36.0.0) dump xmltree --file AndroidManifest.xml <apk> | grep -iE 'networkSecurityConfig|debuggable'
== app-debug.apk
        A: http://schemas.android.com/apk/res/android:debuggable(0x0101000f)=true
        A: http://schemas.android.com/apk/res/android:networkSecurityConfig(0x01010527)=@0x7f110000
   res/xml/palbase_network_security_config.xml present; resource 0x7f110000 xml/palbase_network_security_config
== app-featureDev.apk
        A: http://schemas.android.com/apk/res/android:debuggable(0x0101000f)=true
        A: http://schemas.android.com/apk/res/android:networkSecurityConfig(0x01010527)=@0x7f110000
   res/xml/palbase_network_security_config.xml present; resource 0x7f110000 xml/palbase_network_security_config
== app-release-unsigned.apk   networkSecurityConfig count: 0; no debuggable attr; no res/xml/* entries in the APK
== app-staging-unsigned.apk   networkSecurityConfig count: 0; no debuggable attr; no res/xml/* entries in the APK

$ aapt2 dump xmltree --file res/xml/palbase_network_security_config.xml app-debug.apk
E: network-security-config
    E: domain-config  A: cleartextTrafficPermitted=true
        E: domain includeSubdomains=false T: '127.0.0.1'
        E: domain includeSubdomains=false T: '10.0.2.2'

merged manifests (app/build/intermediates/merged_manifest*/<variant>/.../AndroidManifest.xml), grep -c networkSecurityConfig:
  debug: 1 android:networkSecurityConfig="@xml/palbase_network_security_config"
  featureDev: 1 android:networkSecurityConfig="@xml/palbase_network_security_config"
  release: 0
  staging: 0
```

### A3 — PASS: FR-206 loopback refusal for non-debuggable build types

```
$ ./gradlew --console=plain -Ppalbase.env.release=local :app:assembleRelease      (exit=1)
Palbase: release → local (-Ppalbase.env.release on the command line) [palbase/environments]
FAILURE: Build failed with an exception.
* What went wrong:
Execution failed for task ':app:generatePalbaseRelease'.
> Palbase: `release` is not debuggable, and environment `local` (-Ppalbase.env.release on the command line) has base_url http://127.0.0.1:62096 — a loopback address, over plain HTTP. A build that can ship never talks to a stack on the machine that built it: no device reaches that address, and the cleartext allowance for it would ship too. Choose a deployed environment for `release` where this one was chosen — `-Ppalbase.env.release=<environment>` on the command line, or drop it — or build a debuggable variant against the local stack.
BUILD FAILED in 768ms

$ ./gradlew --console=plain -Ppalbase.env.staging=local :app:assembleStaging      (exit=1)
Palbase: staging → local (-Ppalbase.env.staging on the command line) [palbase/environments]
Execution failed for task ':app:generatePalbaseStaging'.
> Palbase: `staging` is not debuggable, and environment `local` (-Ppalbase.env.staging on the command line) has base_url http://127.0.0.1:62096 — a loopback address, over plain HTTP. A build that can ship never talks to a stack on the machine that built it: no device reaches that address, and the cleartext allowance for it would ship too. Choose a deployed environment for `staging` where this one was chosen — `-Ppalbase.env.staging=<environment>` on the command line, or drop it — or build a debuggable variant against the local stack.
BUILD FAILED in 669ms
```

### A4 — PASS: case twin refused on case-insensitive APFS

```
$ ./gradlew --console=plain -Ppalbase.env.release=Main :app:assembleRelease      (exit=1)
Execution failed for task ':app:generatePalbaseRelease'.
> Palbase: environment `Main` (-Ppalbase.env.release on the command line) has no directory — `main` differs from it only in letter case. Environment names are case-sensitive: a case-insensitive disk (macOS, Windows) would build this and a Linux CI would not. Name it exactly `main` where it is set, or map the build type to it: `palbase.env.release=main`.
BUILD FAILED in 520ms
(note: no "Palbase: release → Main" resolution line is printed before this refusal; A3 does print its line)
```

### A5 — PASS: release with no key is refused; gradle.properties restored byte-identical

```
$ shasum -a 256 gradle.properties (before)  21f1a8cb4e2429267d6c413ccad2198155bf508c7c5f9046861a02f70a76523f
$ (removed the line `palbase.env.release=main`; staging key kept)
$ ./gradlew --console=plain :app:assembleRelease      (exit=1)
Execution failed for task ':app:generatePalbaseRelease'.
> Palbase: `release` has no environment, and a release never gets one by default — it would ship a client aimed at a stack nobody chose. Commit the choice, in ONE of two ways: `palbase.env.release=<environment>` in gradle.properties, or `buildTypes { release { palbase { environment = "<environment>" } } }` in the build script (Kotlin DSL: `import io.palbase.gradle.palbase`). This checkout carries local, main.
BUILD FAILED in 530ms
$ (restored from the saved copy)
$ shasum -a 256 gradle.properties (after)   21f1a8cb4e2429267d6c413ccad2198155bf508c7c5f9046861a02f70a76523f
$ cmp gradle.properties <saved original>   -> BYTE-IDENTICAL
```

### A6 — PASS: unknown key warns, build stays green

```
$ ./gradlew --console=plain -Ppalbase.env.stagign=main :app:assembleDebug      (exit=0)
> Configure project :app
Palbase: `-Ppalbase.env.stagign` on the command line names no variant, product flavor or build type of project ':app' (debug, featureDev, release, staging), so it chooses nothing here — a typo, or a key meant for another module.
BUILD SUCCESSFUL in 556ms
36 actionable tasks: 36 up-to-date
```

### A7 — PASS: configuration cache

```
$ ./gradlew --console=plain --configuration-cache :app:assembleDebug      (run 1, exit=0)
> Task :app:generatePalbaseDebug UP-TO-DATE
BUILD SUCCESSFUL in 900ms
Configuration cache entry stored.
APK asset: "base_url":"http://127.0.0.1:62096" "palbase_environment":"local"

$ ./gradlew --console=plain --configuration-cache :app:assembleDebug      (run 2, exit=0)
Reusing configuration cache.
> Task :app:generatePalbaseDebug UP-TO-DATE
BUILD SUCCESSFUL in 420ms
Configuration cache entry reused.
APK asset: "base_url":"http://127.0.0.1:62096" "palbase_environment":"local"

$ ./gradlew --console=plain -Ppalbase.env.debug=main --configuration-cache :app:assembleDebug      (run 3, exit=0)
Calculating task graph as configuration cache cannot be reused because the set of Gradle properties has changed: 'palbase.env.debug' was added.
> Task :app:generatePalbaseDebug
Palbase: debug → main (-Ppalbase.env.debug on the command line) [palbase/environments]
BUILD SUCCESSFUL in 10s
36 actionable tasks: 14 executed, 22 up-to-date
Configuration cache entry stored.
APK asset: {"app_id":"project","base_url":"https://na1m7lt2m.palbase.studio","api_key":"pb_project_cQl...","palbase_environment":"main"}
aapt2 manifest networkSecurityConfig count: 0   (cleartext config dropped for debug→main as well)

$ ./gradlew --console=plain --configuration-cache :app:assembleDebug      (run 4, override removed, exit=0)
Calculating task graph as configuration cache cannot be reused because the set of Gradle properties has changed: 'palbase.env.debug' was removed.
> Task :app:generatePalbaseDebug
Palbase: debug → local (the default for debug) [palbase/environments]
Palbase: base_url points at 127.0.0.1, which on a device or emulator means THAT DEVICE. ...
BUILD SUCCESSFUL in 2s
Configuration cache entry stored.
APK asset: {"app_id":"project","base_url":"http://127.0.0.1:62096","api_key":"pb_project_ca8...","sealed_root":"q9j4u2ykiop/k+o35wkznq8JBhmSEco3OZWxArmuj9s=","palbase_environment":"local"}
aapt2: A: http://schemas.android.com/apk/res/android:networkSecurityConfig(0x01010527)=@0x7f110000
```

### A8 — PASS (with one expectation note): CLI doctor/status + git

```
$ /Users/erkutbas/Github_Pallasite/palbase-e2e-buildtype/bin/palbase doctor      (exit=0; `--version` prints "palbase version dev")
palbase dev
  ✓ cloud      studio https://palbase.studio, auth https://api.palbase.studio, api https://api.palbase.studio, projects <ref>.palbase.studio
  ✓ login      session token valid
  ✓ pat        not set (fine for interactive use; CI needs a Dashboard-issued PAT)
  ✓ link       penny
  ✓ env        penny/main  (via only)
  ✓ docker     /usr/local/bin/docker
  ✓ compose    Docker Compose version v2.40.3-desktop.1
  ✓ creds      docker-credential-desktop
  ✓ node       v22.22.0 (...)
  ✓ bun        1.4.2 (/opt/homebrew/bin/bun)
android (app/build.gradle.kts)
  ✓ local      android-config.json with an api_key, openapi.json with x-palbase-roles
  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles
```
Note: the doctor prints nothing about git-ignore when all is well. In palbase-cli source (cmd/palbase/doctor.go
localStackProbes) the only git row is `✗ local  palbase/environments/local/ is committed — ...`, printed only when
local/ is committed; `return nil` otherwise. The missing ✗ row is the pass signal; git check-ignore below confirms it.
The wave-2 doctor rows (FR-019 palbase.env.* keys, unmapped release, the "verbs only; the build type picks the app's
environment" note on the env row) are absent, as expected for a wave-1 binary (plan-cli.md D-026: T023–T026 are wave 2).

```
$ /Users/erkutbas/Github_Pallasite/palbase-e2e-buildtype/bin/palbase status      (exit=0)
▸ penny/main
project:      penny
address:      https://na1m7lt2m.palbase.studio
credential:   the cloud (this project's key)
deployed:     fb5be22ff40c, 93 endpoint(s), built with SDK 41.7.1
              activated 2026-09-27 05:42
runtime:      @palbase/backend 41.7.1 — the stack serving this project, not this checkout's dependency
app key:      current (android)
local and main serve different contracts:
  only in local:  DELETE /notes/{id} | GET /notes | GET /notes/{id} | PATCH /notes/{id} | POST /notes
  not in local:   (88 routes of penny's contract, e.g. GET /budgets, POST /transfers, ...)

$ git status --short
 M .gitignore
 M settings.gradle.kts        (the A1 pluginManagement fix)
?? palbase/
$ git status --short -uall
?? palbase/environments/main/android-config.json
?? palbase/environments/main/openapi.json
?? palbase/project.json
$ git status --short --ignored
!! .gradle/  !! app/build/  !! build/  !! palbase/environments/local/
$ git check-ignore -v palbase/environments/local/android-config.json     (exit=0)
.gitignore:7:palbase/environments/local/	palbase/environments/local/android-config.json
$ git check-ignore -v palbase/environments/main/android-config.json      (exit=1: not ignored, will be committed)
```

## Part B — AGP 9.1.1 / Gradle 9.3.1

Switch (mirrors /private/tmp/claude-501/.../exec-proofs/T030/agp9):
```
build.gradle.kts:  com.android.application 8.11.1 -> 9.1.1; org.jetbrains.kotlin.android 2.2.21 removed (AGP 9 built-in Kotlin);
                   org.jetbrains.kotlin.plugin.serialization 2.2.21 -> 2.2.10 (reference's version)
app/build.gradle.kts: id("org.jetbrains.kotlin.android") removed; kotlin { compilerOptions { jvmTarget } } block removed (reference has none)
gradle/wrapper/gradle-wrapper.properties: gradle-8.13-bin.zip -> gradle-9.3.1-bin.zip  (cmp with reference: identical)
settings.gradle.kts: already the reference's repository blocks (A1 fix)
gradlew / gradle-wrapper.jar: cmp identical to reference (no change)
rm -rf app/build build   (fresh outputs for Part B)

$ ./gradlew --version            -> Gradle 9.3.1, Launcher JVM 21.0.9 (JBR)
$ ./gradlew buildEnvironment :app:buildEnvironment
+--- com.android.application:com.android.application.gradle.plugin:9.1.1
+--- org.jetbrains.kotlin.plugin.serialization:org.jetbrains.kotlin.plugin.serialization.gradle.plugin:2.2.10
+--- io.palbase.codegen:io.palbase.codegen.gradle.plugin:2.5.0
|    \--- io.palbase:codegen-gradle:2.5.0
|         +--- io.palbase:palbase-codegen-engine:2.5.0
```

### B-A1 — PASS

```
$ ./gradlew --console=plain :app:assembleDebug :app:assembleRelease :app:assembleStaging :app:assembleFeatureDev      (exit=0)
Palbase: debug → local (the default for debug) [palbase/environments]
Palbase: base_url points at 127.0.0.1, which on a device or emulator means THAT DEVICE. ...
Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]
Palbase: staging → main (palbase.env.staging in gradle.properties) [palbase/environments]
Palbase: featureDev → local (palbase { environment } in the `featureDev` build type) [palbase/environments]
Palbase: base_url points at 127.0.0.1, which on a device or emulator means THAT DEVICE. ...
Unable to strip the following libraries, packaging them as they are: libjnidispatch.so. (x4, AGP, unrelated)
BUILD SUCCESSFUL in 36s
171 actionable tasks: 171 executed
(:app:compileDebugKotlin ran under built-in Kotlin; Lstudio/palbase/e2e/E2EApp; found in app-debug.apk classes4.dex)
```

### B-A2 — PASS

```
debug       {"app_id":"project","base_url":"http://127.0.0.1:62096","api_key":"pb_project_ca8...","sealed_root":"q9j4u2ykiop/k+o35wkznq8JBhmSEco3OZWxArmuj9s=","palbase_environment":"local"}
            aapt2: debuggable=true; android:networkSecurityConfig(0x01010527)=@0x7f110000; res/xml/palbase_network_security_config.xml present
featureDev  {"app_id":"project","base_url":"http://127.0.0.1:62096","api_key":"pb_project_ca8...","sealed_root":"q9j4u2ykiop/k+o35wkznq8JBhmSEco3OZWxArmuj9s=","palbase_environment":"local"}
            aapt2: debuggable=true; android:networkSecurityConfig(0x01010527)=@0x7f110000; res/xml/palbase_network_security_config.xml present
release     {"app_id":"project","base_url":"https://na1m7lt2m.palbase.studio","api_key":"pb_project_cQl...","palbase_environment":"main"}
            aapt2: networkSecurityConfig count 0; no res/xml entries
staging     {"app_id":"project","base_url":"https://na1m7lt2m.palbase.studio","api_key":"pb_project_cQl...","palbase_environment":"main"}
            aapt2: networkSecurityConfig count 0; no res/xml entries
merged manifests: debug 1, featureDev 1 (android:networkSecurityConfig="@xml/palbase_network_security_config"); release 0, staging 0
```

### B-A3 — PASS (release only)

```
$ ./gradlew --console=plain -Ppalbase.env.release=local :app:assembleRelease      (exit=1)
Palbase: release → local (-Ppalbase.env.release on the command line) [palbase/environments]
Execution failed for task ':app:generatePalbaseRelease'.
> Palbase: `release` is not debuggable, and environment `local` (-Ppalbase.env.release on the command line) has base_url http://127.0.0.1:62096 — a loopback address, over plain HTTP. A build that can ship never talks to a stack on the machine that built it: no device reaches that address, and the cleartext allowance for it would ship too. Choose a deployed environment for `release` where this one was chosen — `-Ppalbase.env.release=<environment>` on the command line, or drop it — or build a debuggable variant against the local stack.
BUILD FAILED in 782ms
```

### B-A7 — PASS (first two runs)

```
$ ./gradlew --console=plain --configuration-cache :app:assembleDebug      (run 1, exit=0)
> Task :app:generatePalbaseDebug UP-TO-DATE
BUILD SUCCESSFUL in 985ms
37 actionable tasks: 37 up-to-date
Configuration cache entry stored.
APK asset: "base_url":"http://127.0.0.1:62096" "palbase_environment":"local"

$ ./gradlew --console=plain --configuration-cache :app:assembleDebug      (run 2, exit=0)
Reusing configuration cache.
> Task :app:generatePalbaseDebug UP-TO-DATE
BUILD SUCCESSFUL in 469ms
Configuration cache entry reused.
APK asset: "base_url":"http://127.0.0.1:62096" "palbase_environment":"local"
```

Decision: Part B is green → the project stays on AGP 9.1.1 / Gradle 9.3.1.

## Commit

```
$ git add -A && git status --short
M  .gitignore
M  app/build.gradle.kts
M  build.gradle.kts
M  gradle/wrapper/gradle-wrapper.properties
A  palbase/environments/main/android-config.json
A  palbase/environments/main/openapi.json
A  palbase/project.json
M  settings.gradle.kts
$ git commit -m "E2E: linked + AGP 9.1.1"   -> ec27228203dc878be56c21fd1d70029c41460e42
$ git ls-tree -r --name-only HEAD | grep -c 'palbase/environments/local'   -> 0
$ git status --short   -> (clean)
$ git check-ignore -v palbase/environments/local/android-config.json palbase/environments/local/openapi.json
.gitignore:7:palbase/environments/local/	palbase/environments/local/android-config.json
.gitignore:7:palbase/environments/local/	palbase/environments/local/openapi.json
gradle.properties: cmp with the pre-E2E copy -> unchanged (sha256 21f1a8cb...a76523f)

$ palbase doctor (after the commit), android section:
android (app/build.gradle.kts)
  ✓ local      android-config.json with an api_key, openapi.json with x-palbase-roles
  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles
  (no "✗ local ... is committed" row: count 0)
```

## Problems / deviations

1. Test-app setup (not the plugin): the prepared settings.gradle.kts could not resolve the plugin. Its pluginManagement
   listed only codegen-gradle/build/test-repository, and io.palbase:codegen-gradle:2.5.0 pins
   io.palbase:palbase-codegen-engine:2.5.0, which lives only in palbackend-android-src/build/test-repository.
   Fixed by adopting the T030 reference's repository blocks (committed in ec27228).
2. Expectation vs wave-1 CLI: the A8 expectation said doctor's Android section would show "local ignored by git". The
   wave-1 doctor has no positive git-ignore row. It only prints `✗ local ... is committed` when local/ is committed
   (cmd/palbase/doctor.go localStackProbes). The silence is the pass state, confirmed with git check-ignore. The FR-019
   rows for palbase.env.* keys, the unmapped release and the env-row note are wave 2 (plan-cli.md D-026) and are absent,
   as they should be in a wave-1 binary.
3. Not a failure, just a difference: the A4 case-twin refusal does not print a "Palbase: release → Main (...)"
   resolution line before the refusal. The A3 loopback refusal does print its line first.
