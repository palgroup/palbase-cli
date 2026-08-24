package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Endpoints struct {
	Studio      string
	Auth        string
	PlatformAPI string
	// PublicHost is the suffix every Environment endpoint lives under. An
	// Environment with ref "mu0028" is reachable at "<ref>.<PublicHost>"
	// (e.g. "https://mu0028.dev.palbase.studio"). The CLI hands this to
	// the deploy verbs so the CLI can build a ServerClient
	// pointed at the same hosts the deployed pod hits — no URL parsing
	// or string surgery on Studio's URL.
	PublicHost string
}

// ONE CLOUD, ONE ADDRESS SET.
//
// There used to be two: `prod` at `palbase.studio` and `dev` at
// `v2.palbase.studio`. Only one of them was ever deployed — measured 24.08.2026,
// `pbc-prod-fleet-rg` does not exist in Azure and never did, and `dig
// api.palbase.studio` answers NXDOMAIN. `prod` was also the DEFAULT, so a fresh
// install resolved every command against a host that does not exist and failed
// with "no such host", which the credential layer then reported as "no
// credential for this project". Two errors, neither naming the cause.
//
// So there is one entry. The mode NAMES are still accepted, because
// `~/.palbase/config.json` on real machines carries them and a person should not
// have to edit a file to keep working — but they resolve to the same place,
// which is the truth about this cloud.
//
// THE CUTOVER IS THIS BLOCK. When `palbase.studio` resolves (see
// v2-cloud/bootstrap/dns/cutover.sh), the four strings below become
// `https://palbase.studio`, `https://api.palbase.studio`, and `palbase.studio`.
// Nothing else in this CLI knows a hostname.
//
// One public host per surface, and that is the design rather than a shortcut:
// the control plane terminates everything a client needs — the management API,
// the stack's own auth module, OIDC discovery — behind a single door with an
// explicit allowlist. A second origin would be a second thing to keep in sync,
// and the day they drifted a client would authenticate against one issuer and
// call another.
//
// PublicHost is the TENANT suffix, not the gateway. A tenant with ref
// "j06bwtuum" answers at j06bwtuum.<PublicHost> while the control plane answers
// at api.<PublicHost>. Collapsing the two would send every management call to a
// tenant that has never heard of it.
//
// Studio is the PANEL, and it is its own origin: the gateway serves the API and
// the app on separate virtual hosts, because one is an API whose value is a
// narrow allowlist and the other is an application that needs every path under
// its root. `palbase open` and the browser sign-in both send a person to the
// app, never to the API.
var theCloud = Endpoints{
	Studio:      "https://app.v2.palbase.studio",
	Auth:        "https://api.v2.palbase.studio",
	PlatformAPI: "https://api.v2.palbase.studio",
	PublicHost:  "v2.palbase.studio",
}

// File is ~/.palbase/config.json.
//
// It used to carry a `mode`, and nothing carries anything now: there is one
// cloud, so the file has no choice left to record. Kept as a type because the
// file itself still exists on real machines — a stale `{"mode":"dev"}` decodes
// to an empty struct and is ignored, which is the whole point of not failing on
// unknown fields here.
type File struct{}

// DefaultPlatformAPI is the control plane this binary is built for.
//
// Exported so a caller can tell "the configured cloud" from "somewhere else":
// the only way to reach somewhere else is an explicit PALBASE_PLATFORM_URL, and
// a person who set one should see it where it matters.
func DefaultPlatformAPI() string { return theCloud.PlatformAPI }

func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(home, ".palbase", "config.json"), nil
}

func Load() (File, error) {
	path, err := Path()
	if err != nil {
		return File{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("read config: %w", err)
	}

	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("parse config: %w", err)
	}
	return f, nil
}

func Save(f File) error {
	path, err := Path()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// Resolved is where this CLI acts. One cloud, one address set — see theCloud.
type Resolved struct {
	Endpoints Endpoints
}

// Resolve answers the addresses every command uses.
//
// IT TAKES NOTHING, and that is the change. It used to take a mode: `prod` or
// `dev`, selecting between two address sets. Only one was ever deployed —
// measured 24.08.2026, Azure has no `pbc-prod-fleet-rg` and
// `dig api.palbase.studio` answers NXDOMAIN — and `prod` was the DEFAULT, so a
// fresh install resolved every command against a host that does not exist. The
// failure surfaced as "no such host", which the credential layer then reported
// as "no credential for this project": two errors, neither naming the cause.
//
// The env overrides stay. They are the escape hatch for someone running this
// against a stack of their own, and unlike a mode they name an ADDRESS rather
// than selecting from a list this binary has to keep true.
func Resolve() (Resolved, error) {
	ep := theCloud
	if v := os.Getenv("PALBASE_STUDIO_URL"); v != "" {
		ep.Studio = v
	}
	if v := os.Getenv("PALBASE_AUTH_URL"); v != "" {
		ep.Auth = v
	}
	if v := os.Getenv("PALBASE_PLATFORM_URL"); v != "" {
		ep.PlatformAPI = v
	}
	return Resolved{Endpoints: ep}, nil
}
