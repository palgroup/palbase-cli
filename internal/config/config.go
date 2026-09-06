package config

import (
	"os"
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

// ONE CLOUD, ONE ADDRESS SET — `palbase.studio`.
//
// There used to be two address sets behind a `--mode`, and only one of them was
// ever deployed. There is one product and it has one address; the panel lives at
// the bare domain because `app.` was a prefix a person had to learn for no
// reason, and `v2.` named a migration that is over.
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
	Studio:      "https://palbase.studio",
	Auth:        "https://api.palbase.studio",
	PlatformAPI: "https://api.palbase.studio",
	PublicHost:  "palbase.studio",
}

// DefaultPlatformAPI is the control plane this binary is built for.
//
// Exported so a caller can tell "the configured cloud" from "somewhere else":
// the only way to reach somewhere else is an explicit PALBASE_PLATFORM_URL, and
// a person who set one should see it where it matters.
func DefaultPlatformAPI() string { return theCloud.PlatformAPI }

// DefaultAuth is the address the browser sign-in itself talks to.
//
// It is a SEPARATE question from DefaultPlatformAPI even while both resolve to
// the same host: the sign-in speaks to Auth, every other verb speaks to
// PlatformAPI, and a caller that compares "the cloud I signed in to" against
// the platform address goes quietly wrong the day the two split.
func DefaultAuth() string { return theCloud.Auth }

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
