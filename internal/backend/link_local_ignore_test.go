package backend

// link_local_ignore_test.go — `palbase/environments/local/` IS THIS MACHINE'S,
// SO GIT NEVER SEES IT (FR-020, D-014).
//
// Everything else `link` writes under `palbase/` is the project's and is
// committed (FR-012). `local/` is the one directory that is not: it carries the
// port this machine's stack listens on and that stack's key. Committed, it was
// every teammate's debug build — the plugin's debug default is `local` (D-003).

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localIgnoreLine is the one rule `link` adds, and the sentence it says when it
// does.
const (
	localIgnoreLine  = "palbase/environments/local/"
	localIgnoreAdded = "added palbase/environments/local/ to .gitignore — it holds this machine's stack address and key, " +
		"which no teammate's build should use\n"
)

// androidLinkHere links an Android checkout to a stack on this machine and
// returns what link said.
func androidLinkHere(t *testing.T) string {
	t.Helper()
	seedAndroidApp(t)
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())
	return out.String()
}

func TestAnAppLinkKeepsThisMachinesStackOutOfGit(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"curated", "dist/\n# mine\n", "dist/\n# mine\n" + localIgnoreLine + "\n"},
		{"no final newline", "dist/", "dist/\n" + localIgnoreLine + "\n"},
		{"empty", "", localIgnoreLine + "\n"},
		{"CRLF", "dist/\r\n", "dist/\r\n" + localIgnoreLine + "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inScratchCheckout(t)
			require.NoError(t, os.WriteFile(".gitignore", []byte(tc.before), 0o644))

			out := androidLinkHere(t)

			got, err := os.ReadFile(".gitignore")
			require.NoError(t, err)
			assert.Equal(t, tc.after, string(got))
			assert.Contains(t, out, localIgnoreAdded)
		})
	}
}

// A RULE ALREADY THERE, in any spelling git reads the same, is left as it is —
// and so is the file around it.
func TestAnAppLinkLeavesALocalRuleThatIsAlreadyThere(t *testing.T) {
	for _, rule := range []string{"palbase/environments/local/", "/palbase/environments/local", "palbase/environments/local"} {
		t.Run(rule, func(t *testing.T) {
			inScratchCheckout(t)
			before := "dist/\n" + rule + "\n# mine"
			require.NoError(t, os.WriteFile(".gitignore", []byte(before), 0o644))

			out := androidLinkHere(t)

			got, err := os.ReadFile(".gitignore")
			require.NoError(t, err)
			assert.Equal(t, before, string(got))
			assert.NotContains(t, out, "to .gitignore")
		})
	}
}

// A CHECKOUT WITH NO CLIENT GETS NO `local/`, so it gets no rule for one.
func TestABackendLinkAddsNoLocalRule(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, os.WriteFile(".gitignore", []byte("dist/\n"), 0o644))
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())

	got, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	assert.Equal(t, "dist/\n", string(got))
}
