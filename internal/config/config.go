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
	// PublicHost is the suffix every project endpoint lives under. A
	// project with ref "mu0028" is reachable at "<ref>.<PublicHost>"
	// (e.g. "https://mu0028.dev.palbase.studio"). The CLI hands this to
	// `palbase backend dev` so dev-server.js can build a ServerClient
	// pointed at the same hosts the deployed pod hits — no URL parsing
	// or string surgery on Studio's URL.
	PublicHost string
}

// All OIDC endpoints live on the Studio apex host. The platform fronts
// palauth with ingress-nginx + Kong rules that forward /oauth and
// /.well-known to palauth's cluster-internal service, so the CLI only
// needs one public host — the same one where users log in. Palauth's
// OIDC provider is configured with ExternalIssuer matching this origin
// so JWT `iss` claims and the discovery document agree.
var endpointsByMode = map[Mode]Endpoints{
	ModeProd: {
		Studio:      "https://palbase.studio",
		Auth:        "https://palbase.studio",
		PlatformAPI: "https://api.palbase.studio",
		PublicHost:  "palbase.studio",
	},
	ModeDev: {
		// Real Studio dev hostname is `app.dev.palbase.studio`; the
		// `dev.palbase.studio` apex doesn't resolve. palauth (OAuth +
		// /.well-known) is fronted by the same host so Auth points
		// there too. Project endpoints are at <ref>.dev.palbase.studio.
		//
		// PlatformAPI is the Management API (`/api/v1/*`) origin —
		// `api.dev.palbase.studio` per the Spec-1 contract, fronted by
		// the Kong `api.palbase.studio` route (plan S4). Until that route
		// lands in dev, override with PALBASE_PLATFORM_URL.
		Studio:      "https://app.dev.palbase.studio",
		Auth:        "https://app.dev.palbase.studio",
		PlatformAPI: "https://api.dev.palbase.studio",
		PublicHost:  "dev.palbase.studio",
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
