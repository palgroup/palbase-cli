package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCopiedTemplateScriptCanRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable permissions")
	}
	from, to := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(from, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "scripts", "test.sh"), []byte("#!/bin/sh\nprintf 'scaffold-test-ran'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "readme.txt"), []byte("ordinary file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := copyTemplate(from, to); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("./scripts/test.sh")
	cmd.Dir = to
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "scaffold-test-ran" {
		t.Fatalf("copied test entry point cannot run: %v, %s", err, output)
	}
	info, err := os.Stat(filepath.Join(to, "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Fatal("ordinary template file became executable")
	}
}
