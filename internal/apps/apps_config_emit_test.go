package apps

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmitConfig_WritesWebJSON(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "palbase-config.json")
	art := ConfigArtifact{AppID: "app_1", Identifier: "https://app.example.com", EnvPreset: "production", BaseURL: "https://e1m.palbase.studio", APIKey: "pb_e1_x"}
	require.NoError(t, emitConfig(art, "web", out))
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, "app_1", got["app_id"])
	require.Equal(t, "https://app.example.com", got["identifier"])
	require.Equal(t, "production", got["env_preset"])
	require.Equal(t, "https://e1m.palbase.studio", got["base_url"])
	require.Equal(t, "pb_e1_x", got["api_key"])
}

// parseNestedPlist parses the bundle-id-keyed Palbase-Info.plist (top-level
// <dict> whose keys are bundle ids, each value a nested config <dict>) into
// bundleID -> {key: value}. Walks the XML token stream so the nesting is
// honoured exactly — schema-agnostic, so a missing bundle-id key or a dropped
// field surfaces as an absent map entry (mutation-evident: the OLD
// build-config-keyed form keyed by Debug/Release, NOT bundle ids, so an
// assertion for a bundle-id key would FAIL against it). It mirrors
// backend.parsePerEnvPlist so the apps-package assertion pins the SAME nested
// shape the codegen path is tested against.
func parseNestedPlist(t *testing.T, b []byte) map[string]map[string]string {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(b))
	out := map[string]map[string]string{}
	depth := 0
	var pendingEnv, pendingKey string
	var curEnv map[string]string
	readChars := func() string {
		var s string
		for {
			tok, err := dec.Token()
			require.NoError(t, err)
			if cd, ok := tok.(xml.CharData); ok {
				s += string(cd)
				continue
			}
			return s
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "dict":
				depth++
				if depth == 2 {
					curEnv = map[string]string{}
					out[pendingEnv] = curEnv
				}
			case "key":
				val := readChars()
				if depth == 1 {
					pendingEnv = val
				} else if depth == 2 {
					pendingKey = val
				}
			case "string":
				val := readChars()
				if depth == 2 && curEnv != nil {
					curEnv[pendingKey] = val
				}
			}
		case xml.EndElement:
			if el.Name.Local == "dict" {
				depth--
			}
		}
	}
	return out
}

// TestEmitConfig_WritesIOSPlist pins that `apps config --platform ios` emits the
// bundle-id-keyed NESTED plist the iOS SDK (PalbaseAppConfig.load) decodes — a
// top-level dict whose single key is the env's bundle id (its identifier),
// holding a config sub-dict carrying the five config-match fields. The SDK
// selects the env dict by matching Bundle.main.bundleIdentifier against that
// key. Mutation-evident: the OLD build-config-keyed form keyed by Debug/Release,
// so an assertion for the bundle-id key "com.example.todo" would FAIL against it.
func TestEmitConfig_WritesIOSPlist(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	art := ConfigArtifact{AppID: "app_1", Identifier: "com.example.todo", EnvPreset: "production", BaseURL: "https://e1m.palbase.studio", APIKey: "pb_e1_x"}
	require.NoError(t, emitConfig(art, "ios", out))
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	envs := parseNestedPlist(t, b)

	// Exactly one top-level key, and it is the env's bundle id — NOT Debug/Release.
	require.Len(t, envs, 1, "single-env apps-config plist must carry exactly one bundle-id key")
	require.Contains(t, envs, "com.example.todo", "plist must be keyed by the env's bundle id (SDK decodes envs[Bundle.main.bundleIdentifier])")
	require.NotContains(t, envs, "Debug", "the plist must NOT use the retired build-config key")
	require.NotContains(t, envs, "Release", "the plist must NOT use the retired build-config key")

	got := envs["com.example.todo"]
	require.Equal(t, "app_1", got["app_id"])
	require.Equal(t, "com.example.todo", got["identifier"], "the dict's identifier must equal its bundle-id key")
	require.Equal(t, "production", got["env_preset"])
	require.Equal(t, "https://e1m.palbase.studio", got["base_url"])
	require.Equal(t, "pb_e1_x", got["api_key"])
}

// TestEmitConfig_IOSPlistMatchesByBundleEmitter proves the two emit paths produce
// byte-IDENTICAL plist structure: `apps config` (single env, via writeIOSPlist)
// and `mobile codegen ios --app` (via EmitIOSPlistByBundle) funnel through the
// same emitter, so feeding the same artifact to both yields the same bytes. If
// writeIOSPlist ever drifts back to a bespoke (e.g. build-config-keyed) form,
// this fails.
func TestEmitConfig_IOSPlistMatchesByBundleEmitter(t *testing.T) {
	dir := t.TempDir()
	art := ConfigArtifact{AppID: "app_1", Identifier: "com.example.todo", EnvPreset: "production", BaseURL: "https://e1m.palbase.studio", APIKey: "pb_e1_x"}

	viaApps := filepath.Join(dir, "apps.plist")
	require.NoError(t, emitConfig(art, "ios", viaApps))

	viaCodegen := filepath.Join(dir, "codegen.plist")
	require.NoError(t, EmitIOSPlistByBundle([]ConfigArtifact{art}, viaCodegen))

	a, err := os.ReadFile(viaApps)
	require.NoError(t, err)
	c, err := os.ReadFile(viaCodegen)
	require.NoError(t, err)
	require.Equal(t, string(c), string(a), "apps-config plist must be byte-identical to the codegen bundle-keyed emitter's output (both must be the SDK-decodable bundle-id-keyed shape)")
}

