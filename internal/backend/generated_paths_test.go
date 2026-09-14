package backend

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// HİÇBİR YOL İKİ LİSTEDE BİRDEN OLAMAZ.
//
// Biri "bunu yazıyoruz, ignore et" diyor, öteki "bunu artık yazmıyoruz, sil".
// İkisinde birden duran bir yol, `link`'in her koşusunda bir kuralı yazıp aynı
// koşuda geri alması demektir: dosya her seferinde değişir, hiçbir şey hata
// vermez.
func TestNoPathIsBothGeneratedAndRetired(t *testing.T) {
	retired := map[string]bool{}
	for _, e := range retiredProjectPaths {
		retired[strings.TrimSuffix(e.path, "/")] = true
	}
	for _, e := range generatedProjectPaths {
		if retired[strings.TrimSuffix(e.path, "/")] {
			t.Errorf("%s hem üretiliyor hem emekli sayılıyor — link onu her koşuda "+
				"yazıp geri alır", e.path)
		}
	}
}

// EMEKLİ BİR KURAL, DEPODAN GERİ ALINMALI.
//
// `ensurePalbaseGitignored` yalnızca EKLEYEBİLİYORDU. Üreticisi emekli olan bir
// kural bu yüzden yerinde kalıyordu: müşterinin deposundaki dosya, bu CLI'ın
// artık yazamadığı dizinleri yazdığını söylemeye devam ediyordu — ve okuyan
// kayda inanır.
func TestGitignoreRepairTakesRetiredRulesBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	before := "node_modules/\n" +
		".palbase/local.json\n" +
		".palbase/esm/\n" +
		".palbase/jobs/\n" +
		".palbase/hooks/\n" +
		".palbase-build-controllers/\n" +
		".palbase-link-*/\n" +
		".palbase-staged-controllers/\n" +
		"palbase-env.d.ts\n" +
		"dist/\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := takeBackRetiredIgnoreRules(path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range retiredProjectPaths {
		for _, line := range strings.Split(string(got), "\n") {
			if strings.TrimSuffix(strings.TrimSpace(line), "/") == strings.TrimSuffix(e.path, "/") {
				t.Errorf("emekli kural duruyor: %q — dosya hâlâ üretilmeyen bir "+
					"dizini üretiliyor gibi gösteriyor", line)
			}
		}
	}

	// KULLANICININ KENDİ SATIRINA DOKUNULMAZ. Bir onarım, onardığından fazlasını
	// alırsa artık onarım değildir.
	if !strings.Contains(string(got), "dist/") {
		t.Error("kullanıcının kendi kuralı silindi")
	}
	// VE HÂLÂ YAZDIKLARIMIZ KALIR.
	for _, e := range generatedProjectPaths {
		if !e.ours {
			continue
		}
		if !strings.Contains(string(got), e.path) {
			t.Errorf("yazdığımız bir yolun kuralı düştü: %s", e.path)
		}
	}
}

// VE İKİNCİ KOŞU ONU GERİ GETİRMEZ.
//
// Ekleyen ve silen aynı dosyada durunca, "sil"in kaynağı `retiredProjectPaths`,
// "ekle"nin kaynağı `generatedProjectPaths` olmadıkça iki kural birbirini
// kovalar. Bu test o döngüyü ölçer: bir onarım sabit noktaya varmalı.
func TestGitignoreRepairIsStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules/\n.palbase/esm/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := takeBackRetiredIgnoreRules(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := takeBackRetiredIgnoreRules(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("onarım sabit noktaya varmıyor — her koşu dosyayı değiştiriyor:\n%q\n%q",
			first, second)
	}
}

// YENİ BİR PROJE, EMEKLİ BİR KURALLA DOĞMAZ.
func TestScaffoldedGitignoreNamesNoRetiredPath(t *testing.T) {
	dir := t.TempDir()
	if err := writeGitignore(dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range retiredProjectPaths {
		for _, line := range strings.Split(string(body), "\n") {
			if strings.TrimSuffix(strings.TrimSpace(line), "/") == strings.TrimSuffix(e.path, "/") {
				t.Errorf("şablon, üretilmeyen bir yolu ignore ediyor: %q (%s)", line, e.why)
			}
		}
	}
}

// projectTree, bir checkout'un İÇERİĞİNİ döndürür — bir komuttan önce ve sonra
// karşılaştırılmak üzere.
//
// `node_modules` atlanır: orası bu aracın ürünü değil ve bir sembolik bağın
// altında yürümek testi kurulu paketlerin boyutuna bağlar.
func projectTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("checkout okunamadı: %v", err)
	}
	return out
}

