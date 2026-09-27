package backend

// link_root_spelling_test.go — THE CLI'S DIRECTORY IS SPELLED ONE WAY.
//
// The stage knows the directories a link may write by their exact names, and
// `palbase` is one of them; any other directory is symlinked into the stage and
// left alone. `Palbase/` is `palbase/` on a Mac's disk, so a link in a checkout
// spelled that way wrote, swept and renamed straight in the real checkout — and
// a link that failed left a half-written environment there while it said
// "previous client artifacts were preserved" (final review, Important #1).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linkOneEnvironment links an Android checkout to a project listing only
// `main`, served by a stack that answers every read.
func linkOneEnvironment(t *testing.T) (string, error) {
	t.Helper()
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		platforms:    []string{"android"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}
	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	return out.String(), err
}

// seedSpelledRoot commits one environment's contract under a root spelled
// `root`, and returns its path.
func seedSpelledRoot(t *testing.T, root, env, body string) string {
	t.Helper()
	contract := filepath.Join(root, envSubdir, env, "openapi.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(contract), 0o755))
	require.NoError(t, os.WriteFile(contract, []byte(body), 0o644))
	return contract
}

// A `Palbase/` ROOT IS REFUSED BEFORE ANYTHING IS STAGED, and the checkout is
// left exactly as it was: nothing swept, nothing written, no `palbase/` beside
// it.
//
// ON EVERY DISK, because the disk that decides is the STAGE's as much as the
// checkout's. Measured on a case-sensitive APFS volume with the stage in the
// default temp directory: the link reported success, wrote main's files into
// `Palbase/`, removed `Palbase/environments/x`, and told the reader to commit a
// `palbase/` that did not exist.
func TestALinkRefusesAPalbaseDirectorySpelledAnotherWay(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	contract := seedSpelledRoot(t, "Palbase", "x", "x's contract")

	out, err := linkOneEnvironment(t)

	require.EqualError(t, err, "this checkout spells the palbase directory `Palbase`; "+
		"rename it to `palbase` and run `palbase link` again", out)
	assert.Empty(t, out)
	raw, readErr := os.ReadFile(contract)
	require.NoError(t, readErr)
	assert.Equal(t, "x's contract", string(raw))
	assert.Equal(t, []string{"Palbase", "app"}, entriesIn(t, "."))
	assert.Equal(t, []string{"x"}, entriesIn(t, filepath.Join("Palbase", envSubdir)))
	assert.Equal(t, []string{"openapi.json"}, entriesIn(t, filepath.Join("Palbase", envSubdir, "x")))
}

// A NAME THAT IS NOT ONE PLAIN WORD IS PRINTED AS ONE (FR-006): the full fold
// takes `ſ` for `s`, so `palbaſe/` is the palbase directory on a Mac too.
func TestALinkNamesAnUnusualSpellingOfThePalbaseDirectoryQuoted(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	seedSpelledRoot(t, "palbaſe", "x", "x's contract")

	_, err := linkOneEnvironment(t)

	require.EqualError(t, err, "this checkout spells the palbase directory `\"palbaſe\"`; "+
		"rename it to `palbase` and run `palbase link` again")
}

// ON A CASE-SENSITIVE DISK BOTH SPELLINGS CAN EXIST, and "rename it" would
// collide with the real one: the second is to be moved aside. (A Mac cannot
// hold the two, so there is nothing to measure there.)
func TestALinkRefusesAPalbaseDirectoryBesideTheRealOne(t *testing.T) {
	inScratchCheckout(t)
	if caseInsensitiveDisk(t, ".") {
		t.Skip("this disk takes Palbase and palbase for one name — there is no second directory")
	}
	seedAndroidApp(t)
	other := seedSpelledRoot(t, "Palbase", "x", "x's contract")
	exact := seedSpelledRoot(t, RootDir(), "featurex", "featurex's contract")

	out, err := linkOneEnvironment(t)

	require.EqualError(t, err, "this checkout holds both `Palbase` and `palbase`, which are one directory on a Mac; "+
		"move `Palbase` aside and run `palbase link` again", out)
	for path, body := range map[string]string{other: "x's contract", exact: "featurex's contract"} {
		raw, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, body, string(raw))
	}
	assert.Equal(t, []string{"featurex"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)))
}
