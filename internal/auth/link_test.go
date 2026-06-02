package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// chdirTemp switches the cwd to a fresh temp dir for the duration of the
// test (link/unlink operate on ".palbase/config.json" relative to cwd).
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return dir
}

func TestUnlinkProjectConfigRemovesConfig(t *testing.T) {
	chdirTemp(t)
	if err := SaveProjectConfig(&ProjectConfig{Ref: "centauri0eme7m", DefaultEnv: "main"}); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	cfgPath := filepath.Join(".palbase", "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("precondition: config should exist: %v", err)
	}

	if err := UnlinkProjectConfig(); err != nil {
		t.Fatalf("UnlinkProjectConfig: %v", err)
	}

	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("config.json should be gone, stat err = %v", err)
	}
}

func TestUnlinkProjectConfigIsIdempotent(t *testing.T) {
	chdirTemp(t)
	// No link present — unlink must succeed (no-op), not error.
	if err := UnlinkProjectConfig(); err != nil {
		t.Fatalf("UnlinkProjectConfig on unlinked dir: %v", err)
	}
}
