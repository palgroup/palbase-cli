# Bulguların doğrulanması — 2026-09-25

> Bu dosya ajan çıktılarından makineyle üretildi (2026-09-26). İçindeki `/private/tmp/...` yolları artık yok: macOS 2026-09-26'da geçici dizini temizledi. Prototip `proto-2.4/` altındaki yamalardan yeniden kurulur (birebir: 13 dosya, +1982/−110; 23+8+47 test yeşil).

Beş doğrulayıcı, iki eleştirmenin ve test matrisinin bulgularını mevcut koda (palbase-cli 20e5d7e, palbackend-android-src e72f704, palbase-cloud origin/main f92bc1e02) ve prototipe (80fdab5) karşı çürütmeye çalıştı.

| Alan | ID | Karar | Şiddet | Prototipte | İddia |
|---|---|---|---|---|---|
| verify:cli-link-android | B1 | confirmed | major | yes | The plan's default "debug → local" fails the first build for most users. `palbase link` writes local/ only when a `palbase start` stack is r |
| verify:cli-link-android | B2 | confirmed | major | n/a | When local/ is written for a stopped stack, or with no credential, android-config.json has "api_key": "" and no openapi.json is written. |
| verify:cli-link-android | B3 | confirmed | major | n/a | For the same started stack, `palbase link` names it main/ while `palbase spec` writes local/openapi.json. |
| verify:cli-link-android | B4 | confirmed | major | n/a | There is no Android stale-directory sweep: removeStaleEnvironmentDirs runs only under `if apple` in project_link.go (~1072), so a deleted or |
| verify:cli-link-android | B5 | confirmed | major | n/a | `palbase link` prints nothing Android- or Gradle-specific: no plugin id, no palbe dependency, no palbase.env lines. Apple gets printEnvironm |
| verify:cli-link-android | B6 | partly | minor | yes | Before the first `palbase push`, link writes only android-config.json. The plugin then fails with "inputs are incomplete … Run `palbase link |
| verify:cli-link-android | B7 | partly | minor | n/a | `palbase doctor` has no Android checks, and its env line points Android developers at `palbase env use`. `palbase status` key drift reads on |
| verify:cli-link-android | B8 | confirmed | major | yes | A loopback or self-host link (`palbase link http://localhost:…`, or start then link) is written as palbase/environments/main/, so mapping re |
| verify:cli-link-android | B9 | confirmed | minor | n/a | Android detection is a literal applicationId regex in app/build.gradle(.kts) (planes.go). A gradle file with productFlavors or applicationId |
| verify:cli-link-android | YENİ | — | minor | — | `palbase spec` cannot fill a keyless local entry, although link's own message says it will |
| verify:cli-link-android | YENİ | — | minor | — | link never reports writing openapi.json, so a pushed and a never-pushed link look alike |
| verify:plugin-topology | D1 | partly | blocker | yes | The plugin is applied in a LIBRARY module, and the app declares build types the library lacks (featureX -> debug, or staging -> release). Th |
| verify:plugin-topology | D2 | confirmed | major | yes | Product flavors are ignored. With flavorDimensions("env") and productFlavors { staging; prod }, following the refusal text (palbase.env.rele |
| verify:plugin-topology | D3a | confirmed | major | yes | A stale <module>/palbase/environments, left by running `palbase link` inside app/, silently shadows the root copy. |
| verify:plugin-topology | D3b | confirmed | major | yes | RN/Flutter layout: palbase/ is at the repo root and the Gradle root is repo/android. The plugin finds nothing; the build is green with no co |
| verify:plugin-topology | D4 | confirmed | minor | yes | A plain `benchmark` build type (older macrobenchmark template: initWith release, matchingFallbacks release) resolves to env "benchmark" and  |
| verify:plugin-topology | D5 | partly | minor | no | On APFS, 2.3's File.isDirectory lets build type featureX match directory featurex/: it passes on a Mac and fails on Linux CI. The prototype  |
| verify:plugin-topology | D6a | confirmed | none | n/a | The assistant told the user that a featureX build type created with initWith(debug) does NOT get src/debug/** nor debugImplementation depend |
| verify:plugin-topology | D6b | partly | none | n/a | The assistant told the user that in Kotlin DSL a bare featureX { } does not compile and create("featureX") is required. |
| verify:plugin-topology | YENİ | — | blocker | — | N1: a flavored app plus the plugin in an unflavored library silently ignores the variant keys (the only D2 workaround), and the fallback che |
| verify:plugin-topology | YENİ | — | major | — | N2: `palbase link` at an RN/Flutter repo root never detects Android, so the RN zero-config Android flow cannot produce a config the plugin f |
| verify:versioning | E1 | confirmed | minor | n/a | In palbackend-android-src the plugin cannot ship alone as 2.4: scripts/publish.sh ships every artifact under one PALBE_VERSION and refuses u |
| verify:versioning | E2 | confirmed | minor | yes | The SDK repo's own release gate breaks under 'release refuses unless mapped': consumer-release builds against sample/palbase/environments, w |
| verify:versioning | E3 | partly | minor | yes | Everything that pins 2.3 behaviour or text and must change with 2.4: PalbaseCodegenPluginTest local-default assertions, the README environme |
| verify:versioning | E4 | confirmed | none | no | The palbe runtime reads only the asset palbase/palbase-config.json (GeneratedConfigLoader.kt), and codegen-engine, shared and codegen-gradle |
| verify:versioning | E5 | partly | minor | yes | The assistant told the user that AGP's official DSL extension API (DslExtension.extendBuildTypeWith) supports `palbase { environment = "..." |
| verify:versioning | YENİ | — | minor | — | The 2.4 prototype needs a newer Gradle than 2.3 (8.5 for every module, 8.11 for library modules), and nothing declares it |
| verify:versioning | YENİ | — | minor | — | 2.4 is a breaking release under a minor number; the plan documents disagree about it, and the proto CHANGELOG leaves two breaks out |
| verify:plugin-order | C1 | confirmed | major | yes | Resolution step B.6 (build-type name) runs before B.7 (legacy global palbase.env), so 2.3 consumers with a custom build type break on upgrad |
| verify:plugin-order | C2 | confirmed | major | yes | -Ppalbase.env=X on the command line (the 2.3 README CI recipe) is silently outranked by committed per-build-type keys (gradle.properties pal |
| verify:plugin-order | C3 | confirmed | minor | yes | Step 4 uses providers.gradleProperty, which also sees ORG_GRADLE_PROJECT_*, -Dorg.gradle.project.* and ~/.gradle/gradle.properties. It label |
| verify:plugin-order | C4 | confirmed | major | yes | local.properties palbase.env.release=local overrides the committed release choice, and assembleRelease succeeds with a loopback/cleartext re |
| verify:plugin-order | C5 | partly | minor | yes | The resolved env/origin exists only as a log line, which vanishes on UP-TO-DATE / build-cache / configuration-cache reuse; nothing in the AP |
| verify:plugin-order | C6 | confirmed | minor | yes | Kotlin DSL without `import io.palbase.gradle.palbase`: an inner `palbase { }` in a build type binds to the PROJECT extension. The proposed @ |
| verify:plugin-order | C7 | partly | minor | yes | The release refusal-by-default makes `./gradlew test`, `check` and `build` fail for every consumer that has not mapped release (testReleaseU |
| verify:plugin-order | YENİ | — | major | — | 2.3 consumer with a custom build type whose same-named environment directory exists silently switches environment on upgrade |
| verify:plugin-order | YENİ | — | minor | — | Proto README describes the order as 'command line first', which misleads about -Ppalbase.env |
| verify:cli-names | A1 | confirmed | blocker | n/a | Environment names from the server reach the filesystem unvalidated (layout.go EnvDir = path.Join("palbase","environments",env)). A hostile n |
| verify:cli-names | A2 | confirmed | blocker | n/a | The server accepts any 1–64-character environment name, with no uniqueness and no slug. The first environment is listed as `main` only when  |
| verify:cli-names | A3 | confirmed | major | yes | Case twins (Staging + staging) collapse into one directory on APFS. removeStaleEnvironmentDirs compares names exactly and could delete a dir |
| verify:cli-names | A4 | confirmed | minor | n/a | A cloud environment named `local` is silently overwritten by this machine's stack entry in gatherEnvironments. |
| verify:cli-names | A5 | confirmed | minor | n/a | `palbase env create "Feature X"` cannot be confirmed interactively (env.go uses fmt.Fscanln into one string). |
| verify:cli-names | A6 | partly | minor | yes | D-036: a non-production environment deploys from the git branch equal to its slug/name, so branch names like feature/login collide with one- |
| verify:cli-names | YENİ | — | major | — | The first environment's directory name depends on where the project was created (CLI → `main`, panel → `Production`), and the panel shows ne |
| verify:cli-names | YENİ | — | major | — | Any org member can break every teammate's `palbase link` with an environment name alone (persistent DoS) |
| verify:cli-names | YENİ | — | minor | — | Server surfaces disagree on what an environment is called; D-036 routes on a `slug` field that means three different things |
| verify:cli-names | YENİ | — | minor | — | Environment names are printed raw to teammates' terminals (control-sequence injection) |

---

## verify:cli-link-android

### B1 — confirmed / major (prototipte: yes, repo: palbase-cli + plugin)

**İddia.** The plan's default "debug → local" fails the first build for most users. `palbase link` writes local/ only when a `palbase start` stack is registered under a group taken from the app checkout's directory name, while `start` registers it under the project name. A cloud-only user gets no local/ at all.

**Kanıt.**

CODE AT HEAD 20e5d7e:
- project_link.go:837 `target := Target{URL: base, Insecure: o.insecure, checkoutRoot: o.checkoutRoot}` has no Name or Project.
- app_environments.go:656 `localURL := LookupLocalStack(groupOf(primary))`, and :700-713 groupOf falls back to `filepath.Base(target.checkoutRoot)`. checkoutRoot is the real app checkout (link_artifacts.go:141, :243).
- start.go:676-684 groupName uses `readLinkedProject()`'s `target.Name` (the product name), then the directory. start.go:438 `registerStack(group, …)`.
- The tests register under the linking checkout's own name (project_link_test.go:314, :401). tests/e2e/ios_layout_test.go:81-84 documents this: "an app checkout carries a `local` environment only when it shares the name of the checkout that started one".

RUN, with the installed palbase 0.71.2 (relevant source identical to HEAD: `git diff --stat v0.71.2..HEAD` over these files is empty), an isolated HOME and loopback fake stacks:
- L1: registry group `todoapp`, app checkout `MyApp`. Output: `▸ android / wrote palbase/environments/main/android-config.json / linked to http://127.0.0.1:18865 (project) / commit palbase/`. No local/ was written.
- L2: the same stack registered as `myapp`. Output: `wrote palbase/environments/local/android-config.json`, and local/ holds both files with a key.

PLUGIN, a copy of the prototype at 80fdab5:
- EnvironmentResolver.kt:90-91 and :149 set `DEBUG_DEFAULT = "local"`.
- S1: a consumer holding only main/, with no keys set, ran `./gradlew :app:generatePalbaseDebug` → FAILED: "Palbase: environment `local` (the default for debug) has no directory … this checkout carries main. Run `palbase link` to write it, or choose one … `palbase.env.<build type>=<environment>` in local.properties or gradle.properties…"

WHY MAJOR, NOT BLOCKER:
- The failure is fail-closed: no wrong environment ships.
- The refusal message itself names the working fix (`palbase.env.debug=main`).
- Plugin 2.3's `local` default fails the same way today.
- Its first piece of advice ("Run `palbase link`") cannot help.

**Düzeltme.**

palbase-cli: at project_link.go:837, set `Name: o.product.Name` (or pass the lookup group explicitly) so that groupOf matches start's groupName. Keep the checkout basename as a fallback. When the registry holds stacks under other groups, say which ones.

palbase-cli (the B5 fix): print `palbase.env.debug=<envs.Default>`.

Plugin: when debug resolves to `local` by default and local/ is absent, drop "Run `palbase link`". Say instead: no local stack is linked here; set `palbase.env.debug=<one of: …>`, or run `palbase start` in the backend and then `palbase link` here.

### B2 — confirmed / major (prototipte: n/a, repo: palbase-cli)

**İddia.** When local/ is written for a stopped stack, or with no credential, android-config.json has "api_key": "" and no openapi.json is written.

**Kanıt.**

CODE:
- app_environments.go:662-666 (credErr): `envs.Environments[localEnvName] = appEnvironment{AppID: projectAppID, BaseURL: localURL}` and then returns with no spec.
- app_environments.go:668-674 (keyErr) does the same.
- `APIKey` has no omitempty (:44).
- social_link.go:293-299 passes a keyless non-default entry through, so it is written.

RUN (0.71.2 binary):
- L3a, registered with nothing listening: "local: http://127.0.0.1:18867 did not answer — run `palbase start`, then `palbase spec` to fill it in". local/ = only android-config.json, `"api_key": ""`.
- L3b, registered with no credential: "…holds no credential for it — `palbase start`". The same keyless file.
- A proper `palbase stop` deregisters the stack (start.go:500), so no new local/ is written, but an old one is not removed either.

PLUGIN (proto copy): fail-closed.
- S2a, keyless config only: "Palbase Android inputs are incomplete for environment `local`. Run `palbase link`…"
- S2b, keyless config beside a stale openapi.json: "…/local/android-config.json is missing `api_key`".

WHY MAJOR:
- The file is committed: link prints `commit palbase/` and nothing ignores local/.
- Under debug→local, it breaks the debug builds of every teammate who clones the repo.

**Düzeltme.**

palbase-cli, gatherEnvironments: never emit a keyless or contract-less `local` entry. Skip it with the existing sentence, and change that sentence's advice to 'then `palbase link` again'.

Alternatively, write local/ only when both files can be written, and add `palbase/environments/local/` to the ignore rules, since it carries this machine's port and key.

### B3 — confirmed / major (prototipte: n/a, repo: palbase-cli)

**İddia.** For the same started stack, `palbase link` names it main/ while `palbase spec` writes local/openapi.json.

**Kanıt.**

CODE:
- project_link.go:343-351 followStart sets `o.url = running.URL` with no product.
- project_link.go:876-882 `if linkedEnv == "" { linkedEnv = soleEnvName }`, where environments.go:103 has `soleEnvName = "main"`.
- In gatherEnvironments, :657 `localURL == primary.URL` → no local entry.
- For spec, stack_spec.go:103-106 `env := resolved.ArtifactEnv(); if target.Local { env = localEnvName }`. Resolve marks a start record Local (environments.go:167-169, target.go:505).

RUN, L4 (checkout `Mono` with a simulated start record in HOME/.palbase/checkouts/<sha256[:8]>/local.json):
- `palbase link` → `wrote palbase/environments/main/android-config.json`.
- `palbase spec` → `✓ wrote palbase/environments/local/openapi.json (169945 bytes)`.
- Disk: local/openapi.json, main/android-config.json and main/openapi.json.

Consequences:
- Under debug→local, the build fails with "inputs are incomplete".
- main/ is a loopback address (see B8).

**Düzeltme.**

palbase-cli: one naming function for a stack on this machine, used by link and spec alike. A started stack, or a loopback self-host address, is `local` in both.
- For link: `linkedEnv = localEnvName` when there is no product and `isLoopbackAddress(base)`.
- For spec: make `Resolved.ArtifactEnv()` return `local` for a loopback self-host target too. Otherwise the mismatch just flips direction for an address-linked checkout, where spec currently writes main/.

### B4 — confirmed / major (prototipte: n/a, repo: palbase-cli)

**İddia.** There is no Android stale-directory sweep: removeStaleEnvironmentDirs runs only under `if apple` in project_link.go (~1072), so a deleted or renamed environment's directory survives an Android link.

**Kanıt.**

CODE:
- project_link.go:1072-1083 `if apple { keep := envs.names() … generateForEnvironmentsAt(ctx, envs, keep, w, o.checkoutRoot) }`.
- app_environments.go:740 is the only caller of removeStaleEnvironmentDirs (grep). The other entry is generateForEnvironments, reached from stack_spec.go:133 under `if … apple`.

RUN, L1: a pre-existing palbase/environments/featurex/ (base_url https://deadref00.palbase.studio) survived an Android link, with no line printed about it.

WHY MAJOR: under plan rule B.6 (the build-type name is the environment), build type featureX keeps compiling a deleted tenant's dead directory, and nothing fails at build time.

**Düzeltme.**

palbase-cli: call removeStaleEnvironmentDirs from runLinkPrepared whenever `writesPerEnvironmentArtifacts(platforms)`, not only for Apple.
- Keep the same keep list: listed environments including Failed/Deleting ones, plus `local`.
- Run it only when the project listing was read (`len(o.environments) > 0`).
- Keep the 'every file is ours' guard.
- Make the Xcode-specific wording a parameter.
- Extend apple_sweep_test.go with an Android-only case.

### B5 — confirmed / major (prototipte: n/a, repo: palbase-cli)

**İddia.** `palbase link` prints nothing Android- or Gradle-specific: no plugin id, no palbe dependency, no palbase.env lines. Apple gets printEnvironmentSelectionSnippet.

**Kanıt.**

GREP at HEAD:
- `io\.palbase`, `palbase\.env` and `io.palbase:palbe` have zero hits in any .go file.
- The only non-test hits are docs/paltimate/2026-09-02-cli-tam-onarim/*.md.
- printEnvironmentSelectionSnippet is called only at project_link.go:1046, inside `if apple`.
- Web gets wireWebProject or a 'push, then link' line (:1054-1066).

RUN, L1 Android link output, in full: `▸ android / remembered this stack's key … / wrote palbase/environments/main/android-config.json / linked to http://127.0.0.1:18865 (project) / commit palbase/`.

The link also writes openapi.json without printing it (:1020-1030; see the new findings).

**Düzeltme.**

palbase-cli: add printAndroidSetup(w, envs.Default), called when an `android` config was written.
- Print the settings repositories line, `id("org.jetbrains.kotlin.plugin.serialization")` + `id("io.palbase.codegen") version X`, and `implementation("io.palbase:palbe:X")`.
- Print the environment lines taken from envs, never a literal: for 2.4, `palbase.env.debug=<default>` and `palbase.env.release=<default>`; for 2.3, `palbase.env=<default>` plus the environmentsDir block.
- Pin the output with a test.

### B6 — partly / minor (prototipte: yes, repo: plugin (+ palbase-cli wording))

**İddia.** Before the first `palbase push`, link writes only android-config.json. The plugin then fails with "inputs are incomplete … Run `palbase link`", which cannot help: the cure is a push.

**Kanıt.**

CONFIRMED, CLI:
- app_environments.go:572-575 (address branch) and :643-647 (project branch) record no spec on ErrNoContractYet.
- L5 (fake stack answering 404 spec_unavailable): main/ = only android-config.json.

CONFIRMED, PLUGIN:
- Real 2.3 GeneratePalbaseTask.kt:124 and proto GeneratePalbaseTask.kt:167-171: "Palbase Android inputs are incomplete for environment `$selected`. Run `palbase link` to write …".
- S3 (proto copy, main/ holding only the config, `-Ppalbase.env.debug=main`) failed with that exact text.

WHY PARTLY: link itself names the cure. L5 printed: "no contract yet: … nothing is deployed yet — a backend is what makes a contract, so this ends with `palbase push`". The cloud branch prints "<env> has no contract to give (…) — `palbase push --env <env>`" (:647). Only the plugin's message is wrong.

**Düzeltme.**

Plugin: when android-config.json exists and openapi.json does not, say that environment `<env>` has no contract yet, and that the fix is to push a backend to it (`palbase push --env <env>`) and then run `palbase spec` or `palbase link` here.

palbase-cli, app_environments.go:647: add 'then `palbase link` here'.

### B7 — partly / minor (prototipte: n/a, repo: palbase-cli)

**İddia.** `palbase doctor` has no Android checks, and its env line points Android developers at `palbase env use`. `palbase status` key drift reads only ios configs.

**Kanıt.**

CONFIRMED:
- status_project.go:236 `envs, err := readAppEnvironments("ios")` and :320 do the same. In an Android-only checkout the map is empty, so reportKeyDrift returns silently and appKeyState says "unchecked".
- cmd/palbase/doctor.go:148-216 probes cloud, login, pat, link/env, Docker, node and bun. There is nothing for Gradle, the plugin or palbase/environments.

PARTLY: doctor.go:262 prints `firstLine(err.Error())`. The resolver refusal (environments.go:366-369) is "%s has %d environments and none is selected:\n…\n  palbase env use <name> …", so doctor shows only "✗ env <project> has 2 environments and none is selected:". The `palbase env use` line is cut off. It still flags a state that does not affect which environment an APK compiles.

**Düzeltme.**

palbase-cli, status: run the key drift check for every platform with configs on disk (android and web too).

palbase-cli, doctor, a new Android section:
- palbase/environments/* completeness: both files, a non-empty api_key, x-palbase-roles;
- the palbase.env.* keys found in gradle.properties and local.properties;
- flags for an unmapped release and for a loopback environment mapped to release.

Annotate the env line in app checkouts: verbs only; the build type picks the app's environment.

### B8 — confirmed / major (prototipte: yes, repo: palbase-cli + plugin)

**İddia.** A loopback or self-host link (`palbase link http://localhost:…`, or start then link) is written as palbase/environments/main/, so mapping release to main ships a loopback cleartext release. The plugin only warns.

**Kanıt.**

CLI:
- project_link.go:418-420: a loopback URL is not a cloud address, so no product is set; :876-882 then gives `linkedEnv = soleEnvName` ("main").
- L1: main/android-config.json `"base_url": "http://127.0.0.1:18865"`.
- L4 (start record + link): main/ again.

PLUGIN (proto copy), S4: main/ base_url http://127.0.0.1:54321 plus `palbase.env.release=main`; `./gradlew :app:generatePalbaseRelease` gave:
- the log line `Palbase: release → main (palbase.env.release in gradle.properties)`;
- a warning that is only about emulators ("base_url points at 127.0.0.1, which on a device or emulator means THAT DEVICE…");
- BUILD SUCCESSFUL.

The release outputs contain:
- res/generatePalbaseRelease/xml/palbase_network_security_config.xml with `cleartextTrafficPermitted="true"` for 127.0.0.1 and 10.0.2.2;
- `<application android:networkSecurityConfig=…>`;
- asset base_url http://127.0.0.1:54321.

The proto has no debuggable check: grep for 'debuggable' in the proto's Kotlin sources finds nothing. GeneratePalbaseTask.kt:263-264 accepts http as 'loopback', and :409-415 only warns.

WHY NOT A BLOCKER: the mapping is explicit, and the app cannot reach 127.0.0.1 on a device, so the failure is visible rather than a different tenant silently shipping.

**Düzeltme.**

palbase-cli: name loopback and started stacks `local` (the same function as the B3 fix), so that main/ can never be loopback.

Plugin: refuse an http/loopback base_url for non-debuggable variants. Read the build type's isDebuggable through finalizeDsl or ApplicationVariantBuilder, and do not emit the cleartext network-security-config for them.

### B9 — confirmed / minor (prototipte: n/a, repo: palbase-cli)

**İddia.** Android detection is a literal applicationId regex in app/build.gradle(.kts) (planes.go). A gradle file with productFlavors or applicationIdSuffix makes nativeIdentifiers return nil (social_link.go), so an environment with an Android OAuth client makes link refuse.

**Kanıt.**

CODE:
- planes.go:326 `(?m)applicationId\s*(?:=\s*)?["']([^"']+)["']` over app/build.gradle(.kts) and root build.gradle(.kts) (:329-334).
- social_link.go:234 `\b(?:applicationIdSuffix|productFlavors)\b`, and :248-250 `return nil`.
- :159-181: with no committed selection, a nil identifier list against an enabled android client gives "social sign-in for android needs application_key and variant in palbase/project.json oauth.android; the checkout does not identify one configured target". This is fatal for the default environment; other environments are dropped (:308-321).

PYTHON EMULATION of both regexes:
- The Android Studio template and Groovy forms are detected.
- A non-literal `applicationId = libs.versions.appId.get()` is NOT detected, so the link is backend-only.
- 'suffix only', 'flavors', and even a comment containing 'productFlavors' → nativeIdentifiers=nil.

RUN, L6 (fake stack with an enabled Google android client, package studio.palbase.trial):
- Plain: passes identification and fails later on sealing, a limitation of the fake.
- Flavors and Suffix: both refuse with the message above.

WHY MINOR: the refusal is explicit and names the file, but not why the checkout was ambiguous or which values to write.

**Düzeltme.**

palbase-cli: when androidVariantConfiguration matches, say that flavors or applicationIdSuffix were found. List the candidate (application_key, variant, package_name) triples already fetched in `available`, with the exact `oauth.android` JSON to commit.

Tighten the regex so that comments do not match.

Document that one oauth.android selection applies to every build type.

### YENİ — `palbase spec` cannot fill a keyless local entry, although link's own message says it will (minor)

**Senaryo.** In an app checkout, the stack registered for local/ is down, so link writes a keyless local/ and says "run `palbase start`, then `palbase spec` to fill it in". The person starts the stack and runs `palbase spec`. spec refreshes only the resolved environment's openapi.json (main/) and never writes any *-config.json. local/ stays keyless, and the debug build keeps failing until `palbase link` is run again.

**Kanıt.**

app_environments.go:673 prints the advice. stack_spec.go:97-110 writes only `writeSpec(env, spec)`, where env = ArtifactEnv ("main" for an address-linked checkout).

RUN (0.71.2 binary, sandbox):
- After L3a, the stack at :18867 was started and `palbase spec` run. Output: "▸ http://127.0.0.1:18865 / ✓ wrote palbase/environments/main/openapi.json". local/ still holds only android-config.json with `"api_key": ""`.
- A second `palbase link` then wrote local/{android-config.json with a key, openapi.json}.

**Düzeltme.**

palbase-cli, app_environments.go:665 and :673: advise "`palbase start`, then `palbase link` here". Or skip the keyless entry altogether (the B2 fix).

### YENİ — link never reports writing openapi.json, so a pushed and a never-pushed link look alike (minor)

**Senaryo.** An Android developer checks from link's output whether the plugin has both inputs. Every run prints only `wrote …/android-config.json`. A complete link and a contract-less one differ only by one 'no contract' line, which is easy to miss among the rest. This is also why the B6 state looks like 'link wrote only the config'.

**Kanıt.**

project_link.go:1014-1016 prints each config path. :1020-1030 `writeSpec(name, spec)` prints nothing.

RUN, L1: the output lists only main/android-config.json, but disk has main/openapi.json too (`find palbase -type f`).

**Düzeltme.**

palbase-cli: print `wrote palbase/environments/<env>/openapi.json` in the spec loop at project_link.go:1026. Also print the environments that got no contract.

#### Notlar

## Summary
I checked all nine claims against the current code and, where possible, by running them.
- **Confirmed:** B1, B2, B3, B4, B5, B8, B9.
- **Partly confirmed:** B6 and B7.
- **Refuted:** none.
- **No blockers.** Every failure I reproduced is fail-closed, or it follows an explicit mapping. None of them silently ships another tenant's environment, and the zero-project flow can be completed by following the plugin's own refusal message.
- **The core problem:** today the CLI cannot reliably produce a usable `local/`. So the plan's `debug → local` default fails the first build for cloud-only users and for most users of `palbase start`. It also fails on 2.3 today, whose default is also `local`.
- **Most important fixes:**
  - B1 group key: one line at project_link.go:837.
  - B3/B8 naming: one function naming a stack on this machine `local`, used by both link and spec.
  - B5: print the Gradle lines, including `palbase.env.debug=<default>` and `palbase.env.release=<default>`.
  - B4: run the sweep for Android too.
  - B8: the plugin refuses loopback in release builds.

## How I verified it
- **Go is not installed.** I verified the Go code by reading it at HEAD 20e5d7e, plus Python emulations of the regexes.
- **The installed `palbase` 0.71.2 is a faithful stand-in for HEAD here:** `git diff --stat v0.71.2..HEAD` over project_link.go, app_environments.go, start.go, stack_spec.go, planes.go, social_link.go, layout.go, link_artifacts.go, status_project.go, environments.go, target.go and doctor.go is empty. The whole diff touches only cmd/palbase/main.go, surface_test.go and internal/secret/*.
- **How the CLI runs were isolated:**
  - an isolated `HOME` and loopback Python fake stacks (copied from review-flow);
  - no cloud contact, no login, no environments created.
- **How the plugin runs were done:**
  - a copy of prototype 80fdab5 (clean tree), used through `includeBuild`;
  - a copy of the spike consumer;
  - AGP 8.11.1, the Android Studio JBR, `--offline`.
- **Work directory:** /private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/266a545b-bd07-454f-b730-c8c05afdf07d/scratchpad/verify/cli-link-android/ (consumer/, proto/src/, cli/{apps,home,stack,logs}).
- **Cleanup:** I stopped only my own Python fake stacks. No Gradle daemon was stopped. All real repos are unmodified (`git status` is clean), and the evidence sandbox was not edited.

## Zero-project sequence today (HEAD 20e5d7e, plugin 2.3.0)
1. **`palbase login`**
   - Writes ~/.palbase/session.json (internal/auth/credentials.go:42-45). Nothing goes into a repository.
2. **`palbase project create myapp`**
   - POSTs /v1/cloud/projects, waits until the project is reachable, and prints `Link it with: palbase link myapp` (internal/project/project.go:130-160).
   - Writes nothing locally.
   - **The first environment is named `main`** in the CLI listing on palbase-cloud **origin/main f92bc1e02** (cloud/platform/server/modules/cli/cli.controller.ts:474-491: an index-0 row named like the product → `main`).
   - The local palbase-cloud checkout is at 6bbfcdf54 and has an older rule (v2-cloud/…/cli.controller.ts:475-479), which would return the row's own name. So the directory name depends on the deployed server revision.
3. **The backend, in its own EMPTY directory.** `init` refuses a non-empty directory (init.go:231-253), so it cannot run in the Android Studio root.
   - **`palbase init`:** runs `npm install @palbase/backend@<newest>`, copies template/, and writes .gitignore.
   - **`palbase link myapp`:** prints "no client app here … linking the backend only" and writes only palbase/project.json `{project, name}`. It prints `commit palbase/project.json` (project_link.go:781-783, 1107-1108).
   - **`palbase push`:** needs bun. Its spec refresh is a no-op in a backend-only checkout (stack_spec.go:62-64), so nothing reaches the app checkout.
   - **Optional `palbase start`:** registers the stack as group `myapp` (start.go:670-684, 438).
4. **The Android Studio root: `palbase link myapp`**
   - **Detection:** Android is detected from the literal `applicationId` in app/build.gradle.kts (planes.go:326-346). The Android Studio template has one; the user's test app does too.
   - **palbase/project.json** is written.
   - **palbase/environments/main/android-config.json** is written with mode 0600 and holds `app_id: "project"`, `base_url: https://<ref>.<tenant host>` and `api_key: pb_…`.
   - **main/openapi.json** is written only if the backend was pushed. Otherwise link prints "main has no contract to give … `palbase push --env main`". After a later push, run `palbase spec` or `palbase link` in the app checkout.
   - **local/** is written only when a start stack is registered under the app directory's basename (B1).
   - **.gitignore** is not touched when it exists.
   - **Output:** `▸ android / wrote …/android-config.json / linked to myapp (prd_…) / contract read from main… / commit palbase/`, with no Gradle guidance.
5. **By hand in Gradle** (distribution/README.md in palbackend-android-src):
   - **Repositories:** `maven("https://palgroup.github.io/palbackend-android/")` in both `pluginManagement` and `dependencyResolutionManagement` in settings.gradle.kts.
   - **Root build.gradle.kts:** `id("org.jetbrains.kotlin.plugin.serialization") version "<kotlin>" apply false` and `id("io.palbase.codegen") version "2.3.0" apply false`.
   - **App plugins:** the serialization plugin and `io.palbase.codegen`.
   - **Dependency:** `implementation("io.palbase:palbe:2.3.0")`.
   - **The environments block:** `palbase { environmentsDir.set(rootProject.layout.projectDirectory.dir("palbase/environments")) }`. It is needed because the 2.3 default directory is inside the module (PalbaseCodegenPlugin.kt:15).
   - **gradle.properties:** `palbase.env=main`, because the default is `local`.
   - **Under prototype 2.4:** the block is unnecessary, because the root is discovered (AndroidVariantIntegration.kt:57-61). The user still needs either `palbase.env.debug=main` plus `palbase.env.release=main`, or the legacy `palbase.env=main`, which covers both debug and release (resolver step 7). Otherwise debug fails on `local` (S1) and release refuses.

## Severity notes
- **B1, B2 and B3 are the same design risk:** `local/` is machine-specific and unreliable, and it is committed (link prints `commit palbase/`), so it also breaks teammates' debug builds. This argues for the analysis-final default: the CLI prints `palbase.env.debug=<default>`, rather than relying on `debug → local`.
- **B8 in the prototype:** its only loopback warning is about emulators. It never mentions that the variant is a release.
- **Related, already reported by review-flow:** a loopback URL is committed into palbase/environments/*/android-config.json, although target.go:623-632 deliberately keeps loopback addresses out of the repository.

---

## verify:plugin-topology

### D1 — partly / blocker (prototipte: yes, repo: plugin)

**İddia.** The plugin is applied in a LIBRARY module, and the app declares build types the library lacks (featureX -> debug, or staging -> release). The APK carries the library's fallback environment. Fix round 1 added a configuration-time WARNING plus a suffix on the generation line. Are these adequate?

**Kanıt.**

Setup: my clone of proto 80fdab5; AGP 8.11.1 on Gradle 8.13, run --offline. Logs are in verify/plugin-topology/fx/.

(a) The warning fires in both shapes.
- d1a (app featureX -> lib debug; palbase.env.debug=main): "Palbase: `:app` build type `featureX` is not declared in `:lib`, so AGP packs `:lib`'s `debug` build into it ... `featureX` itself would select `featureX` (from the build type name)". The suffix also printed: "Palbase: debug → main (...) — ALSO packed into `:app` featureX (matchingFallbacks)...". The APK still carries main: `APK app-featureX.apk: base_url = https://main.envprobe.palbase.studio`.
- d1b (t2 shape; palbase.env.release=prod, palbase.env.staging=staging): the warning fires. The APK still carries prod: `app-staging.apk: base_url = https://prod.envprobe.palbase.studio`. EXIT=0, even though palbase.env.staging=staging is committed.
- On AGP 9.1.1 / Gradle 9.3.1 (d1b9), the warning and the suffix also fire.

(b) What prints on reruns:
- Configuration-cache reuse is completely silent. d1a-cc2 and d1b-cc2 print only "Reusing configuration cache.", "> Task :lib:generatePalbaseRelease UP-TO-DATE", "BUILD SUCCESSFUL". There is no Palbase line.
- Why: the warning is emitted in `library.gradle.projectsEvaluated` (LibraryFallbackCheck.kt:54, :62 `library.logger.warn(finding.message)`). The suffix is printed only from the task action (GeneratePalbaseTask.kt:159-163), so it is gone whenever the task is UP-TO-DATE.
- Without the configuration cache, an UP-TO-DATE rerun (d1b-nocc3) prints the warning again but no generation line.
- The warning also prints on unrelated builds: `:app:assembleDebug` in d1b-dbg printed the staging warning.

(c) A warning is not adequate.
- The build ships prod config and the prod key in a staging APK. It stays green even when the committed key names another environment.
- Under configuration-cache reuse it is fully silent, and spec A requires configuration-cache correctness.
- Under Isolated Projects the check is skipped (PalbaseCodegenPlugin.kt:31-32 `crossProjectChecks = !isolated`), so it is silent there too.

Feasibility of the reviewer's stamp idea:
- As stated, it is not feasible. When the app does not apply the plugin, nothing runs in the app module. The APK holds exactly one Palbase asset (the library's), and no app-side resolution exists to compare a stamp against.
- The comparison the stamp would provide already exists in LibraryFallbackCheck.findings (:90 `resolver.resolve(buildType.name, buildType.name)`). Only a variant-scoped failure hook is missing.

A hard fail is feasible. Probe: init script fx/guard-probe.init.gradle.kts adds, in projectsEvaluated (the same hook), `app.tasks.named("preStagingBuild") { doFirst { throw GradleException(msg) } }`. Results:
- :app:assembleStaging with the configuration cache stored → "> Task :app:preStagingBuild FAILED > PROBE-GUARD...".
- Same build with the cache reused → still FAILED.
- :app:assembleDebug → BUILD SUCCESSFUL.
- :app:assembleRelease → BUILD SUCCESSFUL.

**Düzeltme.**

Plugin repo. In LibraryFallbackCheck.register, keep the warning. For each finding, also attach a failure to the app's `pre<AppVariant>Build` tasks, for every app variant of that build type:
- Compute the variant names from the app's productFlavors × build type.
- Capture only the message String, so it is configuration-cache safe.
- Use `doFirst { throw GradleException(message) }`.

Only the fallback variant fails; debug, release and IDE sync of other variants keep working. It also fails on configuration-cache reuse (measured with the probe). The existing ack `palbase.env.<appBuildType>=<fallback env>` still makes the answers agree and removes the guard.

Under Isolated Projects the check cannot run. Document that library placement with matchingFallbacks is unchecked there, or fail closed when the library has any `palbase.env.<X>` key naming a build type it lacks.

Do not pursue the env-stamp design unless the plugin is also made mandatory in the app module.

### D2 — confirmed / major (prototipte: yes, repo: plugin)

**İddia.** Product flavors are ignored. With flavorDimensions("env") and productFlavors { staging; prod }, following the refusal text (palbase.env.release=prod) makes stagingRelease compile prod. Nothing reads a flavor name. DslExtension.Builder.extendProductFlavorWith exists in AGP 8.10.x and 9.x.

**Kanıt.**

Fixture d2 (t1 shape, AGP 8.11.1, clone of 80fdab5).

With nothing set, the stagingRelease refusal points only at the build type:
- It reads: "Palbase: `release` (variant `stagingRelease`) has no environment ... `palbase.env.release=<environment>` in gradle.properties, or `buildTypes { release { palbase { environment = ... } } }`".
- Source: EnvironmentResolver.kt:93-100.

After following it (palbase.env.release=prod), the build prints:
- "Palbase: stagingRelease → prod (palbase.env.release in gradle.properties)"
- "Palbase: stagingDebug → local (the default for debug)"
- The APK: `app-staging-release-unsigned.apk: base_url = https://prod.envprobe.palbase.studio`.

A flavor-name key is silently ignored:
- Adding palbase.env.staging=staging gives BUILD SUCCESSFUL with no Palbase line (the task stays UP-TO-DATE).
- The APK is still prod.

The workaround works: the variant key palbase.env.stagingRelease=staging → "stagingRelease → staging", APK staging.

Code:
- EnvironmentResolver.kt:50 `val keys = listOf(variant, buildType).distinct()`.
- AndroidVariantIntegration.kt:65 `resolver.resolve(variant.name, variant.buildType ?: variant.name)`.
- No flavor is read anywhere.

javap of the cached gradle-api jars:
- `DslExtension$Builder extendProductFlavorWith(Class)` exists in 8.10.1, 8.11.1, 9.1.1 and 9.3.2.
- So does `VariantExtensionConfig.productFlavorsExtensions(Class)`.
- `ComponentIdentity.getProductFlavors(): List<Pair<String,String>>` exists in 8.10.1 and 9.3.2.

It is not silent (the lifecycle line names the origin), but the design gives no flavor-level choice. The refusal text leads straight to the wrong setting for flavored apps.

**Düzeltme.**

Plugin repo:
- Add a flavor level between the variant and build-type keys: `palbase.env.<flavor>`, then the flavor combination, plus `palbase { environment }` on product flavors via extendProductFlavorWith.
- Refuse when the flavor and the build type name different environments.
- In a flavored module, word the release refusal around `palbase.env.<variant>` keys.
- Enumerate `providers.gradlePropertiesPrefixedBy("palbase.env.")`, the local.properties keys and the -P keys. Fail on any key that names no variant, flavor or build type of the module. This catches the silently ignored `palbase.env.staging`.

### D3a — confirmed / major (prototipte: yes, repo: plugin + palbase-cli)

**İddia.** A stale <module>/palbase/environments, left by running `palbase link` inside app/, silently shadows the root copy.

**Kanıt.**

Plugin side:
- The roots are `listOf(project.layout.projectDirectory, rootDirectory)...map { it.dir(ENVIRONMENTS_PATH) }` (AndroidVariantIntegration.kt:57-61).
- The task picks `environmentRoots.get().map{it.asFile}.firstOrNull { it.isDirectory }` (GeneratePalbaseTask.kt:126).
- The lifecycle line does not name the root (:163).

Fixture d3a: root palbase/environments/{local,main}, plus app/palbase/environments/local with a STALE base_url.
- assembleDebug prints "Palbase: debug → local (the default for debug)", BUILD SUCCESSFUL.
- The APK: `base_url = https://STALE-module-copy.envprobe.palbase.studio`.
- With palbase.env.release=main (present only at the root), release fails with ".../fx/d3a/app/palbase/environments/main does not exist, and this checkout carries local". That failure is loud, but the debug case is silent.

CLI side (palbase-cli 20e5d7e; read only, Go is not installed):
- link_artifacts.go:141 `root, err := os.Getwd()`. There is no walk-up to the checkout root.
- The gate is not PlaneOf; the reviewer's planes.go:58 citation is imprecise. It is detectPlatforms → detectAndroidApplicationID (planes.go:317, 328-333), whose candidates include `filepath.Join(root, "build.gradle.kts")`.
- My Python emulation of that function: cwd=app/ → "com.std.app", so android is detected inside app/.
- `palbase link <project>` from app/ therefore writes app/palbase/.
- A no-target link needs app/palbase/project.json, a `palbase start` record there, or a machine-local target (project_link.go:315-360).

**Düzeltme.**

Plugin:
- When more than one candidate root exists (module and checkout root), FAIL and name both paths.
- Always print the chosen root in the lifecycle line.

palbase-cli:
- Before writing, walk up from cwd to the directory that holds palbase/project.json or .git, and write there.
- Or refuse to link from a directory below an existing palbase/project.json.

### D3b — confirmed / major (prototipte: yes, repo: plugin + palbase-cli)

**İddia.** RN/Flutter layout: palbase/ is at the repo root and the Gradle root is repo/android. The plugin finds nothing; the build is green with no config in the APK and no log line.

**Kanıt.**

Fixture d3b: repo/palbase/environments/{featureX,local,main,prod,staging}, Gradle root repo/android. assembleDebug + assembleRelease with palbase.env.release=main:
- "> Task :app:generatePalbaseDebug", "> Task :app:generatePalbaseRelease", BUILD SUCCESSFUL.
- There is no "Palbase:" line.
- Both APKs: `<no palbase asset>`; `unzip -l app-debug.apk | grep -ci palbase` = 0.
- Even the release refusal is skipped.

Code: GeneratePalbaseTask.kt:126 `...firstOrNull { it.isDirectory } ?: return` runs before any logging or refusal. Spec A itself says "None → no-op (as today)".

This layout is reachable from the CLI:
- layout.go:37-38: "an RN app has web and android side by side".
- applePlatforms scans ios/, macos/ and apple/ (planes.go:137).
- `--platform android` at the repo root is accepted: refuseUnsupportedPlatforms checks only web (project_link.go:1446-1460). The CLI then writes repo/palbase/..., which the Gradle build in repo/android never sees.

**Düzeltme.**

Plugin:
- Add candidate roots: `<rootDir>/palbase/environments`, then `<rootDir>/../palbase/environments`, bounded by .git or palbase/project.json. Read them at execution time, as now.
- Fail on ambiguity.
- When no root is found, print a lifecycle line listing the searched paths.
- Consider failing non-debuggable variants when no root is found and a palbase/project.json exists above.

CLI: see new finding N2.

### D4 — confirmed / minor (prototipte: yes, repo: plugin + docs)

**İddia.** A plain `benchmark` build type (older macrobenchmark template: initWith release, matchingFallbacks release) resolves to env "benchmark" and fails. AGP rejects build types named test*, androidTest*, lint and main.

**Kanıt.**

Fixture d4 (AGP 8.11.1, clone 80fdab5, palbase.env.release=prod), `--continue`:
- "Palbase: benchmarkRelease → prod (palbase.env.release in gradle.properties, as `benchmarkRelease` builds as `release`)".
- "> Task :app:generatePalbaseBenchmark FAILED > Palbase: environment `benchmark` (from the build type name) has no directory ...".
- Code: EnvironmentResolver.kt:125-129 `takeIf { buildType.startsWith(prefix) && it.firstOrNull()?.isUpperCase() == true }`. A bare `benchmark` is not mapped, so it falls to the name rule (:84-86).
- The failure is loud, not silent.

AGP alone, create(<name>) + assemble<Name>:
- On 8.11.1 and on 9.3.2 alike:
  - test and testing → "BuildType names cannot start with 'test'".
  - androidTestFoo → "...cannot start with 'androidTest'".
  - lint → "BuildType names cannot be lint".
  - main → "Could not create task ':app:compileMainJavaWithJavac' / ':app:mergeMainAssets'. > Multiple entries with same key: main=[] and main=[]". AGP accepts `main` in the DSL but it crashes at task creation, so the reviewer's "`main` is accepted" holds only for configuration and onVariants.
  - local → OK on 8.11.1. On 9.3.2 it passed configuration; the build later stopped only because aapt2 9.3.2 is not cached offline.

**Düzeltme.**

Plugin:
- In step 5, also treat an exact `benchmark` build type (no suffix) as measuring its first DSL matchingFallbacks entry, read in finalizeDsl, else `release`.
- Or document it.

Docs: environment names starting with test or androidTest, `lint` and `main` can never be chosen by the build-type-name rule. They need a `palbase.env.<buildType>` key or the DSL. This matters because the default Palbase environment is named `main`.

### D5 — partly / minor (prototipte: no, repo: plugin)

**İddia.** On APFS, 2.3's File.isDirectory lets build type featureX match directory featurex/: it passes on a Mac and fails on Linux CI. The prototype now requires an exact-name match and fails with a clear message on a case-only mismatch.

**Kanıt.**

2.3 source (palbackend-android-src e72f704), GeneratePalbaseTask.kt:98-99:
`val environmentDirectory = root.resolve(selected)` / `if (!environmentDirectory.isDirectory) {`

On this disk (APFS, case-insensitive), `ls -d .../environments/featureX` succeeds with only `featurex/` present.

Released 2.3.0 from the file maven repo, `-Ppalbase.env=featureX`, dir `featurex/`: EXIT=0, `APK app-debug.apk: base_url = https://featurex.envprobe.palbase.studio`. Linux was not tested; on a case-sensitive file system isDirectory would be false, which is the refusal path.

Proto 80fdab5:
- GeneratePalbaseTask.kt:145 `root.resolve(selected).takeIf { selected in known }` is an exact match against listFiles names.
- Fixture d5 (build type featureX, dir featurex): EXIT=1, "Palbase: environment `featureX` (from the build type name) has no directory — .../environments/featureX does not exist, and this checkout carries featurex, local, main. ...".

The exact match is fixed. The message is only half clear: on macOS "does not exist" is literally false (the path resolves), and it does not say the only difference is case. The reader must spot `featurex` in the list.

**Düzeltme.**

Plugin, GeneratePalbaseTask. When `selected !in known` but `known.firstOrNull { it.equals(selected, ignoreCase = true) }` exists, throw a dedicated message:
"`<twin>` differs from `<selected>` only in letter case; environment names are case-sensitive (a Linux CI would not find it). Rename, or map it: palbase.env.<buildType>=<twin>".

### D6a — confirmed / none (prototipte: n/a, repo: docs)

**İddia.** The assistant told the user that a featureX build type created with initWith(debug) does NOT get src/debug/** nor debugImplementation dependencies.

**Kanıt.**

Fixtures d6a-8.11.1 (Gradle 8.13) and d6a-9.1.1 (Gradle 9.3.1). Plain AGP, no Palbase plugin.

Setup:
- featureX = `create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") }`.
- app/src/debug/assets/dbg-only.txt and app/src/debug/java/t/app/DebugOnlySource.java.
- `debugImplementation(project(":dbgonly"))`, where :dbgonly is a java-library with dbg.DebugOnlyDep.

Results, identical on both AGP versions:
- `:app:dependencies`: debugRuntimeClasspath has `\--- project :dbgonly`. featureXRuntimeClasspath does not: "No dependencies" on 8.11.1, and only kotlin-stdlib on 9.1.1.
- app-debug.apk: asset=1, DebugOnlySource in dex=1, DebugOnlyDep in dex=1.
- app-featureX.apk: asset=0, DebugOnlySource=0, DebugOnlyDep=0.
- aapt2 badging shows `application-debuggable` for both, so initWith copies debuggable but not source sets or configurations.

**Düzeltme.**

None needed; the statement is accurate on AGP 8.11.1 and 9.1.1.

### D6b — partly / none (prototipte: n/a, repo: docs)

**İddia.** The assistant told the user that in Kotlin DSL a bare featureX { } does not compile and create("featureX") is required.

**Kanıt.**

The bare form fails on both versions:
- AGP 8.11.1: "app/build.gradle.kts:4:18: Unresolved reference: featureX".
- AGP 9.1.1: "Unresolved reference 'featureX'."

But create is not the only form that works. On both versions, `register("featureR") { initWith(getByName("debug")) }`, `val featureC by creating { ... }` and `maybeCreate("featureM").apply { ... }` compile. `:app:tasks --all` lists assembleFeatureC, assembleFeatureM and assembleFeatureR (EXIT=0).

Groovy accepts `featureX {}` (reported by the earlier session; not re-run here).

**Düzeltme.**

Docs and wording: "a bare `featureX { }` does not compile in Kotlin DSL; declare it by name, e.g. `create("featureX") { }` (register/maybeCreate/`by creating` also work)".

### YENİ — N1: a flavored app plus the plugin in an unflavored library silently ignores the variant keys (the only D2 workaround), and the fallback check stays quiet (blocker)

**Senaryo.** The app has flavorDimensions env with staging/prod and depends on :lib. :lib applies io.palbase.codegen and has only debug/release. gradle.properties sets palbase.env.release=prod and palbase.env.stagingRelease=staging. `:app:assembleStagingRelease` succeeds, and the staging APK carries prod. There is no warning, because every app build type exists in the library. The only line printed is `release → prod`, with no 'ALSO packed into' suffix.

**Kanıt.**

Fixture fx/n1 (AGP 8.11.1, clone 80fdab5): EXIT=0. The log prints only "> Task :lib:generatePalbaseRelease" and "Palbase: release → prod (palbase.env.release in gradle.properties)". Then `APK app-staging-release-unsigned.apk: base_url = https://prod.envprobe.palbase.studio` and `APK app-prod-release-unsigned.apk: base_url = https://prod...`. `:lib:tasks --group palbase` lists only generatePalbaseDebug and generatePalbaseRelease. LibraryFallbackCheck.kt:87 `if (buildType.name in libraryBuildTypes) return@mapNotNull null` compares build types only; it never looks at app flavors or app-variant keys.

**Düzeltme.**

Plugin. In LibraryFallbackCheck, enumerate the app's variants (productFlavors × buildTypes from ApplicationExtension). For each variant, compare `resolve(appVariant, appBuildType)` with the library variant AGP picks. Fail that app variant through the same pre<Variant>Build guard proposed for D1. Independently, fail on any `palbase.env.<X>` key (gradle.properties, local.properties or -P) that names no variant or build type of the module that applies the plugin, and say why.

### YENİ — N2: `palbase link` at an RN/Flutter repo root never detects Android, so the RN zero-config Android flow cannot produce a config the plugin finds (major)

**Senaryo.** React Native or Flutter repo: ios/ and android/ sit side by side, and android/app/build.gradle declares applicationId. The developer runs `palbase link <project>` at the repo root. Apple is detected through ios/, but Android is not. With `--platform android` added, the CLI writes repo/palbase/environments/<env>/android-config.json. The Gradle build runs with root repo/android, never looks there, and ships an APK with no config (D3b).

**Kanıt.**

palbase-cli 20e5d7e, read only (Go is not installed):
- planes.go:137 `for _, sub := range []string{"", "ios", "macos", "apple"}`: Apple scans the cross-platform subdirectories.
- planes.go:329-333: detectAndroidApplicationID's only candidates are `<root>/app/build.gradle.kts`, `<root>/app/build.gradle`, `<root>/build.gradle.kts` and `<root>/build.gradle`. There is no android/ subdirectory.
- social_link.go:239-240 uses the same list.

Python transcription of detectAndroidApplicationID on a mock tree:
- "RN repo root -> None"
- "RN android/ -> com.rn.app"

refuseUnsupportedPlatforms (project_link.go:1446-1460) checks only web, so `--platform android` at the root is accepted.

**Düzeltme.**

palbase-cli: add `android/app/build.gradle(.kts)` and `android/build.gradle(.kts)` to the detectAndroidApplicationID and nativeIdentifiers candidate lists, mirroring applePlatforms. Plugin: the parent-directory root candidate from D3b, so repo/android finds repo/palbase.

#### Notlar

## plugin-topology: verification summary

I tested against a clone of prototype 80fdab5 in `verify/plugin-topology/proto`. The fixtures in `verify/plugin-topology/fx/` were rebuilt from the reviewer shapes. The originals point at the pre-fix `proto-snap` snapshot, so I pointed my copies at the clone. Every build ran `--offline`. The logs are in `verify/plugin-topology/fx/*.log`.

Go is not installed. I verified the CLI claims by reading palbase-cli at 20e5d7e and by transcribing `detectAndroidApplicationID` into Python.

### Verdicts

| ID | Verdict | Severity now |
|---|---|---|
| D1 | partly | blocker |
| D2 | confirmed | major |
| D3a | confirmed | major |
| D3b | confirmed | major |
| D4 | confirmed | minor |
| D5 | partly | minor |
| D6a | confirmed | none |
| D6b | partly | none |

There are two new findings:
- **N1, blocker:** flavored app plus plugin in a library. Variant keys are silently ignored, with no warning.
- **N2, major:** the CLI never detects Android at an RN/Flutter repo root.

### D1: the fallback warning is not enough
- The warning fires in both shapes (featureX→debug and staging→release), on AGP 8.11.1 and on 9.1.1.
- A build that reuses the configuration cache prints nothing from Palbase. The APK still carries the fallback environment (prod in the staging APK), and the build is green even with `palbase.env.staging=staging` committed.
- The reviewer's env-stamp idea does not work when the app does not apply the plugin: nothing runs in the app module, and there is only one asset to compare.
- A variant-scoped hard fail does work. I measured it with an init-script probe that adds `doFirst { throw }` to `:app:preStagingBuild` from `projectsEvaluated`, the hook the check already uses:
  - The staging build fails on configuration-cache store and on reuse.
  - Debug and release still build.

### D6
- **(a) is accurate** on AGP 8.11.1 and 9.1.1, checked in both the APK contents and the classpath:
  - featureX built with `initWith(debug)` gets no `src/debug` assets or classes.
  - It gets no `debugImplementation` dependency.
- **(b) is half right.** A bare `featureX {}` fails on both versions. But `register`, `by creating` and `maybeCreate` all compile, so saying `create` is required is too strong.

### Limits
- **AGP 9.3.2** could not be used with the prototype offline. Gradle 9.5.0 needs `kotlin-stdlib-common:2.3.20`, which is not cached, and aapt2 9.3.2 is not cached either. I used 9.3.2 only for AGP-only configuration checks, and AGP 9.1.1 for everything that builds APKs.
- **The D1 run on AGP 9.1.1** stopped at `:lib:compileReleaseKotlin`, because my minimal library fixture has no `palbe` dependency. The warning had already fired.
- **Not tested:** Linux (a case-sensitive file system), Android Studio sync, and Isolated Projects.

### State
- The real repos are unchanged: palbase-cli 20e5d7e, palbackend-android-src e72f704, palbackend-android d840817 and palbe-trial-android acedb85 are all clean. The user's test app is not a git repo; I only read its `gradle.properties`.
- The evidence sandbox `proto/src` is still at 80fdab5 and clean.
- No Gradle daemons were stopped, nothing was published, and no cloud calls were made.

---

## verify:versioning

### E1 — confirmed / minor (prototipte: n/a, repo: plugin (palbackend-android-src) + docs)

**İddia.** In palbackend-android-src the plugin cannot ship alone as 2.4: scripts/publish.sh ships every artifact under one PALBE_VERSION and refuses unless 11 artifact directories were copied, and the README says the library and the plugin must be the SAME version. The right move is to release everything as 2.4.0.

**Kanıt.**

Read at HEAD e72f704.

publish.sh has no plugin-only path:
- Step 2 builds the whole tree at $version: scripts/publish.sh:73-77 (`PALBE_VERSION="$version" ./gradlew publishReleasePublicationToTestRepository :codegen-engine:... ` then `-p codegen-gradle :publishAllPublicationsToTestRepository`).
- The copy step refuses below 11 directories. publish.sh:119-120: `if copied < 11:` / `sys.exit(f"expected 11 artifact directories for {version}, copied {copied}")`. The check is `< 11`, not `!= 11`.
- Step 5 requires all 11 packages to list $version. publish.sh:158-172 names 8 runtime modules plus palbase-codegen-engine, codegen-gradle and the plugin marker.
- Every module takes the same version. codegen-gradle/build.gradle.kts:54-59 reads `providers.environmentVariable("PALBE_VERSION")`, and so do codegen-engine/build.gradle.kts:28 and each palbe-*/build.gradle.kts.

The docs state the lockstep rule:
- README.md:65-67: "The library and the plugin must be the SAME version: the plugin generates code against that version's runtime, and the two are released together".
- distribution/README.md:47-48 says the same.
- distribution/README.md:188-189: "Library, engine and plugin always share one version number."
- CHANGELOG.md:16-20 records why: 2.1.0 and 2.2.0 shipped a runtime while the plugin stayed on 2.0.1.

There is also a hard dependency the reviewer did not mention. The published plugin POM pins the engine at the same version: palbackend-android/io/palbase/codegen-gradle/2.3.0/codegen-gradle-2.3.0.pom:19-22 has `palbase-codegen-engine` `2.3.0`. A plugin-only 2.4.0 would therefore still need engine 2.4.0.

Re-releasing the runtime at 2.4.0 is safe:
- `git diff --stat v2.3.0..HEAD` on all runtime modules, codegen-engine, shared and codegen-gradle is empty (see E4).
- The only runtime byte that changes is palbe-core's BuildConfig PALBASE_SDK_VERSION (palbe-core/build.gradle.kts:25). verify-publications.sh:33-45 requires that value to equal the Maven version.
- palbase-cloud checks no X-Palbase-Sdk-Version header. `git grep -i x-palbase-sdk-version` finds only the docs line studio/.../ios/backend-calls.md:286.

**Düzeltme.**

Release all 11 coordinates as 2.4.0 (plugin changed, runtime and engine rebuilt unchanged) with the existing `PALBE_VERSION=2.4.0 scripts/publish.sh`. Then:
- add a `## 2.4.0` section in CHANGELOG.md;
- update the example versions in README.md:262-264 and distribution/README.md:54, 66 and 188.

Do not add a plugin-only mode. It would contradict README.md:65, distribution/README.md:189 and the POM's same-version engine pin.

### E2 — confirmed / minor (prototipte: yes, repo: plugin (palbackend-android-src))

**İddia.** The SDK repo's own release gate breaks under 'release refuses unless mapped': consumer-release builds against sample/palbase/environments, which holds only local/, and the README gate runs `check lintRelease :consumer-release:assembleRelease`. The fix is e.g. palbase.env.release=local.

**Kanıt.**

Code read at HEAD:
- consumer-release/build.gradle.kts:50-52 sets `palbase { environmentsDir.set(rootProject.layout.projectDirectory.dir("sample/palbase/environments")) }`.
- `git ls-files sample/palbase` lists only sample/palbase/environments/local/{android-config.json,openapi.json}.
- sample/build.gradle.kts has no block; it is found through its own module dir.
- Neither module maps release.
- The root gradle.properties:1-7 has no palbase.* key.
- The gate is README.md:211-214: `./gradlew check lintRelease :consumer-release:assembleRelease --configuration-cache --no-daemon`. The plugin gate is README.md:216: `./gradlew -p codegen-gradle check`.

Runs, all on copies in verify/versioning/:
1. 2.3 copy (sdk23), `./gradlew check lintRelease :consumer-release:assembleRelease -m`: the graph contains `:consumer-release:generatePalbaseRelease` and `:sample:generatePalbaseRelease` (gate23-dryrun.log).
2. 2.4 copy (sdk24 = repo HEAD with codegen-gradle/src replaced by proto 80fdab5), generate tasks: both release tasks FAILED with "Palbase: `release` has no environment, and a release never gets one by default … This checkout carries local." Debug printed `Palbase: debug → local (the default for debug)` (gen24-a.log).
3. Added one line, `palbase.env.release=local`, to the copy's root gradle.properties: `Palbase: release → local (palbase.env.release in gradle.properties)` for both modules.
   - The full README gate `check lintRelease :consumer-release:assembleRelease --configuration-cache --no-daemon` ended "BUILD SUCCESSFUL in 59s, 872 actionable tasks", EXIT=0 (gate24-full.log).
   - `./gradlew -p codegen-gradle check --configuration-cache` ended BUILD SUCCESSFUL, with EnvironmentResolverTest 23/0, LibraryFallbackCheckTest 8/0 and PalbaseCodegenPluginTest 47/0 failures (plugin24-check.log).

CI is NOT affected. .github/workflows/ci.yml:68 runs only `:codegen-gradle:test :palbe-core:testDebugUnitTest`, which are debug tasks. Only the local README gate breaks.

**Düzeltme.**

palbackend-android-src/gradle.properties: add `palbase.env.release=local`, with a comment that the sample and the minified consumer gate build against the sample's only environment. That one line is the whole change, measured green on both README gates.

Alternative: `release { palbase { environment = "local" } }` in sample/build.gradle.kts and consumer-release/build.gradle.kts. That needs `import io.palbase.gradle.palbase` in each .kts, and it is two edits instead of one.

The consumer-release block (lines 47-52) keeps working unchanged. Do not use the legacy global `palbase.env=local`. It also passes (measured: `release → local (palbase.env, the 2.3 global property)`), but it is the deprecated path.

### E3 — partly / minor (prototipte: yes, repo: docs + plugin (palbackend-android-src), palbe-trial-android, user test app)

**İddia.** Everything that pins 2.3 behaviour or text and must change with 2.4: PalbaseCodegenPluginTest local-default assertions, the README environment section, distribution/README.md, CHANGELOG, the trial app (palbase.env=main plus the palbase{} block), and the user's test app.

**Kanıt.**

The list is mostly right, but three items are overstated and one is understated.

(a) The PalbaseCodegenPluginTest assertions do NOT need to change:
- :49 `without the property the local environment is compiled`, :63, :82-89 and :271-282 all build `assembleDebug` only.
- 2.4 keeps debug → local.
- Lines 1-300 of the HEAD test file are byte-identical to the proto's (`diff` printed nothing).
- The proto suite that contains them passed 47/47 in the sdk24 copy.
- Only the comment at :41-47 ("THE ENVIRONMENT IS ONE KEY THE USER OWNS … `-Ppalbase.env=<name>` … Unset means `local`") is stale.

(b) The trial app keeps working under 2.4 and needs only version bumps:
- palbe-trial-android@acedb85 has gradle.properties:4-5 `palbase.env=main`, the block at app/build.gradle.kts:32-35, and only the debug and release build types.
- Step 7 (legacy) still serves debug and release: EnvironmentResolver.kt:84-89 at 80fdab5.
- Prior matrix M11 showed `release → main (palbase.env, the 2.3 global property)`.
- My own sdk24 run with `palbase.env=local` printed `Palbase: release → local (palbase.env, the 2.3 global property)` / `debug → local (…)`.
- What must change: build.gradle.kts:5 `version "2.3.0"` and app/build.gradle.kts:38 `io.palbase:palbe:2.3.0`.

(c) The user's test app needs more than a bump:
- It pins the versions at app/build.gradle.kts:7 (plugin "2.3.0") and gradle/libs.versions.toml:11 (palbe = "2.3.0"), with gradle.properties:16-17 `palbase.env=main` and the block at app/build.gradle.kts:67-70.
- app/build.gradle.kts:43 and :50 declare bare `featureX {` / `featureY {` in .kts. Review-flow G-J reports this does not compile ("Unresolved reference 'featureX'"). They must become `create("featureX")`.
- Under 2.4 those build types then resolve to their own names (step 6 comes before step 7, EnvironmentResolver.kt:84-86) while only palbase/environments/main exists. So each also needs `palbase.env.featureX=main` / `palbase.env.featureY=main`, or its own env dir.

(d) The public distribution README is a copy:
- /Users/erkutbas/Github_Pallasite/palbackend-android/README.md is identical to palbackend-android-src/distribution/README.md (`diff` said IDENTICAL).
- publish.sh:123 overwrites it (`cp "$src_root/distribution/README.md" "$dist/README.md"`).
- Edit only the src copy.

COMPLETE LIST (file:line at current HEADs):

palbackend-android-src@e72f704:
- PalbaseCodegenPlugin.kt:12-41: the `palbase.env` default "local" and the environmentsDir convention. The proto replaces them.
- PalbaseExtension.kt:15 (doc).
- GeneratePalbaseTask.kt:33, :116 (refusal text `-Ppalbase.env=<environment>`) and :178. The proto replaces these.
- PalbaseCodegenPluginTest.kt:41-47: comment only.
- README.md:
  - :55-63: requirements table; add the AGP/Gradle range, see E5.
  - :94-107: "Which environment a build compiles is the `palbase.env` Gradle property, and `local` when it is unset", `-Ppalbase.env=main`, and "a CI job passes the environment it releases".
  - :114-121: environmentsDir block.
  - :262-264: example 2.3.0.
  - :65-67 stays valid if everything ships as 2.4.0.
- CHANGELOG.md:10 ("Son etiket: `v2.3.0`") and :12 (empty "## Yayınlanmamış", where the 2.4.0 entry goes).
- gradle.properties: add `palbase.env.release=local` (E2).
- distribution/README.md:
  - :54 and :66: `2.3.0` pins.
  - :89: "Tried on Android Gradle Plugin 8.11 and 9.1, Gradle 8.13 and 9.3."
  - :105-110: `palbase.env` / `local` when unset / `palbase.env=main`.
  - :112-119: block.
  - :188: `v2.3.0`.
- scripts/publish.sh:19 and :33: example text only, optional.

palbe-trial-android@acedb85:
- build.gradle.kts:5 and app/build.gradle.kts:38: required.
- gradle.properties:4-5 and app/build.gradle.kts:32-35: optional migration.

MyApplicationPalbaseAndroidSdkTest (not a git repo):
- app/build.gradle.kts:7, gradle/libs.versions.toml:11: required.
- app/build.gradle.kts:43, :50: required, plus the featureX/featureY mapping.
- gradle.properties:16-17, app/build.gradle.kts:67-70 and :73 (comment): optional.

No live code in palbase-cli or palbase-cloud references `io.palbase.codegen`, `palbase.env`, `io.palbase:palbe` or `environmentsDir` (git grep: no hits outside docs/plan files).

**Düzeltme.**

Docs:
- Rewrite README.md:94-121 and distribution/README.md:105-119. The proto's README diff (be14cf1..80fdab5) is a usable draft.
- Add the supported AGP/Gradle range to both requirement tables.
- Add a CHANGELOG 2.4.0 section.
- Fix the stale test comment at PalbaseCodegenPluginTest.kt:41-47.

Apps:
- Trial: bump the two pins to 2.4.0. Optionally replace `palbase.env=main` with `palbase.env.debug=main` + `palbase.env.release=main` and drop the block.
- Test app: bump the pins, change `featureX {`/`featureY {` to `create("featureX")`/`create("featureY")`, and add `palbase.env.featureX=main` / `palbase.env.featureY=main` (or link those environments).

The plugin tests need no change for 2.4 beyond what the proto adds.

### E4 — confirmed / none (prototipte: no, repo: plugin (palbackend-android-src))

**İddia.** The palbe runtime reads only the asset palbase/palbase-config.json (GeneratedConfigLoader.kt), and codegen-engine, shared and codegen-gradle are unchanged since v2.3.0, so a resolution-only 2.4 generates the same code. Also: which files did the prototype touch (3ffb61f..80fdab5), and does any of them touch codegen output?

**Kanıt.**

Runtime:
- palbe-core/src/main/kotlin/io/palbase/core/GeneratedConfigLoader.kt:15 has `internal const val GENERATED_CONFIG_ASSET = "palbase/palbase-config.json"`, read at palbe-core/.../Palbase.kt:102 `context.assets.open(GENERATED_CONFIG_ASSET)`.
- One reader the claim missed, same file: palbe-purchases/.../GeneratedConfig.kt:38 (same constant) and :92 `context.assets.open(GENERATED_CONFIG_ASSET)`.
- No runtime module reads `palbase.env` or `environments/` (git grep: no hits).

Repo diff:
- `git log v2.3.0..HEAD` shows only e72f704 (v2.3.0 = c9d9866).
- `git diff --stat v2.3.0..HEAD` touches only CHANGELOG.md, README.md, distribution/README.md and scripts/publish.sh.
- The same diff limited to codegen-engine, shared, codegen-gradle, gradle and all 8 palbe* modules printed nothing.
- The proto baseline 3ffb61f equals v2.3.0: `diff -rq` of git-archived trees for codegen-engine, shared, codegen-gradle, gradle, contracts and gradle.properties shows no difference (only gradlew, which I did not extract from v2.3.0).

Proto `git diff 3ffb61f..80fdab5 --stat` lists 13 files, +1982/-110:
- .gitignore
- CHANGELOG.md, README.md (added as copies at be14cf1; the real edits are +112/-13 in be14cf1..80fdab5)
- codegen-gradle/src/main/.../AndroidVariantIntegration.kt, EnvironmentResolver.kt (new), GeneratePalbaseTask.kt, LibraryFallbackCheck.kt (new), PalbaseBuildType.kt (new), PalbaseCodegenPlugin.kt, PalbaseExtension.kt
- tests: EnvironmentResolverTest.kt, LibraryFallbackCheckTest.kt, PalbaseCodegenPluginTest.kt

It does NOT touch codegen-engine or shared (`--stat -- codegen-engine shared` is empty).

GeneratePalbaseTask.kt has 5 hunks: imports @@-12, properties @@-30 and @@-52, environment selection and log line @@-91, and a doc comment @@-175. Everything after `environmentDirectory` is resolved (spec/config read, generateKotlin, asset, manifest, res) is untouched.

Measured byte-identity:
- Ran the 2.3 plugin (sdk23) and the 2.4 proto (sdk24, release mapped to local) on the same inputs: `:sample` and `:consumer-release` `generatePalbaseDebug` and `generatePalbaseRelease`.
- `cmp` of every generated file whose path contains generatePalbase (generated/java/.../PalbaseGenerated.kt, generated/assets/.../palbase/palbase-config.json, generated/manifests/.../AndroidManifest.xml, debug and release) found: sample "compared 6 files, 0 differ", consumer-release "compared 6 files, 0 differ".

Side effects that do not change output:
- task inputs changed (`environment` is @Optional, new @Input `environmentRefusal`), so there is one build-cache miss after upgrading;
- a root-level palbase/ without a block goes from no-op (2.3) to generating (2.4).

**Düzeltme.**

None needed. A resolution-only 2.4.0 ships the runtime unchanged (see E1).

### E5 — partly / minor (prototipte: yes, repo: plugin (palbackend-android-src) + docs)

**İddia.** The assistant told the user that AGP's official DSL extension API (DslExtension.extendBuildTypeWith) supports `palbase { environment = "..." }` inside a build type, verified in the AGP 8.10.1 jar, and called it 'plugin 2.4'. Check the plugin's declared minimum AGP/Gradle, whether DslExtension + registerExtension exist from that minimum, and the range 2.4 must declare.

**Kanıt.**

Declared minimum: NONE.
- codegen-gradle/build.gradle.kts:37 `compileOnly("com.android.tools.build:gradle:8.10.1")` and :41 testAgp 8.10.1. TestKit runs on the wrapper, Gradle 8.11.1 (gradle/wrapper/gradle-wrapper.properties).
- The README.md:55-63 requirements table names no AGP or Gradle version.
- The only statement is distribution/README.md:89: "Tried on Android Gradle Plugin 8.11 and 9.1, Gradle 8.13 and 9.3."
- The plugin has no runtime version check (grep for AndroidPluginVersion/GradleVersion: none).

javap across AGP gradle-api jars:
- 7.4.2 and 8.0.0-8.9.0 were downloaded from dl.google.com/android/maven2 into verify/versioning/agpjars; 8.10.1, 8.11.1, 9.1.1 and 9.3.2 come from ~/.gradle caches.
- `DslExtension$Builder(String)`, `extendBuildTypeWith`, `extendProductFlavorWith`, `AndroidComponentsExtension.registerExtension(DslExtension, Function1)`, `DslLifecycle.finalizeDsl`, `dsl.BuildType extends … ExtensionAware` and `getMatchingFallbacks` are present in EVERY version 7.4.2 → 9.3.2.
- So the API claim is true.

Three caveats:
1. The DSL API is @Incubating in AGP ≤ 8.10.x, including the compileOnly 8.10.1. `javap -v` shows `RuntimeVisibleAnnotations: org.gradle.api.Incubating` on registerExtension and on the DslExtension class for 7.4.2, 8.0.0, 8.3.0 and 8.10.1, and 0 hits for 8.11.1, 9.1.1 and 9.3.2. "Official" is true only in the incubating sense below 8.11.
2. The API is not what sets the floor. `Sources.getManifests()` / `ManifestFiles.addGeneratedManifestFile` (used by 2.3 and 2.4, AndroidVariantIntegration.kt:114) are absent in 8.0.0-8.2.0 and present from 8.3.0. `Component.getDebuggable` also appears in 8.3.0. So the de-facto AGP floor for both 2.3 and 2.4 is 8.3.
3. In Kotlin DSL the syntax works only with `import io.palbase.gradle.palbase` (PalbaseBuildType.kt:47) or `extensions.configure<PalbaseBuildType>`. Groovy needs no import.

The proto adds Gradle floors 2.3 did not have:
- PalbaseCodegenPlugin(org.gradle.api.configuration.BuildFeatures), per javap of the compiled class: `public io.palbase.gradle.PalbaseCodegenPlugin(org.gradle.api.configuration.BuildFeatures)`. BuildFeatures javadoc says "Since: 8.5"; docs.gradle.org/8.4/.../BuildFeatures.html returns 404 and 8.5 returns 200.
- LibraryFallbackCheck.class calls `InterfaceMethod org/gradle/api/artifacts/ProjectDependency.getPath` (javap). The ProjectDependency javadoc for 8.10.2 has no getPath; 8.11 has it.
- AGP's own minimum Gradle, from SdkConstants.GRADLE_LATEST_VERSION in com.android.tools:common: 8.3.0 → "8.4", 8.4.0 → "8.6", 8.10.1 → "8.11.1".
- So 2.4 fails at plugin instantiation on AGP 8.3 + Gradle 8.4. It fails in library modules on any Gradle < 8.11 (e.g. AGP 8.8 + Gradle 8.10.2, NoSuchMethodError at projectsEvaluated).
- This is from reading and javap; I did not run Gradle 8.4 or 8.10.

Tested versions, from prior runs:
- AGP 8.10.1 / Gradle 8.11.1 (TestKit; I re-ran it: 78 tests green);
- AGP 8.11.1 / Gradle 8.13;
- AGP 9.1.1 / Gradle 9.3.1;
- AGP 9.3.2 / Gradle 9.5.0 (reviewer).

The Kotlin-DSL import works although the plugin is compiled with Kotlin 2.2.21: Gradle's script compiler sets skipMetadataVersionCheck (javap of gradle-kotlin-dsl-8.11.1.jar KotlinCompilerKt: `AnalysisFlags.getSkipMetadataVersionCheck`).

**Düzeltme.**

Declare the supported range in README.md:55-63, distribution/README.md:77-89 and the CHANGELOG: "AGP 8.10.1 or newer (tested 8.10.1-9.3.2), Gradle 8.11.1 or newer (tested 8.11.1-9.5.0), JDK 17+". AGP 8.10's own floor is Gradle 8.11.1, which covers both BuildFeatures (8.5) and ProjectDependency.getPath (8.11).

If a lower floor is wanted:
- stop injecting BuildFeatures in the constructor;
- gate the getPath call;
- add a TestKit run on the lowest claimed AGP/Gradle pair.

Tell the user that the DSL API is @Incubating below AGP 8.11 and that Kotlin DSL needs the import.

### YENİ — The 2.4 prototype needs a newer Gradle than 2.3 (8.5 for every module, 8.11 for library modules), and nothing declares it (minor)

**Senaryo.** Two consumers fail loudly after upgrading, where 2.3 works for both:
- AGP 8.3 on its minimum Gradle 8.4, applying io.palbase.codegen 2.4: plugin instantiation fails, because the constructor takes org.gradle.api.configuration.BuildFeatures, which Gradle 8.4 does not have.
- AGP 8.4-8.8 on Gradle 8.6-8.10.x, with the plugin in a com.android.library module: LibraryFallbackCheck calls ProjectDependency.getPath() and throws NoSuchMethodError at projectsEvaluated.

**Kanıt.**

javap of the proto classes compiled in verify/versioning/sdk24:
- `public io.palbase.gradle.PalbaseCodegenPlugin(org.gradle.api.configuration.BuildFeatures)`
- LibraryFallbackCheck.class: `invokeinterface … org/gradle/api/artifacts/ProjectDependency.getPath:()Ljava/lang/String;`

Gradle javadoc:
- BuildFeatures "Since: 8.5" (the 8.4 doc URL returns 404).
- The ProjectDependency page for 8.10.2 has no getPath; 8.11 has it.

AGP minimum Gradle (com.android.tools:common SdkConstants.GRADLE_LATEST_VERSION): 8.3.0 → 8.4, 8.4.0 → 8.6, 8.10.1 → 8.11.1.

The 2.3 plugin (AndroidVariantIntegration.kt at HEAD) uses neither API.

Not executed: no Gradle 8.4 or 8.10 was run.

**Düzeltme.**

Plugin repo docs:
- declare "AGP 8.10.1+ / Gradle 8.11.1+" in README.md:55-63 and distribution/README.md:77-89;
- replace "Tried on AGP 8.11 and 9.1" at distribution/README.md:89 with that range;
- add the range to the 2.4.0 CHANGELOG entry.

For a lower floor, obtain BuildFeatures lazily or guard it, gate the getPath call, and add a TestKit case on the lowest claimed pair.

### YENİ — 2.4 is a breaking release under a minor number; the plan documents disagree about it, and the proto CHANGELOG leaves two breaks out (minor)

**Senaryo.** A 2.3 consumer bumps to 2.4.0 expecting a compatible minor release. Four things change at once:
- `release` stops building until it is mapped.
- A custom build type (`staging`) ignores the global `palbase.env` and looks for its own directory.
- CI's `-Ppalbase.env=staging` is outranked by committed `palbase.env.release`.
- Build logic that configures `GeneratePalbaseTask.environmentsDir` no longer compiles.

The analysis says 'nothing is required in 2.4'. The tested spec and the proto say the opposite.

**Kanıt.**

Proto at 80fdab5:
- EnvironmentResolver.kt:84-86: a non-debug/release build type → its own name, BEFORE legacy step 7 at :87-89.
- :93-100: release → Refused.
- :52-54 plus step 7 (legacy): `-Ppalbase.env` on the command line now ranks last.
- The `environmentsDir` property was removed from GeneratePalbaseTask (diff hunk @@-52).
- PalbaseCodegenPlugin went from `class` with a no-arg constructor to `abstract class … @Inject constructor(BuildFeatures)`.

The documents:
- analysis-final.md §3.10: "Existing consumers: nothing is required in 2.4. The block, a global palbase.env and the local default all keep working". §3.9.5: "In strict mode, an unmapped release refuses".
- The workflow SPEC, step B.8: "B == release → FAIL".

The proto CHANGELOG (be14cf1..80fdab5) names the release break, the custom-build-type change and the task API change. It does NOT name the `-Ppalbase.env=X` command-line demotion or the new Gradle floor.

House precedent: 2.3.0 shipped a build-breaking change as a minor under "DAVRANIŞ DEĞİŞİKLİĞİ" (CHANGELOG.md:77-89: "Yükselten bir uygulama … derlenmez — bu bilinçli").

**Düzeltme.**

Docs:
- Decide explicitly: 2.4.0 with a complete DAVRANIŞ DEĞİŞİKLİĞİ list (matches house practice), or 3.0.0.
- Either way, add to the CHANGELOG entry: `-Ppalbase.env=X` on the command line no longer beats per-build-type keys (use `-Ppalbase.env.<buildType>=X`), and the AGP/Gradle floor.
- Correct analysis-final §3.10 so it matches the refusal-by-default spec, or change the spec back to non-strict.

#### Notlar

## Versioning: verification summary

**Verdicts**

| Claim | Verdict | Severity | In the proto | Short finding |
|---|---|---|---|---|
| E1 | confirmed | minor | n/a | 2.4 cannot ship as a plugin-only release, so ship all 11 coordinates as 2.4.0. This is also forced by the plugin POM, which pins palbase-codegen-engine at the same version. |
| E2 | confirmed | minor | yes | The README release gate fails on the 2.4 proto. One line, `palbase.env.release=local`, turns both README gates green (measured). CI is not affected. |
| E3 | partly | minor | yes | The list is right, with corrections: the plugin tests need no change (they pass 47/47 unchanged); the trial app needs only its two version pins bumped; the user's test app needs more than a bump. The complete file:line list is in the claim. |
| E4 | confirmed | none | no | The generated outputs are byte-identical between 2.3 and the 2.4 proto (12 files compared). |
| E5 | partly | minor | yes | The DSL extension API exists in AGP 7.4.2 through 9.3.2. But it is @Incubating below 8.11, and the proto raises the Gradle floor without declaring it (8.5 for every module, 8.11 for library modules). |

**What I ran**

Everything ran on copies in `/private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/266a545b-bd07-454f-b730-c8c05afdf07d/scratchpad/verify/versioning/`:
- `sdk23/` is palbackend-android-src HEAD e72f704, taken with `git archive`.
- `sdk24/` is the same tree with `codegen-gradle/src` replaced by proto 80fdab5, taken from the proto repo with `git archive`.
- `proto80/` is the proto at 80fdab5.
- Logs: `gate23-dryrun.log`, `gen24-a.log` (both release tasks refused), `gen24-b.log` (mapped), `gate24-full.log` (full README gate green, EXIT=0), `plugin24-check.log` (78 plugin tests green), `gen23.log`.
- The byte comparison used `cmp` over every file under `build/**/generatePalbase*`.
- `agpjars/` holds the AGP gradle-api jars for 7.4.2 and 8.0.0 through 8.9.0, downloaded from Google Maven, plus `com.android.tools:common` jars for the minimum-Gradle constants. 8.10.1, 8.11.1, 9.1.1 and 9.3.2 came from `~/.gradle`.
- I checked Gradle javadoc pages on docs.gradle.org to see when `BuildFeatures` and `ProjectDependency.getPath` first appear.

**Limits**

- I read only. There were no git state changes and no edits in any real repo, and I did not modify the evidence sandbox.
- All Gradle runs used `--no-daemon` single-use daemons. I did not run `gradle --stop` or kill any process. Nothing was published and nothing called the Palbase cloud.
- No Go code is in this area, so there was nothing to emulate.
- One read was refused and not retried: printing `sample/palbase/environments/local/android-config.json` and `local.properties` (the api_key and the SDK path). I used `ANDROID_HOME` instead of copying `local.properties`.
- Not run: Gradle 8.4 or 8.10, so the E5 Gradle floors come from javap and javadoc; `scripts/verify-publications.sh`; a real build of the trial app. Trial-app compatibility rests on reading the resolver, matrix case M11, and my own legacy-property run in `sdk24`.

**One correction to the reviewer's evidence**

`PalbaseCodegenPluginTest.kt:49` and `:82-89` pin debug→local and the refusal text. They do not pin release, and 2.4 keeps debug→local, so they pass unchanged.

---

## verify:plugin-order

### C1 — confirmed / major (prototipte: yes, repo: plugin)

**İddia.** Resolution step B.6 (build-type name) runs before B.7 (legacy global palbase.env), so 2.3 consumers with a custom build type break on upgrade, including the user's AGP 9 test app.

**Kanıt.**

CODE at 80fdab5, EnvironmentResolver.kt:84-89: `if (buildType != DEBUG && buildType != RELEASE) { return EnvironmentResolution.Selected(buildType, "from the build type name") }` comes BEFORE `sources.legacy()?.trim()?.let { ... "$LEGACY_PROPERTY, the 2.3 global property") }`. This is deliberate: EnvironmentResolverTest pins it as `the legacy global does not override a build type name`, and CHANGELOG.md:32-33 says custom build types now compile their own name.

REPRO on a sandbox copy of MyApplicationPalbaseAndroidSdkTest (AGP 9.1.1, Gradle 9.3.1, palbase.env=main, only palbase/environments/main; repos pointed at the file repo; --offline):
(0) Bare `featureX {` with plugin 2.3.0, running `:app:help`: `Line 43: featureX { ^ Unresolved reference 'featureX'.` (6 errors, BUILD FAILED). The user's file does not compile in Kotlin DSL as written.
(a) `create("featureX"){initWith(getByName("debug"))}` / featureY, plugin 2.3.0, assembleDebug/Release/FeatureX/FeatureY: BUILD SUCCESSFUL. All 4 APKs carry base_url https://8bbwb2pbm.palbase.studio (= main). Release lintVital tasks were excluded with -x only because offline caches lack material3-desktop.
(b) Same app, prototype through includeBuild: debug and release → main (`Palbase: debug → main (palbase.env, the 2.3 global property)`). generatePalbaseFeatureX and generatePalbaseFeatureY FAIL: "environment `featureX` (from the build type name) has no directory … this checkout carries main". Only the debug and release APKs were produced.
`./gradlew build -m` on AGP 9.1.1 lists generatePalbaseDebug, FeatureX, FeatureY and Release, so `build` fails too.

EXTRA, not in the claim (fx/c1s, AGP 8.10.1, proto 80fdab5): with palbase.env=main, a `staging` build type and a staging/ directory present (link writes every environment), the result is `Palbase: staging → staging (from the build type name)` and the staging APK has base_url https://staging.envprobe.palbase.studio. Under 2.3.0 this variant compiled main. The switch on upgrade is SILENT whenever the same-named directory exists.

**Düzeltme.**

plugin: move the legacy global `palbase.env` ahead of the build-type-name rule, so the name rule applies only when no global is set. I prototyped this in my copy (order-fix.patch, 3 lines moved plus test updates).
- Unit tests: 80/80 codegen-gradle and 46/46 engine pass.
- The user app copy then builds all four variants on main, like 2.3.0: `featureX → main (palbase.env, the 2.3 global property)`, BUILD SUCCESSFUL.

Optionally, print a hint when the legacy value wins for a custom build type whose same-named directory exists.

docs: README/CHANGELOG snippets for .kts must use `create("featureX") { initWith(getByName("debug")) }`, never bare `featureX {`.

### C2 — confirmed / major (prototipte: yes, repo: plugin)

**İddia.** -Ppalbase.env=X on the command line (the 2.3 README CI recipe) is silently outranked by committed per-build-type keys (gradle.properties palbase.env.release=prod), because the legacy global is step 7.

**Kanıt.**

CODE:
- AndroidVariantIntegration.kt:137/143 feeds step 1 only per-variant and per-build-type keys from `gradle.startParameter.projectProperties`.
- EnvironmentResolver.kt:50-53 builds `keys = listOf(variant, buildType)…map { PROPERTY_PREFIX + it }`, so the bare `palbase.env` is never looked up in step 1. It is read only at :87 via `project.providers.gradleProperty("palbase.env")` (AGP integration :147), after DSL, gradle.properties and the name rule.

REPRO fx/c2 (proto 80fdab5, AGP 8.10.1, Gradle 8.13), gradle.properties `palbase.env.release=prod`, `assembleDebug assembleRelease -Ppalbase.env=staging`:
- `Palbase: debug → staging (palbase.env, the 2.3 global property)`
- `Palbase: release → prod (palbase.env.release in gradle.properties)`
- APKs: debug https://staging.envprobe…, release https://prod.envprobe… (BUILD SUCCESSFUL)
- `assembleQa -Ppalbase.env=staging` → "environment `qa` (from the build type name) has no directory". Loud here; per fx/c1s it would be silent if qa/ existed.
- Rerun: both generate tasks UP-TO-DATE, no Palbase line printed at all.

DOCS:
- The real 2.3 README (palbackend-android-src README.md:98-106) teaches `./gradlew assembleDebug -Ppalbase.env=main` and "a CI job passes the environment it releases".
- The proto README.md:146-148 says "first hit wins: command line, …, and last the 2.3 global palbase.env". Readers will take -Ppalbase.env to be "command line".

Mitigation: this needs a mixed setup (2.4 per-build-type keys plus the 2.3 CI flag). The one lifecycle line names the real origin when the task runs.

**Düzeltme.**

plugin: treat `palbase.env` present in gradle.startParameter.projectProperties as step 1b, right after -Ppalbase.env.<V>/<B> and before local.properties. Only a file-sourced legacy value keeps its low rank.

Prototyped in order-fix.patch, together with C1:
- fx/c2f `-Ppalbase.env=staging` gives debug, release and qa → staging `(-Ppalbase.env on the command line)`, all APKs staging.
- Without -P, release → prod.
- A new unit test checks that -Ppalbase.env.release still beats -Ppalbase.env.
- Tests: 80/80 and 46/46.

docs: say explicitly that -Ppalbase.env applies to every variant of that invocation.

### C3 — confirmed / minor (prototipte: yes, repo: plugin)

**İddia.** Step 4 uses providers.gradleProperty, which also sees ORG_GRADLE_PROJECT_*, -Dorg.gradle.project.* and ~/.gradle/gradle.properties. It labels them 'in gradle.properties' and can raise a false DSL-vs-gradle.properties conflict. A module-level app/gradle.properties palbase.env.debug is ignored, and typo keys are silently ignored.

**Kanıt.**

CODE:
- AndroidVariantIntegration.kt:146 `gradleProperties = { key -> project.providers.gradleProperty(key).orNull }`
- EnvironmentResolver.kt:66 refusal text "`${committed.first}=${committed.second}` in gradle.properties" and :73 origin "$key in gradle.properties"
- EnvironmentResolver.kt:175-178 assumes "what it answers is, in practice, gradle.properties"
- Only keys for V and B are ever looked up (:50), so nothing enumerates the others.

REPRO (proto 80fdab5, AGP 8.10.1, Gradle 8.13):
- fx/c3a: `release { palbase { environment = "prod" } }`, and the checkout gradle.properties has NO palbase key. With `env 'ORG_GRADLE_PROJECT_palbase.env.release=staging'` the build FAILS: "names two environments in two committed places — `palbase { environment = "prod" }` in the build type and `palbase.env.release=staging` in gradle.properties". `-Dorg.gradle.project.palbase.env.release=staging` gives the identical false refusal. Control: `-Ppalbase.env.release=staging` → `release → staging (-Ppalbase.env.release on the command line)`.
- fx/c3b: gradle.properties `palbase.env.release=prod`, env var =staging → `Palbase: release → staging (palbase.env.release in gradle.properties)`; the APK is staging. The origin is mislabelled.
- app/gradle.properties `palbase.env.debug=main` → `Palbase: debug → local (the default for debug)`; the APK is local.
- Root `palbase.env.debgu=main` → generatePalbaseDebug UP-TO-DATE, APK local, no warning.

NOT RUN: ~/.gradle/gradle.properties. I did not touch the real GRADLE_USER_HOME, and a throwaway one is not cheap offline. The claim holds by the same providers.gradleProperty mechanism, but it is unverified here.

Why minor: every outcome is either a loud refusal or the value someone explicitly set. Silent drift only hits debug, or the name/legacy fallbacks.

**Düzeltme.**

plugin:
- Read step 4 from the committed root gradle.properties FILE via providers.fileContents (a configuration-cache input).
- A providers.gradleProperty value that differs from the file and is not in startParameter is an OVERRIDE. Rank it with the command line and label it "Gradle property (env var / -D / ~/.gradle/gradle.properties)". Never raise the committed-conflict refusal for it.
- Read <module>/gradle.properties via fileContents, and honour or refuse palbase.env.* there.
- Enumerate providers.gradlePropertiesPrefixedBy("palbase.env.") plus local.properties keys, and WARN on suffixes that name no variant or build type of the module.

### C4 — confirmed / major (prototipte: yes, repo: plugin)

**İddia.** local.properties palbase.env.release=local overrides the committed release choice, and assembleRelease succeeds with a loopback/cleartext release APK.

**Kanıt.**

CODE:
- EnvironmentResolver.kt:55-57: local.properties (step 2) is read before the DSL and gradle.properties.
- GeneratePalbaseTask.kt:255-264 accepts an http base_url as loopback.
- GeneratePalbaseTask.kt:388-415 writes the cleartext network-security-config and only `logger.warn`s about 127.0.0.1 being the device.
- No reference to debuggable exists in the proto main sources (grep empty).

REPRO fx/c4 (proto 80fdab5, AGP 8.10.1): local/ has base_url http://127.0.0.1:54321; gradle.properties `palbase.env.release=prod`; local.properties `palbase.env.release=local`. assembleRelease output:
- `Palbase: release → local (palbase.env.release in local.properties)`
- WARN "base_url points at 127.0.0.1, which on a device or emulator means THAT DEVICE…"
- BUILD SUCCESSFUL

The resulting app-release-unsigned.apk:
- asset base_url = http://127.0.0.1:54321
- the manifest has networkSecurityConfig=@xml/palbase_network_security_config
- `aapt2 dump xmltree` of it: `cleartextTrafficPermitted=true` for '127.0.0.1' and '10.0.2.2'
- no android:debuggable

The fix is feasible: `javap` shows `public abstract boolean getDebuggable()` on com.android.build.api.variant.Component in gradle-api 8.10.1 AND 9.1.1.

Rated major, not blocker: it needs an explicit, then forgotten, personal-file edit, and the lifecycle line names local.properties. The same path would silently ship any other environment, such as staging, in a release.

**Düzeltme.**

plugin: pass `variant.debuggable` to GeneratePalbaseTask as an @Input. For non-debuggable variants:
- REFUSE an http/loopback base_url, and never write the cleartext network-security-config.
- Do not let local.properties select the environment. Either ignore those keys, or require the same value on the command line, or at minimum emit a WARN naming local.properties in a release build.

The CLI side (name loopback stacks `local`, never `main`) belongs to another area.

### C5 — partly / minor (prototipte: yes, repo: plugin)

**İddia.** The resolved env/origin exists only as a log line, which vanishes on UP-TO-DATE / build-cache / configuration-cache reuse; nothing in the APK records which environment was compiled.

**Kanıt.**

CODE: GeneratePalbaseTask.kt:163 `logger.lifecycle("Palbase: ${variantName.get()} → $selected ($origin)…")` runs inside the @TaskAction of a @CacheableTask (:32). :209-211 writes `authTarget.config.toString()`, i.e. only the android-config.json fields. CLI app_environments.go:41-56 shows those fields are app_id/base_url/api_key/sealed_root/oauth/notifications/integrity, with no environment name.

REPRO fx/c2 (proto 80fdab5):
- `clean assembleDebug --build-cache` twice. Run 2 prints `> Task :app:generatePalbaseDebug FROM-CACHE` and no Palbase line.
- Plain rerun: UP-TO-DATE, no line (c2 run3, and c3b typo run).
- REFUTED part: `clean assembleDebug --configuration-cache` run 2 shows "Reusing configuration cache.", then the task executes and prints `Palbase: debug → staging (…)`. Configuration-cache reuse by itself does not hide the line; only a non-executing task does.
- APK asset keys: ['api_key', 'app_id', 'base_url']. The environment NAME and origin are absent, but base_url identifies the stack (https://staging.envprobe…), so which stack is not entirely unrecorded.

**Düzeltme.**

plugin: add the environment name (and optionally the origin) to the task outputs. For example, a `palbase_environment` field in palbase/palbase-config.json. This is runtime-safe: palbe-core Codec.kt:23-24 `palbaseJson = Json { ignoreUnknownKeys = true … }`, and strictOAuthJson (shared/…/OAuthConfig.kt:106) rejects only duplicate keys. Alternatively, a generated `PalbaseGenerated.ENVIRONMENT` constant.

Keep the log line as a convenience.

### C6 — confirmed / minor (prototipte: yes, repo: plugin)

**İddia.** Kotlin DSL without `import io.palbase.gradle.palbase`: an inner `palbase { }` in a build type binds to the PROJECT extension. The proposed @Deprecated(level=ERROR) `environment` trap gives a clear compile error without breaking the import form or Groovy.

**Kanıt.**

BINDING, proto 80fdab5 (fx/c6a):
- `debug { palbase { packageName.set("set.inside.debug") } }` with no import compiles. BOTH java/generatePalbaseDebug/set/inside/debug/PalbaseGenerated.kt and java/generatePalbaseRelease/set/inside/debug/PalbaseGenerated.kt exist, so the setting applies to every variant.
- `debug { palbase { environment = "prod" } }` fails with the misleading "Function invocation 'environment(...)' expected" plus the exec-task candidates list.
- PalbaseExtension.kt:7-22 at 80fdab5 has no `environment`.

TRAP, prototyped in my copy (trap.patch): `@Deprecated(ENVIRONMENT_TRAP, level = ERROR) var environment: String?`, with getter AND setter throwing GradleException(ENVIRONMENT_TRAP). The setter matters because Groovy ignores Kotlin deprecation. Results:
- KTS, no import, debug{} and create("featureX"){}, Gradle 8.13/AGP 8.10.1: "Using 'environment: String?' is an error. Palbase: the environment is chosen PER BUILD TYPE … add `import io.palbase.gradle.palbase` …" (2 errors).
- Same on the user's app copy with Gradle 9.3.1/AGP 9.1.1: "'var environment: String?' is deprecated. Palbase: …", BUILD FAILED. The K2 wording differs, so assert on the custom text.
- KTS WITH import: `debug → main (palbase { environment } in the `debug` build type)` and `featureX → prod`; assets main/prod; the project-level `palbase { packageName.set("trap.pkg") }` still applies.
- `extensions.configure<io.palbase.gradle.PalbaseBuildType>` with no import: debug → staging.
- KTS project-level `palbase { environment = "main" }`: compile error with the guidance.
- Groovy: `debug{palbase{environment='main'}}`, `create('featureX')`, bare `featureY {}`, and `release` all resolve through the DSL (main/prod/staging/prod); `palbase { packageName = 'groovy.pkg' }` still works.
- Groovy project-level `palbase { environment = 'main' }`: "A problem occurred evaluating project ':app'. > Palbase: the environment is chosen PER BUILD TYPE…".
- The user app's `palbase { environmentsDir.set(…) }` is unaffected.
- Unit tests with the trap: 79/79 codegen-gradle (the existing no-import test's assertion changed to the import text, plus 1 new Groovy test) and 46/46 engine.

RESIDUAL: the trap does not cover packageName set inside a build type with no import, which still applies globally.

**Düzeltme.**

plugin: add the trap to PalbaseExtension exactly as in trap.patch: ERROR-level @Deprecated with a const message, and a getter/setter that throw GradleException with the same message. A getter returning null with a setter that throws error("unreachable") would leave Groovy users with "unreachable".

Update `the Kotlin build type dsl without the import does not compile` to assert the guidance text.

docs: note that project-level settings such as packageName written inside a build type without the import still apply to all variants.

### C7 — partly / minor (prototipte: yes, repo: docs)

**İddia.** The release refusal-by-default makes `./gradlew test`, `check` and `build` fail for every consumer that has not mapped release (testReleaseUnitTest depends on generatePalbaseRelease). assembleDebug and IDE-style configuration still work.

**Kanıt.**

CODE: AndroidVariantIntegration.kt:80-83 defers the refusal: `is EnvironmentResolution.Refused -> generate.environmentRefusal.set(resolution.reason)`. It is thrown at GeneratePalbaseTask.kt:138-139.

AGP 8.10.1 (fx/c7, proto 80fdab5, no palbase.env at all):
- `help` and `assembleDebug`: BUILD SUCCESSFUL.
- `-m` graphs: test, check, build and assemble list :app:generatePalbaseRelease; lint, testDebugUnitTest and connectedCheck list only Debug.
- Real `test`, `check` and `build` each FAIL at `:app:generatePalbaseRelease` with "`release` has no environment … Commit the choice, in ONE of two ways: `palbase.env.release=<environment>` in gradle.properties, or …".

AGP 9.1.1 (c1/app24n, the user's app with palbase.env removed):
- `test -m` and `check -m` list ONLY generatePalbaseDebug (AGP 9 unit-tests only the tested build type by default).
- `build -m` lists Debug, FeatureX, FeatureY and Release.

So on AGP 9, test and check are not broken by the release refusal; build and assemble are. That makes the claim AGP-version-dependent.

The breakage is intended and documented: proto CHANGELOG.md:26-31 names assembleRelease, assemble, build and testReleaseUnitTest. 2.3 consumers that set palbase.env are unaffected (release → legacy). Rated minor because the failure is loud and gives the exact line to add.

**Düzeltme.**

docs + palbase-cli:
- Keep the refusal inside the task.
- Add a migration note saying that on AGP 8.x `./gradlew test`/`check` also run generatePalbaseRelease.
- Have `palbase link` in an Android checkout print (or offer to write) `palbase.env.release=<env>` for gradle.properties.

No plugin change is required.

### YENİ — 2.3 consumer with a custom build type whose same-named environment directory exists silently switches environment on upgrade (major)

**Senaryo.** A 2.3 checkout has palbase.env=main and a `staging` build type. `palbase link` wrote every environment, so palbase/environments/staging/ exists. Under 2.3.0 the staging APK compiled main. After upgrading to 2.4, the same build compiles staging, with the staging tenant and key, without failing. The only trace is one lifecycle line, which is itself hidden on UP-TO-DATE/FROM-CACHE (C5). Review-flow only reported the LOUD case, where the directory is missing.

**Kanıt.**

fx/c1s (proto 80fdab5, AGP 8.10.1): gradle.properties palbase.env=main, create("staging"){initWith(debug)}, environments local/main/prod/staging. Output: `Palbase: staging → staging (from the build type name)`, `Palbase: release → main (palbase.env, the 2.3 global property)`. APK staging base_url = https://staging.envprobe.palbase.studio; APK release = https://main.envprobe.palbase.studio. Code: EnvironmentResolver.kt:84-89 (name rule before legacy). The 2.3.0 per-variant result is main for every variant (user app on 2.3.0: all 4 APKs = main).

**Düzeltme.**

The same plugin reorder as C1 (legacy palbase.env before the build-type-name rule). With the order-fix.patch copy, the equivalent user-app run gives `featureX → main (palbase.env, the 2.3 global property)`. Optionally emit a one-time hint when legacy wins for a build type whose same-named directory exists.

### YENİ — Proto README describes the order as 'command line first', which misleads about -Ppalbase.env (minor)

**Senaryo.** A reader of the 2.4 README sees "The full order, first hit wins: command line, local.properties, …, and last the 2.3 global palbase.env". They pass -Ppalbase.env=staging on the command line and expect it to win. It does not: it ranks last, below committed per-build-type keys and the build-type name.

**Kanıt.**

proto README.md:146-148 (80fdab5). The actual behaviour is in C2 (fx/c2: release → prod despite -Ppalbase.env=staging).

**Düzeltme.**

docs: with the C2 fix, state that a command-line -Ppalbase.env applies to every variant of that build. Without the fix, say explicitly that 'command line' means only -Ppalbase.env.<variant|buildType>.

#### Notlar

## plugin-order: verification notes

I tested against a clean clone of prototype **80fdab5**. Every claim is reproduced except the `~/.gradle/gradle.properties` sub-part of C3, which I did not run. Go was not needed for this area, and nothing touched the Palbase cloud.

### Baseline, before any change
Command: `../gradlew test --offline` in `verify/plugin-order/proto/codegen-gradle` (wrapper Gradle 8.11.1, Android Studio JBR). Result: BUILD SUCCESSFUL in 2m31s.

| Suite | Tests | Failures |
|---|---|---|
| codegen-gradle, total | 78 | 0 |
| EnvironmentResolverTest | 23 | 0 |
| LibraryFallbackCheckTest | 8 | 0 |
| PalbaseCodegenPluginTest | 47 | 0 |
| codegen-engine | 46 | 0 |

### Verdicts
| Claim | Verdict | Severity now |
|---|---|---|
| C1 | confirmed | major |
| C2 | confirmed | major |
| C3 | confirmed (`~/.gradle` part not run) | minor |
| C4 | confirmed | major |
| C5 | partly: configuration-cache reuse alone does not hide the line; base_url is in the APK | minor |
| C6 | confirmed, and the trap works | minor |
| C7 | partly: on AGP 9.1.1, `test`/`check` only run debug | minor |

None of my claims is a blocker. Every wrong-environment outcome here either needs explicit, conflicting configuration or is printed once on the lifecycle line. The closest to a blocker is the new finding: a 2.3 checkout with a custom build type silently switches environment on upgrade when a same-named directory exists.

### Fixes I prototyped (in my copies only)
- **`order-fix.patch`** (C1 + C2):
  - The legacy `palbase.env` moves ahead of the build-type-name rule.
  - A command-line `-Ppalbase.env` becomes step 1b.
  - Tests: 80/80 codegen-gradle and 46/46 engine.
  - The user's app then builds debug, release, featureX and featureY all on main, the same as 2.3.0.
  - `-Ppalbase.env=staging` now wins over a committed `palbase.env.release=prod`.
- **`trap.patch`** (C6):
  - A deprecated `environment` on the project extension (level ERROR) whose getter and setter both throw.
  - Tests: 79/79 codegen-gradle, including a new Groovy test, and 46/46 engine.
  - Checked on AGP 8.10.1 with Gradle 8.13, and on AGP 9.1.1 with Gradle 9.3.1.

### Where things are
Everything is under `/private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/266a545b-bd07-454f-b730-c8c05afdf07d/scratchpad/verify/plugin-order/`:
- **Logs and patches:** `baseline-test.log`, `trap-test.log`, `fix-test.log`, `order-fix.patch`, `trap.patch`, `count.py`
- **Prototype copies:** `proto` (tests), `proto-inc` (consumed via includeBuild), `proto-trap` / `proto-trap-inc`, `proto-fix` / `proto-fix-inc`
- **Copies of the user's app** (under `c1/`):
  - `app0`: original, with bare `featureX {`
  - `app23`: 2.3.0
  - `app24`: prototype
  - `app24f`: order-fix
  - `app24t`: trap
  - `app24n`: no `palbase.env`
  - Build logs sit beside them: `app23-assemble.log`, `app24-assemble.log`, `app24f-assemble.log`
- **AGP 8.10.1 fixtures** (under `fx/`): `c1s`, `c2`, `c2f`, `c3a`, `c3b`, `c4`, `c6a`, `c6t`, `c6g`, `c7`, plus the `mkp.sh`, `mkf.sh`, `mkt.sh`, `asset.sh` and `env.sh` scripts

### Caveats
- All consumer builds ran `--offline`. On the user's app, the release `lintVital*` tasks were excluded with `-x` because the offline cache lacks the material3-desktop artifacts. This is unrelated to Palbase.
- The reviewers' `proto-snap` differs from 80fdab5 (it has no `LibraryFallbackCheck`, and the resolver differs). I therefore re-ran every probe against 80fdab5 rather than reusing their outputs.

### Hygiene
- Real repos are unmodified; `git status` is clean at palbase-cli 20e5d7e, palbackend-android-src e72f704, palbackend-android d840817, palbe-trial-android acedb85 and palbase-cloud 6bbfcdf54.
- The evidence prototype at 80fdab5 is clean.
- No file in the user's test app is newer than the start of my work.
- Daemons: I started sandbox Gradle daemons (8.11.1, 8.13 with a 15-minute idle timeout, and 9.3.1). I stopped nothing and killed nothing.

---

## verify:cli-names

### A1 — confirmed / blocker (prototipte: n/a, repo: palbase-cli + palbase-cloud)

**İddia.** Environment names from the server reach the filesystem unvalidated (layout.go EnvDir = path.Join("palbase","environments",env)). A hostile name makes `palbase link` write outside the checkout, or into the real checkout through the link stage's symlinks.

**Kanıt.**

Read at CLI HEAD 20e5d7e. Go is not installed. I checked Go's path.Clean/Join semantics with a Python port that passes the standard Clean and Join test vectors from Go's path_test.go; I transcribed those vectors, I did not fetch them.

**Where the name comes from.** The server's JSON name is copied verbatim: `envs = append(envs, Environment{Ref: e.Ref, Name: e.Name, Status: e.Status})` (project_link.go:275; the same at cmd/palbase/main.go:450). No validator exists anywhere in internal/ or cmd/.

**Path helpers.** layout.go:60 `func EnvDir(env string) string { return path.Join(rootDir, envSubdir, env) }`. SpecPath (:63), ConfigPath (:79-81), PlistPath (:89) and GeneratedPath (:97-106) all build on it.

**Writers that take the name:**
- writeEnvironmentConfigs, app_environments.go:103-113 (`dest := ConfigPath(env, platform)` / `os.MkdirAll(filepath.Dir(dest)…)` / `os.WriteFile(dest…)`). It writes android-, ios-, macos- and web-config.json for every listed environment, not only the default (project_link.go:1004).
- writeSpec, app_environments.go:716-722, called from project_link.go:1026. Also from `palbase spec`/push, stack_spec.go:103-107, which writes in the real checkout, not a stage.
- Apple: `out := filepath.Join(root, filepath.FromSlash(GeneratedPath(env, "ios")))` and the plist (app_environments.go:770-797). The error path `discardStaleGenerated` deletes those two file names at the same paths (:750-755, native_codegen.go:58).
- Web: only the default environment, via `PALBASE_ENV=` passed to palbe-gen and EnvDir (web_wiring.go:160, 184-190).

**Stage.**
- The stage is `os.MkdirTemp("", "palbase-link-*")` (link_artifacts.go:137).
- Mutable dirs are `{palbase, src, app, pages, public}` (:195). They are copied into the stage.
- Every other top-level dir is `os.Symlink(filepath.Join(root, name), …)` (:227-231).
- The run executes after `os.Chdir(stage)` (:245).
- collectArtifacts skips symlinks (:440-442), so writes made through a symlink are never staged or rolled back.

**Lexical results (my Python port):**
- `../../gradle` → `gradle/android-config.json`
- `../../app/src/main/assets` → `app/src/main/assets/android-config.json`
- `..` → `palbase/openapi.json`
- `feature/login` → `palbase/environments/feature/login/…`
- `../../../../../../../../../tmp/pwned` is 36 characters (the review says 37) → `../../../../../../../tmp/pwned/android-config.json`. With cwd at the real stage `/private/var/folders/bz/…/T/palbase-link-N`, that resolves to `/tmp/pwned/android-config.json`; the Apple `filepath.Join(stage,…)` gives `/tmp/pwned/PalbaseGenerated.swift`.
- `'../'*19+'tmp/p'` (62 characters) reaches `/` from any stage depth up to 17.

**Filesystem probe (a1_traversal.py)**, with a sandbox stage built exactly like runLink:
- `../../gradle` wrote `fs/checkout/gradle/android-config.json` and `openapi.json` directly into the REAL checkout.
- `../../app/src/main/assets` and `..` landed in the stage copy. publishArtifacts would publish `['app/src/main/assets/android-config.json','app/src/main/assets/openapi.json','palbase/android-config.json','palbase/openapi.json']`.
- `../../../../outside-pwned` wrote to `fs/outside-pwned/` (outside both).

**Server allows it.** On palbase-cloud origin/main f92bc1e:
- cloud-lifecycle.ts:59 `name: z.string().min(1).max(64),`. Creating an environment needs only org role `member` (:174).
- Rename: panel.ts:243 `PanelRenameBody = z.object({ name: z.string().min(1).max(64) })`, owner only (panel.controller.ts:607-609).
- cli.controller.ts:487-488 returns the trimmed name verbatim.
- The victim's link describes the hostile environment because the cloud brokers the key for any project the user can see (credentials.go:221-230).

**Not run:** the real binary against the real cloud (not allowed).

**Düzeltme.**

palbase-cli: add one gate in layout.go, `func ValidEnvName(string) error`, with the same grammar the server will enforce. At minimum: non-empty, one segment (`path.Clean(n)==n`, no `/` or `\\`, not `.` or `..`, no leading `.`, no NUL or control characters). Apply it where names enter:
- the listing parse (project_link.go:272-277, main.go:448-451)
- `Resolved.Env` before `writeSpec` (stack_spec.go:103)

An invalid name is reported and skipped, never written and never fatal. The plugin-side check already exists in proto 80fdab5 (EnvironmentResolver.kt:109-122). I re-ran it: `-Ppalbase.env.release=feature/login` gives "contains a path separator".

palbase-cloud: validate a slug at create (CreateEnvironmentBody) and at rename (PanelRenameBody), e.g. `^[a-z][a-z0-9-]{0,38}$`.

### A2 — confirmed / blocker (prototipte: n/a, repo: palbase-cloud + palbase-cli)

**İddia.** The server accepts any 1–64-character environment name, with no uniqueness and no slug. The first environment is listed as `main` only when its name equals the product name. So (a) `palbase env create main` yields two `main`: gatherEnvironments keeps the last, while push/resolveNamed takes the first. (b) A product rename flips the directory from `main/` to `<product>/`.

**Kanıt.**

**Server** (origin/main f92bc1e):
- CreateEnvironmentBody `name: z.string().min(1).max(64)` (cloud-lifecycle.ts:59), untrimmed.
- The panel create page trims (project-settings-models.ts:3 `projectNameSchema = z.string().trim().min(1)…max(64…)`).
- The rename is untrimmed (panel.ts:243).
- No uniqueness: `cloud_projects` unique constraints are only `slot` and `audit_generation` (db/public.ts:548-549), and `name: text().nullable()` (:298).
- The studio's `environmentSlugSchema` (studio services/environment.ts:241-246) is not on the CLI or panel create path. That path is lifecycle.ts:26 → `/v1/cloud/projects/{id}/environments`.
- The rule, cli.controller.ts:487-490: `if (trimmed !== "" && !(index === 0 && productName !== undefined && trimmed === productName.trim())) { return trimmed; } return index === 0 ? "main" : ref;`. The order is `ORDER BY COALESCE(pr.created_at…) DESC, p.created_at ASC` (cli.service.ts:45).
- A CLI-created project writes the product and first-environment names as the same value: `name: ctx.name` in both inserts (cloud-lifecycle.ts:261, 268).

**(a) CLI consumers:**
- gatherEnvironments `envs.Environments[name] = d.entry` (app_environments.go:642), iterated in listing order, so the last one wins. Specs are keyed the same way (:649).
- resolveNamed returns the first `strings.EqualFold(e.Name, named)` (environments.go:328-334). `env use` does the same (env.go:155-168).
- linkEnvironmentRef uses `defaultEnvironment()` and then the first `e.Name == chosen` (project_link.go:539-543).
- My emulation (a2_names.py) of listing `[aaaa1111 'todoapp', bbbb2222 'main']` for product `todoapp`: both are named `main`. link reads from aaaa1111, `palbase/environments/main/` is written from bbbb2222, and push/plan/spec `--env main` act on aaaa1111. That is a silent divergence: a `release→main` APK talks to B while pushes deploy to A.
- This is reachable through the panel. The panel lists the RAW name (`name: (r.name as string) ?? ref, slug: ref`, panel.controller.ts:282-283), so a CLI-created project's first environment appears there as `todoapp`, and creating `main` there looks harmless.

**(b) Product rename:** `UPDATE cloud_products SET name = $2 …` only (panel.controller.ts:367-369). The emulation gives `main` before and `todoapp` after, for the unchanged row. It flips to the OLD product name stored on the environment row, not the new one.
- Apple: the next link sweeps `main/`.
- Android: nothing is swept, so `main/` stays with frozen files of the same tenant, and new contracts go to `todoapp/`.

**Düzeltme.**

palbase-cloud:
- Store an immutable slug column on `cloud_projects`.
- Write `main` explicitly for the first environment at creation (cloud-lifecycle.ts:265-268), instead of inferring it by comparing with the product name.
- Enforce a unique index on (product_id, lower(slug)) and validate at create and rename.
- Return the stored slug from every environmentSlug caller and from the panel.

palbase-cli: after listing, refuse a link when two environments map to one directory (compare case-folded), naming both refs.

### A3 — confirmed / major (prototipte: yes, repo: palbase-cloud + palbase-cli)

**İddia.** Case twins (Staging + staging) collapse into one directory on APFS. removeStaleEnvironmentDirs compares names exactly and could delete a directory just written for an environment renamed only by case.

**Kanıt.**

Measured on APFS (a3_case.py; `volume case-insensitive: True`). The emulation follows writeEnvironmentConfigs (app_environments.go:98-120, Go byte-order sort), writeSpec (:716-722) and removeStaleEnvironmentDirs (:162-223, exact `wanted[e.Name()]` at :178).

**(i) Twins in one listing.** It wrote `palbase/environments/Staging/android-config.json` and then `…/staging/android-config.json`. Result on disk: `{'Staging': ['android-config.json']}` containing `https://bbbb.example`, i.e. staging's config.

**(ii) Apple checkout, environment renamed Staging→staging.** Output:
- "wrote palbase/environments/staging/ios-config.json"
- "removed …/Staging (the project no longer has that environment)"
- the generator loop finds `staging/openapi.json` missing and skips it

Publish removes all four files of `Staging/` from the real checkout, so the link deletes what it just wrote. The next link recreates `staging/`, so it self-heals after two links.

**(iii) Android-only checkout.** There is no sweep (project_link.go:1072 `if apple`), so the directory stays `Staging/` holding staging's files. Every later link writes into it again, because APFS resolves the name case-insensitively.

**Plugin, proto 80fdab5.** Run on my copy with AGP 8.11.1 and Gradle 8.13, using `palbase/environments/Staging` that holds staging's config:
- `-Ppalbase.env.release=staging` → EXIT=1 "environment `staging` … has no directory … this checkout carries Staging, main". It fails closed because of the exact match `root.resolve(selected).takeIf { selected in known }` (GeneratePalbaseTask.kt:145).
- `-Ppalbase.env.release=Staging` → EXIT=0 "Palbase: release → Staging". The generated asset has `"base_url":"https://lowercase-staging.example.com"`. So a variant mapped to the capitalised twin silently compiles the other environment. The plugin cannot detect this; the fix belongs upstream.

**Düzeltme.**

palbase-cloud: make slugs unique case-insensitively, or lowercase-only, which removes twins entirely.

palbase-cli:
- Before writing, refuse when two names fold to the same directory.
- Make the sweep compare with the on-disk entry case-folded (`strings.EqualFold`) and never remove a directory written in the same run.
- When the on-disk case differs from the wanted name, rename the directory: a two-step rename via a temporary name works on APFS.

### A4 — confirmed / minor (prototipte: n/a, repo: palbase-cloud + palbase-cli)

**İddia.** A cloud environment named `local` is silently overwritten by this machine's stack entry in gatherEnvironments.

**Kanıt.**

By reading (CLI 20e5d7e).
- Cloud environment written: app_environments.go:642 `envs.Environments[name] = d.entry`, and :649 `specs[name] = d.spec`.
- Then `localURL := LookupLocalStack(groupOf(primary))` (:656). When a stack is registered, the entry is replaced in three places:
  - :664 keyless, on missing credential
  - :672 keyless, when the stack does not answer
  - :684 `envs.Environments[localEnvName] = localEnv`
- Nothing is printed about the cloud environment.
- Worse than the review says: on the two keyless paths the function returns before :685, and when the local spec fetch fails :685-687 does not replace `specs["local"]`. `local/` can then hold the machine stack's config beside the CLOUD environment's openapi.json.
- `palbase spec --env local` resolves the cloud environment (environments.go:328-334) and writes its contract into `local/` (stack_spec.go:103-107).
- The overwrite needs a registered stack whose group equals the sanitised checkout-directory name: link's target has no Name or Project (project_link.go:837), so groupOf falls back to `filepath.Base(checkoutRoot)` (app_environments.go:706-707), while start registers under the project name (start.go:675-683).
- Without a registered stack, `local/` holds the cloud environment. The 2.4 `debug → local` default would then compile a cloud tenant.
- The sweep never removes `local/` (app_environments.go:193).

**Düzeltme.**

palbase-cloud: reserve `local` (plus `main` except for the first environment, `debug`, `release`, `lint`, `test*`) in the slug validation.

palbase-cli: if a listed cloud environment is named `local`, skip it with a message rather than letting either source win silently.

### A5 — confirmed / minor (prototipte: n/a, repo: palbase-cli)

**İddia.** `palbase env create "Feature X"` cannot be confirmed interactively (env.go uses fmt.Fscanln into one string).

**Kanıt.**

By reading, not run: Go is not installed, and the command needs a linked cloud project. env.go:209 `name := strings.TrimSpace(args[0])`, then :233-240:
```go
fmt.Fprint(out, "Type the name to confirm: ")
var typed string
if _, scanErr := fmt.Fscanln(cmd.InOrStdin(), &typed); scanErr != nil {
    return fmt.Errorf("aborted")
}
```
Per Go's fmt semantics (the Scanln family scans space-separated operands and then requires a newline), input `Feature X\n` stores `Feature` and returns the error "expected newline" at `X`. The command therefore prints "aborted".

`--yes` bypasses the prompt (:232). The existing tests only use single-word names (env_test.go:226, 250).

The success hint `palbase env use %s` (:268) is also printed unquoted: for this name it reads as two arguments, which `cobra.ExactArgs(1)` refuses.

**Düzeltme.**

palbase-cli: validate the name client-side against the server's slug grammar before prompting. Fixing the read (bufio.ReadString('\n') plus TrimSpace) is moot once slugs are enforced.

### A6 — partly / minor (prototipte: yes, repo: docs + palbase-cloud)

**İddia.** D-036: a non-production environment deploys from the git branch equal to its slug/name, so branch names like feature/login collide with one-segment directory names and AGP build-type names. Also check the assistant's statement "Branch açmak ortam açmıyor" (opening a branch does not create an environment).

**Kanıt.**

**The collision is real, but only as a design conflict.**
- decisions.md:662-667 (origin/main): `| Diğer her environment | **kendi \`slug\`'ı** |` ("every other environment: its own slug"), and "Kullanıcı `develop` adında bir environment açtığında `develop` branch'i oraya deploy eder" ("when the user opens an environment named `develop`, the `develop` branch deploys there").
- Today's slug is the display name (cli.controller.ts:226-227) or the ref (panel.controller.ts:283). So a `feature/login` branch needs an environment named `feature/login`, whose directory is `palbase/environments/feature/login/…` (my path.Join port).
- Proto 80fdab5 refuses it: EXIT=1 "environment `feature/login` … contains a path separator".
- AGP 8.11.1 refuses the build type `create("feature/login")`: "The Configuration name 'androidTestFeature/loginImplementation' must not contain any of the following characters: [/, …]". I re-ran this on my consumer copy.
- Nothing implements D-036. The run JSON says `phase: 'design'`, and cli.controller.ts:234-235 reads `// Dal eşlemesi yok … source_git_branch: null` ("no branch mapping").

**The statement is TRUE today.** I found no live code path in the CLI, the cloud server or the panel that creates or selects an environment from a branch:
- CLI: grep for rev-parse, symbolic-ref, abbrev-ref and GITHUB_REF finds nothing. Environments are created only by `env create` (env.go:256-258) and `project create` (project.go:143-144). No github arm remains in push.
- Server: db/public.ts has 0 hits for github, project_repositories or preview_environments. No Go file mentions CreatePreviewEnvironmentWorkflow, DeployFromGitHubWorkflow or go.temporal.io.
- Panel: unavailable.ts:57 `"environments.setSourceGitBranch": { … why: "code arrives by \`palbase push\`; a project is not tied to a branch here" }`.
- The studio webhook (studio/src/app/api/github/webhook/route.ts:10-16) is ported v1 code with no tables or worker behind it. Its own push rule says "An UNMAPPED branch NEVER creates a runtime"; decisions.md D-004 says the same.
- I did not probe the deployed studio (no cloud calls allowed).

**Caveat the statement needs.** The in-scope GitHub-connect run plans PR previews (D-003, D-007 "PR preview = aynı cloud_products altında yeni bir cloud_projects (tenant) satırı", i.e. a new tenant row under the same product; spec.md:119 FR-072). Once that ships, opening a PR with previews enabled WILL create an environment automatically. That environment will appear in every teammate's `palbase link`.

**Düzeltme.**

docs / palbase-cloud: before D-036 or GitHub connect ships, define one slug grammar that is DNS-, directory-, Xcode- and AGP-safe:
- `[a-z][a-z0-9-]*`
- not `test*`, `main`, `lint`, `local`, `debug` or `release`

Also add a deterministic branch→slug mapping (feature/login → feature-login), and give preview environments a reserved prefix that the CLI and the 2.4 plugin can recognise.

Amend the user-facing statement to: "a branch does not create an environment; a PR will, once previews are enabled."

### YENİ — The first environment's directory name depends on where the project was created (CLI → `main`, panel → `Production`), and the panel shows neither the CLI name nor a slug (major)

**Senaryo.** **Panel-created project.** A user creates a project in the panel, which pre-fills the first environment as "Production". The user then follows 2.4 guidance such as `palbase.env.release=main`. `palbase link` writes `palbase/environments/Production/`, so release fails with "carries Production".

**CLI-created project.** The panel shows the first environment under its raw name (the product name), while the CLI calls it `main`. A panel user who creates a "main" environment produces the duplicate described in A2(a).

**Kanıt.**

- studio projects/new/page.tsx:23 `React.useState("Production")` and :54 `createEnvironment.mutateAsync({ projectId: receipt.id, name: environmentInput.data })`, which becomes CreateEnvironmentBody through lifecycle.ts:26.
- cli.controller.ts:487-488 returns "Production" verbatim (it is not equal to the product name).
- panel.controller.ts:282-283 `name: (r.name as string) ?? ref, slug: ref`.
- Emulation (a2_names.py): panel-created gives `[{'name': 'Production'}]`; CLI-created gives `[{'name': 'main'}]`.

**Düzeltme.**

palbase-cloud: store an explicit slug at creation and show it in the panel beside the display name. For example, the first environment's slug is `main` regardless of its display name, and the panel create form shows the slug.

palbase-cli: link output should print the directory names it wrote and the exact `palbase.env.<buildType>=<dir>` lines, so no documentation hard-codes `main`.

### YENİ — Any org member can break every teammate's `palbase link` with an environment name alone (persistent DoS) (major)

**Senaryo.** **The name `..`.** A member creates an environment named `..` (legal on the server). The next teammate's link publishes `palbase/openapi.json` into the checkout. That file is a legacy-layout marker, so every later link is refused with "this checkout still carries the retired layout: palbase/openapi.json". Deleting the file only helps for one run, because the next successful link writes it again.

**Unwritable names.** Names that cannot be written abort the whole link, not just that environment, because config writes are fatal. Examples: `../../settings.gradle.kts` (MkdirAll hits an existing file, ENOTDIR) or a name containing NUL.

**Kanıt.**

- `EnvDir("..")` gives `palbase`, so SpecPath is `palbase/openapi.json`. In the FS probe (a1_traversal.py), publishArtifacts included 'palbase/openapi.json'.
- layout.go:164-166 LegacyMarkers contains "openapi.json", and :188-192 stats `root/palbase/<marker>`.
- link_artifacts.go:167-174 refuses the link before any write.
- project_link.go:1004-1007: `paths, err := writeEnvironmentConfigs(...); if err != nil { return err }`, so one bad environment fails the entire link.
- The unwritable-name cases (ENOTDIR, NUL) are by reading and Go semantics; I did not run them.

**Düzeltme.**

palbase-cli: the same name gate as A1, but skip-and-report rather than fail. Also make per-environment write failures non-fatal for non-default environments, matching how read failures are handled at app_environments.go:634-640.

palbase-cloud: slug validation.

### YENİ — Server surfaces disagree on what an environment is called; D-036 routes on a `slug` field that means three different things (minor)

**Senaryo.** D-036 (and any future consumer) reads `slug` and `is_production` to decide which branch deploys where. The same environment is:
- `main` in /api/v2/projects
- the product name in /api/v2/projects/{id}/environments and /apps/{id}/bindings
- the ref in the panel

Also, `is_production` is hard-coded `true` for every row, so under D-036 every environment would map to the default branch.

**Kanıt.**

- cli.controller.ts:100 and :128 pass productName. :226-227 (`name` and `slug`) and :291 (`environment_name`) call `environmentSlug(r.name, i, r.ref)` without it, so index 0 is not mapped to `main`.
- cli.controller.ts:229 and panel.controller.ts:285 have `is_production: true`.
- panel.controller.ts:283 has `slug: ref`.
- decisions.md:669 claims the schema carries slug and is_production.
- The CLI uses only /api/v2/projects today (grep), so this is latent.

**Düzeltme.**

palbase-cloud: one stored slug column and a real is_production flag, returned identically by every endpoint. Do this before D-036 is built.

### YENİ — Environment names are printed raw to teammates' terminals (control-sequence injection) (minor)

**Senaryo.** A member names an environment with ANSI/OSC escape bytes, which the server accepts because `z.string()` has no charset check. Every teammate's `palbase link`, `env list` or refusal message prints them raw. That can rewrite lines, set the terminal title, or hide text.

**Kanıt.**

By reading only. Names are formatted with %s:
- project_link.go:589 `"%s is %s — not asked; its files are left as they are\n", e.Name, e.Status`
- project_link.go:898
- listing/listingWithStatus (project_link.go:551-567)
- env.go:168

The server schemas are cloud-lifecycle.ts:59 and panel.ts:243. Not run.

**Düzeltme.**

palbase-cloud: slug validation (removes it).

palbase-cli: the A1 name gate, plus %q for any server-supplied name in human output.

#### Notlar

## cli-names: verification summary

**Scope and limits**
- Repos read: palbase-cli HEAD 20e5d7e and palbase-cloud `origin/main` f92bc1e, via `git show` only. The real repos were not modified.
- Go is not installed. Go behaviour was checked two ways:
  - by reading the code;
  - with a Python port of `path.Clean`/`path.Join`. The port is `gopath.py`. It passes the standard vectors from Go's `path_test.go`, which I transcribed rather than fetched.
- Filesystem effects were measured on real APFS in my own directory.
- Plugin behaviour was re-run on a copy of prototype 80fdab5 (clean `git status`), using a copied consumer with AGP 8.11.1 and Gradle 8.13.
- I made no cloud calls. The real `palbase` binary was not driven with hostile names.
- I started one sandbox Gradle daemon and stopped nothing.

**Verdicts**

| Claim | Verdict | Severity | Short reason |
|---|---|---|---|
| A1 | confirmed | blocker | Traversal outside the checkout and through stage symlinks is reproduced in the FS emulation. Any org member can create such a name. |
| A2 | confirmed | blocker | A duplicate `main` makes link write environment B while push goes to environment A. It is reachable from the panel, which shows the raw name. A product rename moves the directory to the *old* product name. |
| A3 | confirmed | major | Case twins collapse into one directory. Apple deletes a freshly written directory after a case-only rename. Android stays stuck on the old-case name. The proto plugin compiles the twin's content for the capitalised name, which I measured. |
| A4 | confirmed | minor | A cloud environment named `local` is silently overwritten. It can also leave the local config beside the cloud contract. |
| A5 | confirmed | minor | `Fscanln` cannot read a name with a space, so the interactive confirm always aborts. |
| A6 | partly | minor | The D-036 collision is real but exists only in the design. "Branch açmak ortam açmıyor" is true today, but planned PR previews (FR-072) will create environments automatically. |

**Corrections to the reviewer**
- The `/tmp/pwned` name is 36 characters, not 37.
- It lands at `/tmp` only because the stage depth on this Mac is 7. A 62-character name reaches `/` at any realistic depth.
- The directory flip after a product rename goes to the environment row's stored name (the old product name), not to the new product name.
- `PanelCreateEnvironmentBody` (panel.ts:405) is dead. The only reference is its own definition. The live panel create goes through the studio's trimmed `projectNameSchema` and then `CreateEnvironmentBody`.

**Smallest coherent fix set**
1. **Server (palbase-cloud)**
   - Add an immutable slug column. The first environment's slug is `main`, written explicitly at creation.
   - Validate the grammar at create and rename: `^[a-z][a-z0-9-]{0,38}$`.
   - Reserve `local`, `debug`, `release`, `lint`, `test*`, and `main` except for the first environment.
   - Add a unique index on `(product_id, lower(slug))`.
   - Return the slug identically from `/api/v2/projects`, `/projects/{id}/environments`, `/apps/{id}/bindings` and the panel.
2. **CLI (palbase-cli)**
   - A single `ValidEnvName` gate applied at the listing parse and before `writeSpec` in `spec`/`push`. It skips and reports; it never fails the link.
   - A duplicate / case-fold collision refusal.
   - A case-aware sweep.
   - A non-fatal write for non-default environments.
   - `%q` for names in human output.
3. **Plugin:** nothing new. 80fdab5 already enforces one segment and exact-case matching, both re-verified.

**Evidence files** (`/private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/266a545b-bd07-454f-b730-c8c05afdf07d/scratchpad/verify/cli-names/`)
- Scripts: `gopath.py`, `a1_traversal.py`, `a2_names.py`, `a3_case.py`
- Gradle logs: `g1.log` (staging refused), `g2.log` (Staging → twin content), `g3.log` (feature/login refused by proto), `g4.log` (AGP refuses `/` in a build-type name)
- Sandboxes: `fs/`, `case/`, `consumer/`, `proto/src` (80fdab5 copy)

**Outside my area (not measured)**
- `link_artifacts.go:195` puts `app` in the mutable set. That was intended for Next.js. In an Android checkout it means every `palbase link` copies the whole `app/` module, including `app/build`, into `$TMPDIR` and reads every file into memory twice (`copyArtifactTree`, then `collectArtifacts`).
- The user's test app has `app/build` at 101 MB.
- A concurrent Android Studio build touching `app/build` during a link could trip "changed during link; no generated files were published" (`publishArtifacts`, link_artifacts.go:480-483).
- Worth a separate look before the Android flow ships.