// GERÇEK BUILD, MÜŞTERİNİN PROJESİNE `palbase-env.d.ts` DIŞINDA HİÇBİR ŞEY
// YAZMAZ.
//
// Bu, listelerin değil ALETİN ölçüldüğü yer. build-check.js hazırlık ağacını
// `PROJECT_ROOT/.palbase-build-controllers`'a açıyor (archive_test.go bunu
// pinliyor) ve `PROJECT_ROOT` buraya ne verilirse odur — yani "checkout'a bir
// şey yazılıyor mu" sorusunun cevabı bir listede değil, `runBuild`'in o kökü
// nereden seçtiğinde.
//
// `palbase-env.d.ts` İSTENEN tek çıktı: projenin kendi sırlarından türeyen ve
// editörün okuduğu bildirim dosyası, `next-env.d.ts` gibi.
func TestARealBuildWritesNothingUnexpectedIntoTheProject(t *testing.T) {
	requiresRealToolchain(t)
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	requireCutoverSDK(t, dir)
	useTestParserCache(t)
	writeFixture(t, dir, goodControllerTS)

	before := projectTree(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	if err := runBuild(ctx, dir, &out); err != nil {
		t.Fatalf("build: %v\n%s", err, out.String())
	}
	after := projectTree(t, dir)

	had := map[string]bool{}
	for _, p := range before {
		had[p] = true
	}
	// FROM THE DECLARATION. This named `envTypesFile` — the bare file name — while
	// the build writes it under `palbase/`, so the entry could never match and the
	// allowance was inert. A list that cannot fire is not an allowance; it is a
	// trap for the first fixture that makes the file appear.
	//
	// …AND ITS DIRECTORY. Every build renders the one file now, a project with no
	// schema included (FR-008), so a checkout that had no `palbase/` gains that
	// directory with it. Derived from the same declaration, so the allowance is
	// exactly the file and the directories it lives in — nothing beside them.
	allowed := map[string]bool{}
	for rel := filepath.ToSlash(EnvTypesPath()); rel != "." && rel != "/"; rel = filepath.ToSlash(filepath.Dir(rel)) {
		allowed[rel] = true
	}
	for _, p := range after {
		if had[p] || allowed[p] {
			continue
		}
		t.Errorf("build müşterinin projesine yazdı: %s", p)
	}

	// NEGATİF KONTROL: gözlem GERÇEKTEN yeni bir yol görebiliyor mu? Göremeyen
	// bir gözlem her koşuda "temiz" der.
	if err := os.WriteFile(filepath.Join(dir, "canary.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, p := range projectTree(t, dir) {
		if p == "canary.txt" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("gözlem yeni bir dosyayı göremiyor — bu testin 'temiz' demesi bir şey ifade etmez")
	}
}

// HAZIRLANAMAYAN BİR AĞAÇ, ÇALIŞMA DİZİNİNE DÜŞEREK CEVAPLANMAZ.
//
// Düşüş iki şeyi birden yapıyordu: deploy'un sormadığı bir soruyu cevaplıyordu
// (`node_modules` üzerinden çözülen çıplak bir import burada geçip orada
// düşer — uyarının kendi cümlesi) ve `PROJECT_ROOT` müşterinin dizini olduğu
// için build-check.js hazırlık ağacını ORAYA açıyordu. `palbase push` bu komutu
// pre-push hook olarak kuruyor, yani basılan cevap bir deploy'un üzerine
// çıkıldığı cevap.
func TestBuildRefusesWhenTheDeployTreeCannotBeStaged(t *testing.T) {
	requiresRealToolchain(t)
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	requireCutoverSDK(t, dir)
	useTestParserCache(t)
	writeFixture(t, dir, goodControllerTS)

	prev := stageDeployTreeFn
	stageDeployTreeFn = func(string) (string, error) {
		return "", errors.New("no space left on device")
	}
	t.Cleanup(func() { stageDeployTreeFn = prev })

	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	if err == nil {
		t.Fatalf("hazırlanamayan bir ağaç için build 'OK' dedi\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "no space left on device") {
		t.Errorf("red, sebebini taşımıyor: %v", err)
	}
	// VE CHECKOUT'A HİÇBİR ŞEY YAZILMAMIŞ OLMALI — düşüşün asıl bedeli buydu.
	if _, statErr := os.Stat(filepath.Join(dir, stagedControllersDir)); !os.IsNotExist(statErr) {
		t.Errorf("red edilen bir build checkout'a hazırlık ağacı açtı: %s", stagedControllersDir)
	}
}

// VE KURULAMAYAN BİR SDK, "build OK" DİYE CEVAPLANMAZ.
//
// Bu dal `return nil` yapıyordu: çıkış 0, ekranda "build" kelimesi ve okunmuş
// tek bir controller yok. Uyarının kendisi bunu söylüyordu ("cannot validate
// locally") — sorun tam da bu: 0 ile çıkan bir komuttan akılda kalan cümle,
// geçtiğidir. `palbase push` bu komutu pre-push hook olarak kuruyor.
func TestBuildRefusesWhenTheSDKCannotBeInstalled(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, goodControllerTS)

	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	if err == nil {
		t.Fatalf("SDK'sız bir ağaç için build 'OK' dedi\n%s", out.String())
	}
	if !strings.Contains(err.Error(), backendPkg) {
		t.Errorf("red, neyin eksik olduğunu adlandırmıyor: %v", err)
	}
	// NEGATİF KONTROL: kaynak OLMAYAN bir dizin hâlâ dürüstçe geçmeli — "hiçbir
	// şey yok" ile "doğrulanamadı" ayrı cevaplar, ve bu kapı ikincisi için.
	var empty bytes.Buffer
	if err := runBuild(context.Background(), t.TempDir(), &empty); err != nil {
		t.Errorf("kaynağı olmayan bir dizin reddedildi: %v", err)
	}
}

// CLI'IN SIFIRDAN YARATTIĞI .gitignore, node_modules'ü DE KAPSAR.
//
// `link` bir checkout'ta ignore dosyası bulamayınca birini yaratıyor, ve
// yalnızca `ours` kuralları yazıyordu: bir JavaScript projesi için `node_modules/`
// içermeyen bir `.gitignore`. Canlı ölçüm (taze web checkout'u, 08.09.2026): bir
// `git add -A`, kurulu her bağımlılığı sahneye aldı — 500'den fazla dosya,
// CLI'ın az önce kurduğu depoda.
//
// `ours` bayrağı BAŞKASININ dosyasına ne EKLENECEĞİNİ söyler; hiç dosyası
// olmayan bir checkout ise hiçbir şey kürate etmemiştir.
func TestACreatedGitignoreCoversTheWholeScaffold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := takeBackRetiredIgnoreRules(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range generatedProjectPaths {
		if !strings.Contains(string(body), e.path) {
			t.Errorf("yaratılan dosya %s'i kapsamıyor (%s):\n%s", e.path, e.why, body)
		}
	}

	// VE KÜRATE EDİLMİŞ BİR DOSYAYA `node_modules/` EKLENMEZ — ayrımın öbür
	// yarısı: birinin kendi dosyasına bizim gürültümüz girmez.
	curated := filepath.Join(t.TempDir(), ".gitignore")
	if err := os.WriteFile(curated, []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := takeBackRetiredIgnoreRules(curated); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(curated)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "node_modules") {
		t.Errorf("kürate edilmiş bir dosyaya ekosistemin kuralı eklendi:\n%s", after)
	}
	// VE BİZİM HİÇBİR KURALIMIZ EKLENMEZ. Bu CLI'ın checkout'a yazdığı her şey
	// commit'leniyor: makine-yerel iki dosya `~/.palbase/checkouts/<hash>/`e
	// taşındı, üretilen tip bildirimi `palbase/` altına girdi. Kürate edilmiş
	// bir dosyaya eklenecek tek satır kalmadı.
	if strings.Contains(strings.ToLower(string(after)), "palbase") {
		t.Errorf("kürate edilmiş dosyaya artık yazılmaması gereken bir kural eklendi:\n%s", after)
	}
}

// `init`, KENDİ ignore CEVABINI VERMEZ.
//
// Bir dosyayı iki mekanizma bakımlıyorsa, bir soruya iki cevap var demektir ve
// sessiz olanı yanlıştır: `writeGitignore`'un kendi dalı "içinde node_modules
// geçiyorsa hiç dokunma" diyordu, yani zaten bir ignore dosyası taşıyan bir
// dizine `palbase init` çalıştırınca `.palbase/local.json` ve üretilen tipler
// ignore EDİLMİYORDU — ve `.palbase` toptan kuralı daraltılmadan kalıyordu ki o
// kural sözleşmeyi de götürür, bir sonraki klon derleyemez.
func TestInitDoesNotAnswerTheIgnoreQuestionOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	// Kürate edilmiş, node_modules'ü zaten olan VE `.palbase`'i toptan alan bir
	// dosya — eski dal buna hiç dokunmuyordu.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("node_modules/\ndist/\n.palbase/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeGitignore(dir); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == ".palbase" || strings.TrimSpace(line) == ".palbase/" {
			t.Errorf("toptan `.palbase` kuralı duruyor — sözleşme ignore ediliyor, klon derleyemez:\n%s", got)
		}
	}
	if strings.Contains(strings.ToLower(got), "palbase") {
		t.Errorf("`init` hâlâ bir palbase yolunu ignore ediyor:\n%s", got)
	}
	if !strings.Contains(got, "dist/") {
		t.Errorf("kullanıcının kendi kuralı silindi:\n%s", got)
	}
}

