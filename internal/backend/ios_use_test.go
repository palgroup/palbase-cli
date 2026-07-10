package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
)

func TestIOSUse_RefreshesSharedSpecAndIOSSlotWithoutBindingMutation(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "prodref", DefaultEnv: "main", IOSAppID: "app_ios",
	}))
	macConfig := filepath.Join(".palbase", "macos", "palbase-config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(macConfig), 0o755))
	require.NoError(t, os.WriteFile(macConfig, []byte("mac-sentinel"), 0o644))

	mutations := 0
	configBranch := ""
	rig, restBase := iosUseRig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/apikey.reveal":
			iosTRPCOK(w, map[string]any{"endpointRef": "prodrefd", "publishableKey": "pb_generic"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/prodref":
			iosRESTOK(w, http.StatusOK, map[string]any{"group_id": "grp_1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"id": "app_ios", "platform": "ios", "display_name": "Phone"},
				{"id": "app_other", "platform": "ios", "display_name": "Other"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_ios/bindings":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"project_ref": "prodref", "env_preset": "production"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_ios/config-artifact":
			configBranch = r.URL.Query().Get("branch")
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_ios", "project_ref": "prodref", "api_key": "pb_app",
				"base_url": "https://prodrefd.dev.palbase.studio", "env_preset": "production",
				"platform": "ios",
			})
		case r.URL.Path == "/auth/oauth/providers":
			require.Empty(t, r.Header.Get("X-Palbase-Bundle"))
			_, _ = w.Write([]byte(`{"providers":{}}`))
		case r.URL.Path == "/openapi.json":
			require.Equal(t, "pb_app", r.Header.Get("apikey"))
			require.Empty(t, r.Header.Get("X-Palbase-Bundle"))
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
			mutations++
			t.Fatalf("ios use must be read-only: %s %s", r.Method, r.URL.Path)
		default:
			t.Fatalf("unexpected call %s %s", r.Method, r.URL.String())
		}
	})
	restore := redirectHostTo(t, "prodrefd.dev.palbase.studio", rig.BaseURL)
	defer restore()

	cmd := newIOSUseCmd(Resolvers{
		Studio:    func() *studio.Client { return rig },
		REST:      func() REST { return iosRESTClientOn(t, restBase) },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	})
	var flags []string
	cmd.Flags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag.Name) })
	require.Equal(t, []string{"ref"}, flags)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"dev"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "dev", configBranch)
	require.Zero(t, mutations)

	_, err := os.Stat(filepath.Join(".palbase", "openapi.json"))
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(".palbase", "ios", "palbase-config.json"))
	require.NoError(t, err)
	var cfgFile map[string]any
	require.NoError(t, json.Unmarshal(raw, &cfgFile))
	require.Equal(t, map[string]any{
		"app_id": "app_ios", "env_preset": "production",
		"base_url": "https://prodrefd.dev.palbase.studio", "api_key": "pb_app",
	}, cfgFile)
	macRaw, err := os.ReadFile(macConfig)
	require.NoError(t, err)
	require.Equal(t, "mac-sentinel", string(macRaw))
	_, rootConfigErr := os.Stat(filepath.Join(".palbase", "palbase-config.json"))
	require.True(t, os.IsNotExist(rootConfigErr))

	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "dev", linked.DefaultEnv)
	require.Equal(t, "app_ios", linked.IOSAppID)
}

func TestIOSUse_RequiresLocallyLinkedIOSApp(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "prodref", DefaultEnv: "main"}))
	cmd := newIOSUseCmd(Resolvers{})
	cmd.SetArgs([]string{"dev"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	require.ErrorContains(t, err, "run `palbase ios link` first")
}

func TestIOSUse_UsesAndPersistsReplacementForStaleApp(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "prodref", DefaultEnv: "main",
		IOSAppID: "app_stale", MacOSAppID: "app_macos", WebAppID: "app_web",
	}))

	postCalls := 0
	rig, restBase := iosUseRig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/apikey.reveal":
			iosTRPCOK(w, map[string]any{"endpointRef": "prodrefd", "publishableKey": "pb_generic"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/prodref":
			iosRESTOK(w, http.StatusOK, map[string]any{"group_id": "grp_1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "app_other", "platform": "ios"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			postCalls++
			iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_new", "platform": "ios"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_new/bindings":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"project_ref": "prodref", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_new/config-artifact":
			require.Equal(t, "dev", r.URL.Query().Get("branch"))
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_new", "project_ref": "prodref", "api_key": "pb_app",
				"base_url": "https://prodrefd.dev.palbase.studio", "env_preset": "production",
				"platform": "ios",
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

	cmd := newIOSUseCmd(Resolvers{
		Studio:    func() *studio.Client { return rig },
		REST:      func() REST { return iosRESTClientOn(t, restBase) },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	})
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"dev"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, 1, postCalls)

	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "dev", linked.DefaultEnv)
	require.Equal(t, "app_new", linked.IOSAppID)
	require.Equal(t, "app_macos", linked.MacOSAppID)
	require.Equal(t, "app_web", linked.WebAppID)
}
