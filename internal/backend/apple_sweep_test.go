package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedGeneratedEnvironment(t *testing.T, root, env string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(EnvDir(env)))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "PalbaseGenerated.swift"), []byte("// generated"), 0o644))
	return dir
}

// ONE LINK WRITES EVERY ENVIRONMENT, so the sweep that removes an environment
// the project no longer has must be told what the project HAS — not only what
// this run happened to describe. Measured before the change: it deleted
// `staging` because the map it was handed carried `main` alone.
func TestAppleSweepKeepsEveryEnvironmentOfTheProject(t *testing.T) {
	root := linkedProject(t, "ios")
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	staging := seedGeneratedEnvironment(t, root, "staging")
	envs := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main": {AppID: projectAppID, BaseURL: "https://x.palbase.studio"},
	}}

	var out strings.Builder
	require.NoError(t, generateForEnvironmentsAt(context.Background(), envs, []string{"main", "staging"}, &out, ""))
	assert.DirExists(t, staging, "the sweep deleted an environment the project still has")
}

func TestAppleSweepStillRemovesAnEnvironmentTheProjectLost(t *testing.T) {
	root := linkedProject(t, "ios")
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	oldenv := seedGeneratedEnvironment(t, root, "oldenv")
	envs := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main": {AppID: projectAppID, BaseURL: "https://x.palbase.studio"},
	}}

	var out strings.Builder
	require.NoError(t, generateForEnvironmentsAt(context.Background(), envs, []string{"main", "staging"}, &out, ""))
	_, err := os.Stat(oldenv)
	assert.True(t, os.IsNotExist(err), "an environment the project no longer has survived the sweep")
}
