# Palbase CLI — tam yüzey denetimi (deep research, 6 kör açı)

**Tarih:** 2026-09-02 · **Ağaç:** `sdk/cli` HEAD `0b41aaf` == tag `v0.52.0` == brew'daki binary
**Tier:** deep (6 açı, her biri ayrı ajan, birbirine kör) · **Kapsam:** komut yüzeyi · link/platform durum makinesi · modül namespace'leri · `palbase start` doğruluğu · mühendislik sağlığı · onboarding/kimlik

**Ham raporlar:** `findings-{A,B,C,D,E,F}*.md` (oturum scratchpad'i). Toplam **122 ham bulgu**, çapraz tekrarlar ayıklandığında ~105 ayrık.

---

## Karar raporu

> **Karar:** CLI'ın üç yapısal borcu var ve üçü de aynı kökten geliyor — *ölçmeyen kapı*. Yayındaki 0.52.0'da `palbase start`/`stop` hiç çalışmıyor, `palbase init` sonrası `palbase build` düşüyor, ve `--project/--environment` hiçbir fiilde iş görmüyor; üçü de yeşil kapıların altından geçti.
>
> **Ölçülen seçenekler:** yamalama (tek tek 122 bulgu) · yeniden yazım · **kapı-önce onarım + üç kavramsal sadeleştirme** (seçilen).
>
> **Neden:** Bulguların ~%40'ı tek bir sınıf — *bir zamanlar doğruydu, artık değil, kimse ölçmüyor*. Tek tek yamamak aynı sınıfı yarın yeniden üretir. Kapılar ölçmeye başlarsa geri kalanın çoğu mekanik.
>
> **Varsayımlar:** `@palbase/backend@26.0.0`'ın yayımlanması sevkiyat kararı (bu denetimin dışında); `PALBASE_OPERATOR_IDS` ve filo terfisi başka oturumun lane'i.

---

## 1. ÜÇ P0 — yayındaki 0.52.0'da, bugün

### P0-1 · `palbase start` ve `palbase stop` hiç çalışmıyor

Vendor'lanan compose geçersiz. **Bağımsız negatif kontrolle doğrulandı (team-lead):**

```
$ docker compose -f internal/backend/stackfiles/docker-compose.dev.yml config --services
service "barman" refers to undefined volume barmandata: invalid compose project
$ strings /opt/homebrew/bin/palbase | grep -c 'context: ./barman'   → 1
$ /opt/homebrew/bin/palbase --version                                → 0.52.0
```

`stackfiles/docker-compose.dev.yml:182` `barman:` servisi duruyor, `:208` `barmandata:` volume'ünü bağlıyor, ama `volumes:` bloğunda o anahtar **yok** (yorumu öksüz kalmış).

**Kök neden — `stackfiles_test.go:71-85 withoutBarman`:** `  barman:` satırını buluyor, sonra **yorum bloğunun başına geri yürüyor** (`start--`), ardından blok sonunu `start+1`'den arıyor. `topLevelKey` yorum satırlarını atladığı için ilk eşleşen üst-düzey anahtar `  barman:`'ın **kendisi** oluyor → `lines[:start] + lines[end:]` = *yorum silindi, servis kaldı*. Sonraki string replace `  barmandata:\n` volume anahtarını da siliyor. Sonuç: servis var, volume yok.

**Kapı neden yeşil:** karşılaştırmanın **iki tarafı da** aynı bozuk `withoutBarman`'dan geçiyor (`stackfiles_test.go:42-50`) — beklenen de üretilen de aynı şekilde yanlış. `docker compose config` negatif kontrolü hiç koşulmamış.
→ Defter kuralı: [[feedback_a_regex_gate_does_not_measure_what_the_stager_rejects]].

**Ağırlaştırıcı (D-F8):** `stop` compose dosyasını her koşuda **yeniden yazıyor** (`stackfiles.go:41-51`, `start.go:293`) ve `local.json` + register kaydını **sildikten sonra** düşüyor (`start.go:311-320`). Sağlam dosyayla ayakta duran bir stack (örn. sahibin `centauri-local`'i, dosyası 18:05 build'inden) 0.52.0 ile bir `stop` denemesinde hem bozuk dosyayla üzerine yazılıyor hem de konteynerler CLI'dan kopuyor.

**Fix:** `withoutBarman`'da blok sonunu **orijinal** `  barman:` satırından ara; `docker compose config` koşan bir test ekle (docker yoksa skip); yeniden vendor'la; 0.52.1 kes.

### P0-2 · `palbase init` → `palbase build` düşüyor (iki açı bağımsız buldu: D-F3, F-F1)

`init.go:51-55` npm'in en yenisini kuruyor → `@palbase/backend@25.1.0`, şablonunda `*.module.ts` yok.
`devjs/build-check.js:611-622` `*.module.ts` şart koşuyor → `DEPLOY WOULD FAIL`.
Modüllü şablon yalnız **yayımlanmamış** 26.0.0'da (`sdk/palbase-ts/backend`).
Hatanın tavsiyesi: *"run `palbase init` in an empty directory to get one"* — **döngüsel**.
Yayındaki brew binary'siyle yeniden üretildi.

> ⚠️ Bu dosya şu anda **başka bir oturum tarafından düzenleniyor** (`cli-self-host-denkligi` koşusu; `build.go`, `devjs/build-check.js`, `stack_bundle.go` WIP). Bu kalem muhtemelen orada kapanıyor — çakışmamak için dokunulmadı.

### P0-3 · Sessizce hiç çalışmayan iki modül fiili

- **`flags list` her zaman ham JSON basıyor** (C-F01, team-lead doğruladı): `flags.go:223` çıplak `[]struct` çözüyor, v2 `{"flags":[…]}` zarfı dönüyor (`v2/internal/management/operate.go:112`). Unmarshal düşüyor → "ham gövdeyi bas" dalına giriyor. Canlı: `{"flags":[]}`. `this stack declares no flags` yolu **erişilemez**. Fikstür de çıplak dizi (`flags_test.go:129`) — test yanlışı doğruluyor. Aynı sınıf hata `storage`'da ölçülüp düzeltilmiş, `flags`'e uygulanmamış.
- **`notifications remove <provider>` hiçbir zaman silemiyor** (C-F03): CLI sağlayıcı **adını** yolluyor (`notifications.go:394`), rota config **ID**'si istiyor (`v2 repository/provider.go:106` `WHERE id = $1`) → her zaman 404. Test yanlış yolu doğruluyor.

### P0-4 · Android istemcisi üretilemez (B-1)

Gradle eklentisi (`GeneratePalbaseTask.kt:95-108`, `PalbaseCodegenPlugin.kt:11`) **düz** `{app_id,base_url,api_key}` + `.palbase/openapi.json` bekliyor. CLI çok-ortam belge yazıyor ve bulut yolu `.palbase/openapi.json`'ı **siliyor** (`cloud_environments.go:221`). Üstelik eklenti `https` şart koşuyor → yerel yığın hiç çalışamaz.

---

## 2. Sahibin üç sorusunun cevabı

### S1: `palbase start` o kodun doğru stack'ini mi kaldırıyor? → **HAYIR**

`start` projenin koduna, `package.json`'ına, kurulu `@palbase/backend`'ine ya da bulutun pinine **hiç bakmıyor**. Binary'ye gömülü üç sabiti kaldırıyor (`start.go:65-77`):

```go
{"PALBASE_PALSVC_IMAGE",  "ghcr.io/palgroup/palbase/palsvc:0.40.0"},
{"PALBASE_RUNTIME_IMAGE", "ghcr.io/palgroup/palbase/runtime-dev:0.40.0"},
{"PALBASE_EDGE_IMAGE",    "ghcr.io/palgroup/palbase/edge:0.40.0"},
```

`grep -rn "sdk-pins\|generation\|version.env" internal/backend/start*.go` → **0**. Projeden gelen tek girdi: dizin (bind-mount) ve basename (compose proje adı). Seçtiği imajı **yazmıyor** bile.

Bugünkü somut sonuç: `init` SDK 25 veriyor (rung 2, modülsüz), `start` SDK **26.0.0** vendor'layan imajı kaldırıyor → runtime bundler reddediyor. Ama `waitForStack` yalnız palsvc'nin `/readyz`'ini yokluyor (`start.go:583-616`), o yüzden banner *"edit a controller and the running stack serves it"* diyor (D-F4).

Ters yönde (D-F5): `push` projenin SDK'sını kiracının vendor'ladığıyla **değiştiriyor** (`stack_sdk.go:39-86`). Aynı checkout yerelde ve bulutta **farklı SDK'lara** derleniyor. Docs *"the same bundler"* diyor (`docs/cli/local-stack.md:3`) — değil.

Ayrıca `.well-known`'daki `sdk_version` projeninkini değil **imajınkini** anlatıyor (`server.ts:1253 RUNTIME_SDK_VERSION`), ve `push` kararını ona dayandırıyor.

### S2: `ios`/`web` adına gerek var mı? → **YOK — ve fiilen zaten yok**

`.palbase/project.json` var olduğu an `ios|macos|android|web link`'in **hepsi** `linkToBoundProject` (`platform_link_target.go:30`) ile `runLink`'e iniyor. Bulut yolu (uygulama kaydı, OAuth, `kind`, `--json`, next-steps) yalnız project.json **yokken** çalışıyor — ve CLI'ın kendi tavsiyesi (`palbase link <project>` → `ios link`) oraya **hiç ulaşmıyor**; `app_id` `"project"` sabitinde kalıyor.

Örtüşme ölçüldü: `runLink` yazma fazı ↔ `writeNativeEnvironments` aynı 6 adım; `gatherEnvironments` yerel bloğu ↔ `addLocalStack` **%95 birebir**; `resolveNativeApp` ↔ `resolveWebApp` 35 satırın 30'u aynı; `newNativeUseCmd` ↔ `newWebUseCmd` prologu 30 satır aynı.

**Önerilen:** platform algılamalı tek `palbase link [target] [--platform auto]` (algılama malzemesi hazır: `planes.go:125 hasApple`, `:146 hasWeb`, `detectAndroidApplicationID`) + web'in 7 kurulum adımı link sonrasına + yeni `unlink`; `use` düşer (bağlı checkout'ta zaten reddediliyor, bulutta `link <ref>` aynı iş).

