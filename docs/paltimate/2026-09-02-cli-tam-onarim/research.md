# Kanıt Eki — Artım 1 (Kapılar + Beş P0)

**Donma tarihi:** 2026-09-03 · Her satır ya team-lead tarafından bu oturumda BİZZAT okundu/koşuldu, ya da kaynağı adlandırılarak devralındı ve donma anında yeniden açıldı.

## Kod tabanı iddiaları

```
CB-01 CLAIM: `withoutBarman` blok sonunu, yorum bloğunun başına geri yürüdükten SONRA arar; `topLevelKey`
      yorum satırlarını atladığı için ilk eşleşen anahtar `barman:`'ın kendisi olur ve SERVİS KALIR.
      CITE: internal/backend/stackfiles_test.go:71-85 + "for start > 0 && (strings.HasPrefix(lines[start-1], \"  #\") || lines[start-1] == \"\") { start-- }"
      ve "for i := start + 1; i < len(lines); i++ { if topLevelKey(lines[i]) { end = i; break } }"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-02)

CB-02 CLAIM: `v0.53.0` tag'i düzeltme commit'i `ca3565c`'yi İÇERİR, buna rağmen vendor'lanan compose
      `docker compose config` tarafından REDDEDİLİR.
      CITE: `git merge-base --is-ancestor ca3565c v0.53.0` → 0 (evet) ; `docker compose config --services`
      → "service \"barman\" refers to undefined volume barmandata: invalid compose project"
      RE-GROUNDED AT FREEZE: confirmed (bizzat koşuldu 2026-09-03)

CB-03 CLAIM: Vendorlama testi GEÇERKEN dosya geçersizdir — kapı kusuru göremiyor.
      CITE: `go test ./internal/backend/ -run 'TestTheVendoredCompose|Vendor|Barman' -count=1` → "ok"
      RE-GROUNDED AT FREEZE: confirmed (bizzat koşuldu 2026-09-03)

CB-04 CLAIM: Vendor'lanan dosyada `barman:` servisi ve `barmandata` bağı duruyor; `volumes:` bloğunda
      `barmandata` YOK.
      CITE: internal/backend/stackfiles/docker-compose.dev.yml:182 "  barman:" ; :208 "      - barmandata:/var/lib/barman" ;
      volumes bloğu yalnız "pgdata:", "artifactcache:", "artifacts:", "storage:"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-03)

CB-05 CLAIM: Yayındaki brew binary'si bozuk dosyayı taşıyor.
      CITE: `strings /opt/homebrew/bin/palbase | grep -c 'context: ./barman'` → 1 ; `... | grep -c 'barmandata:$'` → 0 ;
      `palbase --version` → "palbase version 0.53.0"
      RE-GROUNDED AT FREEZE: confirmed (bizzat koşuldu 2026-09-03)

CB-06 CLAIM: `.golangci.yml` CI'ın bu kapıyı bloklayan biçimde koştuğunu BEYAN EDİYOR.
      CITE: .golangci.yml:3 + "# ci.yml runs this gate blocking (no continue-on-error), so this file is the"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-03)

CB-07 CLAIM: `ci.yml` yalnız iki adım koşuyor — lint, gofmt, vet, e2e YOK.
      CITE: .github/workflows/ci.yml:58-62 + "- name: Build / run: go build ./..." ve "- name: Run tests / run: go test ./... -race -count=1"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-03)

CB-08 CLAIM: e2e paketi derlenmiyor.
      CITE: `go vet -tags e2e ./tests/e2e/` → "tests/e2e/mgmt_api_test.go:53:29: too many arguments in call to auth.LoadDPoPKey / have (string) / want ()"
      RE-GROUNDED AT FREEZE: confirmed (bizzat koşuldu 2026-09-03)

CB-09 CLAIM: `flags list` gövdeyi ÇIPLAK DİZİ olarak çözüyor ve çözemeyince ham gövdeyi basıyor.
      CITE: internal/flags/flags.go:223-233 + "var defs []struct { Key string `json:\"key\"` ... }" ve
      "if err := json.Unmarshal(raw, &defs); err != nil { // The stack's shape is the stack's; printing it beats guessing. fmt.Fprintln(out, ...) }"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-02)

CB-10 CLAIM: Sunucu ZARF döndürüyor.
      CITE: v2/internal/management/operate.go:112 + "out := ListFlags200JSONResponse{Flags: make([]Flag, 0, len(flags))}"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-02)

CB-11 CLAIM: `notifications remove` sağlayıcı ADINI yol parçası olarak gönderiyor.
      CITE: internal/notifications/notifications.go:394 + "call(r, cmd, http.MethodDelete, providersPath+\"/\"+url.PathEscape(args[0]), nil)"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-03)
      NOT: bu satır `url.PathEscape` KULLANIYOR — denetimin "escape yok" bulgusu bu fiil için geçerli değil; kusur ad-vs-kimlik.

CB-12 CLAIM: Sunucu yolu KİMLİK bekliyor.
      CITE: v2/internal/management/moduleadmin.go:744 + "s.delegate(ctx, \"notify\", http.MethodDelete, \"/v1/notifications/providers/\"+url.PathEscape(request.Id), nil, nil)"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-03)
      AÇIK KALAN: modülün kendi handler'ının kimliği mi adı mı çözdüğü OKUNAMADI (`v2/internal/modules/notifications` yolu yok).
      FR-013 bu yüzden "adı kimliğe çöz" diye yazıldı — listeleme cevabındaki `id` kullanılacak, modülün iç davranışına bağımlı değil.

