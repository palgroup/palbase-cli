package backend

// project_link.go — `palbase link <target>` : binding an app to a project.
//
// TARGET-RELATIVE, which is the rule the design settled on
// (docs/paltimate/2026-08-12-v2-faz0-selfhost/design-management-api.md §6, §10):
// a command that touches ONE tenant works against whatever target it is given,
// and only who authenticates changes. What you write decides which it is —
// something carrying a scheme is the target itself and no control plane is
// asked; a bare environment ref is resolved by ours.
//
// This is the direct half. `palbase ios link` still owns the ref half, because
// that one resolves a project in the cloud, registers an app and asks for an
// environment's key — none of which a project somebody runs has: it is one
// installation with one identity and one pair of keys in the .env beside it.
// The two meet when the management API exists on both sides; until then, the
// direct half is what makes a project you run usable at all.
//
// It writes the SAME slot files `link` writes, so everything after this point —
// `palbase spec`, the Swift generator, `palbe-gen` — behaves identically whether
// the stack is yours or ours. That is the point: one toolchain, two hosts.
import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/sealedclient"
)

// projectAppID names the app slot a linked checkout writes.
//
// A cloud project mints an app id when an app registers; a project somebody runs
// has no registry to mint one from, and inventing a value would put something in
// a committed file that means nothing and can never be looked up. The project's
// own name is the honest answer — and it matches the identity the stack boots
// with (`migrate.BootStackRef`), which is also what its API key carries.
const projectAppID = "project"

type linkOpts struct {
	checkoutRoot string
	url          string
	// linkedEnv is the NAME of the environment this link read the contract and
	// the key from. It decides which directory the artifacts land in; it is
	// never committed.
	linkedEnv string
	// product is set when the target was resolved from a cloud project rather
	// than typed as an address. It decides WHAT the committed file records: an
	// identity for a cloud project, an address for a stack somebody runs.
	product    Product
	platforms  []string
	insecure   bool
	tokenStdin bool
	// env names WHICH environment this link reads the contract and the key
	// from. It does not enter the committed file — that records the project.
	env string
	// Web only, and both optional: where the generated client's import goes,
	// and what it is called. They travelled with `palbase web link`; the work
	// they steer moved into this command with the rest of the web wiring.
	entry string
	out   string
}

// linkHelpWritesBlock renders the "It writes:" table shown by
// `palbase link --help`, FROM the same functions (layout.go's RootDir/EnvDir/
// ConfigPath/SpecPath/GeneratedPath, target.go's projectPath) that decide
// where `link` actually writes — not spelled out here a second time.
//
// This text used to be a hand-written copy of the layout, and it drifted: it
// still named `.palbase/project.json`, `.palbase/<platform>/palbase-config.json`,
// `.palbase/openapi/<env>.json` and `Palbase/Generated/` after all four moved
// to the visible, environment-scoped `palbase/` tree (measured 11.09.2026).
// `template/AGENTS.md` tells a scaffolded project's agent that "`palbase
// --help` is authoritative; a copy of the command surface inside this
// repository goes stale silently" — this function is what keeps that promise
// for `link` itself: there is no second copy left to go stale.
func linkHelpWritesBlock() string {
	const env, platform = "<env>", "<platform>"
	rows := []struct {
		path string
		what string
	}{
		{projectPath(), "the project this checkout belongs to"},
		{ConfigPath(env, platform), "the app's URL + publishable key"},
		{SpecPath(env), "the contract, one per environment"},
		{GeneratedPath(env, "ios"), "(apple) the committed Swift client"},
	}
	width := 0
	for _, row := range rows {
		if len(row.path) > width {
			width = len(row.path)
		}
	}
	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, "  %-*s   %s\n", width, row.path, row.what)
	}
	return strings.TrimRight(b.String(), "\n")
}

