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

	"github.com/palgroup/palbase-cli/internal/auth"
)

func TestWebLinkArtifacts_FirstRunCreatesThenRerunReusesWithoutBindingMutation(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "prodref", DefaultEnv: "main"}))

	tenantCalls := 0
	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantCalls++
		require.Equal(t, "pb_web", r.Header.Get("apikey"))
		require.Equal(t, "Bearer pb_web", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("X-Palbase-Bundle"))
		switch r.URL.Path {
		case "/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case "/auth/oauth/providers":
			_, _ = w.Write([]byte(`{"providers":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(tenant.Close)

	created := false
	postCalls := 0
	mutationCalls := 0
	var createBody map[string]any
	management := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/prodref":
			iosRESTOK(w, http.StatusOK, map[string]any{"group_id": "grp_1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			rows := []map[string]any{{"id": "remote_existing", "platform": "web", "display_name": "Other"}}
			if created {
				rows = append(rows, map[string]any{"id": "app_web", "platform": "web", "display_name": "Local"})
			}
			iosRESTOK(w, http.StatusOK, rows)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			postCalls++
			mutationCalls++
			createBody = iosPostBody(t, r)
			created = true
			iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_web", "platform": "web", "display_name": "Local"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_web/config-artifact":
			require.Equal(t, "prodref", r.URL.Query().Get("env"))
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_web", "project_ref": "prodref", "api_key": "pb_web",
				"base_url": tenant.URL, "env_preset": "production", "platform": "web",
			})
		case r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
			mutationCalls++
			t.Fatalf("web link must not mutate bindings or existing apps: %s %s", r.Method, r.URL.Path)
		default:
			t.Fatalf("unexpected management call %s %s", r.Method, r.URL.String())
		}
	})

	resolvers := Resolvers{REST: func() REST { return management }}
	require.NoError(t, webLinkArtifacts(context.Background(), resolvers, "prodref", io.Discard))
	require.NoError(t, webLinkArtifacts(context.Background(), resolvers, "prodref", io.Discard))
	require.Equal(t, 1, postCalls, "rerun must reuse the locally persisted web app")
	require.Equal(t, 1, mutationCalls, "the only mutation is the first app creation")
	require.Equal(t, map[string]any{"platform": "web", "name": filepath.Base(mustGetwd(t))}, createBody)
	require.Equal(t, 4, tenantCalls, "each run fetches OAuth metadata and OpenAPI")

	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "app_web", linked.WebAppID)
	raw, err := os.ReadFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.Equal(t, map[string]any{
		"app_id": "app_web", "project_ref": "prodref", "base_url": tenant.URL,
		"api_key": "pb_web", "branch": "main", "env_preset": "production",
	}, cfg)
}

func TestWebLinkArtifacts_MismatchedPersistedAppRegistersReplacement(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "prodref", DefaultEnv: "main", WebAppID: "app_ios",
	}))
	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case "/auth/oauth/providers":
			_, _ = w.Write([]byte(`{"providers":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(tenant.Close)

	postCalls := 0
	management := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/prodref":
			iosRESTOK(w, http.StatusOK, map[string]any{"group_id": "grp_1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "app_ios", "platform": "ios"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			postCalls++
			require.Equal(t, map[string]any{
				"platform": "web", "name": filepath.Base(mustGetwd(t)),
			}, iosPostBody(t, r))
			iosRESTOK(w, http.StatusCreated, map[string]any{
				"id": "app_web_new", "platform": "web", "display_name": "Replacement",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_web_new/config-artifact":
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_web_new", "project_ref": "prodref", "api_key": "pb_web_new",
				"base_url": tenant.URL, "env_preset": "production", "platform": "web",
			})
		case r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
			t.Fatalf("replacement must not mutate an existing app: %s %s", r.Method, r.URL.Path)
		default:
			t.Fatalf("unexpected management call %s %s", r.Method, r.URL.String())
		}
	})
	require.NoError(t, webLinkArtifacts(
		context.Background(), Resolvers{REST: func() REST { return management }}, "prodref", io.Discard,
	))
	require.Equal(t, 1, postCalls)
	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "app_web_new", linked.WebAppID)
}

func TestWebLinkArtifacts_PersistsCreatedAppBeforeArtifactFetchAndRetryReuses(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "prodref", DefaultEnv: "main",
		IOSAppID: "app_ios", MacOSAppID: "app_macos", WebAppID: "app_stale",
	}))

	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case "/auth/oauth/providers":
			_, _ = w.Write([]byte(`{"providers":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(tenant.Close)

	created := false
	postCalls := 0
	configCalls := 0
	management := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/prodref":
			iosRESTOK(w, http.StatusOK, map[string]any{"group_id": "grp_1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			rows := []map[string]any{{"id": "app_other", "platform": "web"}}
			if created {
				rows = append(rows, map[string]any{"id": "app_new", "platform": "web"})
			}
			iosRESTOK(w, http.StatusOK, rows)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			postCalls++
			if postCalls > 1 {
				t.Fatalf("retry registered a duplicate web app")
			}
			created = true
			iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_new", "platform": "web"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_new/config-artifact":
			configCalls++
			if configCalls == 1 {
				iosRESTOK(w, http.StatusInternalServerError, map[string]any{"message": "temporary"})
				return
			}
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_new", "project_ref": "prodref", "api_key": "pb_web",
				"base_url": tenant.URL, "env_preset": "production", "platform": "web",
			})
		default:
			t.Fatalf("unexpected management call %s %s", r.Method, r.URL.String())
		}
	})
	resolvers := Resolvers{REST: func() REST { return management }}

	err := webLinkArtifacts(context.Background(), resolvers, "prodref", io.Discard)
	require.ErrorContains(t, err, "fetch app config")

	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "app_new", linked.WebAppID)
	require.Equal(t, "app_ios", linked.IOSAppID)
	require.Equal(t, "app_macos", linked.MacOSAppID)

	require.NoError(t, webLinkArtifacts(context.Background(), resolvers, "prodref", io.Discard))
	require.Equal(t, 1, postCalls)
}

func TestWebLinkCommand_CrossProjectRelinkCreatesAndPersistsReplacement(t *testing.T) {
	t.Chdir(t.TempDir())
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "oldref", DefaultEnv: "staging",
		IOSAppID: "app_old_ios", MacOSAppID: "app_old_macos", WebAppID: "app_old_web",
	}))

	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case "/auth/oauth/providers":
			_, _ = w.Write([]byte(`{"providers":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(tenant.Close)

	postCalls := 0
	management := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/newref":
			iosRESTOK(w, http.StatusOK, map[string]any{"group_id": "grp_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_new/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "app_other_web", "platform": "web"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_new/apps":
			postCalls++
			iosRESTOK(w, http.StatusCreated, map[string]any{
				"id": "app_new_web", "platform": "web", "display_name": "New Web",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_new_web/config-artifact":
			require.Equal(t, "newref", r.URL.Query().Get("env"))
			require.Equal(t, "staging", r.URL.Query().Get("branch"))
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_new_web", "project_ref": "newref", "api_key": "pb_new_web",
				"base_url": tenant.URL, "env_preset": "production", "platform": "web",
			})
		case r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
			t.Fatalf("relink must only create the replacement app: %s %s", r.Method, r.URL.Path)
		default:
			t.Fatalf("unexpected management call %s %s", r.Method, r.URL.String())
		}
	})

	cmd := newWebCmd(Resolvers{REST: func() REST { return management }})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"link", "--ref", "newref"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, 1, postCalls)

	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "newref", linked.Ref)
	require.Equal(t, "staging", linked.DefaultEnv)
	require.Equal(t, "app_new_web", linked.WebAppID)
	require.Equal(t, "app_old_ios", linked.IOSAppID)
	require.Equal(t, "app_old_macos", linked.MacOSAppID)
}
