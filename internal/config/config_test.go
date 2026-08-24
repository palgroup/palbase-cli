package config

// One cloud, one address set.
//
// This file used to test a MODE: prod vs dev, flag over env over config file,
// and an "invalid mode" refusal. All of it described a choice that no longer
// exists — and the choice was not merely redundant, it was harmful: `prod` was
// the DEFAULT and named a deployment that has never been built (measured
// 24.08.2026: no `pbc-prod-fleet-rg` in Azure, `api.palbase.studio` NXDOMAIN),
// so a fresh install failed every command with "no such host", which the
// credential layer reported as "no credential for this project".
//
// What is left is what still means something: the addresses are populated and
// distinct, and the env escape hatches work.

import (
	"os"
	"testing"
)

func withCleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"PALBASE_STUDIO_URL", "PALBASE_AUTH_URL", "PALBASE_PLATFORM_URL"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

// A fresh install must reach the cloud that is actually up. This is the
// assertion that would have caught the trap.
func TestTheDefaultResolvesToTheLiveCloud(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("HOME", t.TempDir())

	r, err := Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.Endpoints != theCloud {
		t.Fatalf("the default resolved somewhere else: %+v", r.Endpoints)
	}
	for name, v := range map[string]string{
		"Studio": r.Endpoints.Studio, "Auth": r.Endpoints.Auth,
		"PlatformAPI": r.Endpoints.PlatformAPI, "PublicHost": r.Endpoints.PublicHost,
	} {
		if v == "" {
			t.Errorf("%s is empty — a surface with no address cannot be reached", name)
		}
	}
	// The tenant suffix and the control plane must not be the same host: every
	// management call would land on a tenant that has never heard of it.
	if r.Endpoints.PublicHost == r.Endpoints.PlatformAPI {
		t.Error("the tenant suffix and the control plane share a host")
	}
}

// A stale `{"mode":"dev"}` in ~/.palbase/config.json must not break anything.
// Making a person edit a file to keep using the CLI is a migration, not a
// simplification.
func TestAStaleConfigFileIsHarmless(t *testing.T) {
	withCleanEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(home+"/.palbase", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home+"/.palbase/config.json", []byte(`{"mode":"dev"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve()
	if err != nil {
		t.Fatalf("a stale config file broke resolution: %v", err)
	}
	if r.Endpoints != theCloud {
		t.Errorf("a stale mode still steered the CLI: %+v", r.Endpoints)
	}
}

// The escape hatch: someone running against a stack of their own names an
// ADDRESS, which is a thing this binary does not have to keep true — unlike a
// mode, which selects from a list that can go stale.
func TestEnvOverridesNameTheAddress(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_AUTH_URL", "http://localhost:9999")
	t.Setenv("PALBASE_PLATFORM_URL", "http://localhost:8888")

	r, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if r.Endpoints.Auth != "http://localhost:9999" {
		t.Errorf("auth override ignored: %s", r.Endpoints.Auth)
	}
	if r.Endpoints.PlatformAPI != "http://localhost:8888" {
		t.Errorf("platform override ignored: %s", r.Endpoints.PlatformAPI)
	}
	// Untouched surfaces keep the default.
	if r.Endpoints.Studio != theCloud.Studio {
		t.Errorf("an unset override changed Studio: %s", r.Endpoints.Studio)
	}
}

// DefaultPlatformAPI is what tells "the configured cloud" from "somewhere
// else" — the sign-in banner reads it to decide whether to say anything.
func TestDefaultPlatformAPIIgnoresOverrides(t *testing.T) {
	withCleanEnv(t)
	t.Setenv("PALBASE_PLATFORM_URL", "http://localhost:8888")
	if DefaultPlatformAPI() != theCloud.PlatformAPI {
		t.Errorf("an override changed the built-in default: %s", DefaultPlatformAPI())
	}
}