func newLinkCmd(r Resolvers) *cobra.Command {
	var o linkOpts
	cmd := &cobra.Command{
		Use:   "link <target>",
		Args:  cobra.MaximumNArgs(1),
		Short: "Bind this app to a stack and generate its client",
		Long: `Bind this checkout to a project, and generate the typed client for it.

    palbase link http://localhost:54321      something running on this machine
    palbase link todoapp                     a project in the cloud

Linking is something you do AS SOMEBODY. Both the publishable key and the
contract come from the project over authenticated routes — the public document
says only what kind of thing answered — so a stranger who knows the address gets
neither. The credential comes from ` + "`palbase start`" + ` (which writes one for the
stack it just brought up), from ` + "`palbase login`" + `, or from PALBASE_ACCESS_TOKEN.

It writes:

` + linkHelpWritesBlock() + `

Run it again after every ` + "`palbase push`" + ` — or just ` + "`palbase spec`" + `, which
refreshes the contract alone, because that is the part that changed.

--insecure is for an address still using the self-signed certificate its first
boot generated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The target may be written as an argument or as --url; they are the
			// same thing, and refusing one of them would be a rule to remember
			// for no reason.
			if len(args) == 1 && o.url == "" {
				o.url = args[0]
			}
			// A BARE REF IS AN ADDRESS THIS CLOUD KNOWS, and the help above has
			// promised so all along: "palbase link <project> — a project in the
			// cloud". The code refused it and sent people to `palbase ios link`,
			// which links an iOS APP — no use at all to a backend checkout, which
			// then had no way to reach a cloud project by name. The only thing
			// missing was the suffix, and the configured cloud carries it.
			if o.url != "" && !strings.Contains(o.url, "://") {
				// A BARE WORD NAMES A PROJECT, and a project is resolved to its
				// PRODUCT — not to one environment's address.
				//
				// THE OLD PATH BROKE THE MOMENT A PROJECT GREW A SECOND
				// ENVIRONMENT. It asked `/v1/cloud/projects`, which returns one
				// row per ENVIRONMENT carrying the PRODUCT's name, and refused
				// to choose when two rows shared a name — so `palbase link
				// todoapp` fell through to the ref-shape check, and "todoapp"
				// is 7 lowercase letters, so it built `https://todoapp.<host>`
				// and reported "does not look like a Palbase stack". The
				// failure named a network problem for a modelling one.
				//
				// Now the listing is by product, so two environments are two
				// entries under ONE answer and nothing is ambiguous.
				product, envs, err := productByName(cmd.Context(), r, o.url)
				if err != nil {
					return err
				}
				o.product = product
				// The address to TALK to during this link is one environment's,
				// and it is only used for this call: the contract, the key and
				// the public document all come from a running stack. What gets
				// COMMITTED is the identity (writeLinkRecord).
				ref, refErr := linkEnvironmentRef(product, envs, o.env)
				if refErr != nil {
					return refErr
				}
				for _, e := range envs {
					if e.Ref == ref {
						o.linkedEnv = e.Name
					}
				}
				host := r.Endpoints().PublicHost
				if host == "" {
					return fmt.Errorf("this CLI has no tenant host configured, so %q cannot be resolved to an address", ref)
				}
				o.url = "https://" + ref + "." + host
			}
			return runLink(cmd.Context(), o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.url, "url", "", "the stack's base URL (e.g. https://127.0.0.1)")
	// NO DEFAULT: an empty list means "read the checkout", and a default of
	// `ios` meant `palbase link` in a web-only project wrote Apple artifacts and
	// nothing else — silently, because a wrong default looks exactly like a
	// right one. The flag stays for the case where somebody wants LESS than
	// what is here, not to make them repeat what is already true.
	f.StringSliceVar(&o.platforms, "platform", nil, "limit to ios, macos, android or web (default: whatever this checkout is)")
	f.BoolVar(&o.insecure, "insecure", false, "accept the stack's self-signed certificate")
	f.BoolVar(&o.tokenStdin, "token-stdin", false,
		"read this stack's key from stdin and remember it for this address (self-hosted stacks)")
	// WHICH environment this link reads the contract and the key from. The
	// committed file records the PROJECT either way; this only decides where
	// the artifacts in this checkout come from.
	f.StringVar(&o.env, "from-env", "", "environment to read the contract and key from (default: the project's only one)")
	f.StringVar(&o.entry, "entry", "", "web: entry file to wire the generated client into (auto-detected when absent)")
	f.StringVar(&o.out, "out", "", "web: name for the generated client (default: palbe.gen.ts)")
	return cmd
}