// BİLDİRİM, YAZANLARIN KULLANDIĞI YOL ÜRETİCİLERİNİ KAPSAMALI.
//
// `localPath` ARTIK BURADA DEĞİL, ve bu bir kapsam daralması değil genişlemesi:
// checkout'ta ignore edilmesi gereken bir yol olmaktan çıktı, `~/.palbase`e
// taşındı. Onu ölçen kapı `machine_state_test.go`'da ve orada iddia daha güçlü —
// "ignore ediliyor" değil, "depoda HİÇ YOK".
//
// Elde tutulan bir liste, kendi kümesinin büyümesini göremez: `.palbase/plan.json`
// aylarca yazıldı ve hiçbir kural onu kapsamadı — `link`'in bastığı
// "commit .palbase/" cümlesi onu içeri alırdı. Kapı bu yüzden listeyi değil,
// dosyayı YAZAN fonksiyonların ürettiği yolları okuyor: bir yeniden adlandırma
// iddiayı kodla birlikte taşır.
func TestEveryPerMachinePathHelperLivesOutsideTheCheckout(t *testing.T) {
	root := t.TempDir()
	plan, err := planFilePath(root)
	if err != nil {
		t.Fatal(err)
	}
	local, err := LocalStatePath(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path string }{
		{"planFilePath (palbase plan)", plan},
		{"LocalStatePath (palbase start)", local},
	} {
		rel, relErr := filepath.Rel(root, tc.path)
		if relErr == nil && !strings.HasPrefix(rel, "..") {
			t.Errorf("%s → %s hâlâ checkout'un İÇİNDE; bu makinenin durumu müşterinin "+
				"deposunda duramaz", tc.name, rel)
		}
	}

	// VE ARTIK IGNORE EDİLECEK BİR ŞEY YOK. Checkout'a yazılan her şey
	// commit'leniyor, yani bu CLI'ın `.gitignore`a koyacağı tek bir satırı bile
	// kalmadı — ekosistemin kendi kuralları dışında (NFR-001).
	body := gitignoreScaffold()
	if strings.Contains(strings.ToLower(body), "palbase") {
		t.Errorf("iskelet hâlâ bir palbase yolunu ignore ediyor:\n%s", body)
	}
}

