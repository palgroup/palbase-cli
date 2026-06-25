package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// oauthArt is a fully-bound iOS artifact carrying both providers — the shape
// the codegen / `apps config` path now merges from palauth's
// `/auth/oauth/providers`.
func oauthArt() ConfigArtifact {
	return ConfigArtifact{
		AppID:      "ios_todoapp",
		Identifier: "com.x.todo",
		EnvPreset:  "production",
		BaseURL:    "https://todoappm8p6zm.palbase.studio",
		APIKey:     "pb_todoappm8p6zm_c_prod",
		OAuth: &OAuthConfig{
			Apple: &OAuthApple{Enabled: true},
			Google: &OAuthGoogle{
				Enabled:     true,
				ClientID:    "123-abc.apps.googleusercontent.com",
				RedirectURI: "com.googleusercontent.apps.123-abc:/oauthredirect",
			},
		},
	}
}

// TestSingleEnvPlist_EmitsOAuthSubDict pins that the single-env plist
// (`palbase apps config --platform ios`) embeds the `oauth` block when the
// artifact carries providers — making the plist a SUPERSET of the legacy
// PalbaseGenerated.json's config role.
//
// Mutation-evident: revert writeIOSOAuthDict's emit (or its call in
// writeIOSConfigDict) and every oauth assertion below fails (the keys vanish).
func TestSingleEnvPlist_EmitsOAuthSubDict(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	require.NoError(t, writeIOSPlist(oauthArt(), out))

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	s := string(raw)

	// The oauth nested dict + both providers are present, snake_case wire.
	require.Contains(t, s, "<key>oauth</key>")
	require.Contains(t, s, "<key>apple</key>")
	require.Contains(t, s, "<key>google</key>")
	require.Contains(t, s, "<key>client_id</key>")
	require.Contains(t, s, "123-abc.apps.googleusercontent.com")
	require.Contains(t, s, "<key>redirect_uri</key>")
	require.Contains(t, s, "com.googleusercontent.apps.123-abc:/oauthredirect")
	// enabled rendered as a real plist boolean node, not a string.
	require.Contains(t, s, "<key>enabled</key>")
	require.Contains(t, s, "<true/>")

	// The oauth dict is nested INSIDE the env config dict (after the api_key
	// string, before the env dict closes) — not a sibling at plist root.
	apiKeyIdx := strings.Index(s, "<key>api_key</key>")
	oauthIdx := strings.Index(s, "<key>oauth</key>")
	require.Greater(t, oauthIdx, apiKeyIdx, "oauth must follow the flat config fields inside the env dict")
}

// TestPerEnvPlist_EmitsOAuthPerEnv pins that the build-config-conditioned
// plist carries each env's own oauth block (Debug=dev providers,
// Release=prod providers), keeping the per-env superset property.
//
// Mutation-evident: if oauth weren't emitted per env, the two distinct
// client_ids below would not BOTH appear.
func TestPerEnvPlist_EmitsOAuthPerEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")

	dev := oauthArt()
	dev.Identifier = "com.x.todo.dev"
	dev.EnvPreset = "development"
	dev.OAuth = &OAuthConfig{Google: &OAuthGoogle{
		Enabled: true, ClientID: "dev-client.apps.googleusercontent.com",
		RedirectURI: "com.googleusercontent.apps.dev-client:/oauthredirect",
	}}

	rel := oauthArt() // prod google client id from oauthArt()

	require.NoError(t, EmitIOSPlistPerEnv(dev, rel, out))
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	s := string(raw)

	require.Contains(t, s, "dev-client.apps.googleusercontent.com", "Debug env oauth client_id present")
	require.Contains(t, s, "123-abc.apps.googleusercontent.com", "Release env oauth client_id present")
	require.Equal(t, 2, strings.Count(s, "<key>oauth</key>"), "one oauth dict per env")
}

// TestPlist_OmitsOAuthWhenAbsent pins the JSON-path-parity degrade: an
// artifact with no providers emits NO `oauth` block (the SDK then falls back
// to the explicit-parameter signInWithGoogle overload, exactly as the JSON
// path does when it omits the block).
func TestPlist_OmitsOAuthWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	art := oauthArt()
	art.OAuth = nil
	require.NoError(t, writeIOSPlist(art, out))
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "<key>oauth</key>", "no oauth block when the env has no providers")
}
