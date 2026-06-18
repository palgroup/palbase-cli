package auth

import (
	"errors"
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

// TestResolveProjectRef is the single source of truth for "which project am I
// in" — backend/db/secret/notifications all key off it. The override path, the
// linked-config path, and the ErrNotLinked sentinel (which the backend serve
// flow branches on via errors.Is) are all locked here.
func TestResolveProjectRef(t *testing.T) {
	t.Run("override wins without touching config", func(t *testing.T) {
		chdirTemp(t) // empty dir: no .palbase/config.json
		ref, err := ResolveProjectRef("explicitm8p6zm")
		if err != nil {
			t.Fatalf("override should not error: %v", err)
		}
		if ref != "explicitm8p6zm" {
			t.Fatalf("ref = %q, want explicitm8p6zm", ref)
		}
	})

	t.Run("reads ref from linked config", func(t *testing.T) {
		chdirTemp(t)
		if err := SaveProjectConfig(&ProjectConfig{Ref: "todoappm8p6zm", DefaultEnv: "main"}); err != nil {
			t.Fatalf("seed link: %v", err)
		}
		ref, err := ResolveProjectRef("")
		if err != nil {
			t.Fatalf("ResolveProjectRef: %v", err)
		}
		if ref != "todoappm8p6zm" {
			t.Fatalf("ref = %q, want todoappm8p6zm", ref)
		}
	})

	t.Run("no config returns ErrNotLinked sentinel", func(t *testing.T) {
		chdirTemp(t) // no .palbase/config.json
		_, err := ResolveProjectRef("")
		if !errors.Is(err, ErrNotLinked) {
			t.Fatalf("err = %v, want errors.Is ErrNotLinked", err)
		}
	})

	t.Run("config without ref returns ErrNotLinked sentinel", func(t *testing.T) {
		chdirTemp(t)
		// Write a config.json with an empty ref directly — SaveProjectConfig is
		// happy to persist it; LoadProjectConfig rejects it, which ResolveProjectRef
		// must collapse to the sentinel so callers can branch on errors.Is.
		if err := os.MkdirAll(".palbase", 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(".palbase", "config.json"), []byte(`{"ref":""}`), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := ResolveProjectRef("")
		if !errors.Is(err, ErrNotLinked) {
			t.Fatalf("err = %v, want errors.Is ErrNotLinked", err)
		}
	})
}

func TestProjectConfig_ModeRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := &ProjectConfig{
		Ref:        "todoappm8p6zm",
		DefaultEnv: "main",
		Mode:       "platform",
		GithubRepo: "",
	}
	if err := SaveProjectConfigIn(dir, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	_ = os.Chdir(dir)

	got, err := LoadProjectConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Mode != "platform" {
		t.Fatalf("mode = %q, want platform", got.Mode)
	}
}
