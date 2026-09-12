package backend

import (
	"bytes"
	"context"
	"errors"
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

// ── inceleme bulgularının testleri ──────────────────────────────────────────

// C-1: A FAILED LISTING IS NOT A LICENCE TO GUESS.
//
// `isCanonicalProjectRef` is `^[a-z0-9]{4,24}$` — it matches `main`, `prod`,
// `staging` and `production`, every word a person actually types. The branch
// this replaces built an address out of whatever was typed whenever the listing
// failed, so a nil hook or a network blip sent `--env production` to
// `https://production.<host>` with Source "flag" and no warning.
func TestAFailedListingRefusesInsteadOfGuessing(t *testing.T) {
	for _, named := range []string{"production", "prod", "main", "mu0028"} {
		t.Run(named, func(t *testing.T) {
			linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
			resolverRig(t, twoEnvs)
			EnvironmentsOf = func(context.Context, string) ([]Environment, error) {
				return nil, errors.New("control plane unreachable")
			}
			SelectedEnvFlag = named

			_, err := ResolveFor(cmdFor(t))
			require.Error(t, err, "a failed listing must never resolve an address")
			require.Contains(t, err.Error(), "cannot check")
			require.Contains(t, err.Error(), named)
		})
	}
}

// C-1, ikinci yarı: the hook being NIL is the same class of failure.
func TestAnUnwiredHookRefusesInsteadOfGuessing(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, twoEnvs)
	EnvironmentsOf = nil
	SelectedEnvFlag = "production"

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "https://production.",
		"an unwired hook built an address out of a typed word")
}

// I-1: AN OLD CLOUD CHECKOUT IS NOT SELF-HOST.
//
// Before the identity format a cloud link wrote {"url":"https://<ref>.<host>"}
// — URL set, Project empty, the exact shape the self-host branch matches.
func TestALegacyCloudCheckoutIsNotTreatedAsSelfHost(t *testing.T) {
	linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, twoEnvs)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return true }
	SelectedEnvFlag = "staging"

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err, "a legacy cloud checkout must not refuse --env as if it were self-host")
	require.Equal(t, "staging", got.Env)
}

// I-2: A RUNNING LOCAL STACK WINS, self-host checkout included.
func TestARunningLocalStackWinsForASelfHostCheckout(t *testing.T) {
	linkedTo(t, Target{URL: "https://stack.firma.com"})
	resolverRig(t, nil)
	local, err := localPath()
	require.NoError(t, err)
	require.NoError(t, ensureMachineStateDir(local))
	require.NoError(t, os.WriteFile(local, []byte(`{"url":"http://127.0.0.1:54321"}`), 0o644))

	got, resolveErr := ResolveFor(cmdFor(t))
	require.NoError(t, resolveErr)
	require.Equal(t, "local", got.Source, "a running local stack lost to the committed address")
	require.Equal(t, "http://127.0.0.1:54321", got.URL)
}

// M-7: an empty TenantHost must refuse by name, not build "https://ref.".
func TestAnUnconfiguredTenantHostRefusesByName(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, []Environment{{Ref: "j06bwtuum", Name: "main"}})
	TenantHost = ""

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tenant host")
}

// M-7: a project with NO environments says what to do about it.
func TestAProjectWithNoEnvironmentsSaysHowToMakeOne(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, nil)
	EnvironmentsOf = func(context.Context, string) ([]Environment, error) { return nil, nil }
	TenantHost = "palbase.studio"

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase env create")
}

// M-7: `--env <ref>` matches by ref, not only by name.
func TestTheFlagMatchesByRefAsWellAsName(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, twoEnvs)
	SelectedEnvFlag = "mu0028"

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "staging", got.Env, "a ref must resolve to its environment's NAME")
}

// M-7: the flag beats PALBASE_ENV — one is typed now, the other is ambient.
func TestTheFlagBeatsTheEnvironmentVariable(t *testing.T) {
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, twoEnvs)
	t.Setenv("PALBASE_ENV", "staging")
	SelectedEnvFlag = "main"

	got, err := ResolveFor(cmdFor(t))
	require.NoError(t, err)
	require.Equal(t, "main", got.Env)
}

