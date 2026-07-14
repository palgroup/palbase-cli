package backend

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedParserCache writes a cached (already-installed) pinned TypeScript into a
// temp tool home and points parserTSHome at it — ensureParserTS must then be a
// pure cache hit (no npm, no network).
func seedParserCache(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mods := filepath.Join(home, "typescript-"+parserTSVersion, "node_modules")
	require.NoError(t, os.MkdirAll(filepath.Join(mods, "typescript"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "typescript", "package.json"),
		[]byte(`{"name":"typescript","version":"`+parserTSVersion+`"}`), 0o644))
	prev := parserTSHome
	parserTSHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { parserTSHome = prev })
	return mods
}

// TestDevNodePath_PinnedParserWinsOverProject locks the resolution ORDER: the
// CLI's own TypeScript must come FIRST on NODE_PATH, ahead of the project's
// node_modules. The harness scripts run from a temp dir outside the project, so
// NODE_PATH is what resolves their `require('typescript')` and the first entry
// wins — with the project first, a user's TypeScript 7 (no compiler API) gets
// loaded and every build dies on `ts.ScriptTarget.ES2022`.
//
// Mutation (M5): drop the prepend in devNodePath (return the project dir alone)
// and this goes RED.
func TestDevNodePath_PinnedParserWinsOverProject(t *testing.T) {
	mods := seedParserCache(t)
	project := t.TempDir()

	got := devNodePath(project, io.Discard)

	parts := strings.Split(got, string(os.PathListSeparator))
	require.Len(t, parts, 2)
	require.Equal(t, mods, parts[0], "the CLI's pinned typescript must be resolved BEFORE the project's")
	require.Equal(t, filepath.Join(project, "node_modules"), parts[1])
}

// TestDevNodePath_FallsBackWhenParserUnavailable locks the fail-open contract:
// when the tool cache can't be provisioned (no HOME, no npm, offline first run),
// NODE_PATH degrades to the project's node_modules — the build still runs on the
// project's typescript if it has one, and the harness prints the actionable
// parser error if it doesn't. Never a hard stop here.
func TestDevNodePath_FallsBackWhenParserUnavailable(t *testing.T) {
	prev := parserTSHome
	parserTSHome = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { parserTSHome = prev })

	project := t.TempDir()
	require.Equal(t, filepath.Join(project, "node_modules"), devNodePath(project, io.Discard))
}

// TestEnsureParserTS_CacheHitDoesNotInstall: a provisioned cache is returned
// as-is, silently (no npm spawn, no output) — the install runs once per pinned
// version per machine, not on every serve/push.
func TestEnsureParserTS_CacheHitDoesNotInstall(t *testing.T) {
	mods := seedParserCache(t)
	var out strings.Builder
	require.Equal(t, mods, ensureParserTS(&out))
	require.Empty(t, out.String(), "a cache hit must not print an install line")
}
