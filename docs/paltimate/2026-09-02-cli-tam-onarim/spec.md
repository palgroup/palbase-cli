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
- **FR-009** WHEN kapı koşarsa THEN test dışı kaynakta kullanıcıya basılan hiçbir dize SHALL Türkçe karakter taşımasın; ölçülen **altı** ihlal İngilizceye çevrilir: `transport/rest.go:307` ve `:313`, `auth/auth.go:322`, `auth/dpop_storage.go:73`, `backend/stack_bundle.go:1177`, `project/project.go:69`.
  > **Sayım düzeltildi (Changelog A-4).** İlk ölçüm satır-tabanlı bir regex'ti ve dizeyi `fmt.Errorf(`'ten SONRAKİ satırda taşıyan üç ihlali kaçırdı. Kapı bu yüzden regex değil **Go AST'si** ile yazılır: `parser.ParseFile` yorumları yapısal olarak dışarıda bırakır, `BasicLit` yalnız gerçek dizeleri verir.
- **FR-010** IF bir testin `npm install` adımı başarısız olursa THEN CI ortamında test SHALL başarısız olsun, atlanmasın; atlama yalnız aracın hiç kurulu olmamasına izinlidir.
- **FR-011** WHEN `go test ./... -short` koşarsa THEN toplam duvar saati NFR-001'deki bütçeyi aşmasın.

### Beş P0

- **FR-012** WHEN `palbase flags list` bir yığından yanıt alırsa THEN sistem SHALL `{"flags":[…]}` zarfını çözsün ve insan tablosunu bassın; boş kümede *"this stack declares no flags"* yolu erişilebilir olsun.
- **FR-013** WHEN `palbase notifications remove <provider>` koşarsa THEN sistem SHALL sağlayıcı adını önce yapılandırma kimliğine çözsün ve silmeyi o kimlikle istesin; ad hiçbir yapılandırmayla eşleşmezse adlandırılmış bir hata versin.
- **FR-014** WHEN bir checkout android platformu için bağlanırsa THEN CLI SHALL Gradle eklentisinin okuduğu artefaktları yazsın: eklentinin beklediği yoldaki sözleşme dosyası ve `app_id`/`base_url`/`api_key` alanlarını taşıyan yapılandırma; ve yerel yığın adresleri için `https` şartı SHALL kaldırılsın.
- **FR-015** WHEN yayımlanmış `@palbase/backend`'in en yeni kararlı sürümüyle boş bir dizinde `palbase init` koşulup ardından `palbase build` çalıştırılırsa THEN `build` SHALL sıfır çıkış koduyla bitsin.
  > **Durum (2026-09-04 ölçümü): ZATEN SAĞLANIYOR.** P0-2, 27.1.0'ın yayımlanmasıyla kapandı — `init` → `build` yayındaki 0.53.0 ile exit 0 (kanıt: `research.md` CB-18a). FR düşürülmüyor, **karakteri değişiyor**: düzeltme değil **regresyon kapısı**. `scaffold_e2e_test.go` bu yüzden hâlâ gerekli — bu yol daha önce yayında sessizce kırıldı ve onu yakalayan bir kapı yoktu.
- **FR-016** WHEN `palbase init` ile iskeleti kurulmuş bir projede `palbase start` koşulursa THEN yığın SHALL ayağa kalksın ve `/.well-known/palbase.json` 200 dönsün; ardından `palbase stop` yığını kapatsın ve `.palbase/local.json` kalmasın. *(Üretim giriş noktasından uçtan uca kanıt.)*

### Bayat metin

- **FR-017** WHEN bir geliştirici `init.go`'nun sürüm seçme gerekçesini okursa THEN yorum SHALL registry'nin bugünkü durumunu anlatsın — `latest`'in v1 hattında tutulduğu iddiası kaldırılsın.

### Lint borcu (planlama sırasında eklendi — bkz. Changelog A-1)

