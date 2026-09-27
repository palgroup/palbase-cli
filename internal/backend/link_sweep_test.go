package backend

// link_sweep_test.go — AN ENVIRONMENT THE PROJECT NO LONGER HAS LEAVES THE
// CHECKOUT, whichever client reads it (FR-011).
//
// The sweep ran for Apple alone, so an Android or web checkout kept the
// directory of a deleted or renamed environment for ever. Measured (verification
// of 2026-09-25, B4): a `featurex/` whose tenant was gone survived an Android
// link without a word, and a build type named featureX went on compiling it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedGeneratedEnvironment leaves the generated Swift client of one
// environment in the checkout, the way an earlier Apple link did.
func seedGeneratedEnvironment(t *testing.T, root, env string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(EnvDir(env)))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "PalbaseGenerated.swift"), []byte("// generated"), 0o644))
	return dir
}

// seedEnvironment commits one environment's files for one platform, the way an
// earlier link left them.
func seedEnvironment(t *testing.T, env, platform string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(EnvDir(env), 0o755))
	require.NoError(t, os.WriteFile(SpecPath(env), []byte(`{"openapi":"3.1.0","paths":{}}`), 0o644))
	require.NoError(t, os.WriteFile(ConfigPath(env, platform), []byte(`{"base_url":"https://old.example"}`+"\n"), 0o600))
}

// sweepCheckouts are the three clients that write per-environment files, each
// set up the way its own link tests set it up.
var sweepCheckouts = []struct {
	platform string
	seed     func(t *testing.T)
	// leftover is what the line about a directory the sweep must not delete
	// tells THIS checkout's reader: Xcode compiles every directory, a Gradle or
	// web build only the one it selects.
	leftover string
}{
	{"ios", func(t *testing.T) { useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil) },
		`Xcode compiles everything under palbase/environments, so move it aside if the build reports "Multiple commands produce"`},
	{"android", seedAndroidApp,
		"a build that selects it still builds an environment this project no longer has, so move it aside"},
	{webPlatform, func(t *testing.T) { seedWebCheckout(t); installStubCodegen(t, "export {}") },
		"a build that selects it still builds an environment this project no longer has, so move it aside"},
}

// ONLY WHAT THE PROJECT NO LONGER HAS GOES. `staging` is listed but cannot be
// read this run, `broken` is Failed, `local/` is this machine's stack while it
// is down, and `mine/` holds a file Palbase never writes: all four stay.
// `featurex` is listed nowhere, and it goes — said, by its path in the checkout.
func TestALinkRemovesOnlyTheEnvironmentsTheProjectNoLongerHas(t *testing.T) {
	for _, c := range sweepCheckouts {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			c.seed(t)
			for _, env := range []string{"featurex", "staging", "broken", localEnvName} {
				seedEnvironment(t, env, c.platform)
			}
			require.NoError(t, os.MkdirAll(EnvDir("mine"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(EnvDir("mine"), "NOTES.md"), []byte("mine\n"), 0o644))
			main := stackServing(t, linkKeyMain, nil)
			routeEnvironments(t, map[string]string{"mainref000": main.URL}) // staging has no route: it cannot be read
			o := linkOpts{
				url:       main.URL,
				platforms: []string{c.platform},
				linkedEnv: "main",
				product:   Product{ID: "prd_a", Name: "todoapp"},
				environments: []Environment{
					{Name: "main", Ref: "mainref000", Status: "Running"},
					{Name: "staging", Ref: "stagref000", Status: "Running"},
					{Name: "broken", Ref: "brokref000", Status: "Failed"},
				},
			}

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.Equal(t, []string{"broken", localEnvName, "main", "mine", "staging"},
				entriesIn(t, filepath.Join(RootDir(), envSubdir)))
			assert.Contains(t, out.String(), "removed palbase/environments/featurex (the project no longer has that environment)\n")
			assert.Contains(t, out.String(), "palbase/environments/mine belongs to no environment in this project and holds "+
				"files Palbase did not write — "+c.leftover+"\n")
			assert.FileExists(t, filepath.Join(EnvDir("mine"), "NOTES.md"))
		})
	}
}

