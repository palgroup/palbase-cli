# String dosyaları ve AI çevirisi — bu koşunun kaydı

Bu koşunun demeti (spec, plan, research, verify, deviations) backend deposunda duruyor:
`v2/docs/paltimate/2026-09-19-string-dosyalari-ai-ceviri/`.

CLI tarafında değişen: string tablosu `palbase/strings/` dizinine taşındı (`_meta.json` + dil başına `<etiket>.json`; `palbase build`
eski `palbase/strings.json`'ı göç ettirir), `palbase build --add <etiket>` yeni dili bütün cümleler `missing` olarak açar,
`palbase build --translate` eksik hücreleri projenin kendi yığını üzerinden OpenAI'ye çevirtip yer tutucu kapısından geçenleri
`needs_review` yazar, `palbase push` arşivde dizini taşır, her push'ta `strings-dir` beyan eder ve hedef yığının
`/.well-known/palbase.json`'da dizini okuduğunu söylemesini şart koşar; ulaşılabilirlik kapısı okuyucuyu artık `go/ast` ile bulur ve
bir betiği yalnız kendi test dosyasının çağırmasını ulaşılabilirlik saymaz.
