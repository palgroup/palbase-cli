# Eski `project.json` okunamıyor — şartname

**Tarih:** 2026-09-14 · **Parkur:** hızlı (tek belge; okuma yolunun geriye uyumluluğu, mimari değişiklik yok)
**Yetki:** kullanıcı 14.09.2026'da "Sen düzelt ve yayınla" dedi (palbase-cloud PITR koşusu, `deviations.md` D-13).

## Problem

12.09.2026'dan önce bağlanmış bir checkout'ta CLI hiçbir fiili koşamıyor. `status`, `link`, `push` hepsi şu hatayla
düşüyor:

```
read palbase/project.json: json: unknown field "stackVersion"
read palbase/project.json: json: unknown field "env"
```

**Debug bloğu**
- REPRO: geçici bir dizinde `palbase/project.json` = `{"url":"https://8bbwb2pbm.palbase.studio","stackVersion":"39"}` →
  `palbase status` → yukarıdaki ilk satır. `{"url":…,"project":"prd_example","env":"main"}` → ikinci satır. Main HEAD
  `94c89bd`den derlenen ikili ve kurulu 0.67.1 aynı çıktıyı veriyor (2026-09-14T03:55Z). Canlı karşılığı:
  `centauri-backdoor` (`8bbwb2pbm`).
- H1: göç ya da `link` eski dosyayı yeniden yazar · H2: okuma yolu bilinmeyen alanı reddediyor ve göç okunamayan
  dosyaya ulaşamıyor · H3: dosya hiçbir CLI sürümünün yazmadığı bir alan taşıyor.
- DISCRIMINATOR:
  - H3 elendi: `Target.Env` `f0ce8bd`den (v0.29.0) `1fcefcb`ye (12.09) kadar vardı ve v0.29.0–v0.64.0'ın hepsinde
    yazılabiliyordu. `Target.StackVersion` 09-05'ten `1fcefcb`ye kadar vardı. `1fcefcb` ("project.json ortam-bağımsız
    kimlik taşır, stackVersion düşer") ikisini de kaldırdı; v0.65.0 ve sonrası bu commit'i taşıyor.
  - H1 elendi: `MigrateLegacyTarget` dosyayı `readLinkedProject()` ile okuyor ve hata alınca `nil` dönüyor
    (`internal/backend/environments.go`). `link` dosya varken okuma hatasını döndürüyor
    (`internal/backend/project_link.go`, hedefsiz ve hedefli iki yol).
- OBSERVED: `decodeTarget` → `authcontract.DecodeStrict` → `d.DisallowUnknownFields()`. `DecodeStrict`in tek çağıranı
  `decodeTarget`.
- Kapının kör noktası: eski dosyayı kullanan tek test (`TestAnOldDeclaredStackVersionDoesNotOverrideTheInstalledSDK`)
  yalnız `stackVersion()`ı çağırıyor ve okuma yolunu hiç ölçmüyor.