// gitCheckout, dir'i GERÇEK bir git deposu yapar ve verilen yolları index'e
// alır. "İzleniyor mu" sorusunun tek otoritesi git'in kendi index'i; sahte bir
// fikstür, süpürücünün sorduğu soruyu değil kendini ölçerdi.
//
// Makine-global hiçbir şey okunmaz ya da yazılmaz: global ve sistem git
// yapılandırması boşa yönlendirilir, böylece bir kişinin `core.hooksPath`'i ya da
// `init.templateDir`'i fikstüre ulaşmaz. HOME'a DOKUNULMAZ — `inScratchCheckout`
// + `linkedAs` kimlik bilgisini oraya yazmış olabilir.
func gitCheckout(t *testing.T, dir string, tracked ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		requireToolOnCI(t, "git", err)
		t.Skip("git yok — izlenme sorusu ölçülemez")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	if len(tracked) > 0 {
		git(append([]string{"add", "--"}, tracked...)...)
	}
}

// İZLENMEYEN GİZLİ KÖK SÜPÜRÜLÜR (FR-010).
//
// `.palbase` bir zamanlar sözleşmeyi taşıdığı için "ASLA dokunulmaz" sayılıyordu;
// o dosya artık `palbase/` altında. Kullanıcının diskinde 36 fosil dizin / ~30 MB
// ölçüldü ve hiçbir fiil onları toplamıyordu. Git'in izlemediği bir `.palbase`
// kimsenin commit'i değildir: bir ürün kalıntısıdır ve gider.
func TestReapSweepsAnUntrackedHiddenRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("git deposunda, izlenmiyor", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, ".palbase/project.json", `{"url":"x"}`)
		mustWrite(t, dir, "package.json", `{}`)
		// NEGATİF KONTROL: depoda izlenen bir dosya VAR. "Bu depoda herhangi bir şey
		// izleniyor mu" diye soran bir süpürücü `.palbase`'i burada yanlışlıkla
		// korurdu.
		gitCheckout(t, dir, "package.json")

		kept := reapRetiredArtifacts(dir)

		if len(kept) != 0 {
			t.Errorf("izlenmeyen bir kök korunmuş sayıldı: %v", kept)
		}
		if _, err := os.Stat(filepath.Join(dir, ".palbase")); !os.IsNotExist(err) {
			t.Errorf("izlenmeyen .palbase süpürülmedi (stat: %v)", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
			t.Errorf("süpürücü kişinin kendi dosyasına dokundu: %v", err)
		}
	})

	t.Run("git deposu olmayan dizinde", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, ".palbase/project.json", `{"url":"x"}`)

		kept := reapRetiredArtifacts(dir)

		if len(kept) != 0 {
			t.Errorf("depo olmayan bir dizinde kök korunmuş sayıldı: %v", kept)
		}
		if _, err := os.Stat(filepath.Join(dir, ".palbase")); !os.IsNotExist(err) {
			t.Errorf("depo olmayan dizindeki .palbase süpürülmedi (stat: %v)", err)
		}
	})
}

