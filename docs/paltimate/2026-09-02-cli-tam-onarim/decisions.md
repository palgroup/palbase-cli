# CLI Tam Onarım — karar günlüğü

**Açılış:** 2026-09-02 · **Girdi:** `sdk/cli/docs/paltimate/research/2026-09-02-cli-full-audit.md` (122 bulgu, 6 kör açı)
**Faz:** design (brainstorm)

---

## Intake envanteri (sohbetin TAMAMINDAN)

| # | Kalem | Kaynak | Durum |
|---|---|---|---|
| 1 | `palbase link` / `ios link` / `web link` nasıl çalışıyor — anlaşılsın | msg 1 | ✓ çıkarıldı |
| 2 | Bu üç komutta **daha iyi DX** | msg 1 | tasarımda |
| 3 | Tüm CLI incelensin, geliştirilebilecekler çıkarılsın | msg 2 | ✓ 122 bulgu |
| 4 | **"Hata veya sorun istemiyorum"** — sıfır bilinen kusur barı | msg 2 | kalite kısıtı |
| 5 | Tüm komut yolları düşünülsün | msg 2 | tasarımda |
| 6 | **Eski kalanlar / silinmesi gerekenler** bulunsun ve silinsin | msg 2 | tasarımda |
| 7 | Mevcutlar nasıl daha iyi olur | msg 2 | tasarımda |
| 8 | Proje linkleme — **ios/web adına ihtiyaç olmayabilir** | msg 2 | tasarımda |
| 9 | **Modül namespace'leri** incelensin + **test edilsin** | msg 2 | ✓ 24 bulgu, tasarımda |
| 10 | `palbase start` doğru stack'i mi kaldırıyor | msg 2 | ✓ HAYIR |
| 11 | **Hepsi yapılsın, baştan sona** — kısmi değil | msg 4 | kapsam kısıtı |
| 12 | CLI **en güncel backend SDK sürümünü** almalı | msg 4 | tasarımda |
| 13 | **Sürüm ve imaj bilgisi network'ten** gelsin | msg 4 | tasarımda |
| 14 | Her zaman **en günceli** kullansın/bassın | msg 4 | tasarımda |
| 15 | **Sürekli CLI güncellemesi gerekmesin** | msg 4 | tasarımda |

Hiçbiri sessizce düşemez; tasarım kapanışında traceability kontrolünde her satır bir tasarım öğesine bağlanacak.

---

## Kararlar

### D-001 · "En güncel SDK" tek başına yeterli değil — otorite birliği şart
**Karar:** ⑫'nin ("en güncel backend SDK") doğru okuması *"npm'in `latest`'i"* değil, **"kaldırılan runtime imajının vendor'ladığı sürüm"**dür; SDK sürümü ile imaj **aynı otoriteden** çözülmelidir.
**Gerekçe (ölçüldü):** `init.go:51-55` bugün ZATEN npm'in en yenisini kuruyor (`latest` = 25.1.0). Kırılma eski sürümden değil, **imajın npm'in önünde olmasından** geliyor: `runtime-dev:0.40.0` 26.0.0 vendor'luyor, npm'de 26.0.0 yok → `init` sonrası `build` düşüyor (P0-2). İki ayrı otorite (npm dist-tag ve imaj sabiti) senkron tutulamaz; tek otorite bunu yapısal olarak çözer.
**Reddedilen alternatif:** "init `latest` yerine `next` alsın" — aynı iki-otorite sorunu, sadece kayması gecikir.
**Durum:** kullanıcı onayı bekliyor (soru batch'inde).

### D-002 · Self-host denkliği pin tasarımını KISITLAR (hard constraint)
**Karar:** Pin otoritesi **işaret edilen kontrol düzlemi**dir, `api.palbase.studio` değil. Self-host bir kurulum kendi pinini kendi düzleminden (ya da kendi yığınından) çözebilmelidir; CLI hiçbir yolda Palbase bulutuna zorunlu bağımlı olmamalıdır.
**Gerekçe:** [[feedback_self_host_parity_outranks_density]] — "cloud ve core ayrı ayrı stack'lenerek çalışabiliyor olmalı; bu ürünün tanımı, bir tercih değil." Ölçülen mevcut kusur bunu zaten deliyor: `PALBASE_PLATFORM_URL` dört adresten yalnızca birini değiştiriyor, `PublicHost`'un override'ı yok (`config.go:20,142-153`) → self-host `login`/`link`/anahtar-broker'ı hâlâ `palbase.studio`'ya gidiyor (denetim F-14).
**Sonuç:** W1 tasarımı, F-14'ün düzeltmesini (tek `PALBASE_CLOUD` + `/v1/cloud/config`'ten türetme) **içermek zorunda**; ayrı kalem değil, önkoşul.

