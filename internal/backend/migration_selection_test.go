package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	MigrateLegacyTarget(context.Background(), &out)

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
	MigrateLegacyTarget(context.Background(), &out)

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
	MigrateLegacyTarget(context.Background(), &out)

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
	MigrateLegacyTarget(context.Background(), &out)

	written, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	require.NotContains(t, string(written), "stackVersion", "the retired field survived a verb")
	after, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "https://8bbwb2pbm.palbase.studio", after.URL, "dropping a retired field moved the address")
	require.Contains(t, out.String(), projectPath(), "the file changed without a word")
	require.Contains(t, out.String(), "stackVersion", "the line does not name what was dropped")
}

// A RETIRED `env` DROPS, IS NAMED, AND CHOOSES NOTHING (FR-5).
//
// Round 1 moved it to this machine's selection, and the verb that ran the
// migration then acted on that environment in the same run. No release since
// v0.34.0 routed on this field: `palbase env <slug>` stopped writing it there,
// and from then until it retired only the banner label and an artifact
// directory name read it. A committed environment is also exactly how a
// colleague pulls a branch and pushes to your staging (Target.Project's
// comment). So it drops, and the line names what it said — quoted, because a
// committed file must not put control characters on a terminal, and cut at 64
// runes, because it must not put a hundred kibibytes there either — and says
// how to choose one. An empty value is still a field the file carried. Nothing
// is chosen for anybody, and nobody is asked which environments exist.
func TestARetiredEnvironmentDropsIsNamedAndChoosesNothing(t *testing.T) {
	// Built, not typed: the file must carry the JSON escape, never the raw byte.
	control := string(rune(0x1b)) + "[2Jprod"
	controlJSON, err := json.Marshal(control)
	require.NoError(t, err)
	// A two-byte rune, so a cut counted in bytes shows as one.
	wide := string(rune(0x15f))
	long := string(bytes.Repeat([]byte(wide), 50*1024)) // 100 KiB
	kept := string(bytes.Repeat([]byte(wide), 64))
	for _, tc := range []struct{ name, raw, quoted, uncut string }{
		{"beside a project", `{"project":"prd_a","env":"staging"}`, `"staging"`, ""},
		{"beside an address that cannot move", `{"url":"https://mu0028.palbase.studio","env":"main"}`, `"main"`, ""},
		{"beside both, with stackVersion",
			`{"url":"https://mu0028.palbase.studio","project":"prd_a","env":"main","stackVersion":"39"}`, `"main"`, ""},
		{"carrying a control sequence", `{"project":"prd_a","env":` + string(controlJSON) + `}`, fmt.Sprintf("%q", control), ""},
		{"empty", `{"project":"prd_a","env":""}`, `("")`, ""},
		{"a hundred kibibytes long", `{"project":"prd_a","env":"` + long + `"}`, `"` + kept, kept + wide},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := linkedByAnOlderCLI(t, tc.raw)
			resolverRig(t, twoEnvs)
			cloudAddresses(t, true)
			ProductOfRef = func(context.Context, string) (Product, error) {
				return Product{}, errors.New("control plane unreachable")
			}
			listed := 0
			EnvironmentsOf = func(context.Context, string) ([]Environment, error) {
				listed++
				return twoEnvs, nil
			}

			var out bytes.Buffer
			MigrateLegacyTarget(context.Background(), &out)

			_, selErr := ReadSelection(root)
			require.ErrorIs(t, selErr, os.ErrNotExist, "the migration chose an environment from a committed file")
			written, err := os.ReadFile(projectPath())
			require.NoError(t, err)
			require.NotContains(t, string(written), `"env"`, "the committed file still names an environment")
			require.Less(t, out.Len(), 1024, "a committed value flooded the terminal")
			require.Contains(t, out.String(), tc.quoted, "the line does not name the value it dropped, quoted")
			if tc.uncut != "" {
				require.False(t, bytes.Contains(out.Bytes(), []byte(tc.uncut)), "the line did not cut the value at 64 runes")
			}
			require.NotContains(t, out.String(), "\x1b", "a committed file put a control character on the terminal")
			require.Contains(t, out.String(), "palbase env use <name>", "the line does not say how to choose one")
			require.Contains(t, out.String(), "--env <name>", "the line does not say how to choose one")
			require.Zero(t, listed, "dropping a retired field asked the cloud which environments exist")
		})
	}
}

