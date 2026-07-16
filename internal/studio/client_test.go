package studio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClient_SignsBoundTokenWithDPoP(t *testing.T) {
	var gotMethod, gotURL, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "DPoP access-token", r.Header.Get("Authorization"))
		require.Equal(t, "signed-proof", r.Header.Get("DPoP"))
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{"ok": true}}}})
	}))
	defer srv.Close()

	c := New(
		srv.URL,
		func(context.Context) (string, error) { return "access-token", nil },
		func(_ context.Context, method, rawURL, token string) (string, error) {
			gotMethod, gotURL, gotToken = method, rawURL, token
			return "signed-proof", nil
		},
	)
	var out map[string]any
	require.NoError(t, c.Query(context.Background(), "backend.status", map[string]any{"ref": "envref"}, &out))
	require.Equal(t, http.MethodGet, gotMethod)
	require.True(t, strings.HasPrefix(gotURL, srv.URL+"/api/trpc/backend.status?input="), gotURL)
	require.Equal(t, "access-token", gotToken)
}

func TestClient_FailsClosedWithoutDPoPProofSigner(t *testing.T) {
	c := New("https://studio.example", func(context.Context) (string, error) { return "bound-token", nil }, nil)
	err := c.Query(context.Background(), "backend.status", nil, nil)
	require.ErrorContains(t, err, "dpop proof signer")
}

func TestClient_DebugLogNeverPrintsResponseBody(t *testing.T) {
	const secret = "pb_app1prod_cSECRETSECRETSECRET12"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", "req_debug_1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"data": map[string]any{"json": map[string]any{
				"api_key": secret,
			}}},
		})
	}))
	defer srv.Close()

	t.Setenv("PALBASE_DEBUG_TRPC", "1")
	readEnd, writeEnd, err := os.Pipe()
	require.NoError(t, err)
	originalStderr := os.Stderr
	os.Stderr = writeEnd
	t.Cleanup(func() { os.Stderr = originalStderr })

	c := New(
		srv.URL,
		func(context.Context) (string, error) { return "access-token", nil },
		func(context.Context, string, string, string) (string, error) { return "proof", nil },
	)
	var out map[string]any
	require.NoError(t, c.Query(context.Background(), "apps.config", nil, &out))
	require.NoError(t, writeEnd.Close())
	os.Stderr = originalStderr
	debug, err := io.ReadAll(readEnd)
	require.NoError(t, err)
	require.NoError(t, readEnd.Close())

	require.NotContains(t, string(debug), secret)
	require.Contains(t, string(debug), "status=200")
	require.Contains(t, string(debug), "request_id=\"req_debug_1\"")
}

// CLI-14 regression: Studio routinely returns error envelopes whose
// `message` is empty (module-not-provisioned, rate-limit, some Zod
// surfaces). The old toError happily produced "studio: " with nothing
// after the colon, and the caller wrapped that into
// "auth.providers.list: studio: " so the user had no way to tell
// whether the upstream returned 404 / 500 / authn / nothing. toError
// now fills the trailing piece with the best available signal —
// cause → http status → tRPC code → a constant — so the user line
// always carries something actionable.
func TestTRPCError_NeverEmpty(t *testing.T) {
	cases := []struct {
		name   string
		body   trpcErrorBody
		wantSS []string // substrings the error MUST contain
		notSS  []string // substrings the error MUST NOT contain (regression markers)
	}{
		{
			name:   "message present — verbatim",
			body:   trpcErrorBody{Message: "row not found"},
			wantSS: []string{"row not found"},
			notSS:  []string{"empty error", "no error message"},
		},
		{
			name: "empty message falls back to data.cause",
			body: func() trpcErrorBody {
				b := trpcErrorBody{}
				b.Data.Cause = "module not provisioned"
				return b
			}(),
			wantSS: []string{"module not provisioned"},
			notSS:  []string{"studio: $", "empty error"},
		},
		{
			name: "empty message + no cause → http status surface",
			body: func() trpcErrorBody {
				b := trpcErrorBody{}
				b.Data.HTTPCode = 404
				return b
			}(),
			wantSS: []string{"HTTP 404"},
			notSS:  []string{"studio: \""},
		},
		{
			name:   "nothing at all → constant fallback (never bare ‘studio: ’)",
			body:   trpcErrorBody{},
			wantSS: []string{"empty error envelope"},
			notSS:  []string{},
		},
		{
			name: "data.code wins as prefix, message still readable",
			body: func() trpcErrorBody {
				b := trpcErrorBody{Message: "denied"}
				b.Data.Code = "FORBIDDEN"
				return b
			}(),
			wantSS: []string{"FORBIDDEN", "denied"},
			notSS:  []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.body.toError()
			if err == nil {
				t.Fatalf("toError returned nil")
			}
			got := err.Error()
			// Hard invariant: never end with the bare prefix.
			if strings.HasSuffix(got, "studio: ") || strings.HasSuffix(got, "studio: \"\"") {
				t.Fatalf("trailing-empty regression: %q", got)
			}
			for _, want := range tc.wantSS {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in %q", want, got)
				}
			}
			for _, no := range tc.notSS {
				if strings.Contains(got, no) {
					t.Errorf("unexpected substring %q in %q", no, got)
				}
			}
		})
	}
}

// parseTRPCError must handle both single-object and batch-array shapes —
// Studio's tRPC mounts respond with one or the other depending on the
// path, and silently swallowing the batch shape used to mask the real
// reason inside an opaque "studio http 500: [..." line.
func TestParseTRPCError_BatchAndSingle(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // substring
	}{
		{
			name: "single",
			body: `{"error":{"message":"not allowed","data":{"code":"FORBIDDEN"}}}`,
			want: "FORBIDDEN: not allowed",
		},
		{
			name: "batch — first entry's error wins",
			body: `[{"error":{"message":"row not found","data":{"code":"NOT_FOUND"}}}]`,
			want: "NOT_FOUND: row not found",
		},
		{
			name: "neither shape → fall back to HTTP status + body",
			body: `not json`,
			want: "studio http 500",
		},
		{
			// Regression: Studio uses superjson, which nests the real
			// fields under `error.json`. The decoder MUST reach through
			// it and surface "UNAUTHORIZED" instead of "empty error
			// envelope". This is the exact body that 401'd `palbase push`.
			name: "superjson-wrapped single error surfaces real message",
			body: `{"error":{"json":{"message":"UNAUTHORIZED","code":-32001,"data":{"code":"UNAUTHORIZED","httpStatus":401,"path":"backend.deploy"}}}}`,
			want: "UNAUTHORIZED: UNAUTHORIZED",
		},
		{
			name: "superjson-wrapped batch error surfaces real message",
			body: `[{"error":{"json":{"message":"row not found","data":{"code":"NOT_FOUND"}}}}]`,
			want: "NOT_FOUND: row not found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := parseTRPCError([]byte(tc.body), 500)
			if err == nil {
				t.Fatalf("nil error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in %q", tc.want, err.Error())
			}
		})
	}
}
