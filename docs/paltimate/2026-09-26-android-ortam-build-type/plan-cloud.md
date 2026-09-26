# Ortam slug'ı (palbase-cloud) — Uygulama Planı

> **Ajan çalışanlar için:** Görev görev yürüt (superpowers:subagent-driven-development ya da superpowers:executing-plans). Adımlar `- [ ]` checkbox. Görev başlıkları makine-okur meta veri taşır (`deps | files | satisfies`).

**Goal:** Her Palbase ortamı değişmez, proje içinde harf büyüklüğünden bağımsız benzersiz bir slug taşır; ilk ortam her projede `main`; CLI, panel ve bütün uçlar aynı slug'ı döner — böylece bir ortam adı ne checkout dışına yazabilir ne de iki ortam aynı dizine düşebilir.

**Architecture:** `cloud_projects`'e `slug` kolonu (genişlet → doldur → daralt): önce nullable kolon + dilbilgisi CHECK'i + `(product_id, lower(slug))` tekilliği; yaratma yolları slug'ı yazar ya da deterministik türetir; bir operatör fiili mevcut satırları doldurup göç raporu yazar; üretimde `n = 0` kapısından sonra NOT NULL. CLI listesinin `name` alanı slug'ı taşır (eski CLI'lar da güvenli ad alır), görünen ad `display_name`'de.

**Tech Stack:** TypeScript (Palbase backend: `@palbase/backend`), bun test, PostgreSQL 16 (PG testleri), Studio (Next.js, vitest), `db/public.ts` deploy'da diff'lenir (göç dosyası yok).

**Spec:** `./spec.md` (FR-101…FR-107) · **Kararlar:** `./decisions.md` (D-008, D-013, D-015, D-016, D-021) · **Kanıt:** `./reports/verification-2026-09-25.md` (A1, A2, A3, yeni bulgular)

**Sıra:** Bu plan `plan-cli.md`'den SONRA, `plan-plugin.md`'den ÖNCE yürütülür (spec "Kapsam ve sıra").

## Global Constraints

- **Hedef:** `palbase-cloud` `origin/main` `f92bc1e02`. Yerel `main` (`6bbfcdf54`) 1685 commit geride ve ileri sarılabilir. İlk iş `git pull --ff-only` (D-013, T001 Adım 0).
- **Sunucu testleri:** `(cd cloud/platform/server && npm test -- <dosyalar>)`. Zincir `npm test` → `scripts/test.sh` → `bun test`. Ölçümler yerel bun 1.4.2 ile yapıldı; CI bun 1.3.9 (`oven-sh/setup-bun`) ve node 24 kullanıyor.
- **Sunucu tip kapısı:** `(cd cloud/platform/server && npm run typecheck)`, yani `tsc --noEmit`, exit 0.
- **Studio kapıları:** `(cd cloud/platform/studio && npx vitest run <yol>)`, `npx tsc --noEmit` exit 0, `npx eslint <dosyalar>` çıktısız, `npm run lint:design` → `Studio workspace: color and geometry token checks passed.`
- **PG testleri (`*.pg.test.ts`):**
  - `PBC_FLAGS_TEST_DATABASE_URL` tek kullanımlık bir `flags_audit_test` veritabanını adlandırmalı; fikstür başka adı reddeder.
  - Bu planın ölçümleri `@embedded-postgres/darwin-arm64@16.14.0-beta.17` (PostgreSQL 16.14) ile `postgresql://postgres@localhost:55471/flags_audit_test` üzerinde yapıldı. Tarif T001 Adım 0b'de.
  - CI'da `postgres:16-alpine` servisi ve `postgresql://postgres:postgres@localhost:5432/flags_audit_test` kullanılır.
- **CI yalnız listeleri koşar:** `.github/workflows/cloud-server-typecheck.yml`, `typecheck` ve `flag-publication-postgres` işlerindeki `suites=(…)` dizilerini koşar. Her yeni test dosyası bir listeye eklenir; listede olmayan test CI'da hiç koşmaz.
- **Taban (`f92bc1e02`, PG değişkeniyle):**
  - Sunucu: `2223 pass` / `10 fail` / `6 errors`, `Ran 2233 tests across 170 files.` Kırıkların tümü Docker isteyen 8 dosya: `cli.projects.pg`, `panel.project-settings.pg`, `panel.deployment-activity.pg`, `panel.sdk-pins.pg`, `usage/compute|ingest|realtime|stock`.
  - Studio: `Test Files  5 failed | 386 passed | 2 skipped (393)`, `Tests  6 failed | 3747 passed | 11 skipped (3764)`. Kırıkların tümü `src/content/docs/` altında ve `sdk/cli` / sdk şablon checkout'u ister: `controller-examples`, `link-model`, `retired-commands`, `retired-surfaces`, `template-sync`.
- **Son durum (`plan-r2` `2ce2c3cbb`):**
  - Sunucu: `2292 pass` / `10 fail` / `6 errors`, `Ran 2302 tests across 179 files.` Kırıklar tabandaki aynı 8 Docker dosyası.
  - Studio: `Tests  6 failed | 3753 passed | 11 skipped (3770)`. Kırıklar tabandaki aynı 5 dosya.
  - CI listeleri: `typecheck` işi `835 pass` / `Ran 835 tests across 51 files.`; `flag-publication-postgres` işi `355 pass` / `Ran 355 tests across 26 files.`
- **Göç dosyası yok.** `db/public.ts` deploy'da canlı veritabanına diff'lenip uygulanır (`AGENTS.md`). NOT NULL yalnız genişlet → doldur → daralt sırasıyla gelir: T002 → T007/T012 → T013.
- **`palbase/palbase-env.d.ts`:** elle, üreticinin biçiminde düzenlenir. `palbase build` (palbase 0.71.2) slug kolonunu ve rapor tablosunu birebir aynı üretir. Tek fark ilgisiz `cage_toured_at` kaymasıdır; `git checkout --` ile geri alınır.
- **Dilbilgisi:** `ENVIRONMENT_SLUG_PATTERN = "^[A-Za-z][A-Za-z0-9-]{0,38}$"`, `db/public.ts`'te (D-008, kullanıcı 2026-09-26'da onayladı). Test-kilitli iki kopyası var: Studio'da (T009) ve CLI'da (FR-007, plan-cli).
- **Dil:** kullanıcıya basılan her dize, kod ve kod yorumları İngilizce (NFR-003). Plan düzyazısı ve commit mesajları Türkçe. Yalnız mevcut Türkçe yorum metinlerindeki eskimiş başvurular silindi.
- **Yazıcı ve commit kuralları:** tek yazıcı, `main`'de; worktree ve yan branch yok (NFR-004). Commit'ler pathspec ile: `git add <yollar> && git commit`. Her commit `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` ile biter.
- **Deploy sırası:**
  1. Sunucu T001–T007.
  2. T012: operatör fiili, ve üretimde `SELECT count(*)::int AS n FROM cloud_projects WHERE slug IS NULL` = 0.
  3. Studio T008–T011. T010 yalnız T005 canlıyken; T008 geri doldurulmamış satırda ref gösterirdi.
  4. T013 (sözleşme).
  5. T014–T016.

  T013 ve sonrası T012 geçmeden commit'lenmez ve push'lanmaz.
- **Go/Android:** bu planda Go ya da Android kodu yok. Makinede Go 1.27.1 `/opt/homebrew/bin/go` var; `go.mod` `go 1.26.6` pinliyor. golangci-lint v2.12.2 kurulu değil ve bu plan için gerekmiyor. AGP 8.10.1+ / Gradle 8.11.1+ / JDK 17+ (FR-212) plan-plugin'in konusu.
- **Yasaklar:** bulut çağrısı yok, `palbase login` yok, yayın yok. Operatör fiili (T012) bu planın yazımında koşturulmadı.

## Review Focus

- **Yarım proje kurtarma.** Ürün var, ilk ortam düşmüş, kişi "Create environment" formunda "Production" yazıyor. Beklenen: form slug göndermez, sunucu `main` yazar; 400 yok. Test: **T010** "sends no slug the person did not type — the server derives the same one, and writes main when this is the project's first environment".
- **Aynı slug için yarışan iki yaratma.** İki ekip arkadaşı, ya da zaman aşımından sonra CLI tekrarı. Beklenen: kaybeden istek `409 this project already has an environment with slug "staging" (slugs are unique regardless of case)` alır; 500 dönmez, "half-created resource" oluşmaz. Test: **T005** `cloud-lifecycle.race.test.ts` (gerçek `SagaService.runSaga`).
- **`main`'i silmek (D-021).** Projede `main` ve `staging` var; biri `main`'i CLI'dan (`palbase env delete main`, `palbase project delete <ref>`), panelin tehlike bölgesinden ya da doğrudan API'den siliyor. Beklenen: `409 main_deleted_last` + `main is this project's default environment and cannot be deleted while other environments exist — delete the others first`. Kiracıya dokunulmaz: ret satır kilidinden sonra, `unpublish-placement`'tan önce gelir. CLI ve panel cümleyi aynen gösterir. `main` projenin son ortamıysa silinir; proje boş kalır ve bir sonraki ortamı `main` olur. Aynı projede bir yaratma sağlanırken gelen `main` silme onu bekler ve yeni ortamı sayar (409). Testler:
  - **T017**: `cloud-lifecycle.delete-main.test.ts`; `main-delete-lock.pg.test.ts` (kilit çifti gerçek PostgreSQL'de); `teardown.test.ts` "a refused delete stops before anything is destroyed"; tehlike bölgesi `page.test.tsx` "a refused delete shows the server's sentence as it came".
  - **T005**: "a project whose environments were all deleted gets `main` on its next environment" ve "an environment that is not the project's first is never handed `main`".

  Bakılacak: çok ortamlı bir projeyi koddan silen bir çağıran (palcore) `main`'i EN SON silmeli. `verify-plane.py`'nin `reap`'i her 409'u durumuna bakarak tekrar dener; hesabındaki projeler tek ortamlı olduğu için bugün etkilenmez.
- **Geri doldurma okuduktan sonra slug'sız eklenen satır.** Deploy sırasında hâlâ eski kodu koşan bir örnek yazabilir. Beklenen: `remaining` bu satırı sayar (1); `remaining: 0` NOT NULL'un gerçekten güvenli olduğu anlamına gelir. Test: **T007** `slug-backfill.pg.test.ts` "an environment written without a slug while the run was planning is counted".
- **Gerçek rotalarla yeniden adlandırma.** Panelde ortam, sonra ürün yeniden adlandırılıyor; ardından CLI listesi ve bağlar okunuyor (doğrulama A2b). Beklenen: hiçbir CLI adı kımıldamaz (`main`/`staging`/`qa`); `display_name` yeni adı izler. Test: **T016** "renaming the environment and the project leaves every CLI name where it was".

## Fidelity Audit

- **Şartnamede dayanağı olmayan öğe: none.** Her ek bir FR'ye bağlı:
  - T007'nin tek-yazıcı AST kapısı → FR-101 "değişmez" ve FR-104.
  - T012 kapı görevi → FR-105 ve genişlet/daralt sırası.
  - T016'nın dağıtım akışı etiketi ve Studio belge paragrafı → FR-106 "her uç" ve `is_production`.
  - `Database.$attempt` → FR-103 "4xx ile reddedilsin".

  Kapsamın biraz dışına taşan tek şey: `$attempt` sarmalı aynı yazıcıyı kullanan `palbase project create` yolunu da kapsıyor. Davranış değişmedi; o yol yarışamaz.
- **Bileşen yüzeyinden farklı imza:**
  - `ENVIRONMENT_SLUG_TAKEN_SQL` (taslak T004) kaldırıldı. Yerini `ENVIRONMENT_SLUGS_OF_PROJECT_SQL` ve `EnvironmentSlugService.held(rows): Map<string, string>` aldı.
  - `environmentSlugFor(productId, first, name, requested): Promise<string>` → `environmentSlugFor(held, name, requested): string`.
  - `EnvironmentSlugBackfillResult.remaining`'in ANLAMI değişti: "planlanıp yazılamayan" iken "yazımlardan sonra hâlâ NULL olan"a döndü. Yeni `ENVIRONMENTS_WITHOUT_SLUG_SQL` eklendi.
  - `DeploymentActivityService` kurucusu `EnvironmentSlugService` aldı. `DeploymentActivityRow.environment_name: string | null` oldu ve `environment_slug: string` eklendi.
  - Görev numaraları değişti (yeni ← eski): T004←T006, T005←T004, T006←T005, T008←T012, T009←T013, T010←T014, T011←T015, T012 yeni, T013←T008, T014←T009, T015←T010, T016←T011.
- **Planlama sırasında yapılan şartname değişiklikleri:**
  - **A-1 (KARAR: D-021 — kullanıcı 2026-09-26'da seçenek 2'yi onayladı: "2 olsun, main silinemesin").** D-008 `main`'i ilk ortama ayırıyor ama silmeyi söylemiyordu. Silme sagası satırı siliyor; `main`'i silinen proje `main`'siz kalıyor ve `palbase.env.release=main` build'leri düşüyordu.
    - Karar: projede başka bir ortam varken `main`'i silme isteği, hangi yoldan gelirse gelsin (CLI, panel, API), sunucuda reddedilir: 409, kural ve çıkış yolu cümlede. `main` ancak projenin son ortamıysa silinir; ortamsız kalan projenin bir sonraki ortamı `main` olur.
    - Plan: **T017** (ret teardown'da, yıkımdan önce; yaratma `FOR KEY SHARE` / `main` silme `FOR UPDATE` kilit çifti) ve **T005** (ilk ortam = `existing === 0`).
    - Reddedilen (seçenek 1, planın önceki önerisi): `main` silinebilir ve `held()`'de `main` yoksa sonraki ortam `main` olur. Release build'i arada kırılırdı; slug vermeden ad yazan kişinin ortamı da beklenmedik biçimde `main` olurdu. Bu kural artık T005'in bir testini düşürüyor (`46 pass` / `1 fail`).
    - İmza: `environmentSlugFor(held, name, requested): string` → `environmentSlugFor(first, held, name, requested): string`. "Bileşen yüzeyinden farklı imza" satırı buna göre güncellenmeli.
    - Şartname: silmeyi anlatan bir FR yok; T017'nin dayanağı bir FR değil, D-021 (kullanıcı kararı). Yukarıdaki "Şartnamede dayanağı olmayan öğe: none" maddesine "T017 → D-021" eklenmeli. İstenirse şartnameye FR-108 olarak girer.
    - Bilinen sınır: panel `main`'de Sil düğmesini hâlâ gösteriyor; ret, sunucunun cümlesiyle bir toast olarak görünüyor (T017 Studio testi). Düğmeyi saklamak T016'dan sonra küçük bir takip işi (`is_production` orada yalnız `main`).
  - **A-2.** FR-107'nin "form türetilen slug'ı düzenlenebilir göstersin" maddesi korunuyor, ama gönderim kuralı daraldı: yalnız kişinin YAZDIĞI slug gönderiliyor (T010). Sebep: formun ortam listesi erişime göre süzülü; "ilk ortam" bilinemiyor. Tek sapma: sunucunun `main` yazacağı ilk ortamda form türetilmiş adı gösterir, yardım metni "A project's first environment is always main" der.
  - **A-3.** D-008 "tek sabit" diyor. Sunucu içinde tek; Studio (T009) ve CLI (FR-007) test-kilitli kopya taşıyor. `decisions.md` D-008 "Etki" satırına "Studio oluşturma formu (FR-107), test-kilitli kopya" eklenmeli. Gerçek depoya yazma yasak olduğu için bu işi lead yapar. CLI kopyasının kilidi (yayımlanmış OpenAPI `pattern:`'a karşı) plan-cli'ye önerilir.
  - **A-4.** D-008/D-014, computed task'ta "PENDING" yazılıydı. `decisions.md` ise ikisini de "kullanıcı onayladı (2026-09-26)" diye işaretliyor, ve kullanıcının mesajı "evet ikisine de". Plan bunu uygular. D-014 (FR-020) CLI işidir, bu planda görev yok.
  - **A-5.** Studio belgesi `introduction.md:130` yeniden yazıldı. Eski "her ortam `is_production: true`" ve "ad anlam taşımaz" ifadeleri FR-106 ile çelişiyordu.
  - **Değişmeyen:** `kind: "production"` (panel) / `kind: "primary"` (CLI) sabitleri. D-036 tasarımda ve bu plan kind'a dokunmuyor.

---

## Görevler

> **Plan notu.** Komutlar depo kökünden koşar; her koşu kendi alt kabuğunda: `(cd cloud/platform/server && …)`, `(cd cloud/platform/studio && …)`. Böylece Adım 5'teki `git add` depo kökünden yol alır. `*.pg.test.ts` dosyaları `PBC_FLAGS_TEST_DATABASE_URL`'i okur; değişken T001 Adım 0b'de bir kez `export` edilir. Görev sırası commit sırasıdır. Deploy sırası farklıdır: Global Constraints → "Deploy sırası". Bu plan `f92bc1e02` üzerinde, kazıma kopyasının `plan-r2` dalında görev görev koşturuldu. Görev başına commit'ler: T001 `54cc8a43a` … T016 `2ce2c3cbb`.

### T001: Slug dilbilgisi tek sabitte — ayrılmış adlar ve deterministik türetme
<!-- deps: [] | files: [cloud/platform/server/db/public.ts, cloud/platform/server/modules/shared/environment-slug.ts, cloud/platform/server/modules/shared/environment-slug.test.ts, cloud/platform/server/modules/shared/shared.module.ts, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-101, FR-103] -->

**Interfaces:**
- Consumes: —
- Produces:
  - `ENVIRONMENT_SLUG_PATTERN = "^[A-Za-z][A-Za-z0-9-]{0,38}$"` (`db/public.ts`). Dilbilgisinin sunucudaki TEK yazımı (D-008, 2026-09-26'da onaylandı).
  - `EnvironmentSlug` (zod, `modules/shared/environment-slug.ts`).
  - `FIRST_ENVIRONMENT_SLUG = "main"`, `LOCAL_ENVIRONMENT_SLUG = "local"`.
  - `EnvironmentSlugService.problemWith(slug: string, first: boolean): string | null`.
  - `EnvironmentSlugService.derive(name: string): string` (`""` = türetilemedi).
  - `SharedModule`, `EnvironmentSlugService`'i dışa verir.

Dilbilgisi `modules/`'te değil, `db/public.ts`'te durur. Sebebi: şema yığına bu dosyanın KAYNAK METNİ olarak gider (`palbase-cli` `SchemaSourcesBody`), yani bu dosya bizden hiçbir şey import edemez. Yön tersine işler: modüller onu import eder. Emsali `sequence/slot.ts` ↔ `SLOT_SPACE`.

Türetme bilinçli olarak basit tutuldu:
- Geçerli bir ad bayt bayt korunur (`palbase env create featureX` → `featureX`).
- Geri kalan ad aksansızlaştırılır, her geçersiz karakter dizisi tek bir tire olur.
- Büyük/küçük harf korunur.

- [ ] **Adım 0: Güncel main** — Yerel `palbase-cloud` kopyası `origin/main`'in 1685 commit gerisinde ve eski `v2-cloud/` düzeninde (D-013). Ölçüm: HEAD `6bbfcdf54`, `git rev-list --count HEAD..origin/main` → `1685`, `git merge-base --is-ancestor HEAD origin/main` → ileri sarılabilir. Run: `git pull --ff-only` · Beklenen: HEAD `f92bc1e02` ya da ötesi, ve `cloud/platform/server/` dizini var. Bu plan `f92bc1e02` üzerinde koşturuldu.
- [ ] **Adım 0b: Tek kullanımlık Postgres** — `*.pg.test.ts` dosyaları gerçek bir PostgreSQL 16 ister. Fikstür yalnız `flags_audit_test` adlı bir veritabanına yazar (kendi kilidi). Bu planın ölçümleri `@embedded-postgres/darwin-arm64@16.14.0-beta.17` ikilileriyle yapıldı (PostgreSQL 16.14):
```bash
npm i --prefix "$PGDIR" @embedded-postgres/darwin-arm64@16.14.0-beta.17
B="$PGDIR/node_modules/@embedded-postgres/darwin-arm64/native/bin"
"$B/initdb" -D "$PGDIR/data" -U postgres --auth=trust
"$B/pg_ctl" -D "$PGDIR/data" -l "$PGDIR/log" -o "-p 55471 -c unix_socket_directories= -c listen_addresses=localhost" start
psql -h localhost -p 55471 -U postgres -c "CREATE DATABASE flags_audit_test"
export PBC_FLAGS_TEST_DATABASE_URL=postgresql://postgres@localhost:55471/flags_audit_test
```
CI'daki eşdeğeri `postgres:16-alpine` servisi ve `PBC_FLAGS_TEST_DATABASE_URL=postgresql://postgres:postgres@localhost:5432/flags_audit_test` (`flag-publication-postgres` işi). Docker yolu bu makinede ölçülmedi, çünkü Docker daemon kapalıydı. Bitince: `"$B/pg_ctl" -D "$PGDIR/data" stop -m fast`.
- [ ] **Adım 1: Kırmızı testi yaz** — `cloud/platform/server/modules/shared/environment-slug.test.ts` (yeni dosya):
```ts
import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import { isolated } from "@palbase/backend/test";
import { ENVIRONMENT_SLUG_PATTERN } from "../../db/public.ts";
import { EnvironmentSlug, EnvironmentSlugService } from "./environment-slug.ts";

/**
 * THE SLUG IS A DIRECTORY NAME ON EVERY TEAMMATE'S DISK (D-008, FR-101/FR-103).
 *
 * `palbase link` writes `palbase/environments/<slug>/`, Android selects it by
 * build type name and `--env` accepts it. So every assertion here is about a
 * name that must — or must never — reach a path.
 */
const slugs = isolated().get(EnvironmentSlugService);

describe("slug grammar (D-008)", () => {
  it("is the approved camelCase grammar, spelled once — in the schema file", () => {
    expect(ENVIRONMENT_SLUG_PATTERN).toBe("^[A-Za-z][A-Za-z0-9-]{0,38}$");
    // The service derives, it does not repeat: the schema file is the only
    // place the text may appear (it ships to the stack on its own).
    const own = readFileSync(new URL("./environment-slug.ts", import.meta.url), "utf8");
    expect(own).not.toContain("[A-Za-z0-9-]{0,38}");
  });

  it("accepts the names people actually use", () => {
    for (const ok of ["main", "staging", "featureX", "feature-profile-update", "a", "a".repeat(39)]) {
      expect(slugs.problemWith(ok, ok === "main"), ok).toBeNull();
      expect(EnvironmentSlug.safeParse(ok).success, ok).toBe(true);
    }
  });

  it("refuses every name that is not one clean path segment, and says the rule", () => {
    const bad = [
      "", "1abc", "-a", "feature/login", "feature\\login", "..", ".hidden", "a b", "a_b", "a.b",
      "a".repeat(40), "a\u0000b", "\u001b[31mred", "Ünal",
    ];
    for (const slug of bad) {
      expect(slugs.problemWith(slug, false), JSON.stringify(slug)).toBe(
        `slug ${JSON.stringify(slug)} is not valid: a slug starts with a letter and continues with up to 38 letters, digits or hyphens (^[A-Za-z][A-Za-z0-9-]{0,38}$)`,
      );
      expect(EnvironmentSlug.safeParse(slug).success, JSON.stringify(slug)).toBe(false);
    }
  });
});

describe("reserved slugs (D-008, D-015)", () => {
  it("`local` is never a cloud environment, in any case", () => {
    for (const slug of ["local", "Local", "LOCAL"]) {
      expect(slugs.problemWith(slug, false)).toBe(
        `slug "${slug}" is reserved: local/ belongs to the stack on each developer's own machine`,
      );
    }
  });

  it("`main` belongs to the first environment only, in any case", () => {
    for (const slug of ["main", "Main", "MAIN"]) {
      expect(slugs.problemWith(slug, false)).toBe(
        `slug "${slug}" is reserved for the project's first environment`,
      );
    }
  });

  it("the first environment is exactly `main` — its display name is free, its slug is not", () => {
    expect(slugs.problemWith("main", true)).toBeNull();
    for (const slug of ["production", "Main"]) {
      expect(slugs.problemWith(slug, true)).toBe(
        `the first environment's slug is always "main"; give it any display name instead`,
      );
    }
  });
});

describe("deriving a slug from a display name (FR-103)", () => {
  it("a name that is already a valid slug is kept byte for byte", () => {
    // `palbase env create featureX` sends only a name; if derivation touched a
    // valid name, the directory would not be the name the developer typed.
    for (const name of ["featureX", "feature-profile-update", "Staging", "a--b"]) {
      expect(slugs.derive(name)).toBe(name);
    }
  });

  it("turns a display name into one clean segment, deterministically", () => {
    const cases: Array<[string, string]> = [
      ["  staging  ", "staging"],
      ["Feature X", "Feature-X"],
      ["feature/login", "feature-login"],
      ["Geliştirme Ortamı", "Gelistirme-Ortami"],
      ["İstanbul Şube", "Istanbul-Sube"],
      ["2nd env", "nd-env"],
      ["QA (EU) / 2", "QA-EU-2"],
      ["x".repeat(38) + " tail", "x".repeat(38)],
    ];
    for (const [name, slug] of cases) {
      expect(slugs.derive(name), name).toBe(slug);
      expect(slugs.problemWith(slug, false), slug).toBeNull();
    }
  });

  it("answers empty when nothing usable is left — the caller decides, nothing is invented", () => {
    for (const name of ["", "   ", "🚀", "123", "---"]) {
      expect(slugs.derive(name), JSON.stringify(name)).toBe("");
    }
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.test.ts)` · Beklenen: **FAIL**, çıktıda `error: Cannot find module './environment-slug.ts' from '…/modules/shared/environment-slug.test.ts'` ve `0 pass` / `1 fail` / `1 error`.
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/server/db/public.ts`: `const phaseCheck = \`phase IN (${PROJECT_PHASES.map((p) => \`'${p}'\`).join(", ")})\`;` satırının ALTINA ekle:
```ts

/**
 * THE ENVIRONMENT SLUG GRAMMAR — ONE CONSTANT (D-008, approved 2026-09-26).
 *
 * The slug is the environment's directory on every teammate's disk
 * (`palbase/environments/<slug>/`), its Android build type
 * (`create("featureX")`) and the name `--env` accepts. A letter first, then
 * letters, digits or hyphens, 39 characters at most: no `/`, `\`, `.`, space
 * or control character can reach a path, and camelCase stays legal because it
 * is AGP's build type convention.
 *
 * IT LIVES HERE, NOT IN `modules/`: the schema reaches the stack as the SOURCE
 * TEXT of this file alone (`palbase-cli` `SchemaSourcesBody`), so this file
 * cannot import anything of ours — the modules import it instead, as
 * `sequence/slot.ts` does with `SLOT_SPACE`. The CLI keeps the same text for
 * `palbase env create` (FR-007); change both or neither.
 */
export const ENVIRONMENT_SLUG_PATTERN = "^[A-Za-z][A-Za-z0-9-]{0,38}$";
```
(b) `cloud/platform/server/modules/shared/environment-slug.ts` (yeni dosya):
```ts
import { Injectable, z } from "@palbase/backend";
import { ENVIRONMENT_SLUG_PATTERN } from "../../db/public";

// THE GRAMMAR IS NOT SPELLED HERE. It lives beside the table it constrains
// (`db/public.ts`, D-008) and everything below is derived from it.
const ENVIRONMENT_SLUG = new RegExp(ENVIRONMENT_SLUG_PATTERN);

/** Every project's first environment is `main`, whatever it is called on screen (D-015). */
export const FIRST_ENVIRONMENT_SLUG = "main";
/** `local/` is the stack on each developer's own machine — never a cloud environment. */
export const LOCAL_ENVIRONMENT_SLUG = "local";

const SLUG_RULE =
  `a slug starts with a letter and continues with up to 38 letters, digits or hyphens (${ENVIRONMENT_SLUG_PATTERN})`;

/** A slug a caller sends. Reserved names need the project's state and are checked by the service. */
export const EnvironmentSlug = z.string().regex(ENVIRONMENT_SLUG, SLUG_RULE);

/**
 * THE ENVIRONMENT SLUG'S RULES — one place, used by every path that names an
 * environment: creation (FR-102/FR-103) and the backfill (FR-105).
 *
 * In `shared` because the environments, fleet and cli modules all need it and
 * fleet cannot import environments (environments imports fleet). Pure: no
 * database, no clock.
 */
@Injectable()
export class EnvironmentSlugService {
  /**
   * Why `slug` cannot name this environment, or `null` when it can.
   *
   * Case-insensitive on the reserved names for the same reason uniqueness is:
   * `Local/` and `local/` are one directory on APFS.
   */
  problemWith(slug: string, first: boolean): string | null {
    if (!ENVIRONMENT_SLUG.test(slug)) return `slug ${JSON.stringify(slug)} is not valid: ${SLUG_RULE}`;
    const folded = slug.toLowerCase();
    if (folded === LOCAL_ENVIRONMENT_SLUG) {
      return `slug ${JSON.stringify(slug)} is reserved: local/ belongs to the stack on each developer's own machine`;
    }
    if (first) {
      return slug === FIRST_ENVIRONMENT_SLUG
        ? null
        : `the first environment's slug is always "${FIRST_ENVIRONMENT_SLUG}"; give it any display name instead`;
    }
    if (folded === FIRST_ENVIRONMENT_SLUG) {
      return `slug ${JSON.stringify(slug)} is reserved for the project's first environment`;
    }
    return null;
  }

  /**
   * A slug from a display name — the same answer for the same name, every time.
   *
   * A name that already IS a valid slug comes back byte for byte: the CLI sends
   * only a name (`palbase env create featureX`) and touching it would give the
   * developer a directory they did not type. Otherwise: fold accents away
   * (NFKD; `ı` by hand, NFKD leaves the dotless i alone), turn every run of
   * anything else into one hyphen, drop what precedes the first letter, cut to
   * 39 and drop a trailing hyphen. `""` means nothing usable was left — the
   * caller decides what that means; this method invents nothing.
   */
  derive(name: string): string {
    const trimmed = name.trim();
    if (ENVIRONMENT_SLUG.test(trimmed)) return trimmed;
    const ascii = trimmed.replaceAll("ı", "i").normalize("NFKD").replace(/\p{M}+/gu, "");
    return ascii
      .replace(/[^A-Za-z0-9]+/g, "-")
      .replace(/^[^A-Za-z]+/, "")
      .slice(0, 39)
      .replace(/-+$/, "");
  }
}
```
(c) `cloud/platform/server/modules/shared/shared.module.ts`: `import { SingleflightService } from "./singleflight";` satırının ÜSTÜNE `import { EnvironmentSlugService } from "./environment-slug";` ekle. Sonra iki listeyi şununla değiştir:
```ts
  providers: [EnvironmentSlugService, SingleflightService, SinglewriterService, SleepStateService],
  exports: [EnvironmentSlugService, SingleflightService, SinglewriterService, SleepStateService],
```
(d) `.github/workflows/cloud-server-typecheck.yml`: `typecheck` işinin `suites=(…)` listesinde `modules/billing/plan.test.ts modules/billing/tier-history.test.ts modules/billing/plan-pitr.test.ts` satırının ALTINA ekle. Sunucu CI'ı yalnız bu listeleri koşuyor; listede olmayan bir test CI'da hiç koşmaz.
```yaml
            # Ortam slug'ı (FR-101–FR-105): dilbilgisi ve türetme, yeniden adlandırma, geri doldurma
            # planı. Aynı gerekçe: CI'da koşmayan kapı testi kapı değildir.
            modules/shared/environment-slug.test.ts
```
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.test.ts && npm run typecheck)` · Beklenen: `9 pass` / `0 fail` / `Ran 9 tests across 1 file.`, ve `tsc --noEmit` exit 0.
- [ ] **Adım 5: Commit** — `git add cloud/platform/server/db/public.ts cloud/platform/server/modules/shared/environment-slug.ts cloud/platform/server/modules/shared/environment-slug.test.ts cloud/platform/server/modules/shared/shared.module.ts .github/workflows/cloud-server-typecheck.yml && git commit -m "feat(environments): ortam slug dilbilgisi tek sabitte — ayrılmış adlar ve deterministik türetme (D-008)"`

---

### T002: `cloud_projects.slug` — dilbilgisi CHECK'i ve proje içinde harf büyüklüğünden bağımsız tekillik
<!-- deps: [T001] | files: [cloud/platform/server/db/public.ts, cloud/platform/server/db/schema.test.ts, cloud/platform/server/palbase/palbase-env.d.ts, cloud/platform/server/modules/shared/flags-postgres.ts, cloud/platform/server/modules/shared/environment-slug.pg.test.ts, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-101] -->

**Interfaces:**
- Consumes: `ENVIRONMENT_SLUG_PATTERN` (T001)
- Produces:
  - `cloud_projects.slug text NULL`. Bu genişletme adımıdır; kolon T013'e kadar nullable kalır.
  - CHECK `cloud_projects_slug` (`slug ~ '<ENVIRONMENT_SLUG_PATTERN>'`).
  - `ENVIRONMENT_SLUG_INDEX = "cloud_projects_product_slug_key"` (`CREATE UNIQUE INDEX … ON cloud_projects (product_id, lower(slug))`).
  - `flagPostgres(tables)` artık `CREATE [UNIQUE] INDEX <ad> ON <tablo> (` biçimindeki `raw()` indekslerini fikstür şemasına yönlendirerek basar.

Göç dosyası YOK. `db/public.ts` canlı veritabanına diff'lenip uygulanır (`AGENTS.md`).

Kolon bir deploy boyunca nullable kalır. Eski satırların slug'ı, T007'nin fiili koşana kadar yok. NOT NULL şimdi yazılsa deploy "column contains null values" ile düşerdi — bu depoda `last_error`/`done_at`/`edge_address` ile üç kez ödenmiş bir ders. Sıra: genişlet → doldur → daralt. Daraltma T013'te.

Tekil indeks `raw()` ile yazılır. `index(…).onExpression("product_id, lower(slug)")` ifadeyi paranteze sarıyor ve Postgres `((product_id, lower(slug)))`'i reddediyor (ölçüldü, PostgreSQL 16.14: `ERROR:  syntax error at or near ","`).

PG testleri depo fikstürü `flagPostgres` ile koşar. Tablo `toSchemaJSON`'dan basılır, elle yazılmaz.

