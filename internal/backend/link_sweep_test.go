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
