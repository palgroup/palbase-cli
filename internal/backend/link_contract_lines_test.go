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

// pushedAndNot is an Android checkout linked to a project whose `main` serves a
// contract and whose `staging` has nothing deployed yet.
func pushedAndNot(t *testing.T) linkOpts {
	t.Helper()
	seedAndroidApp(t)
	main, _ := envServer(t, linkKeyMain, envServerOpts{socialAuth: true})
	staging, _ := envServer(t, "pb_staging_cK", envServerOpts{socialAuth: true, noContract: true})
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	return linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}
}

// EVERY CONTRACT LINK WRITES IS SAID, AND SO IS EVERY ONE IT DID NOT (FR-014).
//
// Only the configs were listed, so a link to a pushed project and one to a
// project nothing was deployed to printed the same `wrote` lines — measured
// (verification L1): the output named main/android-config.json while
// main/openapi.json sat on disk unmentioned. The plugin needs both files, and
// the reader could not see which environments had them.
func TestALinkSaysWhichEnvironmentsGotAContract(t *testing.T) {
	inScratchCheckout(t)
	o := pushedAndNot(t)

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Contains(t, out.String(), "wrote palbase/environments/main/android-config.json\n"+
		"wrote palbase/environments/staging/android-config.json\n"+
		"wrote palbase/environments/main/openapi.json\n"+
		"staging has no contract yet, so palbase/environments/staging/openapi.json is not written and no client "+
		"is generated for it — `palbase push --env staging`, then `palbase link` here\n")
	assert.FileExists(t, SpecPath("main"))
	assert.NoFileExists(t, SpecPath("staging"))
}

// A CONTRACT AN EARLIER LINK WROTE STAYS, and the line says that is what the
// build reads — "not written" would send the reader looking for a file that is
// there.
func TestAnEnvironmentThatGaveNoContractThisTimeKeepsTheEarlierOne(t *testing.T) {
	inScratchCheckout(t)
	o := pushedAndNot(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(SpecPath("staging")), 0o755))
	require.NoError(t, os.WriteFile(SpecPath("staging"), []byte(`{"openapi":"3.2.0","paths":{}}`), 0o644))

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	assert.Contains(t, out.String(), "staging gave no contract this time, so palbase/environments/staging/openapi.json "+
		"is the one an earlier link wrote — `palbase push --env staging`, then `palbase link` here\n")
	raw, err := os.ReadFile(SpecPath("staging"))
	require.NoError(t, err)
	assert.Equal(t, `{"openapi":"3.2.0","paths":{}}`, string(raw))
}

// WHAT GIVES AN ENVIRONMENT ITS FIRST CONTRACT depends on where the contract
// comes from. A project's environment serves what was pushed to it, named with
// --env; a stack somebody hosts, linked by address, has no project to name one
// in; and the stack `palbase start` runs serves the directory it mounted —
// `palbase push` refuses to publish to it (stack_push.go), so for `local` the
// cure is a start. ONE cure: the reading's own "`palbase spec` fills the
// contract in" is not said beside it.
func TestTheMissingContractLineNamesWhatEndsIt(t *testing.T) {
	for _, c := range []struct {
		name      string
		linkedEnv string
		want      string
	}{
		{"a stack somebody hosts", "main", "main has no contract yet, so palbase/environments/main/openapi.json is not written " +
			"and no client is generated for it — `palbase push`, then `palbase link` here\n"},
		{"the stack on this machine", "", "local has no contract yet, so palbase/environments/local/openapi.json is not written " +
			"and no client is generated for it — `palbase start` in the backend, then `palbase link` here\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			inScratchCheckout(t)
			seedAndroidApp(t)
			stack, _ := envServer(t, linkKeyMain, envServerOpts{socialAuth: true, noContract: true})
			linkedAs(t, stack.URL, "operator")

			var out strings.Builder
			o := linkOpts{url: stack.URL, platforms: []string{"android"}, linkedEnv: c.linkedEnv}
			require.NoError(t, runLink(context.Background(), o, &out), out.String())

			assert.Contains(t, out.String(), c.want)
			assert.NotContains(t, out.String(), "`palbase spec` fills the contract in", "two cures for one missing contract")
		})
	}
}

// A CHECKOUT WITH NO CLIENT gets no files, so no line about them: it keeps the
// sentence it always had — the link is recorded, and `palbase spec` fills the
// contract in.
func TestABackendLinkWithNoContractStillSaysSpecFillsItIn(t *testing.T) {
	inScratchCheckout(t)
	stack, _ := envServer(t, linkKeyMain, envServerOpts{noContract: true})
	linkedAs(t, stack.URL, "operator")

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), linkOpts{url: stack.URL}, &out), out.String())

	assert.Contains(t, out.String(), "  the link is recorded; `palbase spec` fills the contract in once something answers\n")
	assert.NotContains(t, out.String(), "has no contract yet, so")
}
