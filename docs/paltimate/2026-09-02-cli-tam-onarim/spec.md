# CLI Tam Onarım — Artım 1: Kapılar ve Beş P0 — Şartname

**Tarih:** 2026-09-03 · **Durum:** onay bekliyor
**Tasarım:** `./design.md` · **Karar günlüğü:** `./decisions.md` (50 karar) · **Kanıt:** `./research.md`

## Problem ve Hedef

Palbase CLI'ın beş kusuru yayındaki binary'de duruyor ve beşi de yeşil kapıların altından geçti. Bunun ikinci kanıtı 2026-09-03'te ölçüldü: `palbase start`'ı kıran geçersiz compose için bir düzeltme yazıldı (`ca3565c`), kapı yeşil kaldı, `v0.53.0` kesildi — ve `docker compose config` dosyayı **hâlâ reddediyor**. Bir düzeltmeyi düzeltme-olmayandan ayırt edecek ölçüm yok.

Bu artımın hedefi: **kapılar gerçek araçla ölçsün, ve beş P0 kapansın.** Bitiş durumu: `palbase start` bir iskelet projede yığını gerçekten kaldırır; `flags list`, `notifications remove`, Android üretimi ve `init`→`build` çalışır; ve her biri, bir daha sessizce kırılırsa CI'ın göreceği bir kapıya bağlıdır.

## Fonksiyonel Gereksinimler

### Kapılar

- **FR-001** WHEN CI ya da yerel kapı koşarsa THEN sistem vendor'lanan compose dosyasını **gerçek `docker compose config`** aracına vermeli ve araç reddederse kapı SHALL başarısız olsun. (Docker yoksa test atlanır ve atlandığını yazar.)
- **FR-002** WHEN `palbase start` ya da `palbase stop` bir grup için compose dosyasını vendor'larsa THEN üretilen dosya SHALL `docker compose config` tarafından geçerli bulunsun — `barman` servisi ve beyan edilmemiş `barmandata` volume'ü artık dosyada olmasın.
- **FR-003** WHEN CI koşarsa THEN `gofmt -l .` çıktısı boş değilse iş SHALL başarısız olsun.
- **FR-004** WHEN CI koşarsa THEN `go vet ./...` başarısızsa iş SHALL başarısız olsun.
- **FR-005** WHEN CI koşarsa THEN `golangci-lint run` bulgu raporlarsa iş SHALL başarısız olsun (`continue-on-error` yok) — `.golangci.yml`'in kendi beyanını gerçek yapar.
- **FR-006** WHEN CI koşarsa THEN `go vet -tags e2e ./tests/e2e/` başarısızsa iş SHALL başarısız olsun.
- **FR-007** WHEN `tests/e2e` derlenirse THEN paket SHALL güncel `auth.LoadDPoPKey` imzasıyla derlensin.
- **FR-008** WHEN kapı koşarsa THEN CLI kaynağındaki her HTTP yol literali SHALL sunucunun servis ettiği rota kümesinde bulunsun; bulunmayan her literal kapıyı düşürsün.
- **FR-009** WHEN kapı koşarsa THEN test dışı kaynakta kullanıcıya basılan hiçbir dize SHALL Türkçe karakter taşımasın (yorumlar hariç).
- **FR-010** IF bir testin `npm install` adımı başarısız olursa THEN CI ortamında test SHALL başarısız olsun, atlanmasın; atlama yalnız aracın hiç kurulu olmamasına izinlidir.
- **FR-011** WHEN `go test ./... -short` koşarsa THEN toplam duvar saati NFR-001'deki bütçeyi aşmasın.

### Beş P0

- **FR-012** WHEN `palbase flags list` bir yığından yanıt alırsa THEN sistem SHALL `{"flags":[…]}` zarfını çözsün ve insan tablosunu bassın; boş kümede *"this stack declares no flags"* yolu erişilebilir olsun.
- **FR-013** WHEN `palbase notifications remove <provider>` koşarsa THEN sistem SHALL sağlayıcı adını önce yapılandırma kimliğine çözsün ve silmeyi o kimlikle istesin; ad hiçbir yapılandırmayla eşleşmezse adlandırılmış bir hata versin.
- **FR-014** WHEN bir checkout android platformu için bağlanırsa THEN CLI SHALL Gradle eklentisinin okuduğu artefaktları yazsın: eklentinin beklediği yoldaki sözleşme dosyası ve `app_id`/`base_url`/`api_key` alanlarını taşıyan yapılandırma; ve yerel yığın adresleri için `https` şartı SHALL kaldırılsın.
- **FR-015** WHEN yayımlanmış `@palbase/backend`'in en yeni kararlı sürümüyle boş bir dizinde `palbase init` koşulup ardından `palbase build` çalıştırılırsa THEN `build` SHALL sıfır çıkış koduyla bitsin.
- **FR-016** WHEN `palbase init` ile iskeleti kurulmuş bir projede `palbase start` koşulursa THEN yığın SHALL ayağa kalksın ve `/.well-known/palbase.json` 200 dönsün; ardından `palbase stop` yığını kapatsın ve `.palbase/local.json` kalmasın. *(Üretim giriş noktasından uçtan uca kanıt.)*