// trackedHiddenRootLine is what the sweep returns for a `.palbase` it will not
// delete because git tracks a file under it (or could not be asked) — the whole
// sentence every verb prints after "kept ".
const trackedHiddenRootLine = ".palbase — it may be committed (git tracks a file under it, or could not be asked); remove it in a commit"

// WHAT NO CLI WROTE IS NOT THE SWEEP'S TO DELETE (FR-010, D-26).
//
// Measured on a real checkout's copy: `.palbase/recovery/` held a maintenance
// procedure's SQL and pod snapshots — files no palbase CLI has ever written
// (`git log -S` over every version: nothing) — and an untracked `.palbase` was
// removed with them in it. The sweep deletes the CLI's own products under the
// hidden root, removes the directory only when that leaves it empty, and names
// every entry it left, whether or not the checkout is a repository.
func TestReapLeavesWhatNoCLIWroteUnderAnUntrackedHiddenRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	products := []string{
		".palbase/esm/controllers/controllers.js",
		".palbase/cache/dev/1-x/controllers.js",
		".palbase/jobs/jobs.manifest.json",
		".palbase/hooks/hooks.manifest.json",
		".palbase/openapi/main.json",
		".palbase/openapi/main.roles.json",
		".palbase/ios/palbase-config.json",
		".palbase/android/palbase-config.json",
		".palbase/project.json",
		".palbase/local.json",
		".palbase/plan.json",
		".palbase/selection.json",
		".palbase/config.json",
	}
	foreign := map[string]string{
		".palbase/recovery/volume-snapshot.json":              `{"snapshot":"pvc-1"}`,
		".palbase/recovery/oauth-000014-empty-identities.sql": "delete from auth.identities where false;\n",
		".palbase/openapi/notes.md":                           "why main.json looks odd\n",
		".palbase/ios/Secrets.xcconfig":                       "KEY = value\n",
	}
	wantKept := []string{
		".palbase/ios/Secrets.xcconfig — no palbase CLI wrote it; the CLI's own files in the hidden root were removed and this was left where it is",
		".palbase/openapi/notes.md — no palbase CLI wrote it; the CLI's own files in the hidden root were removed and this was left where it is",
		".palbase/recovery — no palbase CLI wrote it; the CLI's own files in the hidden root were removed and this was left where it is",
	}
	for _, repo := range []bool{false, true} {
		name := "git deposu olmayan dizinde"
		if repo {
			name = "git deposunda, izlenmiyor"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, dir, "package.json", `{}`)
			for _, p := range products {
				mustWrite(t, dir, p, "// written by a palbase CLI\n")
			}
			for p, body := range foreign {
				mustWrite(t, dir, p, body)
			}
			if repo {
				gitCheckout(t, dir, "package.json")
			}

			kept := reapRetiredArtifacts(dir)

			for _, p := range products {
				if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
					t.Errorf("a CLI product survived: %s (stat: %v)", p, err)
				}
			}
			for p, body := range foreign {
				got, err := os.ReadFile(filepath.Join(dir, p))
				if err != nil || string(got) != body {
					t.Errorf("a file no CLI wrote was not left as it was: %s (err: %v, body: %q)", p, err, got)
				}
			}
			if !slices.Equal(kept, wantKept) {
				t.Errorf("the entries left behind were not named, in order:\n got %q\nwant %q", kept, wantKept)
			}
		})
	}

	// NEGATİF KONTROL: yalnız CLI ürünü taşıyan izlenmeyen kök bütünüyle gider.
	t.Run("yalnız ürün", func(t *testing.T) {
		dir := t.TempDir()
		for _, p := range products {
			mustWrite(t, dir, p, "// written by a palbase CLI\n")
		}
		kept := reapRetiredArtifacts(dir)
		if len(kept) != 0 {
			t.Errorf("a hidden root holding only products was reported as kept: %q", kept)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".palbase")); !os.IsNotExist(err) {
			t.Errorf("a hidden root holding only products was not removed (lstat: %v)", err)
		}
	})
}

