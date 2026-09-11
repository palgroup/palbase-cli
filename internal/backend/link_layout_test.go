package backend

// link_layout_test.go — `link` WRITES THE NEW LAYOUT AND REFUSES THE OLD ONE.
//
// There is no migration. A half-old, half-new tree is the one outcome that makes
// "I will clean the old files up myself" impossible to reason about: two
// contracts, two clients, and no way to tell which one the build read. So a
// checkout still carrying the retired layout is refused — before anything is
// written — and told exactly what to delete.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLinkLayoutRefusesTheRetiredLayout(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T)
		want string
	}{
		{
			// The hidden root is a real second directory on every filesystem.
			name: ".palbase",
			seed: func(t *testing.T) {
				require.NoError(t, os.MkdirAll(".palbase", 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(".palbase", "project.json"), []byte(`{"url":"x"}`), 0o644))
			},
			want: ".palbase",
		},
		{
			// The visible one is NOT a second directory — `palbase` and
			// `Palbase` are the same on macOS and Windows (D-008) — so it is
			// recognised by what only the old layout ever put inside it.
			name: "Palbase/Generated",
			seed: func(t *testing.T) {
				require.NoError(t, os.MkdirAll(filepath.Join(RootDir(), "Generated"), 0o755))
			},
			want: "Generated",
		},
		{
			name: "Palbase/openapi.json",
			seed: func(t *testing.T) {
				require.NoError(t, os.MkdirAll(RootDir(), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(RootDir(), "openapi.json"), []byte(`{}`), 0o644))
			},
			want: "openapi.json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inScratchCheckout(t)
			srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
			linkedAs(t, srv.URL, "a-credential")
			tc.seed(t)

			var out strings.Builder
			err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out)
			require.Error(t, err, "a checkout carrying the retired layout was linked")

			// IT NAMES WHAT IT FOUND. "the old layout is present" sends somebody
			// looking; the path does not.
			require.Contains(t, err.Error(), tc.want, "the refusal does not name what it found")
			// AND WHAT TO DO. Deleting is a commit, not a side effect of a tool.
			require.Contains(t, strings.ToLower(err.Error()), "delete",
				"the refusal does not say how to get past it")
		})
	}
}

// AND IT REFUSES BEFORE IT TOUCHES ANYTHING.
//
// A refusal that has already written half a checkout is worse than no refusal:
// the person now has the retired layout AND part of the new one, which is the
// exact state this task exists to prevent.
func TestLinkLayoutRefusesBeforeAnySideEffect(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	require.NoError(t, os.MkdirAll(".palbase", 0o755))

	before := treeOf(t, dir)
	err := runLink(context.Background(), linkOpts{url: "https://unused.example", platforms: []string{"ios"}}, io.Discard)
	require.Error(t, err)
	require.Equal(t, before, treeOf(t, dir), "the refusal wrote into the checkout first")
}

// A CLEAN CHECKOUT LINKS, AND LEAVES THE PERSON THE THREE LINES.
func TestLinkLayoutPrintsTheSelectionPattern(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out))

	// THE PATTERNS, EXACTLY. `*` does not cross a directory boundary, so the
	// trailing `/*` is what reaches the files inside an environment — a pattern
	// written without it silently excludes nothing.
	for _, line := range []string{
		"PALBASE_ENV = main",
		"EXCLUDED_SOURCE_FILE_NAMES = */palbase/environments/*/*",
		"INCLUDED_SOURCE_FILE_NAMES = */palbase/environments/$(PALBASE_ENV)/*",
	} {
		require.Contains(t, out.String(), line, "the selection pattern is not printed verbatim")
	}
}

// AND `link` NO LONGER REPAIRS AN IGNORE FILE.
//
// It has nothing to ignore: everything it writes is committed. Leaving the call
// in would keep editing somebody's `.gitignore` to add rules for files that no
// longer exist.
func TestLinkLayoutWritesNoIgnoreRules(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	mine := "dist/\n"
	require.NoError(t, os.WriteFile(".gitignore", []byte(mine), 0o644))
	require.NoError(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, io.Discard))

	body, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	require.Equal(t, mine, string(body), "`link` edited the checkout's ignore file")
}

// treeOf lists a directory tree, relative and sorted, for before/after equality.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	require.NoError(t, filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}))
	return paths
}