// A LINK THAT READ NO LISTING TAKES NOTHING AWAY from an Android checkout. A
// stack linked by its address has one environment and no list of the others,
// so nothing here says `staging` is gone — and deleting a project's committed
// environments because this one link went to a bare address would be the worse
// mistake. `main/` stays too while its address is somebody's stack, not this
// machine's. (Apple still sweeps here: Xcode compiles every directory under
// palbase/environments, so for it a leftover is a broken build.)
func TestALinkWithNoProjectListingRemovesNoAndroidEnvironment(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	seedEnvironment(t, "staging", "android")
	seedEnvironment(t, "main", "android")
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())

	assert.FileExists(t, ConfigPath("staging", "android"))
	assert.FileExists(t, ConfigPath("main", "android"))
	assert.NotContains(t, out.String(), "removed ")
}

// seedLoopbackMain is what a link to the stack on this machine wrote before
// that stack was named `local`: main/, carrying its loopback address.
func seedLoopbackMain(t *testing.T, platform, url string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", platform),
		[]byte(`{"app_id":"project","base_url":"`+url+`","api_key":"`+linkKeyMain+`"}`+"\n"), 0o600))
	require.NoError(t, os.WriteFile(SpecPath("main"), []byte(`{"openapi":"3.1.0","paths":{}}`), 0o644))
}

// A LOOPBACK main/ AN OLDER LINK LEFT IS THIS MACHINE'S STACK UNDER THE WRONG
// NAME (FR-010 × FR-011). Before the stack `palbase start` runs here was named
// `local`, a link to it wrote main/ with 127.0.0.1 in it; now that stack is
// local/, and the old main/ is what a release mapped to main would ship. Apple
// swept it already; Android and web kept it, because a link with no listing
// swept nothing there.
func TestALinkToThisMachinesStackTakesAwayTheLoopbackMainAnOlderLinkLeft(t *testing.T) {
	for _, c := range sweepCheckouts {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			c.seed(t)
			stack := stackServing(t, linkKeyMain, nil)
			linkedAs(t, stack.URL, "a-credential")
			seedLoopbackMain(t, c.platform, stack.URL)

			var out strings.Builder
			o := linkOpts{url: stack.URL, platforms: []string{c.platform}}
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.FileExists(t, ConfigPath(localEnvName, c.platform))
			assert.NoDirExists(t, EnvDir("main"), "main/ still carries a loopback base_url:\n%s", out.String())
			assert.Contains(t, out.String(), "removed palbase/environments/main (it held the stack on this machine, which is local/ now)\n")
		})
	}
}

// AND ONE THAT HOLDS A FILE PALBASE NEVER WRITES IS SAID AND LEFT: whatever the
// address in it, the file is somebody's.
func TestAnOldLoopbackMainHoldingSomebodysFileIsSaidAndLeft(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")
	seedLoopbackMain(t, "android", stack.URL)
	require.NoError(t, os.WriteFile(filepath.Join(EnvDir("main"), "NOTES.md"), []byte("mine\n"), 0o644))

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL, platforms: []string{"android"}}, &out), out.String())

	assert.FileExists(t, filepath.Join(EnvDir("main"), "NOTES.md"))
	assert.Contains(t, out.String(), "palbase/environments/main holds the stack on this machine under the name an older link "+
		"gave it, and files Palbase did not write — move them aside, then delete it\n")
}

// namedLikeOurs are entry names the CLI writes as FILES; as a directory, each
// is somebody else's.
var namedLikeOurs = []string{"openapi.json", "x.ts"}

