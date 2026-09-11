package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMain points THIS PACKAGE's machine-state home at a throwaway directory.
//
// THE DEFAULT HAS TO BE THE SAFE ONE. Eleven test files write machine state —
// `WritePlanFile`, `WriteLocalTarget`, the push and plan paths — and each was
// one forgotten `useTempMachineHome` away from leaving a record in the
// DEVELOPER's `~/.palbase/checkouts/`, naming a `t.TempDir()` that vanishes when
// the test ends. That is the litter D-010 measured at 823 directories, and the
// suite that fixed it was recreating it five records per run. A clean CI runner
// never sees this, so "remember to call the seam" is not a gate — the default is.
//
// NOT `os.Setenv("HOME", …)`: this package measured what that does — one such
// test and the suite stopped finishing, because something downstream resolves a
// home-derived path once and keeps it. This moves the package's own seam and
// nothing else.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "palbase-test-home-*")
	if err != nil {
		panic(err)
	}
	machineStateHome = func() (string, error) { return home, nil }
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// useTempMachineHome points the machine-state home at a throwaway directory for
// ONE test, through the package's own seam.
//
// NOT `t.Setenv("HOME", …)`: that changes a machine-global for every test that
// runs after it in the same binary, and it did — the suite stopped finishing.
func useTempMachineHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := machineStateHome
	machineStateHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { machineStateHome = prev })
	return home
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
	// DİZİN HENÜZ YOK, ÇÜNKÜ SORMAK ONU YARATMIYOR (D-010). `EvalSymlinks` var
	// olmayan bir yolda düşer, yani karşılaştırma yolun KENDİSİ üzerinden
	// yapılıyor; ev dizini zaten yukarıda çözülmüş hâliyle geliyor.
	for _, p := range []string{local, plan} {
		resolved := filepath.Dir(p)
		if r, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
			resolved = r
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

	// (c) ÜST DİZİN YARATILMAMIŞ olmalı — ve bu, iddianın TERSİNE ÇEVRİLMESİ
	// (D-010). Burada "çağıran yazmaya hazır bulmalı" yazıyordu ve yol soran her
	// çağrı bir dizin bırakıyordu: canlıda 823 tane sayıldı, her biri artık
	// adlandırılamayan bir hash. Dizini açmak YAZANIN işi.
	for _, p := range []string{local, plan} {
		if _, statErr := os.Stat(filepath.Dir(p)); statErr == nil {
			t.Errorf("%s: yalnızca yolu SORMAK dizini yarattı", p)
		}
	}
	// …ve yazan taraf onu açar.
	for _, p := range []string{local, plan} {
		if err := ensureMachineStateDir(p); err != nil {
			t.Fatal(err)
		}
		if _, statErr := os.Stat(filepath.Dir(p)); statErr != nil {
			t.Errorf("yazan taraf dizini açamadı: %v", statErr)
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

// TestMachineStateDirIsNotCreatedByAsking — ASKING WHERE A FILE GOES MUST NOT
// CREATE ANYTHING.
//
// Measured live on 11.09.2026: `~/.palbase/checkouts/` held 823 directories.
// `machineStateDir` hashed the path and called `os.MkdirAll` on every call, so
// every question left a directory behind — one per temp checkout the test suite
// ever asked about, and one per checkout a person has since deleted. A read that
// writes is a read nobody can use to look.
func TestMachineStateDirIsNotCreatedByAsking(t *testing.T) {
	useTempMachineHome(t)

	checkout := t.TempDir()
	path, err := LocalStatePath(checkout)
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Dir(path), "asking for the path created the directory")

	planPath, err := PlanStatePath(checkout)
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Dir(planPath), "asking for the plan path created the directory")

	// …but WRITING one does create it. Otherwise the split would just move the
	// failure to the writer.
	require.NoError(t, WritePlanFile(checkout, PlanFile{
		Version: 1, Target: PlanTarget{URL: "https://x"}, Fingerprint: "f",
	}))
	require.FileExists(t, planPath)
}

// AND THE RECORDS OF CHECKOUTS THAT NO LONGER EXIST ARE SWEPT.
//
// The key is a hash of an absolute path, so a deleted checkout leaves a
// directory nothing can ever name again — it is not garbage that will be reused,
// it is garbage that accumulates forever in somebody's home.
func TestDeadCheckoutStateIsReaped(t *testing.T) {
	home := useTempMachineHome(t)

	alive := t.TempDir()
	dead := t.TempDir()
	for _, c := range []string{alive, dead} {
		require.NoError(t, WritePlanFile(c, PlanFile{
			Version: 1, Target: PlanTarget{URL: "https://x"}, Fingerprint: "f",
		}))
	}
	require.NoError(t, os.RemoveAll(dead))

	reapDeadCheckoutStateNow()

	livePath, err := PlanStatePath(alive)
	require.NoError(t, err)
	require.FileExists(t, livePath, "a living checkout's state was swept")

	entries, err := os.ReadDir(filepath.Join(home, ".palbase", "checkouts"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "the deleted checkout's record survived: %d entries", len(entries))
}

// AND AN EMPTY RECORD GOES WHATEVER WROTE IT — the migration half of D-010.
//
// Fixing the code that produced a file does not remove the files it already
// produced. The empty directories are unattributable (no origin was recorded
// when they were made) but they are also unambiguous: a write always leaves a
// file behind, so an empty one can only have come from a call that asked.
func TestEmptyStateRecordsAreSwept(t *testing.T) {
	home := useTempMachineHome(t)
	root := filepath.Join(home, ".palbase", "checkouts")

	empty := filepath.Join(root, "deadbeefdeadbeef")
	require.NoError(t, os.MkdirAll(empty, 0o700))

	// One that carries state but no origin: it cannot be attributed, so it is
	// LEFT ALONE — deleting it could forget a living checkout's stack.
	kept := filepath.Join(root, "cafebabecafebabe")
	require.NoError(t, os.MkdirAll(kept, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(kept, "local.json"), []byte(`{"url":"x"}`), 0o600))

	reapDeadCheckoutStateNow()

	require.NoDirExists(t, empty, "an empty record survived")
	require.DirExists(t, kept, "a record carrying state was swept without knowing whose it is")
}

// NO TEST IN THIS PACKAGE MAY WRITE INTO THE DEVELOPER'S REAL HOME.
//
// `WritePlanFile` and `WriteLocalTarget` write under `~/.palbase/checkouts/`, and
// a test that skips `useTempMachineHome` leaves a record there naming a
// `t.TempDir()` that no longer exists — dead the moment the test ends. That is
// the litter D-010 measured at 823 directories, recreated by the suite that
// fixed it; a clean CI runner never sees it, so the gate has to be here.
func TestNoTestWritesIntoTheRealHome(t *testing.T) {
	real, err := os.UserHomeDir()
	require.NoError(t, err)
	used, err := machineStateHome()
	require.NoError(t, err)
	require.NotEqual(t, real, used,
		"this package's machine-state home is the developer's real one — every test that "+
			"writes state leaves a dead record in it")

	// And a write really does land in the throwaway one, or the seam would be
	// pointing somewhere nothing uses.
	checkout := t.TempDir()
	require.NoError(t, WritePlanFile(checkout, PlanFile{
		Version: 1, Target: PlanTarget{URL: "https://x"}, Fingerprint: "f",
	}))
	path, err := PlanStatePath(checkout)
	require.NoError(t, err)
	require.FileExists(t, path)
	require.True(t, strings.HasPrefix(path, used), "%s is not under %s", path, used)
}
