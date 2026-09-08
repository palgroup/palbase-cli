package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// KURAL YAZILDI DİYE UYGULANMIŞ OLMAZ — git'e sorulur.
func TestGitIgnoresTheLinkStage(t *testing.T) {
	dir := t.TempDir()
	if err := writeGitignore(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git yok")
	}
	for _, c := range []string{"init", "-q"} {
		_ = c
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	stage := filepath.Join(dir, ".palbase-link-123")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, ".env.local"), []byte("K=v\n"), 0o600); err != nil {
		t.Fatal(err)
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
