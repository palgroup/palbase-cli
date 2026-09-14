package backend

// environments.go — WHICH ENVIRONMENT A VERB ACTS ON.
//
// ONE RESOLVER. Every verb that reads a target goes through ResolveFor, so the
// order below is the only place the question is answered. A second resolver is
// how two commands end up disagreeing about where a push lands.
//
// THE ORDER IS THE CONTRACT, and its last step is a REFUSAL. That last step is
// the feature, not a rough edge: three providers were measured shipping the
// opposite and all three cost somebody a production deploy — Railway silently
// let a scoped token override an explicit `-e production`, Azure SWA deployed
// an INVALID `--env` value to Production, Supabase skipped configuration
// silently when a project id was wrong. A verb that guesses its environment
// eventually guesses production.
//
// THE MAP IS THE SERVER'S. What environments a project has is never mirrored
// into the customer's repository: a configuration read once becomes a lie the
// moment the system changes itself, and a project grows environments without
// asking this checkout.

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// Environment is one environment as the control plane reports it.
type Environment struct {
	Ref    string
	Name   string
	Status string
}

// Product is the project an environment belongs to.
type Product struct {
	ID   string
	Name string
}

// The control plane is reached through package-level hooks, wired in
// cmd/palbase/main.go's PersistentPreRunE — the same idiom as
// CloudProjectAddress and CloudRuntimePreparer. Importing the transport here
// would tie this package to a leaf of the command tree and produce a cycle.
//
// EVERY ONE OF THESE HAS A WRITER, and that is checked rather than assumed: a
// flag with a reader and no writer is a dead wire, and this one would surface
// as "no tenant host configured" on every address it tried to build.
var (
	EnvironmentsOf func(ctx context.Context, productID string) ([]Environment, error)
	ProductOfRef   func(ctx context.Context, ref string) (Product, error)
	// SelectedEnvFlag carries the root command's --env value. A cobra flag
	// cannot be read from a function that takes no command, and ResolveFor is
	// called from packages that never see the root.
	SelectedEnvFlag string
	// TenantHost is the suffix an environment ref lives under.
	TenantHost string
)

// Resolved is the full address a verb acts on.
type Resolved struct {
	Target Target
	Env    string
	Ref    string
	URL    string
	// Source names which rule answered, so a banner and an error can both say
	// why this environment and not another.
	Source string
}

// Describe is what every verb prints before it acts.
func (r Resolved) Describe() string {
	if r.Target.Local {
		return r.URL + " (local)"
	}
	if r.Env != "" {
		return projectLabel(r.Target) + "/" + r.Env
	}
	return r.URL
}

// ArtifactEnv is the name the per-environment directory takes.
//
// A CLOUD ENVIRONMENT HAS A NAME; A SELF-HOSTED STACK HAS ONE ENVIRONMENT and
// the app knows it as `main`. That constant used to be the answer for
// EVERYTHING — `defaultEnvName` returned "main" for every cloud checkout too,
// so two environments shared one directory and the last link overwrote the
// first. It survives only where it is actually true: one installation, one
// identity, one environment.
func (r Resolved) ArtifactEnv() string {
	if r.Env != "" {
		return r.Env
	}
	return soleEnvName
}

// soleEnvName is what a stack with exactly one environment calls it.
const soleEnvName = "main"

func projectLabel(t Target) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Project
}

func addressOf(ref string) (string, error) {
	if strings.TrimSpace(TenantHost) == "" {
		return "", fmt.Errorf("this CLI has no tenant host configured, so %q cannot be resolved to an address", ref)
	}
	return "https://" + ref + "." + TenantHost, nil
}

// Acting is the target a verb acts on: the committed record carrying the
// RESOLVED address. Existing verbs take a Target; this is how they get one
// without learning a second shape.
func (r Resolved) Acting() Target {
	t := r.Target
	t.URL = r.URL
	return t
}

// ResolveFor answers where this verb acts. The command is used for its context
// and nothing else — `Resolve` is the same answer for callers that hold only a
// context.
func ResolveFor(cmd *cobra.Command) (Resolved, error) {
	ctx := context.Background()
	if cmd != nil && cmd.Context() != nil {
		ctx = cmd.Context()
	}
	return Resolve(ctx)
}

