# Palbase CLI — Tam Onarım Tasarımı

**Tarih:** 2026-09-03 · **Kaynak:** `decisions.md` (46 karar + 6 öz-eleştiri kalemi) · **Girdi:** `research/2026-09-02-cli-full-audit.md` (122 bulgu) + 6 araştırma raporu
**Durum:** onay bekliyor

---

## 0. Kapsam

**İçeride (6 kol):** sevkiyat kapıları · ortam modeli · `start` doğruluğu · tek `link` · seçim katmanının emekliliği · modül sözleşmesi + güvenlik hijyeni. Artı **beş P0'ın tamamı**.

**Dışarıda (kullanıcı kararı, D-030):** imaj/pin/SDK sürümünün **ağdan** çözülmesi. `v2-cloud` + filo yayım hattına giriyor, orası başka oturumun kulvarı. D-006…D-029 arası kanıt arşivde duruyor; iş açılırsa hazır.

**Bağlayıcı kısıtlar:** self-host denkliği dokunulmaz (D-002) · geriye uyum yok, shim yok (D-003) · release kesmek bu programın içinde (D-004) · gereksiz flag/fallback yok.

---

## 1. Kol A — Sevkiyat kapıları (ÖNCE)

> **Karar:** Kapılar ilk iş. **Gerekçe:** Beş P0'ın hepsi yeşil kapıların altından geçti; P0-1'in kapısı yeşildi çünkü karşılaştırmanın iki tarafı da aynı bozuk fonksiyondan geçiyordu. Ölçmeyen kapı altında yapılan her düzeltme aynı sessizlikle çürür. **Alternatif:** "önce görünür düzeltmeler" — reddedildi: görünür düzeltmelerin kanıtı da aynı kapılara bağlı.

1. **`docker compose config` negatif kontrolü.** Vendor'lanan compose gerçek araca verilip doğrulanır. `withoutBarman`'ın blok sonu **orijinal** `barman:` satırından aranır. Kapı, aracın reddettiğini geçirmeyecek.
2. **CI'a gerçek kapılar:** `gofmt -l` (çıktı varsa fail) · `go vet ./...` · pinli `golangci-lint` (48 açık bulgu) · `go vet -tags e2e ./tests/e2e/`. `.golangci.yml` zaten "ci.yml bunu bloklayan kapı olarak koşar" diyor — iddiayı gerçek yap ya da iddiayı sil.
3. **`-short` gerçekten kısa olsun.** `internal/backend` seri 7,4 dk, sıfır `t.Parallel()`. Fixture-izole build testlerine paralellik; ≥10 sn'lik `bun build`/tsc derlemeleri ayrı etikete.
4. **Yeşil yalanı biten iki kural:** `npm install` başarısızlığı CI'da `t.Fatal` (skip yalnız eksik araç için) · kardeş-checkout isteyen parite testleri (compose paritesi dâhil) monorepo CI'ından koşulur.
5. **Yeni kapı — rota literali ↔ sunucu rotası.** Hiçbir test bunu yapmıyor; `GET /api/v2/projects`'in ölü olduğunu kimse söylemedi. CLI'ın çağırdığı her yol, sunucunun sunduğu kümede olacak.
6. **Yeni kapı — terminale Türkçe düşmesin.** Test dışı string literalleri taranır (bugün 6 ihlal, ikisi canlı görüldü).

---

## 2. Kol B — Ortam modeli (yeni; kullanıcının gereksinimi)

**Gereksinim (D-034):** *"Ortamların kodları da farklı olabilir — git branch gibi. Linkleyince ortama checkout edebilmeliyim. Change varsa hata vermeli."*

