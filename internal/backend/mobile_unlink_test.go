package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// findMobileCmd locates the `mobile` command in the flat surface so its
// subcommand tree can be asserted.
func findMobileCmd(t *testing.T) *cobra.Command {
	t.Helper()
	for _, c := range Commands(noopResolvers()) {
		if c.Name() == "mobile" {
			return c
		}
	}
	t.Fatal("mobile command not found in surface")
	return nil
}

// TestMobile_LinkUnlinkSurface pins the rename: `mobile link` / `mobile
// unlink` exist, `mobile setup` is gone, and both expose an `ios`
// subcommand.
func TestMobile_LinkUnlinkSurface(t *testing.T) {
	mobile := findMobileCmd(t)

	sub := map[string]*cobra.Command{}
	for _, c := range mobile.Commands() {
		sub[c.Name()] = c
	}

	require.Contains(t, sub, "link", "mobile link must exist (renamed from setup)")
	require.Contains(t, sub, "unlink", "mobile unlink must exist")
	require.NotContains(t, sub, "setup", "mobile setup must be gone after rename to link")

	for _, parent := range []string{"link", "unlink"} {
		hasIOS := false
		for _, c := range sub[parent].Commands() {
			if c.Name() == "ios" {
				hasIOS = true
			}
		}
		require.True(t, hasIOS, "mobile %s must expose an ios subcommand", parent)
	}
}

// TestMobileUnlinkIOS_RemovesLink runs `mobile unlink ios` end-to-end
// (it touches no Studio client) and asserts the link file is gone.
func TestMobileUnlinkIOS_RemovesLink(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "centauri0eme7m", DefaultEnv: "main"}))

	mobile := findMobileCmd(t)
	mobile.SetArgs([]string{"unlink", "ios"})
	mobile.SetOut(os.Stdout)
	require.NoError(t, mobile.Execute())

	_, statErr := os.Stat(filepath.Join(".palbase", "config.json"))
	require.True(t, os.IsNotExist(statErr), "config.json should be removed, stat err = %v", statErr)
}
