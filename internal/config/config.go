package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Mode string

const (
	ModeProd Mode = "prod"
	ModeDev  Mode = "dev"
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

// THE CLI SERVES THE V2 CLOUD. Both modes name a v2 control plane; there is no
// v1 target left in this binary.
//
// One public host per environment, and that is the design rather than a
// shortcut: the control plane terminates every surface a client needs — the
// management API, the stack's own auth module, and OIDC discovery — behind a
// single door with an explicit allowlist. A second origin would be a second
// thing to keep in sync, and the day they drifted a client would authenticate
// against one issuer and call another.
//
// PublicHost is the TENANT suffix, not the gateway. A tenant with ref
// "j06bwtuum" answers at j06bwtuum.v2.palbase.studio while the control plane
// answers at api.v2.palbase.studio. Collapsing the two would send every
// management call to a tenant that has never heard of it.
var endpointsByMode = map[Mode]Endpoints{
	ModeProd: {
		Studio:      "https://api.palbase.studio",
		Auth:        "https://api.palbase.studio",
		PlatformAPI: "https://api.palbase.studio",
		PublicHost:  "palbase.studio",
	},
	ModeDev: {
		Studio:      "https://api.v2.palbase.studio",
		Auth:        "https://api.v2.palbase.studio",
		PlatformAPI: "https://api.v2.palbase.studio",
		PublicHost:  "v2.palbase.studio",
	},
}

func (m Mode) Valid() bool {
	_, ok := endpointsByMode[m]
	return ok
}

func (m Mode) Endpoints() Endpoints {
	if e, ok := endpointsByMode[m]; ok {
		return e
	}
	return endpointsByMode[ModeProd]
}

type File struct {
	Mode Mode `json:"mode,omitempty"`
}

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

type Resolved struct {
	Mode      Mode
	Source    string
	Endpoints Endpoints
}

// Resolve applies precedence: flagMode > PALBASE_MODE > config file > prod default.
// Endpoints are derived from mode; env overrides (PALBASE_STUDIO_URL, PALBASE_AUTH_URL,
// PALBASE_PLATFORM_URL) take precedence over mode-derived endpoints for escape hatches.
func Resolve(flagMode string) (Resolved, error) {
	var mode Mode
	var source string

	switch {
	case flagMode != "":
		mode = Mode(flagMode)
		source = "flag"
	case os.Getenv("PALBASE_MODE") != "":
		mode = Mode(os.Getenv("PALBASE_MODE"))
		source = "env"
	default:
		f, err := Load()
		if err != nil {
			return Resolved{}, err
		}
		if f.Mode != "" {
			mode = f.Mode
			source = "config"
		} else {
			mode = ModeProd
			source = "default"
		}
	}

	if !mode.Valid() {
		return Resolved{}, fmt.Errorf("invalid mode %q — must be 'prod' or 'dev'", mode)
	}

	ep := mode.Endpoints()
	if v := os.Getenv("PALBASE_STUDIO_URL"); v != "" {
		ep.Studio = v
	}
	if v := os.Getenv("PALBASE_AUTH_URL"); v != "" {
		ep.Auth = v
	}
	if v := os.Getenv("PALBASE_PLATFORM_URL"); v != "" {
		ep.PlatformAPI = v
	}

	return Resolved{Mode: mode, Source: source, Endpoints: ep}, nil
}
