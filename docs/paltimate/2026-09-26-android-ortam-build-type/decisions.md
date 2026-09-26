# Android: build type ortamı seçer — karar günlüğü

**Açılış:** 2026-09-24 · **Şartname:** `./spec.md` · **Kanıt:** `./reports/`

## Intake (sohbetin tamamından)

| # | Kalem | Durum |
|---|---|---|
| 1 | Kişi başına feature ortamları (`featureX`, `featureProfileUpdate`…) Android'de build type olarak gelsin | ✓ FR-201.8 |
| 2 | `palbase {}` bloğu olmasın | ✓ FR-205 |
| 3 | Config'ler `app/src/<buildType>/` altına gelsin, `palbase/` klasörü olmasın | ✗ reddedildi — D-001 |
| 4 | Planın doğruluğundan %100 emin olunsun | ✓ prototip + matris + 2 eleştirmen + 5 doğrulayıcı (`reports/`) |
| 5 | Sonra birlikte sıfırdan bir projede denensin | tüketici görevi (`plan-plugin.md` son görev) |
| 6 | Klasör yapısı ve buildTypes'ın son hâli gösterilsin | ✓ `spec.md` → Hedef son durum |
| 7 | `initWith(debug)` gerçekten doğru mu | ✓ ölçüldü — D-019 |

## Kararlar

### D-001 · Dosyalar `palbase/environments/` altında kalır; build type yalnız SEÇER
**Karar:** Ortam dosyaları CLI'ın yazdığı yerde kalır; variant'ın build type'ı (ve yeni: flavor'ı) hangisinin derleneceğini seçer.
**Gerekçe (ölçüldü):** AGP `src/<bt>/` altındaki serbest JSON'u girdi saymıyor (APK'da yok, `sourceSets`'te yok); `main` bir build type olamıyor ("Multiple entries with same key: main"); `release` adında bir ortam açılırsa her geliştiricinin release APK'sı ona gider; `project.json`'un ve iOS/web'in ortak bir yere ihtiyacı var. (`reports/design-analysis-2026-09-24.md` §2)
**Reddedilen:** `app/src/<bt>/palbase/` — jüri 5–6/10, release'in adla ele geçirilmesi, iki düzen, kırıcı göç.
**Durum:** kullanıcı kabul etti (2026-09-24).

### D-002 · Library'de uyumsuz fallback, app variant'ını düşürür
**Karar:** Plugin bir library'deyse ve bir app variant'ı library'nin fallback variant'ından farklı bir ortam paketleyecekse, o app variant'ının `pre<Variant>Build`'i hata verir. `palbase.env.<appBuildType|appVariant>=<fallback ortamı>` kasıtlı olduğunu söyler.
**Gerekçe (ölçüldü):** Düzeltme turundaki uyarı, configuration cache yeniden kullanılınca tamamen sessiz; flavor'lı app'te (N1) hiç uyarı yok. `pre<Variant>Build` üzerinden hata hem cache kaydında hem yeniden kullanımda düşürüyor, debug/release ve IDE sync'i etkilemiyor (init-script probe).
**Reddedilen:** ortam damgası + app'te karşılaştırma — app plugin'i uygulamıyorsa app'te koşan hiçbir şey yok.
**Durum:** kullanıcı onayladı (2026-09-26).

### D-003 · `debug`'ın varsayılanı `local` kalır
**Karar:** Hiçbir anahtar yoksa debug `local`'i derler (2.3 ile aynı); release reddedilir.
**Gerekçe:** Geriye uyum; link zaten `palbase.env.debug=<varsayılan>` satırını basacak (FR-013); hata metni işe yarar yolu söyleyecek (FR-210).
**Durum:** kullanıcı onayladı (2026-09-26). Bağlı öneri: D-014.

### D-004 · Flavor seviyesi bu sürümde
**Karar:** `palbase.env.<flavor>` / birleşik flavor anahtarı ve product flavor'da `palbase { environment }` (`extendProductFlavorWith`, AGP 8.10.1–9.3.2'de var). Flavor ile build type çelişirse ret.
**Gerekçe:** D2 + N1: flavor'lı app'te bugünkü ret metni kullanıcıyı `stagingRelease`'i prod'a gönderen ayara yönlendiriyor.
**Durum:** kullanıcı onayladı (2026-09-26).