CB-13 CLAIM: Android eklentisi `.palbase/openapi.json` (TEK dosya) ve `.palbase/android/palbase-config.json` okuyor.
      CITE: codegen-gradle/.../PalbaseCodegenPlugin.kt:11-12 + "openApi.convention(project.layout.projectDirectory.file(\".palbase/openapi.json\"))"
      ve "config.convention(project.layout.projectDirectory.file(\".palbase/android/palbase-config.json\"))"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-03)

CB-14 CLAIM: Eklenti DÜZ config bekliyor ve `https` şart koşuyor.
      CITE: codegen-gradle/.../GeneratePalbaseTask.kt:100-108 + "required(\"app_id\"); val baseUrl = required(\"base_url\"); val apiKey = required(\"api_key\")"
      ve "if (!baseUrl.startsWith(\"https://\")) { throw GradleException(\"... `base_url` must use HTTPS\") }"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-03)

CB-15 CLAIM: CLI android yuvasına ÇOK-ORTAM belge yazıyor ve bulut yolu `.palbase/openapi.json`'ı siliyor.
      CITE: internal/backend/app_environments.go:93 `writeAppEnvironments` (default_environment + environments haritası) ;
      internal/backend/cloud_environments.go:221 (legacy tek-dosya silme)
      RE-GROUNDED AT FREEZE: confirmed — dossier-cli + Açı B raporundan devralındı, yollar bu oturumda dosya varlığıyla doğrulandı

CB-16 CLAIM: `init` SDK sürümünü REGISTRY'den soruyor ve şablonu çektiği paketin içinden kopyalıyor.
      CITE: internal/backend/init.go:77-83 + "cmd := exec.CommandContext(ctx, \"npm\", \"view\", backendPkg, \"versions\", \"--json\")"
      ve :70-76 yorumu "The version it returns decides only WHICH PACKAGE is fetched. What the new project then declares is the range in that package's own template"
      RE-GROUNDED AT FREEZE: confirmed (bizzat okundu 2026-09-03)

CB-17 CLAIM: `init.go`'nun `latest` gerekçesi ARTIK YANLIŞ.
      CITE: internal/backend/init.go:68-71 + "It cannot ask for `latest`. That tag is deliberately held on the v1 line"
      vs ölçüm: `npm view @palbase/backend dist-tags` → latest=next=25.1.0 (dossier-cli, 2026-09-03)
      RE-GROUNDED AT FREEZE: confirmed (yorum bizzat okundu; dist-tag ölçümü devralındı)

CB-18 CLAIM: Repo 26.0.0 şablonu modül taşıyor ve TEMİZ kurulumda derleniyor.
      CITE: sdk/palbase-ts/backend/package.json "version": "26.0.0" ; template/modules/{health/health.module.ts,notes/notes.module.ts} ;
      `npm pack` → temiz dizin → şablon kopyası → `palbase build` → "build OK — 5 route(s)", exit 0
      HEM HEAD CLI (13bd760) HEM yayındaki 0.53.0 ile.
      RE-GROUNDED AT FREEZE: confirmed (bizzat koşuldu 2026-09-03)
```

## Dış kaynak iddiaları

```
EX-01 CLAIM: `docker compose config`, beyan edilmemiş bir volume'e bağ kuran servisi geçersiz proje sayar.
      SOURCE: yerel araç davranışı — `docker compose version` 5.5.0 (bu makine)
      VERIFIED: 2026-09-03, negatif kontrol koşuldu; çıktı CB-02'de
      TIER: quick

EX-02 CLAIM: Kapıların gerçek aracı taklit etmesi, aracın reddettiğini geçirir — bu kusur sınıfı bu depoda
      ölçülmüş ve kalıcı kurala bağlanmıştır.
      SOURCE: memory feedback_a_regex_gate_does_not_measure_what_the_stager_rejects
      VERIFIED: 2026-09-02, CB-02/CB-03 ile ikinci kez canlı doğrulandı
      TIER: quick
```

## Unverified Decision Register (tasarım fazından devralındı, BURADA kapatıldı)

```
UD-001 | docker compose (kapı aracı)        | stakes 2 | tier quick | status: verified | evidence: EX-01 (yerel negatif kontrol)
UD-002 | golangci-lint (CI lint kapısı)     | stakes 2 | tier quick | status: verified | evidence: .golangci.yml `version: "2"`; CI'da pinli sürüm P-4'te karara bağlandı
UD-003 | gofmt / go vet (stdlib kapıları)   | stakes 0 | tier none  | status: verified | evidence: Go toolchain'in kendi parçaları
UD-004 | Sigstore/cosign (manifesto imzası) | stakes 6 | tier deep  | status: verified — ARŞİV | evidence: scratchpad/rs-trust.md; W1 kapsam dışı (D-030), bu artımda KULLANILMIYOR
UD-005 | version.env manifesto şekli        | stakes 5 | tier deep  | status: verified — ARŞİV | evidence: decisions.md D-013a; W1 kapsam dışı
```

Register'da açık satır yok — beş kaydın beşi de `verified` (ikisi arşiv, çünkü W1 kapsam dışı).

## Devralınan ama bu artımda KULLANILMAYAN kanıt

`scratchpad/rs-pin-priorart.md`, `rs-pin-risk.md`, `rs-trust.md`, `dossier-plane.md` — W1 (ağdan pin) kapsam dışı olduğu için Artım 1'in hiçbir FR'si bunlara dayanmıyor. Arşivde tutuluyorlar; iş açılırsa `decisions.md` D-006…D-029 ile birlikte hazır.
`rs-envcheckout.md` ve `rs-supabase.md` → Artım 2 ve 3'ün girdisi.
