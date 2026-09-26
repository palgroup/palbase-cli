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

// TWO NAMES, ONE DIRECTORY (FR-003). On APFS `Staging` and `staging` are the
// same directory, so the second write landed inside the first one's and the
// app built one environment's address under the other's name. Neither is the
// environment this link reads from, so both are left out, each by its ref.
func TestLinkSkipsEnvironmentsWhoseNamesShareOneDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	main := stackServing(t, linkKeyMain, nil)
	upper := stackServing(t, linkKeyStaging, nil)
	lower := stackServing(t, linkKeyCanary, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagAref00": upper.URL, "stagBref00": lower.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "Staging", Ref: "stagAref00", Status: "Running"},
			{Name: "staging", Ref: "stagBref00", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Equal(t, []string{"main"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)))
	require.Contains(t, out.String(), `skipped environments "Staging" (stagAref00) and "staging" (stagBref00): `+
		`their names match when letter case and Unicode form are ignored, so they would share one directory — `+
		`rename one in the dashboard`)
}

// TWO SPELLINGS OF ONE NAME, ONE DIRECTORY. APFS ignores Unicode normalisation
// as it ignores case, so `café` composed and `café` decomposed are the same
// directory: the second write landed in the first one's, and the app built one
// environment's address under the other's name — FR-003's failure by another
// road. Measured: `wrote palbase/environments/café/android-config.json` twice.
func TestLinkSkipsEnvironmentsWhoseNamesDifferOnlyInUnicodeNormalisation(t *testing.T) {
	const composed, decomposed = "caf\u00e9", "cafe\u0301"
	inScratchCheckout(t)
	seedAndroidApp(t)
	main := stackServing(t, linkKeyMain, nil)
	nfc := stackServing(t, linkKeyStaging, nil)
	nfd := stackServing(t, linkKeyCanary, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "nfcref0000": nfc.URL, "nfdref0000": nfd.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: composed, Ref: "nfcref0000", Status: "Running"},
			{Name: decomposed, Ref: "nfdref0000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Equal(t, []string{"main"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)), out.String())
	require.Contains(t, out.String(), "skipped environments \""+composed+"\" (nfcref0000) and \""+decomposed+"\" (nfdref0000): "+
		"their names match when letter case and Unicode form are ignored, so they would share one directory — rename one in the dashboard")
}

// AND WHEN ONE OF THEM IS THE ENVIRONMENT THIS LINK READS FROM, THE LINK STOPS
// (D-012). Picking one silently is how `link` wrote environment B while `push
// --env main` deployed to A — measured with two environments both listed as
// `main`.
func TestLinkRefusesWhenTheEnvironmentItReadsFromSharesItsDirectory(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	first := stackServing(t, linkKeyMain, nil)
	second := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"aaaa1111": first.URL, "bbbb2222": second.URL})
	o := linkOpts{
		url:       first.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "aaaa1111", Status: "Running"},
			{Name: "main", Ref: "bbbb2222", Status: "Running"},
		},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environments "main" (aaaa1111) and "main" (bbbb2222) match when letter case and `+
		`Unicode form are ignored, so they would share one directory, and "main" is the one this link reads from — `+
		`rename one in the dashboard`)
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
}

// SKIPPED IS NOT GONE. The Apple sweep deletes the directory of an environment
// the project no longer lists; a twin is still listed, so its committed files
// stay exactly where they are. The sweep is handed the listing as the cloud
// sent it, not what this link could write.
func TestAnAppleLinkKeepsTheFilesOfASkippedTwin(t *testing.T) {
	inScratchCheckout(t)
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)
	root, err := os.Getwd()
	require.NoError(t, err)
	twin := seedGeneratedEnvironment(t, root, "Staging")
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"ios"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "Staging", Ref: "stagAref00", Status: "Running"},
			{Name: "staging", Ref: "stagBref00", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, filepath.Join(twin, "PalbaseGenerated.swift"),
		"the sweep deleted the committed files of an environment the project still lists")
}

// `local/` BELONGS TO THIS MACHINE (FR-004). A cloud environment named `Local`
// used to be written there — and silently replaced by this machine's stack
// whenever one was registered, or left beside the stack's config when it was
// not. It is left out and said, whatever its case.
func TestLinkSkipsACloudEnvironmentNamedLocal(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	main := stackServing(t, linkKeyMain, nil)
	cloud := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "locref0000": cloud.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "Local", Ref: "locref0000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Equal(t, []string{"main"}, entriesIn(t, filepath.Join(RootDir(), envSubdir)))
	require.Contains(t, out.String(), `skipped environment "Local" (locref0000): palbase/environments/local belongs to `+
		"the stack `palbase start` runs on this machine, never to a cloud environment — rename it in the dashboard")
}

func TestLinkRefusesWhenTheEnvironmentItReadsFromIsNamedLocal(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	cloud := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"locref0000": cloud.URL})
	o := linkOpts{
		url:          cloud.URL,
		platforms:    []string{"android"},
		linkedEnv:    "local",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "local", Ref: "locref0000", Status: "Running"}},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment "local" (locref0000) is the one this link reads from, and palbase/environments/local belongs to `+
		"the stack `palbase start` runs on this machine")
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
}

// blockEnvironmentDir puts a FILE where an environment's directory goes, so
// nothing can be written for it — the way an unwritable name used to reach the
// disk (`../../settings.gradle.kts` resolved to an existing file).
func blockEnvironmentDir(t *testing.T, env string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(RootDir(), envSubdir), 0o755))
	require.NoError(t, os.WriteFile(filepath.FromSlash(EnvDir(env)), []byte("in the way\n"), 0o644))
}

// ONE ENVIRONMENT'S DISK DOES NOT DECIDE THE OTHERS' (FR-005). A write that
// failed for an environment this link does not read from failed the whole
// link — so one directory nobody could create stopped every teammate's link.
func TestLinkGoesOnWhenAnotherEnvironmentCannotBeWritten(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	blockEnvironmentDir(t, "staging")
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", "android"))
	require.FileExists(t, SpecPath("main"))
	require.Contains(t, out.String(), "staging could not be written (mkdir palbase/environments/staging: not a directory) — "+
		"skipped; run `palbase link` again once it can be")
	blocker, err := os.ReadFile(filepath.FromSlash(EnvDir("staging")))
	require.NoError(t, err)
	require.Equal(t, "in the way\n", string(blocker), "the link wrote over what stood in staging's way")
}

// THE ENVIRONMENT THIS LINK READS FROM STILL FAILS IT: an app whose default has
// no config does not build.
func TestLinkFailsWhenTheEnvironmentItReadsFromCannotBeWritten(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	blockEnvironmentDir(t, "main")
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), "main could not be written: mkdir palbase/environments/main: not a directory")
}
