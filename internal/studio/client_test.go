package studio

import (
	"strings"
	"testing"
)

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
			name: "message present — verbatim",
			body: trpcErrorBody{Message: "row not found"},
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
			name: "nothing at all → constant fallback (never bare ‘studio: ’)",
			body: trpcErrorBody{},
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
