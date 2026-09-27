# Planlama zincirlerinin git bundle'ları

Planların her görevi, planlama sırasında bir scratch klonunda kırmızı → yeşil olarak uygulandı. Bu bundle'lar o commit'leri taşır. Scratch klonları `/private/tmp` altındaydı; macOS o dizini temizleyebilir (bkz. D-020). Planın metni esastır. Bundle, yürütmede bir görevin kodunu karşılaştırmak ya da doğrudan almak içindir.

| Bundle | Taban | HEAD | Repo |
|---|---|---|---|
| `cli-plan.bundle` | `20e5d7e` | `327a941` | palbase-cli |
| `plugin-plan.bundle` | `e72f704` | `ee1ee36` | palbackend-android-src |
| `cloud-plan.bundle` | `f92bc1e02` | `326172c4c` (dal `plan-r2`) | palbase-cloud (`origin/main`) |

Kullanım: `git fetch <bundle> HEAD:refs/heads/plan-ref` (yalnız okumak için; `main`'e cherry-pick etmeden önce plan sırasına bakın).

Bundle'daki commit sırası iki yerde plan sırasından farklıdır. Plan sırasıyla düz `git cherry-pick` her yerde temiz değildir: cloud'da T017'nin ilk commit'i iş akışı dosyasında (`.github/workflows/cloud-server-typecheck.yml`) çakışır ve plan çıpasıyla çözülür (doğrulayıcı ölçtü):
- **CLI:** bundle'da T019, T018'in hemen ardından gelir. Planda T019 Dalga 2'dedir, yani T022'den sonra (D-025, D-026).
- **Cloud:** bundle'da T017 en sondadır, iki commit hâlinde (uygulayıcı `4ffc14f39`, doğrulayıcının ret kodu `326172c4c`); T005'in revizyonu `4b0fe8e6f`. Planda T017, T006'dan sonra ve T007'den önce koşar (D-021).

**Cloud, yeni taban (2026-09-27):** `cloud-plan-bb547a702.bundle` — `origin/main` `bb547a702` üstünde plan sırasıyla (T001…T006, T017, T007…T011, T013…T016) yeniden oynatılmış zincir; plan metninden bağımsız yeniden oynatma aynı ağacı verdi (`af2d112…`). Yürütmede esas budur.