// M-4: a corrupt selection is distinguishable from no selection.
func TestACorruptSelectionIsNotSilentlyTreatedAsAbsent(t *testing.T) {
	inScratchCheckout(t)
	root, err := os.Getwd()
	require.NoError(t, err)
	path, pathErr := SelectionPath(root)
	require.NoError(t, pathErr)
	require.NoError(t, ensureMachineStateDir(path))
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	_, readErr := ReadSelection(root)
	require.ErrorIs(t, readErr, ErrCorruptSelection,
		"a corrupt selection reported as 'nothing selected' stays corrupt forever")
}

// ── GÖÇ (FR-060/FR-061) ────────────────────────────────────────────────────

// AN OLD CHECKOUT MIGRATES ITSELF, ONCE, AND SAYS SO.
//
// Before the identity format a cloud link wrote the resolved ADDRESS. That file
// pins a tenant: the project can grow a second environment and the checkout
// never learns. The first verb that runs resolves the ref back to its product
// and rewrites the file — and prints what it did, because a file that changed
// under somebody without a word is worse than one that did not change.
func TestALegacyCheckoutIsRewrittenToItsIdentity(t *testing.T) {
	root := linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, twoEnvs)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return true }

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	after, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "prd_a", after.Project, "the committed file still pins an address")
	require.Equal(t, "todoapp", after.Name)
	require.Empty(t, after.URL, "the stale address survived the migration")
	require.Contains(t, out.String(), "todoapp", "the migration happened silently")
	_ = root
}

// A MIGRATION THAT CANNOT RESOLVE CHANGES NOTHING.
//
// The control plane is unreachable, or the ref belongs to somebody else. Either
// way the checkout keeps working against the address it has: a migration is not
// allowed to break a checkout that was fine a minute ago.
func TestAFailedMigrationLeavesTheCheckoutAlone(t *testing.T) {
	linkedTo(t, Target{URL: "https://mu0028.palbase.studio"})
	resolverRig(t, twoEnvs)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return true }
	ProductOfRef = func(context.Context, string) (Product, error) {
		return Product{}, errors.New("control plane unreachable")
	}

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out),
		"a failed migration must not fail the verb")

	after, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "https://mu0028.palbase.studio", after.URL,
		"a failed migration rewrote the file anyway")
}

// A SELF-HOST CHECKOUT IS NOT A MIGRATION CANDIDATE.
func TestSelfHostIsNotMigrated(t *testing.T) {
	linkedTo(t, Target{URL: "https://stack.firma.com"})
	resolverRig(t, nil)
	prev := CloudProjectAddress
	t.Cleanup(func() { CloudProjectAddress = prev })
	CloudProjectAddress = func(string) bool { return false }

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	after, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "https://stack.firma.com", after.URL)
	require.Empty(t, out.String(), "a self-host checkout was told it migrated")
}

// A RUNNING LOCAL STACK WINS IN A CHECKOUT THAT WAS NEVER LINKED.
//
// This is the normal case for `palbase start`, not an edge: `palbase init`
// scaffolds a backend with no `project.json`, `start` brings a stack up, and
// every verb then acts on it. An earlier arrangement read the committed record
// first and returned ITS error, so a person whose stack was running in front of
// them was told "this checkout is not linked to a project" — advice for a
// problem they did not have. Caught by `internal/db`'s suite, which is why the
// assertion lives here too: the resolver is where the order is decided.
func TestALocalStackResolvesWithNoCommittedProject(t *testing.T) {
	inScratchCheckout(t)
	resolverRig(t, nil)

	local, err := localPath()
	require.NoError(t, err)
	require.NoError(t, ensureMachineStateDir(local))
	require.NoError(t, os.WriteFile(local, []byte(`{"url":"http://127.0.0.1:54321"}`), 0o644))

	got, resolveErr := ResolveFor(cmdFor(t))
	require.NoError(t, resolveErr, "a running stack was refused because nothing was linked")
	require.Equal(t, "local", got.Source)
	require.Equal(t, "http://127.0.0.1:54321", got.URL)
	require.True(t, got.Target.Local)
}

