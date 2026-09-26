# İlk tasarım analizi — 2026-09-24

> Bu dosya ajan çıktılarından makineyle üretildi (2026-09-26). İçindeki `/private/tmp/...` yolları artık yok: macOS 2026-09-26'da geçici dizini temizledi. Prototip `proto-2.4/` altındaki yamalardan yeniden kurulur (birebir: 13 dosya, +1982/−110; 23+8+47 test yeşil).

# Android environments: final recommendation

## 1. Is the user's mental model right?

### (a) A Palbase environment is not a branch
- **An environment is a tenant.** On the CLI's plane, an environment is a `cloud_projects` row. It has its own microVM, database, anon and service keys, and `https://<ref>.<tenant-host>` address (`internal/env/env.go:201-203`; cloud `cloud-lifecycle.ts:166-211`, origin/main).
- **Environments are created only by hand.** The first one comes with the project, and each later one comes from `palbase env create <name>` (`env.go:192-276`). Nothing creates or selects an environment from a git branch:
  - The CLI listing hard-codes `source_git_branch: null` (`cli.controller.ts:228-235`).
  - `push`, `plan` and `pull` resolve `--env`, then `PALBASE_ENV`, then `palbase env use`, then the only environment, and otherwise refuse (`environments.go:250-283, 366-376`).
- **Every environment costs money.** The Free plan allows 1 environment per project. Pro and Scale allow unlimited environments, each billed per hour (`gen-catalog.mjs:160`). "One environment per feature per person" means one paid microVM each, created and deleted by hand.
- **Names are free text.** They can be 1 to 64 characters, with no character rule and no uniqueness check (`cloud-lifecycle.ts:58-63`, `panel.ts:243`). The first environment is always shown as `main` (`cli.controller.ts:474-491`).
- **`link` writes every environment of the project for everyone** (`project_link.go:883-903`). So every checkout carries every colleague's `featureX`.

Using "an environment like a branch" can be a team habit, but the system does nothing to support it.

### (b) Nothing scans `app/src/<buildType>` today
- **The CLI** reads two regexes from Gradle files: `applicationId` in `app/build.gradle(.kts)` or the root build file (`planes.go:326-346`), and `applicationIdSuffix|productFlavors` (`social_link.go:234-259`). It writes:
  - `palbase/environments/<env>/{android-config.json, openapi.json}` for every environment (`app_environments.go:98-120`, `project_link.go:1020-1030`);
  - `palbase/project.json`.
- **The plugin** takes the environment from `providers.gradleProperty("palbase.env").orElse("local")` (`PalbaseCodegenPlugin.kt:28,41`). The same value is passed to every variant (`AndroidVariantIntegration.kt:23`); the variant name only names tasks and outputs. If the environment's directory is missing, the build fails and never falls back (`GeneratePalbaseTask.kt:97-117`).
- **The trial app** sets `palbase.env=main` in `gradle.properties`, so debug and release both compile `main`.
- **The runtime** reads only the APK asset `palbase/palbase-config.json` (`Palbase.kt:99-108`, `GeneratedConfigLoader.kt:15`).

### (c) How AGP source-set overlay actually works
- **Priority order**, lowest to highest: `main` → flavors → flavor combination → buildType → full variant. This comes from the bytecode of `VariantSources.getSortedSourceProviders`, and the probe printed `freeFeatureXDebug → [src/main, src/featureX, src/free, src/freeFeatureX, src/debug, src/freeFeatureXDebug]`.
- **Release does not "look at main".** It looks at `src/release` and only falls through to `src/main` because `src/release` is empty. Every buildType falls back to `main` the same way.
- **The overlay covers only AGP source types:** java, kotlin, res, assets, manifest, jni, resources and the rest. A loose `app/src/debug/android-config.json` is not an input. The probe found it absent from both `app-debug.apk` and `:app:sourceSets`. Any "src/debug overrides src/main" rule would have to be written into the Palbase plugin.
- **`main` can never be a buildType or flavor.** `create("main")` crashes with "Multiple entries with same key: main".
- **The Kotlin DSL claim is confirmed.** A bare `featureX { }` inside `buildTypes` fails with "Unresolved reference: featureX" (AGP 8.11.1, Gradle 8.13). Only `debug {}` and `release {}` exist as accessors. Custom names need `create("featureX") { }`. Groovy accepts `featureX {}`.
- **A plain `create("featureX")` is not debug.** It is not debuggable and produces an unsigned APK. With `initWith(getByName("debug"))` it is debuggable and debug-signed, but it still does not get `src/debug/**` or the `debugImplementation` dependencies. The variant's source chain is `[main, featureX]`.

