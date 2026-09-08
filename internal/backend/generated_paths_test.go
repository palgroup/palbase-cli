package backend

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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

	if err := ensurePalbaseGitignored(path); err != nil {
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
	if err := ensurePalbaseGitignored(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePalbaseGitignored(path); err != nil {
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
	allowed := map[string]bool{envTypesFile: true}
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
