# Android: build type ortamı seçer — Şartname

**Tarih:** 2026-09-26 · **Durum:** onay bekliyor
**Karar günlüğü:** `./decisions.md` · **Planlar:** `./plan-cli.md`, `./plan-cloud.md`, `./plan-plugin.md`
**Kanıt:** `./reports/verification-2026-09-25.md` (35 iddia + 12 yeni bulgu, gerçek koda karşı), `./reports/prototype-and-review-2026-09-24.md` (prototip, 18 durumluk matris, iki eleştirmen), `./reports/design-analysis-2026-09-24.md` (ilk tasarım), `./reports/proto-2.4/` (prototipin yamaları)

## Problem ve Hedef

Bugün bir Android uygulaması Palbase ortamını **tek bir global** Gradle özelliğinden alıyor (`palbase.env`, varsayılan `local`); her variant aynı ortamı derliyor. Checkout kökündeki `palbase/`'u bulmak için de bir `palbase { environmentsDir.set(…) }` bloğu gerekiyor. Kullanıcının istediği: **kişi başına feature ortamları** (`featureX`, `featureProfileUpdate`, …) ve Android Studio'da build type seçince o ortamın derlenmesi, bloksuz.

İnceleme bunun Android tarafında yapılabildiğini kanıtladı (build type → ortam, APK'nın içine bakılarak; configuration cache doğru; üretilen kod 2.3 ile byte byte aynı). Ama akışın geri kalanında dört blocker ve bir düzine major bulundu: ortam adları doğrulanmadan dosya yoluna dönüşüyor (checkout dışına yazma), sunucu adları benzersiz tutmuyor (iki `main`), library modülünde plugin sessizce yanlış ortam paketliyor, flavor'lı app'te variant anahtarları sessizce yok sayılıyor; `link` Android için `local/`'u neredeyse hiç doğru yazmıyor, eski ortam dizinlerini temizlemiyor ve Gradle için hiçbir şey söylemiyor.

**Hedef:** Bir Android geliştiricisi `palbase link` sonrasında build type'ını seçer ve **o build type'ın ortamı derlenir** — yanlış ortam hiçbir yolda sessizce paketlenmez, bir ortam adı checkout dışına hiçbir şey yazamaz, silinen bir ortam bir sonraki link'te temizlenir ve ona bağlı build yüksek sesle düşer.

## Kapsam ve sıra

| # | Repo | Plan | Neden bu sırada |
|---|---|---|---|
| 1 | `palbase-cli` (bu repo, `main`) | `plan-cli.md` | Ad güvenliği açığı **bugün** yayında; diğer işlere bağımlı değil. **İki dalga (D-026):** Dalga 1 (T001–T018, T020–T022) hemen; Dalga 2 (T019 ve T023–T026: 2.5'in arama sırasına dayanan iç içe link reddi, 2.5'e özgü Gradle satırları ve doctor anahtarları) plugin 2.5.0 yayımlandıktan sonra. |
| 2 | `palbase-cloud` (`origin/main`; yerel kopya 1685 commit geride) | `plan-cloud.md` | Benzersiz, değişmez slug olmadan "kişiye bir ortam" güvenli değil (iki `main`). |
| 3 | `palbackend-android-src` (plugin + runtime 2.5.0) | `plan-plugin.md` | CLI'ın yazdığı düzene ve sunucunun adlarına dayanır. |
| 4 | Tüketiciler (trial app, kullanıcının test app'i) | `plan-plugin.md` son görevleri | 2.5.0 yayınlandıktan sonra. |

## Fonksiyonel Gereksinimler

### A. palbase-cli — ortam adı güvenliği

- **FR-001** IF sunucunun listelediği bir ortam adı **her takım arkadaşının işletim sisteminde tek temiz bir yol parçası** değilse (boş; `.` ya da `..`; `/` ya da `\` içeriyor; `.` ile başlıyor; NUL ya da bir kontrol karakteri — U+0000–U+001F, U+007F — içeriyor; UTF-8 değil; Windows'ta: sonda nokta ya da boşluk, aygıt adı — `CON`, `NUL`, `COM1`… — ya da `<>:"|?*`) THEN `palbase link` o ortam için SHALL hiçbir dosya yazmasın, adını tırnaklı (`%q`) basıp atlasın ve diğer ortamlara devam etsin; atlanan ortam projenin **varsayılan** ortamıysa link SHALL hatayla dursun ve panelde yeniden adlandırmayı söylesin. *(A1, yeni: kalıcı DoS)*
- **FR-002** WHEN `palbase spec` ya da `palbase push`'un sözleşme yenilemesi bir ortam adıyla dosya yazarsa THEN FR-001'in kapısı SHALL aynı şekilde uygulansın. *(A1 — `stack_spec.go`)*
- **FR-003** IF listelenen iki ortam aynı dizine düşerse (adlar birebir aynı ya da yalnız harf büyüklüğünde ya da Unicode biçiminde — NFC/NFD — farklı) THEN link SHALL ikisinin de ref'ini adlandırsın; ikisi de varsayılan değilse ikisini de atlasın, biri varsayılansa hatayla dursun. *(A2a, A3i)*
- **FR-004** IF listede adı `local` (harf büyüklüğü gözetmeksizin) olan bir bulut ortamı varsa THEN link SHALL onu atlayıp söylesin; `local/` yalnız bu makinenin yığınına aittir. *(A4)*
- **FR-005** WHEN varsayılan olmayan bir ortamın dosyaları yazılamazsa THEN link SHALL o ortamı raporlayıp diğerlerini yazmaya devam etsin. *(yeni: yazılamayan ad tüm link'i düşürüyor)*
- **FR-006** WHEN CLI sunucudan gelen bir ortam adını insan çıktısına basarsa THEN SHALL `%q` ile bassın. *(yeni: terminal kaçış dizisi enjeksiyonu)*
- **FR-007** WHEN `palbase env create <ad>` çağrılırsa THEN ad slug dilbilgisine (D-008) uymuyorsa ya da ayrılmış bir adsa (D-008) CLI istek göndermeden SHALL reddetsin ve kuralı söylesin. *(A5)*

### B. palbase-cli — bu makinenin yığını (`local`)

- **FR-008** WHEN bir app checkout'unda link yapılırsa THEN yerel yığın **`palbase start`'ın kaydettiği grupla** (bağlanan ürünün adı) SHALL aransın, yoksa checkout dizininin adıyla; ikisi de yoksa ve kayıtta başka gruplar varsa link hangilerinin olduğunu SHALL söylesin. *(B1 — `project_link.go:837`, `start.go:676-684`)*
- **FR-009** IF yerel yığın için anahtar **ya da** sözleşme alınamazsa THEN link `local/` için SHALL hiçbir dosya yazmasın (ya ikisi birden ya hiçbiri) ve öneri cümlesi "`palbase start`, then `palbase link` here" olsun. *(B2, yeni: `spec` anahtarsız local'i dolduramıyor)*
- **FR-010** WHEN `link` ya da `spec` bu makinedeki bir yığını hedeflerse (bir `palbase start` kaydı ya da loopback bir self-host adresi) THEN ikisi de ortamı SHALL `local` adlandırsın; `main/` hiçbir yolda loopback bir `base_url` taşımasın. *(B3, B8)*

- **FR-020** WHEN link bir app checkout'unda ortam dosyası yazarsa THEN `.gitignore`'da `palbase/environments/local/` SHALL bulunsun (dosya yoksa oluşturulur, satır yoksa eklenir, varsa dokunulmaz) ve `palbase doctor` git'in takip ettiği bir `palbase/environments/local/` görürse SHALL uyarsın ve `git rm -r --cached palbase/environments/local` önerisini bassın. *(D-014)*

### C. palbase-cli — temizlik

- **FR-011** WHEN link Android ya da web için ortam başına dosya yazarsa THEN artık listede olmayan ortamların dizinlerini SHALL silsin — yalnız dizindeki her dosya CLI'ya aitse; `local` ve listede Failed/Deleting görünen ortamlar korunur — ve her silmeyi basşın. *(B4)*
- **FR-012** WHEN temizlik diskteki dizinleri istenen adlarla karşılaştırırsa THEN harf büyüklüğünü gözetmeden SHALL karşılaştırsın, aynı koşuda yazılmış bir dizini asla silmesin ve yalnız harf büyüklüğü farklı bir dizini istenen ada SHALL yeniden adlandırsın. *(A3ii/iii)*

### D. palbase-cli — link'in Android'e söyledikleri

- **FR-013** WHEN link Android config'i yazarsa THEN Gradle kurulumunu SHALL bassın: depo satırı, `id("org.jetbrains.kotlin.plugin.serialization")`, `id("io.palbase.codegen") version "<sürüm>"`, `implementation("io.palbase:palbe:<sürüm>")`, ve `palbase.env.debug=<varsayılan>` / `palbase.env.release=<varsayılan>` satırları — değerler listeden gelir, asla bir literal değil. *(B5, yeni: ilk ortamın adı oluşturulduğu yere bağlı)*
- **FR-014** WHEN link bir ortamın `openapi.json`'unu yazarsa THEN bunu SHALL basşın; sözleşmesi olmayan ortamları "push first" önerisiyle SHALL listelesin. *(yeni)*
- **FR-015** WHEN link bir React Native / Flutter kökünde koşarsa THEN Android'i `android/app/build.gradle(.kts)` ve `android/build.gradle(.kts)` üzerinden de SHALL algılasın. *(N2)*
- **FR-016** IF link bir **Gradle dizininde** (`build.gradle(.kts)` ya da `settings.gradle(.kts)` taşıyan) ve `palbase/project.json` taşıyan bir dizinin **altında** koşarsa THEN SHALL reddetsin ve kökü adlandırsın (modül içinde ikinci bir `palbase/` doğmasın); yürüyüş, üst dizini bağlı olmayan bir Gradle kökünde biter (monorepo'da kendi Gradle kökü olan app serbest). Gradle'sız dizinler (monorepo'daki web/iOS app'leri) bu kuraldan etkilenmez — `20e5d7e`'deki davranış korunur. *(D3a; D-025)*
- **FR-017** WHEN link'in Android OAuth seçimi flavor ya da `applicationIdSuffix` yüzünden belirsiz kalırsa THEN SHALL nedenini ve commit edilecek `oauth.android` JSON'unu (aday üçlülerden) söylesin. *(B9)*

### E. palbase-cli — status ve doctor

- **FR-018** WHEN `palbase status` anahtar kaymasını ölçerse THEN diskte config'i olan **her** platform için SHALL ölçsün (bugün yalnız `ios`). *(B7)*
- **FR-019** WHEN `palbase doctor` bir Android checkout'unda koşarsa THEN bir Android bölümü SHALL bassın: her `palbase/environments/<env>/`'un tamlığı (iki dosya, boş olmayan `api_key`, `x-palbase-roles`), `gradle.properties` ve `local.properties`'teki `palbase.env.*` anahtarları, eşlenmemiş `release`, release'e eşlenmiş loopback bir ortam ve harf büyüklüğü ikizleri. `env` satırı app checkout'unda "verbs only; the build type picks the app's environment" notunu taşısın. *(B7)*

### F. palbase-cloud — ortam slug'ı

- **FR-101** Her ortam satırı SHALL değişmez bir `slug` taşısın: D-008 dilbilgisi, ayrılmış adlar hariç, ürün içinde **harf büyüklüğü gözetmeksizin benzersiz** (`UNIQUE (product_id, lower(slug))`). *(A1, A2, A3)*
- **FR-102** WHEN bir proje oluşturulursa (CLI ya da panel) THEN ilk ortamın slug'ı SHALL açıkça `main` yazılsın; görünen ad serbesttir. *(A2, yeni: panel "Production")*
- **FR-103** WHEN bir ortam oluşturulursa THEN istek bir slug taşıyabilsin; taşımıyorsa sunucu görünen addan deterministik olarak SHALL türetsin; geçersiz ya da çakışan slug SHALL 4xx ile reddedilsin ve kural söylensin.
- **FR-104** WHEN bir ortam yeniden adlandırılırsa THEN yalnız görünen ad SHALL değişsin; slug değişmez. *(A2b)*
- **FR-105** WHEN göç koşarsa THEN mevcut her satıra slug SHALL doldurulsun: ürünün ilk ortamı `main`; diğerleri bugünkü CLI adı (geçerliyse ve benzersizse), değilse türetilmiş ve çakışmada ekli bir slug; eşleme bir göç raporuna yazılsın.
- **FR-106** Her uç (CLI projeler listesi, ortamlar, bindings, panel) aynı ortam için SHALL aynı `slug`'ı dönsün; CLI listesindeki `name` alanı **slug'ı** taşısın (eski CLI'lar da güvenli dizin adı alır), görünen ad `display_name`'de; `is_production` gerçek değer olsun. *(yeni: yüzeyler ada üç farklı şey diyor)*
- **FR-107** Panel her ortamın slug'ını görünen adın yanında SHALL göstersin; oluşturma formu türetilen slug'ı düzenlenebilir göstersin.

### G. Plugin `io.palbase.codegen` 2.5.0

- **FR-201** Her variant V (build type B, flavor'lar F₁…Fₙ, birleşik flavor adı F) için ortam, ilk bulunanın kazandığı şu sırayla SHALL seçilsin:
  1. komut satırı `-Ppalbase.env.<V>`, sonra `-Ppalbase.env.<F…>` / `-Ppalbase.env.<B>`;
  2. komut satırı `-Ppalbase.env` (o çağrıdaki **bütün** variant'lar); *(C2)*
  3. `local.properties` `palbase.env.<V|F|B>` — **yalnız debuggable variant'larda**; debuggable olmayanda yok sayılır ve bir uyarı adı söyler; *(C4)*
  4. DSL: variant'ın flavor'larındaki ve build type'ındaki `palbase { environment = "…" }`; *(D2)*
  5. kök `gradle.properties` **dosyası** `palbase.env.<V|F|B>`; *(C3)*
  6. B `benchmark<X>` / `nonMinified<X>` ise `<x>` gibi; B tam olarak `benchmark` ise ilk `matchingFallbacks` girdisi gibi, yoksa `release`; *(D4)*
  7. eski global `palbase.env` (dosyadan) — **build type adından önce**; *(C1, yeni: sessiz ortam değişimi)*
  8. B `debug`/`release` değilse B'nin **adı**;
  9. B `debug` ise `local`; B `release` ise **ret**, iki yolu da söyleyen mesajla.
- **FR-202** Aynı seviyede flavor ile build type farklı ortam söylerse ya da DSL ile `gradle.properties` dosyası aynı build type için farklı ortam söylerse THEN build SHALL reddetsin ve iki yeri adlandırsın. Bir değer, dosyadan değil de başka bir Gradle özelliği kaynağından geliyorsa (`ORG_GRADLE_PROJECT_*`, `-Dorg.gradle.project.*`, `~/.gradle/gradle.properties`) "override" etiketiyle komut satırıyla aynı sırada sayılsın ve çatışma reddi üretmesin. *(C3)*
- **FR-203** WHEN hiçbir variant'ın, flavor'ın ya da build type'ın adını taşımayan bir `palbase.env.*` anahtarı görülürse THEN plugin SHALL bir uyarı bassın (hata değil — kök dosya birden çok modülce paylaşılır). Bir modülün kendi `gradle.properties`'indeki `palbase.env.*` SHALL reddedilsin ("put it in the root gradle.properties"). *(C3, D2)*
- **FR-204** Seçilen ad tek yol parçası olmalı ve dizin **birebir aynı adla** bulunmalı; yalnız harf büyüklüğü farklı bir dizin varsa hata ikizi adlandırıp eşleme satırını SHALL söylesin. *(D5)*
- **FR-205** Kök arama: açık `environmentsDir` → `<modül>/palbase/environments` → `<rootDir>/palbase/environments` → `<rootDir>/../palbase/environments` (üstünde `.git` ya da `palbase/project.json` ile sınırlı). Birden fazla aday **varsa** (diskte bulunuyorsa) build SHALL reddetsin ve hepsini adlandırsın; hiçbiri yoksa hangi yolların arandığını bir lifecycle satırında SHALL söylesin. *(D3a, D3b)*
- **FR-206** Debuggable olmayan bir variant loopback ya da `http` bir `base_url` derliyorsa THEN build SHALL reddetsin ve cleartext network-security-config'i o variant'a hiç yazmasın. *(C4, B8)*
- **FR-207** Paketlenen `palbase/palbase-config.json` SHALL `palbase_environment` (ad) alanını taşısın; runtime bilinmeyen anahtarları yok sayar. *(C5)*
- **FR-208** Plugin bir **library**'de uygulanmışsa ve bir app variant'ı (flavor × build type) library'nin `matchingFallbacks` ile verdiği variant üzerinden, kendi seçeceğinden farklı bir ortam paketliyorsa THEN o app variant'ının `pre<Variant>Build` görevi SHALL hata versin — configuration cache yeniden kullanıldığında da; diğer variant'lar ve IDE sync etkilenmez; `palbase.env.<appBuildType|appVariant>=<fallback ortamı>` bunu kasıtlı kılar. Isolated Projects açıkken kontrol yapılamaz ve bu bir kez söylenir. *(D1, N1)*
- **FR-209** Kotlin DSL'de import'suz `palbase { environment = … }` SHALL açık bir derleme hatası versin (`@Deprecated(level = ERROR)` tuzağı); Groovy'de aynı çağrı açık bir `GradleException` versin. *(C6)*
- **FR-210** Hata metinleri: `local` varsayılanı yoksa "no local stack is linked here; set `palbase.env.debug=<one of …>`, or run `palbase start` in the backend and then `palbase link` here"; `android-config.json` var `openapi.json` yoksa "environment `<env>` has no contract yet — `palbase push --env <env>`, then `palbase link` here". *(B1, B6)*
- **FR-211** Her üretim satırı seçilen ortamı, kaynağını ve kökü SHALL söylesin: `Palbase: <variant> → <env> (<origin>) [<root>]`.
- **FR-212** Plugin, runtime ve engine — 11 artifact — **2.5.0** olarak birlikte çıkar; README/distribution README desteklenen aralığı SHALL söylesin: AGP 8.10.1+, Gradle 8.11.1+, JDK 17+; CHANGELOG'da tam bir "DAVRANIŞ DEĞİŞİKLİĞİ" listesi. *(E1, E3, E5, yeni: minor sürümde kırıcı değişiklik)*
- **FR-213** SDK reposunun kendi release kapısı yeşil kalsın: kök `gradle.properties`'e `palbase.env.release=local`. *(E2)*

### H. Tüketiciler

- **FR-301** `palbe-trial-android` 2.5.0'a geçer: sürüm pinleri, `palbase {}` bloğu silinir, `palbase.env=main` yerine `palbase.env.debug=main` + `palbase.env.release=main`, `palbase/.gitattributes` ve `roles.json` kaldırılır.
- **FR-302** Kullanıcının test app'i (`MyApplicationPalbaseAndroidSdkTest`) hedef son duruma geçer (aşağıda).

## Fonksiyonel olmayan gereksinimler

- **NFR-001** CLI: `gofmt -l .` boş, `go vet ./...` ve `go vet -tags e2e ./tests/e2e/` temiz, golangci-lint v2.12.2 temiz, `go test ./...` başlangıçta kırık olan 16 test (`internal/backend`, bundler/check-mode/init — yerel bun 1.4.2 ile CI'ın 1.3.9'u farkı) dışında yeşil; yeni bir kırık yok.
- **NFR-002** Plugin: `./gradlew -p codegen-gradle check` ve README kapısı `./gradlew check lintRelease :consumer-release:assembleRelease --configuration-cache` yeşil; configuration cache her yeni davranışta doğru (yeniden kullanılan girişte de).
- **NFR-003** Kullanıcıya basılan her dize İngilizce (`CLAUDE.md`).
- **NFR-004** Tek yazıcı, `main`; worktree ve yan branch yok; commit'ler pathspec ile.

## Hedef son durum (kullanıcının test app'i)

```
MyApplicationPalbaseAndroidSdkTest/
├── gradle.properties        palbase.env.debug=main · palbase.env.release=main   (palbase.env=main SİLİNİR)
├── local.properties         sdk.dir=…  (commit edilmez; İSTEĞE BAĞLI kişisel satır: palbase.env.debug=featureX — varsa debug'ı featureX'e çevirir, aşağıdaki son satır)
├── app/build.gradle.kts     create("featureX") { initWith(getByName("debug")); matchingFallbacks += listOf("debug") } — palbase {} bloğu YOK
└── palbase/
    ├── project.json
    └── environments/{main, featureX, featureY, …}/{android-config.json, openapi.json}
```

| Variant | Ortam | Neden |
|---|---|---|
| debug | main | `palbase.env.debug` (dosya) |
| release | main | `palbase.env.release` (dosya) |
| featureX | featureX | build type adı |
| featureProfileUpdate | feature-profile-update | `palbase { environment = "feature-profile-update" }` |
| debug, `local.properties`'te `palbase.env.debug=featureX` varken | featureX | kişisel satır dosyadaki anahtarı geçer (FR-201 adım 3; yalnız debuggable variant'ta) |

## Kapsam dışı

- D-036'nın branch → slug eşlemesi ve PR önizleme ortamları (tasarım aşamasında; FR-101'in dilbilgisi onlara uyacak şekilde seçildi).
- `link`'in Android'de `app/` modülünü (ve `app/build`'i) stage'e kopyalaması (`link_artifacts.go:195`) — ayrı bir iş; bu koşunun hiçbir FR'ı ona dayanmaz.
- palbe-core'da oturum/flag depolarının ortam ref'ine göre ayrılması (farklı ortamlar aynı applicationId'yi paylaşıyor).
- Build type başına OAuth seçimi.

## İzlenebilirlik (bulgu → FR)

| Bulgu | FR | | Bulgu | FR |
|---|---|---|---|---|
| A1 | 001, 002, 101 | | C1 + yeni (sessiz değişim) | 201.7 |
| A2 | 003, 101–106 | | C2 | 201.2 |
| A3 | 003, 012, 101 | | C3 | 201.5, 202, 203 |
| A4 | 004 | | C4 | 201.3, 206 |
| A5 | 007 | | C5 | 207, 211 |
| A6 (D-036) | kapsam dışı; 101 dilbilgisi | | C6 | 209 |
| yeni DoS | 001, 005 | | C7 | 212 (CHANGELOG), 013 |
| yeni kaçış dizisi | 006 | | D1, N1 | 208 |
| yeni ilk ortam adı | 102, 013 | | D2 | 201.1/4, 202, 203 |
| B1 | 008, 210 | | D3a | 016, 205 |
| B2 + yeni | 009, 020 | | D3b, N2 | 015, 205 |
| B3, B8 | 010, 206 | | D4 | 201.6 |
| B4 | 011 | | D5 | 204 |
| B5 | 013 | | E1–E5 + yeni | 212, 213 |
| B6 + yeni | 014, 210 | | | |
| B7 | 018, 019 | | | |
| B9 | 017 | | | |
