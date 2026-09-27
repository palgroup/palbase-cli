package backend

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/config"
)

// hostileName is an environment name the control plane accepts today: an OSC
// sequence that retitles the terminal, then a bell.
const (
	hostileName   = "evil\x1b]0;owned\a"
	hostileQuoted = `"evil\x1b]0;owned\a"`
)

var withAHostileName = []Environment{
	{Ref: "j06bwtuum", Name: "main", Status: "Running"},
	{Ref: "evilref001", Name: hostileName, Status: "Running"},
}

// THE MENU A REFUSAL HANDS A PERSON IS SOMEBODY ELSE'S TEXT (FR-006). The
// resolver's "none is selected" lists every environment by name, before any
// gate has looked at them.
func TestTheResolversMenuPrintsANameEscaped(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, withAHostileName)

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, err.Error(), "  "+hostileQuoted+"   evilref001")
}

// AND THE BANNER EVERY VERB PRINTS BEFORE IT ACTS.
func TestTheBannerPrintsANameEscaped(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, withAHostileName)
	SelectedEnvFlag = "evilref001"

	var out strings.Builder
	_, err := PrintResolvedTo(&out, cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "▸ todoapp/"+hostileQuoted+"\n", out.String())
}

// AND THE LINK'S OWN REFUSAL, WHICH RUNS BEFORE THE LINK'S NAME GATE.
func TestLinksRefusalPrintsANameEscaped(t *testing.T) {
	envs := []Environment{
		{Ref: "j06bwtuum", Name: "main", Status: "Running"},
		{Ref: "evilref001", Name: hostileName, Status: "Failed"},
	}

	_, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "evilref001")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, err.Error(), hostileQuoted+" of todoapp is Failed, so nothing can be read from it")
	require.Contains(t, err.Error(), "  "+hostileQuoted+"   evilref001   Failed")
}

// A NAME THAT PASSED THE GATE CAN STILL BE MORE THAN ONE WORD, and a line that
// says `Feature X could not be read` reads as two things. The link's progress
// lines quote it like every other place; a plain name stays as it is.
func TestTheLinksLinesQuoteANameThatIsNotOnePlainWord(t *testing.T) {
	inScratchCheckout(t)
	seedAndroidApp(t)
	blockEnvironmentDir(t, "Blocked One")
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "blokref000": main.URL})
	o := linkOpts{
		url:       main.URL,
		platforms: []string{"android"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "Feature X", Ref: "featref000", Status: "Failed"},
			{Name: "Old Name", Ref: "oldnref000", Status: "Running"},
			{Name: "Blocked One", Ref: "blokref000", Status: "Running"},
		},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.Contains(t, out.String(), `"Feature X" is Failed — not asked; its files are left as they are`)
	require.Contains(t, out.String(), `"Old Name" could not be read (no test server for oldnref000) — its files are left as they are`)
	require.Contains(t, out.String(), `"Blocked One" could not be written (mkdir palbase/environments/Blocked One: not a directory) — skipped`)
	require.Contains(t, out.String(), "contract read from main;")
}

// AND `palbase clone`'S REFUSALS, which name the same environments (FR-006).
func TestCloneRefusalsPrintANameEscaped(t *testing.T) {
	envs := []Environment{
		{Ref: "j06bwtuum", Name: "main", Status: "Running"},
		{Ref: "evilref001", Name: hostileName, Status: "Failed"},
	}
	product := Product{ID: "prd_a", Name: "todoapp"}

	_, err := cloneEnvironmentRef(product, envs, "evilref001", "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, err.Error(), "todoapp/"+hostileQuoted+" is Failed, so there is no source to download")

	_, err = cloneEnvironmentRef(product, envs, "evilref001", "main")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, err.Error(), "evilref001 is the ref of todoapp/"+hostileQuoted+" (Failed), but --from-env names main")
}

// AND THE BANNER `palbase clone` PRINTS BEFORE IT DOWNLOADS. There is no
// credential for the address, so the clone stops right after it.
func TestTheCloneBannerPrintsANameEscaped(t *testing.T) {
	inScratchCheckout(t)
	rest := &nameREST{rows: []map[string]any{
		{"id": "prd_a", "name": "todoapp", "environments": []map[string]any{
			{"ref": "evilref001", "name": hostileName, "status": "Running"},
		}},
	}}
	cmd := newCloneCmd(Resolvers{
		REST:      func() REST { return rest },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	})
	var out, announced bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&announced)
	cmd.SetArgs([]string{"evilref001"})

	require.Error(t, cmd.ExecuteContext(context.Background()), "there is no credential for the address")
	require.NotContains(t, announced.String(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, announced.String(), "▸ todoapp/"+hostileQuoted+"\n")
}

// AND THE LINE `palbase spec` PRINTS ABOUT THE OTHER ENVIRONMENTS, whose names
// come off the disk — directories an older CLI made from whatever the listing
// said.
func TestTheStaleContractsLinePrintsANameEscaped(t *testing.T) {
	inScratchCheckout(t)
	var out strings.Builder
	reportStaleContracts("main", appEnvironments{Environments: map[string]appEnvironment{
		"main": {}, hostileName: {},
	}}, &out)
	require.NotContains(t, out.String(), "\x1b", "a name on disk reached the terminal raw")
	require.Contains(t, out.String(), "only main was refreshed. The others still describe what they last served:\n  "+
		hostileQuoted+" (never fetched)\n")
}

// AND THE MIGRATION LINE, which names the environment this machine keeps
// acting on after an old address-based checkout is rewritten to a project
// identity. It comes straight off the listing (environmentsOf / sel.Env), with
// nothing between it and this line before the fix (fix round 1, Finding 1).
func TestTheMigrationLinePrintsAHostileEnvironmentNameEscaped(t *testing.T) {
	linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, []Environment{
		{Ref: "j06bwtuum", Name: "main", Status: "Running"},
		{Ref: "mu0028", Name: hostileName, Status: "Running"},
	})
	cloudAddresses(t, true)

	var out bytes.Buffer
	MigrateLegacyTarget(context.Background(), &out)

	require.NotContains(t, out.String(), "\x1b", "a listed name reached the terminal raw")
	require.Contains(t, out.String(), "this machine keeps acting on "+hostileQuoted)
}