// productByName resolves what a person typed to ONE product.
//
// It asks the CLI surface (`/api/v2/projects`), which groups environments under
// their product — so a project with two environments is one answer here rather
// than two rows that look like a collision.
func productByName(ctx context.Context, r Resolvers, typed string) (Product, []Environment, error) {
	if r.REST == nil || r.REST() == nil {
		return Product{}, nil, fmt.Errorf(
			"%q is not an address, and this CLI has no cloud session to resolve it as a project — "+
				"`palbase login`, or pass the stack's URL", typed)
	}
	var rows []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Environments []struct {
			Ref    string `json:"ref"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"environments"`
	}
	if err := r.REST().Do(ctx, http.MethodGet, "/api/v2/projects", nil, &rows); err != nil {
		return Product{}, nil, err
	}
	type match struct {
		product Product
		envs    []Environment
	}
	var matches []match
	var known []string
	for _, p := range rows {
		known = append(known, p.Name)
		if !strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(typed)) && p.ID != typed {
			continue
		}
		envs := make([]Environment, 0, len(p.Environments))
		for _, e := range p.Environments {
			envs = append(envs, Environment{Ref: e.Ref, Name: e.Name, Status: e.Status})
		}
		matches = append(matches, match{product: Product{ID: p.ID, Name: p.Name}, envs: envs})
	}
	switch len(matches) {
	case 1:
		// ONE CALL, ONE SOURCE. The environments came back inside the product,
		// so picking which one to read from asks the cloud nothing further —
		// and asking through a second channel would let the two disagree.
		return matches[0].product, matches[0].envs, nil
	case 0:
		sort.Strings(known)
		if len(known) == 0 {
			return Product{}, nil, fmt.Errorf("you have no projects yet — `palbase project create %s`", typed)
		}
		return Product{}, nil, fmt.Errorf("no project of yours is called %q. Yours:\n  %s",
			typed, strings.Join(known, "\n  "))
	default:
		// TWO PRODUCTS, ONE NAME. Choosing would be choosing somebody's
		// production at random; the id disambiguates and the listing prints it.
		return Product{}, nil, fmt.Errorf(
			"%d of your projects are called %q — name one by its id instead (`palbase project list` prints them)",
			len(matches), typed)
	}
}

// linkEnvironmentRef picks the environment this link reads FROM.
//
// It is not written down: the contract and the publishable key are facts about
// one running environment, and which one a later verb acts on is resolved then.
// With more than one and nothing named, it refuses — the same rule every other
// verb follows, for the same reason.
func linkEnvironmentRef(product Product, envs []Environment, named string) (string, error) {
	if named != "" {
		for _, e := range envs {
			if strings.EqualFold(e.Name, named) || e.Ref == named {
				return e.Ref, nil
			}
		}
		return "", fmt.Errorf("%q is not an environment of %s.\n%s", named, product.Name, listing(envs))
	}
	if len(envs) == 1 {
		return envs[0].Ref, nil
	}
	return "", ambiguous(Target{Project: product.ID, Name: product.Name}, envs)
}

// writeLinkRecord commits WHAT THIS CHECKOUT IS BOUND TO, and the two shapes
// are not interchangeable.
//
// A cloud project is recorded by IDENTITY: the product id and its name, no
// address. Which environment a verb acts on is resolved per call, so an address
// here would pin one tenant and make `--env` a lie.
//
// A stack somebody runs is recorded by ADDRESS, because that is all it has —
// one installation, one identity, one pair of keys. That path also refuses a
// loopback address, which is the defect measured in this very repository.
func writeLinkRecord(o linkOpts, target Target) error {
	if o.product.ID != "" {
		return WriteLinkedIdentity(o.product, target)
	}
	return WriteSelfHostTarget(target)
}

