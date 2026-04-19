package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve_Default(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("HOME", t.TempDir())

	r, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != ModeProd {
		t.Fatalf("expected prod, got %s", r.Mode)
	}
	if r.Source != "default" {
		t.Fatalf("expected source=default, got %s", r.Source)
	}
	if r.Endpoints.Studio != "https://palbase.studio" {
		t.Fatalf("unexpected studio url: %s", r.Endpoints.Studio)
	}
}

func TestResolve_Flag(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("HOME", t.TempDir())

	r, err := Resolve("dev")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != ModeDev || r.Source != "flag" {
		t.Fatalf("got %+v", r)
	}
	if r.Endpoints.Studio != "https://dev.palbase.studio" {
		t.Fatalf("unexpected studio url: %s", r.Endpoints.Studio)
	}
}

func TestResolve_Env(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_MODE", "dev")

	r, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != ModeDev || r.Source != "env" {
		t.Fatalf("got %+v", r)
	}
}

func TestResolve_ConfigFile(t *testing.T) {
	withCleanEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".palbase"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Save(File{Mode: ModeDev}); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != ModeDev || r.Source != "config" {
		t.Fatalf("got %+v", r)
	}
}

func TestResolve_Precedence_FlagOverridesEnvOverridesConfig(t *testing.T) {
	withCleanEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".palbase"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Save(File{Mode: ModeDev}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PALBASE_MODE", "prod")
	r, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "env" || r.Mode != ModeProd {
		t.Fatalf("env should win over config: %+v", r)
	}

	r, err = Resolve("dev")
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "flag" || r.Mode != ModeDev {
		t.Fatalf("flag should win over env: %+v", r)
	}
}

func TestResolve_InvalidMode(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("HOME", t.TempDir())

	if _, err := Resolve("staging"); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestResolve_EnvEndpointOverrides(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_AUTH_URL", "http://localhost:9999")

	r, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if r.Endpoints.Auth != "http://localhost:9999" {
		t.Fatalf("expected env override: %s", r.Endpoints.Auth)
	}
}

func withCleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PALBASE_MODE", "PALBASE_STUDIO_URL", "PALBASE_AUTH_URL", "PALBASE_PLATFORM_URL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}
