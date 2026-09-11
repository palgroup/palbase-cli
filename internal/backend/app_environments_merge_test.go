package backend

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// BİR ÖNCEKİ LİNK'İN OAUTH'U TAŞINMAZ.
//
// OAuth anlık görüntüsünü HER ZAMAN o anki link sağlar: taşınan bir görüntü,
// istemcinin YANLIŞ projeye kayıt olması demektir. Bağımsız uygulama meta verisi
// (bildirimler, bütünlük) ise aynı backend içinse korunur — link onları
// üretemez, ve üretemediğini silmek de bir kayıptır.
func TestLinkConfigDoesNotKeepPreviousAuth(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", "ios"),
		[]byte(`{"app_id":"registered-app","base_url":"https://backend.example","api_key":"pb_env_cKEY","oauth":{"contract_revision":1},"notifications":{"kept":true}}`), 0o600))

	written, err := writeEnvironmentConfigs([]string{"ios"}, appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: projectAppID, BaseURL: "https://backend.example", APIKey: "pb_env_cNEW"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{ConfigPath("main", "ios")}, written)

	raw, err := os.ReadFile(ConfigPath("main", "ios"))
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"oauth"`, "eski OAuth görüntüsü taşındı")

	var got appEnvironment
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, projectAppID, got.AppID)
	require.Equal(t, "pb_env_cNEW", got.APIKey)
	require.NotEmpty(t, got.Notifications, "link'in üretemediği meta veri silindi")
}

// BAŞKA BİR YIĞININ DEĞERLERİ ÖDÜNÇ ALINMAZ.
//
// Ölçüldü (25.08.2026): farklı bir projeye yeniden link'lendiğinde birleştirme
// eski değerleri taşıyor ve dosya ESKİ ortamı adlandırıyordu.
func TestLinkDoesNotBorrowIdentifiersFromAnotherStack(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", webPlatform),
		[]byte(`{"app_id":"another-app","base_url":"https://old.example","api_key":"old","notifications":{"stale":true},"integrity":{"stale":true}}`), 0o600))

	next := appEnvironment{AppID: projectAppID, BaseURL: "https://new.example", APIKey: "new"}
	got := mergeConfigWithExisting(ConfigPath("main", webPlatform), next)

	require.Equal(t, projectAppID, got.AppID)
	require.Equal(t, "https://new.example", got.BaseURL)
	require.Empty(t, got.Notifications, "başka bir backend'in meta verisi taşındı")
	require.Empty(t, got.Integrity, "başka bir backend'in meta verisi taşındı")
}

// AYNI BACKEND İSE BAĞIMSIZ META VERİ KORUNUR — yukarıdakinin negatif kontrolü.
// Onsuz bir uygulama "her şeyi at" diyerek iki testi birden geçebilirdi.
func TestSameBackendKeepsIndependentMetadata(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", webPlatform),
		[]byte(`{"app_id":"app","base_url":"https://backend.example","api_key":"key","notifications":{"kept":true},"integrity":{"kept":true}}`), 0o600))

	got := mergeConfigWithExisting(ConfigPath("main", webPlatform),
		appEnvironment{AppID: "app", BaseURL: "https://backend.example", APIKey: "fresh"})

	require.Equal(t, "fresh", got.APIKey)
	require.NotEmpty(t, got.Notifications)
	require.NotEmpty(t, got.Integrity)
}
