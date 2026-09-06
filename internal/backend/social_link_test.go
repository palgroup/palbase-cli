package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func oauthLinkServer(t *testing.T, snapshot string) *httptest.Server {
	t.Helper()
	providers := map[string]any{}
	for _, name := range []string{"google", "apple", "microsoft", "github"} {
		providers[name] = map[string]any{"enabled": false, "browser_clients": []any{}, "native_clients": []any{}}
	}
	providers["google"] = map[string]any{"enabled": true, "browser_clients": []any{}, "native_clients": []any{map[string]any{"key": "google-ios", "enabled": true, "application_key": "consumer", "platform": "ios", "variant": "release", "bundle_id": "com.example.app", "ios_client_id": "IOS.apps.googleusercontent.com", "redirect_uri": "com.googleusercontent.apps.IOS:/oauthredirect"}}}
	admin, err := json.Marshal(map[string]any{"contract_revision": 1, "credentials": []any{}, "providers": providers})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "1", r.Header.Get("Palbase-Auth-Contract"))
		w.Header().Set("Palbase-Auth-Contract", "1")
		switch r.URL.Path {
		case "/v1/management/auth/social-auth":
			require.Equal(t, "Bearer operator", r.Header.Get("Authorization"))
			_, _ = w.Write(admin)
		case "/auth/oauth/config":
			require.Equal(t, "pb_env_cPUBLIC", r.Header.Get("apikey"))
			require.Equal(t, "consumer", r.URL.Query().Get("application_key"))
			require.Equal(t, "ios", r.URL.Query().Get("platform"))
			require.Equal(t, "release", r.URL.Query().Get("variant"))
			_, _ = w.Write([]byte(snapshot))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	linkedAs(t, srv.URL, "operator")
	return srv
}

const iosSnapshot = `{"contract_revision":1,"config_revision":"2","environment_ref":"env","application_key":"consumer","platform":"ios","variant":"release","clients":[{"key":"google-ios","provider":"google","mode":"native","adapter":"google_ios_pkce","bundle_id":"com.example.app","ios_client_id":"IOS.apps.googleusercontent.com","redirect_uri":"com.googleusercontent.apps.IOS:/oauthredirect"}]}`

func TestOAuthBindingRejectsRetiredAndAmbiguousFields(t *testing.T) {
	for _, fields := range []string{
		`"auth":{}`,
		`"socialAuth":{}`,
		`"oauth":null`,
		`"oauth":{"andorid":{"application_key":"consumer","variant":"release"}}`,
		`"oauth":{"ios":{"application_key":"consumer","variant":"release","client_id":"wrong"}}`,
		`"oauth":{"ios":{"application_key":"consumer","variant":"release","variant":"debug"}}`,
	} {
		t.Run(fields, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, os.MkdirAll(".palbase", 0755))
			raw := []byte(`{"url":"https://stack.example",` + fields + `}`)
			require.NoError(t, os.WriteFile(projectPath(), raw, 0644))
			_, err := readLinkedProject()
			require.Error(t, err)
		})
	}
}

func TestOAuthInferenceRequiresAnUnambiguousAndroidApplication(t *testing.T) {
	for _, config := range []string{
		`applicationId = "com.example.app"; applicationIdSuffix = ".debug"`,
		`applicationId "com.example.app"; productFlavors { create("demo") {} }`,
		`applicationId = "com.example.app"; applicationId = "com.example.other"`,
		`applicationId = "com.example.$flavor"`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, os.WriteFile("build.gradle.kts", []byte(config), 0644))
			require.Empty(t, nativeIdentifiers("android"))
		})
	}
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("build.gradle.kts", []byte(`applicationId = "com.example.app"`), 0644))
	require.Equal(t, []string{"com.example.app"}, nativeIdentifiers("android"))
}

func TestFirstLinkCannotSucceedWithoutWebGenerator(t *testing.T) {
	inScratchCheckout(t)
	writePkgJSON(t, minimalPkgJSON())
	writeFile(t, "main.ts", "// entry\n")
	stubInstall(t)
	srv := stackServing(t, "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0", nil)
	linkedAs(t, srv.URL, "operator")
	require.ErrorContains(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, io.Discard), "generator is unavailable")
	require.NoFileExists(t, "palbe.gen.ts")
	require.NoFileExists(t, filepath.Join(webArtifactsDir, "palbase-config.json"))
	raw, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Equal(t, minimalPkgJSON(), string(raw))
}

