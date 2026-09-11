package backend

// layout_migration_test.go — THE THREE PROPERTIES THIS MIGRATION EXISTS FOR,
// each asked of the production code rather than of a fixture.
//
//  1. What this repository IS, is answered by the environment directories.
//  2. This machine's selected stack is not in the repository.
//  3. This machine's plan is not in the repository.
//
// (2) and (3) are the same sentence said twice on purpose: they were two
// separate files, written by two different verbs, and each was found in the
// checkout on its own.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLinkedPlatforms — the checkout's platforms come from the environment
// directories, not from a slot file at a path somebody spelled out.
func TestLinkedPlatforms(t *testing.T) {
	t.Chdir(t.TempDir())

	web, apple, android := linkedPlatforms()
	require.False(t, web || apple || android, "an empty checkout claimed a platform")

	// One environment carrying a web config makes this a web checkout.
	require.NoError(t, os.MkdirAll(filepath.FromSlash(EnvDir("main")), 0o755))
	require.NoError(t, os.WriteFile(filepath.FromSlash(ConfigPath("main", webPlatform)), []byte(`{}`), 0o600))
	web, _, _ = linkedPlatforms()
	require.True(t, web, "a web config under an environment did not make this a web checkout")

	// ANY environment, not only the default: a checkout is often linked to the
	// local stack before a cloud environment exists.
	require.NoError(t, os.MkdirAll(filepath.FromSlash(EnvDir(localEnvName)), 0o755))
	require.NoError(t, os.WriteFile(filepath.FromSlash(ConfigPath(localEnvName, "android")), []byte(`{}`), 0o600))
	_, _, android = linkedPlatforms()
	require.True(t, android, "a config under a non-default environment was not seen")

	// NEGATIVE CONTROL: a platform nobody configured stays false, so the
	// assertions above are measuring the file and not the walk.
	_, apple, _ = linkedPlatforms()
	require.False(t, apple, "an Apple checkout was claimed with no Apple config")
}

// TestStatusEnvironments — `status` lists the environments from the same
// directories the link wrote, so the two can never disagree.
func TestStatusEnvironments(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, env := range []string{"main", "staging"} {
		require.NoError(t, os.MkdirAll(filepath.FromSlash(EnvDir(env)), 0o755))
		require.NoError(t, os.WriteFile(filepath.FromSlash(ConfigPath(env, "ios")),
			[]byte(`{"app_id":"project","base_url":"https://x"}`), 0o600))
	}
	envs, err := readAppEnvironments("ios")
	require.NoError(t, err)

	names := envs.names()
	require.Len(t, names, 2, "the environments came from somewhere other than the directories: %v", names)
	require.Contains(t, names, "main")
	require.Contains(t, names, "staging")
}

// TestLocalStateLivesOutsideTheCheckout — `palbase start` records the stack in
// front of you on THIS MACHINE, and a repository is not where a machine keeps
// its state.
func TestLocalStateLivesOutsideTheCheckout(t *testing.T) {
	checkout := t.TempDir()
	path, err := LocalStatePath(checkout)
	require.NoError(t, err)

	rel, relErr := filepath.Rel(checkout, path)
	require.True(t, relErr != nil || strings.HasPrefix(rel, ".."),
		"the local target is still inside the checkout: %s", rel)

	// And it is keyed by the checkout, so two projects on one machine do not
	// share a selection.
	other, err := LocalStatePath(t.TempDir())
	require.NoError(t, err)
	require.NotEqual(t, path, other, "two checkouts share one local target")
}

// TestPlanStateLivesOutsideTheCheckout — same sentence for `palbase plan`. The
// plan is a measurement made here, now, against the environment selected here;
// committed, two developers overwrite each other's.
func TestPlanStateLivesOutsideTheCheckout(t *testing.T) {
	checkout := t.TempDir()
	path, err := planFilePath(checkout)
	require.NoError(t, err)

	rel, relErr := filepath.Rel(checkout, path)
	require.True(t, relErr != nil || strings.HasPrefix(rel, ".."),
		"the plan is still written into the checkout: %s", rel)

	// A round trip through the real writer and reader, ending with the file
	// nowhere near the repository.
	p := PlanFile{Version: 1, Target: PlanTarget{URL: "https://x.palbase.studio"}, Fingerprint: strings.Repeat("a", 64)}
	require.NoError(t, WritePlanFile(checkout, p))
	back, err := ReadPlanFile(checkout)
	require.NoError(t, err)
	require.Equal(t, p.Fingerprint, back.Fingerprint)

	entries, err := os.ReadDir(checkout)
	require.NoError(t, err)
	require.Empty(t, entries, "`plan` left files in the checkout: %v", entries)

	// AND ITS ABSENCE IS STILL ErrNoPlan, not some other error: `push` refuses
	// on that sentinel, and a plain "file not found" would reach the user as a
	// path they never chose.
	_, err = ReadPlanFile(t.TempDir())
	require.ErrorIs(t, err, ErrNoPlan)
}
