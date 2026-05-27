package backend

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
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

	// Present, top-level, flat.
	for _, want := range []string{"pull", "push", "dev", "list", "rollback", "status", "types"} {
		require.True(t, got[want], "expected top-level command %q in flat surface, got %v", want, keys(got))
	}

	// Removed/renamed: no init/enable/disable (backend is the default — the
	// CLI never enables or tears down), no deploy (→ push), no `backend`
	// parent, no `config` (config-as-code folded into pull/push).
	for _, gone := range []string{"init", "deploy", "disable", "enable", "backend", "config"} {
		require.False(t, got[gone], "command %q must NOT exist after the flat redesign", gone)
	}
}

// TestCommands_NoBackendParent guards against a regression to the old
// `palbase backend X` shape: none of the returned commands is a `backend`
// group with children.
func TestCommands_NoBackendParent(t *testing.T) {
	for _, c := range Commands(noopResolvers()) {
		require.NotEqual(t, "backend", c.Name(), "the `backend` parent command must be gone (palbase IS the backend CLI)")
	}
}

// TestCommands_PushReplacesDeploy: the write verb is `push` and it carries
// the deploy flags (-m / --no-types) so the deploy→push rename preserved
// the flag surface.
func TestCommands_PushReplacesDeploy(t *testing.T) {
	var push *cobra.Command
	for _, c := range Commands(noopResolvers()) {
		if c.Name() == "push" {
			push = c
			break
		}
	}
	require.NotNil(t, push, "push command must exist")
	require.NotNil(t, push.Flags().Lookup("message"), "push must keep deploy's --message flag")
	require.NotNil(t, push.Flags().Lookup("no-types"), "push must keep deploy's --no-types flag")
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
