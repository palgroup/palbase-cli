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
