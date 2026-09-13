package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/sealedclient"
)

// A FAILED READ OF ONE ENVIRONMENT IS NOT A FAILED LINK. Every environment of a
// project is described now, and a social-auth read that fails for staging used
// to take down the link of main with it.
func TestANonDefaultEnvironmentWhoseSocialReadFailsIsDropped(t *testing.T) {
	inScratchCheckout(t)
	main := stackServing(t, "pb_project_cMAIN", nil)
	staging, _ := envServer(t, "pb_project_cSTAGING", envServerOpts{}) // its social read answers 404
	linkedAs(t, main.URL, "a-credential")
	linkedAs(t, staging.URL, "a-credential")
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main":    {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_project_cMAIN"},
		"staging": {AppID: projectAppID, BaseURL: staging.URL, APIKey: "pb_project_cSTAGING"},
	}}

	target := Target{URL: main.URL}
	result, dropped, err := platformEnvironments(context.Background(), &target, "web", source)
	require.NoError(t, err)
	require.Len(t, dropped, 1)
	assert.Error(t, dropped["staging"], "the dropped environment carries its reason")
	assert.Contains(t, result.Environments, "main")
	assert.NotContains(t, result.Environments, "staging")
}

func TestTheDefaultEnvironmentWhoseSocialReadFailsIsFatal(t *testing.T) {
	inScratchCheckout(t)
	main, _ := envServer(t, "pb_project_cMAIN", envServerOpts{})
	linkedAs(t, main.URL, "a-credential")
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main": {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_project_cMAIN"},
	}}

	target := Target{URL: main.URL}
	_, _, err := platformEnvironments(context.Background(), &target, "web", source)
	require.Error(t, err)
}

// divergentSnapshotServer answers a social read whose snapshot names another
// environment than the publishable key does, for a checkout whose Xcode project
// does identify the configured iOS client — so the read itself succeeds, and
// the only thing wrong is which environment it is for.
func divergentSnapshotServer(t *testing.T) string {
	t.Helper()
	require.NoError(t, os.Mkdir("Consumer.xcodeproj", 0o755))
	require.NoError(t, os.WriteFile("Consumer.xcodeproj/project.pbxproj", []byte(`PRODUCT_BUNDLE_IDENTIFIER = com.example.app;`), 0o644))
	srv := oauthLinkServer(t, strings.Replace(iosSnapshot, `"environment_ref":"env"`, `"environment_ref":"elsewhere"`, 1))
	return srv.URL
}

// A SNAPSHOT FOR ANOTHER ENVIRONMENT IS A FAILED READ (FR-013). Written, it
// would hand the app a client that signs in against the wrong environment.
func TestANonDefaultEnvironmentWhoseSnapshotDivergesIsDropped(t *testing.T) {
	inScratchCheckout(t)
	main := stackServing(t, "pb_project_cMAIN", nil)
	linkedAs(t, main.URL, "a-credential")
	staging := divergentSnapshotServer(t)
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main":    {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_project_cMAIN"},
		"staging": {AppID: projectAppID, BaseURL: staging, APIKey: "pb_env_cPUBLIC"},
	}}

	target := Target{URL: main.URL}
	result, dropped, err := platformEnvironments(context.Background(), &target, "ios", source)
	require.NoError(t, err)
	require.Contains(t, dropped, "staging")
	assert.ErrorContains(t, dropped["staging"], "differs from the publishable key")
	assert.NotContains(t, result.Environments, "staging")
	assert.Empty(t, target.OAuth, "a dropped environment's client selection was kept")
}

func TestTheDefaultEnvironmentWhoseSnapshotDivergesIsFatal(t *testing.T) {
	inScratchCheckout(t)
	main := divergentSnapshotServer(t)
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main": {AppID: projectAppID, BaseURL: main, APIKey: "pb_env_cPUBLIC"},
	}}

	target := Target{URL: main}
	_, _, err := platformEnvironments(context.Background(), &target, "ios", source)
	require.ErrorContains(t, err, "differs from the publishable key")
}

