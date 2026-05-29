package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

// restAgainst spins an httptest server and returns a real REST transport
// pointed at it (mirrors branch_test.go).
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

func TestMigrateAllTenants_POSTsModule(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "reconcile-migrations-palnotify-1"})
	})

	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"migrate-all-tenants", "--module", "palnotify"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/admin/migrate-tenants", gotPath)
	require.Equal(t, "palnotify", gotBody["module"])
	require.Contains(t, out.String(), "reconcile-migrations-palnotify-1")
}

func TestMigrateAllTenants_JSONOutput(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "reconcile-migrations-paldocs-2"})
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"migrate-all-tenants", "--module", "paldocs", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), `"workflowId": "reconcile-migrations-paldocs-2"`)
}

func TestMigrateAllTenants_InvalidModuleRejectedClientSide(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API for an invalid module")
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"migrate-all-tenants", "--module", "bogus"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --module")
	// the error lists the valid modules so the user can self-correct
	require.Contains(t, err.Error(), "palnotify")
	require.Contains(t, err.Error(), "palauth")
}

func TestMigrateAllTenants_ModuleRequired(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API when --module is missing")
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"migrate-all-tenants"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "module")
}

func TestMigrateAllTenants_403SurfacesAPIError(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "forbidden", "error_description": "Platform-admin access required.",
			"status": 403, "request_id": "req_e",
		})
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"migrate-all-tenants", "--module", "palnotify"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "forbidden", apiErr.Code)
}
