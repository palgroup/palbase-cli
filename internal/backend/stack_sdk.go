package backend

// stack_sdk.go — the SDK a project's code is built against comes from the
// CHECKOUT, and the running image follows it.
//
// YÖN 06.09.2026'DA TERSİNE ÇEVRİLDİ ve bu dosyanın yarısı o yüzden gitti.
//
// Eskiden: bir backend `@palbase/backend`'e karşı derlenir ve KENDİ kopyasını
// taşıyan bir runtime tarafından koşulurdu. Majörler ayrıysa build, runtime'ın
// koşamayacağı bir şey üretiyordu — ve hata, sebebinden üç katman uzakta,
// eksik bir fonksiyon cümlesi olarak geliyordu (16.08.2026 ölçümü:
// "getRegisteredControllers is not a function"). Çare, runtime'ın sürümünü
// indirip buraya kurmaktı: `ensureProjectSDK`. Runtime sabitti, build ona
// uyuyordu.
//
// Şimdi: imaj SDK'yı TAKİP EDİYOR (D-015). Müşteri sürümünü değiştirir, düzlem
// imajı ona getirir. Eski çare bu yüzden yalnız gereksiz değil ENGELLEYİCİYDİ —
// her push bundle'ı zaten koşan sürüme sabitliyor, yani müşterinin seçimi
// üretime hiç ulaşmıyordu. Sürüm bulunduğu yerde donuyordu.
//
// Endişe hâlâ gerçek ve hâlâ karşılanıyor, ters yönden: artefakt hangi sürüme
// derlendiyse onu BİLDİRİYOR, düzlem imajı o sürüme getiriyor (ileri-yalnız),
// ve arada kalan pencerede runtime bundle'ı TEMİZ reddediyor. `sdkSkewNotice`
// artık bir uyarı DEĞİL bir haber: "bu push imajı taşıyacak".
import (
	"context"
	"fmt"
	"strings"
)

// sdkSkewNotice says the image is about to move — it is NEWS, not a warning.
//
// It used to warn: "this push builds against something other than what you
// typechecked", because the push silently swapped in the running SDK. Nothing
// swaps now. The checkout's SDK is what ships, so the only thing worth saying
// is the CONSEQUENCE: the project runs an older version and the platform will
// bring the image up to this one.
func sdkSkewNotice(installed, running string) string {
	if installed == "" || running == "" || majorOf(installed) == majorOf(running) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  this checkout builds against %s %s and the project runs %s.\n",
		backendPkg, installed, running)
	fmt.Fprintf(&b, "    The image follows the SDK: the platform moves this project onto %s's image,\n", installed)
	fmt.Fprintf(&b, "    forward only, under the holder route — requests wait, none fail.\n")
	return b.String()
}

// projectSDKVersion asks the project what its runtime is running.
func projectSDKVersion(ctx context.Context, target Target, cred Credentials) (string, error) {
	// The version travels on the well-known document, which is public and needs
	// no session: a checkout that is not signed in yet still has to be able to
	// see whether its SDK matches.
	described, err := describeStack(ctx, target.URL, target.Insecure)
	if err != nil {
		return "", err
	}
	return described.SDKVersion, nil
}

func orNone(version string) string {
	if version == "" {
		return "none"
	}
	return version
}

// sdkPruneRefusal, bundle'a girmeden ÖNCE sorulan tek soruyu cevaplar: derleme
// hangi SDK'ya karşı yapılacak, kurduğumuz mu?
//
// `before` bir npm mutasyonundan ÖNCE, `after` SONRA okunan sürümdür. İkisi
// ayrıldıysa `node_modules` bizim koyduğumuzu taşımıyor demektir ve bundan
// sonrası yalandır: CLI "✓ 26.0.0 kuruldu" yazarken 21.0.1'e derler, hata da
// bundler'ın içinden anlamsız bir sözdizimi parçası olarak çıkar.
//
// BİLİNMEYEN BİR SÜRÜM BUDAMA İDDİASI DEĞİLDİR: `installedBackendVersion`
// okuyamadığında "" döner, ve "" ile bir şeyi karşılaştırıp "değişti" demek,
// ölçemediğimiz şeyi kusur ilan etmek olurdu — bu koşuda iki teşhis tam olarak
// öyle çürüdü.
func sdkPruneRefusal(before, after string) string {
	if before == "" || after == "" || before == after {
		return ""
	}
	return fmt.Sprintf(
		"the installed %s changed from %s to %s while preparing the build.\n"+
			"  A tool install re-aligned node_modules against package.json and took the\n"+
			"  stack's SDK with it, so the bundle would compile against %s — not the\n"+
			"  version this stack RUNS. Nothing was pushed.",
		backendPkg, before, after, after)
}
