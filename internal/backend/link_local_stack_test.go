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

// fillLocalAdvice ends every line that leaves local/ unwritten (FR-009): the
// stack is brought up where it lives, and THIS checkout is linked again — the
// only verb that writes a config. `palbase spec` writes a contract alone.
const fillLocalAdvice = "— nothing is written for local; `palbase start`, then `palbase link` here"

// LOCAL IS BOTH FILES OR NEITHER (FR-009). A stack that is registered but does
// not answer used to get a keyless android-config.json and no contract: a
// committed file that fails every teammate's debug build, and a "fill it in
// with `palbase spec`" that could not fill a config in.
func TestAStoppedLocalStackGetsNoLocalFiles(t *testing.T) {
	inScratchCheckout(t)
	const stopped = "http://127.0.0.1:1"
	require.NoError(t, registerStack("todoapp", stopped, "palbase-todoapp", "/elsewhere/backend"))
	require.NoError(t, StoreCredential(stopped, Credentials{Value: "local-key", Kind: KindKey}))
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.FileExists(t, ConfigPath("main", "android"))
	assert.NoDirExists(t, EnvDir(localEnvName))
	assert.Contains(t, out.String(), "local: "+stopped+" did not give its key (")
	assert.Contains(t, out.String(), fillLocalAdvice)
}

// A STACK THIS MACHINE HOLDS NO CREDENTIAL FOR gives no key: the same neither.
func TestALocalStackWithNoCredentialGetsNoLocalFiles(t *testing.T) {
	inScratchCheckout(t)
	local := stackServing(t, linkKeyLocal, nil)
	require.NoError(t, registerStack("todoapp", local.URL, "palbase-todoapp", "/elsewhere/backend"))
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.NoDirExists(t, EnvDir(localEnvName))
	assert.Contains(t, out.String(), "local: "+local.URL+
		" is registered but this machine holds no credential for it "+fillLocalAdvice)
}

// A KEY WITHOUT A CONTRACT IS HALF OF LOCAL, and half is what the plugin refuses
// ("inputs are incomplete"): neither file is written.
func TestALocalStackWithNoContractGetsNoLocalFiles(t *testing.T) {
	inScratchCheckout(t)
	local, _ := envServer(t, linkKeyLocal, envServerOpts{noContract: true, socialAuth: true})
	require.NoError(t, registerStack("todoapp", local.URL, "palbase-todoapp", "/elsewhere/backend"))
	require.NoError(t, StoreCredential(local.URL, Credentials{Value: "local-key", Kind: KindKey}))
	o := productLink(t, "todoapp")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.NoDirExists(t, EnvDir(localEnvName))
	// THE STACK'S WORDS, AND ONE CURE AFTER THEM (FR-014, D-027): its own step
	// used to ride inside the parentheses — "(palbase push)", which that stack
	// refuses — ahead of fillLocalAdvice's `palbase start`.
	assert.Contains(t, out.String(), "local: "+local.URL+" gave its key but not its contract (no contract yet: "+
		local.URL+" has nothing to describe yet) "+fillLocalAdvice)
	assert.NotContains(t, out.String(), "(palbase push)")
}

// ONE NAME FOR THE STACK ON THIS MACHINE, WHICHEVER VERB WRITES IT (FR-010).
//
// Measured on 0.71.2 in one checkout with a start record: `palbase link` wrote
// main/android-config.json, `palbase spec` wrote local/openapi.json — the build
// found half of each. And main/ carried a loopback address, so mapping release
// to main shipped a release that talks to 127.0.0.1 in cleartext.
func TestTheStackStartedHereIsLocalToLinkAndSpecAlike(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	stack, _, _ := startStackHere(t)

	o := linkOpts{}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	cfg := readEnvConfig(t, localEnvName, "android")
	assert.Equal(t, stack.URL, cfg.BaseURL)
	assert.Equal(t, linkKeyMain, cfg.APIKey)
	assert.NoDirExists(t, EnvDir("main"), "a stack on this machine was written as main/")

	var spec strings.Builder
	require.NoError(t, RefreshSpec(context.Background(), &spec), spec.String())
	assert.Contains(t, spec.String(), "✓ wrote palbase/environments/local/openapi.json (")
	assert.NoDirExists(t, EnvDir("main"))
}

// A LOOPBACK ADDRESS IS THIS MACHINE TOO, however the stack got there: linked
// by hand (`palbase link http://localhost:54321`, the documented flow) it was
// main/ to link and to spec alike — the same loopback main/.
func TestALoopbackAddressIsLocalToLinkAndSpecAlike(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	t.Setenv("PALBASE_ENV", "")
	stack := stackServing(t, linkKeyMain, nil)
	linkedAs(t, stack.URL, "a-credential")

	o := linkOpts{url: stack.URL}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Equal(t, stack.URL, readEnvConfig(t, localEnvName, "android").BaseURL)
	assert.NoDirExists(t, EnvDir("main"), "a loopback address was written as main/")

	var spec strings.Builder
	require.NoError(t, RefreshSpec(context.Background(), &spec), spec.String())
	assert.Contains(t, spec.String(), "✓ wrote palbase/environments/local/openapi.json (")
	assert.NoDirExists(t, EnvDir("main"))
}

