package apps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
)

func restAgainst(t *testing.T, h http.HandlerFunc) REST {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(srv.URL, key, "pat_test")
}

func okData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": "req_x"})
}

type fatalREST struct{ t *testing.T }

func (f fatalREST) Do(context.Context, string, string, any, any) error {
	f.t.Fatal("must not call the API")
	return nil
}

func TestAppsCmd_Subcommands(t *testing.T) {
	cmd := Cmd(Resolvers{REST: func() REST { return nil }})
	var got []string
	for _, child := range cmd.Commands() {
		got = append(got, child.Name())
	}
	sort.Strings(got)
	require.Equal(t, []string{"attest", "config", "create", "delete", "enforce", "list"}, got)
}

func TestAttestHelp_MatchesGatewayRouteScope(t *testing.T) {
	cmd := Cmd(Resolvers{REST: func() REST { return nil }})
	attest, _, err := cmd.Find([]string{"attest"})
	require.NoError(t, err)
	require.Contains(t, attest.Long, "user-backend calls")
	require.Contains(t, attest.Long, "all branches")
	require.Contains(t, attest.Long, "Storage uploads")
	require.Contains(t, attest.Long, "remain exempt")
	require.NotContains(t, attest.Long, "requests to that env")
}

func TestAppsCreate_PostsOnlyPlatformAndName(t *testing.T) {
	var body map[string]any
	rest := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/groups/grp_1/apps", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		okData(w, http.StatusCreated, map[string]any{"id": "app_mac", "platform": "macos", "display_name": "Mac"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return rest }})
	cmd.SetArgs([]string{"create", "grp_1", "--platform", "macos", "--name", "Mac", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, map[string]any{"platform": "macos", "name": "Mac"}, body)
	create := createCmd(func() REST { return nil })
	var flags []string
	create.Flags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag.Name) })
	sort.Strings(flags)
	require.Equal(t, []string{"json", "name", "platform"}, flags)
}

func TestAppsCreate_InvalidPlatformFailsBeforeAPI(t *testing.T) {
	cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
	cmd.SetArgs([]string{"create", "grp_1", "--platform", "bogus", "--name", "X"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.ErrorContains(t, cmd.Execute(), "--platform")
}

func TestAppsCreate_PlatformContract(t *testing.T) {
	for _, platform := range []string{"ios", "macos", "tvos", "watchos", "android", "web"} {
		require.True(t, isValidPlatform(platform), platform)
	}
	require.False(t, isValidPlatform("visionos"))
}

func TestAppsConfig_WritesCanonicalWebConfig(t *testing.T) {
	rest := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/apps/app_web/config-artifact", r.URL.Path)
		require.Equal(t, "prodref", r.URL.Query().Get("env"))
		okData(w, http.StatusOK, map[string]any{
			"app_id": "app_web", "project_ref": "prodref", "api_key": "pb_web",
			"base_url": "https://prodm.dev.palbase.studio", "env_preset": "production", "platform": "web",
		})
	})
	out := filepath.Join(t.TempDir(), "palbase-config.json")
	cmd := Cmd(Resolvers{REST: func() REST { return rest }})
	cmd.SetArgs([]string{"config", "--app", "app_web", "--env", "prodref", "--out", out})
	require.NoError(t, cmd.Execute())
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, map[string]any{
		"app_id": "app_web", "env_preset": "production",
		"base_url": "https://prodm.dev.palbase.studio", "api_key": "pb_web",
	}, got)
}

func TestAppsConfig_RejectsNativeApp(t *testing.T) {
	rest := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		okData(w, http.StatusOK, map[string]any{"app_id": "app_ios", "platform": "ios"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return rest }})
	cmd.SetArgs([]string{"config", "--app", "app_ios", "--env", "prodref"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.ErrorContains(t, cmd.Execute(), "web config only")
}

func TestAppsEnforceAndAttestRoutes(t *testing.T) {
	t.Run("enforce", func(t *testing.T) {
		rest := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPatch, r.Method)
			require.Equal(t, "/api/v1/groups/grp_1", r.URL.Path)
			okData(w, http.StatusOK, map[string]any{"ok": true})
		})
		cmd := Cmd(Resolvers{REST: func() REST { return rest }})
		cmd.SetArgs([]string{"enforce", "grp_1", "--json"})
		require.NoError(t, cmd.Execute())
	})
	t.Run("attest", func(t *testing.T) {
		rest := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPatch, r.Method)
			require.Equal(t, "/api/v1/apps/app_ios/bindings/prodref", r.URL.Path)
			okData(w, http.StatusOK, map[string]any{"ok": true})
		})
		cmd := Cmd(Resolvers{REST: func() REST { return rest }})
		cmd.SetArgs([]string{"attest", "--app", "app_ios", "--env", "prodref", "--json"})
		require.NoError(t, cmd.Execute())
	})
}