### D-003 · Geriye uyum YOK — emekli komutlar shim'siz sökülür
**Karar:** `ios|macos|android|web link`, `<platform> use`, `--project/--environment` (kararı ne olursa olsun) ve ölü paketler **shim, alias, deprecation penceresi olmadan** silinir.
**Gerekçe:** [[feedback_no_backward_compat]] (temiz kırılma isteniyor, iki-yol bakımı yaratma) + [[feedback_no_useless_flags_fallbacks]] (cutover DİREKT, flag'li kademe yok). Üretim kiracısı yok (sahibin kararı, 2026-08-22).
**Sonuç:** Tasarımda "eski komut hâlâ çalışsın" seçeneği aday olarak bile üretilmez.

### D-004 · Release kesmek BENİM işim — `@palbase/backend@26.0.0` bir tasarım bağımlılığı değil, iş kalemi
**Karar:** P0-2'nin gerçek düzeltmesi yayımlanmamış 26.0.0'ı gerektiriyorsa, onu yayımlamak ve CLI sürümünü kesmek bu programın **içinde**dir; kullanıcıya "yayın bekliyor" diye bırakılmaz.
**Gerekçe:** [[feedback_i_cut_the_releases_not_the_user]] — "Release edilmemiş kod, olmayan koddur."

### D-005 · Kalite barı: kapılar önce
**Karar:** ⑪ ("hepsi, baştan sona") ve ④ ("hata veya sorun istemiyorum") birlikte, sevkiyat kapılarının onarımını **ilk iş** yapar.
**Gerekçe:** Üç P0'ın üçü de yeşil kapıların altından geçti; P0-1'in kapısı yeşildi çünkü karşılaştırmanın iki tarafı da aynı bozuk fonksiyondan geçiyordu. Ölçmeyen kapı altında yapılan her düzeltme aynı sessizlikle çürüyebilir → geri kalan beş kolun kanıtı kapılara bağlı.
**Not:** Bu, yayında kırık P0'ların ne zaman düzeltileceğinden AYRI bir soru (kullanıcıya soruluyor).
### D-006 · Pin fetch'i ASLA sert bağımlılık olamaz (kanıtla)
**Kısıt:** CLI'ın hiçbir fiili, pin ucuna ulaşamadığı için düşmemelidir. Committed lockfile + son-bilinen-iyi cache; uç erişilemezken CI lockfile'dan koşabilmelidir.
**Kanıt (rs-pin-risk):** Terraform `init`, her sağlayıcı sürümü kilitli VE plugin cache dolu olsa bile eve telefon ediyor; 2021 registry kesintisinde yeni dizinde `init` edilemedi. HashiCorp'un cevabı: *"örtük cache bir offline mod değildir; `filesystem_mirror`'ı açıkça beyan etmelisin"* (hashicorp/terraform#28895). Aynı sınıf kesinti Haziran 2026'da iki kez tekrarladı. → Sert bağımlılık, her geliştiriciyi ve her pipeline'ı AYNI ANDA düşürür ve bu, ele geçirmeden çok daha sık olur.

### D-007 · Tel formatı YALNIZCA digest taşır; istemci tag'i reddeder
**Kısıt:** Sunucu tag gönderse bile istemci kabul etmez.
**Kanıt:** Değişken tag 13 ayda iki ekosistem ölçekli olaya yol açtı — tj-actions (23k depo, CVE-2025-30066, CISA KEV) ve Trivy/TeamPCP (77 tag'in 76'sı force-push, CVE-2026-33634). İkisinde de aynı temiz çizgi: **digest/SHA pinli tüketiciler etkilenmedi.** Ayrıca geri çekme YAYILMIYOR: Docker Hub kötücül Trivy imajlarını sildikten sonra `mirror.gcr.io` onları sunmaya devam etti → silme bir geri çağırma değildir, ileri yuvarlanır.

### D-008 · Güven çıpası BINARY'ye gömülü kalır — "her şey network'ten" burada DURUR
**Kısıt:** İmza, CLI binary'sine derlenmiş bir trust anchor'a ve pinlenmiş imzalayan kimliğine karşı doğrulanır. Kontrol düzlemi **asla** "yeni anahtarıma güven" diyememelidir.
**Sonuç — kullanıcıya söylenecek dürüst kayıt:** ⑮ ("sürekli CLI güncellemesi gerekmesin") sürüm/imaj pinleri için tam sağlanır; ancak **anahtar rotasyonu** bir CLI sürümü gerektirir. Bu, tasarımın kabul ettiği tek istisnadır ve bilinçlidir.
**Kanıt:** Codecov 2021 — sunucudan koşum anında çekilen bir betik iki ay boyunca 108 pencerede sessizce değiştirildi; yalnızca bir müşteri SHASUM'a baktığı için bulundu. Codecov'un kendi düzeltmesi betik sunmayı bırakıp **imzalı binary** göndermek oldu. Bu, tasarladığımız şeklin en yakın analogudur.

### D-009 · İmza zorunluluğu, taze olmayan güven materyaliyle birleşince kesinti üretir
**Kısıt:** Doğrulama zorunlu olacaksa güven materyalinin tazelenme yolu da tasarlanmalıdır.
**Kanıt:** Docker Content Trust <%0,05 benimsenme ile emekliye ayrılıyor; Ağustos 2025'te sertifikaları dolunca `DOCKER_CONTENT_TRUST=1` ile nginx/ubuntu çekmeleri **düştü** ve resmî çözüm "değişkeni kaldırın" oldu.

### D-010 · Pin bir "içerik" değil, fiilen KOD'dur — kademeli yayım şart
**Kısıt:** Pin değişiklikleri canary halkalarıyla ve kullanıcı seçebildiği bir kanalla yayılır; global anında yayım yok.
**Kanıt:** CrowdStrike Channel File 291 — *içerik* güncellemesi (kod değil) global itildi, 8,5M makine çöktü; 78 dakikada geri alındı ama bu yalnız henüz çekmemiş konakları kurtardı. Sensör binary'sinde kademeli yayım vardı, içerik kanalı onu atlıyordu.

### D-011 · Reprodüksiyon bir ödünleşme DEĞİL — çözülmüş biçim var
**Bulgu:** "Hep en güncel" ile "aynı commit aynı şekilde derlenir" çelişmiyor: digest-pin + otomatik yeniden-pinleme (Renovate/Dependabot deseni) ikisini birden veriyor.
**AÇIK KALAN (kullanıcıya sorulacak):** Yeniden pinleme **ne zaman** olur — yalnız açık bir komutla mı, TTL'li otomatik tazelemeyle mi, yoksa her çağrıda mı? Risk kolunun kendi ifadesiyle bu, "tasarımı en çok değiştirecek açık bilinmeyen"; uzak çözümün lockfile'ı ve PR'ı olmadığı için gözden geçirme adımı hiç var olmaz — o yüzden saf her-çağrıda çözüm, eleştirilen mevcut pratikten bile kötüdür.

### D-012 · Hız limitleri ucuz ama gerçek
**Kısıt:** Her çağrıda metadata çekimi tasarlanırsa koşullu istek (ETag) + cache + backoff zorunlu.
**Kanıt:** Docker Hub 100 çekim/6sa (IPv4 **veya IPv6 /64** başına); GitHub REST kimliksiz 60/sa/IP; CI filoları egress IP'sini paylaşıyor (GitLab kendi uyarısında barındırılan runner'ların limite "topluca tabi" olduğunu yazıyor). GHCR kimliksiz **sınırsız değil**.
### D-013 · İKİ AYRI İMAJ AİLESİ var — "proven image" yerel yığının pini DEĞİL
**Bulgu (dossier-plane, alıntılı):** `ghcr.io/palgroup/palbase/{palsvc,runtime-dev,edge}` (CLI'ın `start.go:66-76`'da sabitlediği yerel geliştirme üçlüsü, `workflow_dispatch` ile yayımlanıyor) ile `<ACR>/tenant-stack:sha-<commit>` (`cloud_fleet.stack_image`'ın tuttuğu filo imajı) **farklı depolar, farklı pipeline'lar**.
**Sonuç:** `start`, `readProven()`'i doğrudan kullanamaz. Pin manifestosu **her ikisini birden** taşımalı ve tutarlılıkları yayım hattında kurulmalı: yerel üçlü + kiracı imajı + o imajın vendor'ladığı **SDK sürümü** + kabul ettiği **ABI aralığı**. Tek alan yetmez.
**Bu, tasarımın en kolay gözden kaçacak yeriydi** — "pini network'ten al" cümlesi tek bir pin varmış gibi okunuyor.

### D-014 · Bugün sunucuda okunabilir bir pin YOK — uç İNŞA EDİLECEK
**Bulgu:** `readProven()`'in beş okuyucusu var ama CLI'a açık tek yüzey `POST /v1/cloud/projects/{ref}/upgrade` — yani bir **mutasyon** (pod takas penceresi açıyor). *"Yeni bir proje hangi imajı alır"* sorusunu yanıtlayan **hiçbir GET yok**. `studio/src/server/sdk-pins.ts`'in **sıfır HTTP çağıranı** var (Studio-içi `controlQuery`); erişilebilir olan `GET /v1/panel/fleet/sdk-pins` **operatör kapılı (404)**.
**Sonuç:** W1 bir sunucu iş kalemi içerir; CLI tarafı tek başına yeterli değil.

### D-015 · Manifestonun doğal evi `GET /v1/cloud/config`
**Karar (aday):** Pin manifestosu, düzlemin **tek anonim okuması** olan `GET /v1/cloud/config`'e (`cloud.controller.ts:577`, `auth:false`, bugün `{anonKey, issuer, tenantDomain}`) alan eklenerek ya da onun yanına konularak sunulur.
**Gerekçe:** (a) zaten anonim — pin, login'den ÖNCE gerekli (`palbase init`/`start` oturum istemiyor); (b) zaten bootstrap yolu; (c) yayındaki CLI'lar düz `json.Unmarshal` kullandığı için **eklenen alanları tolere ediyor** — geriye dönük kırılma yok; (d) D-002'yi karşılıyor: self-host kendi düzleminin config'ini sunar, otorite işaret edilen düzlemdir.

### D-016 · `cloud_fleet.stack_image` DIGEST TAŞIMIYOR — şema değişikliği gerekiyor
**Bulgu:** Değer tek opak string: `<registry>/tenant-stack:sha-<40hex>` — bu bir git commit sha'sı, **imaj digest'i değil**. Digest alanı yok.
**Sonuç:** D-007 (telde yalnız digest) düzlemde bir şema + yayım hattı değişikliği gerektirir. "Sadece CLI'ı değiştirelim" ile çözülmez.

### D-017 · ABI merdiveni iki dilde ELLE aynalanıyor, CLI'ın tavanı sabit `2`
**Bulgu:** `abi.ts:122-127` ↔ `stack_bundle.go:610-618`, ortak fikstür yok; `__claimInjectables` 09-02'de ikisine de elle eklendi. CLI'ın fallback tavanı sabit `ceiling := 2` (`stack_bundle.go:1156`). Düzlem MIN/MAX'i **hiç yayımlamıyor**; yalnız bir kiracı pod'u `service_role` arkasında bildiriyor.
**Sonuç:** Manifesto ABI aralığını da taşırsa, elle aynalama sınıfı kökten kapanır — bu, denetimdeki "ilk push her yerde reddediliyor" ailesinin yapısal çözümü.

### D-018 · YENİ KUSUR (denetime ek, #123) · `abi_generation` beyan edilmiş, okunuyor, ama hiç YAZILMIYOR
**Bulgu:** `cloud_deployments.abi_generation` tanımlı ve `SDK_PINS_SQL` tarafından okunuyor; üretimdeki tek INSERT (`cloud.controller.ts:1391-1396`) bu sütunu **adlandırmıyor** — depo genelinde tek yazıcı bir test fikstürü. Dolayısıyla `/v1/panel/fleet/sdk-pins` her gerçek satır için `abiGeneration: null` dönüyor.
**Sınıf:** [[feedback_a_read_flag_needs_a_writer_grep_both_sides]] — okuyanı olan alanın yazanını da grep'le. Denetimin 122 bulgusuna 123. olarak eklenir.

### D-019 · Düzlemde hız limiti / cache / ETag YOK
**Bulgu:** Düzlem kaynağında `cache-control|etag|ratelimit|throttl` için sıfır eşleşme; `@palbase/backend` rota başına limit desteklese de düzlem hiç beyan etmemiş.
**Sonuç:** D-012 ile birleşince: "her çağrıda çöz" seçilirse bu YENİ altyapı demektir. Lockfile + seyrek tazeleme seçilirse gerekmez. Bu, kullanıcıya sorulacak sorunun maliyet tarafı.
### D-015a · D-015'in öncülleri team-lead tarafından BİZZAT doğrulandı (+ bir nüans)
**Doğrulandı 1 — uç gerçekten anonim:** `cloud.controller.ts:577` → `@Get("/config", { auth: false })`, ve üstündeki yorum niyeti açıkça yazıyor: *"`auth` AÇIKÇA `false`: bu uç, kimliğin kendisinden ÖNCE gelir."* Pin'in login'den önce gerekmesiyle birebir örtüşüyor.
**Doğrulandı 2 — alan eklemek yayındaki CLI'ları kırmaz:** `internal/auth/v2login.go:93-94` düz `json.Unmarshal(body, &b)` kullanıyor; `DisallowUnknownFields` YOK → Go bilinmeyen alanları sessizce yok sayar. Eklenen alanlar geriye dönük güvenli.
**NÜANS (öncü daraltıldı):** `/v1/cloud/config`'in depo genelinde **tek çağıranı** `v2login.go:79`, yani yalnızca **login yolu**. `palbase init` ve `palbase start` bugün bu ucu HİÇ çağırmıyor.
**Sonuç:** Manifestoyu oraya koymak bedava değil — `init`/`start`'a yeni bir ağ çağrısı ekler. Bu doğrudan D-006'ya (sert bağımlılık olamaz) ve çevrimdışı hikâyesine dokunur: `start` bugün imajlar cache'liyken ağsız çalışıyor; tasarım bu özelliği KORUMALI. Aday tasarımlar bunu açıkça çözmek zorunda.
### D-020 · Pin ÜÇ değil YEDİ yerde; ve koşan sabit, Go'daki DEĞİL
**Bulgu (dossier-cli + team-lead doğrulaması):**
- `start.go:65-77` üç tag + **aynı üçü compose'da ikinci kez** (`docker-compose.dev.yml:138,224,312` `${VAR:-default}`) + **dördüncü bir imaj: `pgvector/pgvector:pg16` (`:162`) — değişkensiz, override edilemez, `stackImages`'ta yok, `imagesPresent` bakmıyor** + `parserTSVersion="5.9.3"` (`tsparser.go:23`) + `minimumSDKMajor=18` (`stack_bundle.go:1014`) + `ceiling:=2` (`stack_bundle.go:1152`).
- **BİZZAT DOĞRULANDI:** `composeEnv` (`start.go:519-539`) yalnız `PALBASE_PROJECT_DIR`, `PALBASE_HTTP_PORT`, `PALBASE_PUBLIC_ORIGIN` (+`--lan` ise BIND) export ediyor — **imaj değişkenlerini göndermiyor**. Yani fiilen koşan değer compose default'u; Go sabiti yalnız ön kontrol (`imagesPresent`) ve `--init-env` çağrısı için. İkisi literal-vs-literal bir testle eşit tutuluyor ve o test bir kaymayı ZATEN kaydetmiş (0.39.0 vs 0.36.1).
**Sonuç:** Pin tasarımı Go sabitini değiştirmekle bitmez — **compose'a değer geçirmek ya da compose'u üretmek** zorunda. Ve `pgvector` bugün hiçbir mekanizmanın görmediği bir dördüncü pin.

### D-021 · Ağdan çözme deseni CLI'da ZATEN VAR
**Bulgu:** `init.go:60-83` `npm view @palbase/backend versions --json` ile en yeni kararlı sürümü ağdan çözüyor. Ölçülen bugün: en yeni **25.1.0**, `dist-tags {next: 25.1.0, latest: 25.1.0}`.
**Yeniden kullanılabilir mevcut mekanizmalar (dossier-cli):** sürüm-anahtarlı kendini-geçersizleştiren cache `~/.palbase/tools/typescript-<v>/`; `rememberPort`'un "bir kez çöz, grup başına hatırla" emsali (gerekçesiyle); `writeFileAtomic`+flock; `sendWaitingForReady`; ve zaten okuyucusu+yazıcısı olan boş 0600 `~/.palbase/config.json`.
**Sonuç:** W1 sıfırdan mekanizma icat etmiyor; var olanı genişletiyor.

### D-022 · CLI yalnız TAVANI okuyor, TABANI hiç çözmüyor; ve yerel yığında ret ÖLÜ UÇ adlandırıyor
**Bulgu:** `stack_bundle.go:1081-1132` `GET <target>/v1/management/deployments/current` → `serves_bundle_generation` (yalnız tavan). Runtime `min_bundle_generation`'ı yayımlıyor ama CLI **hiç decode etmiyor**; `build` hiçbir şey dayatmıyor. Tavan reddi `palbase upgrade` diyor, ama `refFromTargetURL` (`upgrade.go:52-61`) loopback için tasarım gereği `""` dönüyor → **`palbase start` yığınının upgrade yolu YOK**.
**Bağlantı:** Bu, hafızadaki centauri kilitlenmesinin yerel karşılığı. Manifesto ABI aralığını taşırsa (D-017) hem taban hem tavan tek yerden gelir ve elle aynalama biter.

### D-023 · Prior art: EXPO tam analog — ve gerekçesini birebir bizim cümlemizle yazmış
**Bulgu (rs-pin-priorart, 19 sistem):** Expo CLI sunucuya *"SDK major N için hangi sürümler gider"* diye soruyor; kaynak yorumu motivimizi kelimesi kelimesine adlandırıyor: *"Prefer the remote versions over the bundled versions, this enables us to push emergency fixes that users can access without having to update the `expo` package."*
**Kritik ayrıntı:** anahtar **SDK major**'a göre, **CLI sürümüne göre DEĞİL** — eski istemcilerin çalışmaya devam etmesinin sebebi bu. Canlı doğrulandı: 7.0.0…57.0.0 anahtarlarının hepsi hâlâ sunuluyor.
**Ve onu güvenli kılan katman:** uzak uç → hata/çevrimdışı ise **SDK PAKETİNİN İÇİNDE gelen** `bundledNativeModules.json`'a düş (CLI binary'sinin içinde değil!) → `EXPO_OFFLINE=1` ile ağı tümden atla. İnce hamle: fallback SDK paketiyle seyahat ettiği için **eski bir CLI kendi binary'sinden bayat pin servis edemez**. Cache 5 dk TTL, **stale-on-error YOK** — çevrimdışı hikâyesini tümüyle paketlenmiş dosya taşıyor.