func runLinkPrepared(ctx context.Context, o linkOpts, w io.Writer) error {
	// BEFORE ANY NETWORK. A typo in --platform used to surface as a connection
	// error from the address, which reads as "the stack is down" and sends the
	// reader to the wrong problem entirely. What the caller typed can be judged
	// without asking anybody.
	if err := validatePlatforms(o.platforms); err != nil {
		return err
	}
	// AND A PLATFORM THIS DIRECTORY CANNOT SUPPORT, also before anything is
	// written. The refusal used to arrive from inside the web wiring — AFTER
	// the contract and the config were already on disk — so a typo in
	// `--platform` produced a half-done link reported as an error. What the
	// caller typed can be judged against what is here without asking anybody.
	if err := refuseUnsupportedPlatforms(o.platforms); err != nil {
		return err
	}

	// AND WHAT THIS CHECKOUT IS, also before any network. Reading a directory
	// needs nobody's permission, and saying what was found first means a later
	// connection error reads as "the stack is down" rather than as "link did not
	// understand my project".
	platforms := o.platforms
	if len(platforms) == 0 {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		platforms = detectPlatforms(root)
		if len(platforms) == 0 {
			// NO CLIENT IS NOT NO LINK. Binding a checkout to a stack and
			// writing a client's artifacts are two jobs, and only the second one
			// needs a platform. This used to REFUSE, which broke the flow the
			// product is built around: `palbase init` scaffolds a backend, a
			// backend has no Xcode project and no package.json beside an
			// index.html, so `link` refused it — while `push` in the same
			// directory said "run `palbase link` in this directory". The two
			// commands pointed at each other and neither could be satisfied.
			//
			// Say what was looked for, then bind. An explicitly NAMED platform
			// this directory cannot carry is still refused, before any write —
			// that is a typo, not a backend.
			fmt.Fprintf(w, "▸ no client app here (looked for an Xcode project or workspace, an "+
				"Android applicationId in build.gradle[.kts], and a package.json beside an "+
				"index.html/public/src/app) — linking the backend only\n")
		} else {
			fmt.Fprintf(w, "▸ %s\n", strings.Join(platforms, ", "))
		}
	}

	base := strings.TrimRight(strings.TrimSpace(o.url), "/")
	if base == "" {
		// A BOUND CHECKOUT ALREADY NAMES ITS ADDRESS. Re-linking is how you
		// refresh generated clients after a contract change, and demanding the
		// URL again asks the reader to retype what the committed file says —
		// which is also how the two drift apart.
		if target, err := ReadTarget(); err == nil && strings.TrimSpace(target.URL) != "" {
			base = strings.TrimRight(strings.TrimSpace(target.URL), "/")
		}
	}
	if base == "" {
		return errors.New("--url is required: the address the stack serves on")
	}
	// What is at this address, and does it answer at all. One request, no
	// credential: the document says WHAT answered and which SDK it runs, and a
	// project that does not answer is either not a Palbase project or not up.
	described, err := describeStack(ctx, base, o.insecure)
	if err != nil {
		return err
	}

	// A SELF-HOSTED STACK IS TOLD WHO YOU ARE, HERE.
	//
	// Nothing else can say: it is not on this machine and it is not in our
	// ledger. The key goes in through stdin rather than an argument so it stays
	// out of the shell's history, is checked against the stack before anything
	// is written, and is then remembered for this address — so `push`, `spec`,
	// `secret` and the rest need no environment variable.
	if o.tokenStdin {
		token, err := readTokenFrom(os.Stdin)
		if err != nil {
			return err
		}
		// DOĞRULANAMAMAK ÖLÜMCÜL DEĞİL, SÖYLENİR (FR-063). Servis etmeyen bir
		// kiracıda anahtar saklanır ve sebebi basılır — aksi hâlde `link`,
		// kurtarmanın önündeki son kilit olurdu.
		var unverified errUnverifiedToken
		switch err := storeVerifiedToken(ctx, Target{URL: base, Insecure: o.insecure}, token); {
		case errors.As(err, &unverified):
			fmt.Fprintf(w, "remembered this stack's key for %s — %s\n", base, unverified.reason)
		case err != nil:
			return err
		default:
			fmt.Fprintf(w, "remembered this stack's key for %s\n", base)
		}
	}

	// The publishable key comes from the project, over an authenticated route.
	// It used to ride on the public document, which meant anyone who knew the
	// address was handed a working client credential; now linking is something
	// you do as somebody.
	target := Target{URL: base, Insecure: o.insecure, checkoutRoot: o.checkoutRoot}
	if previous, err := readLinkedProject(); err == nil {
		target.OAuth = previous.OAuth
	} else if _, statErr := os.Stat(projectPath()); !errors.Is(statErr, os.ErrNotExist) {
		return err
	}
	anon, err := projectPublishableKey(ctx, target)
	if err != nil {
		return err
	}

	// Remember the target. `login`, `push` and `spec` read it, so none of them
	// asks for an address again — and a colleague who clones this repository
	// reaches the same stack without being told which one it is.
	if err := writeLinkRecord(o, target); err != nil {
		return err
	}

	// A CHECKOUT WITH NO GENERATOR GETS NO PER-ENVIRONMENT ARTIFACTS.
	//
	// The consumers of `palbase/environments/<env>/` are `palbe` and
	// `palbase-swiftgen`, both of which run in an APP checkout. A backend-only
	// checkout was handed `openapi.json` and `roles.json` on every link and
	// nothing in the product read them — a diff on every branch, for no reader.
	// `palbase spec` still writes them on request, because that verb is
	// somebody asking.
	linkedEnv := o.linkedEnv
	if linkedEnv == "" {
		// A stack somebody runs has ONE environment and the app knows it as
		// `main` — see Resolved.ArtifactEnv for why that constant survives only
		// here and not for cloud checkouts.
		linkedEnv = soleEnvName
	}
	// EVERY environment, not the one being linked. An app that holds only the
	// environment somebody linked last is an app whose address depends on when
	// it was built — which is how a TestFlight build ends up pointed at staging.
	//
	// The environment NAME is the one this link read from, not a constant: the
	// old `defaultEnvName` returned "main" for every cloud checkout, so a
	// second environment overwrote the first one's contract in place.
	envs, specs, err := gatherEnvironments(ctx, target, linkedEnv, anon, writesPerEnvironmentArtifacts(platforms), w)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(RootDir(), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", RootDir(), err)
	}
	// AND THE RETIRED RULES GO, every link.
	//
	// A customer upgrading from 0.61.x carries lines an older `link` appended —
	// `palbase-env.d.ts` among them, unanchored, which git matches at ANY depth.
	// The only code that takes those back is this one, and `link` had stopped
	// calling it: so `link` printed "commit palbase/" while a file inside it
	// stayed invisible to git. Retiring a producer is two acts, and this is the
	// second one for everybody who did not start from a fresh `init`.
	if err := takeBackRetiredIgnoreRules(".gitignore"); err != nil {
		return fmt.Errorf("update .gitignore: %w", err)
	}

	// GENERATED CODE IS MARKED AS SUCH, every link. A spec fetch can move
	// thousands of lines nobody wrote; unmarked, they arrive in a pull request
	// as a change somebody has to read.
	if err := writeGitattributes("."); err != nil {
		return fmt.Errorf("write %s/.gitattributes: %w", RootDir(), err)
	}
	// EACH PLATFORM GETS WHAT ITS OWN GENERATOR READS.
	//
	// This loop used to write one document for all of them and then run the
	// Apple steps unconditionally, so `--platform web` (and `--platform
	// android`) produced an Xcode build configuration, a Swift client, and a
	// config in the native shape sitting at the WEB generator's path. Measured
	// on 2026-08-25 in the palaicloud checkout: `palbase web link` — which
	// lands here whenever the checkout is bound to a target — wrote
	// `Palbase/Config/Main.xcconfig`, overwrote `Palbase/palbase-config.json`
	// with the environment map, and never wrote `Palbase/openapi.json`. Not one
	// step failed; `palbe-gen` simply had nothing it could read.
	//
	// The platform list is not decoration: it says which toolchain is on the
	// other end, and a link that ignores it is a link for one platform wearing
	// a flag for four.
	apple := false
	web := false
	for _, platform := range platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		selectedEnvs, configErr := platformEnvironments(ctx, &target, platform, envs)
		if configErr != nil {
			return configErr
		}
		// APPLE YUVASINI, APPLE PROJESİ OLMAYAN BİR CHECKOUT'A YAZMA.
		//
		// Yuva dosyası commit'lenir ve `spec`/`push` onu "burada bir Apple
		// istemcisi üretilir" diye okur. Bir backend deposuna yazıldığında o
		// okuma sonsuza kadar yanlış olur: üretici orada asla koşamaz ve her
		// push bir kusur satırıyla biter. Gerçek bir müşteri deposunda ölçüldü
		// (08.09.2026, centauri): `.palbase/ios/palbase-config.json` commit'liydi,
		// Xcode projesi yoktu, ve her `palbase push`
		//
		//   the push landed, but the client could not be regenerated: … the
		//   palbackend-ios checkout is not resolved for this project yet
		//
		// ile bitiyordu. Kaynağını yazmayı reddetmek, o satırın bir daha hiç
		// doğmaması demek.
		if isApplePlatform(platform) && !hasAppleProject(".") {
			return fmt.Errorf("this checkout has no Xcode project, so an Apple client cannot be generated here — "+
				"run `palbase link --platform %s` in the app's own checkout (the one holding the .xcodeproj)", platform)
		}
		paths, err := writeEnvironmentConfigs([]string{platform}, selectedEnvs)
		if err != nil {
			return err
		}
		if isApplePlatform(platform) {
			apple = true
		}
		if platform == webPlatform {
			web = true
		}
		for _, p := range paths {
			fmt.Fprintf(w, "wrote %s\n", p)
		}
	}

	if apple {
		// NOTHING IS WRITTEN INTO THE APP'S BUILD SYSTEM.
		//
		// This used to write one xcconfig per environment into
		// `Palbase/Config/` and then print an instruction to add a key to the
		// app's own Info.plist. The CLI could complete neither half — assigning
		// an xcconfig to a build configuration is a pbxproj edit it never makes
		// — so it shipped half a mechanism, and unwired the failure was the
		// worst shape there is: measured on a real simulator, a build in the
		// `Local` configuration signed up against the MAIN environment's
		// address while every build setting still read `local`.
		//
		// The customer owns their configuration system. What they need from us
		// is printed once, below, and it is theirs to place.
		printEnvironmentSelectionSnippet(w, envs.Default)
	}

	// THE SAME STEP APPLE GETS, FOR WEB. An Apple checkout leaves here with
	// build configurations and a compiled Swift client; a web checkout used to
	// leave with two JSON files and nothing that reads them. Its wiring lived
	// behind `palbase web link` and went unreachable when that command was
	// retired — so the platform that needs the most setup got the least.
	if web {
		if err := wireWebProject(ctx, o.entry, o.out, w); err != nil {
			return err
		}
	}

	// The contract, once per environment: they can differ, and a client merged
	// across them would compile calls that do not exist where the app points.
	// Swift only — the web client is generated by `palbe-gen`, which ships in
	// @palbase/web and reads the committed artifacts offline.
	if apple {
		if err := generateForEnvironmentsAt(ctx, envs, w, o.checkoutRoot); err != nil {
			return err
		}
	}
	reportContractDrift(specs, w)
	// THE SECOND WriteTarget USED TO CARRY A DERIVED FIELD FORWARD. It does not
	// any more: `stackVersion` is derived from the installed package on every
	// read and written nowhere, so there is nothing to re-read and preserve.
	if err := writeLinkRecord(o, target); err != nil {
		return err
	}
	reportLinked(w, o.product, base, described.Hosting, linkedEnv)

	// ONE DIRECTORY, SO ONE SENTENCE. The closing line used to name a pair
	// (`.palbase/` hidden, `Palbase/` visible) and branch three ways to say
	// which halves existed. Everything this CLI writes now lives under
	// `RootDir()`, so the line names it and cannot drift from the layout: a
	// spelled-out directory here would be a second truth about where things go.
	// WHAT TO COMMIT, IF ANYTHING.
	//
	// A backend-only checkout receives no per-environment artifacts and, when
	// the stack is one on this machine, no committed record either — so telling
	// that reader to "commit palbase/" would point them at a directory that
	// does not exist. The line names what was actually written.
	switch {
	case !writesPerEnvironmentArtifacts(platforms) && isLoopbackAddress(base):
		fmt.Fprintf(w, "this machine remembers this stack; nothing to commit\n")
	case !writesPerEnvironmentArtifacts(platforms):
		fmt.Fprintf(w, "commit %s\n", projectPath())
	default:
		fmt.Fprintf(w, "commit %s/\n", RootDir())
	}
	return nil
}