// selfHostResolved is the one answer for a stack somebody runs, wherever its
// address is recorded: the committed file (a real hostname) or this machine's
// state (a loopback address, possibly a tunnel). One installation has one
// environment, so a named `--env` is refused rather than silently ignored.
func selfHostResolved(target Target) (Resolved, error) {
	if envNamed() != "" {
		return Resolved{}, fmt.Errorf(
			"this checkout is linked to %s, which is one installation with one environment — "+
				"--env selects between a cloud project's environments and has nothing to select here",
			target.URL)
	}
	return Resolved{Target: target, URL: target.URL, Source: "url"}, nil
}

// Resolve answers where this verb acts.
func Resolve(ctx context.Context) (Resolved, error) {
	// A RUNNING LOCAL STACK WINS, and it is asked FIRST — before the committed
	// record is even read.
	//
	// That order is not a preference, it is the normal case for `palbase start`:
	// a fresh `palbase init` checkout has no `project.json` at all, `start`
	// brings a stack up, and every verb then acts on it. Reading the committed
	// file first would answer "this checkout is not linked to a project" to
	// somebody whose stack is running in front of them — the same shape of
	// defect `stackVersion` once had, one layer up.
	//
	// An explicitly named environment still beats it: `--env` is the caller
	// saying where they mean, and nothing should override that.
	local, localErr := ReadTarget()
	if envNamed() == "" && localErr == nil && local.Local {
		return Resolved{Target: local, URL: local.URL, Source: "local"}, nil
	}
	// A LOOPBACK LINK IS ONE INSTALLATION, just stored on this machine instead of
	// in the committed file. It answers exactly as a committed self-host address
	// does — including refusing `--env` by name — and is never "local": that
	// word means a stack `palbase start` brought up here.
	if localErr == nil && local.SelfHost {
		return selfHostResolved(local)
	}

	target, err := readLinkedProject()
	if err != nil {
		return Resolved{}, err
	}

	// A SELF-HOSTED STACK IS ONE ENVIRONMENT. Saying so by name beats resolving
	// a flag against a project that does not exist.
	//
	// AN OLD CLOUD CHECKOUT IS NOT SELF-HOST. Before the identity format, a
	// cloud link wrote `{"url":"https://<ref>.<host>"}` — URL set, Project
	// empty, exactly the shape this branch matches. Swallowing it here would
	// pin the stale address forever and answer `--env` with "one installation
	// with one environment", which is false for a cloud project. So the branch
	// asks whether the address is one of OUR tenants, and a cloud one falls
	// through to the migration path instead.
	if strings.TrimSpace(target.URL) != "" && strings.TrimSpace(target.Project) == "" &&
		!isCloudProjectAddress(target.URL) {
		return selfHostResolved(target)
	}

	// 3. AN OLD CHECKOUT THE MIGRATION COULD NOT MOVE STILL WORKS (FR-061).
	//
	// The branch above deliberately lets a cloud address fall through so the
	// migration can rewrite it. But the migration is best-effort: it returns
	// without touching anything when the control plane cannot say which product
	// a ref belongs to (offline, a deleted environment, an older CLI's record),
	// and `Resolve` is also called on paths that never run it at all. Without
	// this branch those cases reach `environmentsOf("")` — asking the cloud for
	// the environments of an empty product id — and every verb fails in a
	// checkout that used to work. NFR-005 is that no verb errors on an old
	// record; FR-061 is that a migration may not break a working checkout.
	//
	// So the address it already has IS the answer. `--env` is the exception and
	// it refuses: choosing between environments needs a project, and this
	// record does not name one.
	if strings.TrimSpace(target.Project) == "" && strings.TrimSpace(target.URL) != "" {
		named := envNamed()
		if named == "" {
			return Resolved{Target: target, URL: target.URL, Source: "legacy"}, nil
		}
		// THE RECORD NAMES NO PROJECT, BUT ITS ADDRESS CARRIES A REF, and that
		// ref belongs to one. Deriving the product here is what lets `--env`
		// keep working on a checkout the migration has not moved yet.
		//
		// The product id is derived EXPLICITLY rather than left empty: passing
		// "" to the listing hook is what this branch exists to prevent, and a
		// test double that ignores its argument makes that mistake invisible —
		// which is exactly how it survived until the product was run.
		if ref := refOfURL(target.URL); ref != "" && ProductOfRef != nil {
			if product, prodErr := ProductOfRef(ctx, ref); prodErr == nil && strings.TrimSpace(product.ID) != "" {
				derived := target
				derived.Project = product.ID
				derived.Name = product.Name
				return resolveNamed(ctx, derived, named)
			}
		}
		return Resolved{}, fmt.Errorf(
			"palbase/project.json still records one environment's address (%s) and the cloud "+
				"did not say which project it belongs to, so %q cannot be resolved.\n"+
				"  palbase link <project>   bind this checkout to the project again",
			target.URL, named)
	}

	// 1-2. WHAT THE CALLER NAMED, this call only. It never writes the persisted
	// selection: an override is for one call, and a flag that quietly became
	// the new default would make the NEXT command act on an environment nobody
	// named.
	if named := envNamed(); named != "" {
		return resolveNamed(ctx, target, named)
	}

	// 4. THIS MACHINE'S REMEMBERED CHOICE — dropped when it belongs to a
	// different project, because a checkout relinked elsewhere must not inherit
	// an address from the project it left. The selection carries the ref, so
	// this path asks the control plane nothing.
	if sel, selErr := ReadSelection("."); selErr == nil && sel.Ref != "" && sel.Project == target.Project {
		url, addrErr := environmentAddress(sel.Ref)
		if addrErr != nil {
			return Resolved{}, addrErr
		}
		return Resolved{Target: target, Env: sel.Env, Ref: sel.Ref, URL: url, Source: "selection"}, nil
	}

	// 5-6. THE PROJECT'S ONLY ENVIRONMENT, or a refusal that lists them.
	envs, err := environmentsOf(ctx, target.Project)
	if err != nil {
		return Resolved{}, err
	}
	if len(envs) == 1 {
		url, addrErr := environmentAddress(envs[0].Ref)
		if addrErr != nil {
			return Resolved{}, addrErr
		}
		return Resolved{Target: target, Env: envs[0].Name, Ref: envs[0].Ref, URL: url, Source: "only"}, nil
	}
	return Resolved{}, ambiguous(target, envs)
}