### D-024 · Bugünkü hâlimizin adı var: SUPABASE CLI — ve bırakmak istediğimiz tam olarak o
**Bulgu:** Supabase CLI imajları `//go:embed` ile binary'ye gömülü gerçek bir Dockerfile olarak taşıyor; değişken tag, digest yok, ağ manifestosu yok, min-sürüm ucu yok; Dependabot Dockerfile'ı bump'lıyor. Bu, bizim bugünkü tasarımımızın birebir aynısı.
**Ve karşı uç:** **Bazelisk** (saf her-çağrıda ağ çözümü) 7 yıldır çevrimdışı hikâyesi olmadan duruyor — issue #88 2019'dan, #664 "airplane mode" 2025'ten beri açık. → **Shape A'yı Shape F olmadan gönderme.**

### D-025 · ÇELİŞKİ AÇILDI: güven materyali gömülü mü, çekilebilir mi? (ortalanmayacak)
**Angle 1 (risk):** trust anchor **binary'ye gömülü** olmalı, yoksa ele geçirilmiş düzlem yeni anahtarı da manifestoyla birlikte servis eder ve doğrulama tiyatroya döner (Codecov 2021).
**Angle 2 (prior art):** güven materyalini istemciye gömmek **belgelenmiş bir kesinti üreticisi** — Corepack npm'in imza anahtar id'sini pinledi, npm rotasyona gidince *"her yeni package-manager kurulumu kırıldı"* (nodejs/corepack#612); ayrıca DCT sertifikaları dolunca çekmeler düştü.
**İkisi de gerçek olay gösteriyor.** Ortalama almak ya güvensiz ya sevk edilemez bir tasarım üretir → **hedefli üçüncü tur açıldı** (`rs-trust`): kimlik-pinleme (Sigstore keyless: *hangi anahtar* değil *kim imzalayabilir*) çelişkiyi çözüyor mu, TUF ne veriyor, ve bizim büyüklüğümüz için dürüst asgari basamak hangisi.
**Bu karar çözülmeden W1 tasarımı kullanıcıya sunulamaz** (skill: `status: open` satırına dayanan bölüm onaya sunulmaz).

### D-026 · Çok mimarili imaj + lockfile tuzağı bizde de geçerli
**Bulgu:** Terraform'un çapraz platform kilit tuzağı — bir makinede üretilen kilit yalnız o makinenin platformunu kaydeder; her platform için ayrıca `terraform providers lock -platform=…` gerekir. Bizim imajlarımız çok mimarili (arm64 Mac + amd64 CI).
**Sonuç:** Lockfile digest tutacaksa **manifest-list digest'i** tutmalı (mimari başına manifest digest'i değil), yoksa Mac'te üretilen kilit CI'da çözülemez.
### D-013a · DÜZELTME + BÜYÜK SADELEŞTİRME · Manifesto ZATEN VAR: `version.env`
**D-013 "iki ayrı imaj ailesi, farklı depolar/pipeline'lar" YAYIMLAMA adımı için doğru, ama İLİŞKİ için yanıltıcıydı.** Karşı-hipotez kontrolü (team-lead, bizzat) şunu ölçtü:

