# Ölü `config/` kapısı — karar günlüğü

Açılış: 2026-08-31. Konu: 23.0.0 öncesinden yükselen bir checkout'ta `config/*.ts`
ölü kalıyor ve hiçbir kapı bakmıyor.

## Kod Gerçeği Dosyası

### VERIFIED (okudum)

| Olgu | Kanıt |
|---|---|
| SDK 24.3.0 `defineEgress`/`defineTestUsers`/`testUser` export ETMİYOR | `node -e 'import("@palbase/backend")…'` → üçü de `undefined`; `dist/index.d.ts:820` export listesinde yok |
| 21.0.1 üçünü de export EDİYORDU | müşterinin `node_modules/@palbase/backend/dist/index.d.cts:1687` |
| Kaldırma köprüsüz ve KASITLI | `backend/CHANGELOG.md:575` — *"There is no deprecation bridge…"*; `src/index.ts:360-366` — *"the honest surface is the one with no dead names in it"* |
| `palbase build` `config/` taramıyor | `internal/backend/build.go:66` yalnız `controllers/`; `stack_bundle.go:583-585` → `jobs`/`webhooks`/`hooks` |
| `build` typecheck YAPMIYOR | `build.go:~180` — *"There is no config to evaluate"*; esbuild tipleri siler |
| `push` `config/` okumuyor | `stack_push.go:190-192` — `carrySecrets`/`carryTestUsers` kaldırıldı |
| `doctor` yalnız ORTAM yokluyor | `cmd/palbase/doctor.go:152-230` — docker/compose/creds/node/bun/login/link |
| ŞABLON tsconfig'i de `config/`'ü dışlıyor | `backend/template/tsconfig.json` include listesi — `config/` yok, yorum: *"`config/` is gone — 23.0.0"* |
| Ölü `config/` deploy tarball'ında GİDİYOR | `internal/backend/archive.go:59-63` `defaultIgnoreFiles` yalnız secret globları; `config/` dışlaması yok |
| Emekli beyan envanteri KAPALI ve zaten var | `cmd/palbase/surface_test.go:385-395`; göç tablosu `backend/CHANGELOG.md:596-603` |
| `test-user templates set` VAR | `internal/testuser/testuser.go:148-175` `templatesSetCmd` |
| …ama yardım metni yokluğunu İDDİA EDİYOR | `internal/testuser/testuser.go:52-54` — *"Templates are declared in config/test-users.ts… no verb in this CLI writes a template"* |
| `JobMeta`'da `name` HİÇ olmadı | 21.0.1 `index.d.cts:1500-1505` ve 24.3.0 `index.d.ts:722-727` — ikisi de `{env, environmentId}`; `v2/runtime/src/jobs.ts:19` aynısını kuruyor |

### ÜRETİM (ölçüldü, 2026-08-31)

Müşterinin ağacı scratchpad'e kopyalandı, `@palbase/backend` 24.3.0 kuruldu:

- `palbase build` → `build OK — 67 route(s) … plus 4 job(s), 1 webhook(s)`
- `npx tsc --noEmit` → **exit 0**
- ŞABLONUN include listesiyle `tsc` → `config/` hatası **0**, ama `jobs/` hatası **7**

## Kararlar

### D-001 — SDK düzleyinde deprecation REDDEDİLDİ
**Karar:** Kaldırılmış `define*`'lar için SDK'ya stub/`@deprecated` konmayacak.
**Gerekçe:** Ölçüldü — şablonun kendi include listesiyle bile `config/` hatası sıfır,
çünkü `config/` kasten hiçbir tsconfig'de yok. Bir deprecation stub'ı hiçbir kapının
DERLEMEDİĞİ bir dizinde otururdu: fikstür yolu yanlış olan kural bakmayı bırakır.
Ayrıca `src/index.ts:360-366`'daki "dead names yok" kararını geri alırdı.
**Alternatifler:** (a) stub — yukarıdaki ölçümle çürütüldü; (b) `exports` map'inde
`config/` alt yolu — dosyalar kökten import ediyor, alakasız.
**Kanıt:** bu oturumun tsc üretimi; `backend/CHANGELOG.md:575`.

### D-002 — Kapı `config/` VARLIĞINA değil, emekli DOSYA ADLARINA bakacak
**Karar:** `config/` dizininin varlığı bir kusur değildir.
**Gerekçe:** Bu müşterinin `config/pricing.ts`'i meşru — deponun kendi fiyat tablosu,
`webhooks/stripe.ts` import ediyor, palbase ile ilgisi yok. "config/ varsa reddet"
onu da vururdu.
**Kanıt:** `palai-cloud/backend/config/pricing.ts:1-15`.

