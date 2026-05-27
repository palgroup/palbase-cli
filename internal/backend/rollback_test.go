package backend

import "testing"

// resolveActiveBranch picks the rollback target branch: an explicit --branch
// flag wins; "main" (flag or config) maps to "" so the server resolves the
// default branch (back-compat). With no flag, it falls back to the locally
// active branch (ProjectConfig.DefaultEnv) — exercised in the live smoke,
// since it reads the on-disk config. These cases cover the flag/main logic
// that does NOT touch the filesystem.
func TestResolveActiveBranch_flag(t *testing.T) {
	cases := []struct {
		name string
		flag string
		want string
	}{
		{"explicit branch wins", "staging", "staging"},
		{"explicit main maps to empty (default branch)", "main", ""},
		{"explicit qa branch", "qa", "qa"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveActiveBranch(c.flag); got != c.want {
				t.Fatalf("resolveActiveBranch(%q) = %q, want %q", c.flag, got, c.want)
			}
		})
	}
}
