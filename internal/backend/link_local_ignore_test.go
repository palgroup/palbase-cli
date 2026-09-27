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
// does. localIgnoreKept is what it says instead when a negation already made
// the opposite choice (review-T012).
const (
	localIgnoreLine  = "palbase/environments/local/"
	localIgnoreAdded = "added palbase/environments/local/ to .gitignore — it holds this machine's stack address and key, " +
		"which no teammate's build should use\n"
	localIgnoreKept = ".gitignore keeps palbase/environments/local/ in git (!palbase/environments/local/) — it holds this machine's stack; " +
		"remove that line to keep it out\n"
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

// A NEGATION IS A CHOICE, NOT AN OVERSIGHT (review-T012). When the LAST line
// naming local/ is `!palbase/environments/local/` — even after a broader
// `palbase/` exclude, since git takes the last matching line — somebody wants
// this checkout's stack tracked. `link` leaves the file exactly as it is and
// only reports the choice back.
func TestAnAppLinkLeavesANegatedLocalRuleAlone(t *testing.T) {
	inScratchCheckout(t)
	before := "palbase/\n!palbase/environments/local/\n"
	require.NoError(t, os.WriteFile(".gitignore", []byte(before), 0o644))

	out := androidLinkHere(t)

	got, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	assert.Equal(t, before, string(got))
	assert.Contains(t, out, localIgnoreKept)
}

// AND THE LAST LINE DECIDES, THE WAY GIT DOES. A negation undone by a later
// positive rule is undone: the rule is already there, so `link` neither
// writes nor says anything.
func TestAnAppLinkLeavesALaterPositiveRuleAlone(t *testing.T) {
	inScratchCheckout(t)
	before := "!palbase/environments/local/\npalbase/environments/local/\n"
	require.NoError(t, os.WriteFile(".gitignore", []byte(before), 0o644))

	out := androidLinkHere(t)

	got, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	assert.Equal(t, before, string(got))
	assert.NotContains(t, out, "to .gitignore")
	assert.NotContains(t, out, "keeps")
}

// A REPEAT LINK SAYS IT AGAIN — the file still makes the same choice, so
// saying so again is fine — but says it exactly ONCE PER RUN, never twice
// within the one link that found the negation.
func TestAnAppLinkRepeatsTheNegationNoticeOncePerRun(t *testing.T) {
	inScratchCheckout(t)
	before := "palbase/\n!palbase/environments/local/\n"
	require.NoError(t, os.WriteFile(".gitignore", []byte(before), 0o644))

	first := androidLinkHere(t)
	assert.Equal(t, 1, strings.Count(first, localIgnoreKept), "the notice printed more than once in one run")

	second := androidLinkHere(t)
	assert.Equal(t, 1, strings.Count(second, localIgnoreKept), "the notice printed more than once in one run")

	got, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	assert.Equal(t, before, string(got))
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
