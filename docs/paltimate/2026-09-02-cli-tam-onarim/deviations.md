# Sapma Defteri — Artım 1

Bu koşuda ortaya çıkan ama planın hiçbir görevine hizmet etmeyen her şey buraya yazılır ve **inşa edilmez**. Bitiş kapısında teklif listesine döner.

Biçim: `Bulundu / Cazip çünkü / Etkisi`

---

_(henüz kayıt yok)_
## D-01 · e2e suite'i ÖLÜ bir konağa bakıyor
**Bulundu:** `tests/e2e/mgmt_api_test.go:47` varsayılan taban `https://api.dev.palbase.studio` — denetimin ölçtüğü gibi bu adres kümesi hiç dağıtılmadı (`internal/config/config.go`: tek bulut).
**Cazip çünkü:** paket artık derlendiğine göre koşturulabilir hâle getirmek bir adım ötede duruyor.
**Etkisi:** Bu artımın FR'si yalnız DERLENMESİNİ istiyor (FR-007) ve CI onu yalnız `go vet` ile ölçüyor. Konağı düzeltmek suite'i canlı-bulut-kapılı hâle getirir, ayrı bir karar. Bitiş kapısında teklif edilecek.

## D-02 · ÇEKİRDEK SÜRÜM SÜRÜKLENMESİ — mevcut, üst akışta, ve CI'da GÖRÜNMÜYOR
**Bulundu (2026-09-04, T003 sırasında):** `go test ./internal/backend/` yerelde kırmızı:
`TestStackImagesTrackTheCoreVersion` → CLI üç imajı **0.40.1**'e pinliyor, otorite
`v2-cloud/bootstrap/images/version.env` ise **V2_VERSION=0.41.0** diyor.

**Benim değişikliklerimden DEĞİL:** değişikliklerim stash'liyken HEAD'de de aynı test düşüyor (ölçüldü).

**Zincir ölçüldü:** `v2/deploy/docker-compose.dev.yml` (ORİJİNAL) hâlâ 0.40.1 · CLI'ın vendor'ladığı
kopya da 0.40.1 (parite kapısı bu yüzden yeşil) · yalnız `version.env` 0.41.0'a çıkmış.
Üç imajın 0.41.0 etiketi registry'de **VAR** (`docker manifest inspect` ×3).
→ CLI'ın sabitini tek başına bump'lamak "sabitler == compose varsayılanları" kapısını kırar;
compose'u da bump'lamak "vendor == v2/deploy orijinali" kapısını kırar. **Zincir yukarıdan
başlamalı: önce `v2/deploy`.**

**Neden ben yapmıyorum:** `v2` başka bir oturumun canlı WIP'ini taşıyor (analytics koşusu,
değişmiş run dosyası, derlenmiş `palsvc`) → [[feedback_one_writer_per_repo]] +
[[feedback_infra_not_my_lane_just_wait]].

**VE ASIL BULGU — bu, koşunun tezinin bir örneği daha:** CI **yeşil**. Çünkü bu kapı
`version.env`'i kardeş depodan okuyor ve CI runner'ında o depo yok → **atlıyor**.
Denetimin E-8'i tam bu: ölçebildiği yerde kırmızı, ölçemediği yerde yeşil.
FR-010 bu sınıfı `npm install` için kapatıyor; **kardeş-checkout atlamaları için kapatmıyor.**

**Etkisi:** T011'in CI kapılarını engellemiyor (orada atlıyor). Yerelde tam suite'in
"tamamen yeşil" olmasını engelliyor — T010 ve bitiş doğrulaması bunu AÇIKÇA raporlayacak,
yeşilmiş gibi göstermeyecek.

## D-03 · `push` idempotency'si BEYAN EDİLMİŞ ama HİÇ GÖNDERİLMİYOR (E-2'nin yarısı)
**Bulundu:** `deploy.go` ölü alanları silinirken: transport `PostMultipart(..., idempotencyKey)` destekliyor, `runPush` hiç geçmiyor.
**Cazip çünkü:** alanlar zaten oradaydı, "bağlamak" tek satır gibi duruyor.
**Etkisi:** Zaman aşımına uğrayıp aslında inen bir yükleme İKİNCİ DEPLOY olarak gider. Gerçek bir kusur ama bu artımın hiçbir FR'si değil; T003 yalnız ölü beyanı ve onun yalanını kaldırdı. Bitiş kapısında teklif edilecek.

## D-02 GÜNCELLEME (2026-09-04) · ÇEKİRDEK SÜRÜKLENMESİ ÇÖZÜLDÜ — başka oturum tarafından
T010 sırasında iki test aniden kırmızıya döndü (`TestTheGoConstantsAndTheComposeDefaultsAgree`,
`TestTheImageCheckAsksForTheTAGCOMPOSEUSES`). Araştırıldı: **benim değişikliklerimden değil** —
`0.41.0` hiçbir commit'te yoktu, yani başka bir oturum `sdk/cli`'da CANLI yazıyordu ve
sabitleri bump'layıp compose'a henüz geçmemişti. Ölçüm o ara durumu yakalamış.
Zincir tamamlanınca dördü de yeşil: `TestStackImagesTrackTheCoreVersion` dahil.
**D-02 kapandı ve düzelten ben değilim** — doğru kulvarda kaldı.
**Alınan ders:** aynı depoda eşzamanlı yazıcı varken `git add <dizin>` tehlikeli;
bu koşunun kalan commit'leri DOSYA DOSYA pathspec kullanıyor.
