# Artım 2 — Kanıt

Artım 1'in `research.md`'si (CB-01…CB-21) ve `../2026-09-02-cli-tam-onarim/decisions.md`'nin
araştırma kalemleri (rs-supabase, rs-envcheckout, rs-pin-priorart) bu artımın girdisidir; burada
YALNIZCA bu artımın FR'lerinin dayandığı iddialar duruyor, hepsi **2026-09-05'te taze ölçüldü**.

---

## A. Kod iddiaları — hepsi bu artımda yeniden temellendirildi

| # | İddia | Kanıt | Verdict |
|---|---|---|---|
| CB-30 | Ölü rota `GET /api/v2/projects` üretim kodundan HÂLÂ çağrılıyor | `internal/selection/resolve.go:192` → `rest.Do(ctx, http.MethodGet, "/api/v2/projects", nil, &projects)` | **confirmed** |
| CB-31 | `selection` paketi 1311 satır | `find internal/selection -name '*.go' \| xargs wc -l` → `1311 total` | **confirmed** |
| CB-32 | `internal/apps` 617 satır, tek dosya | `find internal/apps -name '*.go' \| xargs wc -l` → `617` | **confirmed** |
| CB-33 | `internal/hook` 494 satır, iki dosya | aynı ölçüm → `494` | **confirmed** |
| CB-34 | `tests/e2e` 163 satır, tek dosya | aynı ölçüm → `163`; dosya `mgmt_api_test.go` | **confirmed** |
| CB-35 | github `repository_provider` kolunun 12 üretim referansı var | `grep -rn "ProviderGitHub\|repository_provider" --include="*.go" internal/ \| grep -v _test \| wc -l` → `12` | **confirmed** |
| CB-36 | İki ayrı `link` komutu var | `web_link.go:231` `Use: "link"` · `native_link.go:112` `Use: "link"` | **confirmed** |
| CB-37 | Dört platform komut grubu var | `native_link.go:76` `"ios"` · `:87` `"macos"` · `android_link.go:9` `"android"` · `web_link.go:218` `"web"` | **confirmed** |
| CB-38 | `<platform> use` komutu var | `internal/backend/ios_use.go:36` → `Use: "use <environment>"` | **confirmed** |
| CB-39 | Örtüşen çiftler mevcut | `app_environments.go:612 gatherEnvironments` ↔ `cloud_environments.go:167 addLocalStack`; `native_link.go:381 resolveNativeApp` ↔ `web_link.go:148 resolveWebApp` | **confirmed** |
| CB-40 | Platform algılama malzemesi hazır | `planes.go:132 hasApple` · `planes.go:153 hasWeb` · `native_link.go:429 detectAndroidApplicationID` | **confirmed** |
| CB-41 | `--platform` varsayılanı `["ios"]`, bilinmeyen değer reddedilmiyor | `project_link.go:127` → `f.StringSliceVar(&o.platforms, "platform", []string{"ios"}, "ios, macos, android or web")`; doğrulama yok | **confirmed** |
| CB-42 | `refFromTargetURL` loopback adreste `""` döner | `upgrade.go:52-62`: `len(labels) < 3 \|\| !refPattern.MatchString(labels[0])` → `""`; `localhost` tek label, `127.0.0.1`'in ilk label'ı desene uymaz | **confirmed** |
| CB-43 | `/readyz` palsvc kümesine yönleniyor (runtime'ı kanıtlamıyor) | `start.go:584` yorumu: *"/readyz routes to the palsvc cluster"*; istek `start.go:591` | **confirmed** |
| CB-44 | `.palbase/project.json` commit'lenir, `local.json` gitignore'lanır | `target.go:7-21` (iki farklı ömür) · `init.go:284,298` (`.gitignore` yalnız `local.json`) | **confirmed** |
| CB-45 | `Target` struct'ında sürüm alanı YOK | `target.go:34-49` — `URL`, `Project`, `Env`, `Insecure`, `Local` | **confirmed** |

### ⚠️ CB-46 — tasarımın "yedi pin" iddiası **STALE**, gerçek DÖRT

Tasarım §3: *"`pgvector` dâhil **yedi pin** tek mekanizmaya bağlanır (bugün dördü hiçbir kontrolde
yok)"*. Ölçüm (2026-09-05):

```
$ grep -cE "^\s+image:" internal/backend/stackfiles/docker-compose.dev.yml   → 4
  :138  envoy     image: ${PALBASE_EDGE_IMAGE:-ghcr.io/palgroup/palbase/edge:0.42.0}
  :162  postgres  image: pgvector/pgvector:pg16          ← SABİT, değişken YOK
  :192  palsvc    image: ${PALBASE_PALSVC_IMAGE:-…}
  :294  runtime   image: ${PALBASE_RUNTIME_IMAGE:-…}

$ docker compose -f … config --services   → envoy palsvc postgres runtime   (4 servis)
$ grep -c 'PALBASE_.*_IMAGE"' internal/backend/start.go   → 3   (stackImages, start.go:65)
$ grep -rn pgvector --include="*.go" internal/ | grep -v _test   → (boş)
```

**Verdict: stale → düzeltildi.** Kontrolsüz pin **bir** tanedir (`pgvector/pgvector:pg16`) ve parite
kapısı (`stackfiles_test.go:236`, `for _, want := range stackImages`) onu yapısal olarak göremez —
yalnız `stackImages`'i döngülüyor. FR-005 ve D-052 ölçülen gerçeğe yazıldı; tasarımın sayısı
kullanılmadı.

### CB-47 — `tests/e2e`'nin silinmesi iki kalıcı kuralla çelişiyor

