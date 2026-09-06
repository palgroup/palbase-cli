package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// writeProjectSDK lays out a checkout: what package.json DECLARES and what
// node_modules actually holds.
func writeProjectSDK(t *testing.T, dir, declared, installed string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"app","dependencies":{"`+backendPkg+`":"`+declared+`"}}`), 0o644))
	if installed == "" {
		return
	}
	mod := filepath.Join(dir, "node_modules", "@palbase", "backend")
	require.NoError(t, os.MkdirAll(mod, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mod, "package.json"),
		[]byte(`{"name":"`+backendPkg+`","version":"`+installed+`"}`), 0o644))
}

// PUSH PROJENİN SDK'SINI ARTIK İNDİRMİYOR — VE HABER BUNU SÖYLEMELİ.
//
// Bu test 06.09.2026'da TERSİNE ÇEVRİLDİ. Eskiden `ensureProjectSDK` çalışan
// sürümü indirip node_modules'a kurardı, ve buradaki iddia "indirdiğini SÖYLE"
// idi: checkout ^24 beyan ederken 23 kuruluyordu, yani typecheck'in ölçtüğü
// tipler bundle'ınkiler değildi.
//
// Artık kurulum YOK: bundle checkout'un kendi SDK'sıyla derlenir ve imaj onu
// TAKİP EDER. O yüzden söylenecek şey artık bir uyarı değil bir SONUÇ — "bu
// proje eski sürümü koşuyor, düzlem imajı buraya getirecek" — ve haberin
// taşıması gereken iki sayı da bu: derlenen sürüm ve koşan sürüm.
func TestSDKSkewSaysTheImageWillFollowTheCheckout(t *testing.T) {
	got := sdkSkewNotice("34.0.0", "33.0.2")
	require.NotEmpty(t, got, "majörler ayrıyken haber susamaz")

	require.Containsf(t, got, "34.0.0", "derlenen sürüm yok:\n%s", got)
	require.Containsf(t, got, "33.0.2", "koşan sürüm yok:\n%s", got)
	require.Regexpf(t, regexp.MustCompile(`(?i)image`), got,
		"haber imajdan hiç söz etmiyor — okuyan ne olacağını bilemez:\n%s", got)
	require.Regexpf(t, regexp.MustCompile(`(?i)forward`), got,
		"takasın İLERİ-YALNIZ olduğu söylenmiyor:\n%s", got)

	// VE ESKİ CÜMLE GERİ GELMEMELİ. "installing the project's" bir davranışı
	// tarif ediyordu; o davranış silindi, cümlesi kalırsa yalan olur.
	require.NotRegexpf(t, regexp.MustCompile(`(?i)install`), got,
		"haber hâlâ bir KURULUMDAN söz ediyor, oysa push artık hiçbir şey kurmuyor:\n%s", got)
	require.NotRegexpf(t, regexp.MustCompile(`(?i)typecheck`), got,
		"typecheck artık bayat DEĞİL — bundle tam da typecheck edilen sürüme derleniyor:\n%s", got)
}

// NEGATİF KONTROL: söylenecek bir şey yoksa sus. Bu olmadan "her push'ta yaz"
// yukarıdaki testi de geçerdi ve haber gürültüye dönerdi.
func TestSDKSkewIsSilentWhenThereIsNoNews(t *testing.T) {
	require.Empty(t, sdkSkewNotice("24.1.0", "24.0.0"),
		"aynı majör: imaj zaten bu sürümün imajı, haber yok")
	require.Empty(t, sdkSkewNotice("", "24.0.0"),
		"kurulu sürüm okunamıyorsa bir İDDİA kurulamaz")
	require.Empty(t, sdkSkewNotice("24.0.0", ""),
		"projenin koştuğu sürüm bilinmiyorsa karşılaştırılacak bir şey yok")
}

// `palbase status` REPORTED A DIFFERENT SDK THAN THE ONE PUSH ACTS ON.
//
// Two numbers, two sources, one label. `push` asks the project what it RUNS
// (/.well-known/palbase.json, served from a live probe of the runtime —
// v2/internal/server/wellknown.go) and says so when that major differs.
// `status` printed the sdk_version off deployments/current, which is what the
// last ACTIVATED ARTIFACT was BUILT with. Those two diverge the moment a project
// is moved onto a newer runtime without a redeploy — and then status hands
// someone a number that predicts nothing about what push is going to do.
func TestStatusSDKComesFromTheSameSourceAsPush(t *testing.T) {
	inScratchCheckout(t)

	const runs = "23.1.0"      // what the runtime is running — push's source
	const builtWith = "23.0.0" // what the live artifact was built with

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wellKnownPath:
			_ = json.NewEncoder(w).Encode(stackDescription{Hosting: "project", SDKVersion: runs})
		case "/v1/management/deployments/current":
			count := 12
			_ = json.NewEncoder(w).Encode(deploymentState{
				Digest:        "7c232f1484db13acc8b083d905df6ac4d8b00ea8",
				EndpointCount: &count, SDKVersion: builtWith,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	target := Target{URL: srv.URL}
	require.NoError(t, WriteTarget(target))
	cred := Credentials{Value: "k", Kind: KindKey}
	require.NoError(t, StoreCredential(srv.URL, cred))

	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	err := statusOfProject(cmd, false)
	require.NoError(t, err)

	// SAME SOURCE, asserted against the function push itself calls rather than
	// against a literal — a test that only pinned "23.1.0" would still pass if
	// both sides were rewired to a third document.
	fromPush, err := projectSDKVersion(context.Background(), target, cred)
	require.NoError(t, err)
	require.Equal(t, runs, fromPush)

	got := out.String()
	require.Containsf(t, got, "sdk:", "status reports no SDK line at all:\n%s", got)
	sdkLine := regexp.MustCompile(`(?m)^sdk:.*$`).FindString(got)
	require.Containsf(t, sdkLine, fromPush,
		"the sdk line does not carry what push reads (%s):\n%s", fromPush, got)
	require.NotContainsf(t, sdkLine, builtWith,
		"the sdk line still carries the ARTIFACT's version (%s), which push never looks at:\n%s",
		builtWith, got)

	// The artifact's own SDK is not deleted — it is a real fact — but it must no
	// longer be presented under the same bare "SDK" label that made the two
	// numbers look interchangeable.
	require.Containsf(t, got, builtWith, "the artifact's SDK was dropped entirely:\n%s", got)
	require.NotContainsf(t, got, ", SDK "+builtWith,
		"the artifact's SDK is still labelled as if it were THE sdk:\n%s", got)
	require.Truef(t, strings.Contains(got, "built with"),
		"the artifact's SDK is not labelled as the version it was BUILT with:\n%s", got)
}

// SESSİZ BUDAMA — canlıda ölçüldü 2026-09-03, `w4recipe` (88ykkmctm).
//
// O TARİHTE `palbase push` yığının SDK'sını `--no-save` ile kuruyordu (o adım
// 06.09'da silindi); sonra build çıkarıcı aracı için İKİNCİ bir `npm install`
// çağrılıyor — kalan tehlike bu ikincisi. npm o sırada `node_modules`'ı
// `package.json`'a göre yeniden hizalıyor ve elle yerleştirilmiş SDK'yı
// DECLARED sürüme geri alıyor. Deneyle ölçüldü, aynı dizinde:
//
//	başlangıç                     21.0.1
//	SDK tarball kurulunca         26.0.0
//	çıkarıcı araç kurulunca       21.0.1   ← geri döndü
//
// CLI bu sırada "✓ @palbase/backend 26.0.0, from the project itself" YAZIYORDU.
// Bundle 21'e karşı derlendi, `defineTable` orada yok, ve hata bundler'ın
// içinden çıktı: "the bundle could not be inspected". Aynı arıza kontrol
// düzleminin push'unu 02.09'da dört kez düşürdü ve İKİ teşhis birden yanlış
// çıktı — çünkü ikisi de BEYANI okudu, kurulanı değil.
func TestSDKPruneIsNamedNotSilent(t *testing.T) {
	t.Run("sürüm DEĞİŞTİYSE red, ve iki sürümü de adlandırır", func(t *testing.T) {
		why := sdkPruneRefusal("26.0.0", "21.0.1")
		require.NotEmpty(t, why, "budama sessiz kalmamalı")
		require.Contains(t, why, "26.0.0")
		require.Contains(t, why, "21.0.1")
	})

	t.Run("değişmediyse SESSİZ — kapı yalnız budamayı avlar", func(t *testing.T) {
		require.Empty(t, sdkPruneRefusal("26.0.0", "26.0.0"))
	})

	t.Run("BİLİNMEYEN sürüm bir budama İDDİASI değildir", func(t *testing.T) {
		// Kurulu sürüm okunamıyorsa (node_modules yok, package.json bozuk)
		// "değişti" demek uydurmaktır: ölçemediğimiz şeyi kusur ilan etmek,
		// tam da bu koşuda iki kez yapılan hatanın aynısı olurdu.
		require.Empty(t, sdkPruneRefusal("", "21.0.1"))
		require.Empty(t, sdkPruneRefusal("26.0.0", ""))
	})
}

// PUSH `node_modules`'A DOKUNMAZ — VE BU BİR ÖLÇÜM, BİR YORUM DEĞİL.
//
// Burada "yığının SDK'sı SON npm mutasyonu olmalı" diyen bir SIRA kapısı vardı:
// çıkarıcı araç SDK'dan sonra kurulursa ikinci `npm install` SDK'yı budardı
// (03.09, `w4recipe`: 21.0.1 → 26.0.0 → 21.0.1). Kapı doğruydu ve konusu
// SİLİNDİ — push artık hiçbir SDK kurmuyor, o yüzden budanacak bir şey de yok.
//
// Yerine geçen soru daha güçlü: push bittiğinde checkout'un SDK'sı hâlâ
// KİŞİNİN koyduğu sürüm mü? Eski davranış tam burada kaybediliyordu — çalışan
// sürüm indirilip üstüne yazılıyordu, yani müşterinin seçimi üretime hiç
// ulaşamıyordu ve `palbase start` bir sonraki koşusunda yanlış imajı çözüyordu.
//
// Kapı KAYNAK METNİ okumaz, gerçek `push`'u koşar: sunucu 500 dönerek push'u
// düşürür, ama bu ancak SDK adımından SONRA olur — yani ölçüm o adımın hiç
// olmadığını görebilir.
func TestPushLeavesTheCheckoutsOwnSDKInPlace(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, err := os.Getwd()
	require.NoError(t, err)

	const declared = "34.0.0" // checkout'un kendi sürümü
	const runs = "21.0.1"     // projenin ÇALIŞAN sürümü — eski push bunu kurardı
	writeProjectSDK(t, dir, "^"+declared, declared)
	// Bir backend checkout'u: bir modül ve bir şema bildirimi.
	mustWrite(t, dir, "app.module.ts", "export class AppModule {}")
	mustWrite(t, dir, PublicSchemaFile, "export default {}")
	// Çıkarıcı aracı yerinde bul, yoksa build kendi `npm install`'ını koşar ve
	// ölçtüğümüz şeyi kendi eliyle bozar.
	seedNodePkg(t, dir, "zod-to-json-schema")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == wellKnownPath {
			_ = json.NewEncoder(w).Encode(stackDescription{Hosting: "project", SDKVersion: runs})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var out bytes.Buffer
	err = runStackPush(context.Background(), Target{URL: srv.URL},
		Credentials{Value: "k", Kind: KindKey}, false, false, &out)
	require.Error(t, err, "sunucu 500 dönüyor; okuduğumuz şey push'un YOL BOYU yaptığı")

	require.Equal(t, declared, installedBackendVersion(dir),
		"push checkout'un SDK'sını değiştirdi — müşterinin sürüm seçimi üretime ulaşamaz")

	got := out.String()
	require.Containsf(t, got, runs, "haber projenin koştuğu sürümü adlandırmıyor:\n%s", got)
	require.Containsf(t, got, declared, "haber bu checkout'un sürümünü adlandırmıyor:\n%s", got)
}