// AN EXISTING CHOICE IS LEFT BYTE FOR BYTE (FR-5).
//
// Whatever this machine already remembers — for this project or for another —
// the retired field changes none of it. The file is still cleaned, and that is
// asserted FIRST: an untouched selection is worth nothing unless the migration
// ran at all.
func TestARetiredEnvironmentLeavesAnExistingChoiceByteForByte(t *testing.T) {
	for _, tc := range []struct {
		name string
		sel  Selection
	}{
		{"this project's", Selection{Project: "prd_a", Env: "main", Ref: "j06bwtuum"}},
		{"another project's", Selection{Project: "prd_b", Env: "prod", Ref: "otherref1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := linkedByAnOlderCLI(t, `{"project":"prd_a","env":"staging"}`)
			resolverRig(t, twoEnvs)
			require.NoError(t, WriteSelection(root, tc.sel))
			path, err := SelectionPath(root)
			require.NoError(t, err)
			before, err := os.ReadFile(path)
			require.NoError(t, err)

			var out bytes.Buffer
			MigrateLegacyTarget(context.Background(), &out)

			written, err := os.ReadFile(projectPath())
			require.NoError(t, err)
			require.NotContains(t, string(written), `"env"`, "the migration did not run")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "the retired field changed a choice this machine had")
		})
	}
}

// AFTER A RETIRED `env` DROPS, TODAY'S RULES RESOLVE (FR-5).
//
// A project with one environment acts on it whatever the file said; a project
// with two refuses exactly as it does for a checkout that never carried the
// field. The file says `staging` in both, and in neither does that decide.
func TestAfterARetiredEnvironmentDropsTodaysRulesResolve(t *testing.T) {
	t.Run("two environments refuse", func(t *testing.T) {
		linkedByAnOlderCLI(t, `{"project":"prd_a","env":"staging"}`)
		resolverRig(t, twoEnvs)

		var out bytes.Buffer
		MigrateLegacyTarget(context.Background(), &out)

		_, err := Resolve(context.Background())
		require.ErrorContains(t, err, "has 2 environments and none is selected",
			"a committed environment decided where the verb acts")
	})
	t.Run("one environment is the answer", func(t *testing.T) {
		linkedByAnOlderCLI(t, `{"project":"prd_a","env":"staging"}`)
		resolverRig(t, twoEnvs[:1]) // only `main`

		var out bytes.Buffer
		MigrateLegacyTarget(context.Background(), &out)

		got, err := Resolve(context.Background())
		require.NoError(t, err, "a one-environment project refused")
		require.Equal(t, "main", got.Env)
		require.Equal(t, "only", got.Source)
	})
}