### D-003 — Envanteri sabitlemek DRIFT riski taşımıyor
**Karar:** Emekli beyan listesi CLI'da sabit yazılabilir.
**Gerekçe:** `config/` yüzeyi tümüyle emekli; liste KAPALI, yeni üye alamaz.
Sürüm sürüm büyüyen bir liste olsaydı türetmek gerekirdi.
**Kanıt:** `cmd/palbase/surface_test.go:385-395` aynı envanteri zaten tutuyor.

### D-004 — Kapı REDDEDER (exit 1)
**Karar:** Emekli beyan dosyası bulununca `palbase build` exit 1.
**Gerekçe:** Kullanıcının kararı (2026-08-31). `build`'in mevcut sözleşmesiyle
tutarlı (exit 1 = user-code hatası) ve "advisory gate YOK" kuralıyla. Yükseltenin
CI'ı kırılır — ama yalan söyleyen bir yeşilin yerine dürüst bir kırmızı geçer.
**Alternatifler:** uyarı (log'da kaybolur); kaçış bayrağı (borcun ödenmemesinin yolu).

### D-005 — Kapı GÖÇ YAPMAZ, teşhis koyar
**Karar:** Ölü dosya parse edilmez; mesaj canlı değeri okuma ve yazma verb'ünü söyler.
**Gerekçe:** Kullanıcının kararı. Parse edip stack'e yazmak, stack'te YAŞAYAN ayarı
ezme riski taşır ve emekli her biçim için bir parser gerektirir.
**Alternatifler:** `palbase config migrate` (ezme riski); fark raporu (aynı parser
yükü, daha az fayda).

### D-006 — tsconfig kapsama kapısı AYRI bir kapı olarak kapsamda
**Karar:** `build`, bundler'ın derlediği ama checkout'un tsconfig `include`
listesinin kapsamadığı dizinleri reddeder.
**Gerekçe:** Kullanıcının kararı. Bu delik GERÇEK bir kırığı gizledi (aşağıda),
ve şablonun kendi yorumu kuralı zaten yazıyor: *"Every directory the deploy path
compiles has to be in here, or a file in it is type-checked by nothing until it
is already running."*
**Kapsam kısıtı:** yalnız `include` VARSA ve mevcut+derlenen bir dizini
atlıyorsa. `include` yoksa tsc zaten her şeyi alır — delik yok. Bu, `extends`
zincirini çözme zorunluluğunu da ortadan kaldırır.

### D-007 — Müşteri tarafı dört kalemin dördü de kapsamda
**Karar:** (a) `config/egress.ts` + `config/test-users.ts` sil, izolasyon testini
taşı, `pricing.ts` kalır; (b) `meta.name` kırığını düzelt; (c) `palbase-issues.md`'ye
PB-N kayıtları; (d) tsconfig include'unu şablonla hizala.
**Sıra kısıtı:** (d) 7 hatayı görünür kılar, (b) hepsini kapatır — (d) önce, (b) sonra.

## palbase-issues.md açık bulgularının ölçümü (2026-08-31)

| Bulgu | Durum | Kanıt |
|---|---|---|
| PB-12 başlık: "şablon yazacak hiçbir yol kalmadı" | **ÇÜRÜTÜLDÜ** | `templatesSetCmd` — `internal/testuser/testuser.go:148`; `palbase test-user templates set --file <path>` |
| PB-12 korku: "sonraki push şablonları temizler" | **YERSİZ** | `shipDeclaredTestUsers` YOK; `internal/backend/deploy.go` 744. satırda yarım yorumla bitiyor, fonksiyon silinmiş, çağıranı yok |
| PB-12: CLI yardım metni yalanı | **DOĞRULANDI** | `internal/testuser/testuser.go:52-54` — silinmiş dosyayı adlandırıyor + aynı dosyadaki verb'ün yokluğunu iddia ediyor |
| PB-12: SDK test tipi yalanı | **DOĞRULANDI** | `dist/test/index.d.ts:30` ve `:112-113` — `config/test-users.ts`'i kaynak diye gösteriyor |
| PB-12 yan: yayınlanmış README yanlış API | **DOĞRULANDI** | npm'den çekilen `@palbase/backend@24.3.0` README'si 24 satır, `defineEndpoint` + `Database.insert` gösteriyor; `grep -c defineEndpoint dist/index.d.ts` → **0**. Aynı pakette `docs/llms.txt` doğrusunu yazıyor |
| PB-13 | **ŞU AN ANLAŞIYORLAR** — ama sessiz düşme gerçek | `palbase deploys` → `▸ c881cc02d661 is serving 80 endpoint(s)`, push → `live: 80 endpoint(s), c881cc02d661`. Kusur: `deployments.go:92-99` o satırı `err==nil && 200 && EndpointCount!=nil` koşuluna bağlıyor ve koşul tutmazsa **hiçbir şey basmıyor** — listenin okunmak için var olduğu tek satır sessizce kaybolabilir |
| PB-6 | **KAPANDI** | `template/AGENTS.md:84-86` artık doğru kuralı yazıyor: *"Class and method names are your public API. `NotesController.list` generates `pb.notes.list()`"* |
| PB-10 | **AÇIK, analiz doğru** | `v2-cloud/tenant-stack/envoy/routes.yaml:59` — *"An explicit list, and NEVER a wildcard"*: iki localhost bir varsayılan değil, KASITLI politika. CLI'da köken yazan verb yok (`auth settings` yalnız `site_url`) |

### D-008 — Emekli `config/` adını taşıyan DÖRT yayınlanmış metin var
`internal/testuser/testuser.go:52-54` · `internal/backend/deploy.go:735-744` (öksüz yorum) ·
`@palbase/backend/dist/test/index.d.ts:30,112-113` · yayınlanmış README (dolaylı: yok olan API).
Kapı bunları da tutmalı: yalnız dosyayı değil, dosyanın ADINI taşıyan yayınlanmış string'i de.

## Sonuç (2026-08-31)

**CLI — commit `b0714e4`, `go test ./...` 0 FAIL.**

| Ne | Nerede |
|---|---|
| `deadDeclarations` + `reportDeadDeclarations` | `internal/backend/checkout_shape.go` |
| `includeBlindSpots` + `reportIncludeBlindSpots` | aynı dosya |
| İkisi de `controllers/` erken dönüşünden ÖNCE, kısa devre YOK | `internal/backend/build.go` |
| `TestNoShippedStringNamesARetiredConfigFile` (eşli muafiyetle) | `cmd/palbase/surface_test.go` |
| `test-user --help` yalanı | `internal/testuser/testuser.go` |
| `flags --help` yalanı + kendi alt komutuyla çelişkisi | `internal/flags/flags.go` |
| `deploy.go`'nun öksüz yorumu + ölü `testUserTemplatesPath` | silindi |

**Aynı ağaç, aynı koşullar (negatif kontrol):** eski binary `build OK — 67 route(s)`
· yeni binary exit 1, iki beyanı kapılarıyla + iki kör noktayı eklenecek satırlarla.
`config/pricing.ts` işaretlenmedi.

### D-009 — Kapının kuralı OLUMSUZLAMADAN POZİTİFE çevrildi
İlk taslak emekli dosya ADINI yasaklıyordu: dokuz string yakaladı, **altısı haklıydı**
(*"It used to be config/egress.ts, applied on every push…"* — okuyucunun ihtiyacı olan
anlatım). Yalanı konusuyla avlayan kapı, aynı konu hakkındaki gerçeği de vurur.
Kural: **adı anıyorsan emekliliğini de YAZ.** Ayırdığı iki gerçek yalan:
`test-user --help` (var olan `templates set` fiilinin yokluğunu iddia ediyordu) ve
`flags --help` (*"config/flags.ts is git-authoritative"* — ve kendi `remove` alt
komutu *"It is gone: there is no file left to fall back to"* diyor).

### D-010 — `TestBuildIgnoresAConfigDirectoryEntirely` tersine çevrildi
Eski hâli *"a leftover config/ does not fail the build — people have these
directories on disk right now"* diyordu: kusuru adlandırıp özellik sanmış, ve zarar
gören tam da o insanlar. Testin hayatta kalan yarısı (build config/'ü
DEĞERLENDİRMİYOR) emekli OLMAYAN bir dosya adıyla korundu — emekli bir ad
kullanılsaydı red erkenden döner ve iddialar boşa düşerdi.

## AÇIK — kapsam dışı keşif
`palai-cloud/backend/db/` tek dosya taşıyor ve adı `schema.ts`. Yeni CLI onu
reddediyor (eş oturumun `db/` cutover'ı). Bugün canlı deploy'u etkilemiyor
(yayındaki CLI 0.49.1'de bu kapı yok), ama **bir sonraki CLI sürümüyle o proje
build edilemez hâle gelir.** Göç: `db/public.ts`. Kullanıcıya sunuldu.
