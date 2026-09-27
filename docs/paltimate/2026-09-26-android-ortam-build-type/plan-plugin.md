# Plugin `io.palbase.codegen` 2.5.0 — build type ortamı seçer — Uygulama Planı

> **Ajan çalışanlar için:** Görev görev yürüt (superpowers:subagent-driven-development ya da superpowers:executing-plans). Adımlar `- [ ]` checkbox. Görev başlıkları makine-okur meta veri taşır (`deps | files | satisfies`). **T031 (yayın) ve T034 (birlikte deneme) KULLANICIYLA yürütülür; T033 kullanıcının onayı ve yedekle başlar.**

**Goal:** Android'de her variant'ın ortamını build type'ı (ve flavor'ı) seçer — `palbase {}` bloğu gerekmez, yanlış ortam hiçbir yolda sessizce paketlenmez; plugin, engine ve runtime 2.4.0 olarak birlikte çıkar; trial app ve kullanıcının test app'i yeni düzene geçer.

**Architecture:** Gradle tipi taşımayan saf bir çözücü (`EnvironmentResolver`) FR-201'in sırasını uygular; `AndroidVariantIntegration` her variant için kaynakları (komut satırı, `local.properties`, build type/flavor DSL'i, kök `gradle.properties` DOSYASI, eski `palbase.env`) toplar ve reddi göreve erteler (IDE sync ve `assembleDebug` bozulmaz); kök araması görev çalışırken yapılır; library fallback denetimi app variant'ının `pre<Variant>Build`'ini düşürür. Üretilen kod 2.3 ile byte byte aynı; runtime değişmez.

**Tech Stack:** Kotlin, Gradle plugin (AGP `DslExtension`, `androidComponents.onVariants`), Gradle TestKit (AGP 8.10.1 / Gradle 8.11.1), tüketici provaları AGP 8.11.1 ve 9.1.1, JBR 21.

**Spec:** `./spec.md` (FR-201…FR-213, FR-301, FR-302) · **Kararlar:** `./decisions.md` (D-001…D-007, D-009, D-010, D-017, D-018, D-019, D-027…D-029) · **Kanıt:** `./reports/prototype-and-review-2026-09-24.md`, `./reports/verification-2026-09-25.md`, prototip yamaları `./reports/proto-2.4/` · **Plan araçları:** `./tools/` (plugin reposuna girmez; bu klasörle birlikte palbase-cli'ye commit edilir)

**Sıra:** `plan-cli.md` Dalga 1 ve `plan-cloud.md`'den sonra; T031 yayınından sonra `plan-cli.md` Dalga 2.

## Global Constraints

- **Hedef depo:** `palbackend-android-src`, `main`. Taban: `origin/main` `3588bc2` (`ci: setup-android var olmayan 'tools' paketini istemiyor — …`) + üstüne taşınmış `e72f704` (`release: public distribution repo — palgroup/palbackend-android, …`; yalnız yerel depoda, origin'de yok) — D-030. Yerel `main` T001'den önce taşınır: `git fetch origin && git rebase origin/main`. `e72f704` çakışmasız uygulanır (ölçüldü: `Auto-merging CHANGELOG.md`, 4 dosya; upstream'in `## 2.4.0 — 2026-09-26` bölümü ve ``Son etiket: `v2.4.0` `` olduğu gibi kalır; scratch'te taşınmış commit `c164ce4`). Bir submodule değil: parent işaretçisi yok.
- **Upstream'in 2.4.0'ı (D-030):** `233991d` (beyan edilen 401/429/doğrulama kodu kendi tipli vakasına — `KotlinEmitter`'ın ürettiği `from()` artık `backend.envelope` üzerinden eşler; palbe-core `BackendError.envelope`, `palbe-core.api`), `6e97598` (`release: palbackend-android 2.4.0`, etiket `v2.4.0`), `3588bc2` (CI). CI artık `./gradlew :codegen-engine:test :codegen-gradle:test :palbe-core:testDebugUnitTest` koşar. Bu planın dosyalarıyla örtüşen tek dosya `CHANGELOG.md` (T029/T030). `codegen-gradle/`, `README.md`, `distribution/README.md`, `scripts/publish.sh`, `gradle.properties`, `sample/`, `consumer-release/` ve `palbe-core/src/test/kotlin/io/palbase/core/GeneratedConfigLoaderTest.kt` eski (`e72f704`) ve yeni tabanda byte byte aynı: görev metinlerindeki `@e72f704` satır atıfları yeni tabanda aynı satırları gösterir (`codegen-gradle` için `v2.4.0`'da da). T001–T028'in commit'leri yeni tabanda çakışmasız uygulanır; T029 ve T030 CHANGELOG'da çakışır, çözüm o görevlerin metnidir. 2.4.0 public dağıtım ağacında yayımlanmadı (`palgroup/palbackend-android` yalnız `v2.3.0`). Tüketici provalarının sözleşmesi (trial ve kullanıcının test app'i — aynı `openapi.json`) `x-palbase-errors` beyan etmiyor, yani 2.4.0'ın `envelope` yolunu üretmez; o yol T032 Adım 4'te bir kopyada ayrıca ölçüldü (palbe 2.5.0 ile derlenir, 2.3.0 ile `Unresolved reference 'envelope'`).
- **Yazıcı ve commit kuralları (NFR-004):** tek yazıcı, `main`'de; worktree ve yan branch yok. Commit'ler pathspec ile (`git add <yollar> && git commit -m …`, Adım 5'teki gibi). Her commit `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` ile biter. Görev sırası commit sırasıdır.
- **Araçlar:** `JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home"` (JetBrains JDK 21.0.9), `ANDROID_HOME=/Users/erkutbas/Library/Android/sdk`. Her Gradle komutu `--offline`. `gradle --stop` yok, Java/Gradle süreci öldürmek yok.
- **Sürümler:** depo wrapper'ı Gradle 8.11.1. TestKit fikstürleri AGP 8.10.1 ve KGP 2.2.21 ile bu Gradle'da koşar. Tüketici provaları: trial AGP 8.11.1 / Gradle 8.13, kullanıcının test app'i AGP 9.1.1 / Gradle 9.3.1. Desteklenen aralık AGP 8.10.1+, Gradle 8.11.1+, JDK 17+ (FR-212); plugin sınıf dosyası major 61.
- **Test komutu:** `cd codegen-gradle && ../gradlew :test --offline`. `:test` şart: iki nokta olmadan `--tests` filtresi `:codegen-engine:test`'e de gider ve `No tests found for given includes` ile düşer.
- **Build cache:** kök `gradle.properties` `org.gradle.caching=true` taşır. Bir `generatePalbase*` görevi `FROM-CACHE` gelirse `Palbase:` satırı basılmaz; satırı bekleyen kök komutlar görevi `--rerun` ile koşar (T003 Adım 4, T030 Adım 4.3). Önceki provalar aynı girdileri makinenin build cache'ine yazmış olabilir.
- **TestKit koşucusu:** T023'ten itibaren `--stacktrace --no-watch-fs` ile koşar (`Fixture.runner()`). Yeni tabandaki yeniden oynatmada T014'ün tam `:test`'i bir kez başka bir kırılganlıkla düştü: `flavor blocks outrank the build type keys in gradle properties` fikstürünün script'i `Cannot access implicit script receiver class 'org.gradle.api.Project'` ile derlenmedi (T011'in testi; T011–T013'te yeşildi); aynı ağaçta tek başına ve tam koşu yeşil. Kök nedeni kanıtlanmadı; bir tam koşu düşerse bir kez tekrarlanır ve ikisi de kaydedilir.
- **Taban (`3588bc2` + `e72f704`):** `PalbaseCodegenPluginTest` `tests="27"`; codegen-engine 9 sınıf, 47 test (upstream'in `ClassifiedErrorsEmitTest`'i dahil; eski tabanda 8 sınıf, 46); palbe-core `GeneratedConfigLoaderTest` `tests="13"` (`:palbe-core:testDebugUnitTest` 89 sınıf, 602 test). Hepsi `failures="0"`; tabanda kırık test yok.
- **Son durum (T030):**
  - `EnvironmentResolverTest` 52, `LibraryFallbackCheckTest` 11, `PalbaseCodegenPluginTest` 88, codegen-engine 47 (9 sınıf), palbe-core `GeneratedConfigLoaderTest` 14; hepsi `failures="0" errors="0"`.
  - `./gradlew -p codegen-gradle check` (`:validatePlugins` dahil) ve README kapısı yeşil (T030 Adım 4); CI'ın komutu `./gradlew :codegen-engine:test :codegen-gradle:test :palbe-core:testDebugUnitTest --offline` de yeşil.
- **Kapılar (NFR-002):** `./gradlew -p codegen-gradle check --configuration-cache` ve README kapısı `./gradlew check lintRelease :consumer-release:assembleRelease --configuration-cache`. Configuration cache her yeni davranışta yeniden kullanılan girişte de ölçülür (T002, T007, T009, T018, T023).
- **Dil (NFR-003):** kullanıcıya basılan her dize, kod ve kod yorumları İngilizce. Plan düzyazısı ve commit mesajları Türkçe. Tüketici deposunun (trial) commit mesajı kendi git log'u gibi İngilizce.
- **Yayın (D-005, D-030):** 11 artefakt birlikte **`2.5.0`**; sürüm `PALBE_VERSION`'dan gelir. `2.4.0` upstream'in yayımlanmış sürümüdür ve CHANGELOG'da tarih olarak kalır. Runtime ve engine kodu 2.4.0 ile aynı; tek runtime değişikliği palbe-core'daki bir test (T022).
- **Sürüm adları:** plan düzyazısında, kod yorumlarında ve dokümanlarda BU yayını adlandıran "2.4" → "2.5" (T002, T007, T022, T027–T034). Değişmeyenler: `reports/proto-2.4/` yolu ve raporlardan yapılan alıntılar (planlama dönemindeki adlar); plugin'in bastığı `palbase.env, the 2.3 global property` ve eski davranışı anlatan "2.3" cümleleri (2.4.0 ortam seçimini değiştirmedi, cümleler doğru kalır). Upgrade notları 2.4.0'ı da kapsar: `README.md` "Upgrading from 2.3 or 2.4" (T027); public README'nin "Upgrading from 2.3" listesi ve CHANGELOG'un YAYIN bölümü, public ağacın atladığı 2.4.0'ın değişikliğini birer maddeyle söyler (T028, T029).
- **Ajan koşturmaz:** yayın (T031), bulut çağrısı, `palbase login` / `project create` / `env create` (FATURALI) / `push` / `link` (T033, T034). Bunlar kullanıcınındır.
- **Ajan koşturabilir:** `publish…ToTestRepository` ve `scripts/verify-publications.sh`. Yalnız reponun `build/` dizinlerine yazarlar (T030).
- **Kullanıcının test app'i git'te değil:** T033 Adım 0 (yedek + kullanıcının açık onayı) atlanmaz. Artıklar silinmez, yedeğe taşınır.
- **Plan araçları:** `docs/paltimate/2026-09-26-android-ortam-build-type/tools/` palbase-cli'de (`06291bb`): `changelog-check.sh`, `release-notes.sh`, `release-dry-run-check.sh`, `post-publish-check.sh`, `verify-trial.sh`, `verify-myapp.sh`, `proof-init.gradle.kts`, `released-init.gradle.kts`. 2.5.0 revizyonu yalnız sürüm dizelerini değiştirir: `changelog-check.sh`, `release-notes.sh`, `proof-init.gradle.kts`, `verify-trial.sh`, `verify-myapp.sh` (içerikleri T029, T030, T032, T033'ün gösterdiği metin); diğer üçü aynı. Lead onları `plan-plugin.md` ile birlikte palbase-cli'ye pathspec'le commit eder. Plugin reposuna girmezler.
- **Sıra:** plan-cli → plan-cloud → bu plan (T001–T030) → T031 yayın (kullanıcı) → T032–T034 tüketiciler. plan-cli Dalga 2'nin bastığı Android koordinatları da 2.5.0 olmalı (`palbaseAndroidVersion`, D-026).
- **D-008 / D-014:** decisions.md ikisini de "kullanıcı onayladı" diye işaretliyor. Plugin ikisini de kullanmıyor (D-011'in gevşek kapısı + FR-204'ün tek parça kuralı); bu planda KOŞULLU görev yok.

## Review Focus

- **Library'yi bir ara modül üzerinden paketleyen app** (`:app` → `:core` → `:lib`; eklenti yalnız `:lib`'de, app'te `create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") }`) → `:app:preFeatureXBuild` ``Palbase: `:app` variant `featureX` packs `:lib`'s `debug` build — `:lib` declares no `featureX` build type…`` ile düşer; `:app:preDebugBuild` ve `:app:preReleaseBuild` yeşil. Test: **T023** `an app variant that packs the library through another module fails too`.
- **CI'ın ortam değişkeniyle verdiği override** (`ORG_GRADLE_PROJECT_palbase.env.release=staging`; release'in build type bloğu `prod` diyor) → `staging` derlenir, satır `Palbase: release → staging (palbase.env.release override from ORG_GRADLE_PROJECT_*, -Dorg.gradle.project.* or ~/.gradle/gradle.properties)`; "two committed places" reddi YOK. Test: **T009** `an ORG_GRADLE_PROJECT variable is an override and no committed conflict`.
- **Library'nin olup app'in olmadığı flavor boyutu** (app `missingDimensionStrategy("tier", "free")`, library `tier` boyutunda `free`/`paid`) → hiçbir app variant'ı tahminle reddedilmez (`:app:preDebugBuild`, `:app:preReleaseBuild` yeşil) ve ``Palbase: `:app` has no `tier` flavor dimension and `:lib` does, … are NOT checked…`` çıktıda TAM BİR KEZ. Test: **T024** `a library dimension the app lacks is said to be unchecked once and refuses nothing`.
- **Flavor'lı benchmark variant + kişisel flavor satırı** (`freeBenchmarkRelease`, `local.properties` `palbase.env.free=local`, dosyada `palbase.env.release=main`) → `main` seçilir, yok sayılan satır `freeBenchmarkRelease`'i adlandıran TEK bir uyarıdır (iki yürüyüş, bir cümle). Test: **T010** `a flavored benchmark variant warns once about an ignored flavor line`.
- **İki flavor boyutu aynı yerde çelişir** (`palbase.env.free=main`, `palbase.env.staging=staging` → `freeStagingDebug`) → ret, iki köken adlandırılır (``… is given different environments in one place — `main` (palbase.env.free in gradle.properties) … `staging` (palbase.env.staging in gradle.properties)``); `palbase.env.freeStaging=staging` eklenince `staging` (`palbase.env.freeStaging in gradle.properties`). Test: **T010** `two flavors that disagree in one place are refused and the combination settles them`.

## Fidelity Audit

- **Şartnamede dayanağı olmayan öğe:** üç tane var, üçü de test altyapısı ya da güvenlik:
  - `--no-watch-fs` (T023, `Fixture.runner()`). Planlama koşusunda ölçülen bir kırılganlığa karşı alınan önlem; kök nedeni kanıtlanmadı.
  - T033 Adım 0: kullanıcının onayı ve yedek. Proje git'te değil.
  - `tools/` betikleri (T029–T033). Plan araçlarıdır, ürün kodu değildir. `verify-trial.sh` ve `verify-myapp.sh` build'den önce `app/build/outputs/apk`'ı siler: bayat bir APK kabulü yalancı `ok`'la geçirmesin (planlama koşusunda oldu).

  Geri kalan her öğe bir FR'ye bağlı. İki kesişimin dayanakları:
  - T025 → FR-203 + FR-208.
  - T011'in flavor bloğu ↔ flavor anahtarı reddi → FR-202 + D-004 (aşağıda A-2).
- **Bileşen yüzeyinden farklı imza (taslağa göre):**
  - Görev numaraları (yeni ← taslak): T016 ← T016+T017 (birleşti), T017 ← T018, T018 ← T019, … T034 ← T035. Taslak commit'lerinin sonekleri de yeniden numaralandı.
  - plan-cli taslağındaki `draft-cli-4-android-output.tasks.md` `local` cümlesi için "plugin T017"ye atıf yapıyor; artık **T016**.
  - `EnvironmentResolver.Choice.warnings`: `MutableList` → `LinkedHashSet` (iç). `resolve()` onu `toList()` ile verdiği için `EnvironmentResolution.warnings: List<String>` aynı kaldı.
  - Yeni iç yardımcı `Choice.committed(key): Answer.Hit?` (T010).
  - Blok ↔ dosya karşılaştırması taslakta bütün yerin cevabına (`ask`) karşıydı. Artık bloğun kendi kapsamındaki anahtarlara karşı:
    - build type bloğu için `[variant, buildType]`;
    - flavor bloğu için `[variant, combination, flavor]`.
  - Kullanıcıya basılan üç cümle "root" der:
    - T015 ``Set the key in the root gradle.properties for everyone, or in the root local.properties for this machine alone.``
    - T021 ``… `palbase.env.<variant>=<environment>` in the root gradle.properties — or build …``
    - T023 ``… say so: `palbase.env.<V>=<env>` in the root gradle.properties.``
  - `Fixture.runner()` argümanlarına `--no-watch-fs` eklendi (T023).
  - Son sayılar taslağa göre: `EnvironmentResolverTest` 49 → 52, `PalbaseCodegenPluginTest` 84 → 88, `LibraryFallbackCheckTest` 11 (aynı).
- **Planlama sırasında yapılan şartname değişiklikleri** (lead `decisions.md`'ye yazar; gerçek depoya yazmak bu ajana yasak):
  - **A-1 · FR-210, `local` için çare cümlesi.** Şartnamenin metni her ortam için `palbase push --env <env>`, then `palbase link` here diyor. `local` için T016 ``run `palbase start` in the backend (its stack serves the contract of the code it runs; `palbase push` does not publish to it), then `palbase link` here`` basar. Gerekçe: CLI yerel yığına push'u reddediyor (palbase-cli `internal/backend/stack_push.go:162`) ve `--env local` projenin bir ortamı değil (`environments.go:337`). FR-009'un öneri cümlesiyle aynı yol. Kayıt: D-027.
  - **A-2 · FR-202, blok ↔ dosya reddi yalnız AYNI build'ler için.** Taslak başka seviyedeki anahtarları da reddediyordu:
    - flavor bloğu ↔ `palbase.env.release`;
    - build type bloğu ↔ `palbase.env.<flavor>`.

    Bu, FR-201'in "4, 5'ten önce" sırasına ve FR-202'nin "aynı build type için" metnine aykırıydı. Bir de FR-013'ün her app'e bastırdığı satırlarla flavor bloklu her modülü reddediyordu: taslağın kodu yeni TestKit testini ``Palbase: the `staging` product flavor names two environments in two committed places — … `palbase.env.release=main` in gradle.properties`` ile düşürdü (ölçüldü). Plan şartnamenin metnini uygular:
    - Başka build'leri de kapsayan anahtarı blok geçer.
    - Ret, bloğun kendi kapsamındaki anahtar için kalır: variant, build type; flavor için flavor ya da onu içeren kombinasyon.

    Flavor bloğu ↔ flavor anahtarı reddi "aynı build type için" cümlesinin flavor'a taşınmış hâlidir (D-004). Karşı seçenek (taslağın geniş reddi) bir kullanıcı kararı ister. Kayıt: D-028.
  - **A-3 · FR-205, `.git` / `palbase/project.json` sınırı yalnız KÖK PROJEDE ölçülür** (T018). Üst dizinin bir işaret taşıması istenmez. Sonuç: işaretsiz bir kök proje üstünü arar. Kayıt: D-029.
  - **A-4 · Şartnamenin "Hedef son durum"u kendi içinde çelişik.** Ağaçtaki kişisel `palbase.env.debug=featureX` satırı debuggable `debug`'ı `featureX`'e çevirir (adım 3, 5'ten önce); tablo ise `debug → main (dosya)` diyor. T033 ikisini ayrı ölçer. Önerilen şartname notu: "debug → main (dosya); kişisel local.properties satırı onu featureX'e çevirir".
  - **A-5 · D-008 / D-014.** Görev metni ikisini "onay bekliyor" diye veriyordu. `decisions.md` ikisini de onaylı işaretliyor. Plugin ikisini de kullanmıyor; bu planda KOŞULLU görev yok.
  - **A-6 · FR-212'nin alt sınırı yalnız README'lerde yazılı.** Çalışma anında bir Gradle/AGP sürüm denetimi eklenmedi: çevrimdışı ölçülemez, çünkü önbellekte Gradle < 8.11.1 ya da AGP < 8.10.1 yok. AGP 8.10.1'in kendisi de Gradle 8.11.1 istiyor. Takip işi olarak kalır.

---

## Görevler

### T001: Ortam seçimi kuralı — sıra, eski `palbase.env`'in yeri, tek dizin adı
<!-- deps: [] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt] | satisfies: [FR-201, FR-204] -->

Prototipin çözücüsü (`proto-2.4/0001` + `0003`) ile `order-fix.patch`, Gradle'sız saf bir sınıf olarak. Sıra FR-201'in numaralarıyla yazıldı: `-Ppalbase.env.<V|B>` (1) → komut satırındaki `-Ppalbase.env` (2, D-007) → `local.properties` (3) → build type DSL (4) → `gradle.properties` (5) → `benchmark<X>`/`nonMinified<X>` (6) → dosyadaki eski `palbase.env` (7, D-006) → build type adı (8) → `debug`→`local` / `release` ret (9). Kanıt: prototipte isim kuralı eskiden önceydi (`reports/verification-2026-09-25.md` C1, `EnvironmentResolver.kt:84-89` @80fdab5) ve `palbase.env=main` + `staging` build type + `staging/` dizini olan 2.3 checkout'u yükseltmede **sessizce** staging derledi (fx/c1s); `-Ppalbase.env=staging` commit edilmiş `palbase.env.release=prod`'a yeniliyordu (C2, fx/c2).

Prototipten iki bilinçli fark: (a) tek yol parçası kuralı FR-001'in tanımıyla **aynı** (boş; `.`/`..`; `/` ya da `\`; `.` ile başlıyor; kontrol karakteri) — `link`'in yazdığı her adı plugin seçebilsin, reddettiğini link hiç yazmamış olsun. Prototip `a..b`'yi de reddediyordu (FR-001'de yok), kontrol karakterini reddetmiyordu; kontrol karakteri artık reddediliyor ve mesajda `\uXXXX` olarak basılıyor (FR-006'nın plugin karşılığı). (b) Adım 3/5/6 prototipteki biçimleriyle geliyor — `local.properties`'in debuggable kapısı (FR-201.3, C4), `gradle.properties` **dosyası** (FR-201.5/FR-202, C3), düz `benchmark` (FR-201.6, D4) ve flavor anahtarları (FR-201.1'deki F, D-004) sonraki dilimlerin işi; bu görev onların üzerine kurulacağı sırayı sabitler.

**Interfaces:**
- Consumes: —
- Produces:
  - `internal class EnvironmentResolver(sources: EnvironmentSources)` · `fun resolve(variant: String, buildType: String): EnvironmentResolution`
  - `internal sealed interface EnvironmentResolution { data class Selected(val environment: String, val origin: String); data class Refused(val reason: String) }`
  - `internal class EnvironmentSources(commandLine: (String) -> String?, localProperties: (String) -> String?, buildTypeDsl: (String) -> String?, gradleProperties: (String) -> String?, legacy: () -> String?)`
  - `EnvironmentResolver.PROPERTY_PREFIX = "palbase.env."`, `LEGACY_PROPERTY = "palbase.env"`, `DEBUG`, `RELEASE`, `DEBUG_DEFAULT = "local"`, `BUILD_TYPE_NAME_ORIGIN = "from the build type name"`, `MEASURING_PREFIXES`
  - Basılan köken metinleri (sonraki görevlerin testleri bunlara dayanır): `-P<key> on the command line` · `-Ppalbase.env on the command line` · `<key> in local.properties` · ``palbase { environment } in the `<B>` build type`` · `<key> in gradle.properties` · ``<köken>, as `<B>` builds as `<ölçülen>` `` · `palbase.env, the 2.3 global property` · `from the build type name` · `the default for debug`

- [ ] **Adım 1: Kırmızı testi yaz** — `codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt` (yeni dosya):
```kotlin
package io.palbase.gradle

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertFalse
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

/**
 * The resolution ORDER, rule by rule, without a Gradle build in the way. The
 * TestKit suite (PalbaseCodegenPluginTest) proves each place is actually READ
 * from where it lives; this one proves which place wins.
 */
class EnvironmentResolverTest {

    // 9. `debug` gets `local` for free — the environment every checkout has.
    @Test
    fun `debug with nothing set compiles local`() {
        assertSelected("local", "the default for debug", places().resolve("debug", "debug"))
    }

    // 9. A release nobody chose a stack for is REFUSED, and the refusal names both
    // committed ways out.
    @Test
    fun `release with nothing set is refused naming both ways to set it`() {
        val reason = assertRefused(places().resolve("release", "release"))
        assertTrue(reason.contains("palbase.env.release=<environment>"), reason)
        assertTrue(reason.contains("palbase { environment = \"<environment>\" }"), reason)
        assertTrue(reason.contains("import io.palbase.gradle.palbase"), reason)
    }

    @Test
    fun `a flavored release refusal names the variant too`() {
        val reason = assertRefused(places().resolve("freeRelease", "release"))
        assertTrue(reason.contains("variant `freeRelease`"), reason)
    }

    // 1 beats 3 beats 4 beats 5 — each pair measured on its own.
    @Test
    fun `the command line beats local properties`() {
        val resolver = places(
            commandLine = mapOf("palbase.env.debug" to "main"),
            local = mapOf("palbase.env.debug" to "local"),
        )
        assertSelected("main", "-Ppalbase.env.debug on the command line", resolver.resolve("debug", "debug"))
    }

    @Test
    fun `local properties beat the build type dsl`() {
        val resolver = places(
            local = mapOf("palbase.env.release" to "staging"),
            dsl = mapOf("release" to "main"),
        )
        assertSelected("staging", "palbase.env.release in local.properties", resolver.resolve("release", "release"))
    }

    @Test
    fun `the build type dsl beats gradle properties of another build type`() {
        val resolver = places(dsl = mapOf("release" to "main"), gradle = mapOf("palbase.env.debug" to "local"))
        assertSelected("main", "palbase { environment } in the `release` build type", resolver.resolve("release", "release"))
    }

    @Test
    fun `gradle properties select when nothing personal and no dsl is set`() {
        val resolver = places(gradle = mapOf("palbase.env.release" to "main"))
        assertSelected("main", "palbase.env.release in gradle.properties", resolver.resolve("release", "release"))
    }

    // Within one place, the VARIANT key beats the BUILD TYPE key.
    @Test
    fun `the variant key beats the build type key in the same place`() {
        val resolver = places(
            commandLine = mapOf("palbase.env.release" to "main", "palbase.env.paidRelease" to "paid"),
        )
        assertSelected("paid", "-Ppalbase.env.paidRelease on the command line", resolver.resolve("paidRelease", "release"))
        assertSelected("main", "-Ppalbase.env.release on the command line", resolver.resolve("freeRelease", "release"))
    }

    // 4/5. Two COMMITTED places that disagree: refused, both named.
    @Test
    fun `dsl and gradle properties that disagree are refused`() {
        val resolver = places(dsl = mapOf("release" to "main"), gradle = mapOf("palbase.env.release" to "prod"))
        val reason = assertRefused(resolver.resolve("release", "release"))
        assertTrue(reason.contains("palbase { environment = \"main\" }"), reason)
        assertTrue(reason.contains("palbase.env.release=prod"), reason)
    }

    @Test
    fun `dsl and gradle properties that agree are not a conflict`() {
        val resolver = places(dsl = mapOf("release" to "main"), gradle = mapOf("palbase.env.release" to "main"))
        assertSelected("main", "palbase { environment } in the `release` build type", resolver.resolve("release", "release"))
    }

    // A committed VARIANT key the dsl would silently shadow is the same conflict.
    @Test
    fun `a variant key in gradle properties that the dsl would shadow is refused`() {
        val resolver = places(dsl = mapOf("release" to "main"), gradle = mapOf("palbase.env.paidRelease" to "paid"))
        val reason = assertRefused(resolver.resolve("paidRelease", "release"))
        assertTrue(reason.contains("palbase.env.paidRelease=paid"), reason)
    }

    // A PERSONAL override of a conflicting pair is not refused: it was asked for.
    @Test
    fun `a personal value outranks a committed conflict`() {
        val resolver = places(
            local = mapOf("palbase.env.release" to "staging"),
            dsl = mapOf("release" to "main"),
            gradle = mapOf("palbase.env.release" to "prod"),
        )
        assertSelected("staging", "palbase.env.release in local.properties", resolver.resolve("release", "release"))
    }

    // 6. The baseline-profile plugin's build types measure THEIR build's stack.
    @Test
    fun `benchmark and nonMinified build types resolve as the build they measure`() {
        val resolver = places(gradle = mapOf("palbase.env.release" to "main"))
        assertSelected(
            "main",
            "palbase.env.release in gradle.properties, as `benchmarkRelease` builds as `release`",
            resolver.resolve("benchmarkRelease", "benchmarkRelease"),
        )
        assertSelected(
            "main",
            "palbase.env.release in gradle.properties, as `nonMinifiedRelease` builds as `release`",
            resolver.resolve("nonMinifiedRelease", "nonMinifiedRelease"),
        )
    }

    @Test
    fun `a flavored benchmark variant reads its twin variant key`() {
        val resolver = places(dsl = mapOf("release" to "main"), local = mapOf("palbase.env.freeRelease" to "free"))
        assertSelected(
            "free",
            "palbase.env.freeRelease in local.properties, as `benchmarkRelease` builds as `release`",
            resolver.resolve("freeBenchmarkRelease", "benchmarkRelease"),
        )
    }

    @Test
    fun `a benchmark build type can still be set on its own`() {
        val resolver = places(gradle = mapOf("palbase.env.release" to "main", "palbase.env.benchmarkRelease" to "perf"))
        assertSelected(
            "perf",
            "palbase.env.benchmarkRelease in gradle.properties",
            resolver.resolve("benchmarkRelease", "benchmarkRelease"),
        )
    }

    @Test
    fun `a benchmark of an unset release is refused like release`() {
        val reason = assertRefused(places().resolve("benchmarkRelease", "benchmarkRelease"))
        assertTrue(reason.contains("palbase.env.release=<environment>"), reason)
        assertTrue(reason.contains("`benchmarkRelease` builds as `release`"), reason)
    }

    @Test
    fun `a name that merely starts with benchmark is its own build type`() {
        assertSelected("benchmarking", "from the build type name", places().resolve("benchmarking", "benchmarking"))
        assertSelected("benchmark", "from the build type name", places().resolve("benchmark", "benchmark"))
    }

    // 8. Any other build type is its own environment.
    @Test
    fun `a custom build type compiles the environment of its own name`() {
        assertSelected("featureX", "from the build type name", places().resolve("featureX", "featureX"))
        assertSelected("featureX", "from the build type name", places().resolve("freeFeatureX", "featureX"))
    }

    // 7 BEFORE 8 (D-006). A 2.3 checkout — `palbase.env=main`, a custom
    // `staging` build type, and a `staging/` directory because `palbase link`
    // writes every environment — compiled main under 2.3. With the name rule
    // first it would compile staging after the upgrade, green, without a word.
    @Test
    fun `the legacy global beats the build type name`() {
        val resolver = places(legacy = "main")
        assertSelected("main", "palbase.env, the 2.3 global property", resolver.resolve("featureX", "featureX"))
    }

    // 7. …and it keeps a 2.3 checkout building exactly as it did: debug AND release.
    @Test
    fun `the legacy global still selects for debug and release`() {
        val resolver = places(legacy = "main")
        assertSelected("main", "palbase.env, the 2.3 global property", resolver.resolve("debug", "debug"))
        assertSelected("main", "palbase.env, the 2.3 global property", resolver.resolve("release", "release"))
    }

    // 2 (D-007). `-Ppalbase.env` TYPED for this build — the 2.3 README's CI
    // recipe — outranks every committed key, for every variant of the build.
    @Test
    fun `the command line global beats committed build type keys`() {
        val resolver = places(
            commandLine = mapOf("palbase.env" to "staging"),
            gradle = mapOf("palbase.env.release" to "prod"),
            legacy = "staging",
        )
        assertSelected("staging", "-Ppalbase.env on the command line", resolver.resolve("release", "release"))
        assertSelected("staging", "-Ppalbase.env on the command line", resolver.resolve("qa", "qa"))
    }

    // 1 before 2: a per-build-type key on the command line still beats the global one.
    @Test
    fun `a command line build type key beats the command line global`() {
        val resolver = places(commandLine = mapOf("palbase.env" to "staging", "palbase.env.release" to "prod"))
        assertSelected("prod", "-Ppalbase.env.release on the command line", resolver.resolve("release", "release"))
    }

    @Test
    fun `values are trimmed`() {
        val resolver = places(local = mapOf("palbase.env.debug" to "  main  "))
        assertSelected("main", "palbase.env.debug in local.properties", resolver.resolve("debug", "debug"))
    }

    // The name is joined onto palbase/environments/: ONE clean segment, or
    // refused — the same rule `palbase link` applies before it writes one.
    @Test
    fun `an environment that is not one directory name is refused`() {
        val bad = listOf("../main", "a/b", "a\\b", "..", ".", ".hidden", "", "   ", "a\u0000b", "a\u001bb", "a\u007fb")
        for (name in bad) {
            val reason = assertRefused(places(gradle = mapOf("palbase.env.release" to name)).resolve("release", "release"))
            assertTrue(reason.contains("palbase.env.release in gradle.properties"), "origin missing for `$name`: $reason")
            assertTrue(reason.contains("ONE directory name"), reason)
            assertFalse(reason.any { it.isISOControl() }, "a control character reached the message: $reason")
        }
    }

    @Test
    fun `the names palbase link writes are one directory name`() {
        for (name in listOf("main", "Production", "featureX", "feature-profile-update")) {
            assertSelected(
                name,
                "palbase.env.release in gradle.properties",
                places(gradle = mapOf("palbase.env.release" to name)).resolve("release", "release"),
            )
        }
    }

    // Only the places the answer depended on are read — each lookup is a
    // configuration-cache input, so a read that cannot change the answer is a
    // cache miss for nothing.
    @Test
    fun `places after the first hit are never read`() {
        val read = mutableListOf<String>()
        val resolver = EnvironmentResolver(
            EnvironmentSources(
                commandLine = { read += "cli:$it"; null },
                localProperties = { read += "local:$it"; if (it == "palbase.env.debug") "main" else null },
                buildTypeDsl = { read += "dsl:$it"; null },
                gradleProperties = { read += "gradle:$it"; null },
                legacy = { read += "legacy"; null },
            ),
        )
        resolver.resolve("debug", "debug")
        assertEquals(listOf("cli:palbase.env.debug", "cli:palbase.env", "local:palbase.env.debug"), read)
    }

    private fun places(
        commandLine: Map<String, String> = emptyMap(),
        local: Map<String, String> = emptyMap(),
        dsl: Map<String, String> = emptyMap(),
        gradle: Map<String, String> = emptyMap(),
        legacy: String? = null,
    ) = EnvironmentResolver(
        EnvironmentSources(
            commandLine = commandLine::get,
            localProperties = local::get,
            buildTypeDsl = dsl::get,
            gradleProperties = gradle::get,
            legacy = { legacy },
        ),
    )

    private fun assertSelected(environment: String, origin: String, actual: EnvironmentResolution) {
        assertEquals(EnvironmentResolution.Selected(environment, origin), actual)
    }

    private fun assertRefused(actual: EnvironmentResolution): String {
        assertTrue(actual is EnvironmentResolution.Refused, "expected a refusal, got $actual")
        return (actual as EnvironmentResolution.Refused).reason
    }
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest'` (her Gradle komutu `JAVA_HOME` = Android Studio JBR ve `ANDROID_HOME` ayarlıyken; `:test` şart — iki nokta olmadan `--tests` filtresi `:codegen-engine:test`'e de gider ve o `No tests found for given includes` ile düşer, ölçüldü) · Beklenen: **FAIL**, çıktıda `e: …/EnvironmentResolverTest.kt:242:24 Unresolved reference 'EnvironmentResolver'.`
- [ ] **Adım 3: Uygula** — `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt` (yeni dosya; henüz hiçbir yerden çağrılmıyor — bağlama T002'de):
```kotlin
package io.palbase.gradle

/**
 * WHICH ENVIRONMENT ONE VARIANT COMPILES.
 *
 * 2.3 had one answer for the whole build — `palbase.env`, `local` when unset —
 * so `debug` and `release` could not aim at different stacks, and a release
 * built on a machine that never set the property compiled `local` without a
 * word. The build TYPE is what an Android developer already uses to say "this
 * build is for that audience", so the build type picks the stack.
 *
 * For variant V of build type B the first hit wins:
 *
 *  1. `-Ppalbase.env.<V>`, then `-Ppalbase.env.<B>`, on the command line.
 *  2. `-Ppalbase.env` on the command line — every variant of that invocation.
 *     It was typed for THIS build (the 2.3 CI recipe), so it outranks every file.
 *  3. `palbase.env.<V>`, then `palbase.env.<B>`, in the root `local.properties`.
 *  4. `palbase { environment = "…" }` inside build type B.
 *  5. `palbase.env.<V>`, then `palbase.env.<B>`, in `gradle.properties`. When 4
 *     is set as well and the two differ, REFUSE: two committed places disagree,
 *     and whichever one lost would be a lie the next reader believes.
 *  6. `benchmark<X>` / `nonMinified<X>` — the baseline-profile plugin's build
 *     types — resolve as `<x>`: they exist to measure THAT build, so they aim at
 *     its stack instead of getting one of their own.
 *  7. `palbase.env`, the 2.3 global property, from a file. BEFORE the name
 *     rule: a 2.3 checkout with `palbase.env=main` and a custom `staging` build
 *     type compiled main, and must not switch to `staging/` on upgrade just
 *     because `palbase link` wrote that directory too.
 *  8. A build type other than `debug`/`release` is its own environment:
 *     `featureX` compiles `palbase/environments/featureX/`.
 *  9. `debug` compiles `local`. `release` REFUSES: nothing picks the stack a
 *     release ships against by default.
 *
 * The personal places (1–3) outrank the committed ones (4, 5); the committed
 * ones outrank every convention (6–9).
 *
 * The answer is a NAME. Whether `palbase/environments/<name>/` exists is decided
 * when the task runs — `palbase link` writes it, possibly after this build was
 * configured — and a missing one is refused there, never substituted.
 */
internal class EnvironmentResolver(private val sources: EnvironmentSources) {

    fun resolve(variant: String, buildType: String): EnvironmentResolution =
        when (val chosen = choose(variant, buildType)) {
            is EnvironmentResolution.Refused -> chosen
            is EnvironmentResolution.Selected -> requireOneSegment(chosen)
        }

    private fun choose(variant: String, buildType: String): EnvironmentResolution {
        val keys = listOf(variant, buildType).distinct().map { PROPERTY_PREFIX + it }

        keys.firstHit(sources.commandLine)?.let { (key, value) ->
            return EnvironmentResolution.Selected(value, "-P$key on the command line")
        }
        sources.commandLine(LEGACY_PROPERTY)?.trim()?.let {
            return EnvironmentResolution.Selected(it, "-P$LEGACY_PROPERTY on the command line")
        }
        keys.firstHit(sources.localProperties)?.let { (key, value) ->
            return EnvironmentResolution.Selected(value, "$key in local.properties")
        }

        val declared = sources.buildTypeDsl(buildType)?.trim()
        val committed = keys.firstHit(sources.gradleProperties)
        if (declared != null) {
            if (committed != null && committed.second != declared) {
                return EnvironmentResolution.Refused(
                    "Palbase: the `$buildType` build type names two environments in two committed places — " +
                        "`palbase { environment = \"$declared\" }` in the build type and " +
                        "`${committed.first}=${committed.second}` in gradle.properties. Keep ONE of them; " +
                        "a build that picked either would leave the other one lying to the next reader.",
                )
            }
            return EnvironmentResolution.Selected(declared, "palbase { environment } in the `$buildType` build type")
        }
        committed?.let { (key, value) ->
            return EnvironmentResolution.Selected(value, "$key in gradle.properties")
        }

        measuredBuildType(buildType)?.let { measured ->
            val because = "`$buildType` builds as `$measured`"
            return when (val twin = choose(twinVariant(variant, buildType, measured), measured)) {
                is EnvironmentResolution.Selected -> twin.copy(origin = "${twin.origin}, as $because")
                is EnvironmentResolution.Refused -> EnvironmentResolution.Refused("${twin.reason} ($because.)")
            }
        }

        sources.legacy()?.trim()?.let {
            return EnvironmentResolution.Selected(it, "$LEGACY_PROPERTY, the 2.3 global property")
        }
        if (buildType != DEBUG && buildType != RELEASE) {
            return EnvironmentResolution.Selected(buildType, BUILD_TYPE_NAME_ORIGIN)
        }
        if (buildType == DEBUG) {
            return EnvironmentResolution.Selected(DEBUG_DEFAULT, "the default for debug")
        }
        val variantClause = if (variant == buildType) "" else " (variant `$variant`)"
        return EnvironmentResolution.Refused(
            "Palbase: `$RELEASE`$variantClause has no environment, and a release never gets one by default — " +
                "it would ship a client aimed at a stack nobody chose. Commit the choice, in ONE of two ways: " +
                "`$PROPERTY_PREFIX$RELEASE=<environment>` in gradle.properties, or " +
                "`buildTypes { release { palbase { environment = \"<environment>\" } } }` in the build script " +
                "(Kotlin DSL: `import io.palbase.gradle.palbase`).",
        )
    }

    /**
     * ONE CLEAN DIRECTORY NAME, NOTHING ELSE — the rule `palbase link` applies
     * before it writes one, so whatever link wrote can be selected and whatever
     * is refused here was never written. The name is joined onto
     * `palbase/environments/`: a separator or a `..` would read a contract from
     * somewhere link never wrote, a leading `.` names a hidden directory no
     * environment lives in, and a control character is a name nobody typed.
     */
    private fun requireOneSegment(selected: EnvironmentResolution.Selected): EnvironmentResolution {
        val name = selected.environment
        val problem = when {
            name.isEmpty() -> "is empty"
            '/' in name || '\\' in name -> "contains a path separator"
            name.startsWith('.') -> "starts with `.`"
            name.any { it.isISOControl() } -> "contains a control character"
            else -> return selected
        }
        return EnvironmentResolution.Refused(
            "Palbase: environment `${printable(name)}` (${selected.origin}) $problem. An environment is ONE " +
                "directory name under palbase/environments/, exactly as `palbase link` wrote it.",
        )
    }

    /** The name as it can be shown: a control character would drive the terminal the message is read in. */
    private fun printable(name: String) = buildString {
        name.forEach { if (it.isISOControl()) append("\\u%04x".format(it.code)) else append(it) }
    }

    /** `benchmarkRelease` → `release`, `nonMinifiedRelease` → `release`; anything else → null. */
    private fun measuredBuildType(buildType: String): String? = MEASURING_PREFIXES.firstNotNullOfOrNull { prefix ->
        buildType.removePrefix(prefix)
            .takeIf { buildType.startsWith(prefix) && it.firstOrNull()?.isUpperCase() == true }
            ?.replaceFirstChar { it.lowercase() }
    }

    /** `freeBenchmarkRelease` measures `freeRelease`, so it reads `palbase.env.freeRelease` too. */
    private fun twinVariant(variant: String, buildType: String, measured: String): String {
        val suffix = buildType.replaceFirstChar { it.uppercase() }
        return when {
            variant.endsWith(suffix) && variant.length > suffix.length ->
                variant.dropLast(suffix.length) + measured.replaceFirstChar { it.uppercase() }
            else -> measured
        }
    }

    private fun List<String>.firstHit(lookup: (String) -> String?): Pair<String, String>? =
        firstNotNullOfOrNull { key -> lookup(key)?.let { key to it.trim() } }

    internal companion object {
        const val PROPERTY_PREFIX = "palbase.env."
        const val LEGACY_PROPERTY = "palbase.env"
        const val DEBUG = "debug"
        const val RELEASE = "release"
        const val DEBUG_DEFAULT = "local"

        /** Rule 8's origin: nobody named an environment, the build type's own name did. */
        const val BUILD_TYPE_NAME_ORIGIN = "from the build type name"
        val MEASURING_PREFIXES = listOf("benchmark", "nonMinified")
    }
}

/** What [EnvironmentResolver] decided for one variant. */
internal sealed interface EnvironmentResolution {
    /** [origin] says where [environment] came from, in words — it is printed. */
    data class Selected(val environment: String, val origin: String) : EnvironmentResolution

    /** The build must not choose; [reason] is the whole sentence the task refuses with. */
    data class Refused(val reason: String) : EnvironmentResolution
}

/**
 * Every place an environment can be named, each one a lookup that is consulted
 * only when the resolution reaches it — a place never read is never a
 * configuration-cache input, and cannot have changed the answer.
 */
internal class EnvironmentSources(
    /** `-P` values exactly as they were typed on the command line. */
    val commandLine: (String) -> String?,
    /** The root project's `local.properties`. */
    val localProperties: (String) -> String?,
    /** `palbase { environment }` by BUILD TYPE name. */
    val buildTypeDsl: (String) -> String?,
    /**
     * Gradle properties, asked only after the command line had its turn for the
     * same keys. `providers.gradleProperty` also answers from
     * `ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*` and
     * `~/.gradle/gradle.properties`; all of them are labelled gradle.properties.
     */
    val gradleProperties: (String) -> String?,
    /** `palbase.env`, wherever Gradle found it; the command line was asked first. */
    val legacy: () -> String?,
)
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest'` · Beklenen: `BUILD SUCCESSFUL`; `build/test-results/test/TEST-io.palbase.gradle.EnvironmentResolverTest.xml` → `tests="26" skipped="0" failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt && git commit -m "feat(codegen): ortam seçimi variant başına bir KURAL — sıra, eski palbase.env'in yeri ve tek dizin adı birim testle sabit"`

---

### T002: Her variant kendi ortamını derler — release varsayılanla derlenmez, 2.3'ün `palbase.env`'i bozulmaz
<!-- deps: [T001] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-201, FR-204] -->

T001'in kuralı Gradle'a bağlanır: `onVariants` her variant için `resolve(variant.name, variant.buildType)` çağırır; seçilen ad ve kökeni göreve girdi olur, **ret ise görevde, çalışırken** atılır — ayarlanmamış bir `release` ne `assembleDebug`'ı ne IDE sync'i düşürür (prototip, `proto-2.4/0001`). Global `palbase.env` sağlayıcısı ve `local` varsayılanı eklentiden kalkar. Her üretim bir satır basar: `Palbase: <variant> → <env> (<origin>)` (FR-211'in kök kısmı `[<root>]` FR-205 dilimiyle gelir). Build type DSL'i henüz yok (`buildTypeDsl = { null }`, T004'te bağlanır) — ret metinleri `palbase { environment = … }`'i şimdiden anar, T002→T004 arası o yol derlenmez; kök hâlâ `environmentsDir` konvansiyonu (`<modül>/palbase/environments`, T007'de değişir); dizin kontrolü hâlâ 2.3'ün `isDirectory`'si (T006'da birebir ada döner).

Mevcut 27 TestKit testinin hiçbiri değişmez (hepsi `assembleDebug` koşar; `debug`'ın varsayılanı `local` kalır — D-003). Yalnız fixture yardımcıları genişler: build type/flavor/library parametreleri, variant başına paketlenen asset'i okuyan `asset()`/`hasAsset()`, ve her ortamın kendi yığınını adlandırdığı `environment()` — hangi ortamın derlendiği APK'ya girecek dosyadan okunur (`build/generated/assets/generatePalbase<V>/palbase/palbase-config.json`, AGP'nin variant asset'lerine kattığı dizin).

Not: `the 2_3 global property wins over a custom build type name` ve `the command line global outranks a committed build type key for every variant` 2.3'te de doğru ortamı derliyordu; kırmızıları yalnız `Palbase:` satırının yokluğundan. İkisi D-006/D-007'nin **yanlış sırasına** karşı bekçidir — mutasyonla ölçüldü: isim kuralı eskinin önüne alınıp 2. adım silinince ikisi de düştü (`staging compiled {"app_id":"app_android","base_url":"https://staging1234m.dev.palbase.studio",…}`).

Bu görevden sonra SDK reposunun kendi release kapısı kırmızıdır (`:consumer-release:generatePalbaseRelease` reddeder) — T003 onu hemen yeşile döndürür.

**Interfaces:**
- Consumes: `EnvironmentResolver`, `EnvironmentSources`, `EnvironmentResolution` (T001)
- Produces:
  - `AndroidVariantIntegration.configure(project: Project, extension: PalbaseExtension)` (üçüncü parametre `environment: Provider<String>` kalktı)
  - `private fun sources(project: Project, rootDirectory: Directory): EnvironmentSources` — `-P`: `gradle.startParameter.projectProperties`; `local.properties`: `providers.fileContents(<rootDir>/local.properties)`; `gradle.properties` ve `palbase.env`: `providers.gradleProperty`
  - `GeneratePalbaseTask`: `environment: Property<String>` artık `@Input @Optional`; yeni `environmentOrigin: Property<String>` (`@Internal`), `environmentRefusal: Property<String>` (`@Input @Optional`), `variantName: Property<String>` (`@Internal`)
  - Lifecycle satırı: `Palbase: <variant> → <env> (<origin>)`
  - Kaldırılan: `PalbaseCodegenPlugin.ENVIRONMENT_PROPERTY`, `PalbaseCodegenPlugin.DEFAULT_ENVIRONMENT`
  - Test yardımcıları (`PalbaseCodegenPluginTest`): `fixture(openApi, config, roles, buildTypes: String = "", library: Boolean = false, android: String = "")`, `kotlinBuildScript(buildTypes, library, android)`, `Fixture.environment(name)`, `Fixture.asset(variant)`, `Fixture.hasAsset(variant)`, companion'da `Path.write`, `stackOf(environment)`, `assertCompiled(project, variant, environment)`

- [ ] **Adım 1: Kırmızı testi yaz** — `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:

  (a) `a known-empty role field means no role enums` testinin kapanışından sonra, `/** Two environments that differ in BOTH artifacts, … */` KDoc'undan önce ekle:
```kotlin
    // ---- 2.5: THE BUILD TYPE SELECTS THE ENVIRONMENT ------------------------
    //
    // The ORDER of the places is pinned in EnvironmentResolverTest, without a
    // build. These prove each place is READ from where it actually lives, per
    // variant, and that the answer survives the configuration cache.

    // Nothing picks a release stack by default — and a release nobody configured
    // must not take `debug` down with it: the refusal belongs to the release
    // task, at execution, not to configuration.
    @Test
    fun `an unset release refuses its own generation and leaves debug building`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")

        val debug = project.build("generatePalbaseDebug")
        assertEquals(TaskOutcome.SUCCESS, debug.task(":generatePalbaseDebug")?.outcome)
        assertTrue(debug.output.contains("Palbase: debug → local (the default for debug)"), debug.output)
        val failure = project.buildAndFail("generatePalbaseRelease").output

        assertTrue(failure.contains("`release` has no environment"), failure)
        assertTrue(failure.contains("palbase.env.release=<environment>"), failure)
        assertTrue(failure.contains("This checkout carries local, main"), failure)
        assertFalse(project.hasAsset("release"), "a refused release generated a config")
    }

    @Test
    fun `gradle properties select per build type`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=main\n")

        val result = project.build("generatePalbaseDebug", "generatePalbaseRelease")

        assertCompiled(project, "debug", "local")
        assertCompiled(project, "release", "main")
        assertTrue(result.output.contains("Palbase: release → main (palbase.env.release in gradle.properties)"), result.output)
    }

    // A 2.3 checkout — `palbase.env=main` in gradle.properties — builds BOTH
    // variants exactly as it did.
    @Test
    fun `the 2_3 global property still selects for debug and release`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env=main\n")

        project.build("generatePalbaseDebug", "generatePalbaseRelease")

        assertCompiled(project, "debug", "main")
        assertCompiled(project, "release", "main")
    }

    // D-006, where it bites: `palbase link` writes EVERY environment, so a 2.3
    // checkout with a custom `staging` build type has a `staging/` directory
    // beside `main/`. Under 2.3 that build type compiled main; if the name rule
    // came first it would compile staging after the upgrade — green, silently.
    @Test
    fun `the 2_3 global property wins over a custom build type name`() {
        val project = fixture(buildTypes = """create("staging") { initWith(getByName("debug")) }""")
        project.environment("main")
        project.environment("staging")
        project.root.resolve("gradle.properties").write("palbase.env=main\n")

        val result = project.build("generatePalbaseStaging")

        assertCompiled(project, "staging", "main")
        assertTrue(result.output.contains("Palbase: staging → main (palbase.env, the 2.3 global property)"), result.output)
    }

    // D-007: the 2.3 README's CI recipe, `-Ppalbase.env=<env>`, was typed for
    // THIS build. A committed per-build-type key must not quietly outrank it.
    @Test
    fun `the command line global outranks a committed build type key for every variant`() {
        val project = fixture(buildTypes = """create("qa") { initWith(getByName("debug")) }""")
        project.environment("prod")
        project.environment("staging")
        project.root.resolve("gradle.properties").write("palbase.env.release=prod\n")

        val result = project.build(
            "generatePalbaseDebug", "generatePalbaseRelease", "generatePalbaseQa", "-Ppalbase.env=staging",
        )

        assertCompiled(project, "debug", "staging")
        assertCompiled(project, "release", "staging")
        assertCompiled(project, "qa", "staging")
        assertTrue(result.output.contains("Palbase: release → staging (-Ppalbase.env on the command line)"), result.output)
    }

    // local.properties is PERSONAL and outranks the committed choice — and
    // editing it must invalidate a cached configuration instead of compiling the
    // stack the cache remembered.
    @Test
    fun `local properties override gradle properties and are a configuration cache input`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.debug=main\n")

        val first = project.build("generatePalbaseDebug", "--configuration-cache")
        assertTrue(first.output.contains("Configuration cache entry stored"), first.output)
        assertCompiled(project, "debug", "main")

        project.root.resolve("local.properties").write("palbase.env.debug=local\n")
        val second = project.build("generatePalbaseDebug", "--configuration-cache")

        assertFalse(second.output.contains("Configuration cache entry reused"), second.output)
        assertCompiled(project, "debug", "local")
        assertTrue(second.output.contains("(palbase.env.debug in local.properties)"), second.output)
    }

    // The command line outranks local.properties — and dropping the flag must NOT
    // reuse the entry stored with it, even when the flag named the very value
    // gradle.properties carries (the value alone cannot tell the two apart).
    @Test
    fun `the command line outranks local properties and dropping it reconfigures`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.debug=main\n")
        project.root.resolve("local.properties").write("palbase.env.debug=local\n")

        val first = project.build("generatePalbaseDebug", "--configuration-cache", "-Ppalbase.env.debug=main")
        assertTrue(first.output.contains("Configuration cache entry stored"), first.output)
        assertCompiled(project, "debug", "main")

        val second = project.build("generatePalbaseDebug", "--configuration-cache")
        assertFalse(second.output.contains("Configuration cache entry reused"), second.output)
        assertCompiled(project, "debug", "local")
    }

    // A build type is its own environment — and a missing one is REFUSED, saying
    // where the name came from, never substituted.
    @Test
    fun `a custom build type compiles its namesake and refuses a missing one`() {
        val project = fixture(
            buildTypes = """
                create("featureX") { initWith(getByName("debug")) }
                create("qa") { initWith(getByName("debug")) }
            """.trimIndent(),
        )
        project.environment("local")
        project.environment("featureX")

        val built = project.build("generatePalbaseFeatureX")
        assertCompiled(project, "featureX", "featureX")
        assertTrue(built.output.contains("Palbase: featureX → featureX (from the build type name)"), built.output)

        val failure = project.buildAndFail("generatePalbaseQa").output
        assertTrue(failure.contains("environment `qa` (from the build type name) has no directory"), failure)
        assertTrue(failure.contains("palbase/environments/qa"), failure)
        assertTrue(failure.contains("featureX, local"), failure)
        assertFalse(project.hasAsset("qa"), "a missing environment generated a config")
    }

    // A FLAVORED variant reads its own key before its build type's.
    @Test
    fun `a variant key selects for one flavor of a build type`() {
        val project = fixture(
            android = """
                flavorDimensions += "tier"
                productFlavors { create("free") { dimension = "tier" }; create("paid") { dimension = "tier" } }
            """.trimIndent(),
        )
        project.environment("local")
        project.environment("main")

        val result = project.build("generatePalbaseFreeDebug", "generatePalbasePaidDebug", "-Ppalbase.env.paidDebug=main")

        assertCompiled(project, "freeDebug", "local")
        assertCompiled(project, "paidDebug", "main")
        assertTrue(result.output.contains("Palbase: paidDebug → main (-Ppalbase.env.paidDebug on the command line)"), result.output)
    }

    // The baseline-profile plugin's build type measures RELEASE, so it compiles
    // release's stack instead of a `benchmarkRelease` environment nobody has.
    @Test
    fun `a benchmark build type compiles the environment of the build it measures`() {
        val project = fixture(
            buildTypes = """create("benchmarkRelease") { initWith(getByName("release")) }""",
        )
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=main\n")

        val result = project.build("generatePalbaseBenchmarkRelease")

        assertCompiled(project, "benchmarkRelease", "main")
        assertTrue(result.output.contains("as `benchmarkRelease` builds as `release`"), result.output)
    }

    // A LIBRARY module resolves per build type by the same rules.
    @Test
    fun `a library module selects per build type too`() {
        val project = fixture(library = true)
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=main\n")

        project.build("generatePalbaseDebug", "generatePalbaseRelease")

        assertCompiled(project, "debug", "local")
        assertCompiled(project, "release", "main")
    }

    @Test
    fun `an environment that is not one directory name is refused`() {
        val project = fixture()
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=../main\n")

        val failure = project.buildAndFail("generatePalbaseRelease").output

        assertTrue(failure.contains("environment `../main` (palbase.env.release in gradle.properties) contains a path separator"), failure)
        assertFalse(project.hasAsset("release"), "a name outside palbase/environments/ was read")
    }
```
  (b) `fixture(...)` başlığını ve içindeki satır içi build script'i — şu bloğu:
```kotlin
    private fun fixture(openApi: Boolean = false, config: Boolean = false, roles: Boolean = false): Fixture {
        val root = Files.createTempDirectory(tempDir, "fixture-")
        root.resolve("settings.gradle.kts").write(
            """
            pluginManagement { repositories { google(); mavenCentral(); gradlePluginPortal() } }
            dependencyResolutionManagement {
                repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
                repositories { google(); mavenCentral() }
            }
            rootProject.name = "codegen-fixture"
            """.trimIndent(),
        )
        root.resolve("build.gradle.kts").write(
            """
            plugins {
                id("com.android.application")
                id("org.jetbrains.kotlin.android")
                id("org.jetbrains.kotlin.plugin.serialization")
                id("io.palbase.codegen")
            }

            android {
                namespace = "test.fixture"
                compileSdk = 36
                defaultConfig { applicationId = "test.fixture"; minSdk = 26 }
                compileOptions {
                    sourceCompatibility = JavaVersion.VERSION_17
                    targetCompatibility = JavaVersion.VERSION_17
                }
            }

            kotlin {
                compilerOptions {
                    jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
                }
            }

            palbase { packageName.set("io.palbase.generated") }

            dependencies {
                implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
            }
            """.trimIndent(),
        )
```
  şununla değiştir:
```kotlin
    /**
     * @param buildTypes Kotlin DSL placed inside `android { buildTypes { … } }`.
     * @param library applies `com.android.library` instead of the application plugin.
     * @param android Kotlin DSL placed inside `android { }`, before `buildTypes`.
     */
    private fun fixture(
        openApi: Boolean = false,
        config: Boolean = false,
        roles: Boolean = false,
        buildTypes: String = "",
        library: Boolean = false,
        android: String = "",
    ): Fixture {
        val root = Files.createTempDirectory(tempDir, "fixture-")
        root.resolve("settings.gradle.kts").write(
            """
            pluginManagement { repositories { google(); mavenCentral(); gradlePluginPortal() } }
            dependencyResolutionManagement {
                repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
                repositories { google(); mavenCentral() }
            }
            rootProject.name = "codegen-fixture"
            """.trimIndent(),
        )
        root.resolve("build.gradle.kts").write(kotlinBuildScript(buildTypes, library, android))
```
  (c) `fixture(...)`'in kapanışından sonra, `private data class Fixture` satırından önce ekle:
```kotlin
    private fun kotlinBuildScript(buildTypes: String, library: Boolean, android: String) = """
        plugins {
            id("${if (library) "com.android.library" else "com.android.application"}")
            id("org.jetbrains.kotlin.android")
            id("org.jetbrains.kotlin.plugin.serialization")
            id("io.palbase.codegen")
        }

        android {
            namespace = "test.fixture"
            compileSdk = 36
            defaultConfig { ${if (library) "" else "applicationId = \"test.fixture\"; "}minSdk = 26 }
            compileOptions {
                sourceCompatibility = JavaVersion.VERSION_17
                targetCompatibility = JavaVersion.VERSION_17
            }
    """.trimIndent() + "\n" + android + "\n" + """
            buildTypes {
    """.trimIndent() + "\n" + buildTypes + "\n" + """
            }
        }

        kotlin {
            compilerOptions {
                jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
            }
        }

        palbase { packageName.set("io.palbase.generated") }

        dependencies {
            implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
        }
    """.trimIndent()
```
  (d) `Fixture` içinde şu satırın hemen altına:
```kotlin
        /** `palbase/environments/<env>/<name>` — the one place an environment's artifacts live. */
        fun environmentFile(environment: String, name: String): Path =
            root.resolve("palbase/environments/$environment/$name")
```
  şunu ekle:
```kotlin
        /**
         * One complete environment under `palbase/environments/<name>/`, its
         * config naming a stack of its own — `<name>1234m.dev.palbase.studio` — so
         * which environment a variant compiled is readable from its asset.
         */
        fun environment(name: String) {
            environmentFile(name, "openapi.json").write(OPEN_API)
            environmentFile(name, "android-config.json").write(configFor(stackOf(name)))
        }

        /** The runtime config ONE variant packs — the file AGP merges into that variant's APK. */
        private fun assetFile(variant: String): Path {
            val task = "generatePalbase" + variant.replaceFirstChar { it.uppercase() }
            return root.resolve("build/generated/assets/$task/palbase/palbase-config.json")
        }

        fun asset(variant: String): String = Files.readString(assetFile(variant))

        fun hasAsset(variant: String): Boolean = Files.isRegularFile(assetFile(variant))
```
  (e) Sınıf düzeyindeki şu yardımcıyı **sil** (companion'a taşınıyor — `Fixture` iç içe bir sınıf, dış sınıfın özel üyesine erişemez):
```kotlin
    private fun Path.write(content: String) {
        parent?.let(Files::createDirectories)
        Files.writeString(this, content)
    }
```
  ve `private companion object {` satırını şu blokla değiştir (companion'ın geri kalanı aynen kalır):
```kotlin
    private companion object {
        fun Path.write(content: String) {
            parent?.let(Files::createDirectories)
            Files.writeString(this, content)
        }

        /** The stack an environment's config names: `main` → `main1234m`, `featureX` → `featurex1234m`. */
        fun stackOf(environment: String) = environment.lowercase().filter { it.isLetterOrDigit() } + "1234m"

        fun assertCompiled(project: Fixture, variant: String, environment: String) {
            val asset = project.asset(variant)
            assertTrue(asset.contains("${stackOf(environment)}.dev.palbase.studio"), "$variant compiled $asset")
        }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.PalbaseCodegenPluginTest'` · Beklenen: **FAIL**, `39 tests completed, 11 failed`; belirleyici satırlar: `gradle properties select per build type() FAILED` → `release compiled {"app_id":"app_android","base_url":"https://local1234m.dev.palbase.studio","api_key":"pb_local1234m_c0123456789abcdefghij"}` (2.3 release'i sessizce `local` derliyor); `an unset release refuses its own generation and leaves debug building() FAILED` (debug çıktısında `Palbase:` satırı yok); `an environment that is not one directory name is refused() FAILED` → ``Palbase: environment `local` has no directory`` (2.3 `palbase.env.release`'i hiç okumuyor); `a benchmark build type compiles the environment of the build it measures() FAILED` → `UnexpectedBuildFailure`. Eski 27 test geçer.
- [ ] **Adım 3: Uygula** —

  (a) `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt` dosyasının tamamı:
```kotlin
package io.palbase.gradle

import com.android.build.api.variant.AndroidComponentsExtension
import com.android.build.api.variant.ApplicationVariant
import java.io.StringReader
import java.util.Properties
import org.gradle.api.Project
import org.gradle.api.file.Directory

/** Kept out of the plugin entry class so AGP types load only after Android is applied. */
internal object AndroidVariantIntegration {
    fun configure(project: Project, extension: PalbaseExtension) {
        @Suppress("UNCHECKED_CAST")
        val androidComponents = project.extensions.getByType(AndroidComponentsExtension::class.java)
            as AndroidComponentsExtension<*, *, *>

        val rootDirectory = project.layout.projectDirectory.dir(project.rootDir.absolutePath)
        val resolver = EnvironmentResolver(sources(project, rootDirectory))

        androidComponents.onVariants(androidComponents.selector().all()) { variant ->
            val capitalized = variant.name.replaceFirstChar { it.uppercase() }
            val resolution = resolver.resolve(variant.name, variant.buildType ?: variant.name)
            val task = project.tasks.register(
                "generatePalbase$capitalized",
                GeneratePalbaseTask::class.java,
            ) { generate ->
                generate.group = "palbase"
                generate.description = "Generates Palbase Kotlin APIs and config for ${variant.name}"
                generate.variantName.set(variant.name)
                when (resolution) {
                    is EnvironmentResolution.Selected -> {
                        generate.environment.set(resolution.environment)
                        generate.environmentOrigin.set(resolution.origin)
                    }
                    // Refused when the task RUNS, not here: a release nobody chose a
                    // stack for must not take `assembleDebug` — or an IDE sync — down
                    // with it.
                    is EnvironmentResolution.Refused -> generate.environmentRefusal.set(resolution.reason)
                }
                generate.environmentsDir.set(extension.environmentsDir)
                // EVERY environment, not just the selected one — see the property's
                // own note in GeneratePalbaseTask: the SET is part of the input.
                generate.environmentFiles.from(extension.environmentsDir)
                generate.packageName.set(extension.packageName)
                if (variant is ApplicationVariant) {
                    generate.applicationId.set(variant.applicationId)
                }
                generate.kotlinOutput.set(
                    project.layout.buildDirectory.dir("generated/palbase/${variant.name}/kotlin"),
                )
                generate.assetOutput.set(
                    project.layout.buildDirectory.dir("generated/palbase/${variant.name}/assets"),
                )
                generate.resOutput.set(
                    project.layout.buildDirectory.dir("generated/palbase/${variant.name}/res"),
                )
                generate.manifestOutput.set(
                    project.layout.buildDirectory.file("generated/palbase/${variant.name}/AndroidManifest.xml"),
                )
            }

            // External Kotlin Android plugins consume Android's Java source model.
            // Wiring here keeps the plugin compatible before AGP's built-in Kotlin mode too.
            variant.sources.java?.addGeneratedSourceDirectory(task, GeneratePalbaseTask::kotlinOutput)
            variant.sources.assets?.addGeneratedSourceDirectory(task, GeneratePalbaseTask::assetOutput)
            // `res` carries the cleartext allowance for a local stack. AGP 8.10's
            // Sources.getRes() is a Layered source set, same shape as assets above.
            variant.sources.res?.addGeneratedSourceDirectory(task, GeneratePalbaseTask::resOutput)
            variant.sources.manifests.addGeneratedManifestFile(task, GeneratePalbaseTask::manifestOutput)
        }
    }

    /**
     * Every place [EnvironmentResolver] reads, each through an API the
     * configuration cache fingerprints:
     *
     * - `-P` values: Gradle keys a cache entry on the whole set of command-line
     *   properties ("the set of Gradle properties has changed"), so the plain
     *   start-parameter map is safe — and it is the only thing that tells a
     *   command-line value apart from the same key in `gradle.properties`.
     * - `local.properties`: `providers.fileContents`, so editing the file — or
     *   creating it — invalidates the entry. (AGP 8.x happens to fingerprint the
     *   same file for `sdk.dir`; this read does not lean on that.)
     * - `gradle.properties` and `palbase.env`: `providers.gradleProperty`.
     */
    private fun sources(project: Project, rootDirectory: Directory): EnvironmentSources {
        val commandLine = project.gradle.startParameter.projectProperties
        val localProperties by lazy {
            val text = project.providers.fileContents(rootDirectory.file(LOCAL_PROPERTIES)).asText.orNull
            Properties().apply { if (text != null) load(StringReader(text)) }
        }
        return EnvironmentSources(
            commandLine = { key -> commandLine[key] },
            localProperties = { key -> localProperties.getProperty(key) },
            // No build type carries `palbase { environment }` yet.
            buildTypeDsl = { null },
            gradleProperties = { key -> project.providers.gradleProperty(key).orNull },
            legacy = { project.providers.gradleProperty(EnvironmentResolver.LEGACY_PROPERTY).orNull },
        )
    }

    private const val LOCAL_PROPERTIES = "local.properties"
}
```
  (b) `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt` — şu özelliği:
```kotlin
    /**
     * The environment this build compiles: the `palbase.env` Gradle property, or
     * `local` when it is unset. An input, so switching stacks re-generates.
     */
    @get:Input
    abstract val environment: Property<String>
```
  şununla değiştir:
```kotlin
    /**
     * The environment this variant compiles, as [EnvironmentResolver] chose it
     * for the variant's build type. An input, so switching stacks re-generates.
     * Absent exactly when [environmentRefusal] is set.
     */
    @get:Input
    @get:Optional
    abstract val environment: Property<String>

    /** Where [environment] came from, in words: printed with it, and named when it is refused. */
    @get:Internal
    abstract val environmentOrigin: Property<String>

    /**
     * Why no environment could be chosen for this variant. The task refuses with
     * it when it RUNS — once a checkout is linked — so an unresolvable `release`
     * never fails `assembleDebug`, or an IDE sync.
     */
    @get:Input
    @get:Optional
    abstract val environmentRefusal: Property<String>

    @get:Internal
    abstract val variantName: Property<String>
```
  `generate()` içinde kökün okunduğu yerden dizin kontrolünün sonuna kadar — şu bloğu:
```kotlin
        val root = environmentsDir.get().asFile
        if (!root.isDirectory) return

        val selected = environment.get()
        val environmentDirectory = root.resolve(selected)
        if (!environmentDirectory.isDirectory) {
            // NEVER FALL BACK. A build that asked for `staging` and quietly
            // compiled `local` ships a client pointed at the wrong stack, with
            // the wrong publishable key, and nothing anywhere says so.
            val known = root.listFiles()
                ?.filter { it.isDirectory }
                ?.map { it.name }
                ?.sorted()
                .orEmpty()
            val carries = if (known.isEmpty()) {
                "this checkout carries no environment at all"
            } else {
                "this checkout carries ${known.joinToString(", ")}"
            }
            throw GradleException(
                "Palbase: environment `$selected` has no directory — $environmentDirectory does not exist, " +
                    "and $carries. Run `palbase link` to write it, or select an existing one with " +
                    "`-Ppalbase.env=<environment>`. Nothing is generated from an environment that was not asked for.",
            )
        }
```
  şununla değiştir (dizin kontrolü bu görevde hâlâ `isDirectory`; T006 birebir ada çevirir):
```kotlin
        val root = environmentsDir.get().asFile
        if (!root.isDirectory) return
        val known = root.listFiles()
            ?.filter { it.isDirectory }
            ?.map { it.name }
            ?.sorted()
            .orEmpty()
        val carries = if (known.isEmpty()) {
            "this checkout carries no environment at all"
        } else {
            "this checkout carries ${known.joinToString(", ")}"
        }

        val selected = environment.orNull
            ?: throw GradleException("${environmentRefusal.get()} ${carries.replaceFirstChar { it.uppercase() }}.")
        val origin = environmentOrigin.get()
        val environmentDirectory = root.resolve(selected)
        if (!environmentDirectory.isDirectory) {
            // NEVER FALL BACK. A build that asked for `staging` and quietly
            // compiled `local` ships a client pointed at the wrong stack, with
            // the wrong publishable key, and nothing anywhere says so.
            throw GradleException(
                "Palbase: environment `$selected` ($origin) has no directory — $environmentDirectory " +
                    "does not exist, and $carries. Run `palbase link` to write it, or choose one this checkout " +
                    "carries for `${variantName.get()}`: `-Ppalbase.env.${variantName.get()}=<environment>` for " +
                    "one build, `palbase.env.<build type>=<environment>` in local.properties or gradle.properties, " +
                    "or `palbase { environment = \"<environment>\" }` in the build type. Nothing is generated from " +
                    "an environment that was not asked for.",
            )
        }
        logger.lifecycle("Palbase: ${variantName.get()} → $selected ($origin)")
```
  ve `validateConfig`'in KDoc'unda:
```kotlin
     * document carries one environment's slot, and `palbase.env` chooses which
     * directory is read.
```
  →
```kotlin
     * document carries one environment's slot, and the variant's build type
     * chooses which directory is read.
```
  (c) `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt` dosyasının tamamı:
```kotlin
package io.palbase.gradle

import java.util.concurrent.atomic.AtomicBoolean
import org.gradle.api.Plugin
import org.gradle.api.Project

/** Android variant integration for generated Kotlin and packaged runtime config. */
class PalbaseCodegenPlugin : Plugin<Project> {
    override fun apply(project: Project) {
        val extension = project.extensions.create("palbase", PalbaseExtension::class.java).apply {
            // ONE PLACE PER ENVIRONMENT. `palbase link` writes every environment it
            // resolved under `palbase/environments/<env>/` — that environment's
            // contract, its roles and this platform's config, together — and
            // everything under `palbase/` is committed.
            environmentsDir.convention(project.layout.projectDirectory.dir("palbase/environments"))
            packageName.convention("io.palbase.generated")
        }
        // WHICH ONE a variant compiles is decided per VARIANT, from its build
        // type — see EnvironmentResolver. There is no build-wide answer any more:
        // one global property could not aim `debug` and `release` at different
        // stacks, and it let a release compile `local` without a word.
        val configured = AtomicBoolean(false)
        val configure = {
            if (configured.compareAndSet(false, true)) {
                AndroidVariantIntegration.configure(project, extension)
            }
        }
        project.pluginManager.withPlugin("com.android.application") { configure() }
        project.pluginManager.withPlugin("com.android.library") { configure() }
    }
}
```
  (d) `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt` dosyasının tamamı:
```kotlin
package io.palbase.gradle

import org.gradle.api.file.DirectoryProperty
import org.gradle.api.provider.Property

/** Inputs written by `palbase link`. */
abstract class PalbaseExtension {
    /**
     * The root the per-environment directories sit in — `palbase/environments` in
     * the module that carries them.
     *
     * Configurable for the one shape the convention cannot reach: a checkout that
     * keeps `palbase/` beside the app module rather than inside it. The
     * environment WITHIN that root is never configured here — each variant
     * resolves its own, from its build type; see [EnvironmentResolver].
     */
    abstract val environmentsDir: DirectoryProperty

    abstract val packageName: Property<String>
}
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: `BUILD SUCCESSFUL`; `EnvironmentResolverTest` `tests="26" … failures="0"`, `PalbaseCodegenPluginTest` `tests="39" skipped="0" failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen)!: her variant kendi ortamını seçer — release varsayılanla derlenmez, debug local kalır, 2.3'ün palbase.env'i bozulmaz"`

---

### T003: SDK reposunun kendi release kapısı ortamını açıkça seçer
<!-- deps: [T002] | files: [gradle.properties] | satisfies: [FR-213] -->

T002'den sonra bu reponun README kapısı (`./gradlew check lintRelease :consumer-release:assembleRelease --configuration-cache`, README.md:212) kırılır: `:consumer-release` `sample/palbase/environments`'e karşı derlenir, orada yalnız `local` var ve release artık varsayılan almaz (`reports/verification-2026-09-25.md` E2). Kapı doğduğu gün kırmızı kalmasın diye düzeltme onu kıran görevin hemen arkasında. `sample`'ın `local`'i https (`https://abc12345m.dev.palbase.studio`) — FR-206'nın ileride getireceği "debuggable olmayan variant'ta loopback/http ret" kuralına takılmaz.

**Interfaces:**
- Consumes: T002'nin release reddi
- Produces: kök `gradle.properties`'te `palbase.env.release=local` (bu reponun `:sample` ve `:consumer-release` modüllerinin release'i)

- [ ] **Adım 1: Kırmızı testi yaz** — Yeni test kodu yok: kapı bu reponun kendi release derlemesi. Kırmızıyı T002'nin bıraktığı ağaçta gör.
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run (repo kökünde): `./gradlew :consumer-release:generatePalbaseRelease --offline` · Beklenen: **FAIL**, `> Task :consumer-release:generatePalbaseRelease FAILED` ve ``> Palbase: `release` has no environment, and a release never gets one by default — it would ship a client aimed at a stack nobody chose. …``
- [ ] **Adım 3: Uygula** — Kök `gradle.properties` sonuna ekle:
```properties
# This checkout's own apps (`:sample`, and `:consumer-release`, the release gate
# the README runs) compile the sample's `local` stack in RELEASE too. The plugin
# no longer picks a release environment by default, so the choice is written here.
palbase.env.release=local
```
- [ ] **Adım 4: Yeşil** — Run: `./gradlew :consumer-release:generatePalbaseRelease --rerun --offline` · Beklenen: `Palbase: release → local (palbase.env.release in gradle.properties)` ve `BUILD SUCCESSFUL`. `--rerun` şart: repo build cache'i açık (`gradle.properties`: `org.gradle.caching=true`) ve aynı girdiler daha önce üretildiyse görev `FROM-CACHE` gelir, satır basılmaz (ölçüldü: `--rerun`'suz `> Task :consumer-release:generatePalbaseRelease FROM-CACHE`, `7 actionable tasks: 1 from cache, 6 up-to-date`, `Palbase:` satırı yok; `--rerun` ile `7 actionable tasks: 1 executed, 6 up-to-date` ve satır). (Dilimin sonunda, T007'den sonra tam README kapısı ölçüldü: `./gradlew check lintRelease :consumer-release:assembleRelease --configuration-cache --offline` → `BUILD SUCCESSFUL in 28s`, `872 actionable tasks: 324 executed, 216 from cache, 332 up-to-date`.)
- [ ] **Adım 5: Commit** — `git add gradle.properties && git commit -m "build: SDK reposunun kendi release kapısı ortamını açıkça seçer — palbase.env.release=local"`

---

### T004: Build type kendi ortamını yazar — `palbase { environment = … }`
<!-- deps: [T002] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseBuildType.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-201, FR-202] -->

FR-201 adım 4: her build type'a AGP'nin resmî DSL uzantısıyla (`DslExtension.Builder("palbase").extendBuildTypeWith(PalbaseBuildType)`, AGP 8.10.1–9.3.2'de var — verification E5/D2) bir `palbase { environment = "…" }` bloğu; değer `finalizeDsl`'de build type adına göre okunur (bir `benchmarkRelease` variant'ı `release`'in değerini görmek zorunda). Kotlin DSL'de container ELEMANI üzerindeki uzantıya Gradle erişimci üretmediği için `import io.palbase.gradle.palbase` gerekir; Groovy'de gerekmez; `extensions.configure<PalbaseBuildType>` import'suz da çalışır (prototip ölçümü, `reports/prototype-and-review-2026-09-24.md` "DSL question"). DSL ile `gradle.properties` aynı build type için farklı ortam söylerse ret ve iki yer adlandırılır (FR-202'nin DSL↔dosya yarısı; "override" etiketi ve dosya okuması FR-202 dilimiyle). `initWith` değeri kopyalamaz (D-019) — testle sabit. Flavor'daki DSL (`extendProductFlavorWith`, D-004) bu görevin dışında.

**Interfaces:**
- Consumes: `EnvironmentSources.buildTypeDsl` (T001); `sources(...)` ve `configure(...)` (T002)
- Produces:
  - `abstract class PalbaseBuildType { abstract val environment: Property<String> }` (public API)
  - `fun BuildType.palbase(configure: Action<in PalbaseBuildType>)` (public; `import io.palbase.gradle.palbase`)
  - `AndroidVariantIntegration`: `sources(project, rootDirectory, buildTypeDsl: (String) -> String?)`, `DSL_NAME = "palbase"`, `NothingPerVariant : VariantExtension`
  - Köken metni: ``palbase { environment } in the `<B>` build type``
  - Test yardımcıları: `fixture(..., imports: String = "", groovy: Boolean = false)`, `groovyBuildScript(buildTypes)`

- [ ] **Adım 1: Kırmızı testi yaz** — `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:

  (a) `a library module selects per build type too` testinden sonra, `an environment that is not one directory name is refused` testinden önce ekle:
```kotlin
    // THE DSL, KOTLIN, WITH THE IMPORT: binds to the BUILD TYPE — in `debug { }`
    // and in a `create(…) { }` alike — and the project-level block still works.
    @Test
    fun `the Kotlin build type dsl selects per build type`() {
        val project = fixture(
            imports = "import io.palbase.gradle.palbase",
            buildTypes = """
                debug { palbase { environment = "main" } }
                release { palbase { environment = "main" } }
                create("qa") { initWith(getByName("debug")); palbase { environment = "local" } }
            """.trimIndent(),
        )
        project.environment("local")
        project.environment("main")

        val result = project.build("generatePalbaseDebug", "generatePalbaseRelease", "generatePalbaseQa")

        assertCompiled(project, "debug", "main")
        assertCompiled(project, "release", "main")
        assertCompiled(project, "qa", "local")
        assertTrue(
            result.output.contains("Palbase: debug → main (palbase { environment } in the `debug` build type)"),
            result.output,
        )
    }

    // `initWith` copies what the build type WAS, not where it points: a feature
    // build type made from `debug` still compiles its own name (D-019).
    @Test
    fun `initWith does not copy the build type environment`() {
        val project = fixture(
            imports = "import io.palbase.gradle.palbase",
            buildTypes = """
                debug { palbase { environment = "main" } }
                create("featureX") { initWith(getByName("debug")) }
            """.trimIndent(),
        )
        project.environment("main")
        project.environment("featureX")

        project.build("generatePalbaseDebug", "generatePalbaseFeatureX")

        assertCompiled(project, "debug", "main")
        assertCompiled(project, "featureX", "featureX")
    }

    // The import-free spelling reaches the same object.
    @Test
    fun `the Kotlin build type extension is reachable without an import`() {
        val project = fixture(
            buildTypes = """
                release { extensions.configure<io.palbase.gradle.PalbaseBuildType> { environment = "main" } }
                create("qa") { extensions.configure<io.palbase.gradle.PalbaseBuildType> { environment = "main" } }
            """.trimIndent(),
        )
        project.environment("main")

        project.build("generatePalbaseRelease", "generatePalbaseQa")

        assertCompiled(project, "release", "main")
        assertCompiled(project, "qa", "main")
    }

    // Groovy resolves `palbase` against the build type first — no import.
    @Test
    fun `the Groovy build type dsl selects per build type`() {
        val project = fixture(
            groovy = true,
            buildTypes = """
                debug { palbase { environment = 'main' } }
                release { palbase { environment = 'main' } }
                qa { initWith debug; palbase { environment = 'local' } }
            """.trimIndent(),
        )
        project.environment("local")
        project.environment("main")

        project.build("generatePalbaseDebug", "generatePalbaseRelease", "generatePalbaseQa")

        assertCompiled(project, "debug", "main")
        assertCompiled(project, "release", "main")
        assertCompiled(project, "qa", "local")
    }

    // Two COMMITTED places that disagree: refused, both named.
    @Test
    fun `a build type dsl that disagrees with gradle properties is refused`() {
        val project = fixture(
            imports = "import io.palbase.gradle.palbase",
            buildTypes = """release { palbase { environment = "main" } }""",
        )
        project.environment("main")
        project.environment("prod")
        project.root.resolve("gradle.properties").write("palbase.env.release=prod\n")

        val failure = project.buildAndFail("generatePalbaseRelease").output

        assertTrue(failure.contains("the `release` build type names two environments in two committed places"), failure)
        assertTrue(failure.contains("palbase { environment = \"main\" }"), failure)
        assertTrue(failure.contains("palbase.env.release=prod"), failure)
        assertFalse(project.hasAsset("release"), "a build that picked either one generated a config")
    }

    // A LIBRARY module gets the same build-type DSL.
    @Test
    fun `a library module reads the build type dsl too`() {
        val project = fixture(
            library = true,
            imports = "import io.palbase.gradle.palbase",
            buildTypes = """release { palbase { environment = "main" } }""",
        )
        project.environment("local")
        project.environment("main")

        project.build("generatePalbaseDebug", "generatePalbaseRelease")

        assertCompiled(project, "debug", "local")
        assertCompiled(project, "release", "main")
    }
```
  (b) `fixture(...)`'in KDoc'unun sonundan build script yazımına kadar — şu bloğu:
```kotlin
     * @param android Kotlin DSL placed inside `android { }`, before `buildTypes`.
     */
    private fun fixture(
        openApi: Boolean = false,
        config: Boolean = false,
        roles: Boolean = false,
        buildTypes: String = "",
        library: Boolean = false,
        android: String = "",
    ): Fixture {
        val root = Files.createTempDirectory(tempDir, "fixture-")
        root.resolve("settings.gradle.kts").write(
            """
            pluginManagement { repositories { google(); mavenCentral(); gradlePluginPortal() } }
            dependencyResolutionManagement {
                repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
                repositories { google(); mavenCentral() }
            }
            rootProject.name = "codegen-fixture"
            """.trimIndent(),
        )
        root.resolve("build.gradle.kts").write(kotlinBuildScript(buildTypes, library, android))
```
  şununla değiştir:
```kotlin
     * @param android Kotlin DSL placed inside `android { }`, before `buildTypes`.
     * @param imports lines placed above `plugins { }`.
     * @param groovy writes `build.gradle` instead, with [buildTypes] in Groovy.
     */
    private fun fixture(
        openApi: Boolean = false,
        config: Boolean = false,
        roles: Boolean = false,
        buildTypes: String = "",
        library: Boolean = false,
        android: String = "",
        imports: String = "",
        groovy: Boolean = false,
    ): Fixture {
        val root = Files.createTempDirectory(tempDir, "fixture-")
        root.resolve("settings.gradle.kts").write(
            """
            pluginManagement { repositories { google(); mavenCentral(); gradlePluginPortal() } }
            dependencyResolutionManagement {
                repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
                repositories { google(); mavenCentral() }
            }
            rootProject.name = "codegen-fixture"
            """.trimIndent(),
        )
        if (groovy) {
            root.resolve("build.gradle").write(groovyBuildScript(buildTypes))
        } else {
            root.resolve("build.gradle.kts").write(imports + "\n" + kotlinBuildScript(buildTypes, library, android))
        }
```
  (c) `kotlinBuildScript(...)`'ten sonra, `private data class Fixture`'dan önce ekle:
```kotlin
    private fun groovyBuildScript(buildTypes: String) = """
        plugins {
            id 'com.android.application'
            id 'org.jetbrains.kotlin.android'
            id 'org.jetbrains.kotlin.plugin.serialization'
            id 'io.palbase.codegen'
        }

        android {
            namespace 'test.fixture'
            compileSdk 36
            defaultConfig { applicationId 'test.fixture'; minSdk 26 }
            compileOptions {
                sourceCompatibility JavaVersion.VERSION_17
                targetCompatibility JavaVersion.VERSION_17
            }
            buildTypes {
    """.trimIndent() + "\n" + buildTypes + "\n" + """
            }
        }

        kotlin {
            compilerOptions {
                jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
            }
        }

        dependencies {
            implementation 'org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3'
        }
    """.trimIndent()
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.*build type dsl*' --tests '*PalbaseCodegenPluginTest.initWith*' --tests '*PalbaseCodegenPluginTest.the Kotlin build type extension*'` · Beklenen: **FAIL**, `6 tests completed, 6 failed`; Kotlin fixture'larında `e: …/build.gradle.kts:1:26: Unresolved reference: palbase`, `extensions.configure` fixture'ında `e: …/build.gradle.kts:19:50: Unresolved reference: PalbaseBuildType`, Groovy'de `> Could not set unknown property 'environment' for extension 'palbase' of type io.palbase.gradle.PalbaseExtension.`
- [ ] **Adım 3: Uygula** —

  (a) `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseBuildType.kt` (yeni dosya):
````kotlin
package io.palbase.gradle

import com.android.build.api.dsl.BuildType
import org.gradle.api.Action
import org.gradle.api.provider.Property

/**
 * The environment ONE build type compiles, committed beside the build type it
 * belongs to:
 *
 * ```kotlin
 * import io.palbase.gradle.palbase
 *
 * android {
 *     buildTypes {
 *         release { palbase { environment = "main" } }
 *         create("staging") { initWith(getByName("debug")); palbase { environment = "qa" } }
 *     }
 * }
 * ```
 *
 * Groovy needs no import: `release { palbase { environment = 'main' } }`.
 *
 * It is one of several places a variant's environment can come from, and not the
 * strongest — see [EnvironmentResolver] for the order. `initWith` does not copy
 * it: a build type made from `debug` still compiles its own name.
 */
abstract class PalbaseBuildType {
    /** A directory name under `palbase/environments/`, exactly as `palbase link` wrote it. */
    abstract val environment: Property<String>
}

/**
 * `palbase { }` INSIDE a build type, for the Kotlin DSL.
 *
 * Gradle generates accessors for project extensions, not for extensions that sit
 * on a container ELEMENT such as `debug`. Without this import a `palbase { }`
 * written inside `debug { }` does not reach the build type at all: it resolves to
 * the PROJECT's `palbase { }` block. With the import, the innermost receiver —
 * the build type — wins.
 *
 * `extensions.configure<PalbaseBuildType> { environment = "…" }` reaches the same
 * object with no import at all.
 */
fun BuildType.palbase(configure: Action<in PalbaseBuildType>) {
    configure.execute(extensions.getByType(PalbaseBuildType::class.java))
}
````
  (b) `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt` dosyasının tamamı (T002'ye göre fark: dört import, `registerExtension` + `finalizeDsl` bloğu, `sources(...)`'in üçüncü parametresi, `NothingPerVariant` ve `DSL_NAME`):
```kotlin
package io.palbase.gradle

import com.android.build.api.dsl.CommonExtension
import com.android.build.api.variant.AndroidComponentsExtension
import com.android.build.api.variant.ApplicationVariant
import com.android.build.api.variant.DslExtension
import com.android.build.api.variant.VariantExtension
import java.io.StringReader
import java.util.Properties
import org.gradle.api.Project
import org.gradle.api.file.Directory

/** Kept out of the plugin entry class so AGP types load only after Android is applied. */
internal object AndroidVariantIntegration {
    fun configure(project: Project, extension: PalbaseExtension) {
        @Suppress("UNCHECKED_CAST")
        val androidComponents = project.extensions.getByType(AndroidComponentsExtension::class.java)
            as AndroidComponentsExtension<*, *, *>

        // `palbase { environment = "…" }` on every build type, the ones AGP made
        // (debug, release) and every one a script or another plugin adds later.
        // The variant half of this API has nothing to carry: the value is read
        // from the finished DSL below, because a `benchmarkRelease` variant needs
        // the RELEASE build type's value, which its own variant cannot see.
        androidComponents.registerExtension(
            DslExtension.Builder(DSL_NAME).extendBuildTypeWith(PalbaseBuildType::class.java).build(),
        ) { NothingPerVariant }

        val declared = mutableMapOf<String, String>()
        androidComponents.finalizeDsl { dsl ->
            (dsl as CommonExtension<*, *, *, *, *, *>).buildTypes.forEach { buildType ->
                buildType.extensions.getByType(PalbaseBuildType::class.java).environment.orNull
                    ?.let { declared[buildType.name] = it }
            }
        }

        val rootDirectory = project.layout.projectDirectory.dir(project.rootDir.absolutePath)
        val resolver = EnvironmentResolver(sources(project, rootDirectory, declared::get))

        androidComponents.onVariants(androidComponents.selector().all()) { variant ->
            val capitalized = variant.name.replaceFirstChar { it.uppercase() }
            val resolution = resolver.resolve(variant.name, variant.buildType ?: variant.name)
            val task = project.tasks.register(
                "generatePalbase$capitalized",
                GeneratePalbaseTask::class.java,
            ) { generate ->
                generate.group = "palbase"
                generate.description = "Generates Palbase Kotlin APIs and config for ${variant.name}"
                generate.variantName.set(variant.name)
                when (resolution) {
                    is EnvironmentResolution.Selected -> {
                        generate.environment.set(resolution.environment)
                        generate.environmentOrigin.set(resolution.origin)
                    }
                    // Refused when the task RUNS, not here: a release nobody chose a
                    // stack for must not take `assembleDebug` — or an IDE sync — down
                    // with it.
                    is EnvironmentResolution.Refused -> generate.environmentRefusal.set(resolution.reason)
                }
                generate.environmentsDir.set(extension.environmentsDir)
                // EVERY environment, not just the selected one — see the property's
                // own note in GeneratePalbaseTask: the SET is part of the input.
                generate.environmentFiles.from(extension.environmentsDir)
                generate.packageName.set(extension.packageName)
                if (variant is ApplicationVariant) {
                    generate.applicationId.set(variant.applicationId)
                }
                generate.kotlinOutput.set(
                    project.layout.buildDirectory.dir("generated/palbase/${variant.name}/kotlin"),
                )
                generate.assetOutput.set(
                    project.layout.buildDirectory.dir("generated/palbase/${variant.name}/assets"),
                )
                generate.resOutput.set(
                    project.layout.buildDirectory.dir("generated/palbase/${variant.name}/res"),
                )
                generate.manifestOutput.set(
                    project.layout.buildDirectory.file("generated/palbase/${variant.name}/AndroidManifest.xml"),
                )
            }

            // External Kotlin Android plugins consume Android's Java source model.
            // Wiring here keeps the plugin compatible before AGP's built-in Kotlin mode too.
            variant.sources.java?.addGeneratedSourceDirectory(task, GeneratePalbaseTask::kotlinOutput)
            variant.sources.assets?.addGeneratedSourceDirectory(task, GeneratePalbaseTask::assetOutput)
            // `res` carries the cleartext allowance for a local stack. AGP 8.10's
            // Sources.getRes() is a Layered source set, same shape as assets above.
            variant.sources.res?.addGeneratedSourceDirectory(task, GeneratePalbaseTask::resOutput)
            variant.sources.manifests.addGeneratedManifestFile(task, GeneratePalbaseTask::manifestOutput)
        }
    }

    /**
     * Every place [EnvironmentResolver] reads, each through an API the
     * configuration cache fingerprints:
     *
     * - `-P` values: Gradle keys a cache entry on the whole set of command-line
     *   properties ("the set of Gradle properties has changed"), so the plain
     *   start-parameter map is safe — and it is the only thing that tells a
     *   command-line value apart from the same key in `gradle.properties`.
     * - `local.properties`: `providers.fileContents`, so editing the file — or
     *   creating it — invalidates the entry. (AGP 8.x happens to fingerprint the
     *   same file for `sdk.dir`; this read does not lean on that.)
     * - `gradle.properties` and `palbase.env`: `providers.gradleProperty`.
     * - The build-type DSL is the build script itself.
     */
    private fun sources(
        project: Project,
        rootDirectory: Directory,
        buildTypeDsl: (String) -> String?,
    ): EnvironmentSources {
        val commandLine = project.gradle.startParameter.projectProperties
        val localProperties by lazy {
            val text = project.providers.fileContents(rootDirectory.file(LOCAL_PROPERTIES)).asText.orNull
            Properties().apply { if (text != null) load(StringReader(text)) }
        }
        return EnvironmentSources(
            commandLine = { key -> commandLine[key] },
            localProperties = { key -> localProperties.getProperty(key) },
            buildTypeDsl = buildTypeDsl,
            gradleProperties = { key -> project.providers.gradleProperty(key).orNull },
            legacy = { project.providers.gradleProperty(EnvironmentResolver.LEGACY_PROPERTY).orNull },
        )
    }

    /** `registerExtension` insists on a per-variant object; this plugin keeps nothing there. */
    private object NothingPerVariant : VariantExtension

    private const val DSL_NAME = "palbase"
    private const val LOCAL_PROPERTIES = "local.properties"
}
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: `BUILD SUCCESSFUL`; `EnvironmentResolverTest` `tests="26" … failures="0"`, `PalbaseCodegenPluginTest` `tests="45" skipped="0" failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseBuildType.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): build type kendi ortamını yazar — palbase { environment = … }, Kotlin'de import ile, Groovy'de importsuz"`

---

### T005: İmportsuz `palbase { environment }` derlenmez ve çareyi söyler
<!-- deps: [T004] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseBuildType.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-209] -->

`trap.patch` (D-018). Kotlin DSL'de import unutulursa build type içindeki `palbase { }` PROJE eklentisine bağlanır (verification C6, fx/c6a: `debug { palbase { packageName.set(…) } }` import'suz derlenip HER variant'a uygulandı). Bugünkü hata `Function invocation 'environment(...)' expected` + exec-task aday listesi — çareyi söylemiyor. Proje eklentisine `@Deprecated(level = ERROR) var environment` konur: Kotlin'de derleme hatası mesajı bizim metnimiz olur; Groovy Kotlin deprecation'ını görmez, bu yüzden setter (ve getter — null dönen bir getter Groovy okumasını "ayarsız" diye sürdürürdü) `GradleException` atar. Derleyicinin kendi öneki Kotlin sürümüne göre değişir (Gradle 8.11.1: `Using 'environment: String?' is an error.`; AGP 9.1.1/Gradle 9.3.1: `'var environment: String?' is deprecated.`) — testler yalnız bizim metnimize bakar. Import'lu biçim, `extensions.configure` ve Groovy build type DSL'i T004'ün testleriyle yeşil kalır. Kalan açık: import'suz bir build type içindeki `packageName` hâlâ derlenir ve tüm variant'lara uygulanır (C6 RESIDUAL; belgelenir, dokümantasyon dilimi).

**Interfaces:**
- Consumes: `PalbaseBuildType`, `BuildType.palbase(...)` (T004)
- Produces:
  - `PalbaseExtension.environment: String?` — `@Deprecated(ENVIRONMENT_TRAP, level = DeprecationLevel.ERROR)`, getter ve setter `GradleException(ENVIRONMENT_TRAP)` atar
  - `internal const val ENVIRONMENT_TRAP` (metin `Palbase: the environment is chosen PER BUILD TYPE, …` ile başlar, `import io.palbase.gradle.palbase` ve `palbase.env.<buildType>=<env>`'i içerir)
  - Test yardımcısı: companion'da `Path.append(content: String)`

- [ ] **Adım 1: Kırmızı testi yaz** — `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:

  (a) `initWith does not copy the build type environment` testinden sonra, `// The import-free spelling reaches the same object.` yorumundan önce ekle:
```kotlin
    // WITHOUT the import, a `palbase { }` inside a build type is the PROJECT's
    // block — Gradle generates no accessor for an extension on a container
    // element. The script must not compile, and the error must name the fix
    // rather than list exec-task overloads of `environment(…)`. The compiler's
    // own wording differs between Kotlin versions; the guidance is ours.
    @Test
    fun `the Kotlin build type dsl without the import does not compile and names the fix`() {
        val project = fixture(buildTypes = """release { palbase { environment = "main" } }""")
        project.environment("main")

        val failure = project.buildAndFail("generatePalbaseRelease").output

        assertTrue(failure.contains("Script compilation error"), failure)
        assertTrue(failure.contains("the environment is chosen PER BUILD TYPE"), failure)
        assertTrue(failure.contains("import io.palbase.gradle.palbase"), failure)
        assertFalse(project.hasAsset("release"))
    }

    @Test
    fun `the Kotlin project level environment does not compile and names the fix`() {
        val project = fixture()
        project.root.resolve("build.gradle.kts").append("\npalbase { environment = \"main\" }\n")
        project.environment("main")

        val failure = project.buildAndFail("generatePalbaseDebug").output

        assertTrue(failure.contains("Script compilation error"), failure)
        assertTrue(failure.contains("the environment is chosen PER BUILD TYPE"), failure)
    }

    // Groovy ignores Kotlin deprecation, so the same line reaches the setter —
    // which refuses with the same guidance instead of "unknown property".
    @Test
    fun `the Groovy project level environment is refused with guidance`() {
        val project = fixture(groovy = true)
        project.root.resolve("build.gradle").append("\npalbase { environment = 'main' }\n")
        project.environment("main")

        val failure = project.buildAndFail("generatePalbaseDebug").output

        assertTrue(failure.contains("the environment is chosen PER BUILD TYPE"), failure)
        assertTrue(failure.contains("palbase.env.<buildType>=<env>"), failure)
    }
```
  (b) companion'da `fun Path.write(...)`'in hemen altına ekle:
```kotlin
        fun Path.append(content: String) {
            Files.writeString(this, Files.readString(this) + content)
        }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.*level environment*' --tests '*PalbaseCodegenPluginTest.the Kotlin build type dsl without the import*'` · Beklenen: **FAIL**, `3 tests completed, 3 failed`; Kotlin'de `e: …/build.gradle.kts:19:21: Function invocation 'environment(...)' expected` (rehber metin yok), Groovy'de `> Could not set unknown property 'environment' for extension 'palbase' of type io.palbase.gradle.PalbaseExtension.`
- [ ] **Adım 3: Uygula** —

  (a) `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt` dosyasının tamamı:
```kotlin
package io.palbase.gradle

import org.gradle.api.GradleException
import org.gradle.api.file.DirectoryProperty
import org.gradle.api.provider.Property

/** Inputs written by `palbase link`. */
abstract class PalbaseExtension {
    /**
     * The root the per-environment directories sit in — `palbase/environments` in
     * the module that carries them.
     *
     * Configurable for the one shape the convention cannot reach: a checkout that
     * keeps `palbase/` beside the app module rather than inside it. The
     * environment WITHIN that root is never configured here — each variant
     * resolves its own, from its build type; see [EnvironmentResolver].
     */
    abstract val environmentsDir: DirectoryProperty

    abstract val packageName: Property<String>

    /**
     * A TRAP, NOT A SETTING. In the Kotlin DSL a `palbase { }` written inside a
     * build type WITHOUT `import io.palbase.gradle.palbase` binds to THIS
     * (project-level) block. Declaring `environment` here turns that mistake into
     * a compile error that names the fix; Groovy, which ignores Kotlin
     * deprecation, hits the throwing setter instead. The getter throws too: a
     * getter that answered null would let a Groovy read carry on as if unset.
     */
    @Deprecated(ENVIRONMENT_TRAP, level = DeprecationLevel.ERROR)
    var environment: String?
        get() = throw GradleException(ENVIRONMENT_TRAP)
        set(@Suppress("UNUSED_PARAMETER") value) = throw GradleException(ENVIRONMENT_TRAP)
}

internal const val ENVIRONMENT_TRAP =
    "Palbase: the environment is chosen PER BUILD TYPE, not in the project-level `palbase { }` block. " +
        "Write `palbase { environment = \"<env>\" }` INSIDE the build type and, in a .kts script, add " +
        "`import io.palbase.gradle.palbase` at the top of the file (without it, `palbase { }` inside a build " +
        "type is this project-level block) — or set `palbase.env.<buildType>=<env>` in gradle.properties."
```
  (b) `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseBuildType.kt` — `BuildType.palbase`'in KDoc'unda şu satırları:
```kotlin
 * written inside `debug { }` does not reach the build type at all: it resolves to
 * the PROJECT's `palbase { }` block. With the import, the innermost receiver —
 * the build type — wins.
```
  şununla değiştir:
```kotlin
 * written inside `debug { }` does not reach the build type at all: it resolves to
 * the PROJECT's `palbase { }` block, where `environment` is a trap that stops
 * the script compiling (see [PalbaseExtension.environment]) — but a project-level
 * setting such as `packageName` written there WOULD compile, and apply to every
 * variant. With the import, the innermost receiver — the build type — wins, and
 * that mistake stops compiling too.
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: `BUILD SUCCESSFUL`; `PalbaseCodegenPluginTest` `tests="48" skipped="0" failures="0" errors="0"`, `EnvironmentResolverTest` `tests="26" … failures="0"`. Testin gördüğü satırlar: Kotlin ``e: …/build.gradle.kts:19:21: Using 'environment: String?' is an error. Palbase: the environment is chosen PER BUILD TYPE, not in the project-level `palbase { }` block. …``; Groovy ``> Palbase: the environment is chosen PER BUILD TYPE, not in the project-level `palbase { }` block. Write `palbase { environment = "<env>" }` INSIDE the build type and, in a .kts script, add `import io.palbase.gradle.palbase` at the top of the file … — or set `palbase.env.<buildType>=<env>` in gradle.properties.``
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseBuildType.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): importsuz palbase { environment } derlenmiyor ve çareyi söylüyor — Kotlin'de ERROR deprecation, Groovy'de GradleException"`

---

### T006: Ortam dizini ADIYLA bulunur — harf büyüklüğü ikizi adlandırılır
<!-- deps: [T002] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-204] -->

FR-204'ün ikinci yarısı. 2.3'ün `root.resolve(selected).isDirectory` kontrolü (`GeneratePalbaseTask.kt:98-99` @e72f704) APFS'te `Main` için `main/`'i buluyor: Mac'te yeşil, Linux CI'da kırmızı — ve adlandırılandan başka bir dizini derliyor (verification D5; 2.3.0 ile `-Ppalbase.env=featureX` + `featurex/` → `EXIT=0`, APK `https://featurex.envprobe.palbase.studio`). Prototip birebir eşleşmeyi getirdi ama mesajı "does not exist" diyordu — macOS'ta düpedüz yanlış, ve farkın yalnız harf büyüklüğü olduğunu söylemiyordu. Artık dizin listesinde birebir ad aranır; yalnız harf büyüklüğü farklı bir ikiz varsa hata ikizi adlandırır ve eşleme satırını (`palbase.env.<build type>=<ikiz>`) söyler. Bunun için görev build type adını da taşır.

**Interfaces:**
- Consumes: `GeneratePalbaseTask.environment/environmentOrigin/variantName` (T002), `EnvironmentResolver.PROPERTY_PREFIX` (T001)
- Produces:
  - `GeneratePalbaseTask.buildTypeName: Property<String>` (`@Internal`), `AndroidVariantIntegration` onu `variant.buildType ?: variant.name` ile doldurur
  - İkiz mesajı: ``Palbase: environment `<seçilen>` (<köken>) has no directory — `<ikiz>` differs from it only in letter case. … Name it exactly `<ikiz>` where it is set, or map the build type to it: `palbase.env.<B>=<ikiz>`.``

- [ ] **Adım 1: Kırmızı testi yaz** — `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`: `a library module reads the build type dsl too` testinden sonra, `an environment that is not one directory name is refused` testinden önce ekle:
```kotlin
    // THE NAME, EXACTLY: `Main` is not `main`, even on a file system that says it
    // is. On macOS the 2.3 check (`isDirectory`) built this and compiled `main/`;
    // a Linux CI refused the same commit. The refusal names the twin and the line
    // that maps to it.
    @Test
    fun `an environment is matched by its exact name`() {
        val project = fixture()
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=Main\n")

        val failure = project.buildAndFail("generatePalbaseRelease").output

        assertTrue(failure.contains("environment `Main` (palbase.env.release in gradle.properties) has no directory"), failure)
        assertTrue(failure.contains("`main` differs from it only in letter case"), failure)
        assertTrue(failure.contains("`palbase.env.release=main`"), failure)
        assertFalse(project.hasAsset("release"), "a case twin was compiled")
    }

    @Test
    fun `a build type whose directory differs only in case names the twin and the mapping line`() {
        val project = fixture(buildTypes = """create("featureX") { initWith(getByName("debug")) }""")
        project.environment("featurex")

        val failure = project.buildAndFail("generatePalbaseFeatureX").output

        assertTrue(failure.contains("environment `featureX` (from the build type name) has no directory"), failure)
        assertTrue(failure.contains("`featurex` differs from it only in letter case"), failure)
        assertTrue(failure.contains("`palbase.env.featureX=featurex`"), failure)
        assertFalse(project.hasAsset("featureX"), "a case twin was compiled")
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.an environment is matched by its exact name' --tests '*PalbaseCodegenPluginTest.a build type whose directory differs only in case*'` · Beklenen (macOS/APFS): **FAIL**, `2 tests completed, 2 failed`; `UnexpectedBuildSuccess … Palbase: release → Main (palbase.env.release in gradle.properties)` ve `Palbase: featureX → featureX (from the build type name)` + `BUILD SUCCESSFUL` — yanlış dizin yeşil derlendi. (Büyük/küçük harf duyarlı bir diskte kırmızı başka görünür — genel "has no directory" reddi — ama ikiz metnini aradığı için test orada da kırmızı.)
- [ ] **Adım 3: Uygula** —

  (a) `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt` — `variantName` özelliğinin hemen altına ekle:
```kotlin
    /** The variant's build type — the key a refusal tells the reader to set. */
    @get:Internal
    abstract val buildTypeName: Property<String>
```
  `generate()` içinde şu bloğu:
```kotlin
        val environmentDirectory = root.resolve(selected)
        if (!environmentDirectory.isDirectory) {
            // NEVER FALL BACK. A build that asked for `staging` and quietly
            // compiled `local` ships a client pointed at the wrong stack, with
            // the wrong publishable key, and nothing anywhere says so.
            throw GradleException(
                "Palbase: environment `$selected` ($origin) has no directory — $environmentDirectory " +
```
  şununla değiştir (bloğun devamı — "has no directory" mesajının geri kalanı ve `logger.lifecycle(...)` — aynen kalır):
```kotlin
        // THE NAME, EXACTLY. A case-insensitive file system (macOS, Windows)
        // answers `isDirectory` for `Main` when only `main` exists, so a check
        // that asked the file system would build here and refuse on a Linux CI
        // — and compile a directory other than the one that was named.
        val environmentDirectory = root.resolve(selected)
        if (selected !in known) {
            known.firstOrNull { it.equals(selected, ignoreCase = true) }?.let { twin ->
                throw GradleException(
                    "Palbase: environment `$selected` ($origin) has no directory — `$twin` differs from it only " +
                        "in letter case. Environment names are case-sensitive: a case-insensitive disk (macOS, " +
                        "Windows) would build this and a Linux CI would not. Name it exactly `$twin` where it is " +
                        "set, or map the build type to it: `${EnvironmentResolver.PROPERTY_PREFIX}" +
                        "${buildTypeName.get()}=$twin`.",
                )
            }
            // NEVER FALL BACK. A build that asked for `staging` and quietly
            // compiled `local` ships a client pointed at the wrong stack, with
            // the wrong publishable key, and nothing anywhere says so.
            throw GradleException(
                "Palbase: environment `$selected` ($origin) has no directory — $environmentDirectory " +
```
  (b) `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt` — şu satırları:
```kotlin
        androidComponents.onVariants(androidComponents.selector().all()) { variant ->
            val capitalized = variant.name.replaceFirstChar { it.uppercase() }
            val resolution = resolver.resolve(variant.name, variant.buildType ?: variant.name)
```
  şununla değiştir:
```kotlin
        androidComponents.onVariants(androidComponents.selector().all()) { variant ->
            val capitalized = variant.name.replaceFirstChar { it.uppercase() }
            val buildType = variant.buildType ?: variant.name
            val resolution = resolver.resolve(variant.name, buildType)
```
  ve `generate.variantName.set(variant.name)` satırının altına `generate.buildTypeName.set(buildType)` ekle.
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: `BUILD SUCCESSFUL`; `PalbaseCodegenPluginTest` `tests="50" skipped="0" failures="0" errors="0"`, `EnvironmentResolverTest` `tests="26" … failures="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): ortam dizini ADIYLA bulunur — yalnız harf büyüklüğü farklı ikiz adlandırılır ve eşleme satırı söylenir"`

---

### T007: `palbase/` bloksuz bulunur — modülün kendisi, sonra checkout kökü
<!-- deps: [T002, T005] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-205] -->

Intake #2 ("`palbase {}` bloğu olmasın"). `palbase link` `palbase/`'u checkout köküne yazar, app bir alt modüldür; 2.3 bu en yaygın düzen için `palbase { environmentsDir.set(rootProject…) }` istiyordu (trial app ve kullanıcının test app'i bugün bu bloğu taşıyor). Açık `environmentsDir` → `<modül>/palbase/environments` → `<rootDir>/palbase/environments` sırasıyla göreve verilir ve görev **çalışırken** var olan ilkini okur — `palbase link` configuration cache kaydından sonra çalışsa da aynı kayıt yeniden kullanılır (prototipte M2 mutasyonu bunu yakaladı). Bu FR-205'in bir kısmı: `<rootDir>/../palbase/environments` (RN/Flutter, D3b), birden fazla aday **varsa** ret (D3a — bugün modülünkü sessizce kazanır) ve arananları söyleyen satır FR-205 dilimine kalır; bu yüzden prototipin "modülün kendi kopyası kökü ezer" testi bilerek taşınmadı. `GeneratePalbaseTask.environmentsDir` kaldırıldı (`environmentRoots`) — görevi doğrudan yapılandıran kod kırılır; CHANGELOG'daki DAVRANIŞ DEĞİŞİKLİĞİ listesine girmeli (dokümantasyon dilimi).

`an explicit environments dir is the only root read` 2.3'te de geçiyordu (açık blok 2.3'ün tek yolu); bekçi olarak eklendi — `:consumer-release` bu yolu kullanıyor.

**Interfaces:**
- Consumes: `Path.append` (T005), `fixture(...)`/`Fixture` (T002/T004)
- Produces:
  - `GeneratePalbaseTask.environmentRoots: ListProperty<Directory>` (`@Internal`; `environmentsDir: DirectoryProperty`'nin yerine — public API kırılması)
  - `PalbaseExtension.environmentsDir` artık konvansiyonsuz (ayarlıysa tek kök)
  - `AndroidVariantIntegration`: `ENVIRONMENTS_PATH = "palbase/environments"`, `environmentRoots` sağlayıcısı (`extension.environmentsDir.map { listOf(it) }.orElse(listOf(modül, kök).distinctBy { it.asFile }.map { it.dir(ENVIRONMENTS_PATH) })`)
  - Test yardımcıları: `fixture(..., module: String? = null)`, `Fixture(root: Path, app: Path = root)`, `Fixture.environment(name, base: Path = app)`; `environmentFile`, `asset`, `hasAsset`, `hasGeneratedFile`, `generatedFile` artık `app`'e göre

- [ ] **Adım 1: Kırmızı testi yaz** — `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:

  (a) `// ---- 2.5: THE BUILD TYPE SELECTS THE ENVIRONMENT` başlık yorumunun (5 satır) hemen altına, `// Nothing picks a release stack by default` yorumundan önce ekle:
```kotlin
    // `palbase link` writes `palbase/` at the checkout root; the app is a module
    // below it. 2.3 needed a `palbase { environmentsDir.set(…) }` block for
    // exactly this layout — the most common one there is.
    @Test
    fun `the checkout root environments are found with no palbase block`() {
        val project = fixture(module = "app")
        project.environment("local", base = project.root)

        val result = project.build("generatePalbaseDebug")

        assertCompiled(project, "debug", "local")
        assertTrue(result.output.contains("Palbase: debug → local (the default for debug)"), result.output)
    }

    // The root is found when the task RUNS. A checkout configured — and cached —
    // before `palbase link` ever ran picks the directory up once it exists, from
    // the SAME cache entry: nothing about the configuration changed.
    @Test
    fun `an environments root that appears after a cached configuration is read`() {
        val project = fixture(module = "app")
        val first = project.build("generatePalbaseDebug", "--configuration-cache")
        assertEquals(TaskOutcome.SUCCESS, first.task(":app:generatePalbaseDebug")?.outcome)
        assertTrue(first.output.contains("Configuration cache entry stored"), first.output)
        assertFalse(project.hasAsset("debug"))

        project.environment("local", base = project.root)
        val second = project.build("generatePalbaseDebug", "--configuration-cache")

        assertTrue(second.output.contains("Configuration cache entry reused"), second.output)
        assertCompiled(project, "debug", "local")
    }

    // A block that NAMES the root is the only root read — the layout neither
    // convention reaches keeps working exactly as in 2.3.
    @Test
    fun `an explicit environments dir is the only root read`() {
        val project = fixture(module = "app")
        project.environment("local", base = project.root)
        val shared = project.root.resolve("shared")
        shared.resolve("palbase/environments/local/openapi.json").write(OPEN_API)
        shared.resolve("palbase/environments/local/android-config.json").write(configFor("shared1234m"))
        project.app.resolve("build.gradle.kts").append(
            "\npalbase { environmentsDir.set(rootProject.layout.projectDirectory.dir(\"shared/palbase/environments\")) }\n",
        )

        project.build("generatePalbaseDebug")

        assertTrue(project.asset("debug").contains("shared1234m"), project.asset("debug"))
    }
```
  (b) `fixture(...)`'in KDoc'undan `GeneratedUsage.kt` yazımına kadar — şu bloğu:
```kotlin
    /**
     * @param buildTypes Kotlin DSL placed inside `android { buildTypes { … } }`.
     * @param library applies `com.android.library` instead of the application plugin.
     * @param android Kotlin DSL placed inside `android { }`, before `buildTypes`.
     * @param imports lines placed above `plugins { }`.
     * @param groovy writes `build.gradle` instead, with [buildTypes] in Groovy.
     */
    private fun fixture(
        openApi: Boolean = false,
        config: Boolean = false,
        roles: Boolean = false,
        buildTypes: String = "",
        library: Boolean = false,
        android: String = "",
        imports: String = "",
        groovy: Boolean = false,
    ): Fixture {
        val root = Files.createTempDirectory(tempDir, "fixture-")
        root.resolve("settings.gradle.kts").write(
            """
            pluginManagement { repositories { google(); mavenCentral(); gradlePluginPortal() } }
            dependencyResolutionManagement {
                repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
                repositories { google(); mavenCentral() }
            }
            rootProject.name = "codegen-fixture"
            """.trimIndent(),
        )
        if (groovy) {
            root.resolve("build.gradle").write(groovyBuildScript(buildTypes))
        } else {
            root.resolve("build.gradle.kts").write(imports + "\n" + kotlinBuildScript(buildTypes, library, android))
        }
        root.resolve("src/main/AndroidManifest.xml").write("<manifest />")
        root.resolve("src/main/kotlin/io/palbase/PalbaseClient.kt").write(PALBASE_STUB)
        root.resolve("src/main/kotlin/io/palbase/backend/GeneratedStubs.kt").write(BACKEND_STUB)
        val fixture = Fixture(root)
        if (openApi && config) {
            root.resolve("src/main/kotlin/test/fixture/GeneratedUsage.kt").write(GENERATED_USAGE)
```
  şununla değiştir:
```kotlin
    /**
     * @param module null puts the app in the ROOT project; a name puts it in that
     *   subproject, with the checkout root one level up — the layout `palbase link`
     *   writes into.
     * @param buildTypes Kotlin DSL placed inside `android { buildTypes { … } }`.
     * @param library applies `com.android.library` instead of the application plugin.
     * @param android Kotlin DSL placed inside `android { }`, before `buildTypes`.
     * @param imports lines placed above `plugins { }`.
     * @param groovy writes `build.gradle` instead, with [buildTypes] in Groovy.
     */
    private fun fixture(
        openApi: Boolean = false,
        config: Boolean = false,
        roles: Boolean = false,
        buildTypes: String = "",
        library: Boolean = false,
        android: String = "",
        imports: String = "",
        groovy: Boolean = false,
        module: String? = null,
    ): Fixture {
        val root = Files.createTempDirectory(tempDir, "fixture-")
        root.resolve("settings.gradle.kts").write(
            """
            pluginManagement { repositories { google(); mavenCentral(); gradlePluginPortal() } }
            dependencyResolutionManagement {
                repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
                repositories { google(); mavenCentral() }
            }
            rootProject.name = "codegen-fixture"
            """.trimIndent() + module?.let { "\ninclude(\":$it\")" }.orEmpty(),
        )
        val app = module?.let(root::resolve) ?: root
        if (groovy) {
            app.resolve("build.gradle").write(groovyBuildScript(buildTypes))
        } else {
            app.resolve("build.gradle.kts").write(imports + "\n" + kotlinBuildScript(buildTypes, library, android))
        }
        app.resolve("src/main/AndroidManifest.xml").write("<manifest />")
        app.resolve("src/main/kotlin/io/palbase/PalbaseClient.kt").write(PALBASE_STUB)
        app.resolve("src/main/kotlin/io/palbase/backend/GeneratedStubs.kt").write(BACKEND_STUB)
        val fixture = Fixture(root, app)
        if (openApi && config) {
            app.resolve("src/main/kotlin/test/fixture/GeneratedUsage.kt").write(GENERATED_USAGE)
```
  (c) roller dalında:
```kotlin
            root.resolve("src/main/kotlin/test/fixture/RolesUsage.kt").write(ROLES_USAGE)
```
  →
```kotlin
            app.resolve("src/main/kotlin/test/fixture/RolesUsage.kt").write(ROLES_USAGE)
```
  (d) `private data class Fixture(val root: Path) {` satırından `fun generatedFile(name: String)` satırına kadar — şu bloğu:
```kotlin
    private data class Fixture(val root: Path) {
        fun build(vararg arguments: String) = runner(*arguments).build()
        fun buildAndFail(vararg arguments: String) = runner(*arguments).buildAndFail()

        /** `palbase/environments/<env>/<name>` — the one place an environment's artifacts live. */
        fun environmentFile(environment: String, name: String): Path =
            root.resolve("palbase/environments/$environment/$name")

        /**
         * One complete environment under `palbase/environments/<name>/`, its
         * config naming a stack of its own — `<name>1234m.dev.palbase.studio` — so
         * which environment a variant compiled is readable from its asset.
         */
        fun environment(name: String) {
            environmentFile(name, "openapi.json").write(OPEN_API)
            environmentFile(name, "android-config.json").write(configFor(stackOf(name)))
        }

        /** The runtime config ONE variant packs — the file AGP merges into that variant's APK. */
        private fun assetFile(variant: String): Path {
            val task = "generatePalbase" + variant.replaceFirstChar { it.uppercase() }
            return root.resolve("build/generated/assets/$task/palbase/palbase-config.json")
        }

        fun asset(variant: String): String = Files.readString(assetFile(variant))

        fun hasAsset(variant: String): Boolean = Files.isRegularFile(assetFile(variant))

        fun hasGeneratedFile(name: String): Boolean {
            val generated = root.resolve("build/generated")
            if (!Files.isDirectory(generated)) return false
            return Files.walk(generated).use { files ->
                files.anyMatch { it.fileName.toString() == name && it.toFile().isFile }
            }
        }

        fun generatedFile(name: String) = Files.walk(root.resolve("build/generated")).use { files ->
```
  şununla değiştir:
```kotlin
    /** [root] is the checkout (where the build runs); [app] is the module carrying the Android app. */
    private data class Fixture(val root: Path, val app: Path = root) {
        fun build(vararg arguments: String) = runner(*arguments).build()
        fun buildAndFail(vararg arguments: String) = runner(*arguments).buildAndFail()

        /** `palbase/environments/<env>/<name>` in the APP MODULE — the one place an environment's artifacts live. */
        fun environmentFile(environment: String, name: String): Path =
            app.resolve("palbase/environments/$environment/$name")

        /**
         * One complete environment under `<base>/palbase/environments/<name>/`, its
         * config naming a stack of its own — `<name>1234m.dev.palbase.studio` — so
         * which environment a variant compiled is readable from its asset.
         */
        fun environment(name: String, base: Path = app) {
            base.resolve("palbase/environments/$name/openapi.json").write(OPEN_API)
            base.resolve("palbase/environments/$name/android-config.json").write(configFor(stackOf(name)))
        }

        /** The runtime config ONE variant packs — the file AGP merges into that variant's APK. */
        private fun assetFile(variant: String): Path {
            val task = "generatePalbase" + variant.replaceFirstChar { it.uppercase() }
            return app.resolve("build/generated/assets/$task/palbase/palbase-config.json")
        }

        fun asset(variant: String): String = Files.readString(assetFile(variant))

        fun hasAsset(variant: String): Boolean = Files.isRegularFile(assetFile(variant))

        fun hasGeneratedFile(name: String): Boolean {
            val generated = app.resolve("build/generated")
            if (!Files.isDirectory(generated)) return false
            return Files.walk(generated).use { files ->
                files.anyMatch { it.fileName.toString() == name && it.toFile().isFile }
            }
        }

        fun generatedFile(name: String) = Files.walk(app.resolve("build/generated")).use { files ->
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the checkout root*' --tests '*PalbaseCodegenPluginTest.an environments root that appears*' --tests '*PalbaseCodegenPluginTest.an explicit environments dir*'` · Beklenen: **FAIL**, `3 tests completed, 2 failed`; ikisinde de `java.nio.file.NoSuchFileException: <fixture>/app/build/generated/assets/generatePalbaseDebug/palbase/palbase-config.json` (kök `palbase/` okunmadı, görev sessizce boş üretti); `an explicit environments dir is the only root read` geçer (bekçi).
- [ ] **Adım 3: Uygula** —

  (a) `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt` — import'lara `import org.gradle.api.file.Directory` ve `import org.gradle.api.provider.ListProperty` ekle; şu özelliği:
```kotlin
    /** The root the selected environment's directory is resolved against. Tracked through `environmentFiles`. */
    @get:Internal
    abstract val environmentsDir: DirectoryProperty
```
  şununla değiştir:
```kotlin
    /**
     * Where `palbase/environments` may sit, in priority order; the first that
     * EXISTS when the task runs is read. Tracked through `environmentFiles`.
     */
    @get:Internal
    abstract val environmentRoots: ListProperty<Directory>
```
  `generate()` içinde:
```kotlin
        val root = environmentsDir.get().asFile
        if (!root.isDirectory) return
```
  →
```kotlin
        val root = environmentRoots.get().map { it.asFile }.firstOrNull { it.isDirectory } ?: return
```
  (b) `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt` — `val resolver = EnvironmentResolver(sources(project, rootDirectory, declared::get))` satırının altına (bir boş satırla) ekle:
```kotlin
        // WHERE `palbase/` IS, WITHOUT A BLOCK. `palbase link` writes it at the
        // checkout root; a module may carry its own. Both are handed to the task
        // IN ORDER and the task reads the first that exists when it RUNS, so the
        // directory appearing after a configuration cache entry was stored needs
        // no re-configuration — and nothing here rests on Gradle noticing a
        // directory checked at configuration time.
        val environmentRoots = extension.environmentsDir.map { listOf(it) }.orElse(
            listOf(project.layout.projectDirectory, rootDirectory)
                .distinctBy { it.asFile }
                .map { it.dir(ENVIRONMENTS_PATH) },
        )
```
  görev yapılandırmasında şu satırları:
```kotlin
                generate.environmentsDir.set(extension.environmentsDir)
                // EVERY environment, not just the selected one — see the property's
                // own note in GeneratePalbaseTask: the SET is part of the input.
                generate.environmentFiles.from(extension.environmentsDir)
```
  şununla değiştir:
```kotlin
                generate.environmentRoots.set(environmentRoots)
                // EVERY environment, not just the selected one — see the property's
                // own note in GeneratePalbaseTask: the SET is part of the input.
                generate.environmentFiles.from(environmentRoots)
```
  ve `private const val DSL_NAME = "palbase"` satırının altına `private const val ENVIRONMENTS_PATH = "palbase/environments"` ekle.
  (c) `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt` dosyasının tamamı:
```kotlin
package io.palbase.gradle

import java.util.concurrent.atomic.AtomicBoolean
import org.gradle.api.Plugin
import org.gradle.api.Project

/** Android variant integration for generated Kotlin and packaged runtime config. */
class PalbaseCodegenPlugin : Plugin<Project> {
    override fun apply(project: Project) {
        // ONE PLACE PER ENVIRONMENT. `palbase link` writes every environment it
        // resolved under `palbase/environments/<env>/` — that environment's
        // contract, its roles and this platform's config, together — and
        // everything under `palbase/` is committed. WHERE `palbase/` sits is
        // found, not configured (AndroidVariantIntegration): the block is
        // optional, and most builds never write it.
        val extension = project.extensions.create("palbase", PalbaseExtension::class.java).apply {
            packageName.convention("io.palbase.generated")
        }
        // WHICH ONE a variant compiles is decided per VARIANT, from its build
        // type — see EnvironmentResolver. There is no build-wide answer any more:
        // one global property could not aim `debug` and `release` at different
        // stacks, and it let a release compile `local` without a word.
        val configured = AtomicBoolean(false)
        val configure = {
            if (configured.compareAndSet(false, true)) {
                AndroidVariantIntegration.configure(project, extension)
            }
        }
        project.pluginManager.withPlugin("com.android.application") { configure() }
        project.pluginManager.withPlugin("com.android.library") { configure() }
    }
}
```
  (d) `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt` dosyasının tamamı:
```kotlin
package io.palbase.gradle

import org.gradle.api.GradleException
import org.gradle.api.file.DirectoryProperty
import org.gradle.api.provider.Property

/** Inputs written by `palbase link`. Nothing here is required. */
abstract class PalbaseExtension {
    /**
     * The root the per-environment directories sit in.
     *
     * UNSET, it is FOUND: the first of `<module>/palbase/environments` and
     * `<root project>/palbase/environments` that exists when generation runs —
     * which covers the module that carries its own `palbase/` and the checkout
     * `palbase link` wrote at its root. Set it only for a layout neither reaches.
     *
     * The environment WITHIN that root is never configured here — each variant
     * resolves its own, from its build type; see [EnvironmentResolver].
     */
    abstract val environmentsDir: DirectoryProperty

    abstract val packageName: Property<String>

    /**
     * A TRAP, NOT A SETTING. In the Kotlin DSL a `palbase { }` written inside a
     * build type WITHOUT `import io.palbase.gradle.palbase` binds to THIS
     * (project-level) block. Declaring `environment` here turns that mistake into
     * a compile error that names the fix; Groovy, which ignores Kotlin
     * deprecation, hits the throwing setter instead. The getter throws too: a
     * getter that answered null would let a Groovy read carry on as if unset.
     */
    @Deprecated(ENVIRONMENT_TRAP, level = DeprecationLevel.ERROR)
    var environment: String?
        get() = throw GradleException(ENVIRONMENT_TRAP)
        set(@Suppress("UNUSED_PARAMETER") value) = throw GradleException(ENVIRONMENT_TRAP)
}

internal const val ENVIRONMENT_TRAP =
    "Palbase: the environment is chosen PER BUILD TYPE, not in the project-level `palbase { }` block. " +
        "Write `palbase { environment = \"<env>\" }` INSIDE the build type and, in a .kts script, add " +
        "`import io.palbase.gradle.palbase` at the top of the file (without it, `palbase { }` inside a build " +
        "type is this project-level block) — or set `palbase.env.<buildType>=<env>` in gradle.properties."
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew test check --offline --rerun-tasks` · Beklenen: `BUILD SUCCESSFUL` (`:validatePlugins` dahil); `EnvironmentResolverTest` `tests="26"`, `PalbaseCodegenPluginTest` `tests="53"`, codegen-engine 9 sınıf toplam 47 (upstream'in `ClassifiedErrorsEmitTest`'i dahil) — hepsi `failures="0" errors="0"`. Repo kökünde README kapısı: `./gradlew check lintRelease :consumer-release:assembleRelease --configuration-cache --offline` → `BUILD SUCCESSFUL in 1m 4s`, `872 actionable tasks`.
- [ ] **Adım 5: Tüketici kanıtı (repo dışında; hedef son durum)** — Spec'in "Hedef son durum"unu küçük bir app'te APK içeriğiyle ölç. Scratch'te bir dizin (`target-end-state/`, içinde `git init` — checkout kökü), Gradle 8.13 wrapper'ı (trial app'inkinin kopyası), `palbase/project.json` ve `palbase/environments/{main, featureX, feature-profile-update}/` — her birinde trial app'in `openapi.json`'u ve kendi yığınını adlandıran bir config (`main` → `https://8bbwb2pbm.palbase.studio`, `featureX` → `https://featurexref.palbase.studio`, `feature-profile-update` → `https://fpuref.palbase.studio`); örnek, `featureX`:
```json
{
  "app_id": "project",
  "base_url": "https://featurexref.palbase.studio",
  "api_key": "pb_project_c0123456789abcdefghijKLMN"
}
```
  `settings.gradle.kts` (eklenti çalışma ağacından, palbe 2.3.0 dosya maven reposundan — runtime 2.4.0'da değişti (upstream, `BackendError.envelope`), ama bu sözleşme `x-palbase-errors` beyan etmiyor: üretilen istemci 2.4.0'ın eklediği hiçbir şeye dokunmaz; plugin ile kütüphanenin AYNI sürüm kuralı yalnız bu provada esnetilir):
```kotlin
pluginManagement {
    // The plugin under test, straight from the scratch clone's working tree.
    includeBuild("../../plugin/codegen-gradle")
    repositories { google(); mavenCentral(); gradlePluginPortal() }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
        // palbe 2.3.0 — this contract declares no x-palbase-errors, so the client needs nothing 2.4.0 added.
        maven {
            url = uri("file:///Users/erkutbas/Github_Pallasite/palbackend-android")
            content { includeGroup("io.palbase") }
        }
    }
}

rootProject.name = "target-end-state"
include(":app")
```
  `build.gradle.kts`:
```kotlin
plugins {
    id("com.android.application") version "8.11.1" apply false
    id("org.jetbrains.kotlin.android") version "2.2.21" apply false
    id("org.jetbrains.kotlin.plugin.serialization") version "2.2.21" apply false
}
```
  `gradle.properties`:
```properties
org.gradle.jvmargs=-Xmx2048m -Dfile.encoding=UTF-8
android.useAndroidX=true
kotlin.code.style=official
palbase.env.debug=main
palbase.env.release=main
```
  `app/build.gradle.kts` (proje düzeyinde `palbase {}` bloğu YOK):
```kotlin
import io.palbase.gradle.palbase

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.serialization")
    id("io.palbase.codegen")
}

android {
    namespace = "studio.palbase.proof"
    compileSdk = 36

    defaultConfig {
        applicationId = "studio.palbase.proof"
        minSdk = 26
        targetSdk = 36
        versionCode = 1
        versionName = "0.1"
    }

    buildTypes {
        create("featureX") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
        }
        create("featureProfileUpdate") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
            palbase { environment = "feature-profile-update" }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}

dependencies {
    implementation("io.palbase:palbe:2.3.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
}
```
  `app/src/main/AndroidManifest.xml`:
```xml
<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <application android:name=".ProofApp" android:label="proof" />
</manifest>
```
  `app/src/main/kotlin/studio/palbase/proof/ProofApp.kt`:
```kotlin
package studio.palbase.proof

import android.app.Application
import io.palbase.Palbase

class ProofApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Palbase.initialize(this)
    }
}
```
  Run: `./gradlew assembleDebug assembleRelease assembleFeatureX assembleFeatureProfileUpdate --offline --configuration-cache` ve her APK için `unzip -p <apk> assets/palbase/palbase-config.json` · Beklenen (gözlenen, AGP 8.11.1/Gradle 8.13):
  - `Palbase: debug → main (palbase.env.debug in gradle.properties)` · `app-debug.apk: https://8bbwb2pbm.palbase.studio`
  - `Palbase: release → main (palbase.env.release in gradle.properties)` · `app-release-unsigned.apk: https://8bbwb2pbm.palbase.studio`
  - `Palbase: featureX → featureX (from the build type name)` · `app-featureX.apk: https://featurexref.palbase.studio`
  - ``Palbase: featureProfileUpdate → feature-profile-update (palbase { environment } in the `featureProfileUpdate` build type)`` · `app-featureProfileUpdate.apk: https://fpuref.palbase.studio`
  - `BUILD SUCCESSFUL in 29s`, `Configuration cache entry stored.`

  Sonra `local.properties`'e `palbase.env.debug=featureX` yaz, `./gradlew assembleDebug --offline --configuration-cache` · Beklenen: `Palbase: debug → featureX (palbase.env.debug in local.properties)`, `app-debug.apk: https://featurexref.palbase.studio`. Dosyayı sil, aynı komut · Beklenen: `Calculating task graph as configuration cache cannot be reused because properties file …/target-end-state/local.properties has changed.`, `Palbase: debug → main (palbase.env.debug in gradle.properties)`, `app-debug.apk: https://8bbwb2pbm.palbase.studio`.

  Aynı app AGP 9.1.1/Gradle 9.3.1'de (kök `com.android.application` 9.1.1 + serialization 2.2.10, `org.jetbrains.kotlin.android` ve `kotlin {}` bloğu yok — AGP 9'un gömülü Kotlin'i; wrapper kullanıcının test app'inden): aynı dört satır ve aynı dört `base_url`, `BUILD SUCCESSFUL in 28s`. `import io.palbase.gradle.palbase` silinince: `e: …/app/build.gradle.kts:28:23: 'var environment: String?' is deprecated. Palbase: the environment is chosen PER BUILD TYPE, …` + `BUILD FAILED` (satır numarası dosyanın düzenine bağlı: import satırının altındaki boş satır kalınca 28).

  Karşılaştırma (yayımlanmış 2.3.0'a karşı, tabandan bağımsız; 2.5.0 tabanında yeniden koşulmadı) — aynı checkout yayımlanmış **2.3.0** eklentisiyle (dosya maven reposu, kökte `id("io.palbase.codegen") version "2.3.0" apply false`): `e: …/app/build.gradle.kts:1:26: Unresolved reference: palbase`, `BUILD FAILED`; import ve DSL satırı silinince `BUILD SUCCESSFUL` ama dört APK'nın dördünde de `assets/palbase/palbase-config.json` YOK (blok olmadan 2.3 `app/palbase/environments`'e bakıyor, bulamıyor, sessizce boş üretiyor).
- [ ] **Adım 6: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): palbase/ bloksuz bulunur — modülün kendisi, sonra checkout kökü; görev çalışırken okunur"`

---

### T008: `local.properties` yalnız debuggable variant'ın ortamını seçer — release'te satır yok sayılır ve bunu söyler
<!-- deps: [T001, T002, T006] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-201] -->

FR-201 adım 3 ve D-009. Prototipte `local.properties` her variant için DSL'in ve `gradle.properties`'in önündeydi; unutulmuş bir `palbase.env.release=local` satırı yeşil bir release APK'sı üretti — asset `base_url = http://127.0.0.1:54321`, manifest'te cleartext network-security-config (`reports/verification-2026-09-25.md` C4, fx/c4; proto `EnvironmentResolver.kt:55-57`). Kapı AGP'nin kendi cevabı `variant.debuggable`'dır (C4'ün javap'ı: `Component.getDebuggable()` 8.10.1 ve 9.1.1'de var) — `initWith(debug)` ile yapılmış bir feature build type'ı sayılır (D-019), `benchmarkRelease` sayılmaz. Debuggable olmayan variant'ta satır okunur, **sayılmaz** ve adıyla uyarılır; uyarı görevin `@Input`'udur (`environmentWarnings`), yoksa release derlendikten SONRA eklenen satır — olağan sıra — hiçbir girdiyi değiştirmez ve görev UP-TO-DATE'te sessiz kalırdı (bunu `a local properties line added after a release was built is still warned about` sabitler). Sınırı ölçüldü: satır değişmeden tekrar koşan build'de görev UP-TO-DATE'tir ve uyarı yeniden basılmaz (tüketici A1-again: `Configuration cache entry reused`, üç üretim görevi `UP-TO-DATE`) — uyarı satırın eklendiği ya da değiştiği build'de basılır.

Çözücü yapısal olarak değişir: tek variant'ın yürüyüşü bir `inner class Choice`'a taşınır, çünkü okunup sayılmayan yerleri (`warnings`) biriktiren bir durum gerekir ve `benchmark<X>` ikiz yürüyüşü (adım 6) aynı `Choice`'ı — dolayısıyla asıl variant'ın debuggable'ını ve adını — kullanmalıdır: uyarı ölçülen `release`'i değil, derlenen `benchmarkRelease`'i adlandırır.

**Bilinçli değişen mevcut testler (EnvironmentResolverTest):** `local properties beat the build type dsl` ve `a personal value outranks a committed conflict` sıralamayı `release` üzerinden gösteriyordu — D-009'un yasakladığı durum; aynı sırayı `debug` ile sabitlemeye döner. `a flavored benchmark variant reads its twin variant key` ikiz anahtarı `local.properties`'ten okuyordu (debuggable olmayan `freeBenchmarkRelease`); aynı ikiz anahtarı komut satırından okur. Yardımcı `resolve(variant, buildType)` artık debuggable'ı AGP'nin varsayılanıyla verir (`buildType == "debug"`); başka bir debuggable build type'ı test açıkça `debuggable = true` der.

**Interfaces:**
- Consumes: `EnvironmentResolver.resolve(variant, buildType)`, `EnvironmentSources`, `EnvironmentResolution` (T001); `GeneratePalbaseTask`, `AndroidVariantIntegration.configure` (T002)
- Produces:
  - `EnvironmentResolver.resolve(variant: String, buildType: String, debuggable: Boolean): EnvironmentResolution` (T010 dördüncü parametreyi ekler)
  - `EnvironmentResolution.warnings: List<String>`; `Selected(environment, origin, warnings = emptyList())`, `Refused(reason, warnings = emptyList())`
  - `GeneratePalbaseTask.environmentWarnings: ListProperty<String>` — `@Input`; her biri görev çalışırken `logger.warn` ile basılır
  - Uyarı metni: ``Palbase: `<key>=<value>` in local.properties is ignored for `<variant>`, which is not debuggable — local.properties is personal, and a build that can ship never takes its stack from it. For one build, pass `-P<key>=<value>`; to commit the choice, put the line in gradle.properties.``
  - Test yardımcısı (EnvironmentResolverTest): `private fun EnvironmentResolver.resolve(variant: String, buildType: String)` → `debuggable = buildType == "debug"`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt`:
  (1/4) şu bloğu:
```kotlin
            local = mapOf("palbase.env.release" to "staging"),
            dsl = mapOf("release" to "main"),
        )
        assertSelected("staging", "palbase.env.release in local.properties", resolver.resolve("release", "release"))
```
  şununla değiştir:
```kotlin
            local = mapOf("palbase.env.debug" to "staging"),
            dsl = mapOf("debug" to "main"),
        )
        assertSelected("staging", "palbase.env.debug in local.properties", resolver.resolve("debug", "debug"))
```
  (2/4) şu bloğu:
```kotlin
            local = mapOf("palbase.env.release" to "staging"),
            dsl = mapOf("release" to "main"),
            gradle = mapOf("palbase.env.release" to "prod"),
        )
        assertSelected("staging", "palbase.env.release in local.properties", resolver.resolve("release", "release"))
    }
```
  şununla değiştir:
```kotlin
            local = mapOf("palbase.env.debug" to "staging"),
            dsl = mapOf("debug" to "main"),
            gradle = mapOf("palbase.env.debug" to "prod"),
        )
        assertSelected("staging", "palbase.env.debug in local.properties", resolver.resolve("debug", "debug"))
    }

    // 3 (D-009). local.properties is PERSONAL: it never chooses the stack a
    // build that can ship talks to. A forgotten `palbase.env.release=local` there
    // made a green release APK aimed at loopback, over cleartext. The line is
    // ignored — and said to be, naming it.
    @Test
    fun `local properties do not choose for a variant that is not debuggable`() {
        val resolver = places(
            local = mapOf("palbase.env.release" to "local"),
            gradle = mapOf("palbase.env.release" to "main"),
        )
        val resolution = resolver.resolve("release", "release")

        val selected = resolution as EnvironmentResolution.Selected
        assertEquals("main", selected.environment)
        assertEquals("palbase.env.release in gradle.properties", selected.origin)
        assertEquals(
            listOf(
                "Palbase: `palbase.env.release=local` in local.properties is ignored for `release`, which is not " +
                    "debuggable — local.properties is personal, and a build that can ship never takes its stack " +
                    "from it. For one build, pass `-Ppalbase.env.release=local`; to commit the choice, put the line " +
                    "in gradle.properties.",
            ),
            selected.warnings,
        )
    }

    // …and a release whose ONLY choice sat in local.properties is refused like
    // an unset one, with the warning saying why that line did not count.
    @Test
    fun `a release chosen only in local properties is refused and says why`() {
        val resolution = places(local = mapOf("palbase.env.release" to "local")).resolve("release", "release")

        val reason = assertRefused(resolution)
        assertTrue(reason.contains("`release` has no environment"), reason)
        assertTrue(resolution.warnings.single().contains("in local.properties is ignored"), resolution.warnings.toString())
    }

    // A benchmark build type is made from `release` and is not debuggable: the
    // twin key it reads is gated the same way, and the warning names the
    // variant that was built, not the one it measures.
    @Test
    fun `a benchmark variant does not take its stack from local properties either`() {
        val resolver = places(
            local = mapOf("palbase.env.release" to "local"),
            gradle = mapOf("palbase.env.release" to "main"),
        )
        val selected = resolver.resolve("benchmarkRelease", "benchmarkRelease") as EnvironmentResolution.Selected

        assertEquals("main", selected.environment)
        assertEquals("palbase.env.release in gradle.properties, as `benchmarkRelease` builds as `release`", selected.origin)
        assertTrue(selected.warnings.single().contains("is ignored for `benchmarkRelease`"), selected.warnings.toString())
    }

    // The gate is the variant's DEBUGGABILITY, not the name `debug`: a feature
    // build type made with `initWith(debug)` is debuggable (D-019).
    @Test
    fun `local properties choose for a debuggable build type other than debug`() {
        val resolver = places(local = mapOf("palbase.env.featureX" to "main"))
        assertSelected(
            "main",
            "palbase.env.featureX in local.properties",
            resolver.resolve("featureX", "featureX", debuggable = true),
        )
    }
```
  (3/4) şu bloğu:
```kotlin
        val resolver = places(dsl = mapOf("release" to "main"), local = mapOf("palbase.env.freeRelease" to "free"))
        assertSelected(
            "free",
            "palbase.env.freeRelease in local.properties, as `benchmarkRelease` builds as `release`",
```
  şununla değiştir:
```kotlin
        val resolver = places(dsl = mapOf("release" to "main"), commandLine = mapOf("palbase.env.freeRelease" to "free"))
        assertSelected(
            "free",
            "-Ppalbase.env.freeRelease on the command line, as `benchmarkRelease` builds as `release`",
```
  (4/4) şu satırlardan sonra:
```kotlin
            legacy = { legacy },
        ),
    )
```
  şunu ekle:
```kotlin

    /**
     * AGP's default for a build type nobody configured: only `debug` is
     * debuggable. A test about a debuggable build type other than `debug` —
     * one made with `initWith(debug)` — says so with `debuggable = true`.
     */
    private fun EnvironmentResolver.resolve(variant: String, buildType: String) =
        resolve(variant, buildType, debuggable = buildType == "debug")
```

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertFalse(second.output.contains("Configuration cache entry reused"), second.output)
        assertCompiled(project, "debug", "local")
    }
```
  şunu ekle:
```kotlin

    // D-009: local.properties is PERSONAL — it never chooses for a variant that
    // is not debuggable. A forgotten `palbase.env.release=local` there made a
    // green release APK aimed at loopback. The line is ignored, and SAID to be.
    @Test
    fun `local properties are ignored for a release and the warning names the line`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=main\n")
        project.root.resolve("local.properties").write("palbase.env.release=local\n")

        val result = project.build("generatePalbaseRelease")

        assertCompiled(project, "release", "main")
        assertTrue(result.output.contains("Palbase: release → main (palbase.env.release in gradle.properties)"), result.output)
        assertTrue(
            result.output.contains(
                "`palbase.env.release=local` in local.properties is ignored for `release`, which is not debuggable",
            ),
            result.output,
        )
    }

    // The line is written AFTER the release was built — the usual way it
    // happens. Nothing the answer depends on changed, so a warning that is not
    // an input of the task would never be printed: UP-TO-DATE, in silence.
    @Test
    fun `a local properties line added after a release was built is still warned about`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=main\n")
        project.build("generatePalbaseRelease")

        project.root.resolve("local.properties").write("palbase.env.release=local\n")
        val result = project.build("generatePalbaseRelease")

        assertEquals(TaskOutcome.SUCCESS, result.task(":generatePalbaseRelease")?.outcome)
        assertTrue(result.output.contains("in local.properties is ignored for `release`"), result.output)
        assertCompiled(project, "release", "main")
    }

    // …and a release whose ONLY choice sat there is refused like an unset one,
    // the warning saying why the line did not count.
    @Test
    fun `a release chosen only in local properties is refused and the warning says why`() {
        val project = fixture()
        project.environment("local")
        project.root.resolve("local.properties").write("palbase.env.release=local\n")

        val failure = project.buildAndFail("generatePalbaseRelease").output

        assertTrue(failure.contains("`release` has no environment"), failure)
        assertTrue(failure.contains("`palbase.env.release=local` in local.properties is ignored for `release`"), failure)
        assertFalse(project.hasAsset("release"), "a release took its stack from local.properties")
    }

    // The gate is the variant's DEBUGGABILITY, not the name `debug`: a feature
    // build type made with `initWith(debug)` is debuggable (D-019), and a
    // personal line still picks its stack.
    @Test
    fun `local properties still choose for a debuggable custom build type`() {
        val project = fixture(buildTypes = """create("featureX") { initWith(getByName("debug")) }""")
        project.environment("featureX")
        project.environment("main")
        project.root.resolve("local.properties").write("palbase.env.featureX=main\n")

        val result = project.build("generatePalbaseFeatureX")

        assertCompiled(project, "featureX", "main")
        assertTrue(result.output.contains("Palbase: featureX → main (palbase.env.featureX in local.properties)"), result.output)
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.local properties are ignored*' --tests '*PalbaseCodegenPluginTest.a local properties line added*' --tests '*PalbaseCodegenPluginTest.a release chosen only in local properties*' --tests '*PalbaseCodegenPluginTest.local properties still choose*'` (`JAVA_HOME` = Android Studio JBR, `ANDROID_HOME` ayarlı; `:test` şart — iki nokta olmadan `--tests` `:codegen-engine:test`'e de gider) · Beklenen: **FAIL** (derleme): `> Task :compileTestKotlin FAILED`, `e: …/EnvironmentResolverTest.kt:134:22 Unresolved reference 'warnings'.` ve `e: …/EnvironmentResolverTest.kt:173:54 No parameter with name 'debuggable' found.` (7 `e:` satırı). Davranış kırmızısı — yalnız `PalbaseCodegenPluginTest.kt` değişikliği uygulanıp aynı TestKit filtreleriyle koşunca: `4 tests completed, 3 failed`; `local properties are ignored for a release and the warning names the line() FAILED` → `release compiled {"app_id":"app_android","base_url":"https://local1234m.dev.palbase.studio","api_key":"pb_local1234m_c0123456789abcdefghij"}`; `a release chosen only in local properties is refused …() FAILED` → `UnexpectedBuildSuccess` ve çıktıda `Palbase: release → local (palbase.env.release in local.properties)`; `a local properties line added after a release was built is still warned about() FAILED` (uyarı yok). `local properties still choose for a debuggable custom build type` önceden de geçer (bekçi: kapı adla değil debuggable'la).
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/2) şu bloğu:
```kotlin
            val resolution = resolver.resolve(variant.name, buildType)
```
  şununla değiştir:
```kotlin
            val resolution = resolver.resolve(variant.name, buildType, variant.debuggable)
```
  (2/2) şu satırlardan sonra:
```kotlin
                    is EnvironmentResolution.Refused -> generate.environmentRefusal.set(resolution.reason)
                }
```
  şunu ekle:
```kotlin
                generate.environmentWarnings.set(resolution.warnings)
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt`:
  (1/4) şu bloğu:
```kotlin
 *  3. `palbase.env.<V>`, then `palbase.env.<B>`, in the root `local.properties`.
```
  şununla değiştir:
```kotlin
 *  3. `palbase.env.<V>`, then `palbase.env.<B>`, in the root `local.properties` —
 *     ONLY for a debuggable variant. The file is personal and never committed,
 *     so a line forgotten there must not pick the stack a build that can ship
 *     talks to: a `palbase.env.release=local` left behind made a green release
 *     APK aimed at loopback, over cleartext. For any other variant the line is
 *     ignored, and a warning names it.
```
  (2/4) şu bloğu:
```kotlin
    fun resolve(variant: String, buildType: String): EnvironmentResolution =
        when (val chosen = choose(variant, buildType)) {
            is EnvironmentResolution.Refused -> chosen
            is EnvironmentResolution.Selected -> requireOneSegment(chosen)
        }

    private fun choose(variant: String, buildType: String): EnvironmentResolution {
        val keys = listOf(variant, buildType).distinct().map { PROPERTY_PREFIX + it }

        keys.firstHit(sources.commandLine)?.let { (key, value) ->
            return EnvironmentResolution.Selected(value, "-P$key on the command line")
        }
        sources.commandLine(LEGACY_PROPERTY)?.trim()?.let {
            return EnvironmentResolution.Selected(it, "-P$LEGACY_PROPERTY on the command line")
        }
        keys.firstHit(sources.localProperties)?.let { (key, value) ->
            return EnvironmentResolution.Selected(value, "$key in local.properties")
        }

        val declared = sources.buildTypeDsl(buildType)?.trim()
        val committed = keys.firstHit(sources.gradleProperties)
        if (declared != null) {
            if (committed != null && committed.second != declared) {
                return EnvironmentResolution.Refused(
                    "Palbase: the `$buildType` build type names two environments in two committed places — " +
                        "`palbase { environment = \"$declared\" }` in the build type and " +
                        "`${committed.first}=${committed.second}` in gradle.properties. Keep ONE of them; " +
                        "a build that picked either would leave the other one lying to the next reader.",
                )
            }
            return EnvironmentResolution.Selected(declared, "palbase { environment } in the `$buildType` build type")
        }
        committed?.let { (key, value) ->
            return EnvironmentResolution.Selected(value, "$key in gradle.properties")
        }

        measuredBuildType(buildType)?.let { measured ->
            val because = "`$buildType` builds as `$measured`"
            return when (val twin = choose(twinVariant(variant, buildType, measured), measured)) {
                is EnvironmentResolution.Selected -> twin.copy(origin = "${twin.origin}, as $because")
                is EnvironmentResolution.Refused -> EnvironmentResolution.Refused("${twin.reason} ($because.)")
            }
        }

        sources.legacy()?.trim()?.let {
            return EnvironmentResolution.Selected(it, "$LEGACY_PROPERTY, the 2.3 global property")
        }
        if (buildType != DEBUG && buildType != RELEASE) {
            return EnvironmentResolution.Selected(buildType, BUILD_TYPE_NAME_ORIGIN)
        }
        if (buildType == DEBUG) {
            return EnvironmentResolution.Selected(DEBUG_DEFAULT, "the default for debug")
        }
        val variantClause = if (variant == buildType) "" else " (variant `$variant`)"
        return EnvironmentResolution.Refused(
            "Palbase: `$RELEASE`$variantClause has no environment, and a release never gets one by default — " +
                "it would ship a client aimed at a stack nobody chose. Commit the choice, in ONE of two ways: " +
                "`$PROPERTY_PREFIX$RELEASE=<environment>` in gradle.properties, or " +
                "`buildTypes { release { palbase { environment = \"<environment>\" } } }` in the build script " +
                "(Kotlin DSL: `import io.palbase.gradle.palbase`).",
        )
```
  şununla değiştir:
```kotlin
    /**
     * @param debuggable the variant's own `debuggable` — AGP's answer, so a
     *   feature build type made with `initWith(debug)` counts and a
     *   `benchmarkRelease` does not.
     */
    fun resolve(variant: String, buildType: String, debuggable: Boolean): EnvironmentResolution {
        val choice = Choice(variant, debuggable)
        val resolution = when (val chosen = choice.choose(variant, buildType)) {
            is EnvironmentResolution.Refused -> chosen
            is EnvironmentResolution.Selected -> requireOneSegment(chosen)
        }
        return when (resolution) {
            is EnvironmentResolution.Selected -> resolution.copy(warnings = choice.warnings)
            is EnvironmentResolution.Refused -> resolution.copy(warnings = choice.warnings)
        }
    }

    /**
     * One variant's walk down the places. [subject] is the variant that was
     * asked about, whatever build it measures; [warnings] collects the places it
     * read and did NOT count.
     */
    private inner class Choice(private val subject: String, private val debuggable: Boolean) {
        val warnings = mutableListOf<String>()

        fun choose(variant: String, buildType: String): EnvironmentResolution {
            val keys = listOf(variant, buildType).distinct().map { PROPERTY_PREFIX + it }

            keys.firstHit(sources.commandLine)?.let { (key, value) ->
                return EnvironmentResolution.Selected(value, "-P$key on the command line")
            }
            sources.commandLine(LEGACY_PROPERTY)?.trim()?.let {
                return EnvironmentResolution.Selected(it, "-P$LEGACY_PROPERTY on the command line")
            }
            if (debuggable) {
                keys.firstHit(sources.localProperties)?.let { (key, value) ->
                    return EnvironmentResolution.Selected(value, "$key in local.properties")
                }
            } else {
                keys.forEach { key -> sources.localProperties(key)?.let { warnings += ignoredLocalLine(key, it.trim()) } }
            }

            val declared = sources.buildTypeDsl(buildType)?.trim()
            val committed = keys.firstHit(sources.gradleProperties)
            if (declared != null) {
                if (committed != null && committed.second != declared) {
                    return EnvironmentResolution.Refused(
                        "Palbase: the `$buildType` build type names two environments in two committed places — " +
                            "`palbase { environment = \"$declared\" }` in the build type and " +
                            "`${committed.first}=${committed.second}` in gradle.properties. Keep ONE of them; " +
                            "a build that picked either would leave the other one lying to the next reader.",
                    )
                }
                return EnvironmentResolution.Selected(declared, "palbase { environment } in the `$buildType` build type")
            }
            committed?.let { (key, value) ->
                return EnvironmentResolution.Selected(value, "$key in gradle.properties")
            }

            measuredBuildType(buildType)?.let { measured ->
                val because = "`$buildType` builds as `$measured`"
                return when (val twin = choose(twinVariant(variant, buildType, measured), measured)) {
                    is EnvironmentResolution.Selected -> twin.copy(origin = "${twin.origin}, as $because")
                    is EnvironmentResolution.Refused -> EnvironmentResolution.Refused("${twin.reason} ($because.)")
                }
            }

            sources.legacy()?.trim()?.let {
                return EnvironmentResolution.Selected(it, "$LEGACY_PROPERTY, the 2.3 global property")
            }
            if (buildType != DEBUG && buildType != RELEASE) {
                return EnvironmentResolution.Selected(buildType, BUILD_TYPE_NAME_ORIGIN)
            }
            if (buildType == DEBUG) {
                return EnvironmentResolution.Selected(DEBUG_DEFAULT, "the default for debug")
            }
            val variantClause = if (variant == buildType) "" else " (variant `$variant`)"
            return EnvironmentResolution.Refused(
                "Palbase: `$RELEASE`$variantClause has no environment, and a release never gets one by default — " +
                    "it would ship a client aimed at a stack nobody chose. Commit the choice, in ONE of two ways: " +
                    "`$PROPERTY_PREFIX$RELEASE=<environment>` in gradle.properties, or " +
                    "`buildTypes { release { palbase { environment = \"<environment>\" } } }` in the build script " +
                    "(Kotlin DSL: `import io.palbase.gradle.palbase`).",
            )
        }

        /** The personal file named a stack for a variant it may not choose for: say so, naming the line. */
        private fun ignoredLocalLine(key: String, value: String) =
            "Palbase: `$key=$value` in local.properties is ignored for `$subject`, which is not debuggable — " +
                "local.properties is personal, and a build that can ship never takes its stack from it. For one " +
                "build, pass `-P$key=$value`; to commit the choice, put the line in gradle.properties."
```
  (3/4) şu bloğu:
```kotlin
    /** [origin] says where [environment] came from, in words — it is printed. */
    data class Selected(val environment: String, val origin: String) : EnvironmentResolution

    /** The build must not choose; [reason] is the whole sentence the task refuses with. */
    data class Refused(val reason: String) : EnvironmentResolution
```
  şununla değiştir:
```kotlin
    /** Places that were read and did NOT count, each a sentence the task prints when it runs. */
    val warnings: List<String>

    /** [origin] says where [environment] came from, in words — it is printed. */
    data class Selected(
        val environment: String,
        val origin: String,
        override val warnings: List<String> = emptyList(),
    ) : EnvironmentResolution

    /** The build must not choose; [reason] is the whole sentence the task refuses with. */
    data class Refused(val reason: String, override val warnings: List<String> = emptyList()) : EnvironmentResolution
```
  (4/4) şu bloğu:
```kotlin
    /** The root project's `local.properties`. */
```
  şununla değiştir:
```kotlin
    /** The root project's `local.properties` — it CHOOSES only for a debuggable variant. */
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  (1/2) şu satırlardan sonra:
```kotlin
    abstract val environmentRefusal: Property<String>
```
  şunu ekle:
```kotlin

    /**
     * Places that were read for this variant and did NOT count — a
     * `local.properties` line a variant that is not debuggable ignores — each
     * printed as a warning when the task runs. An INPUT, not internal: the line
     * is usually written after the variant was last built and changes nothing
     * else this task depends on, so an internal warning would never be printed —
     * the task would stay UP-TO-DATE, in silence.
     */
    @get:Input
    abstract val environmentWarnings: ListProperty<String>
```
  (2/2) şu satırlardan sonra:
```kotlin
        writeNetworkSecurityConfig(null)
```
  şunu ekle:
```kotlin
        environmentWarnings.get().forEach { logger.warn(it) }
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.local properties are ignored*' --tests '*PalbaseCodegenPluginTest.a local properties line added*' --tests '*PalbaseCodegenPluginTest.a release chosen only in local properties*' --tests '*PalbaseCodegenPluginTest.local properties still choose*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 9s` (EnvironmentResolverTest `tests="30"`, seçilen 4 TestKit testi `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 17s`, `EnvironmentResolverTest` `tests="30" skipped="0" failures="0" errors="0"`, `PalbaseCodegenPluginTest` `tests="57" skipped="0" failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): local.properties yalnız debuggable variant'ın ortamını seçer — release'te satır yok sayılır, uyarı onu adlandırır ve UP-TO-DATE'in arkasına saklanmaz"`

---

### T009: Commit edilmiş seçim kök `gradle.properties` DOSYASINDAN okunur — başka kaynaktaki Gradle özelliği "override"dır
<!-- deps: [T004, T008] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-201, FR-202] -->

FR-201 adım 5 ve 7, FR-202'nin override cümlesi. Prototip adım 5'i `providers.gradleProperty` ile okuyordu; o `ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*` ve `~/.gradle/gradle.properties`'ten de cevap verir, hepsini "in gradle.properties" diye etiketler ve DSL ile **sahte** bir "two committed places" reddi üretir (`reports/verification-2026-09-25.md` C3, fx/c3a: checkout'un dosyasında hiç anahtar yokken `env 'ORG_GRADLE_PROJECT_palbase.env.release=staging'` → "names two environments in two committed places"; fx/c3b: etiket yanlış). Artık: commit edilen seçim kök `gradle.properties` **dosyasından** `providers.fileContents` ile okunur (configuration-cache girdisi); `providers.gradleProperty`'nin komut satırında olmayan **ve** dosyadakinden farklı değeri bir **override**'dır — komut satırıyla aynı sırada (adım 1 ve 2) sayılır, `<key> override from ORG_GRADLE_PROJECT_*, -Dorg.gradle.project.* or ~/.gradle/gradle.properties` diye basılır ve hiçbir zaman committed-conflict'in yarısı olmaz. Dosyadakiyle aynı değer committed sayılır. Eski `palbase.env` (adım 7) da dosyadan okunur; override olarak gelirse `-Ppalbase.env` gibi adım 2'dir (D-007).

`EnvironmentSources`'tan `legacy` kalkar, `overrides` gelir; `gradleProperties` artık yalnız dosyadır (`palbase.env` dahil). **Bilinçli değişen mevcut test:** `places after the first hit are never read` beklenen okuma sırasını genişletir — her komut satırı anahtarının hemen ardından aynı anahtarın override'ı sorulur; sabitlediği ilke (ilk isabetten sonrası okunmaz) aynı. Test yardımcısı `places(legacy = …)` değeri dosya haritasına `palbase.env` olarak koyar.

TestKit'te iki kaynak ölçülür: `-Dorg.gradle.project.*` ve C3'ün kendi yeniden üretimi `ORG_GRADLE_PROJECT_palbase.env.release=staging` (`GradleRunner.withEnvironment`, bir CI işinin özelliği verdiği yol — `an ORG_GRADLE_PROJECT variable is an override and no committed conflict`; kırmızısı C3'ün sahte reddinin kendisi). `~/.gradle/gradle.properties` ölçülmedi (gerçek Gradle kullanıcı dizinine dokunulmadı) — aynı `providers.gradleProperty` yolundan geçer.

**Interfaces:**
- Consumes: `EnvironmentResolver.resolve(variant, buildType, debuggable)`, `Choice` (T008); `AndroidVariantIntegration.sources(...)` (T002)
- Produces:
  - `EnvironmentSources(commandLine, overrides: (String) -> String?, localProperties, buildTypeDsl, gradleProperties)` — `legacy` KALDIRILDI; `gradleProperties` = kök `gradle.properties` DOSYASI (`palbase.env` dahil)
  - `EnvironmentResolver.OVERRIDE_ORIGIN = "override from ORG_GRADLE_PROJECT_*, -Dorg.gradle.project.* or ~/.gradle/gradle.properties"`; köken metni `<key> $OVERRIDE_ORIGIN`
  - `AndroidVariantIntegration`: `private fun properties(project: Project, file: RegularFile): Properties` (fileContents, yoksa boş), `private const val GRADLE_PROPERTIES = "gradle.properties"`
  - Test yardımcısı `places(commandLine, overrides, local, dsl, gradle, legacy)`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt`:
  (1/3) şu satırlardan sonra:
```kotlin
        assertSelected("staging", "-Ppalbase.env on the command line", resolver.resolve("qa", "qa"))
    }
```
  şunu ekle:
```kotlin

    // 1/2 (C3). A Gradle property that is NOT in the committed root
    // gradle.properties — an ORG_GRADLE_PROJECT_ variable, -Dorg.gradle.project.,
    // ~/.gradle/gradle.properties — was set for this machine or this CI job. It
    // ranks with the command line and says it is an override; it is never
    // "in gradle.properties", and never half of a committed conflict.
    @Test
    fun `an override ranks with the command line and says so`() {
        val resolver = places(
            overrides = mapOf("palbase.env.release" to "staging"),
            dsl = mapOf("release" to "prod"),
            gradle = mapOf("palbase.env.release" to "prod"),
        )
        assertSelected(
            "staging",
            "palbase.env.release override from ORG_GRADLE_PROJECT_*, -Dorg.gradle.project.* or ~/.gradle/gradle.properties",
            resolver.resolve("release", "release"),
        )
    }

    @Test
    fun `an override is not a committed conflict with the build type dsl`() {
        val resolver = places(overrides = mapOf("palbase.env.release" to "staging"), dsl = mapOf("release" to "prod"))
        val selected = resolver.resolve("release", "release") as EnvironmentResolution.Selected
        assertEquals("staging", selected.environment)
    }

    @Test
    fun `the command line beats an override of the same key`() {
        val resolver = places(
            commandLine = mapOf("palbase.env.release" to "prod"),
            overrides = mapOf("palbase.env.release" to "staging"),
        )
        assertSelected("prod", "-Ppalbase.env.release on the command line", resolver.resolve("release", "release"))
    }

    // The 2.3 global set the same way ranks with `-Ppalbase.env` (2).
    @Test
    fun `an override of the 2_3 global ranks with the command line global`() {
        val resolver = places(
            overrides = mapOf("palbase.env" to "staging"),
            gradle = mapOf("palbase.env.release" to "prod"),
        )
        assertSelected(
            "staging",
            "palbase.env override from ORG_GRADLE_PROJECT_*, -Dorg.gradle.project.* or ~/.gradle/gradle.properties",
            resolver.resolve("release", "release"),
        )
    }
```
  (2/3) şu bloğu:
```kotlin
                localProperties = { read += "local:$it"; if (it == "palbase.env.debug") "main" else null },
                buildTypeDsl = { read += "dsl:$it"; null },
                gradleProperties = { read += "gradle:$it"; null },
                legacy = { read += "legacy"; null },
            ),
        )
        resolver.resolve("debug", "debug")
        assertEquals(listOf("cli:palbase.env.debug", "cli:palbase.env", "local:palbase.env.debug"), read)
    }

    private fun places(
        commandLine: Map<String, String> = emptyMap(),
```
  şununla değiştir:
```kotlin
                overrides = { read += "override:$it"; null },
                localProperties = { read += "local:$it"; if (it == "palbase.env.debug") "main" else null },
                buildTypeDsl = { read += "dsl:$it"; null },
                gradleProperties = { read += "gradle:$it"; null },
            ),
        )
        resolver.resolve("debug", "debug")
        assertEquals(
            listOf(
                "cli:palbase.env.debug", "override:palbase.env.debug",
                "cli:palbase.env", "override:palbase.env",
                "local:palbase.env.debug",
            ),
            read,
        )
    }

    /**
     * @param gradle the committed root gradle.properties FILE.
     * @param overrides Gradle properties from anywhere else but the command line.
     * @param legacy `palbase.env` in that file.
     */
    private fun places(
        commandLine: Map<String, String> = emptyMap(),
        overrides: Map<String, String> = emptyMap(),
```
  (3/3) şu bloğu:
```kotlin
    ) = EnvironmentResolver(
        EnvironmentSources(
            commandLine = commandLine::get,
            localProperties = local::get,
            buildTypeDsl = dsl::get,
            gradleProperties = gradle::get,
            legacy = { legacy },
        ),
    )
```
  şununla değiştir:
```kotlin
    ): EnvironmentResolver {
        val committed = gradle + listOfNotNull(legacy?.let { "palbase.env" to it })
        return EnvironmentResolver(
            EnvironmentSources(
                commandLine = commandLine::get,
                overrides = overrides::get,
                localProperties = local::get,
                buildTypeDsl = dsl::get,
                gradleProperties = committed::get,
            ),
        )
    }
```

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertFalse(project.hasAsset("release"), "a build that picked either one generated a config")
    }
```
  şunu ekle:
```kotlin

    // C3: `-Dorg.gradle.project.…` (like ORG_GRADLE_PROJECT_… and
    // ~/.gradle/gradle.properties) is not the committed file. A lookup that could
    // not tell them apart refused a CI job's override of a DSL release as "two
    // committed places". It is an override, ranked with the command line.
    @Test
    fun `a gradle property override is not a committed conflict and says it is an override`() {
        val project = fixture(
            imports = "import io.palbase.gradle.palbase",
            buildTypes = """release { palbase { environment = "prod" } }""",
        )
        project.environment("prod")
        project.environment("staging")

        val result = project.build("generatePalbaseRelease", "-Dorg.gradle.project.palbase.env.release=staging")

        assertCompiled(project, "release", "staging")
        assertTrue(
            result.output.contains(
                "Palbase: release → staging (palbase.env.release override from ORG_GRADLE_PROJECT_*, " +
                    "-Dorg.gradle.project.* or ~/.gradle/gradle.properties)",
            ),
            result.output,
        )
    }

    // …and C3's own reproduction: an ORG_GRADLE_PROJECT_ variable — how a CI job
    // sets a property — is the same override, never half of a committed conflict.
    @Test
    fun `an ORG_GRADLE_PROJECT variable is an override and no committed conflict`() {
        val project = fixture(
            imports = "import io.palbase.gradle.palbase",
            buildTypes = """release { palbase { environment = "prod" } }""",
        )
        project.environment("prod")
        project.environment("staging")

        val result = GradleRunner.create()
            .withProjectDir(project.root.toFile())
            .withArguments("generatePalbaseRelease", "--stacktrace")
            .withPluginClasspath()
            .withEnvironment(System.getenv() + ("ORG_GRADLE_PROJECT_palbase.env.release" to "staging"))
            .build()

        assertCompiled(project, "release", "staging")
        assertTrue(
            result.output.contains(
                "Palbase: release → staging (palbase.env.release override from ORG_GRADLE_PROJECT_*, " +
                    "-Dorg.gradle.project.* or ~/.gradle/gradle.properties)",
            ),
            result.output,
        )
    }

    // The committed FILE is read as a file: the same key overridden for one run
    // is labelled an override — not "in gradle.properties" — and dropping the
    // override reconfigures and compiles what the file says.
    @Test
    fun `an override of the committed file is labelled and dropping it reconfigures`() {
        val project = fixture()
        project.environment("prod")
        project.environment("staging")
        project.root.resolve("gradle.properties").write("palbase.env.release=prod\n")

        val first = project.build(
            "generatePalbaseRelease", "--configuration-cache", "-Dorg.gradle.project.palbase.env.release=staging",
        )
        assertCompiled(project, "release", "staging")
        assertTrue(first.output.contains("Palbase: release → staging (palbase.env.release override from "), first.output)

        val second = project.build("generatePalbaseRelease", "--configuration-cache")
        assertFalse(second.output.contains("Configuration cache entry reused"), second.output)
        assertCompiled(project, "release", "prod")
        assertTrue(second.output.contains("Palbase: release → prod (palbase.env.release in gradle.properties)"), second.output)
    }

    // The 2.3 global set the same way is an override too, and ranks with
    // `-Ppalbase.env` (D-007): above every committed per-build-type key.
    @Test
    fun `an override of the 2_3 global outranks a committed build type key`() {
        val project = fixture()
        project.environment("prod")
        project.environment("staging")
        project.root.resolve("gradle.properties").write("palbase.env.release=prod\n")

        val result = project.build("generatePalbaseRelease", "-Dorg.gradle.project.palbase.env=staging")

        assertCompiled(project, "release", "staging")
        assertTrue(result.output.contains("Palbase: release → staging (palbase.env override from "), result.output)
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.a gradle property override*' --tests '*PalbaseCodegenPluginTest.an override of the*' --tests '*PalbaseCodegenPluginTest.an ORG_GRADLE_PROJECT*'` · Beklenen: **FAIL** (derleme): `> Task :compileTestKotlin FAILED`, `e: …/EnvironmentResolverTest.kt:359:17 No parameter with name 'overrides' found.` ve `e: …/EnvironmentResolverTest.kt:362:17 No value passed for parameter 'legacy'.` Davranış kırmızısı — yalnız `PalbaseCodegenPluginTest.kt` değişikliği uygulanıp aynı TestKit filtreleriyle: `4 tests completed, 4 failed`; `an ORG_GRADLE_PROJECT variable is an override and no committed conflict() FAILED` ve `a gradle property override is not a committed conflict and says it is an override() FAILED` → ``> Palbase: the `release` build type names two environments in two committed places — `palbase { environment = "prod" }` in the build type and `palbase.env.release=staging` in gradle.properties. …`` (C3'ün sahte çatışması, iki kaynaktan da); `an override of the committed file is labelled and dropping it reconfigures() FAILED` → `Palbase: release → staging (palbase.env.release in gradle.properties)` (yanlış etiket); `an override of the 2_3 global outranks a committed build type key() FAILED` → `release compiled {"app_id":"app_android","base_url":"https://prod1234m.dev.palbase.studio",…}`.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/4) şu satırlardan sonra:
```kotlin
import org.gradle.api.file.Directory
```
  şunu ekle:
```kotlin
import org.gradle.api.file.RegularFile
```
  (2/4) şu bloğu:
```kotlin
     * - `local.properties`: `providers.fileContents`, so editing the file — or
     *   creating it — invalidates the entry. (AGP 8.x happens to fingerprint the
     *   same file for `sdk.dir`; this read does not lean on that.)
     * - `gradle.properties` and `palbase.env`: `providers.gradleProperty`.
```
  şununla değiştir:
```kotlin
     * - `local.properties` and the committed root `gradle.properties`:
     *   `providers.fileContents`, so editing either file — or creating it —
     *   invalidates the entry. (AGP 8.x happens to fingerprint local.properties
     *   for `sdk.dir`; this read does not lean on that.) The committed choice is
     *   read from the FILE: `providers.gradleProperty` also answers from
     *   `ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*` and
     *   `~/.gradle/gradle.properties`, and a value from there is somebody's
     *   override, not what the checkout committed.
     * - Overrides: `providers.gradleProperty`, kept only when it is neither the
     *   command line's value nor the file's.
```
  (3/4) şu bloğu:
```kotlin
        val localProperties by lazy {
            val text = project.providers.fileContents(rootDirectory.file(LOCAL_PROPERTIES)).asText.orNull
            Properties().apply { if (text != null) load(StringReader(text)) }
        }
        return EnvironmentSources(
            commandLine = { key -> commandLine[key] },
            localProperties = { key -> localProperties.getProperty(key) },
            buildTypeDsl = buildTypeDsl,
            gradleProperties = { key -> project.providers.gradleProperty(key).orNull },
            legacy = { project.providers.gradleProperty(EnvironmentResolver.LEGACY_PROPERTY).orNull },
        )
    }

    /** `registerExtension` insists on a per-variant object; this plugin keeps nothing there. */
```
  şununla değiştir:
```kotlin
        val localProperties by lazy { properties(project, rootDirectory.file(LOCAL_PROPERTIES)) }
        val committed by lazy { properties(project, rootDirectory.file(GRADLE_PROPERTIES)) }
        return EnvironmentSources(
            commandLine = { key -> commandLine[key] },
            overrides = { key ->
                project.providers.gradleProperty(key).orNull
                    ?.takeIf { key !in commandLine && it != committed.getProperty(key) }
            },
            localProperties = { key -> localProperties.getProperty(key) },
            buildTypeDsl = buildTypeDsl,
            gradleProperties = { key -> committed.getProperty(key) },
        )
    }

    /** A properties file as a configuration-cache input; an absent one reads as empty. */
    private fun properties(project: Project, file: RegularFile): Properties {
        val text = project.providers.fileContents(file).asText.orNull
        return Properties().apply { if (text != null) load(StringReader(text)) }
    }

    /** `registerExtension` insists on a per-variant object; this plugin keeps nothing there. */
```
  (4/4) şu satırlardan sonra:
```kotlin
    private const val LOCAL_PROPERTIES = "local.properties"
```
  şunu ekle:
```kotlin
    private const val GRADLE_PROPERTIES = "gradle.properties"
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt`:
  (1/8) şu bloğu:
```kotlin
 *  1. `-Ppalbase.env.<V>`, then `-Ppalbase.env.<B>`, on the command line.
 *  2. `-Ppalbase.env` on the command line — every variant of that invocation.
 *     It was typed for THIS build (the 2.3 CI recipe), so it outranks every file.
```
  şununla değiştir:
```kotlin
 *  1. `-Ppalbase.env.<V>`, then `-Ppalbase.env.<B>`, on the command line — or
 *     the same key as an OVERRIDE: a Gradle property from anywhere but the
 *     command line and the committed root gradle.properties
 *     (`ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*`,
 *     `~/.gradle/gradle.properties`). It was set for this machine or this CI
 *     job, not committed, and its origin says so.
 *  2. `-Ppalbase.env` on the command line (or as an override) — every variant
 *     of that invocation. It was typed for THIS build (the 2.3 CI recipe), so it
 *     outranks every file.
```
  (2/8) şu bloğu:
```kotlin
 *  5. `palbase.env.<V>`, then `palbase.env.<B>`, in `gradle.properties`. When 4
 *     is set as well and the two differ, REFUSE: two committed places disagree,
 *     and whichever one lost would be a lie the next reader believes.
 *  6. `benchmark<X>` / `nonMinified<X>` — the baseline-profile plugin's build
 *     types — resolve as `<x>`: they exist to measure THAT build, so they aim at
 *     its stack instead of getting one of their own.
 *  7. `palbase.env`, the 2.3 global property, from a file. BEFORE the name
 *     rule: a 2.3 checkout with `palbase.env=main` and a custom `staging` build
 *     type compiled main, and must not switch to `staging/` on upgrade just
 *     because `palbase link` wrote that directory too.
```
  şununla değiştir:
```kotlin
 *  5. `palbase.env.<V>`, then `palbase.env.<B>`, in the root `gradle.properties`
 *     FILE — the committed one, read as a file. When 4 is set as well and the
 *     two differ, REFUSE: two committed places disagree, and whichever one lost
 *     would be a lie the next reader believes. An override is never half of
 *     that conflict: it outranked both at 1.
 *  6. `benchmark<X>` / `nonMinified<X>` — the baseline-profile plugin's build
 *     types — resolve as `<x>`: they exist to measure THAT build, so they aim at
 *     its stack instead of getting one of their own.
 *  7. `palbase.env`, the 2.3 global property, from the root gradle.properties
 *     file. BEFORE the name rule: a 2.3 checkout with `palbase.env=main` and a
 *     custom `staging` build type compiled main, and must not switch to
 *     `staging/` on upgrade just because `palbase link` wrote that directory too.
```
  (3/8) şu bloğu:
```kotlin
            keys.firstHit(sources.commandLine)?.let { (key, value) ->
                return EnvironmentResolution.Selected(value, "-P$key on the command line")
            }
            sources.commandLine(LEGACY_PROPERTY)?.trim()?.let {
                return EnvironmentResolution.Selected(it, "-P$LEGACY_PROPERTY on the command line")
            }
```
  şununla değiştir:
```kotlin
            keys.firstNotNullOfOrNull(::invocation)?.let { return it }
            invocation(LEGACY_PROPERTY)?.let { return it }
```
  (4/8) şu bloğu:
```kotlin
            sources.legacy()?.trim()?.let {
```
  şununla değiştir:
```kotlin
            sources.gradleProperties(LEGACY_PROPERTY)?.trim()?.let {
```
  (5/8) şu satırlardan sonra:
```kotlin
                    "(Kotlin DSL: `import io.palbase.gradle.palbase`).",
            )
        }
```
  şunu ekle:
```kotlin

        /** [key] as THIS invocation set it: on the command line, else as an override. */
        private fun invocation(key: String): EnvironmentResolution.Selected? {
            sources.commandLine(key)?.let { return EnvironmentResolution.Selected(it.trim(), "-P$key on the command line") }
            return sources.overrides(key)?.let { EnvironmentResolution.Selected(it.trim(), "$key $OVERRIDE_ORIGIN") }
        }
```
  (6/8) şu satırlardan sonra:
```kotlin
        const val DEBUG_DEFAULT = "local"
```
  şunu ekle:
```kotlin

        /** How a Gradle property from neither the command line nor the committed file is labelled. */
        const val OVERRIDE_ORIGIN =
            "override from ORG_GRADLE_PROJECT_*, -Dorg.gradle.project.* or ~/.gradle/gradle.properties"
```
  (7/8) şu satırlardan sonra:
```kotlin
    val commandLine: (String) -> String?,
```
  şunu ekle:
```kotlin
    /**
     * A Gradle property set anywhere BUT the command line and the committed root
     * gradle.properties — `ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*`,
     * `~/.gradle/gradle.properties` — and only when it differs from the file:
     * the same value in both is the committed one.
     */
    val overrides: (String) -> String?,
```
  (8/8) şu bloğu:
```kotlin
    /**
     * Gradle properties, asked only after the command line had its turn for the
     * same keys. `providers.gradleProperty` also answers from
     * `ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*` and
     * `~/.gradle/gradle.properties`; all of them are labelled gradle.properties.
     */
    val gradleProperties: (String) -> String?,
    /** `palbase.env`, wherever Gradle found it; the command line was asked first. */
    val legacy: () -> String?,
```
  şununla değiştir:
```kotlin
    /** The committed root `gradle.properties` FILE, `palbase.env` included — nothing else Gradle merges into it. */
    val gradleProperties: (String) -> String?,
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.a gradle property override*' --tests '*PalbaseCodegenPluginTest.an override of the*' --tests '*PalbaseCodegenPluginTest.an ORG_GRADLE_PROJECT*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 7s` (EnvironmentResolverTest `tests="34"`, 4 TestKit testi `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 49s`, `EnvironmentResolverTest` `tests="34" … failures="0"`, `PalbaseCodegenPluginTest` `tests="61" … failures="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): commit edilmiş seçim kök gradle.properties DOSYASINDAN okunur — başka kaynaktaki Gradle özelliği override etiketiyle komut satırı sırasında, sahte çatışma yok"`

---

### T010: Flavor anahtarı kendi build'lerinin ortamını seçer — aynı yerde flavor ile build type çelişirse ikisi de adlandırılıp reddedilir
<!-- deps: [T009] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-201, FR-202] -->

FR-201 adım 1/3/5'teki F (D-004) ve FR-202'nin "aynı seviyede flavor ile build type" cümlesi. Bugün hiçbir şey flavor adını okumuyor: `flavorDimensions("env")` + `staging`/`prod` bir app'te ret metni kullanıcıyı `palbase.env.release=prod`'a yönlendiriyor ve `stagingRelease` prod derleniyor; `palbase.env.staging=staging` eklemek sessizce yok sayılıyor (`reports/verification-2026-09-25.md` D2, `app-staging-release-unsigned.apk: base_url = https://prod.envprobe.palbase.studio`; proto `EnvironmentResolver.kt:50` `listOf(variant, buildType)`).

Kural, her YER (komut satırı+override, `local.properties`, `gradle.properties` dosyası) için ayrı sorulur (`ask`): variant'ın kendi anahtarı (`palbase.env.<V>`) o yeri kapatır; yoksa flavor kombinasyonu (`palbase.env.freeStaging`, AGP'nin yazımı, iki boyut ve üstü) kendi flavor'larını kapatır, yoksa tek tek flavor anahtarları; flavor seviyesi ile build type anahtarı FARKLI build kümelerini kapsar — biri diğerini ezmez: aynı yerde farklı ortam söylüyorlarsa o variant reddedilir, iki köken de adlandırılır ve çare `palbase.env.<V>=<environment>`'dır. Yerler arası sıra aynen kalır (üst yerdeki bir flavor anahtarı alttaki build type anahtarını yener). Flavor'lı bir release'in ret metni önce variant'ın ve flavor'ın anahtarlarını söyler (D2'nin önerdiği düzeltme).

**Build type bloğu ile dosya (adım 4 ile 5):** FR-202'nin ikinci reddi "aynı build type için"dir: build type'ın `palbase { }` bloğu ile dosyada AYNI build'leri kapsayan anahtar — variant'ın kendi anahtarı ya da o build type'ınki — farklı ortam söylerse ret (T004'ün kuralı, metni değişmez). Başka build'leri de kapsayan bir FLAVOR anahtarı aynı seçim değildir: FR-201'in sırasıyla adım 4 onu geçer (`the build type dsl outranks a flavor key in gradle properties`) — FR-202 yalnız "aynı build type için" reddeder. Dosyanın kendi içindeki bir flavor↔build type çelişkisi de, adım 4 zaten cevap vermişken ret üretmez: ilk isabet kazanır.

**Uyarılar bir KÜME'dir (`linkedSetOf`):** flavor'lı bir benchmark variant'ı iki kez yürür — kendisi ve ölçtüğü release ikizi — ve iki yürüyüş de flavor'ın anahtarını okur; liste aynı yok sayılan `local.properties` satırını iki kez basıyordu (`a flavored benchmark variant warns once about an ignored flavor line`; `mutableListOf` ile bu test düşer: ``expected: <[Palbase: `palbase.env.free=local` in local.properties is ignored for `freeBenchmarkRelease`, …]> but was: <[…, …]>``). İki flavor BOYUTU aynı yerde çelişirse de ret, kombinasyon anahtarı çözer (`two flavors that disagree in one place are refused and the combination settles them`).

**Bilinçli değişen mevcut test:** `a flavored release refusal names the variant too` → `a flavored release refusal names the variant and flavor keys too` — spec bu mesajı değiştiriyor; eski iddia (``variant `freeRelease` ``) korunur, üç anahtar eklenir. `resolve` dördüncü parametre `flavors`'ı alır; yardımcı `resolve(variant, buildType, flavors = emptyList(), debuggable = …)`.

**Interfaces:**
- Consumes: `Choice`, `invocation(key)`, `EnvironmentSources` (T009)
- Produces:
  - `EnvironmentResolver.resolve(variant: String, buildType: String, debuggable: Boolean, flavors: List<String>): EnvironmentResolution` — `flavors` boyut sırasında
  - `AndroidVariantIntegration`: `variant.productFlavors.map { (_, flavor) -> flavor }` çözücüye verilir
  - Özel tipler (çözücü içinde): `Keys(variant, buildType, flavors)` (`variant`, `combination`, `flavors`, `buildType`, `all`), `Answer.Hit(key, environment, origin)`, `Answer.Disagreement(hits, remedy)`; `ask(keys, lookup)`, `agree(hits, remedy)`, `resolution(answer)`
  - Çatışma metni: ``Palbase: `<V>` is given different environments in one place — `<a>` (<origin a>) and `<b>` (<origin b>). Neither outranks the other; `palbase.env.<V>=<environment>` in that same place names the one it compiles.``
  - `Choice.warnings` bir `LinkedHashSet`; `resolve` onu `toList()` ile verir — aynı cümle bir kez
  - Özel yardımcı `committed(key): Answer.Hit?` — kök `gradle.properties` dosyasındaki anahtar; build type bloğu yalnız `[variant, buildType]` anahtarlarıyla karşılaştırılır

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt`:
  (1/3) şu bloğu:
```kotlin
    @Test
    fun `a flavored release refusal names the variant too`() {
        val reason = assertRefused(places().resolve("freeRelease", "release"))
        assertTrue(reason.contains("variant `freeRelease`"), reason)
```
  şununla değiştir:
```kotlin
    // In a flavored module the refusal names the variant's and the flavor's
    // keys too — pointing only at `palbase.env.release` sent a flavored app to
    // the setting that makes `stagingRelease` ship prod (D2).
    @Test
    fun `a flavored release refusal names the variant and flavor keys too`() {
        val reason = assertRefused(places().resolve("freeRelease", "release", flavors = listOf("free")))
        assertTrue(reason.contains("variant `freeRelease`"), reason)
        assertTrue(reason.contains("`palbase.env.freeRelease=<environment>` for this variant alone"), reason)
        assertTrue(reason.contains("`palbase.env.free=<environment>` for every build of that flavor"), reason)
        assertTrue(reason.contains("`palbase.env.release=<environment>` for every release"), reason)
```
  (2/3) şu satırlardan sonra:
```kotlin
            resolver.resolve("featureX", "featureX", debuggable = true),
        )
    }
```
  şunu ekle:
```kotlin

    // FLAVORS (D-004). A flavor key chooses for every build of that flavor…
    @Test
    fun `a flavor key selects for every build of that flavor`() {
        val resolver = places(gradle = mapOf("palbase.env.staging" to "staging"))
        assertSelected(
            "staging",
            "palbase.env.staging in gradle.properties",
            resolver.resolve("stagingDebug", "debug", flavors = listOf("staging")),
        )
        assertSelected("local", "the default for debug", resolver.resolve("prodDebug", "debug", flavors = listOf("prod")))
    }

    // …and a flavor and a build type that name DIFFERENT stacks in one place are
    // refused, both named: they cover different builds, so neither outranks the
    // other, and picking either ships a variant against a stack its other line
    // says it does not use.
    @Test
    fun `a flavor and a build type that disagree in one place are refused naming both`() {
        val resolver = places(gradle = mapOf("palbase.env.staging" to "staging", "palbase.env.release" to "prod"))
        val reason = assertRefused(resolver.resolve("stagingRelease", "release", flavors = listOf("staging")))
        assertEquals(
            "Palbase: `stagingRelease` is given different environments in one place — " +
                "`staging` (palbase.env.staging in gradle.properties) and `prod` (palbase.env.release in gradle.properties). " +
                "Neither outranks the other; `palbase.env.stagingRelease=<environment>` in that same place names the " +
                "one it compiles.",
            reason,
        )
    }

    @Test
    fun `a flavor and a build type that agree are not a conflict`() {
        val resolver = places(gradle = mapOf("palbase.env.staging" to "staging", "palbase.env.release" to "staging"))
        assertSelected(
            "staging",
            "palbase.env.staging in gradle.properties",
            resolver.resolve("stagingRelease", "release", flavors = listOf("staging")),
        )
    }

    // The variant's own key is more specific than both, and settles it.
    @Test
    fun `the variant key settles a flavor and a build type that disagree`() {
        val resolver = places(
            gradle = mapOf(
                "palbase.env.staging" to "staging",
                "palbase.env.release" to "prod",
                "palbase.env.stagingRelease" to "staging",
            ),
        )
        assertSelected(
            "staging",
            "palbase.env.stagingRelease in gradle.properties",
            resolver.resolve("stagingRelease", "release", flavors = listOf("staging")),
        )
    }

    // With two dimensions the COMBINATION is a key too — AGP's own spelling…
    @Test
    fun `the flavor combination is a key of its own`() {
        val resolver = places(gradle = mapOf("palbase.env.freeStaging" to "staging"))
        assertSelected(
            "staging",
            "palbase.env.freeStaging in gradle.properties",
            resolver.resolve("freeStagingRelease", "release", flavors = listOf("free", "staging")),
        )
    }

    // …and it covers a SUBSET of its flavors' builds, so it settles them, as
    // AGP's own source-set order does. The build type is still a different set
    // of builds: the combination and the build type must agree.
    @Test
    fun `the flavor combination settles its own flavors`() {
        val resolver = places(gradle = mapOf("palbase.env.freeStaging" to "free-staging", "palbase.env.staging" to "staging"))
        assertSelected(
            "free-staging",
            "palbase.env.freeStaging in gradle.properties",
            resolver.resolve("freeStagingDebug", "debug", flavors = listOf("free", "staging")),
        )
        assertSelected(
            "staging",
            "palbase.env.staging in gradle.properties",
            resolver.resolve("paidStagingDebug", "debug", flavors = listOf("paid", "staging")),
        )
    }

    // The agreement is asked PER PLACE: a higher place still outranks a lower
    // one, whichever level each key names.
    @Test
    fun `a flavor key in a higher place outranks a build type key in a lower one`() {
        val resolver = places(
            commandLine = mapOf("palbase.env.staging" to "staging"),
            gradle = mapOf("palbase.env.release" to "prod"),
        )
        assertSelected(
            "staging",
            "-Ppalbase.env.staging on the command line",
            resolver.resolve("stagingRelease", "release", flavors = listOf("staging")),
        )
    }

    // 4 before 5 (FR-201): a build type's `palbase { }` outranks a committed
    // FLAVOR key. The key covers other builds too, so the two are not one
    // committed choice said twice — the higher place wins, as it does anywhere.
    // Only a key for the SAME builds is the block's conflict (4/5 above).
    @Test
    fun `the build type dsl outranks a flavor key in gradle properties`() {
        val resolver = places(dsl = mapOf("release" to "prod"), gradle = mapOf("palbase.env.staging" to "staging"))
        assertSelected(
            "prod",
            "palbase { environment } in the `release` build type",
            resolver.resolve("stagingRelease", "release", flavors = listOf("staging")),
        )
    }

    // Two DIMENSIONS whose keys disagree in one place cover different builds
    // too: refused, both named — and the combination key, AGP's own spelling,
    // settles them.
    @Test
    fun `two flavors that disagree in one place are refused and the combination settles them`() {
        val flavors = listOf("free", "staging")
        val split = places(gradle = mapOf("palbase.env.free" to "main", "palbase.env.staging" to "staging"))
        val reason = assertRefused(split.resolve("freeStagingDebug", "debug", flavors = flavors))
        assertTrue(reason.contains("`freeStagingDebug` is given different environments in one place"), reason)
        assertTrue(reason.contains("`main` (palbase.env.free in gradle.properties)"), reason)
        assertTrue(reason.contains("`staging` (palbase.env.staging in gradle.properties)"), reason)

        val settled = places(
            gradle = mapOf(
                "palbase.env.free" to "main",
                "palbase.env.staging" to "staging",
                "palbase.env.freeStaging" to "staging",
            ),
        )
        assertSelected(
            "staging",
            "palbase.env.freeStaging in gradle.properties",
            settled.resolve("freeStagingDebug", "debug", flavors = flavors),
        )
    }

    // A flavored benchmark variant walks twice — itself, then the release twin
    // it measures — and both read the flavor's key. An ignored personal line is
    // still ONE line: it is warned about once.
    @Test
    fun `a flavored benchmark variant warns once about an ignored flavor line`() {
        val resolver = places(local = mapOf("palbase.env.free" to "local"), gradle = mapOf("palbase.env.release" to "main"))
        val selected = resolver.resolve("freeBenchmarkRelease", "benchmarkRelease", flavors = listOf("free"))
            as EnvironmentResolution.Selected

        assertEquals("main", selected.environment)
        assertEquals(
            listOf(
                "Palbase: `palbase.env.free=local` in local.properties is ignored for `freeBenchmarkRelease`, which is " +
                    "not debuggable — local.properties is personal, and a build that can ship never takes its stack " +
                    "from it. For one build, pass `-Ppalbase.env.free=local`; to commit the choice, put the line in " +
                    "gradle.properties.",
            ),
            selected.warnings,
        )
    }
```
  (3/3) şu bloğu:
```kotlin
     * one made with `initWith(debug)` — says so with `debuggable = true`.
     */
    private fun EnvironmentResolver.resolve(variant: String, buildType: String) =
        resolve(variant, buildType, debuggable = buildType == "debug")
```
  şununla değiştir:
```kotlin
     * one made with `initWith(debug)` — says so with `debuggable = true`. No
     * flavors unless the test names them.
     */
    private fun EnvironmentResolver.resolve(
        variant: String,
        buildType: String,
        flavors: List<String> = emptyList(),
        debuggable: Boolean = buildType == "debug",
    ): EnvironmentResolution = resolve(variant, buildType, debuggable, flavors)
```

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertTrue(result.output.contains("Palbase: paidDebug → main (-Ppalbase.env.paidDebug on the command line)"), result.output)
    }
```
  şunu ekle:
```kotlin

    // D2: a FLAVOR key chooses for every build of that flavor — ignored in
    // silence before, so `stagingRelease` shipped prod once the release refusal
    // was followed. A flavor and a build type that disagree in one place are
    // refused, both named, and only for the variant they disagree on.
    @Test
    fun `a flavor key selects for its builds and a disagreeing build type key is refused`() {
        val project = fixture(
            android = """
                flavorDimensions += "env"
                productFlavors { create("staging") { dimension = "env" }; create("prod") { dimension = "env" } }
            """.trimIndent(),
        )
        project.environment("local")
        project.environment("staging")
        project.environment("prod")
        project.root.resolve("gradle.properties").write("palbase.env.staging=staging\npalbase.env.release=prod\n")

        val result = project.build("generatePalbaseStagingDebug", "generatePalbaseProdRelease")

        assertCompiled(project, "stagingDebug", "staging")
        assertCompiled(project, "prodRelease", "prod")
        assertTrue(result.output.contains("Palbase: stagingDebug → staging (palbase.env.staging in gradle.properties)"), result.output)

        val failure = project.buildAndFail("generatePalbaseStagingRelease").output
        assertTrue(
            failure.contains(
                "Palbase: `stagingRelease` is given different environments in one place — " +
                    "`staging` (palbase.env.staging in gradle.properties) and `prod` (palbase.env.release in gradle.properties)",
            ),
            failure,
        )
        assertTrue(failure.contains("`palbase.env.stagingRelease=<environment>` in that same place"), failure)
        assertFalse(project.hasAsset("stagingRelease"), "a variant whose flavor and build type disagree was generated")
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.a flavor key selects*'` · Beklenen: **FAIL** (derleme): `> Task :compileTestKotlin FAILED`, `e: …/EnvironmentResolverTest.kt:579:60 Argument type mismatch: actual type is 'Boolean', but 'List<String>' was expected.` Davranış kırmızısı — yalnız TestKit değişikliğiyle: `tests="1" … failures="1"`; `a flavor key selects for its builds and a disagreeing build type key is refused() FAILED` → `stagingDebug compiled {"app_id":"app_android","base_url":"https://local1234m.dev.palbase.studio","api_key":"pb_local1234m_c0123456789abcdefghij"}` (flavor anahtarı sessizce yok sayıldı — D2).
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  şu bloğu:
```kotlin
            val resolution = resolver.resolve(variant.name, buildType, variant.debuggable)
```
  şununla değiştir:
```kotlin
            val resolution = resolver.resolve(
                variant.name,
                buildType,
                variant.debuggable,
                variant.productFlavors.map { (_, flavor) -> flavor },
            )
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt`:
  (1/9) şu bloğu:
```kotlin
 * For variant V of build type B the first hit wins:
 *
 *  1. `-Ppalbase.env.<V>`, then `-Ppalbase.env.<B>`, on the command line — or
```
  şununla değiştir:
```kotlin
 * For variant V of build type B and product flavors F₁…Fₙ (F their
 * combination, `freeStaging`, when there are two or more) the first hit wins.
 * Within ONE place the variant's own key settles it, and F settles each Fᵢ it
 * combines — a smaller set of builds is the more specific answer. The flavor
 * level and B cover DIFFERENT builds, so neither outranks the other: when they
 * name different environments the place is REFUSED, naming both — a flavor that
 * says `staging` and a build type that says `prod` cannot both be right for
 * `stagingRelease`.
 *
 *  1. `-Ppalbase.env.<V>`, then `-Ppalbase.env.<F|Fᵢ|B>`, on the command line — or
```
  (2/9) şu bloğu:
```kotlin
 *  3. `palbase.env.<V>`, then `palbase.env.<B>`, in the root `local.properties` —
```
  şununla değiştir:
```kotlin
 *  3. `palbase.env.<V>`, then `palbase.env.<F|Fᵢ|B>`, in the root `local.properties` —
```
  (3/9) şu bloğu:
```kotlin
 *  5. `palbase.env.<V>`, then `palbase.env.<B>`, in the root `gradle.properties`
 *     FILE — the committed one, read as a file. When 4 is set as well and the
 *     two differ, REFUSE: two committed places disagree, and whichever one lost
 *     would be a lie the next reader believes. An override is never half of
 *     that conflict: it outranked both at 1.
```
  şununla değiştir:
```kotlin
 *  5. `palbase.env.<V>`, then `palbase.env.<F|Fᵢ|B>`, in the root `gradle.properties`
 *     FILE — the committed one, read as a file. When 4 is set as well, a key
 *     here for the SAME builds as the block — the variant's own, or the block's
 *     build type's — must name the same environment, or REFUSE: two committed
 *     places disagree, and whichever one lost would be a lie the next reader
 *     believes. A key that covers other builds too (a flavor's, under a build
 *     type's block) is not the same choice: 4 outranks it, as any higher place
 *     does. An override is never half of that conflict: it outranked both at 1.
```
  (4/9) şu bloğu:
```kotlin
     */
    fun resolve(variant: String, buildType: String, debuggable: Boolean): EnvironmentResolution {
        val choice = Choice(variant, debuggable)
```
  şununla değiştir:
```kotlin
     * @param flavors the variant's product flavor names, in dimension order.
     */
    fun resolve(variant: String, buildType: String, debuggable: Boolean, flavors: List<String>): EnvironmentResolution {
        val choice = Choice(variant, debuggable, flavors)
```
  (5/9) şu bloğu:
```kotlin
            is EnvironmentResolution.Selected -> resolution.copy(warnings = choice.warnings)
            is EnvironmentResolution.Refused -> resolution.copy(warnings = choice.warnings)
```
  şununla değiştir:
```kotlin
            is EnvironmentResolution.Selected -> resolution.copy(warnings = choice.warnings.toList())
            is EnvironmentResolution.Refused -> resolution.copy(warnings = choice.warnings.toList())
```
  (6/9) şu bloğu:
```kotlin
    private inner class Choice(private val subject: String, private val debuggable: Boolean) {
        val warnings = mutableListOf<String>()

        fun choose(variant: String, buildType: String): EnvironmentResolution {
            val keys = listOf(variant, buildType).distinct().map { PROPERTY_PREFIX + it }

            keys.firstNotNullOfOrNull(::invocation)?.let { return it }
            invocation(LEGACY_PROPERTY)?.let { return it }
            if (debuggable) {
                keys.firstHit(sources.localProperties)?.let { (key, value) ->
                    return EnvironmentResolution.Selected(value, "$key in local.properties")
                }
            } else {
                keys.forEach { key -> sources.localProperties(key)?.let { warnings += ignoredLocalLine(key, it.trim()) } }
            }

            val declared = sources.buildTypeDsl(buildType)?.trim()
            val committed = keys.firstHit(sources.gradleProperties)
            if (declared != null) {
                if (committed != null && committed.second != declared) {
                    return EnvironmentResolution.Refused(
                        "Palbase: the `$buildType` build type names two environments in two committed places — " +
                            "`palbase { environment = \"$declared\" }` in the build type and " +
                            "`${committed.first}=${committed.second}` in gradle.properties. Keep ONE of them; " +
                            "a build that picked either would leave the other one lying to the next reader.",
                    )
                }
                return EnvironmentResolution.Selected(declared, "palbase { environment } in the `$buildType` build type")
            }
            committed?.let { (key, value) ->
                return EnvironmentResolution.Selected(value, "$key in gradle.properties")
            }
```
  şununla değiştir:
```kotlin
    private inner class Choice(
        private val subject: String,
        private val debuggable: Boolean,
        private val flavors: List<String>,
    ) {
        /** A SET: a flavored benchmark variant walks twice, and both walks read the flavor's key. */
        val warnings = linkedSetOf<String>()

        fun choose(variant: String, buildType: String): EnvironmentResolution {
            val keys = Keys(variant, buildType, flavors)

            ask(keys, ::invocation)?.let { return resolution(it) }
            invocation(LEGACY_PROPERTY)?.let { return resolution(it) }
            if (debuggable) {
                ask(keys) { key -> sources.localProperties(key)?.let { Answer.Hit(key, it.trim(), "$key in local.properties") } }
                    ?.let { return resolution(it) }
            } else {
                keys.all.forEach { key -> sources.localProperties(key)?.let { warnings += ignoredLocalLine(key, it.trim()) } }
            }

            val declared = sources.buildTypeDsl(buildType)?.trim()
            if (declared != null) {
                // Only a key for the SAME builds is the block's other committed
                // place; a flavor key covers other builds too, and 4 outranks it.
                listOf(keys.variant, keys.buildType).distinct().firstNotNullOfOrNull(::committed)
                    ?.takeIf { it.environment != declared }
                    ?.let { key ->
                        return EnvironmentResolution.Refused(
                            "Palbase: the `$buildType` build type names two environments in two committed places — " +
                                "`palbase { environment = \"$declared\" }` in the build type and " +
                                "`${key.key}=${key.environment}` in gradle.properties. Keep ONE of them; " +
                                "a build that picked either would leave the other one lying to the next reader.",
                        )
                    }
                return EnvironmentResolution.Selected(declared, "palbase { environment } in the `$buildType` build type")
            }
            ask(keys, ::committed)?.let { return resolution(it) }
```
  (7/9) şu bloğu:
```kotlin
            return EnvironmentResolution.Refused(
                "Palbase: `$RELEASE`$variantClause has no environment, and a release never gets one by default — " +
                    "it would ship a client aimed at a stack nobody chose. Commit the choice, in ONE of two ways: " +
                    "`$PROPERTY_PREFIX$RELEASE=<environment>` in gradle.properties, or " +
```
  şununla değiştir:
```kotlin
            // A flavored variant is told its OWN keys first: pointing it at
            // `palbase.env.release` alone is the setting that ships prod in
            // `stagingRelease`.
            val committedWays = if (flavors.isEmpty()) {
                "Commit the choice, in ONE of two ways: `$PROPERTY_PREFIX$RELEASE=<environment>` in gradle.properties, or "
            } else {
                "Commit the choice in gradle.properties — `$PROPERTY_PREFIX$variant=<environment>` for this variant " +
                    "alone, ${flavors.joinToString(" or ") { "`$PROPERTY_PREFIX$it=<environment>`" }} for every build " +
                    "of that flavor, or `$PROPERTY_PREFIX$RELEASE=<environment>` for every release — or "
            }
            return EnvironmentResolution.Refused(
                "Palbase: `$RELEASE`$variantClause has no environment, and a release never gets one by default — " +
                    "it would ship a client aimed at a stack nobody chose. $committedWays" +
```
  (8/9) şu bloğu:
```kotlin
        /** [key] as THIS invocation set it: on the command line, else as an override. */
        private fun invocation(key: String): EnvironmentResolution.Selected? {
            sources.commandLine(key)?.let { return EnvironmentResolution.Selected(it.trim(), "-P$key on the command line") }
            return sources.overrides(key)?.let { EnvironmentResolution.Selected(it.trim(), "$key $OVERRIDE_ORIGIN") }
```
  şununla değiştir:
```kotlin
        /**
         * What ONE place says. The variant's own key settles it; else the flavor
         * combination settles the flavors it combines; else every flavor key and
         * the build type key that are set must name the SAME environment, or the
         * place disagrees with itself.
         */
        private fun ask(keys: Keys, lookup: (String) -> Answer.Hit?): Answer? {
            lookup(keys.variant)?.let { return it }
            val flavorLevel = keys.combination?.let(lookup)?.let(::listOf) ?: keys.flavors.mapNotNull(lookup)
            val buildTypeLevel = listOfNotNull(keys.buildType.takeIf { it != keys.variant }?.let(lookup))
            return agree(
                flavorLevel + buildTypeLevel,
                remedy = "`$PROPERTY_PREFIX$subject=<environment>` in that same place names the one it compiles",
            )
        }

        /** Answers of equal rank: one environment, or a disagreement that [remedy] says how to settle. */
        private fun agree(hits: List<Answer.Hit>, remedy: String): Answer? = when {
            hits.isEmpty() -> null
            hits.distinctBy { it.environment }.size == 1 -> hits.first()
            else -> Answer.Disagreement(hits, remedy)
        }

        private fun resolution(answer: Answer): EnvironmentResolution = when (answer) {
            is Answer.Hit -> EnvironmentResolution.Selected(answer.environment, answer.origin)
            is Answer.Disagreement -> EnvironmentResolution.Refused(
                "Palbase: `$subject` is given different environments in one place — " +
                    answer.hits.joinToString(" and ") { "`${it.environment}` (${it.origin})" } +
                    ". Neither outranks the other; ${answer.remedy}.",
            )
        }

        /** [key] in the committed root gradle.properties. */
        private fun committed(key: String): Answer.Hit? =
            sources.gradleProperties(key)?.let { Answer.Hit(key, it.trim(), "$key in gradle.properties") }

        /** [key] as THIS invocation set it: on the command line, else as an override. */
        private fun invocation(key: String): Answer.Hit? {
            sources.commandLine(key)?.let { return Answer.Hit(key, it.trim(), "-P$key on the command line") }
            return sources.overrides(key)?.let { Answer.Hit(key, it.trim(), "$key $OVERRIDE_ORIGIN") }
```
  (9/9) şu bloğu:
```kotlin
    private fun List<String>.firstHit(lookup: (String) -> String?): Pair<String, String>? =
        firstNotNullOfOrNull { key -> lookup(key)?.let { key to it.trim() } }
```
  şununla değiştir:
```kotlin
    /** The property keys one variant answers to. */
    private class Keys(variant: String, buildType: String, flavors: List<String>) {
        /** The variant's own key — the most specific there is, so it settles any place it is in. */
        val variant = PROPERTY_PREFIX + variant

        /** `[free, staging]` → `palbase.env.freeStaging`, AGP's own spelling; one flavor is no combination. */
        val combination = flavors.takeIf { it.size > 1 }
            ?.let { PROPERTY_PREFIX + it.first() + it.drop(1).joinToString("") { flavor -> flavor.replaceFirstChar(Char::uppercase) } }

        val flavors = flavors.map { PROPERTY_PREFIX + it }
        val buildType = PROPERTY_PREFIX + buildType
        val all = (listOfNotNull(this.variant, combination) + this.flavors + this.buildType).distinct()
    }

    /** What ONE place says for a variant. */
    private sealed interface Answer {
        /** One environment, from [key]; [origin] is how it is printed. */
        data class Hit(val key: String, val environment: String, val origin: String) : Answer

        /** Answers of equal rank in one place that name different environments; [remedy] says how to settle it. */
        data class Disagreement(val hits: List<Hit>, val remedy: String) : Answer
    }
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.a flavor key selects*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 5s` (EnvironmentResolverTest `tests="44"`, TestKit `tests="1"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 5s`, `EnvironmentResolverTest` `tests="44" … failures="0"`, `PalbaseCodegenPluginTest` `tests="62" … failures="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): flavor anahtarı kendi build'lerinin ortamını seçer — aynı yerde flavor ile build type çelişirse ikisi de adlandırılıp reddedilir"`

---

### T011: Product flavor kendi ortamını yazar — flavor'da `palbase { environment }`; build type'ınkiyle çelişirse o variant reddedilir
<!-- deps: [T004, T005, T007, T010] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseProductFlavor.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-201, FR-202, FR-209] -->

FR-201 adım 4'ün flavor yarısı (D-004): `DslExtension.Builder.extendProductFlavorWith` (AGP 8.10.1, 8.11.1, 9.1.1, 9.3.2'de var — `reports/verification-2026-09-25.md` D2 javap) ile her product flavor'a `palbase { environment = "…" }`; değer `finalizeDsl`'de build type'ınkiyle aynı yoldan anlık görüntülenir. Kotlin DSL'de build type'la **aynı import** (`io.palbase.gradle.palbase`) yeter — yeni bir `ProductFlavor.palbase(...)` uzantısı; Groovy'de import gerekmez. Flavor blokları ile build type bloğu adım 4'te TEK yerdir: farklı ortam söylerlerse (T010'un kuralı) o variant reddedilir, iki blok da adlandırılır. Her bloğun ikinci commit edilmiş yeri, dosyada AYNI build'leri kapsayan anahtardır — variant'ınki, ve bloğun kendi flavor'ınınki (ya da o flavor'ı içeren kombinasyon) veya build type'ınki; farklıysa build type'ın zaten sahip olduğu "two committed places" reddi — metin artık bloğun sahibini söyler (``the `staging` product flavor`` / ``the `release` build type``) ve "in the build type" yerine "in the build script" der. Başka build'leri de kapsayan anahtarı — bir flavor bloğunun altındaki `palbase.env.release` — blok geçer (adım 4, 5'ten önce): `palbase link`'in her app'e bastığı `palbase.env.debug`/`palbase.env.release` satırları (FR-013) flavor bloklu bir modülde ret üretmez (`flavor blocks outrank the build type keys in gradle properties`). Birim düzeyinde iki yön de sabit: `a flavor dsl outranks a build type key in gradle properties` (→ `staging`) ve `a flavor dsl and a committed key for the same builds that disagree are refused` (flavor anahtarı ve variant anahtarı → ret, tam metin). Flavor bloğu ile kendi flavor anahtarı arasındaki ret, FR-202'nin "aynı build type için" reddinin flavor'a taşınmış hâlidir (D-004: flavor seviyesi build type'ınkiyle aynı kurallarla).

FR-209'un tuzağı flavor'ı da kapsar: import'suz bir flavor'daki `palbase { }` proje bloğudur ve derlenmez; `ENVIRONMENT_TRAP` metni "PER BUILD TYPE (or product flavor)" ve "INSIDE the build type or product flavor" der (T005'in testleri `the environment is chosen PER BUILD TYPE` alt dizesini arar — değişmez). Flavor'lı release'in ret metnine `productFlavors { getByName("<flavor>") { palbase { … } } }` yolu eklenir.

Fixture: `groovyBuildScript(buildTypes, android)` — `android` parametresi artık Groovy fixture'ında da `android { }` içine yazılır (mevcut Groovy testleri `android` vermez; davranışları değişmez).

**Interfaces:**
- Consumes: `Answer`, `agree(...)`, `resolution(...)` (T010); `PalbaseBuildType`, `BuildType.palbase(...)` (T004); `ENVIRONMENT_TRAP` (T005)
- Produces:
  - `abstract class PalbaseProductFlavor { abstract val environment: Property<String> }` ve `fun ProductFlavor.palbase(configure: Action<in PalbaseProductFlavor>)` — yeni dosya `PalbaseProductFlavor.kt`
  - `EnvironmentSources(..., buildTypeDsl, flavorDsl: (String) -> String?, gradleProperties)`
  - Köken metni: ``palbase { environment } in the `<flavor>` product flavor``
  - `ENVIRONMENT_TRAP` yeni metni: `Palbase: the environment is chosen PER BUILD TYPE (or product flavor), not in the project-level …`
  - Test yardımcıları: `places(..., flavorDsl = …)`; `groovyBuildScript(buildTypes: String, android: String)`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt`:
  (1/5) şu satırlardan sonra:
```kotlin
        assertTrue(reason.contains("`palbase.env.release=<environment>` for every release"), reason)
```
  şunu ekle:
```kotlin
        assertTrue(
            reason.contains("`productFlavors { getByName(\"free\") { palbase { environment = \"<environment>\" } } }`"),
            reason,
        )
```
  (2/5) şu satırlardan sonra:
```kotlin
            "-Ppalbase.env.staging on the command line",
            resolver.resolve("stagingRelease", "release", flavors = listOf("staging")),
        )
    }
```
  şunu ekle:
```kotlin

    // 4 (D-004). `palbase { environment }` on a product flavor is the flavor's
    // committed choice, at the same rank as the build type's.
    @Test
    fun `a flavor dsl selects for every build of that flavor`() {
        val resolver = places(flavorDsl = mapOf("staging" to "staging"))
        assertSelected(
            "staging",
            "palbase { environment } in the `staging` product flavor",
            resolver.resolve("stagingRelease", "release", flavors = listOf("staging")),
        )
    }

    @Test
    fun `a flavor dsl and a build type dsl that disagree are refused naming both`() {
        val resolver = places(flavorDsl = mapOf("staging" to "staging"), dsl = mapOf("release" to "prod"))
        val reason = assertRefused(resolver.resolve("stagingRelease", "release", flavors = listOf("staging")))
        assertEquals(
            "Palbase: `stagingRelease` is given different environments in one place — " +
                "`staging` (palbase { environment } in the `staging` product flavor) and " +
                "`prod` (palbase { environment } in the `release` build type). Neither outranks the other; keep the " +
                "environment in ONE of those `palbase { }` blocks, or move both to gradle.properties and add " +
                "`palbase.env.stagingRelease=<environment>`.",
            reason,
        )
    }

    // 4 before 5 for a flavor's block too: it outranks a committed BUILD TYPE
    // key, which covers other builds as well.
    @Test
    fun `a flavor dsl outranks a build type key in gradle properties`() {
        val resolver = places(flavorDsl = mapOf("staging" to "staging"), gradle = mapOf("palbase.env.release" to "prod"))
        assertSelected(
            "staging",
            "palbase { environment } in the `staging` product flavor",
            resolver.resolve("stagingRelease", "release", flavors = listOf("staging")),
        )
    }

    // …but a committed key for the SAME builds as the block — its own flavor's,
    // a combination of it, the variant's — is the same choice said twice: two
    // committed places, refused as a build type's block and key are (4/5).
    @Test
    fun `a flavor dsl and a committed key for the same builds that disagree are refused`() {
        val resolver = places(flavorDsl = mapOf("staging" to "staging"), gradle = mapOf("palbase.env.staging" to "qa"))
        val reason = assertRefused(resolver.resolve("stagingRelease", "release", flavors = listOf("staging")))
        assertEquals(
            "Palbase: the `staging` product flavor names two environments in two committed places — " +
                "`palbase { environment = \"staging\" }` in the build script and `palbase.env.staging=qa` in " +
                "gradle.properties. Keep ONE of them; a build that picked either would leave the other one lying to " +
                "the next reader.",
            reason,
        )
        val variantKey = places(
            flavorDsl = mapOf("staging" to "staging"),
            gradle = mapOf("palbase.env.stagingRelease" to "qa"),
        )
        assertTrue(
            assertRefused(variantKey.resolve("stagingRelease", "release", flavors = listOf("staging")))
                .contains("`palbase.env.stagingRelease=qa` in gradle.properties"),
        )
    }
```
  (3/5) şu satırlardan sonra:
```kotlin
                buildTypeDsl = { read += "dsl:$it"; null },
```
  şunu ekle:
```kotlin
                flavorDsl = { read += "flavorDsl:$it"; null },
```
  (4/5) şu satırlardan sonra:
```kotlin
        dsl: Map<String, String> = emptyMap(),
```
  şunu ekle:
```kotlin
        flavorDsl: Map<String, String> = emptyMap(),
```
  (5/5) şu satırlardan sonra:
```kotlin
                buildTypeDsl = dsl::get,
```
  şunu ekle:
```kotlin
                flavorDsl = flavorDsl::get,
```

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  (1/5) şu satırlardan sonra:
```kotlin
        assertFalse(project.hasAsset("stagingRelease"), "a variant whose flavor and build type disagree was generated")
    }
```
  şunu ekle:
```kotlin

    // D-004: `palbase { environment }` on a PRODUCT FLAVOR chooses for every
    // build of it; a flavor and the build type of one variant that both name a
    // stack must name the same one — refused, both named, for that variant only.
    @Test
    fun `the Kotlin flavor dsl selects per flavor and a disagreeing build type dsl is refused`() {
        val project = fixture(
            imports = "import io.palbase.gradle.palbase",
            android = """
                flavorDimensions += "env"
                productFlavors {
                    create("staging") { dimension = "env"; palbase { environment = "staging" } }
                    create("prod") { dimension = "env" }
                }
            """.trimIndent(),
            buildTypes = """release { palbase { environment = "prod" } }""",
        )
        project.environment("local")
        project.environment("staging")
        project.environment("prod")

        val result = project.build("generatePalbaseStagingDebug", "generatePalbaseProdRelease")

        assertCompiled(project, "stagingDebug", "staging")
        assertCompiled(project, "prodRelease", "prod")
        assertTrue(
            result.output.contains("Palbase: stagingDebug → staging (palbase { environment } in the `staging` product flavor)"),
            result.output,
        )

        val failure = project.buildAndFail("generatePalbaseStagingRelease").output
        assertTrue(
            failure.contains(
                "`staging` (palbase { environment } in the `staging` product flavor) and " +
                    "`prod` (palbase { environment } in the `release` build type)",
            ),
            failure,
        )
        assertFalse(project.hasAsset("stagingRelease"), "a variant whose flavor and build type disagree was generated")
    }

    // A flavor's block is committed one place ABOVE gradle.properties (4 before
    // 5): the `palbase.env.debug` / `palbase.env.release` lines `palbase link`
    // prints for every app cover other builds too, so they are outranked — not
    // a conflict — in a module whose flavors name their own stacks.
    @Test
    fun `flavor blocks outrank the build type keys in gradle properties`() {
        val project = fixture(
            imports = "import io.palbase.gradle.palbase",
            android = """
                flavorDimensions += "env"
                productFlavors {
                    create("staging") { dimension = "env"; palbase { environment = "staging" } }
                    create("prod") { dimension = "env"; palbase { environment = "prod" } }
                }
            """.trimIndent(),
        )
        project.environment("main")
        project.environment("staging")
        project.environment("prod")
        project.root.resolve("gradle.properties").write("palbase.env.debug=main\npalbase.env.release=main\n")

        val result = project.build("generatePalbaseStagingRelease", "generatePalbaseProdDebug")

        assertCompiled(project, "stagingRelease", "staging")
        assertCompiled(project, "prodDebug", "prod")
        assertTrue(
            result.output.contains("Palbase: stagingRelease → staging (palbase { environment } in the `staging` product flavor)"),
            result.output,
        )
    }

    // Groovy resolves `palbase` against the flavor first — no import.
    @Test
    fun `the Groovy flavor dsl selects per flavor`() {
        val project = fixture(
            groovy = true,
            android = """
                flavorDimensions 'env'
                productFlavors { staging { dimension 'env'; palbase { environment = 'staging' } } }
            """.trimIndent(),
        )
        project.environment("staging")

        val result = project.build("generatePalbaseStagingDebug")

        assertCompiled(project, "stagingDebug", "staging")
        assertTrue(
            result.output.contains("Palbase: stagingDebug → staging (palbase { environment } in the `staging` product flavor)"),
            result.output,
        )
    }

    // Without the import a flavor's `palbase { }` is the PROJECT's block, as in a
    // build type — and the trap's guidance names the flavor too.
    @Test
    fun `the Kotlin flavor dsl without the import does not compile and names the fix`() {
        val project = fixture(
            android = """
                flavorDimensions += "env"
                productFlavors { create("staging") { dimension = "env"; palbase { environment = "staging" } } }
            """.trimIndent(),
        )
        project.environment("staging")

        val failure = project.buildAndFail("generatePalbaseStagingDebug").output

        assertTrue(failure.contains("Script compilation error"), failure)
        assertTrue(failure.contains("INSIDE the build type or product flavor"), failure)
        assertTrue(failure.contains("import io.palbase.gradle.palbase"), failure)
    }
```
  (2/5) şu bloğu:
```kotlin
     * @param android Kotlin DSL placed inside `android { }`, before `buildTypes`.
     * @param imports lines placed above `plugins { }`.
     * @param groovy writes `build.gradle` instead, with [buildTypes] in Groovy.
```
  şununla değiştir:
```kotlin
     * @param android DSL placed inside `android { }`, before `buildTypes` — Groovy when [groovy] is set.
     * @param imports lines placed above `plugins { }`.
     * @param groovy writes `build.gradle` instead, with [buildTypes] and [android] in Groovy.
```
  (3/5) şu bloğu:
```kotlin
            app.resolve("build.gradle").write(groovyBuildScript(buildTypes))
```
  şununla değiştir:
```kotlin
            app.resolve("build.gradle").write(groovyBuildScript(buildTypes, android))
```
  (4/5) şu bloğu:
```kotlin
    private fun groovyBuildScript(buildTypes: String) = """
```
  şununla değiştir:
```kotlin
    private fun groovyBuildScript(buildTypes: String, android: String) = """
```
  (5/5) şu satırlardan sonra:
```kotlin
                targetCompatibility JavaVersion.VERSION_17
            }
```
  şunu ekle:
```kotlin
    """.trimIndent() + "\n" + android + "\n" + """
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.*flavor dsl*' --tests '*PalbaseCodegenPluginTest.flavor blocks outrank*'` · Beklenen: **FAIL** (derleme): `> Task :compileTestKotlin FAILED`, `e: …/EnvironmentResolverTest.kt:595:17 No parameter with name 'flavorDsl' found.` Davranış kırmızısı — yalnız TestKit değişikliğiyle: `4 tests completed, 4 failed`; Kotlin fixture'larında `e: …/build.gradle.kts:19:54: Using 'environment: String?' is an error. Palbase: the environment is chosen PER BUILD TYPE, not in the project-level `palbase { }` block. …` (flavor'daki blok proje bloğuna düşüyor — `flavor blocks outrank the build type keys in gradle properties` dahil), Groovy'de `> Palbase: the environment is chosen PER BUILD TYPE, not in the project-level `palbase { }` block. …`; import'suz test `INSIDE the build type or product flavor`'ı bulamıyor.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/4) şu bloğu:
```kotlin
        // `palbase { environment = "…" }` on every build type, the ones AGP made
        // (debug, release) and every one a script or another plugin adds later.
        // The variant half of this API has nothing to carry: the value is read
        // from the finished DSL below, because a `benchmarkRelease` variant needs
        // the RELEASE build type's value, which its own variant cannot see.
        androidComponents.registerExtension(
            DslExtension.Builder(DSL_NAME).extendBuildTypeWith(PalbaseBuildType::class.java).build(),
        ) { NothingPerVariant }

        val declared = mutableMapOf<String, String>()
        androidComponents.finalizeDsl { dsl ->
            (dsl as CommonExtension<*, *, *, *, *, *>).buildTypes.forEach { buildType ->
                buildType.extensions.getByType(PalbaseBuildType::class.java).environment.orNull
                    ?.let { declared[buildType.name] = it }
            }
        }

        val rootDirectory = project.layout.projectDirectory.dir(project.rootDir.absolutePath)
        val resolver = EnvironmentResolver(sources(project, rootDirectory, declared::get))
```
  şununla değiştir:
```kotlin
        // `palbase { environment = "…" }` on every build type and every product
        // flavor, the ones AGP made (debug, release) and every one a script or
        // another plugin adds later. The variant half of this API has nothing to
        // carry: the value is read from the finished DSL below, because a
        // `benchmarkRelease` variant needs the RELEASE build type's value, which
        // its own variant cannot see.
        androidComponents.registerExtension(
            DslExtension.Builder(DSL_NAME)
                .extendBuildTypeWith(PalbaseBuildType::class.java)
                .extendProductFlavorWith(PalbaseProductFlavor::class.java)
                .build(),
        ) { NothingPerVariant }

        val declared = mutableMapOf<String, String>()
        val declaredByFlavor = mutableMapOf<String, String>()
        androidComponents.finalizeDsl { dsl ->
            val android = dsl as CommonExtension<*, *, *, *, *, *>
            android.buildTypes.forEach { buildType ->
                buildType.extensions.getByType(PalbaseBuildType::class.java).environment.orNull
                    ?.let { declared[buildType.name] = it }
            }
            android.productFlavors.forEach { flavor ->
                flavor.extensions.getByType(PalbaseProductFlavor::class.java).environment.orNull
                    ?.let { declaredByFlavor[flavor.name] = it }
            }
        }

        val rootDirectory = project.layout.projectDirectory.dir(project.rootDir.absolutePath)
        val resolver = EnvironmentResolver(sources(project, rootDirectory, declared::get, declaredByFlavor::get))
```
  (2/4) şu bloğu:
```kotlin
     * - The build-type DSL is the build script itself.
```
  şununla değiştir:
```kotlin
     * - The build-type and product-flavor DSL is the build script itself.
```
  (3/4) şu satırlardan sonra:
```kotlin
        buildTypeDsl: (String) -> String?,
```
  şunu ekle:
```kotlin
        flavorDsl: (String) -> String?,
```
  (4/4) şu satırlardan sonra:
```kotlin
            buildTypeDsl = buildTypeDsl,
```
  şunu ekle:
```kotlin
            flavorDsl = flavorDsl,
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt`:
  (1/6) şu bloğu:
```kotlin
 *  4. `palbase { environment = "…" }` inside build type B.
 *  5. `palbase.env.<V>`, then `palbase.env.<F|Fᵢ|B>`, in the root `gradle.properties`
 *     FILE — the committed one, read as a file. When 4 is set as well, a key
 *     here for the SAME builds as the block — the variant's own, or the block's
 *     build type's — must name the same environment, or REFUSE: two committed
 *     places disagree, and whichever one lost would be a lie the next reader
 *     believes. A key that covers other builds too (a flavor's, under a build
 *     type's block) is not the same choice: 4 outranks it, as any higher place
 *     does. An override is never half of that conflict: it outranked both at 1.
```
  şununla değiştir:
```kotlin
 *  4. `palbase { environment = "…" }` inside the variant's product flavors and
 *     inside build type B — ONE place: blocks that name different
 *     environments are refused, as within any place.
 *  5. `palbase.env.<V>`, then `palbase.env.<F|Fᵢ|B>`, in the root `gradle.properties`
 *     FILE — the committed one, read as a file. When 4 is set as well, a key
 *     here for the SAME builds as a block — the variant's own, or the block's
 *     own flavor (a combination of it too) or build type — must name the same
 *     environment, or REFUSE: two committed places disagree, and whichever one
 *     lost would be a lie the next reader believes. A key that covers other
 *     builds too (a flavor's, under a build type's block, or the other way
 *     round) is not the same choice: 4 outranks it, as any higher place does.
 *     An override is never half of that conflict: it outranked both at 1.
```
  (2/6) şu bloğu:
```kotlin
            val declared = sources.buildTypeDsl(buildType)?.trim()
            if (declared != null) {
                // Only a key for the SAME builds is the block's other committed
                // place; a flavor key covers other builds too, and 4 outranks it.
                listOf(keys.variant, keys.buildType).distinct().firstNotNullOfOrNull(::committed)
                    ?.takeIf { it.environment != declared }
                    ?.let { key ->
                        return EnvironmentResolution.Refused(
                            "Palbase: the `$buildType` build type names two environments in two committed places — " +
                                "`palbase { environment = \"$declared\" }` in the build type and " +
                                "`${key.key}=${key.environment}` in gradle.properties. Keep ONE of them; " +
                                "a build that picked either would leave the other one lying to the next reader.",
                        )
                    }
                return EnvironmentResolution.Selected(declared, "palbase { environment } in the `$buildType` build type")
            }
            ask(keys, ::committed)?.let { return resolution(it) }
```
  şununla değiştir:
```kotlin
            // 4: the variant's flavor blocks and its build type's block are ONE
            // place, so they must agree. Each block's other committed place is a
            // gradle.properties key for the SAME builds — the variant's own, and
            // the block's flavor (or a combination of it) or build type; a key that
            // covers other builds too is outranked by 4, as by any higher place.
            val blocks = flavors.mapNotNull { flavor ->
                sources.flavorDsl(flavor)?.let {
                    declaredIn("the `$flavor` product flavor", it) to
                        listOfNotNull(keys.variant, keys.combination, PROPERTY_PREFIX + flavor)
                }
            } + listOfNotNull(
                sources.buildTypeDsl(buildType)?.let {
                    declaredIn("the `$buildType` build type", it) to listOf(keys.variant, keys.buildType)
                },
            )
            val declared = agree(
                blocks.map { (block, _) -> block },
                remedy = "keep the environment in ONE of those `palbase { }` blocks, or move both to gradle.properties " +
                    "and add `$PROPERTY_PREFIX$subject=<environment>`",
            )
            when (declared) {
                is Answer.Disagreement -> return resolution(declared)
                is Answer.Hit -> {
                    for ((block, sameBuilds) in blocks) {
                        sameBuilds.distinct().firstNotNullOfOrNull(::committed)
                            ?.takeIf { it.environment != block.environment }
                            ?.let { key ->
                                return EnvironmentResolution.Refused(
                                    "Palbase: ${block.key} names two environments in two committed places — " +
                                        "`palbase { environment = \"${block.environment}\" }` in the build script and " +
                                        "`${key.key}=${key.environment}` in gradle.properties. Keep ONE of them; " +
                                        "a build that picked either would leave the other one lying to the next reader.",
                                )
                            }
                    }
                    return resolution(declared)
                }
                null -> ask(keys, ::committed)?.let { return resolution(it) }
            }
```
  (3/6) şu bloğu:
```kotlin
                    "of that flavor, or `$PROPERTY_PREFIX$RELEASE=<environment>` for every release — or "
```
  şununla değiştir:
```kotlin
                    "of that flavor, or `$PROPERTY_PREFIX$RELEASE=<environment>` for every release — or " +
                    "`productFlavors { getByName(\"${flavors.first()}\") { palbase { environment = \"<environment>\" } } }` or "
```
  (4/6) şu satırlardan sonra:
```kotlin
                remedy = "`$PROPERTY_PREFIX$subject=<environment>` in that same place names the one it compiles",
            )
        }
```
  şunu ekle:
```kotlin

        /** A `palbase { environment }` block; its key is the flavor or build type it sits in, in words. */
        private fun declaredIn(owner: String, environment: String) =
            Answer.Hit(owner, environment.trim(), "palbase { environment } in $owner")
```
  (5/6) şu bloğu:
```kotlin
        /** One environment, from [key]; [origin] is how it is printed. */
```
  şununla değiştir:
```kotlin
        /** One environment, from [key] — a property key, or the block's owner; [origin] is how it is printed. */
```
  (6/6) şu satırlardan sonra:
```kotlin
    val buildTypeDsl: (String) -> String?,
```
  şunu ekle:
```kotlin
    /** `palbase { environment }` by PRODUCT FLAVOR name. */
    val flavorDsl: (String) -> String?,
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt`:
  şu bloğu:
```kotlin
    "Palbase: the environment is chosen PER BUILD TYPE, not in the project-level `palbase { }` block. " +
        "Write `palbase { environment = \"<env>\" }` INSIDE the build type and, in a .kts script, add " +
        "`import io.palbase.gradle.palbase` at the top of the file (without it, `palbase { }` inside a build " +
        "type is this project-level block) — or set `palbase.env.<buildType>=<env>` in gradle.properties."
```
  şununla değiştir:
```kotlin
    "Palbase: the environment is chosen PER BUILD TYPE (or product flavor), not in the project-level " +
        "`palbase { }` block. Write `palbase { environment = \"<env>\" }` INSIDE the build type or product flavor " +
        "and, in a .kts script, add `import io.palbase.gradle.palbase` at the top of the file (without it, " +
        "`palbase { }` inside a build type or flavor is this project-level block) — or set " +
        "`palbase.env.<buildType>=<env>` in gradle.properties."
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseProductFlavor.kt` (yeni dosya):
````kotlin
package io.palbase.gradle

import com.android.build.api.dsl.ProductFlavor
import org.gradle.api.Action
import org.gradle.api.provider.Property

/**
 * The environment every build of ONE product flavor compiles, committed beside
 * the flavor it belongs to:
 *
 * ```kotlin
 * import io.palbase.gradle.palbase
 *
 * android {
 *     flavorDimensions += "env"
 *     productFlavors {
 *         create("staging") { dimension = "env"; palbase { environment = "staging" } }
 *         create("prod") { dimension = "env"; palbase { environment = "main" } }
 *     }
 * }
 * ```
 *
 * Groovy needs no import: `staging { dimension 'env'; palbase { environment = 'staging' } }`.
 *
 * It ranks with the build type's own `palbase { environment }`: a flavor and the
 * build type of one variant that BOTH name a stack must name the same one, or
 * that variant is refused — see [EnvironmentResolver].
 */
abstract class PalbaseProductFlavor {
    /** A directory name under `palbase/environments/`, exactly as `palbase link` wrote it. */
    abstract val environment: Property<String>
}

/**
 * `palbase { }` INSIDE a product flavor, for the Kotlin DSL — reached by the
 * same import as the build type's, `io.palbase.gradle.palbase` (see
 * PalbaseBuildType.kt for why an extension on a container element needs one).
 * Without it the block is the project's, where `environment` stops the script
 * compiling and names this fix.
 */
fun ProductFlavor.palbase(configure: Action<in PalbaseProductFlavor>) {
    configure.execute(extensions.getByType(PalbaseProductFlavor::class.java))
}
````
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.*flavor dsl*' --tests '*PalbaseCodegenPluginTest.flavor blocks outrank*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 9s` (EnvironmentResolverTest `tests="48"`, 4 TestKit testi `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 3s`, `EnvironmentResolverTest` `tests="48" … failures="0"`, `PalbaseCodegenPluginTest` `tests="66" … failures="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseProductFlavor.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): product flavor kendi ortamını yazar — palbase { environment } flavor'da; build type'ınkiyle çelişirse o variant reddedilir"`

---

### T012: Düz `benchmark` build type'ı ilk `matchingFallbacks` girdisini ölçer, yoksa `release`
<!-- deps: [T011] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-201] -->

FR-201 adım 6'nın ikinci yarısı. Eski macrobenchmark şablonunun düz `benchmark` build type'ı (`initWith(release)`, `matchingFallbacks += "release"`) ad kuralına düşüyor ve kimsenin link'lemediği `benchmark/`'ı arayıp düşüyordu (`reports/verification-2026-09-25.md` D4: ``> Task :app:generatePalbaseBenchmark FAILED > Palbase: environment `benchmark` (from the build type name) has no directory``; proto `EnvironmentResolver.kt:125-129`). Artık tam olarak `benchmark` adlı build type, `finalizeDsl`'de okunan ilk `matchingFallbacks` girdisinin build'i gibi çözülür (onun DSL'i, anahtarları, ikiz variant anahtarı — `freeBenchmark` → `freeRelease`), girdi yoksa `release` gibi. Kendisi de ölçen bir fallback (`benchmark` ya da `benchmarkRelease`) geri ölçerdi — o durumda `release`. Kendi anahtarı (`palbase.env.benchmark`) hâlâ kazanır.

**Bilinçli değişen mevcut test:** `a name that merely starts with benchmark is its own build type` iki iddia taşıyordu; `benchmark` → `benchmark` (ad kuralı) iddiası spec'in (FR-201.6) değiştirdiği davranıştır ve kaldırılır; `benchmarking` → `benchmarking` iddiası kalır. Yeni davranışı dört yeni birim testi sabitler.

**Interfaces:**
- Consumes: `measuredBuildType(...)`, `twinVariant(...)` (T001); `finalizeDsl` anlık görüntüsü (T004/T011)
- Produces:
  - `EnvironmentSources(..., flavorDsl, matchingFallback: (String) -> String?, gradleProperties)` — build type adına göre İLK `matchingFallbacks` girdisi
  - `EnvironmentResolver.BENCHMARK = "benchmark"`; `measuredBuildType(buildType)` → `benchmark` için fallback ya da `release`; `measuredByName(buildType)` (önekli adlar)
  - Köken metni: ``<origin>, as `benchmark` builds as `<fallback>` ``
  - Test yardımcısı `places(..., fallbacks = mapOf("benchmark" to "…"))`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt`:
  (1/4) şu bloğu:
```kotlin
        assertSelected("benchmark", "from the build type name", places().resolve("benchmark", "benchmark"))
    }
```
  şununla değiştir:
```kotlin
    }

    // 6 (D4). A plain `benchmark` — the older macrobenchmark template:
    // `initWith(release)`, `matchingFallbacks += "release"` — measures the build
    // its FIRST matching fallback names, else `release`. It used to be its own
    // environment, `benchmark/`, which nobody links.
    @Test
    fun `a plain benchmark build type measures its first matching fallback`() {
        val resolver = places(dsl = mapOf("staging" to "qa"), fallbacks = mapOf("benchmark" to "staging"))
        assertSelected(
            "qa",
            "palbase { environment } in the `staging` build type, as `benchmark` builds as `staging`",
            resolver.resolve("benchmark", "benchmark"),
        )
    }

    @Test
    fun `a plain benchmark build type with no fallback measures release`() {
        val resolver = places(gradle = mapOf("palbase.env.release" to "main", "palbase.env.freeRelease" to "free"))
        assertSelected(
            "main",
            "palbase.env.release in gradle.properties, as `benchmark` builds as `release`",
            resolver.resolve("benchmark", "benchmark"),
        )
        assertSelected(
            "free",
            "palbase.env.freeRelease in gradle.properties, as `benchmark` builds as `release`",
            resolver.resolve("freeBenchmark", "benchmark", flavors = listOf("free")),
        )
    }

    // A fallback that is itself a measuring build type would measure straight
    // back — `benchmark` naming itself, say. It measures `release` instead.
    @Test
    fun `a plain benchmark whose fallback measures something itself measures release`() {
        val resolver = places(gradle = mapOf("palbase.env.release" to "main"), fallbacks = mapOf("benchmark" to "benchmark"))
        assertSelected(
            "main",
            "palbase.env.release in gradle.properties, as `benchmark` builds as `release`",
            resolver.resolve("benchmark", "benchmark"),
        )
    }

    // Its own key still wins — the measured build is a convention, not a rule.
    @Test
    fun `a plain benchmark build type can still be set on its own`() {
        val resolver = places(gradle = mapOf("palbase.env.release" to "main", "palbase.env.benchmark" to "perf"))
        assertSelected("perf", "palbase.env.benchmark in gradle.properties", resolver.resolve("benchmark", "benchmark"))
    }
```
  (2/4) şu satırlardan sonra:
```kotlin
                flavorDsl = { read += "flavorDsl:$it"; null },
```
  şunu ekle:
```kotlin
                matchingFallback = { read += "fallback:$it"; null },
```
  (3/4) şu satırlardan sonra:
```kotlin
        flavorDsl: Map<String, String> = emptyMap(),
```
  şunu ekle:
```kotlin
        fallbacks: Map<String, String> = emptyMap(),
```
  (4/4) şu satırlardan sonra:
```kotlin
                flavorDsl = flavorDsl::get,
```
  şunu ekle:
```kotlin
                matchingFallback = fallbacks::get,
```

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertTrue(result.output.contains("as `benchmarkRelease` builds as `release`"), result.output)
    }
```
  şunu ekle:
```kotlin

    // D4: the older macrobenchmark template's plain `benchmark` build type —
    // `initWith(release)`, `matchingFallbacks += "release"` — measures what its
    // first matching fallback names, instead of failing on a `benchmark/`
    // environment nobody links.
    @Test
    fun `a plain benchmark build type compiles the environment of its matching fallback`() {
        val project = fixture(
            buildTypes = """create("benchmark") { initWith(getByName("release")); matchingFallbacks += listOf("release") }""",
        )
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=main\n")

        val result = project.build("generatePalbaseBenchmark")

        assertCompiled(project, "benchmark", "main")
        assertTrue(
            result.output.contains(
                "Palbase: benchmark → main (palbase.env.release in gradle.properties, as `benchmark` builds as `release`)",
            ),
            result.output,
        )
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.a plain benchmark*'` · Beklenen: **FAIL** (derleme): `> Task :compileTestKotlin FAILED`, `e: …/EnvironmentResolverTest.kt:643:17 No parameter with name 'matchingFallback' found.` Davranış kırmızısı — yalnız TestKit değişikliğiyle: `tests="1" … failures="1"`; `a plain benchmark build type compiles the environment of its matching fallback() FAILED` → ``> Palbase: environment `benchmark` (from the build type name) has no directory — …/palbase/environments/benchmark does not exist, and this checkout carries main. …`` (D4).
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/6) şu satırlardan sonra:
```kotlin
        val declaredByFlavor = mutableMapOf<String, String>()
```
  şunu ekle:
```kotlin
        val firstFallback = mutableMapOf<String, String>()
```
  (2/6) şu satırlardan sonra:
```kotlin
                    ?.let { declared[buildType.name] = it }
```
  şunu ekle:
```kotlin
                buildType.matchingFallbacks.firstOrNull()?.let { firstFallback[buildType.name] = it }
```
  (3/6) şu bloğu:
```kotlin
        val resolver = EnvironmentResolver(sources(project, rootDirectory, declared::get, declaredByFlavor::get))
```
  şununla değiştir:
```kotlin
        val resolver = EnvironmentResolver(
            sources(project, rootDirectory, declared::get, declaredByFlavor::get, firstFallback::get),
        )
```
  (4/6) şu bloğu:
```kotlin
     * - The build-type and product-flavor DSL is the build script itself.
```
  şununla değiştir:
```kotlin
     * - The build-type and product-flavor DSL, `matchingFallbacks` included, is
     *   the build script itself.
```
  (5/6) şu satırlardan sonra:
```kotlin
        flavorDsl: (String) -> String?,
```
  şunu ekle:
```kotlin
        matchingFallback: (String) -> String?,
```
  (6/6) şu satırlardan sonra:
```kotlin
            flavorDsl = flavorDsl,
```
  şunu ekle:
```kotlin
            matchingFallback = matchingFallback,
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt`:
  (1/4) şu bloğu:
```kotlin
 *     its stack instead of getting one of their own.
```
  şununla değiştir:
```kotlin
 *     its stack instead of getting one of their own. A plain `benchmark` — the
 *     older macrobenchmark template — measures its FIRST `matchingFallbacks`
 *     entry, else `release`.
```
  (2/4) şu bloğu:
```kotlin
    /** `benchmarkRelease` → `release`, `nonMinifiedRelease` → `release`; anything else → null. */
    private fun measuredBuildType(buildType: String): String? = MEASURING_PREFIXES.firstNotNullOfOrNull { prefix ->
```
  şununla değiştir:
```kotlin
    /**
     * `benchmarkRelease` → `release`, `nonMinifiedRelease` → `release`; a plain
     * `benchmark` → its first matching fallback, else `release`; anything
     * else → null.
     */
    private fun measuredBuildType(buildType: String): String? {
        if (buildType == BENCHMARK) {
            return sources.matchingFallback(buildType)?.trim()
                // A fallback that measures something itself would measure straight back.
                ?.takeIf { it.isNotEmpty() && it != BENCHMARK && measuredByName(it) == null }
                ?: RELEASE
        }
        return measuredByName(buildType)
    }

    private fun measuredByName(buildType: String): String? = MEASURING_PREFIXES.firstNotNullOfOrNull { prefix ->
```
  (3/4) şu satırlardan sonra:
```kotlin
        val MEASURING_PREFIXES = listOf("benchmark", "nonMinified")
```
  şunu ekle:
```kotlin

        /** The older macrobenchmark template's build type: no suffix — its fallbacks name what it measures. */
        const val BENCHMARK = "benchmark"
```
  (4/4) şu satırlardan sonra:
```kotlin
    val flavorDsl: (String) -> String?,
```
  şunu ekle:
```kotlin
    /** The FIRST `matchingFallbacks` entry of a build type, by name — the build a plain `benchmark` measures. */
    val matchingFallback: (String) -> String?,
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.EnvironmentResolverTest' --tests '*PalbaseCodegenPluginTest.a plain benchmark*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 8s` (EnvironmentResolverTest `tests="52"`, TestKit `tests="1"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 4s`, `EnvironmentResolverTest` `tests="52" … failures="0"`, `PalbaseCodegenPluginTest` `tests="67" … failures="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): düz benchmark build type'ı ilk matchingFallbacks girdisini ölçer, yoksa release — adı gibi bir ortam aramaz"`

---

### T013: Modülde hiçbir şeyi adlandırmayan `palbase.env.*` anahtarı uyarılır — nerede yazıldığı ve modülün adları söylenir
<!-- deps: [T006, T012] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-203] -->

FR-203'ün ilk cümlesi, D-010. Bir yazım hatası (`palbase.env.debgu=main`) hiçbir şey seçmiyor ve hiçbir şey söylemiyordu: debug sessizce `local`'de kalıyor, görev UP-TO-DATE (`reports/verification-2026-09-25.md` C3: "Root `palbase.env.debgu=main` → generatePalbaseDebug UP-TO-DATE, APK local, no warning"; proto yalnız V ve B anahtarlarını soruyor, `EnvironmentResolver.kt:50`). Artık modülün `onVariants`'ta gördüğü her ad (variant, build type, her flavor, flavor kombinasyonu) toplanır; komut satırı, override'lar (`providers.gradlePropertiesPrefixedBy("palbase.env.")` eksi komut satırı ve dosya), `local.properties` ve kök `gradle.properties` dosyasındaki her `palbase.env.<X>` anahtarı, X bu adlardan biri değilse bir kez, yerini ve modülün adlarını sayarak **uyarılır** — hata değil: kök dosyayı bütün modüller paylaşır, bir app build type'ının anahtarı library için "bilinmeyen"dir (D-010).

Uyarı yapılandırma zamanında, `project.afterEvaluate`'te basılır: AGP variant'larını kendi `afterEvaluate`'inde yapar ve onu Android uygulanırken, bu plugin kendini yapılandırmadan önce kaydeder — bu yüzden adlar o anda tamdır (AGP 9.1.1'de de ölçüldü, dilim kanıtı). Configuration cache ile: her anahtar kaynağı bir cache girdisidir, anahtarın eklenmesi girişi geçersiz kılar ve uyarı o build'de basılır; aynı giriş yeniden kullanılınca yapılandırma koşmadığı için basılmaz (dilim kanıtı, A4). Flavor kombinasyonunun yazımı T010'un `Keys`'inden `EnvironmentResolver.flavorCombination(...)`'a taşınır — iki yer aynı kuralı kullanır.

**İki yorum bu görevle yanlış olur ve düzeltilir** (kod değişmez): `EnvironmentSources` KDoc'u "a place never read is never a configuration-cache input" diyordu, `places after the first hit are never read` testinin yorumu da tembelliği bir cache-miss gerekçesine bağlıyordu; `unknownKeys` her yapılandırmada iki dosyanın tamamını ve bütün `palbase.env.*` Gradle özelliklerini okur. Yorumlar tembelliği bir SIRA özelliği olarak anlatır — testin sabitlediği şey de bu.

**Interfaces:**
- Consumes: `Keys` (T010); `properties(project, file)`, `GRADLE_PROPERTIES`, `OVERRIDE_ORIGIN` (T009)
- Produces:
  - `EnvironmentResolver.flavorCombination(flavors: List<String>): String?` (companion; `[free, staging]` → `freeStaging`, tek flavor → null)
  - `AndroidVariantIntegration`: `private fun unknownKeys(project: Project, rootDirectory: Directory, names: Set<String>): List<String>`; `afterEvaluate`'te `logger.warn`
  - Uyarı metni: ``Palbase: <where> names no variant, product flavor or build type of <project.displayName> (<adlar, virgülle>), so it chooses nothing here — a typo, or a key meant for another module.`` — `<where>`: `` `-P<key>` on the command line`` · `` `<key>` override from …`` · `` `<key>` in local.properties`` · `` `<key>` in gradle.properties``

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt`:
  şu bloğu:
```kotlin
    // Only the places the answer depended on are read — each lookup is a
    // configuration-cache input, so a read that cannot change the answer is a
    // cache miss for nothing.
```
  şununla değiştir:
```kotlin
    // The ORDER, observed: the resolution asks each place only when it gets
    // there, and stops at the first hit — nothing after it can have changed the
    // answer.
```

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertTrue(result.output.contains("Palbase: release → staging (palbase.env override from "), result.output)
    }
```
  şunu ekle:
```kotlin

    // FR-203 (D-010): a `palbase.env.<X>` key whose X is no variant, product
    // flavor or build type of the module chooses nothing — a typo such as
    // `palbase.env.debgu` used to leave debug on `local` in silence. It is a
    // WARNING, not an error: the root file is shared by every module, and a key
    // for another module's build type is not a mistake.
    @Test
    fun `a key that names nothing in the module is warned about wherever it was set`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.debgu=main\n")

        val result = project.build("generatePalbaseDebug", "-Ppalbase.env.relase=main")

        assertCompiled(project, "debug", "local")
        assertTrue(
            result.output.contains(
                "Palbase: `palbase.env.debgu` in gradle.properties names no variant, product flavor or build type " +
                    "of root project 'codegen-fixture' (debug, release), so it chooses nothing here — a typo, or a " +
                    "key meant for another module.",
            ),
            result.output,
        )
        assertTrue(
            result.output.contains("Palbase: `-Ppalbase.env.relase` on the command line names no variant"),
            result.output,
        )
    }

    // Every name the module HAS is a key — variants, each flavor, the flavor
    // combination, build types — wherever it is set; only the typo is named.
    @Test
    fun `keys for variants flavors combinations and build types are not warned about`() {
        val project = fixture(
            android = """
                flavorDimensions += listOf("tier", "env")
                productFlavors {
                    create("free") { dimension = "tier" }; create("paid") { dimension = "tier" }
                    create("staging") { dimension = "env" }; create("prod") { dimension = "env" }
                }
            """.trimIndent(),
        )
        project.environment("main")
        project.root.resolve("gradle.properties").write(
            listOf("freeStaging", "staging", "free", "paidProdRelease", "release", "debug")
                .joinToString("") { "palbase.env.$it=main\n" },
        )
        project.root.resolve("local.properties").write("palbase.env.freeProdDebug=main\npalbase.env.stagign=main\n")

        val result = project.build("generatePalbaseFreeStagingDebug")

        assertCompiled(project, "freeStagingDebug", "main")
        assertTrue(
            result.output.contains("Palbase: `palbase.env.stagign` in local.properties names no variant"),
            result.output,
        )
        assertEquals(1, Regex("names no variant").findAll(result.output).count(), result.output)
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.a key that names nothing*' --tests '*PalbaseCodegenPluginTest.keys for variants*'` · Beklenen: **FAIL**, `2 tests completed, 2 failed`; `a key that names nothing in the module is warned about wherever it was set() FAILED` — çıktıda yalnız `Palbase: debug → local (the default for debug)` ve `BUILD SUCCESSFUL`, uyarı yok; `keys for variants flavors combinations and build types are not warned about() FAILED` — `Palbase: freeStagingDebug → main (palbase.env.freeStaging in gradle.properties)` ama `stagign` yazım hatası için uyarı yok.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/2) şu bloğu:
```kotlin
        androidComponents.onVariants(androidComponents.selector().all()) { variant ->
            val capitalized = variant.name.replaceFirstChar { it.uppercase() }
            val buildType = variant.buildType ?: variant.name
            val resolution = resolver.resolve(
                variant.name,
                buildType,
                variant.debuggable,
                variant.productFlavors.map { (_, flavor) -> flavor },
            )
```
  şununla değiştir:
```kotlin
        // EVERY name a `palbase.env.<name>` key can mean in this module, from the
        // variants AGP actually made — including build types another plugin adds.
        val names = sortedSetOf<String>()
        androidComponents.onVariants(androidComponents.selector().all()) { variant ->
            val capitalized = variant.name.replaceFirstChar { it.uppercase() }
            val buildType = variant.buildType ?: variant.name
            val flavors = variant.productFlavors.map { (_, flavor) -> flavor }
            names += listOf(variant.name, buildType) + flavors + listOfNotNull(EnvironmentResolver.flavorCombination(flavors))
            val resolution = resolver.resolve(variant.name, buildType, variant.debuggable, flavors)
```
  (2/2) şu satırların hemen ardındaki:
```kotlin
            variant.sources.manifests.addGeneratedManifestFile(task, GeneratePalbaseTask::manifestOutput)
        }
```
  şu bloğu:
```kotlin
    }
```
  şununla değiştir:
```kotlin

        // A KEY THAT NAMES NOTHING HERE chooses nothing, and used to say nothing:
        // `palbase.env.debgu=main` left debug on `local` in silence. Only a
        // warning — the root file is shared by every module, so a key for another
        // module's build type is expected here too. After every variant exists:
        // AGP makes them in its own afterEvaluate, which it registered when
        // Android was applied, before this plugin configured itself.
        project.afterEvaluate {
            unknownKeys(project, rootDirectory, names).forEach { project.logger.warn(it) }
        }
    }

    /**
     * One sentence per `palbase.env.<X>` key, wherever it was set, whose X is no
     * variant, product flavor, flavor combination or build type in [names].
     */
    private fun unknownKeys(project: Project, rootDirectory: Directory, names: Set<String>): List<String> {
        val prefix = EnvironmentResolver.PROPERTY_PREFIX
        val commandLine = project.gradle.startParameter.projectProperties.keys
        val committed = properties(project, rootDirectory.file(GRADLE_PROPERTIES)).stringPropertyNames()
        val local = properties(project, rootDirectory.file(LOCAL_PROPERTIES)).stringPropertyNames()
        val overrides = project.providers.gradlePropertiesPrefixedBy(prefix).get().keys - commandLine - committed
        val seen = commandLine.map { it to "`-P$it` on the command line" } +
            overrides.map { it to "`$it` ${EnvironmentResolver.OVERRIDE_ORIGIN}" } +
            local.map { it to "`$it` in local.properties" } +
            committed.map { it to "`$it` in gradle.properties" }
        return seen
            .filter { (key, _) -> key.startsWith(prefix) && key.removePrefix(prefix) !in names }
            .sortedBy { (key, _) -> key }
            .map { (_, where) ->
                "Palbase: $where names no variant, product flavor or build type of ${project.displayName} " +
                    "(${names.joinToString(", ")}), so it chooses nothing here — a typo, or a key meant for " +
                    "another module."
            }
    }
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt`:
  (1/3) şu bloğu:
```kotlin
        /** `[free, staging]` → `palbase.env.freeStaging`, AGP's own spelling; one flavor is no combination. */
        val combination = flavors.takeIf { it.size > 1 }
            ?.let { PROPERTY_PREFIX + it.first() + it.drop(1).joinToString("") { flavor -> flavor.replaceFirstChar(Char::uppercase) } }
```
  şununla değiştir:
```kotlin
        val combination = flavorCombination(flavors)?.let { PROPERTY_PREFIX + it }
```
  (2/3) şu satırlardan sonra:
```kotlin
        const val BENCHMARK = "benchmark"
```
  şunu ekle:
```kotlin

        /** `[free, staging]` → `freeStaging`, AGP's own spelling; one flavor is no combination. */
        fun flavorCombination(flavors: List<String>): String? = flavors.takeIf { it.size > 1 }
            ?.let { it.first() + it.drop(1).joinToString("") { flavor -> flavor.replaceFirstChar(Char::uppercase) } }
```
  (3/3) şu bloğu:
```kotlin
 * only when the resolution reaches it — a place never read is never a
 * configuration-cache input, and cannot have changed the answer.
```
  şununla değiştir:
```kotlin
 * only when the resolution reaches it, so the order is observable: a place
 * after the first hit is never asked, and cannot have changed the answer. (The
 * files themselves are read whole elsewhere — every `palbase.env.*` key is
 * checked against the module's names — so laziness here is about ORDER, not
 * about which files are configuration-cache inputs.)
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.a key that names nothing*' --tests '*PalbaseCodegenPluginTest.keys for variants*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 8s` (`tests="2" … failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 2s`, `EnvironmentResolverTest` `tests="52" … failures="0"`, `PalbaseCodegenPluginTest` `tests="69" … failures="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/EnvironmentResolverTest.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): modülde hiçbir şeyi adlandırmayan palbase.env.* anahtarı uyarılır — nerede yazıldığı ve modülün adları söylenir; kök dosya paylaşıldığı için hata değil"`

---

### T014: Modülün kendi `gradle.properties`'indeki `palbase.env` satırı reddedilir — kök dosya adlandırılır
<!-- deps: [T007, T009, T013] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-203] -->

FR-203'ün ikinci cümlesi, D-010. Çözüm yalnız kök `gradle.properties` dosyasını okur (T009); `app/gradle.properties`'teki `palbase.env.debug=main` bugün sessizce yok sayılıyor ve debug `local` derleniyor (`reports/verification-2026-09-25.md` C3: "app/gradle.properties `palbase.env.debug=main` → `Palbase: debug → local (the default for debug)`; the APK is local"). Tek kaynak ilkesiyle (D-010) okumak yerine **ret**: modülün kendi dosyası (kök proje değilse) `palbase.env` ya da `palbase.env.*` taşıyorsa o modülün HER variant'ı, bir üretim görevi ÇALIŞTIĞINDA, satırları ve kök dosyanın yolunu adlandırarak reddeder ("Put it in the root gradle.properties"). Yapılandırma — ve IDE sync — geçer (`help` yeşil kalır). Dosya `providers.fileContents` ile okunur, configuration-cache girdisidir.

**Interfaces:**
- Consumes: `properties(project, file)`, `GRADLE_PROPERTIES` (T009); `environmentRefusal` (T002)
- Produces:
  - `AndroidVariantIntegration`: `private fun misplacedLines(project: Project, rootDirectory: Directory): String?` — refusal cümlesi ya da null; `onVariants`'ta `misplaced != null` her şeyden önce `environmentRefusal`'a yazılır
  - Ret metni: ``Palbase: <modül yolu>/gradle.properties sets `<k>=<v>`, …, and a module's own gradle.properties never chooses an environment — nothing reads it there, so it would be ignored in silence. Put it in the root gradle.properties (<kök dosya yolu>), which every module shares, and delete it here.`` (+ görevin eklediği `This checkout carries …`)

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertEquals(1, Regex("names no variant").findAll(result.output).count(), result.output)
    }
```
  şunu ekle:
```kotlin

    // FR-203 (D-010): a MODULE's own gradle.properties is never where the
    // environment is chosen — the resolution reads the root file, so
    // `app/gradle.properties` `palbase.env.debug=main` left debug on `local` in
    // silence. Refused for every variant of that module, when a generation task
    // RUNS: configuration — and an IDE sync — still succeed.
    @Test
    fun `palbase env lines in a module gradle properties are refused naming the root file`() {
        val project = fixture(module = "app")
        project.environment("local", base = project.root)
        project.environment("main", base = project.root)
        project.app.resolve("gradle.properties").write("palbase.env.debug=main\npalbase.env=main\n")

        project.build("help")
        val failure = project.buildAndFail(":app:generatePalbaseDebug").output

        assertTrue(
            failure.contains(
                "Palbase: app/gradle.properties sets `palbase.env=main`, `palbase.env.debug=main`, and a module's own " +
                    "gradle.properties never chooses an environment — nothing reads it there, so it would be ignored " +
                    "in silence. Put it in the root gradle.properties (",
            ),
            failure,
        )
        assertTrue(failure.contains("which every module shares, and delete it here."), failure)
        assertFalse(project.hasAsset("debug"), "a module whose own gradle.properties names an environment was generated")
        assertTrue(project.buildAndFail(":app:generatePalbaseRelease").output.contains("app/gradle.properties sets"))
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.palbase env lines in a module*'` · Beklenen: **FAIL**, `tests="1" … failures="1"`; `palbase env lines in a module gradle properties are refused naming the root file() FAILED` → `UnexpectedBuildSuccess … [:app:generatePalbaseDebug, --stacktrace]` ve çıktıda `Palbase: debug → local (the default for debug)` — modül dosyasındaki `palbase.env.debug=main` sessizce yok sayıldı.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/3) şu satırlardan sonra:
```kotlin
                .map { it.dir(ENVIRONMENTS_PATH) },
        )
```
  şunu ekle:
```kotlin

        // A MODULE'S OWN gradle.properties never chooses: the resolution reads the
        // root file, which every module shares. A `palbase.env*` line there would
        // be ignored in silence, so every variant of this module refuses with it.
        val misplaced = misplacedLines(project, rootDirectory)
```
  (2/3) şu bloğu:
```kotlin
                when (resolution) {
                    is EnvironmentResolution.Selected -> {
                        generate.environment.set(resolution.environment)
                        generate.environmentOrigin.set(resolution.origin)
                    }
                    // Refused when the task RUNS, not here: a release nobody chose a
                    // stack for must not take `assembleDebug` — or an IDE sync — down
                    // with it.
                    is EnvironmentResolution.Refused -> generate.environmentRefusal.set(resolution.reason)
```
  şununla değiştir:
```kotlin
                when {
                    // Refused when the task RUNS, not here: a release nobody chose a
                    // stack for must not take `assembleDebug` — or an IDE sync — down
                    // with it.
                    misplaced != null -> generate.environmentRefusal.set(misplaced)
                    resolution is EnvironmentResolution.Selected -> {
                        generate.environment.set(resolution.environment)
                        generate.environmentOrigin.set(resolution.origin)
                    }
                    resolution is EnvironmentResolution.Refused -> generate.environmentRefusal.set(resolution.reason)
```
  (3/3) şu satırlardan sonra:
```kotlin
            unknownKeys(project, rootDirectory, names).forEach { project.logger.warn(it) }
        }
    }
```
  şunu ekle:
```kotlin

    /**
     * The refusal for `palbase.env` / `palbase.env.*` lines in THIS module's own
     * gradle.properties, or null when there are none — or when this module is
     * the root, whose file is the one that counts.
     */
    private fun misplacedLines(project: Project, rootDirectory: Directory): String? {
        val moduleFile = project.layout.projectDirectory.file(GRADLE_PROPERTIES)
        val rootFile = rootDirectory.file(GRADLE_PROPERTIES).asFile
        if (moduleFile.asFile == rootFile) return null
        val module = properties(project, moduleFile)
        val lines = module.stringPropertyNames()
            .filter { it == EnvironmentResolver.LEGACY_PROPERTY || it.startsWith(EnvironmentResolver.PROPERTY_PREFIX) }
            .sorted()
            .map { "`$it=${module.getProperty(it).trim()}`" }
        if (lines.isEmpty()) return null
        val where = project.rootDir.toPath().relativize(moduleFile.asFile.toPath())
        return "Palbase: $where sets ${lines.joinToString(", ")}, and a module's own gradle.properties never " +
            "chooses an environment — nothing reads it there, so it would be ignored in silence. Put it in the " +
            "root gradle.properties ($rootFile), which every module shares, and delete it here."
    }
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.palbase env lines in a module*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 8s`; tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 9s`, `EnvironmentResolverTest` `tests="52" … failures="0"`, `PalbaseCodegenPluginTest` `tests="70" … failures="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): modülün kendi gradle.properties'indeki palbase.env satırı sessizce yok sayılmaz — o modülün her variant'ı kök dosyayı adlandırarak reddeder"`

---

### T015: `local`'i olmayan checkout'ta debug işe yarayan yolu söyler — boşuna "palbase link" demez
<!-- deps: [T006, T014] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-210] -->

FR-210'un ilk metni, B1. `debug`'ın varsayılanı `local` (D-003) — ama `local/` yalnız `palbase start`'ın bu makinede ayağa kaldırdığı bir yığın için yazılır; buluta link'lenmiş bir checkout'ta hiç yoktur ve genel ret "Run `palbase link` to write it" ile başlıyordu — link onu yazamaz (`reports/verification-2026-09-25.md` B1, S1: "Palbase: environment `local` (the default for debug) has no directory … this checkout carries main. Run `palbase link` to write it, …"). Köken `the default for debug` ise (yeni sabit `DEBUG_DEFAULT_ORIGIN`) ve dizin yoksa görev şunu söyler: "no local stack is linked here; set `palbase.env.debug=<one of: …>`, or run `palbase start` in the backend and then `palbase link` here" — tek ortam varsa onu doğrudan önerir, anahtarın nereye yazılacağını (herkes için KÖK `gradle.properties`, bu makine için kök `local.properties`) ekler — "kök" açıkça: bir modülün kendi `gradle.properties`'i hiçbir şey seçmez (T014). Flavor'lı bir debug için de anahtar build type'ınkidir (`palbase.env.debug`). Harf büyüklüğü ikizi (T006) önce kontrol edilir; açıkça `local` seçilmiş bir variant genel metni alır.

**Interfaces:**
- Consumes: `known` dizin listesi, `buildTypeName` (T006); `DEBUG_DEFAULT` (T001)
- Produces:
  - `EnvironmentResolver.DEBUG_DEFAULT_ORIGIN = "the default for debug"` (adım 9'un kökeni; görev bununla karşılaştırır)
  - Ret metni: ``Palbase: environment `local` (the default for debug) has no directory — no local stack is linked here; set `palbase.env.<B>=<tek ortam | one of: a, b>`, or run `palbase start` in the backend and then `palbase link` here. Set the key in the root gradle.properties for everyone, or in the root local.properties for this machine alone.``

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertFalse(project.hasAsset("release"), "a refused release generated a config")
    }
```
  şunu ekle:
```kotlin

    // FR-210 (B1): debug's default, `local`, exists only where `palbase start`
    // brought a stack up and `palbase link` ran after it. A checkout linked to the
    // cloud carries `main/` and no `local/`, and "Run `palbase link`" — the
    // generic advice — cannot write it. The refusal says what can: map debug to
    // an environment the checkout carries, or start a local stack.
    @Test
    fun `a debug build with no local stack says how to choose one`() {
        val project = fixture()
        project.environment("main")

        val one = project.buildAndFail("generatePalbaseDebug").output
        assertTrue(
            one.contains(
                "Palbase: environment `local` (the default for debug) has no directory — no local stack is linked " +
                    "here; set `palbase.env.debug=main`, or run `palbase start` in the backend and then " +
                    "`palbase link` here. Set the key in the root gradle.properties for everyone, or in the root " +
                    "local.properties for this machine alone.",
            ),
            one,
        )
        assertFalse(one.contains("Run `palbase link` to write it"), one)

        project.environment("staging")
        val two = project.buildAndFail("generatePalbaseDebug").output
        assertTrue(two.contains("set `palbase.env.debug=<one of: main, staging>`, or run `palbase start`"), two)
        assertFalse(project.hasAsset("debug"), "a debug with no local stack generated a config")
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.a debug build with no local stack*'` · Beklenen: **FAIL**, `tests="1" … failures="1"`; `a debug build with no local stack says how to choose one() FAILED` — çıktıda ``> Palbase: environment `local` (the default for debug) has no directory — …/palbase/environments/local does not exist, and this checkout carries main. Run `palbase link` to write it, or choose one this checkout carries for `debug`: …`` (B1'in işe yaramaz ilk önerisi).
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt`:
  (1/2) şu bloğu:
```kotlin
                return EnvironmentResolution.Selected(DEBUG_DEFAULT, "the default for debug")
```
  şununla değiştir:
```kotlin
                return EnvironmentResolution.Selected(DEBUG_DEFAULT, DEBUG_DEFAULT_ORIGIN)
```
  (2/2) şu satırlardan sonra:
```kotlin
        const val DEBUG_DEFAULT = "local"
```
  şunu ekle:
```kotlin

        /** Rule 9's origin — the task words a missing `local/` around it: no link can write one. */
        const val DEBUG_DEFAULT_ORIGIN = "the default for debug"
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  şu satırlardan sonra:
```kotlin
                        "${buildTypeName.get()}=$twin`.",
                )
            }
```
  şunu ekle:
```kotlin
            // THE DEBUG DEFAULT HAS NO LINK TO RUN. `local/` is written only for a
            // stack `palbase start` brought up on this machine; a checkout linked
            // to the cloud never gets one, so "run `palbase link`" cannot help.
            if (origin == EnvironmentResolver.DEBUG_DEFAULT_ORIGIN) {
                val key = "${EnvironmentResolver.PROPERTY_PREFIX}${buildTypeName.get()}"
                val choose = when (known.size) {
                    0 -> ""
                    1 -> "set `$key=${known.single()}`, or "
                    else -> "set `$key=<one of: ${known.joinToString(", ")}>`, or "
                }
                val where = if (known.isEmpty()) {
                    ""
                } else {
                    " Set the key in the root gradle.properties for everyone, or in the root local.properties for " +
                        "this machine alone."
                }
                throw GradleException(
                    "Palbase: environment `$selected` ($origin) has no directory — no local stack is linked here; " +
                        "${choose}run `palbase start` in the backend and then `palbase link` here.$where",
                )
            }
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.a debug build with no local stack*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 5s`; tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 5s`, `EnvironmentResolverTest` `tests="52" … failures="0"`, `PalbaseCodegenPluginTest` `tests="71" … failures="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): local'i olmayan checkout'ta debug işe yarayan yolu söyler — palbase.env.debug=<taşınan ortam> ya da palbase start + link; boşuna \"palbase link\" demez"`

---

### T016: Sözleşmesi olmayan ortam "henüz push yok" der — önce `palbase push --env <ortam>`, sonra link; `local` için önce `palbase start`
<!-- deps: [T006, T007, T015] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-210] -->

FR-210'un ikinci metni, B6. İlk `palbase push`'tan önce link yalnız `android-config.json` yazar; plugin "Palbase Android inputs are incomplete … Run `palbase link`" diyordu — link yeniden koşmak hiçbir şey değiştirmez, çare önce bir push (`reports/verification-2026-09-25.md` B6: 2.3 `GeneratePalbaseTask.kt:124`, proto `:167-171`; S3 aynı metinle düştü). Config var sözleşme yoksa görev artık "environment `<env>` has no contract yet — `palbase push --env <env>`, then `palbase link` here" der ve dizinin hangi dosyayı taşıyıp hangisini taşımadığını söyler.

**`local` bu cümleyi almaz — şartnamenin metninden bilinçli sapma (lead karar günlüğüne yazar).** `palbase start` backend checkout'unu yerel yığına yöneltir (`ReadTarget`: "A running dev stack WINS", palbase-cli `internal/backend/target.go:505` `running.Local = !running.SelfHost`) ve `palbase push` o hedefe basmayı reddeder — "this checkout is pointed at the stack running on this machine, which already serves this directory — a push here would activate a version nothing loads" (`internal/backend/stack_push.go:162`); `--env local` projenin bir ortamı değildir (`internal/backend/environments.go:337` `%q is not an environment of %s`; FR-004 bulutta `local` adlı ortamı zaten atlatır). `local`'in sözleşmesini link çalışan yığından alır (`internal/backend/app_environments.go:685` `fetchStackSpec(ctx, localTarget, …)`) ve o yığın bağladığı dizinin kodunu sunar — çare bir start, SONRA bir link; CLI diliminin FR-009 öneri cümlesiyle ("`palbase start`, then `palbase link` here") aynı yol. (Hepsi palbase-cli `20e5d7e`'de okundu.) Test, yanlış olacak bir cümlenin (`` `palbase push` in the backend ``) basılmadığını da sabitler.

**Interfaces:**
- Consumes: `environmentDirectory`, `selected` (T006/T007); `DEBUG_DEFAULT` (T001)
- Produces:
  - Ret metni: ``Palbase: environment `<env>` has no contract yet — `palbase push --env <env>`, then `palbase link` here. <dizin> has android-config.json but no openapi.json.``
  - `local` için: ``Palbase: environment `local` has no contract yet — run `palbase start` in the backend (its stack serves the contract of the code it runs; `palbase push` does not publish to it), then `palbase link` here. <dizin> has android-config.json but no openapi.json.``

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertTrue(result.output.contains("palbase/environments/local/android-config.json"), result.output)
    }
```
  şunu ekle:
```kotlin

    // FR-210 (B6): before the first `palbase push` an environment has a config
    // and no contract — `palbase link` writes the contract only once a backend
    // is deployed, so "run `palbase link`" cannot help. The cure is a push, THEN
    // a link.
    @Test
    fun `a config with no contract says to push before linking`() {
        val project = fixture(config = true)
        project.environmentFile("main", "android-config.json").write(configFor("main1234m"))
        project.root.resolve("gradle.properties").write("palbase.env.release=main\n")

        val main = project.buildAndFail("generatePalbaseRelease").output
        assertTrue(
            main.contains("Palbase: environment `main` has no contract yet — `palbase push --env main`, then `palbase link` here."),
            main,
        )
        assertTrue(main.contains("has android-config.json but no openapi.json"), main)
    }

    // …except `local`, the stack `palbase start` runs on this machine: nothing
    // pushes to it. `palbase push` refuses a checkout pointed at it (the dev
    // runtime serves the directory it mounted, never a pushed version), and
    // `--env local` names no environment of the project. That stack serves the
    // contract of the code it runs, so the cure is a start, THEN a link.
    @Test
    fun `a local config with no contract says to start the stack before linking`() {
        val project = fixture(config = true)

        val local = project.buildAndFail("generatePalbaseDebug").output

        assertTrue(
            local.contains(
                "Palbase: environment `local` has no contract yet — run `palbase start` in the backend (its stack " +
                    "serves the contract of the code it runs; `palbase push` does not publish to it), then " +
                    "`palbase link` here.",
            ),
            local,
        )
        assertTrue(local.contains("has android-config.json but no openapi.json"), local)
        assertFalse(local.contains("`palbase push` in the backend"), local)
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.a config with no contract*' --tests '*PalbaseCodegenPluginTest.a local config with no contract*'` · Beklenen: **FAIL**, `2 tests completed, 2 failed`; `a config with no contract says to push before linking() FAILED` — çıktıda `Palbase: release → main (palbase.env.release in gradle.properties)` ve ``> Palbase Android inputs are incomplete for environment `main`. Run `palbase link` to write …/palbase/environments/main/openapi.json and …`` (B6: link bunu yazamaz); `a local config with no contract says to start the stack before linking() FAILED` — aynı genel metin, `local` için: ``> Palbase Android inputs are incomplete for environment `local`. Run `palbase link` to write …``.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  şu satırlardan sonra:
```kotlin
        val configFile = environmentDirectory.resolve(CONFIG_FILE).takeIf { it.isFile }
```
  şunu ekle:
```kotlin
        // A CONFIG WITH NO CONTRACT is an environment nothing was pushed to yet:
        // `palbase link` writes the contract once a backend is deployed, so
        // linking again changes nothing — the cure is a push, THEN a link.
        // Except `local`, the stack `palbase start` runs on this machine: nothing
        // pushes to it. `palbase push` refuses a checkout pointed at it (the dev
        // runtime serves the directory it mounted, never a pushed version), and
        // `--env local` names no environment of the project. That stack serves
        // the contract of the code it runs, so there the cure is a start, THEN a
        // link.
        if (configFile != null && openApiFile == null) {
            val cure = if (selected == EnvironmentResolver.DEBUG_DEFAULT) {
                "run `palbase start` in the backend (its stack serves the contract of the code it runs; " +
                    "`palbase push` does not publish to it)"
            } else {
                "`palbase push --env $selected`"
            }
            throw GradleException(
                "Palbase: environment `$selected` has no contract yet — $cure, then `palbase link` here. " +
                    "$environmentDirectory has $CONFIG_FILE but no $SPEC_FILE.",
            )
        }
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.a config with no contract*' --tests '*PalbaseCodegenPluginTest.a local config with no contract*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 7s`, `tests="2" skipped="0" failures="0" errors="0"`; tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 3s`, `EnvironmentResolverTest` `tests="52" … failures="0"`, `PalbaseCodegenPluginTest` `tests="73" … failures="0"`. Tüketici düzeyinde (planlama koşusu, AGP 8.11.1, `local/` yalnız config taşırken `:app:generatePalbaseDebug`): ``> Palbase: environment `local` has no contract yet — run `palbase start` in the backend (its stack serves the contract of the code it runs; `palbase push` does not publish to it), then `palbase link` here. …/palbase/environments/local has android-config.json but no openapi.json.``
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): sözleşmesi olmayan ortam \"henüz push yok\" der — önce palbase push --env <ortam>, sonra link; local için palbase start, sonra link — CLI yerel yığına push'u reddediyor"`

---

### T017: Üretim satırı okuduğu kökü söyler — `Palbase: <variant> → <env> (<köken>) [<kök>]`
<!-- deps: [T002, T007, T016] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-211] -->

FR-211 ve D3a'nın ikinci yarısı. Satır seçilen ortamı ve kökenini söylüyordu, hangi `palbase/environments`'ı okuduğunu söylemiyordu: modülde kalmış eski bir kopya ile checkout kökü aynı satırı basıyordu (`reports/verification-2026-09-25.md` D3a: `Palbase: debug → local (the default for debug)` ve APK `base_url = https://STALE-module-copy.envprobe.palbase.studio`; proto `GeneratePalbaseTask.kt:163`). Kök artık kök projeye göre GÖRELİ yazılır — `palbase/environments`, `app/palbase/environments`, React Native'de `../palbase/environments` (T018) — ve satırı okuyan hangi kopyanın derlendiğini görür. Görev kök projenin dizinini `@Internal` bir özellik olarak alır: yalnız yazdırmak için; girdi değildir, aynı içerik başka bir kökten okunduğunda çıktı da aynıdır.

Ölçülen sınır: iki kök BİREBİR aynı içeriği taşıyorsa görev UP-TO-DATE kalır (`environmentFiles` `PathSensitivity.RELATIVE` ile parmak izlenir; `local/openapi.json` iki kökte de aynı göreli yol) ve satır yeniden basılmaz — çıktı da değişmemiştir. Test bu yüzden modül kopyasına başka bir yığın yazar ve asset'ten de ölçer. Mevcut satır iddiaları `contains` ile önek aradığı için değişmez.

**Interfaces:**
- Consumes: `GeneratePalbaseTask.environmentRoots` (T007); üretim satırı (T002)
- Produces:
  - `GeneratePalbaseTask.rootProjectDirectory: DirectoryProperty` — `@Internal`; `AndroidVariantIntegration` kök projenin dizinini (`rootDirectory`) verir
  - Satır: `Palbase: <variant> → <env> (<origin>) [<kök, kök projeye göre göreli>]`
  - `GeneratePalbaseTask`: `private fun shown(directory: java.io.File): String` — `relativeToOrSelf(rootProjectDirectory)`; sonraki görevlerin bütün kök metinleri bunu kullanır

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırların hemen ardındaki:
```kotlin
        assertTrue(project.asset("debug").contains("shared1234m"), project.asset("debug"))
```
  şu bloğu:
```kotlin
    }
```
  şununla değiştir:
```kotlin
    }

    // FR-211: the line names the palbase/environments it READ, relative to the
    // root project. A module's own copy and the checkout root's used to print
    // the same line, so nothing on screen told a stale copy from the real one.
    @Test
    fun `the generation line names the root it read`() {
        val project = fixture(module = "app")
        project.environment("local", base = project.root)

        val atRoot = project.build("generatePalbaseDebug").output
        assertTrue(atRoot.contains("Palbase: debug → local (the default for debug) [palbase/environments]\n"), atRoot)

        project.root.resolve("palbase").toFile().deleteRecursively()
        project.environmentFile("local", "openapi.json").write(OPEN_API)
        project.environmentFile("local", "android-config.json").write(configFor("module1234m"))
        val inModule = project.build("generatePalbaseDebug").output
        assertTrue(
            inModule.contains("Palbase: debug → local (the default for debug) [app/palbase/environments]\n"),
            inModule,
        )
        assertTrue(project.asset("debug").contains("module1234m"), project.asset("debug"))
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the generation line names*'` (`JAVA_HOME` = Android Studio JBR, `ANDROID_HOME` ayarlı; `:test` şart — iki nokta olmadan `--tests` `:codegen-engine:test`'e de gider) · Beklenen: **FAIL**, `PalbaseCodegenPluginTest` `tests="1" … failures="1"`, `BUILD FAILED in 5s`; `the generation line names the root it read() FAILED` — çıktıda `Palbase: debug → local (the default for debug)`, kök yok.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  şu satırlardan sonra:
```kotlin
                generate.environmentRoots.set(environmentRoots)
```
  şunu ekle:
```kotlin
                generate.rootProjectDirectory.set(rootDirectory)
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  (1/3) şu satırlardan sonra:
```kotlin
    abstract val environmentRoots: ListProperty<Directory>
```
  şunu ekle:
```kotlin

    /** The root project's directory: a root is printed relative to it. */
    @get:Internal
    abstract val rootProjectDirectory: DirectoryProperty
```
  (2/3) şu bloğu:
```kotlin
        logger.lifecycle("Palbase: ${variantName.get()} → $selected ($origin)")
```
  şununla değiştir:
```kotlin
        // WHICH palbase/environments, too: a module's own copy and the checkout
        // root's printed the same line, so a stale copy compiled in silence.
        logger.lifecycle("Palbase: ${variantName.get()} → $selected ($origin) [${shown(root)}]")
```
  (3/3) şu satırlardan sonra:
```kotlin
                    "the machine's LAN address from a physical device.",
            )
        }
    }
```
  şunu ekle:
```kotlin

    /** [directory] as it is printed: relative to the root project, `app/palbase/environments`. */
    private fun shown(directory: java.io.File): String =
        directory.relativeToOrSelf(rootProjectDirectory.get().asFile).path
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the generation line names*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 7s`; tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 4s`, `EnvironmentResolverTest` `tests="52" skipped="0" failures="0" errors="0"`, `PalbaseCodegenPluginTest` `tests="74" skipped="0" failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): üretim satırı okuduğu kökü de söyler — Palbase: <variant> → <env> (<köken>) [<kök>]; modülün kopyası ile checkout kökününki artık aynı satırı basmıyor"`

---

### T018: Kök projenin bir üstü de aranır (React Native / Flutter) — kök proje checkout'un kendisiyse (`.git` ya da `palbase/project.json`) aranmaz
<!-- deps: [T009, T017] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-205] -->

FR-205'in `<rootDir>/../palbase/environments` adayı, D3b. React Native ve Flutter'da `palbase/` repo kökündedir, Gradle `android/`'da koşar: ne modül ne kök proje onu taşır ve build config'siz, tek satır basmadan yeşil geçiyordu (`reports/verification-2026-09-25.md` D3b: `> Task :app:generatePalbaseDebug`, `BUILD SUCCESSFUL`, `unzip -l app-debug.apk | grep -ci palbase` = 0). Aday modül ve kök köklerinden SONRA sorulur. Sınır şartnamenin cümlesiyle ("üstünde `.git` ya da `palbase/project.json` ile sınırlı"): kök proje `.git` (dizin ya da worktree/submodule dosyası) ya da `palbase/project.json` taşıyorsa checkout'un kendisidir ve üstüne çıkılmaz — sıradan bir Android checkout'unda (kökte `.git`) üst dizin hiç okunmaz. Yorum: sınır yalnız kök projede ölçülür, üst dizinin kendisinin bir işaret taşıması istenmez — RN'de `palbase link` orada `palbase/project.json`'u zaten yazar.

Sınır GÖREV KOŞARKEN ölçülür: `CheckoutMarker` bir `ValueSource`'tur ve yalnız görevin `@Input`'u (`rootProjectIsCheckout`) onu okur; yapılandırmada sorgulanmayan bir ValueSource configuration cache yeniden kullanıldığında da yeniden hesaplanır. Test bunu ölçer: `.git` eklendikten sonraki build `Configuration cache entry reused` basar ve asset kaybolur (görev yeniden koştu, üst kök artık okunmadı). Girdi bir Boolean'dır, yol değil — build cache taşınabilir kalır. Üst adayın ağacı `environmentFiles`'a eklenir; açık `environmentsDir` varsa üst aday yoktur. `PalbaseExtension.environmentsDir` KDoc'u aramayı anlatır.

**Interfaces:**
- Consumes: `environmentRoots`, `environmentFiles` (T007); `rootProjectDirectory`, `shown(...)` (T017)
- Produces:
  - `GeneratePalbaseTask.environmentsAboveRoot: ListProperty<Directory>` — `@Internal`; boş (açık `environmentsDir`) ya da tek dizin `<kök proje>/../palbase/environments`
  - `GeneratePalbaseTask.rootProjectIsCheckout: Property<Boolean>` — `@Input`; `true` iken üst aday aranmaz
  - `internal abstract class CheckoutMarker : ValueSource<Boolean, CheckoutMarker.Parameters>` (`AndroidVariantIntegration.kt`), `Parameters.directory: DirectoryProperty`; `obtain()` = `.git` var mı ya da `palbase/project.json` dosya mı
  - Aday sırası: `environmentRoots` (açık dizin, ya da modül → kök proje), sonra `environmentsAboveRoot` (sınır açıksa)

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertTrue(project.asset("debug").contains("module1234m"), project.asset("debug"))
    }
```
  şunu ekle:
```kotlin

    // FR-205 (D3b): React Native and Flutter keep `palbase/` at the repository
    // root and run Gradle in `android/`, so neither the module nor the root
    // project holds it — the build was green with no config in the APK. The
    // directory ABOVE the root project is searched too, but only while the root
    // project is not itself the checkout: a `.git` or a `palbase/project.json`
    // there bounds the search. Measured when the task runs, so a `.git` that
    // appears changes the answer from the same configuration cache entry.
    @Test
    fun `the directory above the root project is searched unless the root project is the checkout`() {
        val project = fixture(module = "app")
        project.environment("local", base = project.root.parent)

        val above = project.build("generatePalbaseDebug", "--configuration-cache").output
        assertTrue(above.contains("Palbase: debug → local (the default for debug) [../palbase/environments]\n"), above)
        assertCompiled(project, "debug", "local")

        Files.createDirectories(project.root.resolve(".git"))
        val bounded = project.build("generatePalbaseDebug", "--configuration-cache").output
        assertTrue(bounded.contains("Configuration cache entry reused"), bounded)
        assertFalse(project.hasAsset("debug"), "a .git in the root project did not bound the search")

        Files.delete(project.root.resolve(".git"))
        project.build("generatePalbaseDebug", "--configuration-cache")
        assertCompiled(project, "debug", "local")
        project.root.resolve("palbase/project.json").write("{}")
        project.build("generatePalbaseDebug", "--configuration-cache")
        assertFalse(project.hasAsset("debug"), "a palbase/project.json in the root project did not bound the search")
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the directory above the root project*'` · Beklenen: **FAIL**, `tests="1" … failures="1"`, `BUILD FAILED in 5s`; `the directory above the root project is searched unless the root project is the checkout() FAILED` — çıktıda yalnız `> Task :app:generatePalbaseDebug`, `BUILD SUCCESSFUL in 3s`, `Configuration cache entry stored.`: Palbase satırı yok, asset yok (D3b).
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/4) şu bloğu:
```kotlin
import org.gradle.api.file.RegularFile
```
  şununla değiştir:
```kotlin
import org.gradle.api.file.DirectoryProperty
import org.gradle.api.file.RegularFile
import org.gradle.api.provider.ValueSource
import org.gradle.api.provider.ValueSourceParameters
```
  (2/4) şu satırlardan sonra:
```kotlin
                .map { it.dir(ENVIRONMENTS_PATH) },
        )
```
  şunu ekle:
```kotlin
        // …and ONE directory above the root project: React Native and Flutter
        // keep `palbase/` beside `android/`, the directory Gradle runs in. Only
        // while the root project is not the checkout itself — the task decides
        // that when it runs, from `rootProjectIsCheckout`.
        val environmentsAboveRoot = extension.environmentsDir.map { emptyList<Directory>() }.orElse(
            listOfNotNull(
                project.rootDir.parentFile
                    ?.let { project.layout.projectDirectory.dir(it.absolutePath).dir(ENVIRONMENTS_PATH) },
            ),
        )
        val rootProjectIsCheckout = project.providers.of(CheckoutMarker::class.java) {
            it.parameters.directory.set(rootDirectory)
        }
```
  (3/4) şu bloğu:
```kotlin
                generate.rootProjectDirectory.set(rootDirectory)
                // EVERY environment, not just the selected one — see the property's
                // own note in GeneratePalbaseTask: the SET is part of the input.
                generate.environmentFiles.from(environmentRoots)
```
  şununla değiştir:
```kotlin
                generate.environmentsAboveRoot.set(environmentsAboveRoot)
                generate.rootProjectIsCheckout.set(rootProjectIsCheckout)
                generate.rootProjectDirectory.set(rootDirectory)
                // EVERY environment, not just the selected one — see the property's
                // own note in GeneratePalbaseTask: the SET is part of the input.
                generate.environmentFiles.from(environmentRoots, environmentsAboveRoot)
```
  (4/4) şu satırlardan sonra:
```kotlin
    private const val GRADLE_PROPERTIES = "gradle.properties"
}
```
  şunu ekle:
```kotlin

/**
 * Whether [Parameters.directory] is the TOP of a checkout: it holds `.git` (a
 * directory, or the file a worktree or submodule has) or `palbase/project.json`,
 * which `palbase link` writes at the checkout root. A ValueSource that only a
 * task input reads is computed when the task RUNS, also from a reused
 * configuration cache entry — a `.git` that appears is seen.
 */
internal abstract class CheckoutMarker : ValueSource<Boolean, CheckoutMarker.Parameters> {
    interface Parameters : ValueSourceParameters {
        val directory: DirectoryProperty
    }

    override fun obtain(): Boolean {
        val directory = parameters.directory.get().asFile
        return directory.resolve(".git").exists() || directory.resolve("palbase/project.json").isFile
    }
}
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  (1/2) şu satırlardan sonra:
```kotlin
    abstract val environmentRoots: ListProperty<Directory>
```
  şunu ekle:
```kotlin

    /**
     * `<root project>/../palbase/environments` — React Native and Flutter keep
     * `palbase/` beside `android/`, where Gradle runs — or nothing when a block
     * names the root. Searched after [environmentRoots], and only when
     * [rootProjectIsCheckout] is false. Tracked through `environmentFiles`.
     */
    @get:Internal
    abstract val environmentsAboveRoot: ListProperty<Directory>

    /**
     * Whether the root project IS the checkout — it holds `.git` or
     * `palbase/project.json` — so nothing above it is searched. An input,
     * measured when the task runs: a `.git` that appears changes which root is
     * read without changing anything else this task depends on.
     */
    @get:Input
    abstract val rootProjectIsCheckout: Property<Boolean>
```
  (2/2) şu bloğu:
```kotlin
        val root = environmentRoots.get().map { it.asFile }.firstOrNull { it.isDirectory } ?: return
```
  şununla değiştir:
```kotlin
        val above = environmentsAboveRoot.get().takeUnless { rootProjectIsCheckout.get() }.orEmpty()
        val root = (environmentRoots.get() + above).map { it.asFile }.firstOrNull { it.isDirectory } ?: return
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt`:
  şu bloğu:
```kotlin
     * UNSET, it is FOUND: the first of `<module>/palbase/environments` and
     * `<root project>/palbase/environments` that exists when generation runs —
     * which covers the module that carries its own `palbase/` and the checkout
     * `palbase link` wrote at its root. Set it only for a layout neither reaches.
```
  şununla değiştir:
```kotlin
     * UNSET, it is FOUND: the first of `<module>/palbase/environments`,
     * `<root project>/palbase/environments` and — while the root project holds
     * neither `.git` nor `palbase/project.json`, i.e. is not the checkout itself
     * — `<root project>/../palbase/environments` that exists when generation
     * runs. That covers the module that carries its own `palbase/`, the checkout
     * `palbase link` wrote at its root, and React Native / Flutter, where Gradle
     * runs in `android/` beside `palbase/`. Set it only for a layout none reaches.
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the directory above the root project*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 7s` (PalbaseCodegenPluginTest `tests="1"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 4s`, `EnvironmentResolverTest` `tests="52"`, `PalbaseCodegenPluginTest` `tests="75"` — hepsi `failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): React Native / Flutter düzeni bulunur — kök projenin bir üstündeki palbase/environments da aranır, kök proje .git ya da palbase/project.json taşımadıkça; sınır görev koşarken ölçülür"`

---

### T019: Birden fazla kök erişimdeyse reddedilir, hepsi adlandırılır — modülde kalmış eski kopya sessizce kazanmaz
<!-- deps: [T018] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-205] -->

FR-205'in belirsizlik reddi, D3a. `palbase link` bir modülün içinde koşunca oraya bir kopya yazar (palbase-cli `link_artifacts.go:141` `os.Getwd()`; FR-016 CLI tarafında bunu reddedecek) ve ilk bulunan kök kazanıyordu: checkout kökü güncelken debug build'i eski modül kopyasını derledi (D3a: APK `base_url = https://STALE-module-copy…`). Artık aday köklerden birden fazlası diskte VARSA o modülün her üretim görevi, KOŞARKEN, hepsini kök projeye göre göreli adlandırarak reddeder — hangisinin güncel olduğu buradan bilinemez. Çare: güncel olmayanı silmek ya da `palbase { environmentsDir.set(…) }` ile hangisinin okunacağını söylemek. Açık blok tek kök olduğundan belirsizlik doğmaz: T007'nin `an explicit environments dir is the only root read` testi (kökte de bir kopya varken) aynen geçer. Yapılandırma ve IDE sync etkilenmez. Prototipin "modül kökü yener" testi slice 1'de bilerek taşınmamıştı (plugin-1-port açık konusu); silinen ya da zayıflatılan test yok.

**Interfaces:**
- Consumes: `environmentsAboveRoot`, `rootProjectIsCheckout` (T018); `shown(...)` (T017)
- Produces:
  - Ret metni: ``Palbase: `<variant>` has more than one palbase/environments in reach — <kök>, <kök>. Whichever one this build read, the other would say something else: a `palbase link` run inside a module leaves a copy there. Delete the one that is not current, or name the one to read with `palbase { environmentsDir.set(…) }`.``
  - Görevde `found: List<File>` (var olan adaylar); T020 onu `searched` ile tamamlar

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertFalse(project.hasAsset("debug"), "a palbase/project.json in the root project did not bound the search")
    }
```
  şunu ekle:
```kotlin

    // FR-205 (D3a): `palbase link` run inside the app module leaves a copy
    // there, and the module copy WON in silence — a debug build compiled that
    // stale stack while the checkout root held the current one. More than one
    // root in reach is a refusal that names every one of them.
    @Test
    fun `two environment roots in reach are refused naming both`() {
        val project = fixture(module = "app")
        project.environment("local", base = project.root)
        project.environmentFile("local", "openapi.json").write(OPEN_API)
        project.environmentFile("local", "android-config.json").write(configFor("stale1234m"))

        val stale = project.buildAndFail("generatePalbaseDebug").output
        assertTrue(
            stale.contains(
                "Palbase: `debug` has more than one palbase/environments in reach — app/palbase/environments, " +
                    "palbase/environments.",
            ),
            stale,
        )
        assertFalse(project.hasAsset("debug"), "the stale module copy was compiled")

        project.app.resolve("palbase").toFile().deleteRecursively()
        project.environment("local", base = project.root.parent)
        val above = project.buildAndFail("generatePalbaseDebug").output
        assertTrue(above.contains("in reach — palbase/environments, ../palbase/environments."), above)
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.two environment roots*'` · Beklenen: **FAIL**, `tests="1" … failures="1"`, `BUILD FAILED in 5s`; `two environment roots in reach are refused naming both() FAILED` → `UnexpectedBuildSuccess … [generatePalbaseDebug, --stacktrace]`, çıktıda `Palbase: debug → local (the default for debug) [app/palbase/environments]` — eski modül kopyası sessizce derlendi.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  (1/2) şu bloğu:
```kotlin
     * Where `palbase/environments` may sit, in priority order; the first that
     * EXISTS when the task runs is read. Tracked through `environmentFiles`.
```
  şununla değiştir:
```kotlin
     * Where `palbase/environments` may sit. The one that EXISTS when the task
     * runs is read; more than one is refused. Tracked through `environmentFiles`.
```
  (2/2) şu bloğu:
```kotlin
        val root = (environmentRoots.get() + above).map { it.asFile }.firstOrNull { it.isDirectory } ?: return
```
  şununla değiştir:
```kotlin
        val found = (environmentRoots.get() + above).map { it.asFile }.filter { it.isDirectory }
        val root = found.firstOrNull() ?: return
        // ONE ROOT, OR NONE. `palbase link` run inside a module leaves a copy
        // there, and the first root used to win in silence — a debug build
        // compiled that stale stack while the checkout root held the current
        // one. Which copy is current cannot be told from here.
        if (found.size > 1) {
            throw GradleException(
                "Palbase: `${variantName.get()}` has more than one palbase/environments in reach — " +
                    "${found.joinToString(", ") { shown(it) }}. Whichever one this build read, the other would say " +
                    "something else: a `palbase link` run inside a module leaves a copy there. Delete the one that " +
                    "is not current, or name the one to read with `palbase { environmentsDir.set(…) }`.",
            )
        }
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt`:
  (1/2) şu bloğu:
```kotlin
     * UNSET, it is FOUND: the first of `<module>/palbase/environments`,
```
  şununla değiştir:
```kotlin
     * UNSET, it is FOUND: the one of `<module>/palbase/environments`,
```
  (2/2) şu bloğu:
```kotlin
     * runs in `android/` beside `palbase/`. Set it only for a layout none reaches.
```
  şununla değiştir:
```kotlin
     * runs in `android/` beside `palbase/`. Two that exist are refused, naming
     * both. Set it only for a layout none reaches, or to say which one counts.
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.two environment roots*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 7s` (PalbaseCodegenPluginTest `tests="1"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 8s`, `EnvironmentResolverTest` `tests="52"`, `PalbaseCodegenPluginTest` `tests="76"` — hepsi `failures="0" errors="0"`. Tüketici düzeyinde (planlama koşusu, s3-rn, AGP 8.11.1, modülde bayat kopya; metin bu görevde değişmedi): ``> Palbase: `debug` has more than one palbase/environments in reach — app/palbase/environments, ../palbase/environments. Whichever one this build read, the other would say something else: a `palbase link` run inside a module leaves a copy there. Delete the one that is not current, or name the one to read with `palbase { environmentsDir.set(…) }`.``
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseExtension.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): erişimde birden fazla palbase/environments varsa reddedilir, hepsi adlandırılır — modülde kalmış eski kopya artık sessizce kazanmıyor"`

---

### T020: Hiçbir kök bulunamazsa satır aranan her yeri sayar — build yeşil kalır
<!-- deps: [T019] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-205] -->

FR-205'in son cümlesi, D3b. "Henüz link yok" hata değildir — eklenti link'ten önce uygulanabilir ve boş bir üretilmiş kaynak kümesi build'i ayakta tutar — ama SESSİZDİ: React Native checkout'u config'siz APK üretip tek satır basmıyordu (D3b; 2.3 `GeneratePalbaseTask.kt:126` `firstOrNull { it.isDirectory } ?: return`). Artık görev dönmeden önce bir lifecycle satırı basar ve aranan her adayı kök projeye göre göreli sayar; sınır kapalıysa (kök proje checkout'un kendisi) üst aday listede yoktur. `missing both inputs is a no-op` aynen geçer (SUCCESS).

**Interfaces:**
- Consumes: `found`, `environmentsAboveRoot`, `rootProjectIsCheckout` (T018/T019); `shown(...)` (T017)
- Produces:
  - Satır: ``Palbase: <variant> → nothing generated: no palbase/environments found (searched <adaylar>). Run `palbase link` at the checkout root to write one.``

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertTrue(above.contains("in reach — palbase/environments, ../palbase/environments."), above)
    }
```
  şunu ekle:
```kotlin

    // FR-205: finding nothing is still not an error — the plugin can be applied
    // before `palbase link` ever ran — but it is no longer SILENT. A React
    // Native checkout built green with no config in the APK and not one line
    // saying why; the line names every place that was searched.
    @Test
    fun `no environments root found says where it looked`() {
        val project = fixture(module = "app")

        val result = project.build("generatePalbaseDebug")

        assertEquals(TaskOutcome.SUCCESS, result.task(":app:generatePalbaseDebug")?.outcome)
        assertTrue(
            result.output.contains(
                "Palbase: debug → nothing generated: no palbase/environments found (searched " +
                    "app/palbase/environments, palbase/environments, ../palbase/environments). Run `palbase link` " +
                    "at the checkout root to write one.\n",
            ),
            result.output,
        )
        assertFalse(project.hasAsset("debug"))
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.no environments root found*'` · Beklenen: **FAIL**, `tests="1" … failures="1"`, `BUILD FAILED in 5s`; `no environments root found says where it looked() FAILED` — çıktıda yalnız `> Task :app:generatePalbaseDebug` ve `BUILD SUCCESSFUL in 3s`, satır yok.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  şu bloğu:
```kotlin
        // and it is handled below.
        val above = environmentsAboveRoot.get().takeUnless { rootProjectIsCheckout.get() }.orEmpty()
        val found = (environmentRoots.get() + above).map { it.asFile }.filter { it.isDirectory }
        val root = found.firstOrNull() ?: return
```
  şununla değiştir:
```kotlin
        // and it is handled below. But it is SAID, with every place searched: a
        // React Native checkout built green with no config and not one line.
        val above = environmentsAboveRoot.get().takeUnless { rootProjectIsCheckout.get() }.orEmpty()
        val searched = (environmentRoots.get() + above).map { it.asFile }
        val found = searched.filter { it.isDirectory }
        val root = found.firstOrNull() ?: run {
            logger.lifecycle(
                "Palbase: ${variantName.get()} → nothing generated: no palbase/environments found (searched " +
                    "${searched.joinToString(", ") { shown(it) }}). Run `palbase link` at the checkout root to " +
                    "write one.",
            )
            return
        }
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.no environments root found*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 7s` (PalbaseCodegenPluginTest `tests="1"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 8s`, `EnvironmentResolverTest` `tests="52"`, `PalbaseCodegenPluginTest` `tests="77"` — hepsi `failures="0" errors="0"`. Tüketici düzeyinde (planlama koşusu, s3-rn, `android/.git` varken; metin bu görevde değişmedi): ``Palbase: debug → nothing generated: no palbase/environments found (searched app/palbase/environments, palbase/environments). Run `palbase link` at the checkout root to write one.`` ve `BUILD SUCCESSFUL` — sınır üst dizini listeden düşürür.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "feat(codegen): palbase/environments bulunamayınca susmaz — bir lifecycle satırı aranan her yeri sayar, build yeşil kalır"`

---

### T021: Debuggable olmayan variant loopback ya da `http` base_url'i reddeder — cleartext izni ona hiç yazılmaz
<!-- deps: [T006, T008, T020] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-206] -->

FR-206, C4 ve B8. Bir release'i `local`'e — ya da loopback bir `main/`'e — eşleyen her yol YEŞİL bir release APK'sı üretiyordu: `base_url = http://127.0.0.1:54321`, manifest'te cleartext network-security-config (C4, fx/c4: `aapt2 dump xmltree` → `cleartextTrafficPermitted=true`, `android:debuggable` yok; B8, S4: `palbase.env.release=main` + loopback `main/` → `BUILD SUCCESSFUL`). Slice 2 `local.properties`'i release'te saymaz yaptı (T008), ama `gradle.properties`, `-P` ve DSL yolu açıktı. Kapı AGP'nin kendi cevabıdır: `variant.debuggable` (`Component.getDebuggable()` AGP 8.10.1 ve 9.1.1'de; slice 2 onu zaten `onVariants`'ta okuyor) görevin `@Input`'u olur — build type'ın debuggable'ı değişirse görev yeniden koşar. Debuggable olmayan variant'ta `base_url`'in host'u loopback'se (`localhost`, `127.0.0.1`, `::1`, `[::1]`) ya da şeması `http` ise görev, config asset'ini, manifest girdisini ve cleartext res'ini yazmadan reddeder (üretilmiş Kotlin o ana kadar yazılmıştır; görev düştüğü için hiçbiri paketlenmez); `https://127.0.0.1` de reddedilir (şartname: "loopback ya da http"). `http` zaten yalnız loopback'te kabul ediliyor (`shared/src/main/kotlin/io/palbase/config/BackendURL.kt:12-14`), dolayısıyla uzak bir `http` ayrı bir dal değildir. `initWith(debug)` ile yapılmış bir feature build type'ı debuggable'dır (D-019) ve yerel yığını cleartext izniyle korur — bekçi testi, önceden de geçer.

Ölçülen: reddedilen variant'ta seçim satırı (`Palbase: release → local (…) [..]`) ret'ten önce basılır — seçim doğrudur, yığın reddedilir. Test yardımcısı `hasCleartextAllowance(variant)`: AGP her görevin üretilen res'ini `build/generated/res/<görev>/` altına taşır (asset'in `build/generated/assets/<görev>/`'ü gibi); görevin kendi `generated/palbase/<variant>/res` yolunu arayan ilk yazım bu yüzden boşuna geçiyordu. Loopback host listesi `LOOPBACK_HOSTS` sabitine alınır; emülatör uyarısının dört karşılaştırması da onu kullanır.

Çare cümlesi KÖK `gradle.properties` der (``… `palbase.env.<variant>=<environment>` in the root gradle.properties …``) ve test bunu da tutar: bir modülün kendi dosyasındaki satır hiçbir şey seçmez (T014).

**Interfaces:**
- Consumes: `variant.debuggable` (T008'in `onVariants`'ı); `environment`, `environmentOrigin`, `variantName` (T002); `validateConfig(...)`
- Produces:
  - `GeneratePalbaseTask.debuggable: Property<Boolean>` — `@Input`; `AndroidVariantIntegration` `variant.debuggable`'ı verir
  - `GeneratePalbaseTask` companion: `val LOOPBACK_HOSTS = setOf("localhost", "127.0.0.1", "::1", "[::1]")`
  - Ret metni: ``Palbase: `<variant>` is not debuggable, and environment `<env>` (<origin>) has base_url <url> — a loopback address[, over plain HTTP]. A build that can ship never talks to a stack on the machine that built it: no device reaches that address, and the cleartext allowance for it would ship too. Choose a deployed environment for `<variant>` — `palbase.env.<variant>=<environment>` in the root gradle.properties — or build a debuggable variant against the local stack.``
  - Test yardımcısı (Fixture): `fun hasCleartextAllowance(variant: String): Boolean` — `build/generated/res/generatePalbase<V>/xml/palbase_network_security_config.xml`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  (1/2) şu satırlardan sonra:
```kotlin
        assertTrue(result.output.contains("Palbase: featureX → main (palbase.env.featureX in local.properties)"), result.output)
    }
```
  şunu ekle:
```kotlin

    // FR-206 (C4, B8): a build that can ship never talks to a stack on the
    // machine that built it. `release` mapped to a `local` whose base_url is
    // http://127.0.0.1 made a GREEN release APK, a cleartext
    // network-security-config in it. A variant that is not debuggable refuses a
    // loopback or plain-http base_url — https on loopback too — and the
    // cleartext allowance is never written for it.
    @Test
    fun `a variant that is not debuggable refuses a loopback stack and ships no cleartext allowance`() {
        val project = fixture(openApi = true)
        project.environmentFile("local", "android-config.json").write(CONFIG_LOOPBACK)
        project.root.resolve("gradle.properties").write("palbase.env.release=local\n")

        val http = project.buildAndFail("generatePalbaseRelease").output

        assertTrue(
            http.contains(
                "Palbase: `release` is not debuggable, and environment `local` (palbase.env.release in " +
                    "gradle.properties) has base_url http://127.0.0.1:54321 — a loopback address, over plain HTTP.",
            ),
            http,
        )
        assertTrue(http.contains("`palbase.env.release=<environment>` in the root gradle.properties"), http)
        assertFalse(project.hasAsset("release"), "a release aimed at loopback was generated")
        assertFalse(project.hasCleartextAllowance("release"), "a release carries a cleartext allowance")

        project.environmentFile("local", "android-config.json").write(
            CONFIG_LOOPBACK.replace("http://127.0.0.1:54321", "https://127.0.0.1:54321"),
        )
        val https = project.buildAndFail("generatePalbaseRelease").output
        assertTrue(https.contains("has base_url https://127.0.0.1:54321 — a loopback address."), https)
    }

    // The gate is DEBUGGABILITY, not the name `debug`: a feature build type made
    // with `initWith(debug)` is debuggable (D-019) and keeps the local stack,
    // cleartext allowance included.
    @Test
    fun `a debuggable build type other than debug keeps the local stack`() {
        val project = fixture(openApi = true, buildTypes = """create("featureX") { initWith(getByName("debug")) }""")
        project.environmentFile("local", "android-config.json").write(CONFIG_LOOPBACK)
        project.root.resolve("gradle.properties").write("palbase.env.featureX=local\n")

        project.build("generatePalbaseFeatureX")

        assertTrue(project.asset("featureX").contains("http://127.0.0.1:54321"), project.asset("featureX"))
        assertTrue(project.hasCleartextAllowance("featureX"), "a debuggable build type lost its cleartext allowance")
    }
```
  (2/2) şu satırlardan sonra:
```kotlin
        fun hasAsset(variant: String): Boolean = Files.isRegularFile(assetFile(variant))
```
  şunu ekle:
```kotlin

        /** Whether ONE variant packs the cleartext allowance — AGP keeps each task's generated res apart too. */
        fun hasCleartextAllowance(variant: String): Boolean {
            val task = "generatePalbase" + variant.replaceFirstChar { it.uppercase() }
            return Files.isRegularFile(app.resolve("build/generated/res/$task/xml/palbase_network_security_config.xml"))
        }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.a variant that is not debuggable*' --tests '*PalbaseCodegenPluginTest.a debuggable build type other than debug*'` · Beklenen: **FAIL**, `2 tests completed, 1 failed`, `BUILD FAILED in 6s`; `a variant that is not debuggable refuses a loopback stack and ships no cleartext allowance() FAILED` → `UnexpectedBuildSuccess … [generatePalbaseRelease, --stacktrace]`, çıktıda `Palbase: release → local (palbase.env.release in gradle.properties) [palbase/environments]`, emülatör uyarısı ve `BUILD SUCCESSFUL in 448ms`. `a debuggable build type other than debug keeps the local stack` önceden de geçer (bekçi: kapı adla değil debuggable'la).
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  şu satırlardan sonra:
```kotlin
                generate.buildTypeName.set(buildType)
```
  şunu ekle:
```kotlin
                generate.debuggable.set(variant.debuggable)
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  (1/4) şu satırlardan sonra:
```kotlin
    abstract val buildTypeName: Property<String>
```
  şunu ekle:
```kotlin

    /**
     * AGP's own answer for this variant. One that is NOT debuggable can ship,
     * so it refuses a stack on the machine that built it — a loopback or
     * plain-http base_url — and never gets the cleartext allowance.
     */
    @get:Input
    abstract val debuggable: Property<Boolean>
```
  (2/4) şu satırlardan sonra:
```kotlin
        val loopback = address.scheme == "http"
```
  şunu ekle:
```kotlin
        // A BUILD THAT CAN SHIP NEVER TALKS TO THE MACHINE THAT BUILT IT. A
        // `release` mapped to `local` made a green APK aimed at 127.0.0.1, with
        // the cleartext allowance for it inside. No device reaches that address;
        // the allowance would ship anyway. Refused before anything is written.
        if (!debuggable.get() && (loopback || plainHost in LOOPBACK_HOSTS)) {
            val variant = variantName.get()
            throw GradleException(
                "Palbase: `$variant` is not debuggable, and environment `${environment.get()}` " +
                    "(${environmentOrigin.get()}) has base_url $baseUrl — a loopback address" +
                    "${if (loopback) ", over plain HTTP" else ""}. A build that can ship never talks to a stack on " +
                    "the machine that built it: no device reaches that address, and the cleartext allowance for it " +
                    "would ship too. Choose a deployed environment for `$variant` — " +
                    "`${EnvironmentResolver.PROPERTY_PREFIX}$variant=<environment>` in the root gradle.properties — or " +
                    "build a debuggable variant against the local stack.",
            )
        }
```
  (3/4) şu bloğu:
```kotlin
        if (target.host == "127.0.0.1" || target.host == "localhost" || target.host == "::1" || target.host == "[::1]") {
```
  şununla değiştir:
```kotlin
        if (target.host in LOOPBACK_HOSTS) {
```
  (4/4) şu satırlardan sonra:
```kotlin
        const val EMULATOR_HOST_ALIAS = "10.0.2.2"
```
  şunu ekle:
```kotlin

        /** The hosts `backendURI` lets speak plain HTTP: this machine, by any of its names. */
        val LOOPBACK_HOSTS = setOf("localhost", "127.0.0.1", "::1", "[::1]")
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.a variant that is not debuggable*' --tests '*PalbaseCodegenPluginTest.a debuggable build type other than debug*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 8s` (PalbaseCodegenPluginTest `tests="2"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 8s`, `EnvironmentResolverTest` `tests="52"`, `PalbaseCodegenPluginTest` `tests="79"` — hepsi `failures="0" errors="0"`. Tüketici düzeyinde (planlama koşusu, s3-rn, `:app:assembleRelease -Ppalbase.env.release=local`; cümlenin sonu o koşuda henüz "in gradle.properties" diyordu): ``> Palbase: `release` is not debuggable, and environment `local` (-Ppalbase.env.release on the command line) has base_url http://127.0.0.1:54321 — a loopback address, over plain HTTP. …`` ve `EXIT 1`; `:app:assembleDebug -Ppalbase.env.debug=local` → `BUILD SUCCESSFUL`, `unzip -l app-debug.apk | grep -c palbase_network_security_config` = 1.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): debuggable olmayan variant loopback ya da http base_url'i reddeder — release APK'sı artık 127.0.0.1'e ve cleartext izniyle yeşil çıkmıyor"`

---

### T022: Paketlenen config derlendiği ortamı adlandırır — `palbase_environment`
<!-- deps: [T002, T021] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt, palbe-core/src/test/kotlin/io/palbase/core/GeneratedConfigLoaderTest.kt] | satisfies: [FR-207] -->

FR-207, C5 ve D-017. Üretim satırı görev UP-TO-DATE ya da FROM-CACHE olduğunda basılmıyor ve APK hangi ortamdan derlendiğini söylemiyordu (C5, fx/c2: ikinci `clean assembleDebug --build-cache` → `> Task :app:generatePalbaseDebug FROM-CACHE`, satır yok; asset anahtarları yalnız `['api_key', 'app_id', 'base_url']`). Asset artık CLI'ın config'ini AYNEN taşır ve sonuna `"palbase_environment":"<env>"` ekler; aynı adlı bir alan gelirse eklentininki geçerlidir (CLI böyle bir alan yazmaz). Köken (origin) yazılmaz: `environmentOrigin` `@Internal`'dır; asset'e girseydi bir girdi olması gerekirdi. Runtime alanı okumaz: `parseGeneratedConfig` → `palbaseJson` `ignoreUnknownKeys = true` (`palbe-core/src/main/kotlin/io/palbase/core/Codec.kt:23-24`); bunu runtime'da bir bekçi testi sabitler (`GeneratedConfigLoaderTest`, runtime kodu değişmez — D-005).

**Bilinçli değişen mevcut test:** `OAuth snapshot is read from the environment config and emits exact callback filters` asset'in CLI config'ine BİREBİR eşit olduğunu sabitliyordu; FR-207 tam olarak bunu değiştirir. Test artık "CLI config'i aynen + yalnız `palbase_environment`" der — OAuth alt ağacının dokunulmadan geçtiği iddiası korunur.

**Interfaces:**
- Consumes: `selected` ve `validateConfig(...)` (T002); `SPEC_FILE`/`CONFIG_FILE` companion'ı
- Produces:
  - `GeneratePalbaseTask` companion: `const val ENVIRONMENT_FIELD = "palbase_environment"`
  - Asset `palbase/palbase-config.json` = CLI config'i + `"palbase_environment":"<env>"` (son alan)
  - Runtime bekçisi: `GeneratedConfigLoaderTest.the environment name the plugin packs is ignored`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  (1/2) şu bloğu:
```kotlin
        assertEquals(kotlinx.serialization.json.Json.parseToJsonElement(expected), asset)
```
  şununla değiştir:
```kotlin
        val cli = kotlinx.serialization.json.Json.parseToJsonElement(expected) as kotlinx.serialization.json.JsonObject
        // The CLI's config passes through WHOLE; the plugin adds only the name of
        // the environment it was compiled from (FR-207).
        assertEquals(
            kotlinx.serialization.json.JsonObject(cli + ("palbase_environment" to kotlinx.serialization.json.JsonPrimitive("local"))),
            asset,
        )
```
  (2/2) şu satırlardan sonra:
```kotlin
        assertTrue(project.hasCleartextAllowance("featureX"), "a debuggable build type lost its cleartext allowance")
    }
```
  şunu ekle:
```kotlin

    // FR-207 (C5): the generation line is gone whenever the task is UP-TO-DATE
    // or comes FROM-CACHE, and nothing in the APK said which environment it was
    // built from. The packed config names it.
    @Test
    fun `the packed config names the environment it was compiled from`() {
        val project = fixture()
        project.environment("local")
        project.environment("main")
        project.root.resolve("gradle.properties").write("palbase.env.release=main\n")

        project.build("generatePalbaseDebug", "generatePalbaseRelease")

        assertTrue(project.asset("debug").contains("\"palbase_environment\":\"local\""), project.asset("debug"))
        assertTrue(project.asset("release").contains("\"palbase_environment\":\"main\""), project.asset("release"))
    }
```

  `palbe-core/src/test/kotlin/io/palbase/core/GeneratedConfigLoaderTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertEquals(withoutRef, withStaleRef)
    }
```
  şunu ekle:
```kotlin

    /**
     * The Gradle plugin names the environment a build was compiled from in the
     * packed config (`palbase_environment`, plugin 2.5). The runtime reads
     * nothing from it, and a key it does not know never refuses the config —
     * an app built with the 2.5 plugin starts on this runtime unchanged.
     */
    @Test
    fun `the environment name the plugin packs is ignored`() {
        val plain = parseGeneratedConfig(
            """{"app_id":"app_android","base_url":"https://abc12345m.dev.palbase.studio","api_key":"pb_abc12345m_c0123456789abcdefghij"}"""
                .encodeToByteArray(),
        )
        val named = parseGeneratedConfig(
            """{"app_id":"app_android","base_url":"https://abc12345m.dev.palbase.studio","api_key":"pb_abc12345m_c0123456789abcdefghij","palbase_environment":"main"}"""
                .encodeToByteArray(),
        )
        assertEquals(plain, named)
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the packed config names*' --tests '*PalbaseCodegenPluginTest.OAuth snapshot is read*'` · Beklenen: **FAIL**, `2 tests completed, 2 failed`, `BUILD FAILED in 10s`; `the packed config names the environment it was compiled from() FAILED` → `{"app_id":"app_android","base_url":"https://local1234m.dev.palbase.studio","api_key":"pb_local1234m_c0123456789abcdefghij"} ==> expected: <true> but was: <false>`; `OAuth snapshot is read from the environment config and emits exact callback filters() FAILED` → `expected: <{… "return_uris":["https://example.com:8443/oauth/github"]}]},"palbase_environment":"local"}> but was: <{… "return_uris":["https://example.com:8443/oauth/github"]}]}}>`. Runtime bekçisi (repo kökünde, T021 ağacı + bu görevin runtime testi) `./gradlew :palbe-core:testDebugUnitTest --offline --tests 'io.palbase.core.GeneratedConfigLoaderTest'` → `BUILD SUCCESSFUL in 3s`, `tests="14" skipped="0" failures="0" errors="0"` — önceden de geçer: runtime bilinmeyen alanı zaten yok sayıyor.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt`:
  (1/2) şu bloğu:
```kotlin
            writeText(authTarget.config.toString())
```
  şununla değiştir:
```kotlin
            // …and WHICH environment it is. The generation line is gone whenever
            // this task is UP-TO-DATE or FROM-CACHE, and nothing in the APK said;
            // the runtime ignores a key it does not read.
            writeText(JsonObject(authTarget.config + (ENVIRONMENT_FIELD to JsonPrimitive(selected))).toString())
```
  (2/2) şu satırlardan sonra:
```kotlin
        const val CONFIG_FILE = "android-config.json"
```
  şunu ekle:
```kotlin

        /** The packed config's field naming the environment it was compiled from. */
        const val ENVIRONMENT_FIELD = "palbase_environment"
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the packed config names*' --tests '*PalbaseCodegenPluginTest.OAuth snapshot is read*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 16s` (PalbaseCodegenPluginTest `tests="2"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 6s`, `EnvironmentResolverTest` `tests="52"`, `PalbaseCodegenPluginTest` `tests="80"` — hepsi `failures="0" errors="0"`. Tüketici düzeyinde (planlama koşusu, AGP 8.11.1, `unzip -p app-release-unsigned.apk assets/palbase/palbase-config.json`): `{"app_id":"project","base_url":"https://8bbwb2pbm.palbase.studio","api_key":"pb_project_c0123456789abcdefghijKLMN","palbase_environment":"main"}`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt palbe-core/src/test/kotlin/io/palbase/core/GeneratedConfigLoaderTest.kt && git commit -m "feat(codegen): paketlenen palbase-config.json derlendiği ortamı adlandırır — palbase_environment; runtime bilmediği alanı yok sayar, bunu bir runtime testi sabitler"`

---

### T023: Library'nin fallback variant'ı başka ortam paketlerse o app variant'ı düşer — `pre<Variant>Build`, configuration cache yeniden kullanıldığında da
<!-- deps: [T010, T013, T022] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/LibraryFallbackCheckTest.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-208] -->

FR-208 ve D-002 (D1, blocker). Eklenti bir library'de, app'te library'nin tanımlamadığı bir build type var (`featureX`, `matchingFallbacks += "debug"`): AGP library'nin `debug`'ını — ve onun yığınını — `featureX` APK'sına koyar. Prototipin düzeltme turundaki uyarısı configuration cache yeniden kullanılınca tamamen sessizdi ve APK yine yanlış yığını taşıdı (`reports/verification-2026-09-25.md` D1: `app-staging.apk: base_url = https://prod.envprobe.palbase.studio`, EXIT=0, `palbase.env.staging=staging` commit edilmişken). Slice 1 prototipin `LibraryFallbackCheck`'ini bilerek taşımadı (plugin-1-port açık konusu); saptama burada başarısızlık tasarımına taşınır:

- Library `gradle.projectsEvaluated`'da her `com.android.application` modülünün bitmiş DSL'inden variant'larını çıkarır (bu görevde build type başına bir variant; flavor'lar T024).
- Bir app variant'ı library'yi paketliyor mu: variant'ın `<variant>RuntimeClasspath`'i library'yi — doğrudan ya da ara modüllerin runtime classpath'leri üzerinden — BİLDİRİYOR mu (çözülmez, yalnız bildirim okunur). Ara modül yolu TestKit'te de ölçülür (`:app` → `:core` → `:lib`: `an app variant that packs the library through another module fails too`) — çok modüllü bir app'in olağan şekli; bu yürüyüş bozulursa D1'in blocker'ı sessizce geri gelir. `debugImplementation(project(":lib"))` yalnız `debug`'a ulaşır; prototipin "herhangi bir configuration" grafiği test/`compileOnly` bağımlılıklarında yanlış alarm verirdi.
- Karşılaştırma saf bir fonksiyondur (`findings`): app variant'ının KENDİ seçeceği — aynı çözücü, aynı yerler, app build type'ının `isDebuggable`'ı (D-009 kapısı app variant'ına göre) — ile AGP'nin paketlediği library variant'ının derlediği. Library variant'ı reddedilmişse kendi görevi düşer; app build type'ının library'de hiçbir fallback'i yoksa AGP kendisi yüksek sesle düşer — ikisi de atlanır.
- Fark varsa o app variant'ının `pre<Variant>Build`'ine `doFirst(Refusal(message))`: `Refusal` tek bir String taşıyan düz bir sınıf, configuration cache onu görevle saklar (D1'in init-script probe'u ölçmüştü; burada TestKit: `Configuration cache entry reused` + `> Task :app:preFeatureXBuild FAILED`). Diğer variant'lar ve yapılandırma — IDE sync'in koştuğu kısım — etkilenmez (tüketici L0: `:app:help` `BUILD SUCCESSFUL`).
- `palbase.env.<app variant>=<fallback ortamı>` iki cevabı eşitler (D-002'nin kasıt anahtarı).

Prototipin yapılandırma zamanı uyarısı ve `alsoPackedInto` son eki taşınmaz: ret aynı bilgiyi taşır, uyarı ise ilgisiz her build'de (`assembleDebug`) gürültüydü (D1 (b)). Prototipin 7 birim testi yeni veri tipleriyle taşınır (`naming the fallback's environment … silences it` → `… settles it`), 1 yeni test app variant'ının debuggable'ını sabitler; prototipin flavor'lı library testi T024'te yeniden yazılır. `EnvironmentResolver` KDoc'u "variant" kelimesinin library için ne demek olduğunu söyler.

Ret ve çare KÖK `gradle.properties` der (``… say so: `palbase.env.featureX=local` in the root gradle.properties.``): bu düzende app modülü eklentiyi UYGULAMAZ, dolayısıyla `app/gradle.properties`'e yazılan kasıt satırını T014'ün reddi yakalamaz — sessizce yok sayılır ve ret nedenini söylemeden tekrarlanırdı.

**TestKit koşucusu `--no-watch-fs` ile koşar** (`Fixture.runner()`): planlama koşusunda kasıt adımı `--configuration-cache` ile koşarken tam koşuda bir kez (6 koşudan 1) daemon yeni yazılan kök `gradle.properties`'i görmeden girişi yeniden kullandı (`Reusing configuration cache.` → eski ret). Olası neden dosya izleme: bir test dosyayı yazıp HEMEN yeniden build eder, düzenlemenin olayı henüz ulaşmamış bir izleme anlık görüntüsü giriş parmak izini değişmemiş gösterir. Bu görev koşucuya `--no-watch-fs` ekler ve kasıt adımını yeniden `--configuration-cache` ile koşturur (`Configuration cache entry reused` BASMAMALI). Ölçülen sınır: kırılganlık yalnız koşturulan tek testte yeniden üretilemedi (bayraksız 12/12 yeşil); bayrakla bu testin kendisi ve tam `:test` ardışık koşularda yeşil (sayılar Adım 4'te). Kök neden kanıtlanmadı; bayrak T002/T007'nin configuration-cache testlerini de aynı riskten korur.

**Interfaces:**
- Consumes: `EnvironmentResolver.resolve(variant, buildType, debuggable, flavors)` (T010); `onVariants`'taki `resolution` (T008–T012)
- Produces:
  - `internal object LibraryFallbackCheck` (yeni dosya `LibraryFallbackCheck.kt`):
    - `data class AppVariant(name, buildType, debuggable, buildTypeFallbacks: List<String>)` (T024 alan ekler)
    - `data class LibraryVariant(name, buildType, resolution: EnvironmentResolution)` (T024 alan ekler)
    - `data class Finding(appVariant: String, message: String)`
    - `fun register(library: Project, resolver: EnvironmentResolver, libraryVariants: List<LibraryVariant>)` (T025 `then` ekler)
    - `fun findings(appPath, libraryPath, appVariants, libraryVariants, resolver): List<Finding>` — saf
    - `internal class Refusal(message: String) : Action<Task>`
  - `AndroidVariantIntegration`: `libraryVariants` her variant'ta doldurulur; `LibraryAndroidComponentsExtension` ise `register(...)`
  - Test yardımcısı (PalbaseCodegenPluginTest): `private fun libraryAndApp(appAndroid: String = "", appBuildTypes: String = ""): Fixture` — `:lib` eklentiyi uygular, `:app` ona bağlı ve uygulamaz; dönen fixture `:lib`'in
  - Ret metni: ``Palbase: `<app>` variant `<V>` packs `<lib>`'s `<L>` build — <neden> — and with it the Palbase client and config `<lib>` generated for `<L>`: `<env>` (<origin>). `<V>` itself would select `<env'>` (<origin'>). Declare <ne> in `<lib>` too (<DSL>), or, if `<env>` is what `<V>` should compile, say so: `palbase.env.<V>=<env>` in the root gradle.properties.``
  - `Fixture.runner()` argümanları: `--stacktrace`, `--no-watch-fs`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/LibraryFallbackCheckTest.kt` (yeni dosya):
```kotlin
package io.palbase.gradle

import io.palbase.gradle.LibraryFallbackCheck.AppVariant
import io.palbase.gradle.LibraryFallbackCheck.LibraryVariant
import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.Test

/**
 * A library that lacks an app build type is packed into it through
 * matchingFallbacks — the rule that decides when that hands the app variant a
 * stack it would not have selected. The TestKit suite proves the library reads
 * the app and fails the right task; this pins the decision.
 */
class LibraryFallbackCheckTest {

    private val gradle = mapOf("palbase.env.debug" to "main", "palbase.env.release" to "main")

    private val featureX = app("featureX", debuggable = true, fallbacks = listOf("debug"))

    @Test
    fun `an app build type the library lacks is reported with the environment it actually gets`() {
        val finding = check(listOf(featureX), places(gradle = gradle)).single()

        assertEquals("featureX", finding.appVariant)
        assertTrue(finding.message.contains("`:app` variant `featureX` packs `:lib`'s `debug` build"), finding.message)
        assertTrue(finding.message.contains("`main` (palbase.env.debug in gradle.properties)"), finding.message)
        assertTrue(finding.message.contains("would select `featureX` (from the build type name)"), finding.message)
        assertTrue(finding.message.contains("create(\"featureX\") { initWith(getByName(\"debug\")) }"), finding.message)
        assertTrue(finding.message.contains("`palbase.env.featureX=main`"), finding.message)
    }

    // The same answer both ways is not a fall-back anybody suffers from — and it
    // is how a team says "featureX runs on main, on purpose".
    @Test
    fun `naming the fallback's environment for the app variant settles it`() {
        val findings = check(listOf(featureX), places(gradle = gradle + ("palbase.env.featureX" to "main")))
        assertEquals(emptyList<LibraryFallbackCheck.Finding>(), findings)
    }

    @Test
    fun `build types the library declares are not reported`() {
        val findings = check(listOf(app("debug", debuggable = true), app("release")), places(gradle = gradle))
        assertEquals(emptyList<LibraryFallbackCheck.Finding>(), findings)
    }

    // AGP itself fails the resolution when no fallback matches — loud already.
    @Test
    fun `an app build type with no usable fallback is left to AGP`() {
        val findings = check(
            listOf(app("featureX", debuggable = true), app("qa", fallbacks = listOf("staging"))),
            places(gradle = gradle),
        )
        assertEquals(emptyList<LibraryFallbackCheck.Finding>(), findings)
    }

    // The first fallback the library HAS is the one AGP picks.
    @Test
    fun `the first fallback the library declares is the one compared`() {
        val findings = check(
            listOf(app("staging", fallbacks = listOf("qa", "release", "debug"))),
            places(gradle = gradle + ("palbase.env.staging" to "main")),
            library = listOf(
                LibraryVariant("debug", "debug", EnvironmentResolution.Selected("local", "the default for debug")),
                LibraryVariant("release", "release", EnvironmentResolution.Selected("main", "a")),
            ),
        )
        assertEquals(emptyList<LibraryFallbackCheck.Finding>(), findings)
    }

    // benchmarkRelease measures release, and falls back to it: same stack, nothing to say.
    @Test
    fun `a benchmark build type that falls back to release is not reported`() {
        val findings = check(listOf(app("benchmarkRelease", fallbacks = listOf("release"))), places(gradle = gradle))
        assertEquals(emptyList<LibraryFallbackCheck.Finding>(), findings)
    }

    // A fallback that cannot generate at all fails its own task; nothing to compare.
    @Test
    fun `a refused fallback is left to its own task`() {
        val findings = check(
            listOf(app("staging", fallbacks = listOf("release"))),
            places(),
            library = listOf(LibraryVariant("release", "release", EnvironmentResolution.Refused("no release"))),
        )
        assertEquals(emptyList<LibraryFallbackCheck.Finding>(), findings)
    }

    // What the app variant WOULD select is asked as that variant: a line in
    // local.properties counts for a debuggable app build type and not for one
    // that can ship (D-009) — exactly as it would if the app applied the plugin.
    @Test
    fun `the app variant's own debuggability decides whether local properties count`() {
        val resolver = places(
            gradle = gradle,
            local = mapOf("palbase.env.featureX" to "main", "palbase.env.staging" to "main"),
        )
        val findings = check(listOf(featureX, app("staging", fallbacks = listOf("release"))), resolver)

        assertEquals(listOf("staging"), findings.map { it.appVariant })
        assertTrue(findings.single().message.contains("would select `staging` (from the build type name)"), findings.single().message)
    }

    private fun app(buildType: String, debuggable: Boolean = false, fallbacks: List<String> = emptyList()) =
        AppVariant(name = buildType, buildType = buildType, debuggable = debuggable, buildTypeFallbacks = fallbacks)

    private fun check(
        appVariants: List<AppVariant>,
        resolver: EnvironmentResolver,
        library: List<LibraryVariant> = listOf(
            LibraryVariant("debug", "debug", EnvironmentResolution.Selected("main", "palbase.env.debug in gradle.properties")),
            LibraryVariant("release", "release", EnvironmentResolution.Selected("main", "palbase.env.release in gradle.properties")),
        ),
    ) = LibraryFallbackCheck.findings(":app", ":lib", appVariants, library, resolver)

    private fun places(gradle: Map<String, String> = emptyMap(), local: Map<String, String> = emptyMap()) =
        EnvironmentResolver(
            EnvironmentSources(
                commandLine = { null },
                overrides = { null },
                localProperties = local::get,
                buildTypeDsl = { null },
                flavorDsl = { null },
                matchingFallback = { null },
                gradleProperties = gradle::get,
            ),
        )
}
```

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  (1/3) şu satırlardan sonra:
```kotlin
        project.environment("main")

        project.build("generatePalbaseDebug", "generatePalbaseRelease")

        assertCompiled(project, "debug", "local")
        assertCompiled(project, "release", "main")
    }
```
  şunu ekle:
```kotlin

    // FR-208 (D1, D-002): the LIBRARY holds the Palbase client, and the app has a
    // build type the library does not declare. AGP packs the library's
    // matchingFallbacks variant — and its stack — into it: the `featureX` APK
    // shipped `local` while `featureX` would select `featureX`, and the warning
    // that used to say so was silent on configuration-cache reuse. That app
    // variant now FAILS at its `pre<Variant>Build`, from a reused entry too; its
    // other variants build. Naming the fallback's environment for it says the
    // stand-in is intended.
    @Test
    fun `an app variant that packs a library fallback with another stack fails`() {
        val project = libraryAndApp(
            appBuildTypes = """create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") }""",
        )
        project.environment("local", base = project.root)
        project.environment("featureX", base = project.root)

        val stored = project.buildAndFail(":app:preFeatureXBuild", "--configuration-cache").output
        assertTrue(stored.contains("Configuration cache entry stored"), stored)
        assertTrue(
            stored.contains(
                "Palbase: `:app` variant `featureX` packs `:lib`'s `debug` build — `:lib` declares no `featureX` " +
                    "build type, so AGP takes `debug` from its matchingFallbacks — and with it the Palbase client and " +
                    "config `:lib` generated for `debug`: `local` (the default for debug). `featureX` itself would " +
                    "select `featureX` (from the build type name). Declare the `featureX` build type in `:lib` too " +
                    "(`buildTypes { create(\"featureX\") { initWith(getByName(\"debug\")) } }`), or, if `local` is what " +
                    "`featureX` should compile, say so: `palbase.env.featureX=local` in the root gradle.properties.",
            ),
            stored,
        )
        val reused = project.buildAndFail(":app:preFeatureXBuild", "--configuration-cache").output
        assertTrue(reused.contains("Configuration cache entry reused"), reused)
        assertTrue(reused.contains("> Task :app:preFeatureXBuild FAILED"), reused)

        project.build(":app:preDebugBuild", ":app:preReleaseBuild", "--configuration-cache")

        // The intent key is an input of the entry: the edit configures anew, and
        // the two answers agree.
        project.root.resolve("gradle.properties").write("palbase.env.featureX=local\n")
        val intended = project.build(":app:preFeatureXBuild", "--configuration-cache").output
        assertFalse(intended.contains("Configuration cache entry reused"), intended)
        assertFalse(intended.contains("packs `:lib`'s"), intended)
    }

    // FR-208 through a module in between: `:app` → `:core` → `:lib`, the usual
    // shape once an app has more than one module. The client still rides into
    // the `featureX` APK from `:lib`'s `debug`; the refusal must hold there too.
    @Test
    fun `an app variant that packs the library through another module fails too`() {
        val project = libraryAndApp(
            appBuildTypes = """create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") }""",
        )
        project.environment("local", base = project.root)
        project.environment("featureX", base = project.root)
        project.root.resolve("settings.gradle.kts").append("\ninclude(\":core\")\n")
        project.root.resolve("core/src/main/AndroidManifest.xml").write("<manifest />")
        project.root.resolve("core/build.gradle.kts").write(
            """
            plugins { id("com.android.library") }
            android {
                namespace = "test.core"
                compileSdk = 36
                defaultConfig { minSdk = 26 }
            }
            dependencies { implementation(project(":lib")) }
            """.trimIndent(),
        )
        val app = project.root.resolve("app/build.gradle.kts")
        app.write(Files.readString(app).replace("implementation(project(\":lib\"))", "implementation(project(\":core\"))"))

        val failure = project.buildAndFail(":app:preFeatureXBuild").output

        assertTrue(
            failure.contains(
                "Palbase: `:app` variant `featureX` packs `:lib`'s `debug` build — `:lib` declares no `featureX` build type",
            ),
            failure,
        )
        project.build(":app:preDebugBuild", ":app:preReleaseBuild")
    }
```
  (2/3) şu satırlardan sonra:
```kotlin
        assertFalse(project.hasAsset("release"), "a name outside palbase/environments/ was read")
    }
```
  şunu ekle:
```kotlin

    /**
     * `:lib` applies the plugin, as a library; `:app` depends on it and does NOT
     * — the shape in which the app's variants get the library's stack. The
     * returned fixture is `:lib`'s. [appAndroid] goes inside `:app`'s
     * `android { }`, [appBuildTypes] inside its `buildTypes { }`.
     */
    private fun libraryAndApp(appAndroid: String = "", appBuildTypes: String = ""): Fixture {
        val project = fixture(module = "lib", library = true)
        project.root.resolve("settings.gradle.kts").append("\ninclude(\":app\")\n")
        project.root.resolve("app/src/main/AndroidManifest.xml").write("<manifest />")
        project.root.resolve("app/build.gradle.kts").write(
            """
            plugins { id("com.android.application") }
            android {
                namespace = "test.app"
                compileSdk = 36
                defaultConfig { applicationId = "test.app"; minSdk = 26 }
            """.trimIndent() + "\n" + appAndroid + "\n" + """
                buildTypes {
            """.trimIndent() + "\n" + appBuildTypes + "\n" + """
                }
            }
            dependencies { implementation(project(":lib")) }
            """.trimIndent(),
        )
        return project
    }
```
  (3/3) şu bloğu:
```kotlin
        private fun runner(vararg arguments: String): GradleRunner = GradleRunner.create()
            .withProjectDir(root.toFile())
            .withArguments(*arguments, "--stacktrace")
```
  şununla değiştir:
```kotlin
        // --no-watch-fs: a test writes a file and builds again at once, and a
        // configuration-cache entry must be judged by the file as it is NOW —
        // not by a file-watching snapshot the edit's event has not reached yet
        // (once in six full runs, a reused entry missed a fresh gradle.properties).
        private fun runner(vararg arguments: String): GradleRunner = GradleRunner.create()
            .withProjectDir(root.toFile())
            .withArguments(*arguments, "--stacktrace", "--no-watch-fs")
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.LibraryFallbackCheckTest' --tests '*PalbaseCodegenPluginTest.an app variant that packs*'` · Beklenen: **FAIL** (derleme): `> Task :compileTestKotlin FAILED`, `e: …/LibraryFallbackCheckTest.kt:3:26 Unresolved reference 'LibraryFallbackCheck'.` Davranış kırmızısı — yalnız `PalbaseCodegenPluginTest.kt` değişikliği uygulanıp aynı TestKit filtresiyle: `2 tests completed, 2 failed`; `an app variant that packs a library fallback with another stack fails() FAILED` → `UnexpectedBuildSuccess … [:app:preFeatureXBuild, --configuration-cache, --stacktrace, --no-watch-fs]`, çıktıda `> Task :app:preFeatureXBuild UP-TO-DATE`, `BUILD SUCCESSFUL in 6s`, `Configuration cache entry stored.` (D1: fallback sessizce paketlenir); `an app variant that packs the library through another module fails too() FAILED` → `UnexpectedBuildSuccess … [:app:preFeatureXBuild, --stacktrace, --no-watch-fs]`, `> Task :app:preFeatureXBuild UP-TO-DATE`.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/3) şu satırlardan sonra:
```kotlin
import com.android.build.api.variant.DslExtension
```
  şunu ekle:
```kotlin
import com.android.build.api.variant.LibraryAndroidComponentsExtension
```
  (2/3) şu satırlardan sonra:
```kotlin
            it.parameters.directory.set(rootDirectory)
        }
```
  şunu ekle:
```kotlin

        // A LIBRARY is packed into app variants it may not have; those get one of
        // its variants, and that variant's stack. See LibraryFallbackCheck.
        val libraryVariants = mutableListOf<LibraryFallbackCheck.LibraryVariant>()
        if (androidComponents is LibraryAndroidComponentsExtension) {
            LibraryFallbackCheck.register(project, resolver, libraryVariants)
        }
```
  (3/3) şu satırlardan sonra:
```kotlin
            val resolution = resolver.resolve(variant.name, buildType, variant.debuggable, flavors)
```
  şunu ekle:
```kotlin
            libraryVariants += LibraryFallbackCheck.LibraryVariant(variant.name, buildType, resolution)
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt`:
  şu satırlardan sonra:
```kotlin
 * ones outrank every convention (6–9).
 *
```
  şunu ekle:
```kotlin
 * "Variant" means a variant OF THE MODULE THAT APPLIES THE PLUGIN. A library
 * has no variant for an app build type it does not declare; AGP packs one of
 * its own there instead, and LibraryFallbackCheck asks this same resolver what
 * the app variant would have selected.
 *
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt` (yeni dosya):
```kotlin
package io.palbase.gradle

import com.android.build.api.dsl.ApplicationExtension
import org.gradle.api.Action
import org.gradle.api.GradleException
import org.gradle.api.Project
import org.gradle.api.Task
import org.gradle.api.artifacts.ProjectDependency

/**
 * A LIBRARY CANNOT SEE THE BUILD IT IS PACKED INTO.
 *
 * The plugin resolves one environment per variant of the module it is applied
 * to, and a library has variants only for the build types IT declares. An app
 * build type the library lacks — `featureX` — gets the library's
 * `matchingFallbacks` variant (`debug`) instead, and that variant's client and
 * `palbase-config.json` ride into the `featureX` APK. Nothing in the library's
 * own build runs for `featureX`, so nothing there can tell.
 *
 * So the library LOOKS, once every project is configured: for each variant of
 * every application module that packs it, it compares what that variant WOULD
 * select — the same resolver, the same places — with what the library variant
 * AGP packs instead compiled. A difference FAILS THAT APP VARIANT: its
 * `pre<Variant>Build` refuses when it runs, from a reused configuration cache
 * entry too. No other variant, and no IDE sync, is touched. A warning was
 * measured to be not enough: silent on configuration-cache reuse, and the APK
 * shipped the other stack anyway (D-002).
 *
 * Saying the fallback's environment is intended —
 * `palbase.env.<app variant>=<that environment>` — makes the two answers agree.
 */
internal object LibraryFallbackCheck {

    /** One variant of an application module, as its finished DSL makes it. */
    data class AppVariant(
        val name: String,
        val buildType: String,
        val debuggable: Boolean,
        /** The build type's `matchingFallbacks`, in order. */
        val buildTypeFallbacks: List<String>,
    )

    /** One variant of the library, and what it compiled. */
    data class LibraryVariant(val name: String, val buildType: String, val resolution: EnvironmentResolution)

    /** An app variant that packs a library variant compiled for another environment; [message] is its refusal. */
    data class Finding(val appVariant: String, val message: String)

    /**
     * After every project is configured, fails each app variant that packs
     * [library] with a stack it would not select. [libraryVariants] is filled
     * by the library's own `onVariants` — complete by then.
     */
    fun register(library: Project, resolver: EnvironmentResolver, libraryVariants: List<LibraryVariant>) {
        library.gradle.projectsEvaluated { gradle ->
            for (app in gradle.rootProject.allprojects) {
                if (!app.pluginManager.hasPlugin("com.android.application")) continue
                // A different AGP class loader in the app than in this module would
                // make the cast fail; the check then has nothing it can read.
                val android = app.extensions.findByName("android") as? ApplicationExtension ?: continue
                val packing = appVariants(android).filter { packs(app, it.name, library.path) }
                findings(app.path, library.path, packing, libraryVariants, resolver).forEach { finding ->
                    val preBuild = "pre${finding.appVariant.replaceFirstChar { it.uppercase() }}Build"
                    if (preBuild in app.tasks.names) {
                        app.tasks.named(preBuild).configure { it.doFirst(Refusal(finding.message)) }
                    }
                }
            }
        }
    }

    /** Pure, so the rule is testable without a build: one refusal per app variant that gets the wrong stack. */
    fun findings(
        appPath: String,
        libraryPath: String,
        appVariants: List<AppVariant>,
        libraryVariants: List<LibraryVariant>,
        resolver: EnvironmentResolver,
    ): List<Finding> = appVariants.mapNotNull { app ->
        val libraryBuildTypes = libraryVariants.map { it.buildType }.toSet()
        // No build type the library has: AGP fails the dependency resolution itself, loudly.
        val buildType = app.buildType.takeIf { it in libraryBuildTypes }
            ?: app.buildTypeFallbacks.firstOrNull { it in libraryBuildTypes }
            ?: return@mapNotNull null
        val packed = libraryVariants.singleOrNull { it.buildType == buildType } ?: return@mapNotNull null
        // A library variant that is refused fails its own task when it runs.
        val got = packed.resolution as? EnvironmentResolution.Selected ?: return@mapNotNull null
        val wanted = resolver.resolve(app.name, app.buildType, app.debuggable, emptyList())
        if (wanted is EnvironmentResolution.Selected && wanted.environment == got.environment) return@mapNotNull null

        val reasons = mutableListOf<String>()
        // What to declare in the library, and the DSL that does it.
        val declarations = mutableListOf<Pair<String, String>>()
        if (buildType != app.buildType) {
            reasons += "`$libraryPath` declares no `${app.buildType}` build type, so AGP takes `$buildType` from " +
                "its matchingFallbacks"
            declarations += "the `${app.buildType}` build type" to
                "`buildTypes { create(\"${app.buildType}\") { initWith(getByName(\"$buildType\")) } }`"
        }
        val would = when (wanted) {
            is EnvironmentResolution.Selected -> "would select `${wanted.environment}` (${wanted.origin})"
            is EnvironmentResolution.Refused -> "would be refused: ${wanted.reason.removePrefix("Palbase: ")}"
        }
        val because = if (reasons.isEmpty()) "," else " — ${reasons.joinToString("; ")} —"
        val intended = "if `${got.environment}` is what `${app.name}` should compile, say so: " +
            "`${EnvironmentResolver.PROPERTY_PREFIX}${app.name}=${got.environment}` in the root gradle.properties."
        Finding(
            app.name,
            "Palbase: `$appPath` variant `${app.name}` packs `$libraryPath`'s `${packed.name}` build$because and " +
                "with it the Palbase client and config `$libraryPath` generated for `${packed.name}`: " +
                "`${got.environment}` (${got.origin}). `${app.name}` itself $would. " +
                if (declarations.isEmpty()) {
                    intended.replaceFirstChar { it.uppercase() }
                } else {
                    "Declare ${declarations.joinToString(" and ") { it.first }} in `$libraryPath` too " +
                        "(${declarations.joinToString("; ") { it.second }}), or, $intended"
                },
        )
    }

    /** Every variant the app's finished DSL makes: one per build type. */
    private fun appVariants(android: ApplicationExtension): List<AppVariant> = android.buildTypes.map { buildType ->
        AppVariant(buildType.name, buildType.name, buildType.isDebuggable, buildType.matchingFallbacks.toList())
    }

    /**
     * Whether [variant] of [app] packs [libraryPath]: its runtime classpath
     * declares the library, directly or through modules in between. Declared,
     * not resolved — `debugImplementation` reaches `debug` alone.
     */
    private fun packs(app: Project, variant: String, libraryPath: String): Boolean {
        val classpath = app.configurations.findByName("${variant}RuntimeClasspath") ?: return false
        val seen = mutableSetOf<String>()
        fun reaches(dependencies: Iterable<ProjectDependency>): Boolean = dependencies.any { dependency ->
            dependency.path == libraryPath ||
                (seen.add(dependency.path) && reaches(runtimeDependencies(app.project(dependency.path))))
        }
        return reaches(classpath.allDependencies.withType(ProjectDependency::class.java))
    }

    /** What a module in between packs onwards: every runtime classpath it declares, its tests' aside. */
    private fun runtimeDependencies(module: Project): List<ProjectDependency> = module.configurations
        .filter { it.name == "runtimeClasspath" || (it.name.endsWith("RuntimeClasspath") && TEST.none(it.name::contains)) }
        .flatMap { it.allDependencies.withType(ProjectDependency::class.java) }

    private val TEST = listOf("UnitTest", "AndroidTest", "TestFixtures")

    /**
     * The refusal a `pre<Variant>Build` runs first. A plain object holding one
     * sentence, so the configuration cache stores it with the task — and a
     * reused entry refuses exactly as the first build did.
     */
    internal class Refusal(private val message: String) : Action<Task> {
        override fun execute(task: Task): Unit = throw GradleException(message)
    }
}
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.LibraryFallbackCheckTest' --tests '*PalbaseCodegenPluginTest.an app variant that packs*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 7s` (LibraryFallbackCheckTest `tests="8"`, PalbaseCodegenPluginTest `tests="2"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 11s`, `EnvironmentResolverTest` `tests="52"`, `LibraryFallbackCheckTest` `tests="8"`, `PalbaseCodegenPluginTest` `tests="82"` — hepsi `failures="0" errors="0"`. Kırılganlık ölçümü (`--no-watch-fs` ile): bu görevden T028'e kadar her tam `:test` koşusu (6), T030'un plugin kapısı ve son HEAD'de ardışık 5 kez `cd codegen-gradle && ../gradlew :test --offline --rerun` (`BUILD SUCCESSFUL in 1m 6s`, `BUILD SUCCESSFUL in 1m 5s`, `BUILD SUCCESSFUL in 1m 4s`, `BUILD SUCCESSFUL in 1m 8s`, `BUILD SUCCESSFUL in 1m 8s`) — hepsi yeşil; bayraksız hâlde yalnız bu test 12 kez koşturulmuştu, 12/12 yeşil (kırılganlık tek başına yeniden üretilemedi).
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/EnvironmentResolver.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/LibraryFallbackCheckTest.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen)!: library'nin fallback variant'ı başka ortam paketlerse o app variant'ı düşer — pre<Variant>Build'in ilk işi ret, configuration cache yeniden kullanılınca da; diğer variant'lar etkilenmez"`

---

### T024: Denetim flavor × build type ölçer — flavor'lı app'in variant anahtarı flavor'sız library'de sessizce yok sayılmaz
<!-- deps: [T023] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/LibraryFallbackCheckTest.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-208] -->

FR-208'in "(flavor × build type)" yarısı, N1 (blocker). Flavor'lı bir app, flavor'sız bir library: her flavor'ın release'i library'nin TEK `release` build'ini paketler; `palbase.env.stagingRelease=staging` — D2'nin tek çaresi — hiçbir şey seçmedi, uyarı da yoktu, staging APK'sı prod taşıdı (`reports/verification-2026-09-25.md` N1, fx/n1: `APK app-staging-release-unsigned.apk: base_url = https://prod.envprobe.palbase.studio`; prototip `LibraryFallbackCheck.kt:87` yalnız build type karşılaştırıyordu). App variant'ları artık DSL'den her flavor kombinasyonu (boyut sırasıyla; tek boyut ilan edilmişse flavor'ın boş `dimension`'ı ona sayılır) × her build type olarak çıkarılır ve çözücü app variant'ının flavor'larıyla sorulur. Library'nin flavor'ları boyut boyut eşlenir: app'in o boyuttaki aynı adlı flavor'ı, yoksa o flavor'ın `matchingFallbacks`'inden library'nin sahip olduğu ilki — bir stand-in; neden cümlesi söyler ve çare o flavor'ı library'de tanımlamaktır. Yalnız app'te olan bir boyutun her flavor'ı aynı library build'ini paketler; neden cümlesi bunu söyler.

Library'nin olup app'in olmadığı bir boyut app'in `missingDimensionStrategy`'siyle seçilir ve genel DSL onu bir eklentiye okutmaz: hangi library variant'ının paketlendiği bilinemez. Tahminle ret yok — o app için `findings` atlar ve `unchecked(...)` cümlesi yapılandırmada basılır (app o library'yi paketliyorsa). Bu dal birim testle VE TestKit'te sabit: app `missingDimensionStrategy("tier", "free")` ile, library `tier` boyutuyla — `:app:preDebugBuild` ve `:app:preReleaseBuild` yeşil, uyarı çıktıda TAM BİR KEZ (`a library dimension the app lacks is said to be unchecked once and refuses nothing`).

**Interfaces:**
- Consumes: `AppVariant`, `LibraryVariant`, `findings(...)`, `register(...)` (T023)
- Produces:
  - `AppVariant(..., flavors: List<Pair<String, String>> = emptyList(), flavorFallbacks: Map<String, List<String>> = emptyMap())` — `(boyut, flavor)` boyut sırasında
  - `LibraryVariant(name, buildType, resolution, flavors: List<Pair<String, String>> = emptyList())`; `AndroidVariantIntegration` `variant.productFlavors`'ı verir
  - `fun unchecked(appPath: String, libraryPath: String, appDimensions: List<String>, libraryVariants: List<LibraryVariant>): String?`
  - Neden cümleleri: ``…`<lib>` has no `<dimension>` flavor dimension, so every `<dimension>` flavor of `<app>` packs the same `<lib>` build…`` · ``…`<lib>` has no `<flavor>` flavor, so AGP takes `<fallback>` from its matchingFallbacks…``
  - Uyarı: ``Palbase: `<app>` has no `<dimension>` flavor dimension and `<lib>` does, so which `<lib>` variant each `<app>` variant packs is its missingDimensionStrategy's choice, which a plugin cannot read — the environments `<app>` gets from `<lib>` are NOT checked. Give `<app>` the `<dimension>` dimension to have them checked.``

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/LibraryFallbackCheckTest.kt`:
  şu bloğu:
```kotlin
    private fun app(buildType: String, debuggable: Boolean = false, fallbacks: List<String> = emptyList()) =
        AppVariant(name = buildType, buildType = buildType, debuggable = debuggable, buildTypeFallbacks = fallbacks)

    private fun check(
```
  şununla değiştir:
```kotlin
    // N1: a flavored app over an UNFLAVORED library. Every flavor's release
    // packs the library's one `release` build, so `palbase.env.stagingRelease`
    // — the only way to aim one flavor elsewhere — chose nothing, in silence.
    @Test
    fun `every flavor of the app packs the one build of an unflavored library`() {
        val resolver = places(gradle = mapOf("palbase.env.release" to "prod", "palbase.env.stagingRelease" to "staging"))
        val findings = check(
            listOf(flavored("staging", "release"), flavored("prod", "release")),
            resolver,
            library = listOf(LibraryVariant("release", "release", EnvironmentResolution.Selected("prod", "palbase.env.release in gradle.properties"))),
        )

        val finding = findings.single()
        assertEquals("stagingRelease", finding.appVariant)
        assertTrue(
            finding.message.contains(
                "packs `:lib`'s `release` build — `:lib` has no `env` flavor dimension, so every `env` flavor of " +
                    "`:app` packs the same `:lib` build — and with it",
            ),
            finding.message,
        )
        assertTrue(finding.message.contains("would select `staging` (palbase.env.stagingRelease in gradle.properties)"), finding.message)
        assertTrue(finding.message.contains("productFlavors { create(\"staging\") { dimension = \"env\" } }"), finding.message)
        assertTrue(finding.message.contains("`palbase.env.stagingRelease=prod`"), finding.message)
    }

    // A flavored library is packed by the flavor of the same name — the same
    // keys, the same answer — or, lacking it, by the app flavor's own
    // matchingFallbacks, which is a stand-in like any other.
    @Test
    fun `a flavored library is matched flavor by flavor`() {
        val library = listOf(
            LibraryVariant("stagingRelease", "release", EnvironmentResolution.Selected("staging", "a"), listOf("env" to "staging")),
            LibraryVariant("prodRelease", "release", EnvironmentResolution.Selected("prod", "b"), listOf("env" to "prod")),
        )
        val resolver = places(
            gradle = mapOf("palbase.env.staging" to "staging", "palbase.env.prod" to "prod", "palbase.env.qa" to "qa"),
        )
        val qa = flavored("qa", "release").copy(flavorFallbacks = mapOf("qa" to listOf("staging")))

        val findings = check(listOf(flavored("staging", "release"), flavored("prod", "release"), qa), resolver, library)

        val finding = findings.single()
        assertEquals("qaRelease", finding.appVariant)
        assertTrue(
            finding.message.contains(
                "packs `:lib`'s `stagingRelease` build — `:lib` has no `qa` flavor, so AGP takes `staging` from its " +
                    "matchingFallbacks",
            ),
            finding.message,
        )
        assertTrue(finding.message.contains("would select `qa` (palbase.env.qa in gradle.properties)"), finding.message)
        assertTrue(finding.message.contains("productFlavors { create(\"qa\") { dimension = \"env\" } }"), finding.message)
    }

    // A dimension the LIBRARY has and the app does not is settled by the app's
    // missingDimensionStrategy, which no plugin can read: which library variant
    // is packed is unknown. Nothing is refused on a guess — and that is SAID.
    @Test
    fun `a library dimension the app lacks leaves the app unchecked and says so`() {
        val library = listOf(
            LibraryVariant("freeRelease", "release", EnvironmentResolution.Selected("main", "a"), listOf("tier" to "free")),
            LibraryVariant("paidRelease", "release", EnvironmentResolution.Selected("prod", "b"), listOf("tier" to "paid")),
        )

        assertEquals(emptyList<LibraryFallbackCheck.Finding>(), check(listOf(app("release")), places(), library))
        assertEquals(
            "Palbase: `:app` has no `tier` flavor dimension and `:lib` does, so which `:lib` variant each `:app` " +
                "variant packs is its missingDimensionStrategy's choice, which a plugin cannot read — the environments " +
                "`:app` gets from `:lib` are NOT checked. Give `:app` the `tier` dimension to have them checked.",
            LibraryFallbackCheck.unchecked(":app", ":lib", appDimensions = emptyList(), libraryVariants = library),
        )
        assertEquals(null, LibraryFallbackCheck.unchecked(":app", ":lib", listOf("tier"), library))
    }

    private fun app(buildType: String, debuggable: Boolean = false, fallbacks: List<String> = emptyList()) =
        AppVariant(name = buildType, buildType = buildType, debuggable = debuggable, buildTypeFallbacks = fallbacks)

    /** `stagingRelease`: one flavor in dimension `env`, a build type the library declares. */
    private fun flavored(flavor: String, buildType: String) = AppVariant(
        name = flavor + buildType.replaceFirstChar { it.uppercase() },
        buildType = buildType,
        debuggable = buildType == "debug",
        buildTypeFallbacks = emptyList(),
        flavors = listOf("env" to flavor),
    )

    private fun check(
```

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        project.build(":app:preDebugBuild", ":app:preReleaseBuild")
    }
```
  şunu ekle:
```kotlin

    // FR-208 (N1): a FLAVORED app over an unflavored library. Every flavor's
    // release packs the library's one `release` build, so the variant key — the
    // only way to aim `stagingRelease` elsewhere — was ignored without a word,
    // and the staging APK shipped prod. Flavors × build types are compared.
    @Test
    fun `a flavored app variant that packs an unflavored library with another stack fails`() {
        val project = libraryAndApp(
            appAndroid = """
                flavorDimensions += "env"
                productFlavors { create("staging") { dimension = "env" }; create("prod") { dimension = "env" } }
            """.trimIndent(),
        )
        project.environment("prod", base = project.root)
        project.environment("staging", base = project.root)
        project.root.resolve("gradle.properties").write("palbase.env.release=prod\npalbase.env.stagingRelease=staging\n")

        val staging = project.buildAndFail(":app:preStagingReleaseBuild", "--configuration-cache").output
        assertTrue(
            staging.contains(
                "Palbase: `:app` variant `stagingRelease` packs `:lib`'s `release` build — `:lib` has no `env` " +
                    "flavor dimension, so every `env` flavor of `:app` packs the same `:lib` build — and with it the " +
                    "Palbase client and config `:lib` generated for `release`: `prod` (palbase.env.release in " +
                    "gradle.properties). `stagingRelease` itself would select `staging` (palbase.env.stagingRelease " +
                    "in gradle.properties).",
            ),
            staging,
        )
        project.build(":app:preProdReleaseBuild", ":app:preStagingDebugBuild", "--configuration-cache")
    }

    // A library flavor dimension the app settles with missingDimensionStrategy —
    // which a plugin cannot read — leaves the packed library variant unknown:
    // nothing is refused on a guess, and that is SAID, once.
    @Test
    fun `a library dimension the app lacks is said to be unchecked once and refuses nothing`() {
        val project = libraryAndApp(appAndroid = """defaultConfig { missingDimensionStrategy("tier", "free") }""")
        project.app.resolve("build.gradle.kts").append(
            "\nandroid { flavorDimensions += \"tier\"; productFlavors { create(\"free\") { dimension = \"tier\" }; " +
                "create(\"paid\") { dimension = \"tier\" } } }\n",
        )
        project.environment("local", base = project.root)
        project.environment("main", base = project.root)
        project.root.resolve("gradle.properties").write("palbase.env.paid=main\npalbase.env.release=main\n")

        val output = project.build(":app:preDebugBuild", ":app:preReleaseBuild").output

        assertEquals(1, Regex("are NOT checked").findAll(output).count(), output)
        assertTrue(output.contains("Palbase: `:app` has no `tier` flavor dimension and `:lib` does"), output)
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.LibraryFallbackCheckTest' --tests '*PalbaseCodegenPluginTest.a flavored app variant that packs*' --tests '*PalbaseCodegenPluginTest.a library dimension the app lacks*'` · Beklenen: **FAIL** (derleme): `e: …/LibraryFallbackCheckTest.kt:136:105 Too many arguments for 'constructor(name: String, buildType: String, resolution: EnvironmentResolution): LibraryFallbackCheck.LibraryVariant'.` ve `e: …/LibraryFallbackCheckTest.kt:142:49 No parameter with name 'flavorFallbacks' found.` (9 `e:` satırı). Davranış kırmızısı — yalnız TestKit değişikliğiyle: `2 tests completed, 2 failed`; `a flavored app variant that packs an unflavored library with another stack fails() FAILED` → `UnexpectedBuildSuccess … [:app:preStagingReleaseBuild, --configuration-cache, --stacktrace, --no-watch-fs]`, çıktıda ``Palbase: `palbase.env.stagingRelease` in gradle.properties names no variant, product flavor or build type of project ':lib' (debug, release), so it chooses nothing here — …`` ve `> Task :app:preStagingReleaseBuild UP-TO-DATE` (N1: variant anahtarı sessizce yok sayıldı; uyarının kendisi T025'in konusu); `a library dimension the app lacks is said to be unchecked once and refuses nothing() FAILED` → `> Task :app:preDebugBuild UP-TO-DATE`, `> Task :app:preReleaseBuild UP-TO-DATE`, `BUILD SUCCESSFUL in 6s` ` ==> expected: <1> but was: <0>` (denetimsiz kalan app hakkında tek satır yok).
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  şu bloğu:
```kotlin
            libraryVariants += LibraryFallbackCheck.LibraryVariant(variant.name, buildType, resolution)
```
  şununla değiştir:
```kotlin
            libraryVariants += LibraryFallbackCheck.LibraryVariant(variant.name, buildType, resolution, variant.productFlavors)
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt`:
  (1/7) şu satırlardan sonra:
```kotlin
import com.android.build.api.dsl.ApplicationExtension
```
  şunu ekle:
```kotlin
import com.android.build.api.dsl.ProductFlavor
```
  (2/7) şu bloğu:
```kotlin
    )

    /** One variant of the library, and what it compiled. */
    data class LibraryVariant(val name: String, val buildType: String, val resolution: EnvironmentResolution)
```
  şununla değiştir:
```kotlin
        /** `(dimension, flavor)`, in dimension order. */
        val flavors: List<Pair<String, String>> = emptyList(),
        /** Each of [flavors]' own `matchingFallbacks`, by flavor name. */
        val flavorFallbacks: Map<String, List<String>> = emptyMap(),
    )

    /** One variant of the library, and what it compiled; [flavors] as `(dimension, flavor)`. */
    data class LibraryVariant(
        val name: String,
        val buildType: String,
        val resolution: EnvironmentResolution,
        val flavors: List<Pair<String, String>> = emptyList(),
    )
```
  (3/7) şu satırlardan sonra:
```kotlin
                val packing = appVariants(android).filter { packs(app, it.name, library.path) }
```
  şunu ekle:
```kotlin
                if (packing.isNotEmpty()) {
                    unchecked(app.path, library.path, android.flavorDimensions.toList(), libraryVariants)
                        ?.let { library.logger.warn(it) }
                }
```
  (4/7) şu satırlardan sonra:
```kotlin
    ): List<Finding> = appVariants.mapNotNull { app ->
```
  şunu ekle:
```kotlin
        val reasons = mutableListOf<String>()
        // What to declare in the library, and the DSL that does it.
        val declarations = mutableListOf<Pair<String, String>>()
```
  (5/7) şu bloğu:
```kotlin
        val packed = libraryVariants.singleOrNull { it.buildType == buildType } ?: return@mapNotNull null
        // A library variant that is refused fails its own task when it runs.
        val got = packed.resolution as? EnvironmentResolution.Selected ?: return@mapNotNull null
        val wanted = resolver.resolve(app.name, app.buildType, app.debuggable, emptyList())
        if (wanted is EnvironmentResolution.Selected && wanted.environment == got.environment) return@mapNotNull null

        val reasons = mutableListOf<String>()
        // What to declare in the library, and the DSL that does it.
        val declarations = mutableListOf<Pair<String, String>>()
```
  sil.
  (6/7) şu bloğu:
```kotlin
        val would = when (wanted) {
```
  şununla değiştir:
```kotlin
        // The library's flavors, dimension by dimension: the app's flavor of the
        // same name, else the first of that flavor's matchingFallbacks it has.
        val libraryDimensions = libraryDimensions(libraryVariants)
        val flavors = libraryDimensions.map { dimension ->
            // A dimension the app lacks is its missingDimensionStrategy's pick — see [unchecked].
            val appFlavor = app.flavors.firstOrNull { it.first == dimension }?.second ?: return@mapNotNull null
            val offered = libraryVariants.flatMap { it.flavors }.filter { it.first == dimension }.map { it.second }
            val flavor = appFlavor.takeIf { it in offered }
                ?: app.flavorFallbacks[appFlavor].orEmpty().firstOrNull { it in offered }
                ?: return@mapNotNull null
            if (flavor != appFlavor) {
                reasons += "`$libraryPath` has no `$appFlavor` flavor, so AGP takes `$flavor` from its matchingFallbacks"
                declarations += "the `$appFlavor` product flavor" to
                    "`productFlavors { create(\"$appFlavor\") { dimension = \"$dimension\" } }`"
            }
            dimension to flavor
        }
        // A dimension only the app has: every flavor of it packs the same library build.
        app.flavors.filter { (dimension, _) -> dimension !in libraryDimensions }.forEach { (dimension, flavor) ->
            reasons += "`$libraryPath` has no `$dimension` flavor dimension, so every `$dimension` flavor of " +
                "`$appPath` packs the same `$libraryPath` build"
            declarations += "the `$flavor` product flavor" to
                "`flavorDimensions += \"$dimension\"; productFlavors { create(\"$flavor\") { dimension = \"$dimension\" } }`"
        }
        val packed = libraryVariants.singleOrNull { it.buildType == buildType && it.flavors == flavors }
            ?: return@mapNotNull null
        // A library variant that is refused fails its own task when it runs.
        val got = packed.resolution as? EnvironmentResolution.Selected ?: return@mapNotNull null
        val wanted = resolver.resolve(app.name, app.buildType, app.debuggable, app.flavors.map { it.second })
        if (wanted is EnvironmentResolution.Selected && wanted.environment == got.environment) return@mapNotNull null

        val would = when (wanted) {
```
  (7/7) şu bloğu:
```kotlin
    /** Every variant the app's finished DSL makes: one per build type. */
    private fun appVariants(android: ApplicationExtension): List<AppVariant> = android.buildTypes.map { buildType ->
        AppVariant(buildType.name, buildType.name, buildType.isDebuggable, buildType.matchingFallbacks.toList())
```
  şununla değiştir:
```kotlin
    /**
     * The sentence for a library flavor dimension [appDimensions] lacks, or null.
     * AGP picks the library's flavor there by the app's missingDimensionStrategy,
     * which the public DSL does not let a plugin read: which library variant is
     * packed is unknown, so nothing is refused on a guess — and that is said.
     */
    fun unchecked(
        appPath: String,
        libraryPath: String,
        appDimensions: List<String>,
        libraryVariants: List<LibraryVariant>,
    ): String? {
        val missing = libraryDimensions(libraryVariants).filter { it !in appDimensions }
        if (missing.isEmpty()) return null
        val names = missing.joinToString(", ") { "`$it`" }
        return "Palbase: `$appPath` has no $names flavor dimension and `$libraryPath` does, so which `$libraryPath` " +
            "variant each `$appPath` variant packs is its missingDimensionStrategy's choice, which a plugin cannot " +
            "read — the environments `$appPath` gets from `$libraryPath` are NOT checked. Give `$appPath` the " +
            "$names dimension to have them checked."
    }

    private fun libraryDimensions(libraryVariants: List<LibraryVariant>) =
        libraryVariants.flatMap { variant -> variant.flavors.map { it.first } }.distinct()

    /** Every variant the app's finished DSL makes: each flavor combination, in dimension order, × each build type. */
    private fun appVariants(android: ApplicationExtension): List<AppVariant> {
        val dimensions = android.flavorDimensions.toList()
        // One dimension declared, a flavor may leave its own unset.
        fun dimensionOf(flavor: ProductFlavor) = flavor.dimension ?: dimensions.singleOrNull()
        val combinations = dimensions.fold(listOf(emptyList<ProductFlavor>())) { combined, dimension ->
            val flavors = android.productFlavors.filter { dimensionOf(it) == dimension }
            combined.flatMap { prefix -> flavors.map { prefix + it } }
        }
        return combinations.flatMap { flavors ->
            android.buildTypes.map { buildType ->
                AppVariant(
                    name = (flavors.map { it.name } + buildType.name)
                        .mapIndexed { i, part -> if (i == 0) part else part.replaceFirstChar { it.uppercase() } }
                        .joinToString(""),
                    buildType = buildType.name,
                    debuggable = buildType.isDebuggable,
                    buildTypeFallbacks = buildType.matchingFallbacks.toList(),
                    flavors = flavors.map { (dimensionOf(it) ?: "") to it.name },
                    flavorFallbacks = flavors.associate { it.name to it.matchingFallbacks.toList() },
                )
            }
        }
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests 'io.palbase.gradle.LibraryFallbackCheckTest' --tests '*PalbaseCodegenPluginTest.a flavored app variant that packs*' --tests '*PalbaseCodegenPluginTest.a library dimension the app lacks*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 6s` (LibraryFallbackCheckTest `tests="11"`, PalbaseCodegenPluginTest `tests="2"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 14s`, `EnvironmentResolverTest` `tests="52"`, `LibraryFallbackCheckTest` `tests="11"`, `PalbaseCodegenPluginTest` `tests="84"` — hepsi `failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/LibraryFallbackCheckTest.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): library fallback denetimi flavor × build type ölçer — flavor'lı app'in variant anahtarı flavor'sız library'de artık sessizce yok sayılmıyor; flavor'lı library boyut boyut eşlenir"`

---

### T025: Library onu paketleyen app variant'larının anahtarlarını bilinmeyen saymaz
<!-- deps: [T013, T024] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-208, FR-203] -->

FR-208 ile FR-203'ün kesişimi (D-010). Ret metninin istediği `palbase.env.<app variant>=<env>` library'nin hiçbir variant'ını, flavor'ını ya da build type'ını adlandırmadığı için library onu "a typo, or a key meant for another module" diye uyarıyordu — çareyi uygulayan kullanıcıya "hiçbir şey seçmiyor" denmiş oluyordu (slice 2 devri bunu önceden söylemişti; T024'ün kırmızısında da görünür). `register` artık, library'yi paketleyen app variant'larının adlarını — variant, build type, her flavor, flavor kombinasyonu — `then` ile geri verir; library'nin bilinmeyen-anahtar uyarısı `afterEvaluate` yerine bu adlar eklendikten SONRA, `projectsEvaluated`'da basılır (app modülleri o anda yapılandırılmıştır). App modüllerinde uyarı eskisi gibi `afterEvaluate`'te. Gerçek bir yazım hatası uyarılmaya devam eder ve listede app'in adları da görünür.

**Interfaces:**
- Consumes: `unknownKeys(...)`, `names` (T013); `register(...)`, `AppVariant.flavors` (T023/T024); `EnvironmentResolver.flavorCombination(...)` (T013)
- Produces:
  - `LibraryFallbackCheck.register(library, resolver, libraryVariants, then: (Set<String>) -> Unit)` — `then` paketleyen app variant'larının adlarını alır
  - `AndroidVariantIntegration`: `val warnUnknownKeys = { … }`; library'de `register(...) { appNames -> names += appNames; warnUnknownKeys() }`, diğerlerinde `project.afterEvaluate { warnUnknownKeys() }`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertTrue(output.contains("Palbase: `:app` has no `tier` flavor dimension and `:lib` does"), output)
    }
```
  şunu ekle:
```kotlin

    // FR-208 + FR-203: the key that says a fallback is intended names no
    // variant of the LIBRARY, so the library warned about it as a typo — the fix
    // the refusal asks for was answered with "it chooses nothing here". A
    // library's names include those of the app variants that pack it; a real
    // typo is still warned about.
    @Test
    fun `keys for the app variants that pack a library are not unknown to it`() {
        val project = libraryAndApp(
            appAndroid = """
                flavorDimensions += "env"
                productFlavors { create("staging") { dimension = "env" } }
            """.trimIndent(),
            appBuildTypes = """create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") }""",
        )
        project.environment("local", base = project.root)
        project.root.resolve("gradle.properties").write("palbase.env.stagingFeatureX=local\npalbase.env.featureY=local\n")

        val output = project.build(":app:preStagingFeatureXBuild").output

        assertFalse(output.contains("`palbase.env.stagingFeatureX` in gradle.properties names no"), output)
        assertTrue(
            output.contains(
                "Palbase: `palbase.env.featureY` in gradle.properties names no variant, product flavor or build type " +
                    "of project ':lib' (debug, featureX, release, staging, stagingDebug, stagingFeatureX, " +
                    "stagingRelease), so it chooses nothing here",
            ),
            output,
        )
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.keys for the app variants that pack*'` · Beklenen: **FAIL**, `tests="1" … failures="1"`, `BUILD FAILED in 7s`; `keys for the app variants that pack a library are not unknown to it() FAILED` — çıktıda ``Palbase: `palbase.env.stagingFeatureX` in gradle.properties names no variant, product flavor or build type of project ':lib' (debug, release), so it chooses nothing here — a typo, or a key meant for another module.`` ve `BUILD SUCCESSFUL in 6s`.
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/2) şu bloğu:
```kotlin
        // A LIBRARY is packed into app variants it may not have; those get one of
        // its variants, and that variant's stack. See LibraryFallbackCheck.
        val libraryVariants = mutableListOf<LibraryFallbackCheck.LibraryVariant>()
        if (androidComponents is LibraryAndroidComponentsExtension) {
            LibraryFallbackCheck.register(project, resolver, libraryVariants)
        }
```
  şununla değiştir:
```kotlin
        val libraryVariants = mutableListOf<LibraryFallbackCheck.LibraryVariant>()
```
  (2/2) şu bloğu:
```kotlin
        project.afterEvaluate {
            unknownKeys(project, rootDirectory, names).forEach { project.logger.warn(it) }
```
  şununla değiştir:
```kotlin
        val warnUnknownKeys = { unknownKeys(project, rootDirectory, names).forEach { project.logger.warn(it) } }
        if (androidComponents is LibraryAndroidComponentsExtension) {
            // A LIBRARY is packed into app variants it may not have; those get one
            // of its variants, and that variant's stack — see LibraryFallbackCheck.
            // Their names are keys meant for this library too (the one that says
            // a fallback is intended, first), known once every project is.
            LibraryFallbackCheck.register(project, resolver, libraryVariants) { appNames ->
                names += appNames
                warnUnknownKeys()
            }
        } else {
            project.afterEvaluate { warnUnknownKeys() }
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt`:
  (1/3) şu bloğu:
```kotlin
     * by the library's own `onVariants` — complete by then.
     */
    fun register(library: Project, resolver: EnvironmentResolver, libraryVariants: List<LibraryVariant>) {
        library.gradle.projectsEvaluated { gradle ->
```
  şununla değiştir:
```kotlin
     * by the library's own `onVariants` — complete by then. [then] gets every
     * name a `palbase.env.<name>` key can mean for those app variants: each
     * variant, build type, flavor and flavor combination.
     */
    fun register(
        library: Project,
        resolver: EnvironmentResolver,
        libraryVariants: List<LibraryVariant>,
        then: (Set<String>) -> Unit,
    ) {
        library.gradle.projectsEvaluated { gradle ->
            val appNames = mutableSetOf<String>()
```
  (2/3) şu satırlardan sonra:
```kotlin
                val packing = appVariants(android).filter { packs(app, it.name, library.path) }
```
  şunu ekle:
```kotlin
                packing.forEach { variant ->
                    val flavors = variant.flavors.map { it.second }
                    appNames += listOf(variant.name, variant.buildType) + flavors +
                        listOfNotNull(EnvironmentResolver.flavorCombination(flavors))
                }
```
  (3/3) şu satırlardan sonra:
```kotlin
                        app.tasks.named(preBuild).configure { it.doFirst(Refusal(finding.message)) }
                    }
                }
            }
```
  şunu ekle:
```kotlin
            then(appNames)
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.keys for the app variants that pack*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 8s` (PalbaseCodegenPluginTest `tests="1"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 17s`, `EnvironmentResolverTest` `tests="52"`, `LibraryFallbackCheckTest` `tests="11"`, `PalbaseCodegenPluginTest` `tests="85"` — hepsi `failures="0" errors="0"`. Tüketici düzeyinde (planlama koşusu, s3-lib, `palbase.env.featureX=main` eklenince `:app:assembleStagingFeatureX`): `BUILD SUCCESSFUL`, `grep -c "names no variant"` = 0.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/LibraryFallbackCheck.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): library onu paketleyen app variant'larının anahtarlarını bilinmeyen saymaz — ret metninin istediği palbase.env.<app variant> artık yazım hatası diye uyarılmıyor"`

---

### T026: Isolated Projects açıkken denetim koşmaz ve bunu library başına bir kez söyler
<!-- deps: [T025] | files: [codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt, codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-208] -->

FR-208'in son cümlesi. Isolated Projects başka bir projenin modelini okumayı yasaklar — library'nin hangi app variant'larının ona düştüğünü öğrenebildiği tek yer. Ölçülen kırmızı: T023–T025 hâliyle IP açık bir build eklentinin DÖRT ihlaliyle DÜŞÜYORDU (`Project ':lib' cannot access 'Project.configurations' | 'Project.extensions' | 'Project.pluginManager' | 'Project.tasks' functionality on subprojects of project ':'`); AGP 8.10.1 ve KGP 2.2.21 bu fixture'da ihlal üretmedi. Artık eklenti `BuildFeatures`'ı enjekte eder (Gradle 8.5+; desteklenen taban 8.11.1, FR-212) ve `isolatedProjects.active` ise `AndroidVariantIntegration.configure(…, crossProjectChecks = false)` çağrılır: library çapraz proje denetimini kaydetmez, yapılandırmada BİR KEZ uyarır (variant başına değil) ve bilinmeyen-anahtar uyarısını `afterEvaluate`'te basar — orada app adları bilinemez, kasıt anahtarı yine "bilinmeyen" görünür. IP ile configuration cache hep açıktır; uyarı giriş yeniden kullanıldığında basılmaz. Test aynı zamanda eklentinin IP altında artık hiç ihlal üretmediğini ölçer (ihlal build'i düşürürdü).

**Genel API değişikliği (CHANGELOG):** `PalbaseCodegenPlugin` `class` → `abstract class … @Inject constructor(BuildFeatures)` (slice 1 `@Inject`'i kaldırmıştı); `validatePlugins` yeşil.

**Interfaces:**
- Consumes: `AndroidVariantIntegration.configure(project, extension)`; `register(...)`, `warnUnknownKeys` (T025)
- Produces:
  - `abstract class PalbaseCodegenPlugin @Inject constructor(private val buildFeatures: BuildFeatures) : Plugin<Project>`
  - `AndroidVariantIntegration.configure(project: Project, extension: PalbaseExtension, crossProjectChecks: Boolean)`
  - Uyarı: ``Palbase: Isolated Projects is on, so `<lib>` cannot read the app modules that pack it — an app variant that gets one of `<lib>`'s variants through matchingFallbacks is NOT checked, and packs that variant's environment whatever it would select itself. Declare every app build type and flavor in `<lib>` too, or build once without Isolated Projects to have them checked.``

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
            output,
        )
    }
```
  şunu ekle:
```kotlin

    // FR-208: Isolated Projects forbids reading another project's model — the
    // only place a library learns which app variants fall back to it. The check
    // cannot run there; that is SAID, once for the library, instead of the
    // fallback shipping in silence.
    @Test
    fun `under Isolated Projects a library says once that its fallbacks are not checked`() {
        val project = libraryAndApp(
            appBuildTypes = """create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") }""",
        )
        project.environment("local", base = project.root)

        val output = project.build(":app:preFeatureXBuild", "-Dorg.gradle.unsafe.isolated-projects=true").output

        assertEquals(1, Regex("Isolated Projects is on").findAll(output).count(), output)
        assertTrue(
            output.contains(
                "Palbase: Isolated Projects is on, so `:lib` cannot read the app modules that pack it — an app " +
                    "variant that gets one of `:lib`'s variants through matchingFallbacks is NOT checked, and packs " +
                    "that variant's environment whatever it would select itself. Declare every app build type and " +
                    "flavor in `:lib` too, or build once without Isolated Projects to have them checked.",
            ),
            output,
        )
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.under Isolated Projects*'` · Beklenen: **FAIL**, `tests="1" … failures="1"`, `BUILD FAILED in 5s`; `under Isolated Projects a library says once that its fallbacks are not checked() FAILED` → `UnexpectedBuildFailure … [:app:preFeatureXBuild, -Dorg.gradle.unsafe.isolated-projects=true, --stacktrace, --no-watch-fs]`: `FAILURE: Build completed with 2 failures.` — ret (`> Task :app:preFeatureXBuild FAILED`) ve `Configuration cache problems found in this build.` / `8 problems were found storing the configuration cache, 4 of which seem unique.` / `- Plugin 'io.palbase.codegen': Project ':lib' cannot access 'Project.configurations' functionality on subprojects of project ':'` (+ `extensions`, `pluginManager`, `tasks`).
- [ ] **Adım 3: Uygula** —

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt`:
  (1/3) şu bloğu:
```kotlin
    fun configure(project: Project, extension: PalbaseExtension) {
```
  şununla değiştir:
```kotlin
    /** @param crossProjectChecks false under Isolated Projects: no other project's model may be read. */
    fun configure(project: Project, extension: PalbaseExtension, crossProjectChecks: Boolean) {
```
  (2/3) şu bloğu:
```kotlin
        if (androidComponents is LibraryAndroidComponentsExtension) {
```
  şununla değiştir:
```kotlin
        if (androidComponents is LibraryAndroidComponentsExtension && crossProjectChecks) {
```
  (3/3) şu satırlardan sonra:
```kotlin
        } else {
```
  şunu ekle:
```kotlin
            // Under Isolated Projects the check cannot look — and says so, ONCE
            // for this library rather than per variant, instead of skipping it in
            // silence.
            if (androidComponents is LibraryAndroidComponentsExtension) {
                project.logger.warn(
                    "Palbase: Isolated Projects is on, so `${project.path}` cannot read the app modules that pack it " +
                        "— an app variant that gets one of `${project.path}`'s variants through matchingFallbacks is " +
                        "NOT checked, and packs that variant's environment whatever it would select itself. Declare " +
                        "every app build type and flavor in `${project.path}` too, or build once without Isolated " +
                        "Projects to have them checked.",
                )
            }
```

  `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt`:
  (1/2) şu bloğu:
```kotlin
import org.gradle.api.Plugin
import org.gradle.api.Project

/** Android variant integration for generated Kotlin and packaged runtime config. */
class PalbaseCodegenPlugin : Plugin<Project> {
```
  şununla değiştir:
```kotlin
import javax.inject.Inject
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.api.configuration.BuildFeatures

/** Android variant integration for generated Kotlin and packaged runtime config. */
abstract class PalbaseCodegenPlugin @Inject constructor(
    private val buildFeatures: BuildFeatures,
) : Plugin<Project> {
```
  (2/2) şu bloğu:
```kotlin
                AndroidVariantIntegration.configure(project, extension)
```
  şununla değiştir:
```kotlin
                // Isolated Projects forbids reading another project's model, which
                // is the only place a library learns which app variants fall back
                // to it (LibraryFallbackCheck).
                val isolated = buildFeatures.isolatedProjects.active.getOrElse(false)
                AndroidVariantIntegration.configure(project, extension, crossProjectChecks = !isolated)
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.under Isolated Projects*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 4s` (PalbaseCodegenPluginTest `tests="1"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 1s`, `EnvironmentResolverTest` `tests="52"`, `LibraryFallbackCheckTest` `tests="11"`, `PalbaseCodegenPluginTest` `tests="86"` — hepsi `failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add codegen-gradle/src/main/kotlin/io/palbase/gradle/AndroidVariantIntegration.kt codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "fix(codegen): Isolated Projects açıkken library fallback denetimi koşmaz ve bunu library başına bir kez söyler — başka projenin modelini okuyup build'i IP ihlaliyle düşürmüyor"`

---

### T027: README ortamı build type'ın seçtiğini anlatır — örneğini test derler; desteklenen aralık yazılı
<!-- deps: [T004, T008, T009, T010, T011, T017, T022, T026] | files: [README.md, codegen-gradle/build.gradle.kts, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-212] -->

FR-212'nin README yarısı. `README.md:94-121` hâlâ 2.3'ü anlatıyor: "Which environment a build compiles is the `palbase.env` Gradle property, and `local` when it is unset", `-Ppalbase.env=main` ve `palbase/` için `environmentsDir` bloğu (`reports/verification-2026-09-25.md` E3). Gereksinim tablosu (`README.md:55-63`) AGP/Gradle/JDK söylemiyor; 2.5 ise Gradle 8.5'in `BuildFeatures`'ını ve 8.11'in `ProjectDependency.getPath`'ini kullanıyor (E5, YENİ "Gradle floor"). Prototipin README'si sırayı "command line first" diye anlatıp `-Ppalbase.env`'i yanlış tarif etmişti (C-YENİ) — bu yüzden README'nin örneği artık İDDİA değil, TEST: işaretli blokları (`<!-- palbase-example: <ad> -->`, GitHub'da görünmez) TestKit'te aynen derlenir ve README'nin alıntıladığı her `Palbase: …` satırı ile her variant'ın paketlediği `palbase_environment` tutulur.

Örnek şartnamenin "Hedef son durum"udur: kök `gradle.properties`'te `palbase.env.debug=main` / `palbase.env.release=main`, `create("featureX")` adıyla `featureX`'i, `create("featureProfileUpdate")` DSL'le `feature-profile-update`'i derler; kişisel `local.properties` satırı `debug`'ı `featureX`'e çevirir. Metin sonraki sıra (9 adım), aynı yerde variant > flavor kombinasyonu > flavor, flavor↔build type reddi, bir blok ile AYNI build'lerin dosya anahtarı arasındaki ret (başka build'leri de kapsayan anahtarı blok geçer — T010/T011), modül `gradle.properties` reddi, bilinmeyen anahtar uyarısı, birebir ad, loopback reddi, library fallback (`pre<Variant>Build`) ve Isolated Projects'i anlatır; blok yalnız üç yerin ulaşmadığı düzen için kalır. Yükseltme cümlesi "Upgrading from 2.3 or 2.4" der: upstream'in 2.4.0'ı (D-030) ortam seçimini değiştirmedi, 2.4'ten yükselten de aynı değişiklikleri görür.

`tasks.test` README'yi girdi olarak bildirir: aksi hâlde yalnız README değişince görev UP-TO-DATE kalır ve kayma görülmez. Ölçüldü (Adım 4'te): yeşilden sonra aynı koşu `> Task :test UP-TO-DATE`; README'de tek satır değişince (`(from the build type name)` → `(the build type name)`) `> Task :test FAILED`, mesaj ``../README.md quotes `Palbase: featureX → featureX (the build type name) [palbase/environments]`; the build printed: …`` (README geri alındı). Ayrıca `PalbaseCodegenPluginTest`'teki 2.3 yorumu ("THE ENVIRONMENT IS ONE KEY THE USER OWNS … Unset means `local`", E3) bugünkü davranışa çevrilir; test kodu değişmez.

**Interfaces:**
- Consumes: T004 `palbase { environment }` (build type), T008 `local.properties` yalnız debuggable, T009 dosyadan seçim, T017 `[<kök>]`, T022 `palbase_environment`; test yardımcıları `fixture(module, imports)`, `Fixture.environment(name, base)`, `Fixture.asset(variant)`, `assertCompiled(project, variant, env)`
- Produces:
  - `PalbaseCodegenPluginTest`: `private fun assertExampleHolds(readme: Path)` — bir README'nin işaretli örneğini derler ve alıntıladığı satırları tutar
  - README işaret dili: `<!-- palbase-example: gradle.properties | app/build.gradle.kts | output | local.properties | local output -->`, her birinin hemen ardındaki fence
  - `codegen-gradle/build.gradle.kts`: `tasks.test` girdisi `readme` (`../README.md`)
  - README gereksinim satırları: `Android Gradle Plugin | 8.10.1 or newer`, `Gradle | 8.11.1 or newer — …`, `JDK running Gradle | 17 or newer`

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  (1/3) şu bloğu:
```kotlin
    // THE ENVIRONMENT IS ONE KEY THE USER OWNS, AND ITS DEFAULT IS `local`.
    //
    // Every environment `palbase link` resolved sits in its own directory, and
    // which one a build compiles is decided by the BUILD — `-Ppalbase.env=<name>`
    // — rather than by a `default_environment` field whoever linked last wrote
    // into the config. Unset means `local`, the environment every checkout gets
    // for free from `palbase start`.
```
  şununla değiştir:
```kotlin
    // WITH NO KEY ANYWHERE, DEBUG COMPILES `local`.
    //
    // Every environment `palbase link` resolved sits in its own directory, and
    // which one a variant compiles is decided by the BUILD — its build type, or
    // `-Ppalbase.env=<name>` for every variant of one invocation — rather than
    // by a `default_environment` field whoever linked last wrote into the
    // config. Debug's default is `local`, the stack `palbase start` runs on this
    // machine; a release has none (EnvironmentResolver).
```
  (2/3) şu satırlardan sonra:
```kotlin
        assertFalse(project.hasAsset("release"), "a name outside palbase/environments/ was read")
    }
```
  şunu ekle:
```kotlin

    // FR-212: THE README'S EXAMPLE IS BUILT, NOT BELIEVED. It tells a reader
    // what to commit and what every variant then prints; this builds exactly
    // the blocks it marks and holds it to each line it quotes and to the
    // environment each variant packs. A README that describes an order the
    // plugin does not follow — 2.5's first draft put the command line first —
    // fails here, not in a reader's build.
    @Test
    fun `the README example compiles what the README says`() {
        assertExampleHolds(Path.of("..", "README.md"))
    }
```
  (3/3) şu satırlardan sonra:
```kotlin
        )
        return project
    }
```
  şunu ekle:
````kotlin

    /**
     * Builds the example [readme] shows and asserts what it quotes. Each block
     * is the fence after an `<!-- palbase-example: <name> -->` comment, which
     * GitHub does not render: `gradle.properties` is the checkout root's;
     * `app/build.gradle.kts` is added to the app module's script, its imports on
     * top; every `output` line — `Palbase: <variant> → <environment> …` — names
     * a variant to generate and an environment to write. `local.properties` is
     * added afterwards, and `local output` is what the same build prints then.
     */
    private fun assertExampleHolds(readme: Path) {
        val text = Files.readString(readme)
        fun block(name: String): List<String> {
            val marker = "<!-- palbase-example: $name -->"
            val at = text.indexOf(marker)
            assertTrue(at >= 0, "$readme shows no `$marker` block")
            val body = text.indexOf('\n', text.indexOf("```", at)) + 1
            return text.substring(body, text.indexOf("\n```", body)).lines()
        }

        val script = block("app/build.gradle.kts")
        val project = fixture(module = "app", imports = script.filter { it.startsWith("import ") }.joinToString("\n"))
        project.app.resolve("build.gradle.kts")
            .append("\n" + script.filterNot { it.startsWith("import ") }.joinToString("\n") + "\n")
        project.root.resolve("gradle.properties").write(block("gradle.properties").joinToString("\n", postfix = "\n"))

        fun holds(quoted: List<String>) {
            val compiled = quoted.map { line ->
                val (variant, environment) = Regex("""Palbase: (\S+) → (\S+) .*""").matchEntire(line)?.destructured
                    ?: error("$readme quotes `$line`, which is no generation line")
                variant to environment
            }
            compiled.forEach { (_, environment) ->
                if (!Files.isDirectory(project.root.resolve("palbase/environments/$environment"))) {
                    project.environment(environment, base = project.root)
                }
            }
            val tasks = compiled.map { (variant, _) -> "generatePalbase" + variant.replaceFirstChar { it.uppercase() } }
            val output = project.build(*tasks.toTypedArray()).output
            quoted.forEach { assertTrue(output.contains(it + "\n"), "$readme quotes `$it`; the build printed:\n$output") }
            compiled.forEach { (variant, environment) ->
                assertCompiled(project, variant, environment)
                assertTrue(project.asset(variant).contains("\"palbase_environment\":\"$environment\""), project.asset(variant))
            }
        }
        holds(block("output"))
        project.root.resolve("local.properties").write(block("local.properties").joinToString("\n", postfix = "\n"))
        holds(block("local output"))
    }
````
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the README example*'` (`JAVA_HOME` = Android Studio JBR, `ANDROID_HOME` ayarlı; `:test` şart — iki nokta olmadan `--tests` `:codegen-engine:test`'e de gider) · Beklenen: **FAIL**, `PalbaseCodegenPluginTest` `tests="1" skipped="0" failures="1" errors="0"`, `BUILD FAILED in 1s`; mesaj ``../README.md shows no `<!-- palbase-example: app/build.gradle.kts -->` block ==> expected: <true> but was: <false>``.
- [ ] **Adım 3: Uygula** —

  `README.md`:
  (1/2) şu satırlardan sonra:
```markdown
| JVM target | 17 |
```
  şunu ekle:
```markdown
| Android Gradle Plugin | 8.10.1 or newer |
| Gradle | 8.11.1 or newer — the plugin uses `BuildFeatures` (Gradle 8.5) and `ProjectDependency.getPath` (Gradle 8.11), and AGP 8.10.1 itself needs 8.11.1 |
| JDK running Gradle | 17 or newer |
```
  (2/2) şu bloğu:
````markdown
The plugin reads ONE environment out of `palbase/environments/<environment>/`,
which is where `palbase link` writes that environment's `openapi.json` — which
carries the role definitions inside itself, as `x-palbase-roles` — and its
`android-config.json`. Which environment a build compiles is
the `palbase.env` Gradle property, and `local` when it is unset:

```
./gradlew assembleDebug -Ppalbase.env=main
```

Set it in `gradle.properties` to change the default for a checkout, or pass it
per build; a CI job passes the environment it releases. A selected environment
with no directory FAILS the build naming itself — nothing is ever generated from
an environment that was not asked for.

The plugin generates Kotlin sources per Android variant and packages the runtime
config into the APK as `palbase/palbase-config.json`; no generated Kotlin needs
to be committed. The app config must contain nonempty `app_id`, `base_url`, and
`api_key` values written by the CLI.

Point the plugin somewhere else when `palbase/` lives beside the app module
rather than inside it:

```kotlin
palbase {
    environmentsDir.set(rootProject.layout.projectDirectory.dir("palbase/environments"))
}
```

Initialize once in `Application.onCreate`:
````
  şununla değiştir:
````markdown
`palbase link` writes every environment of the project to
`palbase/environments/<environment>/`: its `openapi.json` — which carries the
role definitions inside itself, as `x-palbase-roles` — and its
`android-config.json`. The plugin compiles ONE of them per build variant, and
**the variant's build type chooses which**.

No `palbase { }` block is needed to find them. The plugin looks in
`<module>/palbase/environments`, `<root project>/palbase/environments` and —
while the root project is not the checkout itself (it holds neither `.git` nor
`palbase/project.json`), as in React Native and Flutter, where Gradle runs in
`android/` — `<root project>/../palbase/environments`. Exactly one may exist:
two are refused, naming both. With none, nothing is generated and that is not an
error: each variant prints so, naming every place it looked.

Out of the box:

| Build type | Compiles |
|---|---|
| `debug` | `local` — the stack `palbase start` runs on this machine |
| `release` | nothing until you choose: `generatePalbaseRelease` FAILS and says how |
| any other, e.g. `create("featureX")` | the environment of the same name, `palbase/environments/featureX/` |
| `benchmarkRelease`, `nonMinifiedRelease` | whatever `release` compiles |
| `benchmark` | whatever its first `matchingFallbacks` entry compiles, else `release` |

Commit the choice in the ROOT `gradle.properties`, one key per build type:

<!-- palbase-example: gradle.properties -->
```properties
palbase.env.debug=main
palbase.env.release=main
```

A build type can also name its environment itself — the way to reach an
environment whose name a build type cannot carry:

<!-- palbase-example: app/build.gradle.kts -->
```kotlin
import io.palbase.gradle.palbase // Kotlin DSL only; Groovy needs no import

android {
    buildTypes {
        create("featureX") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
        }
        create("featureProfileUpdate") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
            palbase { environment = "feature-profile-update" }
        }
    }
}
```

Every generation prints what it compiled, why, and from which
`palbase/environments`:

<!-- palbase-example: output -->
```text
Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]
Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]
Palbase: featureX → featureX (from the build type name) [palbase/environments]
Palbase: featureProfileUpdate → feature-profile-update (palbase { environment } in the `featureProfileUpdate` build type) [palbase/environments]
```

The packaged `palbase/palbase-config.json` names it as well —
`"palbase_environment":"featureX"` — so an APK says what it was built against
even when the task was UP-TO-DATE and printed nothing.

`initWith(getByName("debug"))` makes a feature build type debuggable and
debug-signed, so Android Studio's Run installs it. It copies what `debug` is at
that moment — write any `debug { }` block above it — and never copies
`palbase { environment }`. `matchingFallbacks += listOf("debug")` is needed only
when the app depends on an Android library module. In the Kotlin DSL a
`palbase { }` inside a build type needs the import above: without it the block
is the project's, and `environment` there stops the script compiling, naming
this fix.

Aim your own builds elsewhere without touching a committed file: the root
`local.properties` chooses for debuggable variants.

<!-- palbase-example: local.properties -->
```properties
palbase.env.debug=featureX
```

<!-- palbase-example: local output -->
```text
Palbase: debug → featureX (palbase.env.debug in local.properties) [palbase/environments]
```

For one build, pass the key on the command line — `-Ppalbase.env.release=staging`
— or `-Ppalbase.env=staging` for every variant of that invocation, the recipe
for a CI job.

A key names a variant (`paidRelease`), a product flavor (`paid`), a flavor
combination (`paidStaging`) or a build type (`release`); product flavors take
`palbase { environment = "…" }` as build types do. The first place that names an
environment wins:

1. `-Ppalbase.env.<variant|flavor|build type>` on the command line — or the same
   key as an override: `ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*` or
   `~/.gradle/gradle.properties`;
2. `-Ppalbase.env` on the command line, or as an override: every variant;
3. the root `local.properties` — debuggable variants only; any other variant
   ignores the line, and a warning names it;
4. `palbase { environment }` in the variant's product flavors and build type;
5. the root `gradle.properties` file;
6. `benchmark…` and `nonMinified…` build types follow the build they measure;
7. `palbase.env`, the 2.3 global property, in the root `gradle.properties`;
8. a build type other than `debug` and `release`: its own name;
9. `debug`: `local`. `release`: refused.

Within one place a variant key beats a flavor key, and a flavor combination
beats one flavor. A flavor and a build type that name different environments in
one place are refused, naming both — and so are a `palbase { environment }` and
a `gradle.properties` key for the SAME builds (that build type, that flavor, or
the variant itself) that disagree. A `gradle.properties` key that also covers
other builds — `palbase.env.release` under a flavor's block — is simply
outranked. A `palbase.env` line in a module's OWN `gradle.properties` fails that
module, naming the root file; a key that names nothing in the module is warned
about.

A chosen environment with no directory FAILS the build, naming it and where the
name came from: nothing is ever generated from an environment that was not asked
for, and names match exactly — `Main` is not `main`, on macOS either. A variant
that is not debuggable refuses a loopback or plain-`http` `base_url`, and never
gets the cleartext allowance a local stack needs.

**The module that applies the plugin is the one whose build types choose.** If
the client lives in a library (`:data`) and the app has a build type or flavor
the library lacks (`featureX` with `matchingFallbacks += listOf("debug")`), AGP
packs the library's `debug` build — and its environment — into the `featureX`
APK. The plugin checks every app variant that packs the library: one that would
select a different environment itself FAILS at `pre<Variant>Build`, naming both;
the other variants and IDE sync are untouched. Declare the build type in the
library too, or say the stand-in is intended, naming its environment in
`palbase.env.featureX`. Under Isolated Projects the check cannot read the app
modules, and says so once.

Only a layout none of the three places reaches needs the block, and it is then
the only place read:

```kotlin
palbase {
    environmentsDir.set(layout.projectDirectory.dir("../shared/palbase/environments"))
}
```

Upgrading from 2.3 or 2.4 changes what some builds compile; [CHANGELOG.md](CHANGELOG.md)
lists every change.

The plugin generates Kotlin sources per Android variant and packages the runtime
config into the APK as `palbase/palbase-config.json`; no generated Kotlin needs
to be committed. The app config must contain nonempty `app_id`, `base_url`, and
`api_key` values written by the CLI.

Initialize once in `Application.onCreate`:
````

  `codegen-gradle/build.gradle.kts`:
  şu satırlardan sonra:
```kotlin
    useJUnitPlatform()
```
  şunu ekle:
```kotlin
    // PalbaseCodegenPluginTest builds the example the README shows. An edited
    // README must run the suite again, not leave it UP-TO-DATE.
    inputs.files("../README.md").withPropertyName("readme").withPathSensitivity(PathSensitivity.RELATIVE)
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the README example*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 5s` (PalbaseCodegenPluginTest `tests="1"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 2s`, `EnvironmentResolverTest` `tests="52"`, `LibraryFallbackCheckTest` `tests="11"`, `PalbaseCodegenPluginTest` `tests="87"` — hepsi `failures="0" errors="0"`. Kayma provası (isteğe bağlı): filtreli komutu aynen tekrar koş → `> Task :test UP-TO-DATE`, `BUILD SUCCESSFUL in 414ms`; README'de `(from the build type name)` → `(the build type name)` yap, tekrar koş → `> Task :test FAILED`, `BUILD FAILED in 5s`; README'yi geri al.
- [ ] **Adım 5: Commit** — `git add README.md codegen-gradle/build.gradle.kts codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "docs(codegen): README ortamı build type'ın seçtiğini anlatır — örneği test derliyor; desteklenen aralık AGP 8.10.1+ / Gradle 8.11.1+ / JDK 17+"`

---

### T028: Public README (distribution/README.md) 2.5.0 — aynı denetlenen örnek, 2.3'ten yükseltme notu, desteklenen aralık
<!-- deps: [T027] | files: [distribution/README.md, codegen-gradle/build.gradle.kts, codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt] | satisfies: [FR-212] -->

Tüketicinin okuduğu sayfa bu: `scripts/publish.sh:123` `distribution/README.md`'yi `palgroup/palbackend-android`'e kopyalıyor (E3(d); public kopya src'dekiyle birebir, yalnız src düzenlenir). Değişenler: sürüm pinleri `2.3.0` → `2.5.0` (`:54`, `:66`), gereksinim tablosuna AGP/Gradle/JDK satırları, `:89` "Tried on Android Gradle Plugin 8.11 and 9.1, Gradle 8.13 and 9.3." yerine ÖLÇÜLEN çiftler (TestKit AGP 8.10.1/Gradle 8.11.1; tüketici AGP 8.11.1/Gradle 8.13 — T032'deki trial kopyası — ve AGP 9.1.1/Gradle 9.3.1 — T033'teki kullanıcı app'i ve slice 3'ün `s3-lib-agp9`'u), "Configure" bölümü (`:105-119`, `palbase.env` / `local when unset` / blok) build type anlatımıyla ve T027'nin AYNI işaretli örneğiyle, sıranın altında aynı-yer ve blok↔dosya reddi (README ile aynı kural), yeni "Upgrading from 2.3" alt bölümü (legacy `palbase.env` korunur, release reddi ve AGP 8'de `test`/`check`, özel build type'ın adı, `-Ppalbase.env`'in yeri, loopback reddi, iki kök reddi, modülün kendi `gradle.properties`'indeki satırın reddi, library fallback'inin `pre<Variant>Build` reddi — her biri build'in bastığı cümlenin ilk kelimeleriyle —, alt sınır, ve public ağacın atladığı 2.4.0'ın değişikliği: 2.4.0 orada yayımlanmadı (`palgroup/palbackend-android` yalnız `v2.3.0` taşıyor), oradan yükselten onu da bu sürümle alır — beyan edilen 401/429/doğrulama kodu kendi tipli vakasına ulaşır, o sınıfları kuran ve eski palbe-core'a karşı derlenmiş bir kütüphane yeniden derlenmeli; kaynağı upstream CHANGELOG'unun `## 2.4.0` bölümü). `local.properties`, flavor↔build type ve blok↔dosya reddi yükseltme listesine GİRMEZ: 2.3 `local.properties`'i, build type anahtarlarını ve bloğu hiç okumuyordu (`providers.gradleProperty("palbase.env")`, e72f704 `PalbaseCodegenPlugin.kt:28`) — bir 2.3 checkout'u bunlara yükseltmede takılamaz; "Configure" bölümü anlatır. ve "Versions" örneği `v2.5.0`. Aynı yardımcı bu dosyayı da derler; `tasks.test` girdisi iki README'yi de kapsar.

**Interfaces:**
- Consumes: `assertExampleHolds(readme: Path)` (T027); T027'nin örnek blokları
- Produces:
  - `PalbaseCodegenPluginTest`: `the public README example compiles what it says`
  - `tasks.test` girdisi `readmes` (`../README.md`, `../distribution/README.md`)
  - `distribution/README.md`: pinler `2.5.0`, `### Upgrading from 2.3` (2.4.0'ın değişikliği dahil)

- [ ] **Adım 1: Kırmızı testi yaz** —

  `codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt`:
  şu satırlardan sonra:
```kotlin
        assertExampleHolds(Path.of("..", "README.md"))
    }
```
  şunu ekle:
```kotlin

    // …and the PUBLIC one: `scripts/publish.sh` copies distribution/README.md
    // into palgroup/palbackend-android, the page a consumer actually reads.
    @Test
    fun `the public README example compiles what it says`() {
        assertExampleHolds(Path.of("..", "distribution", "README.md"))
    }
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the public README*'` · Beklenen: **FAIL**, `tests="1" skipped="0" failures="1" errors="0"`, `BUILD FAILED in 1s`; mesaj ``../distribution/README.md shows no `<!-- palbase-example: app/build.gradle.kts -->` block ==> expected: <true> but was: <false>``.
- [ ] **Adım 3: Uygula** —

  `distribution/README.md`:
  (1/6) şu bloğu:
```markdown
    id("io.palbase.codegen") version "2.3.0" apply false
```
  şununla değiştir:
```markdown
    id("io.palbase.codegen") version "2.5.0" apply false
```
  (2/6) şu bloğu:
```markdown
    implementation("io.palbase:palbe:2.3.0")
```
  şununla değiştir:
```markdown
    implementation("io.palbase:palbe:2.5.0")
```
  (3/6) şu bloğu:
```markdown
| Firebase | the SDK pulls Firebase BOM 34 for push; Gradle picks that BOM, and 34 no longer ships the `-ktx` artifacts. Depend on `firebase-messaging`, `firebase-crashlytics`, `firebase-config`, not their `-ktx` twins |
| Manifest | the SDK declares `com.google.firebase.messaging.default_notification_channel_id` (`palbase_default`). An app that sets the same `<meta-data>` adds `tools:replace="android:value"` to its own |

Tried on Android Gradle Plugin 8.11 and 9.1, Gradle 8.13 and 9.3.

## Configure: the CLI fetches the contract and generates the client
```
  şununla değiştir:
```markdown
| Android Gradle Plugin | 8.10.1 or newer |
| Gradle | 8.11.1 or newer |
| JDK running Gradle | 17 or newer |
| Firebase | the SDK pulls Firebase BOM 34 for push; Gradle picks that BOM, and 34 no longer ships the `-ktx` artifacts. Depend on `firebase-messaging`, `firebase-crashlytics`, `firebase-config`, not their `-ktx` twins |
| Manifest | the SDK declares `com.google.firebase.messaging.default_notification_channel_id` (`palbase_default`). An app that sets the same `<meta-data>` adds `tools:replace="android:value"` to its own |

Tested on Android Gradle Plugin 8.10.1 with Gradle 8.11.1, 8.11.1 with Gradle
8.13, and 9.1.1 with Gradle 9.3.1.

## Configure: the CLI fetches the contract, the build type picks the environment
```
  (4/6) şu bloğu:
````markdown
It writes `palbase/project.json`, `palbase/environments/<env>/android-config.json`
(the app's URL and publishable key) and `palbase/environments/<env>/openapi.json`
(the contract). Commit the folder. After every `palbase push` of the backend, run
`palbase spec` to refresh the contract — the next build regenerates the client.

Which environment a build compiles is the `palbase.env` Gradle property
(`local` when unset). Set it in `gradle.properties`:

```
palbase.env=main
```

The plugin looks for `palbase/environments/` inside the module it is applied to.
When the CLI wrote `palbase/` at the repo root — the usual case — point it there:

```kotlin
palbase {
    environmentsDir.set(rootProject.layout.projectDirectory.dir("palbase/environments"))
}
```

Generated Kotlin lands under `build/generated/` per variant; nothing is committed.
````
  şununla değiştir:
````markdown
It writes `palbase/project.json` and, for every environment of the project,
`palbase/environments/<env>/android-config.json` (the app's URL and publishable
key) and `palbase/environments/<env>/openapi.json` (the contract). Commit the
folder. After every `palbase push` of the backend, run `palbase spec` to refresh
the contract — the next build regenerates the client.

The plugin finds `palbase/` on its own — in the module, at the root project, or
one directory above the root project when that is not the checkout itself, as in
React Native and Flutter — so no `palbase { }` block is needed. Each build
variant compiles ONE environment, and its build type chooses: `debug` compiles
`local` (the stack `palbase start` runs on this machine), `release` compiles
nothing until you choose — its generation fails and says how — and any other
build type compiles the environment of its own name.

Commit the choice in the root `gradle.properties`:

<!-- palbase-example: gradle.properties -->
```properties
palbase.env.debug=main
palbase.env.release=main
```

A feature environment is a build type of the same name, and a build type can
also name its environment itself:

<!-- palbase-example: app/build.gradle.kts -->
```kotlin
import io.palbase.gradle.palbase // Kotlin DSL only; Groovy needs no import

android {
    buildTypes {
        create("featureX") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
        }
        create("featureProfileUpdate") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
            palbase { environment = "feature-profile-update" }
        }
    }
}
```

Every generation prints what it compiled, why, and from which
`palbase/environments`:

<!-- palbase-example: output -->
```text
Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]
Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]
Palbase: featureX → featureX (from the build type name) [palbase/environments]
Palbase: featureProfileUpdate → feature-profile-update (palbase { environment } in the `featureProfileUpdate` build type) [palbase/environments]
```

`initWith(getByName("debug"))` makes the build type debuggable and debug-signed,
so Android Studio's Run installs it. It copies what `debug` is at that moment —
write any `debug { }` block above it — and never copies `palbase { environment }`.
`matchingFallbacks` is needed only when the app depends on an Android library
module.

Your own `local.properties`, which is never committed, chooses for debuggable
variants:

<!-- palbase-example: local.properties -->
```properties
palbase.env.debug=featureX
```

<!-- palbase-example: local output -->
```text
Palbase: debug → featureX (palbase.env.debug in local.properties) [palbase/environments]
```

For one build, pass `-Ppalbase.env.release=staging`; `-Ppalbase.env=staging`
sets every variant of that invocation. A key names a variant (`paidRelease`), a
product flavor (`paid`), a flavor combination (`paidStaging`) or a build type,
and product flavors take `palbase { environment }` too. The first place that
names an environment wins:

1. `-Ppalbase.env.<variant|flavor|build type>` on the command line, or the same
   key from `ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*` or
   `~/.gradle/gradle.properties`;
2. `-Ppalbase.env` on the command line, or from those same places: every variant;
3. the root `local.properties` — debuggable variants only;
4. `palbase { environment }` in the variant's product flavors and build type;
5. the root `gradle.properties` file;
6. `benchmark…` and `nonMinified…` build types follow the build they measure;
7. `palbase.env`, the 2.3 global property, in the root `gradle.properties`;
8. a build type other than `debug` and `release`: its own name;
9. `debug`: `local`. `release`: refused.

Within one place a variant key beats a flavor key, and a flavor combination beats
one flavor. A flavor and a build type that name different environments in one
place are refused, naming both; so are a `palbase { environment }` block and a
`gradle.properties` key for the same builds that disagree.

A chosen environment with no directory fails the build, naming it — nothing is
ever generated from an environment nobody asked for — and a variant that is not
debuggable refuses a loopback or plain-`http` `base_url`. The packaged
`palbase/palbase-config.json` names the environment it was built from
(`palbase_environment`).

If the client lives in a library module and the app has a build type or flavor
the library lacks, AGP packs one of the library's own builds into that APK. The
plugin checks it: an app variant that would get another environment than it
selects fails at `pre<Variant>Build`, and says how to fix it.

Generated Kotlin lands under `build/generated/` per variant; nothing is committed.
````
  (5/6) şu satırlardan sonra:
```markdown
naming the two steps: upgrade and push the backend, then `palbase link`.
```
  şunu ekle:
```markdown

### Upgrading from 2.3

Bump the plugin and the library together, to 2.5.0. Then:

- `palbase.env=main` in `gradle.properties` still compiles `main` for every build
  type, and the `palbase { environmentsDir.set(…) }` block still works. To choose
  per build type, replace the line with `palbase.env.debug=main` and
  `palbase.env.release=main`, and delete the block.
- With no `palbase.env`, `release` no longer compiles `local`: its generation
  fails until `palbase.env.release=<env>` is set, so `assembleRelease`,
  `assemble` and `build` stop there — on AGP 8, `test` and `check` too.
  `assembleDebug` and the IDE sync do not.
- With no `palbase.env`, a custom build type such as `staging` compiles
  `palbase/environments/staging/` instead of `local`, and fails if there is none.
- `-Ppalbase.env=<env>` still selects for every variant of that build — unless a
  `palbase.env.<variant|flavor|build type>` key comes from the command line or
  an override.
- A release, or any variant that is not debuggable, now refuses a loopback or
  plain-`http` `base_url`: a release built against the `palbase start` stack
  fails.
- Without the block, `palbase/` at the checkout root is found too (2.3 looked in
  the module only); a checkout that has both is refused, naming both.
- A `palbase.env` line in a module's own `gradle.properties` — 2.3 ignored it —
  now fails that module (`Palbase: app/gradle.properties sets …`): move it to the
  root `gradle.properties`.
- If the client lives in a library and the app has a build type or flavor the
  library lacks, an app variant that would select another environment than the
  library build it packs now fails at `pre<Variant>Build`
  (``Palbase: `:app` variant `featureX` packs `:lib`'s `debug` build — …``) —
  with no `palbase.env`, a `featureX` build type selects `featureX` while the
  library's `debug` compiles `local`. Declare the build type in the library, or
  set `palbase.env.featureX=<env>` in the root `gradle.properties` to say the
  stand-in is intended.
- Gradle 8.11.1 and Android Gradle Plugin 8.10.1 are the minimum.
- 2.4.0 was not published here, and its change comes with this version too:
  an error an endpoint declares for a 401, a 429 or a validation refusal now
  reaches its own typed case instead of `Other`. A library compiled against an
  older `palbe-core` that constructs `BackendError.Validation`, `RateLimited`
  or `Unauthorized` must be recompiled.
```
  (6/6) şu bloğu:
```markdown
Every release of the SDK is a tag here (`v2.3.0`) with notes, and a version in
```
  şununla değiştir:
```markdown
Every release of the SDK is a tag here (`v2.5.0`) with notes, and a version in
```

  `codegen-gradle/build.gradle.kts`:
  şu bloğu:
```kotlin
    // PalbaseCodegenPluginTest builds the example the README shows. An edited
    // README must run the suite again, not leave it UP-TO-DATE.
    inputs.files("../README.md").withPropertyName("readme").withPathSensitivity(PathSensitivity.RELATIVE)
```
  şununla değiştir:
```kotlin
    // PalbaseCodegenPluginTest builds the example each README shows. An edited
    // README must run the suite again, not leave it UP-TO-DATE.
    inputs.files("../README.md", "../distribution/README.md")
        .withPropertyName("readmes")
        .withPathSensitivity(PathSensitivity.RELATIVE)
```
- [ ] **Adım 4: Yeşil** — Run: `cd codegen-gradle && ../gradlew :test --offline --tests '*PalbaseCodegenPluginTest.the public README*'`, sonra `cd codegen-gradle && ../gradlew :test --offline` · Beklenen: filtreli koşu `BUILD SUCCESSFUL in 5s` (PalbaseCodegenPluginTest `tests="1"`, `failures="0"`); tam `cd codegen-gradle && ../gradlew :test --offline` → `BUILD SUCCESSFUL in 1m 3s`, `EnvironmentResolverTest` `tests="52"`, `LibraryFallbackCheckTest` `tests="11"`, `PalbaseCodegenPluginTest` `tests="88"` — hepsi `failures="0" errors="0"`.
- [ ] **Adım 5: Commit** — `git add distribution/README.md codegen-gradle/build.gradle.kts codegen-gradle/src/test/kotlin/io/palbase/gradle/PalbaseCodegenPluginTest.kt && git commit -m "docs(dist): public README 2.5.0 — ortamı build type seçer, örneği test derliyor; 2.3'ten yükseltme notu ve desteklenen aralık"`

---

### T029: CHANGELOG — 2.5'in tam "DAVRANIŞ DEĞİŞİKLİĞİ" listesi; alıntılanan her ret metni plugin'in bastığı
<!-- deps: [T026] | files: [CHANGELOG.md] | satisfies: [FR-212] -->

FR-212'nin CHANGELOG yarısı ve D-005. 2.5 minor numaralı ama kırıcı; ev geleneği bunu "DAVRANIŞ DEĞİŞİKLİĞİ" başlığıyla yazmak (`CHANGELOG.md:100-112`, 2.3.0; upstream'in 2.4.0'ı da öyle, `:16`). Prototipin CHANGELOG'u iki kırılmayı atlamıştı: `-Ppalbase.env`'in yeri ve Gradle alt sınırı (`reports/verification-2026-09-25.md` "YENİ — 2.4 is a breaking release under a minor number"). Liste tam olmalı, o yüzden kırmızı/yeşil bir denetim betiğiyle ölçülür: `Yayınlanmamış` bölümünde her davranış değişikliğinin bir izi aranır ve kullanıcıya basılan bir cümle alıntılanıyorsa aynı cümlenin plugin kaynağında TEK SATIRDA geçtiği de denetlenir — build'de bir hatayla karşılaşan, onun ilk kelimeleriyle CHANGELOG'da arayıp bulabilsin. Betik plugin reposuna girmez; planın yanındaki `tools/`'ta durur.

Tabanda `## Yayınlanmamış` boş ve hemen altında upstream'in yayımlanmış `## 2.4.0 — 2026-09-26` bölümü var (D-030). Yeni bölümler `## Yayınlanmamış`'ın altına, o bölümün ÜSTÜNE girer; `## 2.4.0` ve ``Son etiket: `v2.4.0` `` olduğu gibi kalır. Eski tabandan gelen commit burada CHANGELOG'da çakışır (ölçüldü: `CONFLICT (content): Merge conflict in CHANGELOG.md`); çözüm bu adımın metnidir — ekleme yeri aynı satır, `## Yayınlanmamış`. İki değer yeni tabana göre yazıldı: "Runtime ve engine kodu **2.4.0** ile aynı" (2.3.0 değil: 2.4.0 engine'i ve palbe-core'u değiştirdi) ve YAYIN'daki public ağaç maddesi — `palgroup/palbackend-android` yalnız `v2.3.0` taşıyor, oradan yükselten 2.4.0'ın değişikliğini de bu sürümle alır ve GitHub Release notu yalnız bu bölümdür (`publish.sh`'ın awk satırı). Madde "davranış değişikliği"ni küçük harfle yazar: `release-notes.sh` büyük harfli başlığı satır satır sayar.

Blok↔dosya reddinin cümlesi T010/T011'in kuralını söyler: AYNI build'lerin anahtarı çelişirse ret, başka build'leri de kapsayan anahtarı blok geçer. Listenin iki iddiası planlama koşusunda ölçüldü: AGP 8.11.1 / Gradle 8.13'te `palbase.env.release` yokken `./gradlew :app:test` → `> Task :app:generatePalbaseRelease FAILED` (``Palbase: `release` has no environment, and a release never gets one by default — …``); `:app:test :app:check -m` AGP 9.1.1'de yalnız `:app:generatePalbaseDebug SKIPPED`, AGP 8.11.1'de `:app:generatePalbaseDebug SKIPPED` ve `:app:generatePalbaseRelease SKIPPED` listeler. Runtime'ın ve engine'in 2.4.0'a göre değişmediği: `git diff --stat v2.4.0..HEAD -- codegen-engine shared palbe palbe-core palbe-call palbe-debug-ui palbe-integrity palbe-messaging palbe-notifications palbe-purchases` yalnız `palbe-core/src/test/kotlin/io/palbase/core/GeneratedConfigLoaderTest.kt | 19 +` (T022'nin testi; `1 file changed, 19 insertions(+)`, T028'in ağacında ölçüldü).

**Interfaces:**
- Consumes: T001–T026'nın davranışları ve ret metinleri (`EnvironmentResolver.kt`, `GeneratePalbaseTask.kt`, `AndroidVariantIntegration.kt`)
- Produces:
  - `CHANGELOG.md` `## Yayınlanmamış` altında, upstream'in `## 2.4.0` bölümünün üstünde: `### YAYIN — 11 artefakt birlikte 2.5.0; …` ve ``### DAVRANIŞ DEĞİŞİKLİĞİ — ortamı variant'ın build type'ı seçiyor (`io.palbase.codegen` 2.5.0)``
  - Plan aracı `tools/changelog-check.sh [başlık]` — T030 onu `## 2.5.0` başlığıyla yeniden koşar

- [ ] **Adım 1: Kırmızı testi yaz** — plan aracı `tools/changelog-check.sh` (planın dizininde — lead bu dosyayı `plan-plugin.md` ile birlikte palbase-cli'ye pathspec'le commit eder; plugin reposuna commit edilmez, plugin reposunun kökünde koşar):
```bash
#!/usr/bin/env bash
# Every 2.5 behaviour change is named in the CHANGELOG section that is being
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
echo "the section names every 2.5 behaviour change"
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run (plugin repo kökünde): `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/changelog-check.sh"` · Beklenen: **FAIL** (exit 1), ilk satır ``missing: release is refused — `a release never gets one by default` ``, son satır `22 missing`; `not in …` satırı yok (alıntılanacak her cümle kaynakta tek satırda duruyor).
- [ ] **Adım 3: Uygula** —

  `CHANGELOG.md`:
  şu satırlardan sonra:
```markdown
## Yayınlanmamış
```
  şunu ekle:
```markdown

### YAYIN — 11 artefakt birlikte 2.5.0; runtime ve engine kodu değişmedi

- Plugin, `palbase-codegen-engine` ve 8 runtime modülü tek sürümle çıkıyor;
  plugin POM'u engine'i aynı sürüme pinliyor. Runtime ve engine kodu 2.4.0 ile
  aynı; runtime'da değişen tek değer palbe-core'un `PALBASE_SDK_VERSION`'ı. Tüketici için
  kural aynı: plugin ve kütüphane AYNI sürüm.
- Desteklenen aralık ilk kez yazılı: **AGP 8.10.1+, Gradle 8.11.1+, JDK 17+**
  (README'ler). Denenen: TestKit'te AGP 8.10.1 / Gradle 8.11.1; tüketicide AGP
  8.11.1 / Gradle 8.13 ve AGP 9.1.1 / Gradle 9.3.1.
- Public dağıtım ağacı (`palgroup/palbackend-android`) 2.3.0'dan doğrudan
  2.5.0'a geçiyor: 2.4.0 orada yayımlanmadı. Oradan yükselten, bu dosyadaki
  `## 2.4.0` bölümünün davranış değişikliğini de alır — endpoint'in beyan
  ettiği 401/429/doğrulama kodu kendi tipli vakasına ulaşır; `Validation`,
  `RateLimited` ya da `Unauthorized` kuran ve palbe-core'un eski sürümüne
  karşı derlenmiş bir kütüphane yeniden derlenmeli.

### DAVRANIŞ DEĞİŞİKLİĞİ — ortamı variant'ın build type'ı seçiyor (`io.palbase.codegen` 2.5.0)

2.3'te tek bir cevap vardı: global `palbase.env`, yoksa `local` — her variant
için. `debug` ile `release` farklı yığınlara bakamıyordu ve bayrağı hiç görmeyen
bir makinede derlenen release sessizce `local`'i paketliyordu. Artık her variant
kendi ortamını seçer; ilk isabet kazanır:

1. komut satırında `-Ppalbase.env.<variant|flavor|build type>` — ya da aynı
   anahtar override olarak (`ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*`,
   `~/.gradle/gradle.properties`);
2. komut satırında (ya da override olarak) `-Ppalbase.env` — o çağrının BÜTÜN
   variant'ları;
3. kökteki `local.properties` — yalnız debuggable variant'larda;
4. variant'ın product flavor'larında ve build type'ında `palbase { environment = "…" }`;
5. kökteki `gradle.properties` DOSYASI;
6. `benchmark<X>` / `nonMinified<X>` `<x>` gibi; düz `benchmark` ilk
   `matchingFallbacks` girdisi gibi, yoksa `release` gibi;
7. `palbase.env`, 2.3'ün global özelliği (dosyadan);
8. `debug`/`release` dışındaki build type'ın KENDİ ADI;
9. `debug` → `local`; `release` → ret.

Aynı yerde variant anahtarı flavor'ınkini, flavor kombinasyonu tek flavor'ı
geçer; flavor ile build type aynı yerde farklı ortam söylerse ret (ikisi de
adlandırılır). Bir `palbase { environment }` bloğu ile AYNI build'lerin
`gradle.properties` anahtarı (o build type'ın, o flavor'ın ya da variant'ın
kendisinin) çelişirse ret; başka build'leri de kapsayan bir anahtarı — bir
flavor bloğunun altındaki `palbase.env.release` gibi — blok (4. adım) geçer.

Yükselten bir uygulamanın görebileceği değişiklikler:

- **`release` artık varsayılanla derlenmiyor.** `palbase.env`,
  `palbase.env.release` ya da release'te `palbase { environment }` yoksa
  `generatePalbaseRelease` durur:
  ``Palbase: `release` has no environment, and a release never gets one by default — …``
  (iki yolu da söyler). `assembleRelease`,
  `assemble` ve `build` burada düşer; AGP 8'de `test` ve `check` de —
  `testReleaseUnitTest` release'i üretir (ölçüldü: AGP 8.11.1'de `:app:test`
  `:app:generatePalbaseRelease FAILED`). `assembleDebug` ve IDE sync etkilenmez:
  ret yapılandırmada değil, görevin kendisinde. 2.3 bu durumda `local`'i
  paketliyordu.
- **`palbase.env` yoksa özel bir build type kendi adını derler.** 2.3'te
  `create("staging")` `local`'i derliyordu; 2.5'te `palbase/environments/staging/`'i
  derler (`staging → staging (from the build type name)`), dizin yoksa durur.
  `palbase.env=main` taşıyan checkout'ta değişen yok: eski global ad kuralından
  ÖNCE sorulur (`palbase.env, the 2.3 global property`) — aynı adlı bir dizin
  olsa da `main` kalır.
- **`-Ppalbase.env` komut satırında hâlâ bütün variant'ları seçer**, her dosyayı
  geçer; onu yalnız komut satırında ya da override olarak verilen
  `-Ppalbase.env.<variant|flavor|build type>` geçer. `ORG_GRADLE_PROJECT_*`,
  `-Dorg.gradle.project.*` ve `~/.gradle/gradle.properties`'ten gelen değer
  override diye etiketlenir ve komut satırıyla aynı sırada sayılır; commit
  edilmiş seçim yalnız kökteki `gradle.properties` dosyasıdır.
- **`local.properties` okunuyor, yalnız debuggable variant'ta.** Debuggable
  olmayan variant satırı yok sayar ve bir uyarı onu adlandırır:
  ``Palbase: `palbase.env.release=…` in local.properties is ignored for
  `release`, which is not debuggable — …``. Unutulmuş bir satır release'in
  yığınını seçemez.
- **Debuggable olmayan variant loopback ya da `http` base_url'i reddeder**
  (`https://127.0.0.1` dahil):
  ``Palbase: `release` is not debuggable, and environment `local` (…) has base_url … — a loopback address, over plain HTTP. …``.
  2.3'te `palbase.env=local` ile derlenen release 127.0.0.1'e bakan,
  cleartext izinli yeşil bir APK'ydı. Cleartext network-security-config artık
  yalnız debuggable variant'a yazılır.
- **Ortam adı birebir.** Ad TEK bir dizin adıdır (boş, `/`, `\`, baştaki `.`
  ve kontrol karakteri reddedilir) ve dizinle harf büyüklüğü dahil aynı olmalı:
  macOS'ta `palbase.env.release=Main` 2.3'te `main/`'i derliyordu; 2.5 ikizi ve
  eşleme satırını söyleyip durur (``… `main` differs from it only in letter
  case …``).
- **`palbase/` bloksuz bulunur.** Aranan yerler: `<modül>/palbase/environments`,
  `<kök proje>/palbase/environments` ve kök proje checkout'un kendisi değilse
  (`.git` da `palbase/project.json` da yok — React Native, Flutter)
  `<kök proje>/../palbase/environments`. 2.3 yalnız modüle bakıyordu: kökte
  `palbase/` olup bloğu olmayan bir checkout hiçbir şey üretmeden yeşil
  geçiyordu; 2.5 onu derler. Birden fazlası varsa durur:
  ``Palbase: `<V>` has more than one palbase/environments in reach — …``.
  Hiçbiri yoksa hiçbir şey üretilmez ve bu bir hata değildir; bir satır aranan
  her yeri sayar:
  `Palbase: <V> → nothing generated: no palbase/environments found (searched …)`.
  `palbase { environmentsDir.set(…) }`
  çalışmaya devam eder; o zaman tek aranan yer odur.
- **Modülün kendi `gradle.properties`'indeki `palbase.env*` satırı reddedilir.**
  2.3 onu sessizce yok sayıyordu; 2.5 o modülün her üretimini kök dosyayı
  adlandırarak durdurur (``… and a module's own gradle.properties never chooses
  an environment …``). Hiçbir variant'ı, flavor'ı ya da build type'ı
  adlandırmayan `palbase.env.*` anahtarı uyarılır.
- **Plugin bir library'deyse, başka ortam paketleyen app variant'ı düşer.**
  Library'nin tanımlamadığı bir build type ya da flavor'ı olan app variant'ı
  (`featureX`, `matchingFallbacks += listOf("debug")`) library'nin `debug`
  derlemesini — ve onun ortamını — paketler. Kendi seçeceği ortam başkaysa o
  variant'ın `pre<Variant>Build`'i ``Palbase: `:app` variant `featureX` packs
  `:lib`'s `debug` build — …`` ile düşer; configuration cache yeniden
  kullanıldığında da. Diğer variant'lar ve IDE sync etkilenmez. Kasıtlıysa:
  `palbase.env.<app variant>=<o ortam>`. Isolated Projects açıkken denetim
  koşamaz ve library başına bir kez söylenir (`Palbase: Isolated Projects is on,
  so … cannot read the app modules that pack it …`).
- **Genel API.** `GeneratePalbaseTask.environmentsDir` KALDIRILDI; yerine
  `environmentRoots` ve `environmentsAboveRoot` (`@Internal`). `environment`
  artık `@Optional`; yeni zorunlu girdiler `debuggable`, `rootProjectIsCheckout`
  ve `environmentWarnings`; yeni `environmentOrigin`, `environmentRefusal`,
  `variantName`, `buildTypeName`, `rootProjectDirectory`.
  `PalbaseExtension.environmentsDir`'in varsayılanı yok: okuyan bir script
  `.get()`'te düşer. `PalbaseCodegenPlugin` artık `abstract` ve
  `@Inject constructor(BuildFeatures)` — `plugins { id("io.palbase.codegen") }`
  ile uygulayanı etkilemez. Görev girdileri değiştiği için yükseltmeden sonraki
  ilk build'de build cache ıskalar.
- **Alt sınır: Gradle 8.11.1, AGP 8.10.1, JDK 17.** 2.3 bir sınır yazmıyordu.
  2.5 Gradle 8.5'te gelen `BuildFeatures`'ı ve 8.11'de gelen
  `ProjectDependency.getPath`'i kullanır; AGP 8.10.1'in kendisi de Gradle
  8.11.1 ister. AGP'nin `DslExtension` API'si 8.11'in altında `@Incubating`.

Yeni, kırmayan:

- Build type'ta ve product flavor'da `palbase { environment = "…" }`. Kotlin
  DSL'de `import io.palbase.gradle.palbase` gerekir; import'suz blok projenin
  bloğudur ve `environment` orada derlenmez, çareyi söyler
  (`@Deprecated(level = ERROR)`); Groovy'de aynı çağrı bir `GradleException`.
- Flavor anahtarları: `palbase.env.<flavor>` ve birleşik
  `palbase.env.<flavorCombination>`.
- Her üretim tek satır basar:
  `Palbase: <variant> → <ortam> (<köken>) [<kök>]`, kök kök projeye göre göreli.
- Paketlenen `palbase/palbase-config.json` derlendiği ortamı taşır:
  `"palbase_environment":"<ortam>"`. Runtime bilmediği alanı yok sayar
  (palbe-core'da bir testle sabit).
- Hata metinleri çareyi söyler: `local`'i olmayan checkout'ta debug
  `palbase.env.debug=<taşınan ortam>`'ı ya da `palbase start` + `palbase link`'i;
  sözleşmesi olmayan ortam `palbase push --env <ortam>`, sonra `palbase link`'i
  (`local` için `palbase start`, sonra `palbase link`).

Göç: `palbase.env=main` taşıyan checkout'un yapması gereken bir şey yok. Build
type başına seçmek için satırı `palbase.env.debug=main` +
`palbase.env.release=main` ile değiştirin ve `palbase { environmentsDir.set(…) }`
bloğunu silin. `palbase.env`'i olmayan checkout release için
`palbase.env.release=<ortam>` ekler.
```
- [ ] **Adım 4: Yeşil** — Run: `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/changelog-check.sh"` · Beklenen: `the section names every 2.5 behaviour change`, exit 0.
- [ ] **Adım 5: Commit** — `git add CHANGELOG.md && git commit -m "docs: CHANGELOG 2.5.0 — ortamı build type seçiyor; tam DAVRANIŞ DEĞİŞİKLİĞİ listesi, alıntılanan her ret metni plugin'in bastığı"`

---

### T030: 2.5.0 sürüm kesimi ve yerel prova — CHANGELOG başlığı, README yayın örneği; README kapısı, 11 artefakt, POM zinciri
<!-- deps: [T003, T027, T028, T029] | files: [CHANGELOG.md, README.md] | satisfies: [FR-212, FR-213] -->

D-005/E1: plugin, engine ve 8 runtime modülü birlikte 2.5.0 — `publish.sh` 11'den az dizinle bitmez (`publish.sh:119-120`), plugin POM'u engine'i aynı sürüme pinler; plugin-only yol yok ve açılmaz. Sürüm kodda yazılı değil, `PALBE_VERSION` ortam değişkeninden gelir (`codegen-gradle/build.gradle.kts:54-59`); bu görev yalnız yayın commit'ini yapar (2.3.0'ın `8b74104`'ü gibi: `## Yayınlanmamış`'ın altına `## 2.5.0 — <gün>`, "Son etiket" `v2.5.0`, README'nin yayın örneği) ve yayını YEREL olarak prova eder. Kırmızı/yeşil, `publish.sh`'ın GitHub Release'e koyduğu notun kendisidir: onun awk satırıyla `## 2.5.0` bölümü çıkarılır; başlık yokken not boştur.

Tabanda "Son etiket" ``v2.4.0``'dır (upstream'in yayımlanmış 2.4.0'ı, D-030) ve `## Yayınlanmamış`'ın altında önce T029'un iki bölümü, sonra upstream'in `## 2.4.0 — 2026-09-26` bölümü durur. Yeni başlık `## Yayınlanmamış` ile T029'un `### YAYIN`'ı arasına girer; `## 2.4.0` bölümüne dokunulmaz, "Son etiket" `v2.4.0` → `v2.5.0` olur. Eski tabandan gelen commit burada CHANGELOG'da çakışır (ölçüldü: `CONFLICT (content): Merge conflict in CHANGELOG.md`; README otomatik birleşir ama eski sürümü yazar); çözüm bu adımın metnidir.

FR-213 T003'te uygulandı (kök `gradle.properties`'te `palbase.env.release=local`); burada son HEAD'de README kapısı yeniden ölçülür. Prova yalnız reponun kendi yerel adımlarıdır: `scripts/verify-publications.sh` ve `publish.sh`'ın 2. adımı (`build/test-repository` ve `codegen-gradle/build/test-repository`'ye, ikisi de `build/` altında) — GitHub'a, GitHub Pages'e ya da `~/.m2`'ye hiçbir şey gitmez. Tarih yayın günüdür (`date +%F`); bu provada `2026-09-27`.

**Interfaces:**
- Consumes: T029'un `CHANGELOG.md` bölümü ve `tools/changelog-check.sh`; T003'ün `palbase.env.release=local`'i
- Produces:
  - Yayın commit'i: `CHANGELOG.md` `## 2.5.0 — <gün>`, ``Son etiket: `v2.5.0` ``; `README.md` yayın örneği `v2.5.0`
  - Plan araçları `tools/release-notes.sh <sürüm>`, `tools/release-dry-run-check.sh <sürüm>`
  - `build/test-repository` + `codegen-gradle/build/test-repository`'de 11 koordinat 2.5.0 — T032/T033 yayından ÖNCE bunlarla kopyada prova edebilir

- [ ] **Adım 1: Kırmızı testi yaz** — plan araçları, planın `tools/` dizininde (lead commit eder, T029 gibi):

  `tools/release-notes.sh`:
```bash
#!/usr/bin/env bash
# The notes scripts/publish.sh attaches to the GitHub Release of $1 — its own
# awk line — and whether they carry the whole 2.5 list.
version="$1"
notes="$(awk -v h="## $version" 'index($0,h)==1{p=1;next} p&&/^## /{exit} p' CHANGELOG.md)"
echo "notes for $version: $(wc -l <<<"$notes" | tr -d ' ') lines, $(grep -c 'DAVRANIŞ DEĞİŞİKLİĞİ' <<<"$notes") DAVRANIŞ DEĞİŞİKLİĞİ heading(s)"
grep -q 'DAVRANIŞ DEĞİŞİKLİĞİ' <<<"$notes" || { echo "publish.sh would attach no 2.5 notes for $version"; exit 1; }
"$(dirname "$0")/changelog-check.sh" "## $version"
```

  `tools/release-dry-run-check.sh` (Adım 4'te):
```bash
#!/usr/bin/env bash
# After publish.sh's step 2 (the local Test repositories): what step 3 would
# copy into the public tree, and what a consumer resolves from it.
# usage (repo root): release-dry-run-check.sh <version>
set -u
v="$1"
roots=(build/test-repository codegen-gradle/build/test-repository)
dirs="$(find "${roots[@]}" -type d -name "$v" | sed 's|.*/test-repository/||' | sort)"
n="$(grep -c . <<<"$dirs")"
echo "artifact directories for $v: $n"
sed 's/^/  /' <<<"$dirs"
repo=codegen-gradle/build/test-repository/io/palbase
grep -A1 "<artifactId>palbase-codegen-engine</artifactId>" "$repo/codegen-gradle/$v/codegen-gradle-$v.pom" \
  | grep -q "<version>$v</version>" && echo "plugin POM pins palbase-codegen-engine $v"
grep -q "<artifactId>codegen-gradle</artifactId>" \
  "$repo/codegen/io.palbase.codegen.gradle.plugin/$v/io.palbase.codegen.gradle.plugin-$v.pom" \
  && echo "marker io.palbase.codegen $v → io.palbase:codegen-gradle $v"
major="$(unzip -p "$repo/codegen-gradle/$v/codegen-gradle-$v.jar" io/palbase/gradle/PalbaseCodegenPlugin.class \
  | od -An -j6 -N2 -tu1 | awk 'NF {print $1 * 256 + $2}')"
echo "plugin class file major version $major (61 = Java 17)"
[[ "$n" == 11 ]]
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run (plugin repo kökünde): `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/release-notes.sh" 2.5.0` · Beklenen: **FAIL** (exit 1), `notes for 2.5.0: 1 lines, 0 DAVRANIŞ DEĞİŞİKLİĞİ heading(s)` ve `publish.sh would attach no 2.5 notes for 2.5.0`.
- [ ] **Adım 3: Uygula** — (`2026-09-27` yerine yayın günü, `date +%F`)

  `CHANGELOG.md`:
  şu bloğu:
```markdown
bilmelidir. Son etiket: `v2.4.0`.

## Yayınlanmamış

### YAYIN — 11 artefakt birlikte 2.5.0; runtime ve engine kodu değişmedi
```
  şununla değiştir:
```markdown
bilmelidir. Son etiket: `v2.5.0`.

## Yayınlanmamış

## 2.5.0 — 2026-09-27

### YAYIN — 11 artefakt birlikte 2.5.0; runtime ve engine kodu değişmedi
```

  `README.md`:
  şu bloğu:
```markdown
git tag v2.3.0
PALBE_VERSION=2.3.0 scripts/publish.sh
git push origin main v2.3.0
```
  şununla değiştir:
```markdown
git tag v2.5.0
PALBE_VERSION=2.5.0 scripts/publish.sh
git push origin main v2.5.0
```
- [ ] **Adım 4: Yeşil + yerel prova** — hepsi plugin repo kökünde, `JAVA_HOME` = Android Studio JBR, `ANDROID_HOME` ayarlı. `publish…ToTestRepository` ve `verify-publications.sh` yalnız reponun kendi `build/` dizinlerine yazar (`build/test-repository`, `codegen-gradle/build/test-repository`, `build/verify-publications-*`): GitHub'a, Pages'e, `~/.m2`'ye hiçbir şey gitmez — yürüten ajan koşturabilir.
1. `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/release-notes.sh" 2.5.0` · Beklenen: `notes for 2.5.0: 150 lines, 1 DAVRANIŞ DEĞİŞİKLİĞİ heading(s)` ve `the section names every 2.5 behaviour change`.
  2. README kapısı (FR-213, NFR-002): `./gradlew check lintRelease :consumer-release:assembleRelease --configuration-cache --no-daemon --offline` · Beklenen: girdiyi ilk kez kaydeden koşu `BUILD SUCCESSFUL in 21s`, `872 actionable tasks: 43 executed, 1 from cache, 828 up-to-date`, `Configuration cache entry stored.`; aynı komut yeniden `BUILD SUCCESSFUL in 3s`, `866 actionable tasks: 29 executed, 837 up-to-date`, `Configuration cache entry reused.`
  3. SDK'nın kendi release'lerinin seçimi görünür olsun: `./gradlew :sample:generatePalbaseRelease --rerun :consumer-release:generatePalbaseRelease --rerun --offline` · Beklenen: iki kez `Palbase: release → local (palbase.env.release in gradle.properties) [sample/palbase/environments]`, `BUILD SUCCESSFUL in 1s`.
  4. Plugin kapısı: `./gradlew -p codegen-gradle check --configuration-cache --no-daemon --offline` · Beklenen: `BUILD SUCCESSFUL in 1m 4s` (`:validatePlugins` ve `:codegen-engine:test` koştu), `Configuration cache entry stored.` (bu klonda komutun ilk koşusu; önceden koşulduysa `reused`); `EnvironmentResolverTest` `tests="52"`, `LibraryFallbackCheckTest` `tests="11"`, `PalbaseCodegenPluginTest` `tests="88"`, codegen-engine 9 sınıf 47 test — hepsi `failures="0" errors="0"`; ikinci koşu `BUILD SUCCESSFUL in 2s`, `Configuration cache entry reused.`
  5. `PALBE_VERSION=2.5.0 ./scripts/verify-publications.sh` · Beklenen: son satır `Publication verification passed for 2.5.0` (palbe-core release `BuildConfig`: `PALBASE_SDK_VERSION = "2.5.0";`).
  6. `publish.sh`'ın 2. adımı, yerel: `PALBE_VERSION=2.5.0 ./gradlew publishReleasePublicationToTestRepository :codegen-engine:publishMavenPublicationToTestRepository --no-daemon --offline` ve `PALBE_VERSION=2.5.0 ./gradlew -p codegen-gradle :publishAllPublicationsToTestRepository --no-daemon --offline` · Beklenen: ikisi de `BUILD SUCCESSFUL` (`in 6s`, `in 3s`).
  7. `"$TOOLS/release-dry-run-check.sh" 2.5.0` · Beklenen: `artifact directories for 2.5.0: 11` ve altında `io/palbase/codegen-gradle/2.5.0`, `io/palbase/codegen/io.palbase.codegen.gradle.plugin/2.5.0`, `io/palbase/palbase-codegen-engine/2.5.0`, `io/palbase/palbe-call/2.5.0`, `io/palbase/palbe-core/2.5.0`, `io/palbase/palbe-debug-ui/2.5.0`, `io/palbase/palbe-integrity/2.5.0`, `io/palbase/palbe-messaging/2.5.0`, `io/palbase/palbe-notifications/2.5.0`, `io/palbase/palbe-purchases/2.5.0`, `io/palbase/palbe/2.5.0`; sonra `plugin POM pins palbase-codegen-engine 2.5.0`, `marker io.palbase.codegen 2.5.0 → io.palbase:codegen-gradle 2.5.0`, `plugin class file major version 61 (61 = Java 17)`; exit 0.
- [ ] **Adım 5: Commit** — `git add CHANGELOG.md README.md && git commit -m "release: 2.5.0 — ortamı build type seçiyor; plugin, engine ve runtime birlikte, runtime kodu değişmedi"` (etiket T031'de, yayından hemen önce).

---

### T031: 2.5.0'ı yayınla — KULLANICI koşturur (bu planda koşturulmadı): etiket, `publish.sh`, push, yayın sonrası denetim
<!-- deps: [T030] | files: [] | satisfies: [FR-212] -->

`v2.4.0` etiketi upstream'indir (`6e97598`, D-030) ve yerinde kalır; bu görev `v2.5.0`'ı açar. Public ağaçta 2.4.0 yok: `publish.sh`'ın metadata birleştirmesi oradaki sürümleri (`2.3.0`) korur ve `2.5.0`'ı ekler. Yayın geri alınamaz ve kimlik ister: `publish.sh` temiz ağaç, `v2.5.0` etiketli HEAD, oturum açmış `gh`, `write:packages` credential'ı (`GITHUB_TOKEN` ya da `~/.gradle/gradle.properties`'te `gpr.key`) ve `../palbackend-android`'de temiz bir `palgroup/palbackend-android` checkout'u ister (`publish.sh:31-67`); public Maven ağacına push eder, GitHub Release açar, GitHub Packages'a yayınlar. Bu yüzden ajan KOŞTURMAZ — adımlar kullanıcınındır; lead yanında durur. Şartnamenin sırası: önce `plan-cli.md`, sonra `plan-cloud.md`, sonra bu yayın; tüketici görevleri (T032–T034) bundan sonra. Yayın sonrası denetim betiği bu planda yalnız YEREL olarak prova edildi: T030'un iki Test deposu `publish.sh`'ın 3. adımı gibi tek ağaçta birleştirilip `BASE=file://…` ile.

Reponun kendisinde commit yok: `publish.sh` dağıtım reposunda kendi commit'ini atar (`release: Palbe v2.5.0 (binaries + docs)`) ve GitHub Release'in notu T030'un `## 2.5.0` bölümüdür. `palbackend-android-src` bir submodule değil (`git rev-parse --show-superproject-working-tree` boş) — işaretçi güncellemesi yok.

**Interfaces:**
- Consumes: T030'un yayın commit'i ve kapıları
- Produces:
  - `v2.5.0` etiketi; `https://palgroup.github.io/palbackend-android/` altında 11 koordinat 2.5.0; `palgroup/palbackend-android` GitHub Release `v2.5.0`
  - Plan aracı `tools/post-publish-check.sh <sürüm>` (`BASE` ile başka bir ağaca yöneltilebilir)

- [ ] **Adım 1: Kırmızı testi yaz** — plan aracı `tools/post-publish-check.sh`:
```bash
#!/usr/bin/env bash
# After scripts/publish.sh: the public Maven tree serves every coordinate a
# consumer resolves, at <version>. BASE points elsewhere for a dry run.
# usage: post-publish-check.sh <version>
set -u
v="$1"
base="${BASE:-https://palgroup.github.io/palbackend-android}"
fail=0
for coordinate in palbe palbe-core palbe-integrity palbe-notifications palbe-messaging palbe-purchases \
  palbe-call palbe-debug-ui palbase-codegen-engine codegen-gradle codegen/io.palbase.codegen.gradle.plugin; do
  name="${coordinate##*/}"
  if curl -sf -o /dev/null "$base/io/palbase/$coordinate/$v/$name-$v.pom"; then
    echo "ok    $coordinate $v"
  else
    echo "FAIL  $coordinate $v — $base/io/palbase/$coordinate/$v/$name-$v.pom"; fail=1
  fi
done
exit "$fail"
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/post-publish-check.sh" 2.5.0` (yayından ÖNCE, public ağaca karşı) · Beklenen: **FAIL** — bu planda koşturulmadı (ağa çıkmak yok). Aynı betiğin yerel provası ölçüldü: T030'un iki Test deposu tek ağaçta birleştirildi (`rsync -a build/test-repository/ codegen-gradle/build/test-repository/ <ağaç>/`); `BASE=file://<ağaç> post-publish-check.sh 2.6.0` (hiç üretilmemiş sürüm) → 11 `FAIL` satırı, ilki `FAIL  palbe 2.6.0 — file://<ağaç>/io/palbase/palbe/2.6.0/palbe-2.6.0.pom`, exit 1.
- [ ] **Adım 3: Uygula (KULLANICI)** — `palbackend-android-src` kökünde, `main`'de, T030'un commit'i HEAD'deyken:
```bash
export JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home"
export ANDROID_HOME="$HOME/Library/Android/sdk"
git status --porcelain                       # boş olmalı
git log -1 --format=%s                       # release: 2.5.0 — …
gh auth status                               # signed in
git -C ../palbackend-android status --porcelain   # boş olmalı; main'de
git tag v2.5.0
PALBE_VERSION=2.5.0 scripts/publish.sh
git push origin main v2.5.0
```
  `publish.sh`'ın başarı satırları (kaynaktan; koşturulmadı): `copied 11 artifact directories for 2.5.0 into …`, `public distribution serves 2.5.0`, 11 kez `  ok   io.palbase.… 2.5.0`, son satır `published 2.5.0 — now push the tag: git push origin main v2.5.0`.
- [ ] **Adım 4: Yeşil (KULLANICI)** — Run: `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/post-publish-check.sh" 2.5.0` ve `gh release view v2.5.0 --repo palgroup/palbackend-android --json body --jq .body | grep -c "DAVRANIŞ DEĞİŞİKLİĞİ"` · Beklenen: koşturulmadı. Yerel prova: `BASE=file://<ağaç> post-publish-check.sh 2.5.0` → 11 satır `ok    palbe 2.5.0` … `ok    codegen/io.palbase.codegen.gradle.plugin 2.5.0`, exit 0; notun kaynağı olan bölüm için T030 Adım 4.1 (`1 DAVRANIŞ DEĞİŞİKLİĞİ heading(s)`).
- [ ] **Adım 5: Commit** — plugin reposunda commit yok (etiket + push Adım 3'te); dağıtım reposundaki commit'i `publish.sh` atar.

---

### T032: palbe-trial-android 2.5.0'a geçer — build type anahtarları, blok yok, emekli `roles.json` ve `.gitattributes` yok
<!-- deps: [T031] | files: [build.gradle.kts, app/build.gradle.kts, gradle.properties, palbase/.gitattributes, palbase/environments/main/roles.json] | satisfies: [FR-301] -->

FR-301, `palbe-trial-android` reposunda (`acedb85`). Bu app 2.5'te DEĞİŞMEDEN de çalışır — eski global `palbase.env=main` ad kuralından önce sorulur (D-006) ve blok açık bir kök verir — ama seçimin yolu 2.3'ünkü kalır: `Palbase: debug → main (palbase.env, the 2.3 global property) [palbase/environments]`. Geçiş: iki sürüm pini (`build.gradle.kts:5`, `app/build.gradle.kts:38`), blok silinir (`app/build.gradle.kts:32-35` — `palbase/` kökte, kök `.git` taşıyor, bloksuz bulunur), `palbase.env=main` yerine `palbase.env.debug=main` + `palbase.env.release=main`, ve CLI'ın artık yazmadığı `palbase/.gitattributes` ile `palbase/environments/main/roles.json` (roller sözleşmenin içinde, `x-palbase-roles` — `openapi.json`'da var) `git rm` ile. Kabul bir betiktir: dosyaları, build satırlarını ve iki APK'nın `palbase_environment`'ını ölçer; argümanları Gradle'a geçer. Build'den önce `app/build/outputs/apk`'ı siler — önceki bir build'in APK'sı bu build'in yerine cevap vermesin (planlama koşusunda başarısız bir build'in yanında eski APK'lar `ok` verdi).

Gerçek depoda T031'den SONRA, argümansız koşar (sürümler public ağaçtan). Bu planda bir kopyada ölçüldü, iki yoldan: plugin scratch klonundan `includeBuild` ile (`-I proof-init.gradle.kts`) ve — yayının kendisini de sınayarak — plugin marker'ı, plugin, engine ve palbe 2.5.0 T030'un Test depolarından, `includeBuild` OLMADAN (`-I released-init.gradle.kts`). Yürüten ajan aynı provayı yayından önce yapabilir (init betikleri aşağıda; gerçek depoya girmezler). AGP 8.11.1 / Gradle 8.13. Commit mesajı reponun kendi git log'u gibi İngilizce.

**Interfaces:**
- Consumes: T031'in yayını (gerçek depo) — ya da T030'un Test depoları (prova)
- Produces:
  - `palbe-trial-android`: `id("io.palbase.codegen") version "2.5.0"`, `io.palbase:palbe:2.5.0`, `palbase.env.debug=main`, `palbase.env.release=main`
  - Plan araçları `tools/verify-trial.sh [gradle argümanları]`, `tools/proof-init.gradle.kts`, `tools/released-init.gradle.kts`

- [ ] **Adım 1: Kırmızı testi yaz** — plan aracı `tools/verify-trial.sh`:
```bash
#!/usr/bin/env bash
# FR-301 acceptance for palbe-trial-android, run at its root. Arguments go to
# Gradle as they are (the scratch proof passes an init script there).
set -u
fail=0
check() { if eval "$2"; then echo "ok    $1"; else echo "FAIL  $1"; fail=1; fi; }
check "the plugin is pinned at 2.5.0"   'grep -qF "id(\"io.palbase.codegen\") version \"2.5.0\"" build.gradle.kts'
check "palbe is pinned at 2.5.0"        'grep -qF "io.palbase:palbe:2.5.0" app/build.gradle.kts'
check "no palbase { } block"            '! grep -q "^palbase {" app/build.gradle.kts'
check "no global palbase.env"           '! grep -q "^palbase.env=" gradle.properties'
check "no palbase/.gitattributes"       '! test -e palbase/.gitattributes'
check "no roles.json"                   '! test -e palbase/environments/main/roles.json'
rm -rf app/build/outputs/apk   # an APK an earlier build left must not answer for this one
out="$(./gradlew --console=plain "$@" :app:generatePalbaseDebug --rerun :app:generatePalbaseRelease --rerun \
  :app:assembleDebug :app:assembleRelease 2>&1)"; code=$?
check "debug and release build"         '[ "$code" = 0 ]'
check "debug → main, from its own key"  'grep -qF "Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]" <<<"$out"'
check "release → main, from its own key" 'grep -qF "Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]" <<<"$out"'
for apk in app/build/outputs/apk/debug/app-debug.apk app/build/outputs/apk/release/app-release-unsigned.apk; do
  check "$(basename "$apk") packs main" 'unzip -p "$apk" assets/palbase/palbase-config.json 2>/dev/null | grep -qF "\"palbase_environment\":\"main\""'
done
grep -E "^Palbase: |FAILED$|^e: |What went wrong" <<<"$out" | head -8 | sed 's/^/  | /'
exit "$fail"
```

  Yalnız prova için (gerçek depoya girmez) — `tools/proof-init.gradle.kts`:
```kotlin
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
```

  ve `tools/released-init.gradle.kts`:
```kotlin
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
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run (trial repo kökünde): `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/verify-trial.sh"` (prova: `"$TOOLS/verify-trial.sh" -I "$TOOLS/proof-init.gradle.kts" --offline`) · Beklenen: **FAIL** (exit 1) — `FAIL  the plugin is pinned at 2.5.0`, `FAIL  palbe is pinned at 2.5.0`, `FAIL  no palbase { } block`, `FAIL  no global palbase.env`, `FAIL  no palbase/.gitattributes`, `FAIL  no roles.json`, `ok    debug and release build`, `FAIL  debug → main, from its own key`, `FAIL  release → main, from its own key`, iki APK `ok … packs main`; basılan satırlar `Palbase: debug → main (palbase.env, the 2.3 global property) [palbase/environments]` ve `Palbase: release → main (palbase.env, the 2.3 global property) [palbase/environments]`.
- [ ] **Adım 3: Uygula** —

  `build.gradle.kts`:
  şu bloğu:
```kotlin
    id("io.palbase.codegen") version "2.3.0" apply false
```
  şununla değiştir:
```kotlin
    id("io.palbase.codegen") version "2.5.0" apply false
```

  `app/build.gradle.kts`:
  şu bloğu:
```kotlin
// `palbase link` writes palbase/ at the repo root; the plugin's default is the module directory.
palbase {
    environmentsDir.set(rootProject.layout.projectDirectory.dir("palbase/environments"))
}

dependencies {
    implementation("io.palbase:palbe:2.3.0")
```
  şununla değiştir:
```kotlin
dependencies {
    implementation("io.palbase:palbe:2.5.0")
```

  `gradle.properties`:
  şu bloğu:
```properties
# Which Palbase environment the codegen plugin compiles against (palbase/environments/<env>/).
palbase.env=main
```
  şununla değiştir:
```properties
# Which Palbase environment each build type compiles (palbase/environments/<env>/).
palbase.env.debug=main
palbase.env.release=main
```

  ve: `git rm palbase/.gitattributes palbase/environments/main/roles.json`
- [ ] **Adım 4: Yeşil** — Run: `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/verify-trial.sh"` · Beklenen: (prova, `-I "$TOOLS/proof-init.gradle.kts" --offline`): 11 satırın hepsi `ok`, basılan satırlar `Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]` ve `Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]`, exit 0. Yayının kendisiyle (`-I "$TOOLS/released-init.gradle.kts" --offline`, `includeBuild` yok): aynı 11 `ok`, exit 0; `./gradlew --offline -I "$TOOLS/released-init.gradle.kts" buildEnvironment` → `io.palbase.codegen:io.palbase.codegen.gradle.plugin:2.5.0` → `io.palbase:codegen-gradle:2.5.0` → `io.palbase:palbase-codegen-engine:2.5.0`.

  Upstream 2.4.0'ın üretilen koda getirdiği tek değişiklik (`from()` → `backend.envelope`) yalnız `x-palbase-errors` beyan eden bir uç için üretilir; trial'ın sözleşmesi (kullanıcının app'ininki de — aynı dosya) hiç beyan etmiyor, yani yukarıdaki kabul o yolu derlemez. Yol ayrıca ölçüldü (prova, geçişten sonraki trial'ın bir KOPYASINDA; gerçek depoya girmez): `palbase/environments/main/openapi.json`'un `paths`'ine upstream'in `codegen-engine/src/test/resources/Golden/classified_errors_spec.json`'undaki `/classified/act` yolu eklendi → üretilen `ClassifiedActError.from` `val response = backend.envelope ?: return Other(backend)` taşır; `./gradlew --offline -I "$TOOLS/released-init.gradle.kts" :app:assembleDebug :app:assembleRelease` → `BUILD SUCCESSFUL` (palbe 2.5.0). Aynı kopya `io.palbase:palbe:2.3.0`'a pinlenip `-I "$TOOLS/proof-init.gradle.kts"` ile `:app:compileDebugKotlin` → `e: …/PalbaseGenerated.kt:386:36 Unresolved reference 'envelope'.`, `BUILD FAILED` — plugin ile kütüphanenin AYNI sürüm kuralının nedeni.
- [ ] **Adım 5: Commit** — `git add build.gradle.kts app/build.gradle.kts gradle.properties && git commit -m "trial: Palbase 2.5.0 — each build type picks its environment; the palbase {} block, the global palbase.env and the retired roles.json/.gitattributes go"` (Adım 3'ün `git rm`'i aynı commit'e girer), sonra `git push`.

---

### T033: Kullanıcının test app'i hedef son duruma geçer — `featureX` APK'sı `featureX`'i taşır
<!-- deps: [T031] | files: [app/build.gradle.kts, gradle.properties, gradle/libs.versions.toml, app/src/main/android-config.json, app/src/main/openapi.json, app/src/debug/android-config.json, app/src/debug/openapi.json, app/src/featureX/android-config.json, app/src/featureX/openapi.json, palbase/environments/featureX, palbase/environments/featureY, palbase/environments/feature-profile-update, local.properties] | satisfies: [FR-302] -->

FR-302, `~/AndroidStudioProjects/MyApplicationPalbaseAndroidSdkTest` (bir git deposu değil: `git rev-parse` → `fatal: not a git repository`; AGP 9.1.1, Gradle 9.3.1, Compose). Bugün derlenmiyor bile: `.kts`'te çıplak `featureX {` / `featureY {` (`app/build.gradle.kts:43`, `:50`) — `Unresolved reference 'featureX'` (E3(c)). Hedef şartnamenin "Hedef son durum"u: kök `gradle.properties` `palbase.env.debug=main` + `palbase.env.release=main` (global satır silinir), build type'lar `create(…) { initWith(getByName("debug")); matchingFallbacks += listOf("debug") }` (D-019: `debug { }` yukarıda kalır), `featureProfileUpdate` DSL'le `feature-profile-update`'e (bu yüzden dosyanın başında `import io.palbase.gradle.palbase`), proje düzeyi `palbase { }` bloğu YOK, sürümler 2.5.0. `app/src/{main,debug,featureX}/{android-config.json,openapi.json}` D-001'de reddedilen denemenin artıkları: hiçbir şey okumaz ve APK'ya girmez (ölçüldü, aşağıda) — silinir.

Tablodaki `featureX`/`featureY`/`feature-profile-update` ortamları bulutta YOK (checkout yalnız `main` taşıyor); onları `palbase env create` açar — her biri FATURALI bir tenant; komut bunu sormadan önce söyler — ve `palbase link` yazar: KULLANICI onaylar ve koşturur. O zamana kadar `featureX` build'i yüksek sesle düşer (ölçüldü), `debug`/`release` derlenir. Bu planda ortamlar kopyada elle yazıldı (`main`'in sözleşmesi, sahte `base_url`/anahtar) — gerçek depoya girmez.

Şartnamenin ağacı ile tablosu `debug` için aynı anda doğru olamaz: `local.properties`'teki `palbase.env.debug=featureX` debuggable `debug`'ı `featureX`'e çevirir (adım 3, dosyadan önce), tablo ise `debug → main` diyor. Görev ikisini ayrı ölçer: commit edilen durum tabloyu verir; kişisel satır (git'e girmez, `.gitignore` onu yok sayıyor) eklenince `debug → featureX`. Proje git'te OLMADIĞI için hiçbir değişiklik geri alınamaz: görev bir **Adım 0** ile başlar — kullanıcı onay verir, dokunulacak her dosya yolu korunarak `~/palbase-2.3-backup/`'a kopyalanır, artıklar silinmez yedeğe taşınır ve bir satır her şeyi geri koyar (inceleme, major 3). Yedek ve geri dönüş provada ölçüldü (Adım 0). Release APK'sı çevrimdışı provada `-x lintVitalRelease` ile derlendi: lint-check jar'ı `androidx.compose.material3:material3-desktop:1.3.0` Gradle önbelleğinde yok (`No cached version … available for offline mode`) — Palbase'le ilgisiz. Gerçek (çevrimiçi) koşu bayraksız yapılır; `lintVitalRelease` bu planda ölçülmedi.

**Interfaces:**
- Consumes: T031'in yayını (gerçek proje) — ya da T030'un Test depoları ve T032'nin init betikleri (prova)
- Produces:
  - Test app: variant → ortam tablosu (`debug`/`release` → `main`, `featureX` → `featureX`, `featureY` → `featureY`, `featureProfileUpdate` → `feature-profile-update`)
  - Plan aracı `tools/verify-myapp.sh [gradle argümanları]` (`PERSONAL=1` ile kişisel satır)

- [ ] **Adım 0: Yedek — KULLANICI onayı** — Proje git'te değil: Adım 3 bir dosyanın tamamını değiştirir, üç dosyayı düzenler ve altı dosyayı kaldırır; hiçbiri geri alınamaz. Ajan önce kullanıcıya Adım 3'ün listesini gösterir ve AÇIK bir "evet" almadan Adım 3'ün tek satırını koşturmaz. Onaydan sonra, test app kökünde, dokunulacak her şey yolu korunarak kopyalanır:
```bash
B="$HOME/palbase-2.3-backup/MyApplicationPalbaseAndroidSdkTest"; mkdir -p "$B"
rsync -aR app/build.gradle.kts gradle.properties gradle/libs.versions.toml local.properties app/src palbase "$B/" && find "$B" -type f | wc -l
# geri dönüş, gerekirse — Adım 3'ün taşıdığı artıklar dahil her şeyi yerine koyar:
# rsync -a --exclude=/moved/ "$B/" ./
```
  Beklenen (projenin bir kopyasında ölçüldü, `HOME` kopyanın yanına yöneltilerek): `40` dosya, exit 0; Adım 3'ün düzenlemeleri ve taşıması uygulanıp geri dönüş satırı koşunca `diff -r <özgün kopya> .` hiçbir fark basmadı. Geri dönüş yalnız yedeklenenleri geri koyar, eklenenleri silmez: `palbase link`'in yazdığı ortam dizinleri kalır (kopyada, sahte ortamlar yazıldıktan sonra geri dönülünce `diff -rq` → `Only in ./palbase/environments: feature-profile-update`, `… featureX`, `… featureY`); tam geri dönüş için onları da elle sil. Yedek, kullanıcı build'lerden memnun kalana kadar silinmez.
- [ ] **Adım 1: Kırmızı testi yaz** — plan aracı `tools/verify-myapp.sh`:
```bash
#!/usr/bin/env bash
# FR-302 acceptance for MyApplicationPalbaseAndroidSdkTest — the spec's
# "Hedef son durum" — run at its root. Arguments go to Gradle as they are.
# With PERSONAL=1 it checks the personal local.properties line instead.
set -u
fail=0
check() { if eval "$2"; then echo "ok    $1"; else echo "FAIL  $1"; fail=1; fi; }
apk() { unzip -p "app/build/outputs/apk/$1/app-$1$2.apk" assets/palbase/palbase-config.json 2>/dev/null; }
if [[ "${PERSONAL:-}" == 1 ]]; then
  check "local.properties is ignored by git" 'grep -qxE "/?local.properties" .gitignore'
  check "local.properties aims debug at featureX" 'grep -qx "palbase.env.debug=featureX" local.properties'
  rm -rf app/build/outputs/apk   # an APK an earlier build left must not answer for this one
  out="$(./gradlew --console=plain "$@" :app:generatePalbaseDebug --rerun :app:assembleDebug 2>&1)"; code=$?
  check "debug builds" '[ "$code" = 0 ]'
  check "debug → featureX, from local.properties" 'grep -qF "Palbase: debug → featureX (palbase.env.debug in local.properties) [palbase/environments]" <<<"$out"'
  check "app-debug.apk packs featureX" 'apk debug "" | grep -qF "\"palbase_environment\":\"featureX\""'
  grep -E "^Palbase: |FAILED$|^e: |What went wrong" <<<"$out" | head -8 | sed 's/^/  | /'
  exit "$fail"
fi
check "the plugin is pinned at 2.5.0"       'grep -qF "id(\"io.palbase.codegen\") version \"2.5.0\"" app/build.gradle.kts'
check "palbe is pinned at 2.5.0"            'grep -qx "palbe = \"2.5.0\"" gradle/libs.versions.toml'
check "no project-level palbase { } block"  '! grep -q "^palbase {" app/build.gradle.kts'
check "the build type DSL is imported"      'grep -qx "import io.palbase.gradle.palbase" app/build.gradle.kts'
check "no global palbase.env"               '! grep -q "^palbase.env=" gradle.properties'
check "debug and release keys committed"    'grep -qx "palbase.env.debug=main" gradle.properties && grep -qx "palbase.env.release=main" gradle.properties'
check "no config copies under app/src"      '[ -z "$(find app/src -name android-config.json -o -name openapi.json)" ]'
for env in main featureX featureY feature-profile-update; do
  check "palbase/environments/$env is linked" 'test -f "palbase/environments/$env/android-config.json" -a -f "palbase/environments/$env/openapi.json"'
done
rm -rf app/build/outputs/apk   # an APK an earlier build left must not answer for this one
out="$(./gradlew --console=plain "$@" \
  :app:generatePalbaseDebug --rerun :app:generatePalbaseRelease --rerun :app:generatePalbaseFeatureX --rerun \
  :app:generatePalbaseFeatureY --rerun :app:generatePalbaseFeatureProfileUpdate --rerun \
  :app:assembleDebug :app:assembleRelease :app:assembleFeatureX :app:assembleFeatureY :app:assembleFeatureProfileUpdate 2>&1)"; code=$?
check "every variant builds" '[ "$code" = 0 ]'
while IFS='|' read -r variant apkname env line; do
  check "$variant → $env" 'grep -qF "$line" <<<"$out"'
  check "the $variant APK packs $env" 'apk "$variant" "$apkname" | grep -qF "\"palbase_environment\":\"$env\""'
done <<'TABLE'
debug||main|Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]
release|-unsigned|main|Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]
featureX||featureX|Palbase: featureX → featureX (from the build type name) [palbase/environments]
featureY||featureY|Palbase: featureY → featureY (from the build type name) [palbase/environments]
featureProfileUpdate||feature-profile-update|Palbase: featureProfileUpdate → feature-profile-update (palbase { environment } in the `featureProfileUpdate` build type) [palbase/environments]
TABLE
grep -E "^Palbase: |FAILED$|^e: |What went wrong" <<<"$out" | head -12 | sed 's/^/  | /'
exit "$fail"
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run (test app kökünde): `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/verify-myapp.sh"` (prova: `-I "$TOOLS/proof-init.gradle.kts" --offline`) · Beklenen: **FAIL** (exit 1); yalnız `ok    palbase/environments/main is linked`, geri kalan 21 satır `FAIL` (APK satırları dahil — betik önceki build'in APK'larını sildi); derleme `e: file:///…/app/build.gradle.kts:43:9: Unresolved reference 'featureX'.` ve `…:50:9: Unresolved reference 'featureY'.` ile düşer.
- [ ] **Adım 3: Uygula** —

  `app/build.gradle.kts` — dosyanın TAMAMI şu olur:
```kotlin
import io.palbase.gradle.palbase

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
    // Must match the `kotlin` version in gradle/libs.versions.toml.
    id("org.jetbrains.kotlin.plugin.serialization") version "2.2.10"
    // Palbase codegen: builds the typed `pb` client from palbase/environments/<env>/openapi.json.
    id("io.palbase.codegen") version "2.5.0"
}

android {
    namespace = "com.example.myapplicationpalbaseandroidsdktest"
    compileSdk {
        version = release(36) {
            minorApiLevel = 1
        }
    }

    defaultConfig {
        applicationId = "com.example.myapplicationpalbaseandroidsdktest"
        minSdk = 26
        targetSdk = 36
        versionCode = 1
        versionName = "1.0"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
        debug {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
        // A feature build type compiles the Palbase environment of its own name,
        // palbase/environments/featureX/. initWith(debug) makes it debuggable and
        // debug-signed, so Run installs it — and copies debug as it is HERE, so
        // debug { } stays above.
        create("featureX") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
        }
        create("featureY") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
        }
        // An environment whose name a build type cannot carry is named here.
        create("featureProfileUpdate") {
            initWith(getByName("debug"))
            matchingFallbacks += listOf("debug")
            palbase { environment = "feature-profile-update" }
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_11
        targetCompatibility = JavaVersion.VERSION_11
    }
    buildFeatures {
        compose = true
    }
}

dependencies {
    // Palbase Android SDK — same version as the codegen plugin above.
    implementation(libs.palbe)
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    testImplementation(libs.junit)
    androidTestImplementation(libs.androidx.junit)
    androidTestImplementation(libs.androidx.espresso.core)
    androidTestImplementation(platform(libs.androidx.compose.bom))
    androidTestImplementation(libs.androidx.compose.ui.test.junit4)
    debugImplementation(libs.androidx.compose.ui.tooling)
    debugImplementation(libs.androidx.compose.ui.test.manifest)
}
```

  `gradle.properties`:
  şu bloğu:
```properties
# Which Palbase environment the build compiles (palbase/environments/<env>/).
palbase.env=main
```
  şununla değiştir:
```properties
# Which Palbase environment each build type compiles (palbase/environments/<env>/).
# A build type with no line here compiles the environment of its own name.
palbase.env.debug=main
palbase.env.release=main
```

  `gradle/libs.versions.toml`:
  şu bloğu:
```toml
palbe = "2.3.0"
```
  şununla değiştir:
```toml
palbe = "2.5.0"
```

  Ara ölçüm (yalnız `main` bağlıyken, artıklar henüz yerindeyken): `./gradlew :app:assembleDebug` → `Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]`; `unzip -l app/build/outputs/apk/debug/app-debug.apk | grep -cE '(^|/)(android-config|openapi)\.json$'` → `0` (artıklar APK'da yok). `./gradlew :app:assembleFeatureX` → `> Task :app:generatePalbaseFeatureX FAILED`, ``Palbase: environment `featureX` (from the build type name) has no directory — …/palbase/environments/featureX does not exist, and this checkout carries main. Run `palbase link` to write it, …``.

  Artıkları yedeğe TAŞI (silme yok; Adım 0'ın `$B`'si): `for f in app/src/main/android-config.json app/src/main/openapi.json app/src/debug/android-config.json app/src/debug/openapi.json app/src/featureX/android-config.json app/src/featureX/openapi.json; do mkdir -p "$B/moved/$(dirname "$f")" && mv "$f" "$B/moved/$f"; done; rmdir app/src/debug app/src/featureX 2>/dev/null` (`app/src/debug` ve `app/src/featureX` boş kalınca kalkar; `app/src/debug`'da başka dosya varsa kalır).

  Ortamlar (KULLANICI, FATURALI — backend checkout'unda, projesi `centauri`): `palbase env create featureX`, `palbase env create featureY`, `palbase env create feature-profile-update`; sonra `palbase push --env featureX`, `palbase push --env featureY`, `palbase push --env feature-profile-update`; sonra test app kökünde `palbase link`. Koşturulmadı; beklenen iz: `palbase/environments/{featureX,featureY,feature-profile-update}/` altında `android-config.json` + `openapi.json`.
- [ ] **Adım 4: Yeşil** — Run: `TOOLS=/Users/erkutbas/Github_Pallasite/palbase-cli/docs/paltimate/2026-09-26-android-ortam-build-type/tools; "$TOOLS/verify-myapp.sh"` · Beklenen: (prova, `-I "$TOOLS/proof-init.gradle.kts" --offline -x lintVitalRelease`): 22 satırın hepsi `ok`, exit 0; basılan satırlar:
  `Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]`,
  `Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]`,
  `Palbase: featureX → featureX (from the build type name) [palbase/environments]`,
  `Palbase: featureY → featureY (from the build type name) [palbase/environments]`,
  ``Palbase: featureProfileUpdate → feature-profile-update (palbase { environment } in the `featureProfileUpdate` build type) [palbase/environments]``.
  `featureX` APK'sı: `unzip -p app/build/outputs/apk/featureX/app-featureX.apk assets/palbase/palbase-config.json` → `{"app_id":"project","base_url":"https://featurexproof.palbase.studio","api_key":"pb_project_c0123456789abcdefghijKLMN","palbase_environment":"featureX"}` (provanın sahte ortamı); `aapt dump xmltree … AndroidManifest.xml | grep debuggable` → `A: android:debuggable(0x0101000f)=(type 0x12)0xffffffff`; `apksigner verify --print-certs` → `Signer #1 certificate DN: C=US, O=Android, CN=Android Debug`. `includeBuild` olmadan, yayının kendisiyle (`-I "$TOOLS/released-init.gradle.kts" --offline -x lintVitalRelease`): yine 22 `ok`, exit 0; `:app:buildEnvironment` → `io.palbase.codegen:io.palbase.codegen.gradle.plugin:2.5.0` → `io.palbase:codegen-gradle:2.5.0` → `io.palbase:palbase-codegen-engine:2.5.0`.
  Kişisel satır: `PERSONAL=1 "$TOOLS/verify-myapp.sh"` önce **FAIL** (`FAIL  local.properties aims debug at featureX`, `FAIL  debug → featureX, from local.properties`, `FAIL  app-debug.apk packs featureX`; basılan `Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]`); `local.properties`'e `palbase.env.debug=featureX` KENDİ satırında eklenince 5 `ok`, `Palbase: debug → featureX (palbase.env.debug in local.properties) [palbase/environments]`, exit 0. Satır kişiseldir: kullanıcı isterse bırakır. Dikkat: bu projenin `local.properties`'i satır sonuyla bitmiyor (son satır `sdk.dir=…`); `echo 'palbase.env.debug=featureX' >> local.properties` satırı `sdk.dir`'in sonuna yapıştırır ve kontrol 3 `FAIL` ile kalır (kopyada ölçüldü). Satırı ayrı ekleyin: `printf '\npalbase.env.debug=featureX\n' >> local.properties`.
- [ ] **Adım 5: Commit** — commit yok: proje bir git deposu değil. Yedek (`$B`) kullanıcı build'lerden memnun kalana kadar durur; geri dönüş Adım 0'ın son satırı. (Kopyada `git init` ile ölçülen iki commit: geçiş, ve ayrı tutulan `PROOF ONLY` sahte ortamlar.)

---

### T034: Sıfırdan bir projede birlikte deneme — KULLANICI ve lead birlikte; Android yarısı provada ölçüldü
<!-- deps: [T031, T033] | files: [] | satisfies: [FR-212, FR-301, FR-302] -->

Intake 5 ("Sonra birlikte sıfırdan bir projede denensin"). Bu bir yürütme görevi değil, birlikte yapılacak bir oturumun senaryosu: bulut adımları (giriş, proje ve ortam açma — FATURALI —, push, link, ortam silme) kullanıcınındır ve bu planda KOŞTURULMADI; beklenen izleri olarak yalnız dosya ve satır şekilleri yazıldı. Android adımlarının beklenenleri provada ÖLÇÜLDÜ: Android Studio sihirbazının ürettiği şekildeki (AGP 9.1.1, Gradle 9.3.1, Compose, Kotlin DSL) T033 kopyası, plugin ve palbe 2.5.0'ı T030'un Test depolarından `includeBuild` OLMADAN çözerek — gerçek bir tüketicinin yayından sonra yaptığı gibi. Önkoşul: `plan-cli.md` (link'in Gradle satırlarını basması FR-013, `.gitignore`'a `palbase/environments/local/` FR-020, silinen ortamın temizliği FR-011) ve `plan-cloud.md` (slug, FR-101/102) yayında; T031'in denetimi yeşil.

Bir not, oturumda söylensin: ortamlar aynı uygulamanın farklı derlemeleridir ama sözleşmeleri farklı olabilir. `featureX`'e push edilmiş yeni bir uç için yazılan app kodu (`pb.<yeni>`), o uç `main`'e gelene kadar `debug`/`release` variant'larını DERLETMEZ — üretilen istemci variant'ın ortamının sözleşmesinden gelir.

**Interfaces:**
- Consumes: T031'in yayını; T033'ün build type şekli
- Produces:
  - Sıfırdan bir Android projesi + bir Palbase projesi: `debug`/`release` → `main`, `featureX` → `featureX`; silinen ortam yüksek sesle düşer

- [ ] **Adım 1: Kırmızı testi yaz** — yeni test yok. `tools/verify-myapp.sh` bu projeye birebir uymaz (`featureY` ve `featureProfileUpdate` yok); kabul Adım 4'teki build satırları ve `unzip -p` komutlarıdır — T033'ün ölçtüğü şeylerin aynısı.
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Adım 3.4'ten sonra, `palbase link`'ten ÖNCE: `./gradlew :app:assembleDebug` · Beklenen: `> Task :app:generatePalbaseDebug` geçer ve tek satır basar: ``Palbase: debug → nothing generated: no palbase/environments found (searched app/palbase/environments, palbase/environments). Run `palbase link` at the checkout root to write one.`` (kökte `.git` var: üst dizin aranmaz, T018). Provada ölçüldü — T032'nin trial kopyasında `palbase/` kaldırılarak, satır birebir; orada derleme `compileDebugKotlin`'de düştü çünkü trial'ın `MainActivity`'si üretilen istemciyi çağırıyor (`Unresolved reference 'generated'`). Sihirbaz projesi üretilen istemciyi henüz çağırmaz; `Palbase.initialize` runtime'dandır.
- [ ] **Adım 3: Uygula (KULLANICI + lead)** —
  1. `palbase login`.
  2. Backend (FATURALI): boş bir dizinde `palbase init`, sonra `palbase project create palbase-zero` (ilk ortamı açar; slug `main` — FR-102) ve komutun son satırda bastığı `palbase link …`; `palbase push`. Sonra `palbase env create featureX` (faturayı söyleyip sorar) ve `palbase push --env featureX`.
  3. Android Studio → New Project → Empty Activity; Minimum SDK API 26; Build configuration language Kotlin DSL. Proje kökünde `git init`.
  4. Public README'nin (T028) kurulumu: `settings.gradle.kts`'e iki `maven { url = uri("https://palgroup.github.io/palbackend-android/") … }` bloğu; `app/build.gradle.kts`'e `id("org.jetbrains.kotlin.plugin.serialization") version "2.2.10"` (`gradle/libs.versions.toml`'daki `kotlin` ile aynı olmalı; kullanıcının sihirbaz projesinde 2.2.10), `id("io.palbase.codegen") version "2.5.0"`, `implementation("io.palbase:palbe:2.5.0")`; `Palbase.initialize(this)` çağıran bir `Application` ve manifest'te `android:name`.
  5. Proje kökünde `palbase link palbase-zero` → `palbase/project.json`, `palbase/environments/main/` ve `palbase/environments/featureX/` (her birinde `android-config.json` + `openapi.json`); link Gradle satırlarını basar (FR-013) ve `.gitignore`'a `palbase/environments/local/` ekler (FR-020). Koşturulmadı.
  6. Kök `gradle.properties`'e `palbase.env.debug=main` ve `palbase.env.release=main`; `app/build.gradle.kts`'te `buildTypes { create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") } }`.
  7. Sync → Build Variants → `app` → `featureX` → Run.
- [ ] **Adım 4: Yeşil (birlikte bakılacak)** —
  - Build çıktısında `Palbase: featureX → featureX (from the build type name) [palbase/environments]`, `Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]`, `Palbase: release → main (palbase.env.release in gradle.properties) [palbase/environments]` — satırlar provada birebir bu biçimde ölçüldü (T033 Adım 4, yayından çözülen 2.5.0 ile 22 `ok`).
  - `unzip -p app/build/outputs/apk/featureX/app-featureX.apk assets/palbase/palbase-config.json` → `"palbase_environment":"featureX"` ve `featureX` ortamının `base_url`'i; APK debuggable ve `CN=Android Debug` imzalı (T033'te ölçüldü). Cihazda uygulamanın `featureX` yığınına gittiği birlikte görülecek (koşturulmadı).
  - Kişisel: `local.properties`'e `palbase.env.debug=featureX` → `debug` variant'ı `featureX`'i derler (T033'te ölçüldü: `Palbase: debug → featureX (palbase.env.debug in local.properties) [palbase/environments]`); sonra satırı sil — aşağıdaki silinen ortam adımı `debug`'ın dosyadaki `main`'i derlediğini varsayar.
  - Silinen ortam: panelde `featureX`'i sil, `palbase link` → `palbase/environments/featureX/` temizlenir (FR-011; koşturulmadı). Sonra `./gradlew :app:assembleFeatureX` · Beklenen (provada, dizin kaldırılarak ölçüldü): `> Task :app:generatePalbaseFeatureX FAILED` ve ``Palbase: environment `featureX` (from the build type name) has no directory — …/palbase/environments/featureX does not exist, and this checkout carries …. Run `palbase link` to write it, …``; `./gradlew :app:assembleDebug` aynı checkout'ta, kişisel satır silinmişken, `BUILD SUCCESSFUL` (`Palbase: debug → main (palbase.env.debug in gradle.properties) [palbase/environments]`). Satır `local.properties`'te kalmışsa debug da düşer: `> Task :app:generatePalbaseDebug FAILED`, ``Palbase: environment `featureX` (palbase.env.debug in local.properties) has no directory — …`` (T033 kopyasında ölçüldü).
  - Temizlik: deneme projesini panelde sil (faturalı).
- [ ] **Adım 5: Commit** — commit yok (deneme projesi); bulgular varsa bu planın `decisions.md`'sine lead yazar.

---

---

## Kanıt (planlama koşusu)

**Yerler.** Scratch klon `…/scratchpad/plan/plugin`, `main` = revize zincir; taslağın başı `draft-head` etiketinde (`195d0fb`). Revizyonun kendi dizini `…/scratchpad/plan/revise-plugin/`: `gen.py` + `lib.py` + `build_release.py` (üretici), `chain.py` (görev → SHA), `obs/` (gözlenen her değer), `logs/` ve `logs2/` (her koşunun logu + JUnit XML'i), `replay/` (bağımsız replay klonu), `tools/`, `t033-backup-proof/`. Gerçek depolara yazılmadı. `/tmp/er.bak`'a bir kez yanlışlıkla kopya yazıldı; hemen silindi.

**Revize zincir** (`cc41eef` = T007 üstüne; yeni numara → SHA ← taslak):
- T008 `9c8da45` ← aynı commit.
- T009 `028ead1` ← `daa9156` + ORG_GRADLE_PROJECT testi.
- T010 `85b982b` ← `f040f5c` + kapsamlı blok↔dosya kuralı, `linkedSetOf`, iki test.
- T011 `fadcb76` ← `fe89ff6` + kapsamlı kural, üç test.
- T012 `9bc6bdc`.
- T013 `f770235` + yorumlar.
- T014 `868099d`.
- T015 `7492849` + "root".
- T016 `6b67068` ← `60a3120` + `35c3825`, birleşik.
- T017 `640568f` ← `0fa313c`.
- T018 `7ce0fc8`.
- T019 `d445173`.
- T020 `2316ee9`.
- T021 `5c7b66f` + "root".
- T022 `66d4a89`.
- T023 `ca03665` ← `ddc4882` + "root", `--no-watch-fs`, CC kasıt adımı, ara modül testi.
- T024 `64f2fe9` + missingDimensionStrategy testi.
- T025 `ff2113b`.
- T026 `26a2f8e`.
- T027 `fddb14a` + README kuralı.
- T028 `b095bba` + kural ve yükseltme maddeleri.
- T029 `f3431f0` + CHANGELOG kuralı.
- T030 `ee1ee36` (scratch `v2.4.0` etiketi buraya taşındı).

**Replay (plan metni → ağaç).** Eleştirmenin `replay.py`'si yeni metne ve zincire yöneltildi. Plan markdown'ından, satır hizalı, tek eşleşme şartıyla okundu:
- zincir T008→T030: 23/23 `tree vs scratch: IDENTICAL` (`logs/replay-chain-final.log`);
- her görev kendi ebeveyninde tek başına: 23/23 IDENTICAL (`logs/replay-isolate-final.log`).

Boş satır farkı kalmadı. Deps: blame kontrolü 23/23 görevde 0 eksik (`logs/deps-final.log`).

**Kırmızı/yeşil (her revize görev, `batch.py`).** Her görev için dört koşu:
- ebeveynde bütün test dosyalarıyla (derleme kırmızısı);
- ebeveynde yalnız TestKit dosyasıyla (davranış kırmızısı);
- görevde filtreli (yeşil);
- görevde tam `:test`.

Ayrıntı `logs/T0NN-{red,red-tk,green,full}.{log,sum}` ve XML'lerde. Belirleyici satırlar görev metinlerinde, birebir.

**Mutasyon kanıtları:**
- `linkedSetOf` → `mutableListOf` yapılınca `a flavored benchmark variant warns once about an ignored flavor line` düşer: `expected: <[…]> but was: <[…, …]>` (`logs/T010-mutant-list.log`).
- Taslağın `EnvironmentResolver.kt`'si (fe89ff6) T011'in yeni TestKit testiyle koşturuldu; test ``> Palbase: the `staging` product flavor names two environments in two committed places — … `palbase.env.release=main` in gradle.properties`` ile düşer (`logs/T011-draftrule.log`).

**Kırılganlık:**
- Dosya izleme AÇIKken (T023'ün bayraksız hâli) CC kasıt testi 12 kez tek başına koşturuldu: 12/12 yeşil (`logs/flake-watchfs.loop`), kırılganlık yeniden üretilemedi.
- `--no-watch-fs` ile: batch'te T023–T028 tam `:test` koşuları 6/6, T030 plugin kapısı 1/1, son HEAD'de ardışık `:test --rerun` 5/5 yeşil (`logs/flake-final.loop`; 1m 4s–1m 8s).

**T033 yedek provası.** Kullanıcının projesinin bir kopyasında (build/.gradle/.idea hariç):
- `rsync -aR …` → `40` dosya;
- Adım 3'ün düzenlemeleri ve altı artığın `$B/moved`'a taşınması;
- geri dönüş satırı → `diff -r` özgün kopyayla fark yok (`logs/T033-backup-proof.log`).

**Tüketici provaları (T032/T033), revize araçlarla yeniden ölçüldü** (`logs2/`). `verify-*.sh` artık bayat APK'yı siler. İki yol kullanıldı:
- plugin, scratch zincirinin başından `includeBuild` ile (`s4-init.gradle.kts`);
- yayının kendisi: T030'un Test depolarından, `includeBuild` yok (`s4-released-init.gradle.kts`).

Sonuçlar:
- T032: kırmızı exit 1 (8 FAIL); yeşil 11/11 `ok`, iki yoldan da. `buildEnvironment` marker → `codegen-gradle:2.4.0` → `palbase-codegen-engine:2.4.0`.
- T033: kırmızı 1 `ok` / 21 FAIL. Yeşil 22/22 `ok`, iki yoldan da. `featureX` APK'sı `"palbase_environment":"featureX"`, debuggable, `CN=Android Debug`. Kişisel satır 3 FAIL → 5 `ok`.

Test app'in sahte ortamları ayrı bir `PROOF ONLY` commit'inde.

**Kapılar (T030):**
- `release-notes.sh`: kırmızı `1 lines, 0 …`, yeşil `144 lines, 1 DAVRANIŞ DEĞİŞİKLİĞİ heading(s)`.
- README kapısı: `872 actionable tasks … Configuration cache entry stored` → ikinci koşu `866 … reused`.
- Plugin kapısı: `check` 1m 7s, 52/11/88 + engine 46, `:validatePlugins` koştu.
- `Publication verification passed for 2.4.0`.
- Test depolarına yayın: 7s / 3s.
- 11 artefakt, POM zinciri, class major 61.
- Birleşik ağaçta yayın sonrası denetim: 2.5.0 için 11 FAIL / exit 1, 2.4.0 için 11 ok / exit 0.
- Runtime bekçisi `GeneratedConfigLoaderTest` `tests="14" failures="0"` (`logs/T022-runtime.log`).
- README kayma provası: tekrar koşu `UP-TO-DATE`, tek satır değişince `> Task :test FAILED` (`logs/T027-drift.log`).
- `changelog-check.sh`: kırmızı `22 missing`, yeşil tamam.

**Tam paket, tabana karşı:**
- Taban (`e72f704`): `PalbaseCodegenPluginTest` 27 + codegen-engine 46, kırık yok.
- Son (`ee1ee36`): `EnvironmentResolverTest` 52 + `LibraryFallbackCheckTest` 11 + `PalbaseCodegenPluginTest` 88 + codegen-engine 46; hepsi `failures="0" errors="0"`.
- Silinen ya da zayıflatılan test yok; bilinçli değişenler görevlerinde adlandırılmış.
- Görev başına tam paket sayıları monoton artıyor (PCPT T008 57 → T028 88).

**Üreticide yakalanan bir hata:** ilk yazımda `task()` taslağın gözlem dizelerini `setdefault` ile koruyordu, yani T009–T026 taslağın sayılarını taşıyacaktı. Düzeltildi. Bütün Beklenen'ler bu koşunun `obs/` dosyalarından geliyor; T008 aynı commit olduğu için taslağın ölçümünü taşır.

**Tam liste:** `revise-plugin/plan-plugin.COMPLETE-tasks.md`, sha256 `b7a8865b94edbb8ac092c35ac502f4dde5526965ff680b547de308a9a7e2ed95`, 34 görev.

#### Yeni tabana taşıma ve 2.5.0 (2026-09-27)

**Upstream commits since c9d9866 and their effect on the plan**
- **233991d (classified errors).** Changes codegen-engine `KotlinEmitter`: the generated `from()` now reads `backend.envelope` instead of `backend !is BackendError.Http`. It also updates the goldens, adds `ClassifiedErrorsEmitTest`, and changes palbe-core `BackendTypes`/`ErrorEnvelope`, `palbe-core.api` and tests.
  - **No overlap with plan code.** Upstream did not touch codegen-gradle, so T001–T028 cherry-pick clean. Each task's codegen-gradle/README/distribution tree equals the old chain except for the one `2.5:` comment line (checked per task for T001–T015 and at the head).
  - **Counts.** Engine tests go from 46 to 47 and from 8 to 9 classes; Global Constraints and T007/T030 are updated.
  - **Consumer impact.** A contract that declares `x-palbase-errors` now generates code that needs palbe >= 2.4.0. The plugin/library same-version rule covers this.
  - **T007 proof.** The consumer proof still uses palbe 2.3.0 from the public tree. It compiles because the trial contract declares no `x-palbase-errors`; I measured this on AGP 8.11.1 and 9.1.1. The text now says so rather than claiming the runtime is unchanged.
- **6e97598 (release 2.4.0).** Puts `## 2.4.0 — 2026-09-26` directly under `## Yayınlanmamış` and sets `Son etiket: v2.4.0`; tag v2.4.0 points at 6e97598.
  - The old T029 and T030 commits CONFLICT in CHANGELOG.md. The revised T029/T030 texts are the resolution.
  - Our sections go above the upstream section, which is left untouched. `Son etiket` goes from v2.4.0 to v2.5.0 at T030.
  - "Runtime ve engine kodu 2.3.0 ile aynı" was now false and became "2.4.0 ile aynı". I checked it with `git diff --stat v2.4.0..HEAD` on runtime/engine: only GeneratedConfigLoaderTest changes.
- **3588bc2 (CI).** No overlap. CI now runs `:codegen-engine:test :codegen-gradle:test :palbe-core:testDebugUnitTest`; I ran that exact command at the chain head (green).
- **e72f704 onto 3588bc2.** Applies clean. The lead still has to rebase the real local main the same way before T001.

**Additions that need a lead decision** (all flagged and easy to drop)
- The public tree was checked with `git ls-remote` of palgroup/palbackend-android: main is d840817 and the only tag is v2.3.0. Upstream 2.4.0 was never published there, so public consumers jump from 2.3.0 to 2.5.0, and the v2.5.0 GitHub Release notes are only the `## 2.5.0` section.
  - I added one YAYIN bullet in the 2.5.0 CHANGELOG section (T029).
  - I added one bullet to the public README's `### Upgrading from 2.3` (T028).
  - Both carry 2.4.0's typed-error change and the palbe-core binary-signature recompile note. The CHANGELOG bullet writes "davranış değişikliği" in lower case so release-notes.sh still counts exactly 1 heading.
  - If 2.4.0 gets published to the public tree before T031, drop both bullets.
- README.md: "Upgrading from 2.3 changes what some builds compile" became "Upgrading from 2.3 or 2.4 …". 2.4.0 did not change environment selection, and 2.4.0 may exist on GitHub Packages.
- I left the "2.3" wording that describes old behaviour unchanged, including the plugin-printed `palbase.env, the 2.3 global property`. It stays true, and changing it would ripple through code, tests and changelog-check.

**Plan text outside the returned fields**
- **Header lines to bump.** I applied these in the scratch full copy (see below):
  - Title `# Plugin \`io.palbase.codegen\` 2.4.0 — …` becomes 2.5.0.
  - Goal "…runtime 2.4.0 olarak birlikte çıkar;" becomes "…runtime 2.5.0 olarak birlikte çıkar (2.4.0 upstream'in, D-030);".
  - Architecture "Üretilen kod 2.3 ile byte byte aynı" becomes "Üretilen kod 2.4.0 ile byte byte aynı". The 2.3 claim is now false because 2.4.0 changed the emitter.
- **Kanıt section.** It still describes the old e72f704/ee1ee36 chain. Replace or append the evidence from this run.
- **Full revised copy of plan-plugin.md.** It has the header, Global Constraints and all revised tasks applied: …/plugin-rebase/plan-plugin.revised-2.5.md (8742 lines). The only remaining `2.4.0` mentions are intentional upstream references plus the old Kanıt section.

**Sibling documents (not changed; they need the same bump)**
- **plan-cli.md Dalga 2.** It still says 2.4.0 in `palbaseAndroidVersion = "2.4.0"`, the printed `io.palbase.codegen`/`io.palbase:palbe` coordinates, the heading "Dalga 2 — plugin 2.4.0 yayımlandıktan sonra", and the T023/T019 prose (26 lines mention 2.4).
- **decisions.md.** D-005, D-026 and D-030 already say 2.5.0, but D-026 still reads "reddi plugin 2.4'ün arama sırasına dayanır".

**Other observations**
- **TestKit flake, once, in T014's full run.** T011's test failed with `Cannot access implicit script receiver class 'org.gradle.api.Project'`. It was green alone and in a full rerun on the same tree. This is a different symptom from the one `--no-watch-fs` targets. It is recorded in Global Constraints.
- **verify-publications.sh** calls ./gradlew without `--offline`; its commands are internal to the script. I ran it as the plan does: `Publication verification passed for 2.5.0`, nothing went outside `build/`.
- **Trailing newline in the user's test app.** ~/AndroidStudioProjects/MyApplicationPalbaseAndroidSdkTest/local.properties has no trailing newline. `echo >>` glues the personal key onto `sdk.dir`; I found this on a scratch copy and T033 now warns about it.

**Scratch artifacts**
- Bundles (the prerequisite is 3588bc2, which origin already has):
  - `…/plugin-rebase/plugin-plan-2.5.bundle`: branches base-new c164ce4 and plan-new 54dec1c.
  - `…/plugin-rebase/trial-t032.bundle`: acedb85..ae6f549.
- Per-task texts: …/revise/new/*.md
- Tools: …/tools-new/
- Logs and JUnit XML: …/logs/
- Assembled result: …/out/result.json

No real repo was written, and nothing was pushed or published.

#### Verdict: the rebase and the 2.5.0 retarget hold; 6 minor findings

I replayed the plan text independently. The resulting final tree is byte-identical to the rebaser's `plan-new` 54dec1c. Every red and green I re-ran matched the revised texts. I found no blocker and no major issue. The five tool revisions are correct as returned, so `fixed_tools` is empty. Of the 6 minor findings:
- **Fixed in the returned texts:** T003, T032, T033, T034 and Global Constraints.
- **Fixed only in the assembled plan copy:** the doubled `---` after T034.
- **Not applied:** the Kanıt section is still stale, and the T029 heading wording is editorial.

Nothing was written to any real repo, and nothing was pushed or published. The only network use was Gradle's own resolution inside `scripts/verify-publications.sh`, which calls `./gradlew` without `--offline`.

#### How I verified it

Everything ran under `…/plugin-rebase/verify/`.

**Base.**
- I made a fresh `git clone --no-local` of palbackend-android-src and fetched the local repo's `refs/remotes/origin/main` (3588bc2) and its tags.
- `git cherry-pick e72f704` was clean: 4 files changed, 343 insertions(+), 68 deletions(-). My base 5619e9b is tree-identical to the rebaser's c164ce4.
- These byte-identity claims hold, checked with `git diff --stat e72f704 base-new` (empty): codegen-gradle/, README.md, distribution/README.md, scripts/publish.sh, gradle.properties, sample/, consumer-release/ and GeneratedConfigLoaderTest.
- `git diff --stat v2.4.0 base-new` touches only ci.yml, CHANGELOG.md, README.md, distribution/README.md and scripts/publish.sh.

**Replay.**
- **Replayed from the revised text:** T002, T003, T007, T022, T027, T028, T029 and T030, plus T032 in a fresh trial clone. My own block applier parsed the fenced blocks and applied the text's `şu bloğu/şununla değiştir`, `şu satırlardan sonra/şunu ekle`, `dosyasının tamamı` and prose-anchor instructions. It did not use the rebaser's commits.
- **Cherry-picked from the original proven chain** (`plugin-plan.bundle` ee1ee36), all clean: T001, T004–T006, T008–T021 and T023–T026.
- **Diff of each text task against its original commit:**
  - Code tasks show only the `2.5:` test comment, the `2.5's first draft` comment and the GeneratedConfigLoaderTest KDoc.
  - T027 changes one README line (`Upgrading from 2.3 or 2.4`).
  - T028 changes the pins to 2.5.0 and adds the 2.4.0 upgrade bullet.
- After T029 my tree equals the rebaser's 027a01e. After T030 `git diff --quiet HEAD 54dec1c` is empty.
- The old T029 and T030 commits really conflict on the new base (`CONFLICT (content): Merge conflict in CHANGELOG.md`, re-checked).

#### Reds and greens I observed

| Task | Red | Green |
|---|---|---|
| T002 | `39 tests completed, 11 failed`; `release compiled {…local1234m…}`; ``Palbase: environment `local` has no directory``; benchmark `UnexpectedBuildFailure` | ERT 26, PCPT 39 |
| T003 | `:consumer-release:generatePalbaseRelease FAILED`, `has no environment…` | see finding 1 (FROM-CACHE); with `--rerun` the line prints |
| T007 | `3 tests completed, 2 failed` (`NoSuchFileException …/app/build/generated/assets/generatePalbaseDebug/…`), explicit-dir guard passes | `test check --rerun-tasks` BUILD SUCCESSFUL in 50s, :validatePlugins ran, ERT 26, PCPT 53, engine 9 classes / 47 tests; README gate `872 actionable tasks`, CC stored |
| T022 | `2 tests completed, 2 failed`; runtime guard `--rerun` tests=14 passes before the change | ERT 52, PCPT 80 |
| T027 | ``../README.md shows no `<!-- palbase-example: app/build.gradle.kts -->` block`` | filtered 1/0; full ERT 52, LFCT 11, PCPT 87 |
| T028 | same message for `../distribution/README.md` | filtered 1/0; full PCPT 88 |
| T029 | exit 1, first line `missing: release is refused — …`, `22 missing`, 0 `not in` lines | `the section names every 2.5 behaviour change` |
| T030 | `notes for 2.5.0: 1 lines, 0 DAVRANIŞ DEĞİŞİKLİĞİ heading(s)`, `publish.sh would attach no 2.5 notes for 2.5.0` | `notes for 2.5.0: 150 lines, 1 DAVRANIŞ DEĞİŞİKLİĞİ heading(s)` |

T029's runtime-unchanged claim holds: `git diff --stat v2.4.0..HEAD -- codegen-engine shared palbe palbe-core …` shows only `GeneratedConfigLoaderTest.kt | 19 +`.

**T030's local release steps (Adım 4), all on the chain head:**
- **4.2 README gate:** 872 tasks with CC stored, then `866 actionable tasks: 29 executed, 837 up-to-date` with CC reused.
- **4.3:** two `Palbase: release → local (palbase.env.release in gradle.properties) [sample/palbase/environments]` lines.
- **4.4 plugin gate:** `-p codegen-gradle check`, BUILD SUCCESSFUL in 59s. :validatePlugins, :codegen-engine:test and :test all ran: 151 plugin tests, engine 9 classes / 47 tests. The second run took 2s with CC reused.
- **4.5:** `Publication verification passed for 2.5.0`.
- **4.6:** Test-repository publishes, BUILD SUCCESSFUL in 6s and 3s.
- **4.7:** `artifact directories for 2.5.0: 11`, `plugin POM pins palbase-codegen-engine 2.5.0`, `marker io.palbase.codegen 2.5.0 → io.palbase:codegen-gradle 2.5.0`, `plugin class file major version 61`.
- **Upstream CI command:** `:codegen-engine:test :codegen-gradle:test :palbe-core:testDebugUnitTest --rerun-tasks` gave BUILD SUCCESSFUL in 59s; palbe-core 89 classes / 603 tests, GeneratedConfigLoaderTest 14.

#### Consumer proofs I re-ran

**T007 target end state** (built from the text's blocks).
- On AGP 8.11.1 / Gradle 8.13 I got the 4 planned `Palbase:` lines and all 4 `base_url`s, BUILD SUCCESSFUL in 19s, CC stored.
- The local.properties toggle behaved as planned, including `…/target-end-state/local.properties has changed.`
- On AGP 9.1.1 / Gradle 9.3.1 the same 4 lines appeared, in 18s. With the import removed: `…/app/build.gradle.kts:28:23: 'var environment: String?' is deprecated. Palbase: …`, BUILD FAILED.

**T031 local dry run.** I rsynced both Test repositories into one tree. For 2.6.0 the check prints 11 `FAIL` lines, exit 1. For 2.5.0 it prints 11 `ok` lines, exit 0.

**T032** (trial clone, with scratch copies of the init scripts pointing at the verify clone).
- Red: 8 FAIL / 3 ok, with the `(palbase.env, the 2.3 global property)` lines.
- Green: 11/11 with proof-init and 11/11 with released-init.
- buildEnvironment resolves `…gradle.plugin:2.5.0` → `codegen-gradle:2.5.0` → `palbase-codegen-engine:2.5.0`.
- My trial tree is identical to the rebaser's ae6f549.

**T033** (a copy of the user app; the real app was only read).
- Red: 1 ok / 21 FAIL, with `:43:9` featureX and `:50:9` featureY.
- Adım 0 backup: 40 files.
- Intermediate checks: 0 artefacts in the APK, and featureX refused.
- Green: 22/22 with proof-init and 22/22 with released-init, using `-x lintVitalRelease`.
  - The lint-vital task alone offline really fails: `No cached version of androidx.compose.material3:material3-desktop:1.3.0`.
  - The featureX APK has the expected config, `debuggable 0xffffffff` and `CN=Android Debug`.
- PERSONAL check:
  - Red: 3 FAIL.
  - With `echo >>`: 3 FAIL. The real local.properties has no trailing newline, so the line is glued onto `sdk.dir` (confirmed read-only).
  - With `printf` on its own line: 5 ok.

**T034 Android halves.** The Adım 2 `nothing generated … (searched app/palbase/environments, palbase/environments)` line is exact. The deleted-environment refusal is exact.

**Upstream classified errors (the envelope path).** None of the consumer proofs exercise it, because both contracts declare 0 `x-palbase-errors`. I measured it separately (finding 3):
- It compiles against palbe 2.5.0.
- It fails against palbe 2.3.0 with `Unresolved reference 'envelope'`.

#### Search for leftover 2.4 references

**Final tree.** The only additions mentioning 2.4 relative to the base are:
- the CHANGELOG line `Runtime ve engine kodu 2.4.0 ile aynı`;
- the public-tree YAYIN bullet;
- `Upgrading from 2.3 or 2.4`;
- the public README bullet `2.4.0 was not published here`.

Upstream's `## 2.4.0 — 2026-09-26` section and the `v2.4.0` tag are intact, and `Son etiket` goes to `v2.5.0` only at T030.

**Revised texts, GC and tools.** Every remaining `2.4` is one of:
- upstream's 2.4.0;
- the `proto-2.4/` path;
- the quoted report title `YENİ — 2.4 is a breaking release…`.

The unchanged tasks mention only `proto-2.4/`. Tools extracted from the texts are identical to the rebaser's tools-new, and the diff against the current plan tools is only 2.4 → 2.5 strings.

**Line references re-checked:** CHANGELOG.md:100-112 and :16, Codec.kt:23-24, README.md:212, publish.sh.

**Public tree.** The local distribution clone has main d840817 and only tag v2.3.0. The real repo's local Test repository metadata lists only 2.3.0, so publish.sh's metadata merge will not advertise a 2.4.0.

#### Still open for the lead (confirmed, not changed)

- The `## Kanıt` section of the plan still describes the old chain.
- plan-cli.md still says `palbaseAndroidVersion = "2.4.0"`, and "Dalga 2 — plugin 2.4.0" at line 5357, among its 26 `2.4` lines.
- decisions.md D-026 still carries a 2.4 reference.
- The two public-tree bullets (T028/T029) assume 2.4.0 is never published to palgroup/palbackend-android before T031.

#### Scratch files

- **Verified full plan** with T003/T032/T033/T034/GC and the separator fix: `…/plugin-rebase/verify/plan-plugin.verified-2.5.md`.
- **Fixed texts:** `…/verify/fixed/`.
- **Tools as extracted from the texts:** `…/verify/plantools/`.
- **Replay chain:** branch `plan-verify` in `…/verify/plugin`, tree identical to 54dec1c.
- **Logs and JUnit XML:** `…/verify/logs/`.
- **Proof projects:** `…/verify/t007`, `…/verify/trial`, `…/verify/t033`, `…/verify/classified`.
