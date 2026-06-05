package project

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

// restAgainst spins an httptest server with the given handler and returns
// a real REST transport pointed at it, signing with a real DPoP key.
func restAgainst(t *testing.T, h http.HandlerFunc) (REST, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(srv.URL, key, "pat_test"), srv
}

func okData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": "req_x"})
}

func TestProjectList_REST(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		okData(w, 200, []map[string]any{
			{"ref": "abcd1234", "name": "Demo", "tier": "free", "region": "northeurope", "status": "ready"},
		})
	})

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"list", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, http.MethodGet, gotMethod)
	require.Equal(t, "/api/v1/projects", gotPath)
	require.Equal(t, "DPoP pat_test", gotAuth)
}

func TestProjectCreate_REST_202Handle(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/projects", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--name", "Demo", "--github-account", "personal", "--repo", "demo-repo", "--tier", "pro", "--json"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "abcd1234", gotBody["ref"])
	require.Equal(t, "Demo", gotBody["name"])
	require.Equal(t, "personal", gotBody["githubAccount"])
	require.Equal(t, "demo-repo", gotBody["repoName"])
	require.Equal(t, "pro", gotBody["tier"])
	// No org layer: the owner is the authenticated user, server-derived.
	_, hasOrg := gotBody["orgId"]
	require.False(t, hasOrg, "create body must not carry orgId (org layer removed)")
}

func TestProjectCreate_RequiresGithubAccount(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API without --github-account")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--name", "Demo", "--repo", "demo-repo"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--github-account is required")
}

func TestProjectStatus_REST(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/projects/abcd1234", r.URL.Path)
		okData(w, 200, map[string]any{
			"ref": "abcd1234", "name": "Demo", "tier": "free",
			"region": "northeurope", "status": "provisioning",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"status", "abcd1234", "--json"})
	require.NoError(t, cmd.Execute())
}

func TestProjectStatus_404SurfacesAPIError(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "project_not_found", "error_description": "no such project",
			"status": 404, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"status", "zzzz"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "project_not_found", apiErr.Code)
}

var _ = context.Background