**1. Aynı artefaktlar, iki türlü monte ediliyor.** `v2-cloud/tenant-stack/Dockerfile:31-33,64-65`:
```
FROM --platform=linux/amd64 ${BASE_REGISTRY}/palsvc@${PALSVC_DIGEST}  AS palsvc
FROM --platform=linux/amd64 ${BASE_REGISTRY}/runtime@${RUNTIME_DIGEST} AS runtime
FROM --platform=linux/amd64 pgvector/pgvector:pg16
COPY --from=runtime /app /app
```
Yani kiracı imajı, yerel yığının kullandığı **aynı palsvc ve runtime'ı digest'le tüketip** envoy + pgvector ile tek konteynere monte ediyor. Ve `/app` kopyalandığı için **kiracı imajının SDK sürümü = runtime imajının SDK sürümü**. İkisi ayrı dünya değil; biri diğerinin bileşimi.
**Bonus:** yerel compose'un `:162`'de değişkensiz sabitlediği `pgvector/pgvector:pg16`, kiracı imajının TABAN imajının ta kendisi — "yönetilmeyen dördüncü pin" aslında zaten paylaşılan bir pin.

**2. DIGEST DİSİPLİNİ, TEK-KAYNAK MANİFESTO VE TAG→DIGEST DOĞRULAMASI ZATEN VAR.** `v2-cloud/bootstrap/images/version.env` kendini "ORTAMIN ÇEKİRDEK SÜRÜMÜ VE PİNLERİ — TEK KAYNAK" diye tanımlıyor ve şunları taşıyor: `V2_VERSION=0.40.0`, `V2_PALSVC_DIGEST=sha256:1a85…`, `V2_RUNTIME_DIGEST=sha256:b7dd…`, `V2CLOUD_IMAGE_TAG=sha-<commit>`. `seed.sh` **her koşumda içeri aldığı etiketin bu digest'e çözüldüğünü DOĞRULUYOR — çözmezse duruyor** (`seed.sh:80`).