// A DIRECTORY NAMED LIKE ONE OF OUR FILES IS NOT OURS (T014 review). The
// ownership test read names only, so `featurex/openapi.json/` passed as the
// contract and the sweep deleted the files somebody kept inside it. Anything
// that is not a regular file makes the directory foreign: said, and left.
func TestASweepLeavesAnEnvironmentHoldingADirectoryNamedLikeOurs(t *testing.T) {
	for _, c := range sweepCheckouts {
		for _, name := range namedLikeOurs {
			t.Run(c.platform+"/"+name, func(t *testing.T) {
				inScratchCheckout(t)
				c.seed(t)
				require.NoError(t, os.MkdirAll(filepath.Join(EnvDir("featurex"), name), 0o755))
				theirs := filepath.Join(EnvDir("featurex"), name, "user.txt")
				require.NoError(t, os.WriteFile(theirs, []byte("mine\n"), 0o644))
				require.NoError(t, os.WriteFile(ConfigPath("featurex", c.platform), []byte(`{"base_url":"https://old.example"}`+"\n"), 0o600))
				main := stackServing(t, linkKeyMain, nil)
				routeEnvironments(t, map[string]string{"mainref000": main.URL})
				o := linkOpts{url: main.URL, platforms: []string{c.platform}, linkedEnv: "main",
					product:      Product{ID: "prd_a", Name: "todoapp"},
					environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}}}

				var out strings.Builder
				require.NoError(t, runLink(context.Background(), o, &out), out.String())

				assert.FileExists(t, theirs, "the sweep deleted a file somebody kept in a directory named like ours")
				assert.Contains(t, out.String(), "palbase/environments/featurex belongs to no environment in this project and holds "+
					"files Palbase did not write — "+c.leftover+"\n")
				assert.NotContains(t, out.String(), "removed palbase/environments/featurex")
			})
		}
	}
}

// AND THE SAME FOR THE OLD LOOPBACK main/: a directory in it named like one of
// our files is somebody's, whatever address the config beside it carries.
func TestAnOldLoopbackMainHoldingADirectoryNamedLikeOursIsSaidAndLeft(t *testing.T) {
	for _, name := range namedLikeOurs {
		t.Run(name, func(t *testing.T) {
			inScratchCheckout(t)
			seedAndroidApp(t)
			stack := stackServing(t, linkKeyMain, nil)
			linkedAs(t, stack.URL, "a-credential")
			seedLoopbackMain(t, "android", stack.URL)
			require.NoError(t, os.Remove(SpecPath("main")))
			require.NoError(t, os.MkdirAll(filepath.Join(EnvDir("main"), name), 0o755))
			theirs := filepath.Join(EnvDir("main"), name, "user.txt")
			require.NoError(t, os.WriteFile(theirs, []byte("mine\n"), 0o644))

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL, platforms: []string{"android"}}, &out), out.String())

			assert.FileExists(t, theirs, "the old main/ went with a file somebody kept in it")
			assert.Contains(t, out.String(), "palbase/environments/main holds the stack on this machine under the name an older link "+
				"gave it, and files Palbase did not write — move them aside, then delete it\n")
			assert.NotContains(t, out.String(), "removed palbase/environments/main")
		})
	}
}

// A FILE NAMED main IS NOT AN OLD main/ (T014 review). Reading it as a
// directory failed with "not a directory", and the link to this machine's
// stack failed with it — over a file the link has no business with.
func TestAFileNamedMainDoesNotStopALinkToThisMachinesStack(t *testing.T) {
	for _, c := range sweepCheckouts {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			c.seed(t)
			stack := stackServing(t, linkKeyMain, nil)
			linkedAs(t, stack.URL, "a-credential")
			// The web seed leaves a main/ of its own; here the name is a file's.
			require.NoError(t, os.RemoveAll(EnvDir("main")))
			require.NoError(t, os.MkdirAll(filepath.Join(RootDir(), envSubdir), 0o755))
			require.NoError(t, os.WriteFile(filepath.FromSlash(EnvDir("main")), []byte("mine\n"), 0o644))

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL, platforms: []string{c.platform}}, &out), out.String())

			assert.FileExists(t, ConfigPath(localEnvName, c.platform))
			raw, err := os.ReadFile(filepath.FromSlash(EnvDir("main")))
			require.NoError(t, err)
			assert.Equal(t, "mine\n", string(raw), "the link touched a file named main")
		})
	}
}

