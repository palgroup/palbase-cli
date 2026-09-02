# CLI'ın self-host denkliği — tasarım

Tarih: 2026-09-02 · Karar günlüğü: `decisions.md` · Araştırma:
`sdk/cli/docs/paltimate/research/2026-09-02-cli-self-host-address-model.md`

## 1. Problem

V2 core self-host edilebiliyor. CLI'dan self-host bir kuruluma bağlanmak
kısmen çalışıyor, ama kullanıcı "bağlandıktan sonra CLI konfigürasyonunu
kullanamıyorum, farklı şeyler yapamıyorum" diyor.

**Ölçüm bunu üç ayrı olguya ayırdı:**

**(1) Yığın-bağlı yarı ZATEN ÇALIŞIYOR — ölçüldü.** `v2/.verify/cli/`, 2026-08-31
19:55-20:04 tarihli canlı bir koşum kaydı taşıyor. Sandbox HOME'da tek bir şey
var: `https://127.0.0.1` için `kind=key` bir service_role anahtarı — `config.json`
yok, bulut oturumu yok, seçim yok. Bu koşuda `link`, `build`, `plan`, `push`,
`secret set/list`, `spec`, `status`, `deploys`, `rollback` çalıştı. `push` 118 KB
gönderdi ve yığın **ürün düzeyinde** (`schema_incompatible`) reddetti; `rollback`
gerçekten mutasyon yaptı (`151f201b1f1e is active, serving 37 endpoint(s)`).

**(2) Kalıcı konfigürasyon YOK ve adres kümesi eksik ezilebilir.**
`config.File` boş bir struct (`internal/config/config.go:59`). Üç env ezmesi var,
ama `PublicHost` **hiç ezilemiyor**. Ölçüldü:

```
$ PALBASE_STUDIO_URL=… PALBASE_AUTH_URL=… PALBASE_PLATFORM_URL=… palbase endpoints
Projects:    <ref>.palbase.studio        ← DEĞİŞMEDİ
```

Yayınlanmış docs bunu zaten itiraf ediyor (`cli/overview.md:80`): *"The fourth
cannot: a project's host has no environment override."*

**(3) Başarısızlıkların ŞEKLİ tutarsız.** Bazıları dürüst (`members`, `logs`,
`apikey reveal`), bazıları sessiz ya da yanıltıcı: `clone` bulut oturumu yokken
reddetmeyip `https://<ref>.palbase.studio` adresi kuruyor; `doctor` çalışan bir
self-host kurulumuna ✗ *"not logged in"* diyor; `endpoints` var olmayan bir adres
alanı basıyor; `open` çözüm hatasını yutup çıplak kökü açıyor.

## 2. Kapsam

### İÇERİDE
- Adlandırılmış **dağıtım** kavramı ve kalıcı CLI konfigürasyonu (D-004)
- Sessiz/yanıltıcı kırıkları dürüst kılmak (D-002): `clone`, `open`, `doctor`, `endpoints`
- Gereksiz kırığı düzeltmek: `flags user *`
- `palbase login`'i self-host dağıtımına karşı çalıştırmak (D-005, D-006)

### DIŞARIDA — ve bu bir eksiklik değil, çekirdeğin YAZILI sınırı
`v2/api/management.openapi.yaml:17-22`:

> *Nothing about accounts, members, invitations, projects, branches, tiers, quotas
> or billing: those are concepts of a control plane that a project you run does not
> have **and must never learn**. A command that touches MORE THAN ONE tenant belongs
> to the cloud; this document is the surface of ONE stack.*

Doğrulandı: spec'te 72 işlem, `gen.go`'da 72 kayıt, kümeler birebir. `/v1/cloud`
v2'de yalnız *ayrılmış ön ek* olarak geçiyor.

