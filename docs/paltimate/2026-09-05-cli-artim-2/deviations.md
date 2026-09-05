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
