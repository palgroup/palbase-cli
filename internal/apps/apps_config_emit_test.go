package apps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmitConfig_WritesCanonicalWebJSON(t *testing.T) {
	out := filepath.Join(t.TempDir(), "palbase-config.json")
	require.NoError(t, emitConfig(ConfigArtifact{
		AppID: "app_web", EnvPreset: "production",
		BaseURL: "https://prodm.palbase.studio", APIKey: "pb_web",
	}, out))
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, map[string]any{
		"app_id": "app_web", "env_preset": "production",
		"base_url": "https://prodm.palbase.studio", "api_key": "pb_web",
	}, got)
}
