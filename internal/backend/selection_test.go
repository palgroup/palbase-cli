package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE SELECTION IS THIS MACHINE'S, AND IT NEVER ENTERS THE REPOSITORY.
//
// Firebase's maintainer wrote down why, and the reason is this project's
// scenario exactly: "We do not want to store the 'current' mapping in the
// project directory, because if it were committed into version control you
// could accidentally switch someone from 'staging' to 'production' instantly."
// So the map of what EXISTS is the server's, and the choice of what is SELECTED
// is this checkout's on this machine.
func TestSelectionIsWrittenOutsideTheCheckout(t *testing.T) {
	inScratchCheckout(t)
	root, err := os.Getwd()
	require.NoError(t, err)

	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "staging", Ref: "mu0028"}))

	// Nothing under the checkout — not a file, not a directory.
	var found []string
	require.NoError(t, filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(strings.ToLower(filepath.Base(p)), "selection") {
			found = append(found, p)
		}
		return nil
	}))
	require.Empty(t, found, "the selection leaked into the customer's checkout")

	// And it is readable back from the machine-local store.
	got, readErr := ReadSelection(root)
	require.NoError(t, readErr)
	require.Equal(t, "staging", got.Env)
	require.Equal(t, "mu0028", got.Ref)
}

// THE RECORD CARRIES ITS PROJECT, and that is the whole defence against the one
// failure mode a remembered choice has: a checkout relinked to a DIFFERENT
// project must not inherit an address from the project it left.
func TestSelectionCarriesTheProjectItBelongsTo(t *testing.T) {
	inScratchCheckout(t)
	root, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "staging", Ref: "mu0028"}))

	got, readErr := ReadSelection(root)
	require.NoError(t, readErr)
	require.Equal(t, "prd_a", got.Project,
		"a selection that does not say which project it belongs to cannot be judged stale")
}

// ASKING WHERE THE SELECTION LIVES MUST NOT CREATE ANYTHING. The same defect
// this package already paid for once: a read path that called MkdirAll left one
// directory per question in the user's home — 823 of them, measured.
func TestReadingASelectionCreatesNothing(t *testing.T) {
	inScratchCheckout(t)
	root, err := os.Getwd()
	require.NoError(t, err)

	_, readErr := ReadSelection(root)
	require.Error(t, readErr, "an unselected checkout has no selection")

	dir, dirErr := machineStateDir(root)
	require.NoError(t, dirErr)
	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr),
		"reading a selection created %s — asking for a path must not create it", dir)
}

// A SELECTION IS REPLACED, NOT APPENDED TO.
func TestWritingASelectionReplacesThePrevious(t *testing.T) {
	inScratchCheckout(t)
	root, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "staging", Ref: "mu0028"}))
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "main", Ref: "j06bwtuum"}))

	got, readErr := ReadSelection(root)
	require.NoError(t, readErr)
	require.Equal(t, "main", got.Env)
	require.Equal(t, "j06bwtuum", got.Ref)
}