// THE ADDRESS MIGRATION NAMES A RETIRED `env` IT DROPS, AND THE ADDRESS DECIDES (FR-5).
//
// When the cloud resolves the address, the file becomes an identity and the
// environment comes from the address — as it did in v0.64, where the address
// was what routed. The retired `env` beside it decides nothing, and the line
// says what it named, quoted and cut like the cleanup's, so nobody takes the old
// value for the environment this machine now acts on. The bounds are measured
// on this line as well as the cleanup's: while only `"main"` was, a line that
// quoted the whole value uncut passed (measured in review).
func TestTheAddressMigrationNamesARetiredEnvironmentItDrops(t *testing.T) {
	// Built, not typed: the file must carry the JSON escapes, never the raw bytes.
	control := string(rune(0x1b)) + "[2Jprod" + string(rune(0x07))
	controlJSON, err := json.Marshal(control)
	require.NoError(t, err)
	// A two-byte rune, so a cut counted in bytes shows as one.
	wide := string(rune(0x15f))
	long := string(bytes.Repeat([]byte(wide), 50*1024)) // 100 KiB
	kept := string(bytes.Repeat([]byte(wide), 64))
	for _, tc := range []struct{ name, env, quoted, uncut string }{
		{"a plain name", `"main"`, `"main"`, ""},
		{"carrying a control sequence", string(controlJSON), fmt.Sprintf("%q", control), ""},
		{"a hundred kibibytes long", `"` + long + `"`, `"` + kept, kept + wide},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := linkedByAnOlderCLI(t, `{"url":"https://mu0028.palbase.studio","env":`+tc.env+`}`)
			resolverRig(t, twoEnvs)
			cloudAddresses(t, true)

			var out bytes.Buffer
			MigrateLegacyTarget(context.Background(), &out)

			written, err := os.ReadFile(projectPath())
			require.NoError(t, err)
			require.Contains(t, string(written), `"project": "prd_a"`, "the address migration did not run")
			require.NotContains(t, string(written), `"env"`, "the rewritten file still names an environment")
			sel, err := ReadSelection(root)
			require.NoError(t, err)
			require.Equal(t, "staging", sel.Env, "the environment did not come from the address")
			require.Less(t, out.Len(), 1024, "a committed value flooded the terminal through the address migration's line")
			require.Contains(t, out.String(), tc.quoted, "the address migration's line does not name the env it dropped, quoted")
			if tc.uncut != "" {
				require.False(t, bytes.Contains(out.Bytes(), []byte(tc.uncut)),
					"the address migration's line did not cut the value at 64 runes")
			}
			require.NotContains(t, out.String(), "\x1b", "a committed file put a control character on the terminal")
			require.NotContains(t, out.String(), "\a", "a committed file put a control character on the terminal")
		})
	}
}

