package apikey

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

func restAgainst(t *testing.T, h http.HandlerFunc) REST {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(srv.URL, key, "pat_test")
}

func okData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": "req_x"})
}

func TestApikeyList_REST(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/projects/abcd1234/api-keys", r.URL.Path)
		require.Empty(t, r.URL.Query().Get("reveal"), "list must not request reveal")
		okData(w, 200, []map[string]any{
			{"id": "k1", "name": "anon", "scope": "c", "lookup_prefix": "pb_abc", "is_default": true},
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"list", "abcd1234", "--json"})
	require.NoError(t, cmd.Execute())
}

func TestApikeyCreate_REST_201Plaintext(t *testing.T) {
	var body map[string]any
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v1/projects/abcd1234/api-keys", r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&body)
		okData(w, http.StatusCreated, map[string]any{
			"id": "k2", "endpoint_ref": "abcd1234", "name": "ci", "scope": "s",
			"is_default": false, "plaintext": "pb_abcd1234m_s_secret", "lookup_prefix": "pb_abcd",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--name", "ci", "--scope", "s", "--json"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, "ci", body["name"])
	require.Equal(t, "s", body["scope"])
}

func TestApikeyCreate_RejectsBadScope(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call API with an invalid scope")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "abcd1234", "--name", "x", "--scope", "service_role"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--scope")
}

func TestApikeyCreate_RequiresName(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call API without --name")
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"create", "abcd1234"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--name is required")
}

func TestApikeyReveal_REST_GatedQuery(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/projects/abcd1234/api-keys", r.URL.Path)
		require.Equal(t, "true", r.URL.Query().Get("reveal"), "reveal must set ?reveal=true")
		anon, svc := "pb_anon", "pb_svc"
		okData(w, 200, map[string]any{
			"anonKey": anon, "serviceRoleKey": svc, "keys": []any{},
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"reveal", "abcd1234", "--json"})
	require.NoError(t, cmd.Execute())
}

func TestApikeyRevoke_REST_Delete(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/api/v1/projects/abcd1234/api-keys/k9", r.URL.Path)
		okData(w, 200, map[string]any{"ok": true})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"revoke", "abcd1234", "k9", "--json"})
	require.NoError(t, cmd.Execute())
}

func TestApikeyRevoke_403SurfacesError(t *testing.T) {
	c := restAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "insufficient_scope", "error_description": "needs api-keys:write",
			"status": 403, "request_id": "req_e",
		})
	})
	cmd := Cmd(Resolvers{REST: func() REST { return c }})
	cmd.SetArgs([]string{"revoke", "abcd1234", "k9"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	var apiErr *transport.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 403, apiErr.Status)
}

var _ = context.Background