### S3: Modül namespace'lerinde sorun var mı? → **24 bulgu**

P0'lar yukarıda. Diğer ağırlar:
- **`--project/--environment` hiçbir fiilde iş görmüyor** (C-F04 + A-4 + A-6): dayandığı `GET /api/v2/projects` sunucuda **yok** (doğrulandı: `cli.controller.ts`'te liste rotası yok, `panel.controller.ts:105`'te başka önek altında). `resolve.go:192` hatayı yutup `given`'ı döndürüyor. 15+ komut etkileniyor; `flags list --project x` "not linked" diyor, `deploys --project x` gerçek nedeni söylüyor — aynı bayrak, iki davranış.
- **`storage add` mevcut bucket'ı sessizce sıfırlıyor** (C-F07): tam PUT, `--public/--mime/--variant` kayboluyor. `auth settings set` aynı sorunu read-merge-write ile çözmüş.
- **`flags user *` bağlı checkout'ta kilitleniyor** (C-F02): REST'ten önce plane seçimi çözüyor → "no project selected", ama link varken o bayraklar reddediliyor. Self-host paritesi kırık.
- **Bilinmeyen alt komut 10 grupta exit 0** (C-F08): `palbase secret remvoe X` bir deploy betiğinde başarı sayılıyor. Yalnız `db` düzeltilmiş.
- **`apikey rotate` yeni service-role anahtarını her zaman stdout'a basıyor** (C-F10) — [[feedback_never_print_a_secret_to_prove_it_exists]].
- **Path escape yok**: `auth` ×10, `test-user delete` (C-F11); diğer 5 paket `url.PathEscape` kullanıyor.

