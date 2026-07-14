package apps

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

func run(t *testing.T, fake *selectiontest.Fake, args ...string) error {
	t.Helper()
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	rest := fake.REST()
	resolver := fake.Resolver(&bytes.Buffer{})
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return cmd.Execute()
}

func TestAppsCmd_Subcommands(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var got []string
	for _, c := range cmd.Commands() {
		got = append(got, c.Name())
	}
	sort.Strings(got)
	require.Equal(t, []string{"attest", "config", "create", "delete", "enforce", "list"}, got)
}

// The apps surface splits across the two boundaries: REGISTRATION is
// Project-scoped (apps.project_id is singular), CONFIGURATION is
// Environment-scoped (one binding per environment). A path that mixes them up
// is the bug this table catches.
func TestApps_HitsTheV2Paths(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		route string
		query string
		reply func(f *selectiontest.Fake)
	}{
		{
			name: "list is project-scoped", args: []string{"list", "--json"},
			route: "GET /api/v2/projects/proj_1/apps",
			reply: func(f *selectiontest.Fake) {
				f.OK("GET /api/v2/projects/proj_1/apps", []map[string]any{{"id": "app_1", "platform": "ios", "display_name": "App"}})
			},
		},
		{
			name: "create is project-scoped", args: []string{"create", "--platform", "macos", "--name", "Mac", "--json"},
			route: "POST /api/v2/projects/proj_1/apps",
			reply: func(f *selectiontest.Fake) {
				f.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
					selectiontest.WriteOK(w, http.StatusCreated, map[string]any{"id": "app_mac", "platform": "macos", "display_name": "Mac"})
				})
			},
		},
		{
			name: "delete is app-scoped", args: []string{"delete", "app_mac", "--json"},
			route: "DELETE /api/v2/apps/app_mac",
			reply: func(f *selectiontest.Fake) {
				f.OK("DELETE /api/v2/apps/app_mac", map[string]any{"projectId": "proj_1"})
			},
		},
		{
			name: "enforce patches the PROJECT", args: []string{"enforce", "--json"},
			route: "PATCH /api/v2/projects/proj_1",
			reply: func(f *selectiontest.Fake) {
				f.OK("PATCH /api/v2/projects/proj_1", map[string]any{"id": "proj_1", "apps_required": true})
			},
		},
		{
			name: "attest patches the (app x ENVIRONMENT) binding", args: []string{"attest", "--app", "app_ios", "--json"},
			route: "PATCH /api/v2/apps/app_ios/bindings/app1prod",
			reply: func(f *selectiontest.Fake) {
				f.OK("PATCH /api/v2/apps/app_ios/bindings/app1prod", map[string]any{"projectId": "proj_1"})
			},
		},
		{
			name: "config takes environmentRef as a QUERY param", args: []string{"config", "--app", "app_web"},
			route: "GET /api/v2/apps/app_web/config-artifact", query: "environmentRef=app1prod",
			reply: func(f *selectiontest.Fake) {
				f.OK("GET /api/v2/apps/app_web/config-artifact", map[string]any{
					"app_id": "app_web", "environment_ref": "app1prod", "api_key": "pb_web",
					"base_url": "https://app1prod.dev.palbase.studio", "kind": "production", "platform": "web",
				})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := selectiontest.New(t)
			tc.reply(fake)
			require.NoError(t, run(t, fake, tc.args...))

			req, ok := fake.Find(tc.route)
			require.True(t, ok, "expected %s, got %v", tc.route, fake.Routes())
			require.Equal(t, tc.query, req.Query)
			for _, route := range fake.Routes() {
				require.NotContains(t, route, "/api/v1/")
				require.NotContains(t, route, "/groups/", "groups are gone — apps hang off the PROJECT")
			}
		})
	}
}

func TestAppsCreate_SendsTheV2Body(t *testing.T) {
	fake := selectiontest.New(t)
	fake.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusCreated, map[string]any{"id": "app_a", "platform": "android"})
	})
	require.NoError(t, run(t, fake, "create", "--platform", "android", "--name", "Droid", "--identifier", "com.x.y", "--json"))

	req, ok := fake.Find("POST /api/v2/projects/proj_1/apps")
	require.True(t, ok)
	// `displayName`, not `name` — the v2 body is STRICT, so the old key is a 400.
	require.Equal(t, map[string]any{
		"platform": "android", "displayName": "Droid", "identifier": "com.x.y",
	}, req.Body)
}

func TestAppsEnforce_SendsAppsRequired(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("PATCH /api/v2/projects/proj_1", map[string]any{"id": "proj_1", "apps_required": false})
	require.NoError(t, run(t, fake, "enforce", "--disable", "--json"))

	req, ok := fake.Find("PATCH /api/v2/projects/proj_1")
	require.True(t, ok)
	require.Equal(t, map[string]any{"appsRequired": false}, req.Body)
}

func TestAppsConfig_WritesTheCanonicalWebConfig(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("GET /api/v2/apps/app_web/config-artifact", map[string]any{
		"app_id": "app_web", "environment_ref": "app1prod", "api_key": "pb_web",
		"base_url": "https://app1prod.dev.palbase.studio", "kind": "production", "platform": "web",
	})

	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	out := filepath.Join(dir, "palbase-config.json")

	rest := fake.REST()
	resolver := fake.Resolver(&bytes.Buffer{})
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"config", "--app", "app_web", "--out", out})
	require.NoError(t, cmd.Execute())

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	// The config names the ENVIRONMENT and carries NO branch: the URL + key
	// already identify the runtime.
	require.Equal(t, map[string]any{
		"app_id": "app_web", "environment_ref": "app1prod", "kind": "production",
		"base_url": "https://app1prod.dev.palbase.studio", "api_key": "pb_web",
	}, got)
	require.NotContains(t, string(raw), "branch")
	require.NotContains(t, string(raw), "env_preset")
	require.NotContains(t, string(raw), "project_ref")
}

func TestAppsConfig_RejectsANativeApp(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("GET /api/v2/apps/app_ios/config-artifact", map[string]any{"app_id": "app_ios", "platform": "ios"})
	require.ErrorContains(t, run(t, fake, "config", "--app", "app_ios"), "web config only")
}

func TestAppsCreate_InvalidPlatformFailsBeforeTheAPI(t *testing.T) {
	fake := selectiontest.New(t)
	require.ErrorContains(t, run(t, fake, "create", "--platform", "visionos", "--name", "X"), "--platform")
	require.Empty(t, fake.Routes(), "a bad platform must never reach the server")
}

func TestPlatformContract(t *testing.T) {
	for _, p := range []string{"ios", "macos", "tvos", "watchos", "android", "web"} {
		require.True(t, IsValidPlatform(p), p)
	}
	require.False(t, IsValidPlatform("visionos"))
}

func TestConfigArtifactPath_EscapesTheEnvironmentRef(t *testing.T) {
	require.Equal(t,
		"/api/v2/apps/app_1/config-artifact?environmentRef=app1prod",
		ConfigArtifactPath("app_1", "app1prod"))
}
