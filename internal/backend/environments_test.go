package backend

import (
	"context"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// resolverRig points the package's control-plane hooks at fixed answers and
// restores them afterwards. The hooks are the seam main.go wires in production;
// a test that replaced ResolveFor itself would measure nothing.
func resolverRig(t *testing.T, envs []Environment) {
	t.Helper()
	prevEnvs, prevProd, prevHost, prevFlag := EnvironmentsOf, ProductOfRef, TenantHost, SelectedEnvFlag
	t.Cleanup(func() {
		EnvironmentsOf, ProductOfRef, TenantHost, SelectedEnvFlag = prevEnvs, prevProd, prevHost, prevFlag
	})
	EnvironmentsOf = func(context.Context, string) ([]Environment, error) { return envs, nil }
	ProductOfRef = func(context.Context, string) (Product, error) { return Product{ID: "prd_a", Name: "todoapp"}, nil }
	TenantHost = "palbase.studio"
	SelectedEnvFlag = ""
}

func linkedTo(t *testing.T, target Target) string {
	t.Helper()
	inScratchCheckout(t)
	root, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, WriteTarget(target))
	return root
}

func cmdFor(t *testing.T) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "x"}
	c.SetContext(context.Background())
	return c
}

var twoEnvs = []Environment{
	{Ref: "j06bwtuum", Name: "main", Status: "Running"},
	{Ref: "mu0028", Name: "staging", Status: "Running"},
}

// THE FLAG WINS, AND IT CHANGES NOTHING PERMANENT. Supabase's resolver enforces
// the same rule and its reason is the one that matters here: an override is for
// one call. A flag that quietly became the new default would make the NEXT
// command act on an environment nobody named.
func TestFlagWinsAndDoesNotMoveTheSelection(t *testing.T) {
	root := linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, twoEnvs)
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "staging", Ref: "mu0028"}))
	SelectedEnvFlag = "main"

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "main", got.Env)
	require.Equal(t, "flag", got.Source)

	after, err := ReadSelection(root)
	require.NoError(t, err)
	require.Equal(t, "staging", after.Env, "the flag rewrote the remembered selection")
}

func TestEnvVarResolvesWhenNoFlag(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, twoEnvs)
	t.Setenv("PALBASE_ENV", "staging")

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "staging", got.Env)
	require.Equal(t, "mu0028", got.Ref)
	require.Equal(t, "https://mu0028.palbase.studio", got.URL)
}

func TestRememberedSelectionResolvesWithoutAsking(t *testing.T) {
	root := linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, twoEnvs)
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "staging", Ref: "mu0028"}))
	// The selection carries the ref, so resolving costs no listing call at all.
	EnvironmentsOf = func(context.Context, string) ([]Environment, error) {
		t.Fatal("a remembered selection must not need the control plane")
		return nil, nil
	}

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "staging", got.Env)
	require.Equal(t, "selection", got.Source)
}

// A SELECTION BELONGING TO ANOTHER PROJECT IS NOT A SELECTION. This is the one
// failure mode a remembered choice has, and it is closed by the record naming
// its own project.
func TestStaleSelectionFromAnotherProjectIsDropped(t *testing.T) {
	root := linkedTo(t, Target{Project: "prd_b", Name: "otherapp"})
	resolverRig(t, []Environment{{Ref: "zz1", Name: "main", Status: "Running"}})
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "staging", Ref: "mu0028"}))

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "main", got.Env, "a selection from another project was applied")
	require.Equal(t, "only", got.Source)
}

func TestASingleEnvironmentIsImplicit(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, []Environment{{Ref: "j06bwtuum", Name: "main", Status: "Running"}})

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "main", got.Env)
	require.Equal(t, "only", got.Source)
}

// AMBIGUITY IS A REFUSAL, and the refusal is the feature. Three providers were
// measured shipping the opposite — Railway silently overrode an explicit
// `-e production`, Azure SWA deployed an INVALID `--env` value to Production.
// A push that guesses its environment eventually guesses production.
func TestAmbiguityRefusesAndListsTheEnvironments(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, twoEnvs)

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err, "two environments and no selection must not resolve")
	require.Contains(t, err.Error(), "main")
	require.Contains(t, err.Error(), "j06bwtuum")
	require.Contains(t, err.Error(), "staging")
	require.Contains(t, err.Error(), "mu0028")
	require.Contains(t, err.Error(), "--env")
}

func TestUnknownEnvironmentNameRefusesWithTheList(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, twoEnvs)
	SelectedEnvFlag = "prod"

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "prod")
	require.Contains(t, err.Error(), "staging")
}

// A SELF-HOSTED STACK IS ONE INSTALLATION WITH ONE ENVIRONMENT, and saying so
// by name beats resolving a flag against a project that does not exist.
func TestSelfHostRefusesTheFlagByName(t *testing.T) {
	linkedTo(t, Target{URL: "https://stack.firma.com"})
	resolverRig(t, nil)
	SelectedEnvFlag = "staging"

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "stack.firma.com")
	require.Contains(t, err.Error(), "--env")
}

func TestSelfHostResolvesToItsOwnAddress(t *testing.T) {
	linkedTo(t, Target{URL: "https://stack.firma.com"})
	resolverRig(t, nil)

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "https://stack.firma.com", got.URL)
	require.Equal(t, "url", got.Source)
}

func TestDescribeNamesProjectAndEnvironment(t *testing.T) {
	r := Resolved{Target: Target{Project: "prd_a", Name: "todoapp"}, Env: "staging"}
	require.Equal(t, "todoapp/staging", r.Describe())
}