## 2. Verdict on the proposal

**What is right:**
- **The build type should choose the environment.** That is the Android counterpart of iOS, where each Xcode build configuration sets its own `PALBASE_ENV` (`project_link.go:1490-1506`). Today's single global `palbase.env` cannot express "debug uses local, release uses main".
- **The `palbase {}` block is unnecessary.** It exists only because the plugin's default directory is inside the module (`PalbaseCodegenPlugin.kt:15`) while the CLI writes at the checkout root (trial `app/build.gradle.kts:32-35`).
- **Feature environments as build variants** is a reasonable option to offer.

**What must change for it to be safe:**

| # | Problem | Concrete failure | How the recommendation handles it |
|---|---|---|---|
| 1 | Overlay on `src/main` is a silent fallback | The baseline-profile plugin adds `benchmarkRelease`, or someone adds `create("qa")`. Neither has its own config, so both compile `src/main`'s environment with `main`'s key and no error. Today the plugin refuses (`GeneratePalbaseTask.kt:97-117`). | The environment is chosen by name only; a missing directory refuses. |
| 2 | Environment `main` cannot be a buildType | It can only live in `src/main`, which is the fallback layer for every variant. | `main` is reached by a key: `palbase.env.release=main`. |
| 3 | Release can be taken over by a name | An org member creates an environment called `release`, `Release`, or the name of a flavor such as `paid`. The next link writes `src/release/…`, and every developer's release APK now targets that environment. | `release` is never bound by name. Twins that differ only in case and flavor names are never auto-created. |
| 4 | AGP ignores loose JSON | The plugin has to rebuild the overlay itself. Putting the files under `assets/` instead ships the 170 KB contract in the APK and collides with the generated `palbase/palbase-config.json`. | The files stay build inputs in `palbase/environments`. |
| 5 | Environment names are not Gradle names | `feature/profile-update` nests directories and is not a valid buildType. `test-login` fails with "BuildType names cannot start with test". `FeatureX` and `featurex` collide on APFS. `../../x` already escapes the checkout today (`layout.go:60`). | The CLI validates each name as one path segment and skips case twins. The opt-in skips names AGP rejects. A key can map any name. |
| 6 | Every environment lands on every developer | Link writes all environments, so everyone gets N teammates' `src/<env>/` and N buildTypes; variants grow to flavors × (2+N). Pruning would have to delete files inside developer-owned `src/<bt>/`. Today's sweep deliberately refuses directories that hold foreign files (`app_environments.go:209`). | Files stay in the CLI-owned `palbase/environments`. The existing "every file is ours" sweep is reused. Auto buildTypes are opt-in. |
| 7 | "No palbase/ folder" | `project.json` (the binding plus `oauth.android`) has no checkout-level home. `.palbase/` is swept when untracked and refused when tracked (`generated_paths.go:137`, `layout.go:181-187`). Mixed iOS or web checkouts keep `palbase/` anyway, which means two layouts and a duplicated 167 KB contract (reversing `stack_spec.go:116-121`). | `project.json` stays where it is. |
| 8 | Environment-as-buildType removes the debug/release axis | You cannot build a minified, signed release against `featureX`. `consumer-release` (release against `local`, `consumer-release/build.gradle.kts:47-52`) becomes impossible to express. | `-Ppalbase.env.release=staging` or a committed key. |
| 9 | `initWith(debug)` is not debug | A `featureX` build has no `src/debug` code, res or manifest, and no LeakCanary, Compose ui-tooling or debug network-security-config. All three designs missed this. | The default path keeps building `debug`. The opt-in documents the gap and wires the dependency configurations. |

## 3. Recommended design: "the build type selects, the files stay"

This is the parity-minimal design with the judges' grafts. Both judges picked it: 7.5/10 and 8/10, against 5 to 6 for the literal and plugin-owned designs.