Tasarım §5 onu silinecekler arasında sayıyor. Artım 1 aynı pakete **bloklayıcı bir CI kapısı**
bağladı (`.github/workflows/ci.yml`, `Vet the e2e package compiles`) — gerekçesi defterde: paket
bir kez derlenemez hâle gelmiş ve hiçbir şey fark etmemişti. Silmek o kapıyı da götürür.
İlgili kurallar: *cross-boundary E2E zorunlu* · *bir kapıyı silmek bilgisini de siler*.
**Karar D-051:** paket kalır, seçim-katmanı bağımlılığından arındırılır.

---

## B. Tasarım kararlarının kaynakları (Artım 1'den taşındı, yeniden açılmadı)

| Karar | Kaynak | Tier |
|---|---|---|
| D-036 Supabase config'te sürüm pinlemiyor (14 alan `toml:"-"`) | rs-supabase, kaynak HEAD `eceb7d50` | deep |
| D-037 Nhost servis başına pinliyor; Firebase pinlemiyor | rs-pin-priorart (yayımlanmış JSON şemasına karşı) | standard |
| D-038 Emsallerde CLI bağımlılıktır, yığın ondan türer | rs-pin-priorart | standard |
| D-023 Expo: tablo SDK paketinde | rs-pin-priorart | standard |

---

## C. Unverified Decision Register

Artım 1'in register'ı **kapalı** devralındı — açık satır yok. Bu artımda yeni
teknoloji adı girmedi: FR-002 ağ ucu eklemiyor (D-030), FR-016'nın kapısı Go standart
kütüphanesinin `go/parser`'ıyla kurulur (Artım 1'de aynı desen `surface_test.go:673`'te kanıtlandı —
regex tabanlı ilk denemesi ham TypeScript dizeleri yüzünden yanlış sayım vermişti).

| Karar | Ad | status |
|---|---|---|
| FR-016 kapısının ayrıştırıcısı | `go/parser` (stdlib) | **closed** — Artım 1'de aynı desen üretimde kanıtlandı |
| FR-002 tablo taşıyıcısı | `@palbase/backend` npm paketi | **closed** — D-023, ağ ucu yok |

Açık satır sayısı: **0**. (Bu dosya kapının aradığı literali kendi metninde taşımaz —
Artım 1'de bir kapı tam olarak böyle yanlış tetiklenmişti.)

---

## Çapraz-depo kapılarının CI evi (I-3) — standard

Stakes: geri alınabilirlik 1 · yanlışlığın maliyeti 1 · genişlik 1 = **3 → standard**.
İki lane: resmi eylem dokümanları (WebFetch) + gerçek üretim kullanımı (grep MCP).
Dördüncü soru araştırılmadı, **yerelde ÖLÇÜLDÜ** — dokümandan güçlü kanıt.

CLAIM: `actions/create-github-app-token@v1`'in ürettiği jeton `actions/checkout`'un
`token:` girdisinde BAŞKA bir depoyu klonlamak için çalışır → SOURCE:
https://github.com/nocobase/nocobase/blob/main/.github/workflows/manual-npm-publish-license-kit.yml
(`repository: nocobase/license-kit` + `token: ${{ steps.app-token.outputs.token }}`)
→ VERIFIED: 2026-09-05, grep MCP ile üretim kodunda okundu → TIER: standard

CLAIM: `repositories:` virgül ya da satır sonu ile AYRILMIŞ ÇOKLU depo alır; `owner:`
tek başına verilirse kapsam kurulumdaki TÜM depolara açılır → SOURCE:
https://github.com/actions/create-github-app-token README → VERIFIED: 2026-09-05,
WebFetch → TIER: standard
  ⚠ Bu yüzden `owner:` tek başına KULLANILMADI: en az ayrıcalık için iki depo adlandırıldı.

CLAIM: GENEL bir ikincil depo jetonsuz checkout edilebilir; jeton yalnız ÖZEL ikincil
depolar için gerekir ("the default token is scoped to the current repository") →
SOURCE: https://github.com/actions/checkout README, "Checkout multiple repos (private)"
→ VERIFIED: 2026-09-05, WebFetch → TIER: standard

CLAIM: Çoklu depo yerleşimi için belgelenmiş iki desen var — "Side by side" (kök
`path: main` ile) ve "Nested" (önce kök checkout, sonra alt dizin). Sıralamaya dair
belgelenmiş bir uyarı YOK → SOURCE: https://github.com/actions/checkout README →
VERIFIED: 2026-09-05, WebFetch → TIER: standard
  → Karar: belgelenmiş "Nested" deseni izlendi (kök önce), çünkü kök checkout'un
    workspace'i temizlemesi hâlinde alt dizinler ondan SONRA yazılmış olur.

CLAIM (ÖLÇÜM, araştırma değil): atlanan bir Go testi paketi YEŞİL bırakır
(`--- SKIP` yazılır ama `ok` döner); `go test -json` ise `"Action":"skip"` üretir, ve
kardeş ağaç eksikken `-short` koşusu 10 kez "not beside this checkout" basar, ağaçlar
yerindeyken **0** → VERIFIED: 2026-09-05, `git archive HEAD` ile izole bir ağaçta ve
gerçek ağaçta iki kez koşuldu → TIER: ölçüm
  → Karar: iş akışı kapı ADLARINI saymıyor (liste kayar); atlama CÜMLESİNİ ölçüyor.
    Yeni bir çapraz-depo kapısı eklenirse kendiliğinden kapsama girer.

UD-101 | çapraz-depo kapıları için CI evi: palbase-cloud + app-token | stakes 3 | tier standard | status: verified | evidence: bu bölüm