func TestLinkSelectsOnlyExactNativeTargetAndValidatesSnapshot(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.Mkdir("Consumer.xcodeproj", 0755))
	require.NoError(t, os.WriteFile("Consumer.xcodeproj/project.pbxproj", []byte(`PRODUCT_BUNDLE_IDENTIFIER = com.example.app;`), 0644))
	srv := oauthLinkServer(t, iosSnapshot)
	snapshot, selection, err := linkedOAuth(context.Background(), Target{URL: srv.URL}, "ios", "pb_env_cPUBLIC", OAuthSelection{})
	require.NoError(t, err)
	require.Equal(t, "consumer", selection.ApplicationKey)
	require.Equal(t, "release", selection.Variant)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"ios_client_id"`)
	require.NotContains(t, string(raw), `"web_client_id"`)
	require.NotContains(t, string(raw), `"client_secret"`)
}

func TestLinkRefusesUnidentifiedNativeApplication(t *testing.T) {
	inScratchCheckout(t)
	srv := oauthLinkServer(t, iosSnapshot)
	_, _, err := linkedOAuth(context.Background(), Target{URL: srv.URL}, "ios", "pb_env_cPUBLIC", OAuthSelection{})
	require.ErrorContains(t, err, "oauth.ios")
}

func TestArtifactPublicationRefusesConcurrentEditsBeforeAnyWrite(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a"), []byte("old"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b"), []byte("user-edit"), 0644))
	before := map[string]artifactFile{"a": {[]byte("old"), 0644}, "b": {[]byte("old"), 0644}}
	after := map[string]artifactFile{"a": {[]byte("generated"), 0644}, "b": {[]byte("generated"), 0644}}
	require.ErrorContains(t, publishArtifacts(root, before, after), "changed during link")
	raw, err := os.ReadFile(filepath.Join(root, "a"))
	require.NoError(t, err)
	require.Equal(t, "old", string(raw))
}

func TestLinkPublishesInstalledWebDependencies(t *testing.T) {
	inScratchCheckout(t)
	writePkgJSON(t, minimalPkgJSON())
	writeFile(t, "main.ts", "// entry\n")
	srv := stackServing(t, "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0", nil)
	linkedAs(t, srv.URL, "operator")
	original := ensurePalbeWeb
	t.Cleanup(func() { ensurePalbeWeb = original })
	ensurePalbeWeb = func(_ context.Context, _ io.Writer) {
		require.NoError(t, os.MkdirAll(filepath.Dir(palbeGenBin), 0755))
		require.NoError(t, os.WriteFile(palbeGenBin, []byte("#!/bin/sh\nprintf '// generated\\n' > \"$2\"\n"), 0755))
	}
	require.NoError(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, io.Discard))
	require.FileExists(t, "palbe.gen.ts")
	require.FileExists(t, palbeGenBin, "the installed SDK must survive removal of the staging directory")
}

func TestLinkFailedInstallPreservesExistingDependencies(t *testing.T) {
	inScratchCheckout(t)
	writePkgJSON(t, minimalPkgJSON())
	writeFile(t, "main.ts", "// entry\n")
	require.NoError(t, os.MkdirAll("node_modules/example", 0755))
	writeFile(t, "node_modules/example/index.js", "original dependency")
	srv := stackServing(t, "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0", nil)
	linkedAs(t, srv.URL, "operator")
	original := ensurePalbeWeb
	t.Cleanup(func() { ensurePalbeWeb = original })
	ensurePalbeWeb = func(_ context.Context, _ io.Writer) {
		require.NoError(t, os.MkdirAll("node_modules/example", 0755))
		writeFile(t, "node_modules/example/index.js", "partial install")
		require.NoError(t, os.MkdirAll(filepath.Dir(palbeGenBin), 0755))
		require.NoError(t, os.WriteFile(palbeGenBin, []byte("#!/bin/sh\nexit 1\n"), 0755))
	}
	require.Error(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"web"}}, io.Discard))
	raw, err := os.ReadFile("node_modules/example/index.js")
	require.NoError(t, err)
	require.Equal(t, "original dependency", string(raw))
	require.NoFileExists(t, palbeGenBin)
	require.NoFileExists(t, "palbe.gen.ts")
}

func TestLinkPublicationConflictRestoresDependencies(t *testing.T) {
	for _, existing := range []bool{false, true} {
		root, stage := t.TempDir(), t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "app.ts"), []byte("user edit"), 0644))
		if existing {
			require.NoError(t, os.Mkdir(filepath.Join(root, "node_modules"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "node_modules", "original"), []byte("old"), 0644))
		}
		require.NoError(t, os.Mkdir(filepath.Join(stage, "node_modules"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(stage, "node_modules", "installed"), []byte("new"), 0644))
		before := map[string]artifactFile{"app.ts": {[]byte("original"), 0644}}
		after := map[string]artifactFile{"app.ts": {[]byte("generated"), 0644}}
		require.ErrorContains(t, publishLinkArtifacts(root, stage, before, after), "changed during link")
		if existing {
			require.FileExists(t, filepath.Join(root, "node_modules", "original"))
		} else {
			require.NoDirExists(t, filepath.Join(root, "node_modules"))
		}
		require.NoFileExists(t, filepath.Join(root, "node_modules", "installed"))
	}
}
