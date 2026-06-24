package apps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// trpcOK writes a tRPC success envelope ({result:{data:{json:...}}}).
func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// studioAgainst spins an httptest server and returns a *studio.Client
// backed by it (mirrors secret_test.go).
func studioAgainst(t *testing.T, h http.HandlerFunc) Studio {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	})
}

// innerInput decodes the inner {"json":{...}} payload from a tRPC POST body.
func innerInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
	return outer.JSON
}

// TestAppsCmd_HasSubcommands asserts the command tree is wired:
// list / create / delete / config all present.
func TestAppsCmd_HasSubcommands(t *testing.T) {
	cmd := Cmd(Resolvers{Studio: func() Studio { return nil }})
	got := map[string]bool{}
	for _, c := range cmd.Commands() {
		got[c.Name()] = true
	}
	for _, want := range []string{"list", "create", "delete", "config"} {
		require.True(t, got[want], "apps must have %q subcommand", want)
	}
}

// TestAppsConfig_RequiresAppAndEnv proves cobra rejects `config` when the
// required --app / --env flags are missing (no API call happens).
func TestAppsConfig_RequiresAppAndEnv(t *testing.T) {
	t.Run("missing both", func(t *testing.T) {
		cmd := Cmd(Resolvers{Studio: func() Studio {
			t.Fatal("must not call the API when required flags are missing")
			return nil
		}})
		cmd.SetArgs([]string{"config"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "required flag")
	})
	t.Run("missing env", func(t *testing.T) {
		cmd := Cmd(Resolvers{Studio: func() Studio {
			t.Fatal("must not call the API when --env is missing")
			return nil
		}})
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
		cmd := Cmd(Resolvers{Studio: func() Studio {
			t.Fatal("must not call the API when --platform is missing")
			return nil
		}})
		cmd.SetArgs([]string{"create", "grp_1", "--name", "X"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "platform")
	})
	t.Run("invalid platform rejected client-side", func(t *testing.T) {
		cmd := Cmd(Resolvers{Studio: func() Studio {
			t.Fatal("must not call the API for an invalid platform")
			return nil
		}})
		cmd.SetArgs([]string{"create", "grp_1", "--platform", "bogus", "--name", "X"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "platform")
	})
}

// TestAppsList_Query exercises the apps.list tRPC path + table/json output.
func TestAppsList_Query(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/trpc/apps.list", r.URL.Path)
		trpcOK(w, []map[string]any{
			{"id": "app_1", "platform": "ios", "display_name": "My App", "deleted_at": nil},
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"list", "grp_1", "--json"})
	require.NoError(t, cmd.Execute())
}

// TestAppsCreate_Mutation exercises apps.create + that displayName is sent.
func TestAppsCreate_Mutation(t *testing.T) {
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/apps.create", r.URL.Path)
		body = innerInput(t, r)
		trpcOK(w, map[string]any{"id": "app_2", "platform": "web", "display_name": "Web"})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"create", "grp_1", "--platform", "web", "--name", "Web", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "grp_1", body["groupId"])
	require.Equal(t, "web", body["platform"])
	require.Equal(t, "Web", body["displayName"])
}

// TestAppsDelete_Mutation exercises apps.delete with the app id.
func TestAppsDelete_Mutation(t *testing.T) {
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/apps.delete", r.URL.Path)
		body = innerInput(t, r)
		trpcOK(w, map[string]any{"ok": true})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"delete", "app_9", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "app_9", body["appId"])
}

// TestAppsConfig_WritesArtifactToFile exercises apps.configArtifact and the
// -o file-output path, asserting the per-(app × env) artifact shape.
func TestAppsConfig_WritesArtifactToFile(t *testing.T) {
	var body map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/trpc/apps.configArtifact", r.URL.Path)
		var outer struct {
			JSON map[string]any `json:"json"`
		}
		require.NoError(t, json.Unmarshal([]byte(r.URL.Query().Get("input")), &outer))
		body = outer.JSON
		trpcOK(w, map[string]any{
			"app_id": "app_1", "project_ref": "abcd1234", "endpoint_ref": "abcd1234m",
			"api_key": "pb_abcd1234m_c_x", "base_url": "https://abcd1234m.dev.palbase.studio",
			"env_preset": nil, "platform": "ios", "identifier": "com.example.app",
		})
	})
	dir := t.TempDir()
	outFile := filepath.Join(dir, "artifact.json")
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"config", "--app", "app_1", "--env", "abcd1234", "-o", outFile})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "app_1", body["appId"])
	require.Equal(t, "abcd1234", body["projectRef"])

	raw, err := os.ReadFile(outFile)
	require.NoError(t, err)
	var art configArtifact
	require.NoError(t, json.Unmarshal(raw, &art))
	require.Equal(t, "app_1", art.AppID)
	require.Equal(t, "abcd1234m", art.EndpointRef)
	require.Equal(t, "ios", art.Platform)
	require.Equal(t, "com.example.app", art.Identifier)
}