- **FR-018** WHEN `golangci-lint run` depo üzerinde koşarsa THEN sıfır bulgu SHALL raporlansın; ölü semboller silinir ve gerçek bulgular düzeltilir.
  > **Neden bir FR gerekti:** FR-005 lint kapısını **bloklayıcı** yapıyor, ama ölçüldüğünde 20 dosyada 48 bulgu var (errcheck 2 · staticcheck 7 · unused 39) ve 15 dosya Impact Map'te değildi. Kapıyı borç ödenmeden açmak ya CI'ı kırar ya da kapıyı advisory'ye düşürür — ikincisi [[feedback_no_advisory_gates_pay_the_debt]] ile yasak. Borcun içinde **gerçek bir kusur** var: `internal/backend/start.go` yığın `.env`'ine mühür yazarken `Close()` hatasını yutuyor (denetim E-3) — kısa yazımda yarım mühürlü yığın bırakır.
  > **Kapsam sınırı:** yalnız `unused` sembollerin silinmesi ve 9 errcheck/staticcheck bulgusunun düzeltilmesi. Kol E'nin büyük emeklilikleri (`internal/apps` 617 satır, github kolu, `internal/hook`) Artım 2'de kalır — bunlar `unused` raporlamıyor çünkü kendi içlerinde birbirlerini çağırıyorlar.

### Takip döngüsü (bitiş kapısında kullanıcı onayıyla alındı — Changelog A-6)

- **FR-019** WHEN `palbase push` bir artefakt yüklerse THEN istek SHALL bir `Idempotency-Key` taşısın ve zaman aşımına uğrayan bir yükleme AYNI anahtarla yeniden denensin; böylece inen ama cevabı kaybolan bir yükleme ikinci bir deploy'a dönüşmesin.
- **FR-020** WHEN `TestStartServesAndStopCleansUp` bir CI runner'ında koşarsa THEN yığın SHALL ayağa kalksın; bind-mount edilen durum dizini konteynerin kullanıcısı tarafından yazılabilir olsun ve test opt-in bayrağına ihtiyaç duymasın.
- **FR-021** WHEN `tests/e2e` bir taban adres seçerse THEN varsayılan SHALL dağıtılmış bir adres olsun; hiç dağıtılmamış `api.dev.palbase.studio` varsayılan olmaktan çıksın.

## Fonksiyonel Olmayan Gereksinimler

- **NFR-001** `go test ./... -short` tek makinede **≤ 180 sn** (bugün ölçülen: 455 sn, tek başına `internal/backend`).
- **NFR-002** CI işi (kapılar dâhil) **≤ 20 dk**.
- **NFR-003** Birim testleri (`-short`) ağ erişimi olmadan geçmeli; ağ isteyen testler ayrı etikete taşınır.

## Kapsam Dışı

- Kol B (ortam modeli), C (`start` doğruluğu), D (tek `link`), E (seçim emekliliği), F (modül sözleşmesi + güvenlik) — Artım 2 ve 3.
- Sürüm/imaj pinlerinin **ağdan** çözülmesi (D-030, kullanıcı kararı).
- SDK'nın npm'e yayımlanması — başka oturumun kulvarı. **Sonuç (2026-09-04): 26.0.0 hiç yayımlanmadı; yayımlanan 27.0.0 ve 27.1.0 oldu ve P0-2 bununla kapandı.** Bu şartname yayını üstlenmiyor, yalnız sonucunu FR-015 ile kapıya bağlıyor.
- `pull`'un dosya-başına inceltilmesi ve git'siz projeler için içerik-hash defteri (K-05, K-06).
- **Rota-literal ↔ sunucu-rota kapısı** (Changelog A-3; numarası boş bırakıldı). Ölçüldü 2026-09-04: ölü rota `/api/v2/projects` hâlâ çağrılıyor (`internal/selection/resolve.go:192`) ve onu kaldıran iş Kol E'de. Kapı bugün konulsa **doğduğu gün kırmızı** olurdu; kırmızı doğan kapı ya CI'ı kilitler ya advisory'ye düşer. Artım 2'de, kendisini yeşil yapan işle birlikte gelir. Numara yeniden kullanılmaz — izlenebilirlik için 008 boş bırakıldı.

