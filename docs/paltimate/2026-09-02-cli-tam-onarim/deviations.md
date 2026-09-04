# Sapma Defteri — Artım 1

Bu koşuda ortaya çıkan ama planın hiçbir görevine hizmet etmeyen her şey buraya yazılır ve **inşa edilmez**. Bitiş kapısında teklif listesine döner.

Biçim: `Bulundu / Cazip çünkü / Etkisi`

---

_(henüz kayıt yok)_
## D-01 · e2e suite'i ÖLÜ bir konağa bakıyor
**Bulundu:** `tests/e2e/mgmt_api_test.go:47` varsayılan taban `https://api.dev.palbase.studio` — denetimin ölçtüğü gibi bu adres kümesi hiç dağıtılmadı (`internal/config/config.go`: tek bulut).
**Cazip çünkü:** paket artık derlendiğine göre koşturulabilir hâle getirmek bir adım ötede duruyor.
**Etkisi:** Bu artımın FR'si yalnız DERLENMESİNİ istiyor (FR-007) ve CI onu yalnız `go vet` ile ölçüyor. Konağı düzeltmek suite'i canlı-bulut-kapılı hâle getirir, ayrı bir karar. Bitiş kapısında teklif edilecek.
