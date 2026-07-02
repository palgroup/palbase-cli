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
	for _, want := range []string{"serve", "deploys", "rollback", "status", "spec", "web", "ios"} {
		require.True(t, got[want], "expected top-level command %q in flat surface, got %v", want, keys(got))
	}

	// Removed: no enable/disable (backend is the default — the CLI never
	// enables or tears down), no `dev` (→ serve), no `backend` parent. `merge`
	// stays retired (the go-git merge verb is gone). push/pull are BACK as
	// mode-aware verbs (github: git push/pull; platform: tarball/bundle) — see
	// TestCommands_IncludesGitNativeVerbs. The CLI-audit renames retired
	// `list` (→ deploys), `pull-spec` (→ spec), `gen-types` (→ db types) and
	// `types` (→ web gen, interim until @palbase/web owns its codegen); there
	// is NO top-level `gen` group — client codegen is the SDKs' job.
	for _, gone := range []string{"deploy", "dev", "disable", "enable", "backend", "config", "merge", "list", "types", "gen-types", "pull-spec", "gen"} {
		require.False(t, got[gone], "command %q must NOT exist after the flat redesign", gone)
	}
}

// TestCommands_WebGroup pins the `web` group's children: link/unlink ONLY —
// the CLI wires and fetches artifacts, it does NOT generate the client.
// Client codegen is the SDKs' job: @palbase/web's `palbe-gen` for web (from
// the committed Palbase/ inputs), the PalbaseCodegen SPM plugin for iOS
// (from `palbase spec` output).
func TestCommands_WebGroup(t *testing.T) {
	for _, c := range Commands(noopResolvers()) {
		if c.Name() != "web" {
			continue
		}
		var names []string
		for _, sub := range c.Commands() {
			names = append(names, sub.Name())
		}
		require.ElementsMatch(t, []string{"link", "unlink"}, names)
		return
	}
	t.Fatal("web group not found in flat surface")
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

// TestIsInteractive_DevNullIsNotATTY pins the /dev/null trap: it IS a char
// device, so the old ModeCharDevice check treated `palbase ios link
// </dev/null` as interactive and opened a 66-line picker that died on EOF.
// term.IsTerminal must say false for it. (Mutation-evident: revert
// isInteractive to the ModeCharDevice check and this fails.)
func TestIsInteractive_DevNullIsNotATTY(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer devnull.Close()
	orig := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() { os.Stdin = orig })
	require.False(t, isInteractive(), "/dev/null stdin must NOT count as interactive")
}