// AND WITHOUT ONE, THE REFUSAL STILL CARRIES THE FIX.
func TestAnUnlinkedCheckoutWithNoStackIsRefusedWithTheWaysIn(t *testing.T) {
	inScratchCheckout(t)
	resolverRig(t, nil)

	_, err := ResolveFor(cmdFor(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase link")
}

// ── LINK YAZDIĞI ŞEY (FR-001/FR-003) ───────────────────────────────────────

// A CLOUD LINK WRITES AN IDENTITY, NOT AN ADDRESS.
//
// The committed file is read by everybody who clones the repository, on every
// branch. An address pins one tenant: the project grows a second environment
// and the file never learns, and switching means editing a committed file —
// which is a diff on `main` and a diff on `development` for the same code.
func TestWriteLinkedIdentityWritesNoAddress(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_9f21c7", Name: "todoapp"}, Target{}))

	after, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "prd_9f21c7", after.Project)
	require.Equal(t, "todoapp", after.Name)
	require.Empty(t, after.URL, "a cloud link wrote an environment's address into the repository")

	raw, readErr := os.ReadFile(projectPath())
	require.NoError(t, readErr)
	require.NotContains(t, string(raw), "stackVersion")
	require.NotContains(t, string(raw), "\"env\"")
}

// A LOOPBACK ADDRESS NEVER ENTERS THE REPOSITORY — it goes to this machine's
// own state instead, where `palbase start` already keeps one.
//
// Measured in this repository on 2026-09-11: the control plane's own checkout
// carried `{"url":"http://127.0.0.1:18098","stackVersion":"38"}` — a committed
// file naming a port that exists on exactly one machine, for exactly as long as
// that stack runs.
func TestALoopbackAddressIsNeverCommitted(t *testing.T) {
	for _, addr := range []string{
		"http://127.0.0.1:54321", "http://localhost:8080", "http://[::1]:9000",
	} {
		t.Run(addr, func(t *testing.T) {
			inScratchCheckout(t)
			require.NoError(t, WriteSelfHostTarget(Target{URL: addr}))

			// NOT in the repository...
			_, statErr := os.Stat(projectPath())
			require.True(t, os.IsNotExist(statErr),
				"a machine-local address was written into the committed file")

			// ...but still reachable, because linking a stack you started by
			// hand is a documented flow and must keep working.
			got, readErr := ReadTarget()
			require.NoError(t, readErr)
			require.Equal(t, addr, got.URL)
			require.True(t, got.Local)
		})
	}
}

// A SELF-HOSTED ADDRESS IS STILL WRITTEN — that path is unchanged and must be.
func TestASelfHostAddressIsStillCommitted(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteSelfHostTarget(Target{URL: "https://stack.firma.com"}))

	after, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "https://stack.firma.com", after.URL)
	require.Empty(t, after.Project)
}

// THE MACHINE-LOCAL RECORD IS KEYED TO THE CHECKOUT, NOT TO WHEREVER THE
// WRITER HAPPENED TO BE STANDING.
//
// `link` does its work in a temporary stage and copies artifacts back, so a
// writer that asked the cwd would key this machine's state to a directory that
// is deleted moments later: the record would exist, hashed to nothing, and the
// link would report success while binding nothing. Measured — eight link tests
// went red at once.
func TestALoopbackLinkBindsTheCheckoutNotTheStage(t *testing.T) {
	inScratchCheckout(t)
	checkout, err := os.Getwd()
	require.NoError(t, err)

	stage := t.TempDir()
	require.NoError(t, os.Chdir(stage))
	t.Cleanup(func() { _ = os.Chdir(checkout) })

	// Written from the stage, but carrying the checkout it belongs to.
	require.NoError(t, WriteSelfHostTarget(Target{URL: "http://127.0.0.1:9999", checkoutRoot: checkout}))

	require.NoError(t, os.Chdir(checkout))
	got, readErr := ReadTarget()
	require.NoError(t, readErr, "the link bound a directory that no longer exists")
	require.Equal(t, "http://127.0.0.1:9999", got.URL)
	require.True(t, got.Local)
}
