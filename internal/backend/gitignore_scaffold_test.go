package backend

// gitignore_scaffold_test.go — AN IGNORE FILE IS WRITTEN ONLY WHERE THERE IS
// NONE, AND AFTER THAT THIS CLI ONLY TAKES ITS OWN RETIRED LINES BACK.
//
// FR-012 is the rule: no verb adds a rule for a path this CLI generates, because
// everything it writes into a checkout is committed now. FR-012a is the one
// exception — a directory with NO ignore file gets the ecosystem's rules,
// measured on a fresh web checkout (08.09.2026) where `link` created one without
// `node_modules/` and the next `git add -A` staged 500 installed files. FR-012b
// is the one-way undo: a rule whose producer this CLI retired is removed, and no
// other line moves.
//
// AN EMPTY FILE IS NOT "NO FILE". Somebody created it, and what it says is
// "nothing is ignored in this repository" — an answer this tool does not get to
// overwrite.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// linkInScratch stands up a scratch checkout and returns the production `link`,
// so every assertion below is made against the verb a person runs.
func linkInScratch(t *testing.T) func() {
	t.Helper()
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")
	return func() {
		t.Helper()
		require.NoError(t, runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, io.Discard))
	}
}

// requireEcosystemScaffold: the ecosystem's rules, and nothing of this CLI's.
func requireEcosystemScaffold(t *testing.T, body string) {
	t.Helper()
	require.Contains(t, body, "node_modules/",
		"a JavaScript project's ignore file without node_modules/ is how `git add -A` stages every installed dependency")
	require.Contains(t, body, "*.log")
	require.NotContains(t, strings.ToLower(body), "palbase",
		"the created file ignores a path this CLI writes; everything it writes is committed")
}

// FR-012a: HİÇ DOSYA YOKKEN — VE YALNIZ O ZAMAN — EKOSİSTEMİN KURALLARI YAZILIR.
func TestGitignoreIsScaffoldedOnlyWhereThereIsNone(t *testing.T) {
	t.Run("link", func(t *testing.T) {
		link := linkInScratch(t)

		link()

		body, err := os.ReadFile(".gitignore")
		require.NoError(t, err, "`link` created no .gitignore in a checkout that had none")
		requireEcosystemScaffold(t, string(body))
	})

	t.Run("init", func(t *testing.T) {
		dir := t.TempDir()

		require.NoError(t, writeGitignore(dir))

		body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		require.NoError(t, err, "`init` created no .gitignore in a directory that had none")
		requireEcosystemScaffold(t, string(body))
	})
}

// FR-012: VAR OLAN BİR DOSYAYA TEK SATIR EKLENMEZ — BOŞ OLANA DA.
func TestGitignoreAnExistingFileGainsNoRule(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"boş dosya", ""},
		{"yalnız boş satır", "\n"},
		{"kürate edilmiş", "dist/\n# mine\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("link", func(t *testing.T) {
				link := linkInScratch(t)
				require.NoError(t, os.WriteFile(".gitignore", []byte(tc.body), 0o644))

				link()

				got, err := os.ReadFile(".gitignore")
				require.NoError(t, err)
				require.Equal(t, tc.body, string(got), "`link` wrote into a .gitignore the checkout already had")
			})

			t.Run("init", func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, ".gitignore")
				require.NoError(t, os.WriteFile(path, []byte(tc.body), 0o644))

				require.NoError(t, writeGitignore(dir))

				got, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, tc.body, string(got), "`init` wrote into a .gitignore the directory already had")
			})
		})
	}
}

// FR-012b: EMEKLİ KURALLAR GİDER, BAŞKA HİÇBİR SATIR KIPIRDAMAZ.
//
// Satırlar CRLF, çünkü müşterinin dosyası bu makinede yazılmamış olabilir; içinde
// bir yorum, bir olumsuzlama ve bir boş satır var — bir onarım, onardığından
// fazlasını alırsa artık onarım değildir. Emekli kuralların kaynağı TEK liste
// (`retiredProjectPaths`): `.palbase` oradan geldiği için artık ayrıca elde
// yazılmış bir dala gerek yok.
func TestGitignoreTakesBackOnlyItsRetiredRules(t *testing.T) {
	before := "# mine\r\ndist/\r\n.palbase/\r\n!keep.log\r\n\r\n" +
		envTypesFile + "\r\n" + linkStagePrefix + "*/\r\nnode_modules/\r\n"
	want := "# mine\r\ndist/\r\n!keep.log\r\n\r\nnode_modules/\r\n"

	t.Run("link", func(t *testing.T) {
		link := linkInScratch(t)
		require.NoError(t, os.WriteFile(".gitignore", []byte(before), 0o644))

		link()

		got, err := os.ReadFile(".gitignore")
		require.NoError(t, err)
		require.Equal(t, want, string(got))
	})

	t.Run("init", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore")
		require.NoError(t, os.WriteFile(path, []byte(before), 0o644))

		require.NoError(t, writeGitignore(dir))

		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, want, string(got))
	})
}
