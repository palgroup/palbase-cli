package db

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `palbase db verify` answers with diff(1)'s codes: 0 the database is what its
// applied-schema record says, 1 it is not, 2 there was nothing to compare. A
// script that reads "could not check" as "fine" is what the split is for.
func TestVerifyExitsWithWhatTheStackFound(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int // 0 = no error
		wants      []string
	}{
		{"match", `{"status":"match","differences":[],"record":{"id":3,"applied_at":"2026-09-25T10:00:00Z","hash":"ab","release_digest":"sha256:0123456789abcdef"}}`,
			0, []string{"✓", "record 3", "0123456789ab"}},
		{"drift", `{"status":"drift","differences":["public.notes.by_hand: column present catalog=true view=false"],"record":{"id":3,"applied_at":"2026-09-25T10:00:00Z","hash":"ab"}}`,
			exitVerifyDrift, []string{"✗", "by_hand"}},
		{"unrecorded", `{"status":"unrecorded","reason":"no push has been recorded on this database yet","differences":[]}`,
			exitVerifyUnverifiable, []string{"nothing to compare", "no push has been recorded"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotMethod string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			scratchCheckout(t)
			runningStackAt(t, srv.URL)

			out, _, err := run(t, "verify")
			if gotPath != "/v1/management/schema/verify" || gotMethod != http.MethodGet {
				t.Fatalf("asked %s %s", gotMethod, gotPath)
			}
			code := 0
			if err != nil {
				var ve verifyExit
				if !errors.As(err, &ve) {
					t.Fatalf("an exit that is not a chosen status: %v", err)
				}
				code = ve.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("exit code %d, want %d:\n%s", code, tc.code, out)
			}
			for _, want := range tc.wants {
				if !strings.Contains(out, want) {
					t.Errorf("the answer does not say %q:\n%s", want, out)
				}
			}
		})
	}
}

// A stack that cannot be asked is "could not verify" (2), never "drift" (1).
func TestVerifyThatCannotAskIsNotDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	scratchCheckout(t)
	runningStackAt(t, srv.URL)

	_, _, err := run(t, "verify")
	var ve verifyExit
	if !errors.As(err, &ve) || ve.ExitCode() != exitVerifyUnverifiable {
		t.Fatalf("a failed request exited %v, want code %d", err, exitVerifyUnverifiable)
	}
}
