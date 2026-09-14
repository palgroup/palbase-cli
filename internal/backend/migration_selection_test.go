package backend

import (
	"bytes"
	"context"
	"errors"
	"os"
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

// ── EMEKLİ ALANLAR (`stackVersion`, `env`) ─────────────────────────────────

// A RETIRED `stackVersion` IS DROPPED EVEN WHEN THE ADDRESS CANNOT MOVE (FR-4).
//
// The address migration needs the control plane; dropping `stackVersion` does
// not — the field decided nothing — so an offline verb still leaves a file that
// a colleague who pulls it reads without any tolerance at all. The listing hook
// refuses here, so a cleanup that asked the cloud anything writes nothing and
// this goes red. And it says what it did: a committed file that changed without
// a word is worse than one that did not change.
func TestARetiredStackVersionIsDroppedWhenTheAddressCannotMove(t *testing.T) {
	linkedByAnOlderCLI(t, `{"url":"https://8bbwb2pbm.palbase.studio","stackVersion":"39"}`)
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)
	ProductOfRef = func(context.Context, string) (Product, error) {
		return Product{}, errors.New("control plane unreachable")
	}
	EnvironmentsOf = func(context.Context, string) ([]Environment, error) {
		return nil, errors.New("dropping stackVersion must not ask the cloud anything")
	}

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	written, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	require.NotContains(t, string(written), "stackVersion", "the retired field survived a verb")
	after, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "https://8bbwb2pbm.palbase.studio", after.URL, "dropping a retired field moved the address")
	require.Contains(t, out.String(), projectPath(), "the file changed without a word")
	require.Contains(t, out.String(), "stackVersion", "the line does not name what was dropped")
}

// A RETIRED `env` IS A CHOICE, AND IT MOVES TO THIS MACHINE (FR-5).
//
// Before `1fcefcb` the committed file named the environment. Dropping the field
// and stopping there turns "act on staging" into "which one?" in a
// two-environment project — the refusal NFR-005 exists to prevent. So the
// choice moves to where one belongs now: machine-local, where `palbase env use`
// writes, with the ref the project's own listing gives.
func TestARetiredEnvironmentMovesToThisMachine(t *testing.T) {
	root := linkedByAnOlderCLI(t, `{"project":"prd_a","env":"staging"}`)
	resolverRig(t, twoEnvs)

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	sel, err := ReadSelection(root)
	require.NoError(t, err, "the environment the committed file named was not remembered")
	require.Equal(t, Selection{Project: "prd_a", Env: "staging", Ref: "mu0028"}, sel)
	written, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	require.NotContains(t, string(written), `"env"`, "the committed file still names an environment")
	require.Contains(t, out.String(), "staging",
		"the migration did not say which environment this machine keeps acting on")

	resolved, err := Resolve(context.Background())
	require.NoError(t, err, "the verb after the migration refuses")
	require.Equal(t, "staging", resolved.Env)
	require.Equal(t, "selection", resolved.Source)
}

// A CHOICE SOMEBODY MADE OUTRANKS THE RETIRED FIELD (FR-5).
//
// Somebody who ran `palbase env use` on this checkout meant it, and a field an
// older CLI committed is the staler of the two facts. The file is still
// cleaned, and that is asserted FIRST: "nothing was overwritten" is worth
// nothing unless the migration ran at all.
func TestARetiredEnvironmentDoesNotOverwriteAChoiceSomebodyMade(t *testing.T) {
	root := linkedByAnOlderCLI(t, `{"project":"prd_a","env":"staging"}`)
	resolverRig(t, twoEnvs)
	require.NoError(t, WriteSelection(root, Selection{Project: "prd_a", Env: "main", Ref: "j06bwtuum"}))

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	written, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	require.NotContains(t, string(written), `"env"`, "the migration did not run")
	sel, err := ReadSelection(root)
	require.NoError(t, err)
	require.Equal(t, "main", sel.Env, "the retired field overwrote a choice somebody made")
	require.Contains(t, out.String(), "main")
}

// A RETIRED FIELD THAT CANNOT BE MOVED LEAVES THE FILE EXACTLY AS IT WAS (FR-6).
//
// Dropping `env` without moving the choice is the half-migration FR-5 forbids,
// so the two stand or fall together; a write that fails is the same outcome.
// And the verb still runs — the read no longer depends on the migration having
// happened, which is the assertion that was red before any of this existed.
func TestARetiredFieldThatCannotBeMovedLeavesTheFileAlone(t *testing.T) {
	unreachable := func(context.Context, string) ([]Environment, error) {
		return nil, errors.New("control plane unreachable")
	}
	for _, tc := range []struct {
		name     string
		raw      string
		envs     func(context.Context, string) ([]Environment, error)
		readOnly bool
	}{
		{"the listing cannot be read", `{"project":"prd_a","env":"staging"}`, unreachable, false},
		{"the listing does not name it", `{"project":"prd_a","env":"gone"}`, nil, false},
		{"the write fails", `{"project":"prd_a","stackVersion":"39"}`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := linkedByAnOlderCLI(t, tc.raw)
			resolverRig(t, twoEnvs)
			if tc.envs != nil {
				EnvironmentsOf = tc.envs
			}
			if tc.readOnly {
				if os.Geteuid() == 0 {
					t.Skip("root writes through a read-only file, so no write can be made to fail this way")
				}
				require.NoError(t, os.Chmod(projectPath(), 0o444))
			}
			before, err := os.ReadFile(projectPath())
			require.NoError(t, err)

			var out bytes.Buffer
			require.NoError(t, MigrateLegacyTarget(context.Background(), &out),
				"a migration that could not finish failed the verb")

			after, err := os.ReadFile(projectPath())
			require.NoError(t, err)
			require.Equal(t, string(before), string(after),
				"a migration that could not finish changed the committed file")
			_, selErr := ReadSelection(root)
			require.Error(t, selErr, "a choice was remembered for a file that still makes it")
			require.Empty(t, out.String(), "a migration that did nothing announced something")

			got, err := readLinkedProject()
			require.NoError(t, err, "a file the migration could not rewrite is unreadable")
			require.Equal(t, "prd_a", got.Project)
		})
	}
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