// İZLENEN GİZLİ KÖK SİLİNMEZ VE ADIYLA DÖNER (FR-010).
//
// Commit'lenmiş bir `.palbase`'i silmek, bir kişinin incelemediği bir değişikliği
// bir ilerleme satırının arkasında yapmaktır. Silme onun commit'i; araç yalnız
// yolu adlandırır — dönüş değeri çağıranın raporlayacağı şeydir.
func TestReapKeepsATrackedHiddenRootAndNamesIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("checkout deponun kökü", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, ".palbase/project.json", `{"url":"x"}`)
		mustWrite(t, dir, ".palbase/esm/controllers/controllers.js", "stale\n")
		gitCheckout(t, dir, ".palbase/project.json")

		kept := reapRetiredArtifacts(dir)

		if !slices.Equal(kept, []string{trackedHiddenRootLine}) {
			t.Fatalf("izlenen kök adıyla dönmedi: %v", kept)
		}
		if _, err := os.Stat(filepath.Join(dir, ".palbase", "project.json")); err != nil {
			t.Errorf("izlenen sözleşme silindi: %v", err)
		}
		// İZLENEN KÖKÜN İÇİNDEKİ ÜRÜN YİNE GİDER (D-5): korunan dizin değil, kişinin
		// commit'i.
		if _, err := os.Stat(filepath.Join(dir, ".palbase", "esm")); !os.IsNotExist(err) {
			t.Errorf("izlenen kökün içindeki derleme ürünü kaldı (stat: %v)", err)
		}
	})

	// `palbase push`, `palbase build`i pre-push hook olarak kuruyor ve hook ortamı
	// deponun KÖKÜNE göreli `GIT_DIR`/`GIT_INDEX_FILE` taşıyor. Backend deponun bir
	// ALT dizinindeyse (`smartex/palbase` gibi) `git -C <alt dizin>` o değişkenleri
	// yanlış dizine göre çözer, "not a git repository" der — ve cevapsızlık
	// "izlenmiyor" okunup commit'li dizin silinir.
	t.Run("checkout deponun alt dizini, git hook ortamında", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, "backend")
		mustWrite(t, dir, ".palbase/project.json", `{"url":"x"}`)
		gitCheckout(t, repo, "backend/.palbase/project.json")
		t.Setenv("GIT_DIR", ".git")
		t.Setenv("GIT_INDEX_FILE", ".git/index")

		kept := reapRetiredArtifacts(dir)

		if !slices.Equal(kept, []string{trackedHiddenRootLine}) {
			t.Fatalf("hook ortamında izlenen kök adıyla dönmedi: %v", kept)
		}
		if _, err := os.Stat(filepath.Join(dir, ".palbase", "project.json")); err != nil {
			t.Errorf("hook ortamında izlenen sözleşme silindi: %v", err)
		}
	})

	// macOS ve Windows'ta `.palbase` ile `.Palbase` TEK dizin. Harfe duyarlı bir
	// izlenme sorusu `.Palbase/project.json`'ı görmez, `os.RemoveAll(".palbase")`
	// ise onu siler.
	t.Run("izlenen kök başka harf büyüklüğüyle", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, ".Palbase/project.json", `{"url":"x"}`)
		gitCheckout(t, dir, ".Palbase/project.json")

		kept := reapRetiredArtifacts(dir)

		if _, err := os.Stat(filepath.Join(dir, ".Palbase", "project.json")); err != nil {
			t.Fatalf("başka harfle izlenen sözleşme silindi: %v", err)
		}
		// Duyarsız dosya sisteminde `.palbase` bu dizinin kendisidir ve adıyla
		// raporlanmalı; duyarlı birinde ayrı bir addır ve süpürülecek bir şey yoktur.
		if _, err := os.Lstat(filepath.Join(dir, ".palbase")); err == nil && !slices.Equal(kept, []string{trackedHiddenRootLine}) {
			t.Errorf("duyarsız dosya sisteminde izlenen kök adıyla dönmedi: %v", kept)
		}
	})
}

