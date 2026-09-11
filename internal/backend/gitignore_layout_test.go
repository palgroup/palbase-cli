package backend

// gitignore_layout_test.go — NOTHING THIS CLI WRITES INTO A CHECKOUT IS IGNORED.
//
// The ignore rules existed because machine-local files lived in the repository:
// `local.json` (the stack in front of you) and `plan.json` (this machine's
// measurement). Both moved out to `~/.palbase/checkouts/<hash>/`, so there is
// nothing left to keep out of git — and a rule for a file nobody writes any more
// is a rule that hides the next one by accident.
//
// The property this pins is NFR-001: every file under `palbase/` is trackable.
// An ignore rule here would silently keep a customer's generated client, or
// their environment's contract, out of the history that is supposed to carry it.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitignoreCarriesNoPalbasePath(t *testing.T) {
	for _, e := range generatedProjectPaths {
		if strings.Contains(strings.ToLower(e.path), "palbase") {
			t.Errorf("%q is still declared as an ignored path (%s) — everything this CLI "+
				"writes into a checkout is committed now", e.path, e.why)
		}
	}

	// And the scaffold it produces says the same thing.
	scaffold := gitignoreScaffold()
	for _, line := range strings.Split(scaffold, "\n") {
		if strings.Contains(strings.ToLower(line), "palbase") {
			t.Errorf("the scaffolded .gitignore still ignores %q:\n%s", line, scaffold)
		}
	}

	// NEGATIVE CONTROL: the ecosystem's own rules must survive. A scaffold that
	// ignores nothing is not the goal — a fresh web checkout without
	// `node_modules/` is how `git add -A` stages 500 dependency files.
	if !strings.Contains(scaffold, "node_modules/") {
		t.Errorf("the scaffold stopped ignoring node_modules:\n%s", scaffold)
	}
}

// AND THE RETIRED SWEEPER NEVER NAMES THE VISIBLE ROOT (D-008).
//
// `reapRetiredArtifacts` DELETES what it lists. On macOS and Windows `palbase`
// and `Palbase` are one directory, so a retired entry naming the visible root —
// in either spelling — deletes the directory this migration just created in the
// customer's repository.
func TestRetiredPathsNeverNameTheVisibleRoot(t *testing.T) {
	for _, e := range retiredProjectPaths {
		first, _, _ := strings.Cut(e.path, "/")
		if strings.EqualFold(first, RootDir()) && !strings.Contains(e.path, "/") {
			t.Errorf("%q is swept and differs from %q only by case — on a "+
				"case-insensitive filesystem this deletes the customer's new directory",
				e.path, RootDir())
		}
	}
}

// AN UPGRADING CHECKOUT'S RETIRED RULES ARE TAKEN BACK BY `link`.
//
// A customer coming from 0.61.x carries lines an older `link` appended. The most
// consequential is `palbase-env.d.ts`, written unanchored — git matches it at ANY
// depth, so it hides the file inside `palbase/` that the closing line tells them
// to commit. Only this path removes it, and `link` had stopped calling it.
func TestLinkTakesBackARetiredIgnoreRule(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	// Exactly what 0.61.x left behind, plus a rule of the person's own.
	before := "dist/\n.palbase/local.json\npalbase-env.d.ts\n"
	require.NoError(t, os.WriteFile(".gitignore", []byte(before), 0o644))

	require.NoError(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, io.Discard))

	body, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	require.NotContains(t, string(body), "palbase-env.d.ts",
		"the retired rule survived, so the file `link` tells them to commit stays invisible to git")
	require.NotContains(t, string(body), ".palbase/local.json", "a retired rule survived")
	require.Contains(t, string(body), "dist/", "the person's own rule was taken")
}