// oauthAppServer is an environment whose social configuration names an iOS
// client for com.example.app under each of targets — "shop" (variant release)
// or "shop/debug" — and whose snapshot answers the way the server does: the
// client for an application and variant it configures, and no clients for any
// other.
func oauthAppServer(t *testing.T, targets ...string) *httptest.Server {
	t.Helper()
	type configured struct{ appKey, variant string }
	var configs []configured
	native := []any{}
	for _, target := range targets {
		appKey, variant, found := strings.Cut(target, "/")
		if !found {
			variant = "release"
		}
		configs = append(configs, configured{appKey, variant})
		native = append(native, map[string]any{"key": "google-ios-" + appKey + "-" + variant, "enabled": true, "application_key": appKey, "platform": "ios", "variant": variant, "bundle_id": "com.example.app", "ios_client_id": "IOS.apps.googleusercontent.com", "redirect_uri": "com.googleusercontent.apps.IOS:/oauthredirect"})
	}
	providers := map[string]any{}
	for _, name := range []string{"google", "apple", "microsoft", "github"} {
		providers[name] = map[string]any{"enabled": false, "browser_clients": []any{}, "native_clients": []any{}}
	}
	providers["google"] = map[string]any{"enabled": true, "browser_clients": []any{}, "native_clients": native}
	admin, err := json.Marshal(map[string]any{"contract_revision": 1, "credentials": []any{}, "providers": providers})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sealedclient.KeysetPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Palbase-Auth-Contract", "1")
		switch r.URL.Path {
		case "/v1/management/auth/social-auth":
			_, _ = w.Write(admin)
		case "/auth/oauth/config":
			asked, askedVariant := r.URL.Query().Get("application_key"), r.URL.Query().Get("variant")
			clients := []any{}
			for _, c := range configs {
				if asked == c.appKey && askedVariant == c.variant {
					clients = append(clients, map[string]any{"key": "google-ios-" + c.appKey + "-" + c.variant, "provider": "google", "mode": "native", "adapter": "google_ios_pkce", "bundle_id": "com.example.app", "ios_client_id": "IOS.apps.googleusercontent.com", "redirect_uri": "com.googleusercontent.apps.IOS:/oauthredirect"})
				}
			}
			snapshot, _ := json.Marshal(map[string]any{"contract_revision": 1, "config_revision": "2", "environment_ref": "env", "application_key": asked, "platform": "ios", "variant": askedVariant, "clients": clients})
			_, _ = w.Write(snapshot)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	linkedAs(t, srv.URL, "operator")
	return srv
}

func seedConsumerXcodeProject(t *testing.T) {
	t.Helper()
	require.NoError(t, os.Mkdir("Consumer.xcodeproj", 0o755))
	require.NoError(t, os.WriteFile("Consumer.xcodeproj/project.pbxproj", []byte(`PRODUCT_BUNDLE_IDENTIFIER = com.example.app;`), 0o644))
}

// ONE SELECTION PER CHECKOUT (X-3). main, the default, configures the app under
// "shop" and dev under "consumer". The default's selection is the checkout's
// and dev is read with it — so dev is named, not written with its sign-in
// client gone, and the second link does exactly what the first did.
func TestAnEnvironmentTheCheckoutsSelectionDoesNotFitIsNamedOnEveryLink(t *testing.T) {
	inScratchCheckout(t)
	seedConsumerXcodeProject(t)
	dev := oauthAppServer(t, "consumer")
	main := oauthAppServer(t, "shop")
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"dev":  {AppID: projectAppID, BaseURL: dev.URL, APIKey: "pb_env_cPUBLIC"},
		"main": {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_env_cPUBLIC"},
	}}

	target := Target{URL: main.URL}
	for run := 1; run <= 2; run++ {
		result, dropped, err := platformEnvironments(context.Background(), &target, "ios", source)
		require.NoError(t, err, "link %d", run)
		require.NotNil(t, result.Environments["main"].OAuth, "link %d: main got no auth snapshot", run)
		assert.Len(t, result.Environments["main"].OAuth.Clients, 1, "link %d: main's sign-in client was lost", run)
		assert.NotContains(t, result.Environments, "dev", "link %d: dev was written with a selection that does not fit it", run)
		require.Contains(t, dropped, "dev", "link %d: dev was not named", run)
		assert.ErrorContains(t, dropped["dev"], `"shop"`)
		assert.ErrorContains(t, dropped["dev"], `"consumer"`)
		assert.Equal(t, "shop", target.OAuth["ios"].ApplicationKey, "link %d: the recorded selection is not the default environment's", run)
	}
}

// AN ENVIRONMENT THAT CONFIGURES THE APP UNDER BOTH KEYS is read with the
// checkout's selection on the first link too. It used to be dropped as
// ambiguous until a later link found the selection the first one recorded.
func TestAnEnvironmentConfiguredUnderBothKeysIsReadWithTheCheckoutsSelection(t *testing.T) {
	inScratchCheckout(t)
	seedConsumerXcodeProject(t)
	dev := oauthAppServer(t, "consumer", "shop")
	main := oauthAppServer(t, "shop")
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"dev":  {AppID: projectAppID, BaseURL: dev.URL, APIKey: "pb_env_cPUBLIC"},
		"main": {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_env_cPUBLIC"},
	}}

	target := Target{URL: main.URL}
	result, dropped, err := platformEnvironments(context.Background(), &target, "ios", source)
	require.NoError(t, err)
	require.Empty(t, dropped)
	require.NotNil(t, result.Environments["dev"].OAuth, "dev got no auth snapshot")
	assert.Equal(t, "shop", result.Environments["dev"].OAuth.ApplicationKey)
	assert.Len(t, result.Environments["dev"].OAuth.Clients, 1)
}

// THE VARIANT IS HALF OF THE SELECTION. dev configures the app under "shop" for
// its debug variant only; read with main's shop/release it has no client, and
// is named rather than written without one.
func TestAnEnvironmentConfiguredUnderAnotherVariantIsNamed(t *testing.T) {
	inScratchCheckout(t)
	seedConsumerXcodeProject(t)
	dev := oauthAppServer(t, "shop/debug")
	main := oauthAppServer(t, "shop")
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"dev":  {AppID: projectAppID, BaseURL: dev.URL, APIKey: "pb_env_cPUBLIC"},
		"main": {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_env_cPUBLIC"},
	}}

	target := Target{URL: main.URL}
	result, dropped, err := platformEnvironments(context.Background(), &target, "ios", source)
	require.NoError(t, err)
	assert.NotContains(t, result.Environments, "dev", "dev was written with a variant it does not configure")
	require.Contains(t, dropped, "dev")
	assert.ErrorContains(t, dropped["dev"], `"shop" variant "debug"`)
}
