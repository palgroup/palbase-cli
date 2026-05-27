package configcode

import (
	"bytes"
	"context"
	"fmt"

	"github.com/BurntSushi/toml"
	"github.com/palgroup/palbase-cli/internal/studio"
)

func init() { Register(authSerializer{}) }

// authSerializer pulls the project's auth provider config and writes
// config/auth.toml. It mirrors flags.go (the reference impl) in shape:
// a pure serializeAuth core + a thin Pull that fetches via tRPC.
type authSerializer struct{}

func (authSerializer) Name() string     { return "auth" }
func (authSerializer) Filename() string { return "auth.toml" }

// authProviderListResponse mirrors the `auth.providers.list` tRPC
// response. The procedure (platform/studio/src/server/trpc/routers/auth.ts
// :581-606) returns a WRAPPER object, not a bare array:
//
//	{ configureAvailable: bool, providers: [{ id, label, enabled,
//	  toggleAvailable, runtimeAvailable }] }
//
// configureAvailable is a tenant *capability* flag (whether the admin UI
// can configure providers at all), NOT per-project config, so it is
// decoded but intentionally NOT serialized to TOML.
type authProviderListResponse struct {
	Providers []authProviderRow `json:"providers"`
}

// authProviderRow mirrors one entry of the `auth.providers.list` response.
//
// IMPORTANT — what the admin API actually exposes:
//
// The palauth admin endpoint GET /admin/providers
// (modules/auth/internal/server/admin_phase5_handlers.go:81-99) returns
// ONLY: id, label, enabled, toggle_available, runtime_available. It does
// NOT return client_id or client_secret — those live in deploy-time social
// provider config (modules/auth/internal/social/provider.go:60-61) and are
// never surfaced by the admin list. Studio's tRPC mapper
// (routers/auth.ts:595-601) likewise drops anything beyond these fields at
// the boundary, so even if palauth grew a client_id field the tRPC layer
// would have to be extended too.
//
// Consequence for config-as-code: the only real config knob we can pull is
// `enabled`. There is no secret to reference (so SecretRef is unused here
// today — it stays wired in configcode.go for when Faz 2 adds a richer
// admin GET). label/toggleAvailable/runtimeAvailable are server-side
// reflection (display string + capability flags), not user-controlled
// config, so they are NOT serialized — emitting them would create a
// "what happens if I edit this?" ambiguity once push (Faz 2) lands.
type authProviderRow struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	Enabled          bool   `json:"enabled"`
	ToggleAvailable  bool   `json:"toggleAvailable"`
	RuntimeAvailable bool   `json:"runtimeAvailable"`
}

// authDoc is the root of config/auth.toml. map[string]providerEntry is
// safe for determinism: BurntSushi/toml sorts map keys when encoding, so
// providers appear alphabetically by id and repeated runs produce
// byte-identical output (same strategy as flagsDoc).
//
// TOML mapping (documented for round-trip + future authors):
//
//	[providers.<id>]
//	enabled = true|false           ← provider.enabled
//
// FUTURE (Faz 2): when the admin API exposes provider config, this entry
// grows to:
//
//	[providers.google]
//	enabled = true
//	client_id = "123.apps.googleusercontent.com"   ← public, plain string
//	client_secret = "@secret/GOOGLE_OAUTH_SECRET"  ← SecretRef, NEVER value
//
// The struct shape leaves room (ClientID/ClientSecret with omitempty)
// without breaking this v1 layout.
type authDoc struct {
	Providers map[string]providerEntry `toml:"providers"`
}

type providerEntry struct {
	Enabled bool `toml:"enabled"`
	// ClientID / ClientSecret are Faz 2 placeholders — the admin API does
	// not expose them today, so they are always omitted. ClientSecret, when
	// populated, MUST be a SecretRef ("@secret/<NAME>"), never a raw value.
	ClientID     string `toml:"client_id,omitempty"`
	ClientSecret string `toml:"client_secret,omitempty"`
}

const authHeader = `# config/auth.toml — auth provider configuration (config-as-code, Faz 1).
#
# READ-ONLY MIRROR of server state. ` + "`palbase pull`" + ` overwrites
# this file; this module has no push contract yet. Editing here does not
# change the server.
#
# Each [providers.<id>] mirrors one auth provider. The admin providers API
# (palauth GET /admin/providers, via tRPC auth.providers.list) exposes only
# the on/off state — so today only ` + "`enabled`" + ` is pulled.
#
# client_id and client_secret are NOT pulled: they live in deploy-time
# config (env / secret) and the admin API does not surface them. When a
# richer admin GET lands (Faz 2), this serializer will emit
# ` + "`client_id = \"...\"`" + ` (public, plain) and
# ` + "`client_secret = \"@secret/<NAME>\"`" + ` (a secret REFERENCE, never the
# real value).

`

// Pull fetches auth providers via auth.providers.list and serializes them
// to TOML. An empty project (no providers) still produces a valid
// header-only document so the file exists for diffing.
//
// The tRPC path is the key the root router mounts the auth router under
// (platform/studio/src/server/trpc/router.ts:17 — `auth: authRouter`),
// followed by the nested `providers.list` procedure. tRPC paths are the JS
// object keys, so this must be `auth.providers.list` exactly or the pull
// 404s. (It happens to match the module folder name, unlike flags whose
// path is the camelCase `userFlags.system.list`.)
func (authSerializer) Pull(ctx context.Context, ref string, sc *studio.Client) ([]byte, error) {
	var resp authProviderListResponse
	if err := sc.Query(ctx, "auth.providers.list", map[string]any{"ref": ref}, &resp); err != nil {
		return nil, fmt.Errorf("auth.providers.list: %w", err)
	}
	return serializeAuth(resp.Providers)
}

// serializeAuth is the pure, testable core: provider rows → deterministic
// TOML. Split out from Pull so unit tests cover the mapping without a live
// tRPC client (mirrors serializeFlags).
func serializeAuth(providers []authProviderRow) ([]byte, error) {
	doc := authDoc{Providers: map[string]providerEntry{}}
	for _, p := range providers {
		doc.Providers[p.ID] = providerEntry{Enabled: p.Enabled}
	}

	var buf bytes.Buffer
	buf.WriteString(authHeader)
	// Header-only document when there are no providers: skip the encoder so
	// we don't emit a bare `[providers]` table for an empty map.
	if len(doc.Providers) > 0 {
		if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
			return nil, fmt.Errorf("encode toml: %w", err)
		}
	}
	return buf.Bytes(), nil
}
