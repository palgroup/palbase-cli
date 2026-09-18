# Backend string tablosu — CLI tarafı (2026-09-18)

Ana demet: palgroup/palbase `docs/paltimate/2026-09-18-string-tablosu-kiraci/` (spec, plan, research,
verify). Bu dosya işin palbase-cli'daki kısmının kaydı.

**Ne değişti:** kiracının backend'i `t("…")` yazıyor; `palbase build` bu literal'leri deploy ağacından
toplayıp `palbase/strings.json`'a BİRLEŞTİRİYOR (çeviriyi ezmeden) ve `palbase push` tabloyu deploy
arşivinde taşıyor. Yığın tabloyu artifact'la alıp her isteği kendi `Accept-Language`'inde çözüyor.

## CLI'ın taşıdığı kararlar

- **D-10 — yazım biçimi.** Üst alanlar `version`, `source`, `locales`, `strings`; anahtarlar ve hücre
  dilleri UTF-8 bayt sırasıyla; `locales` kaynak dil başta; 2 boşluk; HTML kaçışı yok; tek `\n`. Aynı
  kodla iki build bayt bayt aynı dosyayı bırakır ve aynı baytları yeniden yazmaz.
- **D-12 — tarama.** `devjs/strings_scan.js`, build'in sabitlenmiş TypeScript'iyle (`devNodePath`)
  derleyicinin sembol çözümünü kullanır: yalnız `@palbase/backend`'in `t`'si sayılır (adlandırılmış,
  takma adlı, ad alanı); yerel `t`, gölgelenen parametre, başka paketin `t`'si sayılmaz. Literal olmayan
  argüman `dosya:satır` ile uyarılır.
- **D-14 — arşiv.** `palbase/` dizininden yalnız kökteki `palbase/strings.json` taşınır
  (`shipsFromPalbase`); sözleşme, `project.json` ve iç içe `web/palbase/strings.json` dışarıda kalır.
- **D-17 — etiketler.** `golang.org/x/text` v0.41.0 doğrudan bağımlılık; `canonicalLocale` ve tablo
  doğrulayıcısı yığının `locale.Canonical` / `locale.Parse` kurallarının aynısı — yığının reddedeceği bir
  tablo yazılmaz.
- **D-18 — dil çıkarma.** `locales`'ten silinen bir dilin hücreleri okurken düşürülür ve basılır; diğer
  her ihlal build'i reddeder.
- **D-19 — adımın yeri.** Tablo adımı `landEnvTypes`'tan hemen sonra, controller kapılarından önce koşar.
  Tarayıcı koşamazsa tabloya dokunulmaz ve bu tek satırda söylenir (taranmamış anahtar kümesi asla
  anahtar silmez).

## Dosyalar

`internal/backend/layout.go` (`StringsPath`) · `archive.go`, `archive_test.go` ·
`strings_table.go`, `strings_table_test.go` · `devjs/strings_scan.js`, `devjs/strings_scan.test.js`,
`devjs_node_test.go`, `backend.go` (gömme) · `build.go` (`--source`, `runBuildWith`,
`landStringsTable`), `build_test.go` · `go.mod`, `go.sum`.