> **Karar:** `checkout` çalışma ağacına **DOKUNMAZ**; duyurur ve sapmayı ölçer. Ret, `push` ve `pull`'a taşınır.
> **Gerekçe:** Bunu deneyen tek büyük platform AWS Amplify Gen 1'di; AWS düzeltmedi, **sildi** (Gen 2'de `env checkout` yok). Belgelenmiş acısı: sessiz üzerine yazma (#12430), kodun uyarısız geri alınması (#5569), 3 yıldır tekrarlayan merge çakışmaları (#7938→#13618), maintainer itirafı (#9116). Kök neden: tek dizinde üç yazıcı, hakem yok. En yakın yapısal analog **Convex**'in kod indirme yolu hiç yok; cevabı **duyuru**.
> **Alternatifler:** (a) Amplify tarzı senkronlu checkout — reddedildi, satıcısı reddetti; (b) hiçbir şey yapma — reddedildi, bugünkü mesaj aktif olarak yalan söylüyor.

**Yüzey:**
- **`palbase env list`** — projenin ortamları: slug, ref, production mu, deploy edilmiş sürüm, bu checkout hangisine bağlı.
- **`palbase env checkout <slug>`** — hedefi değiştirir, ortam başına artefaktları (`.palbase/openapi/<env>.json`, config, Generated) tazeler, **kaynak koda dokunmaz**. Basar: hangi ortam · orada koşan sürüm · yerel ağaç ondan sapıyor mu · sapıyorsa `palbase pull`. *(K-01: bu duyuru süsleme değil ölçüm; sessiz bir `▸ staging` bu gereksinimi karşılamaz.)*
- **`palbase push`** — **kayıp-güncelleme koruması** (D-046, takım gerçek olacak): deploy öncesi ortamın mevcut sürümü sorulur; makine-yerel "en son gördüğüm" kaydından ilerlemişse git'in non-fast-forward reddi gibi durur ve ne yapılacağını söyler.
  *(K-03 — EN CİDDİ: bu kayıt **commit'lenmez**, makine-yerel ve gitignore'lu. Amplify'ın 1 numaralı acısı commit'lenmiş `team-provider-info.json`'du. Kaydın YOKLUĞU ret sebebi değildir — taze klonda çekilir, duyurulur, devam edilir.)*
- **`palbase pull`** — mevcut `refuseDirtyTree` korunur (fail-closed, iyi yazılmış); mesajı iyileşir (hangi dosyalar, ne yapmalı) ve **git olmayan dizinde koruyamadığını SÖYLER** (bugün sessizce devam ediyor).
- **Silinir:** `refuseUseOnBoundProject`'in *"one project with one environment — there is nothing to switch to"* yalanı; bulut hedefinde de basılıyor.

**Açık seçenek (dayatılmıyor):** Heroku'nun **promosyon** modeli (`staging → production`, derlenmiş artefaktı yeniden derlemeden kopyala) ağaca hiç dokunmadan "ortamlar arası kod taşıma" sorusunu çözüyor. Kullanıcının *"orada ne yapacağız bilmiyorum"*una doğrudan cevap. Kapsama alınabilir.

---

## 3. Kol C — `palbase start` doğru yığını kaldırsın

**Gereksinim (D-035):** *"start ilgili projeyi start etmeli; configlere versiyon koyabilirsin."*

> **Karar:** Proje config'inde **tek anlamsal sürüm alanı**; servis başına tag **YOK**. Beyan yoksa kurulu `@palbase/backend`'den türetilir, **sonra yazılır ve commit'lenir**. Sürüm→imaj tablosu **`@palbase/backend` paketinin içinde** dağıtılır.
> **Gerekçe:** Supabase'in 14 imaj alanı `toml:"-"` ile kullanıcıdan **yapısal olarak gizli** — bu bir kaza değil, düşünülmüş ret: servis başına tag vermek kullanıcıya uyumluluk matrisi vermektir (D-036). Supabase'in iki tuzağından da kaçıyoruz: binary'ye kaynaklı değil (proje beyan edebiliyor) ve `.temp` gibi gitignore'lu değil (commit'leniyor, taze klon ve CI da alıyor). Tablonun SDK paketinde olması Expo'nun hamlesi (D-023) ve **K-02'yi** çözer: tabloyu tazelemek için CLI sürümü gerekmez, `npm i` yeter — ağ ucu yok, `v2-cloud` işi yok, D-030 ile çelişmez.
> **Alternatifler:** Nhost tarzı servis-başına pin — reddedildi (uyumluluk matrisi). Yalnız SDK'dan türetme — reddedildi: emsaller **tersini** yapıyor ve yine bir sürüm→imaj tablosu gerekiyor (D-038).

**Ayrıca — `start` dürüst olacak:**
- Kaldırdığı imajları **yazar** (bugün hiç söylemiyor).
- Hazırlık kontrolü **runtime'a** sorulur; bugün `/readyz` yalnız palsvc'yi kanıtlıyor ve runtime bundle'ı reddederken banner "hazır" diyor.
- Uyuşmazlık **iki sayıyı da adlandırarak** söylenir; `upgrade` ölü-ucu (`refFromTargetURL` loopback'te `""`) düzelir.
- `stop` compose'u **ezmeyi bırakır** ve `local.json`'ı silmeden önce başarılı olur.
- `pgvector` dâhil **yedi pin** tek mekanizmaya bağlanır (bugün dördü hiçbir kontrolde yok, ve fiilen koşan değer Go sabiti değil compose default'u).

---

## 4. Kol D — Tek `palbase link`

> **Karar:** Platform algılamalı tek `link` + yeni `unlink`. `ios|macos|android|web link` ve `<platform> use` **silinir** (shim yok, D-003).
> **Gerekçe:** `.palbase/project.json` varken bu komutların hepsi **zaten** `runLink`'e iniyor; ayrı komutların geriye kalan tek işi CLI'ın kendi tavsiyesiyle **hiç ulaşılamayan** bir bulut dalı. Örtüşme ölçüldü: `gatherEnvironments`↔`addLocalStack` %95 birebir, `resolveNativeApp`↔`resolveWebApp` 35 satırın 30'u aynı.
> Algılama malzemesi hazır: `planes.go` `hasApple`/`hasWeb`, `detectAndroidApplicationID`.

Kapsananlar: `--platform` doğrulaması (bugün `bogus` kabul ediliyor) · android'in xcconfig+swiftgen koşturması · bağlı checkout'ta `--json`/`--package-name`'in yutulması · `Palbase/palbase-config.json`'ın üç yazıcı/üç şekli · çıplak `link`'in bağlı checkout'ta düşmesi · **Android codegen (P0)**: eklenti düz config + `.palbase/openapi.json` bekliyor, CLI çok-ortam yazıp o dosyayı siliyor.

---

## 5. Kol E — Seçim katmanının emekliliği

> **Karar:** `--project/--environment` + `selection.json` **ikinci adresleme mekanizması olarak** emekli; hedefi `link`, ortamı `env checkout` seçer. `internal/apps` (617 satır), github `repository_provider` kolu, `internal/hook`, `tests/e2e` silinir.
> **Gerekçe:** Dayandığı `GET /api/v2/projects` sunucuda **yok** (doğrulandı) — bayraklar 15+ komutta sessizce iş görmüyor. Denetimdeki tutarsızlıkların çoğu tam bu ikilikten doğuyor. Ortam değiştirme Kol B ile **daha iyi** karşılanıyor, kaybolmuyor.

---

## 6. Kol F — Modül sözleşmesi + güvenlik hijyeni

**Sözleşme:** fiil adları (`list`/`add`/`remove` tekilleşir) · `--json` **tek anlam** (çıktı; `auth`'un girdi kullanımı `--body`'ye) · `▸` tek yer, stderr, JSON'dan bağımsız · **bilinmeyen alt komutta `exit ≠ 0`** (bugün 10 grupta 0) · yıkıcı fiillerde onay · zarf çözme (`flags list` P0) · `url.PathEscape` (auth ×10, test-user) · tek `ManagementError` (503 bugün beş ayrı cümle).

**Güvenlik:** `--insecure` commit'lenen dosyadan çıkar (TLS atlaması her clone'a yayılıyor) · `apikey rotate` sırrı basmayı bırakır · eski `credentials-dev.json` (0644, tam token) süpürülür · DPoP keyring hatası sesli düşer · `notifications add`'in okunmayan kasa yazımı ve yalan "secret-guard" yorumu gider.

---

## 7. Beş P0

| # | Kusur | Kol |
|---|---|---|
| 1 | `start`/`stop` hiç çalışmıyor (geçersiz compose) | A |
| 2 | `init` → `build` düşüyor | C (+ koordinasyon: dosyalar başka oturumun WIP'inde) |
| 3 | `flags list` her zaman ham JSON | F |
| 4 | `notifications remove` hiçbir zaman silemiyor | F |
| 5 | Android istemcisi üretilemez | D |

---

## 8. Sıralama

**A** (kapılar) → **P0'lar + 0.52.1** → **C** (`start`) → **D**+**E** (link ve seçim, birlikte) → **B** (ortam modeli) → **F** (sözleşme + hijyen).
Gerekçe: kapılar önce çünkü kanıt onlara bağlı; P0'lar hemen ardından çünkü yayında kırık; D ve E birlikte çünkü aynı dosyalara dokunuyor.

---

## 9. Traceability — 15 envanter kalemi

| # | Kalem | Nerede karşılanıyor |
|---|---|---|
| 1 | link üçlüsünün mekanizması | ✓ denetim |
| 2 | o üçünde daha iyi DX | Kol D |
| 3 | tüm CLI denetimi | ✓ 122+1 bulgu |
| 4 | sıfır bilinen kusur | Kol A (kapılar) + tüm kollar |
| 5 | tüm komut yolları | ✓ 116 düğüm/94 yaprak envanteri |
| 6 | eskilerin silinmesi | Kol E |
| 7 | mevcutların iyileştirilmesi | Kol F |
| 8 | ios/web adına gerek yok | Kol D |
| 9 | modül namespace'leri + test | Kol F (24 bulgu) |
| 10 | `start` doğruluğu | Kol C |
| 11 | hepsi baştan sona | 6 kol + 5 P0 |
| 12 | en güncel SDK | Kol C (SDK'dan türet + yaz) |
| 13 | sürüm+imaj network'ten | **KAPSAM DIŞI** (D-030, kullanıcı kararı) |
| 14 | hep en güncel | Kol C — kısmen: `npm i` tabloyu tazeler, ağ yok |
| 15 | sürekli CLI güncellemesi olmasın | Kol C (tablo SDK paketinde, K-02) |

**Eşlenmeyen tasarım öğesi yok.** ⑬ bilinçli olarak kapsam dışı; ⑭/⑮ ağsız yoldan kısmen karşılanıyor ve sınırı açıkça yazıldı.
