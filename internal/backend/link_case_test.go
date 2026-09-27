package backend

// link_case_test.go — ONE ENVIRONMENT, ONE SPELLING, ON EVERY DISK (FR-012).
//
// On a case-insensitive disk — every Mac's — `Staging/` and `staging/` are one
// directory, and a build that looks its environment up by exact name (the
// 2.4 Gradle plugin does, so a Linux CI agrees with a Mac) finds only the
// spelling on disk. An environment renamed by case alone must come out of the
// link under the name the project gives it now.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// underEnvironments is one file's path inside the checkout, as a publish keys it.
func underEnvironments(env, file string) string {
	return filepath.Join(RootDir(), envSubdir, env, file)
}

// A DIRECTORY RENAMED BY CASE ALONE REACHES THE CHECKOUT. Publishing goes file
// by file, and on a case-insensitive disk `staging/openapi.json` is a path
// `Staging/openapi.json` already answers to: the publish took the renamed file
// for one that appeared during the link and refused everything. The checkout
// ends up spelled as the stage is, holding exactly the stage's files.
func TestAPublishCarriesADirectoryRenamedByCaseAlone(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, RootDir(), envSubdir)
	require.NoError(t, os.MkdirAll(filepath.Join(envs, "Staging"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")), []byte("old contract"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "PalbaseGenerated.swift")), []byte("old client"), 0o644))
	before := map[string]artifactFile{
		underEnvironments("Staging", "openapi.json"):           {[]byte("old contract"), 0o644},
		underEnvironments("Staging", "PalbaseGenerated.swift"): {[]byte("old client"), 0o644},
	}
	after := map[string]artifactFile{
		underEnvironments("staging", "openapi.json"):        {[]byte("new contract"), 0o644},
		underEnvironments("staging", "android-config.json"): {[]byte("new config"), 0o600},
	}

	require.NoError(t, publishArtifacts(root, before, after))

	require.Equal(t, []string{"staging"}, entriesIn(t, envs))
	require.Equal(t, []string{"android-config.json", "openapi.json"}, entriesIn(t, filepath.Join(envs, "staging")))
	raw, err := os.ReadFile(filepath.Join(envs, "staging", "openapi.json"))
	require.NoError(t, err)
	require.Equal(t, "new contract", string(raw))
}

// AND A PUBLISH THAT IS REFUSED TAKES THE RENAME BACK: "previous artifacts
// were preserved" covers the directory's spelling too. Somebody edited a file
// while the link ran, so nothing is published.
func TestARefusedPublishLeavesTheDirectorySpelledAsItWas(t *testing.T) {
	root := t.TempDir()
	envs := filepath.Join(root, RootDir(), envSubdir)
	require.NoError(t, os.MkdirAll(filepath.Join(envs, "Staging"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")), []byte("edited meanwhile"), 0o644))
	before := map[string]artifactFile{underEnvironments("Staging", "openapi.json"): {[]byte("old contract"), 0o644}}
	after := map[string]artifactFile{underEnvironments("staging", "openapi.json"): {[]byte("new contract"), 0o644}}

	require.ErrorContains(t, publishArtifacts(root, before, after), "changed during link")

	require.Equal(t, []string{"Staging"}, entriesIn(t, envs))
	raw, err := os.ReadFile(filepath.Join(root, underEnvironments("Staging", "openapi.json")))
	require.NoError(t, err)
	require.Equal(t, "edited meanwhile", string(raw))
}
