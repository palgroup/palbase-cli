package backend

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A stack that does not answer must not be spelled as a ceiling.
//
// `centauri` answered 503 at every path because its runtime was down. The read
// treated that exactly like a 404 — "answered, named no ceiling" — the caller
// turned it into the documented default of 2, and the push refused with "the
// stack's runtime serves at most 2", a measurement nobody made, pointing at
// `palbase upgrade`, which refuses back. Measured live 2026-09-02.
func TestA5xxIsNotACeiling(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		serves, err := stackServesGeneration(t.Context(), Target{URL: srv.URL})
		srv.Close()
		if !errors.Is(err, errStackSilent) {
			t.Errorf("%d: want errStackSilent, got err=%v serves=%v", code, err, serves)
		}
		if serves != nil {
			t.Errorf("%d: a silent stack must name no ceiling, got %d", code, *serves)
		}
	}
}

// NEGATIVE CONTROL — the two states that ARE an answer keep their meaning, or
// the fix above would refuse every push to every image in the fleet today.
func TestAnAnsweringStackStillNamesNoCeiling(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want *int
	}{
		{"404 — nothing deployed yet", 404, "", nil},
		{"200 without the field — an older image", 200, `{"version":"1"}`, nil},
		{"200 with the field", 200, `{"serves_bundle_generation":3}`, intp(3)},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.code)
			_, _ = fmt.Fprint(w, c.body)
		}))
		serves, err := stackServesGeneration(t.Context(), Target{URL: srv.URL})
		srv.Close()
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		switch {
		case c.want == nil && serves != nil:
			t.Errorf("%s: want no ceiling, got %d", c.name, *serves)
		case c.want != nil && (serves == nil || *serves != *c.want):
			t.Errorf("%s: want %d, got %v", c.name, *c.want, serves)
		}
	}
}

func intp(v int) *int { return &v }
