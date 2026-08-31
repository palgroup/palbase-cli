package backend

import "testing"

// A LINKED CHECKOUT ALREADY NAMES ITS PROJECT — IN ITS ADDRESS.
//
// `palbase link` writes only a URL (`https://<ref>.<domain>`), because the
// address is all the other target-relative verbs need: push, status and deploys
// all talk to the stack directly. `upgrade` is different — it calls a CLOUD
// route that names the project — so it has to read the ref back out of the
// address rather than asking for a selection the user already made.
//
// Measured 2026-09-01: without this, `palbase upgrade` in a freshly linked
// checkout answered "no project selected — run `palbase start`", which is
// advice for a completely different situation and sends the reader nowhere.
func TestRefFromTargetURL(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"https://1jhp7jbrm.palbase.studio", "1jhp7jbrm"},
		{"https://1jhp7jbrm.palbase.studio/", "1jhp7jbrm"},
		{"https://abc12345m.dev.palbase.studio", "abc12345m"},
	} {
		if got := refFromTargetURL(tc.url); got != tc.want {
			t.Fatalf("refFromTargetURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// A LOCAL STACK IS NOT A CLOUD PROJECT, and guessing a ref out of `localhost`
// would send an upgrade at a project id that does not exist.
func TestRefFromTargetURLRefusesWhatIsNotARef(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1:54321",
		"http://localhost:9080",
		"https://api.palbase.studio",
		"",
		"not a url",
	} {
		if got := refFromTargetURL(u); got != "" {
			t.Fatalf("refFromTargetURL(%q) = %q, want empty", u, got)
		}
	}
}
