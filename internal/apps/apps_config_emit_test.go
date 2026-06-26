package apps

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
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

// parseNestedPlist parses the build-config-conditioned Palbase-Info.plist
// (top-level <dict> with <key>Debug</key>/<key>Release</key>, each a nested
// config <dict>) into env -> {key: value}. Walks the XML token stream so the
// nesting is honoured exactly — schema-agnostic, so a missing Debug/Release key
// or a dropped field surfaces as an absent map entry (mutation-evident: the OLD
// flat single-dict form has NO Debug/Release keys and yields an empty map). It
// mirrors backend.parsePerEnvPlist so the apps-package assertion pins the SAME
// nested shape the codegen path is tested against.
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
// build-config-conditioned NESTED plist the iOS SDK (PalbaseAppConfig.load)
// decodes — a top-level dict with <key>Debug</key> + <key>Release</key>, each a
// config sub-dict carrying the five config-match fields. The single resolved env
// fills BOTH build-config keys, so the app decodes (and works) in either build
// configuration. Mutation-evident: the OLD flat single-dict form had no
// Debug/Release keys, so parseNestedPlist would yield an empty map and every
// require below would FAIL.
func TestEmitConfig_WritesIOSPlist(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	art := ConfigArtifact{AppID: "app_1", Identifier: "com.example.todo", EnvPreset: "production", BaseURL: "https://e1m.palbase.studio", APIKey: "pb_e1_x"}
	require.NoError(t, emitConfig(art, "ios", out))
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	envs := parseNestedPlist(t, b)

	require.Contains(t, envs, BuildConfigDebug, "plist must carry a Debug build-config dict (SDK decodes envs[buildConfig])")
	require.Contains(t, envs, BuildConfigRelease, "plist must carry a Release build-config dict")
	// The single resolved env fills BOTH keys, so either build config decodes.
	for _, cfg := range []string{BuildConfigDebug, BuildConfigRelease} {
		got := envs[cfg]
		require.Equal(t, "app_1", got["app_id"], cfg)
		require.Equal(t, "com.example.todo", got["identifier"], cfg)
		require.Equal(t, "production", got["env_preset"], cfg)
		require.Equal(t, "https://e1m.palbase.studio", got["base_url"], cfg)
		require.Equal(t, "pb_e1_x", got["api_key"], cfg)
	}
}

// TestEmitConfig_IOSPlistMatchesPerEnvEmitter proves the two emit paths produce
// byte-IDENTICAL plist structure: `apps config` (single env, via writeIOSPlist)
// and `mobile codegen ios --app` (via EmitIOSPlistPerEnv) funnel through the
// same emitter, so feeding the same artifact to both yields the same bytes. If
// writeIOSPlist ever drifts back to a bespoke (e.g. flat) form, this fails.
func TestEmitConfig_IOSPlistMatchesPerEnvEmitter(t *testing.T) {
	dir := t.TempDir()
	art := ConfigArtifact{AppID: "app_1", Identifier: "com.example.todo", EnvPreset: "production", BaseURL: "https://e1m.palbase.studio", APIKey: "pb_e1_x"}

	viaApps := filepath.Join(dir, "apps.plist")
	require.NoError(t, emitConfig(art, "ios", viaApps))

	viaCodegen := filepath.Join(dir, "codegen.plist")
	require.NoError(t, EmitIOSPlistPerEnv(art, art, viaCodegen))

	a, err := os.ReadFile(viaApps)
	require.NoError(t, err)
	c, err := os.ReadFile(viaCodegen)
	require.NoError(t, err)
	require.Equal(t, string(c), string(a), "apps-config plist must be byte-identical to the codegen per-env emitter's output (both must be the SDK-decodable nested shape)")
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
