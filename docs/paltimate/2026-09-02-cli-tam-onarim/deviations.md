# Sapma Defteri — Artım 1

Bu koşuda ortaya çıkan ama planın hiçbir görevine hizmet etmeyen her şey buraya yazılır ve **inşa edilmez**. Bitiş kapısında teklif listesine döner.

Biçim: `Bulundu / Cazip çünkü / Etkisi`

---

_(henüz kayıt yok)_
## D-01 · e2e suite'i ÖLÜ bir konağa bakıyor
**Bulundu:** `tests/e2e/mgmt_api_test.go:47` varsayılan taban `https://api.dev.palbase.studio` — denetimin ölçtüğü gibi bu adres kümesi hiç dağıtılmadı (`internal/config/config.go`: tek bulut).
**Cazip çünkü:** paket artık derlendiğine göre koşturulabilir hâle getirmek bir adım ötede duruyor.
**Etkisi:** Bu artımın FR'si yalnız DERLENMESİNİ istiyor (FR-007) ve CI onu yalnız `go vet` ile ölçüyor. Konağı düzeltmek suite'i canlı-bulut-kapılı hâle getirir, ayrı bir karar. Bitiş kapısında teklif edilecek.

## D-02 · ÇEKİRDEK SÜRÜM SÜRÜKLENMESİ — mevcut, üst akışta, ve CI'da GÖRÜNMÜYOR
**Bulundu (2026-09-04, T003 sırasında):** `go test ./internal/backend/` yerelde kırmızı:
`TestStackImagesTrackTheCoreVersion` → CLI üç imajı **0.40.1**'e pinliyor, otorite
`v2-cloud/bootstrap/images/version.env` ise **V2_VERSION=0.41.0** diyor.

**Benim değişikliklerimden DEĞİL:** değişikliklerim stash'liyken HEAD'de de aynı test düşüyor (ölçüldü).

**Zincir ölçüldü:** `v2/deploy/docker-compose.dev.yml` (ORİJİNAL) hâlâ 0.40.1 · CLI'ın vendor'ladığı
kopya da 0.40.1 (parite kapısı bu yüzden yeşil) · yalnız `version.env` 0.41.0'a çıkmış.
Üç imajın 0.41.0 etiketi registry'de **VAR** (`docker manifest inspect` ×3).
→ CLI'ın sabitini tek başına bump'lamak "sabitler == compose varsayılanları" kapısını kırar;
compose'u da bump'lamak "vendor == v2/deploy orijinali" kapısını kırar. **Zincir yukarıdan
başlamalı: önce `v2/deploy`.**

**Neden ben yapmıyorum:** `v2` başka bir oturumun canlı WIP'ini taşıyor (analytics koşusu,
değişmiş run dosyası, derlenmiş `palsvc`) → [[feedback_one_writer_per_repo]] +
[[feedback_infra_not_my_lane_just_wait]].

**VE ASIL BULGU — bu, koşunun tezinin bir örneği daha:** CI **yeşil**. Çünkü bu kapı
`version.env`'i kardeş depodan okuyor ve CI runner'ında o depo yok → **atlıyor**.
Denetimin E-8'i tam bu: ölçebildiği yerde kırmızı, ölçemediği yerde yeşil.
FR-010 bu sınıfı `npm install` için kapatıyor; **kardeş-checkout atlamaları için kapatmıyor.**

**Etkisi:** T011'in CI kapılarını engellemiyor (orada atlıyor). Yerelde tam suite'in
"tamamen yeşil" olmasını engelliyor — T010 ve bitiş doğrulaması bunu AÇIKÇA raporlayacak,
yeşilmiş gibi göstermeyecek.

## D-03 · `push` idempotency'si BEYAN EDİLMİŞ ama HİÇ GÖNDERİLMİYOR (E-2'nin yarısı)
**Bulundu:** `deploy.go` ölü alanları silinirken: transport `PostMultipart(..., idempotencyKey)` destekliyor, `runPush` hiç geçmiyor.
**Cazip çünkü:** alanlar zaten oradaydı, "bağlamak" tek satır gibi duruyor.
**Etkisi:** Zaman aşımına uğrayıp aslında inen bir yükleme İKİNCİ DEPLOY olarak gider. Gerçek bir kusur ama bu artımın hiçbir FR'si değil; T003 yalnız ölü beyanı ve onun yalanını kaldırdı. Bitiş kapısında teklif edilecek.