// GIT CEVAP VEREMEZSE COMMIT'Lİ OLABİLECEK KÖK SİLİNMEZ (FR-010, review-T002).
//
// "Cevapsızlık = izlenmiyor" kuralı, bir silme yolunda FAIL-OPEN'dı: git'in
// soruyu yanıtlayamadığı her durumda — keşfi kesen bir ortam değişkeni, git'in
// okumayı reddettiği bir depo, PATH'te olmayan git — commit'li bir `.palbase`
// sıradan bir ürün sayılıp siliniyordu. "İzlenmiyor" cevabını yalnız iki şey
// verebilir: temiz koşup hiçbir yol basmayan git, ve dizinden köke kadar hiç
// `.git` olmaması.
func TestReapKeepsAHiddenRootGitCannotAnswerFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Ölçüldü (review-T002): `GIT_CEILING_DIRECTORIES` deponun kökünü
	// gösterdiğinde alt dizinden sorulan `git ls-files` "not a git repository"
	// ile düşüyor. CI sarmalayıcılarından miras kalabilen bir değişken.
	t.Run("keşif sınırı depo kökünü dışarıda bırakıyor", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, "backend")
		mustWrite(t, dir, ".palbase/project.json", `{"url":"x"}`)
		gitCheckout(t, repo, "backend/.palbase/project.json")
		t.Setenv("GIT_CEILING_DIRECTORIES", repo)

		kept := reapRetiredArtifacts(dir)

		if _, err := os.Stat(filepath.Join(dir, ".palbase", "project.json")); err != nil {
			t.Fatalf("keşif sınırı altında izlenen sözleşme silindi: %v", err)
		}
		if !slices.Equal(kept, []string{trackedHiddenRootLine}) {
			t.Errorf("keşif sınırı altında izlenen kök adıyla dönmedi: %v", kept)
		}
	})

	// Git'in okuyamadığı bir depo: `.git` bir `gitdir:` işaretçisi ve gösterdiği
	// yer yok. `safe.directory`'nin "dubious ownership" reddiyle aynı kod yoluna
	// düşer — git sıfırdan farklı çıkar, hiçbir şey basmaz.
	t.Run("git depoyu okuyamıyor", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, ".palbase/project.json", `{"url":"x"}`)
		mustWrite(t, dir, ".git", "gitdir: "+filepath.Join(t.TempDir(), "gone")+"\n")

		kept := reapRetiredArtifacts(dir)

		if _, err := os.Stat(filepath.Join(dir, ".palbase", "project.json")); err != nil {
			t.Fatalf("okunamayan bir depoda .palbase silindi: %v", err)
		}
		if !slices.Equal(kept, []string{trackedHiddenRootLine}) {
			t.Errorf("okunamayan bir depoda kök adıyla dönmedi: %v", kept)
		}
	})

	t.Run("depo var ama git PATH'te yok", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, ".palbase/project.json", `{"url":"x"}`)
		gitCheckout(t, dir, ".palbase/project.json")
		t.Setenv("PATH", t.TempDir())

		kept := reapRetiredArtifacts(dir)

		if _, err := os.Stat(filepath.Join(dir, ".palbase", "project.json")); err != nil {
			t.Fatalf("git'siz bir makinede commit'li .palbase silindi: %v", err)
		}
		if !slices.Equal(kept, []string{trackedHiddenRootLine}) {
			t.Errorf("git'siz bir makinede kök adıyla dönmedi: %v", kept)
		}
	})

	// NEGATİF KONTROL: hiç depo yoksa — git PATH'te olsun olmasın — hiçbir şey
	// commit'lenmiş olamaz ve kök süpürülür. Aksi hâlde "belirsizse koru" kuralı
	// FR-010'un asıl vakasını (izlenmeyen fosil) da kurtarırdı.
	t.Run("depo yok, git de yok", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, ".palbase/esm/controllers/controllers.js", "stale\n")
		t.Setenv("PATH", t.TempDir())

		kept := reapRetiredArtifacts(dir)

		if len(kept) != 0 {
			t.Errorf("depo olmayan bir dizinde kök korundu: %v", kept)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".palbase")); !os.IsNotExist(err) {
			t.Errorf("depo olmayan bir dizinde fosil .palbase kaldı (lstat: %v)", err)
		}
	})
}

