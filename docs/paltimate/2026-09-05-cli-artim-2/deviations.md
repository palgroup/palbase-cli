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