// webPlatform is the one platform whose generator lives in an SDK rather than
// in this CLI, and whose committed config is flat rather than per-environment.
const webPlatform = "web"

// knownPlatforms is the closed set `link` can actually build for.
var knownPlatforms = []string{"ios", "macos", "android", webPlatform}

// validatePlatforms refuses a value this CLI cannot build for, BY NAME.
//
// `--platform bogus` used to sail straight through: the loop below simply never
// matched it, so the run wrote nothing for that entry and said nothing about it.
// A flag that accepts anything teaches the reader that their typo worked, and
// they find out later — from a missing file rather than from the flag.
func validatePlatforms(platforms []string) error {
	for _, p := range platforms {
		name := strings.ToLower(strings.TrimSpace(p))
		if !slices.Contains(knownPlatforms, name) {
			return fmt.Errorf("%q is not a platform this can link: choose from %s",
				p, strings.Join(knownPlatforms, ", "))
		}
	}
	return nil
}

// runUnlink removes the bond, and the bond is the committed project file.
//
// The old `web unlink` deleted the SELECTION — a second addressing mechanism on
// its way out — and pointed the reader at `palbase web link`, a command on its
// way out too. What makes a checkout linked is projectPath() — today
// `palbase/project.json`; it used to be `.palbase/project.json`, the retired
// hidden root, which is why this is asked for rather than spelled out here.
//
// AN ALREADY-UNLINKED CHECKOUT IS NOT AN ERROR: the caller asked to end up
// somewhere, and it is already there.
func runUnlink(w io.Writer) error {
	path := projectPath()
	switch err := os.Remove(path); {
	case err == nil:
		fmt.Fprintf(w, "✓ unlinked — removed %s\n", path)
	case os.IsNotExist(err):
		fmt.Fprintln(w, "this checkout was not linked")
		return nil
	default:
		return fmt.Errorf("remove %s: %w", path, err)
	}
	// NO "remove the directory if it is empty" BRANCH. It could never fire:
	// every `link` writes `palbase/.gitattributes`, so something always lives
	// there — and `unlink` deliberately leaves the generated clients in place,
	// which is the sentence below. A branch that cannot run is one more thing a
	// reader has to prove does nothing.
	fmt.Fprintln(w, "  generated clients and their imports are left in place")
	fmt.Fprintln(w, "  re-link with `palbase link <url>`")
	return nil
}

func newUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Args:  cobra.NoArgs,
		Short: "Detach this checkout from its Palbase project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUnlink(cmd.OutOrStdout())
		},
	}
}

// isApplePlatform reports whether a platform is built by Xcode — the only ones
// an xcconfig, an Info.plist requirement, or a Swift client mean anything to.
func isApplePlatform(platform string) bool {
	return platform == "ios" || platform == "macos"
}

// describeStack asks a stack what it is.
//
// One request, no credential: the document is public because everything in it
// is (see the stack's internal/server/wellknown.go). A stack that does not
// answer is either not a Palbase stack or NOT UP YET, and saying which is the
// whole value of asking first — so the two are now actually told apart.
//
// They used to be collapsed into one sentence, and the sentence was the wrong
// one: a project created seconds earlier answers 503 from the cell's edge while
// it comes up, and `palbase link` told the person who had just created it that
// their address "does not look like a Palbase stack". The comment above already
// promised this distinction; only the code was missing it.
func describeStack(ctx context.Context, base string, insecure bool) (stackDescription, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	transport := http.DefaultTransport
	if insecure {
		transport = insecureTransport()
	}
	// Its own timeout, but never its own rule about what may leave in the
	// clear: every client that addresses a stack carries the guard.
	client.Transport = sealedclient.Guard(transport)
	status, body, err := sendWaitingForReady(ctx, client, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, base+wellKnownPath, nil)
	}, os.Stderr, stackReadyWait, stackReadyRetryEvery)
	if err != nil {
		return stackDescription{}, fmt.Errorf(
			"reach %s: %w\n(a self-signed certificate needs --insecure)", base, err)
	}
	if status == http.StatusServiceUnavailable {
		return stackDescription{}, fmt.Errorf(
			"%s did not start serving within %s — it is a Palbase address that is not up yet, "+
				"so try again rather than changing anything", base, stackReadyWait)
	}
	if status != http.StatusOK {
		return stackDescription{}, fmt.Errorf(
			"%s does not look like a Palbase stack: %s answered %d", base, wellKnownPath, status)
	}
	var described stackDescription
	if err := json.Unmarshal(body, &described); err != nil {
		return stackDescription{}, fmt.Errorf("%s answered something unexpected: %s", wellKnownPath, trimBody(body))
	}
	if described.Hosting == "" {
		return stackDescription{}, fmt.Errorf(
			"%s answered %s without saying what it is", base, wellKnownPath)
	}
	return described, nil
}

