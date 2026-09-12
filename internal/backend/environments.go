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
	if envNamed() == "" {
		if local, localErr := ReadTarget(); localErr == nil && local.Local {
			return Resolved{Target: local, URL: local.URL, Source: "local"}, nil
		}
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
		if envNamed() != "" {
			return Resolved{}, fmt.Errorf(
				"this checkout is linked to %s, which is one installation with one environment — "+
					"--env selects between a cloud project's environments and has nothing to select here",
				target.URL)
		}
		return Resolved{Target: target, URL: target.URL, Source: "url"}, nil
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
		url, addrErr := addressOf(sel.Ref)
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
		url, addrErr := addressOf(envs[0].Ref)
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
			url, addrErr := addressOf(e.Ref)
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
	rows := make([]string, 0, len(envs))
	for _, e := range envs {
		rows = append(rows, "  "+e.Name+"   "+e.Ref)
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
func MigrateLegacyTarget(ctx context.Context, w io.Writer) error {
	target, err := readLinkedProject()
	if err != nil {
		return nil // nothing linked: nothing to migrate
	}
	if strings.TrimSpace(target.Project) != "" || strings.TrimSpace(target.URL) == "" {
		return nil // already an identity, or nothing to work with
	}
	if !isCloudProjectAddress(target.URL) {
		return nil // a stack somebody runs: one installation, one address
	}
	ref := refOfURL(target.URL)
	if ref == "" || ProductOfRef == nil {
		return nil
	}
	product, err := ProductOfRef(ctx, ref)
	if err != nil || strings.TrimSpace(product.ID) == "" {
		return nil
	}

	migrated := target
	migrated.Project = product.ID
	migrated.Name = product.Name
	migrated.URL = ""
	if err := WriteTarget(migrated); err != nil {
		return err
	}
	fmt.Fprintf(w, "▸ %s now records the project %q rather than one environment's address — "+
		"pick an environment with `palbase env use <name>` or `--env <name>`\n",
		projectPath(), product.Name)
	return nil
}