## D-02 GÜNCELLEME (2026-09-04) · ÇEKİRDEK SÜRÜKLENMESİ ÇÖZÜLDÜ — başka oturum tarafından
T010 sırasında iki test aniden kırmızıya döndü (`TestTheGoConstantsAndTheComposeDefaultsAgree`,
`TestTheImageCheckAsksForTheTAGCOMPOSEUSES`). Araştırıldı: **benim değişikliklerimden değil** —
`0.41.0` hiçbir commit'te yoktu, yani başka bir oturum `sdk/cli`'da CANLI yazıyordu ve
sabitleri bump'layıp compose'a henüz geçmemişti. Ölçüm o ara durumu yakalamış.
Zincir tamamlanınca dördü de yeşil: `TestStackImagesTrackTheCoreVersion` dahil.
**D-02 kapandı ve düzelten ben değilim** — doğru kulvarda kaldı.
**Alınan ders:** aynı depoda eşzamanlı yazıcı varken `git add <dizin>` tehlikeli;
bu koşunun kalan commit'leri DOSYA DOSYA pathspec kullanıyor.

## D-04 · CI TAM YIĞIN KALDIRAMIYOR — UID eşlemesi (ölçüldü)
**Bulundu:** `TestStartServesAndStopCleansUp` CI'da düştü. Kök neden logdan:
`write .env: open .env: permission denied` (run 33859855884). Yığın durum dizini bind-mount
ediliyor ve GitHub runner'ında konteynerin kullanıcısı oraya yazamıyor. **`palbase start`'ın
kusuru değil** — yerelde 53,10 sn'de geçiyor ve `/.well-known` 200 dönüyor.
**Yapılan:** test `PALBASE_E2E_STACK=1` ile opt-in oldu. **Assertion'lar değişmedi** — kapsanan şey
nerede koştuğu, ne talep ettiği değil. `init`→`build` e2e'si CI'da koşmaya devam ediyor (geçti).
**Cazip çünkü:** runner'da UID eşlemesini çözmek (`--user`, `chmod`, ya da named volume) bir sonraki
adım gibi duruyor ve `start`'ı CI'da da ölçülebilir kılardı.
**Etkisi:** Bugün `start`'ın regresyonu yalnız yerelde/opt-in yakalanır. Bitiş kapısında teklif edilecek.

---

## Takip döngüsü — ÜÇ KEŞİF DE KAPANDI (2026-09-04, kullanıcı onayıyla)

- **D-01 → FR-021 (T015).** e2e varsayılanı ölü konaktan dağıtılmış adrese. Ölçüldü:
  `api.dev.palbase.studio` → **000 (bağlanamadı)**, `api.palbase.studio` → **200**.
- **D-03 → FR-019 (T013).** `push` artık gerçekten `Idempotency-Key` taşıyor. T003 ölü BEYANI
  silmişti; bu YETENEĞİ getirdi. Kırmızı kanıtı: *"the upload … carries no Idempotency-Key —
  a timed-out retry becomes a second deploy"*.
- **D-04 → FR-020 (T014).** CI artık tam yığın kaldırabiliyor. Kök neden: `--init-env` konteyneri
  root olarak yazıyordu ve userns remap yapan daemon'da o root 0700 dizine yazamıyordu.
  `--user uid:gid` ile çözüldü — **dizin izni gevşetilmedi**, çünkü orada service-role anahtarı var.
  Opt-in bayrağı kaldırıldı; CI run 33867907256 `Run tests` **yeşil**.

**Açık kalem kalmadı.** D-02 (çekirdek sürüm sürüklenmesi) başka bir oturum tarafından çözüldü.

---

## Bağımsız inceleme — düzeltme dalgası (2026-09-04)

Taze bir gözden geçirici `a2fa8a9..HEAD`'i denetledi: 1 CRITICAL, 3 IMPORTANT, 6 MINOR, 3 NIT.

