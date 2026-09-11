package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useTempMachineHome points the machine-state home at a throwaway directory for
// ONE test, through the package's own seam.
//
// NOT `t.Setenv("HOME", …)`: that changes a machine-global for every test that
// runs after it in the same binary, and it did — the suite stopped finishing.
func useTempMachineHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	prev := machineStateHome
	machineStateHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { machineStateHome = prev })
}

// MAKİNE-YEREL DURUM MÜŞTERİNİN DEPOSUNDA DURMAZ.
//
// İki dosya vardı: `local.json` (`palbase start` koşarken "şu an burada çalış"
// mod anahtarı) ve `plan.json` (bu makinede, şu an, seçili ortama karşı yapılmış
// ölçüm; tek okuyucusu hemen ardından koşan `push`). İkisi de makineye ait.
//
// Checkout'ta durmalarının gerekçesi kodda "her fiil okuyor" diye yazılıydı ve
// o yanlış bir gerekçe: her fiil checkout kökünü zaten biliyor. Ve CLI'ın
// makine evi ZATEN var — `~/.palbase/credentials.json` ve `~/.palbase/stacks/`,
// ikisi de "NOT in the checkout: they are secrets, they are per-machine".
func TestMachineStateLivesOutsideTheCheckout(t *testing.T) {
	useTempMachineHome(t)
	checkout := t.TempDir()

	local, err := LocalStatePath(checkout)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanStatePath(checkout)
	if err != nil {
		t.Fatal(err)
	}

	// (a) YOL CHECKOUT'UN DIŞINDA. Bu testin asıl iddiası; ötekiler onu
	// kullanılabilir kılıyor.
	//
	// HER İKİ TARAF DA SEMBOLİK BAĞLARDAN ÇÖZÜLÜR. İlk hâlinde çözülmüyordu ve
	// iddia HİÇBİR ŞEY ÖLÇMÜYORDU: macOS'ta `t.TempDir()` `/var/...` veriyor,
	// üretim kodu `EvalSymlinks` ile `/private/var/...`e çözüyor, ve
	// `filepath.Rel` iki farklı önek gördüğü için her durumda `..` döndürüyordu.
	// Yolu bilerek checkout'un İÇİNE koyan bir mutasyon testten GEÇTİ — kusur
	// böyle bulundu.
	resolvedCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{local, plan} {
		resolved, evalErr := filepath.EvalSymlinks(filepath.Dir(p))
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		rel, relErr := filepath.Rel(resolvedCheckout, resolved)
		if relErr == nil && !strings.HasPrefix(rel, "..") {
			t.Errorf("%s checkout'un İÇİNDE (%s) — depoda makine-yerel dosya kalmamalı", p, rel)
		}
	}

	// (b) İki dosya birbirine karışmaz.
	if local == plan {
		t.Errorf("local ve plan aynı yolu paylaşıyor: %s", local)
	}

	// (c) Üst dizin YARATILMIŞ olmalı: çağıran yazmaya hazır bulmalı.
	for _, p := range []string{local, plan} {
		if _, statErr := os.Stat(filepath.Dir(p)); statErr != nil {
			t.Errorf("%s'in üst dizini yok: %v", p, statErr)
		}
	}
}

// AYNI CHECKOUT AYNI YOLU VERİR — yoksa `palbase start` bir yere yazar, `palbase
// stop` başka yere bakar ve yığın adresi sonsuza kadar kayıtlı görünür.
func TestMachineStatePathIsStableForOneCheckout(t *testing.T) {
	useTempMachineHome(t)
	checkout := t.TempDir()

	first, err := LocalStatePath(checkout)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LocalStatePath(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("aynı checkout iki farklı yol verdi:\n%s\n%s", first, second)
	}
}

// FARKLI CHECKOUT'LAR AYRIŞIR. İki proje aynı makinede yan yana durur ve
// birinde `palbase start` koşmak ötekinin hedefini değiştiremez.
func TestDifferentCheckoutsGetDifferentState(t *testing.T) {
	useTempMachineHome(t)
	a, err := LocalStatePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := LocalStatePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("iki farklı checkout aynı durumu paylaşıyor: %s", a)
	}
}

// GÖRELİ BİR KÖK, MUTLAK HÂLİYLE AYNI YERİ VERİR — `palbase start` bir kez
// `.` ile, bir kez tam yolla çağrılınca iki ayrı yığın kaydı doğmasın.
func TestRelativeAndAbsoluteRootsAgree(t *testing.T) {
	useTempMachineHome(t)
	checkout := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}

	byAbs, err := LocalStatePath(checkout)
	if err != nil {
		t.Fatal(err)
	}
	byDot, err := LocalStatePath(".")
	if err != nil {
		t.Fatal(err)
	}
	if byAbs != byDot {
		t.Errorf("aynı dizin iki yol verdi:\n%s\n%s", byAbs, byDot)
	}
}

// `palbase start` KAYDI CHECKOUT'A YAZMAZ — ve `stop` onu bulup siler.
//
// Bu, T016'nın uçtan uca iddiası: hedef çözümü artık makine evinden geçiyor.
// Checkout'ta `local.json` görünürse depoda ignore edilecek bir dosya doğar ve
// düzenin "her şey commit'lenir" kuralı düşer.
func TestWriteTargetLeavesNothingInTheCheckout(t *testing.T) {
	useTempMachineHome(t)
	checkout := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}

	if err := WriteLocalTarget(Target{URL: "http://127.0.0.1:54321"}); err != nil {
		t.Fatal(err)
	}

	// (a) checkout'ta HİÇBİR ŞEY yok.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("checkout'ta dosya doğdu: %s — makine-yerel durum depoya yazılmamalı", e.Name())
	}

	// (b) ve hedef GERÇEKTEN çözülüyor — (a) tek başına "hiçbir şey yazılmadı"
	// diyen bozuk bir uygulamayla da geçerdi.
	got, err := ReadTarget()
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://127.0.0.1:54321" || !got.Local {
		t.Errorf("hedef çözülmedi: %+v", got)
	}
}