---

## 3. Silinmesi gerekenler (ölçüldü, çağıranı yok)

| Ne | Boyut | Kanıt |
|---|---|---|
| `internal/apps` komut ağacı | **617 satır**, 8 fiil | `Cmd()` çağıransız; `surface_test.go:337` bilerek muaf tutuyor ("its own commit" — o commit atılmadı). A-1 · C-F16 · E-10 |
| GitHub `repository_provider` kolu | `deploy.go:115-126,222-230,287-330` + `selection/config.go:140-147` + `push --help` | Bulut `mode:"platform"` **sabit** (`cli.controller.ts:94-95`). A-2 |
| `internal/hook` | paket + `doctor` satırı | `Ensure` yalnız github kolunda → v2'de hiç kurulmuyor; `doctor` her checkout'ta `✗ hook` ve tavsiyesi **yerine gelemez**. A-3 · C-F13 · F-F12 |
| `tests/e2e/mgmt_api_test.go` | ölü host + ölü rotalar | **derlenmiyor**: `auth.LoadDPoPKey` imzası değişmiş. E-1 · A-7 |
| 39 kullanılmayan sembol | `flags.go` tanım builder'ı, `storage.go` bucket/boyut, `egress.go` 3 regex, `gitroot.go` (61 satır), `pull_spec.go` 5 tip, `deploy.go` 4 alan… | golangci `unused`. E-11 · A-16 |
| `Target.Project/Env` alanları | 4 okuyucu, **0 yazıcı** | `target.go:39-42`; bulut ortam adı hep `"main"` çıkıyor → staging'e link'lenen checkout `Main.xcconfig` üretiyor. B-19 |
| Bayat yorum/fikstür | `palbe_gen.golden.ts` (`branch:'main'`, okuyanı yok), `credentials.go:135` "NOTHING CALLS StoreCredential" (çağırıyor), `pull_spec.go:117` "spec does NOT write the config" (yazıyor) | A-15 · B-20 |

