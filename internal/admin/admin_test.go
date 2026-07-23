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

func TestSetModuleImage_POSTsModuleAndImage(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "set-module-image-palnotify-1"})
	})

	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"set-module-image", "--module", "palnotify", "--image", "registry.example/palnotify:abc123"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/admin/set-module-image", gotPath)
	require.Equal(t, "palnotify", gotBody["module"])
	require.Equal(t, "registry.example/palnotify:abc123", gotBody["image"])
	require.Contains(t, out.String(), "set-module-image-palnotify-1")
}

func TestSetModuleImage_JSONOutput(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "set-module-image-paldocs-2"})
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"set-module-image", "--module", "paldocs", "--image", "registry.example/paldocs:def456", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), `"workflowId": "set-module-image-paldocs-2"`)
}

func TestSetModuleImage_InvalidModuleRejectedClientSide(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API for an invalid module")
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"set-module-image", "--module", "bogus", "--image", "registry.example/bogus:1"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --module")
	// the error lists the valid modules so the user can self-correct
	require.Contains(t, err.Error(), "palnotify")
	require.Contains(t, err.Error(), "palauth")
}

func TestSetModuleImage_EmptyImageRejectedClientSide(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API when --image is empty")
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"set-module-image", "--module", "palnotify", "--image", ""})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "image is required")
}

func TestSetModuleImage_ModuleRequired(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API when --module is missing")
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"set-module-image", "--image", "registry.example/x:1"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "module")
}

func TestSetModuleImage_ImageRequired(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API when --image is missing")
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"set-module-image", "--module", "palnotify"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "image")
}

// TestSetModuleImage_AcceptsBackendRuntime locks the fix: set-module-image pins
// CHANNEL-only modules (no migrate-Job), so backend-runtime — the shared-isolate
// runtime image — must reach the API, not be rejected client-side. Mirrors the
// mgmt API's broader `moduleImageName` enum. Revert set-module-image to validate
// against `validModules` and this goes red (the pre-fix bug: could never pin it).
func TestSetModuleImage_AcceptsBackendRuntime(t *testing.T) {
	var gotBody map[string]any
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		okData(w, http.StatusAccepted, map[string]any{"workflowId": "set-module-image-backend-runtime-1"})
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"set-module-image", "--module", "backend-runtime", "--image", "registry.example/backend-runtime:sha-abc@sha256:def"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	require.Equal(t, "backend-runtime", gotBody["module"])
	require.Contains(t, out.String(), "set-module-image-backend-runtime-1")
}

// TestMigrateAllTenants_RejectsBackendRuntime locks the distinction: backend-runtime
// has NO migrate-Job, so migrate-all-tenants must reject it client-side (only
// image-pinning accepts it).
func TestMigrateAllTenants_RejectsBackendRuntime(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the API — backend-runtime has no migrations")
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"migrate-all-tenants", "--module", "backend-runtime"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid --module")
}

func TestSetModuleImage_403SurfacesAPIError(t *testing.T) {
	c, _ := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "forbidden", "error_description": "Platform-admin access required.",
			"status": 403, "request_id": "req_e",
		})
	})
	cmd := NewCommand(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"set-module-image", "--module", "palnotify", "--image", "registry.example/palnotify:1"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "forbidden", apiErr.Code)
}
