package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestKey mints a real keyring DPoP key so the transport signs with
// the same path production uses. No mock crypto.
// Every request carries the session token as a plain Bearer credential, and
// nothing else: one scheme, one place, so "how does this CLI authenticate" has
// exactly one answer.
func TestREST_Do_SendsBearerToken(t *testing.T) {
	var gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}, "request_id": "req_x"})
	}))
	defer srv.Close()

	c := New(srv.URL, "tok_session")
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/me", nil, nil))
	require.Equal(t, "Bearer tok_session", gotAuth)
	require.Equal(t, "application/json", gotAccept)
}

// With no token the request never leaves: going out anonymous would earn a 401
// that reads like a server problem rather than the truth, which is that nobody
// signed in.
func TestREST_Do_WithoutATokenFailsClosed(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	err := c.Do(context.Background(), http.MethodGet, "/v1/cloud/me", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase login")
	require.False(t, reached, "an unauthenticated request must not reach the server")
}

// The control plane answers with the VALUE, not a {"data": …} wrapper.
//
// This is the regression that shipped a lie: with a wrapper-shaped decoder,
// `project create` parsed the response, found no `data`, decoded nothing, and
// printed "Created  (, cell )" — a success line about a project it had never
// read. A list came off worse and at least failed loudly.
func TestREST_Do_DecodesTheValueNotAWrapper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/projects") && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"ref":"aaa","phase":"Running"}]`))
			return
		}
		_, _ = w.Write([]byte(`{"ref":"bbb","slot":7,"cell":"pbc-cell-01","phase":"Running"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	var list []struct {
		Ref   string `json:"ref"`
		Phase string `json:"phase"`
	}
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects", nil, &list))
	require.Len(t, list, 1)
	require.Equal(t, "aaa", list[0].Ref)

	var one struct {
		Ref  string `json:"ref"`
		Slot int    `json:"slot"`
	}
	require.NoError(t, c.Do(context.Background(), http.MethodPost, "/v1/cloud/projects", nil, &one))
	require.Equal(t, "bbb", one.Ref, "an object response must reach the caller, not vanish into a wrapper")
	require.Equal(t, 7, one.Slot)
}

func TestREST_Do_GETHasNoBody(t *testing.T) {
	var hadBody bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1)
		n, _ := r.Body.Read(buf)
		hadBody = n > 0
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok_x")
	var out []any
	require.NoError(t, c.Do(context.Background(), http.MethodGet, "/v1/cloud/projects", nil, &out))
	require.False(t, hadBody, "GET with nil body must not send a request body")
}

func TestREST_Do_MapsErrorEnvelope(t *testing.T) {
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

	c := New(srv.URL, "pat_x")
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	defer srv.Close()

	c := New(srv.URL, "pat_x")
	err := c.Do(context.Background(), http.MethodGet, "/api/v1/projects", nil, nil)
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, http.StatusBadGateway, apiErr.Status)
}

func TestREST_Do_MissingPATFailsClosed(t *testing.T) {
	// No PAT → the transport must not attempt the call (it would 401).
	c := New("https://api.dev.palbase.studio", "")
	err := c.Do(context.Background(), http.MethodGet, "/api/v1/projects", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "PALBASE_ACCESS_TOKEN")
}

func TestREST_Do_NullDataOk(t *testing.T) {
	// DELETE-style responses with no meaningful data must not error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`null`))
	}))
	defer srv.Close()
	c := New(srv.URL, "pat_x")
	require.NoError(t, c.Do(context.Background(), http.MethodDelete, "/api/v1/projects/x/api-keys/1", nil, nil))
}

// THE REASON IS IN `fields`, AND IT USED TO BE THROWN AWAY.
//
// A validation refusal answers `{"error":"bad_request","error_description":"Bad
// request","fields":[…]}` — the description is a CATEGORY and the fields are the
// SENTENCE. Rendering only the description produced `bad_request (400): Bad
// request`, which says nothing a person can act on. Measured 2026-08-24: a push
// refused because the project's own tests failed reported exactly that, and the
// reason had to be read out of the tenant's log instead.
func TestAPIError_RendersTheFieldDetail(t *testing.T) {
	err := &APIError{
		Code: "bad_request", Status: 400, Description: "Bad request",
		Fields: []APIErrorField{
			{Field: "artifact", Message: "push refused (tests_failed) — the previous release keeps serving"},
		},
	}
	got := err.Error()
	if !strings.Contains(got, "tests_failed") {
		t.Fatalf("the reason is missing:\n%s", got)
	}
	if !strings.Contains(got, "artifact") {
		t.Fatalf("the field name is missing:\n%s", got)
	}
}

// AND THE FIELDS MUST SURVIVE THE PARSE. Rendering them is half the job; the
// other half is reading them off the wire, and only a real response proves it.
func TestREST_Do_ParsesTheFieldDetailOffTheWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "bad_request", "error_description": "Bad request", "status": 400,
			"request_id": "req_x",
			"fields": []map[string]string{
				{"field": "artifact", "message": "push refused (tests_failed)"},
			},
		})
	}))
	defer srv.Close()

	err := New(srv.URL, "tok").Do(context.Background(), http.MethodPost, "/v1/cloud/projects/x/push", map[string]any{}, nil)
	require.Error(t, err)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Len(t, apiErr.Fields, 1)
	require.Equal(t, "artifact", apiErr.Fields[0].Field)
	require.Contains(t, err.Error(), "tests_failed")
}

// AND THE OTHER SHAPE. The control plane's SDK wraps a data-first error, so the
// same detail lands under `data.fields`. Reading only the flat one dropped the
// reason for every refusal that came from the plane rather than from a tenant —
// which is exactly the case that sent somebody to the tenant's log.
func TestREST_Do_ParsesTheFieldDetailNestedUnderData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "bad_request", "error_description": "Bad request", "status": 400,
			"request_id": "req_x",
			"data": map[string]any{
				"fields": []map[string]string{
					{"field": "artifact", "message": "the body is not a gzip stream: unexpected EOF"},
				},
			},
		})
	}))
	defer srv.Close()

	err := New(srv.URL, "tok").Do(context.Background(), http.MethodPost, "/v1/cloud/projects/x/push", map[string]any{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a gzip stream")
}

// A refusal with no fields must read exactly as it always did — the change adds
// detail where there is detail, it does not decorate everything else.
func TestAPIError_WithoutFieldsIsUnchanged(t *testing.T) {
	err := &APIError{Code: "not_found", Status: 404, Description: "no such project"}
	if got, want := err.Error(), "not_found (404): no such project"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