**C-1 — ZATEN KAPALIYDI, ama TEŞHİSİM YANLIŞTI.** Gözden geçirici `--user` düzeltmesini önerdi;
T014'te tam olarak o yapılmıştı (kapsamları dışındaydı). **Ama gerekçem yanlıştı:** D-04'te
"`palbase start`'ın kusuru değil, runner özelliği" yazmıştım. Doğrusu: palsvc imajı
`USER nonroot:nonroot` ile koşuyor (`v2/Dockerfile:101`) ve bind-mount UID'i eşlemeyen
**her Linux konağında** aynı şekilde düşerdi. macOS'ta geçmesinin sebebi Docker Desktop'ın
sahipliği eşlemesi. Yani kırmızı olan şey ortam değil **borçtu** — koşunun kendi tezi.

**Düzeltilenler (hepsi ölçümle):**
- **I-1** `flags list`: struct'a çözüm, `flags` anahtarı OLMAYAN her gövdeyi (`{"error":"unauthorized"}`,
  `null`) temiz çözüp *"this stack declares no flags"* bastırıyordu — kaldırdığımı iddia ettiğim
  sessiz yanlış cevap, başka kapıdan. `*[]…` işaretçisiyle "anahtar yok" ile "boş" ayrıldı; boş gövde
  ayrıca adlandırıldı. Regresyon testi eklendi.
