package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/sealedclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// androidClientsServer answers the social-auth read with one enabled Google
// Android client per {application_key, variant, package_name}. That read is all
// a link asks before it chooses; the choice is where this test ends.
func androidClientsServer(t *testing.T, clients ...[3]string) *httptest.Server {
	t.Helper()
	native := []any{}
	for _, c := range clients {
		native = append(native, map[string]any{"key": "google-android-" + c[0] + "-" + c[1], "enabled": true,
			"application_key": c[0], "platform": "android", "variant": c[1], "package_name": c[2],
			"android_client_id": "ANDROID.apps.googleusercontent.com", "server_client_id": "SERVER.apps.googleusercontent.com",
			"signing_certificate_sha1": "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD"})
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
		if r.URL.Path == "/v1/management/auth/social-auth" {
			_, _ = w.Write(admin)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	linkedAs(t, srv.URL, "operator")
	return srv
}

// AN ANDROID APP LINK CANNOT IDENTIFY SAYS WHY, AND WHAT TO COMMIT (FR-017).
//
// Flavors and an applicationIdSuffix both mean the literal applicationId is not
// the one every build installs as, so link stops guessing — correctly. But the
// refusal said only "the checkout does not identify one configured target"
// (verification B9, L6): not that the Gradle file was the reason, and not the
// values to write, although the clients it had just read carry them.
func TestAnAndroidAppLinkCannotIdentifyNamesTheReasonAndTheSelectionToCommit(t *testing.T) {
	for _, c := range []struct {
		name, gradle, why string
		committed         OAuthSelection
		clients           [][3]string
		choices           string
	}{
		{
			name:   "flavors",
			gradle: `android { defaultConfig { applicationId = "com.example.app" }; productFlavors { create("demo") { } } }`,
			why:    "app/build.gradle.kts mentions productFlavors",
			clients: [][3]string{
				{"shop", "release", "com.example.app"},
				{"shop-demo", "release", "com.example.app.demo"},
			},
			choices: `  application_key "shop", variant "release", package "com.example.app":` + "\n" +
				`    "oauth": {"android": {"application_key": "shop", "variant": "release"}}` + "\n" +
				`  application_key "shop-demo", variant "release", package "com.example.app.demo":` + "\n" +
				`    "oauth": {"android": {"application_key": "shop-demo", "variant": "release"}}`,
		},
		{
			name:    "a suffix",
			gradle:  `android { defaultConfig { applicationId = "com.example.app" }; buildTypes { debug { applicationIdSuffix = ".debug" } } }`,
			why:     "app/build.gradle.kts mentions applicationIdSuffix",
			clients: [][3]string{{"shop", "debug", "com.example.app.debug"}},
			choices: `  application_key "shop", variant "debug", package "com.example.app.debug":` + "\n" +
				`    "oauth": {"android": {"application_key": "shop", "variant": "debug"}}`,
		},
		{
			// Half a selection committed, and no Android client configured to
			// complete it from: the reason stands alone, with no empty menu.
			name:      "nothing to choose from",
			gradle:    `android { defaultConfig { applicationId = "com.example.app" }; productFlavors { create("demo") { } } }`,
			why:       "app/build.gradle.kts mentions productFlavors",
			committed: OAuthSelection{ApplicationKey: "shop"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			inScratchCheckout(t)
			require.NoError(t, os.MkdirAll("app", 0o755))
			writeFile(t, filepath.Join("app", "build.gradle.kts"), c.gradle)
			main := androidClientsServer(t, c.clients...)
			source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
				"main": {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_env_cPUBLIC"},
			}}

			target := Target{URL: main.URL, OAuth: map[string]OAuthSelection{"android": c.committed}}
			_, _, err := platformEnvironments(context.Background(), &target, "android", source)

			want := "main/android: social sign-in for android needs application_key and variant in " +
				"palbase/project.json oauth.android; the checkout does not identify one configured target — " +
				c.why + ", so the applicationId it declares is not the one every build installs as."
			if c.choices != "" {
				want += "\n  One selection applies to every build type; commit the one this app signs in as in palbase/project.json —\n" +
					c.choices
			}
			require.Error(t, err)
			assert.Equal(t, want, err.Error())
		})
	}
}
