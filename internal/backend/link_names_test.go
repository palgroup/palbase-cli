package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// entriesIn names what sits one level under dir, sorted.
func entriesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A NAME FROM THE LISTING IS NOT A PATH UNTIL IT IS ONE DIRECTORY (FR-001).
//
// Every environment below is served by a stack that answers every read, so
// nothing but the name gate keeps its files off the disk. Measured before the
// gate: `../../gradle` wrote through the stage's symlink into the checkout's
// own gradle/, `..` wrote palbase/openapi.json — the retired layout's marker,
// so every later link refused the checkout — and `../../../outside` wrote next
// to the stage, outside the checkout altogether. `main.` is one directory here
// and `main` on a Windows teammate's disk, and a name that is not UTF-8 is no
// directory APFS will make.
func TestLinkSkipsAnEnvironmentWhoseNameIsNotOneDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	outside := t.TempDir()
	t.Setenv("TMPDIR", outside) // the stage opens here, so "outside" has an address
	require.NoError(t, os.MkdirAll("gradle", 0o755))
	main := stackServing(t, linkKeyMain, nil)
	evil := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{
		"mainref000": main.URL, "evilref001": evil.URL, "evilref002": evil.URL,
		"evilref003": evil.URL, "evilref004": evil.URL, "evilref005": evil.URL,
		"evilref006": evil.URL, "evilref007": evil.URL,
	})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "../../gradle", Ref: "evilref001", Status: "Running"},
			{Name: "..", Ref: "evilref002", Status: "Running"},
			{Name: "../../../outside", Ref: "evilref003", Status: "Running"},
			{Name: "feature/login", Ref: "evilref004", Status: "Running"},
			{Name: "evil\x1b]0;owned\a", Ref: "evilref005", Status: "Running"},
			{Name: "main.", Ref: "evilref006", Status: "Running"},
			{Name: "csi\x9b31m", Ref: "evilref007", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", "android"))
	require.Empty(t, entriesIn(t, "gradle"), "a listed name wrote into the checkout's own gradle/")
	require.NoFileExists(t, filepath.Join(RootDir(), "openapi.json"), "`..` wrote the retired layout's marker")
	require.NoDirExists(t, filepath.Join(outside, "outside"), "a listed name wrote outside the checkout")
	require.Equal(t, []string{"main"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)))
	require.Contains(t, out.String(), `skipped environment "../../gradle" (evilref001): the name contains a path separator, so it cannot be a directory under palbase/environments — rename it in the dashboard`)
	require.Contains(t, out.String(), `skipped environment ".." (evilref002): the name is "." or ".."`)
	require.Contains(t, out.String(), `skipped environment "evil\x1b]0;owned\a" (evilref005): the name contains the non-printing character U+001B`)
	require.Contains(t, out.String(), `skipped environment "main." (evilref006): the name ends with a dot or a space, which Windows drops`)
	require.Contains(t, out.String(), `skipped environment "csi\x9b31m" (evilref007): the name is not UTF-8`)
	require.NotContains(t, out.String(), "\x1b", "a listed name reached the terminal raw")
	require.NotContains(t, out.String(), "\x9b", "a listed name reached the terminal raw")
}

// THE ENVIRONMENT THIS LINK READS FROM CANNOT BE SKIPPED: it is what the app
// builds against when nothing else is chosen. The link refuses, names the fix,
// and writes nothing.
func TestLinkRefusesWhenTheEnvironmentItReadsFromIsNotOneDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	require.NoError(t, os.MkdirAll("gradle", 0o755))
	evil := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"evilref001": evil.URL})
	o := linkOpts{
		url:          evil.URL,
		platforms:    []string{"android"},
		linkedEnv:    "../../gradle",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "../../gradle", Ref: "evilref001", Status: "Running"}},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment "../../gradle" (evilref001) is the one this link reads from, and the name contains a path separator`)
	require.Contains(t, err.Error(), "rename it in the dashboard, or read from another environment with `palbase link --from-env <name>`")
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
	require.Empty(t, entriesIn(t, "gradle"), "the refused link still wrote into gradle/")
}