// NOR IS A SYMLINK NAMED main: the old main/ is read through no link. The
// link's stage refuses a symlink under palbase/ before this function runs, so
// this pins the function's own guard, on its own.
func TestTheOldMainIsNeverReadThroughASymlink(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "android-config.json"),
		[]byte(`{"app_id":"project","base_url":"http://127.0.0.1:54321","api_key":"`+linkKeyMain+`"}`+"\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, RootDir(), envSubdir), 0o755))
	link := filepath.Join(root, filepath.FromSlash(EnvDir("main")))
	require.NoError(t, os.Symlink(elsewhere, link))

	var out strings.Builder
	require.NoError(t, removeThisMachinesOldMain(root, &out))

	info, err := os.Lstat(link)
	require.NoError(t, err, "the symlink named main was removed")
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	assert.FileExists(t, filepath.Join(elsewhere, "android-config.json"))
	assert.Empty(t, out.String())
}

// AN EMPTY DIRECTORY IS LEFT, AND NOTHING IS SAID ABOUT IT (T014 review). The
// link's stage removed it and said so, but publishing carries file changes
// only: the real directory stayed, and every link said "removed" again.
func TestASweepSaysNothingAboutAnEmptyDirectoryAndLeavesIt(t *testing.T) {
	for _, c := range sweepCheckouts {
		t.Run(c.platform, func(t *testing.T) {
			inScratchCheckout(t)
			c.seed(t)
			require.NoError(t, os.MkdirAll(EnvDir("featurex"), 0o755))
			main := stackServing(t, linkKeyMain, nil)
			routeEnvironments(t, map[string]string{"mainref000": main.URL})
			o := linkOpts{url: main.URL, platforms: []string{c.platform}, linkedEnv: "main",
				product:      Product{ID: "prd_a", Name: "todoapp"},
				environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}}}

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.DirExists(t, EnvDir("featurex"))
			assert.NotContains(t, out.String(), "palbase/environments/featurex")
		})
	}
}

// THE OLD main/ GOES ONLY WHEN EVERY CONFIG IN IT IS THIS MACHINE'S. One
// loopback config beside one that points at the cloud, says nothing, says it
// in a shape no parser reads, or is not JSON at all, is not this machine's
// stack: the whole directory stays, untouched and unmentioned. The loopback
// config sorts first, so each case measures a veto that comes after a match.
func TestAnOldMainStaysUnlessEveryConfigInItIsThisMachines(t *testing.T) {
	for _, c := range []struct{ name, config string }{
		{"a cloud address", `{"app_id":"app_real","base_url":"https://abcd1234.cloud.example","api_key":"pb_project_cCLOUDKEY"}` + "\n"},
		{"no base_url", `{"app_id":"project","api_key":"` + linkKeyMain + `"}` + "\n"},
		{"a base_url no parser reads", `{"app_id":"project","base_url":"http://127.0.0.1:%zz","api_key":"` + linkKeyMain + `"}` + "\n"},
		{"not JSON", "base_url = http://127.0.0.1\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			inScratchCheckout(t)
			seedAndroidApp(t)
			stack := stackServing(t, linkKeyMain, nil)
			linkedAs(t, stack.URL, "a-credential")
			seedLoopbackMain(t, "android", stack.URL)
			require.NoError(t, os.WriteFile(ConfigPath("main", webPlatform), []byte(c.config), 0o600))
			loopback, err := os.ReadFile(ConfigPath("main", "android"))
			require.NoError(t, err)

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL, platforms: []string{"android"}}, &out), out.String())

			for path, want := range map[string]string{ConfigPath("main", "android"): string(loopback), ConfigPath("main", webPlatform): c.config} {
				raw, err := os.ReadFile(path)
				require.NoError(t, err, "%s is gone:\n%s", path, out.String())
				assert.Equal(t, want, string(raw), path)
			}
			assert.NotContains(t, out.String(), "removed palbase/environments/main")
			assert.NotContains(t, out.String(), "palbase/environments/main holds")
		})
	}
}