### 3.1 On-disk layout (the CLI's layout does not change)
```
<checkout>/
├── gradle.properties        palbase.env.debug=main / palbase.env.release=main   (committed, yours; link prints, never edits)
├── local.properties         palbase.env.debug=featureX   (per developer, already gitignored in the trial app)
├── app/build.gradle.kts     no palbase {} block
├── app/src/**               Palbase writes nothing here
├── app/build/generated/palbase/<variant>/{kotlin,assets/palbase/palbase-config.json,res,AndroidManifest.xml}   (not committed)
└── palbase/
    ├── project.json         binding + oauth.android (unchanged)
    └── environments/<env>/{android-config.json, openapi.json}   one flat directory per environment, shared with iOS/web
```

### 3.2 The consumer's `app/build.gradle.kts`
```kotlin
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("io.palbase.codegen")                 // 2.4.0 (version set in the root build.gradle.kts)
}
android {
    namespace = "studio.palbase.trial"
    compileSdk = 36
    defaultConfig { applicationId = "studio.palbase.trial"; minSdk = 26; targetSdk = 36 }
    buildTypes {
        release { isMinifyEnabled = true }   // environment from palbase.env.release
        debug { }                            // environment from palbase.env.debug (local.properties overrides)
        // Optional: a variant per feature environment, when the opt-in is off.
        // Kotlin DSL needs create(...); `featureX { }` is "Unresolved reference".
        // create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") }
    }
}
// No palbase { } block.
```

### 3.3 Which environment a variant compiles (plugin)
**Where keys come from,** highest priority first:
1. `-P` on the command line (`startParameter.projectProperties`; checked safe under the configuration cache);
2. `local.properties`, per developer, read through `providers.fileContents`;
3. the root `gradle.properties`, committed. It is read with `providers.gradlePropertiesPrefixedBy("palbase.env")`, which is present in Gradle 8.11.1 and 8.13.

**Resolution for variant V (buildType B).** The first step that names an environment wins. After that, `palbase/environments/<env>/` must exist or the build refuses. It never falls back.

