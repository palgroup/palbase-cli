package apps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmitConfig_WritesWebJSON(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "palbase-config.json")
	art := ConfigArtifact{AppID: "app_1", Identifier: "https://app.example.com", EnvPreset: "production", BaseURL: "https://e1m.palbase.studio", APIKey: "pb_e1_x"}
	require.NoError(t, emitConfig(art, out))
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, "app_1", got["app_id"])
	require.NotContains(t, got, "identifier", "emitted config no longer carries a bundle identifier")
	require.Equal(t, "production", got["env_preset"])
	require.Equal(t, "https://e1m.palbase.studio", got["base_url"])
	require.Equal(t, "pb_e1_x", got["api_key"])
}

func TestEmitConfig_RefusesUnconfiguredBinding(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "palbase-config.json")
	art := ConfigArtifact{AppID: "app_1", Identifier: "", EnvPreset: "production"}
	err := emitConfig(art, out)
	require.Error(t, err, "an unconfigured binding (empty identifier) must not produce a partial config file")
	// Mutation-evident: if the code wrote the file anyway, this asserts it did not.
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "no partial config file may exist after a refused emit")
}
