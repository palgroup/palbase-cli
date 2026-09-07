package backend

// The checkout selects the SDK used for builds. Its artifact declares that
// requirement to the platform, which reconciles the runtime image separately.
// A successful code upload does not prove that image reconciliation succeeded:
// schema compatibility, legacy image tags and image availability have their
// own checks. Read the runtime version instead of promising a future swap.
import (
	"context"
	"fmt"
	"strings"
)

// A build/runtime mismatch is observed here; an image migration is not.
// A refused push carries no new target, and the plane can refuse a migration
// independently (for example, a legacy SHA image with no comparable version).
func sdkSkewNotice(installed, running string) string {
	if installed == "" || running == "" || installed == running {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  this checkout builds against %s %s and the project runs %s.\n",
		backendPkg, installed, running)
	fmt.Fprintf(&b, "    Push checks forward migration compatibility for %s and verifies the runtime before uploading code.\n", installed)
	fmt.Fprint(&b, "    Sending code does not confirm that the runtime image has changed.\n")
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
			"  version selected by this checkout. Nothing was pushed.",
		backendPkg, before, after, after)
}