// `palbase spec` ON THE STACK ON THIS MACHINE, WITH NOTHING TO DESCRIBE, NAMES A
// START (D-027, T018 fix round 2). It said "push a backend to it first (palbase
// push)" — and `palbase push` refuses that stack (stack_push.go): it serves the
// directory `palbase start` mounted, so a start is what gives it a contract.
// Whether the stack is `start`'s record or a loopback address linked by hand,
// it is `local` to every verb (stackEnvName), and so is its cure.
func TestSpecOnTheStackOnThisMachineWithNoContractNamesAStart(t *testing.T) {
	for _, c := range []struct {
		name string
		bind func(t *testing.T, url string)
	}{
		{"the stack palbase start runs", func(t *testing.T, url string) {
			require.NoError(t, WriteLocalTarget(Target{URL: url}))
		}},
		{"a loopback address linked by hand", func(t *testing.T, url string) {
			o := linkOpts{url: url}
			require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
			var out strings.Builder
			require.NoError(t, runLink(context.Background(), o, &out), out.String())
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			inScratchCheckout(t)
			seedAndroidApp(t)
			t.Setenv("PALBASE_ENV", "")
			stack, _ := envServer(t, linkKeyMain, envServerOpts{noContract: true, socialAuth: true})
			linkedAs(t, stack.URL, "a-credential")
			c.bind(t, stack.URL)

			var spec strings.Builder
			err := RefreshSpec(context.Background(), &spec)
			require.ErrorIs(t, err, ErrNoContractYet, spec.String())
			assert.Equal(t, "no contract yet: "+stack.URL+" has nothing to describe yet — run `palbase start` in the "+
				"backend (its stack serves the contract of the code it runs), then `palbase spec` here", err.Error())
			assert.NotContains(t, err.Error()+spec.String(), "palbase push")
		})
	}
}

// AND NOTHING ELSE IS: a stack somebody hosts keeps `main`, a project's
// environment keeps its own name, and a start record is this machine whatever
// address it announces (`palbase start --lan` records the LAN one).
func TestOnlyAStackOnThisMachineIsNamedLocal(t *testing.T) {
	for _, c := range []struct {
		resolved Resolved
		want     string
	}{
		{Resolved{URL: "https://stack.example.com"}, "main"},
		{Resolved{URL: "http://192.168.1.20:54321"}, "main"},
		{Resolved{URL: "http://localhost:54321"}, localEnvName},
		{Resolved{URL: "http://127.0.0.1:54321"}, localEnvName},
		{Resolved{URL: "http://[::1]:54321"}, localEnvName},
		{Resolved{Target: Target{Local: true}, URL: "http://192.168.1.20:54321"}, localEnvName},
		{Resolved{Env: "staging", URL: "http://127.0.0.1:54321"}, "staging"},
	} {
		assert.Equal(t, c.want, c.resolved.ArtifactEnv(), "%+v", c.resolved)
	}
}

// A LAN START RECORD IS THIS MACHINE TOO, for a PROJECTLESS LINK exactly as it
// is for `ArtifactEnv` (T011 review round 1, MINOR #2a).
//
// `stackEnvName`'s `Local` field, for the projectless branch of a link, comes
// only from `sameStack(running.URL, base)` — and every test that exercised
// that branch linked a stack on loopback, so replacing
// `Local: started && sameStack(running.URL, base)` with a bare
// `Target{URL: base}` (dropping the start record from the decision entirely)
// still passed every one of them. `palbase start --lan` records the LAN
// address it bound, not a loopback one, and linking to THAT exact address —
// this checkout's own start record — must still write `local/`, not `main/`.
func TestProjectlessLinkedEnvFollowsALANStartRecord(t *testing.T) {
	inScratchCheckout(t)
	const lan = "http://192.168.1.20:54321"
	require.NoError(t, WriteLocalTarget(Target{URL: lan}))

	assert.Equal(t, localEnvName, projectlessLinkedEnv("", lan),
		"a LAN address this checkout's own start record announced was not named local")

	// AND NOTHING ELSE IS: an address that is not this checkout's start
	// record, and not loopback, is still somebody else's main — a start
	// record does not make every LAN address this machine's.
	assert.Equal(t, soleEnvName, projectlessLinkedEnv("", "http://192.168.1.99:54321"),
		"an address no start record announced was named local")
}

// A LOOPBACK LINK KEEPS ITS OWN ADDRESS OVER A DIFFERENT REGISTERED STACK
// (T011 review round 1, MINOR #2b).
//
// `gatherEnvironments`'s early return for `defaultEnv == localEnvName` guards
// exactly this: without it, removing that return still passed every existing
// test, because none of them registers a DIFFERENT stack by group while
// linking a loopback address by hand. Here one is — the way `palbase start`
// would leave a monorepo's backend checkout — and the address actually linked
// is a SEPARATE loopback stack. Without the early return, `findLocalStack`
// would look the registered stack up by group, find it, and overwrite
// `local/` with ITS address and key even though a different one was just
// linked.
func TestALoopbackLinkKeepsItsOwnAddressOverARegisteredStack(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	t.Setenv("PALBASE_ENV", "")

	// A stack registered under this checkout's own directory name — what
	// `palbase start` would leave behind in an unlinked checkout.
	other := stackServing(t, linkKeyLocal, nil)
	dir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, registerStack(sanitiseGroup(filepath.Base(dir)), other.URL, "palbase-other", "/elsewhere/backend"))
	require.NoError(t, StoreCredential(other.URL, Credentials{Value: "other-key", Kind: KindKey}))

	// The address actually linked, by hand: a separate loopback stack.
	linked := stackServing(t, linkKeyMain, nil)
	linkedAs(t, linked.URL, "a-credential")

	o := linkOpts{url: linked.URL}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	cfg := readEnvConfig(t, localEnvName, "android")
	assert.Equal(t, linked.URL, cfg.BaseURL, "a registered stack overwrote the address just linked by hand")
	assert.Equal(t, linkKeyMain, cfg.APIKey, "a registered stack's key overwrote the one just linked by hand")
}