**`env`in tarihi (inceleme tur 1, IMPORTANT-2 — FR-5'in gerekçesi).** Dosyadaki `env` otuz sürümdür bir YÖNLENDİRME
değildir:
- v0.29.0–v0.33.x: `palbase env <slug>` committed dosyaya `{project, env}` yazıp `url`i siliyordu
  (`fda7a7b^:internal/backend/env_switch.go:73-80`).
- v0.34.0 (`fda7a7b`) bu yazıcıyı kaldırdı. v0.34.0–v0.64.0 arasındaki her etikette `Target.Env`in üretimdeki tek
  okuyucuları `Target.Describe` (banner etiketi) ve `defaultEnvName` (artifact dizini adı); hiçbir çözümleyici onu
  okumuyor. v0.64.0 adresi olmayan dosyayı `has no address — run palbase link <ref> again` ile reddediyordu.
- v0.65.0–v0.67.1 dosyayı hiç okuyamıyor.
- `Target.Project` yorumu tehlikeyi adlandırıyor: "A committed environment is how a colleague pulls your branch and
  pushes to your staging."

## Gereksinimler

- **FR-1** WHEN committed `palbase/project.json` ya da makine-yerel kayıt emekli `stackVersion` alanını taşıdığında THEN
  okuma (`readLinkedProject`, `ReadTarget`) SHALL emekli alan yüzünden düşmez. Fiilin sonucu bugünkü çözümleme
  kurallarına kalır (kimlik bilgisi, ortam seçimi). Alanın değeri hiçbir kararda kullanılmaz; yığın sürümü kurulu
  SDK'dan türetilmeye devam eder. Yalnız committed dosya yeniden yazılır (FR-4); makine-yerel kayıt yeniden yazılmaz.
- **FR-2** WHEN committed dosya ya da makine-yerel kayıt emekli `env` alanını taşıdığında THEN okuma SHALL emekli alan
  yüzünden düşmez.
- **FR-3** IF dosya `Target`in hiçbir sürümünde var olmamış bir alan taşırsa THEN okuma SHALL bugünkü gibi o alanı
  adlandırarak düşer. Tanınan emekli alanlar yalnız `stackVersion` ve `env`; tam bu yazımla. Harf varyantları
  (`"STACKVERSION"`, `"Env"`) tanınmaz ve adlarıyla reddedilir. Joker yoktur.
- **FR-4** WHEN bir fiil emekli alan taşıyan bir checkout'ta koştuğunda ve var olan adres → kimlik göçü dosyayı
  yazmadığında THEN `MigrateLegacyTarget` SHALL committed dosyayı emekli alanlar olmadan yeniden yazar ve ne yaptığını
  tek satırla basar. Ağ gerekmez.
- **FR-5** WHEN committed dosya emekli `env` taşıdığında ve emekli alan temizliği koştuğunda THEN göç SHALL o değeri
  hiçbir yere TAŞIMAZ.
  - Temizlik, adres → kimlik göçü dosyayı yazmadığında koşar: adres yoktur, adres bir bulut adresi değildir ya da ürünü
    çözülememiştir. `env` tek emekli alan olabilir ya da `stackVersion` ile birlikte gelebilir.
  - Makine-yerel seçim yazılmaz, var olan seçim değişmez. Alan düşer.
  - Basılan satır düşen değeri en çok 64 rune'a kırpılmış ve Go tırnaklamasıyla (`%q`) adlandırır; ortamın nasıl
    seçileceğini söyler (`--env <name>`, `palbase env use <name>`).
  - Ortam bugünkü kurallarla çözülür: tek ortamlı projede o ortam, çok ortamlıda bugünkü red.
  - WHEN adres → kimlik göçü dosyayı yazdığında (bulut adresi ve çözülen ürün) THEN ortam FR-7/D-2'nin kuralıyla
    ADRESTEN gelir; v0.64'te de yönlendiren adresti. Bu göçün satırı düşen `env` değerini aynı biçimde adlandırır.
  - Committed bir `env` hiçbir koşuda yönlendirme yapmaz.
- **FR-6** IF committed dosyanın yeniden yazımı düşerse (emekli alan temizliğinde ya da adres → kimlik göçünde) THEN
  dosya SHALL bayt bayt aynı kalır, satır basılmaz, makine durumu değişmez ve fiil yine koşar. Göç en iyi çabadır
  (mevcut FR-061 kuralı; `MigrateLegacyTarget`in kendi yorumu da "a migration that FAILED must leave the checkout
  exactly as it was … the verb carries on" diyor). Adres göçünün yazım hatası bugün fiili düşürüyor; bu düzeltmenin
  kapsamındadır.
- **FR-7** WHEN adres → kimlik göçü dosyayı yazdığında THEN yazılan dosya SHALL emekli alanları taşımaz (`WriteTarget`
  `Target`i serileştirir; emekli değerler dışa açık olmayan bir alanda tutulur ve serileştirilmez).
- **FR-8** IF emekli alan taşıyan bir dosya `DecodeStrict`in yapısal kurallarından birini çiğnerse (yinelenen anahtar,
  `null`, 256 KiB sınırı, derinlik, artık JSON) THEN okuma SHALL bugünkü yapısal hatayla düşer. Kurallar dosyanın
  YAZILDIĞI hâline uygulanır: emekli değerin içindeki `null` ya da yinelenen bir emekli anahtar da reddedilir.
- **FR-9** WHEN bir dosya emekli alan taşıdığında THEN çözülen `Target` SHALL aynı dosyanın emekli alansız hâlinden
  çözülenle aynıdır. Emekli alanın varlığı kalan alanların yorumunu (anahtar sırası, harf varyantı olan anahtarlar,
  HTML'de kaçırılan karakterlerin boyutu) değiştirmez.

## Kararlar

- **D-1 · Tek çözüm, yeniden serileştirme yok.** `decodeTarget` dosyayı tek bir `authcontract.DecodeStrict` çağrısıyla,
  yazıldığı hâliyle çözer. Çözüm biçimi `Target` ile iki isteğe bağlı emekli dizedir (`stackVersion`, `env`); bu biçim
  `decodeTarget`e özeldir. Yapısal kurallar ve `DisallowUnknownFields` böylece dosyanın kendisine uygulanır (FR-8) ve
  kalan alanlar yeniden serileştirilmediği için FR-9 tutar. Emekli değerler `Target`in dışa açık olmayan alanına konur.
  `encoding/json`un alan eşleşmesi harf duyarsız olduğu için emekli adların harf varyantları ayrıca adlarıyla
  reddedilir (FR-3). [PLAN-FREE: çözüm biçiminin ve yardımcıların adı; harf varyantının nasıl saptandığı]
- **D-2 · Göç tek yerde ve ağsız.** Emekli alanların temizliği `MigrateLegacyTarget`tedir (fiil başında `PrintResolvedTo`
  onu zaten çağırıyor). Sıra: var olan adres → kimlik göçü önce denenir. Dosyayı yazdıysa emekli alanlar zaten
  düşmüştür (FR-7). Yazmadıysa emekli alan temizliği ayrı koşar ve yalnız dosyayı yazar; ortam listesi okunmaz, seçim
  yazılmaz (FR-5).
- **D-3 · Basılan satır.** Mevcut göç satırının biçiminde: hangi dosyanın hangi emekli alanları bıraktığını söyler.
  `env` düştüyse değerini en çok 64 rune'a kırpıp `%q` ile adlandırır ve ortamın nasıl seçileceğini söyler. Adres göçünün
  satırı da düşen `env`i aynı biçimde adlandırır. Bu satırlar ham değer basmaz: göç satırı üzerinden committed bir dosya
  terminale kontrol karakteri koyamaz. Ürünün başka yolları için kapsam dışı bulgu 4'e bakın. Satır yalnız yazım
  başarılıysa basılır (FR-6). [PLAN-FREE: satırların tam İngilizce metni ve kırpma işareti]
- **D-4 · `DecodeStrict` değişmez.** Emekli alan bilgisi `authcontract`e sızmaz; o paketin öteki sözleşmesi (yerel
  seçimlerin yapısal kuralları) aynı kalır.

## Dosya haritası

| Yol | Değişiklik | FR |
|-----|-----------|----|
| `internal/backend/target.go` | `decodeTarget` tek çözümle emekli alanları tanır; `Target`e serileştirilmeyen emekli değer alanı | FR-1, FR-2, FR-3, FR-7, FR-8, FR-9 |
| `internal/backend/environments.go` | `MigrateLegacyTarget` emekli alan temizliği; `env` düşer, taşınmaz | FR-4, FR-5, FR-6 |
| `internal/backend/target_test.go` | okuma yolu kapıları | FR-1, FR-2, FR-3, FR-7, FR-8, FR-9 |
| `internal/backend/migration_selection_test.go` | göç kapıları | FR-4, FR-5, FR-6 |
| `docs/paltimate/2026-09-14-eski-project-json/spec.md` | bu belge | — |

## Kapılar ve kanıt

- **RED önce:** her yeni ya da değişen test, düzeltmeden ÖNCE (`94c89bd`) koşulur ve kendi iddiasında kırmızıdır. Tur 2'nin
  yeni ya da değişen testleri ayrıca `f42ce18`e karşı da koşulur. FR-5 ve FR-9 testleri orada kendi iddiasında kırmızı
  olmalı. FR-8'in yapısal kurallarını `f42ce18` de uyguluyordu, eksik olan onları tutan testti. Bu yüzden FR-8'in
  kırmızı kanıtı iki tabanda değil, yapısal denetimi kaldıran mutasyondadır. FR-3'ün harf varyantı durumları için de
  aynısı geçerli: iki taban da o anahtarları bugünkü `unknown field` metniyle reddediyordu, bu yüzden kırmızı kanıt
  harf varyantı reddini kaldıran mutasyondadır. Çıktılar rapora.
  - okuma: `{"url":…,"stackVersion":"39"}`, `{"project":…,"env":…}` ve
    `{"url":…,"project":…,"env":…,"stackVersion":…}` `readLinkedProject` ile hatasız okunur. `ReadTarget` adresli iki
    dosyada hatasız döner; `{project, env}` dosyasında çözümleme hatası yerine bugünkü `names a project, not an address`
    kimlik reddini döndürür (FR-1, FR-2).
  - makine-yerel kayıt `{url:loopback, stackVersion, env}` `ReadTarget` ile okunur ve fiilden sonra bayt bayt aynıdır
    (FR-1).
  - `{"url":…,"bogus":1}`, `{"url":…,"STACKVERSION":"39"}` ve `{"url":…,"Env":"main"}` alanı adlandırarak düşer (FR-3).
  - yapısal: yinelenen `env` → `duplicate field`; `"env": null` → `null is not accepted`; `stackVersion` içinde
    256 KiB'ı aşan değer → `exceeds 256 KiB` (FR-8).
  - denklik: emekli alanlı dosyayla emekli alansız hâlinden çözülen `Target`ler eşit. Durumlar: harf varyantı olan
    anahtarlar (`url` ve `URL`) ve 256 KiB sınırına yakın, `<` taşıyan bir `name` (FR-9).
  - göç:
    - `{url, stackVersion}` + çözülemeyen ürün → dosya `stackVersion`sız yeniden yazılır, satır basılır (FR-4).
    - Adres göçü çalıştığında yazılan dosyada emekli alan yok (FR-7).
    - `{project, env:"staging"}` → dosyada `env` yok, seçim YAZILMAZ, satır `"staging"`i tırnaklı adlandırır (FR-5).
    - Aynı dosya, var olan başka bir seçimle → seçim bayt bayt aynı (FR-5).
    - `{url, env}` → `env` düşer, satırda adlandırılır (FR-5).
    - İki ortamlı projede göçten sonra `Resolve` bugünkü `has 2 environments and none is selected` reddini verir
      (FR-5).
    - `project.json` salt okunurken `{project, env}` ve `{url, stackVersion}` → dosya bayt bayt aynı, seçim yok, satır
      yok (FR-6).
    - Adres göçü, ürün çözülüyor: `{url(bulut), env:"main"}` → dosya `{project, name}`, ortam adresten, satır `"main"`i
      tırnaklı adlandırır (FR-5).
    - Adres göçü, ürün çözülüyor, `project.json` salt okunur `{url(bulut), stackVersion}` → dosya bayt bayt aynı, seçim
      yok, satır yok, fiil koşar (FR-6). Bugün fiil `open palbase/project.json: permission denied` ile düşüyor.
    - `{project, env:""}` → dosyada `env` yok, satır `("")` (FR-5; boş değer de taşınmış bir alandır).
    - 100 KiB'lık `env` değeri → satırdaki değer 64 rune'da kırpılmış (FR-5).
- **Mutasyonlar** (her biri tek eşleşmeyle uygulanır, koşulur, bayt bayt geri konur; kırmızı olan iddia adlandırılır):
  - emekli alan tanıma kalkar → okuma testleri kırmızı;
  - FR-3'ün sınırı gevşer (her bilinmeyen alan yutulur) → `bogus` testi kırmızı;
  - harf varyantı reddi kalkar → `STACKVERSION`/`Env` testi kırmızı;
  - yapısal denetim kalkar (`DecodeStrict` → `json.Unmarshal`) → FR-8'in üç durumu kırmızı;
  - göç `env`i seçime yazar → FR-5 "seçim yazılmaz" testi kırmızı;
  - yazım düşse de satır basılır → FR-6 testi kırmızı;
  - kalan alanlar yeniden serileştirilerek çözülür → FR-9 denklik testi kırmızı;
  - adres göçünün yazım hatası yine fiili düşürür → FR-6'nın adres göçü durumu kırmızı;
  - adres göçü satırı düşen `env`i adlandırmaz → FR-5'in adres göçü durumu kırmızı;
  - kırpma kalkar → uzun değer durumu kırmızı;
  - boş `env` emekli alan sayılmaz → `env:""` durumu kırmızı.
- **Tam koşu:** `GOWORK=off go test ./... -race -count=1 -timeout 25m`, `go vet ./...`, `gofmt -l .` boş,
  `golangci-lint run` 0 issue, `go vet -tags e2e ./tests/e2e/`. `ci.yml`nin kapılarıyla aynı. Paketin Docker e2e testi
  (`TestStartServesAndStopCleansUp`) aynı makinede eşzamanlı koşularla compose proje adını paylaşıyor (kapsam dışı
  bulgu 2). Bu testte `palbase-002` kaynaklı bir kırmızı son kapıda atlanmaz: test tek başına yeniden koşulur ve
  sonucu yazılır.
- **Canlı kanıt (yayından sonra):**
  - `brew upgrade palbase` → sürüm satırı `0.67.2`.
  - REPRO'nun iki dosyası geçici dizinde `palbase status` ile okuma hatası vermez.
  - centauri-backdoor'da `palbase status` okuma hatası vermez; dosya değişmez, satır basılmaz.
  - Ardından oturum açıkken `palbase plan` ya da T016'nın `palbase push`u adres göçü satırını basar
    (`… now records the project "<ad>" rather than one environment's address …`). Dosya `{project, name}` olur ve
    `stackVersion` taşımaz (FR-7 yolu).

## Kapsam dışı bulgular (inceleme tur 1, kayıt)

1. Committed loopback adresi (`{url:"http://127.0.0.1:…", stackVersion}`) emekli alan düşerken yeniden yazılıyor ama
   `link`in bu adres için uyguladığı makine-yerel kurala (`WriteSelfHostTarget`) taşınmıyor. Bu, 0.65 öncesinde de
   committed duran bir dosya; göç yalnız bir alanı düşürüyor, gerileme değil. Ayrı iş.
2. `internal/backend/start_e2e_test.go` compose projesini `palbase-`+`t.TempDir()` tabanıyla adlandırıyor (`palbase-002`).
   Aynı makinede bu paketi koşan her oturum aynı yığını başlatıp durduruyor; ilgisiz kırmızı ve başkasının koşusunu
   düşürme. Ayrı iş.
3. `DecodeStrict`in yinelenme denetimi harf duyarlı, `encoding/json`un alan eşleşmesi harf duyarsız: `{"url":…,"URL":…}`
   denetimi geçiyor. Bu düzeltmeden önce de var. D-4 gereği ayrı iş.
4. Committed bir dosya iki başka yoldan terminale ham kontrol karakteri koyabiliyor:
   - banner, committed `name`i ham basıyor (`banner.go:72` → `environments.go:105-110`);
   - `DecodeStrict`in hata yolu metni anahtarı ham basıyor (`authcontract/validate.go:102-104`).
   Bu düzeltmeden önce de var; göç satırları temiz. D-4 gereği ayrı iş.

## Yayın

- Commit'ler izole klonda (`scratchpad/palbase-cli-fix`), push'tan hemen önce `origin/main`e rebase. Başka bir oturum
  aynı depoda aktif (09-13 19:47–09-14 02:36 arası 30 commit, yerel `sdk/cli`de kirli bir test dosyası); yerel `sdk/cli`
  klonuna DOKUNULMAZ.
- `ci.yml` main'de yeşil olduktan sonra `v0.67.2` etiketi (yama: davranış düzeltmesi, API değişmiyor). `release.yml`
  yeşil olmalı. Brew tap'inin 0.67.2'yi taşıdığı ölçülür.

## Değişiklik günlüğü

- 2026-09-14 · şartname yazıldı; uygulama `f42ce18`.
- 2026-09-14 · bağımsız inceleme tur 1 (`cli-fix-rev`, FIX_REQUIRED, IMPORTANT 4 · MINOR 11) sonrası değişti:
  - FR-5 tersine döndü: `env` seçime taşınmaz, düşer ve adlandırılır (IMPORTANT-2, seçenek a). Bu kararın sonucu olarak
    MINOR-2, MINOR-3, MINOR-4, MINOR-9 ve MINOR-11 konusuz kaldı.
  - FR-8 eklendi: yapısal kurallar test altında (IMPORTANT-1).
  - FR-9 ve D-1 değişti: tek çözüm, yeniden serileştirme yok (MINOR-1).
  - FR-1'e makine-yerel kayıt ve "fiilin sonucu bugünkü kurallara kalır" eklendi (MINOR-6, MINOR-7).
  - RED kapısındaki `ReadTarget` cümlesi (IMPORTANT-4) ve canlı kanıt maddesi (IMPORTANT-3) koda göre düzeltildi.
  - MINOR-5 ve MINOR-10 kapsam dışı bulgu olarak kaydedildi.
- 2026-09-14 · uygulama tur 2 `aa73f2c`. Uygulayıcının kaygıları üzerine iki değişiklik:
  - FR-8'in RED beklentisi düzeltildi: kırmızı kanıt mutasyondadır, iki tabanda değil (kaygı 1).
  - FR-5'teki "tek başına" `env` şekli "tek emekli alan olarak" diye düzeltildi. Yalnız `env` taşıyan, ne proje ne adres
    adlandıran dosyayı `ReadTarget` bugünkü kimlik kuralıyla zaten reddediyor (kaygı 4).
  - Kabul edilen davranış (kaygı 3): nesne olmayan bir dosyanın (`[]`, `"x"`) çözüm hatası Go tür adı olarak
    `backend.Target` yerine `decodeTarget`e özel çözüm biçiminin adını taşır. Ret aynıdır. Eski metni korumak hata
    yolunda ikinci bir çözüm demek olurdu ve D-1'e ters düşerdi.
- 2026-09-14 · bağımsız inceleme tur 2 (`cli-fix-rev`, FIX_REQUIRED, IMPORTANT 1 · MINOR 5) sonrası:
  - **IMPORTANT-1, FR-5 kısmı:** FR-5'in "seçim yazılmaz" hükmü emekli alan temizliği yoluna daraltıldı. Adres →
    kimlik göçünde ortam adresten gelir ve o göçün satırı düşen `env`i adlandırır. `{url(bulut), env}` şeklini hiçbir
    CLI sürümü yazmadı: v0.29–v0.33'te `env` yalnız `project` varken ve `url` silinerek yazılıyordu, v0.34–v0.64'te
    yazıcısı yoktu.
  - **IMPORTANT-1, FR-6 kısmı:** FR-6 adres göçünü de kapsıyor. Adres göçünün yazım hatası fiili düşürmeyecek (kod
    değişikliği).
  - **MINOR-1:** kabul edilen davranış genişletildi. Her tür hatasında Go tür adı `targetFile`dır; yayın toolchain'i
    Go 1.26.6'da `targetFile.Target.<alan>`. `Env: 5` artık tür hatası verir. `Env` ile `bogus` birlikteyse hata
    `bogus`u adlandırır. Ret her durumda aynıdır.
  - **MINOR-2:** D-3 göç satırlarına daraltıldı; banner ve `DecodeStrict` yol metni kapsam dışı bulgu 4 oldu.
  - **MINOR-3:** düşen değer 64 rune'a kırpılır.
  - **MINOR-4:** FR-3 varyant durumlarının kırmızı kanıtı mutasyondadır.
  - **MINOR-5:** boş `env` durumu testlere eklendi.
