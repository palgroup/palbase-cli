# Artım 2 — Sapma Defteri

Bu koşuda planın dışına çıkan, planı değiştiren ya da kapsam dışı bırakılan her şey burada.

---

## Koşu başlangıcı — pre-flight bir REGRESYON riski yakaladı

**A-3 · Sürüm→imaj tablosu kapsama alındı (T014).** Execute'un pre-flight taraması, `@palbase/backend`
v33.0.0'ın bir sürüm→imaj tablosu TAŞIMADIĞINI ölçtü. FR-002'nin yalnız CLI yarısını yazmak
`palbase start`'ı **kırardı** — tablo yok, imaj yok, yığın kalkmaz. Bu tam olarak pre-flight
sweep'in var olma sebebi: mid-run keşfedilseydi, ya bir workaround (gömülü listeye fallback) ya da
yarım kalmış bir kol üretirdi.

Kullanıcıya soruldu (kapsam kenarı: ayrı depo + yayın kararı, D-048 gereği ad-hoc yayın yasak).
**Karar: tablo SDK paketine eklenir ve sürüm kesilir** — D-023'ün tasarladığı şekil. Şartname
Impact Map'i üç satır büyüdü, plan T014'ü kazandı, `PLAN VALID`.

---

## T004 — FR-007'nin yarısı ÇÜRÜTÜLDÜ, yarısı doğrulandı

**Çürütülen: "`stop` `local.json`'ı silmeden ÖNCE başarılı olsun."** Tasarım §3 bunu bir kusur
sayıyordu; kod bilinçli olarak TERSİNİ yapıyor ve gerekçesi yerinde yazılı (`start.go`, `runStop`):

> The files come off FIRST. A stop that failed halfway used to leave local.json behind pointing at a
> dead address, which every later verb then tried to reach — and the error it gave was a connection
> refusal rather than "there is no local stack".

İki arıza modu var ve biri diğerinden iyi: dosya önce silinirse yarım kalan bir `stop` "yerel yığın
yok" bırakır (sonraki `start` toparlar); compose önce inerse ölü bir adrese işaret eden `local.json`
bırakır ve sonraki her fiil ona gider. Mevcut sıra **ölçülmüş bir karar**, düzeltilecek bir kusur
değil. FR-007'nin bu yarısı kaldırıldı.

**Doğrulanan: "`stop` compose'u ezmesin."** Gerçek. `runStop` → `stackDirectory(group)` →
`writeVendoredStack(group)` → `os.WriteFile(path, stackCompose)` (`stackfiles.go:41`). Yani `stop`,
indireceği yığını **yeniden yazıyor**. CLI güncellendiyse ve compose değiştiyse, `stop` YENİ tanımla
ESKİ yığını indirmeye çalışır; servis adları değişmişse konteynerler kalır.

## T004 — FR-004'ün mekanizması BULUNDU

Pre-flight'ta "runtime'ın hazırlık ucu yok" diye ölçmüştüm; yanlıştı, `grep` `node_modules`'a
düşmüştü. Gerçek: `v2/runtime/src/server.ts:12` —

> `4006  probes — /healthz (alive) and /readyz (loaded and answering)`

Runtime'ın kendi `/readyz`'i **var** ve tam olarak FR-004'ün sorduğu şeyi söylüyor. Eksik olan
compose tarafı: runtime servisinin healthcheck'i yok, yani `start` onu bekleyemiyor.

---

## UAT üç kusur yakaladı — üçü de birim testlerin göremediği türden

Her yarı kendi testini geçiyordu; kırılan şey **birleşimdi**.

1. **`palbase start` taze bir `palbase init`'i reddediyordu.** T002'de eklediğim `stackVersion`
   çağrısı `ReadTarget()`'ı ölümcül sayıyordu; oysa linksiz checkout `start` için NORMAL durum —
   `start` yığını kaldırıp *kendisi* linkliyor. Kullanıcı "this checkout is not linked" görüyordu:
   az önce koştuğu komuta tavsiye. Artık türetiyor ve dosya yoksa yazmıyor (yarım bir target, henüz
   var olmayan bir adresi adlandırırdı).
2. **`--platform bogus` bağlantı hatası veriyordu**, ret değil. Yazım hatası "yığın kapalı" gibi
   okunuyordu. Doğrulama ağdan ÖNCE'ye alındı.
