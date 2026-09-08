package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// YETİM ORTAM KLASÖRÜ BUILD'İ KIRAR — ve bu, kaldırılan tek dosyanın öteki
// biçimidir. Xcode 16 senkronize grupları `Palbase/Generated` altındaki HER
// dosyayı derliyor, yani bir ortam yeniden adlandırıldığında eskisinin klasörü
// aynı sembolleri ikinci kez üretiyor: "Multiple commands produce …
// PalbaseGenerated.stringsdata". Gerçek müşteri uygulamasında ölçüldü
// (07.09.2026, centauri): `Palbase/Generated/centauri` bir yeniden adlandırmayı
// aşmıştı; klasör kenara alınınca build GEÇTİ.
func TestOrphanedEnvironmentFolderIsRemovedButForeignFilesAreNot(t *testing.T) {
	root := t.TempDir()
	gen := filepath.Join(root, "Palbase", "Generated")
	for _, env := range []string{"main", "centauri", "handwritten"} {
		if err := os.MkdirAll(filepath.Join(gen, env), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gen, env, "PalbaseGenerated.swift"), []byte("// generated"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Bu klasörde BİZİM yazmadığımız bir dosya var: dokunulmamalı.
	if err := os.WriteFile(filepath.Join(gen, "handwritten", "Extra.swift"), []byte("// mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := removeOrphanedEnvironments(root, []string{"main"}, &out); err != nil {
		t.Fatalf("removeOrphanedEnvironments: %v", err)
	}

	if _, err := os.Stat(filepath.Join(gen, "main")); err != nil {
		t.Fatal("bildirilen ortam silindi — projenin kendi istemcisi gitti")
	}
	if _, err := os.Stat(filepath.Join(gen, "centauri")); !os.IsNotExist(err) {
		t.Fatal("yetim ortam klasörü DURUYOR — build 'Multiple commands produce' ile kırılmaya devam eder")
	}
	if _, err := os.Stat(filepath.Join(gen, "handwritten", "Extra.swift")); err != nil {
		t.Fatal("bizim yazmadığımız dosya SİLİNDİ — yazıcı üretemediğini silmemeli")
	}
	if !strings.Contains(out.String(), "handwritten") {
		t.Fatalf("dokunulmayan klasör ADLANDIRILMADI: %q", out.String())
	}
}

// TEMİZLİĞİN ÜRETİMDE BİR ÇAĞIRANI OLMALI.
//
// Fonksiyonun kendisini sınamak yetmez: çağrılmayan bir temizlik, yazılmamış
// bir temizliktir. Bu kapı, üretim yolunun onu gerçekten çağırdığını KAYNAKTAN
// okur — negatif kontrolde çağrı kaldırıldığında kırmızı olur.
func TestOrphanCleanupHasAProductionCaller(t *testing.T) {
	b, err := os.ReadFile("app_environments.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, line := range strings.Split(string(b), "\n") {
		code, _, _ := strings.Cut(line, "//")
		if strings.Contains(code, "removeOrphanedEnvironments(") && !strings.Contains(code, "func removeOrphanedEnvironments") {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("removeOrphanedEnvironments'ın üretim çağıranı YOK — yetim ortam klasörü build'i kırmaya devam eder")
	}
}

// APPLE CHECKOUT'UNUN KANITI YAPILANDIRMA DOSYASI DEĞİL, XCODE PROJESİDİR.
//
// `apple` bir zamanlar yalnız "ios yuva dosyası var mı" diye soruyordu ve bir
// BACKEND deposu o dosyayı meşru biçimde taşıyabilir: biri orada bir kez
// `palbase link --platform ios` koşmuş ve commit'lemiştir. Gerçek müşteri
// deposunda ölçüldü (08.09.2026, centauri): her `palbase push`
//
//	the push landed, but the client could not be regenerated: … the
//	palbackend-ios checkout is not resolved for this project yet
//
// ile bitiyordu — çünkü uygulama AYRI bir depoda ve burada çözülecek bir Xcode
// projesi hiç yok. Üretici, asla koşamayacağı yerde koşmaya zorlanıyordu.
func TestApplePlatformNeedsAnXcodeProjectNotJustAConfig(t *testing.T) {
	// `t.Chdir` dizini değiştirir VE testin sonunda geri alır — elle bir
	// `defer os.Chdir(wd)` yazmak, dönüşü kontrol edilmeyen bir çağrı bırakır.
	t.Chdir(t.TempDir())

	// Yuva dosyası VAR, Xcode projesi YOK — bir backend deposunun hâli.
	if err := os.MkdirAll(filepath.Join(nativeArtifactsDir, "ios"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, apple, _ := linkedPlatforms(); apple {
		t.Fatal("Xcode projesi olmayan bir checkout Apple sayıldı — her push kusur satırıyla biter")
	}

	// Aynı checkout'a bir Xcode projesi koy: ARTIK Apple checkout'udur ve
	// çözülmemiş bir proje GERÇEK bir hatadır ("build once in Xcode").
	if err := os.MkdirAll("white-label.xcodeproj", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, apple, _ := linkedPlatforms(); !apple {
		t.Fatal("Xcode projesi olan bir checkout Apple SAYILMADI — istemci hiç üretilmez")
	}
}
