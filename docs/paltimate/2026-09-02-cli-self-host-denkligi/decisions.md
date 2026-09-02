# CLI'ın self-host denkliği — karar günlüğü

Açılış: 2026-09-02.

**Konu (kullanıcının çerçevesi):** V2 core self-host edilebiliyor. Ama CLI'dan
self-host'lu bir projeye bağlandıktan sonra "CLI konfigürasyonunu" kullanamıyorsun,
"farklı şeyler yapamıyorsun". CLI tarafının da self-host'ta çalışması isteniyor.

## Niyet envanteri (onay bekliyor)

1. CLI, self-host edilmiş bir V2 core kurulumuna bağlanabilmeli ve bağlandıktan
   sonra komut yüzeyi çalışmalı.
2. "CLI konfigürasyonu" — bugün kalıcı bir konfigürasyon YOK (`config.File struct{}`);
   adresler yalnız env değişkenleriyle eziliyor ve `PublicHost` hiç ezilemiyor.
3. Kapsam kararı bekliyor: hangi komutlar self-host'ta çalışmak ZORUNDA,
   hangileri meşru şekilde yalnız-bulut kalır.

## Kod Gerçeği Dosyası

### VERIFIED (okudum)

| Olgu | Kanıt |
|---|---|
| CLI'ın adres kümesi tek buluta SABİT | `sdk/cli/internal/config/config.go:44-49` — `theCloud = {Studio: https://palbase.studio, Auth/PlatformAPI: https://api.palbase.studio, PublicHost: palbase.studio}` |
| Üç env ezmesi var: `PALBASE_STUDIO_URL`, `PALBASE_AUTH_URL`, `PALBASE_PLATFORM_URL` | `config.go:141-152` (`Resolve`) |
| **`PublicHost` için ezme YOK** | `config.go:141-152` — `ep.PublicHost` hiç yazılmıyor |
| Kalıcı konfigürasyon dosyası BOŞ bir tip | `config.go:59` — `type File struct{}`, yorum: *"nothing carries anything now: there is one cloud, so the file has no choice left to record"* |
| Komutlar İKİ otoriteye bölünüyor | `cmd/palbase/main.go` — `managementREST()` → `resolved.Endpoints.PlatformAPI` (bulut) vs `openStackManagement()` → linkli hedefin kendi `/v1/management/*` yüzeyi |
| Yığın-yüzeyi kasıtlı bir tasarım kararı | `main.go:164-169` — *"Sending these through the platform API would make self-host a second, thinner product."* |
| V2 core TAM bir `/v1/management/*` yüzeyi sunuyor | `v2/internal/management/gen.go:928+` — auth/providers/sessions/settings/templates/users, deployments, egress, flags, keys, notifications, storage, … |
| `palbase link <url> --token-stdin` self-host için VAR | `internal/backend/project_link.go:130` |
| Bare ref → adres çözümü `PublicHost`'a bağlı | `project_link.go:113-118` — host boşsa reddediyor |
| `tenantRefOf` "bu bulutun projesi mi" sorusunu `PublicHost` ile cevaplıyor | `main.go:133-155` |
| Bulut olmayan adres için `CloudKeyFetcher` reddediyor | `main.go:112-116` — *"is not a project on this cloud"* |
| `cloudFacts.TenantDomain` bootstrap'tan okunuyor (derlenmiş sabit değil) | `main.go:264-272` |
| CLI yüzeyi 44 komut | ölçüldü: `palbase --help`, 2026-09-02 |
| Atıf verilen önceki self-host tasarımı ARTIK YOK | `project_link.go:6` `docs/paltimate/2026-08-12-v2-faz0-selfhost/design-management-api.md` → repoda bulunamadı (find, 2026-09-02) |

### ASSUMED (henüz okumadım)

| Varsayım | Neden açık |
|---|---|
| Hangi komutların self-host'ta fiilen düştüğü | keşif ajanı ölçüyor |
| V2 core'un `link`/`push` için gereken kimlik/anahtar yüzeyi | keşif ajanı ölçüyor |
| Prior art (Supabase/Appwrite/PocketBase/Nhost CLI self-host modeli) | research bekliyor |

## Kararlar

_(henüz yok)_

## ÜRETİM ÖLÇÜMÜ (2026-09-02)

`GOWORK=off go build ./cmd/palbase` ile derlenen binary üzerinde:

```
$ palbase endpoints
Studio:      https://palbase.studio
Auth:        https://api.palbase.studio
Platform:    https://api.palbase.studio
Projects:    <ref>.palbase.studio

$ PALBASE_STUDIO_URL=https://palbase.acme.com \
  PALBASE_AUTH_URL=https://palbase.acme.com \
  PALBASE_PLATFORM_URL=https://palbase.acme.com palbase endpoints
Studio:      https://palbase.acme.com
Auth:        https://palbase.acme.com
Platform:    https://palbase.acme.com
Projects:    <ref>.palbase.studio        ← DEĞİŞMEDİ
```

