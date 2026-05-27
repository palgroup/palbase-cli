package branch

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

// restAgainst spins an httptest server and returns a real REST transport
// pointed at it (mirrors project_test.go).
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

func TestBranchCreate_REST_202Handle(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	// --ref avoids depending on a linked project config in the test env.
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234", "--kind", "staging", "--json"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/projects/acme1234/branches", gotPath)
	require.Equal(t, "staging", gotBody["branchName"])
	require.Equal(t, "staging", gotBody["kind"])
	require.Equal(t, true, gotBody["deploy"]) // default ON
}

func TestBranchCreate_ForkFromFlag(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "qa", "--ref", "acme1234", "--kind", "qa", "--fork-from", "main", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "main", gotBody["forkFrom"])
	require.Equal(t, "qa", gotBody["kind"])
}

func TestBranchCreate_NoForkOmitsForkFrom(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234", "--json"})
	require.NoError(t, cmd.Execute())
	_, has := gotBody["forkFrom"]
	require.False(t, has, "forkFrom omitted when --fork-from not passed")
}

func TestBranchCreate_NoDeployFlag(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "preview-x", "--ref", "acme1234", "--kind", "preview", "--no-deploy", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, false, gotBody["deploy"])
	require.Equal(t, "preview", gotBody["kind"])
}

func TestBranchList_REST(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/projects/acme1234/branches", r.URL.Path)
		okData(w, 200, []map[string]any{
			{"id": "b_main", "name": "main", "endpoint_ref": "acme1234m", "status": "active", "ephemeral": false, "url": "https://acme1234m.dev.palbase.studio"},
			{"id": "b_s", "name": "staging", "endpoint_ref": "acme1234s", "status": "active", "ephemeral": false, "url": "https://acme1234s.dev.palbase.studio"},
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"list", "--ref", "acme1234", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
}

func TestBranchDelete_WithYes_REST(t *testing.T) {
	var gotMethod, gotPath string
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-d", "runId": "run-d"})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "staging", "--ref", "acme1234", "--yes", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, http.MethodDelete, gotMethod)
	require.Equal(t, "/api/v1/projects/acme1234/branches/staging", gotPath)
}

func TestBranchDelete_RefusesMainClientSide(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API to delete main")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "main", "--ref", "acme1234", "--yes"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot delete the main branch")
}

func TestBranchDelete_AbortsWithoutConfirm(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API when the user declines")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "staging", "--ref", "acme1234"})
	cmd.SetIn(strings.NewReader("n\n")) // decline
	require.NoError(t, cmd.Execute())
}

func TestBranchDelete_403DefaultBranchSurfacesAPIError(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "forbidden", "error_description": "Cannot delete the default branch.",
			"status": 403, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"delete", "staging", "--ref", "acme1234", "--yes"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "forbidden", apiErr.Code)
}

func TestBranchCreate_404SurfacesAPIError(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "not_found", "error_description": "Project not found",
			"status": 404, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "nope"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "not_found", apiErr.Code)
}

func TestBranchCreate_403ForbiddenSurfacesAPIError(t *testing.T) {
	// Free tier → the Studio service returns 403 forbidden; the CLI must
	// surface it as a clean APIError (the tier-gate is server-side).
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "forbidden", "error_description": "Branches require a Pro plan or higher.",
			"status": 403, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "staging", "--ref", "acme1234"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "forbidden", apiErr.Code)
}