// wellKnownPath is where a stack describes itself.
const wellKnownPath = "/.well-known/palbase.json"

// stackDescription is that document. It mirrors the stack's own struct; the two
// are small and the contract between them is three fields, so this is written
// out rather than generated.
type stackDescription struct {
	Hosting    string `json:"hosting"`
	SDKVersion string `json:"sdk_version"`
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// insecureTransport accepts a certificate nobody issued.
//
// It exists for exactly one case: a stack still using the self-signed pair its
// first boot generated, being linked from the machine that runs it. It is opt-in
// per invocation (`--insecure`), never a default and never inferred from the
// host — a tool that silently stopped verifying certificates for "local-looking"
// addresses would be a tool that stops verifying them.
func insecureTransport() http.RoundTripper {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in, documented above
}

// takeBackRetiredIgnoreRules takes BACK the rules this CLI used to write, and
// adds none.
//
// It has nothing left to add. Everything this tool writes into a checkout is
// committed — the contract, the platform configuration, the generated clients,
// the declaration file — and anything that belongs to the MACHINE rather than
// the project (`palbase start`'s selection, `palbase plan`'s measurement) lives
// outside the repository entirely, beside the credentials in
// `~/.palbase/checkouts/<hash>/`.
//
// What it still does is un-write. A rule outlives the producer that justified
// it: after the working trees moved to the temp directory and the machine-local
// files moved out of the checkout, every run still appended lines telling the
// reader that this CLI writes files it can no longer write. Retiring a producer
// is two acts — stop writing the file, and un-write what its existence already
// put in somebody's repository.
//
// The directory-wide `.palbase` rule is DROPPED rather than narrowed. It used to
// become `.palbase/local.json`, because exactly one file in there really was
// per-machine; none is now.
func takeBackRetiredIgnoreRules(path string) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// NO FILE AT ALL IS NOT A CURATED FILE. Everything below is written to be
	// careful with somebody else's rules — narrow one, add only ours, take back
	// only what we retired. None of that applies when there is nothing there,
	// and treating the two the same produced a `.gitignore` this CLI created for
	// a JavaScript project without `node_modules/` in it.
	if strings.TrimSpace(string(content)) == "" {
		return os.WriteFile(path, []byte(gitignoreScaffold()), 0o644)
	}

	// STEP 1 — TAKE BACK WHAT WE RETIRED. The directory-wide `.palbase` rule
	// used to be NARROWED to `.palbase/local.json`, because one file inside it
	// really was per-machine. Nothing in the checkout is any more: `local.json`
	// and `plan.json` moved to `~/.palbase/checkouts/<hash>/` and the generated
	// declaration moved under the committed root. So the rule is not narrowed —
	// it is dropped, like every other rule whose producer this CLI retired.
	//
	// Dropping the line is the honest half of a retirement; `reapRetiredArtifacts`
	// is the other half, and it has already run by the time link reaches here.
	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == ".palbase" || t == ".palbase/" || isRetiredIgnoreRule(line) {
			continue
		}
		kept = append(kept, line)
	}

	// THERE IS NO STEP 2. A loop here appended "everything this CLI generates
	// into the project", and that set is now EMPTY — `generatedProjectPaths`
	// carries only the ecosystem's own rules, which belong to whoever curated
	// this file. The loop could not add a line and still ran on every link; an
	// inert step is one more thing a reader has to prove does nothing.
	updated := strings.Join(kept, "\n")
	if updated == string(content) {
		return nil
	}

	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat %s: %w", path, statErr)
	}
	return os.WriteFile(path, []byte(updated), mode)
}