- **I-2** `TestListReadsTheStack` kusur geri konunca GEÇİYORDU (`Contains(out,"a")` ham JSON'da da doğru).
  Commit mesajım bu zayıflığı adlandırıp düzeltmemişti. Artık tablo başlığını ve `boolean = true`
  satırını arıyor, ham şekli reddediyor. **Mutation: kusur geri konunca artık ÜÇ test birden düşüyor.**
- **I-3** Android: ortam-başına sözleşme Gradle girdisi olarak beyan edilmiyordu → `palbase push`
  sonrası task UP-TO-DATE sayılıp bayat istemci bırakıyordu. `openApiDir`/`openApiDirFiles` eklendi.
- **M-1** `withoutBarman` bir SONRAKİ servisin yorum başlığını da yiyordu; `# ── palsvc ──` orijinalde
  var, vendor'lanan kopyada yoktu ve parite kapısı göremiyordu (iki taraf da aynı fonksiyondan geçiyor).
  Geri yürüyüşün simetriği eklendi, dosya yeniden üretildi.
- **M-3** `release.yml` dört kapının hiçbirini koşmuyordu ve `ci.yml`'de `cancel-in-progress: true` var —
  CI koşusu iptal edilmiş bir commit tag'lenirse kapıları hiç görmeden yayına çıkardı. **Bu koşuda
  gerçekten bir run iptal oldu (33858199035).** Dört kapı release.yml'e de kondu.
- **M-6 (güvenlik)** Android loopback izni prefix kontrolüydü: `http://localhost.evil.com` kabul
  ediliyor ve publishable key'i düz HTTP ile uzak konağa taşıyan istemci üretiliyordu. Host parse
  edilip tam eşleştiriliyor. Kırmızı testi eklendi. → **Android v1.0.2 kesildi.**
- **M-5** FR-014'ün metni yükümlülüğü CLI'a veriyordu, D-4 kararı ise tüketiciye. Metin hizalandı,
  Impact Map'ten iki değişmemiş satır çıkarıldı (Changelog A-7).

**Kabul edilen, düzeltilmeyen:** M-2 (barman dosya sonundaysa son newline düşer — bugün ısırmıyor),
M-4 (Türkçe kapısı ASCII'ye indirgenmiş Türkçeyi görmüyor; bugün ihlal yok, kapı dürüst yeşil ama
adının vaat ettiğinden dar), N-2/N-3 (yorum nitleri; N-2 zaten düzeltilmişti).

---

## İkinci bağımsız inceleme — iki denetçi, aynı yere bastı (2026-09-04)

`final-reviewer` ve `fr-verifier` birbirinden bağımsız aynı kusuru buldu; `reviewer2` üçüncü bir açıdan
aynı sınıfa değdi. Bulguları kabul ediyorum: **koşunun kendi tezi kendi planında ihlal edilmiş.**

### D-05 · FR-011 KARŞILANMADI ve T010'un kutuları ÖLÇÜLMEDEN işaretlendi

Bu, bu koşunun avladığı kusurun ta kendisi: **ölçülmemiş bir şeyin yeşil işaretlenmesi.** Merkez
kanıtımız "kapı gerçek aracı çalıştırmak yerine taklit ettiği için düzeltmeyi düzeltme-olmayandan
ayırt edemedi" idi; T010'da ben aynı şeyi elle yaptım — kutuyu ölçüm yerine niyetle işaretledim.

- T010 Adım 3 (`testing.Short()` kapıları + `t.Parallel()`) `[x]` idi; `git diff a2fa8a9..HEAD --
  internal/backend/build_test.go` YALNIZCA CI-fatal hunk'ını içeriyor. Tek bir skip, tek bir
  `t.Parallel()` eklenmemiş. İki denetçi de bunu bağımsız ölçtü.
- T010 Adım 4 ("süre ≤ 180 sn") `[x]` idi; ölçülen süreler 226 / 257 / 115 / >600 sn.
- Adım 5'in GERÇEK commit mesajı (`6a20575`) planın vaat ettiği "`-short` gerçekten kısa" yarısını
  dürüstçe düşürmüştü — mesaj küçülmüş, kutular küçülmemişti. Tutarsızlık tam oradaydı.

**Yapılan:** iki kutu geri açıldı, iş gerçekten yapıldı, sonra TAZE ölçümle işaretlendi.

### O-1 · AÇIK KALEM (sunucu tarafı, bu deponun DIŞINDA) — push'un idempotency'sini kimse okumuyor

`reviewer2` ölçtü, ben doğruladım: `Idempotency-Key` kontrol düzleminde HİÇ okunmuyor.
`v2-cloud/platform/server` altında tek eşleşme yok; `v2/internal/sealed/replay.go:20` bunu zaten
yazıyor — *"`Idempotency-Key` appears nowhere in this repository outside this package."*

Sonuç: CLI'ın gönderdiği başlık bugün hiçbir şeyi değiştirmiyor. Ve **retry eklemek zararı
ARTIRIRDI**: anahtarı onurlandırmayan bir uca timeout'ta ikinci istek atmak, inen ama cevabı
kaybolan yüklemeyi ikinci kez uygulatır — FR'nin önlemek için var olduğu şeyin ta kendisi. Güvenlik
yolunda fail-open yapmama kuralı burada retry'ı YASAKLIYOR, emretmiyor.

Bu yüzden FR-019'un retry yarısı kaldırıldı (spec Changelog A-8) ve yerine anahtarın ARTEFAKTTAN
türemesi kondu — bugün de doğru, dedup geldiği gün asıl kurtarma yolunu kendiliğinden güvenli kılıyor.
**Sunucu dedup'ı bu koşunun deposunun dışında ve orada eşdüzey oturumlar aktif; kullanıcıya
teklif olarak sunuluyor, sessizce kapatılmıyor.**

### D-05'in kapanışı — ölçümle, ve planın bir adımı ÇÜRÜTÜLEREK

`requiresRealToolchain(t)` adında TEK bir kapı yazıldı (gerekçe bir yerde yaşıyor) ve gerçek
araç koşan **23 teste** takıldı — 20'si ≥10 sn ölçülenler, 3'ü `-short` altında hâlâ `npm`e
uzanırken yakalananlar (`TestBuildAcceptsAPlainModuleUnderConfig`,
`TestBuildAcceptsATsconfigWithNoIncludeList`, `TestSDKSkewSaysTheTypecheckNoLongerDescribesTheBuild`).

| Ölçüm | Önce | Sonra | Bütçe |
|---|---|---|---|
| `go test ./internal/backend/ -short` | **418,75 sn** | **107,41 sn** | — |
| `go test ./... -short` (FR-011/NFR-001) | 226-600 sn (denetçi ölçümleri) | **148,35 sn** | 180 sn ✓ |
| `-short` altında ağ (NFR-003) | 3 `npm` çağrısı | **0** ✓ | 0 |

Kapı testleri ZAYIFLATMIYOR: CI `go test ./... -race` koşuyor, `-short` YOK — kapılananların hepsi
her push'ta hâlâ koşuyor. `-short` bir suite'i hızlı soru ile kapsamlı soruya ayırır, kapsamlı
olanı silmez.

**Ama bu cümleyi ilk yazdığımda KANITLAMAMIŞTIM, ve kanıtlamaya kalkınca bir kusur çıktı.**
Ölçüm: `internal/backend` yerelde ~600 sn, CI'da **85,4 sn** (`gh run view 33871859526 --log`).
Yedi kat fark ya çok daha hızlı bir makinedir ya çok daha ince bir koşu — ve ikisi yeşil bir tikten
ayırt edilemez. `hostNodePkgDir` node/npm PATH'te yoksa `t.Skip` ediyordu, yani bir runner'da araç
eksik olsa gerçek-araç testlerinin TAMAMI koşudan sessizce düşer ve suite yine `ok` basardı — bu
koşunun avladığı "yeşil görünen ama ölçmeyen kapı" şeklinin ta kendisi. `requireToolOnCI` eklendi:
yerelde skip (node'u olmayan biri kalanı koşabilmeli), **CI'da `t.Fatalf`** — araç yokluğu orada
mazur görülen bir test değil, bozuk bir kapıdır. İddia artık kanıtlanabilir.

**Planın Adım 3'ünün ikinci yarısı ÇÜRÜTÜLDÜ, atlanmadı.** Adım "fixture-izole olanlara
`t.Parallel()` ekle" diyordu. Yapılamaz: `seedBackendDir` `t.Chdir(dir)` çağırıyor ve
`go doc testing.T.Chdir` bunu açıkça yasaklıyor — *"Because Chdir affects the whole process, it
cannot be used in parallel tests or tests with parallel ancestors."* Eklenseydi bu testler panikle
düşerdi. Bütçe zaten kapılarla tutuyor, yani `t.Parallel()` bir araçtı, hedef değil; hedef NFR-001
ve ölçülerek karşılandı. Adım metni bu gerçeği söyleyecek şekilde düzeltildi.

### O-2 · KAPANDI — `PostMultipart` silindi

FR-019 çalışırken görüldü: `backend.REST` arayüzü `PostMultipart`'ı beyan ediyor ama onu çağıran
üretim kodu yok (`grep` → yalnız arayüz beyanı). Bu koşunun ürettiği bir şey değil, önceden ölü.
Aynı geçişte `NewIdempotencyKey` de ölü kaldı — **o benim değişikliğimin borcuydu ve ödendi**
(üretici + testi silindi, ona yönlendiren iki yorum düzeltildi). `PostMultipart` kapsam dışı keşif:
teklif olarak sunuluyor.

### Kalan üç bulgu da kapandı (M-2 · M-4 · N-3)

**M-4 en değerlisiydi, çünkü kapının ADI vaadinden genişti.** `TestNoUserFacingStringIsTurkish`
yalnızca `çğıöşüÇĞİÖŞÜ` arıyordu — diakritiksiz yazılmış Türkçe ("dosya bulunamadi", "gecersiz
deger") kapıdan geçiyor, kapı da sessizlik rapor ediyordu. Bir kapının OKUDUĞU küme ile HAKKINDA
KONUŞTUĞU küme ayrı sorudur; bu koşunun tezi tam olarak buydu ve kapının kendisi ondan muaf değil.
ASCII kıvrımı kelime sınırlarıyla eklendi (`deger` "ledger"ın, `icin` "medicine"ın içinde geçtiği
için alt-dizge araması bu kapıyı İngilizce düzyazıda kırmızıya döndürürdü). **İki yönlü negatif
kontrol koştu:** `dosya bulunamadi` enjekte → `FAIL … (Turkish word, ASCII-folded: dosya)`;
`"the ledger says medicine, yokohama and degrade"` enjekte → `ok`. Kapı doğduğu gün yeşil.

**M-2** `withoutBarman`'ın geri yürüyüşü artık yalnız `end < len(lines)` iken koşuyor. Barman dosya
sonunda olsaydı, son newline'ın ürettiği `""` bir başlık üstü boş satırdan ayırt edilemiyor ve
yeniyordu. Bugün kimseyi ısırmıyor (barman iki servis arasında) — pinlemeye değmesinin sebebi tam
da bu: taşındığı gün arıza, kimsenin okumadığı tek baytlık bir diff olurdu.

**N-3** ASCII'ye indirgenmiş Türkçe yorumlar düzeltildi — `start.go`'da 16, `stackfiles_test.go`'da
9 satır. N-3 tek bir satır sanıyordu; sayınca 25 çıktı.

### Kendi usul hatam — ölçümü okumadan commit'ledim

`118a106`'yı atarken `-short` suite'ini commit ile AYNI bash bloğuna koydum; çıktı "2 FAIL" dedi ve
ben onu okumadan commit + push yaptım. Commit mesajı "-short 0 FAIL" diyor.

Kırmızı benim değildi — eşdüzey bir oturum `roles` komutunu ekleyip (`0ede729`) golden listesini bir
commit sonra düzeltti (`5b1bd69`), ölçümüm tam o pencereye denk geldi; benim commit'imin kendi
ağacında CI **success** (`33878577580`) ve suite şimdi 0 FAIL. Yani iddia sonuçta doğru çıktı.

**Ama doğru çıkması onu ölçülmüş yapmaz.** Bu koşunun tamamı "ölçmeden yeşil demek" hakkındaydı ve
ben aynı hatayı, kapanış commit'inde, kanıtı ekranda dururken yaptım. Ölçüm ve ona dayanan karar
aynı bloğa konmamalı: blok, çıktı okunmadan sonuna kadar koşar.

### O-3 · KAPANDI — `appendSealingChain`'in Close hata yolu artık ölçülüyor

`reviewer2` (I-1) haklı: FR-018'in "gerçek kusur" düzeltmesi mekanik olarak doğru (`return closeErr()`
mutlu yolun sonunda, `closed` defteri sağlam) ama **ölçülmüyor** — `start_test.go:693` yalnız mutlu
yolu sürüyor, ve Close'u yutan bir mutasyon o testi kırmızıya döndürmez.

`os.File.Close()`'un hata döndüğü durumu taşınabilir biçimde tetiklemenin bir yolunu bulamadım
(`os.File` tamponlamaz; ENOSPC'yi gecikmeli bildiren FS'ler ve ağ bağlı olanlar gerekir). Sahte bir
test — mutasyonda yakalamayan bir test — bu koşunun tam olarak avladığı şey olurdu, o yüzden
yazmadım ve **eksikliği kodun içine adıyla yazdım**. İnceleme sırasında yorumun gerekçesinin de
abartılı olduğu çıktı: "flush hatası yalnızca Close()'ta görünür" `bufio.Writer` için doğru, `os.File`
için değil. Yorum düzeltildi.


### O-2 ve O-3 kapandı (kullanıcı üçünü de aldı)

**O-2.** `PostMultipart` silindi — implementasyon, `backend.REST` üyesi, test stub'ı ve iki bayat
yorum atfı. Tarihi net: `a6dedc5`'te push multipart olmaktan çıkıp plane'e JSON artefakt vermeye
başladı; metot son çağıranını ÜÇ AY önce kaybetmiş, bu arada bir arayüzde durup her stub'ın boşuna
uygulamak zorunda kaldığı bir üye olmayı sürdürmüş.

**O-3.** Close hata yolunun artık kendi kırmızısı var. `openEnvForAppend` paket düzeyinde bir seam —
bu kod tabanının aynı sorunu zaten böyle çözdüğü yer var (`transport.DPoPSigner`,
`backend.CloudKeyFetcher`): üretim gerçek açıcıyı tutar, test Close'u başarısız olan bir
`io.WriteCloser` koyar. **Mutation kanıtı: `return closeErr()` → `_ = closeErr(); return nil`
yapınca `TestAppendSealingChainRefusesAFailedClose` DÜŞÜYOR; geri alınca yeşil.** Yorumun abartılı
gerekçesi de düzeltildi — `os.File` tamponlamaz, "flush hatası yalnızca Close()'ta görünür"
`bufio.Writer` için doğruydu.
