package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/stretchr/testify/require"
)

// newTestKey mints a real keyring DPoP key so the transport signs with
// the same path production uses. No mock crypto.
func newTestKey(t *testing.T) *auth.DPoPKey {
	t.Helper()
	k, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return k
}

func TestREST_Do_SetsDPoPAuthAndProof(t *testing.T) {
	key := newTestKey(t)
	const pat = "pat_test_abc123"

	var gotAuth, gotProof, gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotProof = r.Header.Get("DPoP")
		gotMethod = r.Method
		gotPath = r.URL.Path
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
		}
		// Success envelope: { data, request_id }.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       map[string]any{"ref": "abcd1234", "status": "ready"},
			"request_id": "req_xxx",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, key, pat)

	var out struct {
		Ref    string `json:"ref"`
		Status string `json:"status"`
	}
	err := c.Do(context.Background(), http.MethodPost, "/api/v1/projects",
		map[string]any{"ref": "abcd1234"}, &out)
	require.NoError(t, err)

	// Authorization carries the PAT as a DPoP-scheme credential (never Bearer).
	require.Equal(t, "DPoP "+pat, gotAuth)
	require.NotEmpty(t, gotProof, "a DPoP proof header must be present")

	// Proof must be a compact JWS bound to this request + token.
	parts := strings.Split(gotProof, ".")
	require.Len(t, parts, 3, "DPoP proof is a compact JWT")
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var proof map[string]any
	require.NoError(t, json.Unmarshal(payloadBytes, &proof))
	require.Equal(t, "POST", proof["htm"])
	require.Equal(t, srv.URL+"/api/v1/projects", proof["htu"])
	// ath binds the proof to the presented PAT.
	require.NotEmpty(t, proof["ath"], "proof must bind to the access token via ath")

	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/projects", gotPath)
	require.Equal(t, "abcd1234", gotBody["ref"])

	// Envelope unwrapped into out.
	require.Equal(t, "abcd1234", out.Ref)
	require.Equal(t, "ready", out.Status)
}

func TestREST_Do_GETHasNoBody(t *testing.T) {
	key := newTestKey(t)
	var hadBody bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1)
		n, _ := r.Body.Read(buf)
		hadBody = n > 0
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       []any{},
			"request_id": "req_x",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, key, "pat_x")
	var out []any
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/api/v1/projects", nil, &out))
	require.False(t, hadBody, "GET with nil body must not send a request body")
}

func TestREST_Do_MapsErrorEnvelope(t *testing.T) {
	key := newTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "insufficient_scope",
			"error_description": "This token is missing the required scope: projects:write",
			"status":            403,
			"request_id":        "req_err",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, key, "pat_x")
	err := c.Do(context.Background(), http.MethodPost, "/api/v1/projects", map[string]any{}, nil)
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr), "error must carry the parsed envelope")
	require.Equal(t, "insufficient_scope", apiErr.Code)
	require.Equal(t, 403, apiErr.Status)
	require.Equal(t, "req_err", apiErr.RequestID)
	require.Contains(t, apiErr.Error(), "insufficient_scope")
	require.Contains(t, apiErr.Error(), "projects:write")
}

func TestREST_Do_NonJSONErrorBody(t *testing.T) {
	// A 502 from an upstream proxy (e.g. Kong) may not be a JSON envelope.
	// The transport must still surface a non-nil error with the status.
	key := newTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	defer srv.Close()

	c := New(srv.URL, key, "pat_x")
	err := c.Do(context.Background(), http.MethodGet, "/api/v1/projects", nil, nil)
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, http.StatusBadGateway, apiErr.Status)
}

func TestREST_Do_MissingPATFailsClosed(t *testing.T) {
	// No PAT → the transport must not attempt the call (it would 401).
	key := newTestKey(t)
	c := New("https://api.dev.palbase.studio", key, "")
	err := c.Do(context.Background(), http.MethodGet, "/api/v1/projects", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "PALBASE_ACCESS_TOKEN")
}

func TestREST_Do_RequiresKey(t *testing.T) {
	c := New("https://api.dev.palbase.studio", nil, "pat_x")
	err := c.Do(context.Background(), http.MethodGet, "/api/v1/projects", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "dpop key")
}

func TestREST_Do_NullDataOk(t *testing.T) {
	// DELETE-style responses with no meaningful data must not error.
	key := newTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "request_id": "req_x"})
	}))
	defer srv.Close()
	c := New(srv.URL, key, "pat_x")
	require.NoError(t, c.Do(context.Background(), http.MethodDelete, "/api/v1/projects/x/api-keys/1", nil, nil))
}