- [ ] **Adım 1: Kırmızı testi yaz** — (a) `cloud/platform/server/db/schema.test.ts`: `import { PROJECT_PHASES } from "./public.ts";` satırını şununla değiştir:
```ts
import { toSchemaJSON } from "@palbase/backend";
import schema, { ENVIRONMENT_SLUG_PATTERN, PROJECT_PHASES } from "./public.ts";
```
ve dosyanın SONUNA ekle:
```ts

/**
 * THE SLUG IS DECLARED ONCE AND THE LEDGER ENFORCES IT (FR-101, D-008).
 *
 * Read from `toSchemaJSON` — the JSON the schema rail receives — not from the
 * source text: what matters is what the deploy will apply. The behaviour of
 * that text on a real Postgres is `modules/shared/environment-slug.pg.test.ts`.
 */
describe("the environment slug is spelled once and the ledger enforces it (FR-101)", () => {
  const projects = Object.values(toSchemaJSON([schema]).tables)
    .find((t) => t.schema === "public" && t.name === "cloud_projects");

  it("the CHECK is DERIVED from ENVIRONMENT_SLUG_PATTERN — the grammar is written once in this file", () => {
    expect(projects?.checks).toContainEqual({
      name: "cloud_projects_slug",
      expr: `slug ~ '${ENVIRONMENT_SLUG_PATTERN}'`,
    });
    expect(oku("./public.ts").split("[A-Za-z0-9-]{0,38}").length - 1, "the grammar is spelled a second time").toBe(1);
  });

  it("uniqueness is per project and case-insensitive", () => {
    expect(projects?.rawConstraints).toContainEqual({
      name: "cloud_projects_product_slug_key",
      up: "CREATE UNIQUE INDEX cloud_projects_product_slug_key ON cloud_projects (product_id, lower(slug))",
    });
  });
});
```
(b) `cloud/platform/server/modules/shared/environment-slug.pg.test.ts` (yeni dosya):
```ts
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "bun:test";
import { flagPostgres } from "./flags-postgres.ts";

/**
 * THE DEPLOYED SLUG CONSTRAINTS, ON A REAL POSTGRES (FR-101).
 *
 * `flagPostgres` renders `cloud_projects` from the JSON the schema rail
 * receives (`toSchemaJSON`) — the slug CHECK and the case-folding unique index
 * included — so this measures what production will enforce, not a retyped
 * copy. A fake database enforces no constraint and could not tell a case twin
 * from two projects.
 */
const fixture = flagPostgres(["cloud_projects"]);
beforeAll(fixture.initialize);
afterAll(fixture.close);
beforeEach(fixture.reset);

let slot = 0;
/** `"accepted"`, or Postgres's own refusal. */
async function insert(ref: string, productId: string, slug: string | null): Promise<string> {
  try {
    await fixture.query(
      `INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, name, slug)
       VALUES ($1, $2, 'cell-01', 'Running', $3, $4, $5)`,
      [ref, ++slot, productId, slug ?? ref, slug],
    );
    return "accepted";
  } catch (e) {
    return (e as Error).message;
  }
}

describe("slug constraints on a real PostgreSQL (FR-101)", () => {
  it("rows written before the backfill (slug NULL) do not collide with each other", async () => {
    expect(await insert("r1", "p1", null)).toBe("accepted");
    expect(await insert("r2", "p1", null)).toBe("accepted");
  });

  it("two slugs that differ only in case are one directory — the second is refused", async () => {
    expect(await insert("r1", "p1", "Staging")).toBe("accepted");
    expect(await insert("r2", "p1", "staging"))
      .toBe(`duplicate key value violates unique constraint "cloud_projects_product_slug_key"`);
  });

  it("another project may use the same slug", async () => {
    expect(await insert("r1", "p1", "staging")).toBe("accepted");
    expect(await insert("r2", "p2", "staging")).toBe("accepted");
  });

  it("a slug that is not one clean path segment never reaches the ledger", async () => {
    for (const slug of ["feature/login", "..", "a b", "1abc", "main\n", "a".repeat(40)]) {
      expect(await insert(`r-${slot}`, "p1", slug), JSON.stringify(slug))
        .toBe(`new row for relation "cloud_projects" violates check constraint "cloud_projects_slug"`);
    }
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run (Adım 0b'nin `PBC_FLAGS_TEST_DATABASE_URL`'i ile): `(cd cloud/platform/server && npm test -- db/schema.test.ts modules/shared/environment-slug.pg.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - `(fail) the environment slug is spelled once and the ledger enforces it (FR-101) > the CHECK is DERIVED from ENVIRONMENT_SLUG_PATTERN — the grammar is written once in this file`
  - `Received: "column "slug" of relation "cloud_projects" does not exist"`
  - `4 pass` / `6 fail` / `Ran 10 tests across 2 files.`
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/server/db/public.ts`: `export const ENVIRONMENT_SLUG_PATTERN = …;` satırının ALTINA ekle:
```ts

const slugCheck = `slug ~ '${ENVIRONMENT_SLUG_PATTERN}'`;

/**
 * Slugs are unique per project REGARDLESS OF CASE: `Staging` and `staging` are
 * one directory on APFS, and two environments in one directory means `link`
 * writes one while `push` deploys the other (verification A3, measured).
 */
export const ENVIRONMENT_SLUG_INDEX = "cloud_projects_product_slug_key";
```
`cloud_projects` tablosunda `name: text().nullable(),` satırının ALTINA ekle:
```ts
    // THE ENVIRONMENT'S SLUG (FR-101) — its directory, build type and `--env`
    // name. `name` above is what people read and may be renamed; this never
    // changes once written (FR-104). Grammar: `slugCheck`; uniqueness:
    // `ENVIRONMENT_SLUG_INDEX`.
    //
    // NULLABLE FOR ONE DEPLOY, deliberately: rows born before this column have
    // no slug until the operator backfill (FR-105) writes one, and a NOT NULL
    // column would fail the deploy with
    // "column contains null values" — the lesson paid three times above
    // (`last_error`, `done_at`, `edge_address`). Expand, backfill, then contract.
    slug: text().nullable(),
```
Aynı tabloda `checks: [{ name: "cloud_projects_phase", expr: phaseCheck }],` satırını şununla değiştir:
```ts
  checks: [{ name: "cloud_projects_phase", expr: phaseCheck }, { name: "cloud_projects_slug", expr: slugCheck }],
  // WHY `raw` AND NOT `index(…).unique().onExpression(…)`: the typed builder
  // takes EITHER columns OR one expression (`palbase` generator.go refuses
  // both) and wraps the expression in parentheses, so
  // `onExpression("product_id, lower(slug)")` would emit
  // `ON cloud_projects ((product_id, lower(slug)))` — measured on Postgres:
  // `syntax error at or near ","`. A NULL slug does not collide (rows before
  // the backfill), which is exactly what the expand step needs.
  raw: [raw(
    ENVIRONMENT_SLUG_INDEX,
    `CREATE UNIQUE INDEX ${ENVIRONMENT_SLUG_INDEX} ON cloud_projects (product_id, lower(slug))`,
  )],
```
(b) `cloud/platform/server/palbase/palbase-env.d.ts`. Bu dosya elle, üreticinin biçiminde düzenlenir. `palbase build` aynı satırları üretiyor; T016 Adım 4'te ölçüldü. Değişiklikler:
  - `cloud_projects` `row` bloğunda `name: Pg<string, "text"> | null;` satırının altına `slug: Pg<string, "text"> | null;`.
  - `insert` bloğunda `name?: Pg<string, "text"> | null;` satırının altına `slug?: Pg<string, "text"> | null;`.

(c) `cloud/platform/server/modules/shared/flags-postgres.ts`:
  - `ddl` içindeki `if (definition.rawConstraints?.length || definition.appendOnly) throw new Error("Unsupported fixture declaration");` satırını `if (definition.appendOnly) throw new Error("Unsupported fixture declaration");` yap.
  - `const indexes = …;` bloğunun hemen ALTINA, `return \`CREATE TABLE ${qualified} …` satırından önce, aşağıdakini ekle. Bu olmadan PG dosyası `error: Unsupported fixture declaration` ile düşer (ölçüldü).
```ts
    // A RAW INDEX IS RENDERED AS DECLARED, ONLY RE-POINTED AT THE FIXTURE'S
    // SCHEMA: `cloud_projects_product_slug_key` is an expression index the typed
    // builder cannot spell, and the case-folding uniqueness it enforces is the
    // behaviour under test. Any other raw statement is still refused, by name.
    for (const constraint of definition.rawConstraints ?? []) {
      const target = ` ON ${definition.name} (`;
      if (!/^CREATE (UNIQUE )?INDEX /.test(constraint.up) || !constraint.up.includes(target)) {
        throw new Error(`Unsupported fixture raw constraint ${constraint.name}`);
      }
      indexes.push(`${constraint.up.replace(target, ` ON ${qualified} (`)};`);
    }
```
(d) `.github/workflows/cloud-server-typecheck.yml`: `flag-publication-postgres` işinin `suites=(…)` listesinde `modules/fleet/cellcage.pg.test.ts` satırının ALTINA `            modules/shared/environment-slug.pg.test.ts` ekle.
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- db/schema.test.ts modules/shared/environment-slug.pg.test.ts && npm run typecheck)` · Beklenen: `10 pass` / `0 fail` / `Ran 10 tests across 2 files.`, ve typecheck exit 0. `"main\n"`'in CHECK'e takıldığı da burada ölçülüyor: Postgres'te `$`, sondaki satır sonundan önce eşleşmiyor.
- [ ] **Adım 5: Commit** — `git add cloud/platform/server/db/public.ts cloud/platform/server/db/schema.test.ts cloud/platform/server/palbase/palbase-env.d.ts cloud/platform/server/modules/shared/flags-postgres.ts cloud/platform/server/modules/shared/environment-slug.pg.test.ts .github/workflows/cloud-server-typecheck.yml && git commit -m "feat(db): cloud_projects.slug — dilbilgisi CHECK'i ve proje içi harf büyüklüğünden bağımsız tekillik (FR-101)"`

---

### T003: İlk ortamın slug'ı her iki yaratma yolunda açıkça `main`
<!-- deps: [T001, T002] | files: [cloud/platform/server/modules/environments/cloud-lifecycle.ts, cloud/platform/server/modules/environments/cloud-lifecycle.test.ts] | satisfies: [FR-102] -->

**Interfaces:**
- Consumes: `FIRST_ENVIRONMENT_SLUG` (T001), `cloud_projects.slug` (T002)
- Produces:
  - `CloudLifecycleService.create` (CLI'ın `palbase project create`'i) ilk ortamı `slug: "main"` ile yazar.
  - `createEnvironment` de aynısını yapar. Panel bu yoldan geçer: önce ürünü, sonra bu çağrıyı yapar — `studio/src/app/(studio)/projects/new/page.tsx`, ad "Production".
  - `provisionDeps(ctx)` artık `ctx.slug` alır ve onu `cloud_projects` satırına yazar.

Bugün `main` okuma anında çıkarılıyor: ortamın adı ürününkine eşitse `main` sayılıyor (`cli.controller.ts:487-490`). Bu yüzden panelden açılan bir projenin ilk ortamı CLI'da `Production/` oluyor. Kural artık yaratılışta YAZILIR (D-015). Görünen ad serbest kalır.

T005 bu kuralı genişletir. "İlk ortam", `main`'i dolduran ortam olur; `existing === 0` yerine `held()` kullanılır.

- [ ] **Adım 1: Kırmızı testi yaz** — `cloud/platform/server/modules/environments/cloud-lifecycle.test.ts`: `import { ProvisionService } from "./provision.ts";` satırını `import { type CreateProjectDeps, ProvisionService } from "./provision.ts";` yap ve dosyanın SONUNA ekle:
```ts

/**
 * WHAT REACHES `cloud_projects`: THE SLUG (FR-102, FR-103, D-015).
 *
 * The provisioning saga is faked, but it CALLS the real canonical writer — the
 * row handed to `Database.$insert` is the claim, not an argument somewhere on
 * the way. A CLI-created project named `todoapp` and a panel-created one whose
 * first environment is displayed as "Production" must land in the same
 * directory: `main`.
 */
describe("the environment row carries its slug", () => {
  const inserts: Array<{ table: string; values: Record<string, unknown> }> = [];
  const answers: Array<{ match: RegExp; rows: unknown[] }> = [];
  const seedBefore = process.env["PBC_SEALED_FLEET_SEED"];
  const db = Object.assign(fakeDatabase().raw, {
    query: async (sql: string) => {
      const hit = answers.find((a) => a.match.test(sql));
      if (hit === undefined) throw new Error(`unexpected query: ${sql.slice(0, 80)}`);
      return hit.rows;
    },
    insert: async (table: string, values: Record<string, unknown>) => {
      inserts.push({ table, values });
      return values;
    },
  }) as never;
  const writing = Object.assign(isolated().get(ProvisionService), {
    createProject: async (input: { ref: string; productId?: string }, deps: CreateProjectDeps) => {
      await deps.writeCanonicalRecord({ ref: input.ref, slot: 1, cell: "cell-01", phase: "Provisioning" }, input.productId);
      return { ref: input.ref, slot: 1, cell: "cell-01", phase: "Running" };
    },
  });
  const run = <T>(fn: (s: CloudLifecycleService) => Promise<T>) =>
    withServices({ Database: db }, () => fn(isolated().with(ProvisionService, writing).get(CloudLifecycleService)));
  const environmentRow = () => inserts.find((i) => i.table === "cloud_projects")?.values;
  /** An existing project on a plan with room for another environment. */
  const existingProject = (environments: number, tier = "pro") => answers.push(
    { match: /FROM cloud_products WHERE id/i, rows: [{ id: "proj_1", organization_id: "org_x", name: "Acme" }] },
    { match: /SELECT tier FROM cloud_organizations/, rows: [{ tier }] },
    { match: /COUNT\(\*\)::int AS n FROM cloud_projects/i, rows: [{ n: environments }] },
  );

  beforeEach(() => {
    process.env["PBC_SEALED_FLEET_SEED"] = "00".repeat(32);
    inserts.length = 0;
    answers.length = 0;
    answers.push(
      { match: /FROM cloud_organization_members m/, rows: [{ role: "member" }] },
      { match: /FROM cloud_org_tier_history/, rows: [] },
    );
  });
  afterEach(() => {
    if (seedBefore === undefined) delete process.env["PBC_SEALED_FLEET_SEED"];
    else process.env["PBC_SEALED_FLEET_SEED"] = seedBefore;
  });

  it("`palbase project create todoapp` writes its environment with slug main", async () => {
    answers.push(
      { match: /SELECT tier FROM cloud_organizations/, rows: [{ tier: "free" }] },
      { match: /COUNT\(\*\)::int AS n FROM cloud_products/i, rows: [{ n: 0 }] },
    );
    await run((s) => s.create({ id: "usr_x" }, { name: "todoapp", tier: "free", organizationId: "org_x" } as Parameters<CloudLifecycleService["create"]>[1]));
    expect(environmentRow()).toMatchObject({ name: "todoapp", slug: "main" });
  });

  it("the panel's first environment, displayed as Production, is written with slug main", async () => {
    existingProject(0, "free");
    await run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Production" }));
    expect(environmentRow()).toMatchObject({ name: "Production", slug: "main", product_id: "proj_1" });
  });

  it("a later environment is not given main", async () => {
    existingProject(1);
    await run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "preview" }));
    expect(environmentRow()?.slug).not.toBe("main");
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/environments/cloud-lifecycle.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - `(fail) the environment row carries its slug > \`palbase project create todoapp\` writes its environment with slug main`
  - farkta `-   "slug": "main",` — yazılan satırda `slug` anahtarı yok.
  - `33 pass` / `2 fail` / `Ran 35 tests across 1 file.`

  Üçüncü test ("a later environment is not given main") şimdiden geçer, çünkü ters yönü bekler.
- [ ] **Adım 3: Uygula** — `cloud/platform/server/modules/environments/cloud-lifecycle.ts`:
(a) `import { liveComputeDeps, ComputeService } from "../usage/compute";` satırının ALTINA `import { FIRST_ENVIRONMENT_SLUG } from "../shared/environment-slug";` ekle.
(b) `create` içinde `organizationId, name: body.name, tier: body.tier, keys, sealedIdentity, user,` satırını şununla değiştir:
```ts
        // A NEW PROJECT'S ONLY ENVIRONMENT IS `main` (FR-102, D-015) — written,
        // not inferred from "its name equals the product's" at read time.
        organizationId, name: body.name, slug: FIRST_ENVIRONMENT_SLUG, tier: body.tier, keys, sealedIdentity, user,
```
(c) `createEnvironment` içinde üç değişiklik:
  - `this.planService.assertEnvironmentCountAllowed(plan, Number(rows[0]?.n ?? 0));` satırını şununla değiştir:
```ts
    const existing = Number(rows[0]?.n ?? 0);
    this.planService.assertEnvironmentCountAllowed(plan, existing);
```
  - `const name = body.name;` satırının ALTINA ekle:
```ts
    // THE PANEL CREATES A PROJECT IN TWO CALLS (product, then this one), so an
    // empty product's first environment arrives HERE — displayed as
    // "Production", and still `main` on every disk (FR-102, D-015).
    const slug = existing === 0 ? FIRST_ENVIRONMENT_SLUG : null;
```
  - `}, this.provisionDeps({ organizationId, name, tier, keys, sealedIdentity, user })),` satırını `}, this.provisionDeps({ organizationId, name, slug, tier, keys, sealedIdentity, user })),` yap.

(d) `provisionDeps(ctx: {` tipinde `name: string;` satırının ALTINA `slug: string | null;` ekle. Ardından `Database.$insert("cloud_projects", {` içinde `name: ctx.name,` satırının ALTINA aşağıdakini ekle. Dikkat: `name: ctx.name,` dosyada iki kez geçer. Biri `cloud_products` ekleyen blokta, biri `cloud_projects` ekleyen blokta; doğru yer İKİNCİSİ, `cloud_projects` bloğu.
```ts
          // The directory name. Written once, here; nothing renames it (FR-104).
          slug: ctx.slug,
```
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- modules/environments/cloud-lifecycle.test.ts && npm run typecheck)` · Beklenen: `35 pass` / `0 fail` / `Ran 35 tests across 1 file.`, ve typecheck exit 0.
- [ ] **Adım 5: Commit** — `git add cloud/platform/server/modules/environments/cloud-lifecycle.ts cloud/platform/server/modules/environments/cloud-lifecycle.test.ts && git commit -m "feat(environments): ilk ortamın slug'ı her iki yaratma yolunda açıkça main (FR-102, D-015)"`

---

### T004: Geri doldurma planı — bugünkü dizin geçerliyse korunur, değilse türetilir ya da numaralanır
<!-- deps: [T001, T002] | files: [cloud/platform/server/db/public.ts, cloud/platform/server/modules/shared/environment-slug.ts, cloud/platform/server/modules/shared/environment-slug.backfill.test.ts, cloud/platform/server/modules/cli/cli.controller.ts, cloud/platform/server/modules/cli/cli.controller.test.ts, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-105] -->

**Interfaces:**
- Consumes: `EnvironmentSlugService.problemWith/derive`, `FIRST_ENVIRONMENT_SLUG` (T001)
- Produces:
  - `SLUG_BACKFILL_RULES = ["first", "kept", "derived", "suffixed"] as const` (`db/public.ts`).
  - `type SlugBackfillRow = { ref; product_id; product_name: string | null; name: string | null; slug: string | null; created_at: string | Date | null }`.
  - `SlugAssignment` (zod: `ref, product_id, previous_name, previous_directory, slug, rule`).
  - `EnvironmentSlugService.legacyName(name: string | null, index: number, ref: string, productName?: string): string`. Bu, CLI listesinin slug'dan önceki ad kuralıdır; tek kopyası burada.
  - `EnvironmentSlugService.planBackfill(rows: readonly SlugBackfillRow[]): SlugAssignment[]`.
  - `CliController` kurucusu 4. parametre olarak `EnvironmentSlugService` alır; `environmentSlug` artık `legacyName`'e devreder.

Bu görev önceki taslakta T006'ydı. T005'in yaratma yolu da bu planı kullandığı için ÖNE alındı (bkz. T005, `held()`).

Plan saf bir fonksiyondur: aynı satırlar hangi sırayla gelirse gelsin aynı planı verir.
- Proje başına sıralama oluşturma sırasıdır (`created_at`). Zamansız satırlar en sona gider, eşitlikte ref karar verir.
- Var olan slug'lar sabittir ve kendi adlarını işgal eder.
- Birinci geçiş: ilk ortam `main` olur. Bugünkü dizini geçerli ve boş olan her ortam onu korur.
- İkinci geçiş: kalanlar önce türetilmiş slug'ı alır; o da doluysa ilk boş `<taban>-N`'i alır (N≥2). Geçerli bir dizin, başka bir satırın ekiyle asla kapılmaz.

"Bugünkü dizin", `/api/v2/projects`'in bugün döndürdüğü addır. Kural `cli.controller.ts`'ten `legacyName`'e TAŞINIR ki geri doldurma birebir aynı cevaptan başlasın. Mevcut `ortam adı kuralı` testleri (`cli.controller.test.ts:486-510`) taşımanın davranışı koruduğunu ölçer.

- [ ] **Adım 1: Kırmızı testi yaz** — `cloud/platform/server/modules/shared/environment-slug.backfill.test.ts` (yeni dosya):
```ts
import { describe, expect, it } from "bun:test";
import { isolated } from "@palbase/backend/test";
import { EnvironmentSlugService, type SlugBackfillRow } from "./environment-slug.ts";

/**
 * THE BACKFILL PLAN (FR-105): which directory each existing environment ends up in.
 *
 * Every row here stands for an environment that some teammate has linked
 * today, under the name `palbase link` gave it (`previous_directory`). The plan
 * keeps that name wherever it is a valid, free slug — a directory that does not
 * move is a migration nobody notices — and otherwise says exactly what it chose
 * and why.
 */
const slugs = isolated().get(EnvironmentSlugService);
const row = (over: Partial<SlugBackfillRow> & Pick<SlugBackfillRow, "ref" | "name" | "created_at">): SlugBackfillRow =>
  ({ product_id: "proj_1", product_name: "todoapp", slug: null, ...over });

describe("EnvironmentSlugService.planBackfill (FR-105)", () => {
  it("the first environment becomes main whatever it was called — CLI and panel projects alike", () => {
    const plan = slugs.planBackfill([
      row({ ref: "cli01ref", name: "todoapp", created_at: "2026-08-01T00:00:00Z" }),
      row({ ref: "pnl01ref", product_id: "proj_2", product_name: "Shop", name: "Production", created_at: "2026-08-02T00:00:00Z" }),
    ]);
    expect(plan).toEqual([
      { ref: "cli01ref", product_id: "proj_1", previous_name: "todoapp", previous_directory: "main", slug: "main", rule: "first" },
      { ref: "pnl01ref", product_id: "proj_2", previous_name: "Production", previous_directory: "Production", slug: "main", rule: "first" },
    ]);
  });

  it("an environment whose directory is already a valid, free slug keeps it", () => {
    const plan = slugs.planBackfill([
      row({ ref: "first1ref", name: "todoapp", created_at: "2026-08-01T00:00:00Z" }),
      row({ ref: "stag01ref", name: "staging", created_at: "2026-08-02T00:00:00Z" }),
      row({ ref: "feat01ref", name: "featureX", created_at: "2026-08-03T00:00:00Z" }),
      row({ ref: "noname1ref", name: null, created_at: "2026-08-04T00:00:00Z" }),
    ]);
    expect(plan.map((a) => [a.previous_directory, a.slug, a.rule])).toEqual([
      ["main", "main", "first"],
      ["staging", "staging", "kept"],
      ["featureX", "featureX", "kept"],
      // A nameless environment was listed under its ref, and still is.
      ["noname1ref", "noname1ref", "kept"],
    ]);
  });

  it("a directory that is not a valid slug is replaced by the derived one", () => {
    const plan = slugs.planBackfill([
      row({ ref: "first1ref", name: "todoapp", created_at: "2026-08-01T00:00:00Z" }),
      row({ ref: "featx1ref", name: "Feature X", created_at: "2026-08-02T00:00:00Z" }),
      row({ ref: "login1ref", name: "feature/login", created_at: "2026-08-03T00:00:00Z" }),
    ]);
    expect(plan.slice(1).map((a) => [a.previous_directory, a.slug, a.rule])).toEqual([
      ["Feature X", "Feature-X", "derived"],
      ["feature/login", "feature-login", "derived"],
    ]);
  });

  it("twins and reserved names get the first free numbered slug — never a valid name another row already has", () => {
    const plan = slugs.planBackfill([
      row({ ref: "first1ref", name: "todoapp", created_at: "2026-08-01T00:00:00Z" }),
      row({ ref: "twinA1ref", name: "Staging", created_at: "2026-08-02T00:00:00Z" }),
      row({ ref: "twinB1ref", name: "staging", created_at: "2026-08-03T00:00:00Z" }),
      row({ ref: "stag21ref", name: "staging-2", created_at: "2026-08-04T00:00:00Z" }),
      row({ ref: "dupm01ref", name: "main", created_at: "2026-08-05T00:00:00Z" }),
      row({ ref: "locl01ref", name: "local", created_at: "2026-08-06T00:00:00Z" }),
    ]);
    expect(plan.slice(1).map((a) => [a.previous_directory, a.slug, a.rule])).toEqual([
      ["Staging", "Staging", "kept"],
      ["staging", "staging-3", "suffixed"],
      ["staging-2", "staging-2", "kept"],
      ["main", "main-2", "suffixed"],
      ["local", "local-2", "suffixed"],
    ]);
  });

  it("a slug written after the deploy is fixed; the older row without one gives way", () => {
    const plan = slugs.planBackfill([
      row({ ref: "first1ref", name: "todoapp", created_at: "2026-08-01T00:00:00Z" }),
      row({ ref: "old001ref", name: "staging", created_at: "2026-08-02T00:00:00Z" }),
      row({ ref: "new001ref", name: "staging", slug: "staging", created_at: "2026-09-27T00:00:00Z" }),
    ]);
    expect(plan.map((a) => [a.ref, a.slug, a.rule])).toEqual([
      ["first1ref", "main", "first"],
      ["old001ref", "staging-2", "suffixed"],
    ]);
  });

  it("the same rows in any order give the same plan", () => {
    const rows = [
      row({ ref: "first1ref", name: "todoapp", created_at: "2026-08-01T00:00:00Z" }),
      row({ ref: "twinA1ref", name: "Staging", created_at: "2026-08-02T00:00:00Z" }),
      row({ ref: "twinB1ref", name: "staging", created_at: "2026-08-02T00:00:00Z" }),
      row({ ref: "nodate1ref", name: "qa", created_at: null }),
      row({ ref: "other1ref", product_id: "proj_0", product_name: "other", name: "Prod", created_at: "2026-07-01T00:00:00Z" }),
    ];
    const forward = slugs.planBackfill(rows);
    expect(slugs.planBackfill([...rows].reverse())).toEqual(forward);
    // Same timestamp: the ref breaks the tie. No timestamp: last, as `ORDER BY created_at` puts it.
    expect(forward.map((a) => [a.ref, a.slug])).toEqual([
      ["other1ref", "main"],
      ["first1ref", "main"],
      ["twinA1ref", "Staging"],
      ["twinB1ref", "staging-2"],
      ["nodate1ref", "qa"],
    ]);
  });

  it("a project whose first environment cannot be main is refused by name — nothing is guessed", () => {
    expect(() => slugs.planBackfill([
      row({ ref: "first1ref", name: "todoapp", created_at: "2026-08-01T00:00:00Z" }),
      row({ ref: "later1ref", name: "x", slug: "main", created_at: "2026-09-27T00:00:00Z" }),
    ])).toThrow(`project proj_1: first1ref is its first environment but "main" already belongs to later1ref — resolve by hand before the backfill`);
  });
});
```
Kurucu değişeceği için `cloud/platform/server/modules/cli/cli.controller.test.ts` koşum takımı da güncellenir:
  - `import { CliService } from "./cli.service.ts";` satırının ALTINA `import { EnvironmentSlugService } from "../shared/environment-slug.ts";` ekle.
  - `controllerWith` içinde `Object.assign(isolated().get(CliService), cli),` satırının ALTINA `isolated().get(EnvironmentSlugService),` ekle.
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.backfill.test.ts modules/cli/cli.controller.test.ts)` · Beklenen: **FAIL**, çıktıda `TypeError: slugs.planBackfill is not a function.` ve `45 pass` / `7 fail` / `Ran 52 tests across 2 files.`
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/server/db/public.ts`: `export const ENVIRONMENT_SLUG_INDEX = …;` satırının ALTINA ekle:
```ts

/**
 * Why the slug backfill (FR-105) gave an environment the slug it did — the
 * migration report's vocabulary, here so a CHECK can be derived from it.
 *
 *   first     the project's first environment: `main` (FR-102, D-015)
 *   kept      today's directory was already a valid, free slug — nothing moves
 *   derived   today's directory was not a valid slug; this is the derived one
 *   suffixed  the valid or derived name was taken or reserved; the first free `-N`
 */
export const SLUG_BACKFILL_RULES = ["first", "kept", "derived", "suffixed"] as const;
```
(b) `cloud/platform/server/modules/shared/environment-slug.ts`: import'u `import { ENVIRONMENT_SLUG_PATTERN, SLUG_BACKFILL_RULES } from "../../db/public";` yap. `export const EnvironmentSlug = …;` satırının ALTINA ekle:
```ts

/** One environment as the backfill reads it — `slug` is `null` until it is given one. */
export type SlugBackfillRow = {
  ref: string;
  product_id: string;
  product_name: string | null;
  name: string | null;
  slug: string | null;
  created_at: string | Date | null;
};

export const SlugAssignment = z.object({
  ref: z.string(),
  product_id: z.string(),
  previous_name: z.string().nullable(),
  /** The directory `palbase link` wrote for it before slugs. */
  previous_directory: z.string(),
  slug: z.string(),
  rule: z.enum(SLUG_BACKFILL_RULES),
});
export type SlugAssignment = z.infer<typeof SlugAssignment>;

const at = (v: string | Date | null): number => (v === null ? Number.POSITIVE_INFINITY : new Date(v).getTime());
```
Sınıf belgesindeki `* environment: creation (FR-102/FR-103) and the backfill (FR-105).` satırını şu iki satırla değiştir:
```ts
 * environment: creation (FR-102/FR-103), the CLI listing's pre-slug names and
 * the backfill (FR-105).
```
`derive` metodunun ALTINA, sınıfın kapanışından önce ekle:
```ts

  /**
   * The name the CLI listing gives an environment BEFORE slugs — the directory
   * on every teammate's disk today, and where the backfill (FR-105) starts.
   *
   * The first environment's name is the product's, because
   * `POST /v1/cloud/projects` writes both rows with one name: offered as-is it
   * would read `--env todoapp`, so it is `main`. A nameless environment is
   * `main` when first and its own ref otherwise — two entries must never share
   * a name, or one would silently overwrite the other's files.
   */
  legacyName(name: string | null, index: number, ref: string, productName?: string): string {
    const trimmed = (name ?? "").trim();
    if (trimmed !== "" && !(index === 0 && productName !== undefined && trimmed === productName.trim())) {
      return trimmed;
    }
    return index === 0 ? FIRST_ENVIRONMENT_SLUG : ref;
  }

  /**
   * THE BACKFILL PLAN (FR-105) — a pure function of the rows, so the same
   * ledger gives the same plan on every run and in every order.
   *
   * Per project, environments in creation order (`created_at`, no timestamp
   * last as Postgres sorts it, ref breaking ties). Slugs that already exist
   * are FIXED and occupy their name. Then two passes, so a valid directory is
   * never taken by another row's suffix:
   *   1. the first environment gets `main`; every other one whose directory is
   *      a valid, free slug keeps it;
   *   2. the rest get the derived slug if it is free, else the first free
   *      `<base>-N` (N from 2).
   */
  planBackfill(rows: readonly SlugBackfillRow[]): SlugAssignment[] {
    const products = new Map<string, SlugBackfillRow[]>();
    for (const r of rows) products.set(r.product_id, [...(products.get(r.product_id) ?? []), r]);

    const plan: SlugAssignment[] = [];
    for (const productId of [...products.keys()].sort()) {
      const environments = [...products.get(productId)!]
        .sort((a, b) => at(a.created_at) - at(b.created_at) || (a.ref < b.ref ? -1 : a.ref > b.ref ? 1 : 0));
      const owner = new Map<string, string>();
      for (const e of environments) if (e.slug !== null) owner.set(e.slug.toLowerCase(), e.ref);
      const free = (slug: string) => this.problemWith(slug, false) === null && !owner.has(slug.toLowerCase());

      const pending = environments.flatMap((row, index) => (row.slug !== null ? [] : [{
        row, index, previous: this.legacyName(row.name, index, row.ref, row.product_name ?? undefined),
      }]));
      const chosen = new Map<string, { slug: string; rule: SlugAssignment["rule"] }>();
      const assign = (ref: string, slug: string, rule: SlugAssignment["rule"]) => {
        chosen.set(ref, { slug, rule });
        owner.set(slug.toLowerCase(), ref);
      };

      for (const p of pending) {
        if (p.index === 0) {
          const holder = owner.get(FIRST_ENVIRONMENT_SLUG);
          if (holder !== undefined) {
            throw new Error(
              `project ${productId}: ${p.row.ref} is its first environment but "${FIRST_ENVIRONMENT_SLUG}" already belongs to ${holder} — resolve by hand before the backfill`,
            );
          }
          assign(p.row.ref, FIRST_ENVIRONMENT_SLUG, "first");
        } else if (free(p.previous)) {
          assign(p.row.ref, p.previous, "kept");
        }
      }
      for (const p of pending) {
        if (chosen.has(p.row.ref)) continue;
        const base = this.derive(p.previous) || "env";
        if (free(base)) {
          assign(p.row.ref, base, "derived");
          continue;
        }
        for (let n = 2; ; n++) {
          const suffix = `-${n}`;
          const candidate = base.slice(0, 39 - suffix.length).replace(/-+$/, "") + suffix;
          if (free(candidate)) {
            assign(p.row.ref, candidate, "suffixed");
            break;
          }
        }
      }
      for (const p of pending) {
        const c = chosen.get(p.row.ref)!;
        plan.push({
          ref: p.row.ref, product_id: productId, previous_name: p.row.name,
          previous_directory: p.previous, slug: c.slug, rule: c.rule,
        });
      }
    }
    return plan;
  }
```
(c) `cloud/platform/server/modules/cli/cli.controller.ts`: `import { CliService } from "./cli.service";` satırının ALTINA `import { EnvironmentSlugService } from "../shared/environment-slug";` ekle. Tek satırlık kurucuyu şununla değiştir:
```ts
  constructor(
    private readonly orgAccessService: OrgAccessService,
    private readonly patIdentityService: PatIdentityService,
    private readonly cliService: CliService,
    private readonly environmentSlugService: EnvironmentSlugService,
  ) {}
```
`private environmentSlug(…)` gövdesini değiştir: `const trimmed = (name ?? "").trim();` satırından `return index === 0 ? "main" : ref;` satırına kadar, aradaki yorum dâhil, şununla:
```ts
    // THE RULE LIVES IN `EnvironmentSlugService.legacyName`: the slug backfill
    // (FR-105) must start from exactly the directory this answer puts on disk
    // today, and a second copy of the rule is a second answer.
    return this.environmentSlugService.legacyName(name, index, ref, productName);
```
(d) `.github/workflows/cloud-server-typecheck.yml`: `typecheck` listesinde T001'in `modules/shared/environment-slug.test.ts` satırının ALTINA `            modules/shared/environment-slug.backfill.test.ts` ekle.
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.backfill.test.ts modules/cli/cli.controller.test.ts && npm run typecheck)` · Beklenen: `52 pass` / `0 fail` / `Ran 52 tests across 2 files.`, ve typecheck exit 0.
- [ ] **Adım 5: Commit** — `git add cloud/platform/server/db/public.ts cloud/platform/server/modules/shared/environment-slug.ts cloud/platform/server/modules/shared/environment-slug.backfill.test.ts cloud/platform/server/modules/cli/cli.controller.ts cloud/platform/server/modules/cli/cli.controller.test.ts .github/workflows/cloud-server-typecheck.yml && git commit -m "feat(environments): slug geri doldurma planı — bugünkü dizin geçerliyse korunur, değilse türetilir/numaralanır (FR-105)"`

---

### T005: Ortam yaratma slug alır ya da addan türetir; geçersiz/ayrılmış 400, çakışan 409 — yarışı kaybeden de 409
<!-- deps: [T001, T002, T003, T004] | files: [cloud/platform/server/modules/environments/cloud-lifecycle.ts, cloud/platform/server/modules/environments/cloud-lifecycle.test.ts, cloud/platform/server/modules/environments/cloud-lifecycle.race.test.ts, cloud/platform/server/modules/environments/cloud-environments.controller.test.ts, cloud/platform/server/modules/shared/environment-slug.ts, cloud/platform/server/modules/shared/environment-slug.pg.test.ts, cloud/platform/api/cloud.openapi.yaml, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-103, FR-101, FR-102] -->

**Interfaces:**
- Consumes: `EnvironmentSlug`, `EnvironmentSlugService.problemWith/derive`, `FIRST_ENVIRONMENT_SLUG` (T001); `ENVIRONMENT_SLUG_INDEX` (T002); `provisionDeps` `ctx.slug` (T003); `EnvironmentSlugService.planBackfill`, `SlugBackfillRow` (T004)
- Produces:
  - `CreateEnvironmentBody.slug?: string` (`POST /v1/cloud/projects/{productId}/environments`).
  - `export const ENVIRONMENT_SLUGS_OF_PROJECT_SQL` (`cloud-lifecycle.ts`). `$1` = product id; kolonlar `ref, product_id, product_name, name, slug, created_at`.
  - `EnvironmentSlugService.held(rows: readonly SlugBackfillRow[]): Map<string, string>`. Küçük harfe katlanmış slug → onu tutan ref. Geri doldurulmamış satırlar için geri doldurmanın VERECEĞİ slug da dahildir.
  - Kanonik `cloud_projects` INSERT'i artık `Database.$attempt((tx) => tx.insert(…))` ile, bir SAVEPOINT içinde koşar.
  - Hatalar:
    - 400 `slug "<s>" is not valid: a slug starts with a letter and continues with up to 38 letters, digits or hyphens (^[A-Za-z][A-Za-z0-9-]{0,38}$)`
    - 400 `slug "local" is reserved: …`
    - 400 `slug "<s>" is reserved for the project's first environment`
    - 400 `the first environment's slug is always "main"; give it any display name instead`
    - 400 `no slug can be derived from the name "<n>"; send one in "slug"`
    - 409 `this project already has an environment with slug "<s>" (slugs are unique regardless of case)`. Yarışı kaybeden istek de aynı 409'u alır.
    - Türetilmiş bir slug'ın hatası ek olarak `— derived from the name "<n>"; send a different "slug"` taşır.

  Yayımlanmış CLI yalnız `name` gönderir; geçerli bir ad aynen slug olur.

Üç tasarım kararı var:

1. **Bir slug, projenin TUTTUĞU slug'lara karşı yargılanır, SQL'de değil TS'te.** `held()`, saklanan slug'lara ek olarak geri doldurulmamış satırlar için `planBackfill`'in vereceği slug'ları da tutar. Böylece geri doldurmadan önceki pencerede iki şey olur:
   - Eski bir CLI projesinin ürün adını taşıyan ilk ortamı (bugün diskte `main/`) yeni bir `todoapp`'ı yanlışlıkla engellemez.
   - Eski bir `Feature X` (geri doldurmada `Feature-X` olacak) yeni bir `feature-x`'i engeller. Engellemeseydi eskisi `Feature-X-2`'ye itilir ve takımın dizini taşınırdı.

   Sözleşme adımından (T013) sonra plan boştur ve `held()` yalnız saklanan slug'lardır. Boş bir projede okuma yapılmaz; `existing === 0` ise `held()` boştur.

2. **"İlk ortam", ortamı OLMAYAN projenin ortamıdır: `existing === 0`.** Yeni bir projenin ilk ortamı ve bütün ortamları silinmiş bir projenin bir sonraki ortamı böyledir; o ortam `main` olur (FR-102). D-021 (kullanıcı onayladı, 2026-09-26): projede başka bir ortam varken `main` silinemez, `main` ancak son ortamsa gider — ret T017'de, sunucuda, her yoldan. Bu yüzden ortamı olan her proje `main`'i tutar ve sonraki hiçbir ortama `main` kendiliğinden verilmez: slug göndermeden "Production" yazan kişinin ortamı `Production` olur, beklenmedik bir `main` değil (D-021'in reddettiği seçenek tam olarak buydu). Bu değişmez T017'ye dayanır: T005, T017'nin sunucu kısmı olmadan canlıya çıkmaz (aynı deploy; Global Constraints → "Deploy sırası"). Tek başına çıkarsa `main` hâlâ silinebilir ve proje, öteki ortamları durdukça bir daha `main` alamaz.

   `held()`'e dayalı "ilk" kuralı (`held()`'de `main` yoksa ilk ortam) ARTIK GEREKMİYOR: yalnız `main`'i silinmiş ama başka ortamları duran bir projede farklı cevap verirdi, ve D-021 o durumu kapatıyor. Üstelik o durumda yanlış cevabı verirdi: kişinin yazdığı adı `main`'e çevirirdi. `held()` ise kalıyor; karar 1'in çakışma denetimi ve geri doldurma penceresi için gerekli. `first` artık `environmentSlugFor`'a parametre olarak gelir: `environmentSlugFor(first, held, name, requested)`. `held.size === 0` bu değişmezde aynı cevabı verirdi (her satır `held()`'de bir slug tutar); sayım D-021'in adlandırdığı olgu olduğu için o yazıldı.

3. **Yarış.** İki yaratma aynı slug'ı boş bulursa tekil indeks ikincisini reddeder. Bir istek TEK transaction'dır. Savepoint olmadan o ret transaction'ı abort eder. Sonra saganın bu adımı geri sarması (`deleteCanonicalRecord`'un DELETE'i) de reddedilir, ve istemci "A HALF-CREATED RESOURCE EXISTS" diyen bir 500 alır; ref talebi de sızar. INSERT artık `Database.$attempt` içinde koşar: SDK'nın kendi motor mesajı bu yolu öneriyor, ve `palbase-backend` 41.0.0 `$attempt`'i taşıyor. Kaybeden istek, arama yapılsaydı alacağı 409'u alır. Test gerçek `SagaService.runSaga` ile, gerçek yazıcı/telafi ile ve abort olan tek transaction'ı modelleyen bir sahteyle koşar.

- [ ] **Adım 1: Kırmızı testi yaz** — (a) `cloud/platform/server/modules/environments/cloud-lifecycle.test.ts` üzerinde şu değişiklikleri yap:
  - `import { CloudLifecycleService } from "./cloud-lifecycle.ts";` satırını şununla değiştir:
```ts
import { CloudLifecycleService, ENVIRONMENT_SLUGS_OF_PROJECT_SQL } from "./cloud-lifecycle.ts";
import type { SlugBackfillRow } from "../shared/environment-slug.ts";
```
  - T003'ün `describe("the environment row carries its slug", …)` bloğunun belge yorumunda `row handed to \`Database.$insert\` is the claim, not an argument somewhere on` satırını `row handed to the ledger's insert is the claim, not an argument somewhere on` yap.
  - Aynı bloğun `db` sahtesinde, `insert: async (…) => { … },` girdisinin ALTINA ekle:
```ts
    // The environment row is written inside a SAVEPOINT (`Database.$attempt`);
    // here the savepoint's handle is this same fake.
    attempt: async <T>(fn: (tx: unknown) => Promise<T>): Promise<T> => fn(db),
```
  - `/** An existing project on a plan with room for another environment. */` yorumunu ve `existingProject` yardımcısını şununla değiştir:
```ts
  /** The project's first environment, as the later tests find it. */
  const MAIN = { ref: "main01ref", name: "Acme", slug: "main", created_at: "2026-08-01T00:00:00Z" };
  /** An existing project "Acme" on a plan with room for another environment, holding `environments`. */
  const existingProject = (environments: Array<Partial<SlugBackfillRow> & { ref: string }>, tier = "pro") => answers.push(
    { match: /FROM cloud_products WHERE id/i, rows: [{ id: "proj_1", organization_id: "org_x", name: "Acme" }] },
    { match: /SELECT tier FROM cloud_organizations/, rows: [{ tier }] },
    { match: /COUNT\(\*\)::int AS n FROM cloud_projects/i, rows: [{ n: environments.length }] },
    {
      match: /FROM cloud_projects p\s+LEFT JOIN cloud_products pr/,
      rows: environments.map((e) => ({ product_id: "proj_1", product_name: "Acme", name: null, slug: null, created_at: null, ...e })),
    },
  );
```
  - T003'ün iki çağrısını değiştir: `existingProject(0, "free");` → `existingProject([], "free");`, ve `existingProject(1);` → `existingProject([MAIN]);`.
  - "a later environment is not given main" testinin ALTINA, bloğun içine ekle:
```ts

  it("a slug sent with the request is written as sent", async () => {
    existingProject([MAIN]);
    await run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Feature X", slug: "featureX" }));
    expect(environmentRow()).toMatchObject({ name: "Feature X", slug: "featureX" });
  });

  it("without one, the slug is derived from the display name", async () => {
    existingProject([MAIN]);
    await run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Feature Profile Update" }));
    expect(environmentRow()).toMatchObject({ name: "Feature Profile Update", slug: "Feature-Profile-Update" });
  });

  it("a slug that breaks the grammar is a 400 that states the rule — and nothing is created", async () => {
    // DEFENCE IN DEPTH: over HTTP `CreateEnvironmentBody` refuses this slug
    // before the service runs, with the same rule as its field message
    // (`cloud-environments.controller.test.ts`). This pins the service's own
    // answer for a caller that reaches it another way.
    existingProject([MAIN]);
    const refused = run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "login", slug: "feature/login" }));
    await expect(refused).rejects.toMatchObject({
      status: 400,
      message: `slug "feature/login" is not valid: a slug starts with a letter and continues with up to 38 letters, digits or hyphens (^[A-Za-z][A-Za-z0-9-]{0,38}$)`,
    });
    expect(inserts).toEqual([]);
  });

  it("a reserved slug is a 400, and a derived one says where it came from", async () => {
    existingProject([MAIN]);
    await expect(run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "laptop", slug: "local" })))
      .rejects.toMatchObject({
        status: 400,
        message: `slug "local" is reserved: local/ belongs to the stack on each developer's own machine`,
      });
    await expect(run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Main" })))
      .rejects.toMatchObject({
        status: 400,
        message: `slug "Main" is reserved for the project's first environment — derived from the name "Main"; send a different "slug"`,
      });
    expect(inserts).toEqual([]);
  });

  it("the first environment cannot be given a slug other than main", async () => {
    existingProject([], "free");
    await expect(run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Production", slug: "production" })))
      .rejects.toMatchObject({
        status: 400,
        message: `the first environment's slug is always "main"; give it any display name instead`,
      });
  });

  it("a name that leaves no usable slug asks for one", async () => {
    existingProject([MAIN]);
    await expect(run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "🚀" })))
      .rejects.toMatchObject({ status: 400, message: `no slug can be derived from the name "🚀"; send one in "slug"` });
  });

  it("a slug the project already has — in any case — is a 409, and nothing is created", async () => {
    existingProject([MAIN, { ref: "stag01ref", name: "Staging", slug: "Staging", created_at: "2026-08-02T00:00:00Z" }]);
    await expect(run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "staging" })))
      .rejects.toMatchObject({
        status: 409,
        message: `this project already has an environment with slug "staging" (slugs are unique regardless of case) — derived from the name "staging"; send a different "slug"`,
      });
    expect(inserts).toEqual([]);
  });

  it("a project whose environments were all deleted gets `main` on its next environment", async () => {
    // The project (`cloud_products`) outlives its environments. `main` goes
    // only as the last one (D-021), so deleting them all leaves the project
    // empty — and an empty project's next environment is its first.
    existingProject([]);
    await run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Production" }));
    expect(environmentRow()).toMatchObject({ name: "Production", slug: "main" });
  });

  it("an environment that is not the project's first is never handed `main` — the name typed is the slug (D-021)", async () => {
    // D-021 keeps `main` while any other environment exists, so a project
    // holding environments without `main` cannot be reached through any verb.
    // Should a ledger ever show one, a person who typed "Production" gets
    // `Production` — not a `main` they did not ask for (the rejected option).
    existingProject([{ ref: "stag01ref", name: "staging", slug: "staging", created_at: "2026-08-02T00:00:00Z" }]);
    await run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Production" }));
    expect(environmentRow()).toMatchObject({ name: "Production", slug: "Production" });
  });

  it("before the backfill, the product's own name is free — that environment is main/ on disk", async () => {
    // A CLI-created project's first environment carries the product's name,
    // and the CLI has always written it to `main/` (FR-105 gives it `main`).
    existingProject([{ ref: "cli01ref", name: "Acme", created_at: "2026-08-01T00:00:00Z" }]);
    await run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Acme" }));
    expect(environmentRow()).toMatchObject({ name: "Acme", slug: "Acme" });
  });

  it("before the backfill, a new environment cannot take the slug an older one will get — nor `main`", async () => {
    // "Feature X" is on every disk as `Feature X/` and the backfill will give it
    // `Feature-X`; taking that now would push it to `Feature-X-2` and move
    // every teammate's directory.
    const legacy = [
      { ref: "cli01ref", name: "Acme", created_at: "2026-08-01T00:00:00Z" },
      { ref: "featx1ref", name: "Feature X", created_at: "2026-08-02T00:00:00Z" },
    ];
    existingProject(legacy);
    await expect(run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "x", slug: "feature-x" })))
      .rejects.toMatchObject({
        status: 409,
        message: `this project already has an environment with slug "feature-x" (slugs are unique regardless of case)`,
      });
    await run((s) => s.createEnvironment({ id: "usr_x" }, "proj_1", { name: "Production" }));
    expect(environmentRow()).toMatchObject({ name: "Production", slug: "Production" });
  });

  it("the project's environments are read with the statement `environment-slug.pg.test.ts` runs on Postgres", async () => {
    // What that statement RETURNS for a real ledger is measured there; this
    // pins that the create path sends exactly it, for this project.
    existingProject([MAIN]);
    const seen: Array<{ sql: string; params: unknown[] }> = [];
    const spying: object = Object.assign(fakeDatabase().raw, {
      query: async (sql: string, params: unknown[] = []) => {
        seen.push({ sql, params });
        return (answers.find((a) => a.match.test(sql)) ?? { rows: [] }).rows;
      },
      insert: async () => ({}),
      attempt: async <T>(fn: (tx: unknown) => Promise<T>): Promise<T> => fn(spying),
    });
    await withServices({ Database: spying as never }, () =>
      isolated().with(ProvisionService, writing).get(CloudLifecycleService)
        .createEnvironment({ id: "usr_x" }, "proj_1", { name: "x", slug: "Staging" }));
    expect(seen.filter((q) => q.sql === ENVIRONMENT_SLUGS_OF_PROJECT_SQL).map((q) => q.params)).toEqual([["proj_1"]]);
  });
