package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A FAILED CLONE LEAVES NOTHING NAMED AFTER THE PROJECT.
//
// `clone` must create the directory before the download, because the download
// writes into it. So every failure between the two — nothing deployed yet, no
// credential for the address, a dropped connection — used to leave an empty
// directory called `linkuat/`. Measured on the product: `palbase clone linkuat`
// against a project with nothing pushed.
func TestAFailedCloneRemovesTheDirectoryItMade(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "linkuat")
	existed := dirExists(dir)
	require.False(t, existed)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.True(t, reapEmptyClone(dir, existed), "clone did not decide to remove what it made")
	require.NoDirExists(t, dir, "a clone that downloaded nothing left a directory behind")
}

// IT DOES NOT TOUCH A DIRECTORY THAT WAS ALREADY THERE. `clone --dir .` into a
// checkout somebody is working in must not delete it because the download
// failed.
func TestCloneDoesNotRemoveADirectoryItDidNotMake(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mine")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	existed := dirExists(dir)
	require.True(t, existed, "dirExists said no about a directory that is there")

	require.False(t, reapEmptyClone(dir, existed), "clone decided to remove a directory it did not create")
	require.DirExists(t, dir, "clone deleted a directory it did not create")
}

// AND IT KEEPS A PARTIAL DOWNLOAD: files on disk are something somebody may
// want to look at, and deleting them would destroy the evidence of the failure.
func TestCloneKeepsAPartialDownload(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "linkuat")
	existed := dirExists(dir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "half.ts"), []byte("//"), 0o644))

	// THE DECISION IS WHAT IS MEASURED, not the filesystem: `os.Remove` refuses
	// a non-empty directory on its own, so looking only at the disk would be
	// green even if this code decided to delete the download. A mutation that
	// made the emptiness check inert passed exactly that way.
	require.False(t, reapEmptyClone(dir, existed), "clone decided to delete a partial download")
	require.DirExists(t, dir, "a partial download was deleted")
	require.FileExists(t, filepath.Join(dir, "half.ts"))
}
