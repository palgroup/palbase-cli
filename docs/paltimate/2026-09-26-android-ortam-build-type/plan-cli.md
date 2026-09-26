# Palbase CLI — Android ortam güvenliği ve link — Uygulama Planı

> **Ajan çalışanlar için:** Görev görev yürüt (superpowers:subagent-driven-development ya da superpowers:executing-plans). Adımlar `- [ ]` checkbox. Görev başlıkları makine-okur meta veri taşır (`deps | files | satisfies`).

**Goal:** Sunucudan gelen hiçbir ortam adı checkout dışına ya da tek dizine iki kez yazamaz; `palbase link` bu makinenin yığınını doğru `local` olarak yazar ve commit ettirmez, silinen ortamların dizinlerini Android/web'de de temizler, Android için Gradle kurulumunu ve ortam eşlemesini söyler; `status` ve `doctor` Android'i görür.

**Architecture:** Tek bir ad kapısı (`envname.CheckDir` / `SameDirectory` / `Label`) link, spec, push ve insan çıktısının girişinde; bu makinenin yığınını adlandıran tek fonksiyon link ve spec'te ortak; mevcut Apple süpürmesi Android/web'e genişler ve harf büyüklüğü/Unicode biçimini katlar. İki dalga: **Dalga 1 (T001–T018, T020–T022)** bugün yayımlanır (güvenlik açığı); **Dalga 2 (T019, T023–T026)** plugin 2.4.0 yayımlandıktan sonra `main`'e girer (2.4'e özgü Gradle satırları ve doctor anahtarları).

**Tech Stack:** Go (go.mod `go 1.26.6`; yerelde Go 1.27.1), cobra, `go test`, golangci-lint v2.12.2 (`GOTOOLCHAIN=go1.26.6`).

**Spec:** `./spec.md` (FR-001…FR-020) · **Kararlar:** `./decisions.md` (D-008, D-011, D-012, D-014, D-023, D-024, D-025, D-026) · **Kanıt:** `./reports/verification-2026-09-25.md`

**Sıra:** Bu plan ilk yürütülür (spec "Kapsam ve sıra"); Dalga 2, `plan-plugin.md`'nin yayın görevinden sonra.

## Global Constraints

- **Depo ve taban:** `palbase-cli` `main`, taban `20e5d7e`; tek modül `github.com/palgroup/palbase-cli`. Scratch zinciri görev başına bir commit, T001 `e6b2fbf` … T026 `327a941` (D-025 sonrası; `reports/bundles/cli-plan.bundle`); taslak zinciri `draft-cli-8714d30` etiketinde.
- **Go:** `go.mod` `go 1.26.6`; bu makinede Go 1.27.1 (`/opt/homebrew/bin/go`), derleme ve test için yeterli.
- **Lint:** golangci-lint v2.12.2 kurulu değil; `GOTOOLCHAIN=go1.26.6 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...` → `0 issues.` (Dalga 1 sonunda ve son kapıda ölçüldü).
- **CI kapıları** (`.github/workflows/ci.yml`; ubuntu-latest, Go 1.26.6, bun 1.3.9): `go build ./...` · `gofmt -l .` boş · `go vet ./...` · `go vet -tags e2e ./tests/e2e/` · golangci-lint v2.12.2 · `go test -race`.
- **B16 (NFR-001 taban kırıkları, hepsi `internal/backend`; yerel bun 1.4.2, CI 1.3.9):** `TestBuildWritesExactlyOneFile`, `TestCheckMode_CentauriClassIsCaught`, `TestCheckMode_UserTypeScript7StillBuilds`, `TestCheckMode_NoTypeScriptInProjectStillBuilds`, `TestCheckMode_AnImportNOTHINGProvidesFails`, `TestCheckMode_BrokenSchemaFails`, `TestBuildIgnoresAConfigDirectoryEntirely`, `TestCheckMode_ABigSchemaStillBuilds`, `TestCheckMode_AControllerThatRegistersNothingIsNamed`, `TestBuildRefusesASurfaceClassNoModuleLists`, `TestBuildAcceptsASurfaceClassAModuleLists`, `TestBuildCheckNodeSuite`, `TestTheScaffoldComesFromTheInstalledPackage`, `TestInitInsideATreeWithAnAncestorPackageJSON`, `TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`. Gerçek depoda `../palbase-ts` olduğu için 16'sı da koşar. Scratch'te (palbase-ts yok) ölçülen: 13 SKIP, `TestBuildCheckNodeSuite` PASS, iki bundler testi FAIL. "Yeni kırık yok" = B16 dışında FAIL yok.
- **Dil (NFR-003):** kullanıcıya basılan her dize, kod ve kod yorumları İngilizce; plan düzyazısı ve commit mesajları Türkçe (`TestNoUserFacingStringIsTurkish` yeşil).
- **Yazıcı (NFR-004):** tek yazıcı, `main`; worktree ve yan branch yok. Commit'ler pathspec'li: `git add <yollar> && git commit -m "…" -- <yollar>`; silinen dosya da pathspec'te (T014). Her commit `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` ile biter.
- **Yayın sırası:** Dalga 1 = T001–T018 ve T020–T022 (T019 D-025/D-026 ile Dalga 2'de). CLI sürümü önce çıkar (spec "Kapsam ve sıra" #1: ad güvenliği açığı bugün yayında) ve T022'den sonra kesilir. Dalga 2 = T019 ve T023–T026: plugin 2.4.0 (`io.palbase.codegen` + `io.palbase:palbe`) `https://palgroup.github.io/palbackend-android/`'da yayımlanmadan `main`'e commit'lenmez. Gerekçe: 2.3 yalnız global `palbase.env`'i okur (`palbackend-android-src` `v2.3.0` `PalbaseCodegenPlugin.kt`: `gradleProperty("palbase.env").orElse("local")`).
- **Android sabitleri (T023):** `palbaseAndroidVersion = "2.4.0"`, `palbaseAndroidRepository = "https://palgroup.github.io/palbackend-android/"`. Plugin'in desteklediği aralık AGP 8.10.1+, Gradle 8.11.1+, JDK 17+ (FR-212, plan-plugin). Bu planda Android build'i yok; `JAVA_HOME`/`ANDROID_HOME` gerekmez.
- **Dilbilgisi (D-008, onaylı):** `envname.SlugPattern = ^[A-Za-z][A-Za-z0-9-]{0,38}$` CLI'daki tek yer; sunucudaki `ENVIRONMENT_SLUG_PATTERN` aynı metni taşır.
- **Ad kuralları tek pakette (`internal/envname`, yaprak):** `CheckDir` (gevşek; link/spec), `CheckSlug` (sıkı; `env create`), `SameDirectory` (APFS'in tek dizin saydığı adlar), `Label` (terminal).
- **Kararlar:** D-008, D-014, D-023 kararlaştırıldı; KOŞULLU görev yok. D-024, D-025 ve D-026 `decisions.md`'ye kaydedildi (D-025 ile T019 daraldı; D-026 ile T019 Dalga 2'de).
- **Diskler:** testler macOS'un varsayılan APFS'inde koşar; bu disk harf büyüklüğüne ve Unicode biçimine duyarsızdır. Duyarlı disk ölçümü üç adımda yapılır: `hdiutil create -size 300m -fs "Case-sensitive APFS" -volname revcs -type SPARSE cs.sparseimage`, ardından `hdiutil attach -nobrowse -mountpoint <dir> cs.sparseimage`, ardından `TMPDIR=<dir>/tmp go test …`. İş bitince `hdiutil detach <dir>`.
- **Test dosyası adları:** `_android_test.go` ya da `_windows_test.go` ile biten bir dosya yalnız o GOOS için derlenir, kullanılmaz (T001, T020, T022).
- **Yasaklar:** bulut çağrısı yok, `palbase login` yok, yayın yok; `gradle --stop` yok; Java, Gradle ve Docker süreçleri durdurulmaz.

## Review Focus

- **Eski bir CLI'ın loopback `main/`'ini taşıyan Android ya da web checkout'u bu makinenin yığınına yeniden link'lenir** → `local/` yazılır, `main/` silinir ve `removed palbase/environments/main (it held the stack on this machine, which is local/ now)` basılır. Bulut adresli bir `main/` kalır. CLI'ın yazmadığı bir dosyayı tutan `main/` söylenir ve bırakılır. Test: **T014** `TestALinkToThisMachinesStackTakesAwayTheLoopbackMainAnOlderLinkLeft` (`/ios`, `/android`, `/web`), `TestAnOldLoopbackMainHoldingSomebodysFileIsSaidAndLeft`, `TestALinkWithNoProjectListingRemovesNoAndroidEnvironment`.
- **Monorepo: kök backend'e bağlı (`.git` + `palbase/project.json`); altında Android'in Gradle kökü `apps/android` (`settings.gradle.kts`, kendi `.git`'i yok), web app'i `apps/web` (`package.json`), iOS app'i `apps/ios` (`App.xcodeproj`) ve bir `docs/`** → dördünde de link ve yeniden link çalışır, kökün `project.json`'u değişmez. Android `apps/android/palbase/environments/main/android-config.json` yazar. Gradle'sız üçü `20e5d7e`'nin yazdığını yazar: aynı testler `20e5d7e`'de PASS, T018 ile T019 arasında çıktı ve dosya listesi birebir aynı. Ret yalnız Gradle dizininde (D-025): bağlı kökün hemen altındaki RN/Flutter `android/`'ı (symlink üzerinden girilse de), D3a'nın `app/` modülü (kendi `.git`'i olsa da) ve bağlanmış monorepo app'inin kendi `app/` modülü (ret `apps/android`'i adlandırır). Orada önceki bir link'in `palbase/`'u duruyorsa ret onu adlandırır ve silinmesini söyler. Ret hedef çözülmeden gelir, oturum ve ağ sorulmadan. Test: **T019** `TestAnAppThatIsItsOwnGradleRootLinksInsideALinkedMonorepo`, `TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore` (`/web`, `/ios`), `TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore`, `TestAReactNativeAndroidDirectoryIsRefusedBelowItsLinkedRoot`, `TestALinkBelowALinkedCheckoutIsRefusedAndNamesItsRoot`, `TestARefusalNamesTheCopyAnEarlierLinkLeftHere`, `TestTheRefusalComesBeforeTheTargetIsResolved`.
- **Windows'ta tek dizin olmayan adlar ve UTF-8 olmayan ad** (`main.`, `main `, `CON`, `nul.json`, `com1`, `LPT9.log`, `COM²`, `conout$`, `feature:x`, `a*b`) → `CheckDir` her birini "the name …" cümlesini tamamlayan birebir sebeple reddeder; link bu ortamı atlar ve söyler. D-011'in yasal adları geçer (`Production`, `Üretim ortamı`, `v1.2`, `console`, `nullable`, `com10`). Test: **T001** `TestCheckDirRefusesWhatWindowsCannotKeepAsOneDirectory`, `TestCheckDirRefusesEveryNameThatIsNotOneDirectory`, `TestLinkSkipsAnEnvironmentWhoseNameIsNotOneDirectory`.
- **Kaçış baytı taşıyan bir ortam adıyla `palbase clone`** → terminale `\x1b` ulaşmaz. Banner `▸ todoapp/"evil\x1b]0;owned\a"` olarak basılır. İki ret de etiketli: `… is Failed, so there is no source to download` ve `evilref001 is the ref of todoapp/"…" (Failed), but --from-env names main`. Test: **T007** `TestCloneRefusalsPrintANameEscaped`, `TestTheCloneBannerPrintsANameEscaped`.
- **Yalnız Unicode normalleştirmesiyle farklı iki ortam** (`café` NFC ve NFD) → ikisi de ref'iyle adlandırılıp atlanır, hiçbir dosya yazılmaz, link sürer. Süpürme NFD dizinini silmez; dizin listelenen adla bulunur kalır. Test: **T002** `TestLinkSkipsEnvironmentsWhoseNamesDifferOnlyInUnicodeNormalisation`, `TestSameDirectoryIsWhatAMacTakesForOneName`; **T016** `TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInUnicodeForm`.

## Fidelity Audit

- **Şartnamede dayanağı olmayan öğe: none.** Her ek bir FR'ye bağlı. FR metnini genişleten ya da daraltanlar aşağıda "şartname değişiklikleri"nde:
  - Windows ve UTF-8 kuralları (T001) → FR-001 "tek temiz bir yol parçası". CLI Windows için de yayımlanıyor (`.goreleaser.yml`).
  - Unicode biçimi ikizleri (T002, T015, T016) → FR-003 ve FR-012'nin arızası başka yoldan: iki ortam tek dizine düşüyor, aynı koşuda yazılan siliniyor.
  - Eski loopback `main/`'in temizliği (T014) → FR-010 "main/ hiçbir yolda loopback taşımasın".
  - Tek çare (T018) → FR-014.
  - Anahtarsız config satırı (T021) → FR-018.
  - clone/spec etiketleri (T007) → FR-006 "insan çıktısı".
- **Bileşen yüzeyinden farklı imza ya da isim:**
  - Yeni: `envname.SameDirectory(a, b string) bool` (T002), `windowsDevice` ve `windowsForbidden` (T001), `removeThisMachinesOldMain(root string, w io.Writer) error` (T014), `seedLoopbackMain` test yardımcısı.
  - `linkedCheckoutAbove`'un anlamı değişti (T019, D-025): yalnız bir Gradle dizini (`build.gradle(.kts)` ya da `settings.gradle(.kts)`) için yürür; diskteki yolu yürür (`EvalSymlinks`); `.git`'te yalnız bir Gradle kökündeyse durur (bir modülün `.git`'i durdurmaz); üst dizini bağlı olmayan bir Gradle kökünde de durur. Yeni: `refuseInsideALinkedCheckout(dir string) error` — `newLinkCmd`'in `RunE`'si (`resolveLinkTarget`'tan önce) ve `runLink` çağırır; `gradleRootFiles`, `gradleDirectoryFiles`, `holdsOneOf`; test yardımcıları `linkedMonorepo`, `assertRootUntouched`. `.git` durağı FR-016'nın lafzında yok (plugin 2.4'ün `CheckoutMarker`'ıyla aynı işaret, yalnız Gradle kökünde); eski kopyanın adlandırılması ve retin hedef çözülmeden gelmesi FR-016'nın 'kökü adlandırsın'ı doğru kalsın diye.
  - Değişen mesajlar: FR-003 satırları "match when letter case and Unicode form are ignored" diyor (T002). Projesiz ve sözleşmesiz link'te "`palbase spec` fills the contract in" cümlesi artık yalnız istemcisiz checkout'ta basılıyor (T018).
  - Görev numaraları (yeni ← eski): T019←T020, T020←T021, T021←T022, T022←T023, T023←T019. Diğerleri aynı.
- **Planlama sırasında yapılan şartname değişiklikleri** (lead onayı gerekir; gerçek depoya yazmak yasak olduğu için `decisions.md`/`spec.md` güncellemesi lead'de):
  - **A-1 (FR-001 genişledi):** `CheckDir` şunları da reddediyor: sondaki nokta ya da boşluk, Windows aygıt adları, `<>:"|?*` ve UTF-8 olmayan ad. D-011'e şu not eklenmeli: "gevşek kural = her takım arkadaşının işletim sisteminde tek dizin".
  - **A-2 (FR-003/FR-012 genişledi):** "harf büyüklüğü" artık "harf büyüklüğü ve Unicode biçimi" demek (`envname.SameDirectory`).
  - **D-024 (FR-010 × FR-011, kayıtlı):** "Liste yoksa Android/web süpürmesi yok" kuralının tek istisnası şu: projesiz ve ortamı `local` olan bir link, `main/`'i her config'i loopback bir adres taşıyor ve yalnız CLI dosyası tutuyorsa siler.
  - **D-025 (FR-016 daraldı, kayıtlı):** ret yalnız bir Gradle dizininde; yürüyüş üst dizini bağlı olmayan bir Gradle kökünde biter; Gradle'sız dizinler (monorepo web `apps/web`, iOS `apps/ios`, `docs/`) `20e5d7e`'deki gibi link'lenir (aynı testler `20e5d7e`'de PASS). T019 bu karara göre yeniden yazıldı ve D-026 ile Dalga 2'ye alındı.
  - **D-026 (yayın dalgaları, kayıtlı):** T019 ve T023–T026, plugin 2.4.0 yayımlanmadan `main`'e girmez. Bu görevler FR-013'ü ve FR-019'un 2.4'e özgü anahtar/release satırlarını taşıyor. Spec'in "Kapsam ve sıra" tablosuna "CLI iki sürümde çıkar" notu eklenmeli.
  - D-008, D-014 ve D-023 kararlaştırılmış olarak anıldı; "onay bekliyor" ve "Karar noktası" ibareleri kaldırıldı.
- **Teslim biçimi ve geçmiş:**
  - Tam görev listesi `…/scratchpad/plan/plan-cli-tasks.md`'de (sha256 `d5bae91c…73485b`). Satır içinde verilmedi: ölçülen maliyetle tek çağrının çıktı sınırını aşıyor.
  - Scratch geçmişi yeniden kuruldu. Her düzeltme sahibi göreve katlandı ve her değişen görevin kırmızısı o görevin yerinde yeniden ölçüldü. Eski zincir `draft-cli-8714d30` etiketinde duruyor.

---

## Görevler

> **Plan notu.** Komutlar depo kökünden koşar. Görev sırası commit sırasıdır ve iki dalgaya bölünür: **Dalga 1 (T001–T018, T020–T022)** ad güvenliği açığını kapatır ve bugün yayımlanır; **Dalga 2 (T019, T023–T026)** plugin 2.4.0'ın modelini anlatır ve plugin yayımlanmadan `main`'e girmez (Global Constraints → "Yayın sırası"). "B16" = NFR-001'in başlangıçta kırık 16 testi (Global Constraints). Her görev scratch'te gerçekten koşturuldu: taban `20e5d7e`, görev başına bir commit — T001 `e6b2fbf`, T002 `4fe2cbd`, T003 `ed27c1b`, T004 `471dab9`, T005 `cb4e7fd`, T006 `f7034ef`, T007 `97b03ed`, T008 `28b710d`, T009 `33f7a5e`, T010 `3114bc3`, T011 `e5abf09`, T012 `b01b132`, T013 `21b9909`, T014 `947259c`, T015 `3c1b8bc`, T016 `33356ac`, T017 `1fd32dc`, T018 `e6f8ac6`, T019 `2762a26`, T020 `dcb94ef`, T021 `e336183`, T022 `ed1b247`, T023 `57af683`, T024 `b5fb84e`, T025 `fe59d86`, T026 `5a8310b`. Taslak zinciri `draft-cli-8714d30` etiketinde duruyor.

## Dalga 1 — CLI sürümü önce (T001–T018, T020–T022)

### T001: Tek dizin olmayan ortam adı link'te yazılmaz (ad kapısı)
<!-- deps: [] | files: [internal/envname/envname.go, internal/envname/envname_test.go, internal/backend/link_names.go, internal/backend/link_names_test.go, internal/backend/project_link.go] | satisfies: [FR-001] -->

**Interfaces:**
- Consumes: —
- Produces: yeni yaprak paket `internal/envname` → `func CheckDir(name string) error` (gevşek kural, D-011; hata metni "the name …" cümlesini tamamlar: `is empty`, `is "." or ".."`, `contains a path separator`, `starts with a dot`, `is not UTF-8`, `contains the non-printing character U+XXXX`, `ends with a dot or a space, which Windows drops`, `contains a character Windows does not allow in a directory name (<>:"|?*)`, `is a device name on Windows`) · `internal/backend/link_names.go` → `func linkableEnvironments(envs []Environment, linkedEnv string, w io.Writer) ([]Environment, error)` · test yardımcısı `entriesIn(t *testing.T, dir string) []string` (`link_names_test.go`)

Kapı listeyi ayrıştıran iki yerde (`project_link.go` `listCLIProjects`, `cmd/palbase/main.go` `EnvironmentsOf`) DEĞİL, ikisinin de aktığı tek yerde — `runLinkPrepared`'ın başında, `o.environments` üzerinde — duruyor: liste `--env`/`env use` çözümünde veri olarak kalır (orada ad yol olmaz), yalnız dizine dönüşmeden önce süzülür. Paket yaprak, çünkü T008'de `internal/project` de aynı kuralı kullanacak ve `backend`'i içeri çekmemeli.

**Windows da bir takım arkadaşının diski** (`.goreleaser.yml` `goos: windows`): orada `main.` ve `main ` `main` dizinidir (bir üyenin ortamı main'in config'inin üstüne yazılır — FR-003'ün tehdidi başka yoldan), `CON`/`nul.json` bir aygıttır, `<>:"|?*` adda olamaz. Kural bu yüzden her işletim sisteminde aynı: bir adın her takım arkadaşının diskinde TEK dizin olması gerekir. UTF-8 olmayan bir ad da reddedilir — APFS onu yaratmaz (`illegal byte sequence`) ve kapısız hâli link'in tamamını düşürüyordu (Adım 2). D-011'in yasal tuttuğu adlar (`Production`, `Üretim ortamı`, `v1.2`, `console`, `nullable`) değişmez. Aygıt listesi Go'nun Windows'ta `filepath.IsLocal`'ın reddettiği liste.

- [ ] **Adım 1: Kırmızı testleri yaz** — yeni dosya `internal/envname/envname_test.go`:
```go
package envname

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// THE NAMES PEOPLE ALREADY USE STAY LEGAL (D-011). This gate is the security
// boundary, not a style rule: `Production`, `featureX` and a name with a space
// in it are each one directory, and they must keep linking until the control
// plane stores slugs.
func TestCheckDirAcceptsEveryNameThatIsOneDirectory(t *testing.T) {
	for _, name := range []string{"main", "local", "Production", "featureX", "feature-profile-update", "Üretim ortamı", "v1.2"} {
		require.NoError(t, CheckDir(name), "%q", name)
	}
}

// EVERY NAME THAT IS NOT EXACTLY ONE DIRECTORY IS REFUSED, and the reason
// completes the sentence "the name …" that the link prints beside it.
func TestCheckDirRefusesEveryNameThatIsNotOneDirectory(t *testing.T) {
	for name, why := range map[string]string{
		"":                       "is empty",
		".":                      `is "." or ".."`,
		"..":                     `is "." or ".."`,
		"../../gradle":           "contains a path separator",
		"feature/login":          "contains a path separator",
		`..\..\gradle`:           "contains a path separator",
		".hidden":                "starts with a dot",
		"a\x00b":                 "contains the non-printing character U+0000",
		"evil\x1b]0;owned\a":     "contains the non-printing character U+001B",
		"tab\there":              "contains the non-printing character U+0009",
		"del\x7f":                "contains the non-printing character U+007F",
		"csi\xc2\x9b31m":         "contains the non-printing character U+009B",
		"rtl\xe2\x80\xaeexe.txt": "contains the non-printing character U+202E",
		"csi\x9b31m":             "is not UTF-8",
	} {
		err := CheckDir(name)
		require.Error(t, err, "%q was accepted", name)
		require.Equal(t, why, err.Error(), "%q", name)
	}
}

// A NAME WINDOWS WOULD KEEP AS ANOTHER ONE IS NOT ONE DIRECTORY EITHER. This
// CLI ships for Windows (.goreleaser.yml): there `main.` and `main ` are the
// directory `main` — a member's environment written over main's config — and
// CON or NUL is a device, not a directory.
func TestCheckDirRefusesWhatWindowsCannotKeepAsOneDirectory(t *testing.T) {
	for name, why := range map[string]string{
		"main.":     "ends with a dot or a space, which Windows drops",
		"main ":     "ends with a dot or a space, which Windows drops",
		"CON":       "is a device name on Windows",
		"nul.json":  "is a device name on Windows",
		"com1":      "is a device name on Windows",
		"LPT9.log":  "is a device name on Windows",
		"COM\u00b2": "is a device name on Windows",
		"conout$":   "is a device name on Windows",
		"feature:x": `contains a character Windows does not allow in a directory name (<>:"|?*)`,
		"a*b":       `contains a character Windows does not allow in a directory name (<>:"|?*)`,
	} {
		err := CheckDir(name)
		require.Error(t, err, "%q was accepted", name)
		require.Equal(t, why, err.Error(), "%q", name)
	}
	for _, name := range []string{"Production", "Üretim ortamı", "v1.2", "console", "nullable", "com10"} {
		require.NoError(t, CheckDir(name), "%q", name)
	}
}
```
  ve yeni dosya `internal/backend/link_names_test.go` (Android checkout — bu koşunun hedefi; `seedAndroidApp`, `stackServing`, `routeEnvironments`, `runLink`, `linkKeyMain/linkKeyStaging` paketin mevcut yardımcıları):
```go
package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// entriesIn names what sits one level under dir, sorted.
func entriesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A NAME FROM THE LISTING IS NOT A PATH UNTIL IT IS ONE DIRECTORY (FR-001).
//
// Every environment below is served by a stack that answers every read, so
// nothing but the name gate keeps its files off the disk. Measured before the
// gate: `../../gradle` wrote through the stage's symlink into the checkout's
// own gradle/, `..` wrote palbase/openapi.json — the retired layout's marker,
// so every later link refused the checkout — and `../../../outside` wrote next
// to the stage, outside the checkout altogether. `main.` is one directory here
// and `main` on a Windows teammate's disk, and a name that is not UTF-8 is no
// directory APFS will make.
func TestLinkSkipsAnEnvironmentWhoseNameIsNotOneDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	outside := t.TempDir()
	t.Setenv("TMPDIR", outside) // the stage opens here, so "outside" has an address
	require.NoError(t, os.MkdirAll("gradle", 0o755))
	main := stackServing(t, linkKeyMain, nil)
	evil := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{
		"mainref000": main.URL, "evilref001": evil.URL, "evilref002": evil.URL,
		"evilref003": evil.URL, "evilref004": evil.URL, "evilref005": evil.URL,
		"evilref006": evil.URL, "evilref007": evil.URL,
	})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "../../gradle", Ref: "evilref001", Status: "Running"},
			{Name: "..", Ref: "evilref002", Status: "Running"},
			{Name: "../../../outside", Ref: "evilref003", Status: "Running"},
			{Name: "feature/login", Ref: "evilref004", Status: "Running"},
			{Name: "evil\x1b]0;owned\a", Ref: "evilref005", Status: "Running"},
			{Name: "main.", Ref: "evilref006", Status: "Running"},
			{Name: "csi\x9b31m", Ref: "evilref007", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", "android"))
	require.Empty(t, entriesIn(t, "gradle"), "a listed name wrote into the checkout's own gradle/")
	require.NoFileExists(t, filepath.Join(RootDir(), "openapi.json"), "`..` wrote the retired layout's marker")
	require.NoDirExists(t, filepath.Join(outside, "outside"), "a listed name wrote outside the checkout")
	require.Equal(t, []string{"main"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)))
	require.Contains(t, out.String(), `skipped environment "../../gradle" (evilref001): the name contains a path separator, so it cannot be a directory under palbase/environments — rename it in the dashboard`)
	require.Contains(t, out.String(), `skipped environment ".." (evilref002): the name is "." or ".."`)
	require.Contains(t, out.String(), `skipped environment "evil\x1b]0;owned\a" (evilref005): the name contains the non-printing character U+001B`)
	require.Contains(t, out.String(), `skipped environment "main." (evilref006): the name ends with a dot or a space, which Windows drops`)
	require.Contains(t, out.String(), `skipped environment "csi\x9b31m" (evilref007): the name is not UTF-8`)
	require.NotContains(t, out.String(), "\x1b", "a listed name reached the terminal raw")
	require.NotContains(t, out.String(), "\x9b", "a listed name reached the terminal raw")
}

// THE ENVIRONMENT THIS LINK READS FROM CANNOT BE SKIPPED: it is what the app
// builds against when nothing else is chosen. The link refuses, names the fix,
// and writes nothing.
func TestLinkRefusesWhenTheEnvironmentItReadsFromIsNotOneDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	require.NoError(t, os.MkdirAll("gradle", 0o755))
	evil := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"evilref001": evil.URL})
	o := linkOpts{
		url:          evil.URL,
		platforms:    []string{"android"},
		linkedEnv:    "../../gradle",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "../../gradle", Ref: "evilref001", Status: "Running"}},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment "../../gradle" (evilref001) is the one this link reads from, and the name contains a path separator`)
	require.Contains(t, err.Error(), "rename it in the dashboard, or read from another environment with `palbase link --from-env <name>`")
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
	require.Empty(t, entriesIn(t, "gradle"), "the refused link still wrote into gradle/")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/envname/ -count=1` · Beklenen: **FAIL**, `internal/envname/envname_test.go:15:22: undefined: CheckDir`. Run: `go test ./internal/backend/ -run 'TestLinkSkipsAnEnvironmentWhoseNameIsNotOneDirectory|TestLinkRefusesWhenTheEnvironmentItReadsFromIsNotOneDirectory' -count=1` · Beklenen: **FAIL** — `--- FAIL: TestLinkSkipsAnEnvironmentWhoseNameIsNotOneDirectory` / `Error: Received unexpected error:` / `link failed; previous client artifacts were preserved: mkdir palbase/environments/csi�31m: illegal byte sequence` (UTF-8 olmayan tek bir ad bugün link'in tamamını düşürüyor ve hatası baytı ham basıyor) ve `--- FAIL: TestLinkRefusesWhenTheEnvironmentItReadsFromIsNotOneDirectory` / `Error: An error is expected but got nil.` / `Messages: wrote gradle/android-config.json`. (Ölçüldü: `csi\x9b31m` satırı çıkarılıp `require.*` geçici olarak `assert.*` yapıldığında kalan ihlaller de göründü — `Should be empty, but was [android-config.json openapi.json]` / `a listed name wrote into the checkout's own gradle/`, `file "palbase/openapi.json" exists`, `directory ".../outside" exists`, link çıktısında `wrote ../outside/android-config.json` ve ham `\x1b`.)
- [ ] **Adım 3: Uygula** — yeni dosya `internal/envname/envname.go`:
```go
// Package envname is the one rule for what an environment's NAME may be.
//
// THE NAME IS SOMEBODY ELSE'S TEXT. Until the control plane stores slugs, any
// member of an organisation can name an environment with any 1–64 characters,
// and every teammate's `palbase link` turns that name into a directory:
// `palbase/environments/<name>/`. A name that is not one directory is a path
// the CLI was never meant to write — measured: `../../gradle` wrote into the
// checkout's own gradle/, `..` wrote the retired layout's marker so every later
// link refused the checkout, and a longer run of `../` wrote outside the
// checkout altogether.
//
// A LEAF PACKAGE, so every command that reads or prints a name can ask the same
// question without importing the link.
package envname

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CheckDir says why name cannot be ONE directory under palbase/environments,
// or nil when it can.
//
// THIS IS THE LOOSE RULE, AND DELIBERATELY SO (D-011). Names the control plane
// lists today — `Production`, `featureX`, a name with a space — are each one
// directory and keep working; the boundary is the path, not a style. Every
// rune must be printable (unicode.IsPrint), which is wider than the control
// characters alone: a C1 byte or a bidi override in a directory name reaches
// the terminal in every "wrote …" line after it.
//
// ONE DIRECTORY ON EVERY TEAMMATE'S DISK, Windows included — this CLI ships for
// it (.goreleaser.yml). Windows drops a trailing dot or space, so `main.` is
// the directory `main` there and its config lands over main's; `CON` or
// `nul.json` is a device, not a directory; and `<>:"|?*` cannot be in a name at
// all.
//
// The error completes the sentence "the name …".
func CheckDir(name string) error {
	switch {
	case name == "":
		return errors.New("is empty")
	case name == "." || name == "..":
		return errors.New(`is "." or ".."`)
	case strings.ContainsAny(name, `/\`):
		return errors.New("contains a path separator")
	case strings.HasPrefix(name, "."):
		return errors.New("starts with a dot")
	case !utf8.ValidString(name):
		return errors.New("is not UTF-8")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("contains the non-printing character %U", r)
		}
	}
	switch {
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return errors.New("ends with a dot or a space, which Windows drops")
	case strings.ContainsAny(name, windowsForbidden):
		return fmt.Errorf("contains a character Windows does not allow in a directory name (%s)", windowsForbidden)
	case windowsDevice(name):
		return errors.New("is a device name on Windows")
	}
	return nil
}

// windowsForbidden are the characters a Windows directory name cannot hold,
// beside the separators and the control characters refused before them.
const windowsForbidden = `<>:"|?*`

// windowsDevice says whether Windows opens name as a device, not a directory:
// CON, PRN, AUX, NUL, CONIN$, CONOUT$, and COM or LPT with a digit 1–9 or a
// superscript ¹²³ — in any case, and with anything after a dot (`nul.json` is
// NUL). The list is the one Go's own filepath.IsLocal refuses on Windows.
func windowsDevice(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.TrimRight(base, " ")
	for _, device := range []string{"CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$"} {
		if strings.EqualFold(base, device) {
			return true
		}
	}
	if len(base) < 4 || (!strings.EqualFold(base[:3], "COM") && !strings.EqualFold(base[:3], "LPT")) {
		return false
	}
	switch base[3:] {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	}
	return false
}
```
  yeni dosya `internal/backend/link_names.go`:
```go
package backend

// link_names.go — which of the listed environments a link may WRITE.
//
// `palbase/environments/<name>/` is built from the name the control plane
// lists, and the listing is somebody else's text (internal/envname). So the
// listing is judged here, once, before the link asks anybody anything and
// before it writes a byte. Both ways a link learns the listing — a project
// named on the command line (listCLIProjects) and a committed record linked
// again (EnvironmentsOf) — arrive in linkOpts.environments, so this is the one
// place that sees every name on its way to becoming a path. The listing itself
// stays as the cloud sent it: a verb that RESOLVES a name (`--env`, `env use`)
// never makes a directory of it.

import (
	"fmt"
	"io"
	"path"

	"github.com/palgroup/palbase-cli/internal/envname"
)

// linkableEnvironments is the listing without the environments this link must
// not write, with a line for each one it left out.
//
// LEAVING ONE OUT IS SAID, AND THE LINK GOES ON (FR-001). Leaving out the
// environment this link READS FROM cannot be: it is what the app builds against
// when nothing else is chosen, so the link refuses and names the fix.
func linkableEnvironments(envs []Environment, linkedEnv string, w io.Writer) ([]Environment, error) {
	envRoot := path.Dir(EnvDir("any"))
	kept := make([]Environment, 0, len(envs))
	for _, e := range envs {
		err := envname.CheckDir(e.Name)
		if err == nil {
			kept = append(kept, e)
			continue
		}
		reason := fmt.Sprintf("the name %v, so it cannot be a directory under %s", err, envRoot)
		if e.Name == linkedEnv {
			return nil, fmt.Errorf("environment %q (%s) is the one this link reads from, and %s — "+
				"rename it in the dashboard, or read from another environment with `palbase link --from-env <name>`",
				e.Name, e.Ref, reason)
		}
		fmt.Fprintf(w, "skipped environment %q (%s): %s — rename it in the dashboard\n", e.Name, e.Ref, reason)
	}
	return kept, nil
}
```
  `internal/backend/project_link.go` `runLinkPrepared` içinde, platform tespit bloğunun kapanışından (`fmt.Fprintf(w, "▸ %s\n", strings.Join(platforms, ", "))` `}` `}`) SONRA ve `// EVERY NO-TARGET CASE IS RESOLVED BEFORE THIS POINT (D-6).` yorumundan ÖNCE ekle:
```go
	// AND WHICH LISTED NAMES CAN BE DIRECTORIES, also before any network: the
	// name decides where a file lands, and a name that is not one directory
	// wrote outside `palbase/environments` — outside the checkout, measured.
	linkable, err := linkableEnvironments(o.environments, o.linkedEnv, w)
	if err != nil {
		return err
	}
	o.environments = linkable
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/envname/ -count=1 -v` · Beklenen: `--- PASS: TestCheckDirAcceptsEveryNameThatIsOneDirectory`, `--- PASS: TestCheckDirRefusesEveryNameThatIsNotOneDirectory`, `--- PASS: TestCheckDirRefusesWhatWindowsCannotKeepAsOneDirectory`, `ok  	github.com/palgroup/palbase-cli/internal/envname`. Run: `go test ./internal/backend/ -run 'TestLinkSkipsAnEnvironmentWhoseNameIsNotOneDirectory|TestLinkRefusesWhenTheEnvironmentItReadsFromIsNotOneDirectory' -count=1 -v` · Beklenen: `--- PASS: TestLinkSkipsAnEnvironmentWhoseNameIsNotOneDirectory`, `--- PASS: TestLinkRefusesWhenTheEnvironmentItReadsFromIsNotOneDirectory`, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Windows kuralının bekçisi ölçüldü: `CheckDir`'in ikinci `switch`'i (sondaki nokta/boşluk, `<>:"|?*`, aygıt adı) geçici olarak silindiğinde `--- FAIL: TestCheckDirRefusesWhatWindowsCannotKeepAsOneDirectory` / `Messages: "conout$" was accepted` (map sırası; ilk düşen ad değişebilir) ve link testinde `actual  : []string{"main", "main."}`. Gerileme: `go test ./internal/backend/ -run 'Link|Environ|Apple|Web|Spec|Social|OAuth|Oauth' -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/internal/backend	60.886s` (süre ölçülen).
- [ ] **Adım 5: Commit** — `git add internal/envname/envname.go internal/envname/envname_test.go internal/backend/link_names.go internal/backend/link_names_test.go internal/backend/project_link.go && git commit -m "fix(link): tek dizin olmayan ortam adı yazılmaz — checkout dışına yazma kapandı; Windows'un başka ad saydığı ya da UTF-8 olmayan ad da (FR-001)" -- internal/envname/envname.go internal/envname/envname_test.go internal/backend/link_names.go internal/backend/link_names_test.go internal/backend/project_link.go`

---

### T002: Harf büyüklüğü ya da Unicode biçimi ikizleri tek dizine yazılmaz; varsayılan ikizse link durur
<!-- deps: [T001] | files: [internal/envname/envname.go, internal/envname/envname_test.go, internal/backend/link_names.go, internal/backend/link_names_test.go, internal/backend/project_link.go] | satisfies: [FR-003] -->

**Interfaces:**
- Consumes: `linkableEnvironments` (T001), `entriesIn` (T001), `seedGeneratedEnvironment`, `useStub`, `stubSwiftgen`, `linkKeyCanary` (paketin mevcut test yardımcıları)
- Produces: `func envname.SameDirectory(a, b string) bool` — "APFS bu iki adı tek dizin sayar mı" sorusunun TEK cevabı (harf büyüklüğü ve Unicode normalleştirmesi gözetilmeden: `strings.EqualFold(norm.NFC(a), norm.NFC(b))`); T015'in `foldedOnly`'si de bunu sorar · `linkableEnvironments` ikinci kuralı (aynı dizine düşen adlar; hepsi atlanır, varsayılan dahilse ret — D-012) · `runLinkPrepared`'da `listed` (bulutun gönderdiği liste; Apple süpürmesinin `keep`'i artık buradan)

**Unicode biçimi de harf büyüklüğü kadar ikizdir.** APFS normalleştirmeye de duyarsızdır: `café` tek kod noktasıyla (NFC) ve iki kod noktasıyla (NFD) aynı dizindir. Eleştirmen ölçtü, burada yeniden ölçüldü: bu iki adla listelenen iki ortam tek dizine yazıldı (`wrote palbase/environments/café/android-config.json` iki kez) — FR-003'ün arızası başka yoldan. Bu yüzden kural `strings.EqualFold` değil `envname.SameDirectory`; mesaj da "when letter case and Unicode form are ignored" der. `golang.org/x/text` `go.mod`'da zaten doğrudan bağımlılık (v0.41.0).

Süzme, Apple süpürmesine yeni bir yan etki getirir: `keep` `o.environments`'tan kuruluyordu, atlanan ikiz orada olmayınca süpürme onun commit'li dizinini "the project no longer has that environment" diye siliyor. Üçüncü test bunu bağlar; bugün (süzme yokken) YEŞİL, süzme `listed` olmadan uygulandığında KIRMIZI — bu ölçüldü (Adım 2).

- [ ] **Adım 1: Kırmızı testleri yaz** — `internal/backend/link_names_test.go` sonuna ekle:
```go
// TWO NAMES, ONE DIRECTORY (FR-003). On APFS `Staging` and `staging` are the
// same directory, so the second write landed inside the first one's and the
// app built one environment's address under the other's name. Neither is the
// environment this link reads from, so both are left out, each by its ref.
func TestLinkSkipsEnvironmentsWhoseNamesShareOneDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	main := stackServing(t, linkKeyMain, nil)
	upper := stackServing(t, linkKeyStaging, nil)
	lower := stackServing(t, linkKeyCanary, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagAref00": upper.URL, "stagBref00": lower.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "Staging", Ref: "stagAref00", Status: "Running"},
			{Name: "staging", Ref: "stagBref00", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Equal(t, []string{"main"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)))
	require.Contains(t, out.String(), `skipped environments "Staging" (stagAref00) and "staging" (stagBref00): `+
		`their names match when letter case and Unicode form are ignored, so they would share one directory — `+
		`rename one in the dashboard`)
}

// TWO SPELLINGS OF ONE NAME, ONE DIRECTORY. APFS ignores Unicode normalisation
// as it ignores case, so `café` composed and `café` decomposed are the same
// directory: the second write landed in the first one's, and the app built one
// environment's address under the other's name — FR-003's failure by another
// road. Measured: `wrote palbase/environments/café/android-config.json` twice.
func TestLinkSkipsEnvironmentsWhoseNamesDifferOnlyInUnicodeNormalisation(t *testing.T) {
	const composed, decomposed = "caf\u00e9", "cafe\u0301"
	inScratchCheckout(t)
	seedAndroidApp(t)
	main := stackServing(t, linkKeyMain, nil)
	nfc := stackServing(t, linkKeyStaging, nil)
	nfd := stackServing(t, linkKeyCanary, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "nfcref0000": nfc.URL, "nfdref0000": nfd.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: composed, Ref: "nfcref0000", Status: "Running"},
			{Name: decomposed, Ref: "nfdref0000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Equal(t, []string{"main"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)), out.String())
	require.Contains(t, out.String(), "skipped environments \""+composed+"\" (nfcref0000) and \""+decomposed+"\" (nfdref0000): "+
		"their names match when letter case and Unicode form are ignored, so they would share one directory — rename one in the dashboard")
}

// AND WHEN ONE OF THEM IS THE ENVIRONMENT THIS LINK READS FROM, THE LINK STOPS
// (D-012). Picking one silently is how `link` wrote environment B while `push
// --env main` deployed to A — measured with two environments both listed as
// `main`.
func TestLinkRefusesWhenTheEnvironmentItReadsFromSharesItsDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	first := stackServing(t, linkKeyMain, nil)
	second := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"aaaa1111": first.URL, "bbbb2222": second.URL})
	o := linkOpts{
		url:       first.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "aaaa1111", Status: "Running"},
			{Name: "main", Ref: "bbbb2222", Status: "Running"},
		},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environments "main" (aaaa1111) and "main" (bbbb2222) match when letter case and `+
		`Unicode form are ignored, so they would share one directory, and "main" is the one this link reads from — `+
		`rename one in the dashboard`)
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
}

// SKIPPED IS NOT GONE. The Apple sweep deletes the directory of an environment
// the project no longer lists; a twin is still listed, so its committed files
// stay exactly where they are. The sweep is handed the listing as the cloud
// sent it, not what this link could write.
func TestAnAppleLinkKeepsTheFilesOfASkippedTwin(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	root, err := os.Getwd()
	require.NoError(t, err)
	twin := seedGeneratedEnvironment(t, root, "Staging")
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"ios"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "Staging", Ref: "stagAref00", Status: "Running"},
			{Name: "staging", Ref: "stagBref00", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, filepath.Join(twin, "PalbaseGenerated.swift"),
		"the sweep deleted the committed files of an environment the project still lists")
}
```
  ve `internal/envname/envname_test.go` sonuna ekle:
```go
// ONE DIRECTORY ON A MAC IS ONE NAME HERE. APFS ignores letter case and Unicode
// normalisation alike, so `Staging` and `staging` — and `café` composed and
// decomposed — are one directory, and two environments named so wrote into
// one (FR-003).
func TestSameDirectoryIsWhatAMacTakesForOneName(t *testing.T) {
	const composed, decomposed = "caf\u00e9", "cafe\u0301"
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"Staging", "staging", true},
		{composed, decomposed, true},
		{"CAF\u00c9", decomposed, true},
		{"main", "main", true},
		{"main", "main2", false},
		{"feature-x", "featurex", false},
	} {
		require.Equal(t, c.same, SameDirectory(c.a, c.b), "%q and %q", c.a, c.b)
	}
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/envname/ -count=1` · Beklenen: **FAIL**, `internal/envname/envname_test.go:87:28: undefined: SameDirectory`. Run: `go test ./internal/backend/ -run 'TestLinkSkipsEnvironmentsWhoseNamesShareOneDirectory|TestLinkSkipsEnvironmentsWhoseNamesDifferOnlyInUnicodeNormalisation|TestLinkRefusesWhenTheEnvironmentItReadsFromSharesItsDirectory|TestAnAppleLinkKeepsTheFilesOfASkippedTwin' -count=1 -v` · Beklenen: **FAIL** — `--- FAIL: TestLinkSkipsEnvironmentsWhoseNamesShareOneDirectory` / `expected: []string{"main"}` / `actual  : []string{"Staging", "main"}` (APFS'te ikisi tek dizine yazıldı), `--- FAIL: TestLinkSkipsEnvironmentsWhoseNamesDifferOnlyInUnicodeNormalisation` / `actual  : []string{"café", "main"}`, `--- FAIL: TestLinkRefusesWhenTheEnvironmentItReadsFromSharesItsDirectory` / `Error: An error is expected but got nil.`; `--- PASS: TestAnAppleLinkKeepsTheFilesOfASkippedTwin` (bugünkü davranışın bekçisi). Ölçüldü: Adım 3'ün `link_names.go` kısmı `project_link.go` kısmı OLMADAN uygulandığında bekçi düşüyor: `unable to find file ".../palbase/environments/Staging/PalbaseGenerated.swift"` / `Messages: the sweep deleted the committed files of an environment the project still lists`.
- [ ] **Adım 3: Uygula** — `internal/envname/envname.go`: import bloğunun sonuna (`"unicode/utf8"`'ten sonra) boş satır + `"golang.org/x/text/unicode/norm"` ekle ve `// windowsForbidden are …` yorumunun hemen ÜSTÜNE ekle:
```go
// SameDirectory says whether a and b are one directory on a disk that ignores
// letter case and Unicode normalisation — APFS, every Mac's. `Staging` and
// `staging` are, and so are `café` written with one code point and with two:
// two environments named so wrote into one directory, and the app built one
// environment's address under the other's name (FR-003).
func SameDirectory(a, b string) bool {
	return strings.EqualFold(norm.NFC.String(a), norm.NFC.String(b))
}
```
  `internal/backend/link_names.go`: import bloğuna `"strings"` ekle (`"path"`'ten sonra) ve `linkableEnvironments`'ı (doc yorumu dahil) şununla DEĞİŞTİR:
```go
// linkableEnvironments is the listing without the environments this link must
// not write, with a line for each one it left out.
//
// Two rules, in this order:
//
//   - a name that is not one directory (envname.CheckDir) never becomes a path;
//   - names that match when letter case and Unicode form are ignored
//     (envname.SameDirectory) are ONE directory on APFS, so the second write
//     landed in the first one's directory and the app built one environment's
//     address under the other's name (FR-003). All of them are left out, each
//     named by its ref — choosing one is choosing at random.
//
// LEAVING ONE OUT IS SAID, AND THE LINK GOES ON (FR-001). Leaving out the
// environment this link READS FROM cannot be: it is what the app builds against
// when nothing else is chosen, so the link refuses and names the fix (D-012).
func linkableEnvironments(envs []Environment, linkedEnv string, w io.Writer) ([]Environment, error) {
	envRoot := path.Dir(EnvDir("any"))
	named := make([]Environment, 0, len(envs))
	for _, e := range envs {
		err := envname.CheckDir(e.Name)
		if err == nil {
			named = append(named, e)
			continue
		}
		reason := fmt.Sprintf("the name %v, so it cannot be a directory under %s", err, envRoot)
		if e.Name == linkedEnv {
			return nil, fmt.Errorf("environment %q (%s) is the one this link reads from, and %s — "+
				"rename it in the dashboard, or read from another environment with `palbase link --from-env <name>`",
				e.Name, e.Ref, reason)
		}
		fmt.Fprintf(w, "skipped environment %q (%s): %s — rename it in the dashboard\n", e.Name, e.Ref, reason)
	}

	// One group per directory, in the listing's order.
	var groups [][]Environment
	for _, e := range named {
		placed := false
		for i := range groups {
			if envname.SameDirectory(groups[i][0].Name, e.Name) {
				groups[i] = append(groups[i], e)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []Environment{e})
		}
	}
	kept := make([]Environment, 0, len(named))
	for _, group := range groups {
		if len(group) == 1 {
			kept = append(kept, group[0])
			continue
		}
		labels := make([]string, 0, len(group))
		readsFromIt := false
		for _, e := range group {
			labels = append(labels, fmt.Sprintf("%q (%s)", e.Name, e.Ref))
			readsFromIt = readsFromIt || e.Name == linkedEnv
		}
		all := strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
		if readsFromIt {
			return nil, fmt.Errorf("environments %s match when letter case and Unicode form are ignored, so they would "+
				"share one directory, and %q is the one this link reads from — rename one in the dashboard", all, linkedEnv)
		}
		fmt.Fprintf(w, "skipped environments %s: their names match when letter case and Unicode form are ignored, "+
			"so they would share one directory — rename one in the dashboard\n", all)
	}
	return kept, nil
}
```
  `internal/backend/project_link.go` — T001'in eklediği bloğu:
```go
	// AND WHICH LISTED NAMES CAN BE DIRECTORIES, also before any network: the
	// name decides where a file lands, and a name that is not one directory
	// wrote outside `palbase/environments` — outside the checkout, measured.
	linkable, err := linkableEnvironments(o.environments, o.linkedEnv, w)
```
  şununla değiştir:
```go
	// AND WHICH LISTED NAMES CAN BE DIRECTORIES, also before any network: the
	// name decides where a file lands, and a name that is not one directory
	// wrote outside `palbase/environments` — outside the checkout, measured.
	// `listed` keeps the listing as the cloud sent it: an environment left out
	// here is not written, and it is not GONE either — the sweep below must not
	// take its committed files.
	listed := o.environments
	linkable, err := linkableEnvironments(o.environments, o.linkedEnv, w)
```
  ve `if apple {` altındaki
```go
		// KEPT: every environment the project lists, whether or not this run
		// could read it (FR-016) — plus `local` and what was just written (C-10).
		keep := envs.names()
		for _, e := range o.environments {
```
  bloğunu şununla değiştir:
```go
		// KEPT: every environment the project lists, whether or not this run
		// could read it (FR-016) or was allowed to write it (FR-003) — plus
		// `local` and what was just written (C-10).
		keep := envs.names()
		for _, e := range listed {
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/envname/ -count=1 -v` · Beklenen: `--- PASS: TestSameDirectoryIsWhatAMacTakesForOneName`, `ok  	github.com/palgroup/palbase-cli/internal/envname`. Run: `go test ./internal/backend/ -run 'TestLinkSkips|TestLinkRefusesWhenTheEnvironmentItReadsFrom|TestAnAppleLinkKeepsTheFilesOfASkippedTwin' -count=1 -v` · Beklenen: `--- PASS: TestLinkSkipsEnvironmentsWhoseNamesShareOneDirectory`, `--- PASS: TestLinkSkipsEnvironmentsWhoseNamesDifferOnlyInUnicodeNormalisation`, `--- PASS: TestLinkRefusesWhenTheEnvironmentItReadsFromSharesItsDirectory`, `--- PASS: TestAnAppleLinkKeepsTheFilesOfASkippedTwin`, T001'in iki testi PASS, `ok  	github.com/palgroup/palbase-cli/internal/backend`.
- [ ] **Adım 5: Commit** — `git add internal/envname/envname.go internal/envname/envname_test.go internal/backend/link_names.go internal/backend/link_names_test.go internal/backend/project_link.go && git commit -m "fix(link): harf büyüklüğü ya da Unicode biçimi ikizi ortamlar tek dizine yazılmaz; varsayılan ikizse link durur (FR-003)" -- internal/envname/envname.go internal/envname/envname_test.go internal/backend/link_names.go internal/backend/link_names_test.go internal/backend/project_link.go`

---

### T003: `local` adlı bulut ortamı yazılmaz — `local/` bu makinenin yığınına ait
<!-- deps: [T002] | files: [internal/backend/link_names.go, internal/backend/link_names_test.go] | satisfies: [FR-004] -->

**Interfaces:**
- Consumes: `linkableEnvironments` (T002 hâli), `localEnvName` (`app_environments.go`)
- Produces: `linkableEnvironments`'ın `local` kuralı (harf büyüklüğü gözetmeksizin)

- [ ] **Adım 1: Kırmızı testleri yaz** — `internal/backend/link_names_test.go` sonuna ekle:
```go
// `local/` BELONGS TO THIS MACHINE (FR-004). A cloud environment named `Local`
// used to be written there — and silently replaced by this machine's stack
// whenever one was registered, or left beside the stack's config when it was
// not. It is left out and said, whatever its case.
func TestLinkSkipsACloudEnvironmentNamedLocal(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	main := stackServing(t, linkKeyMain, nil)
	cloud := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "locref0000": cloud.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "Local", Ref: "locref0000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Equal(t, []string{"main"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)))
	require.Contains(t, out.String(), `skipped environment "Local" (locref0000): palbase/environments/local belongs to `+
		"the stack `palbase start` runs on this machine, never to a cloud environment — rename it in the dashboard")
}

func TestLinkRefusesWhenTheEnvironmentItReadsFromIsNamedLocal(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	cloud := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"locref0000": cloud.URL})
	o := linkOpts{
		url:          cloud.URL,
		platforms:    []string{"android"},
		linkedEnv:    "local",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "local", Ref: "locref0000", Status: "Running"}},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment "local" (locref0000) is the one this link reads from, and palbase/environments/local belongs to `+
		"the stack `palbase start` runs on this machine")
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestLinkSkipsACloudEnvironmentNamedLocal|TestLinkRefusesWhenTheEnvironmentItReadsFromIsNamedLocal' -count=1 -v` · Beklenen: **FAIL** — `expected: []string{"main"}` / `actual  : []string{"Local", "main"}` ve `Error: An error is expected but got nil.`
- [ ] **Adım 3: Uygula** — `internal/backend/link_names.go`, `linkableEnvironments`'ın ilk döngüsündeki
```go
		err := envname.CheckDir(e.Name)
		if err == nil {
			named = append(named, e)
			continue
		}
		reason := fmt.Sprintf("the name %v, so it cannot be a directory under %s", err, envRoot)
```
  bloğunu şununla değiştir:
```go
		var reason string
		switch err := envname.CheckDir(e.Name); {
		case err != nil:
			reason = fmt.Sprintf("the name %v, so it cannot be a directory under %s", err, envRoot)
		case strings.EqualFold(e.Name, localEnvName):
			reason = fmt.Sprintf("%s belongs to the stack `palbase start` runs on this machine, never to a cloud environment",
				path.Join(envRoot, localEnvName))
		default:
			named = append(named, e)
			continue
		}
```
  ve doc yorumunda `// Two rules, in this order:` ile `//   - a name that is not one directory (envname.CheckDir) never becomes a path;` satırlarını şununla değiştir:
```go
// Three rules, in this order:
//
//   - a name that is not one directory (envname.CheckDir) never becomes a path;
//   - `local`, in any case, is the directory of the stack on THIS machine
//     (FR-004): a cloud environment written there was silently replaced by
//     that stack, or left beside its config when none was registered;
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/backend/ -run 'TestLinkSkips|TestLinkRefuses|TestAnAppleLinkKeepsTheFilesOfASkippedTwin' -count=1 -v` · Beklenen: `--- PASS: TestLinkSkipsACloudEnvironmentNamedLocal`, `--- PASS: TestLinkRefusesWhenTheEnvironmentItReadsFromIsNamedLocal`, önceki `TestLinkRefuses*` testleri (mevcut `TestLinkRefusesAFromEnvThatIsUnavailable` vb. dahil) PASS, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Mevcut testlerde bulut ortamı `local` adıyla listelenen yok (`grep -rn 'Name: "local"' --include='*_test.go'` boş).
- [ ] **Adım 5: Commit** — `git add internal/backend/link_names.go internal/backend/link_names_test.go && git commit -m "fix(link): local adlı bulut ortamı yazılmaz — local/ bu makinenin yığınına ait (FR-004)" -- internal/backend/link_names.go internal/backend/link_names_test.go`

---

### T004: Yazılamayan bir ortam link'i düşürmez; varsayılan yazılamazsa düşürür
<!-- deps: [T003] | files: [internal/backend/app_environments.go, internal/backend/project_link.go, internal/backend/link_names_test.go] | satisfies: [FR-005] -->

**Interfaces:**
- Consumes: `writeEnvironmentConfigs(platforms []string, envs appEnvironments) ([]string, error)` (imzası KORUNUR), `writeSpec`
- Produces: `type unwrittenEnvironments map[string]error` (`names() []string`, `Error() string`) — `writeEnvironmentConfigs` artık ilk hatada dönmez, diğerlerini yazar ve yazamadıklarını bu tiple döner · `func writeEnvironmentConfig(dest string, fields appEnvironment) error` · test yardımcısı `blockEnvironmentDir(t *testing.T, env string)`

İmza korunduğu için `writeEnvironmentConfigs`'ı doğrudan çağıran iki mevcut test (`TestLinkConfigDoesNotKeepPreviousAuth`, `TestWebConfigCarriesCurrentSealingRoot`) değişmeden yeşil kalır; nil olmayan bir hata hâlâ "her şey yazılmadı" demektir.

- [ ] **Adım 1: Kırmızı testleri yaz** — `internal/backend/link_names_test.go` sonuna ekle:
```go
// blockEnvironmentDir puts a FILE where an environment's directory goes, so
// nothing can be written for it — the way an unwritable name used to reach the
// disk (`../../settings.gradle.kts` resolved to an existing file).
func blockEnvironmentDir(t *testing.T, env string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(RootDir(), envSubdir), 0o755))
	require.NoError(t, os.WriteFile(filepath.FromSlash(EnvDir(env)), []byte("in the way\n"), 0o644))
}

// ONE ENVIRONMENT'S DISK DOES NOT DECIDE THE OTHERS' (FR-005). A write that
// failed for an environment this link does not read from failed the whole
// link — so one directory nobody could create stopped every teammate's link.
func TestLinkGoesOnWhenAnotherEnvironmentCannotBeWritten(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	blockEnvironmentDir(t, "staging")
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", "android"))
	require.FileExists(t, SpecPath("main"))
	require.Contains(t, out.String(), "staging could not be written (mkdir palbase/environments/staging: not a directory) — "+
		"skipped; run `palbase link` again once it can be")
	blocker, err := os.ReadFile(filepath.FromSlash(EnvDir("staging")))
	require.NoError(t, err)
	require.Equal(t, "in the way\n", string(blocker), "the link wrote over what stood in staging's way")
}

// THE ENVIRONMENT THIS LINK READS FROM STILL FAILS IT: an app whose default has
// no config does not build.
func TestLinkFailsWhenTheEnvironmentItReadsFromCannotBeWritten(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	blockEnvironmentDir(t, "main")
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), "main could not be written: mkdir palbase/environments/main: not a directory")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestLinkGoesOnWhenAnotherEnvironmentCannotBeWritten|TestLinkFailsWhenTheEnvironmentItReadsFromCannotBeWritten' -count=1 -v` · Beklenen: **FAIL** — `Received unexpected error:` / `link failed; previous client artifacts were preserved: mkdir palbase/environments/staging: not a directory` ve `"link failed; previous client artifacts were preserved: mkdir palbase/environments/main: not a directory" does not contain "main could not be written: mkdir palbase/environments/main: not a directory"`.
- [ ] **Adım 3: Uygula** — `internal/backend/app_environments.go`: `writeEnvironmentConfigs`'ın gövdesini (doc yorumunun son satırı `// both platforms write the same flat shape.`'ten fonksiyonun kapanışına kadar) şununla değiştir:
```go
// both platforms write the same flat shape.
//
// ONE ENVIRONMENT'S DISK DOES NOT DECIDE THE OTHERS' (FR-005). An environment
// that cannot be written stops only itself: every other one is written, and
// the error that comes back is an unwrittenEnvironments naming each one that
// was not. It used to return at the first failure, so one directory nobody
// could create failed every teammate's link.
func writeEnvironmentConfigs(platforms []string, envs appEnvironments) ([]string, error) {
	var written []string
	unwritten := unwrittenEnvironments{}
	for _, env := range envs.names() {
		fields := envs.Environments[env]
		for _, platform := range platforms {
			dest := ConfigPath(env, platform)
			if err := writeEnvironmentConfig(dest, fields); err != nil {
				unwritten[env] = err
				break
			}
			written = append(written, dest)
		}
	}
	if len(unwritten) > 0 {
		return written, unwritten
	}
	return written, nil
}

func writeEnvironmentConfig(dest string, fields appEnvironment) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(mergeConfigWithExisting(dest, fields), "", "  ")
	if err != nil {
		return err
	}
	// 0o600: it carries this environment\'s publishable key. Not a
	// secret, but not something to widen either.
	return os.WriteFile(dest, append(blob, '\n'), 0o600)
}

// unwrittenEnvironments is why each environment it names could not be written.
type unwrittenEnvironments map[string]error

func (u unwrittenEnvironments) names() []string {
	out := make([]string, 0, len(u))
	for name := range u {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (u unwrittenEnvironments) Error() string {
	parts := make([]string, 0, len(u))
	for _, name := range u.names() {
		parts = append(parts, fmt.Sprintf("%s: %v", name, u[name]))
	}
	return strings.Join(parts, "; ")
}
```
  `internal/backend/project_link.go` `runLinkPrepared` içindeki
```go
	apple := false
	web := false
	for _, c := range configs {
		paths, err := writeEnvironmentConfigs([]string{c.platform}, c.envs)
		if err != nil {
			return err
		}
```
  başlangıcından `// The contracts and role documents …` döngüsünün sonuna (`if err := writeSpec(name, spec); err != nil { return err }` `}` `}`) kadar olan bloğu şununla değiştir:
```go
	// AN ENVIRONMENT THAT CANNOT BE WRITTEN IS SAID AND LEFT OUT (FR-005) —
	// unless it is the one this link reads from, which is what the app builds
	// against when nothing else is chosen. Once one write for it has failed,
	// nothing further is written for it: no other platform's config, no
	// contract.
	unwritten := unwrittenEnvironments{}
	unwritable := func(name string, err error) error {
		if name == envs.Default {
			return fmt.Errorf("%s could not be written: %w", name, err)
		}
		unwritten[name] = err
		return nil
	}
	apple := false
	web := false
	for _, c := range configs {
		for name := range unwritten {
			delete(c.envs.Environments, name)
		}
		paths, err := writeEnvironmentConfigs([]string{c.platform}, c.envs)
		var failed unwrittenEnvironments
		switch {
		case errors.As(err, &failed):
			for _, name := range failed.names() {
				if err := unwritable(name, failed[name]); err != nil {
					return err
				}
			}
		case err != nil:
			return err
		}
		if isApplePlatform(c.platform) {
			apple = true
		}
		if c.platform == webPlatform {
			web = true
		}
		for _, p := range paths {
			fmt.Fprintf(w, "wrote %s\n", p)
		}
	}
	// The contracts and role documents of the environments that survived, and
	// only where a generator reads them (FR-019).
	if writesPerEnvironmentArtifacts(platforms) {
		for _, name := range envs.names() {
			spec, ok := specs[name]
			if _, failed := unwritten[name]; !ok || failed {
				continue
			}
			if err := writeSpec(name, spec); err != nil {
				if err := unwritable(name, err); err != nil {
					return err
				}
			}
		}
	}
	for _, name := range unwritten.names() {
		fmt.Fprintf(w, "%s could not be written (%v) — skipped; run `palbase link` again once it can be\n", name, unwritten[name])
		delete(envs.Environments, name)
		delete(specs, name)
	}
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/backend/ -run 'TestLinkGoesOnWhenAnotherEnvironmentCannotBeWritten|TestLinkFailsWhenTheEnvironmentItReadsFromCannotBeWritten|TestLinkConfigDoesNotKeepPreviousAuth|TestWebConfigCarriesCurrentSealingRoot' -count=1 -v` · Beklenen: dördü de `--- PASS`, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Gerileme: `go test ./internal/backend/ -run 'Link|Environ|Apple|Web|Spec|Social|OAuth|Oauth|Config|Sweep|Stage|Artifact' -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/internal/backend	72.280s`.
- [ ] **Adım 5: Commit** — `git add internal/backend/app_environments.go internal/backend/project_link.go internal/backend/link_names_test.go && git commit -m "fix(link): yazılamayan bir ortam link'i düşürmez; varsayılan yazılamazsa düşürür (FR-005)" -- internal/backend/app_environments.go internal/backend/project_link.go internal/backend/link_names_test.go`

---

### T005: `palbase spec` ve push yenilemesi link'in ad kapısından geçer
<!-- deps: [T003] | files: [internal/backend/link_names.go, internal/backend/stack_spec.go, internal/backend/layout.go, internal/backend/spec_names_test.go] | satisfies: [FR-002] -->

**Interfaces:**
- Consumes: `linkableEnvironments` (T003 hâli), `resolverRig`, `linkedTo`, `routeEnvironments`, `stackServing`, `seedWebCheckout`, `entriesIn` (T001)
- Produces: `func whyNotWritable(name string, thisMachine bool) string` (`link_names.go`) — link ile spec'in TEK "bu ad burada dizin olabilir mi" cevabı; `thisMachine` yalnız hedef bu makinenin yığınıyken true (o zaman `local` serbest)

`refreshSpec` stage'de değil gerçek checkout'ta yazar; kapı `Credential`/`fetchStackSpec`'ten ÖNCE, ağa çıkmadan koşar. Push yolunda hata `finishStackPush` tarafından `the push landed, but the client could not be regenerated: …` olarak sarılır (`push_result.go:24-25`) — push'un kendisi geri alınmaz.

- [ ] **Adım 1: Kırmızı testleri yaz** — yeni dosya `internal/backend/spec_names_test.go`:
```go
package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// specRigFor links a scratch checkout to a project whose environments are
// served by one stack that answers every read, and selects `named` with
// --env — so nothing but the name gate keeps its contract off the disk.
func specRigFor(t *testing.T, envs []Environment, named string) {
	t.Helper()
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, envs)
	stack := stackServing(t, linkKeyMain, nil)
	byRef := map[string]string{}
	for _, e := range envs {
		byRef[e.Ref] = stack.URL
	}
	routeEnvironments(t, byRef)
	SelectedEnvFlag = named
}

// `palbase spec` WRITES IN THE REAL CHECKOUT, NOT A STAGE (FR-002), so a name
// that is not one directory went straight where it pointed: measured, `--env`
// on an environment named `../../gradle` wrote gradle/openapi.json.
func TestSpecRefusesAnEnvironmentWhoseNameIsNotOneDirectory(t *testing.T) {
	specRigFor(t, []Environment{
		{Ref: "mainref000", Name: "main", Status: "Running"},
		{Ref: "evilref001", Name: "../../gradle", Status: "Running"},
	}, "../../gradle")
	require.NoError(t, os.MkdirAll("gradle", 0o755))

	var out bytes.Buffer
	err := RefreshSpec(context.Background(), &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment "../../gradle" (evilref001) cannot be written to this checkout: `+
		`the name contains a path separator, so it cannot be a directory under palbase/environments — rename it in the dashboard`)
	require.Empty(t, entriesIn(t, "gradle"), "spec wrote into the checkout's own gradle/")
}

// AND THE PUSH'S REFRESH IS THE SAME ACT: `..` wrote palbase/openapi.json, the
// retired layout's marker, and every later `palbase link` refused the checkout.
func TestPushRefreshRefusesAnEnvironmentWhoseNameIsNotOneDirectory(t *testing.T) {
	specRigFor(t, []Environment{
		{Ref: "mainref000", Name: "main", Status: "Running"},
		{Ref: "evilref002", Name: "..", Status: "Running"},
	}, "..")
	seedWebCheckout(t)

	var out bytes.Buffer
	err := RefreshSpecAfterPush(context.Background(), &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment ".." (evilref002) cannot be written to this checkout: the name is "." or ".."`)
	require.NoFileExists(t, filepath.Join(RootDir(), "openapi.json"))
}

// A CLOUD ENVIRONMENT NAMED `local` DOES NOT GET THIS MACHINE'S DIRECTORY
// (FR-002 → FR-004): `palbase spec --env Local` wrote the cloud contract into
// local/, beside the machine stack's config.
func TestSpecRefusesACloudEnvironmentNamedLocal(t *testing.T) {
	specRigFor(t, []Environment{
		{Ref: "mainref000", Name: "main", Status: "Running"},
		{Ref: "locref0000", Name: "Local", Status: "Running"},
	}, "Local")

	var out bytes.Buffer
	err := RefreshSpec(context.Background(), &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment "Local" (locref0000) cannot be written to this checkout: `+
		"palbase/environments/local belongs to the stack `palbase start` runs on this machine, never to a cloud environment")
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestSpecRefusesAnEnvironmentWhoseNameIsNotOneDirectory|TestPushRefreshRefusesAnEnvironmentWhoseNameIsNotOneDirectory|TestSpecRefusesACloudEnvironmentNamedLocal' -count=1 -v` · Beklenen: **FAIL** ×3, her biri `Error: An error is expected but got nil.` ve yazdığı yeri söyleyen mesajla: `Messages: ✓ wrote gradle/openapi.json (61 bytes)`, `Messages: ✓ wrote palbase/openapi.json (61 bytes)`, `Messages: ✓ wrote palbase/environments/Local/openapi.json (61 bytes)`.
- [ ] **Adım 3: Uygula** — `internal/backend/link_names.go`: `linkableEnvironments`'ın başındaki `envRoot := path.Dir(EnvDir("any"))` satırını sil ve ilk döngüdeki T003 `switch` bloğunu
```go
		var reason string
		switch err := envname.CheckDir(e.Name); {
		case err != nil:
			reason = fmt.Sprintf("the name %v, so it cannot be a directory under %s", err, envRoot)
		case strings.EqualFold(e.Name, localEnvName):
			reason = fmt.Sprintf("%s belongs to the stack `palbase start` runs on this machine, never to a cloud environment",
				path.Join(envRoot, localEnvName))
		default:
			named = append(named, e)
			continue
		}
```
  şununla değiştir:
```go
		reason := whyNotWritable(e.Name, false)
		if reason == "" {
			named = append(named, e)
			continue
		}
```
  ve dosyanın sonuna ekle:
```go
// whyNotWritable says why the environment called name must not get a directory
// in this checkout, or "" when it may. The link and `palbase spec` ask it the
// same way, so the two cannot disagree about which names are paths.
//
// thisMachine is true only when the environment IS the stack on this machine:
// that one, and only that one, is written to `local/`.
func whyNotWritable(name string, thisMachine bool) string {
	envRoot := path.Dir(EnvDir("any"))
	if err := envname.CheckDir(name); err != nil {
		return fmt.Sprintf("the name %v, so it cannot be a directory under %s", err, envRoot)
	}
	if !thisMachine && strings.EqualFold(name, localEnvName) {
		return fmt.Sprintf("%s belongs to the stack `palbase start` runs on this machine, never to a cloud environment",
			path.Join(envRoot, localEnvName))
	}
	return ""
}
```
  `internal/backend/stack_spec.go` `refreshSpec`: `target := resolved.Acting()` satırından `if err := writeSpec(env, spec); err != nil {` satırına kadar olan bloğu şununla değiştir (env hesabı ağdan ÖNCEYE taşınır, kapı araya girer):
```go
	target := resolved.Acting()

	// The environment this checkout is pointed at, by the name the app knows it
	// by — so a refresh updates the contract for THAT configuration and leaves
	// the others alone. Refreshing them all would mean reaching every
	// environment on every push, including production from a laptop.
	env := resolved.ArtifactEnv()
	if target.Local {
		env = localEnvName
	}
	// THE LINK'S GATE, BEFORE ANY NETWORK (FR-002). This writes in the real
	// checkout, not a stage, so a name that is not one directory went straight
	// where it pointed: `../../gradle` wrote gradle/openapi.json, `..` wrote the
	// retired layout's marker and every later link refused the checkout.
	if why := whyNotWritable(env, target.Local); why != "" {
		return fmt.Errorf("environment %q (%s) cannot be written to this checkout: %s — rename it in the dashboard",
			env, resolved.Ref, why)
	}

	// The resolver's refusal already names both ways in and which address it
	// looked for. Flattening it into the sentinel replaced all of that with four
	// words and left the person to guess.
	cred, _, err := Credential(target.URL)
	if err != nil {
		return err
	}

	spec, err := fetchStackSpec(ctx, target, cred)
	if err != nil {
		return err
	}
	if err := writeSpec(env, spec); err != nil {
```
  `internal/backend/layout.go`: `// EnvDir is where ONE environment's committed artifacts live.` satırından sonra, `func EnvDir` satırından önce ekle:
```go
//
// IT TRUSTS ITS ARGUMENT. A name the control plane listed reaches it only
// through whyNotWritable (link_names.go) — the link and `palbase spec` both
// ask it first — because path.Join resolves `..` and a listed name is
// somebody else's text.
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/backend/ -run 'TestSpecRefuses|TestPushRefreshRefuses|TestLinkSkips|TestLinkRefuses|TestLinkGoesOn|TestLinkFails|TestAnAppleLinkKeepsTheFilesOfASkippedTwin' -count=1 -v` · Beklenen: yeni üç test ve T001–T004'ün testleri `--- PASS`, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Gerileme: `go test ./internal/backend/ -run 'Spec|Push|Resolve|Status' -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/internal/backend	8.528s`.
- [ ] **Adım 5: Commit** — `git add internal/backend/link_names.go internal/backend/stack_spec.go internal/backend/layout.go internal/backend/spec_names_test.go && git commit -m "fix(spec): spec ve push yenilemesi link'in ad kapısından geçer (FR-002)" -- internal/backend/link_names.go internal/backend/stack_spec.go internal/backend/layout.go internal/backend/spec_names_test.go`

---

### T006: `palbase env create` adı D-008 dilbilgisine uymuyorsa ya da ayrılmışsa istek göndermeden reddeder
<!-- deps: [T001] | files: [internal/envname/envname.go, internal/env/env.go, internal/env/env_test.go] | satisfies: [FR-007] -->

**Interfaces:**
- Consumes: `internal/envname` (T001), `internal/env` test yardımcıları `linkedCheckout`, `twoEnvironments`, `stubREST`, `run`
- Produces: `envname.SlugPattern` = `` `^[A-Za-z][A-Za-z0-9-]{0,38}$` `` (D-008'in CLI'daki TEK sabiti; sunucu şeması aynı metni taşır — `plan-cloud.md` `ENVIRONMENT_SLUG_PATTERN`) · `func envname.CheckSlug(name string) error` (dilbilgisi + ayrılmış `local`/`main`, harf büyüklüğü gözetmeksizin)

**D-008 kararlaştırıldı** (kullanıcı onayı 2026-09-26, camelCase). Dilbilgisi yalnız `SlugPattern`'de yazılı — CLI'daki TEK yeri; sunucudaki eşi `ENVIRONMENT_SLUG_PATTERN` (`plan-cloud.md`) ve ikisi aynı metni taşır. Kural D-011 gereği yalnız `env create`'te: link/spec gevşek `CheckDir`'i kullanmaya devam eder. `TestCreateAcceptsTheNamesTheGrammarIsFor` bugün de yeşil — dilbilgisinin fazla reddetmediğinin bekçisi.

- [ ] **Adım 1: Kırmızı testleri yaz** — `internal/env/env_test.go` import bloğuna `"fmt"` ekle (`"encoding/json"`'dan sonra) ve dosyanın sonuna ekle:
```go
// A NEW NAME FOLLOWS THE SLUG GRAMMAR (FR-007, D-008), and it is judged before
// the CLI asks anybody anything: a name the control plane would store is a
// directory in every teammate's checkout and a build type in their Gradle.
// `Feature X` could not even be confirmed at the prompt — Fscanln stopped at
// the space and the command answered "aborted".
func TestCreateRefusesANameOutsideTheGrammarBeforeAnyRequest(t *testing.T) {
	linkedCheckout(t)
	for _, name := range []string{"Feature X", "feature/login", "..", "2fa", "feature_x", "Üretim", strings.Repeat("a", 40)} {
		rest := &stubREST{projects: twoEnvironments()}
		_, err := run(t, rest, "", "create", name, "--yes")
		require.Error(t, err, "%q was accepted", name)
		require.Equal(t, fmt.Sprintf("%q is not a valid environment name: use a letter, then up to 38 letters, digits or hyphens "+
			"(^[A-Za-z][A-Za-z0-9-]{0,38}$) — featureX or feature-login, for example", name), err.Error())
		require.Empty(t, rest.calls, "%q reached the control plane", name)
	}
}

// `local` AND `main` ARE NOT NAMES A PERSON GIVES (D-008), in any case: the
// control plane keeps names unique regardless of case, so `Main` is `main`.
func TestCreateRefusesAReservedNameInAnyCase(t *testing.T) {
	linkedCheckout(t)
	for name, why := range map[string]string{
		"local": "it names the stack `palbase start` runs on this machine",
		"LOCAL": "it names the stack `palbase start` runs on this machine",
		"main":  "it names a project's first environment",
		"Main":  "it names a project's first environment",
	} {
		rest := &stubREST{projects: twoEnvironments()}
		_, err := run(t, rest, "", "create", name, "--yes")
		require.Error(t, err, "%q was accepted", name)
		require.Equal(t, fmt.Sprintf("%q is reserved: %s", name, why), err.Error())
		require.Empty(t, rest.calls, "%q reached the control plane", name)
	}
}

// AND THE NAMES THE GRAMMAR IS FOR GO THROUGH UNCHANGED — camelCase is the
// point: `featureX` is `create("featureX")` in Gradle with no mapping.
func TestCreateAcceptsTheNamesTheGrammarIsFor(t *testing.T) {
	linkedCheckout(t)
	for _, name := range []string{"featureX", "feature-profile-update", "staging2", strings.Repeat("a", 39)} {
		rest := &stubREST{projects: twoEnvironments()}
		_, err := run(t, rest, "", "create", name, "--yes")
		require.NoError(t, err, name)
		sent, _ := rest.lastWrite().body.(map[string]any)
		require.Equal(t, name, sent["name"])
	}
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/env/ -run 'TestCreateRefusesANameOutsideTheGrammarBeforeAnyRequest|TestCreateRefusesAReservedNameInAnyCase|TestCreateAcceptsTheNamesTheGrammarIsFor' -count=1 -v` · Beklenen: **FAIL** — `Error: An error is expected but got nil.` / `Messages: "Feature X" was accepted`; `Error: An error is expected but got nil.` / `Messages: "local" was accepted` (map sırası; ilk düşen ayrılmış ad değişebilir); `--- PASS: TestCreateAcceptsTheNamesTheGrammarIsFor`.
- [ ] **Adım 3: Uygula** — `internal/envname/envname.go`: import bloğuna `"regexp"` ekle (`"fmt"`'ten sonra) ve dosyanın sonuna ekle:
```go
// SlugPattern is the grammar a NEW environment's name must match (D-008): a
// letter, then up to 38 letters, digits or hyphens.
//
// THE SAME TEXT AS THE CONTROL PLANE'S SCHEMA. One of the two changing alone is
// a name the CLI accepts and the server refuses, or the reverse. camelCase is
// allowed on purpose — it is Gradle's own convention for build types, so an
// environment called `featureX` is `create("featureX")` with no mapping — and
// there is no `/`, space or dot, so the name is safe as a directory, in Xcode
// and in AGP alike.
const SlugPattern = `^[A-Za-z][A-Za-z0-9-]{0,38}$`

var slug = regexp.MustCompile(SlugPattern)

// reserved are the names no person gives an environment, and why. Compared
// regardless of case: the control plane keeps names unique that way, so `Main`
// is `main`. `main` is reserved here because `palbase env create` never makes a
// project's FIRST environment — that one is `main` by the control plane's
// hand.
var reserved = map[string]string{
	"local": "it names the stack `palbase start` runs on this machine",
	"main":  "it names a project's first environment",
}

// CheckSlug says why name cannot be a NEW environment's name, or nil. It is the
// strict rule, for the one verb that makes a name (D-011); names the cloud
// already lists answer to CheckDir.
func CheckSlug(name string) error {
	if !slug.MatchString(name) {
		return fmt.Errorf("%q is not a valid environment name: use a letter, then up to 38 letters, digits or hyphens (%s) — "+
			"featureX or feature-login, for example", name, SlugPattern)
	}
	if why, ok := reserved[strings.ToLower(name)]; ok {
		return fmt.Errorf("%q is reserved: %s", name, why)
	}
	return nil
}
```
  `internal/env/env.go`: import bloğunda `"github.com/palgroup/palbase-cli/internal/backend"` satırından sonra `"github.com/palgroup/palbase-cli/internal/envname"` ekle; `createCmd`'in `RunE`'sinin başındaki
```go
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := linkedProject(cmd, r)
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			out := cmd.OutOrStdout()
```
  bloğunu şununla değiştir:
```go
		RunE: func(cmd *cobra.Command, args []string) error {
			// THE NAME IS JUDGED BEFORE ANYBODY IS ASKED (FR-007). It becomes a
			// directory in every teammate's checkout and a build type in their
			// Gradle; a name the control plane would take and the checkout could
			// not is found out here, not by the next `palbase link`.
			name := strings.TrimSpace(args[0])
			if err := envname.CheckSlug(name); err != nil {
				return err
			}
			p, err := linkedProject(cmd, r)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/env/ ./internal/envname/ -count=1 -v` · Beklenen: yeni üç test ve mevcut on beş `internal/env` testi (`TestCreateNamesTheCostAndAsksFirst`, `TestCreateProceedsWhenTheNameIsTyped` … — hepsi `staging2` kullanıyor, dilbilgisine uyuyor) `--- PASS`, `ok  	github.com/palgroup/palbase-cli/internal/env`, `ok  	github.com/palgroup/palbase-cli/internal/envname`.
- [ ] **Adım 5: Commit** — `git add internal/envname/envname.go internal/env/env.go internal/env/env_test.go && git commit -m "feat(env): env create adı slug dilbilgisine uymuyorsa ya da ayrılmışsa istek göndermeden reddeder (FR-007)" -- internal/envname/envname.go internal/env/env.go internal/env/env_test.go`

---

### T007: Sunucudan gelen ortam adı terminale ham basılmaz — `internal/backend`
<!-- deps: [T001, T004, T006] | files: [internal/envname/envname.go, internal/envname/envname_test.go, internal/backend/environments.go, internal/backend/project_link.go, internal/backend/app_environments.go, internal/backend/social_link.go, internal/backend/deploy.go, internal/backend/stack_spec.go, internal/backend/name_output_test.go] | satisfies: [FR-006] -->

**Interfaces:**
- Consumes: `internal/envname` (T006 hâli), `resolverRig`, `linkedTo`, `cmdFor`, `PrintResolvedTo`, `linkEnvironmentRef`, `cloneEnvironmentRef`, `newCloneCmd`, `nameREST` (`project_link_name_test.go`), `reportStaleContracts`, `blockEnvironmentDir` (T004)
- Produces: `func envname.Label(name string) string` — düz tek kelime (`^[A-Za-z0-9][A-Za-z0-9_-]*$`; her D-008 slug'ı öyle) olduğu gibi, gerisi `strconv.Quote` (`%q`) ile

**D-023 (karar):** `Label` yalnız düz kelime OLMAYAN adı `%q` ile tırnaklar (git'in `core.quotePath` deyimi): kaçış baytı hiçbir yolda ham basılmaz (FR-006'nın amacı), `main`/`staging`/`Production` bugünkü gibi basılır ve bu metinleri sabitleyen mevcut altı test beklentisi (`"staging could not be read"` ×4, `"contract read from staging"`, `"main is Failed"`) değişmez. Kapıdan geçmiş adlar (`CheckDir` basılamayan her runeyi ve UTF-8 olmayanı reddeder) kaçış taşıyamaz; yine de link satırlarına da uygulanır ki boşluklu bir ad (`Feature X`) cümlede iki şey gibi okunmasın.

**Kapsam — adın basıldığı HER yer.** Link ve çözücü satırlarının yanında: `palbase clone`'un iki banner'ı (`deploy.go`, `▸ %s/%s`) ve iki reddi (`cloneEnvironmentRef`'in "unavailable" hatası ve `withStatus`), `palbase spec`'in "only X was refreshed" satırı (`reportStaleContracts` — adlar diskten okunur, yani eski bir CLI'ın listeden yarattığı dizinler), T004'ün iki "could not be written" satırı ve `platformEnvironments`'ın anahtarsız varsayılan hatası. Eleştirmen `clone` reddini ölçtü: `"todoapp/evil\x1b]0;owned\a is Failed, …" should not contain "\x1b"`; burada her biri kendi testiyle kırmızı görüldü (Adım 2).

- [ ] **Adım 1: Kırmızı testleri yaz** — `internal/envname/envname_test.go` sonuna:
```go
// A NAME PRINTS AS IT IS WHEN IT IS ONE PLAIN WORD — every slug is — and
// quoted and escaped otherwise, so no byte a member typed into a name reaches
// a teammate's terminal raw (FR-006).
func TestLabelQuotesEveryNameThatIsNotOnePlainWord(t *testing.T) {
	for name, want := range map[string]string{
		"main":                   "main",
		"Production":             "Production",
		"feature-profile-update": "feature-profile-update",
		"staging_2":              "staging_2",
		"Feature X":              `"Feature X"`,
		"evil\x1b]0;owned\a":     `"evil\x1b]0;owned\a"`,
		"tab\there":              `"tab\there"`,
		"":                       `""`,
	} {
		require.Equal(t, want, Label(name), "%q", name)
	}
}
```
  yeni dosya `internal/backend/name_output_test.go`:
```go
package backend

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/config"
)

// hostileName is an environment name the control plane accepts today: an OSC
// sequence that retitles the terminal, then a bell.
const (
	hostileName   = "evil\x1b]0;owned\a"
	hostileQuoted = `"evil\x1b]0;owned\a"`
)

var withAHostileName = []Environment{
	{Ref: "j06bwtuum", Name: "main", Status: "Running"},
	{Ref: "evilref001", Name: hostileName, Status: "Running"},
}

// THE MENU A REFUSAL HANDS A PERSON IS SOMEBODY ELSE'S TEXT (FR-006). The
// resolver's "none is selected" lists every environment by name, before any
// gate has looked at them.
func TestTheResolversMenuPrintsANameEscaped(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, withAHostileName)

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, err.Error(), "  "+hostileQuoted+"   evilref001")
}

// AND THE BANNER EVERY VERB PRINTS BEFORE IT ACTS.
func TestTheBannerPrintsANameEscaped(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, withAHostileName)
	SelectedEnvFlag = "evilref001"

	var out strings.Builder
	_, err := PrintResolvedTo(&out, cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "▸ todoapp/"+hostileQuoted+"\n", out.String())
}

// AND THE LINK'S OWN REFUSAL, WHICH RUNS BEFORE THE LINK'S NAME GATE.
func TestLinksRefusalPrintsANameEscaped(t *testing.T) {
	envs := []Environment{
		{Ref: "j06bwtuum", Name: "main", Status: "Running"},
		{Ref: "evilref001", Name: hostileName, Status: "Failed"},
	}

	_, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "evilref001")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, err.Error(), hostileQuoted+" of todoapp is Failed, so nothing can be read from it")
	require.Contains(t, err.Error(), "  "+hostileQuoted+"   evilref001   Failed")
}

// A NAME THAT PASSED THE GATE CAN STILL BE MORE THAN ONE WORD, and a line that
// says `Feature X could not be read` reads as two things. The link's progress
// lines quote it like every other place; a plain name stays as it is.
func TestTheLinksLinesQuoteANameThatIsNotOnePlainWord(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	blockEnvironmentDir(t, "Blocked One")
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "blokref000": main.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "Feature X", Ref: "featref000", Status: "Failed"},
			{Name: "Old Name", Ref: "oldnref000", Status: "Running"},
			{Name: "Blocked One", Ref: "blokref000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Contains(t, out.String(), `"Feature X" is Failed — not asked; its files are left as they are`)
	require.Contains(t, out.String(), `"Old Name" could not be read (no test server for oldnref000) — its files are left as they are`)
	require.Contains(t, out.String(), `"Blocked One" could not be written (mkdir palbase/environments/Blocked One: not a directory) — skipped`)
	require.Contains(t, out.String(), "contract read from main;")
}

// AND `palbase clone`'S REFUSALS, which name the same environments (FR-006).
func TestCloneRefusalsPrintANameEscaped(t *testing.T) {
	envs := []Environment{
		{Ref: "j06bwtuum", Name: "main", Status: "Running"},
		{Ref: "evilref001", Name: hostileName, Status: "Failed"},
	}
	product := Product{ID: "prd_a", Name: "todoapp"}

	_, err := cloneEnvironmentRef(product, envs, "evilref001", "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, err.Error(), "todoapp/"+hostileQuoted+" is Failed, so there is no source to download")

	_, err = cloneEnvironmentRef(product, envs, "evilref001", "main")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, err.Error(), "evilref001 is the ref of todoapp/"+hostileQuoted+" (Failed), but --from-env names main")
}

// AND THE BANNER `palbase clone` PRINTS BEFORE IT DOWNLOADS. There is no
// credential for the address, so the clone stops right after it.
func TestTheCloneBannerPrintsANameEscaped(t *testing.T) {
	inScratchCheckout(t)
	rest := &nameREST{rows: []map[string]any{
		{"id": "prd_a", "name": "todoapp", "environments": []map[string]any{
			{"ref": "evilref001", "name": hostileName, "status": "Running"},
		}},
	}}
	cmd := newCloneCmd(Resolvers{
		REST:      func() REST { return rest },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	})
	var out, announced bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&announced)
	cmd.SetArgs([]string{"evilref001"})

	require.Error(t, cmd.ExecuteContext(context.Background()), "there is no credential for the address")
	require.NotContains(t, announced.String(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, announced.String(), "▸ todoapp/"+hostileQuoted+"\n")
}

// AND THE LINE `palbase spec` PRINTS ABOUT THE OTHER ENVIRONMENTS, whose names
// come off the disk — directories an older CLI made from whatever the listing
// said.
func TestTheStaleContractsLinePrintsANameEscaped(t *testing.T) {
	inScratchCheckout(t)
	var out strings.Builder
	reportStaleContracts("main", appEnvironments{Environments: map[string]appEnvironment{
		"main": {}, hostileName: {},
	}}, &out)
	require.NotContains(t, out.String(), "\x1b", "a name on disk reached the terminal raw")
	require.Contains(t, out.String(), "only main was refreshed. The others still describe what they last served:\n  "+
		hostileQuoted+" (never fetched)\n")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/envname/ -count=1` · Beklenen: **FAIL**, `internal/envname/envname_test.go:105:26: undefined: Label`. Run: `go test ./internal/backend/ -run 'TestTheResolversMenuPrintsANameEscaped|TestTheBannerPrintsANameEscaped|TestLinksRefusalPrintsANameEscaped|TestTheLinksLinesQuoteANameThatIsNotOnePlainWord|TestCloneRefusalsPrintANameEscaped|TestTheCloneBannerPrintsANameEscaped|TestTheStaleContractsLinePrintsANameEscaped' -count=1 -v` · Beklenen: **FAIL** ×7 — `"todoapp has 2 environments and none is selected:\n  evil\x1b]0;owned\a   evilref001\n …" should not contain "\x1b"`; `--- FAIL: TestTheBannerPrintsANameEscaped` / `Error: Not equal:`; `"evil\x1b]0;owned\a of todoapp is Failed, so nothing can be read from it — name another with --from-env.\n …" should not contain "\x1b"`; `"Feature X is Failed — not asked; its files are left as they are\nOld Name could not be read (no test server for oldnref000) — …\nBlocked One could not be written (mkdir palbase/environments/Blocked One: not a directory) — ski…` (tırnaksız); `"todoapp/evil\x1b]0;owned\a is Failed, so there is no source to download — clone a running environment by its ref.\n …" should not contain "\x1b"`; `"▸ todoapp/evil\x1b]0;owned\a\nError: no credential for this project: https://evilref001.palbase.studio. …" should not contain "\x1b"`; `"\nonly main was refreshed. The others still describe what they last served:\n  evil\x1b]0;owned\a (never fetched)\n …" should not contain "\x1b"`.
- [ ] **Adım 3: Uygula** — `internal/envname/envname.go`: import bloğuna `"strconv"` ekle (`"regexp"`'ten sonra) ve dosyanın sonuna:
```go
// Label is how a person reads an environment name the control plane sent.
//
// ONE PLAIN WORD PRINTS AS IT IS — every slug is one — and anything else
// prints quoted and escaped (%q), the way git quotes an unusual path. Printed
// raw, a name carrying escape bytes rewrote a teammate's terminal line, set
// its title or hid the text after it (FR-006); a name with a space read as two
// words in a sentence and as two arguments in a suggested command.
func Label(name string) string {
	if plainWord.MatchString(name) {
		return name
	}
	return strconv.Quote(name)
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
```
  `internal/backend/environments.go`, `internal/backend/project_link.go`, `internal/backend/app_environments.go`, `internal/backend/social_link.go`, `internal/backend/deploy.go`, `internal/backend/stack_spec.go`: her birinin import bloğuna `"github.com/palgroup/palbase-cli/internal/envname"` ekle (`environments.go` ve `deploy.go`'da `"github.com/spf13/cobra"`'dan sonra ayrı bir grup; `stack_spec.go`'da `"strings"`'ten sonra ayrı bir grup; diğer üçünde mevcut `github.com/palgroup/palbase-cli/internal/...` grubunda alfabetik yerine) ve aşağıdaki satırları birebir değiştir (her çiftte üstteki eski, alttaki yeni).
  `environments.go` — `Resolved.Describe` ve `listing`:
```go
		return projectLabel(r.Target) + "/" + r.Env
		return projectLabel(r.Target) + "/" + envname.Label(r.Env)

		if n := len([]rune(e.Name)); n > widest {
		if n := len([]rune(envname.Label(e.Name))); n > widest {

		rows = append(rows, fmt.Sprintf("  %-*s   %s", widest, e.Name, e.Ref))
		rows = append(rows, fmt.Sprintf("  %-*s   %s", widest, envname.Label(e.Name), e.Ref))
```
  ve `listing`'in açıklama yorumunun sonuna (`widest := 0`'dan hemen önce) ekle:
```go
	//
	// Each name is printed as envname.Label has it: this is the control plane's
	// text, and the menu is read before any gate has looked at it.
```
  `project_link.go` — `linkEnvironmentRef`, `listingWithStatus`, `runLinkPrepared`, `reportLinked`:
```go
						e.Name, product.Name, e.Status, listingWithStatus(envs))
						envname.Label(e.Name), product.Name, e.Status, listingWithStatus(envs))

		if n := len([]rune(e.Name)); n > widestName {
		if n := len([]rune(envname.Label(e.Name))); n > widestName {

		rows = append(rows, strings.TrimRight(fmt.Sprintf("  %-*s   %-*s   %s", widestName, e.Name, widestRef, e.Ref, e.Status), " "))
		rows = append(rows, strings.TrimRight(fmt.Sprintf("  %-*s   %-*s   %s", widestName, envname.Label(e.Name), widestRef, e.Ref, e.Status), " "))

				fmt.Fprintf(w, "%s is %s — not asked\n", e.Name, e.Status)
				fmt.Fprintf(w, "%s is %s — not asked\n", envname.Label(e.Name), e.Status)

				envs.Default, envs.Default)
				envname.Label(envs.Default), envname.Label(envs.Default))

	fmt.Fprintf(w, "  contract read from %s; each verb resolves its own environment\n", linkedEnv)
	fmt.Fprintf(w, "  contract read from %s; each verb resolves its own environment\n", envname.Label(linkedEnv))
```
  `app_environments.go` — `reportContractDrift` ve `gatherEnvironments`:
```go
		fmt.Fprintf(w, "\n%s and %s serve different contracts:\n", env, base)
		fmt.Fprintf(w, "\n%s and %s serve different contracts:\n", envname.Label(env), envname.Label(base))

			fmt.Fprintf(w, "  only in %s:  %s\n", env, path)
			fmt.Fprintf(w, "  only in %s:  %s\n", envname.Label(env), path)

			fmt.Fprintf(w, "  not in %s:   %s\n", env, path)
			fmt.Fprintf(w, "  not in %s:   %s\n", envname.Label(env), path)

				fmt.Fprintf(w, "%s is %s — not asked; its files are left as they are\n", e.Name, e.Status)
				fmt.Fprintf(w, "%s is %s — not asked; its files are left as they are\n", envname.Label(e.Name), e.Status)

				fmt.Fprintf(w, "%s could not be read (%v) — its files are left as they are\n", e.Name, err)
				fmt.Fprintf(w, "%s could not be read (%v) — its files are left as they are\n", envname.Label(e.Name), err)

					return appEnvironments{}, nil, fmt.Errorf("%s is %s, so there is nothing to read from it", e.Name, e.Status)
					return appEnvironments{}, nil, fmt.Errorf("%s is %s, so there is nothing to read from it", envname.Label(e.Name), e.Status)

			return appEnvironments{}, nil, fmt.Errorf("%s is not an environment of this project", defaultEnv)
			return appEnvironments{}, nil, fmt.Errorf("%s is not an environment of this project", envname.Label(defaultEnv))

					return appEnvironments{}, nil, fmt.Errorf("%s: %w", name, d.err)
					return appEnvironments{}, nil, fmt.Errorf("%s: %w", envname.Label(name), d.err)

					"run `palbase link` again once it answers\n", name, d.err)
					"run `palbase link` again once it answers\n", envname.Label(name), d.err)

				fmt.Fprintf(w, "%s has no contract to give (%v) — `palbase push --env %s`\n", name, d.noContract, name)
				fmt.Fprintf(w, "%s has no contract to give (%v) — `palbase push --env %s`\n",
					envname.Label(name), d.noContract, envname.Label(name))
```
  `social_link.go` — `droppedEnvironmentLine`:
```go
		return fmt.Sprintf("%s is left as it is: %v\n", name, reason)
		return fmt.Sprintf("%s is left as it is: %v\n", envname.Label(name), reason)

	return fmt.Sprintf("%s could not be read (%v) — its files are left as they are; run `palbase link` again once it answers\n", name, reason)
	return fmt.Sprintf("%s could not be read (%v) — its files are left as they are; run `palbase link` again once it answers\n", envname.Label(name), reason)
```
  `social_link.go` — `platformEnvironments`'ın anahtarsız varsayılan hatası:
```go
			return result, dropped, fmt.Errorf("%s: cannot refresh complete platform config while the environment is unavailable", name)
			return result, dropped, fmt.Errorf("%s: cannot refresh complete platform config while the environment is unavailable",
				envname.Label(name))
```
  `project_link.go` — `runLinkPrepared`'da T004'ün iki "could not be written" satırı:
```go
			return fmt.Errorf("%s could not be written: %w", name, err)
			return fmt.Errorf("%s could not be written: %w", envname.Label(name), err)

		fmt.Fprintf(w, "%s could not be written (%v) — skipped; run `palbase link` again once it can be\n", name, unwritten[name])
		fmt.Fprintf(w, "%s could not be written (%v) — skipped; run `palbase link` again once it can be\n",
			envname.Label(name), unwritten[name])
```
  `deploy.go` — `palbase clone`'un iki banner'ı ve `cloneEnvironmentRef`'in "unavailable" reddi:
```go
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s/%s\n", product.Name, envName)
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s/%s\n", product.Name, envname.Label(envName))

				fmt.Fprintf(cmd.OutOrStdout(), "▸ %s/%s\n", product.Name, envName)
				fmt.Fprintf(cmd.OutOrStdout(), "▸ %s/%s\n", product.Name, envname.Label(envName))

				product.Name, e.Name, e.Status, listingWithStatus(envs))
				product.Name, envname.Label(e.Name), e.Status, listingWithStatus(envs))
```
  ve `withStatus`'u (doc yorumu dahil) şununla değiştir:
```go
// withStatus names an environment, and its status when nothing can be read
// from it: the fact a person needs to decide which half of a contradiction to
// drop. The name as envname.Label has it — it is the control plane's text.
func withStatus(e Environment) string {
	if unavailableEnvironment(e.Status) {
		return envname.Label(e.Name) + " (" + e.Status + ")"
	}
	return envname.Label(e.Name)
}
```
  `stack_spec.go` — `reportStaleContracts`:
```go
		stale = append(stale, fmt.Sprintf("%s (%s)", name, age))
		// A directory an older CLI made from whatever the listing said.
		stale = append(stale, fmt.Sprintf("%s (%s)", envname.Label(name), age))

		refreshed, strings.Join(stale, "\n  "))
		envname.Label(refreshed), strings.Join(stale, "\n  "))
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/envname/ -count=1 -v` · Beklenen: `--- PASS: TestLabelQuotesEveryNameThatIsNotOnePlainWord`, `ok  	github.com/palgroup/palbase-cli/internal/envname`. Run: Adım 2'deki backend komutu · Beklenen: yedisi `--- PASS`, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Gerileme: `go test ./internal/backend/ -run 'Link|Environ|Apple|Web|Spec|Social|OAuth|Oauth|Config|Sweep|Stage|Artifact|Resolve|Banner|Drift|Gather|Selection|Migrat|Clone|Stale' -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/internal/backend	63.663s` (düz adları sabitleyen mevcut beklentiler — `clone_ref_test.go`'nun `todoapp/staging`, `todoapp/broken (Failed)` dahil — değişmedi).
- [ ] **Adım 5: Commit** — `git add internal/envname/envname.go internal/envname/envname_test.go internal/backend/environments.go internal/backend/project_link.go internal/backend/app_environments.go internal/backend/social_link.go internal/backend/deploy.go internal/backend/stack_spec.go internal/backend/name_output_test.go && git commit -m "fix(cli): sunucudan gelen ortam adı terminale ham basılmaz — düz kelime değilse %q; clone, spec ve yazılamayan ortam satırları dahil (FR-006)" -- internal/envname/envname.go internal/envname/envname_test.go internal/backend/environments.go internal/backend/project_link.go internal/backend/app_environments.go internal/backend/social_link.go internal/backend/deploy.go internal/backend/stack_spec.go internal/backend/name_output_test.go`

---

### T008: `palbase env` ve `palbase project list` ortam adını terminale ham basmaz
<!-- deps: [T006, T007] | files: [internal/env/env.go, internal/env/env_test.go, internal/project/project.go, internal/project/project_test.go] | satisfies: [FR-006] -->

**Interfaces:**
- Consumes: `envname.Label` (T007); `internal/env` test yardımcıları (`linkedCheckout`, `stubREST{projects, created}`, `run`, `twoEnvironments`); `internal/project` test yardımcıları (`stubREST{reply}`, `resolvers`, `stubCloud`, `run`)
- Produces: — (`internal/project` artık yaprak `internal/envname`'i import eder; `backend`'i değil)

- [ ] **Adım 1: Kırmızı testleri yaz** — `internal/env/env_test.go` sonuna:
```go
// hostileName is an environment name the control plane accepts today: an OSC
// sequence that retitles the terminal, then a bell.
const (
	hostileName   = "evil\x1b]0;owned\a"
	hostileQuoted = `"evil\x1b]0;owned\a"`
)

func withAHostileName() []map[string]any {
	return []map[string]any{{
		"id": "prd_a", "name": "todoapp",
		"environments": []map[string]any{
			{"ref": "j06bwtuum", "name": "main", "status": "Running"},
			{"ref": "evilref001", "name": hostileName, "status": "Running"},
		},
	}}
}

// EVERY LINE OF `palbase env` THAT PRINTS A NAME PRINTS IT ESCAPED (FR-006):
// the name is somebody else's text, and raw it rewrote a teammate's terminal.
func TestEnvPrintsANameEscapedEverywhere(t *testing.T) {
	linkedCheckout(t)
	for _, c := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{"list"}, hostileQuoted + "  evilref001"},
		{"", []string{"use", "evilref001"}, "▸ todoapp/" + hostileQuoted},
		{"", []string{"use", "nope"}, "  " + hostileQuoted + "   evilref001"},
		{"wrong\n", []string{"delete", "evilref001"}, "This deletes todoapp/" + hostileQuoted + " (evilref001)"},
		{"", []string{"delete", "evilref001", "--yes"}, "Deleted " + hostileQuoted + " (evilref001)"},
	} {
		out, err := run(t, &stubREST{projects: withAHostileName()}, c.stdin, c.args...)
		if err != nil {
			out += err.Error()
		}
		require.NotContains(t, out, "\x1b", "`env %s` printed a name raw", strings.Join(c.args, " "))
		require.Contains(t, out, c.want, "env %s", strings.Join(c.args, " "))
	}
}

// AND THE NAME THE CONTROL PLANE ANSWERS A CREATE WITH IS ITS TEXT TOO — the
// CLI checked the name it SENT, not the one that came back.
func TestCreatePrintsTheNameItWasAnsweredWithEscaped(t *testing.T) {
	linkedCheckout(t)
	rest := &stubREST{projects: twoEnvironments(), created: map[string]any{"ref": "evilref002", "name": hostileName, "phase": "Creating"}}

	out, err := run(t, rest, "", "create", "featureX", "--yes")
	require.NoError(t, err)
	require.NotContains(t, out, "\x1b", "`env create` printed a name raw")
	require.Contains(t, out, "Created "+hostileQuoted+" — evilref002 (Creating)")
	require.Contains(t, out, "palbase env use "+hostileQuoted)
}
```
  `internal/project/project_test.go` sonuna:
```go
// AN ENVIRONMENT'S NAME IS SOMEBODY ELSE'S TEXT (FR-006): any member can name
// one with escape bytes, and printed raw it rewrote the reader's terminal.
func TestListPrintsAnEnvironmentNameEscaped(t *testing.T) {
	rest := &stubREST{reply: []Project{
		{ID: "prd_a", Name: "todoapp", Environments: []Environment{
			{Ref: "aaa", Name: "main", Status: "Running"},
			{Ref: "evilref001", Name: "evil\x1b]0;owned\a", Status: "Running"},
		}},
	}}
	out, err := run(t, resolvers(rest, stubCloud{}), "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("an environment name reached the terminal raw:\n%q", out)
	}
	if !strings.Contains(out, `"evil\x1b]0;owned\a"  evilref001`) {
		t.Fatalf("the name is not printed escaped:\n%s", out)
	}
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/env/ -run 'TestEnvPrintsANameEscapedEverywhere|TestCreatePrintsTheNameItWasAnsweredWithEscaped' -count=1 -v` · Beklenen: **FAIL** — `"   NAME            REF         STATUS\n   main            j06bwtuum   Running\n   evil\x1b]0;owned\a  evilref001  Running\n" should not contain "\x1b"` / ``Messages: `env list` printed a name raw`` ve `"…Created evil\x1b]0;owned\a — evilref002 (Creating)\n…  palbase env use evil\x1b]0;owned\a\n" should not contain "\x1b"`. Run: `go test ./internal/project/ -run TestListPrintsAnEnvironmentNameEscaped -count=1 -v` · Beklenen: **FAIL**, `project_test.go:593: an environment name reached the terminal raw:`.
- [ ] **Adım 3: Uygula** — `internal/env/env.go` (T006'nın eklediği `envname` import'u zaten var); her çiftte üstteki eski, alttaki yeni:
```go
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, e.Name, e.Ref, e.Status)
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, envname.Label(e.Name), e.Ref, e.Status)

				fmt.Fprintf(cmd.OutOrStdout(), "▸ %s/%s\n", p.Name, e.Name)
				fmt.Fprintf(cmd.OutOrStdout(), "▸ %s/%s\n", p.Name, envname.Label(e.Name))

				fmt.Fprintf(out, "This deletes %s/%s (%s) and its data permanently.\nType the ref to confirm: ",
					p.Name, found.Name, found.Ref)
				fmt.Fprintf(out, "This deletes %s/%s (%s) and its data permanently.\nType the ref to confirm: ",
					p.Name, envname.Label(found.Name), found.Ref)

			fmt.Fprintf(out, "Deleted %s (%s)\n", found.Name, found.Ref)
			fmt.Fprintf(out, "Deleted %s (%s)\n", envname.Label(found.Name), found.Ref)

			fmt.Fprintf(out, "\n  palbase env use %s\n", created.Name)
			fmt.Fprintf(out, "\n  palbase env use %s\n", envname.Label(created.Name))
```
  `createCmd`'deki `fmt.Fprintf(out, "Created %s — %s (%s)\n", created.Name, created.Ref, created.Phase)` satırını şununla değiştir:
```go
			// THE NAME THAT CAME BACK IS THE CONTROL PLANE'S, not the one checked
			// above — printed as every other listed name is.
			fmt.Fprintf(out, "Created %s — %s (%s)\n", envname.Label(created.Name), created.Ref, created.Phase)
```
  ve `listing` fonksiyonunun gövdesini şununla değiştir:
```go
	// Same shape as the resolver's refusal (internal/backend): the refs line up,
	// because this list is a menu somebody is about to type from — and each
	// name is the control plane's text, printed as envname.Label has it.
	widest := 0
	for _, e := range envs {
		if n := len([]rune(envname.Label(e.Name))); n > widest {
			widest = n
		}
	}
	rows := make([]string, 0, len(envs))
	for _, e := range envs {
		rows = append(rows, fmt.Sprintf("  %-*s   %s", widest, envname.Label(e.Name), e.Ref))
	}
	return strings.Join(rows, "\n")
```
  `internal/project/project.go`: import bloğunda `"github.com/spf13/cobra"`'dan sonra boş satır + `"github.com/palgroup/palbase-cli/internal/envname"` ekle; `list` komutundaki
```go
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, e.Name, e.Ref, e.Status)
```
  satırını şununla değiştir:
```go
					// The environment's name is any member's text (FR-006).
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, envname.Label(e.Name), e.Ref, e.Status)
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/env/ ./internal/project/ -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/internal/env`, `ok  	github.com/palgroup/palbase-cli/internal/project` (mevcut `TestListShowsEveryProjectWithItsEnvironments` vb. düz adlarla değişmeden yeşil).
- [ ] **Adım 5: Commit** — `git add internal/env/env.go internal/env/env_test.go internal/project/project.go internal/project/project_test.go && git commit -m "fix(env): env ve project list ortam adını terminale ham basmaz (FR-006)" -- internal/env/env.go internal/env/env_test.go internal/project/project.go internal/project/project_test.go`

---

### Dilim sonu kapısı (T001–T008, ad güvenliği)
Ölçüldü, scratch `28b710d` (T008'in commit'i):
- Run: `gofmt -l .` · Beklenen: çıktı yok. Run: `go vet ./...` ve `go vet -tags e2e ./tests/e2e/` · Beklenen: çıktı yok, exit 0. Run: `GOTOOLCHAIN=go1.26.6 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...` · Beklenen: `0 issues.`
- Run: `go test -count=1 ./...` · Beklenen: `internal/envname` dahil 24 paket `ok`; `internal/backend` yalnız B16 ile FAIL — `--- FAIL: TestTheBundlerDoesNotGetToNameThePublicAPI`, `--- FAIL: TestTwoClassesOneNameSurviveTheRealModuleWalk`, `FAIL	github.com/palgroup/palbase-cli/internal/backend	111.176s`; yeni kırık yok.
- Run: `go test -race -count=1 ./internal/envname/ ./internal/env/ ./internal/project/` · Beklenen: `ok  	github.com/palgroup/palbase-cli/internal/envname	1.355s`, `ok  	github.com/palgroup/palbase-cli/internal/env	1.470s`, `ok  	github.com/palgroup/palbase-cli/internal/project	1.817s`.

---

### T009: Yerel yığın `palbase start`'ın kaydettiği grupla bulunur; bulunamazsa kayıttakiler söylenir
<!-- deps: [T007] | files: [internal/backend/link_local_stack_test.go, internal/backend/app_environments.go, internal/backend/project_link.go, internal/backend/start.go, tests/e2e/ios_layout_test.go] | satisfies: [FR-008] -->

Bugün app checkout'u yerel yığını **kendi dizininin adıyla** arıyor (`app_environments.go` `groupOf` → `filepath.Base(target.checkoutRoot)`), çünkü `runLinkPrepared`'ın kurduğu hedefte (`project_link.go:851`) ne `Name` ne `Project` var; `palbase start` ise yığını **bağlı projenin adıyla** kaydediyor (`start.go` `groupName`). 0.71.2'de ölçüldü: kayıt grubu `todoapp`, app checkout'u `MyApp` → `local/` hiç yazılmadı (verification B1/L1). Bu görev hedefe ürünü taşır, aramayı `start`'ın kuralıyla aynı sıraya koyar (önce ürünün adı, sonra dizin adı) ve hiçbiri bulunamazken makinede başka yığınlar varsa onları adlandırır. Makinede hiç yığın yoksa (yalnız bulut kullanan biri) hiçbir şey söylenmez.

**Interfaces:**
- Consumes: `registerStack(group, url, project, dir string) error`, `LookupLocalStack(group string) string`, `sanitiseGroup(name string) string` (`start.go`); test yardımcıları `inScratchCheckout`, `stackServing`, `readEnvConfig` (`project_link_test.go`), `seedAndroidApp` (`platform_environments_test.go`), `routeEnvironments` (`gather_environments_test.go`), `linkKeyMain` (`link_all_environments_test.go`).
- Produces: `func findLocalStack(target Target) (url string, looked []string)` ve `func localStackGroups(target Target) []string` (`app_environments.go`; `groupOf` SİLİNİR); `func registeredStackGroups() []string` ve `func readStackRegistry() stackRegistry` (`start.go`); `runLinkPrepared`'ın `target`'ı artık `Project: o.product.ID, Name: o.product.Name` taşır; çıktı satırı `local: no stack on this machine is registered as <grup> or <grup> (registered: <a>, <b>) — local/ is not written; …`. Test yardımcıları (`link_local_stack_test.go`): `const linkKeyLocal`, `startedAs(t, group) string`, `productLink(t, name) linkOpts` — T010/T011 bunları kullanır.

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_local_stack_test.go`:
```go
package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const linkKeyLocal = "pb_local_cL4Kj1fDyNSboIH7H7mZYI8INFW5NpdX3"

// startedAs registers a stack the way `palbase start` does in a backend
// checkout whose group is `group`, and gives this machine the key it holds.
func startedAs(t *testing.T, group string) string {
	t.Helper()
	local := stackServing(t, linkKeyLocal, nil)
	require.NoError(t, registerStack(group, local.URL, "palbase-"+group, "/elsewhere/backend"))
	require.NoError(t, StoreCredential(local.URL, Credentials{Value: "local-key", Kind: KindKey}))
	return local.URL
}

// productLink is an Android app checkout linked to the project `name`, whose one
// environment `main` answers every read.
func productLink(t *testing.T, name string) linkOpts {
	t.Helper()
	seedAndroidApp(t)
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	return linkOpts{
		url:          main.URL,
		platforms:    []string{"android"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: name},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}
}

// THE STACK IS FOUND UNDER THE GROUP `palbase start` REGISTERED IT UNDER (FR-008).
//
// `start` names its stack after the project its checkout is linked to
// (start.go groupName); an app checkout lives in a directory of its own name.
// Looking the stack up by that directory found it only when the two happened to
// share a name — measured on 0.71.2: registry group `todoapp`, app checkout
// `MyApp`, and link wrote main/ with no local/ at all.
func TestAnAppCheckoutFindsTheStackStartedForItsProject(t *testing.T) {
	inScratchCheckout(t)
	localURL := startedAs(t, sanitiseGroup("Todo App"))
	o := productLink(t, "Todo App")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	cfg := readEnvConfig(t, localEnvName, "android")
	assert.Equal(t, localURL, cfg.BaseURL)
	assert.Equal(t, linkKeyLocal, cfg.APIKey)
	assert.FileExists(t, SpecPath(localEnvName))
}

// AND UNDER THE CHECKOUT'S OWN DIRECTORY NAME WHEN THE PROJECT'S FINDS NONE:
// that is what `start` registers in a checkout that is not linked, and what a
// monorepo's app and backend share.
func TestAnAppCheckoutStillFindsAStackRegisteredUnderItsDirectoryName(t *testing.T) {
	inScratchCheckout(t)
	dir, err := os.Getwd()
	require.NoError(t, err)
	localURL := startedAs(t, sanitiseGroup(filepath.Base(dir)))
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Equal(t, localURL, readEnvConfig(t, localEnvName, "android").BaseURL)
}

// NEITHER, AND THIS MACHINE RUNS OTHER STACKS: link names them, because the
// person who started one expects local/ and would otherwise get silence.
func TestALinkThatFindsNoStackNamesTheOnesThisMachineRuns(t *testing.T) {
	inScratchCheckout(t)
	startedAs(t, "billing")
	startedAs(t, "todo-backend")
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.NoDirExists(t, EnvDir(localEnvName))
	dir, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, out.String(), "local: no stack on this machine is registered as todoapp or "+
		sanitiseGroup(filepath.Base(dir))+" (registered: billing, todo-backend) — local/ is not written; "+
		"`palbase start` registers a stack under the name of the project its checkout is linked to, "+
		"or under its directory's name when that checkout is not linked")
}

// A MACHINE THAT RUNS NO STACK IS A CLOUD-ONLY SETUP, not a mistake: nothing is said.
func TestALinkOnAMachineThatRunsNoStackSaysNothingAboutLocal(t *testing.T) {
	inScratchCheckout(t)
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.NoDirExists(t, EnvDir(localEnvName))
	assert.NotContains(t, out.String(), "local:")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestAnAppCheckoutFindsTheStackStartedForItsProject|TestAnAppCheckoutStillFindsAStackRegisteredUnderItsDirectoryName|TestALinkThatFindsNoStackNamesTheOnesThisMachineRuns|TestALinkOnAMachineThatRunsNoStackSaysNothingAboutLocal' -count=1` · Beklenen: **FAIL**, çıktıda `link_local_stack_test.go:57: no config for local/android: open palbase/environments/local/android-config.json: no such file or directory` ve `"wrote palbase/environments/main/android-config.json\n\nlinked to todoapp (prd_a)\n  contract read from main; each verb resolves its own environment\ncommit palbase/\n" does not contain "local: no s…`. Diğer ikisi (dizin adı yedeği, yığınsız makine) bugün de geçer — gerileme bekçileri.
- [ ] **Adım 3: Uygula** —
  (a) `internal/backend/project_link.go` `runLinkPrepared` (~851) içindeki
```go
	target := Target{URL: base, Insecure: o.insecure, checkoutRoot: o.checkoutRoot}
```
  satırını şununla değiştir:
```go
	//
	// THE PROJECT RIDES ALONG (FR-008): it is the name `palbase start` registers
	// its stack under, and a target without it could look the local stack up by
	// this checkout's directory name alone.
	target := Target{URL: base, Insecure: o.insecure, checkoutRoot: o.checkoutRoot,
		Project: o.product.ID, Name: o.product.Name}
```
  (b) `internal/backend/app_environments.go` import bloğuna `"path/filepath"`'ten sonra `"slices"` ekle; `gatherEnvironments`'ın sonundaki (~691)
```go
	localURL := LookupLocalStack(groupOf(primary))
	if localURL == "" || localURL == primary.URL {
		return envs, specs, nil
	}
```
  bloğunu şununla değiştir:
```go
	localURL, looked := findLocalStack(primary)
	if localURL == "" {
		// A MACHINE THAT RUNS STACKS, NONE OF THEM THIS CHECKOUT'S: said, because
		// the person who started one expects local/ and silence reads as "link
		// forgot it". A machine that runs none is a cloud-only setup — nothing
		// to say.
		if running := registeredStackGroups(); len(running) > 0 {
			fmt.Fprintf(w, "local: no stack on this machine is registered as %s (registered: %s) — local/ is not written; "+
				"`palbase start` registers a stack under the name of the project its checkout is linked to, "+
				"or under its directory's name when that checkout is not linked\n",
				strings.Join(looked, " or "), strings.Join(running, ", "))
		}
		return envs, specs, nil
	}
	if localURL == primary.URL {
		return envs, specs, nil
	}
```
  ve `groupOf`'u (doc yorumuyla birlikte)
```go
// groupOf is the project group a target belongs to, for finding its local stack
// in the machine register.
func groupOf(target Target) string {
	// THE NAME, NOT THE IDENTITY. `Target.Project` used to be what a person
	// called their project; it is the product ID now (`prd_9f21c7`), and that
	// value would become the docker compose project name — every local
	// container renamed to an opaque string nobody typed. When a model's
	// direction is inverted, the code that read the old meaning stays behind
	// and keeps compiling; this is that code.
	if target.Name != "" {
		return target.Name
	}
	if target.Project != "" {
		return target.Project
	}
	if target.checkoutRoot != "" {
		return filepath.Base(target.checkoutRoot)
	}
	root, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(root)
}
```
  şu iki fonksiyonla değiştir:
```go
// findLocalStack is the address of the stack `palbase start` registered for this
// target's project, or "" — and every group it looked under, in order.
func findLocalStack(target Target) (url string, looked []string) {
	looked = localStackGroups(target)
	for _, group := range looked {
		if url := LookupLocalStack(group); url != "" {
			return url, looked
		}
	}
	return "", looked
}

// localStackGroups are the groups a target's local stack may be registered
// under, in the order they are tried (FR-008).
//
// THE PROJECT'S NAME FIRST, because it is `start`'s own rule (start.go
// groupName): a backend checkout linked to a project registers its stack under
// that project's name. This used to be the APP checkout's directory name, so an
// app in `MyApp/` never found the stack a backend linked to `todoapp` had
// started — measured on 0.71.2: link wrote main/ and no local/ at all. The
// directory name stays as the second try: it is what `start` registers in a
// checkout that is not linked, and what a monorepo's app and backend share.
func localStackGroups(target Target) []string {
	var groups []string
	add := func(name string) {
		if name == "" {
			return // sanitiseGroup answers "project" for it: a group nobody chose
		}
		if group := sanitiseGroup(name); !slices.Contains(groups, group) {
			groups = append(groups, group)
		}
	}
	// THE NAME, NOT THE IDENTITY. `Target.Project` used to be what a person
	// called their project; it is the product ID now (`prd_9f21c7`), and that
	// value would become the docker compose project name — every local
	// container renamed to an opaque string nobody typed. `start` falls back to
	// the ID only when the record carries no name, and so does this.
	if target.Name != "" {
		add(target.Name)
	} else {
		add(target.Project)
	}
	// The REAL checkout's name, not the working directory's: `link` runs in a
	// stage, whose directory is a temporary name nobody registered.
	root := target.checkoutRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	if root != "" {
		add(filepath.Base(root))
	}
	return groups
}
```
  (c) `internal/backend/start.go` import bloğuna `"regexp"`'ten sonra `"sort"` ekle; `LookupLocalStack`'in gövdesini
```go
func LookupLocalStack(group string) string {
	path, err := registryPath()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var reg stackRegistry
	if json.Unmarshal(raw, &reg) != nil {
		return ""
	}
	return reg.Stacks[sanitiseGroup(group)].URL
}
```
  şununla değiştir (kayıt dosyasını okuyan üçüncü bir kopya doğmasın diye okuma tek fonksiyona iner):
```go
func LookupLocalStack(group string) string {
	return readStackRegistry().Stacks[sanitiseGroup(group)].URL
}

// registeredStackGroups names every group this machine's register holds a stack
// for, sorted — what `link` lists when none of them is the checkout's own
// (FR-008), so a person who started a stack is not answered with silence.
func registeredStackGroups() []string {
	reg := readStackRegistry()
	groups := make([]string, 0, len(reg.Stacks))
	for group := range reg.Stacks {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups
}

// readStackRegistry is the register as it stands, or an empty one when there is
// none or it cannot be read: every reader treats those alike, as "no stack runs
// here".
func readStackRegistry() stackRegistry {
	path, err := registryPath()
	if err != nil {
		return stackRegistry{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return stackRegistry{}
	}
	var reg stackRegistry
	if json.Unmarshal(raw, &reg) != nil {
		return stackRegistry{}
	}
	return reg
}
```
  ve `groupOfLocalStack`'in başındaki
```go
func groupOfLocalStack(url string) (string, bool) {
	path, err := registryPath()
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var reg stackRegistry
	if json.Unmarshal(raw, &reg) != nil {
		return "", false
	}
	for group, stack := range reg.Stacks {
```
  bloğunu şununla değiştir (döngünün geri kalanı aynı):
```go
func groupOfLocalStack(url string) (string, bool) {
	for group, stack := range readStackRegistry().Stacks {
```
  (d) `tests/e2e/ios_layout_test.go` `e2eCheckout`'un yorumu artık olmayan `groupOf`'u ve "yalnız aynı ad" kuralını anlatıyor;
```go
// `groupOf` falls back to the base name when a target carries no project field
// and `LookupLocalStack` reads the machine-wide registry by that group (D-009),
// so an app checkout carries a `local` environment only when it shares the name
// of the checkout that started one.
```
  satırlarını şununla değiştir:
```go
// `localStackGroups` tries the linked project's name first and the checkout's
// directory name second, and `LookupLocalStack` reads the machine-wide registry
// by each (D-009, FR-008) — so naming the checkout after the group finds the
// stack whatever the real project is called.
```
- [ ] **Adım 4: Yeşil** — Run: Adım 2'deki komut `-v` ile · Beklenen: dört `--- PASS` (`TestAnAppCheckoutFindsTheStackStartedForItsProject`, `TestAnAppCheckoutStillFindsAStackRegisteredUnderItsDirectoryName`, `TestALinkThatFindsNoStackNamesTheOnesThisMachineRuns`, `TestALinkOnAMachineThatRunsNoStackSaysNothingAboutLocal`), `ok  	github.com/palgroup/palbase-cli/internal/backend`. Sonra `gofmt -l .` boş, `go vet -tags e2e ./tests/e2e/` temiz, `go test ./internal/backend/ -count=1` · Beklenen: yalnız B16 FAIL (Global Constraints; bu scratch'te: `TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`); yeni kırık yok.
- [ ] **Adım 5: Commit** — `git add internal/backend/link_local_stack_test.go internal/backend/app_environments.go internal/backend/project_link.go internal/backend/start.go tests/e2e/ios_layout_test.go && git commit -m "fix(link): yerel yığın palbase start'ın kaydettiği grupla (ürünün adı) aranır, yoksa dizin adıyla; bulunamazsa kayıttakiler söylenir (FR-008)" -- internal/backend/link_local_stack_test.go internal/backend/app_environments.go internal/backend/project_link.go internal/backend/start.go tests/e2e/ios_layout_test.go`

---

### T010: `local/` ya iki dosyasıyla yazılır ya hiç
<!-- deps: [T009] | files: [internal/backend/link_local_stack_test.go, internal/backend/app_environments.go, internal/backend/project_link_test.go] | satisfies: [FR-009] -->

Bugün kayıtlı yığın anahtar veremezse (durmuş ya da bu makinede kimlik yok) `gatherEnvironments` anahtarsız bir `local` girdisi yazıyor (`"api_key": ""`), sözleşme veremezse anahtarlı ama `openapi.json`'suz; önerisi de `palbase spec` — ki spec hiçbir config yazmıyor (verification B2 ve "YENİ — palbase spec cannot fill a keyless local entry"). Plugin ikisini de "inputs are incomplete" diye reddediyor ve dosya commit'lenince her takım arkadaşının debug build'i düşüyor. Bu görev: anahtar ya da sözleşme alınamazsa `local/` için hiçbir dosya yazılmaz, sebep söylenir, öneri "`palbase start`, then `palbase link` here".

**Mevcut bir testi BİLEREK değiştirir:** `project_link_test.go` `TestAStoppedLocalStackStillGetsAnEntry` tam olarak eski davranışı (anahtarsız girdi) sabitliyor. FR-009 onu tersine çevirdiği için test silinir, yerine bir "EMEKLİ" notu kalır; aynı senaryo (kayıtlı, cevap vermeyen yığın) `TestAStoppedLocalStackGetsNoLocalFiles`ta yeni davranışla ölçülür.

**Interfaces:**
- Consumes: T009'un `productLink`, `linkKeyLocal`; `envServer`/`envServerOpts{noContract, socialAuth}` (`gather_environments_test.go`); `fetchStackSpec`, `projectPublishableKey`, `Credential`.
- Produces: `gatherEnvironments`'ın yerel dalı ya iki girdiyi (`envs.Environments["local"]` + `specs["local"]`) birlikte döndürür ya hiçbirini. Satırlar: ``local: <url> is registered but this machine holds no credential for it — nothing is written for local; `palbase start`, then `palbase link` here`` · `local: <url> did not give its key (<hata>) — …` · `local: <url> gave its key but not its contract (<hata>) — …`. Test sabiti `fillLocalAdvice`.

- [ ] **Adım 1: Kırmızı testi yaz** — `internal/backend/link_local_stack_test.go` sonuna ekle:
```go
// fillLocalAdvice ends every line that leaves local/ unwritten (FR-009): the
// stack is brought up where it lives, and THIS checkout is linked again — the
// only verb that writes a config. `palbase spec` writes a contract alone.
const fillLocalAdvice = "— nothing is written for local; `palbase start`, then `palbase link` here"

// LOCAL IS BOTH FILES OR NEITHER (FR-009). A stack that is registered but does
// not answer used to get a keyless android-config.json and no contract: a
// committed file that fails every teammate's debug build, and a "fill it in
// with `palbase spec`" that could not fill a config in.
func TestAStoppedLocalStackGetsNoLocalFiles(t *testing.T) {
	inScratchCheckout(t)
	const stopped = "http://127.0.0.1:1"
	require.NoError(t, registerStack("todoapp", stopped, "palbase-todoapp", "/elsewhere/backend"))
	require.NoError(t, StoreCredential(stopped, Credentials{Value: "local-key", Kind: KindKey}))
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.FileExists(t, ConfigPath("main", "android"))
	assert.NoDirExists(t, EnvDir(localEnvName))
	assert.Contains(t, out.String(), "local: "+stopped+" did not give its key (")
	assert.Contains(t, out.String(), fillLocalAdvice)
}

// A STACK THIS MACHINE HOLDS NO CREDENTIAL FOR gives no key: the same neither.
func TestALocalStackWithNoCredentialGetsNoLocalFiles(t *testing.T) {
	inScratchCheckout(t)
	local := stackServing(t, linkKeyLocal, nil)
	require.NoError(t, registerStack("todoapp", local.URL, "palbase-todoapp", "/elsewhere/backend"))
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.NoDirExists(t, EnvDir(localEnvName))
	assert.Contains(t, out.String(), "local: "+local.URL+
		" is registered but this machine holds no credential for it "+fillLocalAdvice)
}

// A KEY WITHOUT A CONTRACT IS HALF OF LOCAL, and half is what the plugin refuses
// ("inputs are incomplete"): neither file is written.
func TestALocalStackWithNoContractGetsNoLocalFiles(t *testing.T) {
	inScratchCheckout(t)
	local, _ := envServer(t, linkKeyLocal, envServerOpts{noContract: true, socialAuth: true})
	require.NoError(t, registerStack("todoapp", local.URL, "palbase-todoapp", "/elsewhere/backend"))
	require.NoError(t, StoreCredential(local.URL, Credentials{Value: "local-key", Kind: KindKey}))
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.NoDirExists(t, EnvDir(localEnvName))
	assert.Contains(t, out.String(), "local: "+local.URL+" gave its key but not its contract (no contract yet: "+
		local.URL+" has nothing to describe yet — push a backend to it first (palbase push)) "+fillLocalAdvice)
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestAStoppedLocalStackGetsNoLocalFiles|TestALocalStackWithNoCredentialGetsNoLocalFiles|TestALocalStackWithNoContractGetsNoLocalFiles' -count=1` · Beklenen: **FAIL** ×3, her birinde `Error: directory "palbase/environments/local" exists`; ilkinin çıktısında bugünkü satır ``"local: http://127.0.0.1:1 did not answer — run `palbase start`, then `palbase spec` to fill it in\nwrote palbase/environments/local/android-config.json…``, ikincisinde ``"local: http://127.0.0.1:<port> is registered but this machine holds no credential for it — `palbase start`\nwrote palbase/environments/local/android-config.json…``. (Sözleşmesiz fikstür `socialAuth: true` taşımalı: taşımazsa Android okuması 404 alır ve `local could not be read (local/android: social auth config … HTTP 404)` ile başka bir yoldan düşer — ölçüldü.)
- [ ] **Adım 3: Uygula** — `internal/backend/app_environments.go` `gatherEnvironments`'ın sonundaki
```go
	localTarget := Target{URL: localURL, Local: true}
	localCred, _, credErr := Credential(localURL)
	if credErr != nil {
		envs.Environments[localEnvName] = appEnvironment{AppID: projectAppID, BaseURL: localURL}
		fmt.Fprintf(w, "local: %s is registered but this machine holds no credential for it — `palbase start`\n", localURL)
		return envs, specs, nil
	}
	localKey, keyErr := projectPublishableKey(ctx, localTarget)
	if keyErr != nil {
		// FR-057: the entry is written keyless, and the sequence that fills it
		// is named. A missing entry would be a build configuration that vanishes.
		envs.Environments[localEnvName] = appEnvironment{AppID: projectAppID, BaseURL: localURL}
		fmt.Fprintf(w, "local: %s did not answer — run `palbase start`, then `palbase spec` to fill it in\n", localURL)
		return envs, specs, nil
	}
	localEnv := appEnvironment{
		AppID:   projectAppID,
		BaseURL: localURL,
		APIKey:  localKey,
	}
	if _, root, err := projectKeys(ctx, localTarget); err == nil && root != "" {
		localEnv.SealedRoot = root
	}
	envs.Environments[localEnvName] = localEnv
	if localSpec, err := fetchStackSpec(ctx, localTarget, localCred); err == nil {
		specs[localEnvName] = localSpec
	}
	return envs, specs, nil
}
```
  bloğunu şununla değiştir:
```go
	// BOTH FILES OR NEITHER (FR-009). A stack that could not give its key used to
	// get a keyless entry ("a missing entry would be a build configuration that
	// vanishes"), and one that gave a key but no contract got a config with no
	// contract beside it. Both are inputs a generator refuses — the Android
	// plugin as "inputs are incomplete", on every teammate's debug build once the
	// half was committed — and the advice to fill it in with `palbase spec` could
	// not: spec writes a contract, never a config. What this run could not read
	// is said, and nothing is written for it.
	skipLocal := func(why string) (appEnvironments, map[string][]byte, error) {
		fmt.Fprintf(w, "local: %s %s — nothing is written for local; `palbase start`, then `palbase link` here\n", localURL, why)
		return envs, specs, nil
	}
	localTarget := Target{URL: localURL, Local: true}
	localCred, _, credErr := Credential(localURL)
	if credErr != nil {
		return skipLocal("is registered but this machine holds no credential for it")
	}
	localKey, keyErr := projectPublishableKey(ctx, localTarget)
	if keyErr != nil {
		return skipLocal(fmt.Sprintf("did not give its key (%v)", keyErr))
	}
	localSpec, specErr := fetchStackSpec(ctx, localTarget, localCred)
	if specErr != nil {
		return skipLocal(fmt.Sprintf("gave its key but not its contract (%v)", specErr))
	}
	localEnv := appEnvironment{
		AppID:   projectAppID,
		BaseURL: localURL,
		APIKey:  localKey,
	}
	if _, root, err := projectKeys(ctx, localTarget); err == nil && root != "" {
		localEnv.SealedRoot = root
	}
	envs.Environments[localEnvName] = localEnv
	specs[localEnvName] = localSpec
	return envs, specs, nil
}
```
- [ ] **Adım 4: Yeşil** — Run: Adım 2'deki komut `-v` ile · Beklenen: üç `--- PASS`, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Ayrıca `go test ./internal/backend/ -run TestAStoppedLocalStackStillGetsAnEntry -count=1` · Beklenen: **FAIL** — `project_link_test.go:420: no config for local/ios: open palbase/environments/local/ios-config.json: no such file or directory` (eski davranışın testi; Adım 5).
- [ ] **Adım 5: Eski testi bilerek emekliye ayır** — `internal/backend/project_link_test.go` içindeki fonksiyonun tamamını
```go
func TestAStoppedLocalStackStillGetsAnEntry(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	t.Setenv("HOME", t.TempDir())

	dir, _ := os.Getwd()
	group := sanitiseGroup(filepath.Base(dir))
	// Registered, but nothing is listening there.
	if err := registerStack(group, "http://127.0.0.1:1", "palbase-"+group, dir); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential("http://127.0.0.1:1", Credentials{Value: "k", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out); err != nil {
		t.Fatalf("link: %v\n%s", err, out.String())
	}

	// THE LOCAL ENVIRONMENT HAS ITS OWN DIRECTORY, and it must exist even when
	// the stack is down — an app whose Local configuration disappears with a
	// stopped container stops compiling for a reason nobody connects to it.
	entry := readEnvConfig(t, localEnvName, "ios")
	if entry.APIKey != "" {
		t.Errorf("a key was invented for a stack that did not answer: %q", entry.APIKey)
	}
	if !strings.Contains(out.String(), "palbase start") {
		t.Errorf("the output does not say how to fill it in:\n%s", out.String())
	}
}
```
  şu notla değiştir:
```go
// EMEKLİ: `TestAStoppedLocalStackStillGetsAnEntry`.
//
// Kayıtlı ama cevap vermeyen bir yığına anahtarsız bir `local` girdisi
// yazıldığını ölçüyordu ("Local konfigürasyonu kaybolan bir app derlenmez").
// FR-009 bunu BİLEREK tersine çevirdi: yarım bir `local/` commit'lenince her
// takım arkadaşının debug build'i düşüyordu ve önerdiği `palbase spec` bir
// config'i hiç dolduramıyordu. Aynı senaryo artık
// `TestAStoppedLocalStackGetsNoLocalFiles`ta (link_local_stack_test.go):
// `local/` için hiçbir dosya yazılmaz, öneri "`palbase start`, then
// `palbase link` here".
```
- [ ] **Adım 6: Paket yeşil** — Run: `gofmt -l .` · Beklenen: boş; `go test ./internal/backend/ -count=1` · Beklenen: yalnız B16 FAIL (Global Constraints; bu scratch'te: `TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`). (cli-2 taslakçısı bir koşuda `TestTheWebConfigDoesNotResurrectARemovedField`'ın `stat node_modules/.bin/palbe-gen: no such file or directory` ile düştüğünü gördü — gerçek `npm install` yapan bir test, yük altında; bu revizyonun dört tam paket koşusunda görülmedi ve bu görevin değiştirdiği yolla ilgisi yok.)
- [ ] **Adım 7: Commit** — `git add internal/backend/link_local_stack_test.go internal/backend/app_environments.go internal/backend/project_link_test.go && git commit -m "fix(link): local/ ya iki dosyasıyla yazılır ya hiç — anahtar ya da sözleşme yoksa hiçbir dosya yok, öneri palbase start + palbase link (FR-009)" -- internal/backend/link_local_stack_test.go internal/backend/app_environments.go internal/backend/project_link_test.go`

---

### T011: Bu makinedeki yığına link de spec de `local` der — `main/` loopback taşımaz
<!-- deps: [T005, T010] | files: [internal/backend/link_local_stack_test.go, internal/backend/environments.go, internal/backend/project_link.go, internal/backend/app_environments.go, internal/backend/stack_spec.go, internal/backend/project_link_test.go, internal/backend/link_rerun_test.go, internal/backend/social_link_test.go] | satisfies: [FR-010] -->

Bugün aynı yığın için iki fiil iki ad veriyor: `palbase start` kaydı olan bir checkout'ta `link` `main/` yazıyor (`followStart` ürünsüz `o.url` kurar, `project_link.go:895` `linkedEnv = soleEnvName`), `spec` ise `local/` (`stack_spec.go:91-94` `if target.Local { env = localEnvName }`) — verification B3/L4. Elle bağlanan loopback bir adres (`palbase link http://localhost:54321`, belgeli akış) ikisine de `main` — yani `main/` 127.0.0.1 taşıyor ve release'i `main`'e eşlemek cleartext bir loopback release'i paketliyor (B8/L1). Bu görev "bu makinedeki bir yığın" için TEK bir adlandırma fonksiyonu koyar — `stackEnvName(t Target)`: `t.Local` (bir start kaydı) ya da loopback adres → `local`, diğer her projesiz yığın → `main`. `Resolved.ArtifactEnv()` (spec/push) ve link'in projesiz dalı ikisi de onu sorar; link start kaydını `writeLinkRecord` ile aynı şekilde (`startRecordAt` + `sameStack`) sorar, çünkü `palbase start --lan` loopback olmayan bir adres kaydeder.

İki bağlı değişiklik: (1) link'in kendi hedefi `local` ise `gatherEnvironments` kayıttan ikinci bir `local` aramaz (aynı adla ezerdi); (2) spec'in ad kapısı (T005) `local`'i yalnız bir PROJE o adı verdiğinde reddeder: `whyNotWritable(env, resolved.Env == "")` — projesiz bir ortam yığının kendisidir. Görünür yan etkiler (ikisi de amaçlanan): böyle bir checkout'ta Apple'a basılan seçim parçası `PALBASE_ENV = local` der (`envs.Default`) ve web istemcisi `local/` altında üretilir. Eski bir CLI'ın aynı checkout'a yazdığı loopback'li `main/` bu görevden sonra diskte kalır; onu **T014** alır — projesiz bir link'in genel süpürmesi Android/web'de koşmadığı için (T014'ün `len(listed) > 0` koşulu) ayrı bir kuralla: bu makinenin yığınına `local` diyen bir link, her config'i loopback bir adres taşıyan ve yalnız CLI dosyaları tutan `main/`'i her platformda siler (`removed palbase/environments/main (it held the stack on this machine, which is local/ now)`). Apple'da genel süpürme de alırdı; eleştirmen Android/web'de kaldığını ölçtü (`directory "palbase/environments/main" exists`).

**Mevcut testleri BİLEREK değiştirir.** `httptest` sunucuları 127.0.0.1'de; "bir adrese link" yapan eski testler loopback bir self-host'u `main` diye okuyordu. Uygulamadan sonra (Adım 4) şu dokuz test ölçülerek düşer: `TestANoTargetLinkFollowsALoopbackInstallBeforeTheProjectRecord`, `TestANoTargetLinkFollowsTheStartStack` (link_rerun_test.go), `TestTheAppConfigCarriesThePUBLISHABLEKey`, `TestTheSlotCarriesEveryEnvironment`, `TestLinkingForWebWritesTheWebGeneratorsInputs`, `TestTheWebConfigDoesNotResurrectARemovedField`, `TestTheAppConfigCarriesTheStacksSealingRoot`, `TestAStackWithNoSealingRootStillLinks` (project_link_test.go), `TestLinkPublishesInstalledWebDependencies` (social_link_test.go). Hepsinin konusu başka bir özellik (yayımlanabilir anahtar, mühür kökü, web üretici girdileri…); yalnız okudukları dizin `main` → `local` olur. `TestTheSlotCarriesEveryEnvironment` ise "bir proje + bu makinenin yığını" senaryosunu loopback bir adresle kuruyordu — artık o adres yığının KENDİSİ; proje gerçek bir ürün bağlantısına çevrilir. Ayrıca dört test düşmeden içi boşalırdı, onlar da güncellenir: `TestAnAuthRefreshFollowsALoopbackInstallBeforeTheProjectRecord` (eski kodla da geçerdi — tohum `local/`'e bayat bir adresle konur ki yenilemenin yeniden yazdığı ölçülsün), `TestLinkingWithoutACredentialWritesNOTHING`, `TestAnUnsupportedPlatformIsRefusedBeforeAnythingIsWritten`, `TestFirstLinkCannotSucceedWithoutWebGenerator` (reddin "hiçbir şey yazılmadı" kontrolü artık yazılacak olan dizine, `local/`'e bakar).

**Interfaces:**
- Consumes: `isLoopbackAddress` (`target.go`), `startRecordAt`, `sameStack` (`project_link.go`), `whyNotWritable(name string, thisMachine bool) string` (T001/T003/T005), `startStackHere(t)` (`link_rerun_test.go`), T009'un `productLink`.
- Produces: `func stackEnvName(t Target) string` (`environments.go`) — "bu makinedeki yığın" için TEK ad kaynağı; `Resolved.ArtifactEnv()` projesizken ona devreder. Link'in projesiz `linkedEnv`'i = `stackEnvName(Target{URL: base, Local: <start kaydı bu adresi söylüyor>})`. `gatherEnvironments(..., defaultEnv == "local", ...)` kayıt aramasını atlar. Sonuç: loopback self-host / start yığını için `envs.names()` = `{local}`, `main/` yazılmaz.

- [ ] **Adım 1: Kırmızı testi yaz** — `internal/backend/link_local_stack_test.go` sonuna ekle:
```go
// ONE NAME FOR THE STACK ON THIS MACHINE, WHICHEVER VERB WRITES IT (FR-010).
//
// Measured on 0.71.2 in one checkout with a start record: `palbase link` wrote
// main/android-config.json, `palbase spec` wrote local/openapi.json — the build
// found half of each. And main/ carried a loopback address, so mapping release
// to main shipped a release that talks to 127.0.0.1 in cleartext.
func TestTheStackStartedHereIsLocalToLinkAndSpecAlike(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	stack, _, _ := startStackHere(t)

	o := linkOpts{}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	cfg := readEnvConfig(t, localEnvName, "android")
	assert.Equal(t, stack.URL, cfg.BaseURL)
	assert.Equal(t, linkKeyMain, cfg.APIKey)
	assert.NoDirExists(t, EnvDir("main"), "a stack on this machine was written as main/")

	var spec strings.Builder
	require.NoError(t, RefreshSpec(context.Background(), &spec), spec.String())
	assert.Contains(t, spec.String(), "✓ wrote palbase/environments/local/openapi.json (")
	assert.NoDirExists(t, EnvDir("main"))
}

// A LOOPBACK ADDRESS IS THIS MACHINE TOO, however the stack got there: linked
// by hand (`palbase link http://localhost:54321`, the documented flow) it was
// main/ to link and to spec alike — the same loopback main/.
func TestALoopbackAddressIsLocalToLinkAndSpecAlike(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	t.Setenv("PALBASE_ENV", "")
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")

	o := linkOpts{url: stack.URL}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Equal(t, stack.URL, readEnvConfig(t, localEnvName, "android").BaseURL)
	assert.NoDirExists(t, EnvDir("main"), "a loopback address was written as main/")

	var spec strings.Builder
	require.NoError(t, RefreshSpec(context.Background(), &spec), spec.String())
	assert.Contains(t, spec.String(), "✓ wrote palbase/environments/local/openapi.json (")
	assert.NoDirExists(t, EnvDir("main"))
}

// AND NOTHING ELSE IS: a stack somebody hosts keeps `main`, a project's
// environment keeps its own name, and a start record is this machine whatever
// address it announces (`palbase start --lan` records the LAN one).
func TestOnlyAStackOnThisMachineIsNamedLocal(t *testing.T) {
	for _, c := range []struct {
		resolved Resolved
		want     string
	}{
		{Resolved{URL: "https://stack.example.com"}, "main"},
		{Resolved{URL: "http://192.168.1.20:54321"}, "main"},
		{Resolved{URL: "http://localhost:54321"}, localEnvName},
		{Resolved{URL: "http://127.0.0.1:54321"}, localEnvName},
		{Resolved{URL: "http://[::1]:54321"}, localEnvName},
		{Resolved{Target: Target{Local: true}, URL: "http://192.168.1.20:54321"}, localEnvName},
		{Resolved{Env: "staging", URL: "http://127.0.0.1:54321"}, "staging"},
	} {
		assert.Equal(t, c.want, c.resolved.ArtifactEnv(), "%+v", c.resolved)
	}
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestTheStackStartedHereIsLocalToLinkAndSpecAlike|TestALoopbackAddressIsLocalToLinkAndSpecAlike|TestOnlyAStackOnThisMachineIsNamedLocal' -count=1` · Beklenen: **FAIL**, çıktıda `link_local_stack_test.go:184: no config for local/android: open palbase/environments/local/android-config.json: no such file or directory`, `link_local_stack_test.go:210: no config for local/android: …` ve dört kez `expected: "local"` / `actual  : "main"` (localhost, 127.0.0.1, [::1] ve `Local:true`+LAN adresi).
- [ ] **Adım 3: Uygula** —
  (a) `internal/backend/environments.go` (~97) `ArtifactEnv`'in doc yorumunun son satırından fonksiyonun sonuna kadar
```go
// first. It survives only where it is actually true: one installation, one
// identity, one environment.
func (r Resolved) ArtifactEnv() string {
	if r.Env != "" {
		return r.Env
	}
	return soleEnvName
}
```
  şununla değiştir:
```go
// first. It survives only where it is actually true: one installation, one
// identity, one environment — and that installation is not on this machine.
func (r Resolved) ArtifactEnv() string {
	if r.Env != "" {
		return r.Env
	}
	return stackEnvName(r.Acting())
}

// stackEnvName is the directory the one environment of a stack with no project
// takes: `local` for a stack on THIS machine — one `palbase start` runs here,
// or any loopback address — and `main` for one somebody hosts.
//
// ONE ANSWER FOR EVERY VERB THAT WRITES IT (FR-010). `link` named a started
// stack `main` while `spec` named it `local` (measured on 0.71.2: the build
// found a config in one directory and the contract in the other), and a
// loopback address was `main` to both — so `main/` carried 127.0.0.1, and a
// release mapped to `main` shipped a cleartext address that exists on one
// laptop. `link` asks this too, with the same target.
func stackEnvName(t Target) string {
	if t.Local || isLoopbackAddress(t.URL) {
		return localEnvName
	}
	return soleEnvName
}
```
  (b) `internal/backend/project_link.go` `runLinkPrepared` (~895)
```go
	linkedEnv := o.linkedEnv
	if linkedEnv == "" {
		// A stack somebody runs has ONE environment and the app knows it as
		// `main` — see Resolved.ArtifactEnv for why that constant survives only
		// here and not for cloud checkouts.
		linkedEnv = soleEnvName
	}
```
  şununla değiştir:
```go
	linkedEnv := o.linkedEnv
	if linkedEnv == "" {
		// A stack with no project has ONE environment, and it takes the name
		// `palbase spec` gives it (stackEnvName, FR-010): `local` when it is on
		// this machine, `main` when somebody hosts it. The start record is asked
		// as writeLinkRecord asks it, because `palbase start --lan` records an
		// address that is not loopback and is still this machine.
		running, started := startRecordAt(o.checkoutRoot)
		linkedEnv = stackEnvName(Target{URL: base, Local: started && sameStack(running.URL, base)})
	}
```
  (c) `internal/backend/app_environments.go` `gatherEnvironments`'ta (T009'un yazdığı)
```go
	// The stack on this machine, when there is one and it is not already the
	// target.
	localURL, looked := findLocalStack(primary)
```
  şununla değiştir:
```go
	// The stack on this machine, when there is one and it is not already the
	// target. A target this link names `local` IS that stack (FR-010): another
	// one found by group would overwrite the address the person linked.
	if defaultEnv == localEnvName {
		return envs, specs, nil
	}
	localURL, looked := findLocalStack(primary)
```
  (d) `internal/backend/stack_spec.go` `refreshSpec` (~91)
```go
	env := resolved.ArtifactEnv()
	if target.Local {
		env = localEnvName
	}
	// THE LINK'S GATE, BEFORE ANY NETWORK (FR-002). This writes in the real
	// checkout, not a stage, so a name that is not one directory went straight
	// where it pointed: `../../gradle` wrote gradle/openapi.json, `..` wrote the
	// retired layout's marker and every later link refused the checkout.
	if why := whyNotWritable(env, target.Local); why != "" {
```
  şununla değiştir:
```go
	// A stack on this machine is `local` here exactly as it is to `link`
	// (stackEnvName, FR-010).
	env := resolved.ArtifactEnv()
	// THE LINK'S GATE, BEFORE ANY NETWORK (FR-002). This writes in the real
	// checkout, not a stage, so a name that is not one directory went straight
	// where it pointed: `../../gradle` wrote gradle/openapi.json, `..` wrote the
	// retired layout's marker and every later link refused the checkout.
	// `local` is refused only when a PROJECT names it: an environment no project
	// names is the stack itself, and stackEnvName put it there.
	if why := whyNotWritable(env, resolved.Env == ""); why != "" {
```
- [ ] **Adım 4: Yeşil + düşen eski testleri GÖR** — Run: Adım 2'deki komut `-v` ile · Beklenen: üç `--- PASS`. Sonra `go test ./internal/backend/ -count=1` · Beklenen (ölçüldü): yukarıda sayılan dokuz test FAIL, ör. `project_link_test.go:186: no config for main/ios: open palbase/environments/main/ios-config.json: no such file or directory`, `project_link_test.go:340: no config for main: … LINK: wrote palbase/environments/local/ios-config.json`, `link_rerun_test.go:349: … "{\"environment_ref\":\"main\",\"base_url\":\"https://stub\",…}" does not contain "http://127.0.0.1:…"`; artı B16.
- [ ] **Adım 5: Eski testleri bilerek güncelle** —
  - `internal/backend/project_link_test.go`:
    - `TestTheAppConfigCarriesThePUBLISHABLEKey`: `readEnvConfig(t, "main", "ios")` → `readEnvConfig(t, localEnvName, "ios")`; `os.ReadFile(ConfigPath("main", "ios"))` → `os.ReadFile(ConfigPath(localEnvName, "ios"))`.
    - `TestLinkingWithoutACredentialWritesNOTHING`: `os.Stat(ConfigPath("main", "ios"))` → `os.Stat(ConfigPath(localEnvName, "ios"))`.
    - `TestLinkingForWebWritesTheWebGeneratorsInputs`: `ConfigPath("main", webPlatform)` → `ConfigPath(localEnvName, webPlatform)`, `SpecPath("main")` → `SpecPath(localEnvName)`, `GeneratedPath("main", "ios")` → `GeneratedPath(localEnvName, "ios")`, `PlistPath("main")` → `PlistPath(localEnvName)`.
    - `TestTheWebConfigDoesNotResurrectARemovedField`: `EnvDir("main")` → `EnvDir(localEnvName)`; iki `ConfigPath("main", webPlatform)` → `ConfigPath(localEnvName, webPlatform)`.
    - `TestTheAppConfigCarriesTheStacksSealingRoot` ve `TestAStackWithNoSealingRootStillLinks`: `readEnvConfig(t, "main", "ios")` → `readEnvConfig(t, localEnvName, "ios")`.
    - `TestAnUnsupportedPlatformIsRefusedBeforeAnythingIsWritten`: listedeki `ConfigPath("main", webPlatform)` ve `SpecPath("main")` → `ConfigPath(localEnvName, webPlatform)` ve `SpecPath(localEnvName)`.
    - `TestTheSlotCarriesEveryEnvironment`:
```go
	// …and the project this checkout is being linked to.
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out); err != nil {
```
      bloğunu şununla değiştir (fonksiyonun geri kalanı — `main` ve `local` döngüsü dahil — aynı):
```go
	// …and the project this checkout is being linked to. A PROJECT, not an
	// address: a loopback address linked by hand is this machine's stack
	// itself (FR-010), and would be the `local` this test registers above.
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	routeEnvironments(t, map[string]string{"mainref000": srv.URL})
	o := linkOpts{
		url:          srv.URL,
		platforms:    []string{"ios"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	if err := runLink(context.Background(), o, &out); err != nil {
```
  - `internal/backend/link_rerun_test.go` (`installStubCodegen` fikstürü `main/web-config.json`'u önceden yazıyor; bu yüzden "main yazılmadı" kontrolü dizinin yokluğu değil, fikstürün baytlarının değişmemesidir):
    - `TestANoTargetLinkFollowsALoopbackInstallBeforeTheProjectRecord`:
```go
	assert.Equal(t, installed.URL, o.url, "a no-target link bound the project while this checkout acts on the install linked here")
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	raw, err := os.ReadFile(ConfigPath("main", webPlatform))
	require.NoError(t, err)
	assert.Contains(t, string(raw), installed.URL)
	assert.NoFileExists(t, ConfigPath("staging", webPlatform))
}
```
      →
```go
	assert.Equal(t, installed.URL, o.url, "a no-target link bound the project while this checkout acts on the install linked here")
	mainBefore, err := os.ReadFile(ConfigPath("main", webPlatform)) // installStubCodegen's
	require.NoError(t, err)
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	raw, err := os.ReadFile(ConfigPath(localEnvName, webPlatform))
	require.NoError(t, err)
	assert.Contains(t, string(raw), installed.URL)
	mainAfter, err := os.ReadFile(ConfigPath("main", webPlatform))
	require.NoError(t, err)
	assert.Equal(t, string(mainBefore), string(mainAfter), "the install linked here was written as the project's main")
	assert.NoFileExists(t, ConfigPath("staging", webPlatform))
}
```
    - `TestAnAuthRefreshFollowsALoopbackInstallBeforeTheProjectRecord`:
```go
	installed := loopbackInstallOverAProject(t)
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", webPlatform),
		[]byte(`{"app_id":"project","base_url":"`+installed.URL+`","api_key":"`+linkKeyCanary+`"}`+"\n"), 0o600))

	var out strings.Builder
	require.NoError(t, RefreshLinkedClients(context.Background(), &out), out.String())
	raw, err := os.ReadFile(ConfigPath("main", webPlatform))
```
      →
```go
	installed := loopbackInstallOverAProject(t)
	// What an earlier link of the install left: the refresh must rewrite it
	// FROM the install, so its address starts out as some other one.
	require.NoError(t, os.MkdirAll(EnvDir(localEnvName), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath(localEnvName, webPlatform),
		[]byte(`{"app_id":"project","base_url":"https://stale.example","api_key":"`+linkKeyCanary+`"}`+"\n"), 0o600))

	var out strings.Builder
	require.NoError(t, RefreshLinkedClients(context.Background(), &out), out.String())
	raw, err := os.ReadFile(ConfigPath(localEnvName, webPlatform))
```
    - `TestANoTargetLinkFollowsTheStartStack`:
```go
	assert.Equal(t, stack.URL, o.url)
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	raw, err := os.ReadFile(ConfigPath("main", webPlatform))
	require.NoError(t, err)
	assert.Contains(t, string(raw), stack.URL)
```
      →
```go
	assert.Equal(t, stack.URL, o.url)
	mainBefore, err := os.ReadFile(ConfigPath("main", webPlatform)) // installStubCodegen's
	require.NoError(t, err)
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	raw, err := os.ReadFile(ConfigPath(localEnvName, webPlatform))
	require.NoError(t, err)
	assert.Contains(t, string(raw), stack.URL)
	mainAfter, err := os.ReadFile(ConfigPath("main", webPlatform))
	require.NoError(t, err)
	assert.Equal(t, string(mainBefore), string(mainAfter), "the stack palbase start runs here was written as main/")
```
  - `internal/backend/social_link_test.go`: `TestFirstLinkCannotSucceedWithoutWebGenerator` içinde `require.NoFileExists(t, ConfigPath("main", webPlatform))` → `require.NoFileExists(t, ConfigPath(localEnvName, webPlatform))`; `TestLinkPublishesInstalledWebDependencies` içinde `GeneratedPath("main", webPlatform)` → `GeneratedPath(localEnvName, webPlatform)`.
  Güncellenen testlerin anlamlı olduğu ölçüldü: bu görevin uygulaması geri alınıp (yalnız `environments.go`, `project_link.go`, `stack_spec.go`, `app_environments.go`) güncel testler koşulunca sekizi düşer; `TestAnAuthRefresh…` ise `"{\"app_id\":\"project\",\"base_url\":\"https://stale.example\",…}" does not contain "http://127.0.0.1:…"` · `the refresh re-pointed the app at the cloud while every verb acts on the install` ile düşer.
- [ ] **Adım 6: Paket yeşil** — Run: `gofmt -l .` · Beklenen: boş; `go vet ./...` temiz; `go test ./internal/backend/ ./cmd/... -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/cmd/palbase`; `internal/backend`'te yalnız B16 FAIL (Global Constraints; bu scratch'te: `TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`).
- [ ] **Adım 7: Commit** — `git add internal/backend/link_local_stack_test.go internal/backend/environments.go internal/backend/project_link.go internal/backend/app_environments.go internal/backend/stack_spec.go internal/backend/project_link_test.go internal/backend/link_rerun_test.go internal/backend/social_link_test.go && git commit -m "fix(link,spec): bu makinedeki yığın (start kaydı ya da loopback adres) link'e de spec'e de local — main/ loopback taşımaz (FR-010)" -- internal/backend/link_local_stack_test.go internal/backend/environments.go internal/backend/project_link.go internal/backend/app_environments.go internal/backend/stack_spec.go internal/backend/project_link_test.go internal/backend/link_rerun_test.go internal/backend/social_link_test.go`

---

### T012: App checkout'unda `.gitignore` `palbase/environments/local/`'u taşır
<!-- deps: [T011] | files: [internal/backend/link_local_ignore_test.go, internal/backend/project_link.go, internal/backend/generated_paths.go, internal/backend/gitignore_scaffold_test.go, internal/backend/gitignore_layout_test.go, internal/backend/link_layout_test.go, internal/backend/project_link_test.go] | satisfies: [FR-020] -->

D-014 (kullanıcı onayladı, 2026-09-26): `local/` bu makinenin portunu ve anahtarını taşıyor; D-003 ile debug varsayılanı `local` olduğundan commit edilirse takım arkadaşının debug build'i son link yapanın laptop yığınını derler. Link, ortam başına dosya yazdığı her checkout'ta `.gitignore`'a `palbase/environments/local/` koyar: dosya yoksa oluşur (`takeBackRetiredIgnoreRules`'un ekosistem iskeleti + bu satır), satır yoksa sona eklenir (dosyanın kendi satır sonuyla; son satırı `\n`'siz bir dosya önce kapatılır), git'in aynı okuduğu bir yazımıyla (`/palbase/environments/local`, sonda `/`'lı ya da `/`'suz) zaten varsa dosyaya **hiç** dokunulmaz. İstemcisi olmayan (yalnız backend) bir checkout'a satır eklenmez — orada `local/` yok. Eklendiğinde link bir satırla söyler.

**Mevcut testleri BİLEREK değiştirir.** FR-012a/FR-012 (eski koşu) "link var olan bir `.gitignore`'a tek satır eklemez, boş olana da" diyordu; D-014 bu kurala tek bir istisna açar. Uygulamadan sonra şu testler ölçülerek düşer ve güncellenir: `TestGitignoreIsScaffoldedOnlyWhereThereIsNone/link`, `TestGitignoreAnExistingFileGainsNoRule/{boş_dosya,yalnız_boş_satır,kürate_edilmiş}/link`, `TestGitignoreTakesBackOnlyItsRetiredRules/link` (gitignore_scaffold_test.go), `TestLinkLayoutWritesNoIgnoreRules` (link_layout_test.go — adı `TestLinkLayoutWritesOnlyTheLocalIgnoreRule` olur), `TestLinkWiresAWebCheckoutEndToEnd` (project_link_test.go). `init` alt testleri ve `generatedProjectPaths`/`gitignoreScaffold`'u ölçen testler DEĞİŞMEZ: iskelet (init'in de kullandığı) `palbase` içeren bir kural taşımaya devam etmez; satırı yalnız link, yalnız app checkout'unda ekler.

**Interfaces:**
- Consumes: `takeBackRetiredIgnoreRules`, `writesPerEnvironmentArtifacts`, `EnvDir(localEnvName)`; T009'un `linkKeyMain`/`seedAndroidApp`/`stackServing`/`linkedAs`.
- Produces: `func ignoreThisMachinesStack(path string) (bool, error)` (`project_link.go`); link satırı `added palbase/environments/local/ to .gitignore — it holds this machine's stack address and key, which no teammate's build should use`. Test sabitleri `localIgnoreLine = "palbase/environments/local/"`, `localIgnoreAdded`; yardımcı `androidLinkHere(t) string` (`link_local_ignore_test.go`).

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_local_ignore_test.go`:
```go
package backend

// link_local_ignore_test.go — `palbase/environments/local/` IS THIS MACHINE'S,
// SO GIT NEVER SEES IT (FR-020, D-014).
//
// Everything else `link` writes under `palbase/` is the project's and is
// committed (FR-012). `local/` is the one directory that is not: it carries the
// port this machine's stack listens on and that stack's key. Committed, it was
// every teammate's debug build — the plugin's debug default is `local` (D-003).

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localIgnoreLine is the one rule `link` adds, and the sentence it says when it
// does.
const (
	localIgnoreLine  = "palbase/environments/local/"
	localIgnoreAdded = "added palbase/environments/local/ to .gitignore — it holds this machine's stack address and key, " +
		"which no teammate's build should use\n"
)

// androidLinkHere links an Android checkout to a stack on this machine and
// returns what link said.
func androidLinkHere(t *testing.T) string {
	t.Helper()
	seedAndroidApp(t)
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())
	return out.String()
}

func TestAnAppLinkKeepsThisMachinesStackOutOfGit(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"curated", "dist/\n# mine\n", "dist/\n# mine\n" + localIgnoreLine + "\n"},
		{"no final newline", "dist/", "dist/\n" + localIgnoreLine + "\n"},
		{"empty", "", localIgnoreLine + "\n"},
		{"CRLF", "dist/\r\n", "dist/\r\n" + localIgnoreLine + "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inScratchCheckout(t)
			require.NoError(t, os.WriteFile(".gitignore", []byte(tc.before), 0o644))

			out := androidLinkHere(t)

			got, err := os.ReadFile(".gitignore")
			require.NoError(t, err)
			assert.Equal(t, tc.after, string(got))
			assert.Contains(t, out, localIgnoreAdded)
		})
	}
}

// A RULE ALREADY THERE, in any spelling git reads the same, is left as it is —
// and so is the file around it.
func TestAnAppLinkLeavesALocalRuleThatIsAlreadyThere(t *testing.T) {
	for _, rule := range []string{"palbase/environments/local/", "/palbase/environments/local", "palbase/environments/local"} {
		t.Run(rule, func(t *testing.T) {
			inScratchCheckout(t)
			before := "dist/\n" + rule + "\n# mine"
			require.NoError(t, os.WriteFile(".gitignore", []byte(before), 0o644))

			out := androidLinkHere(t)

			got, err := os.ReadFile(".gitignore")
			require.NoError(t, err)
			assert.Equal(t, before, string(got))
			assert.NotContains(t, out, "to .gitignore")
		})
	}
}

// A CHECKOUT WITH NO CLIENT GETS NO `local/`, so it gets no rule for one.
func TestABackendLinkAddsNoLocalRule(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.WriteFile(".gitignore", []byte("dist/\n"), 0o644))
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())

	got, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	assert.Equal(t, "dist/\n", string(got))
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestAnAppLinkKeepsThisMachinesStackOutOfGit|TestAnAppLinkLeavesALocalRuleThatIsAlreadyThere|TestABackendLinkAddsNoLocalRule' -count=1 -v` · Beklenen: **FAIL** — `TestAnAppLinkKeepsThisMachinesStackOutOfGit`'in dört alt testi: `expected: "dist/\n# mine\npalbase/environments/local/\n"` / `actual  : "dist/\n# mine\n"`, `expected: "dist/\npalbase/environments/local/\n"` / `actual  : "dist/"`, `expected: "palbase/environments/local/\n"` / `actual  : ""`, `expected: "dist/\r\npalbase/environments/local/\r\n"` / `actual  : "dist/\r\n"`. `TestAnAppLinkLeavesALocalRuleThatIsAlreadyThere` ve `TestABackendLinkAddsNoLocalRule` bugün de geçer (bekçi).
- [ ] **Adım 3: Uygula** —
  (a) `internal/backend/project_link.go` `runLinkPrepared` (~933)
```go
	if err := takeBackRetiredIgnoreRules(".gitignore"); err != nil {
		return fmt.Errorf("update .gitignore: %w", err)
	}
```
  şununla değiştir:
```go
	if err := takeBackRetiredIgnoreRules(".gitignore"); err != nil {
		return fmt.Errorf("update .gitignore: %w", err)
	}
	// AND THIS MACHINE'S STACK STAYS OUT OF GIT (FR-020) — wherever a link writes
	// per-environment files at all; a checkout with no client gets no local/.
	if writesPerEnvironmentArtifacts(platforms) {
		added, err := ignoreThisMachinesStack(".gitignore")
		if err != nil {
			return fmt.Errorf("update .gitignore: %w", err)
		}
		if added {
			fmt.Fprintf(w, "added %s/ to .gitignore — it holds this machine's stack address and key, "+
				"which no teammate's build should use\n", EnvDir(localEnvName))
		}
	}
```
  ve `takeBackRetiredIgnoreRules`'un hemen arkasına (`refuseUnsupportedPlatforms`'un doc yorumundan önce) ekle:
```go
// ignoreThisMachinesStack makes sure the ignore file at path keeps
// `palbase/environments/local/` out of git, and reports whether it had to add
// the rule (FR-020, D-014).
//
// THE ONE RULE THIS CLI ADDS TO A FILE SOMEBODY WROTE — the exception to
// FR-012, because of whose directory it is. Everything else under `palbase/`
// is the project's and is committed; `local/` is this machine's: the port its
// stack listens on and that stack's key. Committed, it became every teammate's
// debug build, whose default environment is `local` (D-003) — a clone built
// against the laptop of whoever linked last. A rule already there, in any
// spelling git reads the same, leaves the file exactly as it is; a new one
// follows the file's own line ending.
func ignoreThisMachinesStack(path string) (bool, error) {
	dir := EnvDir(localEnvName)
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	body := string(content)
	for _, line := range strings.Split(body, "\n") {
		if strings.Trim(strings.TrimSpace(line), "/") == dir {
			return false, nil
		}
	}
	eol := "\n"
	if strings.Contains(body, "\r\n") {
		eol = "\r\n"
	}
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += eol
	}
	body += dir + "/" + eol
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	return true, os.WriteFile(path, []byte(body), mode)
}
```
  (b) `internal/backend/generated_paths.go` `generatedProjectPaths`'in doc yorumunun sonuna (satır `var generatedProjectPaths = []struct {`'tan önce):
```go
// subject is guessed is a rule that measures something else (review-T015).
var generatedProjectPaths = []struct {
```
  →
```go
// subject is guessed is a rule that measures something else (review-T015).
//
// `palbase/environments/local/` is not here, on purpose: it is this machine's,
// not a generated path every checkout ignores, and only `link` in an app
// checkout adds its rule (ignoreThisMachinesStack, FR-020).
var generatedProjectPaths = []struct {
```
- [ ] **Adım 4: Yeşil + düşen eski testleri GÖR** — Run: Adım 2'deki komut · Beklenen: üç test `--- PASS`. Sonra `go test ./internal/backend/ -count=1` · Beklenen (ölçüldü): `TestGitignoreIsScaffoldedOnlyWhereThereIsNone/link`, `TestGitignoreAnExistingFileGainsNoRule/…/link` ×3, `TestGitignoreTakesBackOnlyItsRetiredRules/link`, `TestLinkLayoutWritesNoIgnoreRules` ve `project_link_test.go:859: .gitignore still ignores a palbase path:` (`TestLinkWiresAWebCheckoutEndToEnd`) FAIL; artı B16.
- [ ] **Adım 5: Eski testleri bilerek güncelle** —
  - `internal/backend/gitignore_scaffold_test.go`: dosya başı yorumunda `// overwrite.` satırından sonra ekle:
```go
//
// ONE RULE IS THE EXCEPTION, and only `link` in an app checkout adds it:
// `palbase/environments/local/`, this machine's stack (FR-020, D-014). The link
// subtests below carry it; link_local_ignore_test.go pins the rule itself.
```
    `TestGitignoreIsScaffoldedOnlyWhereThereIsNone`'ın `link` alt testinde `requireEcosystemScaffold(t, string(body))` →
```go
		// The ecosystem's rules, and — this is an app checkout — the one rule
		// for this machine's stack (FR-020). Nothing else of this CLI's.
		require.Equal(t, gitignoreScaffold()+localIgnoreLine+"\n", string(body))
```
    `TestGitignoreAnExistingFileGainsNoRule`'un doc satırı ve `link` alt testinin iddiası:
```go
// FR-012: VAR OLAN BİR DOSYAYA TEK SATIR EKLENMEZ — BOŞ OLANA DA.
```
    →
```go
// FR-012: VAR OLAN BİR DOSYAYA TEK SATIR EKLENMEZ — BOŞ OLANA DA. Tek istisna
// FR-020: bir app checkout'unda `link` bu makinenin yığınının kuralını ekler
// (D-014), ve başka hiçbir satır eklemez.
```
    ve
```go
				require.Equal(t, tc.body, string(got), "`link` wrote into a .gitignore the checkout already had")
```
    →
```go
				require.Equal(t, tc.body+localIgnoreLine+"\n", string(got),
					"`link` wrote into a .gitignore the checkout already had — more than this machine's stack rule")
```
    `TestGitignoreTakesBackOnlyItsRetiredRules`'un `link` alt testinde `require.Equal(t, want, string(got))` →
```go
		// …and this machine's stack rule (FR-020), in the file's own line ending.
		require.Equal(t, want+localIgnoreLine+"\r\n", string(got))
```
  - `internal/backend/link_layout_test.go`:
```go
// AND `link` NO LONGER REPAIRS AN IGNORE FILE.
//
// It has nothing to ignore: everything it writes is committed. Leaving the call
// in would keep editing somebody's `.gitignore` to add rules for files that no
// longer exist.
func TestLinkLayoutWritesNoIgnoreRules(t *testing.T) {
```
    →
```go
// AND `link` NO LONGER REPAIRS AN IGNORE FILE.
//
// It has almost nothing to ignore: everything it writes is committed except
// `palbase/environments/local/`, this machine's stack (FR-020, D-014). Leaving
// the old repair in would keep editing somebody's `.gitignore` to add rules for
// files that no longer exist; that one rule is the whole edit.
func TestLinkLayoutWritesOnlyTheLocalIgnoreRule(t *testing.T) {
```
    ve aynı fonksiyonun son satırı ``require.Equal(t, mine, string(body), "`link` edited the checkout's ignore file")`` →
```go
	require.Equal(t, mine+localIgnoreLine+"\n", string(body), "`link` edited the checkout's ignore file beyond the local rule")
```
  - `internal/backend/gitignore_layout_test.go` dosya başı yorumunda `// their environment's contract, out of the history that is supposed to carry it.` satırından sonra ekle:
```go
// `palbase/environments/local/` is the one directory that is this MACHINE's —
// `link` ignores it in an app checkout (FR-020), never through the scaffold.
```
  - `internal/backend/project_link_test.go` `TestLinkWiresAWebCheckoutEndToEnd`'in son bloğu:
```go
	// AND NO IGNORE FILE IS INVENTED. A curated checkout keeps its own rules;
	// one that has none is left alone, because this CLI has nothing to ignore:
	// everything it writes here is committed. `link` only writes a `.gitignore`
	// where it also scaffolds the project.
	if ignore, err := os.ReadFile(".gitignore"); err == nil {
		if strings.Contains(strings.ToLower(string(ignore)), "palbase") {
			t.Errorf(".gitignore still ignores a palbase path:\n%s", ignore)
		}
	}
```
    →
```go
	// AND THE IGNORE FILE NAMES ONE PALBASE PATH AT MOST: everything this CLI
	// writes here is committed except this machine's stack, `local/` (FR-020).
	if ignore, err := os.ReadFile(".gitignore"); err == nil {
		for _, line := range strings.Split(string(ignore), "\n") {
			if strings.Contains(strings.ToLower(line), "palbase") && line != localIgnoreLine {
				t.Errorf(".gitignore ignores a palbase path other than this machine's stack: %q\n%s", line, ignore)
			}
		}
	}
```
- [ ] **Adım 6: Paket yeşil** — Run: `go test ./internal/backend/ -run 'TestAnAppLink|TestABackendLinkAddsNoLocalRule|TestGitignore|TestLinkLayoutWritesOnlyTheLocalIgnoreRule|TestLinkTakesBackARetiredIgnoreRule|TestACreatedGitignore|TestScaffolded|TestLinkWiresAWebCheckoutEndToEnd' -count=1 -v` · Beklenen: hepsi `--- PASS`, `ok  	github.com/palgroup/palbase-cli/internal/backend`; ardından `gofmt -l .` boş ve `go test ./internal/backend/ -count=1` · Beklenen: yalnız B16 FAIL (Global Constraints; bu scratch'te: `TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`).
- [ ] **Adım 7: Commit** — `git add internal/backend/link_local_ignore_test.go internal/backend/project_link.go internal/backend/generated_paths.go internal/backend/gitignore_scaffold_test.go internal/backend/gitignore_layout_test.go internal/backend/link_layout_test.go internal/backend/project_link_test.go && git commit -m "feat(link): app checkout'unda .gitignore palbase/environments/local/ taşır — dosya yoksa oluşur, satır yoksa eklenir, varsa dokunulmaz (FR-020, D-014)" -- internal/backend/link_local_ignore_test.go internal/backend/project_link.go internal/backend/generated_paths.go internal/backend/gitignore_scaffold_test.go internal/backend/gitignore_layout_test.go internal/backend/link_layout_test.go internal/backend/project_link_test.go`

---

### T013: `palbase doctor` git'in takip ettiği `palbase/environments/local/`'u uyarır
<!-- deps: [T012] | files: [cmd/palbase/doctor_local_test.go, cmd/palbase/doctor.go, internal/backend/generated_paths.go] | satisfies: [FR-020] -->

Bir ignore kuralı zaten commit'lenmiş bir dosyayı takipten çıkarmaz: T012'den önce `local/`'u commit'lemiş bir depoda her klon hâlâ son link yapanın yığınını derler. Doctor bunu, **git'in kendi index'ine** sorarak (`git ls-files -- :(icase)palbase/environments/local`) görür ve `git rm -r --cached palbase/environments/local` önerisini basar. Yalnız kanıt varsa konuşur: git yoksa ya da cevap veremiyorsa "hayır" (silme koruyan `gitTracks`'in tersine — o, cevapsızlıkta "izleniyor" der). Diskte olup ignore edilmiş `local/` sağlıklı hâldir; satır basılmaz.

**Interfaces:**
- Consumes: `insideAGitCheckout`, `withoutGitLocation`, `EnvDir`, `localEnvName` (`internal/backend`); `probeLine`, `newRootCmd` (`cmd/palbase`).
- Produces: `func CommittedLocalEnvironment(dir string) bool` (EXPORTED, `internal/backend/generated_paths.go`); `func localStackProbes(dir string, committed func(dir string) bool) []probeLine` (`cmd/palbase/doctor.go`); doctor satırı ``  ✗ local      palbase/environments/local/ is committed — it holds one machine's stack address and key, which every teammate's debug build would use; `git rm -r --cached palbase/environments/local`, then commit``. Test yardımcıları `gitRepoIn(t, dir) func(args ...string)`, `runDoctorIn(t, dir) string`, `writeLocalConfig(t, dir)` (`cmd/palbase/doctor_local_test.go`) — FR-019'un doctor Android bölümü bunları yeniden kullanabilir.

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `cmd/palbase/doctor_local_test.go` (doctor'ı üretim kablolamasıyla, `newRootCmd()` üzerinden koşar; `env_route_test.go` aynı yolu kullanır):
```go
package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A COMMITTED `local/` IS NAMED (FR-020). `link` keeps
// `palbase/environments/local/` out of git from now on, but an ignore rule does
// not untrack what a repository already holds: every clone still builds its
// debug variant against the laptop that committed it.

// committedLocalAdvice is the doctor line for it, exactly as printed.
const committedLocalAdvice = "  ✗ local      palbase/environments/local/ is committed — it holds one machine's stack address and key, " +
	"which every teammate's debug build would use; `git rm -r --cached palbase/environments/local`, then commit\n"

// gitRepoIn makes dir a real git repository and runs git there; global and
// system configuration point at nothing, so a person's hooks never reach it.
func gitRepoIn(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH — whether a file is tracked cannot be measured")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %s\n%s", strings.Join(args, " "), out)
	}
	git("init", "-q")
	return git
}

// runDoctorIn runs the production `palbase doctor` in dir, against a cloud
// that answers nothing, and returns what it printed.
func runDoctorIn(t *testing.T, dir string) string {
	t.Helper()
	cloud := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(cloud.Close)
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_PLATFORM_URL", cloud.URL)
	t.Setenv("PALBASE_AUTH_URL", cloud.URL)
	t.Setenv("PALBASE_ACCESS_TOKEN", "")

	root := newRootCmd()
	root.SetArgs([]string{"doctor"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	require.NoError(t, root.Execute())
	return out.String()
}

func writeLocalConfig(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "palbase", "environments", "local", "android-config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"base_url":"http://127.0.0.1:54321"}`), 0o600))
}

func TestDoctorNamesACommittedLocalStackDirectory(t *testing.T) {
	dir := t.TempDir()
	git := gitRepoIn(t, dir)
	writeLocalConfig(t, dir)
	git("add", "--", "palbase")

	require.Contains(t, runDoctorIn(t, dir), committedLocalAdvice)
}

// ON DISK AND IGNORED, as `link` leaves it, is the healthy state: nothing said.
func TestDoctorSaysNothingOfALocalDirectoryGitIgnores(t *testing.T) {
	dir := t.TempDir()
	git := gitRepoIn(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("palbase/environments/local/\n"), 0o644))
	writeLocalConfig(t, dir)
	git("add", "-A")

	require.NotContains(t, runDoctorIn(t, dir), "✗ local")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./cmd/palbase/ -run 'TestDoctorNamesACommittedLocalStackDirectory|TestDoctorSaysNothingOfALocalDirectoryGitIgnores' -count=1 -v` · Beklenen: **FAIL** `TestDoctorNamesACommittedLocalStackDirectory` — doctor'ın çıktısı (``  ✗ link       this directory is not linked — run `palbase link <project>` here`` … `  ✓ bun        …`) `does not contain "  ✗ local      palbase/environments/local/ is committed — …"`. `TestDoctorSaysNothingOfALocalDirectoryGitIgnores` bugün de geçer (bekçi).
- [ ] **Adım 3: Uygula** —
  (a) `internal/backend/generated_paths.go`'da `gitTracks`'ten sonra, `insideAGitCheckout`'un doc yorumundan önce ekle:
```go
// CommittedLocalEnvironment reports whether git tracks a file under
// `palbase/environments/local/` in the checkout at dir: the directory `link`
// keeps out of git because it is this machine's (FR-020), and which an ignore
// rule cannot untrack once a repository holds it. Exported for `palbase doctor`.
//
// THE OPPOSITE DEFAULT TO gitTracks. That one answers "tracked" when git cannot
// be asked, because it guards a deletion; this one answers "no", because it
// raises a warning, and a warning without evidence is noise.
func CommittedLocalEnvironment(dir string) bool {
	if !insideAGitCheckout(dir) {
		return false
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	cmd := exec.Command(git, "-C", dir, "ls-files", "-z", "--", ":(icase)"+EnvDir(localEnvName))
	cmd.Env = withoutGitLocation(os.Environ())
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}
```
  (b) `cmd/palbase/doctor.go` `doctorCmd`'un `RunE`'sinde link satırlarını basan
```go
			for _, l := range linkProbes(backend.ReadLinkedProject, backend.ReadTarget,
				func() (backend.Resolved, error) { return backend.ResolveFor(cmd) }) {
				if l.ok {
					ok(l.label, l.detail)
				} else {
					bad(l.label, l.detail)
				}
			}
```
  döngüsünü şununla değiştir:
```go
			for _, l := range linkProbes(backend.ReadLinkedProject, backend.ReadTarget,
				func() (backend.Resolved, error) { return backend.ResolveFor(cmd) }) {
				if l.ok {
					ok(l.label, l.detail)
				} else {
					bad(l.label, l.detail)
				}
			}
			if wd, err := os.Getwd(); err == nil {
				for _, l := range localStackProbes(wd, backend.CommittedLocalEnvironment) {
					bad(l.label, l.detail)
				}
			}
```
  ve `firstLine`'dan sonra, `linkProbes`'un doc yorumundan önce ekle:
```go
// localStackProbes names a `palbase/environments/local/` git tracks in the
// checkout at dir (FR-020), and says nothing otherwise: on disk and ignored is
// how `link` leaves it. `link` keeps the directory out of git from now on, but
// an ignore rule does not untrack what a repository already holds — every clone
// would go on building its debug variant against the laptop that committed it.
func localStackProbes(dir string, committed func(dir string) bool) []probeLine {
	if !committed(dir) {
		return nil
	}
	return []probeLine{{
		label: "local",
		detail: "palbase/environments/local/ is committed — it holds one machine's stack address and key, " +
			"which every teammate's debug build would use; `git rm -r --cached palbase/environments/local`, then commit",
	}}
}
```
- [ ] **Adım 4: Yeşil** — Run: Adım 2'deki komut · Beklenen: `--- PASS: TestDoctorNamesACommittedLocalStackDirectory`, `--- PASS: TestDoctorSaysNothingOfALocalDirectoryGitIgnores`, `ok  	github.com/palgroup/palbase-cli/cmd/palbase`; sonra `go test ./cmd/... -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/cmd/palbase` (`TestNoUserFacingStringIsTurkish` dahil).
- [ ] **Adım 5: Commit** — `git add cmd/palbase/doctor.go cmd/palbase/doctor_local_test.go internal/backend/generated_paths.go && git commit -m "feat(doctor): git'in takip ettiği palbase/environments/local/ uyarılır, git rm -r --cached önerilir (FR-020, D-014)" -- cmd/palbase/doctor.go cmd/palbase/doctor_local_test.go internal/backend/generated_paths.go`

---

### T014: Artık listede olmayan ortamın dizini Android ve web link'inde de silinir; bu makinenin yığınının eski loopback `main/`'i her platformda
<!-- deps: [T002, T004, T007, T011] | files: [internal/backend/link_sweep_test.go, internal/backend/app_environments.go, internal/backend/project_link.go, internal/backend/app_environments_test.go, internal/backend/native_codegen_path_test.go, internal/backend/apple_sweep_test.go] | satisfies: [FR-011, FR-010] -->

**Interfaces:**
- Consumes: `runLinkPrepared` yerelleri `listed` (T001/T002: listenin buluttan geldiği hâli, "skipped is not gone") ve `unwritten` döngüsü (T004) · `envname.Label(name string) string` (T007) · projesiz, bu makineye yapılan link'in `envs.Default == localEnvName` olması (T011, `stackEnvName`) · `isLoopbackAddress` (`target.go`), `isGeneratedEnvironmentFile`, `soleEnvName`
- Produces: `removeStaleEnvironmentDirs(root string, keep []string, leftover string, w io.Writer) error` (yeni `leftover` parametresi) · sabitler `xcodeLeftover`, `selectedLeftover` · `shownEnvDir(name string) string` → `palbase/environments/<envname.Label(name)>` · `generateForEnvironmentsAt(ctx context.Context, envs appEnvironments, w io.Writer, toolRoot string) error` (`keep` parametresi KALKTI, artık süpürmüyor) · `generateForEnvironments` (spec/push yolu) süpürür, sonra üretir · `func removeThisMachinesOldMain(root string, w io.Writer) error` (satırlar: `removed palbase/environments/main (it held the stack on this machine, which is local/ now)` · `palbase/environments/main holds the stack on this machine under the name an older link gave it, and files Palbase did not write — move them aside, then delete it`) · test yardımcıları `seedLoopbackMain(t, platform, url string)`, `seedGeneratedEnvironment` (apple_sweep_test.go'dan taşındı), `seedEnvironment(t, env, platform string)`, `var sweepCheckouts` (ios/android/web kurulumu + beklenen `leftover` cümlesi) — hepsi `link_sweep_test.go`'da

Süpürme `runLinkPrepared`'da TEK yerde, her platform için, yazımlardan hemen sonra koşar (Apple için önce üreticinin içindeydi; artık snippet'ten ve web bağlamasından önce). Koşul `writesPerEnvironmentArtifacts(platforms) && (apple || len(listed) > 0)`: liste okunmamışsa (adresle link) Android/web'de hiçbir şey silinmez — "artık listede olmayan" diyebilecek bir liste yok; Apple bugünkü gibi yine süpürür, çünkü Xcode `palbase/environments` altındaki her dizini derliyor. `keep` = yazılanlar + listenin tamamı (okunamayan, atlanan, Failed/Deleting dahil); `local/`'u fonksiyon kendisi korur, çağıranın eklemesi gerekmez. Satırlar artık checkout'a göreli yol basıyor: süpürme stage'de koşuyor ve Mac'te stage adı `/var/…`, çalışma dizini `/private/var/…` döndüğü için satır `removed /private/private/var/…/featurex` diye basılıyordu (ölçüldü, aşağıdaki kırmızıda görülüyor). Yabancı dosya taşıyan dizin satırındaki Xcode cümlesi parametre oldu (Android checkout'unda Xcode demek yanlış). **Bilerek değişen testler:** `apple_sweep_test.go` silinir — iki testi (`TestAppleSweepKeepsEveryEnvironmentOfTheProject`, `TestAppleSweepStillRemovesAnEnvironmentTheProjectLost`) `generateForEnvironmentsAt`'a `keep` veriyordu, o parametre yok; aynı iki gerçeği (okunamayan listeli ortam kalır, listede olmayan gider) artık `TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas/ios` üretim yolundan (`runLink`) ölçer. `app_environments_test.go`'daki 5 çağrı `xcodeLeftover` argümanını, `native_codegen_path_test.go` yeni imzayı alır; hiçbir iddia gevşemez. `TestOrphanCleanupHasAProductionCaller` yeşil kalır (çağrı `app_environments.go`'da `generateForEnvironments` içinde).

**Bu makinenin yığınının eski adı da (FR-010 × FR-011).** T011'den önce `palbase start` yığınına ya da elle bağlanan loopback bir adrese link `main/` yazıyordu, içinde 127.0.0.1. T011 artık `local/` yazıyor, ama yukarıdaki koşul liste olmadan Android/web'de süpürmediği için eski `main/` kalıyordu — eleştirmen ölçtü: `/ios` geçiyor, `/android` ve `/web` `directory "palbase/environments/main" exists`. Bu yüzden projesiz ve ortamı `local` olan bir link, genel süpürmeden ÖNCE, her platformda `removeThisMachinesOldMain`'i koşar: `main/`'deki her `*-config.json` loopback bir `base_url` taşıyorsa ve dizinde yalnız CLI'ın dosyaları varsa `main/` silinir ve söylenir; bir config başka bir adres taşıyorsa (birinin `main`'i) dokunulmaz; CLI'ın yazmadığı bir dosya varsa söylenir ve bırakılır. Bu, "liste olmadan süpürme yok" kuralının bilinçli tek istisnası: silinen, projenin kaybettiği bir ortam değil, bu makinenin yığınının eski adı — lead için karar önerisi Fidelity Audit'te (D-024 önerisi).

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_sweep_test.go`:
```go
package backend

// link_sweep_test.go — AN ENVIRONMENT THE PROJECT NO LONGER HAS LEAVES THE
// CHECKOUT, whichever client reads it (FR-011).
//
// The sweep ran for Apple alone, so an Android or web checkout kept the
// directory of a deleted or renamed environment for ever. Measured (verification
// of 2026-09-25, B4): a `featurex/` whose tenant was gone survived an Android
// link without a word, and a build type named featureX went on compiling it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedEnvironment commits one environment's files for one platform, the way an
// earlier link left them.
func seedEnvironment(t *testing.T, env, platform string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(EnvDir(env), 0o755))
	require.NoError(t, os.WriteFile(SpecPath(env), []byte(`{"openapi":"3.1.0","paths":{}}`), 0o644))
	require.NoError(t, os.WriteFile(ConfigPath(env, platform), []byte(`{"base_url":"https://old.example"}`+"\n"), 0o600))
}

// sweepCheckouts are the three clients that write per-environment files, each
// set up the way its own link tests set it up.
var sweepCheckouts = []struct {
	platform string
	seed     func(t *testing.T)
	// leftover is what the line about a directory the sweep must not delete
	// tells THIS checkout's reader: Xcode compiles every directory, a Gradle or
	// web build only the one it selects.
	leftover string
}{
	{"ios", func(t *testing.T) { useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil) },
		`Xcode compiles everything under palbase/environments, so move it aside if the build reports "Multiple commands produce"`},
	{"android", seedAndroidApp,
		"a build that selects it still builds an environment this project no longer has, so move it aside"},
	{webPlatform, func(t *testing.T) { seedWebCheckout(t); installStubCodegen(t, "export {}") },
		"a build that selects it still builds an environment this project no longer has, so move it aside"},
}

// ONLY WHAT THE PROJECT NO LONGER HAS GOES. `staging` is listed but cannot be
// read this run, `broken` is Failed, `local/` is this machine's stack while it
// is down, and `mine/` holds a file Palbase never writes: all four stay.
// `featurex` is listed nowhere, and it goes — said, by its path in the checkout.
func TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas(t *testing.T) {
	for _, c := range sweepCheckouts {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			c.seed(t)
			for _, env := range []string{"featurex", "staging", "broken", localEnvName} {
				seedEnvironment(t, env, c.platform)
			}
			require.NoError(t, os.MkdirAll(EnvDir("mine"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(EnvDir("mine"), "NOTES.md"), []byte("mine\n"), 0o644))
			main := stackServing(t, linkKeyMain, nil)
			routeEnvironments(t, map[string]string{"mainref000": main.URL}) // staging has no route: it cannot be read
			o := linkOpts{
				url:       main.URL,
				platforms: []string{c.platform},
				linkedEnv: "main",
				product:   Product{ID: "prd_a", Name: "todoapp"},
				environments: []Environment{
					{Name: "main", Ref: "mainref000", Status: "Running"},
					{Name: "staging", Ref: "stagref000", Status: "Running"},
					{Name: "broken", Ref: "brokref000", Status: "Failed"},
				},
			}

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.Equal(t, []string{"broken", localEnvName, "main", "mine", "staging"},
				entriesIn(t, filepath.Join(RootDir(), envSubdir)))
			assert.Contains(t, out.String(), "removed palbase/environments/featurex (the project no longer has that environment)\n")
			assert.Contains(t, out.String(), "palbase/environments/mine belongs to no environment in this project and holds "+
				"files Palbase did not write — "+c.leftover+"\n")
			assert.FileExists(t, filepath.Join(EnvDir("mine"), "NOTES.md"))
		})
	}
}

// A LINK THAT READ NO LISTING TAKES NOTHING AWAY from an Android checkout. A
// stack linked by its address has one environment and no list of the others,
// so nothing here says `staging` is gone — and deleting a project's committed
// environments because this one link went to a bare address would be the worse
// mistake. `main/` stays too while its address is somebody's stack, not this
// machine's. (Apple still sweeps here: Xcode compiles every directory under
// palbase/environments, so for it a leftover is a broken build.)
func TestALinkWithNoProjectListingRemovesNoAndroidEnvironment(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	seedEnvironment(t, "staging", "android")
	seedEnvironment(t, "main", "android")
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())

	assert.FileExists(t, ConfigPath("staging", "android"))
	assert.FileExists(t, ConfigPath("main", "android"))
	assert.NotContains(t, out.String(), "removed ")
}

// seedLoopbackMain is what a link to the stack on this machine wrote before
// that stack was named `local`: main/, carrying its loopback address.
func seedLoopbackMain(t *testing.T, platform, url string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", platform),
		[]byte(`{"app_id":"project","base_url":"`+url+`","api_key":"`+linkKeyMain+`"}`+"\n"), 0o600))
	require.NoError(t, os.WriteFile(SpecPath("main"), []byte(`{"openapi":"3.1.0","paths":{}}`), 0o644))
}

// A LOOPBACK main/ AN OLDER LINK LEFT IS THIS MACHINE'S STACK UNDER THE WRONG
// NAME (FR-010 × FR-011). Before the stack `palbase start` runs here was named
// `local`, a link to it wrote main/ with 127.0.0.1 in it; now that stack is
// local/, and the old main/ is what a release mapped to main would ship. Apple
// swept it already; Android and web kept it, because a link with no listing
// swept nothing there.
func TestALinkToThisMachinesStackTakesAwayTheLoopbackMainAnOlderLinkLeft(t *testing.T) {
	for _, c := range sweepCheckouts {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			c.seed(t)
			stack := stackServing(t, linkKeyMain, nil)
			linkedAs(t, stack.URL, "a-credential")
			seedLoopbackMain(t, c.platform, stack.URL)

			var out strings.Builder
			o := linkOpts{url: stack.URL, platforms: []string{c.platform}}
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.FileExists(t, ConfigPath(localEnvName, c.platform))
			assert.NoDirExists(t, EnvDir("main"), "main/ still carries a loopback base_url:\n%s", out.String())
			assert.Contains(t, out.String(), "removed palbase/environments/main (it held the stack on this machine, which is local/ now)\n")
		})
	}
}

// AND ONE THAT HOLDS A FILE PALBASE NEVER WRITES IS SAID AND LEFT: whatever the
// address in it, the file is somebody's.
func TestAnOldLoopbackMainHoldingSomebodysFileIsSaidAndLeft(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")
	seedLoopbackMain(t, "android", stack.URL)
	require.NoError(t, os.WriteFile(filepath.Join(EnvDir("main"), "NOTES.md"), []byte("mine\n"), 0o644))

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL, platforms: []string{"android"}}, &out), out.String())

	assert.FileExists(t, filepath.Join(EnvDir("main"), "NOTES.md"))
	assert.Contains(t, out.String(), "palbase/environments/main holds the stack on this machine under the name an older link "+
		"gave it, and files Palbase did not write — move them aside, then delete it\n")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas|TestALinkWithNoProjectListingRemovesNoAndroidEnvironment|TestALinkToThisMachinesStackTakesAwayTheLoopbackMainAnOlderLinkLeft|TestAnOldLoopbackMainHoldingSomebodysFileIsSaidAndLeft' -count=1 -v` · Beklenen: **FAIL**:
  - `--- FAIL: TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas/android` ve `/web`: `actual  : []string{"broken", "featurex", "local", "main", "mine", "staging"}` (süpürme hiç koşmuyor); `/ios`: çıktıda `removed /private/private/var/folders/…/palbase/environments/featurex (the project no longer has that environment)` var — Apple süpürüyor ama yolu bozuk basıyor.
  - `--- FAIL: TestALinkToThisMachinesStackTakesAwayTheLoopbackMainAnOlderLinkLeft/android` ve `/web`: `Error: directory "palbase/environments/main" exists`; `/ios`: `main/` genel Apple süpürmesiyle gidiyor ama satır `removed /private/private/var/folders/…/palbase/environments/main …` — beklenen satır yok.
  - `--- FAIL: TestAnOldLoopbackMainHoldingSomebodysFileIsSaidAndLeft` (satır yok).
  - `TestALinkWithNoProjectListingRemovesNoAndroidEnvironment` bu adımda **PASS** — bilerek: bugün Android hiç süpürmediği için yeşil; Adım 3'ün `len(listed) > 0` koşulunu ve "yalnız loopback main" sınırını korur. (Ölçüldü: koşul kaldırılınca `unable to find file "palbase/environments/staging/android-config.json"` ile kırmızı.)
- [ ] **Adım 3: Uygula** —
  (a) `internal/backend/app_environments.go`: `// removeStaleEnvironmentDirs deletes the directory of an environment the project` doc yorumunun HEMEN ÜSTÜNE ekle:
```go
// What the line about a directory the sweep must leave behind tells the reader,
// by the build that reads palbase/environments: Xcode compiles every directory
// under it, Gradle and the web generator only the one a build selects.
const (
	xcodeLeftover    = `Xcode compiles everything under palbase/environments, so move it aside if the build reports "Multiple commands produce"`
	selectedLeftover = "a build that selects it still builds an environment this project no longer has, so move it aside"
)

```
  (b) aynı doc yorumunun son satırı `// until that folder was moved aside.` ile imza `func removeStaleEnvironmentDirs(root string, keep []string, w io.Writer) error {` arasını şununla değiştir (yorum paragrafı eklenir, imza `leftover` alır):
```go
// until that folder was moved aside.
//
// AND GRADLE BUILDS WHAT A BUILD TYPE NAMES (FR-011). An Android build selects
// its environment by name, so a deleted environment's directory is not inert
// there either: measured (verification of 2026-09-25, B4), `featurex/` outlived
// its tenant and the featureX build type went on compiling a dead address, with
// nothing failing.
func removeStaleEnvironmentDirs(root string, keep []string, leftover string, w io.Writer) error {
```
  (c) aynı fonksiyonda `local` yorumunun şu altı satırını
```go
		// directory holding committed products: this runs on the Apple branch
		// only, and `isGeneratedEnvironmentFile` counts the WEB client and its
		// config as ours, so an `ios` link in a checkout that is also a web one
		// would take `local/palbe.gen.ts` and `local/web-config.json` with it —
		// while `palbase/client.ts` went on re-exporting the file that had just
		// been deleted.
```
  şununla değiştir (süpürme artık yalnız Apple dalında koşmuyor):
```go
		// directory holding committed products: `isGeneratedEnvironmentFile`
		// counts every platform's files as ours, so an `ios` link in a
		// checkout that is also a web one would take `local/palbe.gen.ts` and
		// `local/web-config.json` with it — while `palbase/client.ts` went on
		// re-exporting the file that had just been deleted.
```
  (d) fonksiyonun sonundaki
```go
			fmt.Fprintf(w, "%s belongs to no environment in this project and holds files Palbase "+
				"did not write — Xcode compiles everything under palbase/environments, so move it "+
				"aside if the build reports \"Multiple commands produce\"\n", dir)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s (the project no longer has that environment)\n", dir)
	}
	return nil
}
```
  bloğunu şununla değiştir:
```go
			fmt.Fprintf(w, "%s belongs to no environment in this project and holds files Palbase "+
				"did not write — %s\n", shownEnvDir(e.Name()), leftover)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s (the project no longer has that environment)\n", shownEnvDir(e.Name()))
	}
	return nil
}

// shownEnvDir is how a line names one directory under palbase/environments.
//
// RELATIVE TO THE CHECKOUT, because the sweep runs inside the link's stage: an
// absolute path there is the stage's, and swapping the stage back for the
// checkout left `/private/private/var/…` on a Mac (measured — the stage is
// named under /var, the working directory reports /private/var). And a name
// that is not a plain word is quoted: it came from a listing an older CLI did
// not check (FR-006).
func shownEnvDir(name string) string {
	return path.Join(rootDir, envSubdir) + "/" + envname.Label(name)
}
```
  (e) aynı dosyada
```go
// generateForEnvironments emits one client per environment, and one plist for
// all of them.
func generateForEnvironments(ctx context.Context, envs appEnvironments, w io.Writer) error {
	return generateForEnvironmentsAt(ctx, envs, envs.names(), w, "")
}

// keep is every environment whose directory the sweep must leave alone. It is
// a parameter, not envs.names(): one link describes every environment of a
// project, and one it could not read this run is still the project's — its
// directory is kept even though the map carries no entry for it.
func generateForEnvironmentsAt(ctx context.Context, envs appEnvironments, keep []string, w io.Writer, toolRoot string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	// A LEFT-BEHIND ENVIRONMENT BREAKS THE BUILD, so it goes first.
	if err := removeStaleEnvironmentDirs(root, keep, w); err != nil {
		return err
	}

	if toolRoot == "" {
```
  bloğunu şununla değiştir:
```go
// generateForEnvironments emits one client per environment, and one plist for
// all of them — `palbase spec` and `push`'s path, which knows the environments
// only from the disk. `link` knows the project's listing and sweeps with it
// before it generates (runLinkPrepared).
func generateForEnvironments(ctx context.Context, envs appEnvironments, w io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	// A LEFT-BEHIND ENVIRONMENT BREAKS THE BUILD, so it goes first.
	if err := removeStaleEnvironmentDirs(root, envs.names(), xcodeLeftover, w); err != nil {
		return err
	}
	return generateForEnvironmentsAt(ctx, envs, w, "")
}

func generateForEnvironmentsAt(ctx context.Context, envs appEnvironments, w io.Writer, toolRoot string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}

	if toolRoot == "" {
```
  (f) `internal/backend/project_link.go` `runLinkPrepared`: `unwritten` döngüsünün (`fmt.Fprintf(w, "%s could not be written (%v) — skipped; …` … `delete(specs, name)` `}`) hemen ALTINA, `if apple {` / `// NOTHING IS WRITTEN INTO THE APP'S BUILD SYSTEM.` bloğundan ÖNCE ekle:
```go
	// AN ENVIRONMENT THE PROJECT NO LONGER HAS LEAVES THE CHECKOUT, whichever
	// client reads it (FR-011). This ran for Apple alone, so an Android or web
	// checkout kept a deleted environment's directory for ever.
	//
	// KEPT: every environment the project lists, whether or not this run could
	// read it (FR-016) or was allowed to write it (FR-003) — Failed and Deleting
	// ones included — and what was just written (C-10). `local/` the sweep
	// keeps by itself.
	//
	// ONLY WITH A LISTING, except for Apple. A link to a bare address has one
	// environment and no list of the others, so nothing here says the rest are
	// gone; Apple sweeps anyway, because Xcode compiles every directory under
	// palbase/environments and a leftover there is a broken build.
	//
	// EXCEPT THE STACK ON THIS MACHINE UNDER ITS OLD NAME (FR-010). A link that
	// names it `local` takes the loopback `main/` an older link wrote for it,
	// listing or not and on every platform: that is not an environment the
	// project lost, it is this one, still carrying 127.0.0.1 where a release
	// mapped to main would read it.
	if writesPerEnvironmentArtifacts(platforms) {
		stage, err := os.Getwd()
		if err != nil {
			return err
		}
		if len(listed) == 0 && envs.Default == localEnvName {
			if err := removeThisMachinesOldMain(stage, w); err != nil {
				return err
			}
		}
		if apple || len(listed) > 0 {
			keep := envs.names()
			for _, e := range listed {
				keep = append(keep, e.Name)
			}
			leftover := selectedLeftover
			if apple {
				leftover = xcodeLeftover
			}
			if err := removeStaleEnvironmentDirs(stage, keep, leftover, w); err != nil {
				return err
			}
		}
	}

```
  (g) aynı fonksiyonda Swift üretici bloğunu
```go
	if apple {
		// KEPT: every environment the project lists, whether or not this run
		// could read it (FR-016) or was allowed to write it (FR-003) — plus
		// `local` and what was just written (C-10).
		keep := envs.names()
		for _, e := range listed {
			keep = append(keep, e.Name)
		}
		keep = append(keep, localEnvName)
		if err := generateForEnvironmentsAt(ctx, envs, keep, w, o.checkoutRoot); err != nil {
			return err
		}
	}
```
  şununla değiştir:
```go
	if apple {
		if err := generateForEnvironmentsAt(ctx, envs, w, o.checkoutRoot); err != nil {
			return err
		}
	}
```
  (h) testler: `internal/backend/app_environments_test.go`'daki 5 çağrıda `removeStaleEnvironmentDirs(root, []string{"main"}, &out)` → `removeStaleEnvironmentDirs(root, []string{"main"}, xcodeLeftover, &out)` (satır ~37, ~133, ~143, ~162, ~170; bu testler Apple senaryosu). `internal/backend/native_codegen_path_test.go` ~30: `generateForEnvironmentsAt(context.Background(), envs, envs.names(), &out, "")` → `generateForEnvironmentsAt(context.Background(), envs, &out, "")`.
  (i) `git rm -q internal/backend/apple_sweep_test.go`; yardımcısını `internal/backend/link_sweep_test.go`'da import bloğunun altına, `seedEnvironment`'ın üstüne ekle:
```go
// seedGeneratedEnvironment leaves the generated Swift client of one
// environment in the checkout, the way an earlier Apple link did.
func seedGeneratedEnvironment(t *testing.T, root, env string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(EnvDir(env)))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "PalbaseGenerated.swift"), []byte("// generated"), 0o644))
	return dir
}

```
  (j) `internal/backend/app_environments.go`: `shownEnvDir`'in hemen ALTINA ekle (`encoding/json`, `errors`, `strings` dosyada zaten import'lu):
```go
// removeThisMachinesOldMain takes away the `main/` an older link wrote for the
// stack on THIS machine (FR-010).
//
// Before that stack was named `local`, a link to it — a `palbase start`
// record, or a loopback address linked by hand — wrote `main/` with 127.0.0.1
// in it, and a release mapped to main shipped an address that exists on one
// laptop. A link now writes that stack to `local/`, so a `main/` whose every
// config points at this machine is the same stack under its old name, and it
// goes. One whose config points anywhere else is somebody's main and stays;
// one that holds a file Palbase never writes is said and left to its owner.
func removeThisMachinesOldMain(root string, w io.Writer) error {
	dir := filepath.Join(root, filepath.FromSlash(EnvDir(soleEnvName)))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	loopback, foreign := false, false
	for _, e := range entries {
		switch {
		case !isGeneratedEnvironmentFile(e.Name()):
			foreign = true
		case strings.HasSuffix(e.Name(), "-config.json"):
			var config appEnvironment
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || json.Unmarshal(raw, &config) != nil || !isLoopbackAddress(config.BaseURL) {
				return nil
			}
			loopback = true
		}
	}
	switch {
	case !loopback:
		return nil
	case foreign:
		fmt.Fprintf(w, "%s holds the stack on this machine under the name an older link gave it, and files Palbase "+
			"did not write — move them aside, then delete it\n", shownEnvDir(soleEnvName))
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Fprintf(w, "removed %s (it held the stack on this machine, which is %s/ now)\n", shownEnvDir(soleEnvName), localEnvName)
	return nil
}
```
- [ ] **Adım 4: Yeşil** — Run: `gofmt -l . && go vet ./internal/backend/ && go test ./internal/backend/ -run 'TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas|TestALinkWithNoProjectListingRemovesNoAndroidEnvironment|TestALinkToThisMachinesStackTakesAwayTheLoopbackMainAnOlderLinkLeft|TestAnOldLoopbackMainHoldingSomebodysFileIsSaidAndLeft|TestOrphan|Sweep|TestNativeCodegenPath|TestAnAppleLinkKeepsTheFilesOfASkippedTwin|TestOneLinkWritesEveryReadableEnvironmentAndKeepsAFailedOnesFiles' -count=1 -v` · Beklenen: gofmt boş, vet temiz; `--- PASS: TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas` (`/ios`, `/android`, `/web`), `--- PASS: TestALinkWithNoProjectListingRemovesNoAndroidEnvironment`, `--- PASS: TestALinkToThisMachinesStackTakesAwayTheLoopbackMainAnOlderLinkLeft` (`/ios`, `/android`, `/web`), `--- PASS: TestAnOldLoopbackMainHoldingSomebodysFileIsSaidAndLeft`, `--- PASS: TestOrphanCleanupHasAProductionCaller`, `--- PASS: TestAnAppleLinkKeepsTheFilesOfASkippedTwin`, `--- PASS: TestASweptEnvironmentLeavesNoDirectoryInTheCheckout`, `--- PASS: TestNativeCodegenPath`, `--- PASS: TestOneLinkWritesEveryReadableEnvironmentAndKeepsAFailedOnesFiles`, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Loopback kuralının bekçisi ölçüldü: (j) ve (f)'deki `removeThisMachinesOldMain` çağrısı olmadan (yalnız genel süpürme) `/android` ve `/web` `directory "palbase/environments/main" exists`, `/ios` satır farkıyla düşer. Paketin tamamı (`go test ./internal/backend/ -count=1`): yalnız B16 FAIL (Global Constraints; bu scratch'te: `TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`), `FAIL	github.com/palgroup/palbase-cli/internal/backend	104.027s`. Büyük/küçük harf duyarlı bir diskte de (burada `hdiutil create -size 300m -fs "Case-sensitive APFS" -volname revcs -type SPARSE cs.sparseimage` + `hdiutil attach -nobrowse -mountpoint <dir> cs.sparseimage`, ardından `TMPDIR=<dir>/tmp go test …`) `TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas` ve `TestALinkToThisMachinesStackTakesAwayTheLoopbackMainAnOlderLinkLeft` PASS.
- [ ] **Adım 5: Commit** — `git add internal/backend/app_environments.go internal/backend/project_link.go internal/backend/app_environments_test.go internal/backend/native_codegen_path_test.go internal/backend/link_sweep_test.go && git commit -m "fix(link): artık listede olmayan ortamın dizini Android ve web link'inde de silinir, bu makinenin yığınının eski loopback main/'i her platformda; satırlar checkout'a göreli yol basar (FR-011, FR-010)" -- internal/backend/app_environments.go internal/backend/project_link.go internal/backend/app_environments_test.go internal/backend/native_codegen_path_test.go internal/backend/link_sweep_test.go internal/backend/apple_sweep_test.go` (silinen dosya pathspec'te: `git rm` onu index'ten zaten çıkardı; ölçüldü, commit silmeyi taşıyor)

---

### T015: Yalnız harf büyüklüğüyle (ya da Unicode biçimiyle) yeniden adlanan ortam dizini checkout'a da taşınır
<!-- deps: [T001, T002] | files: [internal/backend/link_case_test.go, internal/backend/link_artifacts.go] | satisfies: [FR-012] -->

**Interfaces:**
- Consumes: `publishArtifacts`'ın mevcut gövdesi (adı `publishFiles` olur), `entriesIn(t, dir)` (T001), `envname.SameDirectory` (T002)
- Produces: `publishArtifacts(root string, before, after map[string]artifactFile) error` = `followCaseRenames` + `publishFiles` + reddedilirse geri alma · `followCaseRenames(root string, before, after map[string]artifactFile) (map[string]artifactFile, func() error, error)` · `envDirsOf(base string, files map[string]artifactFile) map[string]bool` · `foldedOnly(name string, in, notIn map[string]bool) []string` — "başka bir yazımla aynı dizin" için link tarafındaki TEK kural, `envname.SameDirectory`'yi sorar (T002'nin link kapısıyla aynı cevap: harf büyüklüğü VE Unicode biçimi); T016'nın süpürmesi de bunu kullanır · test yardımcısı `underEnvironments(env, file string) string` (`link_case_test.go`)

T016'nın ön koşulu. Link stage'de koşar, sonra dosya dosya yayımlar; harf büyüklüğüyle yeniden adlandırma bir dosya değil. Mac'in büyük/küçük harf duyarsız diskinde `staging/openapi.json` zaten `Staging/openapi.json`'u gösteren bir yol, bu yüzden yayın onu "link sırasında beliren dosya" sanıp HER ŞEYİ reddediyor (ölçüldü, aşağıda). Yani stage'de dizini yeniden adlandıran bir süpürme (T016) bu görev olmadan Mac'te link'i tamamen düşürürdü — ölçüldü: bu görev geri alınınca T016'nın link testi `palbase/environments/staging/android-config.json changed during link; no generated files were published` ile düşüyor. Çözüm: yayından önce checkout'taki dizin stage'deki yazımına çevrilir (tek adım `os.Rename` yeter: Go, aynı dosyayı gösteren iki adı harf-büyüklüğü değişimi sayıp izin veriyor — `os/file_unix.go` "a case-only rename on a case-insensitive filesystem, which is ok"; APFS'te ölçüldü: `direct rename err: <nil> entries: staging`), `before`'daki yollar yeni yazıma taşınır, yayın her zamanki gibi sürer; yayın reddedilirse ad geri alınır. Linux'ta (duyarlı disk) bu test düzeltmesiz de geçer — orada yayın silme + oluşturma olarak zaten doğru sonuç veriyordu. `foldedOnly` `strings.EqualFold` değil `envname.SameDirectory` sorar: APFS Unicode biçimine de duyarsız, ve link kapısı (T002) ikizi zaten o kuralla tanıyor; bu kuralın kendi kırmızısı T016'da (`TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInUnicodeForm` — `EqualFold`'la `"[]" should have 1 item(s), but has 0`).

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_case_test.go`:
```go
package backend

// link_case_test.go — ONE ENVIRONMENT, ONE SPELLING, ON EVERY DISK (FR-012).
//
// On a case-insensitive disk — every Mac's — `Staging/` and `staging/` are one
// directory, and a build that looks its environment up by exact name (the
// 2.4 Gradle plugin does, so a Linux CI agrees with a Mac) finds only the
// spelling on disk. An environment renamed by case alone must come out of the
// link under the name the project gives it now.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// underEnvironments is one file's path inside the checkout, as a publish keys it.
func underEnvironments(env, file string) string {
	return filepath.Join(RootDir(), envSubdir, env, file)
}

// A DIRECTORY RENAMED BY CASE ALONE REACHES THE CHECKOUT. Publishing goes file
// by file, and on a case-insensitive disk `staging/openapi.json` is a path
// `Staging/openapi.json` already answers to: the publish took the renamed file
// for one that appeared during the link and refused everything. The checkout
// ends up spelled as the stage is, holding exactly the stage's files.
func TestAPublishCarriesADirectoryRenamedByCaseAlone(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, RootDir(), envSubdir)
	require.NoError(t, os.MkdirAll(filepath.Join(envs, "Staging"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")), []byte("old contract"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "PalbaseGenerated.swift")), []byte("old client"), 0o644))
	before := map[string]artifactFile{
		underEnvironments("Staging", "openapi.json"):           {[]byte("old contract"), 0o644},
		underEnvironments("Staging", "PalbaseGenerated.swift"): {[]byte("old client"), 0o644},
	}
	after := map[string]artifactFile{
		underEnvironments("staging", "openapi.json"):        {[]byte("new contract"), 0o644},
		underEnvironments("staging", "android-config.json"): {[]byte("new config"), 0o600},
	}

	require.NoError(t, publishArtifacts(root, before, after))

	require.Equal(t, []string{"staging"}, entriesIn(t, envs))
	require.Equal(t, []string{"android-config.json", "openapi.json"}, entriesIn(t, filepath.Join(envs, "staging")))
	raw, err := os.ReadFile(filepath.Join(envs, "staging", "openapi.json"))
	require.NoError(t, err)
	require.Equal(t, "new contract", string(raw))
}

// AND A PUBLISH THAT IS REFUSED TAKES THE RENAME BACK: "previous artifacts
// were preserved" covers the directory's spelling too. Somebody edited a file
// while the link ran, so nothing is published.
func TestARefusedPublishLeavesTheDirectorySpelledAsItWas(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, RootDir(), envSubdir)
	require.NoError(t, os.MkdirAll(filepath.Join(envs, "Staging"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")), []byte("edited meanwhile"), 0o644))
	before := map[string]artifactFile{underEnvironments("Staging", "openapi.json"): {[]byte("old contract"), 0o644}}
	after := map[string]artifactFile{underEnvironments("staging", "openapi.json"): {[]byte("new contract"), 0o644}}

	require.ErrorContains(t, publishArtifacts(root, before, after), "changed during link")

	require.Equal(t, []string{"Staging"}, entriesIn(t, envs))
	raw, err := os.ReadFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")))
	require.NoError(t, err)
	require.Equal(t, "edited meanwhile", string(raw))
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestAPublishCarriesADirectoryRenamedByCaseAlone|TestARefusedPublishLeavesTheDirectorySpelledAsItWas' -count=1 -v` (macOS, APFS) · Beklenen: **FAIL** — `--- FAIL: TestAPublishCarriesADirectoryRenamedByCaseAlone`, çıktıda `palbase/environments/staging/openapi.json changed during link; no generated files were published`. `TestARefusedPublishLeavesTheDirectorySpelledAsItWas` bu adımda **PASS** — bilerek: adlandırma yokken zaten geri alınacak bir şey yok; Adım 3'ün geri almasını korur. Büyük/küçük harf duyarlı diskte (`TMPDIR=<duyarlı birim>/tmp`) iki test de düzeltmesiz PASS — kırmızı yalnız duyarsız diskte (Mac) görülür.
- [ ] **Adım 3: Uygula** — `internal/backend/link_artifacts.go`: `func publishArtifacts(root string, before, after map[string]artifactFile) error {` satırını aşağıdaki blokla değiştir (eski gövde olduğu gibi `publishFiles`'ın gövdesi olarak kalır; `errors`, `strings`, `fmt` dosyada zaten import'lu; import bloğunun sonuna boş satır + `"github.com/palgroup/palbase-cli/internal/envname"` ekle):
```go
// publishArtifacts makes the checkout hold what the stage holds.
//
// A DIRECTORY RENAMED BY LETTER CASE ALONE GOES FIRST (FR-012). Publishing is
// file by file, and a case-only rename is not a file: on a case-insensitive
// disk `staging/openapi.json` is a path `Staging/openapi.json` already answers
// to, so the new path read as a file that appeared during the link and the
// whole publish was refused — measured on APFS. The directory is renamed, the
// files it held are carried to their new paths, and publishing goes on as for
// any other change; a publish that is refused takes the rename back.
func publishArtifacts(root string, before, after map[string]artifactFile) error {
	moved, undo, err := followCaseRenames(root, before, after)
	if err != nil {
		return err
	}
	if err := publishFiles(root, moved, after); err != nil {
		if undoErr := undo(); undoErr != nil {
			return fmt.Errorf("%w; and the directories renamed for it could not be named back: %v", err, undoErr)
		}
		return err
	}
	return nil
}

// followCaseRenames renames each environment directory of the checkout whose
// name the stage changed by letter case alone, and returns `before` as it reads
// after that, with the way back.
//
// ONE OLD SPELLING FOR ONE NEW ONE. Two directories that fold to one name can
// both exist only on a case-sensitive disk, and there their files publish as
// they always did: as removals and creations.
func followCaseRenames(root string, before, after map[string]artifactFile) (map[string]artifactFile, func() error, error) {
	base := filepath.Join(RootDir(), envSubdir)
	was, is := envDirsOf(base, before), envDirsOf(base, after)
	renames := map[string]string{}
	for old := range was {
		if is[old] {
			continue
		}
		if news := foldedOnly(old, is, was); len(news) == 1 && len(foldedOnly(news[0], was, is)) == 1 {
			renames[old] = news[0]
		}
	}
	var done []string
	undo := func() error {
		var errs []error
		for _, old := range done {
			errs = append(errs, os.Rename(filepath.Join(root, base, renames[old]), filepath.Join(root, base, old)))
		}
		return errors.Join(errs...)
	}
	for old, next := range renames {
		// ONE STEP IS ENOUGH: os.Rename lets a name through to itself in another
		// case when the disk says both are the same file (measured on APFS).
		if err := os.Rename(filepath.Join(root, base, old), filepath.Join(root, base, next)); err != nil {
			return nil, nil, errors.Join(err, undo())
		}
		done = append(done, old)
	}
	moved := make(map[string]artifactFile, len(before))
	for path, file := range before {
		if rest, ok := strings.CutPrefix(path, base+string(filepath.Separator)); ok {
			if env, inside, nested := strings.Cut(rest, string(filepath.Separator)); nested && renames[env] != "" {
				path = filepath.Join(base, renames[env], inside)
			}
		}
		moved[path] = file
	}
	return moved, undo, nil
}

// envDirsOf names every environment directory a set of files lives in.
func envDirsOf(base string, files map[string]artifactFile) map[string]bool {
	dirs := map[string]bool{}
	for path := range files {
		if rest, ok := strings.CutPrefix(path, base+string(filepath.Separator)); ok {
			if env, _, nested := strings.Cut(rest, string(filepath.Separator)); nested {
				dirs[env] = true
			}
		}
	}
	return dirs
}

// foldedOnly names the entries of `in` that are `name` spelled in another
// letter case — or another Unicode form, which APFS ignores the same way
// (envname.SameDirectory) — and are not entries of `notIn`.
func foldedOnly(name string, in, notIn map[string]bool) []string {
	var out []string
	for other := range in {
		if other != name && !notIn[other] && envname.SameDirectory(other, name) {
			out = append(out, other)
		}
	}
	return out
}

func publishFiles(root string, before, after map[string]artifactFile) error {
```
- [ ] **Adım 4: Yeşil** — Run: `gofmt -l . && go vet ./internal/backend/ && go test ./internal/backend/ -run 'TestAPublishCarriesADirectoryRenamedByCaseAlone|TestARefusedPublishLeavesTheDirectorySpelledAsItWas|TestArtifactPublication|TestLinkPublicationConflictRestoresDependencies|TestASweptEnvironmentLeavesNoDirectoryInTheCheckout' -count=1 -v` · Beklenen: `--- PASS: TestAPublishCarriesADirectoryRenamedByCaseAlone`, `--- PASS: TestARefusedPublishLeavesTheDirectorySpelledAsItWas`, `--- PASS: TestArtifactPublicationRefusesConcurrentEditsBeforeAnyWrite`, `--- PASS: TestLinkPublicationConflictRestoresDependencies`, `--- PASS: TestASweptEnvironmentLeavesNoDirectoryInTheCheckout`, `ok  	github.com/palgroup/palbase-cli/internal/backend`; duyarlı diskte de iki yeni test PASS. Geri alma ölçüldü: `publishArtifacts`'taki `undo()` çağrısı kaldırılınca `TestARefusedPublishLeavesTheDirectorySpelledAsItWas` → `expected: []string{"Staging"}` / `actual  : []string{"staging"}`. Paketin tamamı: yalnız B16 FAIL (Global Constraints; bu scratch'te: `TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`).
- [ ] **Adım 5: Commit** — `git add internal/backend/link_artifacts.go internal/backend/link_case_test.go && git commit -m "fix(link): yalnız harf büyüklüğüyle ya da Unicode biçimiyle yeniden adlanan ortam dizini checkout'a da taşınır; yayın reddedilirse ad geri alınır (FR-012)" -- internal/backend/link_artifacts.go internal/backend/link_case_test.go`

---

### T016: Temizlik ortam adını harf büyüklüğü ve Unicode biçimi gözetmeden karşılaştırır — yalnız yazımı farklı dizin yeniden adlanır, aynı koşuda yazılan asla silinmez
<!-- deps: [T014, T015] | files: [internal/backend/link_case_test.go, internal/backend/app_environments.go] | satisfies: [FR-012] -->

**Interfaces:**
- Consumes: `removeStaleEnvironmentDirs(root, keep, leftover, w)`, `shownEnvDir`, `selectedLeftover`, `sweepCheckouts`, `seedEnvironment` (T014) · `foldedOnly`, `underEnvironments`, yayının adlandırmayı taşıması (T015) · `readEnvConfig`, `stackServing`, `routeEnvironments`, `entriesIn` (mevcut)
- Produces: harf-büyüklüğü farkında `removeStaleEnvironmentDirs` — satırlar: `renamed palbase/environments/<eski> to <yeni> — the project spells that environment <yeni> now, and a build finds its directory by the exact name` · (yalnız duyarlı diskte) `removed palbase/environments/<eski> (the project spells that environment <yeni> now)` · test yardımcıları `caseInsensitiveDisk(t, dir string) bool`, `seedUnder(t, root, env, file, body string)` (`link_case_test.go`)

Kural, diskteki her dizin girdisi için, sırayla: `keep`'te birebir varsa kalır; `local` birebirse kalır (T014'ten beri çağıran `local` eklemiyor; `local` yalnız bu koşu onu yazdıysa `keep`'te, yani `Local/` gibi bir bulut kalıntısı ASLA `local/`'a çevrilmez); `keep`'te birden çok ad ona büyüklük-duyarsız eşitse (FR-003 ikizleri, ikisi de atlandı) dokunulmaz; tek bir ad eşitse ve o ad diskte birebir yoksa dizin o ada **yeniden adlandırılır** (içeriğe bakılmaz — adlandırma hiçbir şey kaybettirmez); birebir ad da diskteyse (yalnız duyarlı disk tutabilir; birebir olanı bu koşu yazdı) eski yazım normal süpürmeye düşer ve satırı yeni yazımı söyler. Böylece "aynı koşuda yazılmış dizin asla silinmez" kurgu gereği sağlanır: Mac'te yazılan dosyalar eski yazımlı dizine düşse de o dizin artık eşleşiyor. Bağlantı testi Mac'te kırmızı (Apple için doğrulama raporunun A3(ii) ölçümü: link az önce yazdığını siliyordu; Android ve web'de de aynı), duyarlı diskte düzeltmesiz de yeşil — orada eski `Staging/` süpürülüp `staging/` yazılıyordu; birim testleri iki diskte de kırmızı.

- [ ] **Adım 1: Kırmızı testi yaz** — `internal/backend/link_case_test.go`: import bloğunu
```go
import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)
```
  şununla değiştir:
```go
import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```
  ve dosyanın sonuna ekle:
```go
// caseInsensitiveDisk answers whether dir's disk takes `a` and `A` for one name.
func caseInsensitiveDisk(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "caseprobe")
	require.NoError(t, os.Mkdir(probe, 0o755))
	defer func() { require.NoError(t, os.Remove(probe)) }()
	_, err := os.Stat(filepath.Join(dir, "CASEPROBE"))
	return err == nil
}

// seedUnder commits one file into an environment directory under root.
func seedUnder(t *testing.T, root, env, file, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, RootDir(), envSubdir, env), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments(env, file)), []byte(body), 0o644))
}

// AN ENVIRONMENT IN ANOTHER LETTER CASE IS NOT A LOST ONE. The sweep compared
// names exactly, so `Staging/` read as an environment the project no longer
// has once the project spelled it `staging` — and went, files and all. It is
// renamed instead, with everything it holds.
func TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInCase(t *testing.T) {
	root := t.TempDir()
	seedUnder(t, root, "Staging", "openapi.json", "staging's contract")

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main", "staging"}, selectedLeftover, &out))

	require.Equal(t, []string{"staging"}, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)))
	raw, err := os.ReadFile(filepath.Join(root, underEnvironments("staging", "openapi.json")))
	require.NoError(t, err)
	require.Equal(t, "staging's contract", string(raw))
	require.Equal(t, "renamed palbase/environments/Staging to staging — the project spells that environment "+
		"staging now, and a build finds its directory by the exact name\n", out.String())
}

// AND IN ANOTHER UNICODE FORM, which APFS ignores the same way: `café` written
// with two code points is the directory the project now spells with one. It is
// renamed, never swept — on a Mac this link has just written the new
// spelling's files into it, as it does for a case-only rename. (A
// case-sensitive APFS volume, which still ignores normalisation, keeps the old
// bytes through the rename; the one directory answers to the listed name on
// every disk, and that is what is measured.)
func TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInUnicodeForm(t *testing.T) {
	const composed, decomposed = "caf\u00e9", "cafe\u0301"
	root := t.TempDir()
	seedUnder(t, root, decomposed, "openapi.json", "its contract")

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main", composed}, selectedLeftover, &out))

	require.Len(t, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)), 1)
	raw, err := os.ReadFile(filepath.Join(root, underEnvironments(composed, "openapi.json")))
	require.NoError(t, err)
	require.Equal(t, "its contract", string(raw))
	require.Equal(t, "renamed palbase/environments/\""+decomposed+"\" to \""+composed+"\" — the project spells that environment \""+
		composed+"\" now, and a build finds its directory by the exact name\n", out.String())
}

// TWO LISTED NAMES THAT SHARE ONE DIRECTORY (FR-003) say nothing about which
// spelling it should have: both were skipped, and skipped is not gone.
func TestTheSweepLeavesADirectoryTwoListedNamesShare(t *testing.T) {
	root := t.TempDir()
	seedUnder(t, root, "STAGING", "openapi.json", "one of the twins")

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main", "Staging", "staging"}, selectedLeftover, &out))

	require.Equal(t, []string{"STAGING"}, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)))
	require.Empty(t, out.String())
}

// `local/` IS NEVER MADE OUT OF SOMETHING ELSE. `Local/` is what a cloud
// environment of that name left behind; when this machine's stack was not
// written this run, nothing says the directory holds it — so it is swept like
// any environment the project no longer has, never renamed into local/.
func TestTheSweepNeverRenamesALeftoverIntoLocal(t *testing.T) {
	root := t.TempDir()
	seedUnder(t, root, "Local", "android-config.json", `{"base_url":"https://cloud.example"}`)

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main"}, selectedLeftover, &out))

	require.Empty(t, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)))
	require.Equal(t, "removed palbase/environments/Local (the project no longer has that environment)\n", out.String())
}

// ON A CASE-SENSITIVE DISK BOTH SPELLINGS CAN EXIST — a Linux CI's, after a
// link wrote `staging/` beside the old `Staging/`. The exact one is the
// project's; the other goes, and the line says where its environment is now.
// (A Mac cannot hold the two, so there is nothing to measure there.)
func TestTheSweepRemovesTheOldSpellingBesideTheNewOne(t *testing.T) {
	root := t.TempDir()
	if caseInsensitiveDisk(t, root) {
		t.Skip("this disk takes Staging and staging for one name — there is no second directory to sweep")
	}
	seedUnder(t, root, "Staging", "openapi.json", "old")
	seedUnder(t, root, "staging", "openapi.json", "written this run")

	var out strings.Builder
	require.NoError(t, removeStaleEnvironmentDirs(root, []string{"main", "staging"}, selectedLeftover, &out))

	require.Equal(t, []string{"staging"}, entriesIn(t, filepath.Join(root, RootDir(), envSubdir)))
	require.Equal(t, "removed palbase/environments/Staging (the project spells that environment staging now)\n", out.String())
}

// A LINK NEVER DELETES WHAT IT JUST WROTE. The project renamed `Staging` to
// `staging`; the link wrote staging's files, and on a Mac they landed in the
// directory that already existed, `Staging/`. The sweep then read `Staging/`
// as an environment the project no longer has and deleted it — measured for
// Apple (verification of 2026-09-25, A3): the link removed what it had just
// written. Whatever the disk, the checkout ends up with `staging/`, holding
// staging's configuration.
func TestALinkKeepsAnEnvironmentRenamedByCaseAlone(t *testing.T) {
	for _, c := range sweepCheckouts {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			c.seed(t)
			seedEnvironment(t, "Staging", c.platform)
			main := stackServing(t, linkKeyMain, nil)
			staging := stackServing(t, linkKeyStaging, nil)
			routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
			o := linkOpts{
				url:       main.URL,
				platforms: []string{c.platform},
				linkedEnv: "main",
				product:   Product{ID: "prd_a", Name: "todoapp"},
				environments: []Environment{
					{Name: "main", Ref: "mainref000", Status: "Running"},
					{Name: "staging", Ref: "stagref000", Status: "Running"},
				},
			}

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.Equal(t, []string{"main", "staging"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)), out.String())
			assert.Equal(t, staging.URL, readEnvConfig(t, "staging", c.platform).BaseURL)
		})
	}
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestTheSweep|TestALinkKeepsAnEnvironmentRenamedByCaseAlone' -count=1 -v` · Beklenen (macOS, APFS): **FAIL** —
  - `--- FAIL: TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInCase`: `expected: []string{"staging"}` / `actual  : []string(nil)` (dizin silindi);
  - `--- FAIL: TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInUnicodeForm`: `Error: "[]" should have 1 item(s), but has 0` (dizin silindi);
  - `--- FAIL: TestTheSweepLeavesADirectoryTwoListedNamesShare`: `expected: []string{"STAGING"}` / `actual  : []string(nil)`;
  - `--- FAIL: TestALinkKeepsAnEnvironmentRenamedByCaseAlone/ios`, `/android`, `/web`: `expected: []string{"main", "staging"}` / `actual  : []string{"main"}` — link az önce yazdığını sildi;
  - `--- PASS: TestTheSweepNeverRenamesALeftoverIntoLocal` — bilerek (bugün de siliniyor; Adım 3'ün `local`'e çevirmemesini korur); `--- SKIP: TestTheSweepRemovesTheOldSpellingBesideTheNewOne`.
  Duyarlı diskte (`TMPDIR=<duyarlı birim>/tmp`, taslakçının ölçümü): iki birim testi aynı biçimde FAIL, `TestTheSweepRemovesTheOldSpellingBesideTheNewOne` FAIL — `actual  : "removed palbase/environments/Staging (the project no longer has that environment)\n"`; `TestALinkKeepsAnEnvironmentRenamedByCaseAlone` orada zaten PASS.
- [ ] **Adım 3: Uygula** — `internal/backend/app_environments.go` `removeStaleEnvironmentDirs`:
  (a) `entries, err := os.ReadDir(base)` hata dalından sonra, `for _, e := range entries {` döngüsünden ÖNCE ekle:
```go
	onDisk := make(map[string]bool, len(entries))
	for _, e := range entries {
		onDisk[e.Name()] = true
	}
```
  (b) `dir := filepath.Join(base, e.Name())` satırının hemen ALTINA, `inside, err := os.ReadDir(dir)`'den ÖNCE ekle:
```go
		// AN ENVIRONMENT IN ANOTHER LETTER CASE IS NOT A LOST ONE (FR-012).
		// Names were compared exactly, so once the project spelled `Staging`
		// as `staging`, `Staging/` read as gone — and on a Mac it was the very
		// directory this link had just written staging's files into, because
		// the disk takes both spellings for one name. Measured for Apple: the
		// link deleted what it had written. It is renamed instead, whatever it
		// holds: nothing is lost by a rename.
		//
		// NOT WHEN TWO LISTED NAMES SHARE IT (FR-003): both were skipped, and
		// nothing says which spelling it should have. NOR WHEN THE EXACT
		// SPELLING IS ON DISK TOO — only a case-sensitive disk holds both, and
		// there the exact one is what this run wrote; the other is swept.
		reason := "the project no longer has that environment"
		switch spelled := foldedOnly(e.Name(), wanted, nil); {
		case len(spelled) > 1:
			continue
		case len(spelled) == 1 && !onDisk[spelled[0]]:
			if err := os.Rename(dir, filepath.Join(base, spelled[0])); err != nil {
				return err
			}
			onDisk[e.Name()], onDisk[spelled[0]] = false, true
			fmt.Fprintf(w, "renamed %s to %s — the project spells that environment %s now, and a build finds "+
				"its directory by the exact name\n", shownEnvDir(e.Name()), envname.Label(spelled[0]), envname.Label(spelled[0]))
			continue
		case len(spelled) == 1:
			reason = "the project spells that environment " + envname.Label(spelled[0]) + " now"
		}
```
  (c) fonksiyonun sonundaki `fmt.Fprintf(w, "removed %s (the project no longer has that environment)\n", shownEnvDir(e.Name()))` satırını şununla değiştir:
```go
		fmt.Fprintf(w, "removed %s (%s)\n", shownEnvDir(e.Name()), reason)
```
- [ ] **Adım 4: Yeşil** — Run: `gofmt -l . && go vet ./internal/backend/ && go test ./internal/backend/ -run 'TestTheSweep|TestALinkKeepsAnEnvironmentRenamedByCaseAlone|TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas|TestAnAppleLinkKeepsTheFilesOfASkippedTwin|TestOrphan|TestAnAppleSweepKeepsTheLocalEnvironmentsWebClient|TestASweepRemovesAnEnvironmentWithACustomClientName' -count=1 -v` · Beklenen (APFS): `--- PASS: TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInCase`, `--- PASS: TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInUnicodeForm`, `--- PASS: TestTheSweepLeavesADirectoryTwoListedNamesShare`, `--- PASS: TestTheSweepNeverRenamesALeftoverIntoLocal`, `--- SKIP: TestTheSweepRemovesTheOldSpellingBesideTheNewOne`, `--- PASS: TestALinkKeepsAnEnvironmentRenamedByCaseAlone` (`/ios`, `/android`, `/web`), `--- PASS: TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas`, `--- PASS: TestAnAppleLinkKeepsTheFilesOfASkippedTwin`, `--- PASS: TestOrphanCleanupHasAProductionCaller`, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Duyarlı diskte aynı liste, `--- PASS: TestTheSweepRemovesTheOldSpellingBesideTheNewOne` dahil. Paketin tamamı: yalnız B16 FAIL (Global Constraints; bu scratch'te: `TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`). Unicode-biçimi kuralı (T015'in `foldedOnly`'si) ölçüldü: `foldedOnly` geçici olarak `strings.EqualFold`'a döndürüldüğünde `--- FAIL: TestTheSweepRenamesADirectoryWhoseNameDiffersOnlyInUnicodeForm` / `"[]" should have 1 item(s), but has 0`. Disk notu (ölçüldü): büyük/küçük harf duyarlı APFS birimi normalleştirmeye yine duyarsızdır ve normalleştirme-farklı bir ada yeniden adlandırmada eski baytları korur — testin ilk hâli (`require.Equal(t, []string{composed}, entriesIn(…))`) orada `(len=6) "café"` ile düştü; bu yüzden test, dizinin TEK olduğunu ve listelenen adla bulunduğunu ölçer (her diskte doğru olan kullanıcı davranışı). Son hâliyle duyarlı birimde (Adım 2'deki `hdiutil` tarifi, `TMPDIR=<birim>/tmp`) `TestTheSweep*`, `TestALinkKeepsAnEnvironmentRenamedByCaseAlone`, `TestAPublishCarriesADirectoryRenamedByCaseAlone`, `TestARefusedPublishLeavesTheDirectorySpelledAsItWas` PASS, `TestTheSweepRemovesTheOldSpellingBesideTheNewOne` orada da koşar ve PASS.
- [ ] **Adım 5: Commit** — `git add internal/backend/app_environments.go internal/backend/link_case_test.go && git commit -m "fix(link): temizlik ortam adını harf büyüklüğü ve Unicode biçimi gözetmeden karşılaştırır — yalnız yazımı farklı dizin yeniden adlanır, aynı koşuda yazılan asla silinmez (FR-012)" -- internal/backend/app_environments.go internal/backend/link_case_test.go`

---

### T017: React Native / Flutter kökünde Android `android/` altındaki Gradle dosyalarından da algılanır
<!-- deps: [] | files: [internal/backend/link_rn_root_test.go, internal/backend/planes.go, internal/backend/social_link.go] | satisfies: [FR-015] -->

React Native ve Flutter bütün Gradle yapısını `ios/`'un yanındaki `android/` altında tutar. `detectAndroidApplicationID` yalnız `<kök>/app/build.gradle(.kts)` ve `<kök>/build.gradle(.kts)`'e bakıyor (20e5d7e'de `planes.go:329-333`), `nativeIdentifiers` da aynı dört dosyayı ayrı bir listede okuyor (`social_link.go` `nativeIdentifiers`, 20e5d7e'de `:240`). Sonuç: böyle bir kökte `palbase link` Android'i hiç görmüyor — "▸ no client app here … linking the backend only" basıp `android-config.json` yazmıyor (`reports/verification-2026-09-25.md` N2: "RN repo root -> None"). Bu görev iki okuyucuyu TEK listeye (`androidBuildFiles`) bağlar ve `android/app/build.gradle(.kts)`, `android/build.gradle(.kts)`'i ekler; `applePlatforms`'un `ios/`/`macos/` taramasının Android ikizi. Dosyalar kökteki `palbase/`'a yazılır; Gradle kökü `android/` olan build onu plugin'in üst dizin adayıyla bulur (`plan-plugin.md`, FR-205).

**Interfaces:**
- Consumes: `detectPlatforms(dir string) []string` (`planes.go`), `nativeIdentifiers(platform string) []string` (`social_link.go`); test yardımcıları `inScratchCheckout`, `stackServing`, `writeFile` (`project_link_test.go`), `routeEnvironments` (`gather_environments_test.go`), `linkKeyMain` (`link_all_environments_test.go`).
- Produces: `var androidBuildFiles []string` (`planes.go`) — Android Gradle dosyalarının checkout'a göreli TEK listesi, sırayla `app/build.gradle.kts`, `app/build.gradle`, `build.gradle.kts`, `build.gradle`, `android/app/build.gradle.kts`, `android/app/build.gradle`, `android/build.gradle.kts`, `android/build.gradle`. `detectAndroidApplicationID` ve `nativeIdentifiers` bunu okur; T020 (OAuth) ve T022 (doctor'ın Android bölümü) de bunu okur. Test yardımcısı `seedGradleFile(t, name, body string)` (`link_rn_root_test.go`).

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_rn_root_test.go`:
```go
package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedGradleFile writes one Gradle file under the current directory, making its
// parent directories.
func seedGradleFile(t *testing.T, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
	writeFile(t, name, body)
}

// A REACT NATIVE OR FLUTTER ROOT KEEPS ITS WHOLE GRADLE BUILD IN android/ (FR-015),
// beside ios/ — the layout applePlatforms already reads. Detection looked at
// app/ and the root alone, so `palbase link` there found no Android at all
// (verification N2: "RN repo root -> None"), and the OAuth identifier, reading
// the same list, could never name the app either.
func TestAReactNativeRootIsAnAndroidCheckout(t *testing.T) {
	for _, file := range []string{
		"android/app/build.gradle",
		"android/app/build.gradle.kts",
		"android/build.gradle",
		"android/build.gradle.kts",
	} {
		t.Run(file, func(t *testing.T) {
			inScratchCheckout(t)
			seedGradleFile(t, file, "android {\n  defaultConfig {\n    applicationId \"com.rnapp\"\n  }\n}\n")

			assert.Equal(t, []string{"android"}, detectPlatforms("."))
			assert.Equal(t, []string{"com.rnapp"}, nativeIdentifiers("android"))
		})
	}
}

// AND A LINK THERE WRITES THE ANDROID CONFIG, at the root the build's Gradle
// root sits under (android/ is the Gradle root; the plugin looks one level up).
func TestALinkAtAReactNativeRootWritesTheAndroidConfig(t *testing.T) {
	inScratchCheckout(t)
	seedGradleFile(t, "android/app/build.gradle", "android {\n  defaultConfig {\n    applicationId \"com.rnapp\"\n  }\n}\n")
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		linkedEnv:    "main",
		product:      Product{ID: "prd_rn", Name: "rnapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.True(t, strings.HasPrefix(out.String(), "▸ android\n"), out.String())
	assert.FileExists(t, ConfigPath("main", "android"))
	assert.NoDirExists(t, filepath.Join("android", RootDir()))
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestAReactNativeRootIsAnAndroidCheckout|TestALinkAtAReactNativeRootWritesTheAndroidConfig' -count=1` · Beklenen: **FAIL** — dört alt testin her birinde `expected: []string{"android"}` / `actual  : []string(nil)` ve `expected: []string{"com.rnapp"}` / `actual  : []string(nil)`; link testinde `Messages:   	▸ no client app here (looked for an Xcode project or workspace, an Android applicationId in build.gradle[.kts], and a package.json beside an index.html/public/src/app) — linking the backend only` ve `Error:      	unable to find file "palbase/environments/main/android-config.json"`.
- [ ] **Adım 3: Uygula** —
  (a) `internal/backend/planes.go` içinde şu bloğu:
```go
func detectAndroidApplicationID(root string) (string, error) {
	candidates := []string{
		filepath.Join(root, "app", "build.gradle.kts"),
		filepath.Join(root, "app", "build.gradle"),
		filepath.Join(root, "build.gradle.kts"),
		filepath.Join(root, "build.gradle"),
	}
	for _, candidate := range candidates {
		contents, err := os.ReadFile(candidate)
```
  şununla değiştir:
```go
// androidBuildFiles are the Gradle files an Android app may declare its
// applicationId in, relative to the checkout, in the order they are read.
//
// ANDROID/ TOO (FR-015). React Native and Flutter keep the whole Gradle build in
// `android/`, beside `ios/` — the cross-platform layout applePlatforms already
// reads — so `palbase link` at such a root found Apple and never Android
// (verification N2). Detection and the OAuth identifier read this ONE list: two
// lists are two answers to "which app is this".
var androidBuildFiles = []string{
	"app/build.gradle.kts", "app/build.gradle", "build.gradle.kts", "build.gradle",
	"android/app/build.gradle.kts", "android/app/build.gradle", "android/build.gradle.kts", "android/build.gradle",
}

func detectAndroidApplicationID(root string) (string, error) {
	for _, name := range androidBuildFiles {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
```
  (b) `internal/backend/social_link.go` `nativeIdentifiers` içinde şu iki satırı:
```go
		for _, path := range []string{"app/build.gradle.kts", "app/build.gradle", "build.gradle.kts", "build.gradle"} {
			raw, err := os.ReadFile(filepath.Join(root, path))
```
  şununla değiştir:
```go
		for _, path := range androidBuildFiles {
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/backend/ -run 'TestAReactNativeRootIsAnAndroidCheckout|TestALinkAtAReactNativeRootWritesTheAndroidConfig|TestDetectPlatformsReadsTheCheckout|TestOAuthInferenceRequiresAnUnambiguousAndroidApplication' -count=1 -v` · Beklenen: `--- PASS: TestAReactNativeRootIsAnAndroidCheckout`, `--- PASS: TestALinkAtAReactNativeRootWritesTheAndroidConfig`, `--- PASS: TestDetectPlatformsReadsTheCheckout`, `--- PASS: TestOAuthInferenceRequiresAnUnambiguousAndroidApplication`, `ok  	github.com/palgroup/palbase-cli/internal/backend` (son ikisi mevcut algılama/kimlik testleri — gerileme bekçisi).
- [ ] **Adım 5: Commit** — `git add internal/backend/planes.go internal/backend/social_link.go internal/backend/link_rn_root_test.go && git commit -m "fix(link): React Native / Flutter kökünde Android android/app/build.gradle(.kts) ve android/build.gradle(.kts) üzerinden de algılanır (FR-015)" -- internal/backend/planes.go internal/backend/social_link.go internal/backend/link_rn_root_test.go`

---

### T018: Link yazdığı her `openapi.json`'u basar; sözleşmesi olmayan ortamı onu neyin bitireceğiyle adlandırır
<!-- deps: [T007, T011, T014] | files: [internal/backend/link_contract_lines_test.go, internal/backend/project_link.go, internal/backend/app_environments.go] | satisfies: [FR-014] -->

Link config yollarını basıyor (`project_link.go` config döngüsü, `wrote %s`), ama sözleşmeleri yazan döngü (`writeSpec(name, spec)`) hiçbir şey basmıyor. Plugin iki dosyaya da muhtaç; push edilmiş bir proje ile hiç push edilmemiş bir proje aynı `wrote` satırlarını üretiyor (`reports/verification-2026-09-25.md` YENİ, L1: "the output lists only main/android-config.json, but disk has main/openapi.json too"). Bu görev (1) her yazılan sözleşmeyi config satırlarının hemen ardından `wrote palbase/environments/<env>/openapi.json` diye basar, (2) config'i yazılıp sözleşmesi gelmeyen her ortam için tek bir satır basar: dosyanın durumu (hiç yok ya da önceki link'in yazdığı duruyor) ve onu neyin bitireceği. Çare kaynağa göre değişir: projenin ortamı → `` `palbase push --env <ad>`, then `palbase link` here `` (plugin'in FR-210 metniyle aynı); adresle bağlı, projesiz bir yığın → `` `palbase push`, then `palbase link` here `` (`--env` projesiz checkout'ta çözülmez: `environments.go` `resolveNamed`); `local` → `` `palbase start` in the backend, then `palbase link` here `` (`palbase push` yerel yığına basmayı reddeder, `stack_push.go:162`; FR-009'un ve plugin T017'nin cümlesiyle aynı yol). Okuma sırasında basılan satır ("<env> has no contract to give (<projenin cümlesi>) — …") DEĞİŞMEZ: projenin kendi sebebini o taşır ve `TestAnEnvironmentWithNothingDeployedIsWrittenAndNamed` / `TestAnEnvironmentWithNoContractKeepsTheProjectsOwnSentence` onu sabitliyor; yeni satır checkout'un ne tuttuğunu söyler.

**Tek çare.** Projesiz bir link'te okuma, sözleşme yokken "the link is recorded; `palbase spec` fills the contract in once something answers" diyordu; yeni satırla aynı çıktıda iki farklı sonraki adım okunuyordu (eleştirmen: `palbase spec` / `palbase push`, then `palbase link`). Dosya alan bir checkout'ta (istemcisi olan) eski cümle artık basılmaz; istemcisiz bir backend checkout'unda missingContractLine hiç basılmadığı için eski cümle kalır (`TestABackendLinkWithNoContractStillSaysSpecFillsItIn` bekçi).

**Interfaces:**
- Consumes: `envname.Label(name string) string` (T007), `shownEnvDir(name string) string` (T014), `stackEnvName` ile projesiz loopback link'in `local` adlanması (T011), `SpecPath`, `isRegularFile`, `writesPerEnvironmentArtifacts`; test yardımcıları `envServer`/`envServerOpts{socialAuth, noContract}`, `routeEnvironments` (`gather_environments_test.go`), `seedAndroidApp` (`platform_environments_test.go`), `linkedAs`, `inScratchCheckout` (`project_link_test.go`), `linkKeyMain`.
- Produces: `func missingContractLine(name string, project, earlier bool) string` (`app_environments.go`). Çıktı satırları, `runLinkPrepared`'da config `wrote` satırlarından ve unwritten raporundan sonra, temizlikten önce:
  - `wrote palbase/environments/<env>/openapi.json`
  - `<env> has no contract yet, so palbase/environments/<env>/openapi.json is not written and no client is generated for it — <çare>`
  - `<env> gave no contract this time, so palbase/environments/<env>/openapi.json is the one an earlier link wrote — <çare>`
  - Test yardımcısı `pushedAndNot(t) linkOpts` (`link_contract_lines_test.go`): `main` sözleşmeli, `staging` sözleşmesiz iki ortamlı Android link'i.

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_contract_lines_test.go`:
```go
package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushedAndNot is an Android checkout linked to a project whose `main` serves a
// contract and whose `staging` has nothing deployed yet.
func pushedAndNot(t *testing.T) linkOpts {
	t.Helper()
	seedAndroidApp(t)
	main, _ := envServer(t, linkKeyMain, envServerOpts{socialAuth: true})
	staging, _ := envServer(t, "pb_staging_cK", envServerOpts{socialAuth: true, noContract: true})
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	return linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}
}

// EVERY CONTRACT LINK WRITES IS SAID, AND SO IS EVERY ONE IT DID NOT (FR-014).
//
// Only the configs were listed, so a link to a pushed project and one to a
// project nothing was deployed to printed the same `wrote` lines — measured
// (verification L1): the output named main/android-config.json while
// main/openapi.json sat on disk unmentioned. The plugin needs both files, and
// the reader could not see which environments had them.
func TestALinkSaysWhichEnvironmentsGotAContract(t *testing.T) {
	inScratchCheckout(t)
	o := pushedAndNot(t)

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Contains(t, out.String(), "wrote palbase/environments/main/android-config.json\n"+
		"wrote palbase/environments/staging/android-config.json\n"+
		"wrote palbase/environments/main/openapi.json\n"+
		"staging has no contract yet, so palbase/environments/staging/openapi.json is not written and no client "+
		"is generated for it — `palbase push --env staging`, then `palbase link` here\n")
	assert.FileExists(t, SpecPath("main"))
	assert.NoFileExists(t, SpecPath("staging"))
}

// A CONTRACT AN EARLIER LINK WROTE STAYS, and the line says that is what the
// build reads — "not written" would send the reader looking for a file that is
// there.
func TestAnEnvironmentThatGaveNoContractThisTimeKeepsTheEarlierOne(t *testing.T) {
	inScratchCheckout(t)
	o := pushedAndNot(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(SpecPath("staging")), 0o755))
	require.NoError(t, os.WriteFile(SpecPath("staging"), []byte(`{"openapi":"3.2.0","paths":{}}`), 0o644))

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Contains(t, out.String(), "staging gave no contract this time, so palbase/environments/staging/openapi.json "+
		"is the one an earlier link wrote — `palbase push --env staging`, then `palbase link` here\n")
	raw, err := os.ReadFile(SpecPath("staging"))
	require.NoError(t, err)
	assert.Equal(t, `{"openapi":"3.2.0","paths":{}}`, string(raw))
}

// WHAT GIVES AN ENVIRONMENT ITS FIRST CONTRACT depends on where the contract
// comes from. A project's environment serves what was pushed to it, named with
// --env; a stack somebody hosts, linked by address, has no project to name one
// in; and the stack `palbase start` runs serves the directory it mounted —
// `palbase push` refuses to publish to it (stack_push.go), so for `local` the
// cure is a start. ONE cure: the reading's own "`palbase spec` fills the
// contract in" is not said beside it.
func TestTheMissingContractLineNamesWhatEndsIt(t *testing.T) {
	for _, c := range []struct {
		name      string
		linkedEnv string
		want      string
	}{
		{"a stack somebody hosts", "main", "main has no contract yet, so palbase/environments/main/openapi.json is not written " +
			"and no client is generated for it — `palbase push`, then `palbase link` here\n"},
		{"the stack on this machine", "", "local has no contract yet, so palbase/environments/local/openapi.json is not written " +
			"and no client is generated for it — `palbase start` in the backend, then `palbase link` here\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			inScratchCheckout(t)
			seedAndroidApp(t)
			stack, _ := envServer(t, linkKeyMain, envServerOpts{socialAuth: true, noContract: true})
			linkedAs(t, stack.URL, "operator")

			var out strings.Builder
			o := linkOpts{url: stack.URL, platforms: []string{"android"}, linkedEnv: c.linkedEnv}
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.Contains(t, out.String(), c.want)
			assert.NotContains(t, out.String(), "`palbase spec` fills the contract in", "two cures for one missing contract")
		})
	}
}

// A CHECKOUT WITH NO CLIENT gets no files, so no line about them: it keeps the
// sentence it always had — the link is recorded, and `palbase spec` fills the
// contract in.
func TestABackendLinkWithNoContractStillSaysSpecFillsItIn(t *testing.T) {
	inScratchCheckout(t)
	stack, _ := envServer(t, linkKeyMain, envServerOpts{noContract: true})
	linkedAs(t, stack.URL, "operator")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())

	assert.Contains(t, out.String(), "  the link is recorded; `palbase spec` fills the contract in once something answers\n")
	assert.NotContains(t, out.String(), "has no contract yet, so")
}
```
  (`linkedEnv: "main"` projesiz bir link'te, bir başkasının barındırdığı yığını taklit eder: test sunucuları loopback olduğundan `stackEnvName` onları `local` adlandırır; üretimde bu ad `main`'dir.)
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestALinkSaysWhichEnvironmentsGotAContract|TestAnEnvironmentThatGaveNoContractThisTimeKeepsTheEarlierOne|TestTheMissingContractLineNamesWhatEndsIt|TestABackendLinkWithNoContractStillSaysSpecFillsItIn' -count=1 -v` · Beklenen: **FAIL** ×4 — ilk test: çıktı `…wrote palbase/environments/main/android-config.json\nwrote palbase/environments/staging/android-config.json\n\nlinked to todoapp (prd_a)\n…` ve `does not contain "wrote palbase/environments/main/android-config.json\nwrote palbase/environments/staging/android-config.json\nwrote palbase/environments/main/openapi.json\nstaging has no contract yet, …`; ikinci test `does not contain "staging gave no contract this time, …"`; `a_stack_somebody_hosts` çıktısı `no contract yet: http://127.0.0.1:… has nothing to describe yet — push a backend to it first (palbase push)\n  the link is recorded; `palbase spec` fills the contract in once something answers\n…wrote palbase/environments/main/android-config.json\n\nlinked to http://127.0.0.1:… (project)\ncommit palbase/\n` — `main has no contract yet …` yok; `the_stack_on_this_machine` aynı biçimde `wrote palbase/environments/local/android-config.json` ile biter, `local has no contract yet …` yok. `--- PASS: TestABackendLinkWithNoContractStillSaysSpecFillsItIn` — bilerek (istemcisiz checkout'un bugünkü cümlesinin bekçisi). Tek-çare kuralı ölçüldü: (a)+(b) uygulanıp (c) uygulanmadan `TestTheMissingContractLineNamesWhatEndsIt`'in iki alt testi `Messages: two cures for one missing contract` ile düşer.
- [ ] **Adım 3: Uygula** —
  (a) `internal/backend/project_link.go` `runLinkPrepared`'daki sözleşme döngüsünde şu bloğu:
```go
			if err := writeSpec(name, spec); err != nil {
				if err := unwritable(name, err); err != nil {
					return err
				}
			}
		}
	}
	for _, name := range unwritten.names() {
		fmt.Fprintf(w, "%s could not be written (%v) — skipped; run `palbase link` again once it can be\n",
			envname.Label(name), unwritten[name])
		delete(envs.Environments, name)
		delete(specs, name)
	}
```
  şununla değiştir:
```go
			if err := writeSpec(name, spec); err != nil {
				if err := unwritable(name, err); err != nil {
					return err
				}
				continue
			}
			// SAID LIKE THE CONFIGS (FR-014): a build needs both files, and a
			// link that listed only the config looked the same whether the
			// contract came with it or not.
			fmt.Fprintf(w, "wrote %s\n", SpecPath(name))
		}
	}
	for _, name := range unwritten.names() {
		fmt.Fprintf(w, "%s could not be written (%v) — skipped; run `palbase link` again once it can be\n",
			envname.Label(name), unwritten[name])
		delete(envs.Environments, name)
		delete(specs, name)
	}
	// AND EVERY ENVIRONMENT THAT GOT NO CONTRACT, beside the files (FR-014). The
	// reading above already said why in the project's own words; this is what
	// the checkout now holds for it, and what ends that.
	if writesPerEnvironmentArtifacts(platforms) {
		for _, name := range envs.names() {
			if _, ok := specs[name]; !ok {
				fmt.Fprint(w, missingContractLine(name, o.product.ID != "", isRegularFile(SpecPath(name))))
			}
		}
	}
```
  (b) `internal/backend/app_environments.go` içinde `func writeSpec(env string, spec []byte) error {` satırının hemen üstüne ekle:
```go
// missingContractLine is what a link says about an environment it wrote a
// config for and no contract (FR-014) — `earlier` when a contract an earlier
// link wrote is still on disk, which is then what a build reads.
//
// WHAT ENDS IT DEPENDS ON WHERE THE CONTRACT COMES FROM. A project's environment
// serves what was last pushed to it, and --env names it. A stack somebody hosts,
// linked by address, has no project to name one in. The stack `palbase start`
// runs serves the directory it mounted, and `palbase push` refuses to publish to
// it (stack_push.go) — for `local` the cure is a start. Each ends with a link:
// that is what brings the contract into this checkout.
func missingContractLine(name string, project, earlier bool) string {
	cure := "`palbase push`, then `palbase link` here"
	switch {
	case name == localEnvName:
		cure = "`palbase start` in the backend, then `palbase link` here"
	case project:
		cure = fmt.Sprintf("`palbase push --env %s`, then `palbase link` here", envname.Label(name))
	}
	spec := shownEnvDir(name) + "/openapi.json"
	if earlier {
		return fmt.Sprintf("%s gave no contract this time, so %s is the one an earlier link wrote — %s\n",
			envname.Label(name), spec, cure)
	}
	return fmt.Sprintf("%s has no contract yet, so %s is not written and no client is generated for it — %s\n",
		envname.Label(name), spec, cure)
}

```
  (c) aynı dosyada `gatherEnvironments`'ın projesiz dalındaki
```go
		case errors.Is(err, ErrNoContractYet):
			fmt.Fprintf(w, "%v\n", err)
			fmt.Fprintf(w, "  the link is recorded; `palbase spec` fills the contract in once something answers\n")
```
  bloğunu şununla değiştir (TEK çare: dosya alan bir checkout'ta çare missingContractLine'dadır):
```go
		case errors.Is(err, ErrNoContractYet):
			fmt.Fprintf(w, "%v\n", err)
			// ONE CURE PER MISSING CONTRACT. A checkout that gets files hears
			// what ends this beside them (missingContractLine, FR-014); saying
			// "`palbase spec` fills it in" here as well was two different
			// next steps for one gap.
			if !writeArtifacts {
				fmt.Fprintf(w, "  the link is recorded; `palbase spec` fills the contract in once something answers\n")
			}
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/backend/ -run 'TestALinkSaysWhichEnvironmentsGotAContract|TestAnEnvironmentThatGaveNoContractThisTimeKeepsTheEarlierOne|TestTheMissingContractLineNamesWhatEndsIt|TestABackendLinkWithNoContractStillSaysSpecFillsItIn|TestAnEnvironmentWithNothingDeployedIsWrittenAndNamed|TestAnEnvironmentWithNoContractKeepsTheProjectsOwnSentence' -count=1 -v` · Beklenen: `--- PASS` ×6 (`TestAnEnvironmentWithNothingDeployedIsWrittenAndNamed`, `TestAnEnvironmentWithNoContractKeepsTheProjectsOwnSentence`, `TestALinkSaysWhichEnvironmentsGotAContract`, `TestAnEnvironmentThatGaveNoContractThisTimeKeepsTheEarlierOne`, `TestTheMissingContractLineNamesWhatEndsIt` iki alt testiyle, `TestABackendLinkWithNoContractStillSaysSpecFillsItIn`), `ok  	github.com/palgroup/palbase-cli/internal/backend`. Gerileme: `go test ./internal/backend/ -run 'Link|Contract|Deployed|Web|Spec|Nothing' -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/internal/backend	52.537s`.
- [ ] **Adım 5: Commit** — `git add internal/backend/project_link.go internal/backend/app_environments.go internal/backend/link_contract_lines_test.go && git commit -m "fix(link): yazılan her openapi.json basılır; sözleşmesi olmayan ortam neyin biteceğiyle adlanır — push --env, adresle bağlıda push, local'de start; sonra link — ve yanında ikinci bir çare söylenmez (FR-014)" -- internal/backend/project_link.go internal/backend/app_environments.go internal/backend/link_contract_lines_test.go`

---

### T020: Android OAuth seçimi belirsizse neden ve commit edilecek `oauth.android` JSON'u söylenir
<!-- deps: [T017] | files: [internal/backend/link_android_oauth_test.go, internal/backend/social_link.go] | satisfies: [FR-017] -->

Bir Gradle dosyası `productFlavors` ya da `applicationIdSuffix` içeriyorsa `nativeIdentifiers` hiçbir kimlik döndürmüyor (20e5d7e'de `social_link.go:234` regex, `:248-250` `return nil`) — doğru: yazılı `applicationId` her build'in kurulduğu kimlik değil. Ama ret yalnız "social sign-in for android needs application_key and variant in palbase/project.json oauth.android; the checkout does not identify one configured target" diyor (20e5d7e'de `:179-180`): ne sebebi (hangi dosya, hangi anahtar), ne de az önce okunan istemcilerde (`available`) duran değerleri (`reports/verification-2026-09-25.md` B9, L6). Varsayılan ortamda bu link'i düşürüyor. Bu görev Android dosya okumasını `androidIdentifiers()`'e çıkarır (kimlikler + neden), `nativeIdentifiers`'ı onun ince sarmalayıcısı yapar ve Android reddine (1) nedeni — "`<dosya>` mentions `<anahtar>`, so the applicationId it declares is not the one every build installs as" — (2) tek seçimin her build type'a uygulandığını ve (3) her aday üçlü için `application_key`/`variant`/`package` etiketiyle commit edilecek `"oauth": {"android": {…}}` satırını ekler. Değerler JSON'da `json.Marshal` ile, etikette `%q` ile basılır (sunucudan gelen metin; mevcut `selectionDoesNotFit` de `%q` kullanıyor). Neden "declares" değil "mentions": regex yorumları da eşliyor (B9) ve cümle her durumda doğru kalmalı. iOS reddi değişmez (`TestLinkRefusesUnidentifiedNativeApplication`).

Not: test dosyasının adı `link_android_oauth_test.go` — `…_android_test.go` ile biten bir dosya Go'da `GOOS=android` kısıtıdır ve macOS/Linux'ta derlenmez (ölçüldü: ilk ad `link_oauth_android_test.go` ile `ok … [no tests to run]`).

**Interfaces:**
- Consumes: `androidBuildFiles` (T017), `androidVariantConfiguration`, `androidApplicationIDPattern`, `platformEnvironments(ctx, *Target, platform, appEnvironments)`; test yardımcıları `inScratchCheckout`, `writeFile`, `linkedAs`; `sealedclient.KeysetPath`.
- Produces: `func androidIdentifiers() (identifiers []string, why string)` (`social_link.go`) — checkout'un TEK `applicationId`'si ya da hiçbiri; bir dosya flavor/sonek içeriyorsa `why` = `"<dosya> mentions <anahtar>"` ve kimlik yok. `nativeIdentifiers("android")` = onun ilk dönüşü. Ret metninin sonu: `— <why>, so the applicationId it declares is not the one every build installs as.\n  One selection applies to every build type; commit the one this app signs in as in palbase/project.json —\n  application_key "<k>", variant "<v>", package "<p>":\n    "oauth": {"android": {"application_key": "<k>", "variant": "<v>"}}` (adaylar sıralı, tekrarsız; aday yoksa `One selection …` bloğu hiç basılmaz — boş menü yok). Test yardımcısı `androidClientsServer(t, clients ...[3]string) *httptest.Server`.

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_android_oauth_test.go`:
```go
package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/sealedclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// androidClientsServer answers the social-auth read with one enabled Google
// Android client per {application_key, variant, package_name}. That read is all
// a link asks before it chooses; the choice is where this test ends.
func androidClientsServer(t *testing.T, clients ...[3]string) *httptest.Server {
	t.Helper()
	native := []any{}
	for _, c := range clients {
		native = append(native, map[string]any{"key": "google-android-" + c[0] + "-" + c[1], "enabled": true,
			"application_key": c[0], "platform": "android", "variant": c[1], "package_name": c[2],
			"android_client_id": "ANDROID.apps.googleusercontent.com", "server_client_id": "SERVER.apps.googleusercontent.com",
			"signing_certificate_sha1": "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD"})
	}
	providers := map[string]any{}
	for _, name := range []string{"google", "apple", "microsoft", "github"} {
		providers[name] = map[string]any{"enabled": false, "browser_clients": []any{}, "native_clients": []any{}}
	}
	providers["google"] = map[string]any{"enabled": true, "browser_clients": []any{}, "native_clients": native}
	admin, err := json.Marshal(map[string]any{"contract_revision": 1, "credentials": []any{}, "providers": providers})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sealedclient.KeysetPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Palbase-Auth-Contract", "1")
		if r.URL.Path == "/v1/management/auth/social-auth" {
			_, _ = w.Write(admin)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	linkedAs(t, srv.URL, "operator")
	return srv
}

// AN ANDROID APP LINK CANNOT IDENTIFY SAYS WHY, AND WHAT TO COMMIT (FR-017).
//
// Flavors and an applicationIdSuffix both mean the literal applicationId is not
// the one every build installs as, so link stops guessing — correctly. But the
// refusal said only "the checkout does not identify one configured target"
// (verification B9, L6): not that the Gradle file was the reason, and not the
// values to write, although the clients it had just read carry them.
func TestAnAndroidAppLinkCannotIdentifyNamesTheReasonAndTheSelectionToCommit(t *testing.T) {
	for _, c := range []struct {
		name, gradle, why string
		committed         OAuthSelection
		clients           [][3]string
		choices           string
	}{
		{
			name:   "flavors",
			gradle: `android { defaultConfig { applicationId = "com.example.app" }; productFlavors { create("demo") { } } }`,
			why:    "app/build.gradle.kts mentions productFlavors",
			clients: [][3]string{
				{"shop", "release", "com.example.app"},
				{"shop-demo", "release", "com.example.app.demo"},
			},
			choices: `  application_key "shop", variant "release", package "com.example.app":` + "\n" +
				`    "oauth": {"android": {"application_key": "shop", "variant": "release"}}` + "\n" +
				`  application_key "shop-demo", variant "release", package "com.example.app.demo":` + "\n" +
				`    "oauth": {"android": {"application_key": "shop-demo", "variant": "release"}}`,
		},
		{
			name:    "a suffix",
			gradle:  `android { defaultConfig { applicationId = "com.example.app" }; buildTypes { debug { applicationIdSuffix = ".debug" } } }`,
			why:     "app/build.gradle.kts mentions applicationIdSuffix",
			clients: [][3]string{{"shop", "debug", "com.example.app.debug"}},
			choices: `  application_key "shop", variant "debug", package "com.example.app.debug":` + "\n" +
				`    "oauth": {"android": {"application_key": "shop", "variant": "debug"}}`,
		},
		{
			// Half a selection committed, and no Android client configured to
			// complete it from: the reason stands alone, with no empty menu.
			name:      "nothing to choose from",
			gradle:    `android { defaultConfig { applicationId = "com.example.app" }; productFlavors { create("demo") { } } }`,
			why:       "app/build.gradle.kts mentions productFlavors",
			committed: OAuthSelection{ApplicationKey: "shop"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			inScratchCheckout(t)
			require.NoError(t, os.MkdirAll("app", 0o755))
			writeFile(t, filepath.Join("app", "build.gradle.kts"), c.gradle)
			main := androidClientsServer(t, c.clients...)
			source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
				"main": {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_env_cPUBLIC"},
			}}

			target := Target{URL: main.URL, OAuth: map[string]OAuthSelection{"android": c.committed}}
			_, _, err := platformEnvironments(context.Background(), &target, "android", source)

			want := "main/android: social sign-in for android needs application_key and variant in " +
				"palbase/project.json oauth.android; the checkout does not identify one configured target — " +
				c.why + ", so the applicationId it declares is not the one every build installs as."
			if c.choices != "" {
				want += "\n  One selection applies to every build type; commit the one this app signs in as in palbase/project.json —\n" +
					c.choices
			}
			require.Error(t, err)
			assert.Equal(t, want, err.Error())
		})
	}
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestAnAndroidAppLinkCannotIdentifyNamesTheReasonAndTheSelectionToCommit' -count=1` · Beklenen: **FAIL** — üç alt testte de (`/flavors`, `/a_suffix`, `/nothing_to_choose_from`) `actual  : "main/android: social sign-in for android needs application_key and variant in palbase/project.json oauth.android; the checkout does not identify one configured target"` (neden yok, commit edilecek satır yok).
- [ ] **Adım 3: Uygula** — `internal/backend/social_link.go`:
  (a) `linkedOAuth` içinde şu bloğu:
```go
		} else {
			return nil, selection, fmt.Errorf("social sign-in for %s needs application_key and variant in %s oauth.%s; "+
				"the checkout does not identify one configured target", platform, projectPath(), platform)
		}
```
  şununla değiştir:
```go
		} else {
			refusal := fmt.Sprintf("social sign-in for %s needs application_key and variant in %s oauth.%s; "+
				"the checkout does not identify one configured target", platform, projectPath(), platform)
			if platform == "android" {
				// WHY, AND WHAT TO COMMIT (FR-017). The refusal is right — a
				// flavored or suffixed build installs under an id no file spells
				// — but it named neither the reason nor the values, and the
				// clients just read carry them.
				if _, why := androidIdentifiers(); why != "" {
					refusal += " — " + why + ", so the applicationId it declares is not the one every build installs as."
				}
				choices := make([]string, 0, len(available))
				for _, c := range available {
					key, _ := json.Marshal(c.ApplicationKey)
					variant, _ := json.Marshal(c.Variant)
					choice := fmt.Sprintf("  application_key %q, variant %q, package %q:\n"+
						`    "oauth": {"android": {"application_key": %s, "variant": %s}}`,
						c.ApplicationKey, c.Variant, c.PackageName, key, variant)
					if !slices.Contains(choices, choice) {
						choices = append(choices, choice)
					}
				}
				if len(choices) > 0 {
					slices.Sort(choices)
					refusal += "\n  One selection applies to every build type; commit the one this app signs in as in " +
						projectPath() + " —\n" + strings.Join(choices, "\n")
				}
			}
			return nil, selection, errors.New(refusal)
		}
```
  (b) şu bloğu (T017'den sonraki hâli):
```go
func nativeIdentifiers(platform string) []string {
	if platform == "android" {
		root, _ := os.Getwd()
		var identifiers []string
		for _, path := range androidBuildFiles {
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil {
				continue
			}
			// A literal defaultConfig ID does not identify the final application
			// when Gradle applies flavors or build-type suffixes. Require the
			// explicit OAuth selection; the Gradle plugin checks the final ID.
			if androidVariantConfiguration.Match(raw) {
				return nil
			}
			for _, match := range androidApplicationIDPattern.FindAllSubmatch(raw, -1) {
				identifiers = append(identifiers, string(match[1]))
			}
		}
		if len(identifiers) == 1 && !strings.Contains(identifiers[0], "$") {
			return identifiers
		}
		return nil
	}
```
  şununla değiştir:
```go
// androidIdentifiers is the one applicationId this checkout's Gradle files
// declare, or none — and, when a file mentions flavors or a suffix, why: then
// the literal id is not the one every build installs as.
func androidIdentifiers() (identifiers []string, why string) {
	root, _ := os.Getwd()
	for _, path := range androidBuildFiles {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			continue
		}
		// A literal defaultConfig ID does not identify the final application
		// when Gradle applies flavors or build-type suffixes. Require the
		// explicit OAuth selection; the Gradle plugin checks the final ID.
		if found := androidVariantConfiguration.Find(raw); found != nil {
			return nil, path + " mentions " + string(found)
		}
		for _, match := range androidApplicationIDPattern.FindAllSubmatch(raw, -1) {
			identifiers = append(identifiers, string(match[1]))
		}
	}
	if len(identifiers) == 1 && !strings.Contains(identifiers[0], "$") {
		return identifiers, ""
	}
	return nil, ""
}

func nativeIdentifiers(platform string) []string {
	if platform == "android" {
		identifiers, _ := androidIdentifiers()
		return identifiers
	}
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/backend/ -run 'TestAnAndroidAppLinkCannotIdentifyNamesTheReasonAndTheSelectionToCommit|TestOAuthInferenceRequiresAnUnambiguousAndroidApplication|TestLinkRefusesUnidentifiedNativeApplication|TestAnAndroidEnvironmentIsReadUnderThePackageItsGradleDeclares' -count=1 -v` · Beklenen: `--- PASS: TestAnAndroidAppLinkCannotIdentifyNamesTheReasonAndTheSelectionToCommit` (`/flavors`, `/a_suffix`, `/nothing_to_choose_from`), `--- PASS: TestAnAndroidEnvironmentIsReadUnderThePackageItsGradleDeclares`, `--- PASS: TestOAuthInferenceRequiresAnUnambiguousAndroidApplication`, `--- PASS: TestLinkRefusesUnidentifiedNativeApplication`, `ok  	github.com/palgroup/palbase-cli/internal/backend` (son üçü mevcut: tek kimlik çıkarımı, iOS reddi, paketle seçim — değişmedi).
- [ ] **Adım 5: Commit** — `git add internal/backend/social_link.go internal/backend/link_android_oauth_test.go && git commit -m "fix(link): Android OAuth seçimi flavor ya da applicationIdSuffix yüzünden belirsizse nedeni ve commit edilecek oauth.android JSON'u aday üçlülerden söylenir (FR-017)" -- internal/backend/social_link.go internal/backend/link_android_oauth_test.go`

---

### T021: `status` anahtar kaymasını diskte config'i olan her platform için ölçer
<!-- deps: [T007] | files: [internal/backend/status_project.go, internal/backend/app_environments.go, internal/backend/status_key_platforms_test.go, internal/backend/status_key_env_test.go] | satisfies: [FR-018] -->

`reportKeyDrift` ve `appKeyState` yalnız `readAppEnvironments("ios")` okuyordu (`status_project.go:236` ve `:320`, doğrulama B7). Android ya da web checkout'unda harita boş kalıyor, metin çıktısında hiç `app key:` satırı çıkmıyor, `--json` da bayat bir anahtar için "unchecked" diyordu. iOS+Android checkout'unda ise iOS "current" çıkıyor, bayat Android anahtarı görünmüyordu. Bu görevden sonra `knownPlatforms` sırasıyla (ios, macos, android, web) diskte config'i olan her platform okunur. Proje **bir kez** sorulur. Her satır, konuştuğu platformları parantez içinde adlandırır. Karşılaştırılan ortam önceki kuralla belirlenir: seçilen ortam, yoksa diskin varsayılanı. Diskin varsayılanı artık bütün platformların adları birlikte ele alınarak bulunur, böylece bir rapor tek bir ortamdan söz eder. JSON tek kelime kalır. Herhangi bir platform bayatsa `stale` döner. `current` ancak diskteki her platform karşılaştırılmış ve güncel çıkmışsa döner. Anahtarsız bir config de (eski bir CLI'ın durmuş yerel yığın için yazdığı `"api_key": ""`) metinde adlandırılır: taslakta `keyless` doluyor ama basılmıyordu — JSON "unchecked" derken metin yalnız `current (ios)` gösteriyordu, sessizlik "sorun yok" diye okunuyordu (eleştirmen bulgusu, burada kırmızısı görüldü).

**Bilerek değiştirilen mevcut test:** `TestKeyDriftSaysWhenTheResolvedEnvironmentHasNoConfig` şimdiye kadar `app key:      unchecked — staging has no committed config` bekliyordu. Artık `app key:      unchecked (ios) — staging has no committed config` bekliyor. Satır, okuduğu platformu adlandırıyor. Başka bir platformda staging bulunabilir ve "unchecked" yalnız bu platformun cevabı. Bu değişiklik testi zayıflatmıyor, sıkılaştırıyor. `seedIOSKeys` yeni `seedKeys`'e devredilir (aynı dosyaları yazar).

**Interfaces:**
- Consumes: `envname.Label` (T007), `knownPlatforms` (`project_link.go`), `readAppEnvironments`, `defaultEnvironment`, `projectPublishableKey`, test yardımcıları `inScratchCheckout`, `stackServing`, `runStatus`
- Produces:
  - `func measureAppKeys(ctx context.Context, target Target, env string) appKeyDrift`
  - `type appKeyDrift struct{ env string; current, stale, missing, keyless []string; err error }`
  - `func diskDefault(names []string) string` (`app_environments.go`; `readAppEnvironments` bunu kullanır)
  - Test yardımcıları: `seedKeys(t, platform, url string, keys map[string]string)`, `stackRunningHere(t, key string) string`, `appKeyOf(t) any`
  - Satırlar:
    - `app key:      STALE (<platformlar>) — <env> ships a key this project no longer hands out.`
    - `app key:      could not be checked (<err>)`
    - `app key:      unchecked (<platformlar>) — <env> has no committed config here; `palbase link` writes it`
    - `app key:      unchecked (<platformlar>) — <env> ships no key here; `palbase link` writes it` (config'i var ama `api_key`'i boş — eski bir CLI'ın durmuş yığın için yazdığı)
    - `app key:      current (<platformlar>)`

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/status_key_platforms_test.go`:
```go
package backend

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// EVERY PLATFORM WITH A CONFIG ON DISK IS COMPARED (FR-018). Key drift read the
// iOS configs alone, so in an Android or web checkout it found nothing, printed
// nothing, and `--json` answered "unchecked" about a key that was stale.

// seedKeys writes one platform's config per environment, each carrying its key.
func seedKeys(t *testing.T, platform, url string, keys map[string]string) {
	t.Helper()
	for env, key := range keys {
		require.NoError(t, os.MkdirAll(EnvDir(env), 0o755))
		blob, err := json.MarshalIndent(appEnvironment{AppID: projectAppID, BaseURL: url, APIKey: key}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(ConfigPath(env, platform), blob, 0o644))
	}
}

// stackRunningHere is a stack `palbase start` runs for this checkout, handing
// out key; status compares the `local` environment against it.
func stackRunningHere(t *testing.T, key string) string {
	t.Helper()
	srv := stackServing(t, key, nil)
	require.NoError(t, WriteLocalTarget(Target{URL: srv.URL, Local: true}))
	require.NoError(t, StoreCredential(srv.URL, Credentials{Value: "k", Kind: KindKey}))
	return srv.URL
}

func appKeyOf(t *testing.T) any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(runStatus(t, true)), &doc))
	return doc["app_key"]
}

func TestKeyDriftReadsAnAndroidOnlyCheckout(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "android", url, map[string]string{"local": "pb_project_cLOCALOLD"})

	assert.Contains(t, runStatus(t, false),
		"app key:      STALE (android) — local ships a key this project no longer hands out.\n"+
			"              Run `palbase link` to refresh it, then rebuild the app.\n")
	assert.Equal(t, "stale", appKeyOf(t))
}

// ONE STALE PLATFORM IS NOT HIDDEN BY THE OTHERS: each line names the platforms
// it speaks for, so "current" can no longer be read as "every config I ship".
func TestKeyDriftNamesEachPlatformItCompared(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "ios", url, map[string]string{"local": "pb_project_cLOCALNOW"})
	seedKeys(t, "android", url, map[string]string{"local": "pb_project_cLOCALOLD"})
	seedKeys(t, webPlatform, url, map[string]string{"local": "pb_project_cLOCALNOW"})

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      STALE (android) — local ships a key this project no longer hands out.\n")
	assert.Contains(t, out, "app key:      current (ios, web)\n")
	assert.Equal(t, "stale", appKeyOf(t))
}

// A PLATFORM THAT DOES NOT CARRY THE ENVIRONMENT IS NAMED, not passed over:
// iOS being current says nothing about an Android config that is not there.
func TestKeyDriftNamesThePlatformThatLacksTheEnvironment(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "ios", url, map[string]string{"local": "pb_project_cLOCALNOW"})
	seedKeys(t, "android", url, map[string]string{"main": "pb_project_cMAIN"})

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      unchecked (android) — local has no committed config here; `palbase link` writes it\n")
	assert.Contains(t, out, "app key:      current (ios)\n")
	assert.Equal(t, "unchecked", appKeyOf(t), "a platform that could not be compared is not current")
}

// A CONFIG THAT CARRIES NO KEY IS NAMED TOO — what an older CLI wrote for a
// stack that was down. Silence beside "current (ios)" read as "every key this
// checkout ships is fine" while the Android build shipped none.
func TestKeyDriftNamesAPlatformWhoseConfigCarriesNoKey(t *testing.T) {
	inScratchCheckout(t)
	url := stackRunningHere(t, "pb_project_cLOCALNOW")
	seedKeys(t, "ios", url, map[string]string{"local": "pb_project_cLOCALNOW"})
	seedKeys(t, "android", url, map[string]string{"local": ""})

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      unchecked (android) — local ships no key here; `palbase link` writes it\n")
	assert.Contains(t, out, "app key:      current (ios)\n")
	assert.Equal(t, "unchecked", appKeyOf(t), "a platform that ships no key is not current")
}
```
  `internal/backend/status_key_env_test.go` dosyasında import listesinden `"os"` satırını sil. `seedIOSKeys`'in gövdesini değiştir:
```go
func seedIOSKeys(t *testing.T, url string, keys map[string]string) {
	t.Helper()
	seedKeys(t, "ios", url, keys)
}
```
  Aynı dosyada `TestKeyDriftSaysWhenTheResolvedEnvironmentHasNoConfig`'teki `assert.Contains(t, out.String(), "app key:      unchecked — staging has no committed config")` satırını şununla değiştir:
```go
	// The line names the platform whose configs were read (FR-018): another
	// platform may carry staging, and "unchecked" is only this one's answer.
	assert.Contains(t, out.String(), "app key:      unchecked (ios) — staging has no committed config")
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -count=1 -run 'TestKeyDrift'` · Beklenen: **FAIL**, beş test:
  - `--- FAIL: TestKeyDriftReadsAnAndroidOnlyCheckout`. Çıktı `runtime: … not this checkout's dependency\n` ile bitiyor: hiç `app key:` satırı yok, ardından `does not contain "app key:      STALE (android) — local ships a key …"` geliyor. JSON için `expected: "stale"` / `actual  : "unchecked"`.
  - `--- FAIL: TestKeyDriftNamesEachPlatformItCompared`. Çıktıda `app key:      current\n" does not contain "app key:      STALE (android) …`. JSON için `expected: "stale"` / `actual  : "current"`.
  - `--- FAIL: TestKeyDriftNamesThePlatformThatLacksTheEnvironment`. JSON için `expected: "unchecked"` / `actual  : "current"`.
  - `--- FAIL: TestKeyDriftSaysWhenTheResolvedEnvironmentHasNoConfig`. Çıktıda `"app key:      unchecked — staging has no committed config here; `palbase link` writes it\n" does not contain "app key:      unchecked (ios) — staging has no committed config"`.
  - `--- FAIL: TestKeyDriftNamesAPlatformWhoseConfigCarriesNoKey`. Çıktıda `app key:      current` var, `unchecked (android) — local ships no key here` yok; JSON için `expected: "unchecked"` / `actual  : "current"`.
- [ ] **Adım 3: Uygula** — `internal/backend/status_project.go` import bloğunun sonunu değiştir:
```go
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/envname"
)
```
  `// reportKeyDrift compares the key this app SHIPS …` yorumundan `reportKeyDrift` fonksiyonunun kapanışına kadar olan bloğun tamamını şununla değiştir:
```go
// reportKeyDrift compares the key this app SHIPS with the one the project hands
// out now.
//
// The drift is ordinary and its symptom is not: a rotated publishable key leaves
// every installed build authenticating with something the project no longer
// accepts, and the app reports it as a sign-in failure. Nothing else in this CLI
// would notice, because the committed slot is a file and files do not expire.
//
// EVERY PLATFORM THIS CHECKOUT SHIPS, EACH NAMED (FR-018). It read the iOS
// configs alone, so an Android or web checkout got no line at all — and a
// checkout with both got "current" for iOS while its Android key was stale.
func reportKeyDrift(ctx context.Context, target Target, env string, cred Credentials, out io.Writer) {
	d := measureAppKeys(ctx, target, env)
	if len(d.stale) > 0 {
		// The keys themselves are not printed — a publishable key is not a secret,
		// but printing two nearly-identical strings invites reading them for the
		// difference instead of running the command that fixes it.
		fmt.Fprintf(out, "app key:      STALE (%s) — %s ships a key this project no longer hands out.\n",
			strings.Join(d.stale, ", "), envname.Label(d.env))
		fmt.Fprintln(out, "              Run `palbase link` to refresh it, then rebuild the app.")
	}
	if d.err != nil {
		// Cannot check is not the same as checked. Silence here would read as
		// "the key is fine".
		fmt.Fprintf(out, "app key:      could not be checked (%v)\n", d.err)
	}
	if len(d.missing) > 0 {
		// Silence would read as "the key is fine" here too: other environments
		// are on disk, the one this command acts on is not.
		fmt.Fprintf(out, "app key:      unchecked (%s) — %s has no committed config here; `palbase link` writes it\n",
			strings.Join(d.missing, ", "), envname.Label(d.env))
	}
	if len(d.keyless) > 0 {
		// And a config that carries no key — what an older CLI wrote for a
		// stack that was down — ships nothing to compare.
		fmt.Fprintf(out, "app key:      unchecked (%s) — %s ships no key here; `palbase link` writes it\n",
			strings.Join(d.keyless, ", "), envname.Label(d.env))
	}
	if len(d.current) > 0 {
		fmt.Fprintf(out, "app key:      current (%s)\n", strings.Join(d.current, ", "))
	}
}

// appKeyDrift is what the keys this checkout ships say about one environment,
// platform by platform.
type appKeyDrift struct {
	env     string   // the environment compared
	current []string // platforms whose key is the one the project hands out
	stale   []string // platforms whose key the project no longer hands out
	missing []string // platforms with configs here, none of them for env
	keyless []string // platforms whose config for env carries no key
	err     error    // the project could not be asked
}

// measureAppKeys compares env's key on every platform with a config on disk,
// in knownPlatforms' order, and asks the project once.
//
// EVERY ENVIRONMENT IS ON DISK after a link, so the key to compare is the one
// of the environment this command resolved; with none, the one a build without
// a choice uses — decided over every platform's environments together, so one
// report speaks of one environment.
func measureAppKeys(ctx context.Context, target Target, env string) appKeyDrift {
	onDisk := map[string]appEnvironments{}
	var platforms, names []string
	for _, platform := range knownPlatforms {
		envs, err := readAppEnvironments(platform)
		if err != nil || len(envs.Environments) == 0 {
			continue
		}
		onDisk[platform] = envs
		platforms = append(platforms, platform)
		names = append(names, envs.names()...)
	}
	d := appKeyDrift{env: env}
	if len(platforms) == 0 {
		return d
	}
	if d.env == "" {
		d.env = diskDefault(names)
	}
	var keyed []string
	for _, platform := range platforms {
		entry, ok := onDisk[platform].Environments[d.env]
		switch {
		case !ok:
			d.missing = append(d.missing, platform)
		case entry.APIKey == "":
			d.keyless = append(d.keyless, platform)
		default:
			keyed = append(keyed, platform)
		}
	}
	if len(keyed) == 0 {
		return d
	}
	current, err := projectPublishableKey(ctx, target)
	if err != nil {
		d.err = err
		return d
	}
	for _, platform := range keyed {
		if onDisk[platform].Environments[d.env].APIKey == current {
			d.current = append(d.current, platform)
		} else {
			d.stale = append(d.stale, platform)
		}
	}
	return d
}
```
  Dosyanın sonundaki `appKeyState`'i (yorumu dahil) şununla değiştir:
```go
// appKeyState is reportKeyDrift's finding as one word.
//
// "unchecked" covers both "this checkout ships no key" and "the project could
// not be asked" — a script that must not run against a stale key treats them the
// same, and calling either of them "current" is the failure this reports. So
// one stale platform makes the answer "stale", and "current" needs every
// platform on disk compared and current.
func appKeyState(ctx context.Context, target Target, env string) string {
	d := measureAppKeys(ctx, target, env)
	switch {
	case len(d.stale) > 0:
		return "stale"
	case len(d.current) > 0 && len(d.missing) == 0 && len(d.keyless) == 0:
		return "current"
	default:
		return "unchecked"
	}
}
```
  `internal/backend/app_environments.go` içinde `readAppEnvironments`'in son bloğunu değiştir. Eski blok şu:
```go
	// `local` is not the default while any other environment is here: a build
	// that forgot to say which environment it wanted must not silently talk to a
	// developer's laptop. A checkout that carries only `local` has nothing else the
	// default could be.
	out.Default = defaultEnvironment(out.names())
	if out.Default == "" && len(out.Environments) > 0 {
		out.Default = out.names()[0]
	}
	return out, nil
}
```
  Yerine gelecek blok:
```go
	out.Default = diskDefault(out.names())
	return out, nil
}

// diskDefault is the environment a checkout carrying these names acts on when
// nothing chose one.
//
// `local` is not the default while any other environment is here: a build
// that forgot to say which environment it wanted must not silently talk to a
// developer's laptop. A checkout that carries only `local` has nothing else the
// default could be.
func diskDefault(names []string) string {
	if d := defaultEnvironment(names); d != "" {
		return d
	}
	if len(names) == 0 {
		return ""
	}
	return slices.Min(names)
}
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/backend/ -count=1 -run 'TestKeyDrift|TestStatusCompares|TestAStaleAppKey|TestACurrentAppKey|TestAKeyThatCannot|TestStatus' -v` · Beklenen: `--- PASS: TestKeyDriftReadsAnAndroidOnlyCheckout`, `--- PASS: TestKeyDriftNamesEachPlatformItCompared`, `--- PASS: TestKeyDriftNamesThePlatformThatLacksTheEnvironment`, `--- PASS: TestKeyDriftNamesAPlatformWhoseConfigCarriesNoKey`, `--- PASS: TestKeyDriftSaysWhenTheResolvedEnvironmentHasNoConfig`, mevcut `TestStatus*`/`TestAStaleAppKeyIsReported`/`TestACurrentAppKeySaysSo`/`TestAKeyThatCannotBeCheckedSaysThat` PASS, `ok  	github.com/palgroup/palbase-cli/internal/backend	1.398s`. Daha geniş koşu: `go test ./internal/backend/ -count=1 -run 'Status|KeyDrift|AppKey|Spec|Default|Environment'` · Beklenen: `ok  	github.com/palgroup/palbase-cli/internal/backend	22.867s`. Anahtarsız satırın bekçisi ölçüldü: `keyless` bloğu olmadan (taslağın `reportKeyDrift`'i) `TestKeyDriftNamesAPlatformWhoseConfigCarriesNoKey` metin iddiasında düşer.
- [ ] **Adım 5: Commit** — `git add internal/backend/status_project.go internal/backend/app_environments.go internal/backend/status_key_platforms_test.go internal/backend/status_key_env_test.go && git commit -m "fix(status): anahtar kayması diskte config'i olan her platform için ölçülür ve satır platformları adlandırır — Android/web checkout'u ve anahtarsız config artık sessiz değil (FR-018)" -- internal/backend/status_project.go internal/backend/app_environments.go internal/backend/status_key_platforms_test.go internal/backend/status_key_env_test.go`

---

### T022: `doctor` Android bölümü — her ortam dizininin tamlığı
<!-- deps: [T007, T013, T014, T017, T018] | files: [internal/backend/android_doctor.go, internal/backend/planes.go, internal/backend/app_environments.go, cmd/palbase/doctor.go, cmd/palbase/doctor_android_section_test.go] | satisfies: [FR-019] -->

`doctor` bugün bulutu, girişi, PAT'i, link/env bağını, Docker'ı, Node'u ve Bun'ı yokluyor (`cmd/palbase/doctor.go:148-216`). Bir Android build'inin okuduğu hiçbir şeye bakmıyor, bu yüzden anahtarsız bir config ya da eksik bir sözleşme ancak Gradle hatası olarak çıkıyor. Bu görev çıktının **sonuna**, kendi başlığı (`android (<build dosyası>)`) altında bir bölüm ekler. Bölümde `palbase/environments/` altındaki her dizin için bir satır bulunur. Satır, plugin'in okuduğu şeyleri denetler: `android-config.json` var mı ve JSON olarak okunuyor mu, `api_key` boş değil mi, `openapi.json` var mı, üst düzeyde `x-palbase-roles` alanı taşıyor mu. Alan metin aramasıyla değil anahtar olarak aranır (`Roles.kt`'nin dersi). Her sorun, onu neyin bitireceğini söyler:
- Sözleşme eksikse cümle T018'in `missingContractLine`'ıyla **aynı kaynaktan** gelir. Bunun için `contractCure(name, project)` fonksiyonu ayrılır. Projeye bağlı checkout'ta `palbase push --env <env>`, adresle bağlıda `palbase push`, `local`'de `palbase start` önerilir.
- `local/` config'i eksikse öneri `palbase start` + `palbase link` olur.
- İki dizin yalnız harf büyüklüğünde farklıysa (yalnız büyük/küçük harf ayıran bir diskte mümkün) ikisi de adlandırılır.

"Android checkout" kararı `palbase link`'in kararıyla aynıdır. `androidBuildFiles` (T017) içinde `applicationId` taşıyan ilk dosya aranır. Bunun için `detectAndroidApplicationID` dosya adını da döndüren `androidApplicationFile`'a ayrılır. Yalnız bir config dosyası kanıt sayılmaz: bir backend reposunda tek bir `link --platform android` koşusundan kalmış olabilir. Test dosyasının adı `doctor_android_section_test.go`'dur, çünkü `_android_test.go` yalnız GOOS=android için derlenir (T020 notu).

**Interfaces:**
- Consumes: `androidBuildFiles` (T017), `missingContractLine` (T018), `shownEnvDir` (T014), `envname.Label` (T007), `ConfigPath`/`SpecPath`/`EnvDir`, `localEnvName`, `appEnvironment`. Test yardımcısı olarak `runDoctorIn(t, dir)` (T013, `doctor_local_test.go`).
- Produces:
  - `type DoctorLine struct{ OK bool; Label, Detail string }` (backend, dışa açık)
  - `func AndroidCheckout(dir string) string`: Android'i yapan Gradle dosyası (checkout'a göreli, slash biçiminde) ya da `""`
  - `func AndroidDoctor(dir string, project bool) []DoctorLine`
  - `func androidApplicationFile(root string) (file, id string, err error)` (`planes.go`)
  - `func contractCure(name string, project bool) string` (`app_environments.go`)
  - `func environmentDirsIn(dir string) []string`, `func environmentProblems(dir, name string, project bool) []string`, `func readJSONFile(path string, v any) (found bool, err error)`
  - `doctor` çıktısı: son bölüm `android (<file>)` başlığı ve `  ✓/✗ <env>  …` satırları
  - Test yardımcıları (`doctor_android_section_test.go`): `writeFileIn(t, dir, rel, body)`, `androidCheckoutIn(t, dir)`, `withRoles`/`withoutRoles` sabitleri, `androidConfig(baseURL, key) string`, `androidEnvironmentIn(t, dir, env, config, contract)`, `linkedToAProject(t, dir)`, `caseInsensitive(t, dir) bool`

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `cmd/palbase/doctor_android_section_test.go`:
```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// AN ANDROID CHECKOUT GETS ITS OWN SECTION (FR-019). Doctor probed the cloud,
// the login, the link, Docker, Node and Bun, and nothing an Android build reads:
// a missing contract, a keyless config, a release build with no environment
// all surfaced as a Gradle failure instead.
//
// (Not `doctor_android_test.go`: a `_android` suffix makes Go compile the file
// for GOOS=android only, and its tests would silently not exist here.)

// writeFileIn writes body at the slash path rel under dir.
func writeFileIn(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// androidCheckoutIn makes dir an Android app checkout: the Gradle file `palbase
// link` detects Android from.
func androidCheckoutIn(t *testing.T, dir string) {
	t.Helper()
	writeFileIn(t, dir, "app/build.gradle.kts", "android {\n    defaultConfig {\n        applicationId = \"com.example.todo\"\n    }\n}\n")
}

// Contracts as the stack serves them: with the roles inside, and from before
// they were.
const (
	withRoles    = `{"openapi":"3.1.0","paths":{},"x-palbase-roles":{"roles":[]}}`
	withoutRoles = `{"openapi":"3.1.0","paths":{}}`
)

func androidConfig(baseURL, key string) string {
	return fmt.Sprintf(`{"app_id":"prj_1","base_url":%q,"api_key":%q}`, baseURL, key)
}

// androidEnvironmentIn writes an environment's two files where `palbase link`
// does; an empty body leaves that file out.
func androidEnvironmentIn(t *testing.T, dir, env, config, contract string) {
	t.Helper()
	if config != "" {
		writeFileIn(t, dir, "palbase/environments/"+env+"/android-config.json", config)
	}
	if contract != "" {
		writeFileIn(t, dir, "palbase/environments/"+env+"/openapi.json", contract)
	}
}

func linkedToAProject(t *testing.T, dir string) {
	t.Helper()
	writeFileIn(t, dir, "palbase/project.json", `{"project":"prd_a","name":"todoapp"}`+"\n")
}

// EVERY ENVIRONMENT DIRECTORY IS CHECKED for what the Gradle plugin reads: both
// files, a key in the config, the roles inside the contract. Each problem names
// what ends it.
func TestDoctorChecksEachAndroidEnvironment(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "staging", androidConfig("https://staging.example", "pb_staging_cK"), "")
	androidEnvironmentIn(t, dir, "featureX", androidConfig("https://featurex.example", ""), withoutRoles)
	androidEnvironmentIn(t, dir, "local", "", withRoles)
	androidEnvironmentIn(t, dir, "broken", "{", withRoles)

	require.Contains(t, runDoctorIn(t, dir), "android (app/build.gradle.kts)\n"+
		"  ✗ broken     android-config.json cannot be read (unexpected end of JSON input) — `palbase link` here\n"+
		"  ✗ featureX   android-config.json has no api_key — `palbase link` here; "+
		"openapi.json carries no x-palbase-roles, which the Gradle plugin refuses — upgrade @palbase/backend, then `palbase push`, then `palbase link` here\n"+
		"  ✗ local      no android-config.json — `palbase start` in the backend, then `palbase link` here\n"+
		"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n"+
		"  ✗ staging    no openapi.json — `palbase push`, then `palbase link` here\n")
}

// A CHECKOUT LINKED TO A PROJECT PUSHES TO ONE OF ITS ENVIRONMENTS, so the
// cure names it — the same sentence `palbase link` prints (FR-014).
func TestDoctorNamesTheEnvironmentAContractIsPushedTo(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	linkedToAProject(t, dir)
	androidEnvironmentIn(t, dir, "staging", androidConfig("https://staging.example", "pb_staging_cK"), "")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ staging    no openapi.json — `palbase push --env staging`, then `palbase link` here\n")
}

func TestDoctorSaysAnAndroidCheckoutHoldsNoEnvironmentYet(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)

	require.Contains(t, runDoctorIn(t, dir), "android (app/build.gradle.kts)\n"+
		"  ✗ envs       nothing under palbase/environments — `palbase link` here writes one directory per environment\n")
}

// REACT NATIVE AND FLUTTER keep the Gradle build under android/ (FR-015): the
// section is there too, named after the file that made it.
func TestDoctorFindsTheAndroidAppOfACrossPlatformCheckout(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, dir, "android/app/build.gradle", "android {\n    defaultConfig {\n        applicationId \"com.example.todo\"\n    }\n}\n")
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)

	require.Contains(t, runDoctorIn(t, dir), "android (android/app/build.gradle)\n"+
		"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n")
}

// NOT AN ANDROID CHECKOUT, NO SECTION — a config file is not the proof: a
// backend repository can hold one from a `link --platform android` run once.
func TestDoctorPrintsNoAndroidSectionWithoutAnAndroidApp(t *testing.T) {
	dir := t.TempDir()
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)

	require.NotContains(t, runDoctorIn(t, dir), "android (")
}

// TWO DIRECTORIES ONE DISK CANNOT TELL APART are named: only a case-sensitive
// disk can hold both, and a clone on macOS or Windows gets one of the two.
func TestDoctorNamesEnvironmentDirectoriesThatDifferOnlyInCase(t *testing.T) {
	dir := t.TempDir()
	if caseInsensitive(t, dir) {
		t.Skip("this disk ignores case, so it cannot hold two directories whose names differ only in case")
	}
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "Main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ Main       differs only in case from palbase/environments/main — a disk that ignores case (macOS, Windows) holds one of the two; rename one in the dashboard\n"+
			"  ✗ main       differs only in case from palbase/environments/Main — a disk that ignores case (macOS, Windows) holds one of the two; rename one in the dashboard\n")
}

// caseInsensitive reports whether the disk under dir ignores letter case.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "caseprobe")
	require.NoError(t, os.Mkdir(probe, 0o755))
	defer func() { require.NoError(t, os.Remove(probe)) }()
	_, err := os.Stat(filepath.Join(dir, "CASEPROBE"))
	return err == nil
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./cmd/palbase/ -count=1 -run 'TestDoctor' -v` · Beklenen: **FAIL**. Dört test kırmızı: `TestDoctorChecksEachAndroidEnvironment`, `TestDoctorNamesTheEnvironmentAContractIsPushedTo`, `TestDoctorSaysAnAndroidCheckoutHoldsNoEnvironmentYet`, `TestDoctorFindsTheAndroidAppOfACrossPlatformCheckout`. Hepsinde çıktı `  ✓ bun        1.4.2 (/opt/homebrew/bin/bun)\n" does not contain "android (app/build.gradle.kts)\n…` ile bitiyor, yani bölüm hiç yok. Negatif kontrol `TestDoctorPrintsNoAndroidSectionWithoutAnAndroidApp` şimdiden PASS. İkiz testi APFS'te `--- SKIP` verir. Büyük/küçük harf ayıran bir diskte ikiz testinin kırmızısı şöyle görülür (yalnız macOS; ubuntu CI'da test doğal olarak koşar):
  ```sh
  hdiutil create -size 64m -fs "Case-sensitive APFS" -volname cli5cs -type SPARSE "$SCRATCH/cs.sparseimage"
  hdiutil attach -nobrowse -mountpoint "$SCRATCH/csvol" "$SCRATCH/cs.sparseimage" && mkdir -p "$SCRATCH/csvol/tmp"
  TMPDIR="$SCRATCH/csvol/tmp" go test ./cmd/palbase/ -count=1 -run TestDoctorNamesEnvironmentDirectoriesThatDifferOnlyInCase -v
  ```
  Beklenen: `--- FAIL: TestDoctorNamesEnvironmentDirectoriesThatDifferOnlyInCase`, çıktı yine `✓ bun …\n` ile bitiyor. Adım 4'ten sonra `hdiutil detach "$SCRATCH/csvol"` çalıştır.
- [ ] **Adım 3: Uygula** — `internal/backend/planes.go`: `detectAndroidApplicationID`'i dosya adını da döndüren bir fonksiyona ayır. Bu fonksiyonun tamamını değiştir:
```go
func detectAndroidApplicationID(root string) (string, error) {
	_, id, err := androidApplicationFile(root)
	return id, err
}

// androidApplicationFile is the first of androidBuildFiles under root that
// declares an applicationId, and the id it declares.
func androidApplicationFile(root string) (file, id string, err error) {
	for _, name := range androidBuildFiles {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			continue
		}
		match := androidApplicationIDPattern.FindSubmatch(contents)
		if len(match) == 2 {
			return name, string(match[1]), nil
		}
	}
	return "", "", fmt.Errorf("applicationId not found in the Android Gradle files; pass --package-name")
}
```
  `internal/backend/app_environments.go`: `missingContractLine`'ın ilk satırlarını değiştir. Eski blok şu:
```go
	cure := "`palbase push`, then `palbase link` here"
	switch {
	case name == localEnvName:
		cure = "`palbase start` in the backend, then `palbase link` here"
	case project:
		cure = fmt.Sprintf("`palbase push --env %s`, then `palbase link` here", envname.Label(name))
	}
```
  Yerine gelecek satır:
```go
	cure := contractCure(name, project)
```
  Aynı dosyada `func writeSpec(` satırının hemen önüne ekle:
```go
// contractCure is what brings name's contract into this checkout — the cure
// missingContractLine and doctor's Android section both name.
func contractCure(name string, project bool) string {
	switch {
	case name == localEnvName:
		return "`palbase start` in the backend, then `palbase link` here"
	case project:
		return fmt.Sprintf("`palbase push --env %s`, then `palbase link` here", envname.Label(name))
	default:
		return "`palbase push`, then `palbase link` here"
	}
}
```
  Yeni dosya `internal/backend/android_doctor.go`:
```go
package backend

// android_doctor.go — `palbase doctor`'s Android section (FR-019).
//
// An Android build picks its environment by build type, from files `palbase
// link` wrote and keys a person wrote. Neither reached doctor: it probed the
// cloud, the login, the link and the toolchains, and a keyless config, a
// missing contract or a release build with no environment surfaced as a Gradle
// failure instead. This reads what the Gradle plugin reads and says what a
// build would trip on, naming the command that ends it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/palgroup/palbase-cli/internal/envname"
)

// DoctorLine is one row `palbase doctor` prints: whether it passed, a short
// label, and a detail that names the next command when it did not.
type DoctorLine struct {
	OK     bool
	Label  string
	Detail string
}

// AndroidCheckout names the Gradle file that makes dir an Android app checkout
// — the one `palbase link` detects Android from — or "" when none does.
//
// A CONFIG FILE IS NOT THE PROOF (hasAppleProject says why): a backend
// repository can hold an android-config.json from one `link --platform
// android`, and a section about Gradle keys there would be noise.
func AndroidCheckout(dir string) string {
	file, _, err := androidApplicationFile(dir)
	if err != nil {
		return ""
	}
	return file
}

// AndroidDoctor is doctor's Android section for the Android checkout at dir.
// project says whether dir is linked to a project, which decides what brings a
// missing contract in.
func AndroidDoctor(dir string, project bool) []DoctorLine {
	return environmentDirLines(dir, project)
}

// environmentDirLines is one line per directory under palbase/environments:
// what the Gradle plugin reads from it, and what it would refuse.
func environmentDirLines(dir string, project bool) []DoctorLine {
	names := environmentDirsIn(dir)
	if len(names) == 0 {
		return []DoctorLine{{
			Label:  "envs",
			Detail: "nothing under palbase/environments — `palbase link` here writes one directory per environment",
		}}
	}
	lines := make([]DoctorLine, 0, len(names))
	for _, name := range names {
		problems := environmentProblems(dir, name, project)
		for _, other := range names {
			if other != name && strings.EqualFold(other, name) {
				problems = append(problems, fmt.Sprintf("differs only in case from %s — a disk that ignores case "+
					"(macOS, Windows) holds one of the two; rename one in the dashboard", shownEnvDir(other)))
			}
		}
		if len(problems) == 0 {
			lines = append(lines, DoctorLine{OK: true, Label: envname.Label(name),
				Detail: "android-config.json with an api_key, openapi.json with x-palbase-roles"})
			continue
		}
		lines = append(lines, DoctorLine{Label: envname.Label(name), Detail: strings.Join(problems, "; ")})
	}
	return lines
}

// environmentDirsIn is the environment directories under dir, by their exact
// names on disk, sorted.
func environmentDirsIn(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, filepath.Dir(filepath.FromSlash(EnvDir("any")))))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names
}

// environmentProblems is what the Gradle plugin would refuse in name's
// directory: the config it packages, and the contract it generates from.
func environmentProblems(dir, name string, project bool) []string {
	// `local/` comes from the stack `palbase start` runs here, and from nothing
	// else: a link alone cannot fill it.
	configCure := "`palbase link` here"
	if name == localEnvName {
		configCure = "`palbase start` in the backend, then `palbase link` here"
	}
	var problems []string
	var config appEnvironment
	switch found, err := readJSONFile(filepath.Join(dir, filepath.FromSlash(ConfigPath(name, "android"))), &config); {
	case !found:
		problems = append(problems, "no android-config.json — "+configCure)
	case err != nil:
		problems = append(problems, fmt.Sprintf("android-config.json cannot be read (%v) — %s", err, configCure))
	case config.APIKey == "":
		problems = append(problems, "android-config.json has no api_key — "+configCure)
	}

	// THE FIELD, NOT THE TEXT. The plugin reads `x-palbase-roles` as a key of
	// the document; a contract that carries the string somewhere else — an
	// example body, a schema property — still has none (Roles.kt).
	var contract map[string]json.RawMessage
	switch found, err := readJSONFile(filepath.Join(dir, filepath.FromSlash(SpecPath(name))), &contract); {
	case !found:
		problems = append(problems, "no openapi.json — "+contractCure(name, project))
	case err != nil:
		problems = append(problems, fmt.Sprintf("openapi.json cannot be read (%v) — %s", err, contractCure(name, project)))
	case contract["x-palbase-roles"] == nil:
		problems = append(problems, "openapi.json carries no x-palbase-roles, which the Gradle plugin refuses — "+
			"upgrade @palbase/backend, then "+contractCure(name, project))
	}
	return problems
}

// readJSONFile decodes the file at path into v; found is false when there is
// no such file.
func readJSONFile(path string, v any) (found bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return true, json.Unmarshal(raw, v)
}
```
  `cmd/palbase/doctor.go`: `doctorCmd`'nin `RunE` gövdesinde `toolchainProbes` döngüsünden sonra, `return nil`'in hemen önüne ekle:
```go

			// LAST, UNDER ITS OWN HEADING: what a Gradle build of this checkout
			// reads, which none of the lines above looks at (FR-019).
			if wd, err := os.Getwd(); err == nil {
				if file := backend.AndroidCheckout(wd); file != "" {
					_, notAProject := backend.ReadLinkedProject()
					fmt.Fprintf(out, "android (%s)\n", file)
					for _, l := range backend.AndroidDoctor(wd, notAProject == nil) {
						if l.OK {
							ok(l.Label, l.Detail)
						} else {
							bad(l.Label, l.Detail)
						}
					}
				}
			}
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./cmd/palbase/ -count=1 -run 'TestDoctor' -v` · Beklenen: yeni beş test ve mevcut beş doctor testi `--- PASS`, ikiz testi APFS'te `--- SKIP`, `ok  	github.com/palgroup/palbase-cli/cmd/palbase	0.940s`. Büyük/küçük harf ayıran diskte (Adım 2'deki `TMPDIR`) Beklenen: `--- PASS: TestDoctorNamesEnvironmentDirectoriesThatDifferOnlyInCase`, `ok  	github.com/palgroup/palbase-cli/cmd/palbase	0.494s` (HEAD'de ölçüldü). Ayrılan `contractCure` ve `androidApplicationFile` için koruma: `go test ./internal/backend/ -count=1 -run 'TestALinkSaysWhichEnvironmentsGotAContract|TestAnEnvironmentThatGaveNoContractThisTimeKeepsTheEarlierOne|TestTheMissingContractLineNamesWhatEndsIt|TestAReactNativeRootIsAnAndroidCheckout|TestALinkAtAReactNativeRootWritesTheAndroidConfig' -v` · Beklenen: beş `--- PASS`, `ok  	github.com/palgroup/palbase-cli/internal/backend	0.610s`.
- [ ] **Adım 5: Commit** — `git add cmd/palbase/doctor.go cmd/palbase/doctor_android_section_test.go internal/backend/android_doctor.go internal/backend/app_environments.go internal/backend/planes.go && git commit -m "feat(doctor): Android checkout'unda Android bölümü — her ortam dizininin iki dosyası, api_key ve x-palbase-roles'ü denetlenir, eksik olan neyin biteceğiyle adlanır (FR-019)" -- cmd/palbase/doctor.go cmd/palbase/doctor_android_section_test.go internal/backend/android_doctor.go internal/backend/app_environments.go internal/backend/planes.go`

---

### Dalga 1 kapısı — CLI sürümü buradan kesilir (T001–T018, T020–T022)
Ölçüldü, scratch `ed1b247` (T022'nin commit'i):
- Run: `gofmt -l .` · Beklenen: çıktı yok. Run: `go vet ./...`, `go vet -tags e2e ./tests/e2e/` · Beklenen: exit 0. Run: `GOTOOLCHAIN=go1.26.6 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...` · Beklenen: `0 issues.`
- Run: `go test -count=1 ./...` · Beklenen: 24 paket `ok`; `internal/backend` yalnız B16 ile FAIL (`TestTheBundlerDoesNotGetToNameThePublicAPI`, `TestTwoClassesOneNameSurviveTheRealModuleWalk`; `FAIL	github.com/palgroup/palbase-cli/internal/backend	112.014s`).
- Run: `go test -race -count=1 ./cmd/palbase/ ./internal/envname/` · Beklenen: `ok  	github.com/palgroup/palbase-cli/cmd/palbase	10.596s`, `ok  	github.com/palgroup/palbase-cli/internal/envname	1.375s`.
- **Sürüm kapısı:** ad güvenliği açığını kapatan CLI sürümü (tag) bu commit'ten kesilir. T019 ve T023–T026 plugin 2.4.0 (`io.palbase.codegen` + `io.palbase:palbe`) `https://palgroup.github.io/palbackend-android/`'da yayımlanmadan `main`'e commit'lenmez — tek dal olduğu için, arada kesilecek bir yama sürümü onları da taşırdı.

---

## Dalga 2 — plugin 2.4.0 yayımlandıktan sonra (T019, T023–T026)

Bu dört görev 2.4'ün modelini anlatır: build type başına `palbase.env.<x>` anahtarları, release'in tahmin etmeyip reddetmesi, loopback release reddi, `2.4.0` koordinatları. 2.3 yalnız global `palbase.env`'i okur (`palbackend-android-src` `v2.3.0`, `codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt`: `project.providers.gradleProperty(ENVIRONMENT_PROPERTY).orElse(DEFAULT_ENVIRONMENT)`, `DEFAULT_ENVIRONMENT = "local"`). Kod ve testler Dalga 1'e bağlı değil; yalnız yayın zamanı farklı.

> **Dalga notu (D-026, D-025).** T019 numarasını korur ama Dalga 2'de, T022'den sonra commit'lenir: reddi, plugin 2.4'ün arama sırasına (FR-205) dayanır; yayındaki 2.3 blok yoksa yalnız modülün `palbase/environments`'ini okur (`v2.3.0` `PalbaseCodegenPlugin.kt:15`), yani Dalga 1'de bloksuz bir 2.3 app'inin tek çalışan link yerini reddederdi. Çakışmasız sıra ölçüldü: T018, T020–T022, ardından T019 ve T023–T026; son ağaç aynı. T020–T022'nin hiçbiri T019'a bağlı değil.

### T019: Bağlı bir checkout'un altındaki Gradle dizininde link reddedilir ve kök adlandırılır — kendi Gradle kökü olan monorepo app'i ve Gradle'sız dizinler hariç
<!-- deps: [T009, T017] | files: [internal/backend/link_nested_test.go, internal/backend/link_artifacts.go, internal/backend/project_link.go] | satisfies: [FR-016] -->

Link nerede koşarsa oraya yazıyor (`link_artifacts.go:141` `root, err := os.Getwd()`, yukarı yürüme yok). Bir Android modülü `build.gradle.kts` taşıdığı için algılama `app/`'ı da app sayıyor (`detectAndroidApplicationID`'nin adaylarından biri `<kök>/build.gradle.kts`), ve `app/` içinde koşan link `app/palbase/` yazıyor. Plugin o modül kopyasını checkout'unkinden önce okuyor: debug APK'sı `base_url = https://STALE-module-copy…` taşıdı, build yeşil (`reports/verification-2026-09-25.md` D3a). Bu görev, cwd bir **Gradle dizini**yse yukarı yürür; bir üst dizinde `palbase/project.json` varsa reddeder, kökü ve `palbase/project.json`'u adlandırır, `--platform` ipucunu verir. Ret tek fonksiyondadır (`refuseInsideALinkedCheckout`) ve iki yerde sorulur: `palbase link`'in `RunE`'sinde hedef çözülmeden (`resolveLinkTarget`) önce, ve `runLink`'in başında — eski sürümlerin kalıntı toplamasından sonra, eski düzen kapısından ve `runLink`'in her ağ çağrısından önce. Auth yenilemesi (`RefreshLinkedClients`) `runLink`'ten geçer.

**Yalnız Gradle dizini sorulur (D-025).** Gradle dizini: `build.gradle(.kts)` ya da `settings.gradle(.kts)` taşıyan dizin (bir kök ya da modül). FR-016'nın amacı D3a: `palbase/`'u yazıldığı dizinin **üstünden** okuyan tek şey Gradle plugin'i (modül → rootDir → rootDir/.., FR-205); ikinci bir kopya yalnız orada checkout'unkinden önce bulunur. Gradle'sız bir dizin — monorepo'nun web app'i `apps/web` (`package.json`), iOS app'i `apps/ios` (`*.xcodeproj`), bir `docs/` — nerede koşarsa oraya link'lenir, `20e5d7e`'de olduğu gibi. Taslak kural onları da reddediyordu; bu, bugün çalışan monorepo web/iOS link'lerinin gerilemesi olurdu (ölçüldü: önceki T019'un `link_artifacts.go`'su, `2762a26`, bu görevin testleriyle `/web`, `/ios` ve `docs` üçünde `… is inside the checkout linked at …` ile düşer).

**Yürüyüş, kendi `.git`'i olan bir Gradle kökünde biter** (dizin ya da worktree/submodule'ün `.git` dosyası; cwd'ninki dahil). Plugin kök projenin üstüne yalnız kök proje ne `.git` ne `palbase/project.json` taşırken bakar (FR-205; plugin `v2.4.0` `AndroidVariantIntegration.kt:310` `CheckoutMarker`, `GeneratePalbaseTask.kt:164` `takeUnless { rootProjectIsCheckout.get() }`): böyle bir kök kendi checkout'udur. **Bir modülün `.git`'i yürüyüşü bitirmez.** Plugin modülün kopyasını Gradle kökününkiyle birlikte okur (`AndroidVariantIntegration.kt:67` `listOf(project.layout.projectDirectory, rootDirectory)`), aradaki `.git`'e bakmadan; yürüyüş modülden Gradle köküne kadar `.git`'i saymaz. Önceki metin `.git`'i her seviyede sayıyordu. Ölçüldü: bağlı bir Android kökünde submodule olan `app/` modülünde (`app/.git` dosyası) link geçti ve `app/palbase/environments/main/android-config.json` yazdı; 2.4 bu iki kopyayla build'i reddeder, 2.3 blok yoksa modülünkini okur.

**Yürüyüş bir Gradle kökünde de biter — üst dizini bağlı değilse.** FR-016'nın lafzî okuması sıradan bir monorepo'yu kırıyordu: kök backend'e bağlı (`.git` + `palbase/project.json`), Android'in Gradle kökü `apps/android` (`settings.gradle.kts`, kendi `.git`'i yok). Orada ilk link de, yeniden link de, auth yenilemesi de reddediliyordu; ret metninin önerdiği "kökte `--platform` ile link" ise `palbase/`'u plugin'in arama yolunun göremeyeceği iki üst dizine yazardı (eleştirmen ölçtü: `…/apps/android is inside the checkout linked at …`). Kural: `settings.gradle(.kts)` taşıyan bir dizin kendi build'inin köküdür ve yürüyüşü bitirir — AMA önce üst dizinine bakılır: bağlı dizin tam bir üstündeyse (React Native / Flutter `android/`'ı, T017) o build o `palbase/`'u zaten okur (rootDir/..) ve ikinci bir kopya ondan önce bulunurdu, bu yüzden ret sürer. D3a'nın `app/` modülünde `settings.gradle` yoktur; ret sürer. Bağlanmış monorepo app'inin kendi `app/` modülü de reddedilir ve ret `apps/android`'i (en yakın bağlı dizini) adlandırır, repo kökünü değil.

**Yürüyüş dizinin diskteki yerinden yapılır** (`filepath.EvalSymlinks`; `machine_state.go` aynı nedenle kullanır). `os.Getwd` kabuğun `PWD`'sini döndürür; bir symlink üzerinden girilmiş `android/`'ın mantıksal üst dizini, Gradle'ın build dizinlerini çözüp baktığı üst dizin değildir. Ölçüldü, gerçek ikiliyle: RN kökü bağlı, `ln -s …/rn/android …/rn-android-shortcut`, `cd rn-android-shortcut && palbase link`. Önceki T019'da kapı açılmadı, link içeri yürüdü: `link failed; previous client artifacts were preserved: --url is required: the address the stack serves on`. Bu görevle: `…/rn-android-shortcut is inside the checkout linked at …/rn (palbase/project.json) — …`.

**Burada duran eski bir kopya adlandırılır.** Eski CLI nerede koşarsa oraya yazıyordu, ve bir RN checkout'unda Android'i yalnız `android/`'da buluyordu (FR-015; N2: "RN android/ -> com.rn.app"). Bu kuralın doğmasını engellediği ikinci kopya zaten orada olabilir: `android/palbase/project.json` + `android/palbase/environments/…`. Plugin 2.4 böyle bir kök projeyi checkout sayar ve üstüne bakmaz (`CheckoutMarker`). Önceki ret "A palbase/ written here would be a second copy" diyordu. Önerisine uyulunca kök yazıldı, `android/palbase` yerinde kaldı ve 2.4 build'i onu okumaya devam ederdi, sessizce. Ölçüldü (önceki T019): `android/`'da ret, ardından kökte link `err=<nil>`, ardından diskte ikisi de var — `android/palbase/environments/main/android-config.json` ve `palbase/environments/main/android-config.json`. Kökteki link'in `android/palbase`'i görmesi bu görevin kapsamında değil; o yön için ayrı bir uyarı (doctor ya da kökteki link) lead'e önerildi. Dizin bir `palbase/` taşıyorsa retin ikinci satırı onu adlandırır ve silinmesini söyler; ret kopyaya dokunmaz.

**Ret hedef çözülmeden gelir.** Hedefsiz `palbase link`, bulunduğu dizinin kaydını (`palbase/project.json`, yani eski kopyayı) okur ve o projenin ortamlarını listeler; bir adla projeleri listeler. Kapı yalnız `runLink`'teyken ölçüldü: gerçek ikiliyle `cd rn-legacy/android && palbase link` → `not authenticated — run `palbase login` (or, for headless use, export PALBASE_ACCESS_TOKEN)`. Testte ortam listesi bir kez çağrıldı; ad verilince `"todoapp" is not an address, and this CLI has no cloud session to resolve it as a project — …` döndü. Kapı artık `RunE`'de de sorulur: bu görevin ikilisiyle aynı dizinde `palbase link` de `palbase link todoapp` da oturumsuz `…/rn-legacy/android is inside the checkout linked at …/rn-legacy (palbase/project.json) — …` döner, ikinci satırı eski kopyayı adlandırır, diskte yeni dosya yok. `runLink`'teki kopyası auth yenilemesi içindir: o yol kaydın projesini retten önce listeler (ağ okuması; yazma yok).

**`20e5d7e` davranışının kanıtı (ölçüldü, plan adımı değil).** `TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore` ve `TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore` yalnız `20e5d7e`'de de bulunan yardımcıları kullanır (`inScratchCheckout`, `writeFile`, `stackServing`, `routeEnvironments`, `linkKeyMain`, `seedWebCheckout`, `installStubCodegen`, `useStub`, `stubSwiftgen`); `productLink`'e (T009) dayanmaz. İkisi, iki yardımcılarıyla (`linkedMonorepo`, `assertRootUntouched`) birlikte bu görevin commit'inden betikle çıkarılıp `20e5d7e`'deki tek kullanımlık bir klonda koşuldu: `--- PASS: TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore` (`/web`, `/ios`), `--- PASS: TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore`, `ok  	github.com/palgroup/palbase-cli/internal/backend	1.052s`. Web, iOS ve `docs/` için link + yeniden link'in tam çıktısı ve monorepo'daki dosya listesi (port ve geçici yol normalleştirilerek) T018 (`e6f8ac6`) ile bu görev arasında **birebir aynı**: 106 satır, `diff` boş, altı koşunun altısı `err=<nil>`.

**Karar (lead, D-026): T019 Dalga 2'de — plugin 2.3 ve dalga.** Bu kuralın gerekçesi FR-205'in arama sırası: modül → kök → kökün bir üstü, birden çok adayda ret. Bu, plugin 2.4'ün modeli. Yayındaki 2.3 tek yer okur: `environmentsDir`, varsayılanı modülün kendisi (`v2.3.0` `PalbaseCodegenPlugin.kt:15` `environmentsDir.convention(project.layout.projectDirectory.dir("palbase/environments"))`; README:130 "Point the plugin somewhere else when `palbase/` lives beside the app module rather than inside it"). 2.3'te blok yoksa build'in okuduğu tek kopya modülünkidir. Ret onu yenilemeyi engeller, önerisi (kökte link) ise 2.3'ün okumadığı bir kopya yazar. Blok varsa modül kopyası zaten okunmaz. Yani 2.3 altında ret hiçbir yanlış build'i önlemez; bloksuz bir app'in tek çalışan link yerini kapatır. Ölçüldü, iki düzende: kökü bağlı bir Android deposunda `app/`, ve backend'i kökte bağlı bir fullstack deposunda `android/app/`. `20e5d7e`'de ikisinde de link geçer ve modülün `palbase/environments/main/android-config.json`'unu yazar (`err=<nil>`, `module config written: true`). Bu görevle ikisi de `… is inside the checkout linked at …` ile reddedilir. D-026 bu görevi Dalga 1'e koyuyor, yani 2.4'ten önce çıkar. Öneri: T019'u Dalga 2'ye almak. Hiçbir görev ona bağlı değil. Ölçüldü: T020–T022 T018'in üstüne, T019 ve T023–T026 onların üstüne çakışmasız uygulanıyor ve son ağaç aynı. Karar gelene kadar görev yerinde durur.

**Interfaces:**
- Consumes: `projectPath()`, `RootDir()`, `EnvDir`, `ConfigPath`, `webPlatform`, `isRegularFile`, `EnvironmentsOf`, `newLinkCmd`, `Resolvers`; test yardımcıları `productLink(t, name) linkOpts` (T009, `link_local_stack_test.go`), `seedGradleFile(t, name, body string)` (T017, `link_rn_root_test.go`), `inScratchCheckout`, `writeFile`, `stackServing`, `seedWebCheckout` (`project_link_test.go`), `routeEnvironments` (`gather_environments_test.go`), `linkKeyMain` (`link_all_environments_test.go`), `installStubCodegen` (`web_wiring_test.go`), `useStub`, `stubSwiftgen` (`native_codegen_test.go`).
- Produces:
  - `func linkedCheckoutAbove(dir string) string` (`link_artifacts.go`) — `dir` bir Gradle dizini değilse `""`; öyleyse `dir`'in diskteki yerinden (`EvalSymlinks`) ÜSTÜNDEKİ en yakın `palbase/project.json` sahibi dizin, yoksa `""`; `.git` taşıyan bir Gradle kökünde ve üst dizini bağlı olmayan bir Gradle kökünde (`settings.gradle(.kts)`) durur, bir modülün ya da aradaki bir dizinin `.git`'inde durmaz.
  - `func refuseInsideALinkedCheckout(dir string) error` (`link_artifacts.go`) — FR-016'nın reddi ya da `nil`; `newLinkCmd`'in `RunE`'si `resolveLinkTarget`'tan önce, `runLink` `reapRetiredArtifacts`'tan sonra çağırır.
  - `var gradleRootFiles`, `var gradleDirectoryFiles` ve `func holdsOneOf(dir string, names []string) bool` (`link_artifacts.go`).
  - Ret metni, ilk satır: `<cwd> is inside the checkout linked at <kök> (palbase/project.json) — run `palbase link` there; --platform names this app's platform if it is not found from there.` İkinci satır, dizinde `palbase/` yoksa: `  A palbase/ written here would be a second copy, and a build in this directory would find it before the checkout's own`; varsa: `  The palbase/ here, from an earlier link, is a second copy, which a build in this directory reads before or instead of the checkout's own — delete it and commit that deletion`.
  - Test yardımcıları `linkedMonorepo(t) (root string, project []byte)` ve `assertRootUntouched(t, root, project)` (`link_nested_test.go`).

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_nested_test.go`:
```go
package backend

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linkedMonorepo makes the current directory a repository whose root is linked
// to the backend — `.git` and palbase/project.json, as `palbase link` in the
// backend leaves it — and returns its absolute path and the committed file.
func linkedMonorepo(t *testing.T) (root string, project []byte) {
	t.Helper()
	require.NoError(t, os.Mkdir(".git", 0o755))
	require.NoError(t, os.MkdirAll(RootDir(), 0o755))
	writeFile(t, projectPath(), `{"project":"prd_backend","name":"todo-backend"}`)
	root, err := os.Getwd()
	require.NoError(t, err)
	project, err = os.ReadFile(projectPath())
	require.NoError(t, err)
	return root, project
}

// assertRootUntouched says the linked root kept its own project.json byte for
// byte and gained no environments: a link below it wrote only where it ran.
func assertRootUntouched(t *testing.T, root string, project []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(projectPath())))
	require.NoError(t, err)
	assert.Equal(t, string(project), string(got), "the linked root's project.json changed")
	assert.NoDirExists(t, filepath.Join(root, RootDir(), "environments"), "the link wrote into the linked root")
}

// A LINK IN A GRADLE DIRECTORY BELOW A LINKED CHECKOUT IS REFUSED, AND THE ROOT
// IS NAMED (FR-016).
//
// link wrote wherever it ran (link_artifacts.go `root, err := os.Getwd()`), and
// an Android module is a directory with a build.gradle.kts — detection found an
// app there. `palbase link` inside app/ wrote app/palbase/, and the Gradle
// plugin read that module copy before the checkout's own: measured, the debug
// APK carried `base_url = https://STALE-module-copy…` and the build was green
// (verification D3a).
func TestALinkBelowALinkedCheckoutIsRefusedAndNamesItsRoot(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.Mkdir(".git", 0o755))
	o := productLink(t, "todoapp")
	require.NoError(t, runLink(context.Background(), o, io.Discard))
	require.FileExists(t, projectPath())
	root, err := os.Getwd()
	require.NoError(t, err)

	require.NoError(t, os.Chdir("app"))
	var out strings.Builder
	err = runLink(context.Background(), o, &out)

	assert.NoDirExists(t, RootDir(), "a second palbase/ was written inside the module")
	require.Error(t, err)
	assert.Equal(t, filepath.Join(root, "app")+" is inside the checkout linked at "+root+
		" (palbase/project.json) — run `palbase link` there; --platform names this app's platform if it is not found from there.\n"+
		"  A palbase/ written here would be a second copy, and a build in this directory would find it before the checkout's own",
		err.Error())
	assert.Empty(t, out.String())
}

// SO IS A GRADLE ROOT RIGHT BELOW THE LINKED CHECKOUT. React Native and Flutter
// keep the Gradle build in android/, beside ios/ (FR-015), and the plugin looks
// one level above its Gradle root (FR-205): the checkout's own palbase/ is the
// one that build reads, and a copy in android/ would be found before it.
func TestAReactNativeAndroidDirectoryIsRefusedBelowItsLinkedRoot(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	require.NoError(t, os.Mkdir("android", 0o755))
	writeFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	require.NoError(t, os.Chdir("android"))
	o := productLink(t, "todoapp")

	err := runLink(context.Background(), o, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(root, "android")+" is inside the checkout linked at "+root+" (palbase/project.json)")
	assert.NoDirExists(t, RootDir(), "a second palbase/ was written in the Gradle root")
	assertRootUntouched(t, root, project)
}

// A GRADLE ROOT FURTHER DOWN IS A CHECKOUT OF ITS OWN, even inside a repository
// whose root is linked to the backend: the plugin searches apps/android/palbase
// and one level up, never the repository root two levels away, so refusing
// here left the app nowhere a build could find its environments — on the first
// link and on every one after it. Its own module is still a module: app/ below
// it is refused, and the checkout named is the app's.
func TestAnAppThatIsItsOwnGradleRootLinksInsideALinkedMonorepo(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	app := filepath.Join(root, "apps", "android")
	require.NoError(t, os.MkdirAll(app, 0o755))
	require.NoError(t, os.Chdir(app))
	writeFile(t, "settings.gradle.kts", "include(\":app\")\n")
	o := productLink(t, "todoapp")

	for _, run := range []string{"link", "re-link"} {
		var out strings.Builder
		require.NoError(t, runLink(context.Background(), o, &out), "%s:\n%s", run, out.String())
		assert.FileExists(t, ConfigPath("main", "android"), run)
		assert.FileExists(t, projectPath(), run)
	}
	assertRootUntouched(t, root, project)

	require.NoError(t, os.Chdir("app"))
	err := runLink(context.Background(), o, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(app, "app")+" is inside the checkout linked at "+app+" (palbase/project.json)")
	assert.NoDirExists(t, RootDir(), "a second palbase/ was written inside the module")
}

// A GRADLE ROOT WITH A `.git` OF ITS OWN IS A CHECKOUT OF ITS OWN, as the
// plugin's search above a root project stops there (FR-205): a clone kept in
// another project's directory is linked where it is — here a Gradle root right
// below the linked directory, which without its `.git` is refused.
func TestARepositoryInsideALinkedDirectoryLinksOnItsOwn(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join("sample", ".git"), 0o755))
	require.NoError(t, os.Chdir("sample"))
	writeFile(t, "settings.gradle.kts", "include(\":app\")\n")
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.FileExists(t, ConfigPath("main", "android"))
	assertRootUntouched(t, root, project)
}

// A DIRECTORY WITH NO GRADLE BUILD IS LINKED WHERE IT IS, AS BEFORE (D-025).
// FR-016 is about the Gradle plugin, which reads palbase/ from above the
// directory it builds; a monorepo's web app (package.json) and iOS app (an
// Xcode project) have no build that does, and a link in each of them worked
// before the refusal existed (20e5d7e). Refusing them would take away a working
// link to prevent a copy nothing reads first. Both runs are the same as there.
func TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore(t *testing.T) {
	for _, c := range []struct {
		platform string
		dir      string
		seed     func(t *testing.T)
	}{
		{webPlatform, "apps/web", func(t *testing.T) { seedWebCheckout(t); installStubCodegen(t, "export {}") }},
		{"ios", "apps/ios", func(t *testing.T) {
			require.NoError(t, os.Mkdir("App.xcodeproj", 0o755))
			writeFile(t, filepath.Join("App.xcodeproj", "project.pbxproj"), "SDKROOT = iphoneos;\nPRODUCT_BUNDLE_IDENTIFIER = com.example.app;\n")
			useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
		}},
	} {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			root, project := linkedMonorepo(t)
			require.NoError(t, os.MkdirAll(filepath.FromSlash(c.dir), 0o755))
			require.NoError(t, os.Chdir(filepath.FromSlash(c.dir)))
			c.seed(t)
			main := stackServing(t, linkKeyMain, nil)
			routeEnvironments(t, map[string]string{"mainref000": main.URL})
			o := linkOpts{
				url:          main.URL,
				linkedEnv:    "main",
				product:      Product{ID: "prd_a", Name: "todoapp"},
				environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
			}

			for _, run := range []string{"link", "re-link"} {
				var out strings.Builder
				require.NoError(t, runLink(context.Background(), o, &out), "%s:\n%s", run, out.String())
				assert.True(t, strings.HasPrefix(out.String(), "▸ "+c.platform+"\n"), "%s:\n%s", run, out.String())
				assert.FileExists(t, ConfigPath("main", c.platform), run)
				assert.FileExists(t, projectPath(), run)
			}
			assertRootUntouched(t, root, project)
		})
	}
}

// AND SO IS ONE WITH NO APP AT ALL: docs/ below the linked root binds the
// backend only, as it did before the refusal existed (20e5d7e).
func TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	require.NoError(t, os.Mkdir("docs", 0o755))
	require.NoError(t, os.Chdir("docs"))
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.True(t, strings.HasPrefix(out.String(), "▸ no client app here"), out.String())
	assert.FileExists(t, projectPath())
	assert.NoDirExists(t, EnvDir("main"))
	assertRootUntouched(t, root, project)
}

// A MODULE IS A MODULE EVEN WHEN IT IS A REPOSITORY OF ITS OWN. The plugin
// reads a module's palbase/ together with its Gradle root's whatever `.git`
// sits between them — the `.git` bound is the root project's alone (FR-205) —
// so an app module kept as a submodule inside a linked Android checkout would
// hold the second copy all the same.
func TestAModuleWithAGitOfItsOwnIsStillAModule(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	writeFile(t, "settings.gradle.kts", "include(\":app\")\n")
	o := productLink(t, "todoapp")
	writeFile(t, filepath.Join("app", ".git"), "gitdir: ../.git/modules/app\n")
	require.NoError(t, os.Chdir("app"))

	err := runLink(context.Background(), o, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(root, "app")+" is inside the checkout linked at "+root+" (palbase/project.json)")
	assert.NoDirExists(t, RootDir(), "a second palbase/ was written inside the module")
	assertRootUntouched(t, root, project)
}

// THE WALK IS WHERE THE DIRECTORY IS ON DISK. A shell that reached android/
// through a symlink names it by the link, and the link's parent is not the
// directory the plugin looks in: Gradle resolves a build's directories, and
// the React Native root above android/ is where that build reads palbase/.
func TestAGradleDirectoryReachedThroughASymlinkIsWalkedWhereItIs(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	seedGradleFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	shortcut := filepath.Join(t.TempDir(), "android")
	require.NoError(t, os.Symlink(filepath.Join(root, "android"), shortcut))
	require.NoError(t, os.Chdir(shortcut))
	t.Setenv("PWD", shortcut) // a shell's `cd` keeps the name it was given
	o := productLink(t, "todoapp")

	err := runLink(context.Background(), o, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), " is inside the checkout linked at "+root+" (palbase/project.json)")
	assert.NoDirExists(t, filepath.Join(root, "android", RootDir()), "a second palbase/ was written through the symlink")
	assertRootUntouched(t, root, project)
}

// A COPY AN EARLIER LINK LEFT HERE IS NAMED, NOT PROMISED AWAY. An older CLI
// linked wherever it ran, and in a React Native checkout android/ was the only
// place it found Android (FR-015): the copy this rule keeps from being born may
// be here already. Its palbase/project.json makes android/ a checkout of its
// own to the plugin, which then never looks above it (FR-205) — linking at the
// root, as the refusal says, would leave the build on the old copy, in silence,
// unless the refusal says to delete it.
func TestARefusalNamesTheCopyAnEarlierLinkLeftHere(t *testing.T) {
	inScratchCheckout(t)
	root, project := linkedMonorepo(t)
	seedGradleFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	old := filepath.Join(root, "android", "palbase", "environments", "main", "android-config.json")
	seedGradleFile(t, filepath.Join("android", "palbase", "project.json"), `{"project":"prd_a","name":"todoapp"}`)
	seedGradleFile(t, old, `{"base_url":"https://stale.example"}`)
	require.NoError(t, os.Chdir("android"))
	o := productLink(t, "todoapp")

	err := runLink(context.Background(), o, io.Discard)

	require.Error(t, err)
	assert.Equal(t, filepath.Join(root, "android")+" is inside the checkout linked at "+root+
		" (palbase/project.json) — run `palbase link` there; --platform names this app's platform if it is not found from there.\n"+
		"  The palbase/ here, from an earlier link, is a second copy, which a build in this directory reads before or instead of the checkout's own — "+
		"delete it and commit that deletion",
		err.Error())
	got, readErr := os.ReadFile(old)
	require.NoError(t, readErr)
	assert.Equal(t, `{"base_url":"https://stale.example"}`, string(got), "the refusal touched the old copy")
	assertRootUntouched(t, root, project)
}

// THE REFUSAL COMES BEFORE THE TARGET IS RESOLVED. With no target, `palbase
// link` reads the record in the directory it runs in — the copy an older link
// left there — and lists that project's environments; with a project's name
// it lists the projects. Asked in that order, a person with no session was
// sent to `palbase login` first, and one with a session had the cloud asked,
// before either was told this is the wrong directory.
func TestTheRefusalComesBeforeTheTargetIsResolved(t *testing.T) {
	inScratchCheckout(t)
	root, _ := linkedMonorepo(t)
	seedGradleFile(t, filepath.Join("android", "settings.gradle"), "include ':app'\n")
	seedGradleFile(t, filepath.Join("android", "palbase", "project.json"), `{"project":"prd_a","name":"todoapp"}`)
	require.NoError(t, os.Chdir("android"))
	listed := 0
	prev := EnvironmentsOf
	EnvironmentsOf = func(context.Context, string) ([]Environment, error) {
		listed++
		return nil, errors.New("the project's environments were listed")
	}
	t.Cleanup(func() { EnvironmentsOf = prev })

	for _, args := range [][]string{{}, {"todoapp"}} {
		cmd := newLinkCmd(Resolvers{})
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true

		err := cmd.Execute()

		require.Error(t, err, "%q", args)
		assert.True(t, strings.HasPrefix(err.Error(), filepath.Join(root, "android")+" is inside the checkout linked at "+root+" "), "%q: %v", args, err)
	}
	assert.Zero(t, listed, "the cloud was asked before the refusal")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestALinkBelowALinkedCheckoutIsRefusedAndNamesItsRoot|TestAReactNativeAndroidDirectoryIsRefusedBelowItsLinkedRoot|TestAnAppThatIsItsOwnGradleRootLinksInsideALinkedMonorepo|TestARepositoryInsideALinkedDirectoryLinksOnItsOwn|TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore|TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore|TestAModuleWithAGitOfItsOwnIsStillAModule|TestAGradleDirectoryReachedThroughASymlinkIsWalkedWhereItIs|TestARefusalNamesTheCopyAnEarlierLinkLeftHere|TestTheRefusalComesBeforeTheTargetIsResolved' -count=1 -v` · Beklenen: **FAIL** — `link_nested_test.go:63: Error: directory "palbase" exists` / `Messages: a second palbase/ was written inside the module` ve `link_nested_test.go:64: Error: An error is expected but got nil.` (`--- FAIL: TestALinkBelowALinkedCheckoutIsRefusedAndNamesItsRoot`); `link_nested_test.go:86: Error: An error is expected but got nil.` (`--- FAIL: TestAReactNativeAndroidDirectoryIsRefusedBelowItsLinkedRoot`); `link_nested_test.go:117: Error: An error is expected but got nil.` (`--- FAIL: TestAnAppThatIsItsOwnGradleRootLinksInsideALinkedMonorepo` — link ve yeniden link bugün de geçer; düşen, bağlanmış app'in `app/` modülünün reddi); `link_nested_test.go:227`, `:249` ve `:274`: `Error: An error is expected but got nil.` (`--- FAIL: TestAModuleWithAGitOfItsOwnIsStillAModule`, `--- FAIL: TestAGradleDirectoryReachedThroughASymlinkIsWalkedWhereItIs`, `--- FAIL: TestARefusalNamesTheCopyAnEarlierLinkLeftHere`); `link_nested_test.go:317: Error: Should be true` / `Messages: []: the project's environments were listed` ve `Messages: ["todoapp"]: "todoapp" is not an address, and this CLI has no cloud session to resolve it as a project — `palbase login`, or pass the stack's URL`, ardından `link_nested_test.go:319: Error: Should be zero, but was 1` / `Messages: the cloud was asked before the refusal` (`--- FAIL: TestTheRefusalComesBeforeTheTargetIsResolved`); `FAIL	github.com/palgroup/palbase-cli/internal/backend`. `--- PASS: TestARepositoryInsideALinkedDirectoryLinksOnItsOwn`, `--- PASS: TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore` (`/web`, `/ios`) ve `--- PASS: TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore` bugün de geçer — `.git` sınırının ve `20e5d7e` davranışının bekçileri.
- [ ] **Adım 3: Uygula** —
  (a) `internal/backend/link_artifacts.go` içinde `func runLink(ctx context.Context, o linkOpts, w io.Writer) error {` satırının hemen üstüne ekle:
```go
// gradleRootFiles mark the root of a Gradle build; gradleDirectoryFiles mark
// any directory Gradle builds, a root or one of its modules.
var (
	gradleRootFiles      = []string{"settings.gradle.kts", "settings.gradle"}
	gradleDirectoryFiles = []string{"build.gradle.kts", "build.gradle", "settings.gradle.kts", "settings.gradle"}
)

// holdsOneOf reports whether dir holds a regular file by one of names.
func holdsOneOf(dir string, names []string) bool {
	for _, name := range names {
		if isRegularFile(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

// linkedCheckoutAbove is, for a Gradle directory, the nearest directory ABOVE
// it that holds palbase/project.json, or "".
//
// ONLY A GRADLE DIRECTORY IS ASKED (D-025) — one holding build.gradle(.kts) or
// settings.gradle(.kts). The Gradle plugin is what reads palbase/ from above
// the directory it builds (the module, the Gradle root and one level up,
// FR-205), so only there is a second copy found before the checkout's own. A
// directory with no Gradle build — a monorepo's web or iOS app, a docs/ folder
// — is linked where it is, as it was before this rule (20e5d7e).
//
// THE WALK IS WHERE THE DIRECTORY IS ON DISK. A shell that reached it through a
// symlink names it by the link, and the link's parent is not the directory the
// plugin reads: Gradle resolves a build's directories.
//
// A MODULE IS WALKED UP TO ITS GRADLE ROOT, whatever repository it sits in: the
// plugin reads a module's palbase/ together with its Gradle root's, and a `.git`
// between them — an app module kept as a submodule — bounds neither.
//
// THE WALK ENDS AT A GRADLE ROOT THAT IS A REPOSITORY OF ITS OWN — it carries
// `.git`, the directory or the file a worktree or submodule points with. The
// plugin looks above a root project only while it carries no `.git` (FR-205),
// so such a root is a checkout of its own, even inside another project's
// directory.
//
// AND AT A GRADLE ROOT WHOSE PARENT IS NOT LINKED. A linked directory right
// above a Gradle root — React Native's and Flutter's android/ (FR-015) — is the
// checkout that build reads, and a second copy below it would be found first;
// one further up — a monorepo whose root is linked to the backend, its app in
// apps/android — is out of that build's reach, and the app is linked where its
// Gradle root is.
func linkedCheckoutAbove(dir string) string {
	at, err := filepath.Abs(dir)
	if err != nil || !holdsOneOf(at, gradleDirectoryFiles) {
		return ""
	}
	if onDisk, err := filepath.EvalSymlinks(at); err == nil {
		at = onDisk
	}
	for {
		gradleRoot := holdsOneOf(at, gradleRootFiles)
		if gradleRoot {
			if _, err := os.Lstat(filepath.Join(at, ".git")); err == nil {
				return ""
			}
		}
		parent := filepath.Dir(at)
		if parent == at {
			return ""
		}
		if isRegularFile(filepath.Join(parent, filepath.FromSlash(projectPath()))) {
			return parent
		}
		if gradleRoot {
			return ""
		}
		at = parent
	}
}

// refuseInsideALinkedCheckout is FR-016's refusal of a link in dir, or nil
// when dir is not a Gradle directory inside a linked checkout.
//
// A COPY ALREADY HERE IS NAMED. An older CLI linked wherever it ran — in a
// React Native checkout android/ was the only place it found Android (FR-015)
// — so the second copy this rule keeps from being born may be here already. A
// build here reads it before, or instead of, the checkout's own: a Gradle root
// holding palbase/project.json is a checkout of its own to the plugin, which
// then never looks above it (FR-205). Sent to link at the root with that copy
// left in place, the build would go on compiling it, in silence.
func refuseInsideALinkedCheckout(dir string) error {
	linked := linkedCheckoutAbove(dir)
	if linked == "" {
		return nil
	}
	why := fmt.Sprintf("A %s/ written here would be a second copy, and a build in this directory would find it before the checkout's own", RootDir())
	if info, err := os.Stat(filepath.Join(dir, RootDir())); err == nil && info.IsDir() {
		why = fmt.Sprintf("The %s/ here, from an earlier link, is a second copy, which a build in this directory reads before or instead of "+
			"the checkout's own — delete it and commit that deletion", RootDir())
	}
	return fmt.Errorf("%s is inside the checkout linked at %s (%s) — run `palbase link` there; "+
		"--platform names this app's platform if it is not found from there.\n  %s", dir, linked, projectPath(), why)
}

```
  (b) `internal/backend/link_artifacts.go` `runLink` içinde şu bloğu:
```go
	reapRetiredArtifacts(root)
	// THE RETIRED LAYOUT IS REFUSED NEXT, BEFORE ANY SIDE EFFECT OF ITS OWN.
```
  şununla değiştir:
```go
	reapRetiredArtifacts(root)
	// A GRADLE DIRECTORY INSIDE A LINKED CHECKOUT IS NOT ONE (FR-016). An Android
	// module has a build.gradle.kts, so detection finds an app in it, and a link
	// there wrote a second palbase/ that the Gradle plugin read before the
	// checkout's own — measured: a debug APK carried the module copy's stale
	// address, build green (verification D3a).
	if err := refuseInsideALinkedCheckout(root); err != nil {
		return err
	}
	// THE RETIRED LAYOUT IS REFUSED NEXT, BEFORE ANY SIDE EFFECT OF ITS OWN.
```
  (c) `internal/backend/project_link.go` `newLinkCmd`'in `RunE`'si içinde şu bloğu:
```go
			if len(args) == 1 && o.url == "" {
				o.url = args[0]
			}
			if err := resolveLinkTarget(cmd.Context(), r, &o); err != nil {
```
  şununla değiştir:
```go
			if len(args) == 1 && o.url == "" {
				o.url = args[0]
			}
			// FR-016 BEFORE THE TARGET IS RESOLVED. Resolving it reads the
			// record in this directory and asks the cloud about it; a link
			// refused here needs neither, and without a session the refusal
			// hid behind `palbase login`. runLink asks again, for the auth
			// refresh that reaches it without this command.
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := refuseInsideALinkedCheckout(wd); err != nil {
				return err
			}
			if err := resolveLinkTarget(cmd.Context(), r, &o); err != nil {
```
- [ ] **Adım 4: Yeşil** — Run: Adım 2'deki komut · Beklenen: `--- PASS: TestALinkBelowALinkedCheckoutIsRefusedAndNamesItsRoot`, `--- PASS: TestAReactNativeAndroidDirectoryIsRefusedBelowItsLinkedRoot`, `--- PASS: TestAnAppThatIsItsOwnGradleRootLinksInsideALinkedMonorepo`, `--- PASS: TestARepositoryInsideALinkedDirectoryLinksOnItsOwn`, `--- PASS: TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore` (`/web`, `/ios`), `--- PASS: TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore`, `--- PASS: TestAModuleWithAGitOfItsOwnIsStillAModule`, `--- PASS: TestAGradleDirectoryReachedThroughASymlinkIsWalkedWhereItIs`, `--- PASS: TestARefusalNamesTheCopyAnEarlierLinkLeftHere`, `--- PASS: TestTheRefusalComesBeforeTheTargetIsResolved`, `ok  	github.com/palgroup/palbase-cli/internal/backend`. Her kural ayrı ayrı ölçüldü: bir kural kaldırılınca ya da sırası değişince yalnız o kuralın bekçileri düşer, diğer testler geçer.
  - Gradle dizini kapısı olmadan (önceki T019'un yürüyüşü): `TestAnAppWithNoGradleBuildLinksInsideALinkedMonorepoAsBefore` `/web` ve `/ios` ile `TestADirectoryWithNoGradleBuildBelowALinkedCheckoutLinksAsBefore` düşer: `link_nested_test.go:177` `Received unexpected error:` / `…/apps/web is inside the checkout linked at …` (`…/apps/ios`, `…/docs` aynı).
  - Gradle kökü durağı olmadan: `TestAnAppThatIsItsOwnGradleRootLinksInsideALinkedMonorepo` düşer: `link_nested_test.go:109` `Received unexpected error:` / `…/apps/android is inside the checkout linked at …`.
  - Gradle kökü durağı üst dizinin `project.json`'ından önceye alınınca: bağlı dizinin hemen altındaki `android/`'ı soran dört test düşer — `TestAReactNativeAndroidDirectoryIsRefusedBelowItsLinkedRoot` (`:86 An error is expected but got nil.`), `TestAGradleDirectoryReachedThroughASymlinkIsWalkedWhereItIs`, `TestARefusalNamesTheCopyAnEarlierLinkLeftHere`, `TestTheRefusalComesBeforeTheTargetIsResolved`.
  - `.git` durağı olmadan: `TestARepositoryInsideALinkedDirectoryLinksOnItsOwn` düşer: `link_nested_test.go:135` `Received unexpected error:` / `…/sample is inside the checkout linked at …`.
  - `.git` her seviyede sayılınca (önceki kural): `TestAModuleWithAGitOfItsOwnIsStillAModule` düşer: `link_nested_test.go:227` `An error is expected but got nil.`
  - `EvalSymlinks` olmadan: `TestAGradleDirectoryReachedThroughASymlinkIsWalkedWhereItIs` düşer: `link_nested_test.go:249` `An error is expected but got nil.`
  - Eski kopya satırı olmadan: `TestARefusalNamesTheCopyAnEarlierLinkLeftHere` düşer: `link_nested_test.go:275` `Not equal:`, `actual` ikinci satırı `  A palbase/ written here would be a second copy, …`.
  - `RunE`'deki kapı olmadan: `TestTheRefusalComesBeforeTheTargetIsResolved` düşer.

  Ardından `go test ./internal/backend/ ./cmd/... -count=1` · Beklenen: yeni bir kırık yok — `internal/backend` yalnız B16 ile FAIL (bu scratch'te `--- FAIL: TestTheBundlerDoesNotGetToNameThePublicAPI`, `--- FAIL: TestTwoClassesOneNameSurviveTheRealModuleWalk`, `FAIL	github.com/palgroup/palbase-cli/internal/backend	108.270s`), `ok  	github.com/palgroup/palbase-cli/cmd/palbase	8.104s`. Kapı, geçici dizinlerde koşan hiçbir mevcut link testini etkilemez (`TestLinkHelpNamesEveryForm` ve `TestAnAddressWhoseListingCannotBeReadIsLeftAlone` dahil).
- [ ] **Adım 5: Commit** — `git add internal/backend/link_artifacts.go internal/backend/link_nested_test.go internal/backend/project_link.go && git commit -m "fix(link): bağlı bir checkout'un altındaki Gradle dizininde link reddedilir ve kök adlandırılır — modülde ikinci palbase/ doğmaz, orada duran eski kopya adlandırılır, ret hedef çözülmeden gelir; kendi Gradle kökü olan monorepo app'i ve Gradle'sız dizinler (web, iOS, docs) 20e5d7e'deki gibi bağlanır (FR-016, D-025)" -- internal/backend/link_artifacts.go internal/backend/link_nested_test.go internal/backend/project_link.go`

### T023: Android link'i Gradle kurulumunu basar — sürüm tek sabitten, ortam listeden
<!-- deps: [T009, T011, T012, T018] | files: [internal/backend/link_gradle_setup_test.go, internal/backend/android_setup.go, internal/backend/project_link.go] | satisfies: [FR-013] -->

Apple'a `printEnvironmentSelectionSnippet` basılıyor (`project_link.go`, yalnız `if apple` içinde); Android'e hiçbir şey: `io\.palbase`, `palbase\.env`, `io.palbase:palbe` hiçbir `.go` dosyasında geçmiyor (`reports/verification-2026-09-25.md` B5; L1 çıktısının tamamı `▸ android / … / wrote …/android-config.json / linked to … / commit palbase/`). Bu görev, bu koşuda bir `android-config.json` yazıldıysa Apple bloğunun hemen ardından bir Gradle bloğu basar: depo satırı (`settings.gradle.kts`'te iki yerde), proje `plugins {}`'ine serialization + `io.palbase.codegen` sürümüyle, app modülüne ikisi sürümsüz ve `implementation("io.palbase:palbe:<sürüm>")`, `gradle.properties`'e `palbase.env.debug=<envs.Default>` ve `palbase.env.release=<envs.Default>`. **Ortam asla bir literal değil**: `envs.Default`, bu link'in okuduğu ortamın listedeki adı — panelden açılmış bir projede `production` (test bunu sabitler). **Sürüm tek bir sabitte** (`palbaseAndroidVersion`), plugin satırı ve bağımlılık ikisi de onu okur; plugin ve runtime tek sürümle çıkıyor (`publish.sh`, plugin POM'u engine'i aynı sürüme pinliyor — verification E-bölümü). Değer `2.4.0`: basılan `palbase.env.<build type>` anahtarları 2.4'ün; 2.3 yalnız global `palbase.env`'i okur. **`local` release'e eşlenmez**: projesiz, bu makineye yapılmış bir link'in tek ortamı `local`'dir (T011) ve release build'i loopback bir adresi reddeder (plugin FR-206) — o satır yerine nedeni söyleyen bir `#` yorum satırı basılır (yapıştırılsa zararsız).

Ölçüldü (sözdizimi; cli-4 taslakçısının ölçümü, bu revizyonda yeniden koşulmadı): kullanıcının test app'inin bir kopyasına (`AGP 9.1.1`, Gradle 9.3.1) bloğun satırları yapıştırıldı — 2.4.0 henüz yayında olmadığından sürüm `2.3.0`, Kotlin `2.2.10`, çıplak `featureX {` → `create("featureX") { initWith(getByName("debug")) }`: `./gradlew --offline -q :app:dependencies --configuration debugRuntimeClasspath` → `+--- io.palbase:palbe:2.3.0`; `./gradlew --offline :app:generatePalbaseDebug` → `BUILD SUCCESSFUL in 509ms`. Yani `maven("…")` kısa biçimi hem `pluginManagement` hem `dependencyResolutionManagement` içinde çözülüyor.

**Yayın kapısı — Dalga 2.** Bu görev plugin 2.4.0 Maven deposunda yayımlanmadan bir CLI sürümüne girmez: bastığı `io.palbase.codegen`/`io.palbase:palbe` **2.4.0** koordinatları o güne kadar çözülmez, ve 2.3'te kalan bir kullanıcının yapıştırdığı `palbase.env.debug`/`palbase.env.release` satırları sessizce yok sayılır (2.3 yalnız global `palbase.env`'i okur, yoksa `local` — `palbackend-android-src` `v2.3.0` `PalbaseCodegenPlugin.kt`: `providers.gradleProperty("palbase.env").orElse("local")`). Bu yüzden görev Dalga 1'in (T001–T022) arkasına taşındı ve kimliği değişti (eski T019). Red çıktısı yeni yerinde yeniden ölçüldü.

**Interfaces:**
- Consumes: `envs.Default` (`runLinkPrepared`), `localEnvName`, T018'in `wrote …/openapi.json` satırı (testin beklenen metni onun ardından başlar); test yardımcıları `linkKeyLocal` (T009, `link_local_stack_test.go`), `seedAndroidApp`, `stackServing`, `routeEnvironments`, `linkedAs`, `linkKeyMain`.
- Produces:
  - `const palbaseAndroidVersion = "2.4.0"` ve `const palbaseAndroidRepository = "https://palgroup.github.io/palbackend-android/"` (`internal/backend/android_setup.go`) — Android SDK sürümünün CLI'daki TEK yeri; Android SDK'nın release'i onu (ve bu görevin testindeki `gradleSetup` metnini) günceller.
  - `func printAndroidSetup(w io.Writer, env string)` (`android_setup.go`); `runLinkPrepared`'da `android` bayrağı (bu koşuda en az bir android config yazıldı) ile, Apple bloğundan sonra, web kablolamasından önce çağrılır.
  - Test yardımcısı `gradleSetup(env, release string) string` (`link_gradle_setup_test.go`) — basılan bloğun birebir metni.

- [ ] **Adım 1: Kırmızı testi yaz** — yeni dosya `internal/backend/link_gradle_setup_test.go`:
```go
package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gradleSetup is the block an Android link prints, for a checkout whose
// default environment is `env` and whose release line is `release`.
//
// The version is spelled out rather than read from the constant: it is what a
// person pastes into their build, and a bump is a decision the diff of this
// file should show.
func gradleSetup(env, release string) string {
	return `
Add Palbase to the Gradle build (Kotlin DSL shown):

  settings.gradle.kts, in pluginManagement { repositories { } } and in
  dependencyResolutionManagement { repositories { } }:
      maven("https://palgroup.github.io/palbackend-android/")

  build.gradle.kts of the project:
      plugins {
          id("org.jetbrains.kotlin.plugin.serialization") version "<your Kotlin version>" apply false
          id("io.palbase.codegen") version "2.4.0" apply false
      }

  build.gradle.kts of the app module:
      plugins {
          id("org.jetbrains.kotlin.plugin.serialization")
          id("io.palbase.codegen")
      }
      dependencies {
          implementation("io.palbase:palbe:2.4.0")
      }

  gradle.properties — the environment each build type compiles:
      palbase.env.debug=` + env + `
      ` + release + `
`
}

// AN ANDROID LINK SAYS HOW THE BUILD USES WHAT IT WROTE (FR-013).
//
// Apple got its three lines printed and Android got nothing: no repository, no
// plugin id, no dependency, and no word about which environment a build
// compiles (verification B5, L1 output in full: `▸ android / wrote …/
// android-config.json / linked to … / commit palbase/`). The environment is the
// one this link read from, as the listing names it — a project made in the
// panel names its first environment `production`, and a literal `main` here
// would be a build that finds no directory.
func TestAnAndroidLinkPrintsTheGradleSetupWithTheListedEnvironment(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	production := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"prodref000": production.URL})
	o := linkOpts{
		url:          production.URL,
		platforms:    []string{"android"},
		linkedEnv:    "production",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "production", Ref: "prodref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Contains(t, out.String(), "wrote palbase/environments/production/openapi.json\n"+
		gradleSetup("production", "palbase.env.release=production")+"\nlinked to todoapp (prd_a)\n")
}

// THE STACK ON THIS MACHINE IS NEVER A RELEASE. A link to it by address has
// `local` as its only environment, and a release build refuses a loopback
// address: the line that would map release to it is not printed as a setting.
func TestALinkToThisMachinesStackPrintsNoReleaseMapping(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	stack := stackServing(t, linkKeyLocal, nil)
	linkedAs(t, stack.URL, "operator")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL, platforms: []string{"android"}}, &out), out.String())

	assert.Contains(t, out.String(), gradleSetup("local",
		"# palbase.env.release — none here: local is the stack on this machine, which a release build refuses; "+
			"link a project and map release to one of its environments"))
	assert.NotContains(t, out.String(), "palbase.env.release=")
}

// ONLY WHERE ANDROID WAS WRITTEN. A checkout with no client binds the backend
// and has no Gradle build to set up.
func TestALinkWithNoAndroidClientPrintsNoGradleSetup(t *testing.T) {
	inScratchCheckout(t)
	stack := stackServing(t, linkKeyLocal, nil)
	linkedAs(t, stack.URL, "operator")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())

	assert.NotContains(t, out.String(), "Gradle")
	assert.NotContains(t, out.String(), "palbase.env.")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./internal/backend/ -run 'TestAnAndroidLinkPrintsTheGradleSetupWithTheListedEnvironment|TestALinkToThisMachinesStackPrintsNoReleaseMapping|TestALinkWithNoAndroidClientPrintsNoGradleSetup' -count=1` · Beklenen (T022'nin ardından ölçüldü): **FAIL** ×2 — `"added palbase/environments/local/ to .gitignore — …\nwrote palbase/environments/production/android-config.json\nwrote palbase/environments/production/openapi.json\n\nlinked to todoapp (prd_a)\n  contract read from production; each verb resolves its own environment\ncommit palbase/\n" does not contain "wrote palbase/environments/production/openapi.json\n\nAdd Palbase to the Gradle build (Kotlin DSL shown):\n…"` ve `"…wrote palbase/environments/local/openapi.json\n\nlinked to http://127.0.0.1:… (project)\ncommit palbase/\n" does not contain "\nAdd Palbase to the Gradle build (Kotlin DSL shown):\n…"`. `TestALinkWithNoAndroidClientPrintsNoGradleSetup` bugün de geçer — blok yalnız Android yazıldığında basılmalı (bekçi).
- [ ] **Adım 3: Uygula** —
  (a) yeni dosya `internal/backend/android_setup.go`:
```go
package backend

import (
	"fmt"
	"io"
)

// palbaseAndroidVersion is the Android SDK release `palbase link` tells an app to
// build with.
//
// ONE CONSTANT, BECAUSE THEY SHIP AS ONE. The `io.palbase.codegen` plugin and
// `io.palbase:palbe` are published together at a single version (the Android
// SDK's publish.sh; the plugin's POM pins the engine at its own version), and
// the plugin generates code against that version's runtime — two numbers here
// could only disagree. The Android SDK's release bumps it. It is 2.4.0 because
// the per-build-type keys printed below are 2.4's: 2.3 reads only the global
// `palbase.env`.
const palbaseAndroidVersion = "2.4.0"

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
// NOT A RELEASE WHEN IT IS THIS MACHINE. A link to the stack `palbase start`
// runs has `local` as its only environment, and a release build refuses a
// loopback address (the plugin, FR-206). Printing `palbase.env.release=local`
// would be handing over a line the build rejects, so the line says why there is
// none instead — as a comment, harmless if it is pasted.
func printAndroidSetup(w io.Writer, env string) {
	release := "palbase.env.release=" + env
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

  gradle.properties — the environment each build type compiles:
      palbase.env.debug=%[3]s
      %[4]s
`, palbaseAndroidRepository, palbaseAndroidVersion, env, release)
}
```
  (b) `internal/backend/project_link.go` `runLinkPrepared` — (1/3) şu bloğu:
```go
	apple := false
	web := false
	for _, c := range configs {
```
  şununla değiştir:
```go
	apple := false
	web := false
	android := false
	for _, c := range configs {
```
  (2/3) şu bloğu:
```go
		if c.platform == webPlatform {
			web = true
		}
		for _, p := range paths {
```
  şununla değiştir:
```go
		if c.platform == webPlatform {
			web = true
		}
		if c.platform == "android" && len(paths) > 0 {
			android = true
		}
		for _, p := range paths {
```
  (3/3) şu bloğu:
```go
		printEnvironmentSelectionSnippet(w, envs.Default)
	}
```
  şununla değiştir:
```go
		printEnvironmentSelectionSnippet(w, envs.Default)
	}
	// AND ANDROID'S, which got nothing (FR-013): the config and the contract sat
	// on disk with no word about the plugin that reads them, the dependency it
	// generates against, or which environment a build compiles.
	if android {
		printAndroidSetup(w, envs.Default)
	}
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./internal/backend/ -run 'TestAnAndroidLinkPrintsTheGradleSetupWithTheListedEnvironment|TestALinkToThisMachinesStackPrintsNoReleaseMapping|TestALinkWithNoAndroidClientPrintsNoGradleSetup' -count=1 -v` · Beklenen: `--- PASS: TestAnAndroidLinkPrintsTheGradleSetupWithTheListedEnvironment`, `--- PASS: TestALinkToThisMachinesStackPrintsNoReleaseMapping`, `--- PASS: TestALinkWithNoAndroidClientPrintsNoGradleSetup`, `ok  	github.com/palgroup/palbase-cli/internal/backend`.
- [ ] **Adım 5: Commit** — `git add internal/backend/android_setup.go internal/backend/project_link.go internal/backend/link_gradle_setup_test.go && git commit -m "feat(link): Android link'i Gradle kurulumunu basar — depo, plugin ve palbe tek sabitteki sürümle, palbase.env.debug/release listedeki ortamla; local release'e eşlenmez (FR-013)" -- internal/backend/android_setup.go internal/backend/project_link.go internal/backend/link_gradle_setup_test.go`

---

### T024: `doctor` Android bölümü — `gradle.properties` ve `local.properties`'teki `palbase.env.*` anahtarları
<!-- deps: [T022] | files: [internal/backend/android_doctor.go, cmd/palbase/doctor_android_section_test.go] | satisfies: [FR-019] -->

Ortamı seçen anahtarlar, plugin'in okuduğu yerlerden listelenir: Gradle kökünün `gradle.properties`'i, ardından `local.properties`'i. Gradle kökü RN/Flutter checkout'unda `android/`'dır (FR-015). Her satır anahtarın seçtiği ortamı plugin'in kendi köken diliyle söyler: `→ <env> (palbase.env.<x> in <dosya>)`. FR-211 ve prototipin `"$key in local.properties"` biçimi de bu. Etiket anahtarın son ekidir. Çıplak global `palbase.env` için etiket `any`'dir. Değer ve etiket `envname.Label`'dan geçer, çünkü dosya içeriği de başkasının metni olabilir. Anahtar şu durumlarda ✗ olur:
- Değerin adıyla birebir aynı bir dizin yoksa. Yalnız harf büyüklüğü farklı bir dizin varsa o dizin ve eşleme satırı söylenir. Plugin tam adla arar (FR-204): bu Mac'te bulunan dizin Linux CI'da bulunmaz. Hiçbir dizin yoksa ya `palbase link` ya da (`local` için) `palbase start` + `palbase link` önerilir.
- `local.properties`'te release anahtarı varsa. Bu dosya yalnız debuggable build'lere ulaşır (D-009, FR-201.3).
- `local.properties`'te çıplak `palbase.env` varsa. Plugin bu dosyada yalnız `palbase.env.<V|F|B>` okur.

Bölümün iki listesi de artık ortam adlarını kullandığı için `AndroidDoctor` bu adları bir kez okuyup ikisine verir. Bu yüzden `environmentDirLines` artık `names` alır. `readJSONFile`'ın parametresi `file` olur, çünkü dosya artık `path` paketini import ediyor.

Ayrıştırıcı, bu anahtarların yazıldığı `.properties` alt kümesini kapsar: `key=value`, `key: value`, `key value`, `#` ve `!` yorumları. Kaçış dizileri ve satır devamı desteklenmez. Aynı anahtar iki kez yazılmışsa son değer geçerlidir (Java). Değerin sonundaki boşluk korunur, tıpkı Java'nın okuyucusu gibi: `"main "` main değildir ve bunun söylenmesi amaçlanan davranıştır.

**Dalga 2** (T023'le birlikte, plugin 2.4.0 yayımlandıktan sonra): bu satırlar 2.4'ün çözümlemesini anlatır — build type başına `palbase.env.<x>` anahtarı, release'in tahmin etmeyip reddetmesi, loopback release reddi (FR-201, FR-206). 2.3 yalnız global `palbase.env`'i okur (yoksa `local`, `PalbaseCodegenPlugin.kt` `v2.3.0`); 2.3 kullanıcısına bu satırlar yanlış olurdu (`✓ debug → main` 2.3'te sayılmaz, `release not mapped — a release build refuses to guess` 2.3'te `local` derler).

**Interfaces:**
- Consumes: `AndroidCheckout`, `environmentDirsIn`, `shownEnvDir`, `envname.Label`, `localEnvName`, T022'nin test yardımcıları
- Produces:
  - `func gradleRootOf(buildFile string) string` (`"android"` ya da `"."`)
  - `func environmentKeyLines(dir, gradleRoot string, names []string) []DoctorLine` (T025 bunu genişletir)
  - `func environmentKeyLine(k propertyKey, file string, personal bool, names []string) DoctorLine` (T025 bunu yeniden yazar)
  - `const envKeyPrefix = "palbase.env"`
  - `func releaseKey(key string) bool`: `palbase.env.release` ya da `palbase.env.<x>Release`
  - `type propertyKey struct{ key, value string }`
  - `func palbaseEnvKeysIn(file string) []propertyKey`
  - `environmentDirLines(dir string, names []string, project bool)`

- [ ] **Adım 1: Kırmızı testi yaz** — `cmd/palbase/doctor_android_section_test.go` sonuna ekle:
```go

// THE KEYS THAT PICK AN ENVIRONMENT are listed where the Gradle plugin reads
// them — the root gradle.properties, then local.properties — each with the
// environment it names, in the plugin's own words for where a choice came from.
func TestDoctorListsThePalbaseEnvKeys(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "featureX", androidConfig("https://featurex.example", "pb_featurex_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "org.gradle.jvmargs=-Xmx2g\npalbase.env.debug=main\npalbase.env.release=main\n")
	writeFileIn(t, dir, "local.properties", "sdk.dir=/opt/android-sdk\npalbase.env.debug = featureX\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n"+
			"  ✓ debug      → main (palbase.env.debug in gradle.properties)\n"+
			"  ✓ release    → main (palbase.env.release in gradle.properties)\n"+
			"  ✓ debug      → featureX (palbase.env.debug in local.properties)\n")
}

// A KEY THAT NAMES NO DIRECTORY fails the build that reads it. One whose name
// differs only in case finds its directory on this Mac and not on CI: the
// plugin matches the exact name.
func TestDoctorNamesAKeyWhoseEnvironmentIsNotHere(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "featureX", androidConfig("https://featurex.example", "pb_featurex_cK"), withRoles)
	writeFileIn(t, dir, "local.properties", "palbase.env.debug=Featurex\npalbase.env.staging=featureZ\npalbase.env.qa=local\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ debug      → Featurex (palbase.env.debug in local.properties) — palbase/environments/featureX differs only in case, "+
			"and a build finds its directory by the exact name: palbase.env.debug=featureX\n"+
			"  ✗ staging    → featureZ (palbase.env.staging in local.properties) — no palbase/environments/featureZ here; "+
			"`palbase link` here writes one directory per environment of the project\n"+
			"  ✗ qa         → local (palbase.env.qa in local.properties) — no palbase/environments/local here; "+
			"`palbase start` in the backend, then `palbase link` here\n")
}

// WHAT local.properties CANNOT CARRY is said, not listed as if it counted: it
// reaches debuggable builds only (a release build is not one), and it holds
// per-build-type keys, not the global one.
func TestDoctorSaysWhichKeysLocalPropertiesDoesNotReach(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "local.properties", "palbase.env.release=main\npalbase.env=main\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    → main (palbase.env.release in local.properties) — ignored: local.properties reaches only debuggable builds, "+
			"and a release build is not one; put it in gradle.properties\n"+
			"  ✗ any        → main (palbase.env in local.properties) — ignored: local.properties carries palbase.env.<build type> keys, "+
			"not the global palbase.env\n")
}

// A CROSS-PLATFORM CHECKOUT'S GRADLE BUILD IS android/, and so are its
// properties files.
func TestDoctorReadsTheKeysOfACrossPlatformCheckout(t *testing.T) {
	dir := t.TempDir()
	writeFileIn(t, dir, "android/app/build.gradle", "android {\n    defaultConfig {\n        applicationId \"com.example.todo\"\n    }\n}\n")
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "android/gradle.properties", "palbase.env.release=main\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✓ release    → main (palbase.env.release in android/gradle.properties)\n")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./cmd/palbase/ -count=1 -run 'TestDoctor' -v` · Beklenen: **FAIL**, dört yeni test kırmızı. Bölüm dizin satırlarında bitiyor, örneğin `"…android (app/build.gradle.kts)\n  ✓ featureX   android-config.json with an api_key, openapi.json with x-palbase-roles\n  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles\n" does not contain "  ✓ main       …\n  ✓ debug      → main (palbase.env.debug in gradle.properties)\n…`. RN testinde: `"…android (android/app/build.gradle)\n  ✓ main       …\n" does not contain "  ✓ release    → main (palbase.env.release in android/gradle.properties)\n"`.
- [ ] **Adım 3: Uygula** — `internal/backend/android_doctor.go` importlarına `"path"` (`"os"`'tan sonra) ve `"slices"` (`"path/filepath"`'tan sonra) ekle. `AndroidDoctor`'ın gövdesini ve `environmentDirLines`'ın başını değiştir. Eski blok şu:
```go
func AndroidDoctor(dir string, project bool) []DoctorLine {
	return environmentDirLines(dir, project)
}

// environmentDirLines is one line per directory under palbase/environments:
// what the Gradle plugin reads from it, and what it would refuse.
func environmentDirLines(dir string, project bool) []DoctorLine {
	names := environmentDirsIn(dir)
	if len(names) == 0 {
```
  Yerine gelecek blok:
```go
func AndroidDoctor(dir string, project bool) []DoctorLine {
	names := environmentDirsIn(dir)
	lines := environmentDirLines(dir, names, project)
	return append(lines, environmentKeyLines(dir, gradleRootOf(AndroidCheckout(dir)), names)...)
}

// gradleRootOf is the Gradle root of the build whose file made the checkout an
// Android one, relative to the checkout: `android/` in a React Native or
// Flutter checkout (FR-015), the checkout itself otherwise. The plugin reads
// gradle.properties and local.properties from there.
func gradleRootOf(buildFile string) string {
	if strings.HasPrefix(buildFile, "android/") {
		return "android"
	}
	return "."
}

// environmentDirLines is one line per directory under palbase/environments —
// names, as environmentDirsIn read them: what the Gradle plugin reads from it,
// and what it would refuse.
func environmentDirLines(dir string, names []string, project bool) []DoctorLine {
	if len(names) == 0 {
```
  `readJSONFile`'ın başlığını değiştir. `path` artık bir import. Eski blok şu:
```go
// readJSONFile decodes the file at path into v; found is false when there is
// no such file.
func readJSONFile(path string, v any) (found bool, err error) {
	raw, err := os.ReadFile(path)
```
  Yerine gelecek blok:
```go
// readJSONFile decodes file into v; found is false when there is no such file.
func readJSONFile(file string, v any) (found bool, err error) {
	raw, err := os.ReadFile(file)
```
  Dosyanın sonuna ekle:
```go

// environmentKeyLines is one line per palbase.env key in the Gradle root's
// gradle.properties, then its local.properties — the two files a person writes
// a choice in — with the environment each names and whether a build can use it.
func environmentKeyLines(dir, gradleRoot string, names []string) []DoctorLine {
	var lines []DoctorLine
	for _, file := range []string{"gradle.properties", "local.properties"} {
		shown := path.Join(gradleRoot, file)
		for _, k := range palbaseEnvKeysIn(filepath.Join(dir, filepath.FromSlash(shown))) {
			lines = append(lines, environmentKeyLine(k, shown, file == "local.properties", names))
		}
	}
	return lines
}

// environmentKeyLine says what one key picks, in the plugin's own words for
// where a choice came from (`palbase.env.debug in local.properties`).
func environmentKeyLine(k propertyKey, file string, personal bool, names []string) DoctorLine {
	label := envname.Label(strings.TrimPrefix(k.key, envKeyPrefix+"."))
	if k.key == envKeyPrefix {
		label = "any"
	}
	head := fmt.Sprintf("→ %s (%s in %s)", envname.Label(k.value), k.key, file)
	switch {
	case personal && k.key == envKeyPrefix:
		return DoctorLine{Label: label, Detail: head + " — ignored: local.properties carries palbase.env.<build type> keys, not the global palbase.env"}
	case personal && releaseKey(k.key):
		// D-009: a forgotten `palbase.env.release=local` here built a
		// loopback, cleartext release APK, green.
		return DoctorLine{Label: label, Detail: head + " — ignored: local.properties reaches only debuggable builds, " +
			"and a release build is not one; put it in gradle.properties"}
	}
	if slices.Contains(names, k.value) {
		return DoctorLine{OK: true, Label: label, Detail: head}
	}
	for _, name := range names {
		if strings.EqualFold(name, k.value) {
			return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — %s differs only in case, and a build finds its "+
				"directory by the exact name: %s=%s", head, shownEnvDir(name), k.key, name)}
		}
	}
	cure := "`palbase link` here writes one directory per environment of the project"
	if k.value == localEnvName {
		cure = "`palbase start` in the backend, then `palbase link` here"
	}
	return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — no %s here; %s", head, shownEnvDir(k.value), cure)}
}

// envKeyPrefix is the Gradle property an environment is chosen with: bare, the
// global of plugin 2.3, or suffixed with a build type, flavor or variant.
const envKeyPrefix = "palbase.env"

// releaseKey reports whether key picks the environment of a release build — the
// release build type itself, or a flavored variant of it (`freeRelease`).
func releaseKey(key string) bool {
	suffix, ok := strings.CutPrefix(key, envKeyPrefix+".")
	return ok && (suffix == "release" || strings.HasSuffix(suffix, "Release"))
}

// propertyKey is one key of a .properties file and the value it holds.
type propertyKey struct{ key, value string }

// palbaseEnvKeysIn is every palbase.env key in the .properties file, in file
// order; a key written twice holds its last value, as Gradle reads it.
//
// The grammar is the part these keys are written in — `key=value`,
// `key: value` or `key value`, `#` and `!` comments — without escapes or line
// continuations. A value keeps its trailing blanks, as Java's reader does: a
// directory named "main " is not main, and saying so is the point.
func palbaseEnvKeysIn(file string) []propertyKey {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var keys []propertyKey
	at := map[string]int{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimLeft(strings.TrimSuffix(line, "\r"), " \t\f")
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		key, value := line, ""
		if end := strings.IndexAny(line, "=: \t\f"); end >= 0 {
			key = line[:end]
			value = strings.TrimLeft(line[end:], " \t\f")
			if value != "" && (value[0] == '=' || value[0] == ':') {
				value = strings.TrimLeft(value[1:], " \t\f")
			}
		}
		if key != envKeyPrefix && !strings.HasPrefix(key, envKeyPrefix+".") {
			continue
		}
		if i, seen := at[key]; seen {
			keys[i].value = value
			continue
		}
		at[key] = len(keys)
		keys = append(keys, propertyKey{key: key, value: value})
	}
	return keys
}
```
- [ ] **Adım 4: Yeşil** — Run: `go test ./cmd/palbase/ -count=1 -run 'TestDoctor'` · Beklenen: `ok  	github.com/palgroup/palbase-cli/cmd/palbase	0.920s`. `-v` ile yeni dört test ve T022'nin testleri `--- PASS`, ikiz testi APFS'te `--- SKIP`.
- [ ] **Adım 5: Commit** — `git add cmd/palbase/doctor_android_section_test.go internal/backend/android_doctor.go && git commit -m "feat(doctor): Android bölümü gradle.properties ve local.properties'teki palbase.env.* anahtarlarını ortamıyla basar — dizini olmayan, yalnız harf büyüklüğü tutan ve local.properties'in ulaşmadığı anahtar söylenir (FR-019)" -- cmd/palbase/doctor_android_section_test.go internal/backend/android_doctor.go`

---

### T025: `doctor` Android bölümü — eşlenmemiş `release` ve release'e eşlenmiş bu makinenin yığını
<!-- deps: [T024, T001] | files: [internal/backend/android_doctor.go, cmd/palbase/doctor_android_section_test.go] | satisfies: [FR-019] -->

Release build'in ortamı yalnız `gradle.properties`'ten okunur. `local.properties` ona ulaşmaz (D-009). Anahtarı olmayan bir release build tahmin edilmez, reddedilir (FR-201.9). Bu görev iki şeyi söyler:
- **Eşlenmemiş release.** `gradle.properties`'te bir release anahtarı (`palbase.env.release` ya da `palbase.env.<x>Release`) ve global `palbase.env` yoksa en sonda bir `✗ release not mapped in gradle.properties` satırı basılır. Satır eşlenecek ortamı adlandırır: checkout'un varsayılanı, o bu makinenin yığınıysa loopback olmayan ilk ortam, asla `local`. Build script'teki `palbase { environment = "<name>" }` seçeneği de parantez içinde söylenir, çünkü doctor DSL'i okuyamaz. Aday yoksa öneri link'tir. Checkout bir projeye bağlıysa "`palbase link` here, then map release to one of the project's environments", bağlı değilse "link a project and map release to one of its environments" basılır. İkincisi T023'ün Gradle bloğundaki yorumla aynı dildir.
- **Release'e eşlenmiş bu makinenin yığını.** Plugin bunu FR-206 ile reddeder. Hedef ortam adıyla `local` olabilir ya da `android-config.json`'unun `base_url`'i loopback olabilir (B8: eski bir link'in `main/`'e yazdığı loopback). Böyle bir release anahtarının satırı ✗ olur ve bulut ortamını önerir. Başka hiçbir release anahtarı yoksa global `palbase.env` de release'in düştüğü yerdir (FR-201.7). O zaman global anahtarın satırı "a release build falls back to it" der ve `palbase.env.release=<aday>` önerir.

**Anahtar değeri bir addır, asla bir yol değil.** Loopback denetimi değerle bir dosya okur ve `EnvDir`'in `path.Join`'i `..`'yu çözer. Bu yüzden `envname.CheckDir`'den (T001) geçmeyen bir değer için hiçbir dosya okunmaz. `TestDoctorReadsNoFileThroughAKeyThatIsAPath` T024'te zaten yeşil (T024 dosya okumuyor). Bu test, bu görevin eklediği okumanın yol üzerinden kaçmadığını kilitler. Kapısız bir uygulamaya karşı kırmızısı ölçüldü: `✗ release    → "../outside" (palbase.env.release in gradle.properties) — "../outside"'s base_url, http://127.0.0.1:1, is this machine, which a release build refuses; …`, yani `palbase/outside/android-config.json` okundu.

**Dalga 2** (T023'le birlikte, plugin 2.4.0 yayımlandıktan sonra): bu satırlar 2.4'ün çözümlemesini anlatır — build type başına `palbase.env.<x>` anahtarı, release'in tahmin etmeyip reddetmesi, loopback release reddi (FR-201, FR-206). 2.3 yalnız global `palbase.env`'i okur (yoksa `local`, `PalbaseCodegenPlugin.kt` `v2.3.0`); 2.3 kullanıcısına bu satırlar yanlış olurdu (`✓ debug → main` 2.3'te sayılmaz, `release not mapped — a release build refuses to guess` 2.3'te `local` derler).

**Interfaces:**
- Consumes: T024'ün `environmentKeyLines`/`environmentKeyLine`/`releaseKey`/`envKeyPrefix`/`palbaseEnvKeysIn`'i, `defaultEnvironment`, `isLoopbackAddress` (`target.go`), `envname.CheckDir` (T001), `readJSONFile`
- Produces:
  - `func environmentKeyLines(dir, gradleRoot string, names []string, project bool) []DoctorLine`
  - `func environmentKeyLine(dir string, k propertyKey, file string, personal, release bool, names []string, cure releaseCure) DoctorLine`
  - `type releaseCure struct{ candidate string; project bool }` ve `func (c releaseCure) of(build, key string) string`
  - `func loopbackEnvironment(dir, name string) string` (neden cümlesi ya da `""`)
  - `func releaseCandidate(dir string, names []string) string`

- [ ] **Adım 1: Kırmızı testi yaz** — `cmd/palbase/doctor_android_section_test.go` sonuna ekle:
```go

// A RELEASE BUILD WITH NO ENVIRONMENT is refused by the plugin; doctor says so
// before Gradle does, with the environment to map it to — never `local`.
func TestDoctorSaysReleaseIsNotMapped(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.debug=local\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✓ debug      → local (palbase.env.debug in gradle.properties)\n"+
			"  ✗ release    not mapped in gradle.properties — a release build refuses to guess its environment; "+
			"map release to a cloud environment: palbase.env.release=main (or palbase { environment = \"<name>\" } in the build script)\n")
}

// WITH NOTHING HERE A RELEASE BUILD MAY BUILD, the cure is the step that
// brings one: a link — of a project, when the checkout names none yet.
func TestDoctorSaysALocalOnlyCheckoutHasNoEnvironmentForRelease(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    not mapped in gradle.properties — a release build refuses to guess its environment; "+
			"link a project and map release to one of its environments\n")
}

func TestDoctorSendsALinkedCheckoutWithNoEnvironmentToLink(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	linkedToAProject(t, dir)

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    not mapped in gradle.properties — a release build refuses to guess its environment; "+
			"`palbase link` here, then map release to one of the project's environments\n")
}

// A RELEASE BUILD OF THIS MACHINE'S STACK is refused by the plugin (FR-206):
// `local` by name, and any environment whose address is loopback — what an
// older `link` wrote under main/ for a stack started here (B8).
func TestDoctorNamesALoopbackEnvironmentMappedToRelease(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	androidEnvironmentIn(t, dir, "onbox", androidConfig("http://127.0.0.1:18865", "pb_onbox_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env.release=local\npalbase.env.freeRelease=onbox\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ release    → local (palbase.env.release in gradle.properties) — local is the stack on this machine, "+
			"which a release build refuses; map release to a cloud environment: palbase.env.release=main\n"+
			"  ✗ freeRelease → onbox (palbase.env.freeRelease in gradle.properties) — onbox's base_url, http://127.0.0.1:18865, "+
			"is this machine, which a release build refuses; map freeRelease to a cloud environment: palbase.env.freeRelease=main\n")
	require.NotContains(t, out, "not mapped")
}

// A KEY'S VALUE IS A NAME, NEVER A PATH: doctor reads no file through one
// that is not a single directory name, and says it names no environment.
func TestDoctorReadsNoFileThroughAKeyThatIsAPath(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "palbase/outside/android-config.json", androidConfig("http://127.0.0.1:1", "pb_outside_cK"))
	writeFileIn(t, dir, "gradle.properties", "palbase.env.release=../outside\n")

	require.Contains(t, runDoctorIn(t, dir),
		"  ✗ release    → \"../outside\" (palbase.env.release in gradle.properties) — no palbase/environments/\"../outside\" here; "+
			"`palbase link` here writes one directory per environment of the project\n")
}

// THE GLOBAL KEY OF PLUGIN 2.3 reaches a release build no other key maps: it
// is a mapping, and a loopback one is refused like any other.
func TestDoctorNamesTheGlobalKeyWhenReleaseFallsToALoopbackEnvironment(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	androidEnvironmentIn(t, dir, "local", androidConfig("http://127.0.0.1:54321", "pb_local_cK"), withRoles)
	androidEnvironmentIn(t, dir, "main", androidConfig("https://main.example", "pb_main_cK"), withRoles)
	writeFileIn(t, dir, "gradle.properties", "palbase.env=local\n")

	out := runDoctorIn(t, dir)
	require.Contains(t, out,
		"  ✗ any        → local (palbase.env in gradle.properties) — a release build falls back to it, and local is the stack "+
			"on this machine, which a release build refuses; map release to a cloud environment: palbase.env.release=main\n")
	require.NotContains(t, out, "not mapped")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./cmd/palbase/ -count=1 -run 'TestDoctor' -v` · Beklenen: **FAIL**, beş test kırmızı. `TestDoctorReadsNoFileThroughAKeyThatIsAPath` T024'te zaten yeşil (yukarıya bak). Belirleyici satırlar:
  - `TestDoctorNamesALoopbackEnvironmentMappedToRelease`: `"…  ✓ release    → local (palbase.env.release in gradle.properties)\n  ✓ freeRelease → onbox (palbase.env.freeRelease in gradle.properties)\n" does not contain "  ✗ release    → local …`. Loopback bir release eşlemesi ✓ basılıyor.
  - `TestDoctorNamesTheGlobalKeyWhenReleaseFallsToALoopbackEnvironment`: `"…  ✓ any        → local (palbase.env in gradle.properties)\n" does not contain "  ✗ any        → local …`.
  - `TestDoctorSaysReleaseIsNotMapped`: `"…  ✓ debug      → local (palbase.env.debug in gradle.properties)\n" does not contain "…  ✗ release    not mapped in gradle.properties …`.
  - `TestDoctorSaysALocalOnlyCheckoutHasNoEnvironmentForRelease` ve `TestDoctorSendsALinkedCheckoutWithNoEnvironmentToLink`: çıktıda `✗ release    not mapped` satırı yok.
- [ ] **Adım 3: Uygula** — `internal/backend/android_doctor.go` içinde `AndroidDoctor`'ın son satırını değiştir:
```go
	return append(lines, environmentKeyLines(dir, gradleRootOf(AndroidCheckout(dir)), names, project)...)
```
  T024'ün `environmentKeyLines` ve `environmentKeyLine` fonksiyonlarını, `// environmentKeyLines is one line per palbase.env key …` yorumundan `// envKeyPrefix is the Gradle property …` yorumuna kadar, yorumlarıyla birlikte **tamamen** şununla değiştir:
```go
// environmentKeyLines is one line per palbase.env key in the Gradle root's
// gradle.properties, then its local.properties — the two files a person writes
// a choice in — with the environment each names and whether a build can use it;
// and a last line when nothing in gradle.properties gives a release build one.
//
// RELEASE IS READ FROM gradle.properties ALONE. local.properties reaches
// debuggable builds only (D-009), and a release build with no key is refused
// rather than guessed (FR-201).
func environmentKeyLines(dir, gradleRoot string, names []string, project bool) []DoctorLine {
	shared := path.Join(gradleRoot, "gradle.properties")
	personal := path.Join(gradleRoot, "local.properties")
	committed := palbaseEnvKeysIn(filepath.Join(dir, filepath.FromSlash(shared)))
	releaseMapped, global := false, false
	for _, k := range committed {
		releaseMapped = releaseMapped || releaseKey(k.key)
		global = global || k.key == envKeyPrefix
	}

	cure := releaseCure{candidate: releaseCandidate(dir, names), project: project}
	var lines []DoctorLine
	for _, k := range committed {
		// The global key of plugin 2.3 is where a release build no other key
		// maps lands (FR-201 step 7).
		release := releaseKey(k.key) || (k.key == envKeyPrefix && !releaseMapped)
		lines = append(lines, environmentKeyLine(dir, k, shared, false, release, names, cure))
	}
	for _, k := range palbaseEnvKeysIn(filepath.Join(dir, filepath.FromSlash(personal))) {
		lines = append(lines, environmentKeyLine(dir, k, personal, true, false, names, cure))
	}
	if !releaseMapped && !global {
		detail := "not mapped in " + shared + " — a release build refuses to guess its environment; " +
			cure.of("release", envKeyPrefix+".release")
		if cure.candidate != "" {
			detail += " (or palbase { environment = \"<name>\" } in the build script)"
		}
		lines = append(lines, DoctorLine{Label: "release", Detail: detail})
	}
	return lines
}

// releaseCure is how a release build gets an environment it may build: the
// candidate here, or — with none — the link that brings one.
type releaseCure struct {
	candidate string // releaseCandidate's answer
	project   bool   // the checkout is linked to a project
}

// of is the cure for build, whose environment the key names.
func (c releaseCure) of(build, key string) string {
	switch {
	case c.candidate != "":
		return fmt.Sprintf("map %s to a cloud environment: %s=%s", build, key, c.candidate)
	case c.project:
		return fmt.Sprintf("`palbase link` here, then map %s to one of the project's environments", build)
	default:
		return fmt.Sprintf("link a project and map %s to one of its environments", build)
	}
}

// environmentKeyLine says what one key picks, in the plugin's own words for
// where a choice came from (`palbase.env.debug in local.properties`). personal
// is local.properties; release is a key a release build reads.
func environmentKeyLine(dir string, k propertyKey, file string, personal, release bool, names []string, cure releaseCure) DoctorLine {
	label := envname.Label(strings.TrimPrefix(k.key, envKeyPrefix+"."))
	if k.key == envKeyPrefix {
		label = "any"
	}
	head := fmt.Sprintf("→ %s (%s in %s)", envname.Label(k.value), k.key, file)
	switch {
	case personal && k.key == envKeyPrefix:
		return DoctorLine{Label: label, Detail: head + " — ignored: local.properties carries palbase.env.<build type> keys, not the global palbase.env"}
	case personal && releaseKey(k.key):
		// D-009: a forgotten `palbase.env.release=local` here built a
		// loopback, cleartext release APK, green.
		return DoctorLine{Label: label, Detail: head + " — ignored: local.properties reaches only debuggable builds, " +
			"and a release build is not one; put it in gradle.properties"}
	}
	if release {
		if why := loopbackEnvironment(dir, k.value); why != "" {
			// A release build of this machine's stack is refused by the plugin
			// (FR-206) — and reaches nobody else's device if it were not.
			build, key := label, k.key
			if k.key == envKeyPrefix {
				build, key = "release", envKeyPrefix+".release"
				why = "a release build falls back to it, and " + why
			}
			return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — %s, which a release build refuses; %s", head, why, cure.of(build, key))}
		}
	}
	if slices.Contains(names, k.value) {
		return DoctorLine{OK: true, Label: label, Detail: head}
	}
	for _, name := range names {
		if strings.EqualFold(name, k.value) {
			return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — %s differs only in case, and a build finds its "+
				"directory by the exact name: %s=%s", head, shownEnvDir(name), k.key, name)}
		}
	}
	missing := "`palbase link` here writes one directory per environment of the project"
	if k.value == localEnvName {
		missing = "`palbase start` in the backend, then `palbase link` here"
	}
	return DoctorLine{Label: label, Detail: fmt.Sprintf("%s — no %s here; %s", head, shownEnvDir(k.value), missing)}
}

// loopbackEnvironment says why name is this machine's stack — its name, or the
// loopback address its Android config carries — or "" when it is not.
//
// A KEY'S VALUE IS SOMEBODY'S TEXT, and path.Join resolves `..` (EnvDir): a
// name that is not one directory is never read through.
func loopbackEnvironment(dir, name string) string {
	if strings.EqualFold(name, localEnvName) {
		return envname.Label(name) + " is the stack on this machine"
	}
	if envname.CheckDir(name) != nil {
		return ""
	}
	var config appEnvironment
	if found, err := readJSONFile(filepath.Join(dir, filepath.FromSlash(ConfigPath(name, "android"))), &config); found &&
		err == nil && isLoopbackAddress(config.BaseURL) {
		return fmt.Sprintf("%s's base_url, %s, is this machine", envname.Label(name), config.BaseURL)
	}
	return ""
}

// releaseCandidate is the environment here a release build may be mapped to:
// the checkout's default when it is not this machine's stack, else the first
// by name that is not; "" when every one is.
func releaseCandidate(dir string, names []string) string {
	for _, name := range append([]string{defaultEnvironment(names)}, names...) {
		if name != "" && loopbackEnvironment(dir, name) == "" {
			return name
		}
	}
	return ""
}

```
- [ ] **Adım 4: Yeşil** — Run: `go test ./cmd/palbase/ -count=1 -run 'TestDoctor'` · Beklenen: `ok  	github.com/palgroup/palbase-cli/cmd/palbase	1.141s`. `-v` ile T022, T024 ve T025'in bütün testleri `--- PASS`, ikiz testi APFS'te `--- SKIP`.
- [ ] **Adım 5: Commit** — `git add cmd/palbase/doctor_android_section_test.go internal/backend/android_doctor.go && git commit -m "feat(doctor): release'e ortam eşlenmemişse ya da bu makinenin yığını (local, loopback base_url) eşlenmişse Android bölümü söyler ve eşlenecek bulut ortamını adlandırır (FR-019)" -- cmd/palbase/doctor_android_section_test.go internal/backend/android_doctor.go`

---

### T026: Android checkout'unda `doctor`'ın `env` satırı "verbs only" notunu taşır
<!-- deps: [T022] | files: [cmd/palbase/doctor.go, cmd/palbase/doctor_local_test.go, cmd/palbase/doctor_android_section_test.go] | satisfies: [FR-019] -->

`env` satırı bir **fiilin** o an nerede iş göreceğini söyler (`linkProbes`, `doctor.go:262`). Android checkout'unda bu satır app'in derlediği ortam gibi okunuyordu. "none is selected" hatası insanları `palbase env use`'a gönderiyordu, oysa bu komut bir APK'nın derlediği hiçbir şeyi değiştirmez (doğrulama B7 PARTLY). Android checkout'unda satırın sonuna `— verbs only; the build type picks the app's environment` eklenir. Çözücünün reddi, doctor'ın basmadığı seçenek listesini tanıtan bir iki nokta üst üsteyle biter. Not bu iki noktanın yerini alır. Not **yalnız Android checkout'unda** basılır: "build type" Android'in sözcüğüdür, iOS'ta seçimi Xcode configuration'ı yapar. `doctor`'ın `Short` metni ve doc yorumu Android bölümünü anar. `AndroidCheckout` artık tek bir `os.Getwd()` ile bir kez hesaplanır ve T022'nin bloğu onu kullanır.

Test, üretim `doctor`'ını gerçek çözücüyle koşturur. Bunun için `runDoctorIn` bir bulut handler'ı ve token alan `runDoctorAgainst`'e devredilir (T013'ün yardımcısı, davranışı değişmez). Bulut, iki ortamlı `todoapp`'i `GET /api/v2/projects`'ten döner. `env_route_test.go` de aynı rotayı kullanıyor.

**Dalga 2** (T023'le birlikte, plugin 2.4.0 yayımlandıktan sonra): not 2.4'ün modelini anlatır — ortamı build type seçer (FR-201). 2.3 yalnız global `palbase.env`'i okur (yoksa `local`, `PalbaseCodegenPlugin.kt` `v2.3.0`); 2.3 kullanıcısına bu satırlar yanlış olurdu (2.3'te ortamı global `palbase.env` seçer, build type değil).

**Interfaces:**
- Consumes: `backend.AndroidCheckout`, `backend.AndroidDoctor` (T022), `linkProbes`, `runDoctorIn` (T013)
- Produces:
  - `func withAppEnvNote(detail string) string` (`cmd/palbase/doctor.go`)
  - Test yardımcıları: `runDoctorAgainst(t *testing.T, dir string, cloud http.Handler, token string) string` (`doctor_local_test.go`) ve `twoEnvironmentsCloud() http.Handler` (`doctor_android_section_test.go`)
  - `doctor` Short: `Show cloud addresses and diagnose login, link, Docker, Node, Bun and an Android app's environments`

- [ ] **Adım 1: Kırmızı testi yaz** — `cmd/palbase/doctor_local_test.go` içinde `runDoctorIn`'in başını değiştir. Eski blok şu:
```go
func runDoctorIn(t *testing.T, dir string) string {
	t.Helper()
	cloud := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(cloud.Close)
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_PLATFORM_URL", cloud.URL)
	t.Setenv("PALBASE_AUTH_URL", cloud.URL)
	t.Setenv("PALBASE_ACCESS_TOKEN", "")
```
  Yerine gelecek blok (fonksiyonun geri kalanı `root := newRootCmd()`'den itibaren aynı kalır):
```go
func runDoctorIn(t *testing.T, dir string) string {
	t.Helper()
	return runDoctorAgainst(t, dir, http.NotFoundHandler(), "")
}

// runDoctorAgainst runs the production `palbase doctor` in dir against a cloud
// that answers as cloud does, holding token as PALBASE_ACCESS_TOKEN.
func runDoctorAgainst(t *testing.T, dir string, cloud http.Handler, token string) string {
	t.Helper()
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_PLATFORM_URL", srv.URL)
	t.Setenv("PALBASE_AUTH_URL", srv.URL)
	t.Setenv("PALBASE_ACCESS_TOKEN", token)
```
  `cmd/palbase/doctor_android_section_test.go` importlarına `"encoding/json"` ve `"net/http"` ekle. Dosyanın sonuna ekle:
```go

// twoEnvironmentsCloud lists one project, todoapp, with two environments —
// the listing the resolver refuses to pick from when nothing selected one.
func twoEnvironmentsCloud() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/projects" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": "prd_a", "name": "todoapp",
			"environments": []map[string]any{
				{"ref": "mainref000", "name": "main", "status": "Running"},
				{"ref": "stagref000", "name": "staging", "status": "Running"},
			},
		}})
	})
}

// THE ENV LINE IS WHERE A VERB WOULD ACT, and in an Android checkout it reads
// as the environment the app builds — whose fix, `palbase env use`, changes
// nothing an APK compiles. There it says what it is.
func TestDoctorSaysTheEnvLineIsForVerbsInAnAndroidCheckout(t *testing.T) {
	dir := t.TempDir()
	androidCheckoutIn(t, dir)
	linkedToAProject(t, dir)

	require.Contains(t, runDoctorAgainst(t, dir, twoEnvironmentsCloud(), "person-token"),
		"  ✗ env        todoapp has 2 environments and none is selected — verbs only; the build type picks the app's environment\n")
}

// NEGATIVE CONTROL: outside an Android checkout the line is the resolver's own,
// as before.
func TestDoctorLeavesTheEnvLineAloneOutsideAnAndroidCheckout(t *testing.T) {
	dir := t.TempDir()
	linkedToAProject(t, dir)

	require.Contains(t, runDoctorAgainst(t, dir, twoEnvironmentsCloud(), "person-token"),
		"  ✗ env        todoapp has 2 environments and none is selected:\n")
}
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `go test ./cmd/palbase/ -count=1 -run 'TestDoctor' -v` · Beklenen: **FAIL**, `--- FAIL: TestDoctorSaysTheEnvLineIsForVerbsInAnAndroidCheckout`. Çıktıdaki satır `  ✗ env        todoapp has 2 environments and none is selected:` şeklinde: not yok, iki nokta duruyor. Negatif kontrol `TestDoctorLeavesTheEnvLineAloneOutsideAnAndroidCheckout` şimdiden PASS.
- [ ] **Adım 3: Uygula** — `cmd/palbase/doctor.go`'da `doctorCmd`'nin doc yorumunu ve `Short`'u değiştir. Eski blok şu:
```go
// and Bun (`push`'s bundler). Informative
// only (always exit 0): doctor diagnoses, the failing command still owns its
// error.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Show cloud addresses and diagnose login, link, Docker, Node and Bun",
```
  Yerine gelecek blok:
```go
// and Bun (`push`'s bundler) — and, in an Android checkout, what its Gradle
// build reads. Informative
// only (always exit 0): doctor diagnoses, the failing command still owns its
// error.
func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Show cloud addresses and diagnose login, link, Docker, Node, Bun and an Android app's environments",
```
  `linkProbes` döngüsünü ve `localStackProbes` bloğunu değiştir. Eski blok şu:
```go
			for _, l := range linkProbes(backend.ReadLinkedProject, backend.ReadTarget,
				func() (backend.Resolved, error) { return backend.ResolveFor(cmd) }) {
				if l.ok {
					ok(l.label, l.detail)
				} else {
					bad(l.label, l.detail)
				}
			}
			if wd, err := os.Getwd(); err == nil {
				for _, l := range localStackProbes(wd, backend.CommittedLocalEnvironment) {
					bad(l.label, l.detail)
				}
			}
```
  Yerine gelecek blok:
```go
			wd, wdErr := os.Getwd()
			android := ""
			if wdErr == nil {
				android = backend.AndroidCheckout(wd)
			}
			for _, l := range linkProbes(backend.ReadLinkedProject, backend.ReadTarget,
				func() (backend.Resolved, error) { return backend.ResolveFor(cmd) }) {
				if l.label == "env" && android != "" {
					l.detail = withAppEnvNote(l.detail)
				}
				if l.ok {
					ok(l.label, l.detail)
				} else {
					bad(l.label, l.detail)
				}
			}
			if wdErr == nil {
				for _, l := range localStackProbes(wd, backend.CommittedLocalEnvironment) {
					bad(l.label, l.detail)
				}
			}
```
  T022'nin eklediği bölüm bloğunu hesaplanmış `android` değerine bağla. Eski blok şu:
```go
			if wd, err := os.Getwd(); err == nil {
				if file := backend.AndroidCheckout(wd); file != "" {
					_, notAProject := backend.ReadLinkedProject()
					fmt.Fprintf(out, "android (%s)\n", file)
					for _, l := range backend.AndroidDoctor(wd, notAProject == nil) {
						if l.OK {
							ok(l.Label, l.Detail)
						} else {
							bad(l.Label, l.Detail)
						}
					}
				}
			}
```
  Yerine gelecek blok:
```go
			if android != "" {
				_, notAProject := backend.ReadLinkedProject()
				fmt.Fprintf(out, "android (%s)\n", android)
				for _, l := range backend.AndroidDoctor(wd, notAProject == nil) {
					if l.OK {
						ok(l.Label, l.Detail)
					} else {
						bad(l.Label, l.Detail)
					}
				}
			}
```
  `// localStackProbes names a …` yorumunun hemen önüne ekle:
```go
// withAppEnvNote glosses the env line in an Android checkout (FR-019).
//
// The line is where a VERB would act — push, spec, logs — and there it read as
// the environment the app builds: "none is selected" sent people to `palbase
// env use`, which changes nothing an APK compiles. The resolver's refusal ends
// on a colon introducing the choices doctor does not print; the note takes its
// place.
func withAppEnvNote(detail string) string {
	return strings.TrimSuffix(detail, ":") + " — verbs only; the build type picks the app's environment"
}

```
- [ ] **Adım 4: Yeşil** — Run: `go test ./cmd/palbase/ -count=1 -run 'TestDoctor' -v` · Beklenen: bütün doctor testleri `--- PASS` (ikiz testi APFS'te `--- SKIP`), `ok  	github.com/palgroup/palbase-cli/cmd/palbase	1.111s`. Paketin tamamı: `go test ./cmd/palbase/ -count=1` · Beklenen: `ok  	github.com/palgroup/palbase-cli/cmd/palbase	9.463s`.
- [ ] **Adım 5: Commit** — `git add cmd/palbase/doctor.go cmd/palbase/doctor_android_section_test.go cmd/palbase/doctor_local_test.go && git commit -m "fix(doctor): Android checkout'unda env satırı \"verbs only; the build type picks the app's environment\" notunu taşır — env use APK'yı değiştirmez (FR-019)" -- cmd/palbase/doctor.go cmd/palbase/doctor_android_section_test.go cmd/palbase/doctor_local_test.go`

---

### Son kapı (T001–T026)
Ölçüldü, scratch `5a8310b` (T026'nın commit'i; taban `20e5d7e`'nin üstünde 26 commit, görev başına bir):
- Run: `gofmt -l .` · Beklenen: çıktı yok. Run: `go vet ./...`, `go vet -tags e2e ./tests/e2e/` · Beklenen: exit 0. Run: `GOTOOLCHAIN=go1.26.6 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...` · Beklenen: `0 issues.`
- Run: `go test -count=1 ./...` · Beklenen: 24 paket `ok`; `internal/backend` yalnız B16 ile FAIL (`FAIL	github.com/palgroup/palbase-cli/internal/backend	106.783s`).
- Run: `go test -race -count=1 -timeout 25m ./...` (CI'ın kapısı) · Beklenen: 24 paket `ok` (ör. `ok  	github.com/palgroup/palbase-cli/cmd/palbase	11.082s`, `ok  	github.com/palgroup/palbase-cli/internal/envname	3.699s`); `internal/backend` yalnız B16 ile FAIL (`FAIL	github.com/palgroup/palbase-cli/internal/backend	107.734s`); çıktıda `WARNING: DATA RACE` yok (0).
- Uçtan uca, derlenmiş `palbase` ile (`go build -o <scratch>/palbase ./cmd/palbase`). Scratch bir Android checkout'u: `app/build.gradle.kts` (`applicationId = "com.example.todo"`); `palbase/environments/main` (config + `x-palbase-roles`'lü sözleşme), `featureX` (yalnız config), `local` (loopback config + sözleşme); `gradle.properties` `palbase.env.debug=main` ve `palbase.env.release=local`; `local.properties` `palbase.env.debug=Featurex`. Run: `HOME=<boş dizin> PALBASE_PLATFORM_URL=http://127.0.0.1:9 PALBASE_AUTH_URL=http://127.0.0.1:9 PALBASE_ACCESS_TOKEN= palbase doctor` · Beklenen, bölüm:
```
android (app/build.gradle.kts)
  ✗ featureX   no openapi.json — `palbase push`, then `palbase link` here
  ✓ local      android-config.json with an api_key, openapi.json with x-palbase-roles
  ✓ main       android-config.json with an api_key, openapi.json with x-palbase-roles
  ✓ debug      → main (palbase.env.debug in gradle.properties)
  ✗ release    → local (palbase.env.release in gradle.properties) — local is the stack on this machine, which a release build refuses; map release to a cloud environment: palbase.env.release=main
  ✗ debug      → Featurex (palbase.env.debug in local.properties) — palbase/environments/featureX differs only in case, and a build finds its directory by the exact name: palbase.env.debug=featureX
```

---

## Kanıt (planlama koşusu)

**Eleştirmen probları 8714d30'da yeniden koşuldu; hepsi kırmızı:**
- Windows: `"main." was accepted`.
- Loopback main: `directory "palbase/environments/main" exists`, `/android` ve `/web`.
- Clone: `"todoapp/evil\x1b]0;owned\a is Failed, …" should not contain "\x1b"`.
- UTF-8: `csi\x9b31m could not be written (mkdir …: illegal byte sequence)`, ham basıldı.
- NFC/NFD: `actual : []string{"café", "main"}` ve `wrote palbase/environments/café/android-config.json` iki kez.
- Monorepo: `…/apps/android is inside the checkout linked at …`.

**Scratch zinciri:** `plan/cli`'de `20e5d7e` üstüne yeniden kuruldu; 26 commit, `e6b2fbf` … `5a8310b`. Eski zincir `draft-cli-8714d30` etiketinde. Değişen her görev önce testleriyle yazıldı, kırmızısı görüldü, sonra uygulandı ve yeşili görüldü. Değişmeyen görevler cherry-pick edildi ve her birinin Adım 4 yeşili yeniden koşuldu. Plan metnindeki 194 `go` bloğu görevin commit'i ya da ebeveyniyle karşılaştırıldı: 0 uyumsuz.

**Mutasyon ölçümleri** (kuralı kaldırınca test düşer):
- T001 Windows switch'i olmadan: `"conout$" was accepted`, `actual : []string{"main", "main."}`.
- T002 `listed` olmadan: `the sweep deleted the committed files…`.
- T014 `removeThisMachinesOldMain` olmadan: `/android` ve `/web` `directory … exists`.
- T016'da foldedOnly EqualFold'la: `"[]" should have 1 item(s), but has 0`.
- T018'de (c) olmadan: `two cures for one missing contract`.
- T019 Gradle kökü kuralı olmadan monorepo reddediliyor; sıra ters çevrilince `An error is expected but got nil.`.
- T021 keyless bloğu olmadan metin iddiası düşüyor.

**Kapılar:**

| Nokta | gofmt | vet + e2e vet | lint | `go test ./...` | race |
|---|---|---|---|---|---|
| `28b710d` (T008) | boş | temiz | `0 issues.` | 24 `ok` + B16 (`internal/backend 111.176s`) | envname/env/project `ok` |
| `ed1b247` (T022) | boş | temiz | `0 issues.` | 24 `ok` + B16 (`112.014s`) | cmd/palbase `10.596s`, envname `1.375s` |
| `5a8310b` (HEAD) | boş | temiz | `0 issues.` | 24 `ok` + B16 (`106.783s`) | `./...` 24 `ok` + B16 (`107.734s`), `WARNING: DATA RACE` 0 |

B16, scratch'te `-run` ile tek tek sayıldı: 13 SKIP, 1 PASS (`TestBuildCheckNodeSuite`), 2 FAIL.

**Duyarlı APFS:** hdiutil sparse imajında şu testler PASS: `TestTheSweep*` (`TestTheSweepRemovesTheOldSpellingBesideTheNewOne` burada koşar), `TestALinkKeepsAnEnvironmentRenamedByCaseAlone`, `TestAPublishCarriesADirectoryRenamedByCaseAlone`, NFC/NFD link testi, loopback-main testi ve `TestDoctorNamesEnvironmentDirectoriesThatDifferOnlyInCase` (`ok … 0.494s`).

**Uçtan uca:** HEAD'de derlenmiş `palbase doctor`, taslaktaki Android bölümünü birebir bastı (6 satır, dosyada).

**Taban kanıtı:** 2.3 yalnız global anahtarı okuyor — `palbackend-android-src` `v2.3.0` `PalbaseCodegenPlugin.kt:40` `ENVIRONMENT_PROPERTY = "palbase.env"`, `DEFAULT_ENVIRONMENT = "local"`, `providers.gradleProperty(...).orElse(...)`.

**Token ölçümü:** 24.449 karakterlik bir plan parçası ≈ 11,1K token (sayaç farkı).

**Dosyalar:**
- Tam görev listesi: `reports/drafts/revise-cli.tasks.md (/tmp'den kurtarılan bayt-birebir kopya)`, sha256 `d5bae91cb56c3a55d12647c8ec4cd178926931dc3d769d20eca62b8ee273485b`.
- Scratch klonu: `…/scratchpad/plan/cli`, HEAD `5a8310b`, temiz.

Gerçek depolara hiçbir şey yazılmadı; push yok.

**D-025 ek koşusu (T019, 2026-09-26).** Zincir: T001–T018 aynı; T019 `3a15927`, T020 `a65b51c`, T021 `25703d1`, T022 `4473781`, T023 `af7b4b5`, T024 `e451488`, T025 `370ee36`, T026 `327a941` (T020–T026 yamaları öncekilerle aynı, `git patch-id --stable` 7/7). T019'un 6 Go bloğu betikle `e6f8ac6` üzerine uygulandı; üç dosya `3a15927` ile birebir. Mutasyon: sekiz kural, sekiz bekçi. Guard kapatılınca üç ret testi `An error is expected but got nil.` ile düşer.

| Commit | Statik | Lint | `go test` | Race |
|---|---|---|---|---|
| T022 `4473781` (Dalga 1 kapısı) | build, gofmt, vet, e2e vet temiz | `0 issues.` | 24 ok; backend yalnız B16 (112.226s) | `cmd/palbase`, `envname` ok |
| HEAD `327a941` | temiz | `0 issues.` | 24 ok; backend yalnız B16 (108.149s) | 24 ok, backend yalnız B16, `DATA RACE` 0 |

Açık takipler (bu planın dışında): T023'ün Gradle bloğu RN kökünde dosya yollarını `android/` önekisiz basıyor (T024'ün doctor'ı önekli okuyor); RN kökünde yapılan link, altındaki eski bir `android/palbase`'i görmüyor.