3. **Platform algılaması da ağdan sonraydı.** Dizin okumak kimsenin iznini gerektirmiyor.
   `▸ ios, web` artık bağlantıdan önce basılıyor.

**Ders:** UAT'yi "kutuyu işaretlemek" sanmak, koşunun en ucuz teşhis aracını harcamaktır. Üç kusur
da üretim yolundan ilk geçişte çıktı.

## Tasarımın DÖRT bayat iddiası (hepsi ölçümle çürütüldü)

| # | Tasarım | Ölçüm |
|---|---|---|
| CB-46 | "yedi pin, dördü kontrolsüz" | Dört pin, **biri** kontrolsüz (`pgvector`) |
| FR-007 | "`stop` dosyaları silmeden önce başarılı olsun" | Mevcut sıra **ölçülmüş bir karar**; yalnız compose'u yeniden yazması gerçekti |
| FR-012 | "`gatherEnvironments` ↔ `addLocalStack` %95 birebir" | 78 vs 35 satır, **farklı işler**; gerçek örtüşme öbür çiftte |
| FR-014 | "`internal/apps` (617) ve `internal/hook` (494) silinsin" | İkisi de kısmen **canlı**; kesim satır sayısını değil çağıranları izledi |

Ayrıca pre-flight'ta bir ölçümüm yanlıştı ("runtime'ın hazırlık ucu yok" — `grep` `node_modules`'a
düşmüştü) ve bir kapı yazarken kendi kusurumu buldum (rota kapısının regex'i seçenekli
dekoratörleri yutuyordu, `/v1/cloud/me` ve `/config`'i "servis edilmiyor" sanıyordu).

---

## Bağımsız inceleme — bir VERİ KAYBI kusuru buldu

Üç gözden geçirici rapor verdi. En ağırı:

### C-1 · `stackVersion` proje bağını EZİYORDU (benim T001 kodum)

`ReadTarget()` `.palbase/local.json`'ı **tercih eder**; `WriteTarget()` `.palbase/project.json`'a
**yazar**. Birinden okuyup öbürüne yazmak, bir meslektaşın commit'li projesini localhost adresiyle
değiştiriyordu. Kendi gözümle yeniden ürettim:

```
ÖNCE:  {"project":"myproj","env":"prod"}
SONRA: {"url":"http://127.0.0.1:54321","stackVersion":"33"}
```

**Nadir bir kenar değil:** `WriteLocalTarget` yalnız `Target{URL}` yazdığı için yerel hedefte
`StackVersion` hiçbir zaman dolu olmaz — koşan bir yığında atılan **her** `palbase start` yeniden
türetir ve yeniden yazar.

Ve `target.go`'nun kendi yorumu bunu on iki satır yukarıda uyarıyordu: *"Writing one through the
other is how a `palbase start` ends up committing a localhost address into a colleague's checkout."*
**Uyarı oradaydı; ben okumadan üstünden geçtim.** Düzeltme tek satır: `readLinkedProject()`.

### C-2 (rev-b) · `upgrade`'in wiring'i ölçülmüyordu — DOĞRULANDI
Çağrı yerini silip **tüm depoyu** koştum: **0 FAIL**. Tek test yardımcıyı doğrudan çağırıyordu; hiçbir
şey komutu kurmuyordu. Artık `newUpgradeCmd`'i kuran bir test var ve mutation kırmızı veriyor.

### rev-b'nin diğer iki bulgusu — ölçünce KAPANMIŞ çıktı
- **C-2 (algılama wiring'i):** `b158580`'de ölçülmüş; UAT sırasında algılamayı ağdan öne alınca
  kapsama girmiş. Bugün geri alınca **3 test** düşüyor.
- **I-2 (yarım-link):** algılama artık satır 225'te, ilk yazım 288'de — yazımdan **önce**.

### I-3 · Ret, okuyucunun içinde olmadığı duruma tavsiye veriyordu
Bulut projesine bağlı bir checkout'ta dev yığın koşarken `ReadTarget` yereli tercih ediyor ve ret
"stackVersion ayarla" diyordu. Artık koşan yığını ve projeyi ADIYLA anıyor, `palbase stop`'u
öneriyor — T005'in kapattığı sınıfın bir kaynak-türü ötesi.

**Not:** `w1-a` SDK deposunda benim olmayan bir kusuru doğru teşhis etti (`docs.test.ts`,
commit'lenmemiş `services.md`); sınırının dışında olduğu için dokunmadı. Bugün ölçtüm: **10/10 geçiyor**,
başka bir oturum kapatmış.

### I-2 · Muafiyet BEYANA değil TAHMİNE dayanıyordu

T003'te "muafiyet kör değil, kendi ölçüsünü taşıyor" demiştim. Doğruydu ama eksikti: ayrım imaj
girdisinin BEYAN ettiği bir şeye değil, **ref'in önekine** bakıyordu. Gözden geçirenin iki mutasyonunu
kendim koştum ve ikisi de yeşil geçti:

```
edge   → ghcr.io/palgroup/palbase-edge:0.1.0    → ok   (önek ıskalandı)
palsvc → docker.io/palgroup/palsvc:0.1.0        → ok   (önek ıskalandı)
```

Yani bizim bir imajımız başka bir yola yayımlanınca sessizce "upstream" sayılıp çekirdek-eşitlik
kontrolünden **tamamen çıkıyordu** — ve o kontrolün tek işi bayat bir çekirdek pinini yakalamak.

`stackImage` artık açık bir `upstream bool` taşıyor: **girdi ne olduğunu SÖYLER, ref'inden tahmin
edilmez.** Üç mutasyon da artık kırmızı: iki taşınmış-bizim-imaj + upstream'in `:latest`'i.

**Ders:** bir kuralın öznesi tahmin edilemez. "Bizim imajımız" bir önek deseni değil, bir olgudur ve
veriye yazılmalıdır.

---

## Kapanıştan sonra: KOŞUNUN KENDİ ARTIKLARI

Docs korpusunu hizalarken (rev-b I-6) bir iplik çekildi ve koşunun kendi
sökümlerinin yarım kaldığı çıktı. Kullanıcı direktifi zaten yazılıydı:
*"legacy ile karşılaşırsan sil, kendin legacy de yaratma."*

### A-4 · T008 ARDINDA 3.969 SATIR ÖLÜ KOD BIRAKMIŞ

T008 dört platform komut grubunun **kaydını** sildi, **gövdesini** bıraktı.
`web_link.go`'nun 32 sembolünün hiçbirinin üretim çağıranı yoktu — 1.208
satırlık bir ada. `golangci-lint`'in `unused` linter'ı onu göremiyordu, çünkü
**1.477 satırlık `web_link_test.go` hepsini canlı tutuyordu.**

> **Ders:** *test edilen ölü kod ölü görünmez.* `unused` reachability'yi test
> dosyaları dahil hesaplıyor, yani bir sökümün artığını tam da onu ölçmesi
> beklenen alet gizliyor. Üretim çağıranını AYRICA sormak gerekiyor.

Silinen: `web_link.go` (1208) · `web_link_test.go` (1477) ·
`platform_link_target(_test).go` (260) · `native_link.go`'nun komut yarısı ve
testleri. Hayatta kalan iki sembol tek çağıranlarının yanına taşındı.

**Kesilmeyen:** `linkNativeEnvironments` ölü sanılabilirdi — ikinci çağıranı
`pull_spec.go:162`, yani `palbase pull`. Ölçmeden silseydim bulut ortam yolunu
kırardım.

### A-5 · Sökümün GÖTÜRDÜĞÜ KULLANICI YÜZEYİ — geri getirildi

`palbase web link` sekiz adımlık kurulum yapıyordu (SDK kurulumu · ilk
`palbe-gen` · predev/prebuild script'leri · entry import'u · Next providers.tsx
ve proxy.ts · gitignore koruması). Şartname FR-008'i "artefaktları yazsın" ile
sınırladığı için bu kayıp **hiç adlandırılmamıştı**: web projesi "başarıyla"
linkleniyor, üretilmiş istemcisi olmuyor, Next uygulamasında tarayıcı paketi
`pb`'yi hiç yapılandırmıyordu.

Kullanıcıya soruldu (ürün kararı). **Karar: geri gelsin** — "tek yol oldu bu
link işi; ios android nasılsa web'te öyle olacak platform tag'i ile."
`runLink`'in web dalına bağlandı, `--entry`/`--out` bayrakları `link`e taşındı.
Kanıt üretim yolundan: `TestLinkWiresAWebCheckoutEndToEnd`, mutasyon kırmızı.

### A-6 · pre-push hook altsistemi — SİLİNDİ

`hook.Ensure`'ün üretim çağıranı yoktu (hook yıllardır kurulmuyor), ama
`palbase doctor` bunu KIRMIZI raporluyor ve çaresi olarak `palbase push` ya da
`clone` öneriyordu — ikisi de kurmuyor. Her `doctor` koşusu, kullanıcının
düzeltemeyeceği kalıcı bir yanlış alarmdı.

Kullanıcı kararı: *"git hook ile hiçbir şey kalmaması lazım."* 494 satır gitti.
Onunla birlikte tavsiye kapısının `palbase:frozen-fingerprint` istisnası da —
bir kapının istisnası kendi silinmesini istemeli.

### A-7 · İKİ IGNORE LİSTESİ BİRBİRİNDEN HABERSİZDİ

`palbase build` → `.palbase-build-controllers/` yazıyor.
Scaffold → `.palbase-staged-controllers/` ve `.palbase-serve-controllers/`
ignore ediyordu. Yani **build'in gerçekten yarattığı dizin, ignore edilmeyen
tek dizindi** (17 dosya / 88 KB, her biri bir controller'ın bayat kopyası); ve
ikinci ad iki yeniden adlandırmadır hiçbir şeyin yazmadığı ölü bir isimdi.

Aynı hikâye bir dizin içeride: `.palbase/` bilerek commit'leniyor ama
`.palbase/esm|jobs|hooks` her build'in yeniden yazdığı ÇIKTI ve hiçbiri ignore
edilmiyordu.

Artık tek beyan (`generatedProjectPaths`), iki okuyucu (scaffold + onarım).
Kapı iddiasını hatırlanan adlardan değil KODUN SABİTLERİNDEN kuruyor — eski
test tam da hatırladığı iki adı doğruluyor, canlı olanı ıskalıyordu.

### A-8 · BUNDLE ÇIKTISI KOMUTU YAŞIYORDU

`.palbase/esm|jobs|hooks` iki komut arasında yaşamak zorunda değil: `push`
bundle'ı üretip tar'ı aynı süreçte alıyor, `plan` üretip hiçbir şey
göndermiyor. Geride kalanlar, kullanıcının commit'lemesi söylenen dizinin
içinde duran build çıktısıydı — ve push yolunun kendi yorumuna göre diskte
duran bayat bir bundle "yesterday's code under today's commit message" demek.
Üç çağrı yerinde de temizleniyor artık.

### A-9 · `clone <proj_id>` ADRESLENEMEZ CHECKOUT ÜRETİYORDU

İd yolu bağlamayı yalnız `.palbase/selection.json`'a yazıyordu; FR-013 ile
`ReadTarget` o dosyayı okumayı bıraktı. Klon iniyor, sonra o dizindeki HER
fiil "this checkout is not linked" diyordu. Üstelik o dala girmek için
`proj_…` yazmak gerekiyor — komutun kendi yorumunun dediği gibi "a value this
CLI shows on no surface at all". Dal redde çevrildi, kapanımı silindi;
`selection.json`'ın üretimdeki son yazıcısı da böylece gitti.

### A-10 · ALETİN TAVANI PLATFORMUN SANILDI

`golangci-lint` panic verdi. Sebep kodda değildi: Homebrew Go'yu **1.27.0**'a
yükseltmiş, linter **go1.26.2** ile derlenmişti ve 1.27'nin stdlib'ini
ayrıştıramıyordu. CI 1.26.6'ya pinli olduğu için orada sağlamdı. CI ile AYNI
sürüm (v2.12.2) yerel Go ile yeniden derlendi — ve ilk temiz koşusunda
`refuseUseOnBoundProject`'i buldu.

### A-11 · ÇEKİRDEK KAYMASI: 0.42.0 → 0.42.1

T003'te yazdığım parite kapısı kırmızı verdi: otorite (`version.env`,
`V2_VERSION`) 0.42.1'e çıkmıştı, CLI'ın gömülü varsayılanları, vendor'lanan
compose ve SDK tablosu 0.42.0'da kalmıştı — `palbase start` buluttan ESKİ bir
çekirdek koşuyordu. Pinlemeden önce üç imajın da GHCR'de yayında olduğu
manifest ile doğrulandı. Kapının var olma sebebi tam olarak buydu.

### A-12 · DOCS KORPUSU + İKİNCİ KAPI

On beş sayfada 66 satır emekli komut öğretiyordu; koşunun kendi değişiklikleri
de altı bayat iddia bırakmıştı. Hepsi hizalandı ve
`retired-commands.test.ts` kuruldu: korpusun öğrettiği her `palbase <word>`,
CLI kaynağındaki cobra `Use:` alanlarından TÜRETİLEN kümeye karşı ölçülür.

> **Ders (bu koşunun altıncı tekrarı):** *bir kuralın öznesi tahmin edilemez.*
> Kapı önce SATIR bazlı yazıldı ve 14 ihlal saydı — on dördü de komutun var
> olmadığını SÖYLEYEN cümlelerdi, çünkü düzyazı sarılıyor ve olumsuzlama bir
> önceki satırda kalıyor. BLOK bazlı ölçüm sıfıra indirdi ve tek gerçek kusuru
> bıraktı: `backend/channels.md`'nin "check `palbase version` first" tavsiyesi
> — binary `unknown command "version"` diyor.

Aynı sınıfın CLI tarafı için `cmd/palbase/advice_test.go`: shipped string'lerin
verdiği tavsiyeler aynı türetilmiş kümeye karşı ölçülür, AST ile (yorumlar
görünmez). İlk koşusunda `palbase pull`'un "run `palbase web link` … first"
reddini buldu — çaresi yazılamayan bir ret.

### A-13 · ÖZ-DENETİM: KURALLARDAN BİRİNİ BEN İHLAL ETTİM

GOAL'ün üç davranış kuralı (*workaround yapma · legacy ile karşılaşırsan sil ·
kendin legacy de yaratma*) sonuç raporunda değil, **tek tek** denetlenmeliydi.
Denetim yapılınca biri ihlal edilmiş çıktı — ve ihlal eden bendim.

**İhlal:** `--platform web`, package.json'sız bir dizinde. Teşhisim doğruydu
(ret artefaktlar yazıldıktan SONRA geliyordu → yarım link, hata olarak
raporlanmış), ama seçtiğim çare **iki testi değiştirmeden geçiren** çareydi:
reddi NOT'a çevirmek. Aynı kusurun ikinci ve dürüst çaresi vardı — *hiçbir şey
yazılmadan önce reddet* — ve onu seçmemiştim.

> **Ders:** *bir düzeltmeyi, kırmızı testleri yeşile çeviren yönde seçmek
> workaround'un şeklidir.* Testler düştükten SONRA üretim davranışını
> değiştiriyorsan, önce şunu sor: bu kusurun testlere dokunmayan başka bir
> çaresi var mı? Varsa, seçtiğin çareyi testler seçti.

Geri alındı: `refuseUnsupportedPlatforms` artık `validatePlatforms`ın yanında,
ağdan ve ilk bayttan önce. Dört fikstür güçlendirildi (gevşetilmedi) — hepsi
web linkini web olmayan bir dizinde koşuyordu ve yalnız ön koşul GEÇ kontrol
edildiği için geçiyorlardı. `TestAnUnsupportedPlatformIsRefusedBeforeAnythingIsWritten`
hem reddi hem DİSKTE HİÇBİR ŞEY kalmadığını ölçüyor; mutasyon kırmızı.

**Diğer iki kural, ölçümle:**
- *kendin legacy yaratma* → bu oturumda eklenen sembollerin **hepsinin** üretim
  çağıranı var (test yardımcıları dahil; `unused` onları da sayıyor, 0 issue).
  `hasApple` shim'lenmedi, SİLİNDİ — build `undefined: hasApple` verene kadar.
- *legacy ile karşılaşırsan sil* → silinenler A-4…A-9'da. Silinmeyen bir tane
  adlandırıldı: `internal/selection` ölü ada DEĞİL (8 dosyada 11 üretim
  tüketicisi); emekli olan adresleme yarısıydı. `selection.json` dosyasının ise
  artık yazanı yok, tek okuyanı var — eski CLI'ların bıraktığı checkout'lar için
  bir GÖÇ KOLAYLIĞI olarak kodda adlandırıldı, canlı mekanizma olarak değil.

**Ve bir süreç hatası:** bu düzeltmeyi `-short` yeşil diye push ettim; CI
düştü. `-short` tam takım değil — dördüncü bir fikstür (`requiresRealToolchain`
altında, yerelde atlanan) aynı ön koşula takılıyordu. Kural zaten yazılıydı:
YERELDE KOŞ, CI teyittir. Bu kez CI'ın koştuğu komutun aynısı yerelde koşuldu.
