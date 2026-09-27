package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const linkKeyLocal = "pb_local_cL4Kj1fDyNSboIH7H7mZYI8INFW5NpdX3"

// startedAs registers a stack the way `palbase start` does in a backend
// checkout whose group is `group`, and gives this machine the key it holds.
func startedAs(t *testing.T, group string) string {
	t.Helper()
	local := stackServing(t, linkKeyLocal, nil)
	require.NoError(t, registerStack(group, local.URL, "palbase-"+group, "/elsewhere/backend"))
	require.NoError(t, StoreCredential(local.URL, Credentials{Value: "local-key", Kind: KindKey}))
	return local.URL
}

// productLink is an Android app checkout linked to the project `name`, whose one
// environment `main` answers every read.
func productLink(t *testing.T, name string) linkOpts {
	t.Helper()
	seedAndroidApp(t)
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	return linkOpts{
		url:          main.URL,
		platforms:    []string{"android"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: name},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}
}

// THE STACK IS FOUND UNDER THE GROUP `palbase start` REGISTERED IT UNDER (FR-008).
//
// `start` names its stack after the project its checkout is linked to
// (start.go groupName); an app checkout lives in a directory of its own name.
// Looking the stack up by that directory found it only when the two happened to
// share a name — measured on 0.71.2: registry group `todoapp`, app checkout
// `MyApp`, and link wrote main/ with no local/ at all.
func TestAnAppCheckoutFindsTheStackStartedForItsProject(t *testing.T) {
	inScratchCheckout(t)
	localURL := startedAs(t, sanitiseGroup("Todo App"))
	o := productLink(t, "Todo App")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	cfg := readEnvConfig(t, localEnvName, "android")
	assert.Equal(t, localURL, cfg.BaseURL)
	assert.Equal(t, linkKeyLocal, cfg.APIKey)
	assert.FileExists(t, SpecPath(localEnvName))
}

// AND UNDER THE CHECKOUT'S OWN DIRECTORY NAME WHEN THE PROJECT'S FINDS NONE:
// that is what `start` registers in a checkout that is not linked, and what a
// monorepo's app and backend share.
func TestAnAppCheckoutStillFindsAStackRegisteredUnderItsDirectoryName(t *testing.T) {
	inScratchCheckout(t)
	dir, err := os.Getwd()
	require.NoError(t, err)
	localURL := startedAs(t, sanitiseGroup(filepath.Base(dir)))
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Equal(t, localURL, readEnvConfig(t, localEnvName, "android").BaseURL)
}

// NEITHER, AND THIS MACHINE RUNS OTHER STACKS: link names them, because the
// person who started one expects local/ and would otherwise get silence.
func TestALinkThatFindsNoStackNamesTheOnesThisMachineRuns(t *testing.T) {
	inScratchCheckout(t)
	startedAs(t, "billing")
	startedAs(t, "todo-backend")
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.NoDirExists(t, EnvDir(localEnvName))
	dir, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, out.String(), "local: no stack on this machine is registered as todoapp or "+
		sanitiseGroup(filepath.Base(dir))+" (registered: billing, todo-backend) — local/ is not written; "+
		"`palbase start` registers a stack under the name of the project its checkout is linked to, "+
		"or under its directory's name when that checkout is not linked")
}

// A MACHINE THAT RUNS NO STACK IS A CLOUD-ONLY SETUP, not a mistake: nothing is said.
func TestALinkOnAMachineThatRunsNoStackSaysNothingAboutLocal(t *testing.T) {
	inScratchCheckout(t)
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.NoDirExists(t, EnvDir(localEnvName))
	assert.NotContains(t, out.String(), "local:")
}
