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
	// ONE CLOUD: the mode name no longer selects an address. It used to, and
	// `prod` — the default — named a deployment that has never existed.
	if r.Endpoints.Studio != ModeProd.Endpoints().Studio {
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
	if r.Endpoints.Studio != ModeDev.Endpoints().Studio {
		t.Fatalf("unexpected studio url: %s", r.Endpoints.Studio)
	}
	if r.Endpoints.PlatformAPI != "https://api.v2.palbase.studio" {
		t.Fatalf("unexpected platform API url: %s", r.Endpoints.PlatformAPI)
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
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

// THERE IS ONE CLOUD, AND `--mode prod` MUST REACH IT.
//
// `prod` was the DEFAULT and pointed at `palbase.studio`, which has never been
// deployed — measured 24.08.2026: no `pbc-prod-fleet-rg` in Azure, and
// `api.palbase.studio` answers NXDOMAIN. So a fresh install failed every command
// with "no such host", which the credential layer reported as "no credential for
// this project": two errors, neither naming the cause. A person following the
// documented default could not use the CLI at all.
func TestEveryModeReachesTheSameCloud(t *testing.T) {
	prod := ModeProd.Endpoints()
	dev := ModeDev.Endpoints()

	if prod != dev {
		t.Fatalf("prod and dev resolve differently:\n  prod=%+v\n  dev=%+v", prod, dev)
	}
	for name, v := range map[string]string{
		"Studio": prod.Studio, "Auth": prod.Auth,
		"PlatformAPI": prod.PlatformAPI, "PublicHost": prod.PublicHost,
	} {
		if v == "" {
			t.Errorf("%s is empty — a surface with no address cannot be reached", name)
		}
	}
}

// The DEFAULT is what a fresh install uses, and it must be the cloud that is
// actually up. This is the assertion that would have caught the trap.
func TestTheDefaultResolvesToTheLiveCloud(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_MODE", "")

	r, err := Resolve("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.Endpoints != ModeProd.Endpoints() {
		t.Errorf("the default resolved somewhere else: %+v", r.Endpoints)
	}
	// The tenant suffix and the API host must not be the same string: every
	// management call would land on a tenant that has never heard of it.
	if r.Endpoints.PublicHost == r.Endpoints.PlatformAPI {
		t.Error("the tenant suffix and the control plane share a host")
	}
}

// A mode name still in somebody's ~/.palbase/config.json keeps working. Making a
// person edit a file to keep using the CLI is a migration, not a simplification.
func TestALegacyModeNameStillResolves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"prod", "dev"} {
		if _, err := Resolve(name); err != nil {
			t.Errorf("--mode %s: %v", name, err)
		}
	}
	if _, err := Resolve("staging"); err == nil {
		t.Error("an unknown mode was accepted — it would resolve to nowhere")
	}
}
