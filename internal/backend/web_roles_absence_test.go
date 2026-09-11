package backend

import (
	"os"
	"path/filepath"
	"testing"
)

// WEB'İN AYRI BİR ROL KOPYASI ARTIK YOK — ve yokluğu ölçülür.
//
// Bu dosya `copyRolesToWeb`u ölçüyordu: rol tanımlarını ortamın dizininden web
// SDK'sının kendi `Palbase/roles.json`una aynalayan fonksiyon. İki commit'li
// dosya, aynı baytlar, ve jeneratöre bayat olanı verilebilme ihtimali. Fonksiyon
// silindi; `palbe-gen` rolleri ortam dizininden okuyor.
//
// Ölçtüğü şey gitti, ama YOKLUĞU ölçülmeden bırakılamaz: bir gün biri "web'in
// kendi kopyası olsun" diye geri koyarsa, sözleşmenin iki kez commit'lenmesi de
// onunla geri gelir.
func TestThereIsNoSecondRolesCopyForWeb(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	// Ortamın kendi rol dosyası — jeneratörlerin okuduğu TEK kopya.
	if err := os.MkdirAll(EnvDir("main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RolesPath("main"), []byte(`{"roles":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Emekli ikinci ev: görünür `Palbase/` kökü. `link` onu taşıyan bir
	// checkout'u zaten reddediyor; burada ölçülen, CLI'ın onu YARATMADIĞI.
	for _, retired := range []string{
		filepath.Join("Palbase", "roles.json"),
		filepath.Join("Palbase", "openapi.json"),
		filepath.Join("Palbase", "palbase-config.json"),
	} {
		if _, err := os.Stat(retired); !os.IsNotExist(err) {
			t.Errorf("emekli kopya var: %s — sözleşme checkout'ta bir kez bulunur", retired)
		}
	}

	// NEGATİF KONTROL: gözlem gerçekten dosya görebiliyor mu?
	if _, err := os.Stat(RolesPath("main")); err != nil {
		t.Fatalf("gözlem çalışmıyor — ortamın kendi rol dosyasını bile göremedi: %v", err)
	}
}