### Bayat metin

- **FR-017** WHEN bir geliştirici `init.go`'nun sürüm seçme gerekçesini okursa THEN yorum SHALL registry'nin bugünkü durumunu anlatsın — `latest`'in v1 hattında tutulduğu iddiası kaldırılsın.

## Fonksiyonel Olmayan Gereksinimler

- **NFR-001** `go test ./... -short` tek makinede **≤ 180 sn** (bugün ölçülen: 455 sn, tek başına `internal/backend`).
- **NFR-002** CI işi (kapılar dâhil) **≤ 20 dk**.
- **NFR-003** Birim testleri (`-short`) ağ erişimi olmadan geçmeli; ağ isteyen testler ayrı etikete taşınır.

## Kapsam Dışı

- Kol B (ortam modeli), C (`start` doğruluğu), D (tek `link`), E (seçim emekliliği), F (modül sözleşmesi + güvenlik) — Artım 2 ve 3.
- Sürüm/imaj pinlerinin **ağdan** çözülmesi (D-030, kullanıcı kararı).
- `@palbase/backend@26.0.0`'ın npm'e yayımlanması — **başka bir oturum tarafından yürütülüyor**; bu şartname yalnız sonucunu FR-015 ile doğrular.
- `pull`'un dosya-başına inceltilmesi ve git'siz projeler için içerik-hash defteri (K-05, K-06).

## Sınır Durumları

| Durum | Karşılayan |
|---|---|
| Docker kurulu değil | FR-001 — kapı atlar ve atladığını yazar; sessiz yeşil yok |
| `golangci-lint` yerel toolchain'de koşamıyor | FR-005 — CI pinli sürümle koşar; yerel koşum CI'ı temsil etmeli |
| Sunucu rota listesi okunamıyor | FR-008 — kapı **düşer**, "eşleşme yok" diye geçmez |
| Sağlayıcı adı birden fazla yapılandırmayla eşleşiyor | FR-013 — belirsizlik adlandırılır, rastgele seçim yok |
| Yerel yığın `http://127.0.0.1` | FR-014 — `https` şartı kalkar |
| `init` ağsız makinede | FR-015 — registry sorusu ağ ister; ağsızsa adlandırılmış hata |
| `start` sırasında port çakışması | FR-016 — mevcut port seçme mekanizması korunur |

## Teknik Yaklaşım

### Kararlar (design.md'den taşındı, yeniden tartışılmaz)

| ID | Karar | Neden kazandı | Kanıt |
|----|-------|---------------|-------|
| D-1 | Kapılar **gerçek aracı çalıştırır**, taklit etmez | Taklit eden kapı iki kez yalan söyledi: P0-1 yayına çıktı, sonra düzeltmesi de yeşil kapının altından geçti | `research.md` CB-01, CB-02 |
| D-2 | Vendor'lanan artefakt **çıktı olarak** doğrulanır, üreten fonksiyon olarak değil | Karşılaştırmanın iki tarafı da aynı bozuk fonksiyondan geçtiği için eşitlik testi kusuru göremedi | `research.md` CB-03 |
| D-3 | `.golangci.yml`'in beyanı **gerçek** yapılır | Dosya "ci.yml bunu bloklayan kapı olarak koşar" diyor; koşmuyor. Beyan ya doğrulanır ya silinir | `research.md` CB-04 |
| D-4 | P0-5 **eklenti tarafında** çözülür | Tek yazıcı ilkesi: CLI dört platforma tek şekil yazar; uyum sağlayacak olan tüketicidir | design.md §4 |
| D-5 | 26.0.0 yayını bu şartnamenin **dışında**, sonucu FR-015 ile doğrulanır | Yayın başka oturumda yürüyor; iki yazıcı olmaz | decisions.md D-050 |

### Bileşen yüzeyi (görev sınırı aşanlar)

- **C-1 `composeConfigIsValid(t, path)`** (`internal/backend/stackfiles_test.go`) — Amaç: vendor'lanan dosyayı gerçek `docker compose config`'e vermek. Docker yoksa `t.Skip` + gerekçe. FR-001, FR-002 bunu paylaşır.
- **C-2 `servedRoutes() []string`** (`cmd/palbase/surface_test.go`) — Amaç: sunucu kaynağından servis edilen rota kümesini çıkarmak; C-3 tüketir. FR-008.
- **C-3 `cliRouteLiterals() []string`** (`cmd/palbase/surface_test.go`) — Amaç: CLI kaynağındaki HTTP yol literallerini toplamak. FR-008.
- **C-4 `providerConfigID(ctx, name) (string, error)`** (`internal/notifications/notifications.go`) — Amaç: sağlayıcı adını yapılandırma kimliğine çözmek; `remove` tüketir. FR-013.

