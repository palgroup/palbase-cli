package backend

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestLinkConfigDoesNotKeepPreviousAuth(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	dir := filepath.Join(nativeArtifactsDir, "ios")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "palbase-config.json"), []byte(`{"default_environment":"main","environments":{"main":{"app_id":"registered-app","base_url":"https://backend.example","api_key":"pb_env_cKEY","oauth":{"google":{"client_id":"old"}}}}}`), 0644))
	path, err := writeAppEnvironments("ios", appEnvironments{Default: "main", Environments: map[string]appEnvironment{"main": {AppID: projectAppID, BaseURL: "https://backend.example", APIKey: "pb_env_cNEW"}}})
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"oauth"`)
	require.NotContains(t, string(raw), `"auth"`)
	var got appEnvironments
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "registered-app", got.Environments["main"].AppID)
	require.Equal(t, "pb_env_cNEW", got.Environments["main"].APIKey)
}

func TestLinkDoesNotBorrowIdentifiersFromAnotherStack(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "palbase-config.json"), []byte(`{"default_environment":"main","environments":{"main":{"app_id":"another-app","base_url":"https://old.example","api_key":"old"},"staging":{"app_id":"another-app","base_url":"https://staging.old.example","api_key":"old"}}}`), 0644))
	next := appEnvironments{Default: "main", Environments: map[string]appEnvironment{"main": {AppID: projectAppID, BaseURL: "https://new.example", APIKey: "new"}}}
	got := mergeWithExisting(dir, next)
	require.Equal(t, projectAppID, got.Environments["main"].AppID)
	require.Len(t, got.Environments, 1)
}

func TestWebConfigRefreshesOAuthWithoutDroppingOtherFeatures(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(webArtifactsDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(webArtifactsDir, "palbase-config.json"), []byte(`{"app_id":"app","base_url":"https://backend.example","api_key":"key","oauth":{"apple":{"enabled":true}},"auth":{"clients":["stale"]},"integrity":{"stale":true},"notifications":{"stale":true}}`), 0644))
	got := mergeWebConfigWithExisting(map[string]any{"app_id": "app", "base_url": "https://backend.example", "api_key": "fresh"})
	for _, field := range []string{"oauth", "auth"} {
		require.NotContains(t, got, field)
	}
	for _, field := range []string{"notifications", "integrity"} {
		require.Contains(t, got, field)
	}
	other := mergeWebConfigWithExisting(map[string]any{"app_id": "other", "base_url": "https://other.example", "api_key": "other"})
	for _, field := range []string{"oauth", "auth", "notifications", "integrity"} {
		require.NotContains(t, other, field)
	}
}