Dolayısıyla `project`, `members`, `apikey reveal/rotate`, `clone`, `ios/android use`
**bulut kavramıdır ve öyle kalır**. Ayrıca `logs`: çekirdekte log işlemi yok ve
yokluğu beyan edilmiş (`v2/deploy/cli_harness.sh:28-30` — *"which is its own
design work"*).

## 3. Tasarım

### 3.1 Dağıtım modeli (D-004)

`~/.palbase/config.json` bugün boş; adlandırılmış dağıtımlar taşır:

```json
{
  "current": "acme",
  "deployments": {
    "palbase": { "kind": "cloud",
      "studio": "https://palbase.studio",
      "auth": "https://api.palbase.studio",
      "platform": "https://api.palbase.studio",
      "publicHost": "palbase.studio" },
    "acme": { "kind": "selfhost",
      "studio": null, "auth": "https://palbase.acme.com",
      "platform": null, "publicHost": null }
  }
}
```

`kind` alanı Upbound `up`'ın `cloud`/`disconnected` profil tipinden alınmıştır:
yetenek kapıları ve kimlik gevşetmesi **seçili hedefin özelliği** olur, koda
saçılmış koşullar değil. Mevcut kurulumlar `palbase` (cloud) sayılarak devralınır.

**Çözüm sırası** (kubectl/docker/aws/Upbound/clig.dev'de oybirliği):

```
--deployment bayrağı
  → PALBASE_* env
    → .palbase/project.json (LINK — varsa HER ZAMAN kazanır)
      → current
        → derlenmiş varsayılan (palbase)
```

**Linkin `current`'ı yenmesi kasıtlı bir güvenlik kararıdır.** Bu modelin
belgelenmiş ana riski yanlış hedefe iş yapmaktır ve kaynağı global seçim
durumudur (kubectl `use-context` her açık kabuğu sessizce takip ettirir; unutulmuş
bir global `DOCKER_HOST` üretim imajlarını sildirdi). Linkli bir checkout'ta
global seçim hiç okunmaz, dolayısıyla kaza sınıfı orada yapısal olarak yok olur.
`current` yalnız link'siz verb'ler (`login`, `project`, `clone`) için devreye girer.

**Kimlikler config'e GİRMEZ.** Bugünkü `~/.palbase/credentials.json` (0600, adresle
anahtarlı) kalır. Directus `d6s`'in kuralı: konfig dosyası sır taşımaz.

**Gösterge, komutun okuduğu kaynaktan TÜRETİLİR.** Ayrı bir "aktif hedef" işareti
tutmak, prompt'un yalan söylediği belgelenmiş bir tuzaktır.

### 3.2 Reddin şekli (D-002)

Komutlar **gizlenmez**. Bu bir tercih değil, `cmd/palbase/surface_test.go:387`'deki
FR-037 kapısının şartı: *"the surface is the SAME on a self-hosted stack… There is
no command that exists only for one of them and no flag that turns one off."*

Üç sonuç, iki değil (gh modeli): **destekleniyor** / **bilinen-ama-bu-dağıtımda-yok**
(ürünü ve adresi adlandırarak) / **bilinmeyen adres**.

**Sıra kuralı — gh'nin kendi testinden:** dağıtım kısıtı **kimlik hatasından ÖNCE**
bildirilir, çünkü kimlik hatasının çaresi o hedefte yanlış tavsiyedir. Bugünkü
`doctor` tam bu hatayı yapıyor.

### 3.3 `login` (D-005, D-006)

Ölçülen kısıt: çekirdek `/auth`, `/oauth`, `/admin`'i `Verify` grubunun içine
mount ediyor; kimliksiz istek 401 `missing_authorization` alıyor. Anon key
`apikey` olarak sunulursa `RoleAnon` üretiliyor ve 2./4. ayaklar geçiyor. Ama
anon key'i verecek pre-auth rota YOK — `wellknown.go` publishable key'i kasten
bıraktı.

**Çözüm:** operatör anahtarı zaten elinde tutuyor — `.env`'de duruyor ve
`deploy/up.sh:153` açılışta ekrana basıyor (`publishable key : …`).

Dolayısıyla:
- Dağıtım kaydında anon key bir kez verilir, `credentials.json`'a (0600) yazılır.
- `login`'in **2/3/4. ayakları hiç değişmez.**
- Yalnız 1. ayak (`GET /v1/cloud/config`) `kind: selfhost`'ta atlanır:
  `anonKey` kayıttan · `issuer` çekirdeğin kendi
  `/.well-known/openid-configuration`'ından (anonim, `Verify` dışında) ·
  `tenantDomain` yok.

Çekirdek işin geri kalanını zaten yapmış: `palbase-cli` her kuruluma **public
PKCE istemcisi** olarak ekiliyor, redirect port'ları CLI'ınkiyle kilit adımda,
grant'ler `authorization_code` + `refresh_token`, scope'lar `offline_access`
dâhil (`bootstrap_handler.go:38-60`). Yönetim yüzeyi `Bearer`+management iddiasını
zaten kabul ediyor ve **kişiyi anahtara tercih ediyor** (`management/auth.go:96-105`).

**Tarihçe bu seçimi bağımsız doğruluyor.** Yığın-doğrudan parola girişi denendi ve
emekliye ayrıldı (`stack_login.go:4-14`): token 1800 saniye yaşıyordu, refresh
atılıyordu, `push` yarım saat sonra düşüyordu. Seçilen yolda o kusur yapısal
olarak yok.

## 4. Etki haritası

`PublicHost`'un 16 okuyucusu denetlendi. `publicHost: null` altında:

| Davranış | Okuyucular | İş |
|---|---|---|
| Zaten doğru | `tenantRefOf` (×4), `selectedProjectTarget`, bare-ref çözümü, `apps.go` guard | **yok** |
| Geçersiz adres kuruyor | `deploy.go:367,624`, `backend.go:559` | dürüst red |
| Yanlış/boş rapor | `main.go:561` (`endpoints`), `doctor.go:162` | dağıtımı doğru yaz |
| Bulut `use` yolu | `ios_use.go`, `native_link.go`, `web_link.go`, `pull_spec.go` | dürüst red |

Ayrıca: `config` paketi (dağıtım modeli), `auth` paketi (1. ayak), `flags/user.go`
(gereksiz seçim çözümü), `logs`/`members`/`apikey` (mesaj sırası).

## 5. İzlenebilirlik

| Problem cephesi | Tasarım öğesi |
|---|---|
| (1) yığın yarısı çalışıyor | — doğrulandı, iş yok |
| (2) kalıcı konfig yok | §3.1 dağıtım modeli |
| (2) `PublicHost` ezilemiyor | §3.1 + §4 |
| (3) sessiz/yanıltıcı red | §3.2 + §4 |
| bulut-özel verb'ler | §2 DIŞARIDA + §3.2 dürüst red |
| self-host kimliği statik sır | §3.3 |

Eşlenmeyen tasarım öğesi yok; eşlenmeyen problem cephesi yok.

## 6. Riskler ve açık uçlar

- **R-1 — Yanlış hedefe iş yapma.** Azaltma: linkin `current`'ı yenmesi;
  hedefin her komutta yazılması; göstergenin komutun okuduğu kaynaktan türetilmesi.
  Kalan risk: link'siz verb'lerde `current` hâlâ global durumdur.
- **R-2 — Yetenek kapısı kendi gerekçesinden uzun yaşar.** gh #13277'de tam bu
  oldu. Azaltma: kapının gerekçesi kodda adlandırılır ve emekliliğini kendi ister
  (`feedback_a_gates_exception_must_retire_itself`).
- **R-3 — Anon key'in konumu.** `credentials.json`'a yazılıyor; `config.json`'a
  sızarsa Directus'un yasakladığı şey olur. Kapıyla korunmalı.
- **AÇIK (kapsam dışı, kaydedildi):** çekirdeğin kendi `PublicHost`'u
  yapılandırılmamışsa **bizim `dev.palbase.studio`** adresimize düşüyor —
  `v2/internal/modules/messaging/internal/storage/presigner.go:120-121` ve
  `internal/pushwake/bridge.go:100-101`. Self-host eden biri için gerçek bir kusur.