`[PLAN-FREE: test dosyası adları, yardımcı fonksiyon iç yapıları, CI adımlarının sırası ve iş adları, Türkçe-dize kapısının tarama regex'inin tam biçimi.]`

### Impact Map

Yollar `sdk/cli/` köküne görelidir; çapraz-depo satırı ayrıca işaretlenmiştir.

| Yol | Create/Modify | Sorumluluk | FR'ler |
|-----|---------------|------------|--------|
| `internal/backend/stackfiles_test.go` | modify | gerçek `docker compose config` negatif kontrolü (C-1); `withoutBarman` sınır düzeltmesi | FR-001, FR-002 |
| `internal/backend/stackfiles/docker-compose.dev.yml` | modify | `barman` servisi ve `barmandata` bağı çıkar | FR-002 |
| `internal/backend/stackfiles.go` | modify | vendor'lama yazımı doğrulanmış çıktı üretsin | FR-002 |
| `.github/workflows/ci.yml` | modify | gofmt · vet · golangci-lint · `-tags e2e` derlemesi adımları | FR-003, FR-004, FR-005, FR-006 |
| `tests/e2e/mgmt_api_test.go` | modify | `auth.LoadDPoPKey` çağrısı güncel imzaya | FR-007 |
| `cmd/palbase/surface_test.go` | modify | rota-literal kapısı (C-2, C-3) + Türkçe-dize kapısı | FR-008, FR-009 |
| `internal/backend/testdeps_test.go` | modify | CI'da `npm install` hatası `t.Fatal` | FR-010 |
| `internal/backend/build_test.go` | modify | ağ isteyen/uzun testler `-short` dışına | FR-010, FR-011 |
| `internal/flags/flags.go` | modify | zarf çözümü; ham-gövde fallback'i kalkar | FR-012 |
| `internal/flags/flags_test.go` | modify | fikstür gerçek sunucu zarfına | FR-012 |
| `internal/notifications/notifications.go` | modify | ad→kimlik çözümü (C-4) | FR-013 |
| `internal/notifications/notifications_test.go` | modify | fikstür zarf + kimlik | FR-013 |
| `internal/backend/cloud_environments.go` | modify | android yolunda eklentinin okuduğu sözleşme dosyası korunur | FR-014 |
| `internal/backend/app_environments.go` | modify | android yuvası eklentinin okuduğu alanları taşır | FR-014 |
| `internal/backend/init.go` | modify | bayat `latest` yorumu düzeltilir | FR-017 |
| `internal/backend/scaffold_e2e_test.go` | create | yayımlanmış SDK'ya karşı `init`→`build` uçtan uca | FR-015 |
| `internal/backend/start_e2e_test.go` | create | `start`→`/.well-known` 200→`stop` uçtan uca | FR-016 |
| **ÇAPRAZ DEPO** `sdk/palbackend-android-src/codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt` | modify | çok-ortam config okuma + `https` şartının kalkması | FR-014 |
| **ÇAPRAZ DEPO** `sdk/palbackend-android-src/codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt` | modify | sözleşme dosyası yolu CLI'ın yazdığıyla eşleşir | FR-014 |

### Sıralama kısıtları

1. **FR-001 önce FR-002.** Kapı, düzeltmeden ÖNCE konur ve **kırmızı görülür** — aksi hâlde düzeltmenin işe yaradığının kanıtı olmaz. Bu artımın bütün gerekçesi budur.
2. **FR-007 önce FR-006.** Derlenmeyen bir suite'i CI'a bağlamak işi kırar.
3. **FR-011 (bütçe) FR-010'dan sonra** — atlanan testler koşmaya başlayınca süre değişir.
4. FR-015, 26.0.0 yayınına bağlıdır (dış bağımlılık, aşağıda).
5. Diğerleri sıradan bağımlılık düzeninin ötesinde kısıt taşımaz.

## Önkoşullar (kullanıcı/dış kaynaklı)

| # | Önkoşul | Durum |
|---|---------|-------|
| P-1 | `@palbase/backend@26.0.0` npm'e yayımlanmış olmalı (FR-015'in ölçtüğü şey) | **Yürütülüyor** — başka oturum, kullanıcı onayıyla |
| P-2 | Compose artefaktını düzelten commit tek elden gelmeli (aynı dosyaya ikinci yazıcı olmaz) | Koordinasyon açık — bu şartname kapıyı (FR-001) üstlenir; artefakt (FR-002) hangisi önce gelirse |
| P-3 | Android submodule'üne yazma erişimi (`sdk/palbackend-android-src`) | Var (yerel checkout) |
| P-4 | CI'da `golangci-lint` için pinli sürüm seçimi | Karar bu şartnamede: `.golangci.yml` `version: "2"` ile uyumlu en son kararlı sürüm pinlenir |

## Kanıt

Bkz. `./research.md` — 17 kod-tabanı iddiası (`file:line` + alıntı, donma anında yeniden temellendirildi) ve dış kaynak satırları. UD register'ında `status: open` satır yok.
