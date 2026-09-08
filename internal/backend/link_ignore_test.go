package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ESKİ BİR CLI'IN BIRAKTIĞI HAZIRLIK ALANI, IGNORE EDİLMEZ — SİLİNİR.
//
// Bu test önce "git onu görüyor mu" diye soruyordu ve cevabı bir `.gitignore`
// kuralıydı. Kural, alanın müşterinin deposunda DOĞDUĞU dünyaya aitti; artık
// doğmuyor (`newLinkStage` işletim sisteminin temp'inde açıyor), dolayısıyla o
// kuralı istemek üretilmeyen bir dizinin üretildiğini söylemek olurdu.
//
// Ölçülen şey değişti: 07.09.2026'da müşterinin deposunda bulunan kalıntının —
// bir gün yaşamış, içinde bir API anahtarı taşıyan `.palbase-link-1344746409/` —
// bugün ne olduğu. Cevap: bir sonraki `link` onu SİLER, ve git'in gözünde de
// kalmaz. Gizlemek değil kaldırmak, çünkü ignore edilen bir sır yine de diskte
// durur.
func TestAnOlderCLIsLinkStageIsReapedNotIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git yok")
	}
	dir := t.TempDir()
	if err := writeGitignore(dir); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	stage := filepath.Join(dir, linkStagePrefix+"1344746409")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(stage, ".env.local")
	if err := os.WriteFile(secret, []byte("K=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// `link`'in yaptığı ilk şey: yeni alanı açmadan önce eskisini toplamak.
	fresh, err := newLinkStage(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fresh) })

	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("kalıntı duruyor: %s — içindeki anahtarla birlikte", stage)
	}
	// VE YENİ ALAN MÜŞTERİNİN PROJESİNDE DEĞİL.
	if rel, err := filepath.Rel(dir, fresh); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("hazırlık alanı müşterinin projesinde açıldı: %s", fresh)
	}

	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v %s", err, out)
	}
	if strings.Contains(string(out), "palbase-link") {
		t.Errorf("hazırlık alanı git'in gözünde duruyor:\n%s", out)
	}
	// NEGATİF KONTROL: git gerçekten bakıyor mu — ignore EDİLMEYEN bir dosya
	// görünmeli, yoksa bu test her şeyi ignore sanır.
	if err := os.WriteFile(filepath.Join(dir, "kaynak.ts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out2, _ := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if !strings.Contains(string(out2), "kaynak.ts") {
		t.Fatalf("negatif kontrol düştü: git hiçbir şey görmüyor:\n%s", out2)
	}
}
