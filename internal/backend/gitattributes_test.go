package backend

// gitattributes_test.go — THE RULE IS ASKED OF GIT, NOT OF THE FILE.
//
// Writing `*.swift linguist-generated=true` into a file proves nothing: the
// pattern can be anchored wrong, the path can be spelled with a separator git
// does not match, the file can be in a directory git never consults. The only
// authority on whether an attribute applies is `git check-attr`, so that is
// what this asks.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// checkAttr asks git what one attribute resolves to for one path.
func checkAttr(t *testing.T, root, attr, path string) string {
	t.Helper()
	cmd := exec.Command("git", "check-attr", attr, "--", path)
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "git check-attr %s -- %s", attr, path)
	// "<path>: <attr>: <value>"
	parts := strings.Split(strings.TrimSpace(string(out)), ": ")
	require.Len(t, parts, 3, "unexpected check-attr output: %q", out)
	return parts[2]
}

func TestGitattributesMarksGeneratedCode(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", root, "init", "-q").Run())

	require.NoError(t, writeGitattributes(root))

	// EVERY generated product, asked of git by the path the CLI actually writes.
	for _, path := range []string{
		GeneratedPath("main", "ios"),
		GeneratedPath("main", webPlatform),
		SpecPath("main"),
		PlistPath("main"),
		ClientBarrelPath(),
	} {
		require.Equal(t, "unset", checkAttr(t, root, "diff", path),
			"%s still produces a diff in review", path)
		require.Equal(t, "true", checkAttr(t, root, "linguist-generated", path),
			"%s is not marked generated, so it counts toward the repository's language stats", path)
	}

	// NEGATIVE CONTROL: the person's own source is untouched. A pattern broad
	// enough to cover everything would hide the diffs that matter most.
	require.Equal(t, "unspecified", checkAttr(t, root, "diff", "src/app.tsx"),
		"the rule reaches the application's own source")
	require.Equal(t, "unspecified", checkAttr(t, root, "diff", filepath.ToSlash(filepath.Join(RootDir(), "project.json"))),
		"the committed contract is generated-marked; it is written by a person's `link`, and its diff is the point")
}

// AND THE PERSON'S OWN `.gitattributes` IS NEVER TOUCHED.
//
// The rules live in `palbase/.gitattributes` — inside the one directory this
// CLI owns. Writing into the checkout's root file would be this tool editing
// somebody else's configuration, which is the same mistake the retired xcconfig
// mechanism made.
func TestGitattributesLeavesTheCheckoutsOwnFileAlone(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", root, "init", "-q").Run())

	mine := "*.png binary\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitattributes"), []byte(mine), 0o644))
	require.NoError(t, writeGitattributes(root))

	body, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	require.NoError(t, err)
	require.Equal(t, mine, string(body), "the checkout's own .gitattributes was edited")
	// The person's rule still APPLIES, asked of git. `binary` is a macro that
	// expands to `-diff -merge -text`, so `diff` reads as unset — and `text`
	// reading unset too is what distinguishes "their rule matched" from "no rule
	// matched at all", which would report `unspecified`.
	require.Equal(t, "unset", checkAttr(t, root, "text", "logo.png"), "somebody's own rule stopped applying")
	require.Equal(t, "unspecified", checkAttr(t, root, "text", "notes.md"), "a file no rule covers reported one")

	// Idempotent: a second link must not stack a second copy.
	ours := filepath.Join(root, RootDir(), ".gitattributes")
	first, err := os.ReadFile(ours)
	require.NoError(t, err)
	require.NoError(t, writeGitattributes(root))
	second, err := os.ReadFile(ours)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "a re-link changed the rules it had already written")
}

// AND THE PRODUCTION PATH CALLS IT. A declaration nothing ships is not a
// feature, and a test that calls the writer directly makes dead code look
// alive.
func TestGitattributesHasAProductionCaller(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	require.NoError(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, io.Discard))

	dir, _ := os.Getwd()
	body, err := os.ReadFile(filepath.Join(dir, RootDir(), ".gitattributes"))
	require.NoError(t, err, "`link` wrote no .gitattributes, so the rules never reach a checkout")
	require.Contains(t, string(body), "linguist-generated=true")
}
