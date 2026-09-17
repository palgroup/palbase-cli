# `palbase build` hook manifestini yazmıyor

**Parkur: hızlı.** Tek dosya (`internal/backend/stack_bundle.go`), migration yok, kalıcı şema
yok. Yayınlanmış sözleşme DEĞİŞMİYOR — tam tersi: zaten yayınlanmış bir sözleşmenin eksik
üreticisi yazılıyor.

## Kusur

Runtime hook'larını biliyor, sunucuya sıfır hook varıyor. palbase/v2'nin CI koşusu
35153323636'nın yığın günlüğü, iki satır arayla:

```
palsvc-1  | "msg":"documents: belge hook kaydı yazıldı" ... "events":[]
palsvc-1  | "msg":"auth: hook kayıtları yazıldı"        ... "count":0
runtime-1 | [runtime] hooks: document.created (listener), file.uploaded (listener),
            file.deleted (listener), before.user.create, after.session.revoke (listener)
```

Sözleşme ÜÇ yerde ilan edilmiş, üreticisi yok:

| halka | yer | durum |
|---|---|---|
| sunucu okur | `palbase/v2 hooksmanifest/manifest.go:185` → `.palbase/hooks/hooks.manifest.json` | var |
| artifact paketler (varsa) | `palbase/v2 internal/deploy/artifact.go:599` | var |
| arşivin yorumu | `internal/backend/archive.go:116` — *"`hooks.manifest.json` the only way"* | var |
| CLI kendi ürünü sayar | `internal/backend/generated_paths_test.go:530` | var |
| `bundleOutputDirs` yorumu | `internal/backend/stack_bundle.go:44` — *"the compiled controllers and **the two manifests**"* | var |
| **bundler YAZAR** | `stack_bundle.go` `writeDefinitionManifests` | **YOK** |

`writeDefinitionManifests`'in adı çoğul, yalnız `jobs.manifest.json` yazıyor (`:1021`).
`.palbase/hooks/` dizini tam olarak bu dosya için ayrılmış ve hep boş kalıyor.

Veri kaybolmuyor, **düzleştiriliyor**: sonda probu `getHookManifest(k)`'ten
`{blocking, listeners}` alıp `[...m.blocking, ...m.listeners]` diye tek bir ada listesine
eziyor. Sunucunun istediği ayrım (`blocking`) elde varken atılıyor.

### Sonucu

palbase/v2'nin kapısında beş düşüş: `15-14` (before.user.create kaydı yok),
`16-4` (belge hook kaydı yok) ve onun ardılları `16-6`, `16-8`, `16-11` (document.created,
file.uploaded, file.deleted dinleyicileri hiç ateşlenmiyor). Üründe: bir projenin `@Hook` /
`@On` beyanları deploy edildiğinde **hiçbir şey yapmıyor** — sessizce.

## Gereksinimler

**FR-001 — Manifest yazılır.** WHEN bundle en az bir hook beyan ettiğinde THEN `palbase build`
SHALL `.palbase/hooks/hooks.manifest.json` dosyasını sunucunun şemasında yazar:
`{"hooks":[{"event":…,"blocking":…,"file":…}]}`.

**FR-002 — Bloklama ayrımı korunur.** WHERE bir olay `@Hook` ile beyan edilmişse THE kayıt
`blocking:true`, `@On` ile beyan edilmişse `blocking:false` taşır. (Sunucu yanlış bildirimi
reddediyor: bloklayamayan bir olay `blocking` bildirilirse `ParseManifest` hata veriyor.)

**FR-003 — Bayat manifest YAŞAMAZ.** WHEN son hook da silindiğinde THEN build SHALL
`.palbase/hooks` dizinini kaldırır — `jobs` için zaten yapılan şey, ve aynı sebeple: bayat bir
manifest, kaldırılmış bir hook'u kayıtlı tutardı.

**FR-004 — Çift beyan build'de durur.** WHEN aynı olayı iki kayıt işlediğinde THEN build SHALL
adıyla durur. Sunucu bunu zaten reddediyor; orada reddetmek deploy'u yarıda bırakmak demek.

**FR-005 — Çıktı metni değişmez.** WHERE `bundled hook(s) → …` satırı basılıyorsa THE sıra ve
adlar aynen korunur (sınıf başına önce blocking, sonra listener).

## Etki haritası

| dosya | iş |
|---|---|
| `internal/backend/stack_bundle.go` | modify — prob yapılandırılmış kayıt üretir, `bundleSurfaces.Hooks` tipi kayıt listesi olur, `writeDefinitionManifests` hook manifestini yazar |
| `internal/backend/stack_bundle_test.go` | modify — FR-001..FR-004'ün testleri |

## İKİNCİ ÜRETİCİ — palbase deposunda

Ölçünce ikinci bir bundler çıktı ve CI'daki düşüşlerin sebebi ASIL oydu: `verify.sh` fixture'ı
bu CLI ile değil `runtime/scripts/bundle-controllers.sh` ile paketliyor (`verify.sh:780`).
O dosyanın kendi yorumu tuzağı adıyla söylüyor:

> The two bundlers change TOGETHER (stack_bundle.go); they drifted once over
> `buildModuleClients` and the drift lasted a release.

İkisi de aynı gün düzeltildi. palbase tarafındaki commit `6305f5e0`.

## Doğrulama

Ölçüldü 2026-09-17:

| komut | sonuç |
|---|---|
| `go test ./internal/backend/ -run 'TestHookManifest\|TestTwoHooksOnOneEvent\|TestAStaleHookManifest'` | **ÖNCE kırmızı** (`undefined: hookDef`), sonra PASS ×3 |
| `go build ./...` | exit 0 |
| `go test ./internal/backend/` | exit 0 (460 s, tüm paket) |
| prob, GERÇEK fixture bundle'ına karşı (`bun -e`) | beş kayıt üretti, `before.user.create` `blocking:true`, diğer dördü `false` — runtime'ın açılışta bastığı listeyle birebir |

Son satır önemli: prob bir **JS dizesi**, Go testleri onu koşturmaz. Sözleşmenin okuyucu tarafı
da ayrıca doğrulandı — palbase deposunda `hooksmanifest.ParseManifest` üretilen manifesti kabul
ediyor (`bundler_contract_test.go`, iki test: biri golden şemayı sabitler, diğeri bu makinedeki
üreticinin ŞU AN yazdığını ayrıştırır).