// A REWRITE THAT FAILS LEAVES EVERYTHING AS IT WAS (FR-6).
//
// The file byte for byte, no selection, no line — and the verb still runs,
// because the read does not wait on the migration. Round 1 wrote the selection
// before a rewrite that then failed, so a read-only checkout was routed silently
// on every run; with `env` choosing nothing that path is gone, and this keeps it
// gone. It is the FILE that is read-only, not its directory: a rename asks only
// the directory, so this is also what holds WriteTarget to leaving a file it may
// not write alone. The address migration writes a selection ahead of its file,
// and what that leaves when the file write then fails is measured on its own
// (TestAChoiceWrittenAheadOfAFailedRewriteDecidesNothing).
func TestARetiredFieldWhoseRewriteFailsLeavesEverythingAlone(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		// runs is set where today's rules resolve a target, so the verb itself
		// can be run; a two-environment project refuses for its own reasons.
		runs bool
	}{
		{"a project and env", `{"project":"prd_a","env":"staging"}`, false},
		{"an address and stackVersion", `{"url":"https://8bbwb2pbm.palbase.studio","stackVersion":"39"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root writes through a read-only file, so no rewrite can be made to fail this way")
			}
			root := linkedByAnOlderCLI(t, tc.raw)
			resolverRig(t, twoEnvs)
			cloudAddresses(t, true)
			ProductOfRef = func(context.Context, string) (Product, error) {
				return Product{}, errors.New("control plane unreachable")
			}
			require.NoError(t, os.Chmod(projectPath(), 0o444))
			before, err := os.ReadFile(projectPath())
			require.NoError(t, err)

			var out bytes.Buffer
			// tip düzeyinde garanti: göç hiçbir hata döndürmez (a rewrite that failed failed the verb)
			MigrateLegacyTarget(context.Background(), &out)

			after, err := os.ReadFile(projectPath())
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "a rewrite that failed changed the committed file")
			_, selErr := ReadSelection(root)
			require.ErrorIs(t, selErr, os.ErrNotExist, "a rewrite that failed still chose an environment")
			require.Empty(t, out.String(), "a rewrite that failed announced itself")

			_, err = readLinkedProject()
			require.NoError(t, err, "a file the migration could not rewrite is unreadable")
			if tc.runs {
				var banner bytes.Buffer
				_, err = PrintResolvedTo(&banner, nil)
				require.NoError(t, err, "the verb did not run in a checkout whose rewrite failed")
			}
		})
	}
}

// A CHOICE WRITTEN AHEAD OF A REWRITE THAT FAILS DECIDES NOTHING (FR-6's one
// exception).
//
// The address migration writes this machine's selection first and the committed
// file last, so a selection that cannot be written changes nothing. The price is
// this case: the selection lands, and the file write after it fails — here a
// read-only `project.json` in a checkout the cloud resolves. The file still
// records the address, and a file that records an address is answered from it
// (Resolve's `legacy` branch), which reads no selection at all. So the
// selection is asserted to be there — that is the order — and then shown to
// decide nothing: moved to the other environment, the verb still acts on the
// address the file records.
func TestAChoiceWrittenAheadOfAFailedRewriteDecidesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only file, so no rewrite can be made to fail this way")
	}
	const address = "https://mu0028.palbase.studio"
	root := linkedByAnOlderCLI(t, `{"url":"`+address+`","stackVersion":"39"}`)
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)
	require.NoError(t, os.Chmod(projectPath(), 0o444))
	before, err := os.ReadFile(projectPath())
	require.NoError(t, err)

	var out bytes.Buffer
	// tip düzeyinde garanti: göç hiçbir hata döndürmez (a rewrite that failed failed the verb)
	MigrateLegacyTarget(context.Background(), &out)

	after, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "a rewrite that failed changed the committed file")
	require.Empty(t, out.String(), "a rewrite that failed announced itself")
	sel, err := ReadSelection(root)
	require.NoError(t, err, "the choice was not written ahead of the committed file")
	require.Equal(t, "prd_a", sel.Project)
	require.Equal(t, "staging", sel.Env, "the choice written ahead is not the address's environment")
	require.Equal(t, "mu0028", sel.Ref)

	for _, choice := range []Selection{sel, {Project: "prd_a", Env: "main", Ref: "j06bwtuum"}} {
		require.NoError(t, WriteSelection(root, choice))
		got, err := Resolve(context.Background())
		require.NoError(t, err, "a choice written ahead of a failed rewrite broke the verb")
		require.Equal(t, "legacy", got.Source, "a file that still records an address was not answered from it")
		require.Equal(t, address, got.URL, "a selection decided where a checkout that records an address acts")
	}
	var banner bytes.Buffer
	_, err = PrintResolvedTo(&banner, nil)
	require.NoError(t, err, "the verb did not run after a rewrite that failed")
	require.Contains(t, banner.String(), address)
}

// A SELECTION THIS MACHINE CANNOT WRITE MOVES NOTHING (FR-6).
//
// The address migration's other write is this machine's selection, and it used
// to come second: the file became an identity, the selection write failed, and
// the verb failed with it — `mkdir …/.palbase: permission denied` — in a
// checkout whose next verb then refused with "has 2 environments and none is
// selected". Written first, its failure has changed nothing: the file byte for
// byte, no selection, no line, and the verb runs against the address. The file
// carries a retired field on purpose: once a write the address migration began
// has failed, the cleanup does not get a second go at the same file.
func TestASelectionThisMachineCannotWriteMovesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory, so no selection can be made to fail this way")
	}
	const address = "https://mu0028.palbase.studio"
	raw := `{"url":"` + address + `","stackVersion":"39"}`
	root := linkedByAnOlderCLI(t, raw)
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)
	home := useTempMachineHome(t)
	require.NoError(t, os.Chmod(home, 0o555))
	t.Cleanup(func() { _ = os.Chmod(home, 0o755) })

	var out bytes.Buffer
	// tip düzeyinde garanti: göç hiçbir hata döndürmez (a selection this machine could not write failed the verb)
	MigrateLegacyTarget(context.Background(), &out)

	after, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	require.Equal(t, raw, string(after), "a selection this machine could not write left the committed file changed")
	require.Empty(t, out.String(), "a migration that moved nothing announced something")
	_, selErr := ReadSelection(root)
	require.ErrorIs(t, selErr, os.ErrNotExist, "a selection was remembered on a machine that cannot write one")

	var banner bytes.Buffer
	got, err := PrintResolvedTo(&banner, nil)
	require.NoError(t, err, "the verb did not run in a checkout whose selection could not be written")
	require.Equal(t, "legacy", got.Source)
	require.Equal(t, address, got.URL)
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