// refuseUnsupportedPlatforms rejects a NAMED platform this directory has no way
// to carry. Only the explicit list is judged: detection never yields web
// without a package.json, so the only way to arrive here is to have asked.
func refuseUnsupportedPlatforms(platforms []string) error {
	for _, p := range platforms {
		if strings.ToLower(strings.TrimSpace(p)) != webPlatform {
			continue
		}
		if !exists("package.json") {
			cwd, _ := os.Getwd()
			return fmt.Errorf(
				"--platform web was asked for, and %s has no package.json.\n"+
					"  A web checkout is linked from its app root; run `palbase link` there, "+
					"or name a platform this directory does have", cwd)
		}
	}
	return nil
}

// printEnvironmentSelectionSnippet tells an Apple developer the ONE key they
// own, and the two static lines that make it select.
//
// The pattern is environment-AGNOSTIC: it names `$(PALBASE_ENV)` rather than any
// environment, so it is written once and never regenerated as environments come
// and go. That is why the CLI can print it instead of maintaining a file — the
// per-environment xcconfigs it used to write existed only because each one
// listed the OTHERS by name.
//
// The two-level glob is not a typo. Measured on a real Xcode 26.6 build: `*`
// does NOT cross a directory boundary in these settings, so
// `*/palbase/environments/*` excludes nothing at all, while the form below
// excludes correctly — verified in both directions, with the unselected
// environment's plist never entering the app bundle.
// THE SNIPPET NAMES THIS CHECKOUT'S OWN ENVIRONMENT, not a guess.
//
// It printed a literal `main`, which was right only because `defaultEnvName`
// happens to return `main` when the target carries no environment name. A
// project whose environment is `production` gets `palbase/environments/production/`
// on disk and `PALBASE_ENV = main` on screen: copied as printed, `INCLUDED`
// matches nothing while `EXCLUDED` takes everything, so the app ships with NO
// `Palbase-Info.plist` and the SDK is unconfigured at runtime. Silently.
//
// And the old closing sentence — "leave PALBASE_ENV unset and the build takes
// `local`" — was false HERE. That default belongs to `palbe-gen`, on the web
// side. Xcode expands an unset variable to nothing, so the pattern becomes
// `*/palbase/environments//*` and matches NOTHING: not `local`, not anything.
// It also contradicted the three lines above it, which already set the value.
func printEnvironmentSelectionSnippet(w io.Writer, env string) {
	if env == "" {
		env = localEnvName
	}
	fmt.Fprintf(w, `
Add these to your own build configuration (xcconfig, build settings, Tuist —
whichever you already use). PALBASE_ENV is the only line you change; the other
two never do:

    PALBASE_ENV = %s
    EXCLUDED_SOURCE_FILE_NAMES = */palbase/environments/*/*
    INCLUDED_SOURCE_FILE_NAMES = */palbase/environments/$(PALBASE_ENV)/*

Then add palbase/environments to your app target. Set PALBASE_ENV to any
directory under it; an UNSET one expands to nothing in Xcode, so the include
pattern matches nothing and the app ships unconfigured.
`, env)
}

// reportLinked says what this checkout is now bound to.
//
// WHAT IT IS BOUND TO IS NOT AN ADDRESS — except where it is. A cloud link
// binds to the PRODUCT, and `base` is merely the environment this run read the
// contract from. Printing the address as the thing linked is the sentence that
// taught people the old model: they read "linked to https://<ref>.palbase.studio"
// and concluded the checkout was pinned to that environment. It WAS, and it is
// not any more, so the sentence had to change with the mechanism.
//
// A self-host link is the opposite case and keeps the address: there the URL is
// the identity, there is no product to name, and the environment is the stack.
func reportLinked(w io.Writer, product Product, base, hosting, linkedEnv string) {
	if product.ID == "" {
		fmt.Fprintf(w, "\nlinked to %s (%s)\n", base, hosting)
		return
	}
	fmt.Fprintf(w, "\nlinked to %s (%s)\n", product.Name, product.ID)
	fmt.Fprintf(w, "  contract read from %s; each verb resolves its own environment\n", linkedEnv)
}
