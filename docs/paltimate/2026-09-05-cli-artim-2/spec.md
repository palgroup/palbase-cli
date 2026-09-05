# CLI Tam Onarım — Artım 2: `start` doğruluğu, tek `link`, seçim katmanının emekliliği — Şartname

**Onaylı tasarım:** `../2026-09-02-cli-tam-onarim/design.md` — Kol C (§3), Kol D (§4), Kol E (§5).
**Kararlar:** `../2026-09-02-cli-tam-onarim/decisions.md` (D-001…D-050); bu şartname onları
operasyonelleştirir, yeniden tartışmaz. Bu artımda eklenen kararlar D-051…D-053.
**Artım 1 bitti** ve defteri `../2026-09-02-cli-tam-onarim/deviations.md`'de.
**Artım 3'e kalan:** Kol B (ortam modeli) ve Kol F (modül sözleşmesi + güvenlik hijyeni).

---

## Problem ve Hedef

Üç kusur sınıfı, tasarımın üç kolu:

1. **`palbase start` hangi yığını kaldırdığını bilmiyor ve söylemiyor.** İmaj etiketi projeye değil
   BINARY'ye ait; proje bir sürüm beyan edemiyor. Ölçüldü: dört imaj pininden **biri**
   (`pgvector/pgvector:pg16`) hiçbir Go kontrolünde yok, ve fiilen koşan değer Go sabiti değil
   compose varsayılanı (Artım 1'de `TestTheGoConstantsAndTheComposeDefaultsAgree` bu ikisini
   hizaladı ama pgvector'ü hâlâ göremiyor).
2. **`link` dört ayrı komutta yaşıyor ve üçü aynı işi yapıyor.** `.palbase/project.json` varken
   `ios|macos|android|web link` zaten `runLink`'e iniyor; ayrı komutların geriye kalan tek işi
   CLI'ın kendi tavsiyesiyle hiç ulaşılamayan bir bulut dalı.
3. **İkinci bir adresleme mekanizması sessizce iş görmüyor.** `--project/--environment` +
   `selection.json`, sunucuda **olmayan** bir rotaya dayanıyor (`GET /api/v2/projects`), yani
   bayraklar 15+ komutta hiçbir şey seçmiyor.

**Koşu bittiğinde:** proje kendi yığın sürümünü beyan eder ve `start` onu kaldırdığını yazar; tek
`palbase link` platformu algılar ve emekli komutlar shim bırakmadan gider; seçim katmanı ve ona
dayanan ölü kol silinir; ve Artım 1'de bilerek ertelenen rota kapısı — kendisini yeşil yapan işle
birlikte — yerine konur.

---

## Fonksiyonel Gereksinimler

### Kol C — `start` doğru yığını kaldırsın

- **FR-001** WHERE bir checkout `.palbase/project.json` taşır THEN o dosya SHALL tek bir anlamsal
  yığın sürümü alanı taşısın; alan yoksa CLI SHALL onu kurulu `@palbase/backend`'den türetip
  dosyaya YAZSIN (dosya commit'lenir, `.palbase/local.json` gibi gitignore'lanmaz).
- **FR-002** WHEN `palbase start` hangi imajları kaldıracağına karar verirse THEN sürüm→imaj
  tablosunu SHALL `@palbase/backend` paketinden okusun; ağ isteği, sunucu ucu ve CLI sürüm
  yükseltmesi SHALL gerekmesin.
- **FR-003** WHEN `palbase start` bir yığın kaldırırsa THEN kaldırdığı her imajı etiketiyle SHALL
  yazsın.
- **FR-004** WHEN `palbase start` "hazır" derse THEN hazırlık SHALL runtime'ı da kanıtlasın; runtime
  bundle'ı reddederken banner "hazır" SHALL dememesi.
- **FR-005** WHEN yığın imaj pinleri değişirse THEN parite kapısı SHALL **dört** pinin dördünü de
  görsün — `pgvector/pgvector:pg16` dâhil; bugün o pin hiçbir kontrolde yok.
- **FR-006** WHEN `palbase upgrade` yerel bir yığında koşarsa THEN SHALL yapabileceğini yapsın ya da
  yapamadığını ADIYLA söylesin; bugün `refFromTargetURL` loopback adreste `""` döndüğü için komut
  ölü uca düşüyor.
- **FR-007** WHEN `palbase stop` koşarsa THEN `.palbase/local.json`'ı silmeden ÖNCE SHALL başarılı
  olsun ve vendor'lanan compose belgesini SHALL ezmesin.

### Kol D — Tek `palbase link`

- **FR-008** WHEN kullanıcı `palbase link` koşarsa THEN CLI SHALL checkout'un platformlarını
  ALGILASIN (`hasApple`, `hasWeb`, `detectAndroidApplicationID`) ve bulduğu her platform için
  artefaktları yazsın.
- **FR-009** WHEN kullanıcı `palbase ios link`, `palbase macos link`, `palbase android link`,
  `palbase web link` ya da `palbase <platform> use` koşarsa THEN komut SHALL var olmasın ve CLI
  SHALL sıfırdan farklı çıksın — shim, uyarı-ve-devam, yönlendirme YOK (D-003).
- **FR-010** WHEN bir checkout bir projeye bağlıysa THEN `palbase unlink` SHALL bağı kaldırsın.
- **FR-011** WHEN `--platform` bilinmeyen bir değer taşırsa THEN CLI SHALL onu ADIYLA reddetsin;
  bugün `--platform bogus` kabul ediliyor.
- **FR-012** WHERE iki kod yolu aynı işi yapıyorsa (`gatherEnvironments`↔`addLocalStack`,
  `resolveNativeApp`↔`resolveWebApp`) THEN tek uygulama SHALL kalsın.

### Kol E — Seçim katmanının emekliliği

- **FR-013** WHEN bir komut hedefini belirlerse THEN hedef SHALL `.palbase/project.json`'dan (ve
  koşan yığın varsa `.palbase/local.json`'dan) gelsin; `--project`/`--environment` bayrakları ve
  `.palbase/selection.json` SHALL var olmasın.
- **FR-014** THEN `internal/apps` (617 satır), `internal/hook` (494 satır) ve deploy'un github
  `repository_provider` kolu (12 üretim referansı) SHALL silinsin — çağıranlarıyla birlikte, shim
  bırakmadan.
- **FR-015** WHEN CLI bir projeyi çözerse THEN sunucuda olmayan `GET /api/v2/projects` çağrısı
  (`internal/selection/resolve.go:192`) SHALL yapılmasın.
- **FR-016** WHEN kaynak kodda bir HTTP rota literali varsa THEN bir kapı SHALL onun sunucunun
  servis ettiği bir rotaya karşılık geldiğini ölçsün ve karşılığı olmayanı ADIYLA reddetsin.
  *(Artım 1'de bilerek ertelendi — o gün doğsaydı kırmızı doğardı çünkü FR-015'in kaldırdığı çağrı
  duruyordu; numara `008` izlenebilirlik için boş bırakılmıştı, Artım 1 Changelog A-3.)*

### Entegrasyon (zorunlu)

- **FR-017** WHEN bir kullanıcı `palbase init` → `palbase start` → `palbase link` → `palbase push`
  zincirini ÜRETİM giriş noktasından (derlenmiş binary) koşarsa THEN zincir SHALL uçtan uca
  tamamlansın; testin fonksiyonları elle sıralaması SAYILMAZ.

---

## Fonksiyonel Olmayan Gereksinimler

- **NFR-001** `go test ./... -short` **≤ 180 sn** kalır (Artım 1'de 148,35 sn ölçüldü).
- **NFR-002** Her yeni kapı **doğduğu gün yeşil** olur; advisory kapı yasak.
- **NFR-003** Silinen komutlar shim bırakmaz: `palbase ios link` → bilinmeyen komut, `exit ≠ 0`.
- **NFR-004** Artım 1'in kapıları bozulmaz: gofmt, vet, e2e-vet, golangci-lint (CI **ve** release),
  `requireToolOnCI`, `requiresRealToolchain`.

---

## Kapsam Dışı

- **Kol B (ortam modeli)** ve **Kol F (modül sözleşmesi + güvenlik hijyeni)** — Artım 3.
- **İmaj/pin ağdan çözme** — D-030, kullanıcı kararı ("bu image işine girme"). FR-002'nin tablosu
  pakette dağıtılır, ağda değil.
- **`v2-cloud` tarafında rota ekleme/kaldırma.** FR-016'nın kapısı sunucunun BUGÜN servis ettiğini
  okur; sunucuyu değiştirmez.

---

## Sınır Durumları

- **`@palbase/backend` kurulu değilken `start`.** FR-001'in türetmesi kaynaksız kalır → CLI ne
  yapamadığını adıyla söyler; sessizce gömülü varsayılana düşmez (Supabase'in 1 numaralı tuzağı,
  D-036).
- **Sürüm alanı var ama tabloda karşılığı yok.** Bilinmeyen sürüm ADIYLA reddedilir; en yakın
  sürüme yuvarlanmaz.
- **Platform algılanamıyor** (ne Apple, ne web, ne Android). `link` ne aradığını ve nerede
  bulamadığını söyler; sessizce hiçbir şey yazmaz.
- **Bağlı checkout'ta çıplak `link`.** Bugün düşüyor; FR-008 sonrası mevcut bağı yeniden çözmeli.
- **`selection.json` taşıyan eski bir checkout.** Dosya artık okunmaz. Varlığı hata sebebi
  DEĞİLDİR — okunmadığı söylenir ve komut devam eder (kalıntı dosya bir kesinti sebebi olamaz).
- **FR-016'nın kapısı bir rota literalini çözemezse** (değişkenden kurulan yol) — kapı onu
  ADLANDIRIR ve ölçemediğini söyler; sessizce atlamaz (Artım 1'in N-2 dersi).

---

## Teknik Yaklaşım

### Kararlar (design.md'den taşındı, yeniden tartışılmaz)

- **D-035/D-039 · Config'te tek anlamsal sürüm alanı, servis başına tag YOK.** Beyan yoksa kurulu
  SDK'dan türetilir, sonra YAZILIR ve commit'lenir. Supabase'in iki tuzağından da kaçınır:
  binary'ye kaynaklı değil, ve `.temp` gibi gitignore'lu değil.
- **D-036 · Servis-başına tag pinlemek REDDEDİLDİ.** Supabase'in 14 imaj alanı `toml:"-"` ile
  kullanıcıdan yapısal olarak gizli; bu bir kaza değil düşünülmüş ret — kullanıcıya servis başına
  tag vermek ona bir uyumluluk matrisi vermektir.
- **D-023/D-038 · Sürüm→imaj tablosu `@palbase/backend` paketinde.** Expo'nun hamlesi. Tabloyu
  tazelemek için CLI sürümü gerekmez, `npm i` yeter; ağ ucu yok, D-030 ile çelişmez.
- **D-003 · Geriye uyum YOK.** Emekli komutlar shim'siz sökülür.

### Bu artımda eklenen kararlar

- **D-051 · `tests/e2e` SİLİNMİYOR, seçim katmanından ARINDIRILIYOR.**
  Tasarım §5 onu silinecekler arasında sayıyor. Gerekçesi geçerli (seçim katmanına dayanıyor), ama
  sonucu iki kalıcı kuralla çelişiyor: *cross-boundary E2E zorunlu*, ve *bir kapıyı silmek
  bilgisini de siler*. Artım 1 o pakete bir CI kapısı bağladı (`go vet -tags e2e ./tests/e2e/`,
  bloklayıcı) çünkü paket bir kez derlenemez hâle gelmiş ve kimse görmemişti. Silmek o kapıyı da
  götürür.
  **Karar:** paket kalır; içindeki seçim-katmanı bağımlılığı kaldırılır. Bu, tasarımın amacını
  (ikinci adresleme mekanizmasının emekliliği) tam karşılar ve kapıyı korur.
  **Ölçüm (2026-09-05):** `tests/e2e` 163 satır, tek dosya (`mgmt_api_test.go`), Artım 1'de ölü
  konağı Artım 1'de düzeltildi.
- **D-052 · Pin sayısı DÖRT, yedi değil.** Tasarım §3 "pgvector dâhil yedi pin" diyor; ölçüldü
  (2026-09-05): compose dört `image:` satırı taşıyor (`envoy`→`${PALBASE_EDGE_IMAGE}`,
  `postgres`→`pgvector/pgvector:pg16` **sabit**, `palsvc`, `runtime`), `docker compose config
  --services` dört servis döndürüyor, ve `start.go:65` `stackImages` üç eleman taşıyor. Yani
  kontrolsüz pin **bir** tanedir ve adı `pgvector`. FR-005 ölçülen gerçeğe yazıldı.
- **D-053 · Emekli komut = KOMUTUN YOKLUĞU, "kaldırıldı" mesajı değil.**
  Bir "bu komut kaldırıldı, şunu kullan" dalı bırakmak, sökülen yüzeyi başka biçimde YAŞATMAKTIR ve
  kullanıcının bu koşu için verdiği direktifle çelişir (*"kendin legacy de yaratma"*). Cobra'nın
  bilinmeyen-komut hatası zaten doğru şeyi söylüyor ve `exit ≠ 0` veriyor.

### Bileşen yüzeyi (görev sınırı aşanlar)

- **C-1** `func stackVersion(dir string) (string, error)` — checkout'un beyan ettiği yığın sürümü;
  yoksa kurulu SDK'dan türetir, dosyaya yazar ve yazdığını döndürür.
- **C-2** `func imagesFor(version string) ([]stackImage, error)` — sürüm→imaj tablosunu
  `@palbase/backend` paketinden çözer. Bilinmeyen sürüm hata döndürür, yuvarlamaz.
- **C-3** `func detectPlatforms(dir string) []Platform` — checkout'ta hangi platformların olduğunu
  söyler. `hasApple`/`hasWeb`/`detectAndroidApplicationID` bunun altına iner.
- **C-4** `func routeLiterals(root string) ([]RouteLiteral, error)` — FR-016'nın kapısı için: kaynak
  ağacındaki HTTP rota literallerini `file:line` ile toplar (AST, regex değil — Artım 1'in dersi).

`[PLAN-FREE: her fonksiyonun iç yardımcıları, test dosyası adları, ve silinen kodun yerine kalan
yapıların yerel şekli.]`

### Impact Map

| Dosya | İşlem | Neden | FR |
|---|---|---|---|
| `internal/backend/target.go` | modify | `Target` yığın sürümü alanını taşır | FR-001 |
| `internal/backend/target_test.go` | modify | sürüm alanının ölçüsü | FR-001 |
| `internal/backend/start_test.go` | modify | tablo çözümü, imaj yazımı, hazırlık, `stop` ölçüleri | FR-002, FR-003, FR-004, FR-007 |
| `internal/backend/upgrade_test.go` | modify | yerel yığında ölü ucun ölçüsü | FR-006 |
| `internal/backend/project_link_test.go` | modify | algılama, `--platform` doğrulaması, `unlink` ölçüleri | FR-008, FR-010, FR-011 |
| `internal/backend/backend.go` | modify | emekli komut grupları kayıttan düşer | FR-009 |
| `internal/backend/start.go` | modify | `stackImages` ve `isRegistryImage` burada yaşıyor | FR-002, FR-003, FR-004, FR-005, FR-007 |
| `internal/backend/stackfiles/docker-compose.dev.yml` | modify | `postgres` pini değişkene bağlanır | FR-005 |
| `../../v2/deploy/docker-compose.dev.yml` | modify | vendor'lananın ORİJİNALİ; parite kapısı ikisini karşılaştırıyor | FR-005 |
| `internal/backend/stackfiles_test.go` | modify | parite kapısı dördü de görür | FR-005 |
| `internal/backend/upgrade.go` | modify | yerel yığında ölü uç | FR-006 |
| `internal/backend/project_link.go` | modify | tek `link`, platform algılama, `--platform` doğrulaması, `unlink` | FR-008, FR-010, FR-011 |
| `internal/backend/planes.go` | modify | algılama yardımcıları tek yüzeye iner | FR-008 |
| `internal/backend/native_link.go` | modify | `ios`/`macos` komut grupları söküldü; ortak çözüm kalır | FR-009, FR-012 |
| `internal/backend/web_link.go` | modify | `web` komut grubu söküldü; ortak çözüm kalır | FR-009, FR-012 |
| `internal/backend/android_link.go` | modify | `android` komut grubu söküldü | FR-009 |
| `internal/backend/ios_use.go` | delete | `<platform> use` emekli | FR-009 |
| `internal/backend/app_environments.go` | modify | `gatherEnvironments` tekilleşir | FR-012 |
| `internal/backend/cloud_environments.go` | modify | `addLocalStack` tekilleşir | FR-012 |
| `internal/backend/deploy.go` | modify | github kolu söküldü | FR-014 |
| `internal/selection/resolve.go` | modify | ölü rota çağrısı ve seçim çözümü kalkar | FR-013, FR-015 |
| `internal/selection/config.go` | modify | seçim dosyasının okuyucusu/yazıcısı kalkar | FR-013 |
| `internal/selectiontest/fake.go` | modify | ölü rotanın fikstürü kalkar | FR-013, FR-015 |
| `internal/apps/` | delete | 617 satır, seçim katmanına dayanıyor | FR-014 |
| `internal/hook/` | delete | 494 satır, çağıranı seçim katmanı | FR-014 |
| `cmd/palbase/main.go` | modify | emekli komut grupları kaydedilmez | FR-009, FR-013, FR-014 |
| `cmd/palbase/surface_test.go` | modify | golden komut listesi küçülür; emekli komutların YOKLUĞU ölçülür | FR-009, NFR-003 |
| `cmd/palbase/routes_test.go` | create | FR-016'nın rota kapısı | FR-016 |
| `tests/e2e/mgmt_api_test.go` | modify | seçim katmanı bağımlılığı kalkar (D-051) | FR-013 |
| `internal/backend/link_e2e_test.go` | create | FR-017'nin uçtan uca zinciri | FR-017 |
| `../palbase-ts/backend/stack-images.json` | create | sürüm→imaj tablosunun kendisi | FR-002 |
| `../palbase-ts/backend/package.json` | modify | tablo yayınlanan dosyalara girer | FR-002 |
| `../palbase-ts/backend/__tests__/stack-images.test.ts` | create | tablonun şekli ve dört imajı ölçülür | FR-002 |

### Sıralama kısıtları

1. **FR-015 (ölü rota kalkar) FR-016'dan (rota kapısı) ÖNCE.** Kapı o çağrı dururken doğarsa
   kırmızı doğar — Artım 1'de tam bu sebeple ertelendi.
2. **FR-013/FR-014 (söküm) FR-008…FR-012'den (tek `link`) SONRA ya da BİRLİKTE.** Tasarım §8: "D ve
   E birlikte çünkü aynı dosyalara dokunuyor."
3. **FR-001 (sürüm alanı) FR-002'den (tablo) önce** — tablo neyi çözeceğini bilmeli.
4. **FR-005'in kapısı, `postgres` pini değişkene bağlandıktan sonra** yeşil doğar.

---

## Önkoşullar (kullanıcı/dış kaynaklı)

- **`@palbase/backend`'in sürüm→imaj tablosu — ÇÖZÜLDÜ, kapsama alındı (Changelog A-3).**
  Pre-flight ölçümü: paket (v33.0.0) böyle bir tablo TAŞIMIYOR. Yalnız CLI tarafını yazmak `palbase
  start`'ı kırardı — tablo yok, imaj yok, yığın kalkmaz. Kullanıcı kararı: tablo SDK paketine
  eklenir ve sürüm kesilir (D-023'ün tasarladığı şekil). Tablo bu artımın kapsamında; yayın D-048
  gereği plan içinden yapılır, ad-hoc değil.
- Ek kimlik/erişim gerekmiyor: bu artım ağ ucu eklemiyor (D-030).

---

## Kanıt

Her kod iddiası `research.md`'de `file:line` + verdict ile duruyor; hepsi 2026-09-05'te yeniden
temellendirildi (`confirmed`/`stale`).

---

## Changelog

- **A-5 · 2026-09-05 · `stackfiles.go` haritadan çıktı, `start.go` girdi; `isRegistryImage` T003'e katıldı.**
  İki ölçüm: (1) `stackImages` `start.go:65`'te yaşıyor, `stackfiles.go`'da değil — harita yanlış
  dosyayı gösteriyordu. (2) `postgres`'i pin listesine eklemek `isRegistryImage`'in gerekçesini
  çürütüyor (*"nothing in this stack defaults to one"* — artık ediyor); düzeltilmezse `ensureImages`
  Docker Hub kısa formunu YEREL sanıp `palbase start`'ı ilk koşuda kırardı. Kural sadeleşti: slash
  varsa registry referansıdır.

- **A-4 · 2026-09-05 · Impact Map +1: `v2/deploy/docker-compose.dev.yml` (execute, T003 önü).**
  Vendor'lanan compose, Artım 1'in parite kapısıyla (`TestTheVendoredComposeMatchesTheRepository`)
  `v2/deploy`'daki ORİJİNALİNE bağlı. `postgres` pinini yalnız vendor'lanan kopyada değişkene
  bağlamak o kapıyı KIRARDI. Orijinal de değişir; **varsayılan aynı kalıyor**
  (`${PALBASE_POSTGRES_IMAGE:-pgvector/pgvector:pg16}`), yani koşan yığının davranışı değişmez —
  yalnız override edilebilir ve Go tarafından görülebilir hâle gelir.

- **A-3 · 2026-09-05 · Sürüm→imaj tablosu kapsama alındı; Impact Map +3 satır.** Execute'un
  pre-flight taraması bir regresyon riski buldu: `@palbase/backend` v33.0.0 bir sürüm→imaj tablosu
  taşımıyor, yani FR-002'nin CLI yarısı tek başına inseydi `palbase start` çalışmayı bırakırdı.
  Kullanıcıya soruldu (kapsam kenarı, pre-flight'ın tam amacı) → tablo SDK paketine eklenir ve sürüm
  kesilir. Üç satır Impact Map'e girdi; plan T014'ü kazandı ve T002 ona bağlandı.

- **A-2 · 2026-09-05 · Impact Map +6 satır; iki metin düzeltmesi (planlama sırasında, `validate` bulgusu).**
  `plan-tools validate` beş test dosyasının ve `backend.go`'nun haritada olmadığını gösterdi — plan
  onları yazacaktı, yani harita eksikti. Ayrıca iki metin kusuru: "Neden" sütununda backtick içine
  yazılan dosya adı validator tarafından bir YOL sanılıyordu, ve Artım 1'in bir gereksinim numarasına yapılan atıf
  bu şartnamenin kapsama tablosunu kirletiyordu (burada tanımsız bir numara). Üçü de düzeltildi.

- **A-1 · 2026-09-05 · Şartname yazıldı.** Kol C+D+E kapsama alındı; tasarımın "yedi pin" ve
  "`tests/e2e` silinir" iddiaları ölçümle düzeltildi (D-051, D-052).