**Bulgu (M-001):** Var olan kaçış kapısı EKSİK. Bir self-hoster üç değişkenin
üçünü de kursa bile CLI, kiracı ad alanının hâlâ `palbase.studio` olduğunu
sanıyor. Sonuçları zincirleme:

- `tenantRefOf` (`cmd/palbase/main.go:133`) self-host adresini "bu bulutun projesi
  değil" sayar → `CloudKeyFetcher` `"is not a project on this cloud"` ile reddeder
  (`main.go:112-116`).
- `palbase link <bare-ref>` self-host'ta `https://<ref>.palbase.studio` üretir —
  yani kullanıcının kendi kurulumuna değil, BİZİM üretim bulutumuza
  (`project_link.go:113-118`).
- `selectedProjectTarget` (`main.go:229-240`) aynı sabitle adres kurar.

Yani "adresi ezebiliyorsun" doğru DEĞİL: ezilemeyen tek alan, hangi adreslerin
"bizim" sayıldığına karar veren alan.

**Bulgu (M-002):** Kimlik katmanı zaten hedef-başına ve self-host-farkında —
`~/.palbase/credentials.json` URL ile anahtarlanıyor, üç kaynak sırası
`PALBASE_ACCESS_TOKEN` → store → red (`internal/backend/credentials.go:14-23`),
ve `Kind` ayrımı (person/key/PAT) sunum farkını taşıyor. Eksik olan KİMLİK
değil, ADRES/DAĞITIM kavramı.

## Zaten VAR olan yapıtaşları (yeniden inşa etmeyelim)

