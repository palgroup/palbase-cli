package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
		{"out-dir", "./.palbase"},
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
		"abc1", "main", dir, "",
		io.Discard,
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
		"abc1", "main", dir, "",
		io.Discard,
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

// TestPullSpec_ConfigShape locks the palbase-config.json shape: a SINGLE FLAT
// object (app_id/identifier/env_preset/base_url/api_key + optional oauth) for
// the ONE binding whose project_ref == ref — NOT a bundle-id-keyed map. Other
// bindings (different projects) are ignored; the CLI already picked the one
// active env.
func TestPullSpec_ConfigShape(t *testing.T) {
	bindings := []AppBinding{
		{ProjectRef: "prod", Identifier: "com.x.app", EnvPreset: "production"},
		{ProjectRef: "stgref", Identifier: "com.x.app.staging", EnvPreset: "staging"}, // different project, ignored
	}
	arts := map[string]apps.ConfigArtifact{
		"prod": {
			AppID: "app_1", Identifier: "com.x.app", EnvPreset: "production",
			BaseURL: "https://prodm.palbase.studio", APIKey: "pb_prodm_ckey",
			OAuth: &apps.OAuthConfig{
				Apple:  &apps.OAuthApple{Enabled: true},
				Google: &apps.OAuthGoogle{Enabled: true, ClientID: "123.apps.googleusercontent.com", RedirectURI: "com.googleusercontent.apps.123:/oauthredirect"},
			},
		},
	}

	dir := t.TempDir()
	var fetchedRef string
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
			fetchedRef = envRef
			art, ok := arts[envRef]
			require.Truef(t, ok, "config artifact fetched for the wrong env %q — must be the ref binding", envRef)
			return art, nil
		},
		"prod", "main", dir, "app_1",
		io.Discard,
	)
	require.NoError(t, err)
	require.Equal(t, "prod", fetchedRef, "config artifact must be fetched for the REF binding's env")

	raw, err := os.ReadFile(filepath.Join(dir, "palbase-config.json"))
	require.NoError(t, err)

	// FLAT object — not a map. Decoding into pullSpecConfigEntry directly must
	// succeed with populated fields (mutation guard: if buildPullSpecConfig
	// regressed to a bundle-id-keyed map, this flat decode sees no fields and the
	// identifier assertion goes RED).
	var got pullSpecConfigEntry
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "app_1", got.AppID)
	require.Equal(t, "production", got.EnvPreset)
	require.Equal(t, "https://prodm.palbase.studio", got.BaseURL)
	require.Equal(t, "pb_prodm_ckey", got.APIKey)
	require.NotNil(t, got.OAuth)
	require.NotNil(t, got.OAuth.Apple)
	require.True(t, got.OAuth.Apple.Enabled)
	require.NotNil(t, got.OAuth.Google)
	require.Equal(t, "123.apps.googleusercontent.com", got.OAuth.Google.ClientID)

	// The top-level JSON keys are the flat fields (base_url, api_key, …), NOT a
	// bundle id — locks that the file is not a bundle-id-keyed map. And it carries
	// NO identifier: the SDK sends X-Palbase-Bundle from Bundle.main.
	var rawKeys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &rawKeys))
	require.NotContains(t, rawKeys, "identifier", "config no longer carries a bundle identifier")
	require.Contains(t, rawKeys, "base_url")
	require.NotContains(t, rawKeys, "com.x.app", "config must be flat, not keyed by bundle id")
	_, hasOAuth := rawKeys["oauth"]
	require.True(t, hasOAuth, "provider env must include the oauth key")
}

// TestPullSpec_ConfigShape_OmitsOAuthWhenNoProviders: an env with no OAuth
// providers must omit the `oauth` key entirely (omitempty) so the SPM plugin's
// plist doesn't get an empty oauth dict.
func TestPullSpec_ConfigShape_OmitsOAuthWhenNoProviders(t *testing.T) {
	dir := t.TempDir()
	err := runPullSpec(
		context.Background(),
		stubTarget("https://stgs.palbase.studio", "pb_stgs_ckey"),
		stubFetch(`{"openapi":"3.1.0"}`, nil),
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{ProjectRef: "stg", Identifier: "com.x.app.staging", EnvPreset: "staging"}}, nil
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{
				AppID: "app_1", Identifier: "com.x.app.staging", EnvPreset: "staging",
				BaseURL: "https://stgs.palbase.studio", APIKey: "pb_stgs_ckey",
				// no oauth
			}, nil
		},
		"stg", "main", dir, "app_1",
		io.Discard,
	)
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(dir, "palbase-config.json"))
	require.NoError(t, err)
	var got pullSpecConfigEntry
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Nil(t, got.OAuth, "env with no providers must omit the oauth block")

	var rawKeys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &rawKeys))
	_, hasOAuth := rawKeys["oauth"]
	require.False(t, hasOAuth, "no-provider env must omit the oauth key from JSON")
}

// TestPullSpec_ErrorsWhenRefBindingHasNoIdentifier: the ref binding EXISTS but
// its identifier is empty → error (a config without a bundle id can't
// config-match). openapi.json is still written first.
func TestPullSpec_ErrorsWhenRefBindingHasNoIdentifier(t *testing.T) {
	dir := t.TempDir()
	err := runPullSpec(
		context.Background(),
		stubTarget("https://x.dev.palbase.studio", "pb_x_ckey"),
		stubFetch(`{}`, nil),
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{ProjectRef: "x", Identifier: ""}}, nil
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{}, errors.New("should not be reached: ref binding has no bundle id")
		},
		"x", "main", dir, "app_x",
		io.Discard,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no registered bundle id")

	// openapi.json is still written before the config step fails.
	_, statErr := os.Stat(filepath.Join(dir, "openapi.json"))
	require.NoError(t, statErr)

	// config was NOT written (the config step failed).
	_, cfgErr := os.Stat(filepath.Join(dir, "palbase-config.json"))
	require.True(t, os.IsNotExist(cfgErr), "no config must be written when the ref binding has no bundle id")
}

// TestPullSpec_ErrorsWhenRefNotBound: --app given but NONE of its bindings match
// the ref → error (the app isn't bound to this project). The config artifact is
// never fetched.
func TestPullSpec_ErrorsWhenRefNotBound(t *testing.T) {
	dir := t.TempDir()
	err := runPullSpec(
		context.Background(),
		stubTarget("https://x.dev.palbase.studio", "pb_x_ckey"),
		stubFetch(`{}`, nil),
		func(context.Context, string) ([]AppBinding, error) {
			// Bindings exist, but for OTHER projects — none is the ref "x".
			return []AppBinding{{ProjectRef: "other", Identifier: "com.other.app"}}, nil
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{}, errors.New("should not be reached: ref not bound")
		},
		"x", "main", dir, "app_x",
		io.Discard,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not bound to project ref")

	// openapi.json is still written before the config step fails.
	_, statErr := os.Stat(filepath.Join(dir, "openapi.json"))
	require.NoError(t, statErr)

	_, cfgErr := os.Stat(filepath.Join(dir, "palbase-config.json"))
	require.True(t, os.IsNotExist(cfgErr), "no config must be written when the ref isn't bound")
}