```
(b) `cloud/platform/server/modules/environments/cloud-lifecycle.race.test.ts` (yeni dosya):
```ts
import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { UniqueViolation } from "@palbase/backend";
import { fakeDatabase, isolated, withServices } from "@palbase/backend/test";
import { CloudLifecycleService } from "./cloud-lifecycle.ts";
import { type CreateProjectDeps, ProvisionService } from "./provision.ts";
import { SagaService } from "./saga.ts";

/**
 * TWO CREATES RACING FOR ONE SLUG, ON THE REAL SAGA AND ONE REQUEST TRANSACTION (FR-103).
 *
 * Two teammates — or a CLI retry after a timeout — both find `staging` free.
 * The unique index lets one through; the other's INSERT fails with 23505. A
 * request is ONE Postgres transaction (`saga.ts`; the SDK's `put` doc: "the
 * failed insert aborts it and every later statement answers `current
 * transaction is aborted`"), so everything after that INSERT in the same
 * request — the saga's compensation of that very step included — is refused
 * unless the INSERT ran inside a savepoint (`Database.$attempt`).
 *
 * The runner and the canonical writer and compensator are the real ones; the
 * database is a fake that behaves as that transaction does.
 */
describe("the environment slug race, through SagaService.runSaga", () => {
  const answers: Array<{ match: RegExp; rows: unknown[] }> = [];
  const seedBefore = process.env["PBC_SEALED_FLEET_SEED"];
  let aborted = false;
  const refuse = (): never => {
    throw new Error("current transaction is aborted, commands ignored until end of transaction block");
  };
  const tx: object = Object.assign(fakeDatabase().raw, {
    query: async (sql: string) => {
      if (aborted) refuse();
      return (answers.find((a) => a.match.test(sql)) ?? { rows: [] }).rows;
    },
    insert: async (table: string, values: Record<string, unknown>) => {
      if (aborted) refuse();
      if (table === "cloud_projects") {
        aborted = true;
        throw new UniqueViolation("cloud_projects_product_slug_key");
      }
      return values;
    },
    // A SAVEPOINT: a failure inside rolls back only to it.
    attempt: async <T>(fn: (t: unknown) => Promise<T>): Promise<T> => {
      try {
        return await fn(tx);
      } catch (e) {
        aborted = false;
        throw e;
      }
    },
  });
  const sagas = isolated().get(SagaService);
  const realSaga = Object.assign(isolated().get(ProvisionService), {
    createProject: async (input: { ref: string; productId?: string }, deps: CreateProjectDeps) => {
      const project = { ref: input.ref, slot: 1, cell: "cell-01", phase: "Provisioning" as const };
      await sagas.runSaga("job_race", [
        { name: "reserve-ref", forward: async () => {}, compensate: async () => {} },
        {
          name: "write-canonical-record",
          forward: () => deps.writeCanonicalRecord(project, input.productId),
          compensate: () => deps.deleteCanonicalRecord(input.ref, input.productId),
        },
      ]);
      return { ...project, phase: "Running" as const };
    },
  });

  beforeEach(() => {
    process.env["PBC_SEALED_FLEET_SEED"] = "00".repeat(32);
    aborted = false;
    answers.length = 0;
    answers.push(
      { match: /FROM cloud_organization_members m/, rows: [{ role: "member" }] },
      { match: /FROM cloud_org_tier_history/, rows: [] },
      { match: /FROM cloud_products WHERE id/i, rows: [{ id: "proj_1", organization_id: "org_x", name: "Acme" }] },
      { match: /SELECT tier FROM cloud_organizations/, rows: [{ tier: "pro" }] },
      { match: /COUNT\(\*\)::int AS n FROM cloud_projects/i, rows: [{ n: 1 }] },
      // What both racers read: the project holds only `main`, so `staging` looks free.
      {
        match: /FROM cloud_projects p\s+LEFT JOIN cloud_products pr/,
        rows: [{ ref: "main01ref", product_id: "proj_1", product_name: "Acme", name: "Acme", slug: "main", created_at: "2026-08-01T00:00:00Z" }],
      },
    );
  });
  afterEach(() => {
    if (seedBefore === undefined) delete process.env["PBC_SEALED_FLEET_SEED"];
    else process.env["PBC_SEALED_FLEET_SEED"] = seedBefore;
  });

  it("the loser gets the 409 the lookup would have given — not a 500 that says a half-created resource exists", async () => {
    const outcome = await withServices({ Database: tx as never }, () =>
      isolated().with(ProvisionService, realSaga).get(CloudLifecycleService)
        .createEnvironment({ id: "usr_x" }, "proj_1", { name: "x", slug: "staging" }))
      .then(() => "created", (e: Error & { status?: number }) => `${e.status ?? e.name}: ${e.message}`);
    expect(outcome).toBe(
      `409: this project already has an environment with slug "staging" (slugs are unique regardless of case)`,
    );
  });
});
```
(c) `cloud/platform/server/modules/environments/cloud-environments.controller.test.ts`: `import { CloudLifecycleService } from "./cloud-lifecycle.ts";` satırını şununla değiştir:
```ts
import { CloudLifecycleService, CreateEnvironmentBody } from "./cloud-lifecycle.ts";
import { ENVIRONMENT_SLUG_PATTERN } from "../../db/public.ts";
```
ve `describe("üretim giriş noktası", …)` bloğunun ÜSTÜNE ekle:
```ts
/**
 * THE ENVIRONMENT BODY AND ITS PUBLISHED CONTRACT CARRY THE SLUG (FR-103).
 *
 * The body is what the HTTP layer validates before the service runs; the
 * OpenAPI document is what a client reads. Both take the grammar from
 * `ENVIRONMENT_SLUG_PATTERN` — a third spelling would drift silently.
 */
describe("the environment body and its contract carry the slug (FR-103)", () => {
  it("the body takes an optional slug and refuses one that breaks the grammar", () => {
    expect(CreateEnvironmentBody.safeParse({ name: "Feature X" }).success).toBe(true);
    expect(CreateEnvironmentBody.safeParse({ name: "Feature X", slug: "featureX" }).success).toBe(true);
    const refused = CreateEnvironmentBody.safeParse({ name: "login", slug: "feature/login" });
    expect(refused.success).toBe(false);
    expect(refused.error?.issues[0]?.message).toBe(
      `a slug starts with a letter and continues with up to 38 letters, digits or hyphens (${ENVIRONMENT_SLUG_PATTERN})`,
    );
  });

  it("the OpenAPI contract documents the slug with the same pattern, and the 409", () => {
    const post = OPENAPI.slice(
      OPENAPI.indexOf("/v1/cloud/projects/{productId}/environments:"),
      OPENAPI.indexOf("/v1/cloud/projects/{ref}/keys/rotate:"),
    );
    expect(post).toContain(`pattern: "${ENVIRONMENT_SLUG_PATTERN}"`);
    expect(post).toMatch(/"409":/);
  });
});

```
(d) `cloud/platform/server/modules/shared/environment-slug.pg.test.ts` üzerinde:
  - `import { flagPostgres } from "./flags-postgres.ts";` satırının ÜSTÜNE ekle:
```ts
import { isolated } from "@palbase/backend/test";
import { ENVIRONMENT_SLUGS_OF_PROJECT_SQL } from "../environments/cloud-lifecycle.ts";
import { EnvironmentSlugService, type SlugBackfillRow } from "./environment-slug.ts";
```
  - `const fixture = flagPostgres(["cloud_projects"]);` → `const fixture = flagPostgres(["cloud_products", "cloud_projects"]);`
  - `insert` yardımcısını şununla değiştir:
```ts
async function insert(
  ref: string, productId: string, slug: string | null, name = slug ?? ref, createdAt = "2026-08-01",
): Promise<string> {
  try {
    await fixture.query(
      `INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, name, slug, created_at)
       VALUES ($1, $2, 'cell-01', 'Running', $3, $4, $5, $6::timestamptz)`,
      [ref, ++slot, productId, name, slug, createdAt],
    );
    return "accepted";
  } catch (e) {
    return (e as Error).message;
  }
}
```
  - Dosyanın SONUNA ekle:
```ts

describe("the slugs a project holds, read by the create path on a real PostgreSQL (FR-103)", () => {
  const slugs = isolated().get(EnvironmentSlugService);
  const held = async (productId: string) => [
    ...slugs.held((await fixture.query(ENVIRONMENT_SLUGS_OF_PROJECT_SQL, [productId])) as unknown as SlugBackfillRow[]),
  ].sort();

  it("every slug the project's environments hold, case folded — and only that project's", async () => {
    await insert("r1", "p1", "main", "Acme");
    await insert("r2", "p1", "Staging");
    await insert("r3", "p2", "qa");
    expect(await held("p1")).toEqual([["main", "r1"], ["staging", "r2"]]);
  });

  it("before the backfill, the slugs it WILL give — from the product's name and the rows' order", async () => {
    // A CLI-created project: its first environment carries the product's name
    // and is `main/` on disk; "Feature X" will become `Feature-X`.
    await fixture.query(
      `INSERT INTO cloud_products (id, organization_id, name, created_by) VALUES ('p1', 'org_1', 'Acme', 'usr_1')`,
    );
    await insert("r1", "p1", null, "Acme", "2026-08-01");
    await insert("r2", "p1", null, "Feature X", "2026-08-02");
    expect(await held("p1")).toEqual([["feature-x", "r2"], ["main", "r1"]]);
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/environments/cloud-lifecycle.test.ts modules/environments/cloud-lifecycle.race.test.ts modules/environments/cloud-environments.controller.test.ts modules/shared/environment-slug.pg.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - İki dosyada `SyntaxError: Export named 'ENVIRONMENT_SLUGS_OF_PROJECT_SQL' not found in module '…/modules/environments/cloud-lifecycle.ts'.`
  - `(fail) the environment body and its contract carry the slug (FR-103) > the body takes an optional slug and refuses one that breaks the grammar`
  - `(fail) … the OpenAPI contract documents the slug with the same pattern, and the 409`
  - `(fail) the environment slug race, through SagaService.runSaga > the loser gets the 409 …`, ve `Received: "SagaCompensationFailed: step 'write-canonical-record' failed: Unique constraint violated; the compensation for 'write-canonical-record' failed too (current transaction is aborted, commands ignored until end of transaction block) — A HALF-CREATED RESOURCE EXISTS, manual intervention required"`
  - `36 pass` / `5 fail` / `2 errors` / `Ran 41 tests across 4 files.`
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/server/modules/environments/cloud-lifecycle.ts` import'ları:
  - İlk `@palbase/backend` satırını şu iki satırla değiştir:
```ts
import { BadRequest, Conflict, Database, HttpError, Injectable, NotFound, UniqueViolation, z } from "@palbase/backend";
import { ENVIRONMENT_SLUG_INDEX } from "../../db/public";
```
  - T003'ün `import { FIRST_ENVIRONMENT_SLUG } from "../shared/environment-slug";` satırını `import { EnvironmentSlug, EnvironmentSlugService, FIRST_ENVIRONMENT_SLUG, type SlugBackfillRow } from "../shared/environment-slug";` yap.
  - `SagaCompensated` zaten `./saga`'dan import ediliyor.

(b) `CreateEnvironmentBody` içinde `name: z.string().min(1).max(64),` satırının ALTINA ekle:
```ts
  // THE DIRECTORY NAME, optional (FR-103): omitted, it is derived from `name`
  // (`EnvironmentSlugService.derive`). The published CLI sends only `name`.
  slug: EnvironmentSlug.optional(),
```
ve `CreateEnvironmentBody`'nin kapanışının (`});`) ALTINA ekle:
```ts

/**
 * Every environment of one project, with the columns the slug rules read.
 *
 * The same columns the backfill (FR-105) reads, because the create path judges
 * a new slug against `EnvironmentSlugService.held`: the slugs this project's
 * environments hold AND, for rows the backfill has not reached yet, the ones it
 * will give them. Exported so `environment-slug.pg.test.ts` runs this very
 * statement on a real Postgres.
 */
export const ENVIRONMENT_SLUGS_OF_PROJECT_SQL = `
  SELECT p.ref, p.product_id, pr.name AS product_name, p.name, p.slug, p.created_at
    FROM cloud_projects p
    LEFT JOIN cloud_products pr ON pr.id = p.product_id
   WHERE p.product_id = $1`;

/** A refused slug: a 400 whose message IS the rule, on the `slug` field. */
function slugRefused(message: string): BadRequest {
  return new BadRequest({ fields: [{ field: "slug", message }] }, message);
}

function slugTaken(slug: string): string {
  return `this project already has an environment with slug ${JSON.stringify(slug)} (slugs are unique regardless of case)`;
}
```
(c) Kurucunun son parametresi `private readonly cloudOperationsService: CloudOperationsService,` satırının ALTINA `private readonly environmentSlugService: EnvironmentSlugService,` ekle.
(d) `createEnvironment` içinde üç değişiklik:
  - T003'ün `const slug = existing === 0 ? FIRST_ENVIRONMENT_SLUG : null;` satırını şununla değiştir:
```ts
    // An empty project holds no slug; any other is read whole — the slugs its
    // environments hold and the ones the backfill will give its older rows.
    const environments = existing === 0
      ? []
      : (await Database.$query(ENVIRONMENT_SLUGS_OF_PROJECT_SQL, [productId])) as unknown as SlugBackfillRow[];
    const slug = this.environmentSlugFor(existing === 0, this.environmentSlugService.held(environments), name, body.slug);
```
  - `singleFlight(…)` çağrısını kapatan `);` satırını şununla değiştir. Bu satır, `}, this.provisionDeps({ organizationId, name, slug, tier, keys, sealedIdentity, user })),` satırının hemen altındadır.
```ts
    ).catch((e: unknown) => {
      // TWO CREATES RACING FOR ONE SLUG: `environmentSlugFor` saw it free for both,
      // the unique index let one through and the saga unwound the other. That
      // loser gets the same 409 as the lookup would have given — not a 500.
      if (e instanceof SagaCompensated && UniqueViolation.is(e.cause) && e.cause.constraint === ENVIRONMENT_SLUG_INDEX) {
        throw new Conflict(slugTaken(slug));
      }
      throw e;
    });
```
  - `createEnvironment` metodunun kapanışının ALTINA, `provisionDeps` belgesinden önce ekle:
```ts

  /**
   * The slug this environment will be written with — or the 4xx that says why not (FR-103).
   *
   * `first` means the project holds no environment: a new project's, or one
   * whose environments were all deleted. That environment is `main` (FR-102).
   * `main` cannot be deleted while other environments exist (D-021), so a
   * project that holds environments holds `main`, and no later environment is
   * ever handed it — a name typed without a slug stays that name.
   *
   * `held` is every slug the project's environments hold or will be given by
   * the backfill, case folded (`EnvironmentSlugService.held`). Any environment
   * but the first takes the slug it was sent, or one derived from its display
   * name; either way it must pass the grammar, avoid the reserved names and be
   * free in this project regardless of case. A derived slug that fails says
   * so, because the caller never typed it.
   */
  private environmentSlugFor(
    first: boolean, held: ReadonlyMap<string, string>, name: string, requested: string | undefined,
  ): string {
    const derived = requested === undefined && !first;
    const slug = requested ?? (first ? FIRST_ENVIRONMENT_SLUG : this.environmentSlugService.derive(name));
    const hint = derived ? ` — derived from the name ${JSON.stringify(name)}; send a different "slug"` : "";
    if (slug === "") {
      throw slugRefused(`no slug can be derived from the name ${JSON.stringify(name)}; send one in "slug"`);
    }
    const problem = this.environmentSlugService.problemWith(slug, first);
    if (problem !== null) throw slugRefused(problem + hint);
    if (held.has(slug.toLowerCase())) throw new Conflict(slugTaken(slug) + hint);
    return slug;
  }
```
(e) `provisionDeps` içinde:
  - Tipteki `slug: string | null;` → `slug: string;`.
  - `writeCanonicalRecord`'da `await Database.$insert("cloud_projects", {` satırını şununla değiştir:
```ts
        // IN A SAVEPOINT, because this INSERT can lose a race: two creates that
        // both found a slug free both reach it, and the unique index refuses the
        // second (FR-103). A request is ONE transaction — without the savepoint
        // that refusal aborts it, the saga's compensation below is refused too,
        // and the caller gets a 500 saying a half-created resource exists
        // instead of the 409 `createEnvironment` turns it into.
        await Database.$attempt((tx) => tx.insert("cloud_projects", {
```
  - Aynı nesnenin kapanışını (`sealed_binding: ctx.sealedIdentity.binding,` satırının hemen altındaki `});`) `}));` yap.

(f) `cloud/platform/server/modules/shared/environment-slug.ts`: `derive` metodunun ALTINA, `legacyName`'in belge yorumundan (`The name the CLI listing gives an environment BEFORE slugs`) ÖNCE ekle:
```ts
  /**
   * Every slug this project's environments hold — and, for rows the backfill
   * (FR-105) has not reached yet, the slug it WILL give them — case folded and
   * mapped to the environment that holds it.
   *
   * A new environment is judged against this (FR-103): it must not take a
   * directory a teammate already has on disk, nor push an older environment to
   * a numbered slug at the backfill. Once every row has a slug the plan is
   * empty and this is just the stored slugs.
   */
  held(rows: readonly SlugBackfillRow[]): Map<string, string> {
    const held = new Map<string, string>();
    for (const r of rows) if (r.slug !== null) held.set(r.slug.toLowerCase(), r.ref);
    for (const a of this.planBackfill(rows)) held.set(a.slug.toLowerCase(), a.ref);
    return held;
  }

```
(g) `cloud/platform/api/cloud.openapi.yaml`, `/v1/cloud/projects/{productId}/environments` `post` gövdesinde `name: { type: string }` satırının ALTINA ekle:
```yaml
                slug:
                  type: string
                  pattern: "^[A-Za-z][A-Za-z0-9-]{0,38}$"
                  description: >-
                    Optional. The environment's directory name
                    (`palbase/environments/<slug>/`), Android build type and
                    `--env` name; it never changes. Omitted, it is derived from
                    `name`. `local` is reserved, `main` belongs to the project's
                    first environment, and the first environment is always `main`.
                    Unique within the project regardless of case.
```
Aynı işlemin `"400": { description: Plan's environment count reached, or envelope not permitted }` satırını şununla değiştir:
```yaml
        "400": { description: "Plan's environment count reached, envelope not permitted, or the slug is invalid or reserved (the message states the rule)" }
        "409": { description: The project already has an environment with this slug, in any case }
```
(h) `.github/workflows/cloud-server-typecheck.yml`: `typecheck` listesinde `modules/environments/cloud-lifecycle.test.ts modules/environments/cloud-environments.transfer.test.ts` satırının ALTINA ekle:
```yaml
            # İki yaratma aynı slug için yarışırsa kaybeden 409 alır, 500 değil (FR-103): gerçek saga koşucusu
            # ve tek istek transaction'ı. Aynı gerekçe: CI'da koşmayan kapı testi kapı değildir.
            modules/environments/cloud-lifecycle.race.test.ts
```
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- modules/environments/cloud-lifecycle.test.ts modules/environments/cloud-lifecycle.race.test.ts modules/environments/cloud-environments.controller.test.ts modules/shared/environment-slug.pg.test.ts && npm run typecheck)` · Beklenen: `92 pass` / `0 fail` / `Ran 92 tests across 4 files.`, ve typecheck exit 0.

  Yeni testlerin ısırdığı ölçüldü; her değişiklik ölçümden sonra geri alındı:
  - Reddedilen seçenek, çağrıda `existing === 0` → `!this.environmentSlugService.held(environments).has(FIRST_ENVIRONMENT_SLUG)` → "an environment that is not the project's first is never handed `main`" testi düştü (`46 pass` / `1 fail`, 1 dosya). Aynı kural bu testlere karşı ilk koşturulduğunda fark `-   "slug": "Production",` / `+   "slug": "main",` idi (`91 pass` / `1 fail` / `Ran 92 tests across 4 files.`).
  - `existing === 0` → `false` → üç test düştü: "the panel's first environment, displayed as Production, is written with slug main", "the first environment cannot be given a slug other than main", "a project whose environments were all deleted gets `main` on its next environment" (`44 pass` / `3 fail`, 1 dosya).
  - `held()`'den `planBackfill` satırı çıkarılınca → "before the backfill, a new environment cannot take the slug an older one will get — nor `main`" ve PG testi "before the backfill, the slugs it WILL give" düştü (`51 pass` / `2 fail`, 2 dosya). "before the backfill, the product's own name is free" artık bu mutasyonda düşmüyor: "ilk" kararı `held()`'e bağlı değil.

  Tam süit bu görevden sonra `2265 pass` / `10 fail` / `6 errors` verdi (`Ran 2275 tests across 174 files.`). Taban `10 fail` / `6 errors` ile aynıdır; tümü Docker'lı testler.
- [ ] **Adım 5: Commit** — `git add .github/workflows/cloud-server-typecheck.yml cloud/platform/api/cloud.openapi.yaml cloud/platform/server/modules/environments/cloud-lifecycle.ts cloud/platform/server/modules/environments/cloud-lifecycle.test.ts cloud/platform/server/modules/environments/cloud-lifecycle.race.test.ts cloud/platform/server/modules/environments/cloud-environments.controller.test.ts cloud/platform/server/modules/shared/environment-slug.ts cloud/platform/server/modules/shared/environment-slug.pg.test.ts && git commit -m "feat(environments): ortam yaratma slug alır ya da addan türetir; geçersiz/ayrılmış 400, çakışan 409 — yarışta da; ilk ortam = ortamsız projenin ortamı (FR-103, D-021)"`

### T006: Yeniden adlandırma yalnız görünen adı değiştirir; panel saklanan slug'ı döner
<!-- deps: [T002, T004] | files: [cloud/platform/server/modules/panel/panel.controller.ts, cloud/platform/server/modules/panel/panel.environment-slug.test.ts, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-104] -->

**Interfaces:**
- Consumes: `cloud_projects.slug` (T002); iş akışı çıpası T004'ün satırı
- Produces: `PanelController.listEnvironments` / `environmentByRef` / `renameEnvironment` `slug` alanında SAKLANAN slug'ı döner. `renameEnvironment`'ın rotası `PATCH /v1/panel/environments/{ref}`; gövdesi `PanelRenameBody` (panel.ts:243), yalnız `name` taşır. Geri doldurulmamış bir satır eskisi gibi ref döner; bu yedeği T013 kaldırır.

Yeniden adlandırmanın SQL'i zaten yalnız `name` yazıyor: `UPDATE cloud_projects SET name = $2 WHERE ref = $1` (panel.controller.ts:610). Eksik olan başkaydı: panel slug diye REF gösteriyordu (`slug: ref`, :283).

Test, bir SELECT'e yalnız metninin adlandırdığı kolonları döndüren bir sahteyle koşar. `p.slug`'ı unutan bir sorgu slug alamaz, tıpkı Postgres'te olduğu gibi.

- [ ] **Adım 1: Kırmızı testi yaz** — `cloud/platform/server/modules/panel/panel.environment-slug.test.ts` (yeni dosya):
```ts
import { beforeEach, describe, expect, it } from "bun:test";
import { fakeDatabase, isolated, withServices } from "@palbase/backend/test";
import { PanelController } from "./panel.controller.ts";

/**
 * A RENAME CHANGES WHAT PEOPLE READ, NEVER WHERE THE FILES LIVE (FR-104).
 *
 * The slug is every teammate's `palbase/environments/<slug>/`; the display name
 * is a label. The fake below keeps rows in memory and answers a SELECT with
 * ONLY the columns its text names — so a query that forgets `p.slug` gets no
 * slug back, exactly as Postgres would answer it.
 */
type Row = Record<string, unknown>;
let ledger: Row[] = [];

/** The SELECT list's output names: `p.slug` → `slug`, `o.tier AS org_tier` → `org_tier`. */
function selected(sql: string): string[] {
  const list = sql.slice(sql.indexOf("SELECT") + 6, sql.indexOf("FROM"));
  return list.split(",").map((item) => {
    const alias = /\sAS\s+(\w+)\s*$/i.exec(item);
    return alias ? alias[1]! : item.trim().replace(/^\w+\./, "");
  });
}

const db = Object.assign(fakeDatabase().raw, {
  query: async (sql: string, params: unknown[] = []) => {
    if (sql.includes("CASE WHEN p.owner_id = $2 THEN 'owner'")) {
      const row = ledger.find((r) => r.ref === params[0]);
      if (!row) return [];
      return [{ role: row.owner_id === params[1] ? "owner" : "member" }];
    }
    if (sql.startsWith("UPDATE cloud_projects SET name = $2 WHERE ref = $1")) {
      for (const row of ledger) if (row.ref === params[0]) row.name = params[1];
      return [];
    }
    if (sql.includes("FROM cloud_projects p") && sql.includes("LEFT JOIN cloud_products pr")) {
      const names = selected(sql);
      return ledger
        .filter((r) => r.owner_id === params[0])
        .map((r) => Object.fromEntries(names.map((n) => [n, r[n] ?? null])));
    }
    throw new Error(`unexpected query: ${sql.slice(0, 80)}`);
  },
}) as never;
const panel = isolated().get(PanelController);
const run = <T>(fn: () => Promise<T>) => withServices({ Database: db }, fn);
const owner = { id: "usr_owner" };

beforeEach(() => {
  ledger = [
    {
      ref: "stag01ref", name: "staging", slug: "staging", owner_id: "usr_owner", product_id: "proj_1",
      tier: "free", phase: "Running", cell_id: "cell-01", created_at: "2026-09-01T00:00:00Z",
      organization_id: "org_1", org_tier: "free",
    },
    {
      ref: "old01ref", name: "Legacy", slug: null, owner_id: "usr_owner", product_id: "proj_1",
      tier: "free", phase: "Running", cell_id: "cell-01", created_at: "2026-08-01T00:00:00Z",
      organization_id: "org_1", org_tier: "free",
    },
  ];
});

