package merge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

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

func TestMergeOpen_REST(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "wf-1", "runId": "run-1"})
	})

	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"feat-x", "--into", "dev", "--ref", "acme1234", "--json"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/projects/acme1234/merge-requests", gotPath)
	require.Equal(t, "feat-x", gotBody["source"])
	require.Equal(t, "dev", gotBody["target"])
	require.NotEmpty(t, gotBody["title"]) // defaulted
}

func TestMergeOpen_requiresInto(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("REST should not be called when --into is missing")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"feat-x", "--ref", "acme1234"})
	require.Error(t, cmd.Execute())
}

func TestMergeOpen_rejectsSelfMerge(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("REST should not be called for a self-merge")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"dev", "--into", "dev", "--ref", "acme1234"})
	require.Error(t, cmd.Execute())
}

func TestMergeList_REST(t *testing.T) {
	var gotMethod, gotPath string
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		okData(w, http.StatusOK, []map[string]any{
			{"id": "mr_1", "source_branch": "feat-x", "target_branch": "dev", "state": "open", "title": "t", "created_at": "now"},
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"list", "--ref", "acme1234", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, http.MethodGet, gotMethod)
	require.Equal(t, "/api/v1/projects/acme1234/merge-requests", gotPath)
}
