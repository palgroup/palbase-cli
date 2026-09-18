package backend

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// installBackend writes node_modules/@palbase/backend/package.json under dir.
func installBackend(t *testing.T, dir, version string) {
	t.Helper()
	pkg := filepath.Join(dir, "node_modules", "@palbase", "backend")
	require.NoError(t, os.MkdirAll(pkg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "package.json"),
		[]byte(`{"name":"@palbase/backend","version":"`+version+`"}`), 0o644))
}

func writeLock(t *testing.T, dir, key, version string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(`{
  "name": "app", "lockfileVersion": 3, "requires": true,
  "packages": {
    "": {"name": "app"},
    "`+key+`": {"version": "`+version+`", "resolved": "https://registry.npmjs.org/x.tgz"}
  }
}`), 0o644))
}

// THE ARTIFACT IS BUILT WITH WHAT THE LOCKFILE SAYS, OR NOT AT ALL
// (palbase-cli#7 §4). `node_modules` at 39.1.1 while the lockfile pins 38.0.15
// is an install nobody refreshed after a pull; push built with it silently, so
// the artifact that shipped was compiled against an SDK nobody committed or
// tested. Refused before anything is built, naming both numbers and the fix.
func TestPushAndPlanRefuseAnInstallTheLockfileDoesNotPin(t *testing.T) {
	for _, tc := range []struct {
		name      string
		layout    func(t *testing.T, root string) string // returns the project dir
		wantRefus bool
	}{
		{"drifted", func(t *testing.T, root string) string {
			writeLock(t, root, "node_modules/@palbase/backend", "38.0.15")
			installBackend(t, root, "39.1.1")
			return root
		}, true},
		{"drifted inside an npm workspace", func(t *testing.T, root string) string {
			dir := filepath.Join(root, "backend")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			writeLock(t, root, "backend/node_modules/@palbase/backend", "38.0.15")
			installBackend(t, dir, "39.1.1")
			return dir
		}, true},
		{"in step", func(t *testing.T, root string) string {
			writeLock(t, root, "node_modules/@palbase/backend", "39.1.1")
			installBackend(t, root, "39.1.1")
			return root
		}, false},
		{"no lockfile", func(t *testing.T, root string) string {
			installBackend(t, root, "39.1.1")
			return root
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
			dir := tc.layout(t, root)
			_, _, err := buildStackArtifact(context.Background(), dir, t.TempDir(), io.Discard)
			require.Error(t, err) // the fixture has no module either way
			if tc.wantRefus {
				require.ErrorContains(t, err, "38.0.15")
				require.ErrorContains(t, err, "39.1.1")
				require.ErrorContains(t, err, "npm ci")
			} else {
				require.NotContains(t, err.Error(), "npm ci", "refused an install the lockfile does pin")
			}
		})
	}
}