describe("renaming an environment (FR-104)", () => {
  it("changes its display name and never its slug", async () => {
    const renamed = await run(() => panel.renameEnvironment("stag01ref", { name: "Staging (EU)" }, owner));
    expect(renamed).toMatchObject({ ref: "stag01ref", name: "Staging (EU)", slug: "staging" });
    expect(ledger[0]).toMatchObject({ name: "Staging (EU)", slug: "staging" });
  });

  it("the panel lists the stored slug beside the display name", async () => {
    const [environment] = (await run(() => panel.listEnvironments(owner))).filter((e) => e.ref === "stag01ref");
    expect(environment).toMatchObject({ name: "staging", slug: "staging" });
  });

  it("a row the backfill has not reached yet still answers with its ref", async () => {
    const [environment] = (await run(() => panel.listEnvironments(owner))).filter((e) => e.ref === "old01ref");
    expect(environment).toMatchObject({ name: "Legacy", slug: "old01ref" });
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/panel/panel.environment-slug.test.ts)` · Beklenen: **FAIL**. Çıktıda `(fail) renaming an environment (FR-104) > changes its display name and never its slug` görünür. Farkta `-   "slug": "staging",` / `+   "slug": "stag01ref",` görünür. Özet: `1 pass` / `2 fail` / `Ran 3 tests across 1 file.`
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/server/modules/panel/panel.controller.ts`:
  - `listEnvironments` sorgusunun ilk satırı `\`SELECT p.ref, p.name, p.tier, p.phase, p.cell_id, p.created_at, p.product_id,` → `\`SELECT p.ref, p.name, p.slug, p.tier, p.phase, p.cell_id, p.created_at, p.product_id,`.
  - Aynı metodda `slug: ref,` satırını şununla değiştir:
```ts
        // THE STORED SLUG — the directory every teammate's `link` writes; a
        // rename changes `name` above and never this (FR-104). A row the
        // backfill (FR-105) has not reached yet answers its ref, as before.
        slug: (r.slug as string | null) ?? ref,
```
(b) `.github/workflows/cloud-server-typecheck.yml`: `typecheck` listesinde T004'ün `modules/shared/environment-slug.backfill.test.ts` satırının ALTINA `            modules/panel/panel.environment-slug.test.ts` ekle.
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- modules/panel/panel.environment-slug.test.ts && npm run typecheck)` · Beklenen: `3 pass` / `0 fail` / `Ran 3 tests across 1 file.`, ve typecheck exit 0. `listEnvironments`'ı çağıran başka test yok. Docker'lı `panel.project-settings.pg` / `panel.deployment-activity.pg` testleri elle kurulmuş `cloud_projects` tablolarıyla bu metoda inmiyor; grep ile doğrulandı.
- [ ] **Adım 5: Commit** — `git add cloud/platform/server/modules/panel/panel.controller.ts cloud/platform/server/modules/panel/panel.environment-slug.test.ts .github/workflows/cloud-server-typecheck.yml && git commit -m "feat(panel): ortam saklanan slug'ı döner; yeniden adlandırma yalnız görünen adı değiştirir (FR-104)"`

---

### T017: Başka ortamlar varken `main` silinmez — ret sunucuda, her yoldan, yıkımdan önce; yaratma ile `main` silme proje kilidiyle sıralanır
<!-- deps: [T001, T004, T005, T006] | files: [cloud/platform/server/modules/environments/teardown.ts, cloud/platform/server/modules/environments/teardown.test.ts, cloud/platform/server/modules/environments/teardown.jobs.test.ts, cloud/platform/server/modules/environments/teardown.object.test.ts, cloud/platform/server/modules/environments/cloud-lifecycle.ts, cloud/platform/server/modules/environments/cloud-lifecycle.test.ts, cloud/platform/server/modules/environments/cloud-lifecycle.delete-main.test.ts, cloud/platform/server/modules/environments/main-delete-lock.pg.test.ts, cloud/platform/api/cloud.openapi.yaml, .github/workflows/cloud-server-typecheck.yml, cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/[environmentRef]/settings/danger/page.test.tsx] | satisfies: [FR-102, D-021] -->

**Interfaces:**
- Consumes: `FIRST_ENVIRONMENT_SLUG` (T001); `SlugBackfillRow`, `EnvironmentSlugService.planBackfill` (T004); `ENVIRONMENT_SLUGS_OF_PROJECT_SQL`, `EnvironmentSlugService.held`, `environmentSlugFor(first, …)` ve `cloud-lifecycle.test.ts`'in yaratma koşumu (`existingProject`, `writing`, `MAIN`) (T005). T006: yalnız yürütme sırası — bu görev T006'dan SONRA, T007'den ÖNCE koşar ve T005 ile aynı deploy'a girer.
- Produces:
  - `TeardownDeps.assertDeletable: (ref: string) => Promise<void>`. `TeardownService.deleteProject` onu `lockProjectRow`'dan hemen sonra, saganın ilk adımından (`unpublish-placement`) önce çağırır. Fırlatırsa hiçbir adım koşmaz ve hata olduğu gibi yükselir.
  - `CloudLifecycleService.assertDeletable(ref)` (private). `remove()` onu teardown'a bağlar; `DELETE /v1/cloud/projects/{ref}`'e gelen her istek ondan geçer.
  - `export const PRODUCT_FOR_CREATE_SQL` = `SELECT id, organization_id, name FROM cloud_products WHERE id = $1 FOR KEY SHARE`. `createEnvironment`'ın İLK sorgusu.
  - `export const MAIN_DELETE_LOCK_SQL` = `SELECT 1 FROM cloud_products WHERE id = $1 FOR UPDATE`.
  - Hata, `DELETE /v1/cloud/projects/{ref}`: 409 `main_deleted_last` `main is this project's default environment and cannot be deleted while other environments exist — delete the others first`. Tel gövdesi (`HttpError#toJSON`, ölçüldü): `{"error":"main_deleted_last","error_description":"main is this project's default environment and cannot be deleted while other environments exist — delete the others first","status":409,"request_id":"req_probe"}`. Aynı rotanın öteki 409'u değişmez: `conflict` `a delete job is already running for this project: <ref>` (`DeleteInProgress`).
  - OpenAPI `deleteProject`: `"409"`, iki kodu adıyla ayırır.

D-021 (kullanıcı onayladı, 2026-09-26): projede başka bir ortam varken `main`'i silme isteği hangi yoldan gelirse gelsin sunucuda reddedilir; `main` ancak projenin son ortamıysa silinir; ortamsız kalan projenin bir sonraki ortamı `main` olur (T005).

**Silme yolları (kazıma kopyasında sayıldı).** Bir ortam satırını silen tek canlı yol `DELETE /v1/cloud/projects/{ref}` (`cloud-environments.controller.ts:282`) → `CloudLifecycleService.remove` → teardown sagasının `delete-canonical-record` adımıdır (`cloud-lifecycle.ts` `DELETE FROM cloud_projects WHERE ref = $1`). Bu rotaya inenler:
- CLI `palbase env delete <ad>` ve `palbase project delete <ref>` (`palbase-cli` `internal/env/env.go:319`, `internal/project/project.go:331`).
- Panelin tehlike bölgesi: `settings/danger/page.tsx` → `trpc.environments.delete` → `pb.cloudEnvironments.remove` (`src/lib/panel/routes/lifecycle.ts:34`).
- palcore (makine kimliği, rotanın belgesi) ve `cloud/platform/deploy/verify-plane.py` (doğrulama projesi ve `reap`).

Kalan iki `DELETE` yaratma sagasının telafisidir (`provisionDeps.deleteCanonicalRecord`). Yalnız aynı isteğin az önce yazdığı ortam satırını ve mint ettiği ürünü siler; var olan bir `main`'e ulaşamaz, bu yüzden bilinçli olarak serbest. Ürünü (`cloud_products`), organizasyonu ya da hesabı silen bir fiil yok; `db/public.ts`'te FK/cascade yok. Studio'nun eski `/api/v2/projects/…` DELETE rotaları Temporal iş akışı başlatır ve `cloud_projects`'e dokunmaz. "Projeyi silmek" bu düzlemde ortamları tek tek silmektir: `main` en son serbesttir, proje boş kalır ve bir sonraki ortam `main` olur.

Dört tasarım kararı var:

1. **Ret sunucuda, teardown'ın içinde, yıkımdan önce.** Sıra `readProject` → `claimJob` → `lockProjectRow` → `assertDeletable` → saga. Saganın telafilerinin hepsi boş: ret `unpublish-placement`'tan sonra gelseydi yönlendirmesi kesilmiş bir `main` kalırdı. `claimJob`'ın iş satırı isteğin transaction'ında yazılır, yani 409 onu da geri alır ve sonraki deneme `fresh` başlar. `remove()`'un `catch`'i yalnız `DeleteInProgress`'i ve saga hatalarını çevirir; bu ret olduğu gibi çıkar, `delete_incomplete` olmaz.
2. **"Bu `main` mi" sorusunun cevabı yaratma yolununkiyle aynıdır:** aynı satırlar üzerinde `held()`, yani saklanan slug ya da geri doldurmanın vereceği slug. Geri doldurulmamış bir projede en eski satır `main`'dir ve aynı biçimde korunur. `main` olmayan bir ortamın silinmesi değişmez: proje kilidi yok, bekleme yok.
3. **Yarış, isteğin tek transaction'ıyla kapanır.** Yaratma, projesini ilk sorgusunda `FOR KEY SHARE` ile okur ve commit'e kadar tutar. `main` silme projeyi `FOR UPDATE` ile kilitler ve kardeşleri kilitten SONRA yeniden sayar. Önce gelen yaratma sayılır (silme 409 döner); sonra gelen yaratma silmeyi bekler, projeyi boş bulur ve `main` yazar. Gerçek PostgreSQL'de ölçüldü.
   - `FOR KEY SHARE`, `FOR UPDATE` dışında hiçbir kilitle çakışmaz: yaratmalar yan yana koşar, proje adı değiştirme (`UPDATE cloud_products SET name`) ve devir (`SET organization_id`) etkilenmez.
   - Kilit sırası kurala uyar: satır kilidi (`lockProjectRow`) → proje kilidi → yayın advisory kilidi (`unpublish-placement`). Yaratma var olan hiçbir ortam satırını kilitlemez; devir `cloud_projects(ref)` → `cloud_products` sırasıyla, ad değiştirme yalnız `cloud_products` kilitler. Döngü yok.
   - Bedel: projenin son ortamı `main` silinirken aynı projede yaratma ve proje adı değiştirme silme bitene kadar bekler (silme canlıda ~68 sn ölçüldü, `classify.client.ts`). Bekleyen yaratma kendi sağlamasını ancak ondan SONRA koşar; ikisi birlikte Envoy'un `/v1/cloud/` için 120 sn'lik zarfını aşabilir ve istemci zaman aşımı görür (düşen istek transaction'ıyla birlikte geri sarılır; proje o arada boşalmıştır, tekrar `main` yazar). Bir yaratma sağlanırken gelen `main` silme onu bekler, sonra 409 alır. Beklerken `main`'in satır kilidini (`lockProjectRow`, `FOR UPDATE`) tutar: o süre boyunca `main`'in uyanışı (`WAKE_ROW_LOCK_SQL`) ve faturalama turu da bekler; süre yaratmanınkiyle sınırlı. İsteğin transaction'ında kilit zaman aşımı yok (SDK'da 5 sn yalnız `$atomic` ve servis istemcisinde).
   - Bilinen sınır: son ortam `main`'in silinmesi `delete_incomplete` (503) ile yarıda kalır ve tekrarından ÖNCE aynı projede yeni bir ortam açılırsa, tekrar `main_deleted_last` alır; kiracısı yarı sökülmüş `main` o ortam silinene kadar defterde kalır. Yarıda kalan silme defterde iz bırakmaz (iş satırı isteğin transaction'ıyla geri sarılır), yani sunucu bu `main`'i sağlam olandan ayıramaz. Çıkış yolu cümlenin kendisi: önce ötekiler.
4. **Ret kendi koduyla döner: `main_deleted_last`, `conflict` değil.** Bu rotada `conflict` zaten "bu ortamın silmesi sürüyor — bekle ve tekrar dene" demek: `DeleteInProgress` → `Conflict` (`remove()`), `teardown.ts`'in belgesi ("çağıran bekleyip yeniden dener") ve `verify-plane.py` `reap` (409'u 10 sn × 12 tekrar dener). Bir projeyi koddan silen çağıran — palcore, krediyi biten kullanıcının projesini siler (`cloud-environments.controller.ts` rota belgesi) — "bekle" ile "önce ötekileri sil, `main` en son"u ancak koddan ayırabilir; aynı kodla, hiçbir beklemenin kaldırmayacağı bir reddi bekleyip tekrar denerdi. CLI'ın kendi kuralı da bu: "ADA bakılır, statüye değil" (`palbase-cli` `internal/transport/rest.go`). Durum 409 kalır; emsal `cutover_refused` (409, `internal.controller.ts`). CLI ve `@palbase/web` kodu olduğu gibi taşır (aşağıda).

**Panel ve CLI.** Tehlike bölgesi `err.message`'ı toast'a basar. 409 zarfından `@palbase/web`'in ürettiği `BackendError`'ın mesajı `error_description`'dır (`fromEnvelope`); eşlenmemiş bir kod (`main_deleted_last`) olduğu gibi fırlatılır. Yani cümle kişiye aynen ulaşır; Studio testi bunu gerçek `BackendError` sınıfıyla kilitler. Sil düğmesini `main`'de saklamak bu yürütme sırasında test-kilitli bir `main` sabiti ister: Studio kopyası T009'da doğar, `is_production` T016'da yalnız `main` olur. Bu yüzden burada yapılmadı; T016'dan sonra küçük bir takip işidir. CLI değişmez: `palbase env delete main` ve `palbase project delete <ref>` taşıma katmanının hatasını `fmt.Fprintln(os.Stderr, err)` ile aynen basar (`cmd/palbase/main.go`). `internal/transport` `Client.Do`'nun bu zarftan ürettiği hata atılabilir bir kopyada `httptest` ile ölçüldü: `main_deleted_last (409) [request_id req_probe]: main is this project's default environment and cannot be deleted while other environments exist — delete the others first`. Sunucuya tek istek gider: DELETE otomatik tekrarlanmaz (`IsNamedTransient` → `false`). `verify-plane.py` `reap` 409'u durumuna bakarak tekrar dener; hesabındaki projeler `POST /v1/cloud/projects` ile doğar, yani tek ortamlıdır ve bugün etkilenmez.

- [ ] **Adım 1: Kırmızı testleri yaz** — (a) `cloud/platform/server/modules/environments/teardown.test.ts`, `teardown.jobs.test.ts` ve `teardown.object.test.ts`: her birinin fikstüründe `    lockProjectRow: jest.fn(async () => {}),` satırının ALTINA ekle:
```ts
    assertDeletable: jest.fn(async () => {}),
```
(b) `cloud/platform/server/modules/environments/teardown.test.ts` dosyasının SONUNA ekle:
```ts
/**
 * A REFUSED DELETE STOPS BEFORE ANYTHING IS DESTROYED (D-021).
 *
 * `main` with siblings is refused in `assertDeletable`. Every compensation in
 * this saga is empty, so a refusal after `unpublish-placement` would leave a
 * `main` with its routing cut; it comes after the row lock (the lock order
 * above) and before the first step, and it surfaces exactly as thrown — the
 * route turns nothing into a "delete_incomplete".
 */
describe("a refused delete stops before anything is destroyed (D-021)", () => {
  it("row lock, then the deletable check, then the saga — and a refusal starts no step", async () => {
    return await withServices({ Database: __dbRaw, Secrets: __sirlar }, async () => {
      const order: string[] = [];
      const allowed = deps({
        lockProjectRow: jest.fn(async (r: string) => { order.push(`lock:${r}`); }),
        assertDeletable: jest.fn(async (r: string) => { order.push(`deletable:${r}`); }),
        unpublishPlacement: jest.fn(async () => { order.push("unpublish"); }),
      });
      await teardownService.deleteProject("abc12345m", allowed);
      expect(order.slice(0, 3)).toEqual(["lock:abc12345m", "deletable:abc12345m", "unpublish"]);

      const refusal = new Error("refused");
      const refused = deps({ assertDeletable: jest.fn(async () => { throw refusal; }) });
      const outcome = await teardownService.deleteProject("abc12345m", refused).then(() => null, (e: unknown) => e);
      expect(outcome).toBe(refusal);
      for (const step of [
        refused.unpublishPlacement, refused.withdrawTenant, refused.purgeTenantData,
        refused.purgeTenantVolume, refused.releaseRef, refused.releaseSlot, refused.deleteCanonicalRecord,
      ]) {
        expect(step).not.toHaveBeenCalled();
      }
    });
  });
});
```
(c) `cloud/platform/server/modules/environments/cloud-lifecycle.delete-main.test.ts` (yeni dosya):
```ts
import { beforeEach, describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import { fakeDatabase, isolated, withServices } from "@palbase/backend/test";
import type { SlugBackfillRow } from "../shared/environment-slug.ts";
import { CloudLifecycleService, ENVIRONMENT_SLUGS_OF_PROJECT_SQL, MAIN_DELETE_LOCK_SQL } from "./cloud-lifecycle.ts";
import { type TeardownDeps, TeardownService } from "./teardown.ts";

/**
 * `main` IS NOT DELETED WHILE THE PROJECT HAS OTHER ENVIRONMENTS (D-021).
 *
 * `palbase env delete`, `palbase project delete`, the panel's danger zone and
 * a direct API call all land on `DELETE /v1/cloud/projects/{ref}`, which is
 * `remove()`. The teardown saga is faked, but it CALLS the real row lock and
 * the real `assertDeletable` the route wires in, in the order the real saga
 * does (`teardown.test.ts` pins that order). The ledger below answers only
 * the statements it names: a check that reads anything else fails here.
 *
 * The refusal's code is `main_deleted_last`, not `conflict`: on this route
 * `conflict` means "a delete is already running — wait and retry"
 * (`cloud-lifecycle.test.ts` pins that one), and a caller deleting a whole
 * project from code must not wait on a refusal that no wait will lift.
 */
const OPENAPI = readFileSync(new URL("../../../api/cloud.openapi.yaml", import.meta.url), "utf8");
const REFUSAL =
  "main is this project's default environment and cannot be deleted while other environments exist — delete the others first";

const environment = (ref: string, slug: string | null, name: string, createdAt: string): SlugBackfillRow =>
  ({ ref, product_id: "proj_1", product_name: "Acme", name, slug, created_at: createdAt });
const MAIN = environment("main01ref", "main", "Production", "2026-08-01T00:00:00Z");
const STAGING = environment("stag01ref", "staging", "staging", "2026-08-02T00:00:00Z");

let ledger: SlugBackfillRow[] = [];
let torn: string[] = [];
let sent: string[] = [];
/** What commits while the delete waits for the project lock: a create that got there first. */
let whileLocking: () => void = () => {};

const db = Object.assign(fakeDatabase().raw, {
  query: async (sql: string, params: unknown[] = []) => {
    sent.push(sql);
    if (sql === "SELECT 1 FROM cloud_projects WHERE ref = $1 FOR UPDATE") return [{}];
    if (sql === "SELECT product_id FROM cloud_projects WHERE ref = $1") {
      return ledger.filter((r) => r.ref === params[0]).map((r) => ({ product_id: r.product_id }));
    }
    if (sql === ENVIRONMENT_SLUGS_OF_PROJECT_SQL) return ledger.filter((r) => r.product_id === params[0]);
    if (sql === MAIN_DELETE_LOCK_SQL) {
      whileLocking();
      return [{}];
    }
    throw new Error(`unexpected query: ${sql.slice(0, 80)}`);
  },
}) as never;

const tearing = Object.assign(isolated().get(TeardownService), {
  deleteProject: async (ref: string, deps: TeardownDeps) => {
    await deps.lockProjectRow(ref);
    await deps.assertDeletable(ref);
    torn.push(ref);
  },
});
const lifecycle = isolated().with(TeardownService, tearing).get(CloudLifecycleService);
const remove = (ref: string) => withServices({ Database: db }, () => lifecycle.remove(ref));

beforeEach(() => {
  ledger = [];
  torn = [];
  sent = [];
  whileLocking = () => {};
});

describe("deleting main (D-021)", () => {
  it("while another environment exists is a 409 that states the rule and the way out — and nothing is torn down", async () => {
    ledger = [MAIN, STAGING];
    await expect(remove("main01ref")).rejects.toMatchObject({ status: 409, error: "main_deleted_last", errorDescription: REFUSAL });
    expect(torn).toEqual([]);
  });

  it("as the project's last environment goes ahead", async () => {
    ledger = [MAIN];
    await remove("main01ref");
    expect(torn).toEqual(["main01ref"]);
  });

  it("any other environment is deleted as before: no project lock, no wait", async () => {
    ledger = [MAIN, STAGING];
    await remove("stag01ref");
    expect(torn).toEqual(["stag01ref"]);
    expect(sent).not.toContain(MAIN_DELETE_LOCK_SQL);
  });

  it("before the backfill, the environment the backfill will make main is kept the same way", async () => {
    // A CLI-created project: its first environment carries the product's name,
    // is `main/` on disk, and the backfill (FR-105) gives it `main`.
    ledger = [
      environment("cli01ref", null, "Acme", "2026-08-01T00:00:00Z"),
      environment("featx1ref", null, "Feature X", "2026-08-02T00:00:00Z"),
    ];
    await expect(remove("cli01ref")).rejects.toMatchObject({ status: 409, errorDescription: REFUSAL });
    await remove("featx1ref");
    expect(torn).toEqual(["featx1ref"]);
  });

  it("a create that committed while the delete waited for the project lock is counted — the siblings are read again after it", async () => {
    // Before the lock the project held only `main`. The create holding the
    // project `FOR KEY SHARE` commits `staging` and lets go; the wait itself is
    // measured on PostgreSQL in `main-delete-lock.pg.test.ts`.
    ledger = [MAIN];
    whileLocking = () => {
      ledger = [MAIN, STAGING];
    };
    await expect(remove("main01ref")).rejects.toMatchObject({ status: 409, errorDescription: REFUSAL });
    expect(torn).toEqual([]);
    const lock = sent.indexOf(MAIN_DELETE_LOCK_SQL);
    expect(lock).toBeGreaterThan(-1);
    expect(sent.lastIndexOf(ENVIRONMENT_SLUGS_OF_PROJECT_SQL)).toBeGreaterThan(lock);
  });

  it("the published contract documents the refusal", () => {
    const route = OPENAPI.slice(
      OPENAPI.indexOf("  /v1/cloud/projects/{ref}:"),
      OPENAPI.indexOf("  /v1/cloud/projects/{ref}/migrate:"),
    );
    expect(route).toContain("operationId: deleteProject");
    expect(route).toMatch(/"409": \{ description: "`main_deleted_last` — main cannot be deleted while/);
    expect(route).toContain("`conflict` — a delete of this environment is already running");
  });
});
```
(d) `cloud/platform/server/modules/environments/cloud-lifecycle.test.ts`:
  - `import { CloudLifecycleService, ENVIRONMENT_SLUGS_OF_PROJECT_SQL } from "./cloud-lifecycle.ts";` → `import { CloudLifecycleService, ENVIRONMENT_SLUGS_OF_PROJECT_SQL, PRODUCT_FOR_CREATE_SQL } from "./cloud-lifecycle.ts";`
  - T005'in "the project's environments are read with the statement `environment-slug.pg.test.ts` runs on Postgres" testinin ALTINA, bloğun içine ekle:
```ts
  it("the project is read first, with the shared lock `main-delete-lock.pg.test.ts` measures — a delete of main waits for this create (D-021)", async () => {
    existingProject([MAIN]);
    const seen: Array<{ sql: string; params: unknown[] }> = [];
    const spying: object = Object.assign(fakeDatabase().raw, {
      query: async (sql: string, params: unknown[] = []) => {
        seen.push({ sql, params });
        return (answers.find((a) => a.match.test(sql)) ?? { rows: [] }).rows;
      },
      insert: async () => ({}),
      attempt: async <T>(fn: (tx: unknown) => Promise<T>): Promise<T> => fn(spying),
    });
    await withServices({ Database: spying as never }, () =>
      isolated().with(ProvisionService, writing).get(CloudLifecycleService)
        .createEnvironment({ id: "usr_x" }, "proj_1", { name: "x", slug: "Staging" }));
    // FIRST: the lock is held before the environments are counted.
    expect(seen[0]).toEqual({ sql: PRODUCT_FOR_CREATE_SQL, params: ["proj_1"] });
  });
```
  - "deleteProject still answers 409 while a delete is running (korunan)" testinde `expect(e.status).toBe(409);` satırının ALTINA ekle:
```ts
      // `conflict` HERE MEANS "WAIT AND RETRY" — the D-021 refusal has its own
      // code (`main_deleted_last`, `cloud-lifecycle.delete-main.test.ts`).
      expect(e.error).toBe("conflict");
```
(e) `cloud/platform/server/modules/environments/main-delete-lock.pg.test.ts` (yeni dosya):
```ts
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "bun:test";
import type { PoolClient } from "pg";
import { flagPostgres } from "../shared/flags-postgres.ts";
import { ENVIRONMENT_SLUGS_OF_PROJECT_SQL, MAIN_DELETE_LOCK_SQL, PRODUCT_FOR_CREATE_SQL } from "./cloud-lifecycle.ts";

/**
 * THE TWO LOCKS THAT KEEP `main`, ON A REAL POSTGRESQL (D-021).
 *
 * A request is ONE transaction. A create reads its project `FOR KEY SHARE`
 * (`PRODUCT_FOR_CREATE_SQL`) and holds that until it commits; a delete of
 * `main` takes `FOR UPDATE` on the same row (`MAIN_DELETE_LOCK_SQL`) and only
 * then counts the siblings. A fake cannot say whether those two wait for each
 * other, or whether a second create or a rename of the project is held up by
 * them — this file measures it. A wait is measured with a 300 ms
 * `lock_timeout` on its own connection: the claim is the wait, not its length.
 */
const fixture = flagPostgres(["cloud_products", "cloud_projects"]);
beforeAll(fixture.initialize);
afterAll(fixture.close);
beforeEach(fixture.reset);

let slot = 0;
const INSERT_ENVIRONMENT = `INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, name, slug)
  VALUES ($1, $2, 'cell-01', 'Running', 'p1', $3, $3)`;

beforeEach(async () => {
  await fixture.query(
    `INSERT INTO cloud_products (id, organization_id, name, created_by) VALUES ('p1', 'org_1', 'Acme', 'usr_1')`,
  );
  await fixture.query(INSERT_ENVIRONMENT, ["main01ref", ++slot, "main"]);
});

/**
 * What `probe` answers while another request is still inside its saga: that
 * request ran `steps`, holds what they took, and commits only after `probe`
 * has answered — whatever the answer, so a failed claim never leaves it open.
 */
async function whileInFlight<T>(steps: (c: PoolClient) => Promise<unknown>, probe: () => Promise<T>): Promise<T> {
  let ran!: () => void;
  let finish!: () => void;
  const started = new Promise<void>((resolve) => (ran = resolve));
  const held = new Promise<void>((resolve) => (finish = resolve));
  const request = fixture.transaction(async (c) => {
    await steps(c);
    ran();
    await held;
  });
  try {
    await Promise.race([started, request]);
    return await probe();
  } finally {
    finish();
    await request;
  }
}

/** Whether `sql` gets its lock right now — on its own connection, giving up after 300 ms. */
const lockNow = (sql: string) =>
  fixture
    .transaction(async (c) => {
      await c.query("SET LOCAL lock_timeout = '300ms'");
      await c.query(sql, ["p1"]);
      return "granted";
    })
    .catch((e: Error) => e.message);

/** The project's environments as a request sees them once it holds `lock`. */
const slugsAfter = (lock: string) =>
  fixture.transaction(async (c) => {
    await c.query(lock, ["p1"]);
    return (await c.query(ENVIRONMENT_SLUGS_OF_PROJECT_SQL, ["p1"])).rows.map((r) => r.slug as string).sort();
  });

describe("the project lock pair (D-021)", () => {
  it("a create in flight makes the main delete wait — and once it commits, the delete counts the new environment", async () => {
    const waited = await whileInFlight(async (c) => {
      await c.query(PRODUCT_FOR_CREATE_SQL, ["p1"]);
      await c.query(INSERT_ENVIRONMENT, ["stag01ref", ++slot, "staging"]);
    }, () => lockNow(MAIN_DELETE_LOCK_SQL));
    expect(waited).toBe("canceling statement due to lock timeout");
    expect(await slugsAfter(MAIN_DELETE_LOCK_SQL)).toEqual(["main", "staging"]);
  });

  it("a main delete in flight makes a create wait — and once it commits, the create finds the project empty", async () => {
    const waited = await whileInFlight(async (c) => {
      await c.query(MAIN_DELETE_LOCK_SQL, ["p1"]);
      await c.query("DELETE FROM cloud_projects WHERE ref = $1", ["main01ref"]);
    }, () => lockNow(PRODUCT_FOR_CREATE_SQL));
    expect(waited).toBe("canceling statement due to lock timeout");
    expect(await slugsAfter(PRODUCT_FOR_CREATE_SQL)).toEqual([]);
  });

  it("creates do not wait for each other, and a rename of the project does not wait for a create", async () => {
    const answers = await whileInFlight((c) => c.query(PRODUCT_FOR_CREATE_SQL, ["p1"]), async () => [
      await lockNow(PRODUCT_FOR_CREATE_SQL),
      await lockNow("UPDATE cloud_products SET name = 'Acme 2' WHERE id = $1"),
    ]);
    expect(answers).toEqual(["granted", "granted"]);
  });
});
```
(f) `cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/[environmentRef]/settings/danger/page.test.tsx`: `import userEvent from "@testing-library/user-event";` satırının ALTINA `import { BackendError } from "@palbase/web";` ekle, ve "maintainer: surfaces a toast.error and stays put when delete fails" testinin ALTINA, `describe`'ın içine ekle:
```tsx
  // THE SERVER'S SENTENCE IS THE ANSWER (D-021). `pb.cloudEnvironments.remove`
  // rejects with a `BackendError` whose message is the envelope's
  // `error_description`; the rule and the way out must reach the person as
  // written, not as a generic "Failed to delete environment".
  it("maintainer: a refused delete shows the server's sentence as it came — the rule and the way out", async () => {
    const sentence =
      "main is this project's default environment and cannot be deleted while other environments exist — delete the others first";
    deleteMutation.mutateAsync.mockRejectedValue(
      new BackendError("server", { code: "main_deleted_last", status: 409, message: sentence }),
    );
    renderPage({ maintainer: "ok" });
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /^delete environment$/i }));
    await user.type(await screen.findByTestId("confirm-type-input"), "ref1");
    await user.click(screen.getByTestId("confirm-button"));

    await waitFor(() => {
      expect(toastError).toHaveBeenCalledWith("Could not delete", expect.objectContaining({ description: sentence }));
    });
    expect(routerPush).not.toHaveBeenCalled();
  });
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/environments/cloud-lifecycle.delete-main.test.ts modules/environments/main-delete-lock.pg.test.ts modules/environments/cloud-lifecycle.test.ts modules/environments/teardown.test.ts modules/environments/teardown.jobs.test.ts modules/environments/teardown.object.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - `SyntaxError: Export named 'PRODUCT_FOR_CREATE_SQL' not found in module '…/modules/environments/cloud-lifecycle.ts'.` (`cloud-lifecycle.test.ts`), ve iki dosyada `SyntaxError: Export named 'MAIN_DELETE_LOCK_SQL' not found in module '…/modules/environments/cloud-lifecycle.ts'.`
  - `(fail) a refused delete stops before anything is destroyed (D-021) > row lock, then the deletable check, then the saga — and a refusal starts no step`; farkta `-   "deletable:abc12345m",`.
  - `25 pass` / `4 fail` / `3 errors` / `Ran 29 tests across 6 files.`

  Studio: `(cd cloud/platform/studio && npx vitest run 'src/app/(studio)/projects/[projectId]/environments/[environmentRef]/settings/danger/page.test.tsx')` → `Tests  6 passed (6)`. Bu test bugünkü davranışı KİLİTLER, kırmızı olmaz; ısırdığı Adım 4'te ölçüldü.
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/server/modules/environments/teardown.ts`:
  - `TeardownDeps` içinde `  lockProjectRow: (ref: string) => Promise<void>;` satırının ALTINA ekle:
```ts
  /**
   * REFUSES A DELETE THE PROJECT CANNOT TAKE — before anything is destroyed (D-021).
   *
   * Called right after `lockProjectRow` and before the saga's first step: every
   * compensation below is empty, so a refusal that came after
   * `unpublish-placement` would leave the environment with its routing cut.
   * What is refused is the caller's rule (`CloudLifecycleService.remove`: `main`
   * while other environments exist); it throws the HTTP answer itself and the
   * saga never starts.
   */
  assertDeletable: (ref: string) => Promise<void>;
```
  - `deleteProject` içinde `    await deps.lockProjectRow(ref);` satırının ALTINA ekle:
```ts
    // A REFUSAL COMES BEFORE THE FIRST DESTRUCTIVE STEP (D-021) — after the row
    // lock, so the lock order above holds for the project lock it may take.
    await deps.assertDeletable(ref);
```
(b) `cloud/platform/server/modules/environments/cloud-lifecycle.ts`:
  - T005'in `slugTaken` fonksiyonunun ALTINA ekle:
```ts

/**
 * THE CREATE PATH READS ITS PROJECT UNDER A SHARED LOCK (D-021).
 *
 * `FOR KEY SHARE`, held until the request commits: creates in one project run
 * side by side and a rename of the project (`UPDATE … SET name`) is not held
 * up by them, but a delete of `main` (`MAIN_DELETE_LOCK_SQL`, `FOR UPDATE`)
 * waits for every create in flight, and a create that arrives during it waits
 * for it. Without the pair a create that counted `main` and a delete of `main`
 * that counted no sibling could both commit: a project with environments and
 * no `main`. Measured on PostgreSQL in `main-delete-lock.pg.test.ts`.
 */
export const PRODUCT_FOR_CREATE_SQL = "SELECT id, organization_id, name FROM cloud_products WHERE id = $1 FOR KEY SHARE";

/** A delete of `main` holds its project exclusively until it commits (D-021) — see `PRODUCT_FOR_CREATE_SQL`. */
export const MAIN_DELETE_LOCK_SQL = "SELECT 1 FROM cloud_products WHERE id = $1 FOR UPDATE";

/**
 * The rule and the way out, as every surface prints it (D-021) — under its own
 * code, `main_deleted_last`, not `conflict`: on this route `conflict` already
 * means "a delete of this environment is running — wait and retry"
 * (`DeleteInProgress`; `verify-plane.py` retries it). A caller that deletes a
 * whole project from code (palcore) must tell "retry" from "delete the others
 * first" by the code, the way the CLI classifies answers (the name, not the
 * status).
 */
const MAIN_KEPT =
  "main is this project's default environment and cannot be deleted while other environments exist — delete the others first";
```
  - `createEnvironment`'ın ilk sorgusunu (`const products = (await Database.$query(` ile başlayan, `"SELECT id, organization_id, name FROM cloud_products WHERE id = $1",` taşıyan dört satır) şununla değiştir:
```ts
    // UNDER A SHARED LOCK, FIRST (D-021): a delete of `main` waits for this create.
    const products = (await Database.$query(PRODUCT_FOR_CREATE_SQL, [productId])) as Array<{
      id: string; organization_id: string; name: string;
    }>;
```
  - `remove()`'un teardown bağımlılıklarında `lockProjectRow` girdisinin (`await Database.$query("SELECT 1 FROM cloud_projects WHERE ref = $1 FOR UPDATE", [r]);` taşıyan üç satır) ALTINA ekle:
```ts
          // `main` WITH SIBLINGS IS REFUSED, WHATEVER ASKED (D-021): the CLI, the
          // panel and the API all land on this route.
          assertDeletable: (r) => this.assertDeletable(r),
```
  - `async adopt(ref: string, ownerId: string)` metodunun ÜSTÜNE ekle:
```ts
  /**
   * DELETING `main` WHILE THE PROJECT HAS OTHER ENVIRONMENTS IS REFUSED (D-021).
   *
   * `main` is what every teammate's `palbase.env.release=main` builds against;
   * a project that lost it would ship its release builds nowhere. It may go
   * only as the project's last environment, and the project's next
   * environment is `main` again (`createEnvironment`, FR-102).
   *
   * "Is this `main`" is the create path's own answer: `held()` over the same
   * rows — the stored slug, or the one the backfill will give (FR-105). Any
   * other environment is deleted as before: no project lock, no wait.
   *
   * RACE-SAFE BY THE REQUEST'S ONE TRANSACTION. This takes the project
   * `FOR UPDATE`; every create holds it `FOR KEY SHARE` until it commits. The
   * siblings are read AGAIN once the lock is held, so a create that got there
   * first is counted, and one that arrives later waits for this delete and
   * then finds the project empty. The teardown calls this after its row lock:
   * `cloud_projects` first, as everywhere.
   */
  private async assertDeletable(ref: string): Promise<void> {
    const own = (await Database.$query("SELECT product_id FROM cloud_projects WHERE ref = $1", [ref])) as Array<{
      product_id: string;
    }>;
    const productId = own[0]?.product_id;
    if (productId === undefined) return;
    const project = async () => {
      const rows = (await Database.$query(ENVIRONMENT_SLUGS_OF_PROJECT_SQL, [productId])) as unknown as SlugBackfillRow[];
      return { main: this.environmentSlugService.held(rows).get(FIRST_ENVIRONMENT_SLUG) === ref, environments: rows.length };
    };
    if (!(await project()).main) return;
    await Database.$query(MAIN_DELETE_LOCK_SQL, [productId]);
    const locked = await project();
    if (locked.main && locked.environments > 1) throw new HttpError(409, "main_deleted_last", MAIN_KEPT);
  }
```
(c) `cloud/platform/api/cloud.openapi.yaml`: `/v1/cloud/projects/{ref}` → `delete` altında `"204": { description: Hücredeki ve global katmandaki her iz kaldırıldı }` satırının ALTINA ekle:
```yaml
        "409": { description: "`main_deleted_last` — main cannot be deleted while the project has other environments (D-021): delete the others first, main last. `conflict` — a delete of this environment is already running: wait and retry." }
```
(d) `.github/workflows/cloud-server-typecheck.yml`:
  - `typecheck` listesinde `            modules/environments/cloud-lifecycle.race.test.ts` satırının ALTINA ekle:
```yaml
            # `main` kardeşleri varken silinmez (D-021): kural, dönüş yolu ve kilitten sonra yeniden sayım.
            # Aynı gerekçe: CI'da koşmayan kapı testi kapı değildir.
            modules/environments/cloud-lifecycle.delete-main.test.ts
```
  - `flag-publication-postgres` listesinde `            modules/shared/environment-slug.pg.test.ts` satırının ALTINA ekle:
```yaml
            # D-021 kilit çifti gerçek PostgreSQL'de: yaratma FOR KEY SHARE, main silme FOR UPDATE birbirini bekler.
            modules/environments/main-delete-lock.pg.test.ts
```
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- modules/environments/cloud-lifecycle.delete-main.test.ts modules/environments/main-delete-lock.pg.test.ts modules/environments/cloud-lifecycle.test.ts modules/environments/teardown.test.ts modules/environments/teardown.jobs.test.ts modules/environments/teardown.object.test.ts && npm run typecheck)` · Beklenen: `83 pass` / `0 fail` / `Ran 83 tests across 6 files.`, ve typecheck exit 0. Studio: aynı vitest komutu → `Tests  6 passed (6)`; `(cd cloud/platform/studio && npx tsc --noEmit)` exit 0; `npx eslint '<page.test.tsx>'` çıktısız; `npm run lint:design` → `Studio workspace: color and geometry token checks passed.`

  Komşu süitler aynı noktada değişmedi: `cloud-lifecycle.race.test.ts`, `cloud-environments.controller.test.ts`, `environment-slug.pg.test.ts`, `panel.environment-slug.test.ts`, `saga.test.ts` → `58 pass` / `0 fail` / `Ran 58 tests across 5 files.`

  Testlerin ısırdığı ölçüldü; her değişiklik ölçümden sonra geri alındı:
  - Teardown `assertDeletable`'ı çağırmıyor → sıra testi düştü (`14 pass` / `1 fail`, `teardown.test.ts`).
  - `remove()` kuralı bağlamıyor (`assertDeletable: async () => {},`) → üç ret testi düştü, her biri `Received promise that resolved: Promise { <resolved> }` (`3 pass` / `3 fail`, `cloud-lifecycle.delete-main.test.ts`). Adım 2'nin kırmızısı dışa aktarım `SyntaxError`'ı olduğu için davranışın ısırdığını asıl bu gösterir.
  - Ret kodu `conflict`'e döndü (`throw new Conflict(MAIN_KEPT)`) → "while another environment exists is a 409 …" düştü; fark `-   "error": "main_deleted_last",` / `+ [Conflict: main is this project's default environment …]` (`5 pass` / `1 fail`).
  - Kardeşler kilitten ÖNCE sayılıyor (kilitten sonra yeniden okuma yok) → "a create that committed while the delete waited for the project lock is counted" düştü (`5 pass` / `1 fail`).
  - Her silme proje kilidini alıyor → "any other environment is deleted as before: no project lock, no wait" düştü (`5 pass` / `1 fail`).
  - `locked.environments > 1` → `> 0` → "as the project's last environment goes ahead" düştü (`5 pass` / `1 fail`).
  - `PRODUCT_FOR_CREATE_SQL`'den `FOR KEY SHARE` çıkarıldı → PG'de iki bekleme testi düştü: `Expected: "canceling statement due to lock timeout"` / `Received: "granted"` (`1 pass` / `2 fail`).
  - `FOR KEY SHARE` → `FOR UPDATE` → "creates do not wait for each other, and a rename of the project does not wait for a create" düştü; iki cevap da `canceling statement due to lock timeout` (`2 pass` / `1 fail`).
  - `createEnvironment` kilitsiz ilk sorguyu gönderiyor → "the project is read first, with the shared lock …" düştü (`47 pass` / `1 fail`).
  - Studio: sayfa bir `BackendError`'ın cümlesini "Failed to delete environment" ile değiştiriyor → yalnız yeni test düştü (`Tests  1 failed | 5 passed (6)`). Düz `Error` kullanan eski test bunu göremiyordu.

  Tam süit bu görevden sonra `2279 pass` / `10 fail` / `6 errors` verdi (`Ran 2289 tests across 177 files.`). Kırıklar tabandaki aynı 8 Docker dosyası.
- [ ] **Adım 5: Commit** — `git add cloud/platform/server/modules/environments/teardown.ts cloud/platform/server/modules/environments/teardown.test.ts cloud/platform/server/modules/environments/teardown.jobs.test.ts cloud/platform/server/modules/environments/teardown.object.test.ts cloud/platform/server/modules/environments/cloud-lifecycle.ts cloud/platform/server/modules/environments/cloud-lifecycle.test.ts cloud/platform/server/modules/environments/cloud-lifecycle.delete-main.test.ts cloud/platform/server/modules/environments/main-delete-lock.pg.test.ts cloud/platform/api/cloud.openapi.yaml .github/workflows/cloud-server-typecheck.yml 'cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/[environmentRef]/settings/danger/page.test.tsx' && git commit -m "feat(environments): başka ortamlar varken main silinmez — ret sunucuda, her yoldan, yıkımdan önce; yaratma ile main silme proje kilidiyle sıralanır (D-021)"`

### T007: Geri doldurma operatör fiili — slug ve göç raporu tek ifadede yazılır, `remaining` defterden sayılır; slug'ın tek yazıcısı kapısı
<!-- deps: [T002, T004] | files: [cloud/platform/server/db/public.ts, cloud/platform/server/palbase/palbase-env.d.ts, cloud/platform/server/modules/shared/environment-slug.ts, cloud/platform/server/modules/fleet/cloud-operations.ts, cloud/platform/server/modules/fleet/cloud-operations.test.ts, cloud/platform/server/modules/fleet/cloud-fleet.controller.ts, cloud/platform/server/modules/fleet/cloud-fleet.controller.test.ts, cloud/platform/server/modules/fleet/slug-backfill.pg.test.ts, cloud/platform/server/modules/shared/route-contract.golden, cloud/platform/api/cloud.openapi.yaml, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-105, FR-101, FR-104] -->

**Interfaces:**
- Consumes: `EnvironmentSlugService.planBackfill`, `SlugAssignment`, `SlugBackfillRow`, `SLUG_BACKFILL_RULES` (T004); `flagPostgres` raw indeks desteği (T002)
- Produces:
  - `POST /v1/cloud/environments/slugs/backfill` (operatör, `CloudFleetController.backfillEnvironmentSlugs`) → `EnvironmentSlugBackfillResult = { assigned: SlugAssignment[]; remaining: number }`.
  - `assigned`, göç raporunun ta kendisidir.
  - `remaining`, yazımlardan SONRA hâlâ slug'ı olmayan ortam sayısıdır. Plandan çıkarılmaz; defterden sayılır.
  - `export const ENVIRONMENT_SLUG_BACKFILL_SQL`, `export const ENVIRONMENTS_WITHOUT_SLUG_SQL = "SELECT count(*)::int AS n FROM cloud_projects WHERE slug IS NULL"` (`cloud-operations.ts`). T012'nin kapısı bu ifadeyi koşar.
  - Tablo `cloud_environment_slug_backfills (ref PK, product_id, previous_name, previous_directory, slug, rule CHECK IN SLUG_BACKFILL_RULES, created_at)`.
  - `CloudOperationsService.backfillEnvironmentSlugs()`.

Bu depoda göç dosyası yok. Veri düzeltmesinin emsali operatör fiilidir: `POST /sealed/backfill`, `POST /projects/reconcile-organizations` → sonra `product_id` NOT NULL. SDK'nın `backfills:` DSL'i bu depoda hiç kullanılmamış. Kurallar (eski ad + NFKD türetme + numaralama) TS'te; bir SQL kopyası ikinci bir gerçek olurdu.

Her yazım tek ifadedir. `UPDATE … WHERE slug IS NULL RETURNING` CTE'sinden rapor satırı `INSERT` edilir. Bunun üç sonucu var:
- Rapor satırı her zaman yazılmış bir slug demektir.
- İki operatör birbirini ezemez.
- İkinci koşu hiçbir şey yazmaz.

**`remaining` KAPIDIR** (T012 → T013). Mühür geri doldurmasının `rows.length - minted.length` deyimiyle hesaplansaydı, fiil ledger'ı okuduktan SONRA slug'sız yazılan bir satırı göremezdi. Böyle bir satırı, deploy sırasında hâlâ eski kodu koşan bir örnek yazabilir. `remaining: 0` o zaman yalan söylerdi. Sonraki NOT NULL deploy'u da "column contains null values" ile düşerdi. Bu gerçek Postgres'te ölçüldü: `Expected: 1` / `Received: 0`.

**FR-101 "değişmez", FR-104 kapısı.** Ortam satırı slug'la doğar (T003/T005'in INSERT'i). Sonrasında slug'ı set edebilecek TEK ifade bu fiilinkidir, o da yalnız NULL olan yerde. Kapı AST ile ölçülür; `cellcage.test.ts`'in `cage_passed` tek-yazıcı kapısıyla aynı deyim.

Mevcut `cloud-fleet.controller.test.ts` rota listesini ("dört rota") tutuyor. Şartname bir rota eklediği için o test BİLEREK beşe çıkarılır.

- [ ] **Adım 1: Kırmızı testi yaz** — (a) `cloud/platform/server/modules/fleet/cloud-operations.test.ts`: `import { CloudOperationsService } from "./cloud-operations.ts";` satırını `import { CloudOperationsService, ENVIRONMENTS_WITHOUT_SLUG_SQL } from "./cloud-operations.ts";` yap ve dosyanın SONUNA ekle:
```ts

/**
 * THE SLUG BACKFILL WRITES THE PLAN AND ITS REPORT TOGETHER (FR-105).
 *
 * The plan itself is `shared/environment-slug.backfill.test.ts`; this is the part
 * that touches the ledger. One statement per environment sets the slug only
 * where it is still NULL AND writes the report row from what that UPDATE
 * returned — a report row can never exist for a slug that was not written.
 */
describe("CloudOperationsService — environment slug backfill (FR-105)", () => {
  const ledger = [
    { ref: "cli01ref", product_id: "proj_1", product_name: "todoapp", name: "todoapp", slug: null, created_at: "2026-08-01T00:00:00Z" },
    { ref: "featx1ref", product_id: "proj_1", product_name: "todoapp", name: "Feature X", slug: null, created_at: "2026-08-02T00:00:00Z" },
  ];
  const writes = () => queries.filter((q) => q.sql.includes("INSERT INTO cloud_environment_slug_backfills"));

  it("gives every environment its slug and reports old directory → slug", async () => {
    answers.push({ match: /INSERT INTO cloud_environment_slug_backfills/, rows: [{ ref: "written" }] });
    answers.push({ match: /WHERE p\.product_id IN \(SELECT product_id FROM cloud_projects WHERE slug IS NULL\)/, rows: ledger });
    answers.push({ match: /count\(\*\)::int AS n FROM cloud_projects WHERE slug IS NULL/, rows: [{ n: 0 }] });
    expect(await run(() => service.backfillEnvironmentSlugs())).toEqual({
      assigned: [
        { ref: "cli01ref", product_id: "proj_1", previous_name: "todoapp", previous_directory: "main", slug: "main", rule: "first" },
        { ref: "featx1ref", product_id: "proj_1", previous_name: "Feature X", previous_directory: "Feature X", slug: "Feature-X", rule: "derived" },
      ],
      remaining: 0,
    });
    expect(writes().map((q) => q.params)).toEqual([
      ["cli01ref", "main", "todoapp", "main", "first"],
      ["featx1ref", "Feature-X", "Feature X", "Feature X", "derived"],
    ]);
    expect(writes()[0]?.sql).toContain("SET slug = $2 WHERE ref = $1 AND slug IS NULL");
  });

  it("a row another run filled in between is not reported as ours", async () => {
    answers.push({ match: /WHERE p\.product_id IN \(SELECT product_id FROM cloud_projects WHERE slug IS NULL\)/, rows: ledger });
    answers.push({ match: /count\(\*\)::int AS n FROM cloud_projects WHERE slug IS NULL/, rows: [{ n: 0 }] });
    expect(await run(() => service.backfillEnvironmentSlugs())).toEqual({ assigned: [], remaining: 0 });
  });

  it("`remaining` is what the ledger still holds without a slug AFTER the writes — counted, not inferred from the plan", async () => {
    // It is the go signal for the NOT NULL contract: every planned row was
    // written, yet one environment is still without a slug (see
    // `slug-backfill.pg.test.ts` for how that happens on a real Postgres).
    answers.push({ match: /INSERT INTO cloud_environment_slug_backfills/, rows: [{ ref: "written" }] });
    answers.push({ match: /WHERE p\.product_id IN \(SELECT product_id FROM cloud_projects WHERE slug IS NULL\)/, rows: ledger });
    answers.push({ match: /count\(\*\)::int AS n FROM cloud_projects WHERE slug IS NULL/, rows: [{ n: 1 }] });
    expect((await run(() => service.backfillEnvironmentSlugs())).remaining).toBe(1);
    expect(queries.at(-1)?.sql).toBe(ENVIRONMENTS_WITHOUT_SLUG_SQL);
  });

  it("a project whose first environment cannot be main stops the run before any write", async () => {
    answers.push({ match: /WHERE p\.product_id IN \(SELECT product_id FROM cloud_projects WHERE slug IS NULL\)/, rows: [
      ledger[0],
      { ...ledger[1], slug: "main" },
    ] });
    await expect(run(() => service.backfillEnvironmentSlugs())).rejects.toThrow(`"main" already belongs to featx1ref`);
    expect(writes()).toEqual([]);
  });
});

/**
 * FR-101/FR-104: A SLUG, ONCE WRITTEN, NEVER CHANGES.
 *
 * Every teammate's `palbase/environments/<slug>/`, every Android build type and
 * every `--env` name hangs on it. The environment row is born with it (the
 * INSERT in `cloud-lifecycle.ts`); after that the ONLY statement that may set
 * it is the backfill's, and only where it is still NULL. Measured on the AST,
 * as `cellcage.test.ts` measures `cage_passed`'s single writer: comments and
 * type declarations are not writes.
 */
describe("FR-101/FR-104: a slug, once written, never changes", () => {
  it("the only statement in the modules that sets a slug is the backfill's, and it sets only a missing one", async () => {
    const { readdirSync, readFileSync, statSync } = await import("node:fs");
    const { join } = await import("node:path");
    const ts = (await import("typescript")).default;
    const root = new URL("..", import.meta.url).pathname;
    const UPDATERS = ["update", "$update", "put", "$put", "supersede", "$supersede", "updateWhere"];
    // `SET` … `slug =` before any `WHERE`: a slug in the WHERE clause is a read.
    const sqlSetsSlug = (text: string) =>
      /\bUPDATE\s+(?:public\.)?cloud_projects\b/i.test(text) && /\bSET\b(?:(?!\bWHERE\b)[\s\S])*\bslug\s*=/i.test(text);
    const writesIn = (file: string, src: string): string[] => {
      const sf = ts.createSourceFile(file, src, ts.ScriptTarget.Latest, true);
      const found: string[] = [];
      const hasSlugKey = (a: import("typescript").Node) => ts.isObjectLiteralExpression(a) && a.properties.some(
        (p) => p.name !== undefined && p.name.getText(sf).replace(/["'`]/g, "") === "slug",
      );
      const visit = (n: import("typescript").Node) => {
        if ((ts.isStringLiteralLike(n) || ts.isTemplateExpression(n)) && sqlSetsSlug(n.getText(sf))) {
          found.push(n.getText(sf).replace(/\s+/g, " "));
        }
        if (ts.isCallExpression(n) && ts.isPropertyAccessExpression(n.expression) && UPDATERS.includes(n.expression.name.text)) {
          const target = n.expression.expression;
          const onProjects = (ts.isPropertyAccessExpression(target) && target.name.text === "cloud_projects") ||
            (n.arguments[0] !== undefined && ts.isStringLiteralLike(n.arguments[0]) && n.arguments[0].text === "cloud_projects");
          if (onProjects && n.arguments.some(hasSlugKey)) found.push(n.getText(sf).replace(/\s+/g, " "));
        }
        ts.forEachChild(n, visit);
      };
      visit(sf);
      return found;
    };
    // The gate is not blind: it sees each way of writing, and no read.
    expect(writesIn("a.ts", "q(`UPDATE cloud_projects SET name = $2, slug = $3 WHERE ref = $1`)")).toHaveLength(1);
    expect(writesIn("a.ts", `Database.$update("cloud_projects", ref, { slug: "x" })`)).toHaveLength(1);
    expect(writesIn("a.ts", `Database.public.cloud_projects.update(ref, { "slug": "x" })`)).toHaveLength(1);
    expect(writesIn("a.ts", `q("UPDATE cloud_projects SET name = $2 WHERE ref = $1 AND slug = $3")`)).toEqual([]);
    expect(writesIn("a.ts", `q("SELECT ref FROM cloud_projects WHERE lower(slug) = lower($1)")`)).toEqual([]);
    expect(writesIn("a.ts", `// UPDATE cloud_projects SET slug = 'x'\ntype R = { slug: string }`)).toEqual([]);

    const writers: Array<[string, string[]]> = [];
    const walk = (dir: string) => {
      for (const e of readdirSync(dir)) {
        const p = join(dir, e);
        if (statSync(p).isDirectory()) { walk(p); continue; }
        if (!p.endsWith(".ts") || p.endsWith(".test.ts")) continue;
        const found = writesIn(p, readFileSync(p, "utf8"));
        if (found.length > 0) writers.push([p.slice(root.length), found]);
      }
    };
    walk(root);
    expect(writers).toEqual([
      ["fleet/cloud-operations.ts", [expect.stringContaining("UPDATE cloud_projects SET slug = $2 WHERE ref = $1 AND slug IS NULL")]],
    ]);
  });
});
```
(b) `cloud/platform/server/modules/fleet/cloud-fleet.controller.test.ts`. Bu mevcut test BİLEREK değişir:
  - `it("dört rota mount edilmiştir", …)` başlığını `it("beş rota mount edilmiştir", …)` yap.
  - Beklenen listeye `"GET /jobs/{jobId} job",` satırının ALTINA `"POST /environments/slugs/backfill backfillEnvironmentSlugs",` ekle.
  - `it("listede olmayan hesap dört rotanın hiçbirinden geçemez ve veritabanına inilmez", …)` başlığını `it("listede olmayan hesap beş rotanın hiçbirinden geçemez ve veritabanına inilmez", …)` yap.
  - `calls` dizisine `() => controller.redeclareSealedIdentity(user),` satırının ALTINA `() => controller.backfillEnvironmentSlugs(user),` ekle.
  - `describe("CloudFleetController — listedeki operatör servise iner", …)` içinde `it("mühür backfill'i kimliksiz kiracıları okur", …)` testinin ALTINA ekle:
```ts

  it("the slug backfill reads the projects that still have environments without a slug, then counts what is left", async () => {
    expect(await run(() => controller.backfillEnvironmentSlugs(operator))).toEqual({ assigned: [], remaining: 0 });
    expect(queries.map((q) => q.sql)).toEqual([
      expect.stringContaining("WHERE p.product_id IN (SELECT product_id FROM cloud_projects WHERE slug IS NULL)"),
      expect.stringContaining("count(*)::int AS n FROM cloud_projects WHERE slug IS NULL"),
    ]);
  });
