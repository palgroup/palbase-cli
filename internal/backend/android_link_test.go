package backend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestAndroidLink_PrintsGradleAndInitializeNextSteps(t *testing.T) {
	var out bytes.Buffer
	printNativeNextSteps(&out, "android", filepath.Join(".palbase", "android"))
	for _, want := range []string{
		`implementation("io.palbase:palbe:<version>")`,
		`plugin id("io.palbase.codegen")`,
		`.palbase/android/palbase-config.json`,
		`Palbase.initialize(this)`,
		`import io.palbase.pb`,
	} {
		require.Contains(t, out.String(), want)
	}
}

func TestAndroidLink_WritesFixedSlotAndPersistsOnlyAndroidApp(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "oldref", DefaultEnv: "main",
		IOSAppID: "app_ios", MacOSAppID: "app_macos", WebAppID: "app_web",
	}))
	appleSentinel := filepath.Join(".palbase", "ios", "palbase-config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(appleSentinel), 0o755))
	require.NoError(t, os.WriteFile(appleSentinel, []byte("apple"), 0o644))

	rest := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"ref": "prodref", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "app_ios", "platform": "ios"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			body := iosPostBody(t, r)
			require.Equal(t, "android", body["platform"])
			require.Equal(t, "com.example.todo", body["package_name"])
			iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_android", "platform": "android"})
		default:
			t.Fatalf("unexpected call %s %s", r.Method, r.URL.Path)
		}
	})
	lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "app_android", "android")

	summary, err := runNativeLink(context.Background(), nativeLinkDeps{
		rest: rest, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
	}, nativeLinkOpts{
		platform: "android", group: "grp_1", branch: "main", identifier: "com.example.todo",
	}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(".palbase", "android"), summary.ConfigDir)
	require.FileExists(t, filepath.Join(".palbase", "openapi.json"))
	require.FileExists(t, filepath.Join(".palbase", "android", "palbase-config.json"))
	require.Equal(t, "apple", string(mustReadFile(t, appleSentinel)))

	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "prodref", cfg.Ref)
	require.Equal(t, "app_android", cfg.AndroidAppID)
	require.Equal(t, "app_ios", cfg.IOSAppID)
	require.Equal(t, "app_macos", cfg.MacOSAppID)
	require.Equal(t, "app_web", cfg.WebAppID)
}

func TestDetectAndroidApplicationID_KotlinAndGroovy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filename string
		contents string
	}{
		{"kotlin", "build.gradle.kts", `android { defaultConfig { applicationId = "com.example.kotlin" } }`},
		{"groovy", "build.gradle", `android { defaultConfig { applicationId 'com.example.groovy' } }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			appDir := filepath.Join(root, "app")
			require.NoError(t, os.MkdirAll(appDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(appDir, tc.filename), []byte(tc.contents), 0o644))

			got, err := detectAndroidApplicationID(root)
			require.NoError(t, err)
			require.Contains(t, tc.contents, got)
		})
	}
}

func TestAndroidLink_RequiresApplicationID(t *testing.T) {
	_, err := runNativeLink(context.Background(), nativeLinkDeps{}, nativeLinkOpts{
		platform: "android", branch: "main",
	}, io.Discard)
	require.ErrorContains(t, err, "applicationId is required")
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}
