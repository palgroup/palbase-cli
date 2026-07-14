package main

import "testing"

// canonicalStudioURL is what `palbase open` prints/opens. UAT CLI-011: from a
// production vs a staging context it must deep-link to THAT Environment's page,
// not the bare Studio root. Mutation guard: reverting canonicalStudioURL to
// `return root` (the pre-fix bug) makes the first two cases fail.
func TestCanonicalStudioURL(t *testing.T) {
	const root = "https://app.dev.palbase.studio"
	cases := []struct {
		name       string
		studioRoot string
		projectID  string
		ref        string
		want       string
	}{
		{
			name:       "production env deep-links to its page",
			studioRoot: root,
			projectID:  "proj_019f604c-3f8f-73bf-8479-1eb54016f794",
			ref:        "e2e140357m",
			want:       root + "/projects/proj_019f604c-3f8f-73bf-8479-1eb54016f794/environments/e2e140357m",
		},
		{
			name:       "staging env deep-links to a DIFFERENT page than production",
			studioRoot: root,
			projectID:  "proj_019f604c-3f8f-73bf-8479-1eb54016f794",
			ref:        "e2e140357s",
			want:       root + "/projects/proj_019f604c-3f8f-73bf-8479-1eb54016f794/environments/e2e140357s",
		},
		{
			name:       "no selection falls back to the Studio root",
			studioRoot: root,
			projectID:  "",
			ref:        "",
			want:       root,
		},
		{
			name:       "trailing slash on the root is normalised",
			studioRoot: root + "/",
			projectID:  "proj_x",
			ref:        "abcm",
			want:       root + "/projects/proj_x/environments/abcm",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalStudioURL(tc.studioRoot, tc.projectID, tc.ref)
			if got != tc.want {
				t.Fatalf("canonicalStudioURL(%q,%q,%q) = %q, want %q",
					tc.studioRoot, tc.projectID, tc.ref, got, tc.want)
			}
		})
	}
}
