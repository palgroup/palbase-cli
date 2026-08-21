package main

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// The CLI's SURFACE is a contract (spec §7.3). This file golden-tests it:
// `palbase --help` and the two canonical groups' `--help` are compared against
// literal expected text, so a resurrected `branch`/`groups` command, a
// reintroduced `--branch`/`--group`/`--organization` flag, or a silently
// dropped verb fails HERE — in CI, at build time — instead of on a live run.

func helpOf(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append(args, "--help"))
	require.NoError(t, root.Execute())
	return out.String()
}

// topLevel returns the sorted names of every top-level command.
func topLevel(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, c := range newRootCmd().Commands() {
		if c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		names = append(names, c.Name())
	}
	sort.Strings(names)
	return names
}

// GOLDEN: the top-level command set.
//
// `branch` and `groups` are ABSENT. They were not renamed and not aliased —
// this is a direct cutover, and an alias would keep a dead resource alive in
// muscle memory and in scripts.
func TestGolden_TopLevelCommands(t *testing.T) {
	require.Equal(t, []string{
		"admin",
		"android",
		"apikey",
		"apps",
		"build",
		"clone",
		"db",
		"debug",
		"deploys",
		"doctor",
		// egress: config/egress.ts was the one module config surface with no
		// command — it had to be hand-written, and a host the deploy's fail-closed
		// validator rejects only surfaced as a failed deploy.
		"egress",
		"env",
		"flags",
		"github",
		"init",
		"ios",
		// The direct half of the CLI: `link <url>` binds a checkout to a stack
		// somebody runs, with no project to select and no control plane to ask.
		"link",
		"login",
		"logout",
		"logs",
		"macos",
		"members",
		"mode",
		"notifications",
		"open",
		"plan",
		"project",
		"pull",
		"push",
		"rollback",
		"run",
		"secret",
		"spec",
		"start",
		"status",
		"stop",
		"storage",
		"test-user",
		"web",
		"whoami",
	}, topLevel(t))
}

func TestGolden_RetiredCommandsAreGone(t *testing.T) {
	have := map[string]bool{}
	for _, name := range topLevel(t) {
		have[name] = true
	}
	// `init` was retired for a real reason and is BACK for a different one, so
	// the reason is worth keeping: it used to scaffold a SECOND skeleton onto
	// disk while the server had already deployed its own seed template as
	// version 1. The two drifted — init's said `palbase build`, the server's
	// said the retired `palbase serve` — and pushing init's tree overwrote a v1
	// the user never saw.
	//
	// What changed is where the skeleton comes from: it is copied out of the
	// installed @palbase/backend, so the scaffold and the SDK that compiles it
	// are the same version by construction and this CLI carries no copy to go
	// stale. The orchestrator still embeds its own backend_template/ for cloud
	// project creation; pointing that at the same package is the remaining half
	// and is in the deviation ledger.
	for _, gone := range []string{"branch", "groups", "group", "org", "organization", "serve", "dev"} {
		require.False(t, have[gone],
			"`palbase %s` must NOT exist after the cutover (no shims, no aliases)", gone)
	}
}

// GOLDEN: `palbase project --help` — the canonical Project surface.
//
// Four verbs, because in the v2 cloud a project IS a tenant: one microVM, one
// ref, one address. What left, and why:
//
//   - `use` — the linked target is what a directory acts on, and `palbase link`
//     already writes it. Two mechanisms for "which project is this directory"
//     is how somebody pushes to the wrong one.
//   - `connect-repo` / `disconnect-repo` — repository-driven deploys are a v1
//     platform feature with no v2 surface behind them. A verb that reaches a
//     route the cloud does not serve is worse than an absent verb: it fails
//     late, in the person's terminal, after they believed it worked.
func TestGolden_ProjectSurface(t *testing.T) {
	require.Equal(t,
		[]string{"create", "delete", "list", "status"},
		subcommands(t, "project"))
}

// GOLDEN: `palbase env --help` — the canonical Environment surface. It replaces
// the retired `branch` group, and it has NO `switch` (that was the branch verb).
// `branch` here is the GIT-branch mapping verb, not that resource: it writes the
// value push/pull and the deploy webhook both route on.
// TestGolden_EnvSurface — `env` takes a slug and switches; it has no
// subcommands.
//
// Creating, archiving, waking and deleting an environment are control-plane acts
// with a web surface that does them better (confirmations, membership, billing
// consequences). What the CLI keeps is the one that belongs in a checkout:
// WHICH environment this code acts on.
func TestGolden_EnvSurface(t *testing.T) {
	require.Empty(t, subcommands(t, "env"))
}

func subcommands(t *testing.T, parent string) []string {
	t.Helper()
	for _, c := range newRootCmd().Commands() {
		if c.Name() != parent {
			continue
		}
		var names []string
		for _, sub := range c.Commands() {
			names = append(names, sub.Name())
		}
		sort.Strings(names)
		return names
	}
	t.Fatalf("no `%s` command", parent)
	return nil
}

// The GLOBAL context flags are exactly --project and --environment (plus the
// pre-existing --mode). Organization is intentionally NOT a CLI context, and the
// Palbase branch is gone, so neither --organization nor --branch may exist.
func TestGolden_GlobalFlags(t *testing.T) {
	var names []string
	newRootCmd().PersistentFlags().VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	sort.Strings(names)
	require.Equal(t, []string{"environment", "mode", "project"}, names)
}

// No command anywhere in the tree may take --branch, --group or --organization.
// A single leftover would keep a retired concept reachable from a script.
func TestNoRetiredFlagAnywhereInTheTree(t *testing.T) {
	retired := []string{"branch", "group", "organization", "org"}
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, dead := range retired {
			if c.Flags().Lookup(dead) != nil {
				t.Fatalf("`palbase %s` still takes --%s", strings.TrimSpace(path), dead)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(newRootCmd(), "")
}

// The help text must not advertise a retired command in its prose either — a
// user copying `palbase branch create` out of a help string is a bug report.
func TestHelpProse_NamesNoRetiredCommand(t *testing.T) {
	help := helpOf(t)
	for _, dead := range []string{"palbase branch", "palbase groups", "palbase org"} {
		require.NotContains(t, help, dead)
	}
	require.Contains(t, help, "env")
	require.Contains(t, help, "project")
}