| Yapıtaşı | Kanıt | Ne veriyor |
|---|---|---|
| Hedef-başına kimlik deposu | `internal/backend/credentials.go:14-23`, `StoreCredential` URL ile anahtarlıyor | `gh hosts.yml` / `docker auths` şekli — self-host için doğru birim |
| Kimlik SUNUM ayrımı | `credentials.go:63-92` — `KindPerson`/`KindKey`/`KindPAT` | `apikey` vs `Bearer` vs `DPoP` farkı taşınıyor |
| `--token-stdin` ile self-host anahtarı | `project_link.go:130` | Bulut oturumu olmadan bağlanma yolu |
| **Yığın kendini TANITAN belge** | `v2/internal/server/wellknown.go:37` — `GET /.well-known/palbase.json` → `{"hosting":"project","sdk_version":"…"}` | Yetenek keşfi için HAZIR dikiş; bugün yalnız iki alan taşıyor ve `hosting` SABİT `"project"` |
| Çekirdeğin tam yönetim yüzeyi | `v2/internal/management/gen.go:928+` | Yığın-bağlı komutların çoğu self-host'ta zaten çalışabilir |
| `--insecure` (ilk boot'un kendi imzalı sertifikası) | `project_link.go:415-425` | Self-host ilk kurulum gerçeği karşılanmış |

## Yayınlanmış docs zaten AÇIĞI YAZIYOR (yeni bir keşif değil, kabul edilmiş borç)

`v2-cloud/platform/studio/src/content/docs/cli/overview.md:80`:

> "The first three lines can be overridden for a self-hosted deployment with
> `PALBASE_STUDIO_URL`, `PALBASE_AUTH_URL` and `PALBASE_PLATFORM_URL`. **The
> fourth cannot: a project's host has no environment override**, so a project's
> address is always its ref under `palbase.studio`."

`cli/deploy.md:354-362`: uzak bir self-host yığınının logları **reddediliyor** —
*"a project's own management surface has no log operation at all"*.

Yani ürün, açığı biliyor ve belgeliyor. Bu tasarımın işi açığı KEŞFETMEK değil,
KAPATMAK.

## Komut yüzeyi ölçümü (kısmi — kuyruk bekleniyor)

Senaryo: `palbase link https://my.host --token-stdin` ile bağlanmış bir checkout,
`.palbase/selection.json` YOK, bulut oturumu YOK.

| Komut | Otorite | Self-host bugün | Engel |
|---|---|---|---|
| `auth` | YIĞIN | **ÇALIŞIYOR** | — |
| `egress` | YIĞIN | **ÇALIŞIYOR** | — |
| `deploys` | YIĞIN | **ÇALIŞIYOR** | `deployments.go:229-238` linkli yoldan dönüyor |
| `debug attach` | YIĞIN | **ÇALIŞIYOR** | — |
| `build` | YEREL | **ÇALIŞIYOR** | ağ yok |
| `apikey list` | YIĞIN | **ÇALIŞIYOR** | `/v1/management/keys` (`apikey.go:88`) |
| `apikey reveal/rotate` | BULUT | KIRIK — **dürüst** | `apikey.go:203`: *"is not a project on this cloud — its keys are its own"* |
| `android/ios use` | BULUT | KIRIK — **tasarımca dürüst** | `platform_link_target.go:49-58` |
| `flags list/add/remove` | YIĞIN | **ÇALIŞIYOR** | — |
| `flags user *` | BULUT | KIRIK | `user.go:476` → `Selection().Resolve` |
| `db` | YEREL-ONLY | KIRIK | `db.go:116-126` — hedef `Local` değilse reddediyor; yalnız `.palbase/local.json` `Local` yazıyor |
| `clone` | BULUT | **KIRIK ve SESSİZ** | `project_link.go:188` `knownRefs` `asked=false` dönüyor → reddetmiyor, `deploy.go:611-618` `PublicHost` ile **bizim buluta** adres kurup düşüyor |
| `doctor` | karışık | **YANLIŞ RAPOR** | `doctor.go:161-162` her zaman `palbase.studio` yazıyor; `doctor.go:166-167` çalışan bir self-host kurulumuna ✗ *"not logged in"* diyor |
| `endpoints` | — | **YALAN SÖYLÜYOR** | `main.go:561` — ölçüldü (M-001) |

**Şekil (kısmi veriyle):** yüzeyin kayda değer bir bölümü self-host'ta ZATEN
çalışıyor. Asıl kusur "hiçbir şey çalışmıyor" değil; **başarısızlıkların bir
kısmının DÜRÜST, bir kısmının SESSİZ ya da YANILTICI olması.** `apikey reveal`
doğru cümleyi kuruyor; `clone` sessizce yanlış buluta gidiyor, `doctor` çalışan
bir kuruluma bozuk diyor, `endpoints` var olmayan bir adres alanı basıyor.

Bu, `feedback_never_fail_open_in_security_paths` ve gh'nin #13277 dersiyle aynı
sınıf: kapının VARLIĞI değil, ŞEKLİ ve DOĞRULUĞU sorun.

## ÇEKİRDEĞİN KENDİ SINIRI — kapsamı belirleyen önerme (DOĞRULANDI)

`v2/api/management.openapi.yaml:17-22`, birebir (2026-09-02'de okudum):

> **WHAT IS NOT HERE, on purpose.** Nothing about accounts, members, invitations,
> projects, branches, tiers, quotas or billing: those are concepts of a control
> plane that a project you run does not have **and must never learn**. A command
> that touches MORE THAN ONE tenant belongs to the cloud; this document is the
> surface of ONE stack.

Doğrulayan ölçümler:
- Spec'te 72 metot+yol çifti, `gen.go:3270-3600`'de 72 kayıt — **kümeler birebir aynı**; elle mount edilmiş ya da yalnız-spec kalmış hiçbir şey yok. (core-surface-scout, mekanik diff)
- `grep -rn "v1/cloud" v2/` → yalnızca `internal/deploy/reservedprefixes.go`'da **ayrılmış ön ek** olarak geçiyor; çekirdek orada hiçbir şey SUNMUYOR. (kendim koştum, 2026-09-02)
- `POST /v1/management/push` VAR — dağıtım kapısı çekirdekte mevcut.
- `POST /v1/management/session` **tek kimlik doğrulamasız işlem** — yığının kendi giriş kapısı.
- **`logs` işlemi YOK ve yokluğu beyan edilmiş:** `v2/deploy/cli_harness.sh:28-30` —
  *"the management surface has no log operation (the runtime writes to stdout and
  nothing keeps it), **which is its own design work**."*

### D-001 — Bulut-özel komutlar bulut-özel KALIR (kapsam kararı)

**Karar:** Bu tasarımın hedefi "44 komutun 44'ünü self-host'ta çalıştırmak" DEĞİL.
`project`, `members`, `apikey reveal/rotate`, `clone`, `ios/android use` gibi
BİRDEN FAZLA kiracıya dokunan verb'ler bulut kavramıdır ve öyle kalır.

**Gerekçe:** Bu bir eksiklik değil, çekirdeğin YAZILI mimari sınırı (yukarıdaki
alıntı). Kontrol düzlemi kavramlarını çekirdeğe taşımak, self-host'u denk kılmaz —
çekirdeği çok-kiracılı bir kontrol düzlemine dönüştürür ve ürünü ikiye böler.
Ayrıca `feedback_self_host_parity_outranks_density` "core'a mimari dokunma" diyor.

**Alternatifler:** (a) çekirdeğe mini bir kontrol düzlemi eklemek — sınır beyanını
çiğner, iki ürün bakımı doğurur; (b) bu verb'leri bulutta bırakıp self-host'ta
SESSİZ bırakmak — bugünkü hâl, ve asıl kusur bu (aşağıya bakınız).

**Kanıt:** `v2/api/management.openapi.yaml:17-22`; `reservedprefixes.go` ölçümü.

**Sonuç:** İş, yüzeyi büyütmek değil — **adres modelini düzeltmek** ve
**reddi dürüst kılmak**.

### Bulunan yan kusur: atıf verilen tasarım belgesi YOK

Hem `v2/api/management.openapi.yaml:23` hem `sdk/cli/internal/backend/project_link.go:6`
şuna atıf veriyor: `docs/paltimate/2026-08-12-v2-faz0-selfhost/design-management-api.md`.
`find` ile repoda **bulunamadı** (2026-09-02). İki canlı dosya var olmayan bir
belgeyi kanıt diye gösteriyor.

## İKİ BELİRLEYİCİ BULGU (kendim doğruladım, 2026-09-02)

### B-1 — `palbase push` self-host'ta ZATEN UÇTAN UCA ÇALIŞIYOR

`deploy.go:456-473` linkli hedefi görünce bulut kolunu (`deploy.go:494-501`)
tümüyle atlıyor. `runStackPush`'un her sıçraması yalnız `target.URL`'e gidiyor:
SDK sapma kontrolü (`stack_sdk.go:40,58`), bundle (yerel Bun), ABI tavanı
(`stack_push.go:188`), `@Upload` kovaları (`:219`), dağıtım
(`:98` → `<base>/v1/management/push`), ve `RefreshSpec` (`stack_spec.go:44-56`).

**Hiçbir noktada `PlatformAPI`, `PublicHost`, seçim ya da defter okunmuyor.**
Defter yalnız `status`'un `lastPushAttempt`'inde beliriyor ve orada da hata
vermeden `nil` dönüyor (`status_project.go:346-348`). Kod bunu zaten yazmış:
*"A push straight at a stack — a linked checkout, a self-hosted stack, a
port-forwarded push — never reaches the ledger"* (`pull_spec.go:238-241`).

### B-2 — `palbase login` self-host'ta TEK BİR ÇAĞRI uzakta

Çekirdek, `palbase-cli`'ı **her kurulumun kendi veritabanına** açılışta ekliyor —
ve bunu açıkça self-host için yapıyor (`v2/internal/modules/auth/internal/server/bootstrap_handler.go:38-60`):

> *"in the fleet model `palbase-cli` was NOT seeded per installation (that would
> put the CLI's client row in every customer database), so a fleet seeded it in
> one place. **Here there is one place.** … Without it, `palbase login`'s
> /oauth/token leg fails with 'client palbase-cli not found'."*

Public PKCE istemcisi olarak (`ClientSecretHash: nil`, `TokenEndpointAuthMethod: "none"`,
`IsPublic: true`), redirect URI'leri CLI'ın `LoopbackCallbackPorts = {54321..54325}`
listesinin **birebir aynası** — ve iki taraf da karşısındakini adlandırarak
kilit adım istiyor (`bootstrap_handler.go:43-45` ↔ `internal/auth/auth.go:389-394`).
Çekirdek OIDC uçlarını ve `/auth/login` köprü sayfasını da sunuyor.

**Kırılan yer: 4 ayaktan BİRİNCİSİ.** `PALBASE_AUTH_URL` akışı gerçekten
yönlendiriyor, ama ilk çağrı `plane.Bootstrap(ctx)` (`browser_login.go:48`) →
`GET <base>/v1/cloud/config` (`v2login.go:79`) ve bu bir **kontrol düzlemi**
rotası — `v2-cloud/platform` sunuyor, çekirdek sunmuyor. Alınan tek şey her
sonraki ayağın `apikey` başlığında gönderdiği anon key (`v2login.go:145,190`).

### B-3 — Çekirdeğin yönetim yüzeyi self-host için İKİ kimliği zaten kabul ediyor

`v2/internal/management/auth.go:33-40`:

> *"TWO credentials open it… the operator's `service_role` key, which is what a
> person running it yourself already holds in their .env; **a USER of this stack
> whose token carries the management claim — what `palbase login` authenticates
> against.**"*

- `apikey: pb_project_s…` → özne `service_role_key`, rol `service_role` (`auth.go:71-73`)
- `Authorization: Bearer <token>` + management iddiası → özne = kullanıcı id (`auth.go:107-116`)
- **Kişi anahtarı YENER** (`auth.go:96-105`) — 2026-08-16'da ölçülmüş bir düzeltme
- Management iddiası kendi kendine atanamaz: `auth.users.is_management` tek yoldan yazılıyor (`auth.go:19-21`)
- `POST /v1/management/session` = yığının kendi auth modülüne **e-posta+parola** girişi, dönen `{token, expires_at}` kısa ömürlü ve diske yazılmıyor (`session.go:35-68`, `api/management.openapi.yaml:2277-2287`); muafiyet **tam yol** eşleşmesiyle, ön ekle değil (`auth.go:83-89`)

**Sonuç:** Self-host'ta oturum açmanın iki yolu çekirdekte HAZIR. CLI ikisini de
kullanmıyor — yalnız `--token-stdin` ile service_role anahtarını kabul ediyor,
yani `ra-authmodel`'in "Hasura/Convex şekli" diye reddettiği kimliksiz-süresiz
paylaşılan sır yolunu.

## Komut yüzeyi — ikinci parti

| Komut | Otorite | Self-host | Engel |
|---|---|---|---|
| **`push`** | YIĞIN | **ÇALIŞIYOR** | — (B-1) |
| `link --token-stdin` | YIĞIN | **ÇALIŞIYOR** | anahtar yazılmadan önce `/v1/management/keys`'e karşı doğrulanıyor (`link_token.go:35-53`) |
| `link <bare-ref>` | BULUT | KIRIK | `project_link.go:116-120` `PublicHost` |
| **`login`** | BULUT | **KIRIK — 1. ayak** | `v2login.go:79` `/v1/cloud/config` (B-2) |
| `logout` | karışık | **ÇALIŞIYOR** | `--token-stdin` kimliğini düşürmenin TEK yolu (`main.go:520-521`) |
| `logs` | — | KIRIK — **dürüst** | `logs.go:242-265`; çekirdekte log işlemi yok (beyan edilmiş) |
| `members` | BULUT | KIRIK — **dürüst** | `members.go:153` *"is not a project on this cloud — it has no membership"* |
| `notifications`, `macos link`, `ios link`, `android link` | YIĞIN | **ÇALIŞIYOR** | — |
| `init` | YEREL | **ÇALIŞIYOR** | — |
| `flags user *` | — | KIRIK — **gereksizce** | `user.go:476` seçim çözüyor, ama ref **yalnız başarı satırına** enterpole ediliyor; asıl çağrı (`user.go:67`) zaten yığına gidiyor |
| `ios/android use` | — | KIRIK — **tasarımca** | `platform_link_target.go:54-58` |
| `open` | BULUT | **SESSİZCE İŞE YARAMAZ** | `doctor.go:254` çözüm hatasını yutuyor, çıplak kök URL açılıyor |

**`flags user *` bir hediye:** seçim çözümü orada sırf çıktı cümlesindeki ref için
duruyor. Gerçek iş zaten yığına gidiyor. Bu, kapsamın en ucuz kalemi.

### D-002 — Desteklenmeyen verb GİZLENMEZ, ADLANDIRARAK reddedilir

**Karar:** Self-host bir dağıtımda bulut-özel komutlar komut ağacından
kaldırılmayacak/gizlenmeyecek; çalışma anında, dağıtımı ve sebebi adlandıran bir
mesajla reddedecekler.

**Gerekçe (bu karar bana ait değil, repo zaten vermiş):**
`cmd/palbase/surface_test.go:387` — FR-037 canlı bir kapı:

> *"the surface is the SAME on a self-hosted stack… **There is no command that
> exists only for one of them and no flag that turns one off.** This is the
> assertion that stops the drift the cutover invites."*

Ekosistem de aynı yerde: `gh` GHES'te komut gizlemiyor, `auth.IsEnterprise(host)`
ile çalışma anında adlandırarak reddediyor
(`"An unsupported host was detected. Note that gh attestation does not currently
support GHES"`). `cmd.Hidden`'ı konağa göre set eden bir yer yok.

**Reddin ŞEKLİ — gh'nin kendi testinden alınan kural** (`internal/attachments/client_test.go:409`):

> *"This row defends a message, not an order. On an enterprise server the token
> message would tell the user to re-authenticate, and **that remedy does not work
> there**."*

→ **Dağıtım kısıtı, KİMLİK hatasından ÖNCE bildirilmeli.** Bugünkü `doctor` tam
bu hatayı yapıyor: çalışan bir self-host kurulumuna ✗ *"not logged in"* diyor ve
çaresi o hedefte yanlış tavsiye.

**Üç sonuç, iki değil** (gh `source.go:56-68` modeli): destekleniyor /
bilinen-ama-bu-dağıtımda-yok (ürünü + adresi adlandırarak) / bilinmeyen adres.

**Ve kapı gerçeğe karşı yeniden ölçülmeli:** gh #13277'de bir yetenek kapısı,
kendisini doğuran yetersizlik ortadan kalktıktan sonra da reddetmeye devam etti;
kullanıcı kontrolü silince GHES 3.19'da çalıştı. (bkz.
`feedback_a_gates_exception_must_retire_itself`)

