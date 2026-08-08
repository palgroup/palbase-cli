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
	_, err := runOutput(t, fake, args...)
	return err
}

func runOutput(t *testing.T, fake *selectiontest.Fake, args ...string) (string, error) {
	t.Helper()
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	rest := fake.REST()
	resolver := fake.Resolver()
	cmd := Cmd(Resolvers{
		REST:       func() REST { return rest },
		Selection:  func() *selection.Resolver { return resolver },
		PublicHost: func() string { return "dev.palbase.studio" },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestAppsCmd_Subcommands(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var got []string
	for _, c := range cmd.Commands() {
		got = append(got, c.Name())
	}
	sort.Strings(got)
	require.Equal(t, []string{"attest", "config", "create", "delete", "enforce", "identifier", "list", "team-id"}, got)
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
			// The identifier is APP-level, not per-Environment: both association
			// documents are derived from it for every Environment the app is
			// bound to, so the route must carry no environment ref.
			name:  "identifier patches the APP, not a binding",
			args:  []string{"identifier", "--app", "app_ios", "com.demo.palbase", "--json"},
			route: "PATCH /api/v2/apps/app_ios",
			reply: func(f *selectiontest.Fake) {
				f.OK("PATCH /api/v2/apps/app_ios", map[string]any{
					"projectId": "proj_1", "identifier": "com.demo.palbase",
				})
			},
		},
		{
			// team id is BINDING-level (app × Environment) while the bundle id is
			// APP-level — the two halves of one association entry living in two
			// places, which is why only this one carries an environment ref.
			name:  "team-id patches the (app x ENVIRONMENT) binding",
			args:  []string{"team-id", "--app", "app_ios", "K42P7PK4NT", "--json"},
			route: "PATCH /api/v2/apps/app_ios/bindings/app1prod",
			reply: func(f *selectiontest.Fake) {
				f.OK("PATCH /api/v2/apps/app_ios/bindings/app1prod", map[string]any{"projectId": "proj_1"})
			},
		},
		{
			name: "config takes environment_ref as a QUERY param", args: []string{"config", "--app", "app_web"},
			route: "GET /api/v2/apps/app_web/config-artifact", query: "environment_ref=app1prod",
			reply: func(f *selectiontest.Fake) {
				f.OK("GET /api/v2/apps/app_web/config-artifact", map[string]any{
					"app_id": "app_web", "environment_ref": "app1prod", "api_key": "pb_app1prod_c01234567890123456789",
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

func TestAppsAttest_JSONUsesCanonicalEnvironmentRef(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("PATCH /api/v2/apps/app_ios/bindings/app1prod", map[string]any{"projectId": "proj_1"})

	out, err := runOutput(t, fake, "attest", "--app", "app_ios", "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, "app1prod", got["environment_ref"])
	require.NotContains(t, got, "environmentRef")
}

func TestAppsConfig_WritesTheCanonicalWebConfig(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("GET /api/v2/apps/app_web/config-artifact", map[string]any{
		"app_id": "app_web", "environment_ref": "app1prod", "api_key": "pb_app1prod_c01234567890123456789",
		"base_url": "https://app1prod.dev.palbase.studio", "kind": "production", "platform": "web",
	})

	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	out := filepath.Join(dir, "palbase-config.json")

	rest := fake.REST()
	resolver := fake.Resolver()
	cmd := Cmd(Resolvers{
		REST:       func() REST { return rest },
		Selection:  func() *selection.Resolver { return resolver },
		PublicHost: func() string { return "dev.palbase.studio" },
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
		"base_url": "https://app1prod.dev.palbase.studio", "api_key": "pb_app1prod_c01234567890123456789",
	}, got)
	// Exact equality above locks the artifact to the canonical Environment
	// identity without permitting any extra identity field.
	require.NotContains(t, string(raw), "branch")
	require.NotContains(t, string(raw), "env_preset")
}

// TestAppsConfig_DefaultsToTheCanonicalWebDirectory: without --out, config
// must land in Palbase/palbase-config.json — the SAME directory `palbase web
// link` writes to and @palbase/web's palbe-gen reads from by default. Before
// this, the default was a bare ./palbase-config.json that palbe-gen never
// saw unless the caller passed --out by hand. Palbase/ does not exist yet on
// a fresh checkout, so this also locks that WriteWebConfig creates it.
func TestAppsConfig_DefaultsToTheCanonicalWebDirectory(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("GET /api/v2/apps/app_web/config-artifact", map[string]any{
		"app_id": "app_web", "environment_ref": "app1prod", "api_key": "pb_app1prod_c01234567890123456789",
		"base_url": "https://app1prod.dev.palbase.studio", "kind": "production", "platform": "web",
	})

	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	_, statErr := os.Stat(filepath.Join(dir, "Palbase"))
	require.True(t, os.IsNotExist(statErr), "Palbase/ must not pre-exist — this test proves config creates it")

	rest := fake.REST()
	resolver := fake.Resolver()
	cmd := Cmd(Resolvers{
		REST:       func() REST { return rest },
		Selection:  func() *selection.Resolver { return resolver },
		PublicHost: func() string { return "dev.palbase.studio" },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"config", "--app", "app_web"}) // no --out
	require.NoError(t, cmd.Execute())

	want := filepath.Join(dir, "Palbase", "palbase-config.json")
	raw, err := os.ReadFile(want)
	require.NoError(t, err, "default output must be Palbase/palbase-config.json")
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "app_web", got["app_id"])
	require.Contains(t, out.String(), filepath.Join("Palbase", "palbase-config.json"))
}

func TestAppsConfig_RejectsForeignArtifactBeforeWriting(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("GET /api/v2/apps/app_web/config-artifact", map[string]any{
		"app_id": "app_foreign", "environment_ref": "app1prod",
		"api_key":  "pb_app1prod_c01234567890123456789",
		"base_url": "https://app1prod.dev.palbase.studio", "kind": "production", "platform": "web",
	})

	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	out := filepath.Join(dir, "palbase-config.json")
	rest := fake.REST()
	cmd := Cmd(Resolvers{
		REST:       func() REST { return rest },
		Selection:  func() *selection.Resolver { return fake.Resolver() },
		PublicHost: func() string { return "dev.palbase.studio" },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"config", "--app", "app_web", "--out", out})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true

	require.ErrorContains(t, cmd.Execute(), "app_id")
	require.NoFileExists(t, out)
}

func TestValidateConfigArtifact(t *testing.T) {
	valid := ConfigArtifact{
		AppID: "app_web", EnvironmentRef: "app1prod",
		APIKey:  "pb_app1prod_c01234567890123456789",
		BaseURL: "https://app1prod.dev.palbase.studio",
	}
	require.NoError(t, ValidateConfigArtifact(valid, "app_web", "app1prod", "dev.palbase.studio"))
	for _, publicHost := range []string{"palbase.studio", "integ.dev.palbase.studio"} {
		art := valid
		art.BaseURL = "https://app1prod." + publicHost + "/"
		require.NoError(t, ValidateConfigArtifact(art, "app_web", "app1prod", publicHost))
	}
	require.ErrorContains(
		t,
		ValidateConfigArtifact(valid, "app_web", "app1prod", "https://dev.palbase.studio"),
		"hostname-only public host",
	)

	tests := []struct {
		name string
		edit func(*ConfigArtifact)
		want string
	}{
		{"foreign app", func(a *ConfigArtifact) { a.AppID = "app_other" }, "app_id"},
		{"foreign environment", func(a *ConfigArtifact) { a.EnvironmentRef = "app1stg" }, "environment_ref"},
		{"malformed publishable key", func(a *ConfigArtifact) { a.APIKey = "pb_app1prod_cshort" }, "api_key"},
		{"foreign publishable key", func(a *ConfigArtifact) { a.APIKey = "pb_app1stg_c01234567890123456789" }, "api_key"},
		{"http base url", func(a *ConfigArtifact) { a.BaseURL = "http://app1prod.dev.palbase.studio" }, "base_url"},
		{"official foreign host", func(a *ConfigArtifact) { a.BaseURL = "https://app1stg.dev.palbase.studio" }, "base_url"},
		{"external host", func(a *ConfigArtifact) { a.BaseURL = "https://evil.example" }, "base_url"},
		{"official suffix confusion", func(a *ConfigArtifact) { a.BaseURL = "https://app1prod.evil.palbase.studio" }, "base_url"},
		{"explicit port", func(a *ConfigArtifact) { a.BaseURL = "https://app1prod.dev.palbase.studio:8443" }, "base_url"},
		{"query", func(a *ConfigArtifact) { a.BaseURL = "https://app1prod.dev.palbase.studio?target=other" }, "base_url"},
		{"fragment", func(a *ConfigArtifact) { a.BaseURL = "https://app1prod.dev.palbase.studio#other" }, "base_url"},
		{"non-root path", func(a *ConfigArtifact) { a.BaseURL = "https://app1prod.dev.palbase.studio/proxy" }, "base_url"},
		{"userinfo", func(a *ConfigArtifact) { a.BaseURL = "https://user@app1prod.dev.palbase.studio" }, "base_url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			art := valid
			tc.edit(&art)
			require.ErrorContains(t, ValidateConfigArtifact(art, "app_web", "app1prod", "dev.palbase.studio"), tc.want)
		})
	}
}

func TestValidateConfigArtifact_EnforcesCanonicalEnvironmentRefEnvelope(t *testing.T) {
	const random = "01234567890123456789"
	for _, ref := range []string{"abcd", "abcdefghijklmnopqrstuvwx"} {
		art := ConfigArtifact{
			AppID:          "app_web",
			EnvironmentRef: ref,
			APIKey:         "pb_" + ref + "_c" + random,
			BaseURL:        "https://" + ref + ".dev.palbase.studio",
		}
		require.NoError(t, ValidateConfigArtifact(art, "app_web", ref, "dev.palbase.studio"))
	}

	for _, ref := range []string{
		"abc",
		"abcdefghijklmnopqrstuvwxy",
		"UPPERCASE",
		"bad_ref",
		"bad-ref",
		"tést",
	} {
		t.Run(ref, func(t *testing.T) {
			art := ConfigArtifact{
				AppID:          "app_web",
				EnvironmentRef: ref,
				APIKey:         "pb_" + ref + "_c" + random,
				BaseURL:        "https://" + ref + ".dev.palbase.studio",
			}
			require.Error(t, ValidateConfigArtifact(art, "app_web", ref, "dev.palbase.studio"))
		})
	}
}

func TestAppsConfig_RejectsANativeApp(t *testing.T) {
	fake := selectiontest.New(t)
	fake.OK("GET /api/v2/apps/app_ios/config-artifact", map[string]any{
		"app_id": "app_ios", "environment_ref": "app1prod", "platform": "ios",
		"api_key":  "pb_app1prod_c01234567890123456789",
		"base_url": "https://app1prod.dev.palbase.studio",
	})
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
		"/api/v2/apps/app_1/config-artifact?environment_ref=app1prod",
		ConfigArtifactPath("app_1", "app1prod"))
}