// TestEmitIOSPlistByBundle_NEnvs pins the N-environment shape: three artifacts
// with DISTINCT bundle ids produce a top-level dict with all three keys, each
// holding its own env's config dict, and the keys emitted in SORTED order
// (golden-stable regardless of input order). Mutation-evident: the OLD
// build-config-keyed emitter produced exactly two fixed keys (Debug, Release) —
// the three-bundle-id assertion below would FAIL against it.
func TestEmitIOSPlistByBundle_NEnvs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")

	// Deliberately UNsorted input order to prove keys come out sorted.
	arts := []ConfigArtifact{
		{AppID: "app_1", ProjectRef: "stg", Identifier: "com.x.todo.staging", EnvPreset: "staging", BaseURL: "https://stg.palbase.studio", APIKey: "pb_stg"},
		{AppID: "app_1", ProjectRef: "dev", Identifier: "com.x.todo.dev", EnvPreset: "development", BaseURL: "https://dev.palbase.studio", APIKey: "pb_dev"},
		{AppID: "app_1", ProjectRef: "prod", Identifier: "com.x.todo", EnvPreset: "production", BaseURL: "https://prod.palbase.studio", APIKey: "pb_prod"},
	}
	require.NoError(t, EmitIOSPlistByBundle(arts, out))

	b, err := os.ReadFile(out)
	require.NoError(t, err)
	envs := parseNestedPlist(t, b)

	require.Len(t, envs, 3, "one top-level dict per registered bundle id")
	for _, want := range []struct{ id, preset, baseURL string }{
		{"com.x.todo", "production", "https://prod.palbase.studio"},
		{"com.x.todo.dev", "development", "https://dev.palbase.studio"},
		{"com.x.todo.staging", "staging", "https://stg.palbase.studio"},
	} {
		got, ok := envs[want.id]
		require.True(t, ok, "plist must carry the %s env dict", want.id)
		require.Equal(t, want.id, got["identifier"], "the dict's identifier must equal its bundle-id key")
		require.Equal(t, want.preset, got["env_preset"], want.id)
		require.Equal(t, want.baseURL, got["base_url"], want.id)
	}

	// Keys emitted in sorted order (golden-stable).
	s := string(b)
	prodIdx := strings.Index(s, "<key>com.x.todo</key>")
	devIdx := strings.Index(s, "<key>com.x.todo.dev</key>")
	stgIdx := strings.Index(s, "<key>com.x.todo.staging</key>")
	require.Less(t, prodIdx, devIdx, "keys must be emitted in sorted order")
	require.Less(t, devIdx, stgIdx, "keys must be emitted in sorted order")
}

// TestEmitIOSPlistByBundle_RefusesEmptyIdentifier pins that an artifact with an
// empty identifier (an unconfigured binding) is REFUSED — nothing is written.
func TestEmitIOSPlistByBundle_RefusesEmptyIdentifier(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	arts := []ConfigArtifact{
		{AppID: "app_1", ProjectRef: "prod", Identifier: "com.x.todo", EnvPreset: "production"},
		{AppID: "app_1", ProjectRef: "dev", Identifier: "", EnvPreset: "development"},
	}
	err := EmitIOSPlistByBundle(arts, out)
	require.Error(t, err, "an unconfigured binding (empty identifier) must refuse the whole emit")
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "no partial plist may exist after a refused emit")
}

// TestEmitIOSPlistByBundle_RefusesDuplicateIdentifier pins that two envs sharing
// one bundle id are REFUSED — a duplicate key would silently drop an env.
func TestEmitIOSPlistByBundle_RefusesDuplicateIdentifier(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	arts := []ConfigArtifact{
		{AppID: "app_1", ProjectRef: "prod", Identifier: "com.x.todo", EnvPreset: "production"},
		{AppID: "app_1", ProjectRef: "dev", Identifier: "com.x.todo", EnvPreset: "development"},
	}
	err := EmitIOSPlistByBundle(arts, out)
	require.Error(t, err, "two envs sharing one bundle id must be refused")
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "no partial plist may exist after a refused emit")
}

// TestEmitIOSPlistByBundle_RefusesEmpty pins that an empty artifact slice is
// refused (nothing to write).
func TestEmitIOSPlistByBundle_RefusesEmpty(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	err := EmitIOSPlistByBundle(nil, out)
	require.Error(t, err, "no environments to emit must be refused")
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr))
}

func TestEmitConfig_RefusesUnconfiguredBinding(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "palbase-config.json")
	art := ConfigArtifact{AppID: "app_1", Identifier: "", EnvPreset: "production"}
	err := emitConfig(art, "web", out)
	require.Error(t, err, "an unconfigured binding (empty identifier) must not produce a partial config file")
	// Mutation-evident: if the code wrote the file anyway, this asserts it did not.
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "no partial config file may exist after a refused emit")
}

func TestEmitConfig_RefusesUnconfiguredBindingIOS(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	art := ConfigArtifact{AppID: "app_1", Identifier: "", EnvPreset: "production"}
	err := emitConfig(art, "ios", out)
	require.Error(t, err)
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "no partial plist may exist after a refused emit")
}