| # | Rule | Result |
|---|---|---|
| 1 | `palbase.env.<V>` | that environment |
| 2 | `palbase.env.<B>`, `palbase.env.<flavor>`, `palbase.env.<flavorName>` | If they all agree, that environment. If they disagree, **refuse** and name `palbase.env.<V>`. |
| 3 | B was created by the opt-in from `palbase/environments/B` | B |
| 4 | B is `benchmark<X>` or `nonMinified<X>` (baseline-profile convention) | resolve as buildType `<x>`. This removes the cliff where adding the first key breaks these variants. |
| 5 | global `palbase.env=<x>` (today's key) | x. But **refuse** if B is not debug or release, `palbase/environments/B` exists, and x ≠ B. This closes the "keeps `palbase.env=main`, adds `create("featureX")`, silently compiles main" trap. |
| 6 | strict mode, and B is not debug or release | B (the name convention) |
| 7 | B == debug | `local` (today's default) |
| 8 | anything else | Strict mode: **refuse** and name the key to set. Legacy mode: `local` plus a deprecation warning. |

**Strict mode** is on once any `palbase.env.<name>` key exists, or once `palbase.buildTypesFromEnvironments=true` is set.

### 3.4 Environment name to build type
In the table below, "AGP rule" means what `AbstractVariantInputManager.checkName` actually enforces: no whitespace, no case-sensitive `test` or `androidTest` prefix, not `lint`, no clash with a flavor name, and no `main`, which crashes. Anything else listed is Palbase's own conservative filter.

| Environment | Directory | Auto-created with the opt-in? | How to select it |
|---|---|---|---|
| `main` | `main/` | never (AGP crash) | `palbase.env.release=main` / `palbase.env.debug=main` |
| `local` (this machine's stack) | `local/` | never | debug's default, or `palbase.env.<bt>=local` |
| cloud environment named `local` (any case) | **not written**, with a warning; the link fails if it is the default | n/a | rename it in the panel. Today it is silently overwritten (`app_environments.go:642-688`). |
| `staging`, `featureX`, `feature-x` | same name | yes, unless a buildType of that name is declared | pick the variant, or put `palbase.env.debug=featureX` in local.properties |
| `feature/profile-update`, `..`, `.x`, names with `\` or control characters | **not written**, with a warning; the link fails if it is the default | n/a | rename, e.g. to `feature-profile-update` |
| `test-login`, `androidTestX`, `lint` | written | skipped with a warning (AGP rule) | `create("qaLogin")` + `palbase.env.qaLogin=test-login` |
| `My QA Env`, `Ödeme`, `0k3j5h2am` (ref fallback) | written | skipped with a warning (Palbase filter `^[A-Za-z][A-Za-z0-9_-]{0,63}$`) | a key |
| `debug`, `release`, `Release`, `Main`, a flavor's name | written | never | a key only (release safety) |
| `FeatureX` + `featurex`, or exact duplicates | **neither written**, with a warning; the link fails if either is the default | n/a | rename one |

### 3.5 Plugin behaviour (`io.palbase.codegen` 2.4.0, a minor release)
- **Finding the root (no block needed).** Resolved at configuration time through a ValueSource, which fixes the parity design's contradiction between execution-time and configuration-time resolution. The order is:
  1. `environmentsDir`, if set;
  2. `<module>/palbase/environments`;
  3. `<rootDir>/palbase/environments`;
  4. `<rootDir>/../palbase/environments`, for React Native and Flutter, where the Gradle root is `android/`.

  If two of these exist, the module one wins and the build warns. If none exists, the task does nothing, as today.
- **Opt-in `palbase.buildTypesFromEnvironments=true`.** In `finalizeDsl`, for each legal environment name that no buildType or flavor already has, the plugin calls `create(env) { initWith(debug); matchingFallbacks += "debug" }`. It also:
  - sets `beforeVariants { enableUnitTest = false }` for those types (`HasUnitTestBuilder` is present in AGP 8.10.1 through 9.3.2);
  - makes `<bt>Implementation` extend `debugImplementation` and `<bt>RuntimeOnly` extend `debugRuntimeOnly` (not probed);
  - never modifies a buildType the developer declared.

  The docs must state that `src/debug/**` is not included.
- **Execution.**
  - It keeps the NEVER FALL BACK refusal, with a message that now names the origin step and the key to set.
  - It refuses an environment name that would escape the root.
  - It logs one line per run, e.g. `Palbase: featureX → featureX (build type created from palbase/environments/featureX)`.
  - It generates a `PalbaseEnvironment.NAME` constant.
  - A new `palbaseEnvironments` task prints variant → environment → origin → directory.
- **Loopback guard.** A variant that is not debuggable and resolves to a loopback `base_url` without an explicit key warns in 2.4 and refuses in 3.0.

### 3.6 CLI behaviour
- **Layout, `project.json` and `mergeConfigWithExisting` do not change.** Neither do `anyEnvironmentHas("android")`, the `auth` refresh, the spec refresh or status contract drift.
- **Link prints the Android lines** whenever it wrote Android configs. The values come from `envs.Default`, never a literal, and debug is never set to `local`, because a keyless local config fails `api_key` validation and teammates would hit the last linker's laptop:
  ```
  Android: each build type compiles the environment named in gradle.properties
  (yours; link never edits it):
      palbase.env.debug=main
      palbase.env.release=main
  To build debug against your own environment without committing anything,
  put `palbase.env.debug=<env>` in local.properties.
  ```
- **Environment-name safety** (fixes an existing traversal bug). A new `validEnvDirName` rejects:
  - empty, `.`, `..`, or a leading `.`;
  - `/` or `\`;
  - control characters;
  - case twins and duplicates;
  - a cloud environment named `local`.

  Non-default environments are skipped with a warning; a default one fails the link. The writers assert it too.
- **Android stale sweep.** `removeStaleEnvironmentDirs` runs when `apple || android`, not only for Apple (`project_link.go:1071-1082`). The keep set is unchanged: listed environments, unreadable ones and `local`. `isGeneratedEnvironmentFile` also learns `RetiredRolesFile` (`app_environments.go:228-254`), so directories left by older CLIs can be removed. The Xcode-specific wording becomes a parameter.
- **Grafted fixes, independent of the rest:**
  - stage only what a link writes, so an Android link stops copying the 95 MB `app/build` and stops aborting on symlinks under `app/`; keep `app` mutable only when web is detected (`link_artifacts.go:195, 404-407`);
  - refuse `--platform android` when there is no Gradle app module;
  - fix the hint naming the nonexistent `--package-name` flag, and stop discarding that error (`planes.go:345, 317`);
  - add an Android `api_key` drift check to `status` (`status_project.go:235-273`).

### 3.7 Where `project.json` lives
It stays at `palbase/project.json` (`target.go:129`). No other repo reads it. Moving it gains nothing and touches every verb.

### 3.8 Lifecycle and pruning of dead feature environments
- **Create.** `palbase env create featureX` makes a billed tenant. Anyone's next `link` writes `palbase/environments/featureX/`. The developer then either:
  - sets `palbase.env.debug=featureX` in local.properties, which keeps debug tooling and commits nothing; or
  - with the opt-in on, picks the `featureX` variant after a sync.
- **Delete.** The next link removes the directory, but only if every file in it is Palbase's, and never `local`. What happens next:
  - an opt-in buildType disappears on the next sync;
  - a hand-declared `create("featureX")`, or a local.properties key pointing at it, refuses at `generatePalbase<V>` and names the key;
  - a dead ref is never compiled.
- **Rename.** The new name gets a new directory and the old one is swept.
- **Failed, Deleting and unreadable environments** are left as they are.

### 3.9 Release-safety rule
1. `release` is never bound by name.
2. The opt-in never creates or claims `debug`, `release`, `main`, `local`, a name that matches an existing buildType ignoring case, or a flavor name.
3. File existence never selects an environment. It is only used to refuse (step 5).
4. Conflicting keys refuse.
5. In strict mode, an unmapped release refuses. The CLI always prints `palbase.env.release=<envs.Default>`.
6. A release that compiles loopback without an explicit key warns in 2.4 and refuses in 3.0.

### 3.10 Migration from today's `palbase/environments`
- **Existing consumers:** nothing is required in 2.4. The block, a global `palbase.env` and the `local` default all keep working. The one narrow new refusal is step 5.
- **Trial app:**
  1. Bump the plugin to 2.4.
  2. Delete the block at `app/build.gradle.kts:32-35`.
  3. Replace `palbase.env=main` with `palbase.env.debug=main` and `palbase.env.release=main`.
  4. `git rm` `palbase/.gitattributes` and `palbase/environments/main/roles.json`.
- **Plugin 3.0 (later):** remove legacy step 8, so an unmapped variant always refuses.

## 4. Change list, ordered by dependency

**palbase-cli rule:** all work is committed on `main`. No branches, no worktrees, one writer at a time. The lead bumps the parent submodule pointer. User-facing strings are in English.

**Phase 1: palbase-cli safety fixes (no dependencies, can ship first)**
1. `internal/backend/layout.go` (new `validEnvDirName`), `app_environments.go:540-689` (gatherEnvironments: segment check, twins, reserved cloud `local`), and the assertions in `app_environments.go:98-120` and `:716-722`. Tests go in `gather_environments_test.go` and `layout_test.go`.
2. `project_link.go:1071-1082` and `app_environments.go:162-254`: run the sweep for Android, count `roles.json` as ours, and make the wording a parameter. Add an Android-only case to `apple_sweep_test.go`. `TestOrphanCleanupHasAProductionCaller` must stay green.
3. `link_artifacts.go:195, 394-426, 507-521`: path-granular staging.
4. `planes.go:311-346` (the hint, the dropped error, and optionally module discovery through `settings.gradle` includes and `android/app`), plus the `--platform android` gate at `project_link.go:956-986`.
5. `status_project.go:235-273, 319-339`: Android key drift.

**Phase 2: palbackend-android-src, plugin 2.4.0**

6. New `codegen-gradle/.../EnvironmentMapping.kt`, holding the pure functions `chooseEnvironment` and `buildTypeNameVerdict`, plus `EnvironmentMappingTest.kt`, which covers every row of §3.3 and §3.4.
7. `PalbaseCodegenPlugin.kt:10-41`:
   - drop the module-relative default (`:15`);
   - build the key provider from `gradlePropertiesPrefixedBy`, `local.properties` and `startParameter`;
   - read the opt-in property;
   - keep `ENVIRONMENT_PROPERTY` and `DEFAULT_ENVIRONMENT`.
8. `PalbaseExtension.kt:8-20`: `environmentsDir` becomes an optional override with no default.
9. `AndroidVariantIntegration.kt:15-53`:
   - the root ValueSource;
   - the opt-in `finalizeDsl`, with `initWith(debug)`, `matchingFallbacks`, `extendsFrom` and `enableUnitTest=false`;
   - `chooseEnvironment` from `ComponentIdentity` in `onVariants`;
   - the `palbaseEnvironments` task.
10. `GeneratePalbaseTask.kt:32-127`:
    - `environment` becomes `@Optional`; add `unresolvedReason` and `origin`;
    - keep the refusal at `:97-117` and enrich its message;
    - add the root-escape guard, the lifecycle log line and the loopback-release guard.
11. Codegen engine: emit `PalbaseEnvironment.NAME`.
12. `PalbaseCodegenPluginTest.kt`:
    - every existing `palbase.env` and `environmentsDir` test must pass unchanged;
    - new cases: root-level `palbase/` with no block; debug and release keys producing different assets; a local.properties override; a `-P` override; a flavor conflict; an unmapped release in strict mode; the step-5 refusal; `benchmarkRelease` inheriting release's key; the opt-in skipping illegal names; configuration-cache invalidation when an environment directory is added.
13. `consumer-release/build.gradle.kts:47-52`: keep its block, and add `palbase.env.release=local` so its loopback release is explicit.
14. `README.md:94-121`, `distribution/README.md:100-117`, `CHANGELOG.md`.

**Phase 3: palbase-cli, after 2.4 is published**

15. `project_link.go`: new `printAndroidEnvironmentSelection` beside `printEnvironmentSelectionSnippet` (`:1490`), called where Android configs are written (around `:1004-1046`). Add a test that pins values drawn from `envs`.
16. `cmd/palbase/doctor.go`, flagging:
    - a plugin older than 2.4 (version-catalog aware);
    - a leftover block that points at the root;
    - a global `palbase.env` next to per-name keys;
    - no `palbase.env.release`;
    - a committed `local/`.
17. `internal/env/env.go:192-276`: `env create` prints whether the name can be an Android buildType, and otherwise the key line to use.

**Phase 4: consumers and docs**

18. `palbe-trial-android`: the steps in §3.10.
19. Studio docs have no Android page; add one. Also fix the stale iOS pages (`ios/overview.md:142-177`, `cli/codegen.md:124-135`), and the "unset `PALBASE_ENV` takes `local`" text in iOS `README.md:268` and `PalBackend.swift:507`, which contradicts `project_link.go:1485-1488`.

**Follow-ups (not blocking)**
- palbase-cloud: a server-side name rule on create and rename (`cloud-lifecycle.ts:58-63`, `panel.ts:243`).
- palbe-core: scope `KeystoreTokenStore`, `FlagsClient` and `AnalyticsStorage` by the environment ref. Variants share an applicationId, so a session minted by environment A can otherwise be sent to environment B.
- Per-buildType OAuth selection. Today there is one `oauth.android` selection per checkout (`social_link.go:280-351`).

## 5. Decisions for the user

1. **Keep the files in `palbase/environments` and let the build type select, instead of moving them into `app/src/<bt>/`?**
   - Recommended default: **yes.** It delivers "the build type decides" and "no `palbase {}` block" without a fallback, a lockstep plugin 3.0 or a second layout.
   - If they insist on files in the module, the best alternative is `src/<ss>/palbase/` with closed inheritance (only debug and release may inherit `main`). It scored 5 to 6/10 and brings release takeover by naming, a duplicated contract and a breaking migration.
2. **How should a developer work against their own feature environment?**
   - Recommended default: **per-developer `palbase.env.debug=<env>` in `local.properties`.** Nothing is committed, and `src/debug` and debug tooling are kept.
   - Keep `palbase.buildTypesFromEnvironments=true` as an **off-by-default** opt-in for teams that want one variant per environment.
   - Also confirm that one billed environment per feature is intended, since the Free plan allows 1.
3. **Should release be fail-closed?**
   - Recommended default: **yes.** Once any key exists, an unmapped release refuses; the CLI always prints `palbase.env.release=<default>`.
   - A non-debuggable build that compiles a loopback stack without an explicit key warns in 2.4 and refuses in 3.0.
4. **Should `palbase/environments/local/` stop being committed?** It holds a machine-specific loopback URL and key, and is keyless while the stack is down.
   - Recommended default: **yes.** `link` adds a gitignore rule for it and `doctor` warns about an existing committed copy.
   - This is new policy: today `link` only creates `.gitignore` when it is missing (`project_link.go:1384-1441`).

Verification note: all probes ran on scratch copies (AGP 8.11.1, Gradle 8.13, JBR 21), and no repository file was modified. One probe's `./gradlew --stop` stopped 3 Gradle 8.13 daemons on this machine; they restart on the next build. Not probed: the `local.properties` provider, `extendsFrom` of debug configurations, and the `<rootDir>/..` root candidate.