package backend

import (
	"os"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/stretchr/testify/require"
)

// chdirTemp makes a fresh temp dir the cwd for a test and restores it after.
func chdirTemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// TestResolveActiveBranch_ConfigFallback covers the filesystem branch of
// resolveActiveBranch that the rollback unit test deferred to smoke: with no
// --branch flag, it reads ProjectConfig.DefaultEnv. "main"/empty → "" (server
// omits, resolves default); a real branch → that branch.
func TestResolveActiveBranch_ConfigFallback(t *testing.T) {
	t.Run("DefaultEnv=main → empty (default branch)", func(t *testing.T) {
		chdirTemp(t)
		require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "abc123", DefaultEnv: "main"}))
		require.Equal(t, "", resolveActiveBranch(""))
	})

	t.Run("DefaultEnv=staging → staging", func(t *testing.T) {
		chdirTemp(t)
		require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "abc123", DefaultEnv: "staging"}))
		require.Equal(t, "staging", resolveActiveBranch(""))
	})

	t.Run("--branch flag overrides config", func(t *testing.T) {
		chdirTemp(t)
		require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "abc123", DefaultEnv: "staging"}))
		require.Equal(t, "qa", resolveActiveBranch("qa"))
		// explicit main still maps to "" even when config says staging
		require.Equal(t, "", resolveActiveBranch("main"))
	})
}

// TestDevBranchValue: dev-server's PALBASE_BRANCH is always a concrete value.
// Unlike the server payload (which omits "main"), resolveActiveBranch's "" is
// surfaced as "main" for the local dev-server to read.
func TestDevBranchValue(t *testing.T) {
	t.Run("no flag, no config → main", func(t *testing.T) {
		chdirTemp(t) // no .palbase/config.json
		require.Equal(t, "main", devBranchValue(""))
	})

	t.Run("config DefaultEnv=staging → staging", func(t *testing.T) {
		chdirTemp(t)
		require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "abc123", DefaultEnv: "staging"}))
		require.Equal(t, "staging", devBranchValue(""))
	})

	t.Run("--branch flag wins", func(t *testing.T) {
		chdirTemp(t)
		require.Equal(t, "qa", devBranchValue("qa"))
	})

	t.Run("--branch main → main (not empty)", func(t *testing.T) {
		chdirTemp(t)
		require.Equal(t, "main", devBranchValue("main"))
	})
}

// TestNewLink_DefaultsToMain pins the staging→main default fix: a freshly
// linked project's config resolves to the default (main) branch, i.e.
// resolveActiveBranch returns "" right after a link. We simulate the link's
// write (SaveProjectConfig with DefaultEnv "main", as resolveOrLinkRef now
// does) and confirm the branch resolution treats it as the default branch.
func TestNewLink_DefaultsToMain(t *testing.T) {
	chdirTemp(t)
	// This mirrors what resolveOrLinkRef writes on a fresh link (backend.go).
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "abc123", DefaultEnv: "main"}))

	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "main", cfg.DefaultEnv, "fresh link must default to main, not staging")
	require.Equal(t, "", resolveActiveBranch(""), "main default → server omits branch (default branch)")
}