```
(c) `cloud/platform/server/modules/fleet/slug-backfill.pg.test.ts` (yeni dosya):
```ts
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "bun:test";
import { fakeDatabase, isolated, withServices } from "@palbase/backend/test";
import { flagPostgres } from "../shared/flags-postgres.ts";
import { CloudOperationsService } from "./cloud-operations.ts";

/**
 * THE SLUG BACKFILL AGAINST A REAL POSTGRES (FR-105).
 *
 * The service's own SQL runs here: the read with its `IN (…)` and ordering, and
 * the one-statement write whose report row can only exist if its UPDATE hit.
 * A fake answers whatever it is told; only Postgres can say the CTE is legal,
 * the parameters type, the unique index holds and a second run writes nothing.
 * The three tables — the slug CHECK, the case-folding unique index and the
 * report's rule CHECK included — are rendered from the JSON the schema rail
 * receives (`toSchemaJSON`), not retyped here.
 */
const fixture = flagPostgres(["cloud_products", "cloud_projects", "cloud_environment_slug_backfills"]);
beforeAll(fixture.initialize);
afterAll(fixture.close);

const db = Object.assign(fakeDatabase().raw, { query: (sql: string, params: unknown[] = []) => fixture.query(sql, params) }) as never;
const service = isolated().get(CloudOperationsService);
const backfill = () => withServices({ Database: db }, () => service.backfillEnvironmentSlugs());
const lines = async (sql: string) => (await fixture.query(sql)).map((r) => String(Object.values(r)[0]));

let slot = 0;
/** One environment as it exists before the deploy — or, with `slug`, one created after it. */
const environment = (ref: string, productId: string, name: string, createdAt: string, slug: string | null = null) =>
  fixture.query(
    `INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, name, slug, created_at)
     VALUES ($1, $2, 'cell-01', 'Running', $3, $4, $5, $6::timestamptz)`,
    [ref, ++slot, productId, name, slug, createdAt],
  );

beforeEach(async () => {
  await fixture.reset();
  await fixture.query(
    `INSERT INTO cloud_products (id, organization_id, name, created_by)
     VALUES ('proj_cli', 'org_1', 'todoapp', 'usr_1'), ('proj_panel', 'org_1', 'Shop', 'usr_1')`,
  );
  await environment("cli01ref", "proj_cli", "todoapp", "2026-08-01");
  await environment("twinA1ref", "proj_cli", "Staging", "2026-08-02");
  await environment("twinB1ref", "proj_cli", "staging", "2026-08-03");
  await environment("pnl01ref", "proj_panel", "Production", "2026-08-04");
  await environment("pnl02ref", "proj_panel", "Feature X", "2026-08-05");
});

describe("slug backfill on a real PostgreSQL (FR-105)", () => {
  it("gives every environment a slug, answers the migration report and keeps it in the ledger", async () => {
    const result = await backfill();
    expect(result.remaining).toBe(0);
    expect(result.assigned.map((a) => `${a.previous_directory} -> ${a.slug} (${a.rule})`)).toEqual([
      "main -> main (first)", "Staging -> Staging (kept)", "staging -> staging-2 (suffixed)",
      "Production -> main (first)", "Feature X -> Feature-X (derived)",
    ]);
    expect(await lines("SELECT ref || '=' || slug FROM cloud_projects ORDER BY ref")).toEqual([
      "cli01ref=main", "pnl01ref=main", "pnl02ref=Feature-X", "twinA1ref=Staging", "twinB1ref=staging-2",
    ]);
    expect(await lines(
      "SELECT previous_directory || ' -> ' || slug || ' (' || rule || ')' FROM cloud_environment_slug_backfills ORDER BY ref",
    )).toEqual([
      "main -> main (first)", "Production -> main (first)", "Feature X -> Feature-X (derived)",
      "Staging -> Staging (kept)", "staging -> staging-2 (suffixed)",
    ]);
  });

  it("a second run writes nothing and reports nothing", async () => {
    await backfill();
    expect(await backfill()).toEqual({ assigned: [], remaining: 0 });
    expect(await lines("SELECT count(*) FROM cloud_environment_slug_backfills")).toEqual(["5"]);
  });

  it("a slug written after the deploy is left alone and the older row gives way", async () => {
    await environment("new01ref", "proj_panel", "qa", "2026-09-27", "qa");
    await environment("oldqa1ref", "proj_panel", "QA", "2026-08-06");
    await backfill();
    expect(await lines("SELECT ref || '=' || slug FROM cloud_projects WHERE product_id = 'proj_panel' ORDER BY ref")).toEqual([
      "new01ref=qa", "oldqa1ref=QA-2", "pnl01ref=main", "pnl02ref=Feature-X",
    ]);
  });
});