**Alternatifler:** (a) komutu gizlemek — FR-037 kapısını kırar, ve `gh`'nin
bulunmuş bir yeri yok; (b) bugünkü karışık hâl — `members`/`logs`/`apikey reveal`
dürüst, `clone`/`open`/`doctor`/`endpoints` sessiz ya da yanıltıcı.

## M-003 — "ÇALIŞIYOR" İDDİASI ÖLÇÜLDÜ (kullanıcının sorusu üzerine)

Kullanıcı kapsam sorusuna "çalışıyor mu?" diye karşılık verdi. İddiam o ana kadar
KOD YOLU okumasına dayanıyordu; canlı koşu kanıtı `v2/.verify/cli/` altında
bulundu — 2026-08-31 19:55-20:04 tarihli gerçek bir `cli_harness.sh` koşusu.

**Senaryonun self-host olduğu doğrulandı:** sandbox HOME'da (`.verify/cli/home/.palbase/`)
YALNIZCA `credentials.json` var, içinde tek girdi —
`https://127.0.0.1 → {kind: "key", value: "pb_project_sFT…"}`.
**`config.json` YOK, bulut oturumu YOK, `selection.json` YOK.**

| Verb | Kayıt | Sonuç |
|---|---|---|
| `link` | `link.log` | `linked to https://127.0.0.1 (project)`; `.palbase/ios/palbase-config.json` + `Main.xcconfig` yazıldı |
| `build` | `build.log` (boş) | başarı |
| `plan` | `plan.log` | yığına karşı tam şema diff'i — `create table notes`, `enable RLS`, `add policy`, ve `--approve` isteyen yıkıcı kalemler |
| `push` | `push.log` | 118 KB gönderildi → yığın `schema_incompatible` ile **ürün düzeyinde** reddetti (expand→deploy→contract tavsiyesiyle). Kimlik/yönlendirme hatası DEĞİL |
| `secret set/list` | `secret.log` | `✓ HARNESS_TOKEN is set`; liste iki sır gösterdi |
| `spec` | `spec.log` | `wrote .palbase/openapi/main.json (49922 bytes)` |
| `status` | `status.log` | `credential: store (this project's key)`, `deployed: b752491d20a5, 37 endpoint(s)`, SDK sapması raporlandı |
| `deploys` | `deploys.log` | yığından sürüm geçmişi |
| `rollback` | `rollback.log` | **MUTASYON BAŞARILI** — `151f201b1f1e is active, serving 37 endpoint(s)` |

