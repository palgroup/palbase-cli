package backend

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func cloudAddresses(t *testing.T, yes bool) {
	t.Helper()
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return yes }
}

// THE MIGRATION KEEPS THE ENVIRONMENT THE OLD FILE NAMED.
//
// This is NFR-005 — "an old record makes no verb error" — and it is the whole
// difference between a migration and a break. The old file said "act on
// https://mu0028.<host>", and that address IS one environment. Rewriting it to
// a project identity and stopping there changes the answer to "which one?", so
// the next verb in a checkout that worked a minute ago refuses.
//
// Measured on the product before it was measured here: `palbase plan` in a
// migrated checkout of a two-environment project printed the ambiguity refusal.
//
// The kept choice goes where a choice belongs — MACHINE-LOCAL, the same place
// `palbase env use` writes — so nothing new appears in the repository.
func TestTheMigrationRemembersTheEnvironmentTheAddressNamed(t *testing.T) {
	root := linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	sel, err := ReadSelection(root)
	require.NoError(t, err, "the migration left no remembered environment")
	require.Equal(t, "staging", sel.Env, "the environment the old address named was lost")
	require.Equal(t, "mu0028", sel.Ref)
	require.Equal(t, "prd_a", sel.Project, "the record does not say which project it belongs to")
	require.Contains(t, out.String(), "staging",
		"the migration did not say which environment this machine keeps acting on")

	// AND THE VERB THAT FOLLOWS RESOLVES, which is the claim NFR-005 actually
	// makes: the refusal is what the product printed before this existed.
	resolved, err := Resolve(context.Background())
	require.NoError(t, err, "the verb after the migration still refuses")
	require.Equal(t, "staging", resolved.Env)
	require.Equal(t, "selection", resolved.Source)
}

// A DELIBERATE CHOICE OUTRANKS THE OLD ADDRESS.
//
// Somebody who already ran `palbase env use` on this checkout means it, and of
// the two facts the committed address is the staler one.
func TestTheMigrationDoesNotOverwriteAChoiceSomebodyMade(t *testing.T) {
	root := linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)
	require.NoError(t, WriteSelection(root, Selection{
		Project: "prd_a", Env: "main", Ref: "j06bwtuum",
	}))

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	sel, err := ReadSelection(root)
	require.NoError(t, err)
	require.Equal(t, "main", sel.Env, "the migration overwrote a choice somebody made")
	require.Contains(t, out.String(), "main")
}

// IF THE ENVIRONMENT CANNOT BE NAMED, NOTHING IS WRITTEN AT ALL (FR-061).
//
// Writing the identity without the environment is exactly the half-migration
// that produces the refusal above, so the two writes stand or fall together.
func TestAMigrationThatCannotNameTheEnvironmentWritesNothing(t *testing.T) {
	root := linkedTo(t, Target{URL: "https://gonerefx.palbase.studio"})
	resolverRig(t, twoEnvs) // neither environment has this ref
	cloudAddresses(t, true)

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	after, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "https://gonerefx.palbase.studio", after.URL,
		"a migration that could not name the environment rewrote the file anyway")
	require.Empty(t, after.Project)
	_, selErr := ReadSelection(root)
	require.Error(t, selErr, "a selection was remembered for an environment nobody could name")
	require.Empty(t, out.String(), "a migration that did nothing announced something")
}

// AN UNMIGRATED OLD RECORD STILL RESOLVES — the address it has IS the answer.
//
// The self-host branch deliberately lets a cloud address fall through so the
// migration can rewrite it, and the migration is best-effort: offline, a
// deleted environment, or a code path that never runs it. Before this branch
// existed those cases reached `environmentsOf("")` — the environments of an
// empty product id — and EVERY verb failed in a checkout that used to work.
func TestAnUnmigratedLegacyRecordStillResolvesToItsAddress(t *testing.T) {
	linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)
	EnvironmentsOf = func(context.Context, string) ([]Environment, error) {
		return nil, errors.New("the control plane must not be asked for the environments of nothing")
	}

	resolved, err := Resolve(context.Background())
	require.NoError(t, err, "an old record that the migration did not move broke every verb")
	require.Equal(t, "https://mu0028.palbase.studio", resolved.URL)
	require.Equal(t, "legacy", resolved.Source)
}

// AND `--env` ON SUCH A RECORD REFUSES, naming the address and the way out:
// choosing between environments needs a project, and this record names none.
func TestNamingAnEnvironmentOnAnUnmigratedRecordRefuses(t *testing.T) {
	linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)
	SelectedEnvFlag = "staging"
	// THE REF IS THE ONLY WAY BACK TO A PROJECT here, so the refusal is what
	// happens when even that fails — offline, or an environment somebody
	// deleted. While it works, `--env` keeps working (measured just above by
	// TestALegacyCloudCheckoutIsNotTreatedAsSelfHost).
	ProductOfRef = func(context.Context, string) (Product, error) {
		return Product{}, errors.New("control plane unreachable")
	}

	_, err := Resolve(context.Background())
	require.Error(t, err, "an environment was selected against a record with no project")
	require.Contains(t, err.Error(), "mu0028.palbase.studio")
	require.Contains(t, err.Error(), "palbase link <project>")
}

// AND THE LISTING IS ASKED ABOUT THE DERIVED PRODUCT, not about nothing.
//
// This is the assertion whose absence hid the defect. `resolveNamed` matches a
// name against `environmentsOf(target.Project)`, and on a legacy record that
// field is EMPTY — so production asked the control plane for the environments
// of "" while the suite stayed green, because the test double ignores its
// argument and answers the same fixture whatever it is handed. A double that
// ignores a parameter cannot measure it, so the parameter is measured here.
func TestTheListingIsAskedAboutTheProductDerivedFromTheRef(t *testing.T) {
	linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)
	SelectedEnvFlag = "staging"

	var askedAbout []string
	EnvironmentsOf = func(_ context.Context, productID string) ([]Environment, error) {
		askedAbout = append(askedAbout, productID)
		return twoEnvs, nil
	}

	got, err := Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "staging", got.Env)
	for _, id := range askedAbout {
		require.NotEmpty(t, id,
			"the control plane was asked for the environments of an empty product id")
		require.Equal(t, "prd_a", id)
	}
}