describe("`remaining` is the gate for the NOT NULL contract (FR-105)", () => {
  it("an environment written without a slug while the run was planning is counted, not reported as done", async () => {
    // An instance still on the pre-slug code (a rolling deploy) inserts a row
    // AFTER the backfill read the ledger. `remaining: 0` would send the
    // operator to the contract deploy, which then fails on this row.
    const racing = Object.assign(fakeDatabase().raw, {
      query: async (sql: string, params: unknown[] = []) => {
        const rows = await fixture.query(sql, params);
        if (sql.includes("WHERE p.product_id IN (SELECT product_id FROM cloud_projects WHERE slug IS NULL)")) {
          await environment("late01ref", "proj_panel", "late", "2026-09-28");
        }
        return rows;
      },
    }) as never;
    const result = await withServices({ Database: racing }, () => service.backfillEnvironmentSlugs());
    expect(await lines("SELECT count(*) FROM cloud_projects WHERE slug IS NULL")).toEqual(["1"]);
    expect(result.remaining).toBe(1);
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/fleet/cloud-operations.test.ts modules/fleet/cloud-fleet.controller.test.ts modules/fleet/slug-backfill.pg.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - `error: Missing canonical fixture table cloud_environment_slug_backfills`
  - `(fail) CloudFleetController — operatör rotaları fleet alanında (FR-412) > beş rota mount edilmiştir`
  - `TypeError: controller.backfillEnvironmentSlugs is not a function.`
  - `SyntaxError: Export named 'ENVIRONMENTS_WITHOUT_SLUG_SQL' not found in module '…/modules/fleet/cloud-operations.ts'.`
  - `9 pass` / `5 fail` / `1 error` / `Ran 14 tests across 3 files.`
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/server/db/public.ts`: `const cloud_products = defineTable("cloud_products", {` satırının ÜSTÜNE ekle:
```ts
// THE SLUG BACKFILL'S REPORT (FR-105) — one row per environment the backfill
// gave a slug: the directory `palbase link` wrote for it before, the slug it
// has now, and why. It is the migration note's source ("Production/ → main/"),
// and it is written in the SAME statement as the slug, so a row here always
// means the slug was written.
const cloud_environment_slug_backfills = defineTable("cloud_environment_slug_backfills", {
  columns: {
    ref: text().primaryKey(),
    product_id: text().notNull(),
    previous_name: text().nullable(),
    previous_directory: text().notNull(),
    slug: text().notNull(),
    rule: text().notNull(),
    created_at: timestamp().defaultNow(),
  },
  checks: [{
    name: "cloud_environment_slug_backfills_rule",
    expr: `rule IN (${SLUG_BACKFILL_RULES.map((r) => `'${r}'`).join(", ")})`,
  }],
  policies: () => [policy("cloud_environment_slug_backfills_service_only").for("all").to("service_role").using("true")],
});

```
Ardından `export default defineSchema("public", {` listesinde `cloud_products,` satırının ALTINA `    cloud_environment_slug_backfills,` ekle.
(b) `cloud/platform/server/palbase/palbase-env.d.ts` (üreticinin biçiminde elle): `cloud_products` bloğunun kapanışından (`uniqueKeys: [["id"]];` `};`) sonra, `cloud_account_emails: {`'ten önce ekle:
```ts
    cloud_environment_slug_backfills: {
      row: {
        ref: Pg<string, "text">;
        product_id: Pg<string, "text">;
        previous_name: Pg<string, "text"> | null;
        previous_directory: Pg<string, "text">;
        slug: Pg<string, "text">;
        rule: Pg<string, "text">;
        created_at: Pg<string, "timestamp">;
        /** Names of this table's UPDATE policies whose `using` is true for THIS row and caller — an AFFORDANCE for the interface (draw or hide a control); never an authorization decision, may be incomplete, the server decides again on every request. */
        can: never[];
      };
      insert: {
        ref: Pg<string, "text">;
        product_id: Pg<string, "text">;
        previous_name?: Pg<string, "text"> | null;
        previous_directory: Pg<string, "text">;
        slug: Pg<string, "text">;
        rule: Pg<string, "text">;
        created_at?: Pg<string, "timestamp">;
      };
      relations: {};
      uniqueKeys: [["ref"]];
    };
```
(c) `cloud/platform/server/modules/shared/environment-slug.ts`: `export type SlugAssignment = z.infer<typeof SlugAssignment>;` satırının ALTINA ekle:
```ts

/**
 * What the operator verb answers: every assignment it WROTE, and how many
 * environments are still without a slug once it is done — counted in the
 * ledger after the writes, not inferred from the plan, because `remaining: 0`
 * is what lets the column become NOT NULL.
 */
export const EnvironmentSlugBackfillResult = z.object({
  assigned: z.array(SlugAssignment),
  remaining: z.number().int(),
});
export type EnvironmentSlugBackfillResult = z.infer<typeof EnvironmentSlugBackfillResult>;
```
(d) `cloud/platform/server/modules/fleet/cloud-operations.ts`: `import { SealedBackfillResult, SealedRedeclareResult } from "../identity/backfill";` satırının ALTINA ekle:
```ts
import {
  type EnvironmentSlugBackfillResult, EnvironmentSlugService, type SlugAssignment, type SlugBackfillRow,
} from "../shared/environment-slug";
```
`import { JobStatusSchema } from "./ops";` satırının ALTINA ekle:
```ts

/**
 * Every environment of every project that still has one without a slug — the
 * whole project, because the plan needs the slugs that already exist to know
 * which names are taken. `slug-backfill.pg.test.ts` runs it, through the
 * service, on a real Postgres.
 */
export const ENVIRONMENT_SLUG_BACKFILL_SQL = `
  SELECT p.ref, p.product_id, pr.name AS product_name, p.name, p.slug, p.created_at
    FROM cloud_projects p
    LEFT JOIN cloud_products pr ON pr.id = p.product_id
   WHERE p.product_id IN (SELECT product_id FROM cloud_projects WHERE slug IS NULL)
   ORDER BY p.product_id, p.created_at ASC NULLS LAST, p.ref`;

/**
 * How many environments still have no slug. The backfill answers it as
 * `remaining`, and the NOT NULL contract ships only when it is 0 on
 * production — this exact statement, run by the operator.
 */
export const ENVIRONMENTS_WITHOUT_SLUG_SQL = "SELECT count(*)::int AS n FROM cloud_projects WHERE slug IS NULL";
```
Kurucuda `private readonly cellClientService: CellClientService,` satırının ALTINA `private readonly environmentSlugService: EnvironmentSlugService,` ekle. `async redeclareSealedIdentity(): Promise<SealedRedeclareResult> {` satırının ÜSTÜNE ekle:
```ts
  /**
   * GIVES EVERY ENVIRONMENT BORN BEFORE THE SLUG COLUMN ITS SLUG (FR-105).
   *
   * The decision is `EnvironmentSlugService.planBackfill` — the SAME rules the
   * create path uses, in the same language; a SQL copy of them would be a
   * second truth. Each write sets the slug only where it is still NULL and
   * inserts the report row from what that UPDATE returned, in one statement:
   * two operators running at once cannot overwrite each other, and a report
   * row always means a slug. Idempotent: a second run finds nothing to do.
   */
  async backfillEnvironmentSlugs(): Promise<EnvironmentSlugBackfillResult> {
    const rows = (await Database.$query(ENVIRONMENT_SLUG_BACKFILL_SQL)) as unknown as SlugBackfillRow[];
    const plan = this.environmentSlugService.planBackfill(rows);
    const assigned: SlugAssignment[] = [];
    for (const a of plan) {
      const wrote = (await Database.$query(
        `WITH assigned AS (
           UPDATE cloud_projects SET slug = $2 WHERE ref = $1 AND slug IS NULL RETURNING ref, product_id
         )
         INSERT INTO cloud_environment_slug_backfills (ref, product_id, previous_name, previous_directory, slug, rule)
         SELECT ref, product_id, $3::text, $4::text, $2, $5::text FROM assigned
         RETURNING ref`,
        [a.ref, a.slug, a.previous_name, a.previous_directory, a.rule],
      )) as Array<{ ref: string }>;
      if (wrote.length > 0) assigned.push(a);
    }
    // COUNTED IN THE LEDGER, AFTER THE WRITES — not `plan.length - assigned.length`
    // as the sealed-identity backfill answers. An environment written without a
    // slug while this run was planning (an instance still on the pre-slug code,
    // mid-deploy) is in no plan, and a `remaining: 0` that missed it would send
    // the operator to a NOT NULL deploy that fails on it.
    const [left] = (await Database.$query(ENVIRONMENTS_WITHOUT_SLUG_SQL)) as Array<{ n: number }>;
    return { assigned, remaining: Number(left?.n ?? 0) };
  }

```
(e) `cloud/platform/server/modules/fleet/cloud-fleet.controller.ts`: `import { SealedBackfillResult, SealedRedeclareResult } from "../identity/backfill";` satırının ALTINA `import { EnvironmentSlugBackfillResult } from "../shared/environment-slug";` ekle. Bu bir değer import'udur: `type` niteleyicisi bundle'da sembolü siliyor (dosyanın kendi yorumu). `backfillSealedIdentity` metodunun ALTINA ekle:
```ts

  /**
   * GIVES EVERY ENVIRONMENT BORN BEFORE SLUGS ITS SLUG — operator verb (FR-105).
   *
   * Run once after the deploy that adds `cloud_projects.slug`, and again until
   * it answers `remaining: 0`; only then can the column become NOT NULL. The
   * answer is the migration report (old directory → slug, and why), and the
   * same rows are kept in `cloud_environment_slug_backfills`.
   *
   * IDEMPOTENT: an environment that has a slug is never touched.
   */
  @Post("/environments/slugs/backfill", { auth: { required: true } })
  async backfillEnvironmentSlugs(@User() user: { id: string }): Promise<EnvironmentSlugBackfillResult> {
    this.operatorsService.assertOperator(user);
    return this.cloudOperationsService.backfillEnvironmentSlugs();
  }
```
(f) `cloud/platform/server/modules/shared/route-contract.golden`: `POST /v1/classify/publish false {}` satırının ALTINA `POST /v1/cloud/environments/slugs/backfill {"required":true} {}` ekle.
(g) `cloud/platform/api/cloud.openapi.yaml`: `/v1/cloud/sealed/redeclare:` yolunun ÜSTÜNE ekle:
```yaml
  /v1/cloud/environments/slugs/backfill:
    post:
      tags: [control]
      operationId: backfillEnvironmentSlugs
      summary: Give every environment created before slugs its slug — operator only
      description: >-
        Environments created before `slug` existed have none. The project's first
        environment gets `main`; every other keeps the directory `palbase link`
        writes for it today when that is a valid, free slug, and otherwise gets
        the derived slug or the first free numbered one. The decision is the
        server's own derivation, not a script's. Each slug is written only where
        none exists, together with its report row, so two operators cannot
        overwrite each other and a second run does nothing. Run until
        `remaining` is 0.
      responses:
        "200":
          description: The migration report — old directory to slug, and why
          content:
            application/json:
              schema:
                type: object
                required: [assigned, remaining]
                properties:
                  assigned:
                    type: array
                    items:
                      type: object
                      required: [ref, product_id, previous_name, previous_directory, slug, rule]
                      properties:
                        ref: { type: string }
                        product_id: { type: string }
                        previous_name: { type: string, nullable: true }
                        previous_directory: { type: string }
                        slug: { type: string }
                        rule: { type: string, enum: [first, kept, derived, suffixed] }
                  remaining:
                    type: integer
                    description: Environments still without a slug, counted after this run's writes
        "403": { description: Not an operator }

```
(h) `.github/workflows/cloud-server-typecheck.yml`:
  - `typecheck` listesinde `modules/identity/cloud-identity.controller.test.ts modules/fleet/cloud-fleet.controller.test.ts` satırının ALTINA `            modules/fleet/cloud-operations.test.ts` ekle. Bu dosya hiçbir listede yoktu.
  - `flag-publication-postgres` listesinde T002'nin `modules/shared/environment-slug.pg.test.ts` satırının ALTINA `            modules/fleet/slug-backfill.pg.test.ts` ekle.
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- modules/fleet/cloud-operations.test.ts modules/fleet/cloud-fleet.controller.test.ts modules/fleet/slug-backfill.pg.test.ts modules/shared/route-contract.test.ts && npm run typecheck)` · Beklenen: `30 pass` / `0 fail` / `Ran 30 tests across 4 files.`, ve typecheck exit 0. Görevin kendi üç dosyası: `27 pass` / `0 fail` / `Ran 27 tests across 3 files.`

  `remaining` testlerinin ısırdığı da ölçüldü. `return { assigned, remaining: plan.length - assigned.length };` mutasyonunda PG testi `Expected: 1` / `Received: 0` ile düştü. İki sahte-veritabanı testi de düştü: `12 pass` / `3 fail`. Mutasyon geri alındı.
- [ ] **Adım 5: Commit** — `git add .github/workflows/cloud-server-typecheck.yml cloud/platform/api/cloud.openapi.yaml cloud/platform/server/db/public.ts cloud/platform/server/palbase/palbase-env.d.ts cloud/platform/server/modules/shared/environment-slug.ts cloud/platform/server/modules/fleet/cloud-operations.ts cloud/platform/server/modules/fleet/cloud-operations.test.ts cloud/platform/server/modules/fleet/cloud-fleet.controller.ts cloud/platform/server/modules/fleet/cloud-fleet.controller.test.ts cloud/platform/server/modules/fleet/slug-backfill.pg.test.ts cloud/platform/server/modules/shared/route-contract.golden && git commit -m "feat(fleet): slug geri doldurma operatör fiili — slug ve göç raporu tek ifadede yazılır, kalan defterden sayılır (FR-105)"`

---

### T008: Ortam kartı slug'ı görünen adın yanında gösterir
<!-- deps: [T006] | files: [cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/env-card.tsx, cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/page.test.tsx] | satisfies: [FR-107] -->

**Interfaces:**
- Consumes: panel ortam satırının `slug` alanı (T006). T016'dan sonra adsız bir satırın `name`'i de slug'dır.
- Produces: `EnvCard` prop tipi `env: { ref; name; slug: string; status; created_at?; url? }`. `slug` ZORUNLU.

Panel slug'ı T006'dan beri `GET /v1/panel/projects/{id}/environments` cevabında taşıyor. Studio tipleri de hazır: `EnvironmentRow.slug: string` ve `PanelListEnvironmentsResponseItem.slug`. Eksik olan kartın onu göstermesi. Panelden açılmış bir projede kişi `Production` görüyor; `palbase.env.release=main` yazması gerektiğini hiçbir yer söylemiyor.

Kart artık başlıktaki adın hemen altında `Slug` satırını basar. Mevcut sayfa testinin fikstürleri `slug` alanını kazanır, çünkü panel her satırda gönderiyor. Hiçbir iddia gevşetilmez.

Deploy notu: geri doldurulmamış bir satırda panel `slug` olarak ref döner (T006). Bu yüzden Studio, T012'nin operatör koşusundan SONRA deploy edilir.

- [ ] **Adım 1: Kırmızı testi yaz** — `cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/page.test.tsx` üzerinde:
  - İmport satırı `import { render, screen } from "@testing-library/react";` → `import { render, screen, within } from "@testing-library/react";`.
  - `state.envs` fikstüründe `name: "Production",`'dan sonra `slug: "main",`, ve `name: "Staging",`'den sonra `slug: "staging",` ekle.
  - `it("keeps creation disabled for users outside the billing account", …)` satırının ÖNÜNE ekle:
```tsx
 it("shows each environment's slug beside its display name — the name `palbase link`, build types and --env use (FR-107)", () => { render(<Page />); const production = screen.getByRole("heading", { name: "Production" }).closest("section")!; expect(within(production).getByText("Slug").nextElementSibling).toHaveTextContent(/^main$/); const staging = screen.getByRole("heading", { name: "Staging" }).closest("section")!; expect(within(staging).getByText("Slug").nextElementSibling).toHaveTextContent(/^staging$/); });
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/studio && npx vitest run "src/app/(studio)/projects/[projectId]/environments/page.test.tsx")` · Beklenen: **FAIL**, çıktıda `TestingLibraryElementError: Unable to find an element with the text: Slug.` ve `Tests  1 failed | 6 passed (7)`.
- [ ] **Adım 3: Uygula** — `cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/env-card.tsx`: `export function EnvCard({ env, projectId }: { env: { ref: string; name: string; status: string; created_at?: string; url?: string }; projectId: string }) {` satırını şununla değiştir:
```tsx
// THE SLUG SITS BESIDE THE NAME (FR-107): the name is what people call the
// environment and may be renamed; the slug is its directory on every checkout,
// its Android build type and the `--env` value, and it never changes.
export function EnvCard({ env, projectId }: { env: { ref: string; name: string; slug: string; status: string; created_at?: string; url?: string }; projectId: string }) {
```
ve `<dl className="grid gap-(--layout-field-gap)"><dt className="text-(--color-text-muted)">Environment reference</dt>` başlangıcını şununla değiştir:
```tsx
<dl className="grid gap-(--layout-field-gap)"><dt className="text-(--color-text-muted)">Slug</dt><dd className="font-mono break-all">{env.slug}</dd><dt className="text-(--color-text-muted)">Environment reference</dt>
```
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/studio && npx vitest run "src/app/(studio)/projects/[projectId]/environments/" && npx tsc --noEmit)` · Beklenen: `Test Files  77 passed (77)` ve `Tests  636 passed (636)`; tsc exit 0.
- [ ] **Adım 5: Commit** — `git add "cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/env-card.tsx" "cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/page.test.tsx" && git commit -m "feat(studio): ortam kartı slug'ı görünen adın yanında gösterir (FR-107)"`

---

### T009: Studio'nun slug önizlemesi sunucunun kuralıdır — kopya sunucunun testine bağlı
<!-- deps: [T001, T006] | files: [cloud/platform/studio/src/lib/environment-slug.ts, cloud/platform/server/modules/shared/environment-slug.studio.test.ts, cloud/platform/server/db/public.ts, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-107] -->

**Interfaces:**
- Consumes: `ENVIRONMENT_SLUG_PATTERN` (`db/public.ts`, T001) · `EnvironmentSlugService.derive`, `.problemWith(slug, false)` (T001) · iş akışı çıpası T006'nın satırı.
- Produces:
  - `cloud/platform/studio/src/lib/environment-slug.ts`:
    - `ENVIRONMENT_SLUG_PATTERN: string`.
    - `deriveEnvironmentSlug(name: string): string` (`""` = türetilemedi).
    - `environmentSlugProblem(slug: string): string | null`. Sunucunun `problemWith(slug, false)` cümlelerini verir; `main`/`Main` için `null`.
  - Kilit: `cloud/platform/server/modules/shared/environment-slug.studio.test.ts`.

Oluşturma formu slug'ı kişi yazarken, istek gitmeden göstermeli (FR-107). Tarayıcı `EnvironmentSlugService`'i çağıramaz, yani Studio kuralın bir kopyasını taşır. Kayan bir kopya bir dizin gösterip başka birini yaratır.

Bu yüzden kopya **sunucunun test süitine bağlanır**. Sunucu testi Studio dosyasını doğrudan yükler ve iki uygulamayı aynı adlar üzerinde koşar:
- dilbilgisi metni `ENVIRONMENT_SLUG_PATTERN` ile birebir aynı,
- türetme 20 ad üzerinde aynı,
- ret cümleleri aynı.

`main`'i Studio yargılamaz. İlk ortam için serbesttir, diğerleri için ayrılmıştır, ve hangisinin yaratıldığını yalnız sunucu bilir.

Studio dosyası import'suzdur, çünkü sunucunun CI işi Studio'nun bağımlılıklarını kurmuyor. Ev deyimi: `billing/catalog.test.ts` de Studio'nun `lib/tiers.ts`'ini aynı yoldan kilitliyor.

**D-008'in "tek sabit" ilkesi artık sunucu içinde geçerli.** Sunucu dışında test-kilitli iki kopya var: bu Studio kopyası ve CLI'ın `palbase env create` kopyası (FR-007, plan-cli). `db/public.ts`'teki belge bunu söyler.

- [ ] **Adım 1: Kırmızı testi yaz** — `cloud/platform/server/modules/shared/environment-slug.studio.test.ts` (yeni dosya):
```ts
import { describe, expect, it } from "bun:test";
import { isolated } from "@palbase/backend/test";
import { ENVIRONMENT_SLUG_PATTERN } from "../../db/public.ts";
import * as studio from "../../../studio/src/lib/environment-slug.ts";
import { EnvironmentSlugService } from "./environment-slug.ts";

/**
 * THE CREATE FORM SHOWS THE SLUG THIS SERVER WILL WRITE (FR-107).
 *
 * Studio derives the slug while the person types, before anything is sent, so
 * it carries a copy of the rule — a browser cannot call this class. A copy
 * that drifted would show one directory and create another; this file runs
 * both on the same names and fails on the first difference.
 */
const slugs = isolated().get(EnvironmentSlugService);

const NAMES = [
  "Staging", "  staging  ", "featureX", "feature-profile-update", "a--b", "Production",
  "Feature X", "feature/login", "Geliştirme Ortamı", "İstanbul Şube", "ÇALIŞMA", "Ünal",
  "2nd env", "QA (EU) / 2", "x".repeat(38) + " tail", "", "   ", "🚀", "123", "---",
];

describe("Studio's slug preview is the server's rule (FR-107)", () => {
  it("spells the grammar with the schema's own text", () => {
    expect(studio.ENVIRONMENT_SLUG_PATTERN).toBe(ENVIRONMENT_SLUG_PATTERN);
  });

  it("derives the same slug from every name", () => {
    expect(NAMES.map((n) => studio.deriveEnvironmentSlug(n))).toEqual(NAMES.map((n) => slugs.derive(n)));
  });

  it("refuses what the server refuses, in the server's words — except `main`, which only the server can judge", () => {
    // `main` is free for a project's FIRST environment and reserved for every
    // other; only the server knows which one a request creates.
    const candidates = ["featureX", "staging", "", "1abc", "feature/login", "a b", "a".repeat(40), "local", "Local", "main", "Main"];
    expect(candidates.map((s) => studio.environmentSlugProblem(s)))
      .toEqual(candidates.map((s) => (s.toLowerCase() === "main" ? null : slugs.problemWith(s, false))));
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.studio.test.ts)` · Beklenen: **FAIL**, çıktıda `error: Cannot find module '../../../studio/src/lib/environment-slug.ts' from '…/modules/shared/environment-slug.studio.test.ts'` ve `0 pass` / `1 fail` / `1 error`.
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/studio/src/lib/environment-slug.ts` (yeni dosya):
```ts
/**
 * THE ENVIRONMENT SLUG, AS THE SERVER WILL WRITE IT (FR-107).
 *
 * The rule belongs to the server (`platform/server/modules/shared/environment-slug.ts`,
 * grammar in `platform/server/db/public.ts`, D-008), which refuses anything else
 * with a 400 or a 409. This copy exists only so the create form can show the
 * slug while the person types; `environment-slug.studio.test.ts` on the server
 * runs both on the same names and fails on the first difference.
 *
 * No imports: the server's test job loads this file without Studio's
 * dependencies.
 */
export const ENVIRONMENT_SLUG_PATTERN = "^[A-Za-z][A-Za-z0-9-]{0,38}$";
const ENVIRONMENT_SLUG = new RegExp(ENVIRONMENT_SLUG_PATTERN);

const SLUG_RULE =
  `a slug starts with a letter and continues with up to 38 letters, digits or hyphens (${ENVIRONMENT_SLUG_PATTERN})`;

/**
 * The slug the server derives from a display name when none is sent: a valid
 * name byte for byte, otherwise accents folded, every run of anything else one
 * hyphen, leading non-letters dropped, cut to 39. `""` means none can be derived.
 */
export function deriveEnvironmentSlug(name: string): string {
  const trimmed = name.trim();
  if (ENVIRONMENT_SLUG.test(trimmed)) return trimmed;
  const ascii = trimmed.replaceAll("ı", "i").normalize("NFKD").replace(/\p{M}+/gu, "");
  return ascii
    .replace(/[^A-Za-z0-9]+/g, "-")
    .replace(/^[^A-Za-z]+/, "")
    .slice(0, 39)
    .replace(/-+$/, "");
}

/**
 * Why the server would refuse this slug, in its own words — or `null`.
 *
 * `main` is not judged here: it is the first environment's slug and reserved
 * for every other, and only the server knows which one a request creates.
 */
export function environmentSlugProblem(slug: string): string | null {
  if (!ENVIRONMENT_SLUG.test(slug)) return `slug ${JSON.stringify(slug)} is not valid: ${SLUG_RULE}`;
  if (slug.toLowerCase() === "local") {
    return `slug ${JSON.stringify(slug)} is reserved: local/ belongs to the stack on each developer's own machine`;
  }
  return null;
}
```
(b) `cloud/platform/server/db/public.ts`: `ENVIRONMENT_SLUG_PATTERN`'ın belge yorumunun son iki satırını değiştir. Eski satırlar:
```
 * `sequence/slot.ts` does with `SLOT_SPACE`. The CLI keeps the same text for
 * `palbase env create` (FR-007); change both or neither.
```
Yerine:
```ts
 * `sequence/slot.ts` does with `SLOT_SPACE`. Two copies live outside this
 * server, each locked to this constant by a test: Studio's create form
 * (`studio/src/lib/environment-slug.ts`, `environment-slug.studio.test.ts`)
 * and the CLI's `palbase env create` (FR-007). Change all three or none.
```
(c) `.github/workflows/cloud-server-typecheck.yml`: `typecheck` işinin `suites` listesinde T006'nın `modules/panel/panel.environment-slug.test.ts` satırından sonra, kapanan `)`'dan önce ekle:
```yaml
            # Studio'nun slug önizlemesi bu sunucunun kuralı (FR-107): kopya kayarsa form bir
            # dizin gösterip başka birini yaratır. Dosya Studio'nun kopyasını doğrudan yükler.
            modules/shared/environment-slug.studio.test.ts
```
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.studio.test.ts modules/shared/environment-slug.test.ts db/schema.test.ts && npm run typecheck) && (cd cloud/platform/studio && npx tsc --noEmit)` · Beklenen: `18 pass` / `0 fail` / `Ran 18 tests across 3 files.`; iki tsc de exit 0.

  **Kilidin ısırdığını gör (zorunlu):**
  1. Studio kopyasında `const ascii = trimmed.replaceAll("ı", "i").normalize` → `const ascii = trimmed.normalize` yap.
  2. Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.studio.test.ts)` · Beklenen: **FAIL**, `-   "Gelistirme-Ortami",` / `+   "Gelistirme-Ortam",`, `2 pass` / `1 fail`.
  3. Düzenlemeyi elle geri çevir. Dosya yeni olduğu için `git checkout` kullanılamaz.
  4. Aynı komutla yeşili tekrar gör: `3 pass`.
- [ ] **Adım 5: Commit** — `git add .github/workflows/cloud-server-typecheck.yml cloud/platform/server/db/public.ts cloud/platform/server/modules/shared/environment-slug.studio.test.ts cloud/platform/studio/src/lib/environment-slug.ts && git commit -m "feat(studio): slug önizlemesi sunucunun kuralı — Studio'nun kopyası sunucunun testiyle aynı cevaba bağlı (FR-107)"`

---

### T010: Ortam oluşturma formu türetilen slug'ı düzenlenebilir gösterir; yalnız kişinin yazdığı slug'ı gönderir
<!-- deps: [T005, T009] | files: [cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.tsx, cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.test.tsx, cloud/platform/studio/src/lib/panel/routes/lifecycle.ts, cloud/platform/studio/src/lib/panel/routes/project-settings-contracts.test.ts, cloud/platform/studio/src/lib/project-settings-models.ts, cloud/platform/studio/src/palbe.gen.ts] | satisfies: [FR-107, FR-102] -->

**Interfaces:**
- Consumes: `deriveEnvironmentSlug`, `environmentSlugProblem` (T009) · `POST /v1/cloud/projects/{productId}/environments` gövdesi `{ name, slug?, tier? }` (T005).
- Produces:
  - `createEnvironmentInputSchema` = `{ projectId, name, slug?: string (min 1) }` (strict).
  - `environments.create` rotasının gövdesi `{ name }` ya da `{ name, slug }`.
  - `CloudEnvironmentsCreateEnvironmentRequest` = `{ name: string; slug?: string; tier?: 'free' | 'pro' | 'scale' }`.
  - `CreateEnvironmentSheet`, `trpc.environments.create`'i kişi slug'a yazmadıysa `{ projectId, name }`, yazdıysa `{ projectId, name, slug }` ile çağırır.

Form, ad alanının altında bir `Slug` alanı taşır. Kişi yazmadıkça slug addan türetilir: `Feature Profile Update` → `Feature-Profile-Update`. Kişi yazınca slug onundur ve ad değişse de bir daha değişmez.

**Kişinin yazdığı slug gönderilir.** Sunucu tam onu yazar ya da 400/409 ile reddeder.

**Dokunulmamış slug gönderilmez.** Sunucu adından aynı değeri türetir; kopya T009'un kilidiyle bağlı. Ya da bu projenin ilk ortamıysa (FR-102) `main` yazar. Form bunu bilemez: elindeki liste (`projects.environments`) yalnız bu hesabın gördüğü ortamları içeriyor (owner ya da üye; `panel.controller.ts` `listEnvironments`). Yani "liste boş" "proje boş" demek değildir. Bu yüzden form `main`'i kendisi göstermez; yardım metni kuralı söyler: "A project's first environment is always main."

Bu tasarım yeni-proje sayfasının kurtarma yolunu korur. İlk ortamı düşmüş yarım bir proje, "View environments" → "Create environment" ile tamamlanırken artık 400 almaz. Eskiden form türetilmiş `Production`'ı gönderiyordu ve sunucu `the first environment's slug is always "main"` diyordu.

Sunucunun reddedeceği bir slug (dilbilgisi, `local`) istek gitmeden, sunucunun kendi cümlesiyle, alanın altında söylenir.

Rota (`environments.create`) slug'ı değiştirmeden taşır. Slug'sız çağrı gövdeye `slug` anahtarı koymaz. Üretilmiş istemcinin istek tipi `slug?: string` kazanır. Elle yazılan bu satır **üreticinin kendi çıktısıdır**; Adım 4'te ölçülür.

Mevcut "uses the actual project and then the server-returned ref" testine DOKUNULMAZ. Dokunulmamış slug gönderilmediği için beklentisi (`{ projectId: "project-1", name: "Staging" }`) aynen doğru kalır.

**Deploy sırası:** sunucunun `CreateEnvironmentBody`'si T005'ten önce `slug` tanımıyordu, ve `z.object` bilinmeyen anahtarı atar. Bu görev T005 canlıya çıktıktan sonra deploy edilir.

- [ ] **Adım 1: Kırmızı testi yaz** — (a) `cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.test.tsx`: `it("locks duplicate submit and Escape during creation", …)` satırının ÖNÜNE ekle:
```tsx
 it("shows the slug derived from the name, lets it be edited, and creates with the slug on screen (FR-107)", async () => { show(); await userEvent.type(screen.getByLabelText(/Environment name/), "Feature Profile Update"); expect(screen.getByLabelText(/Slug/)).toHaveValue("Feature-Profile-Update"); await userEvent.clear(screen.getByLabelText(/Slug/)); await userEvent.type(screen.getByLabelText(/Slug/), "featureProfileUpdate"); await userEvent.type(screen.getByLabelText(/Environment name/), "s"); expect(screen.getByLabelText(/Slug/)).toHaveValue("featureProfileUpdate"); await userEvent.click(screen.getByRole("button", { name: "Create environment" })); await waitFor(() => expect(state.create).toHaveBeenCalledExactlyOnceWith({ projectId: "project-1", name: "Feature Profile Updates", slug: "featureProfileUpdate" })); });
 it("refuses a slug the server would refuse, in the server's words, before sending (FR-107)", async () => { show(); await userEvent.type(screen.getByLabelText(/Environment name/), "Local"); await userEvent.click(screen.getByRole("button", { name: "Create environment" })); expect(screen.getByText(`slug "Local" is reserved: local/ belongs to the stack on each developer's own machine`)).toBeVisible(); await userEvent.clear(screen.getByLabelText(/Environment name/)); await userEvent.type(screen.getByLabelText(/Environment name/), "🚀"); expect(screen.getByText(/^slug "" is not valid: a slug starts with a letter/)).toBeVisible(); expect(state.create).not.toHaveBeenCalled(); });
 it("sends no slug the person did not type — the server derives the same one, and writes main when this is the project's first environment (FR-102, FR-107)", async () => { show(); await userEvent.type(screen.getByLabelText(/Environment name/), "Production"); expect(screen.getByLabelText(/Slug/)).toHaveValue("Production"); await userEvent.click(screen.getByRole("button", { name: "Create environment" })); await waitFor(() => expect(state.create).toHaveBeenCalledExactlyOnceWith({ projectId: "project-1", name: "Production" })); });
```
(b) `cloud/platform/studio/src/lib/panel/routes/project-settings-contracts.test.ts`: `it.each(["kind", "sourceEnvironmentRef", "workflowId"])` satırının ÖNÜNE ekle:
```ts
 it("carries the slug the form showed, unchanged (FR-107)", async () => {
  api.createEnvironment.mockResolvedValue({ ref: "new-ref", name: "Feature X", phase: "Running" });
  await lifecycleRoutes["environments.create"]({ projectId: "project-1", name: "Feature X", slug: "featureX" });
  expect(api.createEnvironment).toHaveBeenCalledExactlyOnceWith("project-1", { name: "Feature X", slug: "featureX" });
 });
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/studio && npx vitest run "src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.test.tsx" src/lib/panel/routes/project-settings-contracts.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - rotada `ZodError` … `"message": "Unrecognized key(s) in object: 'slug'"`
  - formda `TestingLibraryElementError: Unable to find a label with the text of: /Slug/`
  - `Tests  4 failed | 25 passed (29)`
- [ ] **Adım 3: Uygula** —
  1. `cloud/platform/studio/src/lib/project-settings-models.ts`: `export const createEnvironmentInputSchema = z.object({ projectId: z.string().min(1), name: projectNameSchema }).strict();` satırının yerine şunu koy:
```ts
// `slug` is the one the form showed (FR-107); the server judges it and writes exactly it or refuses. Omitted, the server derives it.
export const createEnvironmentInputSchema = z.object({ projectId: z.string().min(1), name: projectNameSchema, slug: z.string().min(1).optional() }).strict();
```
  2. `cloud/platform/studio/src/lib/panel/routes/lifecycle.ts`: `environments.create` girdisini, yorumuyla birlikte, şununla değiştir:
```ts
  /**
   * The existing product owns the new environment. No implicit clone or compute choice.
   * The slug the form showed goes as it is (FR-107); without one the server derives it.
   */
  "environments.create": async (i: Input) => {
    const input = createEnvironmentInputSchema.parse(i);
    const body = input.slug === undefined ? { name: input.name } : { name: input.name, slug: input.slug };
    return createdEnvironmentSchema.parse(await pb.cloudEnvironments.createEnvironment(input.projectId, body));
  },
```
  3. `cloud/platform/studio/src/palbe.gen.ts`: `CloudEnvironmentsCreateEnvironmentRequest` arayüzünü şununla değiştir:
```ts
export interface CloudEnvironmentsCreateEnvironmentRequest {
  name: string;
  slug?: string;
  tier?: 'free' | 'pro' | 'scale';
}
```
  4. `cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.tsx`:
     - `import { projectNameSchema, type CreatedEnvironment } from "@/lib/project-settings-models";` satırından sonra `import { deriveEnvironmentSlug, environmentSlugProblem } from "@/lib/environment-slug";` ekle.
     - `const [name, setName] = React.useState("");` satırından sonra ekle:
```tsx
  // THE SLUG FOLLOWS THE NAME UNTIL SOMEONE TYPES IN IT (FR-107). A typed slug
  // is sent, and the server writes exactly it or refuses it. An untouched one is
  // NOT sent: the server derives the same value from the name (the copy is
  // locked by `environment-slug.studio.test.ts`) — or writes `main` when this is
  // the project's first environment, which this form cannot know, because the
  // list it has shows only the environments this account can see.
  const [editedSlug, setEditedSlug] = React.useState<string | null>(null);
  const slug = editedSlug ?? deriveEnvironmentSlug(name);
  const slugProblem = environmentSlugProblem(slug);
```
     - `submit`'te `setSubmitted(true); if (!parsed.success) return;` satırını `setSubmitted(true); if (!parsed.success || slugProblem) return;` yap.
     - `const result = await create.mutateAsync({ projectId, name: parsed.data });` satırını `const result = await create.mutateAsync(editedSlug === null ? { projectId, name: parsed.data } : { projectId, name: parsed.data, slug });` yap.
     - "Environment name" `Field`'ının kapanan `</Field>`'ından sonra ekle:
```tsx
        <Field label="Slug" required helper="Its folder in every checkout, its Android build type and its --env name. The name can change later; the slug cannot. A project's first environment is always main." error={submitted && slugProblem ? slugProblem : undefined}><Input value={slug} onChange={(event) => setEditedSlug(event.target.value)} disabled={busy} maxLength={39} spellCheck={false} autoCapitalize="off" autoComplete="off" className="font-mono" /></Field>
```
- [ ] **Adım 4: İstek tipinin üreticinin çıktısı olduğunu ölç** — Run (depo kökünden):
```bash
cd cloud/platform/server && tmp="$(mktemp -d)" && mkdir -p "$tmp/Palbase" && cat > .contract-probe.ts <<'EOF'
import { aggregateOpenAPI } from "@palbase/backend/openapi";
import { CloudEnvironmentsController } from "./modules/environments/cloud-environments.controller.ts";
const { document } = await aggregateOpenAPI(process.cwd(), {
  loadedControllers: [{ relativePath: "modules/environments/cloud-environments.controller.ts", controller: CloudEnvironmentsController }],
});
await Bun.write(process.argv[2]!, JSON.stringify(document));
EOF
bun .contract-probe.ts "$tmp/Palbase/openapi.json"; rm .contract-probe.ts
printf '{"base_url":"https://api.palbase.studio","api_key":"pb_project_ctcLXcUyFmvEncRSu3uVg","app_id":"project"}' > "$tmp/Palbase/palbase-config.json"
../studio/node_modules/.bin/palbe-gen --dir "$tmp/Palbase" --out "$tmp/palbe.gen.ts"
grep -A4 "interface CloudEnvironmentsCreateEnvironmentRequest" "$tmp/palbe.gen.ts"; rm -rf "$tmp"; cd -
```
· Beklenen: `✓ wrote …/palbe.gen.ts (17 operations)` ve Adım 3.3'teki üç alanlı arayüzün birebiri: `name: string;` / `slug?: string;` / `tier?: 'free' | 'pro' | 'scale';`.

  Çevrimdışı sözleşme yalnız istek gövdelerini doğru verir; yanıt tipleri stager ister ve `unknown` çıkar. Bu yüzden dosyanın tamamı buradan yeniden üretilmez. Ev yordamı (`palbase start` → `palbase spec` → `palbe-gen --dir`, bkz. `4635e4beb`) sunucu deploy'undan sonra koşulduğunda aynı satırı üretir.
- [ ] **Adım 5: Yeşil** — Run: `(cd cloud/platform/studio && npx vitest run "src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.test.tsx" src/lib/panel/routes/project-settings-contracts.test.ts && npx tsc --noEmit && npx eslint "src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.tsx" src/lib/environment-slug.ts src/lib/panel/routes/lifecycle.ts src/lib/project-settings-models.ts && npm run lint:design)` · Beklenen: `Test Files  2 passed (2)`, `Tests  29 passed (29)`; tsc exit 0; eslint çıktısız exit 0; `Studio workspace: color and geometry token checks passed.`
- [ ] **Adım 6: Commit** — `git add "cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.tsx" "cloud/platform/studio/src/app/(studio)/projects/[projectId]/environments/create-environment-sheet.test.tsx" cloud/platform/studio/src/lib/panel/routes/lifecycle.ts cloud/platform/studio/src/lib/panel/routes/project-settings-contracts.test.ts cloud/platform/studio/src/lib/project-settings-models.ts cloud/platform/studio/src/palbe.gen.ts && git commit -m "feat(studio): ortam oluşturma formu türetilen slug'ı düzenlenebilir gösterir, yalnız kişinin yazdığı slug'ı gönderir (FR-107)"`

---

### T011: Yeni proje formu ilk ortamın slug'ını `main` olarak gösterir, sunucuya bırakır
<!-- deps: [T003, T009] | files: [cloud/platform/studio/src/app/(studio)/projects/new/page.tsx, cloud/platform/studio/src/app/(studio)/projects/new/page.test.tsx, cloud/platform/studio/src/lib/environment-slug.ts, cloud/platform/server/modules/shared/environment-slug.studio.test.ts] | satisfies: [FR-107, FR-102] -->

**Interfaces:**
- Consumes: T009'un Studio kopyası ve kilidi · `FIRST_ENVIRONMENT_SLUG` (sunucu, T001) · ilk ortamın `main` yazılması (T003).
- Produces: Studio `FIRST_ENVIRONMENT_SLUG = "main"` (`src/lib/environment-slug.ts`). Yeni proje sayfası `environments.create`'i `{ projectId, name }` ile çağırır; slug YOK.

Yeni proje sayfası ilk ortamı `Production` adıyla öneriyor. Sunucu (T003) o ortamın slug'ını, adı ne olursa olsun, `main` yazar. Form bunu söylemiyordu. Doğrulayıcının bulgusu: "panel-created gives `Production`", ve kişi `palbase.env.release=main`'in nereden geldiğini göremiyor.

Sayfa artık "First environment"ın altında salt okunur bir `Slug: main` alanı gösterir. Ad değişse de alan `main` kalır. İstek **slug göndermez**: ilk ortam için `main` dışındaki her slug sunucuda 400'dür, göndermemek tek doğru yol.

`main` literali Studio kopyasına sabit olarak girer. T009'un kilidiyle sunucunun `FIRST_ENVIRONMENT_SLUG`'ına bağlanır.

- [ ] **Adım 1: Kırmızı testi yaz** — (a) `cloud/platform/studio/src/app/(studio)/projects/new/page.test.tsx`: `it("does not open an environment on a failed status read", …)` satırının ÖNÜNE ekle:
```tsx
 it("shows the first environment's slug as main, whatever it is called, and lets the server write it (FR-107, D-015)", async () => { render(<NewProjectPage />); const slug = screen.getByLabelText(/Slug/); expect(slug).toHaveValue("main"); expect(slug).toHaveAttribute("readonly"); await userEvent.clear(screen.getByLabelText(/First environment/)); await userEvent.type(screen.getByLabelText(/First environment/), "Live"); expect(slug).toHaveValue("main"); await submit(); expect(await screen.findByText("Your environment is ready to open.")).toBeVisible(); expect(state.createEnvironment).toHaveBeenCalledExactlyOnceWith({ projectId: "project-real", name: "Live" }); });
```
(b) `cloud/platform/server/modules/shared/environment-slug.studio.test.ts`: import'u `import { EnvironmentSlugService, FIRST_ENVIRONMENT_SLUG } from "./environment-slug.ts";` yap. "spells the grammar…" testinden sonra ekle:
```ts

  it("names the first environment's slug as the server writes it", () => {
    expect(studio.FIRST_ENVIRONMENT_SLUG).toBe(FIRST_ENVIRONMENT_SLUG);
  });
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — İki koşu:
  - Run: `(cd cloud/platform/studio && npx vitest run "src/app/(studio)/projects/new/page.test.tsx")` · Beklenen: **FAIL**, `TestingLibraryElementError: Unable to find a label with the text of: /Slug/`, `Tests  1 failed | 17 passed (18)`.
  - Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.studio.test.ts)` · Beklenen: **FAIL**, `Expected: "main"` / `Received: undefined`, `3 pass` / `1 fail`.
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/studio/src/lib/environment-slug.ts`: `const ENVIRONMENT_SLUG = new RegExp(ENVIRONMENT_SLUG_PATTERN);` satırından sonra ekle:
```ts

/** Every project's first environment, whatever it is called on screen (D-015). */
export const FIRST_ENVIRONMENT_SLUG = "main";
```
(b) `cloud/platform/studio/src/app/(studio)/projects/new/page.tsx`:
  - `import { createProjectInputSchema, … } from "@/lib/project-settings-models";` satırından sonra `import { FIRST_ENVIRONMENT_SLUG } from "@/lib/environment-slug";` ekle.
  - "First environment" `Field`'ının kapanan `</Field>`'ından sonra ekle:
```tsx
              {/* THE FIRST SLUG IS NOT A CHOICE (FR-102, D-015): the server writes `main` whatever the name, so the form shows it and sends none. */}
              <Field label="Slug" helper="A project's first environment is always main: its folder in every checkout, its Android build type and its --env name. Its name can be anything."><Input value={FIRST_ENVIRONMENT_SLUG} readOnly className="font-mono" /></Field>
```
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/studio && npx vitest run "src/app/(studio)/projects/new/page.test.tsx" && npx tsc --noEmit && npx eslint "src/app/(studio)/projects/new/page.tsx" src/lib/environment-slug.ts) && (cd cloud/platform/server && npm test -- modules/shared/environment-slug.studio.test.ts && npm run typecheck)` · Beklenen: `Tests  18 passed (18)`; iki tsc de exit 0; eslint exit 0; `4 pass` / `0 fail`.
- [ ] **Adım 5: Commit** — `git add "cloud/platform/studio/src/app/(studio)/projects/new/page.tsx" "cloud/platform/studio/src/app/(studio)/projects/new/page.test.tsx" cloud/platform/studio/src/lib/environment-slug.ts cloud/platform/server/modules/shared/environment-slug.studio.test.ts && git commit -m "feat(studio): yeni proje formu ilk ortamın slug'ını main olarak gösterir, sunucuya bırakır (FR-107, D-015)"`

---

### T012: KAPI — geri doldurma canlıda bitti (operatör adımı; kod yok)
<!-- deps: [T007] | files: [] | satisfies: [FR-105, FR-101] -->

**Interfaces:**
- Consumes: `POST /v1/cloud/environments/slugs/backfill` ve `ENVIRONMENTS_WITHOUT_SLUG_SQL` (T007)
- Produces: T013'ün tek önkoşulu — üretimde `n = 0`.

T013 `cloud_projects.slug`'ı NOT NULL yapar. NULL bir satır varken bu deploy "column contains null values" ile düşer. T014–T016 da `slug`'ı NOT NULL okur. Bu yüzden **T013 ve sonrası bu kapı geçilmeden commit'lenmez ve push'lanmaz.**

T008–T011 bu kapıya bağlı değildir; önceden commit'lenebilir. Studio deploy'u ise Adım 2'den sonra yapılır: geri doldurulmamış satırda kart slug yerine ref gösterirdi. T010 ayrıca T005 canlıyken deploy edilir.

**Bu görevin hiçbir adımı bu planın yazımında KOŞTURULMADI**, çünkü bulut çağrısı yasaktı. Aşağıdaki "Koşul"lar gözlem değil, geçme şartıdır. Fiilin yerel davranışı T007'nin PG testinde ölçüldü: ikinci koşu `{ assigned: [], remaining: 0 }` döndü, NULL kalan satır `remaining`'e sayıldı.

- [ ] **Adım 1: Sunucuyu deploy et** — T001–T007. Şema rayı `db/public.ts`'i uygular: `slug` nullable kolonu, CHECK, tekil indeks ve `cloud_environment_slug_backfills` tablosu.
- [ ] **Adım 2: Operatör fiili** — `POST /v1/cloud/environments/slugs/backfill`'i operatör hesabıyla çağır. `remaining: 0` dönene kadar tekrarla. `assigned` dizisi (eski dizin → slug, kural) göç notuna girer. Aynı satırlar `cloud_environment_slug_backfills`'te de kalır. D-015'in göç notu: panelden açılmış projelerde bir sonraki `link`, `Production/` yerine `main/` yazar.
- [ ] **Adım 3: Kapı** — Üretimde koş: `SELECT count(*)::int AS n FROM cloud_projects WHERE slug IS NULL` (T007'nin `ENVIRONMENTS_WITHOUT_SLUG_SQL`'i). Koşul: `n = 0`. Değilse T013'e geçilmez; Adım 2 tekrarlanır.
- [ ] **Adım 4: Studio'yu deploy et** — T008–T011.

---

### T013: `cloud_projects.slug` NOT NULL — sözleşme adımı (AYRI DEPLOY — KOŞULLU: T012'nin kapısı üretimde `n = 0`)
<!-- deps: [T005, T006, T007, T012] | files: [cloud/platform/server/db/public.ts, cloud/platform/server/db/schema.test.ts, cloud/platform/server/palbase/palbase-env.d.ts, cloud/platform/server/modules/panel/panel.controller.ts, cloud/platform/server/modules/panel/panel.environment-slug.test.ts, cloud/platform/server/modules/shared/environment-slug.pg.test.ts, cloud/platform/server/modules/fleet/slug-backfill.pg.test.ts, cloud/platform/server/modules/cli/cli.projects.pg.test.ts, cloud/platform/server/modules/panel/panel.sdk-pins.pg.test.ts, cloud/platform/server/modules/usage/compute.test.ts, cloud/platform/server/modules/usage/ingest.test.ts, cloud/platform/server/modules/usage/realtime.test.ts, cloud/platform/server/modules/usage/stock.test.ts] | satisfies: [FR-101] -->

**Interfaces:**
- Consumes: T012'nin kapısı; panel slug alanı (T006); `held()` (T005)
- Produces: `cloud_projects.slug text NOT NULL`. Üretilen tipte `slug: Pg<string, "text">` olur ve insert'te zorunludur. Panel `slug: r.slug` döner; ref yedeği yok.

**Bu görev, T012'nin kapısı üretimde `n = 0` vermeden commit'lenmez ve push'lanmaz.**

Sözleşme adımı iki geçiş davranışını BİLEREK bitirir ve onları tutan testleri bilerek değiştirir:
1. Slug'sız satırlar artık reddedilir. T002'nin "rows written before the backfill (slug NULL) do not collide" testi NOT NULL reddini bekleyen bir teste döner. T005'in NULL slug'lı satır yazan PG testi ("before the backfill, the slugs it WILL give…") silinir, çünkü defter o satırları artık kabul etmez. `held()`'in geri doldurulmamış satır planlaması `cloud-lifecycle.test.ts`'teki birim testlerinde ölçülmeye devam eder. Kod yerinde kalır: slug'ı dolu bir projede `planBackfill` boştur, `held()` yalnız saklanan slug'lardır.
2. Panelin ref yedeği ölüdür; onu tutan sahte-veritabanı testi silinir.

Geri doldurma fiili kalır; artık `{ assigned: [], remaining: 0 }` döner. Onun PG testi, sözleşmeden ÖNCEKİ defter biçiminde koşar.

Canlı şemadan `LIKE public.cloud_projects INCLUDING ALL` ile tablo türeten altı Docker'lı test fikstürü, satırlarına slug alır. Almazlarsa yerel veritabanına sözleşme uygulandığında `null value in column "slug" of relation "cloud_projects" violates not-null constraint` ile düşerler (ölçüldü, aşağıda).

- [ ] **Adım 1: Kırmızı testi yaz** — (a) `cloud/platform/server/db/schema.test.ts`: `it("uniqueness is per project and case-insensitive", …)` testinin ÜSTÜNE ekle:
```ts
  it("every environment row has a slug — the backfill is done, the column is NOT NULL (contract)", () => {
    expect(projects?.columns.slug).toMatchObject({ type: "text", nullable: false });
  });

```
(b) `cloud/platform/server/modules/shared/environment-slug.pg.test.ts`:
  - "rows written before the backfill (slug NULL) do not collide with each other" testini şununla değiştir:
```ts
  it("an environment without a slug is refused — every row has one since the contract", async () => {
    expect(await insert("r1", "p1", null))
      .toBe(`null value in column "slug" of relation "cloud_projects" violates not-null constraint`);
  });
```
  - T005'in "before the backfill, the slugs it WILL give — from the product's name and the rows' order" testini SİL; `insert` yardımcısı T005'teki hâliyle kalır.

(c) `cloud/platform/server/modules/panel/panel.environment-slug.test.ts`: `ledger`'deki `old01ref` satırını ve "a row the backfill has not reached yet still answers with its ref" testini SİL.
(d) `cloud/platform/server/modules/fleet/slug-backfill.pg.test.ts`: `beforeAll(fixture.initialize);` satırını şununla değiştir:
```ts
beforeAll(async () => {
  await fixture.initialize();
  // THE LEDGER THE BACKFILL RAN ON: rows born before the slug column had none,
  // and the NOT NULL that ended them came after it (the contract step). As the
  // table's owner, not as `service_role`.
  await fixture.transaction((c) => c.query("RESET ROLE; ALTER TABLE cloud_projects ALTER COLUMN slug DROP NOT NULL"));
});
```
(e) Canlı şemadan türeyen fikstürler. Her `cloud_projects` INSERT'i slug alır: geri doldurmanın vereceği ad, proje içinde tekil.

`cloud/platform/server/modules/cli/cli.projects.pg.test.ts` — beş INSERT'in kolon listesi değişir:
  - `(ref, slot, cell_id, phase, product_id, name, owner_id, created_at)` → `(ref, slot, cell_id, phase, product_id, name, slug, owner_id, created_at)`.
  - Değerlerde: `'prd_a', 'todoapp', 'usr_1',` → `'prd_a', 'todoapp', 'main', 'usr_1',` (4 yer).
  - `'prd_a', 'staging', 'usr_1',` → `'prd_a', 'staging', 'staging', 'usr_1',` (2 yer).
  - `'prd_gone', 'oldapp', 'usr_1',` → `'prd_gone', 'oldapp', 'main', 'usr_1',`.

`cloud/platform/server/modules/panel/panel.sdk-pins.pg.test.ts`:
```sql
     INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, slug, sdk_version)
     VALUES ('aaa11111m', 1, 'cell-03', 'Running',  'prd_a', 'main', '22.0.1'),
            ('bbb22222m', 2, 'cell-03', 'Archived', 'prd_b', 'main', NULL),
            ('ccc33333m', 3, 'cell-03', 'Running',  'prd_c', 'main', '33.0.2');
```
`cloud/platform/server/modules/usage/compute.test.ts`:
```sql
     INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, slug, compute_size)
     VALUES ('${AAA}', 1, '01', 'Running', 'prd_a', 'main', 'small'),
            ('${BBB}', 2, '01', 'Running', 'prd_b', 'main', 'small'),
            ('${ORPHAN}', 3, '01', 'Running', 'prd_yok', 'main', 'small');`,
```
`cloud/platform/server/modules/usage/ingest.test.ts`:
```sql
     INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, slug)
     VALUES ('${AAA}', 1, '01', 'Running', 'prd_a', 'main'),
            ('${BBB}', 2, '01', 'Running', 'prd_b', 'main'),
            ('${ORPHAN}', 3, '01', 'Running', 'prd_yok', 'main');`,
```
`cloud/platform/server/modules/usage/realtime.test.ts`:
```sql
     INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, slug)
     VALUES ('${AAA}', 1, '01', 'Running', 'prd_a', 'main'),
            ('${BBB}', 2, '01', 'Archived', 'prd_b', 'main'),
            ('${STOPPED}', 3, '01', 'Archived', 'prd_a', 'stopped');`,
```
`cloud/platform/server/modules/usage/stock.test.ts`:
```sql
     INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, slug)
     VALUES ('${AAA}', 1, '01', 'Running', 'prd_a', 'main'),
            ('${BBB}', 2, '01', 'Running', 'prd_b', 'main'),
            ('${STOPPED}', 3, '01', 'Archived', 'prd_a', 'stopped'),
            ('${ORPHAN}', 4, '01', 'Running', 'prd_yok', 'main');`,
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- db/schema.test.ts modules/shared/environment-slug.pg.test.ts modules/fleet/slug-backfill.pg.test.ts modules/panel/panel.environment-slug.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - `(fail) the environment slug is spelled once and the ledger enforces it (FR-101) > every environment row has a slug — the backfill is done, the column is NOT NULL (contract)`
  - `Expected: "null value in column "slug" of relation "cloud_projects" violates not-null constraint"` / `Received: "accepted"`
  - `16 pass` / `2 fail` / `Ran 18 tests across 4 files.`
- [ ] **Adım 3: Uygula** — (a) `cloud/platform/server/db/public.ts`: T002'nin `// NULLABLE FOR ONE DEPLOY, deliberately: …` yorumunu ve `slug: text().nullable(),` satırını şununla değiştir:
```ts
    // NOT NULL — THE CONTRACT STEP. It was nullable for exactly one deploy:
    // rows born before the column had no slug until the operator backfill
    // (FR-105) wrote one, and NOT NULL then would have failed the deploy with
    // "column contains null values" (the lesson of `last_error`, `done_at`,
    // `edge_address`). This line ships only after
    // `SELECT count(*)::int AS n FROM cloud_projects WHERE slug IS NULL`
    // answered 0 on production.
    slug: text().notNull(),
```
`raw` yorumunun son iki satırını tek satıra indir. Eski satırlar: `// \`syntax error at or near ","\`. A NULL slug does not collide (rows before` ve `// the backfill), which is exactly what the expand step needs.`. Yeni satır: `  // \`syntax error at or near ","\`.`
(b) `cloud/platform/server/palbase/palbase-env.d.ts`: `cloud_projects` `row`'da `slug: Pg<string, "text"> | null;` → `slug: Pg<string, "text">;`. `insert`'te `slug?: Pg<string, "text"> | null;` → `slug: Pg<string, "text">;`.
(c) `cloud/platform/server/modules/panel/panel.controller.ts`: T006'nın slug satırını ve yorumunu şununla değiştir:
```ts
        // THE STORED SLUG — the directory every teammate's `link` writes; a
        // rename changes `name` above and never this (FR-104).
        slug: r.slug as string,
```
`cloud-lifecycle.ts`'e dokunulmaz.
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm test -- db/schema.test.ts modules/shared/environment-slug.pg.test.ts modules/fleet/slug-backfill.pg.test.ts modules/panel/panel.environment-slug.test.ts modules/environments/cloud-lifecycle.test.ts modules/environments/cloud-lifecycle.race.test.ts && npm run typecheck)` · Beklenen: `65 pass` / `0 fail` / `Ran 65 tests across 6 files.`, ve typecheck exit 0.

  Docker'lı altı fikstür dosyası burada koşturulamadı, çünkü Docker yoktu. Değiştirilmiş INSERT metinleri sözleşme biçimli `cloud_projects` tablosunda tek tek koşturuldu. Tablo NOT NULL slug + CHECK + tekil indeks içeriyordu, `toSchemaJSON`'dan `flagPostgres` ile basıldı, PostgreSQL 16.14'te.
  - Koşturulanlar: `cli.projects` dosyasının dört farklı metni (#3 ile #4 birebir aynı), `panel.sdk-pins`, ve `usage/compute|ingest|realtime|stock`. Dokuzu da `accepted`.
  - Kontrol olarak slug'sız eski `stock` INSERT'i koşturuldu: `null value in column "slug" of relation "cloud_projects" violates not-null constraint`.
- [ ] **Adım 5: Commit** — `git add cloud/platform/server/db/public.ts cloud/platform/server/db/schema.test.ts cloud/platform/server/palbase/palbase-env.d.ts cloud/platform/server/modules/panel/panel.controller.ts cloud/platform/server/modules/panel/panel.environment-slug.test.ts cloud/platform/server/modules/shared/environment-slug.pg.test.ts cloud/platform/server/modules/fleet/slug-backfill.pg.test.ts cloud/platform/server/modules/cli/cli.projects.pg.test.ts cloud/platform/server/modules/panel/panel.sdk-pins.pg.test.ts cloud/platform/server/modules/usage/compute.test.ts cloud/platform/server/modules/usage/ingest.test.ts cloud/platform/server/modules/usage/realtime.test.ts cloud/platform/server/modules/usage/stock.test.ts && git commit -m "feat(db): cloud_projects.slug NOT NULL — geri doldurma canlıda bitti, sözleşme adımı (FR-101)"`

---

### T014: CLI listesi ortamı saklanan slug'la adlandırır, görünen ad `display_name`'de
<!-- deps: [T013] | files: [cloud/platform/server/modules/shared/environment-slug.ts, cloud/platform/server/modules/shared/environment-slug.test.ts, cloud/platform/server/modules/cli/cli.service.ts, cloud/platform/server/modules/cli/cli.ts, cloud/platform/server/modules/cli/cli.controller.ts, cloud/platform/server/modules/cli/cli.controller.test.ts, cloud/platform/server/modules/cli/cli.environment-slug.pg.test.ts, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-106] -->

**Interfaces:**
- Consumes: `cloud_projects.slug text NOT NULL` (T002, T013) · `EnvironmentSlugService` (T001; `SharedModule` dışa verir) · `flagPostgres(tables)` (T002) · `CliController`'ın mevcut kurucusu (`EnvironmentSlugService` T004'ten beri enjekte).
- Produces:
  - `EnvironmentSlugService.displayName(name: string | null, slug: string): string`.
  - `CLI_PROJECTS_SQL` artık `p.slug AS env_slug` seçer; `CliService.projectsVisibleTo()` satırı `env_slug: string` taşır.
  - `CliService.environmentsOfProduct(productId)` → `Array<{ ref; name: string | null; slug: string; phase }>`.
  - `CliService.environmentRecord(ref)` → `{ ref; name; slug: string; product_id } | undefined`.
  - `CliProjectSchema.environments[]` = `{ ref, name /* slug */, display_name, status }`.
  - `GET /api/v2/environments/{ref}` → `{ project_id, ref, name: <slug> }`.

Bugün `GET /api/v2/projects` ortamın `name`'ini slug'dan önceki ad kuralıyla (`legacyName`) üretiyor. Sonuçları:
- Panelden açılmış bir projenin ilk ortamı `Production` olur (ürün adı değil, olduğu gibi).
- Yeniden adlandırılmış bir ortam `Staging (EU)` olur: boşluklu, parantezli bir dizin adı.
- Adsız bir ortam kendi ref'i olur.

D-016: `name` **saklanan slug**'ı taşır. Yayımlanmış her CLI dizini `name`'den kurduğu için eski CLI'lar da güvenli dizine geçer. İnsanların okuduğu ad yanına, `display_name`'e gider.

Kural tek yerdedir: `EnvironmentSlugService.displayName`. Ad boş ya da yalnız boşluksa slug döner, ref asla dönmez.

Tekil proje (`/projects/{id}`) ve ref→proje araması (`/environments/{ref}`) aynı cevabı verir. İkincisi artık ürünün diğer ortamlarını okumaz; slug satırın kendisindedir.

**Davranış değişikliği (D-015 göç notu):** panelden açılmış bir projede bir sonraki `link`, `Production/` yerine `main/` yazar.

- [ ] **Adım 1: Kırmızı testi yaz** — (a) `cloud/platform/server/modules/cli/cli.environment-slug.pg.test.ts` (yeni dosya):
```ts
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "bun:test";
import { fakeDatabase, isolated, withServices } from "@palbase/backend/test";
import { flagPostgres } from "../shared/flags-postgres.ts";
import { CliController } from "./cli.controller.ts";

/**
 * EVERY CLI SURFACE NAMES AN ENVIRONMENT BY ITS STORED SLUG (FR-106, D-016).
 *
 * `palbase link` turns an environment's `name` into
 * `palbase/environments/<name>/` — and so does every CLI published before the
 * slug existed. So `name` IS the slug, and what people call the environment
 * travels beside it in `display_name`. The controller runs here with its own
 * services and its own SQL on a real Postgres whose tables are rendered from
 * the JSON the schema rail receives (`toSchemaJSON`): a fake answers whatever
 * it is told and could not show a query that forgot `slug`.
 */
const fixture = flagPostgres([
  "cloud_organizations", "cloud_organization_members", "cloud_products", "cloud_projects", "cloud_apps",
]);
beforeAll(fixture.initialize);
afterAll(fixture.close);

const db = Object.assign(fakeDatabase().raw, { query: (sql: string, params: unknown[] = []) => fixture.query(sql, params) }) as never;
const cli = isolated().get(CliController);
const run = <T>(fn: () => Promise<T>) => withServices({ Database: db }, fn);
const member = { id: "usr_1" };
const session = { headers: new Headers() } as Request;

let slot = 0;
const environment = (ref: string, name: string | null, slug: string, createdAt: string) =>
  fixture.query(
    `INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, name, slug, owner_id, created_at)
     VALUES ($1, $2, 'cell-01', 'Running', 'proj_shop', $3, $4, 'usr_1', $5::timestamptz)`,
    [ref, ++slot, name, slug, createdAt],
  );

beforeEach(async () => {
  await fixture.reset();
  await fixture.query(`INSERT INTO cloud_organizations (id, name, tier) VALUES ('org_1', 'Acme', 'free')`);
  await fixture.query(`INSERT INTO cloud_organization_members (organization_id, user_id, role) VALUES ('org_1', 'usr_1', 'member')`);
  await fixture.query(`INSERT INTO cloud_products (id, organization_id, name, created_by) VALUES ('proj_shop', 'org_1', 'Shop', 'usr_1')`);
  // A PANEL-CREATED PROJECT (D-015): the first environment is on screen as
  // "Production" and its slug is `main`; the second was renamed after it was
  // created (FR-104), so its display name is not a directory name at all; the
  // third was never given a name and is called by its slug.
  await environment("pnl01ref", "Production", "main", "2026-08-01");
  await environment("stg01ref", "Staging (EU)", "staging", "2026-08-02");
  await environment("qa001ref", null, "qa", "2026-08-03");
});

describe("the CLI listing names every environment by its slug (FR-106, D-016)", () => {
  const listed = [
    { ref: "pnl01ref", name: "main", display_name: "Production", status: "Running" },
    { ref: "stg01ref", name: "staging", display_name: "Staging (EU)", status: "Running" },
    { ref: "qa001ref", name: "qa", display_name: "qa", status: "Running" },
  ];

  it("`name` is the slug — the directory every CLI, old or new, writes — and the display name is beside it", async () => {
    const [project] = await run(() => cli.projects(member, session));
    expect(project?.environments).toEqual(listed);
  });

  it("the single project answers the same environments as the listing", async () => {
    expect((await run(() => cli.project("proj_shop", member))).environments).toEqual(listed);
  });

  it("the ref → project lookup names the environment by the same slug", async () => {
    expect(await run(() => cli.environment("pnl01ref", member, session)))
      .toEqual({ project_id: "proj_shop", ref: "pnl01ref", name: "main" });
  });
});
```
(b) `cloud/platform/server/modules/shared/environment-slug.test.ts` sonuna ekle:
```ts

describe("what people call an environment (FR-106)", () => {
  it("is the name someone gave it, exactly as they typed it", () => {
    expect(slugs.displayName("Staging (EU)", "staging")).toBe("Staging (EU)");
    expect(slugs.displayName("Production", "main")).toBe("Production");
  });

  it("is its slug when nobody named it — never its ref, never an empty label", () => {
    for (const name of [null, "", "   "]) {
      expect(slugs.displayName(name, "qa"), JSON.stringify(name)).toBe("qa");
    }
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — İki koşu:
  - Run: `(cd cloud/platform/server && npm test -- modules/cli/cli.environment-slug.pg.test.ts)` · Beklenen: **FAIL**, üç testte de fark. İlk ortam için `+     "name": "Production",`, adsız ortam için `+     "name": "qa001ref",`. Özet: `0 pass` / `3 fail` / `Ran 3 tests across 1 file.`
  - Run: `(cd cloud/platform/server && npm test -- modules/shared/environment-slug.test.ts)` · Beklenen: **FAIL**, `TypeError: slugs.displayName is not a function.`, `9 pass` / `2 fail`.
- [ ] **Adım 3: Uygula** —
  1. `cloud/platform/server/modules/shared/environment-slug.ts`: `derive(...)` metodunun kapanışından sonra, T005'in `held`'inin belge yorumundan (`Every slug this project's environments hold`) ÖNCE ekle:
```ts
  /**
   * What people call an environment: the name someone gave it, or its slug
   * when nobody did. Every surface answers this same label beside the slug —
   * the CLI's `display_name` and the panel's `name` (FR-106) — so a nameless
   * environment is not `main` in one place and a ref in another.
   */
  displayName(name: string | null, slug: string): string {
    return name !== null && name.trim() !== "" ? name : slug;
  }

```
  2. `cloud/platform/server/modules/cli/cli.service.ts`: `CLI_PROJECTS_SQL` içinde `p.name AS env_name,` satırından sonra ekle:
```sql
         -- THE SLUG IS THE ENVIRONMENT'S NAME ON THIS SURFACE (FR-106, D-016):
         -- the CLI writes palbase/environments/<name>/, so it gets the stored,
         -- immutable slug; env_name is only the label beside it.
         p.slug                                      AS env_slug,
```
  `projectsVisibleTo`'nun iki tip satırında (dönüş tipi ve `rows` cast'i) `env_name: string | null;`'dan sonra `env_slug: string;` ekle. `environmentsOfProduct` ve `environmentRecord`'u şununla değiştir:
```ts
  async environmentsOfProduct(productId: string): Promise<Array<{
    ref: string; name: string | null; slug: string; phase: string;
  }>> {
    return (await Database.$query(
      `SELECT ref, name, slug, phase FROM cloud_projects WHERE product_id = $1 ORDER BY created_at`,
      [productId],
    )) as unknown as Array<{ ref: string; name: string | null; slug: string; phase: string }>;
  }

  async environmentRecord(ref: string): Promise<{
    ref: string; name: string | null; slug: string; product_id: string | null;
  } | undefined> {
    const rows = (await Database.$query(
      `SELECT ref, name, slug, product_id FROM cloud_projects WHERE ref = $1`,
      [ref],
    )) as unknown as Array<{ ref: string; name: string | null; slug: string; product_id: string | null }>;
    return rows[0];
  }
```
  3. `cloud/platform/server/modules/cli/cli.ts`: `CliProjectSchema`'nın `environments` nesnesini şununla değiştir:
```ts
  environments: z.array(z.object({
    ref: z.string(),
    /**
     * THE STORED SLUG (FR-106, D-016). Every published CLI turns this field into
     * `palbase/environments/<name>/`, so it carries the one value that is a
     * directory name by construction — older CLIs get a safe directory too.
     */
    name: z.string(),
    /** What people call it — the panel's name for the same environment. */
    display_name: z.string(),
    status: z.string(),
  })),
```
  4. `cloud/platform/server/modules/cli/cli.controller.ts`:
     - `projects()` içindeki `product.environments.push({ … })`'ta şu iki satırı değiştir:
```ts
        // ADLANDIRMA KURALI TEK YERDE: ilk ortamın adı ürünün adıysa `main`.
        name: this.environmentSlug(r.env_name, product.environments.length, r.env_ref, r.name),
```
       Yerine:
```ts
        // THE STORED SLUG, NOT A RULE OVER THE NAME (FR-106, D-016): a rename or a
        // product rename used to move this directory; the slug never moves.
        name: r.env_slug,
        display_name: this.environmentSlugService.displayName(r.env_name, r.env_slug),
```
     - Özel `environmentsOfProduct` metodunu, belge yorumuyla birlikte, şununla değiştir:
```ts
  /**
   * A product's environments in the listing's shape — the slug in `name`, the
   * display name beside it.
   *
   * The single-project route calls this and must answer exactly what the
   * listing does: one shape filled two ways is a contract that changes with the
   * route a reader happened to call.
   */
  private async environmentsOfProduct(productId: string): Promise<CliProjectSchema["environments"]> {
    const rows = await this.cliService.environmentsOfProduct(productId);
    return rows.map((r) => ({
      ref: r.ref,
      name: r.slug,
      display_name: this.environmentSlugService.displayName(r.name, r.slug),
      status: r.phase,
    }));
  }
```
     - `project()`'te `const envs = await this.environmentsOfProduct(p.id, p.name);` → `const envs = await this.environmentsOfProduct(p.id);`.
     - `environment()`'in sonundaki şu bloğu:
```ts
    const productId = row.product_id ?? row.ref;
    const product = row.product_id !== null
      ? await this.cliService.productVisibleTo(row.product_id, caller.userId)
      : { id: row.ref, organization_id: "", name: row.name ?? row.ref, created_at: "" };

    const envs = await this.environmentsOfProduct(productId, product.name);
    const mine = envs.find((e) => e.ref === ref);
    return { project_id: productId, ref, name: mine?.name ?? row.name ?? ref };
```
       şununla değiştir:
```ts
    const productId = row.product_id ?? row.ref;
    if (row.product_id !== null) await this.cliService.productVisibleTo(row.product_id, caller.userId);

    // THE STORED SLUG — the name the listing gives this same environment
    // (FR-106). It used to be derived from the product's other environments,
    // which is why this route read them; the slug is on the row itself.
    return { project_id: productId, ref, name: row.slug };
```
  5. `cloud/platform/server/modules/cli/cli.controller.test.ts` ("bilet çiti — oturumsuz PAT isteğiyle"): fikstürleri yeni satır şekline getir.
     - İki `projectsVisibleTo` satırında `env_name: null,`'dan sonra `env_slug: "main",` ekle.
     - İki `environmentRecord` stub'ında `name: null,`'dan sonra `slug: "main",` ekle.
     - Artık çağrılmayan iki `environmentsOfProduct: async () => [...]` stub satırını sil.

     İddialar değişmez. `name: "main"` beklentisi aynen kalır; artık satırın slug'ından gelir.
  6. `.github/workflows/cloud-server-typecheck.yml`: `flag-publication-postgres` işinin listesinde `modules/fleet/slug-backfill.pg.test.ts` satırından sonra `            modules/cli/cli.environment-slug.pg.test.ts` ekle.
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm run typecheck && npm test -- modules/cli/cli.environment-slug.pg.test.ts modules/cli/cli.controller.test.ts modules/cli/cli.service.test.ts modules/shared/environment-slug.test.ts)` · Beklenen: `tsc --noEmit` exit 0; `70 pass` / `0 fail` / `Ran 70 tests across 4 files.`
- [ ] **Adım 5: Commit** — `git add .github/workflows/cloud-server-typecheck.yml cloud/platform/server/modules/shared/environment-slug.ts cloud/platform/server/modules/shared/environment-slug.test.ts cloud/platform/server/modules/cli/cli.service.ts cloud/platform/server/modules/cli/cli.ts cloud/platform/server/modules/cli/cli.controller.ts cloud/platform/server/modules/cli/cli.controller.test.ts cloud/platform/server/modules/cli/cli.environment-slug.pg.test.ts && git commit -m "feat(cli): CLI listesinde ortamın adı saklanan slug, görünen ad display_name'de (FR-106, D-016)"`

---

### T015: Ortamlar ve bağlar da aynı slug'ı döner; `is_production` yalnız `main`
<!-- deps: [T014] | files: [cloud/platform/server/modules/shared/environment-slug.ts, cloud/platform/server/modules/shared/environment-slug.test.ts, cloud/platform/server/modules/cli/cli.service.ts, cloud/platform/server/modules/cli/cli.ts, cloud/platform/server/modules/cli/cli.controller.ts, cloud/platform/server/modules/cli/cli.controller.test.ts, cloud/platform/server/modules/cli/cli.environment-slug.pg.test.ts, cloud/platform/server/modules/cli/cli.projects.pg.test.ts] | satisfies: [FR-106] -->

**Interfaces:**
- Consumes: `EnvironmentSlugService.displayName` (T014) · `FIRST_ENVIRONMENT_SLUG` (T001) · T014'ün PG fikstürü.
- Produces:
  - `EnvironmentSlugService.isProduction(slug: string): boolean` (`slug === "main"`).
  - `CliService.environmentsWithPlacement(productId)` satırı `slug: string` taşır.
  - `CliService.environmentsOf(productId)` → `Array<{ ref: string; slug: string }>` (artık `name` yok).
  - `CliEnvironmentSchema` = `{ …, name /* slug */, slug, display_name, kind, is_production /* slug === main */, … }`.
  - `CliAppBindingSchema.environment_name` = slug.
  - `CliController`'da ad kuralı YOK. `legacyName`'in tek okuyucusu geri doldurmadır.

İki rota ad kuralını ürün adı GEÇMEDEN çağırıyordu: `/api/v2/projects/{id}/environments` ve `/api/v2/apps/{id}/bindings` (`cli.controller.ts:226-227`, `:291`). Sonuç tutarsızdı: aynı ortam listede `main`, burada `Production` oluyordu; `slug` alanı `Staging (EU)` diyordu.

Bağ sorgusu adsız ortama `COALESCE(p.name, pr.name)` ile **ürünün adını** veriyordu. İki adsız ortam aynı dizine düşerdi.

`is_production` her satırda sabit `true`'ydu. D-036 bunu okusaydı her ortamı varsayılan dala eşlerdi. Gerçek değer: slug'ı `main` olan ortam, yani ekranda adı ne olursa olsun ilk ortam (D-015). Yeni kolon gerekmez.

Controller'ın özel `environmentSlug` metodunun çağıranı kalmadığı için silinir.

**Bilerek değişen test:** `cli.controller.test.ts`'in "ortam adı kuralı" bloğu kuralı controller'ın özel metodu üzerinden ölçüyordu; şartname bu yüzeyin o kuralı sunmasını kaldırıyor. Kural silinmiyor, çünkü geri doldurma (FR-105) oradan başlıyor. Bu yüzden dört iddia, kuralın yaşadığı yere — `environment-slug.test.ts`'e, `legacyName` üzerinden — zayıflatılmadan **taşınır**.

- [ ] **Adım 1: Kırmızı testi yaz** — (a) `cloud/platform/server/modules/cli/cli.environment-slug.pg.test.ts` sonuna ekle:
```ts

describe("the environments and bindings routes answer the same slug (FR-106)", () => {
  it("`/projects/{id}/environments`: name and slug are the stored slug, and only `main` is production", async () => {
    const rows = await run(() => cli.environments("proj_shop", member));
    expect(rows.map(({ ref, name, slug, display_name, is_production }) => ({ ref, name, slug, display_name, is_production })))
      .toEqual([
        { ref: "pnl01ref", name: "main", slug: "main", display_name: "Production", is_production: true },
        { ref: "stg01ref", name: "staging", slug: "staging", display_name: "Staging (EU)", is_production: false },
        { ref: "qa001ref", name: "qa", slug: "qa", display_name: "qa", is_production: false },
      ]);
  });

  it("`/apps/{id}/bindings`: every binding names its environment by the same slug", async () => {
    await fixture.query(
      `INSERT INTO cloud_apps (id, product_id, platform, display_name) VALUES ('app_1', 'proj_shop', 'android', 'Shop')`,
    );
    const bindings = await run(() => cli.bindings("app_1", member));
    expect(bindings.map((b) => [b.environment_ref, b.environment_name]))
      .toEqual([["pnl01ref", "main"], ["stg01ref", "staging"], ["qa001ref", "qa"]]);
  });
});
```
(b) `cloud/platform/server/modules/shared/environment-slug.test.ts` sonuna ekle:
```ts

describe("the production environment (FR-106, D-015)", () => {
  it("is the one whose slug is `main`, whatever it is called on screen", () => {
    expect(slugs.isProduction("main")).toBe(true);
    for (const slug of ["staging", "production", "Production", "featureX"]) {
      expect(slugs.isProduction(slug), slug).toBe(false);
    }
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/cli/cli.environment-slug.pg.test.ts modules/shared/environment-slug.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - `TypeError: slugs.isProduction is not a function.`
  - Ortamlar rotasında: `+     "slug": "Staging (EU)",`, `+     "is_production": true,` (staging ve qa için), `+     "display_name": undefined,`.
  - Bağlarda, adsız ortam için: `+     "Shop",` (ürün adı).
  - `14 pass` / `3 fail` / `Ran 17 tests across 2 files.`
- [ ] **Adım 3: Uygula** —
  1. `cloud/platform/server/modules/shared/environment-slug.ts`:
     - Sınıf yorumundaki iki satırı değiştir. Eski: `environment: creation (FR-102/FR-103), the CLI listing's pre-slug names and` + ` * the backfill (FR-105).`. Yeni: `environment: creation (FR-102/FR-103), the backfill (FR-105) and every` + ` * surface that answers a slug beside a display name (FR-106).`.
     - `legacyName`'in belge yorumunun ilk iki satırını değiştir ve önüne `isProduction` ekle. Değişecek iki satır:
```ts
  /**
   * The name the CLI listing gives an environment BEFORE slugs — the directory
   * on every teammate's disk today, and where the backfill (FR-105) starts.
```
       Yerine:
```ts
  /**
   * Is this the project's production environment — the one its default branch
   * deploys to (D-036)? Exactly the one whose slug is `main`: the first
   * environment, whatever it is called on screen (D-015). Every surface answers
   * this instead of a constant `true`.
   */
  isProduction(slug: string): boolean {
    return slug === FIRST_ENVIRONMENT_SLUG;
  }

  /**
   * The name the CLI listing gave an environment BEFORE slugs (FR-106) — the
   * directory on every teammate's disk until then, and where the backfill
   * (FR-105) starts. No surface serves it any more; the backfill reads it.
```
  2. `cloud/platform/server/modules/cli/cli.service.ts`:
     - Sınıf yorumunda eskimiş başvuruyu sil. Eski iki satır: ` * taşıyordu. İfadeler, hata cümleleri ve yorumlar AYNEN; ad kuralı (\`environmentSlug\`)` + ` * ve HTTP şekli controller'da kalır.`. Yeni tek satır: ` * taşıyordu. İfadeler, hata cümleleri ve yorumlar AYNEN; HTTP şekli controller'da kalır.`
     - `environmentsWithPlacement`'ta üç yerde `name: string | null;`'dan sonra `slug: string;` ekle. SELECT'e `slug` ekle: `SELECT ref, name, slug, phase, cell_id, created_at`.
     - `environmentsOf`'u, yorumuyla birlikte, şununla değiştir:
```ts
  /**
   * The product's environments — the bindings and config routes both read this.
   *
   * THE SLUG, NOT THE NAME: the bindings list turns each environment into a
   * directory in the CLI, and that directory is the stored slug (FR-106). The
   * old `COALESCE(p.name, pr.name)` existed only for the naming rule — it gave
   * every nameless environment the product's name, i.e. the SAME directory.
   * Every row has a slug since the contract step; there is nothing to fall
   * back to.
   */
  async environmentsOf(productId: string): Promise<Array<{ ref: string; slug: string }>> {
    return (await Database.$query(
      `SELECT ref, slug FROM cloud_projects WHERE product_id = $1 ORDER BY created_at`,
      [productId],
    )) as unknown as Array<{ ref: string; slug: string }>;
  }
```
  3. `cloud/platform/server/modules/cli/cli.ts`: `CliEnvironmentSchema`'da:
     - `name: z.string(),`'ın önüne `/** The stored slug, as in the project listing (FR-106, D-016). */` ekle.
     - `slug: z.string(),`'dan sonra `display_name: z.string(),` ekle.
     - `is_production: z.boolean(),`'ın önüne `/** The project's production environment — the one whose slug is \`main\` (D-015, D-036). */` ekle.
  4. `cloud/platform/server/modules/cli/cli.controller.ts`:
     - `environments()`'ta `rows.map((r, i) => ({` → `rows.map((r) => ({`. Ardından şu dört satırı:
```ts
      name: this.environmentSlug(r.name, i, r.ref),
      slug: this.environmentSlug(r.name, i, r.ref),
      kind: "primary",
      is_production: true,
```
       şununla değiştir:
```ts
      // THE SAME SLUG THE LISTING ANSWERS (FR-106): this route called the first
      // environment by its raw name while the listing called it `main`.
      name: r.slug,
      slug: r.slug,
      display_name: this.environmentSlugService.displayName(r.name, r.slug),
      kind: "primary",
      is_production: this.environmentSlugService.isProduction(r.slug),
```
     - `bindings()`'te `envs.map((e, i) => ({` → `envs.map((e) => ({`. Ardından şu iki satırı:
```ts
      // AD, CLI'DA BİR DİZİN VE BİR XCCONFIG ADI OLUYOR — ref değil.
      environment_name: this.environmentSlug(e.name, i, e.ref),
```
       şununla değiştir:
```ts
      // THE NAME BECOMES A DIRECTORY AND AN XCCONFIG NAME IN THE CLI — not the
      // ref, and the same name the listing answers: the stored slug (FR-106).
      environment_name: e.slug,
```
     - Dosyanın sonundaki `ORTAMIN ADI — CLI'da bir DİZİN…` belge yorumunu ve `private environmentSlug(...) { … legacyName(...) }` metodunu bütünüyle sil. Sınıf `apikey()`'le biter.
  5. `cloud/platform/server/modules/cli/cli.controller.test.ts`:
     - `type SlugFn = …` satırını ve `slugOf` köprüsünü, üstündeki iki satırlık yorumla birlikte, sil.
     - "config artifact" testindeki `environmentsOf: async () => [{ ref: "envb", name: null }],` → `environmentsOf: async () => [{ ref: "envb", slug: "main" }],`.
     - `describe("ortam adı kuralı", …)` bloğunu, üstündeki `ORTAM ADI KURALI (FR-053)` yorumuyla birlikte, sil. İddialarını `cloud/platform/server/modules/shared/environment-slug.test.ts` sonuna taşı:
```ts

/**
 * THE PRE-SLUG NAME (FR-053) — moved here from `cli.controller.test.ts`.
 *
 * No surface serves it any more (FR-106), but the backfill (FR-105) starts
 * from exactly the directory it put on every teammate's disk, so the rule
 * stays pinned where it lives.
 */
describe("the name the CLI listing gave an environment before slugs", () => {
  it("a name is kept as it is when no product name is given — the rule really reads it", () => {
    expect(slugs.legacyName("todoapp", 0, "j06bwtuum")).toBe("todoapp");
  });

  it("the first environment named like its product is `main`", () => {
    expect(slugs.legacyName("todoapp", 0, "j06bwtuum", "todoapp")).toBe("main");
    expect(slugs.legacyName("  todoapp  ", 0, "j06bwtuum", "todoapp")).toBe("main");
  });

  it("a SECOND environment named like the product keeps its name", () => {
    expect(slugs.legacyName("todoapp", 1, "mu0028", "todoapp")).toBe("todoapp");
  });

  it("a nameless environment: the first is `main`, the others their own ref", () => {
    expect(slugs.legacyName(null, 0, "j06bwtuum", "todoapp")).toBe("main");
    expect(slugs.legacyName("", 1, "mu0028", "todoapp")).toBe("mu0028");
  });
});
```
  6. `cloud/platform/server/modules/cli/cli.projects.pg.test.ts`: Docker'lı dosya; burada koşmadı, yalnız yorum değişir. Şu iki satırı:
```ts
    // `environmentSlug` ilk ortama `main` adını veriyor ve o kural SIRAYA
    // dayanıyor. Sıra bozulursa ad yanlış ortama takılır.
```
     şununla değiştir:
```ts
    // `palbase project list` prints the environments in this order. The name is
    // no longer DERIVED from the order (it is the stored slug, FR-106); the
    // order is only what the reader sees.
```
- [ ] **Adım 4: Yeşil** — Run: `(cd cloud/platform/server && npm run typecheck && npm test -- modules/cli/cli.environment-slug.pg.test.ts modules/cli/cli.controller.test.ts modules/cli/cli.service.test.ts modules/shared/environment-slug.test.ts)` · Beklenen: tsc exit 0; `73 pass` / `0 fail` / `Ran 73 tests across 4 files.` Ayrıca `grep -n "environmentSlug(\|legacyName" cloud/platform/server/modules/cli/cli.controller.ts` → çıktı yok (exit 1).
- [ ] **Adım 5: Commit** — `git add cloud/platform/server/modules/shared/environment-slug.ts cloud/platform/server/modules/shared/environment-slug.test.ts cloud/platform/server/modules/cli/cli.service.ts cloud/platform/server/modules/cli/cli.ts cloud/platform/server/modules/cli/cli.controller.ts cloud/platform/server/modules/cli/cli.controller.test.ts cloud/platform/server/modules/cli/cli.environment-slug.pg.test.ts cloud/platform/server/modules/cli/cli.projects.pg.test.ts && git commit -m "feat(cli): ortamlar ve bağlar da saklanan slug'ı döner, is_production yalnız main (FR-106)"`

---

### T016: Panel, filo görünümü ve dağıtım akışı aynı cevabı verir — adsız ortam slug'ıyla görünür, `is_production` yalnız `main`
<!-- deps: [T015] | files: [cloud/platform/server/modules/panel/panel.controller.ts, cloud/platform/server/modules/panel/panel.module.ts, cloud/platform/server/modules/panel/panel.environment-slug.pg.test.ts, cloud/platform/server/modules/environments/deployment-activity.sql.ts, cloud/platform/server/modules/environments/deployment-activity.ts, cloud/platform/server/modules/panel/panel.deployment-activity.pg.test.ts, cloud/platform/studio/src/content/docs/getting-started/introduction.md, .github/workflows/cloud-server-typecheck.yml] | satisfies: [FR-106, FR-104] -->

**Interfaces:**
- Consumes: `EnvironmentSlugService.displayName`, `.isProduction` (T014, T015) · `SharedModule` (dışa verir) · `CliController` yüzeyleri (T014, T015).
- Produces:
  - `PanelController` kurucusunun SON parametresi `environmentSlugService: EnvironmentSlugService`.
  - `GET /v1/panel/environments`, `/environments/{ref}`, `/projects/{id}/environments`: `name` = `displayName(name, slug)`, `slug` = saklanan slug, `is_production` = `slug === "main"`.
  - `GET /v1/panel/fleet/environments`: `name` aynı kuralla, `is_production` gerçek değer.
  - `GET /v1/panel/deployment-activity`: `environmentName` = `displayName(name, slug)`.
  - `DEPLOYMENT_ACTIVITY_SQL` `p.name AS environment_name, p.slug AS environment_slug` seçer.
  - `DeploymentActivityRow.environment_name: string | null`, `environment_slug: string`.
  - `DeploymentActivityService` kurucusu `EnvironmentSlugService` alır.
  - `PanelModule.imports` `SharedModule` içerir.

Panel slug'ı T006'dan beri saklanan değerden veriyor. Ama üç yerde hâlâ eski kural var:
- Panel adsız bir ortamı **ref'iyle** adlandırıyor (`(r.name as string) ?? ref`, `panel.controller.ts:282`). CLI aynı ortama `display_name: "qa"` diyor.
- `is_production` hem ortam listesinde (`:285`) hem operatörün filo görünümünde (`/fleet/environments`) sabit `true`.
- Dağıtım akışı adsız ortamı `COALESCE(p.name, p.ref)` ile ref'iyle etiketliyor (`deployment-activity.sql.ts:22`).

Hepsi T014/T015'in kuralına bağlanır: görünen ad `displayName`, üretim bayrağı `isProduction`.

Kabul resmi tek bir tablodur. Aynı ortam için CLI listesi, ortamlar rotası, bağlar ve panel **aynı slug'ı, aynı etiketi, aynı üretim bayrağını** söyler. Ölçüm gerçek Postgres'te, her yüzeyin kendi SQL'iyle yapılır.

**Yeniden adlandırma kapısı (A2b).** Aynı dosyada gerçek rotalar koşturulur: panelde ortam yeniden adlandırılır, sonra ürün yeniden adlandırılır, sonra CLI listesi ve bağlar okunur. Hiçbir CLI adının kımıldamadığı ölçülür. Doğrulamanın A2b bulgusu buydu: bir ürün yeniden adlandırması `main/`'i `<eski ürün adı>/`'na çeviriyordu. Bu test T014–T015 sonrası baştan yeşildir. Bir **koruma**dır, kırmızı adımın parçası değildir; başka hiçbir test bu yolu uçtan uca sürmüyor.

`PanelController` ve `DeploymentActivityService`, `EnvironmentSlugService`'i enjekte eder. Bu yüzden `PanelModule`, `SharedModule`'ü içe alır; `palbase build` modül grafiğini yargılar. `EnvironmentsModule` zaten alıyor.

Studio belgesi (`introduction.md:130`) iki şey söylüyordu: "her ortam `is_production: true`" ve "ad platform için anlam taşımaz". Bu görevin davranışıyla çelişiyordu; düzeltilir. Belgeyi sabitleyen bir test yok.

`kind: "production"` sabiti bu görevde değişmez; bkz. Fidelity Audit.

- [ ] **Adım 1: Kırmızı testi yaz** — `cloud/platform/server/modules/panel/panel.environment-slug.pg.test.ts` (yeni dosya):
```ts
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "bun:test";
import { fakeDatabase, isolated, withServices } from "@palbase/backend/test";
import { CliController } from "../cli/cli.controller.ts";
import { flagPostgres } from "../shared/flags-postgres.ts";
import { PanelController } from "./panel.controller.ts";

/**
 * ONE ENVIRONMENT, ONE NAME, ON EVERY SURFACE (FR-106).
 *
 * The same environment used to be `main` in the CLI listing, its raw name on
 * the environments and bindings routes and its ref in the panel — and every
 * one of them said `is_production: true`. Each surface runs here with its own
 * SQL on a real Postgres (tables rendered from `toSchemaJSON`), and the
 * assertion is the table a person would draw: per environment, what each
 * surface calls it.
 */
const fixture = flagPostgres([
  "cloud_organizations", "cloud_organization_members", "cloud_products", "cloud_projects",
  "cloud_project_members", "cloud_apps", "cloud_cells", "cloud_deployments",
]);
beforeAll(fixture.initialize);
afterAll(fixture.close);

const db = Object.assign(fakeDatabase().raw, { query: (sql: string, params: unknown[] = []) => fixture.query(sql, params) }) as never;
const panel = isolated().get(PanelController);
const cli = isolated().get(CliController);
const run = <T>(fn: () => Promise<T>) => withServices({ Database: db }, fn);
const owner = { id: "usr_1" };
const session = { headers: new Headers() } as Request;

let slot = 0;
const environment = (ref: string, name: string | null, slug: string, createdAt: string) =>
  fixture.query(
    `INSERT INTO cloud_projects (ref, slot, cell_id, phase, product_id, name, slug, owner_id, created_at)
     VALUES ($1, $2, 'cell-01', 'Running', 'proj_shop', $3, $4, 'usr_1', $5::timestamptz)`,
    [ref, ++slot, name, slug, createdAt],
  );

beforeEach(async () => {
  await fixture.reset();
  await fixture.query(`INSERT INTO cloud_organizations (id, name, tier) VALUES ('org_1', 'Acme', 'free')`);
  await fixture.query(`INSERT INTO cloud_organization_members (organization_id, user_id, role) VALUES ('org_1', 'usr_1', 'owner')`);
  await fixture.query(`INSERT INTO cloud_products (id, organization_id, name, created_by) VALUES ('proj_shop', 'org_1', 'Shop', 'usr_1')`);
  await fixture.query(`INSERT INTO cloud_apps (id, product_id, platform, display_name) VALUES ('app_1', 'proj_shop', 'android', 'Shop')`);
  // Panel-created (D-015): "Production" on screen, `main` on disk; a renamed
  // environment (FR-104); one nobody named.
  await environment("pnl01ref", "Production", "main", "2026-08-01");
  await environment("stg01ref", "Staging (EU)", "staging", "2026-08-02");
  await environment("qa001ref", null, "qa", "2026-08-03");
});

describe("every surface answers the same slug for the same environment (FR-106)", () => {
  it("CLI listing, environments route, bindings and panel: one slug, one label, one production flag", async () => {
    const [listed] = await run(() => cli.projects(owner, session));
    const routed = await run(() => cli.environments("proj_shop", owner));
    const bound = await run(() => cli.bindings("app_1", owner));
    const shown = await run(() => panel.projectEnvironments("proj_shop", owner));

    const surfaces = ["pnl01ref", "stg01ref", "qa001ref"].map((ref) => {
      const l = listed?.environments.find((e) => e.ref === ref);
      const r = routed.find((e) => e.ref === ref);
      const b = bound.find((e) => e.environment_ref === ref);
      const p = shown.find((e) => e.ref === ref);
      return {
        ref,
        slug: [l?.name, r?.slug, b?.environment_name, p?.slug],
        label: [l?.display_name, r?.display_name, p?.name],
        production: [r?.is_production, p?.is_production],
      };
    });
    expect(surfaces).toEqual([
      { ref: "pnl01ref", slug: ["main", "main", "main", "main"], label: ["Production", "Production", "Production"], production: [true, true] },
      { ref: "stg01ref", slug: ["staging", "staging", "staging", "staging"], label: ["Staging (EU)", "Staging (EU)", "Staging (EU)"], production: [false, false] },
      { ref: "qa001ref", slug: ["qa", "qa", "qa", "qa"], label: ["qa", "qa", "qa"], production: [false, false] },
    ]);
  });
});

describe("the operator's fleet view (FR-106)", () => {
  const previous = process.env.PBC_OPERATOR_IDS;
  beforeEach(() => { process.env.PBC_OPERATOR_IDS = "usr_op"; });
  afterEach(() => {
    if (previous === undefined) delete process.env.PBC_OPERATOR_IDS;
    else process.env.PBC_OPERATOR_IDS = previous;
  });

  it("marks only the environment whose slug is `main` as production, under the same label", async () => {
    const fleet = await run(() => panel.listFleetEnvironments({ id: "usr_op" }));
    expect(fleet.map(({ ref, name, is_production }) => [ref, name, is_production])).toEqual([
      ["qa001ref", "qa", false],
      ["stg01ref", "Staging (EU)", false],
      ["pnl01ref", "Production", true],
    ]);
  });
});

describe("the deployment feed names environments the same way (FR-106)", () => {
  it("a push to a nameless environment is shown under its slug, never its ref", async () => {
    await fixture.query(
      `INSERT INTO cloud_deployments (id, ref, status, trigger, created_at)
       VALUES ('dep_qa', 'qa001ref', 'succeeded', 'cli', now()), ('dep_main', 'pnl01ref', 'succeeded', 'cli', now() - interval '1 minute')`,
    );
    const feed = await run(() => panel.deploymentActivity(owner));
    expect(feed.map((d) => [d.environmentRef, d.environmentName])).toEqual([["qa001ref", "qa"], ["pnl01ref", "Production"]]);
  });
});

describe("renames never move a directory (FR-104, FR-106; verification A2b)", () => {
  // A GUARD, green before this task: the product rename that used to flip
  // `main/` to `<old product name>/` went away with T014–T015. No other test
  // drives the real rename routes and then reads a CLI surface.
  it("renaming the environment and the project leaves every CLI name where it was", async () => {
    await run(() => panel.renameEnvironment("stg01ref", { name: "Staging (EU, new)" }, owner));
    await run(() => panel.renameProject("proj_shop", { name: "Shop 2" }, owner));
    const [listed] = await run(() => cli.projects(owner, session));
    expect(listed?.environments.map((e) => [e.ref, e.name, e.display_name])).toEqual([
      ["pnl01ref", "main", "Production"],
      ["stg01ref", "staging", "Staging (EU, new)"],
      ["qa001ref", "qa", "qa"],
    ]);
    const bound = await run(() => cli.bindings("app_1", owner));
    expect(bound.map((b) => b.environment_name)).toEqual(["main", "staging", "qa"]);
  });
});
```
- [ ] **Adım 2: Kırmızı olduğunu GÖR** — Run: `(cd cloud/platform/server && npm test -- modules/panel/panel.environment-slug.pg.test.ts)` · Beklenen: **FAIL**. Çıktıda şunlar görünür:
  - Yüzey tablosunda panelin etiketi `+       "qa001ref",` ve üretim bayrağı `+       true,` (staging ve qa için).
  - Filoda `+     "qa001ref",` / `+     true,`.
  - Dağıtım akışında `-     "qa",` / `+     "qa001ref",`.
  - `1 pass` / `3 fail` / `Ran 4 tests across 1 file.` Geçen tek test yeniden adlandırma korumasıdır.
- [ ] **Adım 3: Uygula** —
  1. `cloud/platform/server/modules/panel/panel.controller.ts`:
     - `import { PitrWindowService, PitrWindowViewSchema } from "../environments/pitr-window";` satırından sonra `import { EnvironmentSlugService } from "../shared/environment-slug";` ekle.
     - Tek satırlık kurucunun sonundaki `private readonly pitrWindowService: PitrWindowService) {}` → `private readonly pitrWindowService: PitrWindowService, private readonly environmentSlugService: EnvironmentSlugService) {}`.
     - `deploymentActivity()`'de `return rows.map(this.deploymentActivityService.deploymentActivity);` satırını şununla değiştir:
```ts
    // A closure, not the bare method: `deploymentActivity` reads its own service.
    return rows.map((row) => this.deploymentActivityService.deploymentActivity(row));
```
     - `listEnvironments()`'ın map'inde şu bloğu:
```ts
        name: (r.name as string) ?? ref,
        // THE STORED SLUG — the directory every teammate's `link` writes; a
        // rename changes `name` above and never this (FR-104).
        slug: r.slug as string,
        kind: "production",
        is_production: true,
```
       şununla değiştir:
```ts
        // THE SAME LABEL THE CLI ANSWERS AS `display_name` (FR-106): a nameless
        // environment is called by its slug here too, not by its ref.
        name: this.environmentSlugService.displayName(r.name as string | null, r.slug as string),
        // THE STORED SLUG — the directory every teammate's `link` writes; a
        // rename changes `name` above and never this (FR-104).
        slug: r.slug as string,
        kind: "production",
        // Only the environment whose slug is `main` (D-015, D-036) — a constant
        // `true` sent every environment to the default branch.
        is_production: this.environmentSlugService.isProduction(r.slug as string),
```
     - `listFleetEnvironments()`'ta üç değişiklik:
       - SELECT'in ilk satırı `SELECT p.ref, p.phase, p.cell_id, p.created_at, p.name,` → `SELECT p.ref, p.phase, p.cell_id, p.created_at, p.name, p.slug,`.
       - Map'te `name: (r.name as string) ?? (r.ref as string),` → `name: this.environmentSlugService.displayName(r.name as string | null, r.slug as string),`.
       - `is_production: true,` → `is_production: this.environmentSlugService.isProduction(r.slug as string),`.
  2. `cloud/platform/server/modules/panel/panel.module.ts`: `import { PanelController } from "./panel.controller";`'dan sonra `import { SharedModule } from "../shared/shared.module";` ekle. `imports` listesinde `MessagingModule,`'den sonra `SharedModule,` ekle.
  3. `cloud/platform/server/modules/environments/deployment-activity.sql.ts`:
     - `p.ref AS environment_ref, COALESCE(p.name, p.ref) AS environment_name,` → `p.ref AS environment_ref, p.name AS environment_name, p.slug AS environment_slug,`.
     - `DeploymentActivityRow`'da `environment_ref: string; environment_name: string;` → `environment_ref: string; environment_name: string | null; environment_slug: string;`.
  4. `cloud/platform/server/modules/environments/deployment-activity.ts`:
     - `import type { PanelDeploymentActivitySchema } from "../panel/deployment-activity";` satırından sonra `import { EnvironmentSlugService } from "../shared/environment-slug";` ekle.
     - Sınıfın başını şununla değiştir. Eski:
```ts
@Injectable()
export class DeploymentActivityService {
  deploymentActivity(row: DeploymentActivityRow): PanelDeploymentActivitySchema {
    return {
      id: row.id, projectId: row.project_id, projectName: row.project_name,
      environmentRef: row.environment_ref, environmentName: row.environment_name,
```
       Yeni:
```ts
@Injectable()
export class DeploymentActivityService {
  constructor(private readonly environmentSlugService: EnvironmentSlugService) {}

  deploymentActivity(row: DeploymentActivityRow): PanelDeploymentActivitySchema {
    return {
      id: row.id, projectId: row.project_id, projectName: row.project_name,
      // THE SAME LABEL EVERY OTHER SURFACE ANSWERS (FR-106): a nameless
      // environment is called by its slug here too, not by its ref.
      environmentRef: row.environment_ref,
      environmentName: this.environmentSlugService.displayName(row.environment_name, row.environment_slug),
```
  5. `cloud/platform/server/modules/panel/panel.deployment-activity.pg.test.ts`: Docker'lı dosya; elle kurulmuş tablosu yeni sorgunun `p.slug`'ını taşımalı. İki satır değişir:
     - `CREATE TABLE cloud_projects (ref text PRIMARY KEY, name text, product_id text, owner_id text, sdk_version text);` → `CREATE TABLE cloud_projects (ref text PRIMARY KEY, name text, slug text NOT NULL, product_id text, owner_id text, sdk_version text);`
     - `INSERT INTO cloud_projects (ref, name, product_id, owner_id) VALUES ('env-a', 'Production', 'product-a', 'owner'), ('env-b', 'Other env', 'product-b', 'other-owner');` → `INSERT INTO cloud_projects (ref, name, slug, product_id, owner_id) VALUES ('env-a', 'Production', 'main', 'product-a', 'owner'), ('env-b', 'Other env', 'main', 'product-b', 'other-owner');`
  6. `cloud/platform/studio/src/content/docs/getting-started/introduction.md`: `` `palbase project create` mints a Project together with its first Environment. `` ile başlayan paragrafı (satır 130) şununla değiştir:
```md
`palbase project create` mints a Project together with its first Environment. A second Environment under the same Project is how you get a staging copy that shares nothing with production. Every Environment has two names: a display name such as `Staging (EU)`, free text you can change at any time, and a **slug** such as `staging`, which never changes — it is the Environment's folder in every checkout (`palbase/environments/<slug>/`), its Android build type and the value `--env` takes. A slug starts with a letter and continues with up to 38 letters, digits or hyphens, and it is unique within the Project regardless of case; `local` is reserved for the stack on your own machine. The first Environment's slug is always `main`, whatever it is called on screen, and it is the only Environment that reports `is_production: true`. There are **no Environment kinds** — every Environment reports `kind: "primary"` — and no preview Environments, no per-pull-request automation and no Git-branch mapping anywhere on the platform. A branch that needs its own database is its own project.
```
  7. `.github/workflows/cloud-server-typecheck.yml`: `flag-publication-postgres` listesinde `modules/cli/cli.environment-slug.pg.test.ts` satırından sonra `            modules/panel/panel.environment-slug.pg.test.ts` ekle.
- [ ] **Adım 4: Yeşil** — Beş koşu:
  - Run: `(cd cloud/platform/server && npm run typecheck && npm test -- modules/panel/panel.environment-slug.pg.test.ts modules/panel/panel.environment-slug.test.ts modules/panel/panel.controller.test.ts modules/panel/panel.sql-columns.test.ts)` · Beklenen: tsc exit 0; `90 pass` / `0 fail` / `Ran 90 tests across 4 files.`
  - Run: `(cd cloud/platform/server && palbase build 2>&1 | tail -1)` · Beklenen: `build OK — 153 route(s) across the controllers would deploy cleanly, plus 1 webhook(s)`. Ölçüm palbase 0.71.2 ile yapıldı. `palbase build` `palbase/palbase-env.d.ts`'i yeniden üretir. Tek fark, bu planla ilgisiz ve tabanda da var olan `cage_toured_at` satırlarıdır. Slug kolonu ve `cloud_environment_slug_backfills` tablosu elle yazılanla BİREBİR çıktı. Pathspec'li commit bu dosyayı almaz; geri almak için `git checkout -- cloud/platform/server/palbase/palbase-env.d.ts`.
  - Docker'lı `panel.deployment-activity.pg.test.ts` burada koşamadı. Yerine, fikstürünün yeni DDL+INSERT'i ve yeni `DEPLOYMENT_ACTIVITY_SQL`, PostgreSQL 16.14'te `PREPARE … EXECUTE('owner')` ile koşturuldu. Dönen satır: `"environment_name":"Production","environment_slug":"main"`. Kontrol olarak eski DDL denendi: `ERROR:  column p.slug does not exist`.
  - Belge testleri `sdk/cli` checkout'u ister. Kazıma kopyasında `sdk/cli`, `/Users/erkutbas/Github_Pallasite/palbase-cli`'ye geçici bir symlink'le verildi; yalnız okundu, sonra silindi. Run: `(cd cloud/platform/studio && npx vitest run src/content/docs/)` · Gözlenen: `Test Files  3 failed | 7 passed (10)`, `Tests  2 failed | 60 passed (62)`. `retired-commands` ve `link-model` bu paragrafla geçti. Kalan üç dosya (`controller-examples`, `retired-surfaces`, `template-sync`) başka sdk parçaları istiyor ve tabanda da düşüyor.
- [ ] **Adım 5: Commit** — `git add .github/workflows/cloud-server-typecheck.yml cloud/platform/server/modules/panel/panel.controller.ts cloud/platform/server/modules/panel/panel.module.ts cloud/platform/server/modules/panel/panel.environment-slug.pg.test.ts cloud/platform/server/modules/environments/deployment-activity.sql.ts cloud/platform/server/modules/environments/deployment-activity.ts cloud/platform/server/modules/panel/panel.deployment-activity.pg.test.ts cloud/platform/studio/src/content/docs/getting-started/introduction.md && git commit -m "feat(panel): is_production yalnız main, adsız ortam slug'ıyla görünür — panel, filo ve dağıtım akışı dahil her yüzey aynı cevabı verir (FR-106)"`

---

## Kanıt (planlama koşusu)

**Kazıma kopyası:** `/private/tmp/claude-501/-Users-erkutbas-Github-Pallasite-palbase-cli/b6cc7adb-70af-442f-a35e-941e6ac5eaa1/scratchpad/plan/cloud`. Revizyon dalı `plan-r2`, taban `f92bc1e02`.

Önceki `plan` dalı (`4b1f28772`) yerinde bırakıldı. Uyarı: `upstream` dalı bir önceki denemeyle `d86521564`'e ilerletilmiş; taban olarak `base` / `f92bc1e02` kullanıldı. Hiçbir şey push'lanmadı, gerçek depolara yazılmadı. Tek istisna: `palbase-cli` T016'nın belge testi için geçici bir symlink'le, yalnız okundu, sonra silindi.

**Görev → commit:**

| Görev | Commit |
|---|---|
| T001 | `54cc8a43a` |
| T002 | `1639c83dc` |
| T003 | `645c46cdc` |
| T004 | `fe52ff0c3` |
| T005 | `416b72975` |
| T006 | `0805091e7` |
| T007 | `6a6b7b117` |
| T008 | `378a39630` |
| T009 | `2d0036796` |
| T010 | `294b35897` |
| T011 | `d1a791283` |
| T013 | `5fc96d246` |
| T014 | `0f858cced` |
| T015 | `c4c469592` |
| T016 | `2ce2c3cbb` |

T012 kodsuz. Değişmeyen görevler için taslağın commit'i test dosyaları → kırmızı → uygulama → yeşil sırasıyla yeniden oynatıldı. T001–T003'te `git diff <eski commit>` boş çıktı.

**Postgres:** kendi başlattığım PostgreSQL 16.14 (`@embedded-postgres/darwin-arm64@16.14.0-beta.17`, `localhost:55471`, `flags_audit_test`). İş bitince `pg_ctl stop` ile durduruldu. Makinede başka bir oturumun 55439'daki Postgres'i çalışıyordu; ona dokunulmadı.

**Her görevde kırmızı/yeşil, gözlenen:**

| Görev | Kırmızı | Yeşil |
|---|---|---|
| T001 | `0/1/1err` | `9` |
| T002 | `4/6` | `10` |
| T003 | `33/2` | `35` |
| T004 | `45/7` | `52` |
| T005 | `36 pass/5 fail/2 errors/41` | `91/4 dosya` |
| T006 | `1/2` | `3` |
| T007 | `9/5/1err/14` | `30/4 dosya` (kendi 3 dosyası: `27`) |
| T008 | `1 failed / 6 passed (7)` | `77 dosya / 636 test` |
| T009 | `0/1/1err` | `18/3 dosya`; kilit ısırması `2/1` |
| T010 | `4 failed / 25 passed (29)` | `29 passed (29)`; tsc/eslint/lint:design temiz |
| T011 | `1 failed / 17 passed (18)`; sunucu `3/1` | `18 passed`; sunucu `4` |
| T013 | `16/2/18` | `65/6 dosya` |
| T014 | pg `0/3`; birim `9/2` | `70/4 dosya` |
| T015 | `14/3/17` | `73/4 dosya` |
| T016 | `1/3/4` | `90/4 dosya`; `palbase build` → `build OK — 153 route(s) …` |

T005 kırmızısındaki karar satırı: `Received: "SagaCompensationFailed: step 'write-canonical-record' failed: Unique constraint violated; the compensation for 'write-canonical-record' failed too (current transaction is aborted, commands ignored until end of transaction block) — A HALF-CREATED RESOURCE EXISTS, manual intervention required"`.

**Mutasyonla ısırma ölçümleri** (her biri geri alındı):
- T005, `const first = held.size === 0;` → "main was deleted" testi düştü (`45/1`).
- T005, `held()`'den `planBackfill` satırı çıkarıldı → 3 test düştü (`49/3`).
- T007, `remaining = plan.length - assigned.length` → PG testinde `Expected: 1` / `Received: 0`; toplam `12/3`.
- T009, Studio kopyasında `ı` katlaması kaldırıldı → `-   "Gelistirme-Ortami"` / `+   "Gelistirme-Ortam"`.

**Tam süitler:**

| | Taban `f92bc1e02` | Son durum `2ce2c3cbb` |
|---|---|---|
| Sunucu (PG ile) | `2223 pass / 10 fail / 6 errors, 2233 test / 170 dosya` | `2292 / 10 / 6, 2302 test / 179 dosya` |
| Studio | `6 failed | 3747 passed | 11 skipped (3764)` | `6 failed | 3753 passed | 11 skipped (3770)` |

- Sunucu kırıkları iki ölçümde aynı 8 Docker dosyası: `docker ps` / `docker run` hataları.
- Studio kırıkları iki ölçümde aynı 5 `src/content/docs` dosyası; `sdk/cli` ve sdk şablonu yok.
- İki tsc de exit 0.
- CI listeleri iş akışından çıkarılıp aynen koşturuldu: `typecheck` `835 pass / 51 dosya` (PG değişkeni olmadan), `flag-publication-postgres` `355 pass / 26 dosya`. Listedeki her dosya mevcut.

**Diğer doğrulamalar:**
- `palbase-cloud` gerçek deposu salt okunur ölçüldü: HEAD `6bbfcdf54`, `rev-list --count HEAD..origin/main` = `1685`, ileri sarılabilir.
- SDK kanıtı:
  - `@palbase/backend` 41.0.0 `DBClient.attempt` / `Database.$attempt` taşıyor.
  - Motorun `diagnosingDriver`'ı abort olmuş transaction için `Database.$attempt` öneriyor.
  - Motor 23505'i `UniqueViolation(constraintOf(e))`'ye çeviriyor.
  - `saga.ts` `runSaga`, `[s, ...done.reverse()]` sırasıyla düşen adımı önce telafi ediyor.
- T013'ün Docker fikstür INSERT metinleri sözleşme biçimli `flagPostgres` tablosunda koşturuldu: dokuzu `accepted`, slug'sız kontrol `null value in column "slug" …`.
- T016'nın Docker fikstürü DDL + yeni `DEPLOYMENT_ACTIVITY_SQL` PG 16.14'te koşturuldu: `"environment_name":"Production","environment_slug":"main"`. Eski DDL ile `ERROR:  column p.slug does not exist`.
- T010'un üretici sondası `✓ wrote …/palbe.gen.ts (17 operations)` verdi ve `name: string; slug?: string; tier?: 'free' | 'pro' | 'scale';` üretti.

**Koşturulamayanlar:**
- 8 Docker'lı sunucu test dosyası (Docker daemon kapalı).
- CI'ın bun 1.3.9'u (yerelde 1.4.2 kullanıldı).
- Studio'nun `controller-examples` / `retired-surfaces` / `template-sync` belge testleri (sdk şablon checkout'u yok).
- golangci-lint (kurulu değil; bu planda Go kodu yok).
- T012'nin operatör fiili ve üretim sorgusu (bulut çağrısı yasak).

**D-021 (main silinemez) — ek koşu:** 4b0fe8e6f fix(environments): ilk ortam = ortamsız projenin ortamı; main başka ortamlar varken kimseye kendiliğinden verilmez (D-021, T005 revizyonu) — on top of 2ce2c3cbb, branch plan-r2; 4ffc14f39 feat(environments): başka ortamlar varken main silinmez — ret sunucuda, her yoldan, yıkımdan önce; yaratma ile main silme proje kilidiyle sıralanır (D-021) — T017, on top of 4b0fe8e6f; 326172c4c fix(environments): main reddi kendi koduyla döner (main_deleted_last) — bu rotada conflict 'silme sürüyor, bekle ve tekrar dene' demek (D-021, T017 doğrulayıcı). On top of 4ffc14f39 (the implementer's T017), branch plan-r2 in the scratch clone. Touches cloud-lifecycle.ts, cloud-lifecycle.test.ts, cloud-lifecycle.delete-main.test.ts, cloud.openapi.yaml and the danger page.test.tsx. No existing commit was rewritten.

#### Verdict: the D-021 guard holds; one contract defect fixed, four smaller items documented

I found no path that deletes `main` while the project has another environment. I also found no legitimate delete that now fails, apart from the documented edge cases below. The one real defect: the refusal used the same wire code as "a delete is already running, wait and retry" (409 `conflict`). I fixed it with commit `326172c4c` on `plan-r2`. The returned T017 text includes the fix; the T005 text gets one added sentence and no code change.

#### The fix (major)
- The refusal is now `HttpError(409, "main_deleted_last", …)` instead of `conflict`.
- **Why it mattered:** on this route `conflict` already means "a delete of this environment is running" (`DeleteInProgress`).
  - `teardown.ts` documents that callers wait and retry on it.
  - `verify-plane.py` retries every 409.
  - palcore deletes a user's project from code (per the route's own doc comment), and would have waited on a refusal that never clears.
- The OpenAPI 409 now names both codes. Tests pin both: the delete-main test expects `main_deleted_last`, and the existing running-delete test now also asserts `conflict`.

#### What I checked (the Silme yolları section of T017 has the path list and line numbers)
- **Other delete paths:**
  - The only other production `DELETE FROM cloud_projects` is the create saga's compensation, which only removes the row its own request inserted.
  - `CloudLifecycleService.remove` has exactly one caller, the route.
  - `TeardownService.deleteProject` has exactly one caller.
  - No SQL `DELETE`, table-API delete or `TRUNCATE` of `cloud_projects` exists outside tests.
  - Nothing updates `product_id`. Transfer moves the whole product; the slug backfill only fills `slug IS NULL` rows.
  - `reap-retained` deletes no rows. The Studio `/api/v2` routes start Temporal workflows and never touch `cloud_projects`. There is no panel "delete project" route.
- **Backfill edge cases:**
  - `planBackfill` gives `main` to the oldest row whenever that row has no slug, so the pre-backfill guard and the backfill agree on which row is `main`.
  - A project with environments but no `main` can only come from an old server instance deleting `main` during the rolling deploy (not tested).
  - If a slug-less oldest row coexists with a stored `main` on another row, `planBackfill` throws, so every create and delete in that project returns 500. This is the same failure T005's create path already has and should not happen in practice.
- **Races and retries:**
  - Delete `main` alongside delete `staging`: the guard can refuse conservatively, but never lets both leave the project without `main` while siblings remain.
  - Create alongside delete `main`: covered by the PG test.
  - Transfer and rename: no lock cycle. Transfer locks the row, then the product; rename locks only the product. Taking the product lock first would deadlock with transfer, so the implementer's order is right.
  - A second delete of the same ref from the same process shares the first one's result (`singleFlight`).
  - Retry after `delete_incomplete`: see the known limit below.
- **Message:** English and actionable. The CLI prints it verbatim through `fmt.Fprintln` (`APIError` has no `ExitCode`). `@palbase/web` passes unmapped codes through with `message = error_description`. The Studio "trpc" layer is a direct REST shim with no error rewrapping.

#### Measured (all runs are mine)
Postgres was my own embedded PostgreSQL 16.14 on `localhost:55493/flags_audit_test`, now stopped. The throwaway copy is `…/b6cc7adb…/scratchpad/plan/cloud-d021-verify` (branch `v-pos`, which is the replay's T004 → revised T005 → T006 → T017 plus my fix; it borrows the replay's git objects through alternates).

| What | Where | Result |
|---|---|---|
| Implementer's T017 files | final state `4ffc14f39` | `83 pass / 0 fail` |
| Guard unwired in `remove()`, exports kept | same | `3 pass / 3 fail`, each `Received promise that resolved: Promise { <resolved> }` |
| Teardown call removed | same | order test fails with `-   "deletable:abc12345m",` |
| `FOR KEY SHARE` dropped | same | PG `1 pass / 2 fail`, `Received: "granted"` |
| Red | T017's own position | `25 pass / 4 fail / 3 errors / Ran 29 tests across 6 files` (identical to the implementer's) |
| Studio test before the change | T017's position | `Tests  6 passed (6)` |
| Green | T017's position | `83 pass / 0 fail / Ran 83 tests across 6 files`; server tsc exit 0 |
| Neighbouring suites | same | `58 pass / Ran 58 tests across 5 files` |
| Refusal code back to `conflict` | same | `5 pass / 1 fail` |
| Studio mutation (sentence replaced) | same | `Tests  1 failed \| 5 passed (6)` |
| Studio vitest / tsc / eslint | same | `Tests  6 passed (6)` / exit 0 / exit 0 |
| Full server suite | same | `2279 pass / 10 fail / 6 errors, Ran 2289 tests across 177 files` |
| T005 files, green | T005's position (`7b24d6a72`) | `92 pass / Ran 92 tests across 4 files` |
| T005 full suite | same | `2265 / 10 / 6, Ran 2275 across 174 files` |
| T005 rejected-option mutation | same | `46 pass / 1 fail`, diff `-   "slug": "Production",` / `+   "slug": "main",` |
| Full server suite | final `326172c4c` | `2304 pass / 10 fail / 6 errors, Ran 2314 tests across 181 files`; the failures are the 8 Docker files |
| CI `typecheck` list, run as CI does | final | `Ran 843 tests across 52 files`, 0 fail |
| CI `flag-publication-postgres` list | final | `Ran 359 tests across 27 files`, 0 fail; no listed file missing |
| Wire body | — | `{"error":"main_deleted_last","error_description":"main is this project's default environment and cannot be deleted while other environments exist — delete the others first","status":409,"request_id":"req_probe"}` |
| CLI transport probe (httptest, throwaway clone, since deleted) | — | `main_deleted_last (409) [request_id req_probe]: main is this project's …`; `namedTransient=false calls=1` |

#### Not run by me
- `npm run lint:design`: the implementer ran it at their final state; I changed no design tokens.
- The Docker-dependent tests.
- CI's bun 1.3.9.
- An HTTP request through the SDK router.
- The full CLI command against a real plane.

#### Documented rather than changed (minor)
- **Lock waits:** a create that arrives during the last `main`'s delete (about 68 s) waits for it, then provisions. Together they can exceed Envoy's 120 s envelope. A refused `main` delete that waits behind a create holds `main`'s row lock the whole time, so wake and billing on `main` wait too.
- **Retry after `delete_incomplete`:** if the delete of the last `main` stops halfway and someone creates another environment before the retry, the retry is refused. The half-torn `main` stays until that environment is deleted; the server can't tell it apart from an intact `main`.
- **T005 depends on T017:** T005 now states it must not go live without T017's server part (same deploy).
- **Step 2's red is a missing-export `SyntaxError`:** the guard-unwired mutation above is what shows the tests fail for the right reason; it is now in T017 Step 4.

#### Lead: lines to update in plan-cloud.md
- **Review Focus item 3:** replace with the implementer's text, with two changes:
  - "Beklenen: `409 main_deleted_last` + `main is this project's default environment and cannot be deleted while other environments exist — delete the others first`"
  - Add to "Bakılacak": "palcore `main`'i EN SON silmeli ve `main_deleted_last`'i bekleyip tekrar denenecek bir cevap saymamalı (o `conflict`'tir: silme sürüyor)."
- **A-1 "Karar" bullet:** "… sunucuda reddedilir: 409 `main_deleted_last`, kural ve çıkış yolu cümlede."
- **Kanıt task→commit table:** add the T017 fix `326172c4c`.
- **"Son durum":** unchanged from the implementer's figures, which I re-measured: 2304/10/6 (2314/181), CI 843/52 and 359/27.
- **Implementer's other list** (:11, "Bileşen yüzeyinden farklı imza", :537, Deploy sırası): still applies unchanged.

#### Repo state
- Real `palbase-cli`: read only, status unchanged (only the pre-existing untracked `docs/paltimate/2026-09-26-android-ortam-build-type/`).
- Scratch clone: clean at `326172c4c`.
- Nothing was pushed and there were no cloud calls.