---

## 4. Kapılar — asıl mesele

Üç P0'ın üçü de yeşil kapıların altından geçti. Ölçülen kapı boşlukları (E):

- **`.golangci.yml:3` "ci.yml runs this gate blocking" diyor — `ci.yml`'de lint adımı YOK** (`745dd73`'te silinmiş, geri gelmemiş). Açık: **48 bulgu** (errcheck 2, staticcheck 7, unused 39).
- **`gofmt` ve tam `go vet` CI'da yok**; 2 dosya biçimsiz.
- **21 test `npm install` başarısız olunca yeşile dönüyor**; 5 test kardeş checkout yokken **her zaman** atlıyor — aralarında compose-vendorlama parite kapısı (P0-1'in kapısı!).
- **Birim suiti ağa çıkıyor** (`npm i`, `@palbase/backend@latest`'e karşı assert) — registry kayması = aynı commit altında davranış değişimi.
- **`-short` kısa değil**: `internal/backend` seri **7,4 dk**, sıfır `t.Parallel()`. Commit öncesi hızlı kapı imkânsız → [[feedback_local_gates_stay_minimal]] uygulanamıyor.
- **Yerel ≠ CI derleyici**: makine Go 1.27.0, `go.mod`/CI 1.26.6; golangci-lint 1.27 stdlib'ini parse **edemiyor**.
- Coverage **%66,1**; `cmd/palbase` **%23,8** — komut ağacını canlı uçlara bağlayan **kablolamanın tamamı %0** (`managementREST`, `wireDPoPSigner`, `selectedProjectTarget`, `linkedTarget`…), `internal/versions` %0 (testsiz), `start --lan` tamamı %0.

`surface_test.go`'nun kapsamadıkları (A-22): `.go` dışı dosyalar (`install.sh`) · emekli **alt** fiiller (`db types`) · "palbase " öneki olmayan emekli adlar (`serve`) · yardımda adı geçen dosya yolları · linksiz checkout'ta bayrakların gerçekten okunduğu · `--json` anlam tutarlılığı · **ölü rota çağrıları** (hiçbir test sunucu rota listesiyle CLI yol literallerini karşılaştırmıyor).

---

## 5. Güvenlik kalemleri

| # | Bulgu | Kanıt |
|---|---|---|
| S-1 | **`--insecure` commit'lenen dosyaya yazılıyor** ve 5 okuyucu sessizce uyguluyor → TLS atlaması her clone ve CI runner'ına yayılıyor, süresiz | `target.go:43-46` (0644); F-F7 |
| S-2 | **`~/.palbase/credentials-dev.json` 0644**, tam access+refresh token, emekli `--mode dev` döneminden; kimse temizlemiyor. `logout` "this machine's credentials" diyor, yalnız mevcut checkout'unkini unutuyor | F-F7/F9 |
| S-3 | `apikey rotate` sırrı her zaman basıyor | `apikey.go:179`; C-F10 |
| S-4 | `auth` ×10 + `test-user delete` path escape yok | C-F11 |
| S-5 | `notifications add` sırrı okunmayan kasa anahtarına da yazıyor, `remove` bırakıyor; "secret-guard" yorumu **yalan** (v2'de prefix koruması yok) | C-F09 |
| S-6 | DPoP keyring hatası **sessizce** dosya anahtarına düşüyor (`_ = err`) — kullanıcı keyring anahtarıyla imzaladığını sanıyor | `dpop_storage.go:94`; E-18 |
| S-7 | `doctor` sahte `PALBASE_ACCESS_TOKEN` ile `✓ login ✓ pat` diyor; hiç doğrulamıyor, önceliği söylemiyor, login probu `session.json`'ı **yeniden yazıyor** | F-F3 |

Olumlu: tarayıcı akışı sağlam (önce dinleyici, `state` `code`'dan önce, PKCE S256 crypto/rand, kaçışlı callback). Güvenlik yolunda fail-open bulunmadı.

---

## 6. Brainstorm gündemi (önerilen sıra)

1. **Sevkiyat kapıları** — P0-1'i doğuran sınıf: taklit eden kapı, negatif kontrolsüz. CI'a lint/gofmt/vet/`compose config`/`-tags e2e`; `-short`'u gerçekten kısa yap; rota-literal ↔ sunucu-rota kapısı.
2. **Tek `link`** — platform algılama, `unlink`, config şekil birleştirme (çok-ortam belge tek yazıcı; palbe-gen ve Gradle `environments[default]` okusun), `Palbase/` ↔ `.palbase/` bölünmesi.
3. **Seçim katmanının emekliliği** — `--project/--environment` + `selection.json` + `internal/apps` + github kolu + `internal/hook`: ya rota eklenir ya katman sökülür. v2'de proje=kiracı=ref olduğu için sökme tutarlı.
4. **`start` ↔ deploy denkliği** — imajı projenin SDK'sından türet (ya da reddet), seçtiği imajı **yaz**, palsvc değil **runtime**'ı yokla, `push`'un SDK takasını rung reddiyle değiştir.
5. **Modül yüzeyi sözleşmesi** — fiil adları, `--json` tek anlam, `▸` tek yer, bilinmeyen alt komut exit≠0, yıkıcı fiillerde onay, zarf çözme, path escape.
6. **Kimlik & mesaj hijyeni** — `--insecure`'ü gitignore'lu dosyaya, eski credential süpürme, sır basmama, Türkçe dizeleri çevir + kapı.

---

## Açık bilinmeyenler

- `@palbase/backend@26.0.0`'ın yayımlanma kararı bu denetimin dışında (sevkiyat).
- `auth providers config set` cevabı modül credential'ı geri döndürüyorsa ekrana düşüyor olabilir — **ölçülmedi**.
- `-short`'suz tam suite sonucu — koşu tamamlanmadı, **ölçülemedi**.
- `PALBASE_RUNTIME_IMAGE` override'ının canlı doğrulaması yapılamadı (Docker daemon denetim sırasında doygundu); koda göre onurlanıyor ama hiç yazdırılmıyor.