func envNamed() string {
	if v := strings.TrimSpace(SelectedEnvFlag); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("PALBASE_ENV"))
}

func environmentsOf(ctx context.Context, productID string) ([]Environment, error) {
	if EnvironmentsOf == nil {
		return nil, fmt.Errorf("this CLI cannot ask the cloud which environments %q has", productID)
	}
	return EnvironmentsOf(ctx, productID)
}

// resolveNamed turns what the caller typed into one environment.
//
// A REF NEEDS NO LOOKUP. The address is derived from it, so `--env <ref>` costs
// no network call at all; only a NAME has to be matched against the project's
// list.
func resolveNamed(ctx context.Context, target Target, named string) (Resolved, error) {
	// A LISTING WE COULD NOT READ IS NOT A LICENCE TO GUESS.
	//
	// This branch used to fall back on "it looks like a ref, build the address"
	// — and that was the fail-open this whole design exists to prevent.
	// `isCanonicalProjectRef` is `^[a-z0-9]{4,24}$`, which matches `main`,
	// `prod`, `staging` and `production`: every word a person actually types.
	// So a failed listing (a network blip, or a hook nobody wired) turned
	// `--env production` into `https://production.<host>` silently. Worse, it
	// skipped the membership check the listing path performs, so a ref
	// belonging to ANOTHER product resolved as if it were this project's.
	//
	// We cannot attribute `named` to this project without the list, and acting
	// on an environment we cannot attribute is exactly what the refusal rule
	// forbids. The error names the cause so a reader knows it is not a typo.
	envs, err := environmentsOf(ctx, target.Project)
	if err != nil {
		return Resolved{}, fmt.Errorf(
			"cannot check that %q is an environment of %s: %w", named, projectLabel(target), err)
	}
	for _, e := range envs {
		if strings.EqualFold(e.Name, named) || e.Ref == named {
			url, addrErr := environmentAddress(e.Ref)
			if addrErr != nil {
				return Resolved{}, addrErr
			}
			return Resolved{Target: target, Env: e.Name, Ref: e.Ref, URL: url, Source: "flag"}, nil
		}
	}
	return Resolved{}, fmt.Errorf("%q is not an environment of %s.\n%s",
		named, projectLabel(target), listing(envs))
}