**3. Ve dosyanın kendi yorumları, risk araştırmamın bağımsız olarak bulduğu iki dersi ZATEN kaydetmiş:**
- *"ETİKET DEĞİŞEBİLİR, DIGEST DEĞİŞMEZ — ve bu ölçüldü: tenant yığını `palsvc@sha256:7b21…` pinliydi; yukarı akış `0.33.1` etiketini yeniden itince o digest yeni defterde HİÇ YOKTU ve derleme 'not found' ile düştü. Pin'in bütün amacı buydu: baytlar değişince SESSİZ kalmamak."* → D-007'nin (telde yalnız digest) kanıtı bu depoda, kendi acımızla.
- *"ÖLÇÜLDÜ (2026-08-19): imajlar v2'nin sürüm etiketiyle yayımlanıyordu ve o etiket her yayında YENİ baytlara çözülüyordu. Hücrede koşan pod'lar etiketi değişmediği için YENİDEN ÇEKMEDİ: chart 'deployed', imaj eski."*

**SONUÇ — W1'in maliyeti düştü.** Manifesto formatı icat edilmeyecek, digest çözümü inşa edilmeyecek: **`version.env` zaten manifesto**, `seed.sh` zaten tag→digest doğrulayıcı. Eksik olan tek şey **bu içeriği HTTP üzerinden yayımlamak** ve CLI'a okutmak. `rs-pin-priorart`'ın *"istemci adına tag→digest çözen bir sunucu için prior art bulamadım, istersek kendimiz inşa ederiz"* notu bu yüzden konusuz: biz **yayım anında** çözüp kaydediyoruz; sunucunun yapacağı tek şey kaydedileni sunmak.
**Kapanacak küçük boşluk:** `version.env` bugün `edge` digest'ini ve `pgvector` pinini taşımıyor (palsvc + runtime var). Manifesto o ikisini de almalı.
### D-027 · D-025 ÇELİŞKİSİ ÇÖZÜLDÜ — iki KATMAN, tek katman değil
**Karar:** İki kol da haklıydı ama **farklı katmanlar** hakkında. **Yetki beyanı** ("kim imzalayabilir") değişmez ve binary'ye gömülüdür, düzlemin erişemeyeceği yerde. **Anahtar materyali** (CA sertifikaları, log anahtarları) çekilebilirdir. Pinlenen şey *hangi anahtar* değil, *kim* — yani OIDC issuer + workflow kimliği.
**Kanıt:**
- TUF spesifikasyonu **gömülü kökü ZORUNLU kılıyor**: *"The client-side of the framework MUST ship with trusted root keys."* Güvenli, çünkü kök N+1, N'in anahtarlarının eşiğini VE kendisininkini gerektiriyor. Yani Angle 2'nin kendi alanı, sonucunu çürütüyor.
- **Corepack pinlenmiş bir KİMLİK değil, pinlenmiş bir ANAHTAR ID'siydi** ve imzalı rotasyon yolu yoktu; thread'deki çözüm `COREPACK_INTEGRITY_KEYS=0`, yani **fail-open**. Üstelik ikinci bir sebebi daha var ve o da çekilen anahtarları suçluyor: npm'in ucu rotasyon ortasında eski anahtarı DÜŞÜRDÜ ve CDN düğümü başına tutarsız kümeler servis etti.
- DCT'nin arızası ikisinden de değildi: sertifikalar *"Docker istemcisi tarafından bir kez cache'lendikten sonra tazelenmiyor, bu da sertifika rotasyonunu pratik olmaktan çıkarıyor."* Tümüyle emekli; `notary.docker.io` 2026-12-08'de kapanıyor.
- **Bizim şeklimiz için en güçlü üçgenleme — GitHub:** tek satıcı, kendi kontrol düzlemi; keyless kimlik **ARTI** TUF yönetimli güven kökü (donanım-token çoğunluğuyla) seçti. İkisi birden, biri değil. Gerekçesini de açıkça yazıyor: *"certificate revocation lists are too problematic to use effectively… the future of software provenance goes through workload identity."*
**Bizim için özelliğin gücü somut:** GitHub Actions'ta imzalayıp `token.actions.githubusercontent.com`'u pinlersek, **tümüyle ele geçirilmiş bir palbase düzlemi yine de o kimliği basamaz** — bunun için GitHub org'umuz da gerekir, ki o farklı bir operatör.
**Çevrimdışı (Q5) = HAYIR, şeffaflık günlüğü koşum-zamanı bağımlılığı değil:** keyless varsayılan olarak bundle iliştiriyor; cosign Rekor SET'ini ve Merkle içerme kanıtını **yerel** doğruluyor (`VerifyTLogEntryOffline`). GitHub'ın özel-depo bundle'ları hiç Rekor kaydı taşımıyor, yerine RFC 3161 zaman damgası koyuyor.
**Basamak (d) TUF REDDEDİLDİ:** PyPI — PSF fonlu, PEP kabul edilmiş, 2020'de tören yapılmış, runbook yayımlanmış — PEP 458'i üretime **hiç** alamadı (*"no signatures were ever produced from those roots"*) ve yerine Sigstore attestation'larını (PEP 740) gönderdi.

