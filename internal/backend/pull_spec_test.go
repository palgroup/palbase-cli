package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/apps"
)

// stubTarget / stubFetch / stubList / stubArt are the injected seams that keep
// runPullSpec entirely off the network — no httptest, no tRPC, no :4003 probe.

func stubTarget(url, key string) specTargetLookup {
	return func(_ context.Context, _, _ string) (backendTarget, error) {
		return backendTarget{URL: url, APIKey: key}, nil
	}
}

func stubFetch(body string, capture *string) remoteSpecFetch {
	return func(_ context.Context, specURL, _ string, _ io.Writer) ([]byte, error) {
		if capture != nil {
			*capture = specURL
		}
		return []byte(body), nil
	}
}

// TestPullSpec_FlagDefaults pins the command surface: the flags exist with the
// documented defaults, so a stale wiring (renamed flag, dropped default) is RED.
func TestPullSpec_FlagDefaults(t *testing.T) {
	cmd := newSpecCmd(Resolvers{})
	require.Equal(t, "spec", cmd.Name())
	for _, tc := range []struct{ name, def string }{
		{"ref", ""},
		{"branch", ""},
		{"out-dir", "./Palbase"},
	} {
		f := cmd.Flags().Lookup(tc.name)
		require.NotNilf(t, f, "missing --%s flag", tc.name)
		require.Equalf(t, tc.def, f.DefValue, "--%s default", tc.name)
	}
	// spec refreshes the API contract ONLY — config (and thus app selection)
	// is `ios link`'s job. These flags must NOT exist on spec.
	require.Nil(t, cmd.Flags().Lookup("app"), "spec must not take --app (config is ios link's job)")
	require.Nil(t, cmd.Flags().Lookup("group"), "spec must not take --group")
	require.Nil(t, cmd.Flags().Lookup("no-config"), "spec must not take --no-config")
}

// TestRunPullSpec_EmptyAppNeverWritesConfig locks the role split at the core
// newSpecCmd relies on: runPullSpec with an EMPTY appID never touches the
// binding-list / config-artifact seams and never writes palbase-config.json —
// even though the project has an ios app registered. spec passes "" (config is
// ios link's job); this pins that "" means openapi-only. The seams t.Error if
// reached.
func TestRunPullSpec_EmptyAppNeverWritesConfig(t *testing.T) {
	dir := t.TempDir()
	err := runPullSpec(
		context.Background(),
		stubTarget("https://abc1m.dev.palbase.studio", "pb_abc1m_ckey"),
		stubFetch(`{"openapi":"3.1.0","paths":{}}`, nil),
		func(context.Context, string) ([]AppBinding, error) {
			t.Error("spec must not list bindings with empty appID")
			return nil, errors.New("unreachable")
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			t.Error("spec must not fetch config with empty appID")
			return apps.ConfigArtifact{}, errors.New("unreachable")
		},
		"abc1", "main", dir, "", io.Discard,
	)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "openapi.json"))
	require.NoError(t, err, "spec must write openapi.json")
	_, statErr := os.Stat(filepath.Join(dir, "palbase-config.json"))
	require.True(t, os.IsNotExist(statErr), "spec must NEVER write palbase-config.json — that is ios link's job")
}

// TestPullSpec_WritesSpecOnly: no --app → ONLY openapi.json is written, fetched
// from the remote target URL (REMOTE only — the stub captures the URL so we lock
// that it never probes localhost:4003).
func TestPullSpec_WritesSpecOnly(t *testing.T) {
	dir := t.TempDir()
	const spec = `{"openapi":"3.1.0","paths":{}}`
	var fetchedURL string
	err := runPullSpec(
		context.Background(),
		stubTarget("https://abc1m.dev.palbase.studio", "pb_abc1m_ckey"),
		stubFetch(spec, &fetchedURL),
		func(context.Context, string) ([]AppBinding, error) { return nil, errors.New("must not list without --app") },
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{}, errors.New("must not fetch config without --app")
		},
		"abc1", "main", dir, "", io.Discard,
	)
	require.NoError(t, err)

	require.Equal(t, "https://abc1m.dev.palbase.studio/openapi.json", fetchedURL,
		"pull-spec must fetch the REMOTE target spec, never a local :4003 probe")

	got, err := os.ReadFile(filepath.Join(dir, "openapi.json"))
	require.NoError(t, err)
	require.Equal(t, spec, string(got))

	_, statErr := os.Stat(filepath.Join(dir, "palbase-config.json"))
	require.True(t, os.IsNotExist(statErr), "no --app → palbase-config.json must NOT be written")
}

