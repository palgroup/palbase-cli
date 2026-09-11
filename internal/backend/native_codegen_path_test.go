package backend

// native_codegen_path_test.go — WHERE THE GENERATED CLIENT IS BORN, and where
// it must never be born again.
//
// The Swift client used to land under `Palbase/Generated/<env>/`, one of the two
// roots this migration removes. It now belongs to the environment it was
// generated for, beside that environment's contract, so an app that switches
// environments switches directories rather than editing its own source.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeCodegenPath(t *testing.T) {
	root := linkedProject(t, "ios")
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)

	envs := appEnvironments{
		Default:      "main",
		Environments: map[string]appEnvironment{"main": {AppID: projectAppID, BaseURL: "https://x.palbase.studio"}},
	}
	var out strings.Builder
	require.NoError(t, generateForEnvironmentsAt(context.Background(), envs, &out, ""))

	// THE client, at the declared path and nowhere else.
	require.FileExists(t, filepath.Join(root, filepath.FromSlash(GeneratedPath("main", "ios"))),
		"the generated client is not in its environment's directory")

	// AND NOTHING OF THE RETIRED LAYOUT IS CREATED — measured by CONTENT.
	//
	// Not by directory name: `palbase` and `Palbase` are ONE directory on macOS
	// and Windows, so `NoDirExists(root/"Palbase")` fails against the new root
	// itself. That is exactly the trap `CarriesLegacyLayout` exists to avoid,
	// and this assertion is its live check.
	require.Empty(t, CarriesLegacyLayout(root),
		"the retired layout was written again")

	// AND THE GENERATOR'S INPUT IS NOT ITS OUTPUT. `ConfigPath` is what `link`
	// writes for the platform; `PlistPath` is what swiftgen emits from it. They
	// were the same file, so the second call overwrote the first call's input —
	// invisible in every test because the Apple path runs against a stub.
	require.NotEqual(t, ConfigPath("main", "ios"), PlistPath("main"),
		"the generator reads and writes the same file")
}

// NEGATIVE CONTROL: the detector must actually SEE the retired layout.
//
// Without this, `TestNativeCodegenPath`'s "nothing retired was written" line
// could be measuring a function that never returns anything — a guard that
// cannot fail is not a guard. It also pins the property that made the old,
// name-based check wrong: the NEW root's own contents must not read as legacy.
func TestCarriesLegacyLayoutSeesContentNotTheName(t *testing.T) {
	root := t.TempDir()

	// The new layout alone: not legacy, even though on macOS this directory IS
	// `Palbase/` as far as the filesystem is concerned.
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(EnvDir("main"))), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(ClientBarrelPath())), []byte("export {}\n"), 0o644))
	require.Empty(t, CarriesLegacyLayout(root), "the new layout read as the old one")

	// Each marker of the old layout, one at a time.
	for _, marker := range LegacyMarkers() {
		p := filepath.Join(root, RootDir(), marker)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		require.NotEmpty(t, CarriesLegacyLayout(root), "%s was not seen", marker)
		require.NoError(t, os.Remove(p))
	}

	// And the hidden root, which is a real second directory.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".palbase"), 0o755))
	require.NotEmpty(t, CarriesLegacyLayout(root), ".palbase was not seen")
}