**Sonuç:** Yığın-bağlı yarı self-host'ta ÖLÇÜLMÜŞ biçimde çalışıyor. `push`'un
reddi en güçlü kanıt: anahtar kabul edildi, artefakt teslim edildi, şema
değerlendirildi, düşünülmüş cevap döndü.

**Bu koşuda ölçülmeyenler (dürüstlük payı):** `login`, `clone`, `doctor`,
`endpoints`, `members`, `flags user *`, `open` — harness bunları hiç çağırmıyor.
Onlara dair iddialarım kod yolu okumasıdır ve öyle etiketlenmiştir.

**Taze koşu yapılamadı:** 443 portunu başka bir oturumun yığını (`pb-verierisim`)
tutuyor ve `deploy/docker-compose.yml:650-654` o portu sabit bağlıyor (portless
redirect zorunluluğu). Başkasının yığını indirilmedi —
`feedback_infra_not_my_lane_just_wait`.

---

## KULLANICI KARARLARI (2026-09-02)

### D-003 — Kapsam: adres modeli + dürüst red + `login` (Seçenek 1)
**Karar:** Kullanıcı, M-003 ölçümünü gördükten sonra Seçenek 1'i seçti.
**Kapsam içi:** (a) ezilebilir adlandırılmış dağıtım kavramı; (b) sessiz/yanıltıcı
kırıkları dürüst kılmak (`clone`, `open`, `doctor`, `endpoints`); (c) gereksiz
kırıkları düzeltmek (`flags user *`); (d) `palbase login`'i kendi yığınına karşı
çalıştırmak.
**Kapsam DIŞI:** çekirdeğe kontrol düzlemi kavramı eklemek (D-001); `logs` için
çekirdeğe log saklama eklemek (beyan edilmiş ayrı iş).