// CLI'IN ÜRETTİĞİ HER ŞEY, GIT İZLESE BİLE GİDER (D-5).
//
// Yanlışlıkla commit'lenmiş bir hazırlık ağacı yine bir hazırlık ağacıdır. Korunma
// yalnız `keepIfTracked` diye BİLDİRİLEN girişe aittir; izlenme sorusu ürünlere
// sorulmaz.
func TestReapRemovesWhatTheCLIProducedEvenWhenTracked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	mustWrite(t, dir, deployStagingDir+"/b.ts", "export {}\n")
	mustWrite(t, dir, stagedControllersDir+"/a.ts", "export {}\n")
	mustWrite(t, dir, ".palbase/local.json", `{}`)
	gitCheckout(t, dir, deployStagingDir+"/b.ts", stagedControllersDir+"/a.ts")

	kept := reapRetiredArtifacts(dir)

	for _, p := range []string{deployStagingDir, stagedControllersDir} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("izlenen bir CLI ürünü kaldı: %s (stat: %v)", p, err)
		}
	}
	// SINIR BİR YOL BİLEŞENİDİR: `.palbase-staged-controllers/…` izleniyor diye
	// `.palbase` izlenmiş sayılmaz. Önek eşleşmesiyle soran bir sorgu burada kökü
	// korurdu.
	if len(kept) != 0 {
		t.Errorf("izlenen bir KOMŞU yüzünden kök korunmuş sayıldı: %v", kept)
	}
	if _, err := os.Stat(filepath.Join(dir, ".palbase")); !os.IsNotExist(err) {
		t.Errorf("izlenmeyen .palbase, izlenen bir komşu yüzünden kaldı (stat: %v)", err)
	}
}
