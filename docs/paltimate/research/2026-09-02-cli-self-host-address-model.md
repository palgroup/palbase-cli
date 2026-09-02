# CLI'ın self-host adres/profil modeli — araştırma

Tarih: 2026-09-02 · Tier: **deep** (stakes 6/6) · 6 kör açı

**Çerçeve cümlesi:** Tek bir `palbase` binary'sinin hem çok-kiracılı bulutumuza
hem de self-host edilmiş bir V2 core kurulumuna karşı denk çalışması için adres +
kimlik + komut-yüzeyi modeli ne olmalı?

---

## Açı 4 — kubectl / docker / gh / aws / tea / Upbound context modeli

### Kanıt satırları

- CLAIM: kubeconfig hedefi ÜÇ ayrı listeye böler (`clusters` = uç nokta+CA, `users` = kimlik, `contexts` = ikisine + namespace'e işaret eden üçlü) ve `current-context` tek skaler → SOURCE: https://kubernetes.io/docs/concepts/configuration/organize-cluster-access-kubeconfig/ → VERIFIED: 2026-09-02 WebFetch → TIER: deep
- CLAIM: `KUBECONFIG` çok-dosya birleştirmesi **anahtar bazında ilk-dosya-kazanır**; birinci dosyadaki KISMİ bir girdi, ikincideki tam girdiyi bastırır → SOURCE: aynı URL (birebir alıntı) → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Docker öncelik sırası `--host` → `--context` → `DOCKER_HOST` → `DOCKER_CONTEXT` → `docker context use` → varsayılan soket → SOURCE: https://docs.docker.com/reference/cli/docker/ + https://docs.docker.com/engine/manage-resources/contexts/ → VERIFIED: 2026-09-02 WebFetch → TIER: deep
- CLAIM: Docker context'i env değişkeninin ÜSTÜNE ekledi çünkü env bir uç nokta ADLANDIRABİLİR ama **kimlik, TLS materyali ve metadata TAŞIYAMAZ**, listelenemez, denetlenemez → SOURCE: https://github.com/moby/moby/pull/38148 (2018-11-06) + https://www.docker.com/blog/how-to-deploy-on-remote-docker-hosts-with-docker-compose/ → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: `gh hosts.yml` KONAK ADIYLA anahtarlanıyor ve konak içinde ikinci bir eksen taşıyor (`users:` haritası + aktif `user:` skaleri) → SOURCE: yerel `~/.config/gh/hosts.yml`, gh 2.98.0 → VERIFIED: 2026-09-02 okundu → TIER: deep
- CLAIM: `GH_HOST` resmî sözleşmesi git remote çıkarımını İÇERİYOR: *"where a hostname has not been provided, or cannot be inferred from the context of a local Git repository"* → SOURCE: https://cli.github.com/manual/gh_help_environment → VERIFIED: 2026-09-02 WebFetch → TIER: deep
- CLAIM: gh'nin **global `--host` bayrağı YOK** (yalnız birkaç komutta `--hostname`), ve bu yüzden `gh api` çıkarım kaynağı olmadığında TEK yapılandırılmış konağa değil github.com'a düşüyor → SOURCE: https://github.com/cli/cli/issues/1942 (maintainer yorumu) → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: gh'nin GHES'i başta reddetme sebebi YÖNLENDİRME değil YETENEK'ti: *"we use some internal preview GraphQL APIs… only available to us through our OAuth apps… not all commands would work out of the box"* → SOURCE: https://github.com/cli/cli/issues/273 → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: gh'nin bugünkü kapısı ÜÇ sonuç veriyor (destekleniyor / **bilinen-ama-desteklenmiyor** (GHES, ürünü ve konağı adlandıran mesaj) / bilinmeyen konak) → SOURCE: `cli/cli` `internal/skills/source/source.go:56-68`, `gh api` ile okundu + https://github.com/cli/cli/pull/13264 → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Yanlış konumlanmış bir yetenek kapısı, ortadan kalkmış bir yetersizliği iddia etmeye devam eder — `gh skills` GHES 3.19'da kontrol kaldırılınca ÇALIŞTI, mesaj ise GHES remote'unu "GitHub reposu değil" diye yanlış tanıttı → SOURCE: https://github.com/cli/cli/issues/13277 (2026-04-24) → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: AWS önceliği 10 katmanlı ve **credentials dosyası config dosyasını yener**; `--endpoint-url` profilden BAĞIMSIZ olarak ucu ezer (LocalStack/MinIO kolu) → SOURCE: https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-quickstart.html + /cli-configure-files.html → VERIFIED: 2026-09-02 WebFetch → TIER: deep
- CLAIM: `tea` (Gitea/Forgejo, self-host-önce ürün) çıkarım KAYNAĞINI da bayrak yapıyor: `--login` (hangi kurulum) · `--repo` · `--remote` → SOURCE: https://gitea.com/gitea/tea → VERIFIED: 2026-09-02 WebFetch → TIER: deep
- CLAIM: **Upbound `up`** — en sıkı yapısal analog — profile bir **TİP** koyuyor: `cloud` vs `disconnected`; *"authentication with a disconnected profile is optional, providing flexibility for air-gapped environments"*; öncelik `--profile` → `UP_PROFILE` → seçili profil; v0.37.0'da mevcut profiller `cloud` sayılarak devralındı → SOURCE: https://docs.upbound.io/manuals/cli/concepts/configuration/ → VERIFIED: 2026-09-02 WebFetch → TIER: deep
- CLAIM: clig.dev kanonik önceliği: **bayrak → kabuk env → proje-düzeyi config → kullanıcı-düzeyi config → sistem-düzeyi config** → SOURCE: https://clig.dev/ → VERIFIED: 2026-09-02 WebFetch → TIER: deep
- CLAIM: Context modelinin ÇEKİRDEK KUSURU — seçim paylaşılan bir dosyada global değişken durumdur, oysa iş birimi TERMİNAL'dir; `use-context` her açık kabuğu sessizce takip ettirir → SOURCE: https://kuryzhev.cloud/2026/08/23/kubectl-ran-on-the-wrong-cluster-fix-your-context-switching/ + https://dev.to/alitron/... (2026-07-14) → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Aynı sınıf Docker'da ENV üzerinden vurdu: `/etc/profile.d`'de unutulmuş global `DOCKER_HOST` yüzünden "yerel dev" sanılan `prune` ÜRETİM imajlarını sildi; önerilen düzeltme global env'i kaldırıp adlandırılmış context + prompt göstergesi + yıkıcı komutlarda zorunlu `--context` → SOURCE: https://cr0x.net/en/docker-context-manage-multiple-hosts/ (2025-12-20) → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Komutun okuduğu kaynaktan TÜRETİLMEYEN her gösterge eninde sonunda yalan söyler (kubectx global config'i günceller, prompt'un okuduğu ayrı marker'ı güncellemez) → SOURCE: https://serard.dev/content/blog/homelab-k8s/37-kubeconfig-juggling.html (2026-04-19) → VERIFIED: 2026-09-02 → TIER: deep

### Sentez — yinelenen altı yuva

1. **Adlandırılmış hedef girdisi** — uç nokta + kimlik + metadata'yı BİRLİKTE taşıyan demet (kubeconfig context, docker context, `hosts.yml` konak bloğu, aws profile, tea login, `up` profile). Ders: çıplak bir uç nokta string'i yetmez.
2. **Geçerli/varsayılan seçim** — tek skaler; yanlış-hedef kazalarının neredeyse tamamının kaynağı.
3. **Env ezmesi.**
4. **Çağrı-başına bayrak** — gh'nin global bayrağı olmaması tam da `gh api`'nin sessizce yanlış konağa gitmesinin sebebi.
5. **Dizin-başına çıkarım kaynağı** — git remote, `fly.toml`, proje config'i.
6. **Görünür gösterge** — `docker context ls`'teki `*`, `current-context`; prompt'a taşınması evrensel tavsiye.

**Öncelik sırası (kubectl/docker/aws/Upbound/clig.dev'de neredeyse oybirliği):**

> çağrı-başına bayrak → env değişkeni → dizin-başına çıkarım → saklanmış geçerli seçim → derlenmiş varsayılan

İki incelik: (a) bir DOSYA adlandıran bayrak (`--kubeconfig`) birleştirme makinesini tümüyle devre dışı bırakır, üstüne katman olmaz; (b) bir BİLEŞENİ ezen bayrak (`--server`, `--endpoint-url`) hedef seçildikten SONRA bağımsız çözülür — LocalStack/MinIO/self-host uçlarını profil icat etmeden erişilebilir kılan şey budur.

### Bu tasarım için en yük taşıyan iki bulgu

1. **gh'den:** İkinci bir konağa yönlendirmek KOLAY yarıdır; zor yarı bulut ile self-host'un aynı API yüzeyine sahip OLMAMASIDIR, ve **reddin ŞEKLİ tüm kullanıcı deneyimidir**. Kopyalanacak model: iki değil ÜÇ sonuç (destekleniyor / bilinen-ama-desteklenmiyor, ürünü ve konağı adlandırarak / bilinmeyen). Ve #13277'nin dersi: bir yetenek kapısı, onu doğuran yetersizlikten daha uzun yaşar — kapı gerçeğe karşı yeniden ölçülmelidir.
2. **Upbound'dan:** Dağıtımın **TİPİ** girdinin bir alanı olsun (`cloud` vs `disconnected`). Böylece özellik kapıları ve kimlik gevşetmesi, koda saçılmış koşullar değil, SEÇİLİ HEDEFİN özelliği olur; yükseltmede mevcut girdiler `cloud` sayılarak devralınır.

### Açık bilinmeyenler (bu açı)

- Docker'da `--host` ve `--context` birlikte verilince hata mı, belgelerde yazmıyor.
- gh'nin tam sıralı konak-çözüm fonksiyonu kaynaktan doğrulanmadı; git-remote çıkarımı doc metni + maintainer yorumuna dayanıyor.
- flyctl'in `--app`/`FLY_APP`/`fly.toml` çıkarımı doğrulanmadı.

---

## Açı 1 — Supabase CLI (kısmi; kuyruğu bekleniyor)

### Kanıt satırları

- CLAIM: `supabase link` YALNIZ bulut-şekilli bir proje ref'i alıyor: `ProjectRefPattern = ^[a-z]{20}$`; hata *"Invalid project ref format"* → SOURCE: https://github.com/supabase/cli/blob/develop/apps/cli-go/internal/utils/misc.go → VERIFIED: 2026-09-02 ham kaynak okundu → TIER: deep
- CLAIM: Ref çözüm sırası `--project-ref` → `SUPABASE_PROJECT_ID` → `supabase/.temp/project-ref` → **Management API'ye giden interaktif seçici**; hiçbir katmanda KONAK yok, `link`'te `--db-url` YOK → SOURCE: `apps/cli-go/internal/utils/flags/project_ref.go` → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: **`SUPABASE_API_URL` diye bir değişken YOK** — Supabase'in kendi iç dokümanlarında uydurulmuş, ~36 dosyada yayılmış ve repo içinde düzeltilmiş: *"No such env var exists in Go… The real override is `SUPABASE_PROFILE`."* → SOURCE: https://github.com/supabase/cli/commit/47e9a2c61b1e83b4d96e3407d973cb8b66c43575 → VERIFIED: 2026-09-02 `gh api` ile commit mesajı okundu → TIER: deep
  - **Yan ders:** DeepWiki aynı soruya `INTERNAL_API_HOST` cevabı verdi — BAYAT. Canlı kaynak çürüttü. (bkz. `feedback_a_documented_behavior_may_never_have_existed`)
- CLAIM: **Gerçek konak ezmesi `--profile` / `SUPABASE_PROFILE` ve bir ADI ya da bir CONFIG DOSYASI YOLUNU kabul ediyor**; alanlar: `api_url`, `dashboard_url`, `docs_url`, **`project_host`**, `pooler_host`, `client_id`, `studio_image`, `regions`; `UnmarshalExact` ile bilinmeyen anahtar reddediliyor → SOURCE: `apps/cli-go/cmd/root.go:343`, `apps/cli-go/internal/utils/profile.go` → VERIFIED: 2026-09-02 ham kaynak → TIER: deep
- CLAIM: Binary'ye DÖRT profil derlenmiş: `supabase`, `supabase-staging`, `supabase-local` (http://localhost:8080) ve bir **beyaz etiket müşterisi** `snap` (`api_url: https://cloudapi.snap.com`, `project_host: snapcloud.dev`, kendi `client_id`'si) → SOURCE: `profile.go` → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Seçili profil adı `~/.supabase/profile` düz metin dosyasında; **kimlikler profil adıyla anahtarlanıyor** (`credentials.StoreProvider.Get(CurrentProfile.Name)`) → SOURCE: `profile.go`, `access_token.go` → VERIFIED: 2026-09-02 → TIER: deep

### Bu tasarım için önemi

Supabase'in `profile` yapısı bizim `config.Endpoints`'imizin **birebir aynısı**:

| Supabase profil alanı | Bizim `Endpoints` alanı |
|---|---|
| `api_url` | `PlatformAPI` |
| `dashboard_url` | `Studio` |
| **`project_host`** | **`PublicHost`** ← bizde ezilemeyen tek alan |
| `client_id` | `auth.Config{ClientID: "palbase-cli"}` (bizde sabit) |

Yani sektör lideri, tam olarak bizim eksik bıraktığımız alanı profilin bir ALANI
yapmış ve profili hem derlenmiş bir ad hem de kullanıcının verdiği bir DOSYA
olarak çözüyor. Ayrıca `snap` profili kanıtlıyor ki bu mekanizma üretimde
beyaz-etiket bir dağıtımı taşıyor — teorik bir kaçış kapısı değil.

---

## Açı 2 — Appwrite · Directus · PocketBase

### En taze ve en yakın emsal: Directus `d6s` (PR #27861, **2026-08-17 merge** — iki hafta önce)

Directus'un yıllardır uzak-hedef CLI'ı YOKTU (`directus/cli` arşivlendi, son push
2024-01-22); `npx directus` tümüyle sunucu-yerel, Knex ile doğrudan DB'ye bağlanıyor.
Geçen ay tam da bizim sorduğumuz soruyu cevaplayan bir CLI çıkardılar:

- CLAIM: Konfig İKİYE bölünmüş — commit edilen `directus.config.json` → `profiles: { ad: { url, auth: { type: 'token' } } }`; kimlikler ayrı `~/.directus/credentials.json`, `[url][profileName]` ile anahtarlı, dizin 0700 / dosya 0600, atomik yazım → SOURCE: https://github.com/directus/directus/pull/27861, `packages/cli/src/kernel/config/{file,credentials}.ts` → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: **URL'nin sır taşıması YASAK** — `isSafeUrl` `user:pass@`, `?token=`, fragment, kontrol karakteri reddediyor; profil adlarında case-fold çakışması da reddediliyor (adlar case-insensitive store'lara düşüyor) → SOURCE: aynı → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Kimlik çözüm sırası `--token` → `DIRECTUS_<PROFILE>_TOKEN` → kayıtlı store, **ve store CI'da HİÇ okunmuyor** (`isCI()`); prefix'siz genel env fallback'i BİLİNÇLİ olarak yok — kod yorumu: *"No unprefixed fallback, so a credential can never be borrowed for a target the user did not mean to authenticate."* → SOURCE: aynı → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: **Gerçek yetenek müzakeresi, üç katman:** (a) `SYNC_MIN_DIRECTUS = '12.2.0'` tabanı — altındaysa `Environment Sync needs Directus 12.2.0 or later; "<profile>" runs <version>` ile ret, ama **parse edilemeyen sürüm hiçbir şeyi kapatmıyor** (tahmin üzerine ret yok); (b) `assertAdminAccess` ayrı kapı — çünkü yetkisiz okuma HATA VERMEDEN eksik dosya üretir; (c) şema apply sunucunun diff hash'ini replay ediyor, sürüm sapması `--allow-version-drift` + gürültülü uyarı istiyor → SOURCE: `packages/cli/src/commands/sync/utils/preflight.ts` → VERIFIED: 2026-09-02 → TIER: deep
  - **Bizim kalıbımızla aynı:** (b) tam olarak `feedback_a_placeholder_is_a_lie...` — sessizce inceltilmiş okuma bir yalandır.
  - Not: bu sürüm-tabanı preflight'ı PR review'ında *"bunu bir şey kontrol ediyor mu?"* sorulduktan SONRA eklenmiş.

### Appwrite — çok-oturumlu uzak CLI (Go implementasyonu yayında, npm 27.3.0)

- CLAIM: `~/.appwrite/prefs.json`'da üst düzeydeki **her bilinmeyen anahtar bir session'dır** (`ignoredAttributes` ayrıştırıyor); session kaydı ID + Endpoint + Email + accessToken/refreshToken/tokenExpiry/clientID/cookie/key taşıyor → SOURCE: `internal/config/global.go`, `internal/cmd/session.go` → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: **ÇALINACAK EMNİYET KİLİDİ** — çözümlenen endpoint mevcut oturumun endpoint'iyle eşleşmezse komut ÇALIŞMIYOR: `"endpoint %s does not match the current login session endpoint %s"` → SOURCE: `internal/cmd/pull.go:171`, `internal/sdk/sdk.go:178` → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Bulut vs self-hosted **aynı komutlar, farklı auth** — bulut OAuth device flow, self-hosted email+parola(+MFA); bölgesel bulut host'ları normalize ediliyor, **self-hosted endpoint'ler dokunulmadan geçiyor** → SOURCE: `internal/auth/device.go`, `internal/config/config_test.go` → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: `--self-signed` bayrağı **aynı invocation'da saklı değeri EZİYOR** — yoksa self-signed sertifikalı birinin İLK komutu hiç geçemezdi; negatif kontrolü testte (`internal/client/selfsigned_test.go`) → SOURCE: aynı → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: prefs 0700/0600 + atomik — bu doğrudan **CVE-2023-50974**'ün tamiri (3.0.0 öncesi 0644, makinedeki herkes okuyabiliyordu) → SOURCE: https://github.com/trickest/cve/blob/main/2023/CVE-2023-50974.md → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Endpoint doğrulaması `GET /health/version` çağırıp alanın DOLU olmasına bakıyor — **sürüm karşılaştırmıyor**; bu "Appwrite mı?" yoklaması, yetenek müzakeresi DEĞİL → SOURCE: `internal/cmd/endpoint.go` → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Appwrite'ın çok-oturum yüzeyi (`sessions`/`whoami`/`login --switch|--new`) **resmî komut referansında hiç geçmiyor** — doc↔kod drift'i → SOURCE: https://appwrite.io/docs/tooling/command-line/commands → VERIFIED: 2026-09-02 → TIER: deep

### PocketBase — bilinçli RED

- CLAIM: PocketBase CLI'ında uzak hedef kavramı YOK (ne endpoint, ne token, ne config); maintainer `pocketbase login --url` + `migrate push` isteğini **12 dakikada kapattı**: *"This is out of the scope. PocketBase migrations are code… you'll need a restart/recompile step anyway."* → SOURCE: https://github.com/pocketbase/pocketbase/issues/7788 (2026-08-03) → VERIFIED: 2026-09-02 → TIER: deep
  - Bize UYGULANAMAZ (bizim CLI uzak instance'la konuşmak zorunda), ama kanıtladığı şey: kullanıcılar bunu İSTİYOR.

### Supabase — kritik asimetri

- CLAIM: **Self-hosted Supabase Management API'yi HİÇ göndermiyor**; docs: self-hosted'da *"branching, advanced metrics…, and the platform management API"* yok, ve *"Supabase CLI runs a local stack for development and testing. That stack is not a self-hosted deployment."* → SOURCE: https://supabase.com/docs/guides/self-hosting → VERIFIED: 2026-09-02 → TIER: deep
  - **Bizim konumumuz DAHA İYİ:** bizim çekirdeğimiz 72 işlemlik tam yönetim yüzeyini GÖNDERİYOR. Supabase'in `--profile`'ı konuşacak bir şey bulamazken bizimki bulur.
- CLAIM: `supabase config push` — `config.toml`'u bir dağıtım artefaktı yapacak tek komut — **yalnız bulut**, `--db-url` kabul etmiyor; tasarımdaki en keskin asimetri → SOURCE: https://supabase.com/docs/reference/cli/supabase-config-push → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: `[remotes.<name>]` genel bir çok-hedef mekanizması DEĞİL — bulut branch project-ref'lerine bağlı (DeepWiki'nin "named contexts" çerçevelemesi YANILTICI) → SOURCE: https://supabase.com/docs/guides/local-development/cli/config → VERIFIED: 2026-09-02 → TIER: deep

---

## Açı 6 — kimlik modeli (self-host'ta admin CLI)

- CLAIM: İKİ aile var. (1) *Kimliksiz, süresiz, tek tek iptal edilemez paylaşılan sır*: Hasura `HASURA_GRAPHQL_ADMIN_SECRET`; Convex admin key — Rust kaynağında **her zaman `MemberId(0)`** için üretiliyor (`broker.issue_admin_key(MemberId(0))`). (2) *Sunucunun ürettiği, kapsamlı, tek tek iptal edilebilir*: Grafana service account token, Appwrite console API key, Gitea scoped PAT, Supabase yeni `sb_secret_*` → SOURCE: https://github.com/get-convex/convex-backend `crates/local_backend/src/main.rs`; https://hasura.io/docs/2.0/deployment/securing-graphql-endpoint/; https://grafana.com/docs/grafana/latest/administration/service-accounts/ → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Eksik/zayıf Hasura admin secret'ı ticari tarayıcılarda **CVSS 9.8** kayıt — anonim `POST /v1/metadata {"type":"export_metadata"}` 200 dönüyorsa kanıtlanmış → SOURCE: https://www.invicti.com/web-application-vulnerabilities/hasura-graphql-api-without-authentication + Acunetix → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Supabase `service_role` JWT'den tek tek döndürülebilir `sb_secret_*`'a geçiş, **sektörün bu hatayı alenen düzeltmesi**: eski rotasyon `JWT_SECRET` değişimi gerektiriyordu ve tüm kullanıcı oturumlarını düşürüyordu → SOURCE: https://supabase.com/docs/guides/api/api-keys → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: AWS: *"Access keys… stored in the shared AWS credentials file are stored in plaintext"* ve (2025-11-21) *"Exposed long-term credentials continue to be the top entry point used by threat actors in security incidents observed by the AWS CIRT."* → SOURCE: https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_access-keys.html + AWS Security Blog → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: **Ölümcül üçlü (Temmuz 2025, Supabase MCP):** bir destek talebinin GÖVDESİNDE yazan `SELECT * FROM integration_tokens` ajana koşturuldu ve sonuç herkese açık konuya geri yazıldı; Supabase yanıtı `--read-only` varsayılanı + en-az-yetki kılavuzu → SOURCE: https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/ → VERIFIED: 2026-09-02 → TIER: deep
  - **Bize doğrudan:** CLI kimliği aynı zamanda bir AJAN kimliğiyse bu kusuru miras alır.
- CLAIM: Self-host sunucusu kendi device-code/PKCE ucunu koşabilir — Keycloak `/realms/{realm}/protocol/openid-connect/auth/device` gönderiyor; Grafana'nın kendi CLI'ı `config set-instance <ad> --url` + `auth` ile KENDİ instance'ına karşı PKCE tarayıcı akışı yapıyor → SOURCE: https://www.keycloak.org/securing-apps/oidc-layers; https://github.com/grafana/assistant-cli/blob/main/docs/SETUP.md → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Yön ayrışıyor ve ikisi de bilgilendirici: AWS CLI 2.22.0'dan beri **PKCE varsayılan**, device code `--use-device-code`'a indirildi; Vercel ise tüm CLI login'ini **RFC 8628 device flow'a** standartlaştırdı → SOURCE: https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sso.html; https://vercel.com/changelog/new-vercel-cli-login-flow → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: `gh` keyring-önce + düz metin fallback, ve bu fallback en büyük açık sürtünme alanı — SIX açık issue (#10108, #8954, #13872, #13317 *"silently sends unauthenticated requests"*, #12885, #14171 *"misreports an inaccessible keyring as an invalid token"*); maintainer: *"We've had so many issues of people complaining about gh falling back to insecure storage, not being particularly transparent about doing that"* → SOURCE: `gh search issues --repo cli/cli`, 2026-09-02 → VERIFIED: 2026-09-02 → TIER: deep
  - **Ders "keyring kullan" DEĞİL:** erişilemeyen bir keyring **gürültülü ve AYIRT EDİLEBİLİR** bir hata olmalı; "keyring yok" ile "kimlik geçersiz" ayrı çıkış yolları.
- CLAIM: `gh` kimliği KONAK ADIYLA anahtarlıyor (keyring servis adları literal `gh:github.com` / `gh:enterprise.com`), ve kendi dokümanı şunu kaydediyor: *"the `hosts.yml` file only supported a mapping of one-to-one in the host to account relationship"* — çoklu hesap için veri göçü çıkarmak zorunda kaldılar → SOURCE: cli/cli `internal/config/migration/multi_account_test.go`, `docs/multiple-accounts.md` → VERIFIED: 2026-09-02 → TIER: deep
  - **İlk günden (konak, hesap) ikilisiyle anahtarla.**
- CLAIM: `gh`'nin env kolu tek değişkenle "hangi hedef" diyemediği için self-hosted çıkınca İKİNCİ bir değişken gerekti: `GH_TOKEN`/`GITHUB_TOKEN` github.com için, `GH_ENTERPRISE_TOKEN`/`GITHUB_ENTERPRISE_TOKEN` GHES için. Grafana bunu `GRAFANA_URL` + `GRAFANA_SA_TOKEN` çiftiyle aşıyor — **URL token'la birlikte seyahat ediyor** → SOURCE: https://cli.github.com/manual/gh_help_environment → VERIFIED: 2026-09-02 → TIER: deep

---

## Açı 5 — dürüst bozulma ve yetenek keşfi (kısmi)

- CLAIM: RFC 8414 yeteneği **dizinin YOKLUĞUYLA** ifade eder: *"Claims with zero elements MUST be omitted"*; `code_challenge_methods_supported` yoksa PKCE desteklenmiyor demek → SOURCE: https://www.rfc-editor.org/rfc/rfc8414.html → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Matrix `/_matrix/client/versions` sözleşmesi: *"Features not listed here, or the lack of this property all together, indicate that a feature is not supported"* — **yokluk = desteklenmiyor, sözleşme bu**; ayrıca sunucu bir özelliği yalnız bazı kullanıcılara açabileceği için istemci **kimlik doğrulayarak** sormalı → SOURCE: https://spec.matrix.org/latest/client-server-api/#get_matrixclientversions → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Docker Registry `GET /v2/` bir **sürüm el sıkışmasıdır**, veri ucu değil: `Docker-Distribution-API-Version: registry/2.0` başlığı yoksa istemci status ne derse desin V2 varsaymamalı → SOURCE: https://distribution.github.io/distribution/spec/api/ → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: Kubernetes aggregated discovery tek istekte grup/sürüm/kaynak/scope/**desteklenen fiiller**i veriyor; `--runtime-config=batch/v1=false` ile grup kapatılabildiği için **aynı binary'nin farklı kümede farklı yüzey görmesi tasarımın kendisi** → SOURCE: https://kubernetes.io/docs/concepts/overview/kubernetes-api/#api-discovery → VERIFIED: 2026-09-02 → TIER: deep
- CLAIM: **`gh` GİZLEMEZ, çalışma anında ADLANDIRARAK reddeder:** `"An unsupported host was detected. Note that gh attestation does not currently support GHES"`; kapı `auth.IsEnterprise(host)` → SOURCE: cli/cli `pkg/cmd/attestation/auth/host.go` → VERIFIED: 2026-09-02 kaynak grep → TIER: deep
- CLAIM: **`gh`'nin testi mesaj SIRASINI savunuyor** — `internal/attachments/client_test.go:409` yorumu birebir: *"This row defends a message, not an order. On an enterprise server the token message would tell the user to re-authenticate, and that remedy does not work there."* → SOURCE: aynı repo → VERIFIED: 2026-09-02 → TIER: deep
  - **Kural:** konak/dağıtım kısıtı, KİMLİK hatasından ÖNCE bildirilmeli — çünkü kimlik hatasının çaresi o hedefte yanlış tavsiyedir.
- CLAIM: `gh` sürüm bazlı kapı da koyuyor: GHES sürümünü `/meta` ucundaki `installed_version` ile çözüyor (`resolveEnterpriseVersion`) ve örn. gelişmiş issue aramayı GHES ≥ 3.18.0'a kapılıyor; **`cmd.Hidden`'ı konağa göre set eden bir yer bulunamadı** → SOURCE: cli/cli `internal/featuredetection` (DeepWiki, iki sorgu) → VERIFIED: 2026-09-02 → TIER: deep
  - BİLİNMEYEN: `X-GitHub-Enterprise-Version` başlığının varlığı doğrulanamadı; `gh` onu değil `/meta`'yı kullanıyor.