### D-028 · Kimlik pinlemenin DÜRÜST kalıntısı: risk yok olmuyor, YER DEĞİŞTİRİYOR
**Kabul:** Arıza modu "başkası anahtar döndürdü"den "biz bir workflow'u yeniden adlandırdık"a taşınıyor. Kubernetes bunu **iki kez** yaşadı (birkaç yama sürümünde yanlış imzalayan kimliği; sonra v1.29–v1.31'de hiç imza yok).
**Zorunlu tasarım sonucu:** Binary tek bir kimlik dizesi değil, **KİMLİK KABUL LİSTESİ** taşır — böylece bir yeniden adlandırma iki sürümlük örtüşmeyle geçer. (npm'in sonunda anahtarlara `expires` ile uyguladığı disiplinin aynısı.)
**Ayrıca — reusable-workflow tuzağı (üç bağımsız kaynak, `gh attestation verify --help` dahil):** SAN, çağıranın deposu değil **reusable workflow'un** deposu olur; politika `source-repository` claim'lerini kontrol etmek zorunda.

### D-029 · Basamak (c)'yi geçersiz kılacak TEK koşul — imzalayan AYRI güven alanında kalmalı
**Kısıt:** Manifesto imzalama işi bir gün palbase altyapısına taşınırsa, basamak (c) **sessizce (b)'ye düşer** ve tüm gerekçe çöker. İmzalayan, kontrol düzleminden **farklı bir güven alanında** kalmalıdır (GitHub Actions OIDC).
**Bu bir mimari değişmez olarak spec'e girecek.**

---

## Kullanıcı kararları (2026-09-02 soru turu)

### D-030 · W1 (imaj/pin ağdan çözme) KAPSAM DIŞI — kullanıcı kararı
**Karar:** *"kanka sen bu image işine girme bence."* → İmaj/pin/SDK sürümünün ağdan çözülmesi bu programdan ÇIKARILDI.
**Sonuç:** ⑬/⑭/⑮ (sürüm+imaj network'ten, hep en güncel, CLI güncellemesi gerekmesin) kapsamdan düşer. D-006…D-029 arası pin/güven kararları **arşivlenir** — silinmez, çünkü iş ileride açılırsa kanıt hazır (özellikle D-013a: `version.env` zaten manifesto; D-027: güven iki katmanlı).
**Gerekçe (çıkarım):** İş `v2-cloud` + filo yayım hattına giriyor; orası başka oturumun kulvarı ([[feedback_infra_not_my_lane_just_wait]]) ve `cli-self-host-denkligi` koşusu şu anda tam o dosyalarda çalışıyor.
**Kalan etki:** W5'in (start↔deploy denkliği) **pin'e bağlı olmayan** kısımları kapsamda kalır: runtime'ı yokla (palsvc değil), seçilen imajı yazdır, `upgrade` ölü-ucunu düzelt, `stop`'un compose'u ezmesini durdur.

### D-031 · Güvenlik/imzalama sorusu KONUSUZ kaldı
**Karar:** Soru manifesto imzalamayla ilgiliydi; manifesto kapsamdan çıkınca (D-030) imzalama da düşer. Kullanıcıya sorulmayacak.

### D-032 · Üç P0'ın TAMAMI düzelecek
**Karar:** *"tamamının çalışması lazım."* → `start`/`stop`, `init`→`build`, `flags list`, `notifications remove`, Android codegen: hepsi kapsamda.
**Not:** `init`→`build` (P0-2) düzeltmesi `build.go`/`devjs/build-check.js`'e dokunuyor ve **o dosyalar şu anda başka oturumun WIP'inde**. Çakışmayı önlemek için o kalem koordine edilecek, körlemesine yazılmayacak ([[feedback_one_writer_per_repo]]).

### D-033 · AÇIK SORU — ortam değiştirme (kullanıcı soru sordu, karar vermedi)
**Kullanıcı:** *"bir projenin birden fazla environment'ı olabilir onu değiştirebiliyor olmamız lazım sanki?"*
**Bu bir gereksinim beyanı ve haklı.** Sorumu yanlış çerçevelemişim: "seçim katmanını sök" seçeneği ortam değiştirmeyi KALDIRMIYOR — ama bunu seçenek metninde açıkça yazmadım. Ölçülen gerçek aşağıda; soru netleştirilip yeniden sorulacak.

### D-034 · YENİ GEREKSİNİM · Ortam = git branch gibi; ortamlar FARKLI KOD taşır
**Kullanıcı (2026-09-02):** *"environment'ların kodları da farklı olabilir. yani git branch gibi aslında o yapı. CLI'da projeyi linkleyince sadece environment'a checkout edebiliyor olmam lazım. change varsa eğer hata vermesi lazım, kod değişecek çünkü. orada ne yapacağız edeceğiz bilmiyorum."*
**Ne demek:** Ortam değiştirmek URL+anahtar takası DEĞİL — **çalışma ağacını da ilgilendiren bir checkout**. Ve `git checkout`'un kirli ağaçta reddetmesi gibi, yerelde commit'lenmemiş değişiklik varken ortam değiştirmek REDDEDİLMELİ.
**Dokunduğu yüzeyler:** `pull` (ortamın deploy edilmiş sürümünü indiriyor), `clone`, ortam başına `.palbase/openapi/<env>.json`, `Palbase/Generated/<env>/`, ve çalışma ağacının kendisi.
**Durum:** Kullanıcı çözümü bilmiyor ve bana bırakıyor → tasarım+araştırma kalemi, kullanıcıya sorulacak soru DEĞİL.
**Not:** Denetimdeki `refuseUseOnBoundProject` yalanı (*"one project with one environment — there is nothing to switch to"*) bu gereksinimin tam zıddını söylüyor; kesin gidecek.

### D-035 · `palbase start` DOĞRU projeyi kaldırmalı — mekanizma: CONFIG'te sürüm, ağ DEĞİL
**Kullanıcı:** *"palbase start ilgili projeyi start etmesi lazım. configlere versiyon koyabilirsin bence — supabase nasıl yapıyor bu işi?"*
**Okuma:** ⑩ (doğru stack) gereksinim olarak GERİ GELDİ, ama mekanizma D-030 ile uyumlu: **proje-yerel, commit'lenen config'te sürüm beyanı** — ağ manifestosu, sunucu ucu, filo yayım hattı YOK. Bu yüzden `v2-cloud` kulvarına hiç girmiyor ve "image işine girme" talimatıyla çelişmiyor.
**Araştırma açıldı:** Supabase'in yerel yığın sürümleme/`config.toml` modeli (kullanıcı doğrudan sordu) + "proje kendi çekirdek sürümünü beyan eder" deseninin diğer örnekleri.
**Ön bilgi (rs-pin-priorart):** Supabase CLI bugün imajları `//go:embed` ile binary'ye gömülü bir Dockerfile'da taşıyor, değişken tag, digest yok — yani bizim bugünkü hâlimizin aynısı. Ama `config.toml` tarafında sürüm alanları olup olmadığı AYRI bir soru ve ölçülecek.
