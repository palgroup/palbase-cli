package configcode

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

// authRow builds an authProviderRow for table cases close to the wire
// shape returned by auth.providers.list.
func authRow(id, label string, enabled, toggle, runtime bool) authProviderRow {
	return authProviderRow{
		ID:               id,
		Label:            label,
		Enabled:          enabled,
		ToggleAvailable:  toggle,
		RuntimeAvailable: runtime,
	}
}

// TestSerializeAuth_Golden asserts exact TOML bytes for a representative
// provider set — the determinism gate. Any reordering or formatting drift
// breaks this test. Input is intentionally NOT alphabetical; output must
// sort by provider id (BurntSushi sorts map keys).
func TestSerializeAuth_Golden(t *testing.T) {
	providers := []authProviderRow{
		authRow("google", "Google", true, true, true),
		authRow("email", "Email", true, false, true),
		authRow("github", "GitHub", false, true, false),
	}

	got, err := serializeAuth(providers)
	require.NoError(t, err)

	const want = `# config/auth.toml — auth provider configuration (config-as-code, Faz 1).
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

[providers]
  [providers.email]
    enabled = true
  [providers.github]
    enabled = false
  [providers.google]
    enabled = true
`
	require.Equal(t, want, string(got))
}

// TestSerializeAuth_Deterministic runs the same input twice and asserts
// byte-identical output (independent of Go map iteration order).
func TestSerializeAuth_Deterministic(t *testing.T) {
	providers := []authProviderRow{
		authRow("microsoft", "Microsoft", true, true, false),
		authRow("apple", "Apple", false, true, true),
		authRow("github", "GitHub", true, true, true),
	}
	a, err := serializeAuth(providers)
	require.NoError(t, err)
	b, err := serializeAuth(providers)
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
	// Providers appear sorted by id regardless of input order.
	require.Less(t, strings.Index(string(a), "apple"), strings.Index(string(a), "github"))
	require.Less(t, strings.Index(string(a), "github"), strings.Index(string(a), "microsoft"))
}

// TestSerializeAuth_Empty asserts a project with no providers produces a
// valid header-only document (no bare [providers] table).
func TestSerializeAuth_Empty(t *testing.T) {
	got, err := serializeAuth(nil)
	require.NoError(t, err)
	require.Contains(t, string(got), "READ-ONLY MIRROR")
	require.NotContains(t, string(got), "[providers]")
}

// TestSerializeAuth_RoundTrip decodes the emitted TOML back and checks the
// enabled state survives the trip — the format is meant to be edited by
// humans and (later, Faz 2) pushed.
func TestSerializeAuth_RoundTrip(t *testing.T) {
	providers := []authProviderRow{
		authRow("google", "Google", true, true, true),
		authRow("github", "GitHub", false, true, true),
		authRow("email", "Email", true, false, true),
	}
	got, err := serializeAuth(providers)
	require.NoError(t, err)

	var doc authDoc
	require.NoError(t, toml.Unmarshal(got, &doc))

	require.Len(t, doc.Providers, 3)
	require.True(t, doc.Providers["google"].Enabled)
	require.False(t, doc.Providers["github"].Enabled)
	require.True(t, doc.Providers["email"].Enabled)
}

// TestSerializeAuth_NoSecretLeak is the defensive guard required by the
// secret-ref rule: the auth serializer must NEVER emit a raw credential.
// Today the admin API exposes no secrets, so no provider entry should
// carry a client_id or client_secret field at all. We assert the
// structural invariant by parsing the emitted TOML back (rather than
// substring-matching, which would false-positive on the documentation
// header that mentions the Faz 2 @secret/ shape). The @secret/ reference
// convention stays wired via SecretRef for Faz 2.
func TestSerializeAuth_NoSecretLeak(t *testing.T) {
	providers := []authProviderRow{
		authRow("google", "Google", true, true, true),
		authRow("github", "GitHub", false, true, true),
	}
	got, err := serializeAuth(providers)
	require.NoError(t, err)

	var doc authDoc
	require.NoError(t, toml.Unmarshal(got, &doc))
	for id, p := range doc.Providers {
		require.Empty(t, p.ClientID, "provider %s: client_id not exposed by admin API today", id)
		require.Empty(t, p.ClientSecret, "provider %s: a secret VALUE must never be emitted", id)
	}
	// The @secret/ reference helper remains available + correct for Faz 2.
	require.Equal(t, "@secret/GOOGLE_OAUTH_SECRET", SecretRef("GOOGLE_OAUTH_SECRET"))
}

// TestSerializeAuth_MapsOnlyEnabled documents that the serializer
// intentionally drops server-reflection fields (label, toggleAvailable,
// runtimeAvailable) and keeps only the enabled config knob.
func TestSerializeAuth_MapsOnlyEnabled(t *testing.T) {
	tests := []struct {
		name        string
		row         authProviderRow
		wantEnabled bool
	}{
		{"enabled provider", authRow("google", "Google", true, true, true), true},
		{"disabled provider", authRow("github", "GitHub", false, true, false), false},
		{"toggle-locked email", authRow("email", "Email", true, false, true), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := serializeAuth([]authProviderRow{tc.row})
			require.NoError(t, err)
			var doc authDoc
			require.NoError(t, toml.Unmarshal(got, &doc))
			require.Len(t, doc.Providers, 1)
			require.Equal(t, tc.wantEnabled, doc.Providers[tc.row.ID].Enabled)
			// Reflection fields never appear in the serialized output.
			out := string(got)
			require.NotContains(t, out, "label")
			require.NotContains(t, out, "toggle_available")
			require.NotContains(t, out, "runtime_available")
		})
	}
}