### D-005 · 2.5.0 (ilk karar 2.4.0), 11 artifact birlikte, tam "DAVRANIŞ DEĞİŞİKLİĞİ" listesiyle
**Karar:** Plugin, engine ve 8 runtime modülü **2.5.0** (D-030: 2.4.0 upstream'de başka bir değişiklikle yayımlandı); bu koşunun runtime'a kendi kod değişikliği yok (yalnız `PALBASE_SDK_VERSION` ve bir bekçi testi).
**Gerekçe:** `publish.sh` 11 artifact'ı tek sürümle çıkarıyor, plugin POM'u engine'i aynı sürüme pinliyor; ev geleneği (2.3.0 da kırıcı bir değişikliği minor'da "DAVRANIŞ DEĞİŞİKLİĞİ" başlığıyla çıkardı).
**Durum:** kullanıcı onayladı (2026-09-26).

### D-006 · Eski global `palbase.env`, build type adından ÖNCE
**Karar:** FR-201 adım 7, adım 8'den önce.
**Gerekçe (ölçüldü):** Test edilen sırada (ad önce) kullanıcının kendi projesi kırılıyor ve — daha kötüsü — aynı adlı dizin varsa 2.3 tüketicisi yükseltmede **sessizce** başka ortama geçiyor (`staging` APK'sı main yerine staging). Yama hazır ve ölçüldü: `reports/proto-2.4/order-fix.patch` (80/80 + 46/46).
**Sonuç:** Ad kuralını kullanmak isteyen, `palbase.env=…` satırını silip build type anahtarlarına geçer (göç notu).

### D-007 · Komut satırındaki `-Ppalbase.env`, o çağrıdaki bütün variant'lar için ikinci adım
**Gerekçe (ölçüldü):** 2.3 README'sinin CI tarifi (`-Ppalbase.env=staging`) aksi hâlde commit edilmiş `palbase.env.release=prod` tarafından sessizce eziliyor. `order-fix.patch` bunu da içeriyor.

### D-008 · Ortam slug dilbilgisi: camelCase
**Karar:** `^[A-Za-z][A-Za-z0-9-]{0,38}$`, ürün içinde **harf büyüklüğü gözetmeksizin benzersiz**; ayrılmış: `local` (her zaman), `main` (ilk ortam dışında). Tek bir sabitte (CLI ve sunucu şemasında aynı metin).
**Durum:** kullanıcı onayladı (2026-09-26) — camelCase. Plan ajanları: "onay bekliyor" ibaresi geçersizdir.
**Gerekçe:** camelCase AGP'nin build type geleneği — `featureX` ortamı `create("featureX")` ile eşlemesiz çalışır; slug host adında kullanılmıyor (host = ref), DNS kısıtı gereksiz; büyük/küçük harf duyarsız benzersizlik APFS ikizlerini kökten kaldırır; `/`, boşluk, nokta yok → dizin, Xcode ve AGP için güvenli.
**Alternatif (doğrulayıcının önerisi):** `^[a-z][a-z0-9-]{0,38}$` — daha dar; `featureX` yerine `featurex`/`feature-x`, build type'ta ya aynı küçük ad ya DSL eşlemesi.
**Etki:** CLI `env create` (FR-007), sunucu (FR-101/103), göç (FR-105).

### D-009 · `local.properties` debuggable olmayan variant'ta sayılmaz
**Karar:** FR-201 adım 3 yalnız debuggable variant'larda; debuggable olmayanda anahtar yok sayılır ve bir uyarı adını söyler.
**Gerekçe (ölçüldü):** unutulmuş bir `palbase.env.release=local` satırı loopback + cleartext bir release APK üretiyor ve build yeşil.

### D-010 · Bilinmeyen `palbase.env.*` anahtarı uyarı, modül `gradle.properties`'i ret
**Gerekçe:** Kök dosya bütün modüllerce paylaşılır; bir app build type'ının anahtarı library için "bilinmeyen"dir — hata yanlış olur. Modül dosyası bugün sessizce yok sayılıyor (ölçüldü) — ya okunmalı ya reddedilmeli; tek kaynak ilkesi için ret.

### D-011 · CLI ad kapısı: listede gevşek, `env create`'te sıkı
**Karar:** Link/spec'te yalnız "tek temiz yol parçası" (FR-001) — bugün kullanılan `Production`, `featureX`, `feature-profile-update` gibi adlar kırılmasın. `env create`'te D-008.
**Gerekçe:** Sunucu göçü (FR-105) gelene kadar mevcut adlar geçerli kalmalı; güvenlik sınırı yol parçası kuralıdır.

### D-012 · Ad çakışmasında varsayılan dahilse link durur
**Karar:** FR-003. Varsayılan olmayan çakışanlar atlanır; varsayılan çakışıyorsa hata.
**Gerekçe:** Sessizce birini seçmek, `link` B'yi yazarken `push`'un A'ya gitmesi demek (ölçüldü). Bir üyenin `Main` açarak herkesi durdurabilmesi geçici; FR-101'in benzersizliği bu yolu kapatır.

### D-013 · Cloud planı `origin/main`'e göre yazılır
**Gerekçe:** Yerel `palbase-cloud` kopyası `origin/main`'in 1685 commit gerisinde ve dizin düzeni farklı (`v2-cloud/` → `cloud/`). Yürütmeden önce `git pull`.

### D-014 · `palbase/environments/local/` commit edilmez
**Karar:** Link, `.gitignore`'a `palbase/environments/local/` ekler (dosya varsa satırı ekler, yoksa oluşturur; satır zaten varsa dokunmaz); doctor commit edilmiş bir kopyayı uyarır (FR-020).
**Gerekçe:** `local/` bu makinenin portunu ve anahtarını taşıyor; D-003 ile debug varsayılanı `local` olduğundan, commit edilirse takım arkadaşının debug build'i son link yapanın laptop yığınını derler.
**Durum:** kullanıcı onayladı (2026-09-26). Plan ajanları: bu iş KOŞULLU değil, normal bir görevdir.

### D-015 · İlk ortamın slug'ı her projede `main`
**Karar:** FR-102/105; panelden açılmış projelerde de (`Production` → slug `main`, görünen ad `Production` kalır).
**Sonuç:** Böyle bir projede bir sonraki link `main/` yazar ve `Production/`'ı temizler; `palbase.env=Production` kullanan build **yüksek sesle** düşer ("carries main") — sessiz değil. Göç notunda yazılır.

### D-016 · CLI listesindeki `name` slug'ı taşır
**Gerekçe:** Eski CLI sürümleri dizin adını `name`'den alıyor; sunucu `name`'e slug koyarsa eski CLI'lar da güvenli ada geçer. Görünen ad `display_name`'de.

### D-017 · Paketlenen config ortam adını taşır
**Karar:** `palbase_environment` alanı (FR-207). Runtime `ignoreUnknownKeys = true` (`Codec.kt:23-24`) — güvenli.

### D-018 · Kotlin DSL tuzağı
**Karar:** Proje eklentisinde `@Deprecated(level = ERROR) var environment` + getter/setter `GradleException` (Groovy Kotlin deprecation'ını görmez). `reports/proto-2.4/trap.patch` (79/79 + 46/46; AGP 8.10.1 ve 9.1.1).

### D-019 · Feature build type'ları `initWith(getByName("debug"))` ile
**Karar (ölçüldü 2026-09-25, AGP 8.11.1):** `initWith` olmadan build type imzasız ve debuggable değil (Run ile yüklenmez); `initWith(debug)` debuggable + debug imzalı. `initWith` o andaki değerleri kopyalar: `debug {}` bloğu `create(...)`'ten ÖNCE yazılmalı (ölçüldü: sonra yazılan `applicationIdSuffix` kopyalanmadı). `matchingFallbacks += listOf("debug")` yalnız Android library modülü varsa gerekli (yokken hata ölçüldü: "No matching variant of project :alib"). `initWith` Palbase ortamını kopyalamaz. `src/debug` ve `debugImplementation` gelmez.

### D-021 · Başka ortamlar varken `main` silinemez
**Sorun (planlamada bulundu, plan-cloud A-1):** D-008 "`main` ilk ortama ait" diyor ama silmeyi söylemiyor. Silme sagası satırı siliyor; `main` silinen proje `main`'siz kalır ve `palbase.env.release=main` build'leri düşer.
**Karar:** Projede başka bir ortam varken `main`'i silme isteği (CLI, panel, API — hangi yoldan gelirse) sunucuda reddedilir ve kural söylenir; `main` ancak projenin son ortamıysa silinebilir. Proje hiç ortamsız kalırsa bir sonraki ortam `main` olur.
**Reddedilen:** `main` silinebilir, sonraki yeni ortam `main` olur — release build'i arada kırılır ve kişi slug vermeden yalnız ad yazdığında ortamı beklenmedik biçimde `main` olur.
**Durum:** kullanıcı onayladı (2026-09-26).

### D-022 · Oluşturma formu yalnız kişinin yazdığı slug'ı gönderir (FR-107 daraltıldı)
**Karar (planlamada, plan-cloud A-2):** Form türetilen slug'ı düzenlenebilir gösterir ama kişi değiştirmediyse göndermez; sunucu aynı kuralla türetir ve ilk ortamda `main` yazar.
**Gerekçe (ölçüldü):** Formun ortam listesi erişime göre süzülü; "bu proje boş mu" formdan bilinemez. Her zaman gönderen form, ortamı düşmüş yarım projede "Production" için 400 alıyordu.

### D-023 · Terminale basılan ortam adı: düz kelime değilse tırnaklı (FR-006)
**Karar (planlamada, plan-cli):** `envname.Label`, yalnız `^[A-Za-z0-9][A-Za-z0-9_-]*$` olmayan adları `%q` ile basar (git `core.quotePath` gibi).
**Gerekçe:** Kaçış baytları hiçbir yolda terminale ulaşmaz; düz adlar (`staging`) okunaklı kalır ve mevcut altı test iddiası değişmez. Şartnamenin "her zaman `%q`" metni bu karar lehine yorumlanır.

### D-024 · Projesiz link, bu makinenin eski loopback `main/`'ini temizler
**Karar (planlamada, plan-cli):** Liste yoksa Android/web süpürmesi koşmaz (D-011 ruhu); tek istisna: projesiz ve ortamı `local` olan bir link, `main/`'in her config'i loopback bir adres taşıyor ve dizinde yalnız CLI dosyası varsa onu siler (T014).
**Gerekçe (ölçüldü):** Eski CLI'lar start/loopback link'ini `main/` diye yazıyordu; FR-010 "main/ hiçbir yolda loopback taşımasın" diyor, ama liste olmadığı için FR-011'in süpürmesi onu hiç görmüyordu.

### D-025 · FR-016 yalnız Gradle dizinlerine uygulanır
**Karar:** Bağlı bir checkout'un altındaki bir **Gradle dizininde** link reddedilir; yürüyüş, üst dizini bağlı olmayan bir Gradle kökünde biter (monorepo'da kendi Gradle kökü olan Android app serbest; RN'in `android/`'ı, kökü bağlıysa reddedilir). Gradle'sız dizinler (monorepo'daki web/iOS app'leri) etkilenmez.
**Gerekçe:** FR-016'nın amacı D3a — Android modülünün içinde ikinci bir `palbase/` doğması. Taslak kural, bugün (`20e5d7e`) çalışan monorepo web/iOS link'lerini de reddediyordu; bu bir gerileme olurdu.
**Durum:** lead kararı (2026-09-26); T019 buna göre daraltılır.

### D-026 · CLI iki dalgada çıkar
**Karar:** Dalga 1 (T001–T018, T020–T022) hemen yayımlanır; Dalga 2 (T019, T023–T026) plugin 2.5.0 yayımlanmadan `main`'e girmez.
**Gerekçe (ölçüldü):** Dalga 2, 2.5.0 koordinatlarını ve 2.5'e özgü `palbase.env.<build type>` anahtarlarını basıyor; 2.3 yalnız global `palbase.env`'i okuyor (v2.3.0 kaynağıyla doğrulandı). Önce çıkarsa kullanıcıya olmayan bir sürümü önerir. **T019 da Dalga 2'de** (D-025 doğrulayıcısı): reddi plugin 2.4'ün arama sırasına dayanır; yayındaki 2.3 blok yoksa yalnız modülün `palbase/environments`'ini okur (`v2.3.0` `PalbaseCodegenPlugin.kt:15`), yani Dalga 1'de bloksuz bir 2.3 app'inin tek çalışan link yerini reddederdi. Sıra değişikliği ölçüldü: T020–T022 T019'a bağlı değil, son ağaç aynı.

### D-027 · `local` için "sözleşme yok" çaresi `palbase start`, push değil (FR-210)
**Karar (planlamada, plan-plugin A-1):** Sözleşmesi olmayan ortamda plugin, bulut ortamları için "`palbase push --env <env>`, then `palbase link` here" der; `local` için "run `palbase start` in the backend (its stack serves the contract of the code it runs; `palbase push` does not publish to it), then `palbase link` here".
**Gerekçe (kodla doğrulandı):** CLI çalışan yerel yığına push'u reddediyor (`internal/backend/stack_push.go:162`) ve `--env local` projenin bir ortamı değil (`environments.go:337`). FR-009'un öneri cümlesiyle aynı yol.

### D-028 · Blok ↔ dosya reddi yalnız AYNI build'ler için (FR-202)
**Karar (planlamada, plan-plugin A-2):** Bir `palbase { environment }` bloğu, yalnız kendi kapsamındaki `gradle.properties` anahtarıyla çelişirse reddedilir: build type bloğu için `[variant, buildType]`, flavor bloğu için flavor ya da onu içeren kombinasyon. Başka build'leri de kapsayan bir anahtarı (flavor bloğu ↔ `palbase.env.release`) blok geçer.
**Gerekçe (ölçüldü):** Taslağın geniş reddi FR-201'in "4, 5'ten önce" sırasına ve FR-202'nin "aynı build type için" metnine aykırıydı. Üstelik FR-013'ün her app'e bastırdığı `palbase.env.debug`/`palbase.env.release` satırlarıyla flavor bloklu her modülü reddediyordu.

### D-029 · Kök araması sınırı kök projede ölçülür (FR-205)
**Karar (planlamada, plan-plugin A-3):** `<rootDir>/../palbase/environments` adayı, kök proje `.git` ya da `palbase/project.json` taşıyorsa aranmaz (kök proje checkout'un kendisidir); işaretsiz bir kök proje (RN/Flutter'ın `android/`'ı) bir üstünü arar. Üst dizinin işaret taşıması istenmez.

**Takip (plan-plugin A-6):** FR-212'nin alt sınırı (AGP 8.10.1 / Gradle 8.11.1) yalnız README'lerde yazılı; çalışma anında sürüm denetimi yok (çevrimdışı ölçülemedi; AGP 8.10.1 zaten Gradle 8.11.1 istiyor).

### D-030 · Plugin tabanı `origin/main` + yerel dağıtım commit'i; sürüm 2.5.0
**Olay (2026-09-27):** `palbackend-android-src` `origin/main` üç commit ilerledi (`233991d` sınıflandırılmış hata tipleri — codegen-engine `KotlinEmitter` ve palbe-core `ErrorEnvelope`'a dokunuyor; `6e97598` **"release: palbackend-android 2.4.0"**; `3588bc2` CI). Planın tabanı `e72f704` ("public distribution repo") ise yalnız yerel depoda — origin'de yok. Yayın deposunda (palbackend-android) yalnız 2.3.0 var.
**Karar:** Plugin planı `origin/main` (`3588bc2`) + üstüne taşınmış `e72f704` tabanına alınır (upstream'in 2.4.0 CHANGELOG bölümü ve `Son etiket: v2.4.0` korunur); bu koşunun sürümü **2.5.0**. Plan, bu tabanda yeniden ölçülerek güncellenir (sandbox'ta yeniden oynatma + doğrulayıcı). Yerel `main`, yürütmeden önce aynı biçimde `origin/main`'e taşınır.
**Etki:** plan-cli T023'ün `palbaseAndroidVersion` sabiti `"2.5.0"`; spec FR-212/FR-301 ve D-026 metinleri 2.5.0.

### D-031 · Upstream ilerlemesi yürütme sırasında taşınır
**Karar (2026-09-27):** `palbase-cli` yerel `main`'i (iki doküman commit'i + T001) `origin/main` `5dbc354`'ün üstüne taşındı (9 upstream commit; plan-cli dosyalarıyla çakışma yok; yeni taban ölçüldü: yalnız B16, 24 paket `ok`). `palbase-cloud` planı, `origin/main`'in `f92bc1e02` → `bb547a702` ilerlemesi (planın 11 dosyasına dokunuyor) nedeniyle sandbox'ta yeni tabana taşınıp yeniden ölçülüyor.

### D-020 · Prototip kurtarıldı; ham çıktı `/private/tmp`'de bırakılmaz
**Olay:** macOS 2026-09-26'da `/private/tmp/claude-501`'i temizledi; prototip, yamalar ve sandbox'lar silindi.
**Kurtarma:** ajan transkriptlerindeki Write/Edit/Bash çağrıları sırayla yeniden oynatıldı; sonuç birebir (13 dosya, +1982/−110; 23+8+47 test yeşil); iki yama da orijinal uzunlukta (83/63 satır). Hepsi `reports/proto-2.4/`'te.