// TestPullSpec_ConfigShape is the table-driven lock on the palbase-config.json
// shape (bundle-id-keyed entry: app_id/identifier/env_preset/base_url/api_key +
// optional oauth) produced from the binding list + per-env config artifacts.
func TestPullSpec_ConfigShape(t *testing.T) {
	bindings := []AppBinding{
		{ProjectRef: "prodref", Identifier: "com.x.app", EnvPreset: "production"},
		{ProjectRef: "stgref", Identifier: "com.x.app.staging", EnvPreset: "staging"},
		{ProjectRef: "noidref", Identifier: "", EnvPreset: "development"}, // skipped: no bundle id
	}
	arts := map[string]apps.ConfigArtifact{
		"prodref": {
			AppID: "app_1", Identifier: "com.x.app", EnvPreset: "production",
			BaseURL: "https://prodm.palbase.studio", APIKey: "pb_prodm_ckey",
			OAuth: &apps.OAuthConfig{
				Apple:  &apps.OAuthApple{Enabled: true},
				Google: &apps.OAuthGoogle{Enabled: true, ClientID: "123.apps.googleusercontent.com", RedirectURI: "com.googleusercontent.apps.123:/oauthredirect"},
			},
		},
		"stgref": {
			AppID: "app_1", Identifier: "com.x.app.staging", EnvPreset: "staging",
			BaseURL: "https://stgs.palbase.studio", APIKey: "pb_stgs_ckey",
			// no oauth on this env → entry.oauth omitted
		},
	}

	dir := t.TempDir()
	err := runPullSpec(
		context.Background(),
		stubTarget("https://prodm.palbase.studio", "pb_prodm_ckey"),
		stubFetch(`{"openapi":"3.1.0"}`, nil),
		func(_ context.Context, appID string) ([]AppBinding, error) {
			require.Equal(t, "app_1", appID)
			return bindings, nil
		},
		func(_ context.Context, appID, envRef, _ string) (apps.ConfigArtifact, error) {
			require.Equal(t, "app_1", appID)
			art, ok := arts[envRef]
			require.Truef(t, ok, "unexpected envRef %q", envRef)
			return art, nil
		},
		"prod", "main", dir, "app_1", io.Discard,
	)
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(dir, "palbase-config.json"))
	require.NoError(t, err)
	var got map[string]pullSpecConfigEntry
	require.NoError(t, json.Unmarshal(raw, &got))

	// Keyed by bundle id; the no-bundle-id binding is dropped.
	require.Len(t, got, 2)
	require.NotContains(t, got, "")

	prod := got["com.x.app"]
	require.Equal(t, "app_1", prod.AppID)
	require.Equal(t, "com.x.app", prod.Identifier)
	require.Equal(t, "production", prod.EnvPreset)
	require.Equal(t, "https://prodm.palbase.studio", prod.BaseURL)
	require.Equal(t, "pb_prodm_ckey", prod.APIKey)
	require.NotNil(t, prod.OAuth)
	require.NotNil(t, prod.OAuth.Apple)
	require.True(t, prod.OAuth.Apple.Enabled)
	require.NotNil(t, prod.OAuth.Google)
	require.Equal(t, "123.apps.googleusercontent.com", prod.OAuth.Google.ClientID)

	stg := got["com.x.app.staging"]
	require.Equal(t, "staging", stg.EnvPreset)
	require.Nil(t, stg.OAuth, "env with no providers must omit the oauth block")

	// omitempty: the staging entry's serialized form carries NO oauth key, so the
	// SPM plugin's plist doesn't get an empty oauth dict.
	var rawByKey map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &rawByKey))
	_, hasOAuth := rawByKey["com.x.app.staging"]["oauth"]
	require.False(t, hasOAuth, "no-provider env must omit the oauth key from JSON")
	_, prodHasOAuth := rawByKey["com.x.app"]["oauth"]
	require.True(t, prodHasOAuth, "provider env must include the oauth key")
}

// TestPullSpec_NoBundleIDErrors: --app given but NO env has a registered bundle
// id → error (nothing to write), matching the plist path's behavior.
func TestPullSpec_NoBundleIDErrors(t *testing.T) {
	dir := t.TempDir()
	err := runPullSpec(
		context.Background(),
		stubTarget("https://x.dev.palbase.studio", "pb_x_ckey"),
		stubFetch(`{}`, nil),
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{ProjectRef: "r1", Identifier: ""}}, nil
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{}, errors.New("should not be reached: binding has no bundle id")
		},
		"x", "main", dir, "app_x", io.Discard,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no environment with a registered bundle id")

	// openapi.json is still written before the config step fails.
	_, statErr := os.Stat(filepath.Join(dir, "openapi.json"))
	require.NoError(t, statErr)
}

// TestPullSpec_DuplicateBundleIDErrors: two envs sharing one bundle id is a
// config error (the plist path refuses the same collision).
func TestPullSpec_DuplicateBundleIDErrors(t *testing.T) {
	dir := t.TempDir()
	err := runPullSpec(
		context.Background(),
		stubTarget("https://x.dev.palbase.studio", "pb_x_ckey"),
		stubFetch(`{}`, nil),
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{
				{ProjectRef: "r1", Identifier: "com.dup"},
				{ProjectRef: "r2", Identifier: "com.dup"},
			}, nil
		},
		func(_ context.Context, _, envRef, _ string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{AppID: "app_d", Identifier: "com.dup", ProjectRef: envRef}, nil
		},
		"x", "main", dir, "app_d", io.Discard,
	)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "share the bundle id")
}
