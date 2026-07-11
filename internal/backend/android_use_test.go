package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

func TestAndroidUse_RefreshesOnlyAndroidSlotAndRecordsBranch(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "prodref", DefaultEnv: "main", AndroidAppID: "app_android",
		IOSAppID: "app_ios", MacOSAppID: "app_macos", WebAppID: "app_web",
	}))
	iosConfig := filepath.Join(".palbase", "ios", "palbase-config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(iosConfig), 0o755))
	require.NoError(t, os.WriteFile(iosConfig, []byte("ios-sentinel"), 0o644))

	configBranch := ""
	rig, restBase := iosUseRig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/apikey.reveal":
			iosTRPCOK(w, map[string]any{"endpointRef": "prodrefd", "publishableKey": "pb_generic"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/prodref":
			iosRESTOK(w, http.StatusOK, map[string]any{"group_id": "grp_1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "app_android", "platform": "android"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_android/bindings":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"project_ref": "prodref", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_android/config-artifact":
			configBranch = r.URL.Query().Get("branch")
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_android", "project_ref": "prodref", "api_key": "pb_app",
				"base_url": "https://prodrefd.dev.palbase.studio", "env_preset": "production",
				"platform": "android",
			})
		case r.URL.Path == "/auth/oauth/providers":
			_, _ = w.Write([]byte(`{"providers":{}}`))
		case r.URL.Path == "/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		default:
			t.Fatalf("unexpected call %s %s", r.Method, r.URL.String())
		}
	})
	restore := redirectHostTo(t, "prodrefd.dev.palbase.studio", rig.BaseURL)
	defer restore()

	cmd := newAndroidUseCmd(Resolvers{
		Studio:    func() *studio.Client { return rig },
		REST:      func() REST { return iosRESTClientOn(t, restBase) },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"dev"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "dev", configBranch)

	raw := mustReadFile(t, filepath.Join(".palbase", "android", "palbase-config.json"))
	var generated map[string]any
	require.NoError(t, json.Unmarshal(raw, &generated))
	require.Equal(t, "app_android", generated["app_id"])
	require.Equal(t, "ios-sentinel", string(mustReadFile(t, iosConfig)))

	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "dev", linked.DefaultEnv)
	require.Equal(t, "app_android", linked.AndroidAppID)
	require.Equal(t, "app_ios", linked.IOSAppID)
	require.Equal(t, "app_macos", linked.MacOSAppID)
	require.Equal(t, "app_web", linked.WebAppID)
}

func TestAndroidUse_RequiresLinkedAndroidApp(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "prodref", DefaultEnv: "main"}))
	cmd := newAndroidUseCmd(Resolvers{})
	cmd.SetArgs([]string{"dev"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.ErrorContains(t, cmd.Execute(), "run `palbase android link` first")
}
