package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

// restAgainst spins an httptest server and returns a REST client (a real
// *transport.Client) backed by it — so the DPoP-bound Do + {data,request_id}
// envelope unwrap are the SAME as production (mirrors apikey_test.go).
func restAgainst(t *testing.T, h http.HandlerFunc) REST {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(srv.URL, key, "pat_test")
}

// okData writes the /api/v1 success envelope ({data, request_id}).
func okData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": "req_x"})
}

// fatalREST is a REST that fails the test if any request is made — used by the
// client-side-validation tests that must reject before touching the API.
type fatalREST struct{ t *testing.T }

func (f fatalREST) Do(_ context.Context, _, _ string, _, _ any) error {
	f.t.Fatal("must not call the API")
	return nil
}

// TestAppsCmd_HasSubcommands asserts the command tree is wired:
// list / create / delete / config / bind all present.
func TestAppsCmd_HasSubcommands(t *testing.T) {
	cmd := Cmd(Resolvers{REST: func() REST { return nil }})
	got := map[string]bool{}
	for _, c := range cmd.Commands() {
		got[c.Name()] = true
	}
	for _, want := range []string{"list", "create", "delete", "config", "bind"} {
		require.True(t, got[want], "apps must have %q subcommand", want)
	}
}

// TestAppsConfig_RequiresAppAndEnv proves cobra rejects `config` when the
// required --app / --env flags are missing (no API call happens).
func TestAppsConfig_RequiresAppAndEnv(t *testing.T) {
	t.Run("missing both", func(t *testing.T) {
		cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
		cmd.SetArgs([]string{"config"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "required flag")
	})
	t.Run("missing env", func(t *testing.T) {
		cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
		cmd.SetArgs([]string{"config", "--app", "app_1"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "env")
	})
}

// TestAppsCreate_RequiresPlatformAndName proves cobra requires --platform
// and --name, and that an invalid platform is rejected client-side.
func TestAppsCreate_RequiresPlatformAndName(t *testing.T) {
	t.Run("missing platform", func(t *testing.T) {
		cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
		cmd.SetArgs([]string{"create", "grp_1", "--name", "X"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "platform")
	})
	t.Run("invalid platform rejected client-side", func(t *testing.T) {
		cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
		cmd.SetArgs([]string{"create", "grp_1", "--platform", "bogus", "--name", "X"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "platform")
	})
}

// TestAppsList_REST exercises the GET /api/v1/groups/{groupRef}/apps path +
// table/json output.
func TestAppsList_REST(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/groups/grp_1/apps", r.URL.Path)
		okData(w, http.StatusOK, []map[string]any{
			{"id": "app_1", "platform": "ios", "display_name": "My App", "deleted_at": nil},
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"list", "grp_1", "--json"})
	require.NoError(t, cmd.Execute())
}

// TestAppsCreate_REST exercises POST /api/v1/groups/{groupRef}/apps + the
// {platform,name} body.
func TestAppsCreate_REST(t *testing.T) {
	var body map[string]any
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/groups/grp_1/apps", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&body)
		okData(w, http.StatusCreated, map[string]any{"id": "app_2", "platform": "web", "display_name": "Web"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "grp_1", "--platform", "web", "--name", "Web", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "web", body["platform"])
	require.Equal(t, "Web", body["name"])
	require.NotContains(t, body, "groupId", "the group id rides in the PATH, not the body")
	require.NotContains(t, body, "displayName", "the field is `name`, not `displayName`")
}

// TestAppsDelete_REST exercises DELETE /api/v1/apps/{appId}.
func TestAppsDelete_REST(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/api/v1/apps/app_9", r.URL.Path)
		okData(w, http.StatusOK, map[string]any{"ok": true})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "app_9", "--json"})
	require.NoError(t, cmd.Execute())
}

// TestAppsEnforce_REST locks the config-match toggle: `apps enforce <grp>` PATCHes
// the group with apps_required=true; --disable sends false.
func TestAppsEnforce_REST(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantOn  bool
	}{
		{"on (default)", []string{"enforce", "grp_1", "--json"}, true},
		{"off (--disable)", []string{"enforce", "grp_1", "--disable", "--json"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPatch, r.Method)
				require.Equal(t, "/api/v1/groups/grp_1", r.URL.Path)
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, tc.wantOn, body["apps_required"], "apps_required must reflect --disable")
				okData(w, http.StatusOK, map[string]any{"ok": true})
			})
			cmd := Cmd(Resolvers{REST: func() REST { return c }})
			cmd.SetArgs(tc.args)
			require.NoError(t, cmd.Execute())
		})
	}
}

// TestAppsAttest_REST locks the App-Attest toggle: `apps attest --app --env`
// PATCHes the (app × env) binding with attest_enforce; --disable sends false.
func TestAppsAttest_REST(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPatch, r.Method)
		require.Equal(t, "/api/v1/apps/app_1/bindings/todoappm8p6z", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, true, body["attest_enforce"])
		okData(w, http.StatusOK, map[string]any{"ok": true})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"attest", "--app", "app_1", "--env", "todoappm8p6z", "--json"})
	require.NoError(t, cmd.Execute())
}

// TestAppsAttest_RequiresAppEnv asserts the client-side required-flag gate fires
// before any API call.
func TestAppsAttest_RequiresAppEnv(t *testing.T) {
	cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
	cmd.SetArgs([]string{"attest", "--app", "app_1"}) // missing --env
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.Error(t, cmd.Execute())
}

