package backend

import "testing"

// resolveDevProjectRef must prefer the branch endpoint_ref (what
// apikey.reveal returns and what Kong routes) over the bare linked ref,
// and fall back to the bare ref only when reveal was skipped or failed.
//
// Regression guard for the bare-ref-vs-endpoint_ref bug class: we use
// ref != endpointRef (test0r8q3 / test0r8q3m) so a fix that silently
// returns the bare ref would fail the first case. A ref where the two
// are equal would mask exactly this bug.
func TestResolveDevProjectRef(t *testing.T) {
	const (
		bareRef     = "test0r8q3"
		endpointRef = "test0r8q3m"
	)

	cases := []struct {
		name        string
		ref         string
		endpointRef string
		want        string
	}{
		{
			name:        "prefers endpoint_ref when reveal returns one",
			ref:         bareRef,
			endpointRef: endpointRef,
			want:        endpointRef,
		},
		{
			name:        "falls back to bare ref when reveal was skipped or failed",
			ref:         bareRef,
			endpointRef: "",
			want:        bareRef,
		},
		{
			name:        "local ref with no reveal stays local (dev launches, ctx errors clearly)",
			ref:         "local",
			endpointRef: "",
			want:        "local",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDevProjectRef(tc.ref, tc.endpointRef)
			if got != tc.want {
				t.Fatalf("resolveDevProjectRef(%q, %q) = %q, want %q",
					tc.ref, tc.endpointRef, got, tc.want)
			}
			// Negative: when an endpoint_ref is present, the bare ref must
			// never be the chosen value (the unroutable-URL regression).
			if tc.endpointRef != "" && got == tc.ref && tc.ref != tc.endpointRef {
				t.Fatalf("chose bare ref %q while endpoint_ref %q was available",
					tc.ref, tc.endpointRef)
			}
		})
	}
}