// A LISTED ENVIRONMENT WHOSE WRITE WAS PUT BACK IS STILL THE PROJECT'S (FR-005
// × FR-011). `staging`'s contract cannot be written, so its config goes back
// to what it was (T004) — and the sweep that runs after the writes must not
// read "not written this run" as "no longer the project's". Its `openapi.json/`
// is a directory, so a sweep that forgot the listing would name it foreign.
func TestAListedEnvironmentWhoseWriteWasPutBackSurvivesTheSweep(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	before := []byte(`{"app_id":"project","base_url":"https://old","api_key":"` + linkKeyStaging + `"}` + "\n")
	require.NoError(t, os.MkdirAll(EnvDir("staging"), 0o755))
	require.NoError(t, os.WriteFile(filepath.FromSlash(ConfigPath("staging", "android")), before, 0o600))
	blockSpecPath(t, "staging")
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	o := linkOpts{url: main.URL, platforms: []string{"android"}, linkedEnv: "main",
		product: Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		}}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	require.Contains(t, out.String(), "staging could not be written", "the write did not fail — this test measures nothing")
	after, err := os.ReadFile(filepath.FromSlash(ConfigPath("staging", "android")))
	require.NoError(t, err, "the sweep took a listed environment whose write was put back:\n%s", out.String())
	assert.Equal(t, string(before), string(after))
	assert.NotContains(t, out.String(), "removed palbase/environments/staging")
	assert.NotContains(t, out.String(), "palbase/environments/staging belongs to no environment")
}

// fileBrowserNoise is what Finder and Explorer leave in any directory a person
// opens in them.
var fileBrowserNoise = []string{".DS_Store", "Thumbs.db"}

// A FILE BROWSER'S LEFTOVER PROTECTS NOTHING (final review, Minor #4). A Mac
// developer who once opened `featurex/` in Finder left a `.DS_Store` in it, and
// every link from then on printed "holds files Palbase did not write — move it
// aside" and kept the directory for ever. It goes with the directory, from the
// checkout too.
func TestALinkRemovesAStaleEnvironmentAFileBrowserLeftItsFileIn(t *testing.T) {
	for _, noise := range fileBrowserNoise {
		t.Run(noise, func(t *testing.T) {
			inScratchCheckout(t)
			seedAndroidApp(t)
			seedEnvironment(t, "featurex", "android")
			require.NoError(t, os.WriteFile(filepath.Join(EnvDir("featurex"), noise), []byte("noise"), 0o644))
			main := stackServing(t, linkKeyMain, nil)
			routeEnvironments(t, map[string]string{"mainref000": main.URL})
			o := linkOpts{url: main.URL, platforms: []string{"android"}, linkedEnv: "main",
				product:      Product{ID: "prd_a", Name: "todoapp"},
				environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}}}

			var out strings.Builder
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.NoDirExists(t, EnvDir("featurex"), out.String())
			assert.Contains(t, out.String(), "removed palbase/environments/featurex (the project no longer has that environment)\n")
			assert.NotContains(t, out.String(), "Palbase did not write")
		})
	}
}

// AND THE OLD LOOPBACK main/ THE SAME WAY: a file browser's leftover is not a
// file somebody wrote.
func TestAnOldLoopbackMainAFileBrowserLeftItsFileInIsRemoved(t *testing.T) {
	for _, noise := range fileBrowserNoise {
		t.Run(noise, func(t *testing.T) {
			inScratchCheckout(t)
			seedLoopbackMain(t, "android", "http://127.0.0.1:54321")
			require.NoError(t, os.WriteFile(filepath.Join(EnvDir("main"), noise), []byte("noise"), 0o644))

			var out strings.Builder
			require.NoError(t, removeThisMachinesOldMain(".", &out))

			assert.NoDirExists(t, EnvDir("main"))
			assert.Equal(t, "removed palbase/environments/main (it held the stack on this machine, which is local/ now)\n", out.String())
		})
	}
}