// TestAppsConfig_WritesConfigFileToPath exercises the config-artifact route and
// the end-to-end config-emit path: the fetched (app × env) artifact is written
// as the per-env palbase-config.json the web SDK reads. --env rides the `env`
// query param.
func TestAppsConfig_WritesConfigFileToPath(t *testing.T) {
	var gotEnv string
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/apps/app_1/config-artifact", r.URL.Path)
		gotEnv = r.URL.Query().Get("env")
		okData(w, http.StatusOK, map[string]any{
			"app_id": "app_1", "project_ref": "abcd1234", "endpoint_ref": "abcd1234m",
			"api_key": "pb_abcd1234m_c_x", "base_url": "https://abcd1234m.dev.palbase.studio",
			"env_preset": "development", "platform": "web", "identifier": "https://app.example.com",
		})
	})
	dir := t.TempDir()
	outFile := filepath.Join(dir, "palbase-config.json")
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"config", "--app", "app_1", "--env", "abcd1234", "-o", outFile})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "abcd1234", gotEnv)

	raw, err := os.ReadFile(outFile)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "app_1", got["app_id"])
	require.Equal(t, "https://app.example.com", got["identifier"])
	require.Equal(t, "development", got["env_preset"])
	require.Equal(t, "https://abcd1234m.dev.palbase.studio", got["base_url"])
	require.Equal(t, "pb_abcd1234m_c_x", got["api_key"])
}

// TestAppsConfig_RefusesIOSAppID pins that the retired iOS plist emit path is
// gone: an ios_-prefixed app id is rejected with a pointer at `palbase spec`
// (the PalbaseCodegen SPM plugin owns Palbase-Info.plist now) and the API is
// never called.
func TestAppsConfig_RefusesIOSAppID(t *testing.T) {
	cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
	cmd.SetArgs([]string{"config", "--app", "ios_app_1", "--env", "abcd1234"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase spec")
}

// TestAppsBind_RequiresAppEnvIdentifier proves cobra rejects `bind` when any of
// the three required flags is missing (no API call happens).
func TestAppsBind_RequiresAppEnvIdentifier(t *testing.T) {
	for _, tc := range []struct {
		name, missing string
		args          []string
	}{
		{"missing all", "app", []string{"bind"}},
		{"missing env", "env", []string{"bind", "--app", "app_1", "--identifier", "com.x"}},
		{"missing identifier", "identifier", []string{"bind", "--app", "app_1", "--env", "abc1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
			cmd.SetArgs(tc.args)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err := cmd.Execute()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.missing)
		})
	}
}

// TestAppsBind_RejectsBadApns proves an invalid --apns value is rejected
// client-side, before any API call (matches the server's enum).
func TestAppsBind_RejectsBadApns(t *testing.T) {
	cmd := Cmd(Resolvers{REST: func() REST { return fatalREST{t} }})
	cmd.SetArgs([]string{"bind", "--app", "app_1", "--env", "abc1", "--identifier", "com.x", "--apns", "bogus"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "apns")
}

// TestAppsBind_REST is the table-driven lock on the PUT bindings route: appId +
// projectRef ride the PATH; the body carries identifier + optional teamId/apns
// (present only when their flags are given, so the server never sees an empty
// optional). --env is sent VERBATIM in the path — the command does NOT rewrite
// the env's bare ref into a branch endpoint ref.
func TestAppsBind_REST(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantPath string
		wantBody map[string]any
		wantOmit []string // keys that must NOT be present in the body
	}{
		{
			name:     "required only",
			args:     []string{"bind", "--app", "app_1", "--env", "todoappm8p6z", "--identifier", "com.demo.palbase.dev"},
			wantPath: "/api/v1/apps/app_1/bindings/todoappm8p6z",
			wantBody: map[string]any{"identifier": "com.demo.palbase.dev"},
			wantOmit: []string{"teamId", "apns", "appId", "projectRef"},
		},
		{
			name:     "with team and apns",
			args:     []string{"bind", "--app", "app_2", "--env", "abc1", "--identifier", "com.x", "--team-id", "TEAM123", "--apns", "production"},
			wantPath: "/api/v1/apps/app_2/bindings/abc1",
			wantBody: map[string]any{
				"identifier": "com.x",
				"teamId":     "TEAM123",
				"apns":       "production",
			},
			wantOmit: []string{"appId", "projectRef", "apnsEnvironment"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var body map[string]any
			c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPut, r.Method)
				gotPath = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&body)
				okData(w, http.StatusOK, map[string]any{"ok": true})
			})
			cmd := Cmd(Resolvers{REST: func() REST { return c }})
			cmd.SetArgs(append(tc.args, "--json"))
			require.NoError(t, cmd.Execute())
			require.Equal(t, tc.wantPath, gotPath)
			for k, v := range tc.wantBody {
				require.Equalf(t, v, body[k], "payload field %q", k)
			}
			for _, k := range tc.wantOmit {
				_, present := body[k]
				require.Falsef(t, present, "field %q must be omitted from the body", k)
			}
		})
	}
}

// TestAppsBind_SurfacesRESTError proves a server-side FORBIDDEN (below admin
// role) surfaces cleanly as a command error rather than a silent success.
func TestAppsBind_SurfacesRESTError(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "forbidden",
			"error_description": "admin role required",
			"status":            403,
			"request_id":        "req_x",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"bind", "--app", "app_1", "--env", "abc1", "--identifier", "com.x"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
}
