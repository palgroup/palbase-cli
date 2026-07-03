package groups

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestGroupsList_REST pins the GET /api/v1/groups wire: GET, no query, rows
// rendered without error (table + --json paths).
func TestGroupsList_REST(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/groups", r.URL.Path)
		okData(w, http.StatusOK, []map[string]any{
			{"id": "grp_1", "name": "My Product", "plan": "free"},
		})
	})
	for _, args := range [][]string{{"list"}, {"list", "--json"}} {
		cmd := Cmd(Resolvers{REST: func() REST { return c }})
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute())
	}
}

// TestGroupsEnvs_REST pins GET /api/v1/groups/{groupId}/environments: the group
// id travels in the PATH, nullable preset/display-name decode.
func TestGroupsEnvs_REST(t *testing.T) {
	var gotPath string
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodGet, r.Method)
		okData(w, http.StatusOK, []map[string]any{
			{"ref": "todoappm8p6z", "env_preset": "production", "env_display_name": nil, "status": "active"},
			{"ref": "todoappdev", "env_preset": nil, "env_display_name": "Dev", "status": "active"},
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"envs", "grp_1"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "/api/v1/groups/grp_1/environments", gotPath)
}