## Sınır Durumları

| Durum | Karşılayan |
|---|---|
| Docker kurulu değil | FR-001 — kapı atlar ve atladığını yazar; sessiz yeşil yok |
| `golangci-lint` yerel toolchain'de koşamıyor | FR-005 — CI pinli sürümle koşar; yerel koşum CI'ı temsil etmeli |
| Lint borcu ödenmeden kapı açılırsa | FR-018 önce, FR-005 sonra — kapı doğduğu gün yeşil olmalı |
| Türkçe kapısı kaynakta ihlal bulursa | FR-009 — üç ihlal çevrilir, kapı sonra konur (aynı sıra) |
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
- **C-2 `providerConfigID(ctx, name) (string, error)`** (`internal/notifications/notifications.go`) — Amaç: sağlayıcı adını yapılandırma kimliğine çözmek; `remove` tüketir. FR-013.

*(C-3/C-4 idi: rota-literal kapısının iki yardımcısı — o kapı Artım 2'ye ertelendiği için düştüler, Changelog A-3.)*

`[PLAN-FREE: test dosyası adları, yardımcı fonksiyon iç yapıları, CI adımlarının sırası ve iş adları, Türkçe-dize kapısının tarama regex'inin tam biçimi.]`

### Impact Map

Yollar `sdk/cli/` köküne görelidir; çapraz-depo satırı ayrıca işaretlenmiştir.

| Yol | Create/Modify | Sorumluluk | FR'ler |
|-----|---------------|------------|--------|
| `internal/backend/stackfiles_test.go` | modify | gerçek `docker compose config` negatif kontrolü (C-1); `withoutBarman` sınır düzeltmesi | FR-001, FR-002 |
| `internal/backend/stackfiles/docker-compose.dev.yml` | modify | `barman` servisi ve `barmandata` bağı çıkar | FR-002 |
| `.github/workflows/ci.yml` | modify | gofmt · vet · golangci-lint · `-tags e2e` derlemesi adımları | FR-003, FR-004, FR-005, FR-006 |
| `tests/e2e/mgmt_api_test.go` | modify | auth.LoadDPoPKey çağrısı güncel imzaya | FR-007 |
| `cmd/palbase/surface_test.go` | modify | Türkçe-dize kapısı | FR-009 |
| `internal/transport/rest.go` | modify | Türkçe hata metni İngilizceye | FR-009 |
| `internal/auth/auth.go` | modify | Türkçe hata metni İngilizceye | FR-009 |
| `internal/auth/dpop_storage.go` | modify | Türkçe hata metni İngilizceye | FR-009 |
| `internal/project/project.go` | modify | Türkçe yer tutucu İngilizceye | FR-009 |
| `internal/backend/testdeps_test.go` | modify | CI'da npm install hatası t.Fatal | FR-010 |
| `internal/backend/build_test.go` | modify | ağ isteyen/uzun testler `-short` dışına; kurulum hatası CI'da ölümcül | FR-010, FR-011 |
| `internal/backend/init_test.go` | modify | `npm pack` hatası CI'da ölümcül (aynı sınıfın ikinci örneği) | FR-010 |
| `internal/flags/flags.go` | modify | zarf çözümü; ham-gövde fallback'i kalkar | FR-012 |
| `internal/flags/flags_test.go` | modify | fikstür gerçek sunucu zarfına | FR-012 |
| `internal/notifications/notifications.go` | modify | ad→kimlik çözümü (C-4) | FR-013 |
| `internal/notifications/notifications_test.go` | modify | fikstür zarf + kimlik | FR-013 |
| `internal/backend/cloud_environments.go` | modify | android yolunda eklentinin okuduğu sözleşme dosyası korunur | FR-014 |
| `internal/backend/app_environments.go` | modify | android yuvası eklentinin okuduğu alanları taşır | FR-014 |
| `internal/backend/init.go` | modify | bayat `latest` yorumu düzeltilir | FR-017 |
| `internal/backend/start.go` | modify | .env mühür yazımında Close() hatası kontrol edilir (E-3) + staticcheck | FR-018 |
| `internal/backend/pull_spec.go` | modify | 5 ölü sembol silinir | FR-018 |
| `internal/backend/deploy.go` | modify | 4 ölü alan silinir | FR-018 |
| `internal/backend/stack_bundle.go` | modify | 1 ölü sembol + 1 staticcheck | FR-018 |
| `internal/backend/stack_push.go` | modify | 1 staticcheck (hata metni noktalaması) | FR-018 |
| `internal/backend/schema_sources.go` | modify | 1 staticcheck | FR-018 |
| `internal/backend/archive.go` | modify | 1 staticcheck | FR-018 |
| `internal/backend/stack_bundle_test.go` | modify | 3 ölü test yardımcısı silinir | FR-018 |
| `internal/backend/plan_test.go` | modify | 2 ölü test yardımcısı silinir | FR-018 |
| `internal/auth/auth_test.go` | modify | 7 ölü test yardımcısı silinir | FR-018 |
| `internal/storage/storage.go` | modify | 4 ölü sembol silinir (config/*.ts kalıntısı) | FR-018 |
| `internal/egress/egress.go` | modify | 3 ölü regex silinir | FR-018 |
| `internal/logs/logs.go` | modify | 1 ölü sembol + 1 staticcheck | FR-018 |
| `internal/project/gitroot.go` | modify | dosya tümüyle ölü — silinir | FR-018 |
| `cmd/palbase/doctor.go` | modify | 1 staticcheck | FR-018 |
| `internal/backend/deploy_test.go` | modify | idempotency anahtarının taşındığını ölçen test | FR-019 |
| `internal/backend/backend.go` | modify | `REST` arayüzü `DoIdempotent`'ı taşır | FR-019 |
| `internal/backend/scaffold_e2e_test.go` | create | yayımlanmış SDK'ya karşı init→build uçtan uca | FR-015 |
| `internal/backend/start_e2e_test.go` | create | start→well-known 200→stop uçtan uca | FR-016 |
| **ÇAPRAZ DEPO** `../palbackend-android-src/codegen-gradle/src/main/kotlin/io/palbase/gradle/GeneratePalbaseTask.kt` | modify | çok-ortam config okuma + `https` şartının kalkması | FR-014 |
| **ÇAPRAZ DEPO** `../palbackend-android-src/codegen-gradle/src/main/kotlin/io/palbase/gradle/PalbaseCodegenPlugin.kt` | modify | sözleşme dosyası yolu CLI'ın yazdığıyla eşleşir | FR-014 |

### Sıralama kısıtları

1. **FR-001 önce FR-002.** Kapı, düzeltmeden ÖNCE konur ve **kırmızı görülür** — aksi hâlde düzeltmenin işe yaradığının kanıtı olmaz. Bu artımın bütün gerekçesi budur.
2. **FR-007 önce FR-006.** Derlenmeyen bir suite'i CI'a bağlamak işi kırar.
3. **FR-011 (bütçe) FR-010'dan sonra** — atlanan testler koşmaya başlayınca süre değişir.
4. FR-015, 26.0.0 yayınına bağlıdır (dış bağımlılık, aşağıda).
5. Diğerleri sıradan bağımlılık düzeninin ötesinde kısıt taşımaz.

## Önkoşullar (kullanıcı/dış kaynaklı)

| # | Önkoşul | Durum |
|---|---------|-------|
| P-1 | Modül raylı bir SDK npm'de yayımlanmış olmalı (FR-015'in ölçtüğü şey) | **ÇÖZÜLDÜ** — 27.1.0 yayında (`latest`=`next`=27.1.0); 26.0.0 hiç çıkmadı. Ölçüm: CB-18a |
| P-2 | Compose artefaktını düzelten commit tek elden gelmeli (aynı dosyaya ikinci yazıcı olmaz) | Koordinasyon açık — bu şartname kapıyı (FR-001) üstlenir; artefakt (FR-002) hangisi önce gelirse |
| P-3 | Android submodule'üne yazma erişimi (`sdk/palbackend-android-src`) | Var (yerel checkout) |
| P-4 | CI'da `golangci-lint` için pinli sürüm seçimi | Karar bu şartnamede: `.golangci.yml` `version: "2"` ile uyumlu en son kararlı sürüm pinlenir |

## Kanıt

Bkz. `./research.md` — 17 kod-tabanı iddiası (`file:line` + alıntı, donma anında yeniden temellendirildi) ve dış kaynak satırları. UD register'ında `status: open` satır yok.

## Changelog

- **A-1 · 2026-09-04 · FR-018 eklendi + Impact Map 15 satır büyüdü.** Planlama sırasında §7 taraması bir karar sızıntısı buldu: FR-005 lint kapısını bloklayıcı yapıyor ama borç ölçülmemişti. Ölçüm: 20 dosyada 48 bulgu (errcheck 2 · staticcheck 7 · unused 39), 15 dosya Impact Map dışında. Kapıyı borç ödenmeden açmak CI'ı kırardı; advisory'ye düşürmek yasak. Borç FR-018 olarak kapsama alındı, sınırı yazıldı (Kol E'nin büyük emeklilikleri Artım 2'de kalıyor). Kapsam büyüdüğü için kullanıcı onayına sunuldu.
- **A-2 · 2026-09-04 · FR-015 karakteri değişti, P-1 çözüldü.** Yeniden doğrulamada CB-18 bayat çıktı: 26.0.0 hiç yayımlanmadı, 27.1.0 çıktı ve P0-2 kapandı (taze ölçüm CB-18a). FR düşürülmedi; düzeltme değil regresyon kapısı oldu.
- **A-3 · 2026-09-04 · rota kapısı Artım 2'ye ertelendi; FR-009 kapsamı ölçüldü; C-3/C-4 düştü; `stackfiles.go` Impact Map'ten çıktı.** Fidelity taraması üç şey buldu: (1) rota kapısı doğduğu gün kırmızı olurdu — ölü `/api/v2/projects` çağrısı `selection/resolve.go:192`'de duruyor ve onu kaldıran iş Kol E'de; kapı kendisini yeşil yapan işle birlikte gider. (2) Türkçe kapısının yeşil olması için üç kaynak dosyanın çevrilmesi gerekiyor — ölçüldü ve Impact Map'e eklendi. (3) `stackfiles.go` yalnız gömülü baytları yazıyor, değişmesi gerekmiyor; sahte satır bırakmak yerine çıkarıldı.

- **A-4 · 2026-09-04 · FR-009 sayımı 3 → 6, Impact Map +1 (`project/project.go`).** İlk ölçüm satır-tabanlı regex'ti; dizeyi çağrıdan sonraki satırda taşıyan üç ihlali kaçırdı. AST taraması altı buldu. `stack_bundle.go` zaten T003'ün Impact Map satırıydı; ihlali T005 kapsamında düzeltiliyor.

- **A-5 · 2026-09-04 · Impact Map +1 (`internal/backend/init_test.go`).** T010 uygulanırken FR-010'un sınıfının ikinci örneği bulundu: `packLocalSDK` `npm pack` başarısızlığını KOŞULSUZ atlamaya çeviriyor (CI dahil). FR-010 bunu zaten talep ediyor; dosya Impact Map'te değildi.

- **A-6 · 2026-09-04 · FR-019/020/021 eklendi.** Bitiş kapısında kullanıcı üç defter keşfini de aldı: `push`'un hiç göndermediği idempotency (D-03), CI'ın tam yığın kaldıramaması (D-04), ve e2e'nin ölü konağı (D-01). Impact Map +2; kalan dosyalar zaten verilmişti.
