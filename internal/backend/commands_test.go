package backend

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// noopResolvers builds a Resolvers whose accessors return nil — enough to
// construct the command tree (constructors don't touch the clients until
// RunE fires), which is all the structural tests below need.
func noopResolvers() Resolvers {
	return Resolvers{Auth: nil, Studio: nil, Endpoints: nil}
}

// TestCommands_FlatSurface pins the CLI-1 flat redesign: the backend
// lifecycle verbs are returned as TOP-LEVEL commands (no `backend`
// parent), and the renamed/removed verbs are gone.
func TestCommands_FlatSurface(t *testing.T) {
	cmds := Commands(noopResolvers())

	got := map[string]bool{}
	for _, c := range cmds {
		// Use may be "rollback <version-sha>" — take the first word.
		name := c.Name()
		got[name] = true
	}

	// Present, top-level, flat. Deploy is GitHub-native (`git push`), so the
	// CLI keeps local dev + observation/control verbs only.
	for _, want := range []string{"serve", "list", "rollback", "status", "types"} {
		require.True(t, got[want], "expected top-level command %q in flat surface, got %v", want, keys(got))
	}

	// Removed: no init/enable/disable (backend is the default — the CLI never
	// enables or tears down), no `dev` (→ serve), no `backend` parent. `merge`
	// stays retired (the go-git merge verb is gone). push/pull are BACK as
	// mode-aware verbs (github: git push/pull; platform: tarball/bundle) — see
	// TestCommands_IncludesGitNativeVerbs.
	for _, gone := range []string{"init", "deploy", "dev", "disable", "enable", "backend", "config", "merge"} {
		require.False(t, got[gone], "command %q must NOT exist after the flat redesign", gone)
	}
}

// TestCommands_IncludesGitNativeVerbs pins that the mode-aware deploy verbs
// (push/pull/clone) are registered as top-level commands. They route by the
// linked project's mode: github (git push/pull/clone) or platform (tarball /
// bundle). Constructing with a zero-value Resolvers must not panic — the REST
// accessor is only called at RunE time.
func TestCommands_IncludesGitNativeVerbs(t *testing.T) {
	cmds := Commands(Resolvers{})
	have := map[string]bool{}
	for _, c := range cmds {
		have[c.Name()] = true
	}
	for _, want := range []string{"push", "pull", "clone"} {
		if !have[want] {
			t.Fatalf("Commands() missing %q; have %v", want, have)
		}
	}
}

// TestCommands_NoBackendParent guards against a regression to the old
// nested shape (a `backend` parent group with the lifecycle verbs as its
// children): none of the returned commands is a `backend` group.
func TestCommands_NoBackendParent(t *testing.T) {
	for _, c := range Commands(noopResolvers()) {
		require.NotEqual(t, "backend", c.Name(), "the `backend` parent command must be gone (palbase IS the backend CLI)")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// chdirLinked makes a temp dir the cwd, seeds .palbase/config.json so
// resolveOrLinkRef finds the ref without prompting, and restores cwd.
func chdirLinked(t *testing.T, ref string) {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })
	require.NoError(t, os.MkdirAll(".palbase", 0o755))
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: ref, DefaultEnv: "main"}))
}