### D-004 — Adres modeli: adlandırılmış profiller, checkout linki KAZANIR
**Karar:** `~/.palbase/config.json` adlandırılmış dağıtımlar taşır
(`{kind: cloud|selfhost, studio, auth, platform, publicHost}`) + bir `current`.
Çözüm sırası: `--deployment` bayrağı → `PALBASE_*` env → `.palbase/project.json`
(LINK, varsa **hep kazanır**) → `current` → derlenmiş varsayılan.
Kimlikler AYRI dosyada, adresle anahtarlı (bugünkü `~/.palbase/credentials.json`).
**Gerekçe:** Sektörün ortak sırası (kubectl/docker/aws/Upbound/clig.dev). Linkin
`current`'ı yenmesi, belgelenmiş yanlış-hedef kaza sınıfını linkli checkout'larda
tümüyle ortadan kaldırıyor — global seçim yalnız link'siz verb'ler (`login`,
`project`, `clone`) için devreye giriyor.
**Alternatifler:** yalnız checkout-bağlı (login/clone her çağrıda bayrak isterdi,
"CLI konfigürasyonu" talebini karşılamazdı); yalnız `PALBASE_PUBLIC_HOST`
(kalıcı konfigürasyon yok, env görünmez+miras alınır — moby#38148 ve cr0x olayı).
**Kanıt:** Supabase profil alanları (`project_host` = bizim `PublicHost`);
Directus `d6s` (PR #27861, 2026-08-17); Upbound profil tipi; docker öncelik sırası.

### D-005 — Self-host kimliği: kendi yığınına tarayıcı PKCE, anahtar yedekte
**Karar:** `palbase login` self-host'ta kullanıcının kendi kurulumunun OIDC
uçlarına gider. `--token-stdin` service_role yolu ilk kurulum ve CI için KALIR.
**Gerekçe:** Çekirdek işin çoğunu yapmış (B-2): `palbase-cli` her kuruluma public
PKCE istemcisi olarak ekiliyor, redirect port'ları CLI ile kilit adımda, OIDC
uçları ve `/auth/login` köprüsü sunuluyor; yönetim yüzeyi `Bearer`+management
iddiasını zaten kabul ediyor ve **kişiyi anahtara tercih ediyor** (`auth.go:96-105`).
Tek statik service_role sırrı sektörün terk ettiği şekil (Hasura CVSS 9.8;
Supabase'in `sb_secret_*` göçü; ölümcül üçlü).
**Alternatifler:** yalnız `--token-stdin` (bugünkü, kimliksiz/süresiz/iptalsiz);
`POST /v1/management/session` e-posta+parola (çekirdekte var ama `login`'in kendi
yazdığı "parolanı asla bu terminale yazma" ilkesini self-hoster için delerdi).

## `PublicHost` denetimi — 16 okuyucu, `null` altında davranış (2026-09-02, kendim grep'ledim)

`kind: selfhost` + `publicHost: null` verildiğinde her okuyucunun ne yapacağı:

| Okuyucu | `publicHost=""` altında | Yargı |
|---|---|---|
| `main.go:113,256,346,414` `tenantRefOf` | `false` döner (`main.go:135`: host boşsa erken çıkış) | **DOĞRU** — self-host adresi hiçbir zaman "bu bulutun projesi" değil |
| `main.go:230` `selectedProjectTarget` | `false` döner (açık guard) | **DOĞRU** |
| `project_link.go:116` bare-ref | *"this CLI has no tenant host configured, so %q cannot be resolved to an address"* | **ZATEN DÜRÜST** |
| `apps.go:547-550` | `r.PublicHost == nil` guard'ı var | **DOĞRU** |
| `main.go:561` `endpoints` | `Projects:    <ref>.` basar | **DÜZELTİLECEK** — "bu dağıtımda kontrol düzlemi yok" demeli |
| `doctor.go:162` | boş host'lu satır | **DÜZELTİLECEK** |
| `deploy.go:367,624` (`clone`/`pull`) · `backend.go:559` | `https://<ref>.` — **geçersiz adres kurar** | **DÜZELTİLECEK** — `clone`'un sessiz kusurunun kökü |
| `ios_use.go:111-113` · `native_link.go:166-167` · `web_link.go:88` · `pull_spec.go:164-166` | Studio config artefaktı yolu; hepsi bulut `use` yolunda | **REDDETMELİ** (D-002 şekliyle) |

**Sonuç:** 16 okuyucunun 8'i `null` altında zaten doğru davranıyor. Kapsam,
kalan 8 çağrı yerinde dürüst red kurmak — geniş bir refactor değil.

## Bitişik bulgu (kapsam DIŞI, ama kaydedilmeli)

Çekirdeğin KENDİ `PublicHost` yapılandırması self-host'ta bulut adresine düşüyor:
`v2/internal/modules/messaging/internal/storage/presigner.go:120-121` ve
`internal/pushwake/bridge.go:100-101` — ikisinde de yorum aynen:
*"PublicHost is the gateway host suffix; **\"\" defaults to dev.palbase.studio**."*

Yani self-host eden birinin storage presign / push-wake yolu, yapılandırılmamışsa
BİZİM dev alan adımıza düşüyor. Bu CLI'ın değil çekirdeğin kusuru ve bu koşunun
kapsamında değil — ama `feedback_no_remaining_work_finish_everything` gereği
kaydediliyor ve bitişte kullanıcıya sunulacak.

## `login` yolunun ölçümü — gerçek kısıt ve çözümü

### Ölçülen kısıt (login-path-scout + kendi doğrulamam)

Çekirdek `/auth`, `/oauth`, `/admin` yollarının HEPSİNİ `Verify` grubunun içine
mount ediyor (`v2/internal/modules/auth/auth.go:126-138` ← `internal/server/server.go:197-201`).
`Verify`'dan kaçan tek şey `MountPublic` ve auth modülü orada **yalnız iki yol**
yayımlıyor: `/.well-known` ve `/.well-known/*` (`auth.go:200-211`).

Kimlik yoksa `credentialFor` (`identitymw.go:624-634`) `extractBearer`'a düşüyor →
`"missing authorization header"` → **401, hint `missing_authorization`**. Handler'a
hiç ulaşılmıyor.

`apikey` olarak anon key sunulursa `mintFromAPIKey` `RoleAnon` iJWT üretiyor ve
`/oauth/authorize` + `/oauth/token` geçiyor. **Yani CLI'ın 2. ve 4. ayakları
self-host çekirdeğe karşı OLDUĞU GİBİ çalışır — tek şart anon key'i önceden
edinmek.**

Ve edinilecek pre-auth rota YOK: `/.well-known/palbase.json` publishable key'i
KASTEN bıraktı (`wellknown.go:11-18` — *"handing a working client credential to
whoever knows the address means a stranger can sign up, spend rate limit and read
whatever row-level security lets an anonymous caller reach"*), `/v1/management/keys`
ise kimlik istiyor. Tavuk-yumurta.

### Çözüm ölçüldü: operatör anahtarı ZATEN elinde tutuyor

- `v2/.verify/.env` → `PALBASE_ANON_KEY=pb_project_c…` (kendim baktım, 2026-09-02)
- `v2/deploy/up.sh:153` → `printf "   publishable key : %s\n"` — **açılışta ekrana basılıyor**

### D-006 — Anon key, dağıtım kaydında BİR KEZ verilir; `login`'in yalnız 1. ayağı değişir

**Karar:** Self-host dağıtımı kaydedilirken operatör publishable (anon) anahtarını
bir kez verir. `login`'in 2/3/4. ayakları **hiç değişmez** — bugünkü `apikey`
başlığı aynen gider. Yalnız 1. ayak (`GET /v1/cloud/config`) `kind: selfhost`
dağıtımlarda atlanır; üç Bootstrap olgusu şuradan gelir:
`anonKey` = kayıtta verilen değer · `issuer` = çekirdeğin kendi
`/.well-known/openid-configuration`'ı (anonim erişilebilir, `Verify` dışında) ·
`tenantDomain` = yok (self-host'ta kiracı ad alanı yok).

**Gerekçe:** En küçük değişiklik. Çekirdeğin güvenlik kararını (`wellknown.go`)
geri almıyor, çekirdeğe yeni pre-auth rota açmıyor, mimari sınırı (D-001)
çiğnemiyor. Operatör anahtarı zaten görüyor.

**Anahtar NEREYE yazılır:** `~/.palbase/credentials.json`'a (0600, zaten var,
adresle anahtarlı) — `config.json`'a DEĞİL. Directus'un dersi: konfig dosyası sır
taşımaz, kimlik ayrı dosyada durur.

**Alternatifler ve neden düştüler:**
- Çekirdeğe `/v1/cloud/config` muadili pre-auth rota → `wellknown.go`'nun kapattığı deliği aynen yeniden açar.
- `/oauth/*`'ı `Verify` dışına almak → çekirdekte mimari değişiklik; anonim yüzey kontrolünü kaldırır.
- `POST /v1/management/session` (e-posta+parola) → kullanıcı reddetti (D-005) **ve tarihçe de reddediyor** (aşağı).

### Tarihsel kanıt: yığın-doğrudan parola girişi DENENDİ ve EMEKLİYE AYRILDI

`sdk/cli/internal/backend/stack_login.go:4-14` — dosya artık yalnız bir HTTP
istemci fabrikası, ve içindeki not şunu yazıyor:

> *"What used to live here was a second way to authenticate: sign in to a project
> with an email and password, keep the access token, throw the refresh away.
> **Measured on 2026-08-16 that token lives 1800 seconds**, which is why
> `palbase push` kept answering 'that stack no longer accepts this session' half
> an hour into an afternoon. Identity now has ONE resolver (credentials.go) and
> that path is gone rather than patched."*

Grep: `stackLogin|StackLogin` çağıranı **YOK** — yol gerçekten kaldırılmış.

**Bu, D-005'i bağımsız olarak doğruluyor:** PKCE doğru cevap çünkü refresh token
üretiyor, ve çekirdek `palbase-cli`'ı tam da `offline_access` + `refresh_token`
grant'iyle ekiyor (`bootstrap_handler.go:46-53`). Emekliye ayrılan yolun kusuru
(refresh yokluğu) seçilen yolda yapısal olarak yok.
