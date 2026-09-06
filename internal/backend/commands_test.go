package backend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// noopResolvers builds a Resolvers whose accessors return nil — enough to
// construct the command tree (constructors don't touch the clients until
// RunE fires), which is all the structural tests below need.
func noopResolvers() Resolvers {
	return Resolvers{}
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
	// CLI keeps the pre-deploy validator + observation/control verbs only.
	for _, want := range []string{"build", "deploys", "rollback", "status", "spec", "link", "unlink"} {
		require.True(t, got[want], "expected top-level command %q in flat surface, got %v", want, keys(got))
	}

	// Removed: no enable/disable (backend is the default — the CLI never
	// enables or tears down), no `serve`/`dev` (the local Node dev server is
	// gone — deploy to a dev Environment instead), no `backend` parent. `merge`
	// stays retired (the go-git merge verb is gone). push/pull are BACK as
	// mode-aware verbs (github: git push/pull; platform: tarball/bundle) — see
	// TestCommands_IncludesGitNativeVerbs. The CLI-audit renames retired
	// `list` (→ deploys), `pull-spec` (→ spec), `gen-types` (→ db types) and
	// `types` (→ an interim `web gen`, itself since RETIRED now that
	// @palbase/web owns its own codegen via palbe-gen, invoked by `web link`
	// and its predev/prebuild hooks — see web_link.go); there is NO top-level
	// `gen` group — client codegen is the SDKs' job.
	for _, gone := range []string{"deploy", "dev", "disable", "enable", "backend", "config", "merge", "list", "types", "gen-types", "pull-spec", "gen"} {
		require.False(t, got[gone], "command %q must NOT exist after the flat redesign", gone)
	}
}

// TestCommands_IncludesGitNativeVerbs pins that the provider-aware deploy verbs
// (push/pull/clone) are registered as top-level commands. They route by the
// project's repository_provider: github (git push/pull/clone) or palbase
// (tarball / bundle). Constructing with a zero-value Resolvers must not panic —
// the accessors are only called at RunE time.
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

// THE PLATFORM GROUPS ARE GONE, and their group tests went with them (T008).
//
// TestCommands_MacOSGroup, _AndroidGroup and _WebGroup each asserted a group
// existed and carried a `link` child. One `palbase link` reads what the checkout
// IS, so there is no group to assert — and a test kept alive against a surface
// that no longer ships is a test that measures nothing.
//
// What replaced them is stronger and lives in cmd/palbase:
// TestRetiredPlatformCommandsAreGone runs the REAL binary and demands cobra's
// "unknown command" — because a non-zero exit alone proved nothing. The first
// version of that test passed while every one of these commands was still
// registered, since they each failed for their own reasons anyway.
