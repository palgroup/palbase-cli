package apps

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// parsePlistDict parses the flat <key>…</key><string>…</string> sequence of a
// minimal Apple plist back into a map. It is intentionally schema-agnostic so
// a missing or mistyped key surfaces as an absent map entry — making the
// round-trip assertions mutation-evident.
func parsePlistDict(t *testing.T, b []byte) map[string]string {
	t.Helper()
	type entry struct {
		XMLName xml.Name
		Value   string `xml:",chardata"`
	}
	type dict struct {
		Entries []entry `xml:",any"`
	}
	type plist struct {
		Dict dict `xml:"dict"`
	}
	var p plist
	require.NoError(t, xml.Unmarshal(b, &p), "emitted plist must be valid XML")
	out := map[string]string{}
	var pendingKey string
	haveKey := false
	for _, e := range p.Dict.Entries {
		switch e.XMLName.Local {
		case "key":
			pendingKey = e.Value
			haveKey = true
		case "string":
			if haveKey {
				out[pendingKey] = e.Value
				haveKey = false
			}
		}
	}
	return out
}

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

func TestEmitConfig_WritesIOSPlist(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "Palbase-Info.plist")
	art := ConfigArtifact{AppID: "app_1", Identifier: "com.example.todo", EnvPreset: "production", BaseURL: "https://e1m.palbase.studio", APIKey: "pb_e1_x"}
	require.NoError(t, emitConfig(art, "ios", out))
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	got := parsePlistDict(t, b)
	require.Equal(t, "app_1", got["app_id"])
	require.Equal(t, "com.example.todo", got["identifier"])
	require.Equal(t, "production", got["env_preset"])
	require.Equal(t, "https://e1m.palbase.studio", got["base_url"])
	require.Equal(t, "pb_e1_x", got["api_key"])
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