func listing(envs []Environment) string {
	// THE REFS LINE UP. This list is the most-read output the change has: it is
	// what a person sees at the moment a verb refuses, and it is where they
	// pick the name they will type next. Ragged columns make two short rows
	// look like a formatting accident rather than a menu.
	widest := 0
	for _, e := range envs {
		if n := len([]rune(e.Name)); n > widest {
			widest = n
		}
	}
	rows := make([]string, 0, len(envs))
	for _, e := range envs {
		rows = append(rows, fmt.Sprintf("  %-*s   %s", widest, e.Name, e.Ref))
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

// ambiguous refuses, and names what the reader can do about it.
func ambiguous(target Target, envs []Environment) error {
	if len(envs) == 0 {
		return fmt.Errorf("%s has no environments yet — `palbase env create <name>` makes one",
			projectLabel(target))
	}
	return fmt.Errorf("%s has %d environments and none is selected:\n%s\n"+
		"  palbase env use <name>        remember one for this checkout\n"+
		"  palbase <verb> --env <name>   act on one without remembering it",
		projectLabel(target), len(envs), listing(envs))
}

// MigrateLegacyTarget rewrites an old committed address as an identity.
//
// WHY IT EXISTS. Before the identity format, a cloud link wrote the RESOLVED
// ADDRESS — `{"url":"https://<ref>.<host>"}`. That file pins one tenant: the
// project can grow a second environment and the checkout never learns, and
// `--env` has nothing to resolve against because the file names no project.
//
// IT IS BEST EFFORT, AND THAT IS THE POINT. Fixing the producer does not move
// what the producer already wrote, so this runs on the read path — but a
// migration that FAILED must leave the checkout exactly as it was. A control
// plane that cannot be reached, a ref somebody else owns: either way the verb
// carries on against the address it has. A migration is not allowed to break a
// checkout that worked a minute ago.
//
// IT SAYS WHAT IT DID. A committed file that changed under somebody without a
// word is worse than one that did not change.
//
// AND IT DROPS WHAT AN OLDER CLI COMMITTED UNDER A RETIRED FIELD
// (dropRetiredFields), when rewriting the address has not already done so.
func MigrateLegacyTarget(ctx context.Context, w io.Writer) error {
	target, err := readLinkedProject()
	if err != nil {
		return nil // nothing linked: nothing to migrate
	}
	moved, err := migrateLegacyAddress(ctx, w, target)
	if err != nil || moved {
		// A rewritten file carries no retired field: WriteTarget serialises
		// Target, and the retired values are not part of it.
		return err
	}
	dropRetiredFields(w, target)
	return nil
}

// migrateLegacyAddress is the address half of MigrateLegacyTarget, and says
// whether it rewrote the committed file.
func migrateLegacyAddress(ctx context.Context, w io.Writer, target Target) (bool, error) {
	if strings.TrimSpace(target.Project) != "" || strings.TrimSpace(target.URL) == "" {
		return false, nil // already an identity, or nothing to work with
	}
	if !isCloudProjectAddress(target.URL) {
		return false, nil // a stack somebody runs: one installation, one address
	}
	ref := refOfURL(target.URL)
	if ref == "" || ProductOfRef == nil {
		return false, nil
	}
	product, err := ProductOfRef(ctx, ref)
	if err != nil || strings.TrimSpace(product.ID) == "" {
		return false, nil
	}

	// THE ENVIRONMENT THE OLD FILE NAMED IS KEPT, and keeping it is the whole
	// difference between a migration and a break.
	//
	// The old record said "act on this address", and that address is one
	// environment. Rewriting the file to a project identity and stopping there
	// changes the answer to "which one?" — so the next verb in a checkout that
	// worked a minute ago REFUSES. Measured on the product: `palbase plan` in a
	// migrated checkout of a two-environment project. NFR-005 says no verb
	// errors on an old record, and this is how that is honoured: the intent
	// moves to where a choice belongs, MACHINE-LOCAL, uncommitted — the same
	// place `palbase env use` writes. Nothing new appears in the repository.
	envName := ""
	if envs, envErr := environmentsOf(ctx, product.ID); envErr == nil {
		for _, e := range envs {
			if e.Ref == ref {
				envName = e.Name
				break
			}
		}
	}
	if envName == "" {
		// FR-061: a migration that cannot finish leaves the checkout exactly as
		// it was. Writing the identity without the environment would produce
		// the refusal this comment exists to prevent.
		return false, nil
	}

	migrated := target
	migrated.Project = product.ID
	migrated.Name = product.Name
	migrated.URL = ""
	if err := WriteTarget(migrated); err != nil {
		// A REWRITE THAT FAILED MOVES NOTHING (FR-061), and this is the one error
		// in the migration that does not fail the verb. It is the write of the
		// committed file itself — read-only, a full disk — and nothing has changed
		// yet: the selection below is written only after it. So the checkout is
		// still the old record, exactly as it was, and the verb carries on against
		// the address it has. Returning the error here failed every verb in a
		// read-only checkout the cloud could resolve, with `open
		// palbase/project.json: permission denied`. The errors after this line
		// still fail the verb, because by then the file HAS changed.
		return false, nil
	}
	// A DELIBERATE CHOICE IS NOT OVERWRITTEN: somebody who already ran
	// `palbase env use` on this checkout means it, and the old address is the
	// staler of the two facts.
	kept := ""
	if sel, selErr := ReadSelection("."); selErr != nil || sel.Ref == "" || sel.Project != product.ID {
		root, wdErr := os.Getwd()
		if wdErr != nil {
			return true, wdErr
		}
		if err := WriteSelection(root, Selection{Project: product.ID, Env: envName, Ref: ref}); err != nil {
			return true, err
		}
		kept = envName
	} else {
		kept = sel.Env
	}
	fmt.Fprintf(w, "▸ %s now records the project %q rather than one environment's address; "+
		"this machine keeps acting on %s (`palbase env use <name>` or `--env <name>` to change it)",
		projectPath(), product.Name, kept)
	// THE ADDRESS DECIDED. A retired `env` beside it chose nothing, and the line
	// says what it named so nobody takes it for the environment kept above.
	if env := target.retired.env; env != nil {
		fmt.Fprintf(w, "; the environment the file also named (%s) is not chosen from a committed file",
			droppedValue(*env))
	}
	fmt.Fprintln(w)
	return true, nil
}

// dropRetiredFields rewrites a committed file that still carries a field an
// older CLI wrote and this one retired (targetFile), and asks nobody anything.
//
// The read no longer needs it — both fields are tolerated by name — but every
// clone of the repository would carry that tolerance forward.
//
// `env` IS DROPPED, NOT MOVED. The first attempt moved it to this machine's
// selection, and the verb running the migration then acted on that environment
// in the same run. No release since v0.34.0 routed on it: `fda7a7b` removed the
// `palbase env <slug>` that wrote it, and from then until it retired only the
// banner label and an artifact directory name read it. A committed environment
// is how a colleague pulls your branch and pushes to your staging
// (Target.Project), so the environment is resolved by today's rules — the only
// one, or a refusal that lists them — and the line says what the file named and
// how to choose.
//
// THE VALUE IS QUOTED AND CUT (droppedValue), as it is on the address
// migration's line.
//
// BEST EFFORT, THE SAME RULE AS THE ADDRESS (FR-061). A rewrite that fails
// leaves the file exactly as it was and says nothing, and the verb carries on.
func dropRetiredFields(w io.Writer, target Target) {
	names := target.retired.names()
	if len(names) == 0 {
		return
	}
	if err := WriteTarget(target); err != nil {
		return
	}
	noun := "field"
	if len(names) > 1 {
		noun = "fields"
	}
	fmt.Fprintf(w, "▸ %s no longer carries the retired %s %s", projectPath(), strings.Join(names, " and "), noun)
	if env := target.retired.env; env != nil {
		fmt.Fprintf(w, "; the environment it named (%s) is not chosen from a committed file — "+
			"`palbase env use <name>` or `--env <name>` chooses one", droppedValue(*env))
	}
	fmt.Fprintln(w)
}

// droppedValue is how a migration line names a value it dropped from a
// committed file: at most its first 64 runes, quoted.
//
// QUOTED, because a committed file must not put a control character on
// somebody's terminal. CUT, because it must not put a hundred kibibytes there
// either. The quoting is applied to what is kept, so the cut can never split an
// escape, and the mark that says it was cut sits outside the quotes.
func droppedValue(value string) string {
	const most = 64
	runes := 0
	for i := range value {
		if runes == most {
			return fmt.Sprintf("%q…", value[:i])
		}
		runes++
	}
	return fmt.Sprintf("%q", value)
}
