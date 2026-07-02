package groups

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// studioAgainst spins an httptest server and returns a Studio backed by it
// (mirrors apps_test.go).
func studioAgainst(t *testing.T, h http.HandlerFunc) Studio {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	})
}

func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// TestGroupsList_Query pins the groups.mine wire: GET, no meaningful input,
// rows rendered without error (table + --json paths).
func TestGroupsList_Query(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/trpc/groups.mine", r.URL.Path)
		trpcOK(w, []map[string]any{
			{"id": "grp_1", "name": "My Product", "plan": "free"},
		})
	})
	for _, args := range [][]string{{"list"}, {"list", "--json"}} {
		cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute())
	}
}

// TestGroupsEnvs_Query pins groups.environments: the grpId travels as input,
// nullable preset/display-name decode.
func TestGroupsEnvs_Query(t *testing.T) {
	var gotInput string
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/groups.environments", r.URL.Path)
		gotInput = r.URL.Query().Get("input")
		trpcOK(w, []map[string]any{
			{"ref": "todoappm8p6z", "env_preset": "production", "env_display_name": nil, "status": "active"},
			{"ref": "todoappdev", "env_preset": nil, "env_display_name": "Dev", "status": "active"},
		})
	})
	cmd := Cmd(Resolvers{Studio: func() Studio { return c }})
	cmd.SetArgs([]string{"envs", "grp_1"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, gotInput, `"grpId":"grp_1"`)
}
