package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// backendPkg is the SDK a controller imports. A runtime image carries exactly
// ONE SDK: `v2/runtime/Dockerfile` unpacks a single tarball into a single
// `node_modules/@palbase/backend`. A tenant stays on an older SDK by running an
// older IMAGE — the image tag IS the SDK version — not by a runtime that keeps
// several majors side by side and picks one per lockfile. So "which SDK will
// this deploy build against" is not a question this tree can answer; it is the
// tag of the image the tenant runs. This command validates the code, not the
// version.
const backendPkg = "@palbase/backend"

// buildTempPrefix is the os.MkdirTemp prefix for the extracted build-check
// runner. `palbase build` is one-shot and removes its own dir on return, so
// nothing sweeps this prefix.
const buildTempPrefix = "palbase-build-"

// newBuildCmd wires `palbase build` — the local pre-deploy validator. It runs
// the SAME stage + bundle + extract_meta.js the deploy runs (via build-check.js),
// so a broken push (e.g. a `@Query("field")` string where a zod schema is
// required) is caught before it produces a FAILED deploy. Non-interactive, no
// Studio auth, NO network call. Exit 0 = PASSED (or environment couldn't run it —
// warned); exit 1 = user-code validation error.
func newBuildCmd() *cobra.Command {
	var opts buildOptions
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Validate the backend locally the way a deploy would (catches broken pushes before they ship)",
		Long: `Run the same validation the deploy runs — stage, bundle, and extract
controller metadata — against your local tree, so a push that would produce a
FAILED deploy is caught here first. Exits non-zero on a user-code error
(bad decorator, return-type, version skew); exits 0 when it passes or when the
local environment can't run it (a warning is printed and the server still
gates the real deploy).

It also keeps palbase/strings/ in step with the t() calls in your code: new
sentences are added, removed ones dropped, translations kept. Start the table
once with --source <language>, open a language with --add <language>, and let
--translate fill the missing sentences with AI on your project's own OpenAI key.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return runBuildWith(cmd.Context(), cwd, cmd.OutOrStdout(), opts)
		},
	}
	// Only a project's FIRST build needs it: from then on the table carries its
	// own source language, and a different one is refused (FR-024).
	cmd.Flags().StringVar(&opts.source, "source", "",
		"the language your t() strings are written in — starts palbase/strings/ when there is no table yet (e.g. --source tr)")
	cmd.Flags().StringArrayVar(&opts.add, "add", nil,
		"open a language in palbase/strings/, every sentence missing (repeatable, e.g. --add en --add de)")
	cmd.Flags().BoolVar(&opts.translate, "translate", false,
		"translate every missing sentence on the linked stack with the project's OPENAI_API_KEY; written as needs_review")
	return cmd
}

// runBuild is `palbase build` with no flags — the form the tests drive; the
// command itself calls runBuildWith. Returns an error ONLY for a user-code
// validation failure (exit 1); environment problems (no controllers, npm
// install failed) warn and return nil (fail-open — the server gate is the
// authoritative backstop). `palbase push` does not call it: a push ships the
// committed tree, string table included, as it is.
func runBuild(ctx context.Context, cwd string, out io.Writer) error {
	return runBuildWith(ctx, cwd, out, buildOptions{})
}

// buildOptions are the flags `palbase build` takes.
type buildOptions struct {
	// source is --source: the language the project's t() keys are written in.
	source string
	// add is --add, repeatable: a language to open in palbase/strings/ (FR-009).
	add []string
	// translate is --translate: fill the missing cells with AI (FR-017).
	translate bool
}

// runBuildWith is runBuild with the command's flags.
func runBuildWith(ctx context.Context, cwd string, out io.Writer, opts buildOptions) error {
	// THE SWEEP FIRST — BEFORE EVERY GATE AND EVERY EARLY RETURN (FR-009).
	//
	// A retired artefact is what an OLDER CLI wrote into somebody's checkout,
	// and no verb collected one: measured on the user's disk, 36 directories
	// and ~30 MB survived every green run. It runs ahead of the refusals below
	// because those RETURN — a tree carrying a retired declaration would keep
	// its fossils forever, and the fossils are not what is wrong with it.
	//
	// What git TRACKS — or what sits in a repository git could not be asked
	// about — is reported and left alone (FR-010). Deleting a committed
	// directory behind a progress line is not this tool's decision to make;
	// saying it is there, by name, is — and so is a file no CLI wrote under
	// `.palbase` (D-26). Each kept entry arrives as its whole sentence, so it
	// reads the same whichever verb found it.
	for _, kept := range reapRetiredArtifacts(cwd) {
		fmt.Fprintf(out, "  kept %s\n", kept)
	}

	// THE SHAPE OF THE CHECKOUT, NEXT — AND BEFORE THE controllers/ EARLY RETURN.
	//
	// All three checks are pure filesystem: no node, no bun, no network,
	// microseconds. They run ahead of everything because a tree can carry any
	// of them while having no controllers/ at all, and because the answer is
	// more useful before sixty lines of route listing than after them.
	//
	// NOT SHORT-CIRCUITED. A checkout that has been through one upgrade tends
	// to carry more than one, and reporting them one at a time turns a single
	// fix into three builds.
	deadDecl := reportDeadDeclarations(cwd, out)
	blindSpots := reportIncludeBlindSpots(cwd, out)
	unreachable := reportUnreachableModuleResolution(cwd, out)
	if deadDecl || blindSpots || unreachable {
		return fmt.Errorf("build failed")
	}

	// BUN, said in the same words `push` uses.
	//
	// The local check bundles with Bun now, because esbuild never emits
	// `emitDecoratorMetadata` and without that metadata the dependency graph
	// cannot be validated here at all — the build would report success on a
	// graph the deploy refuses. Bun was already required for `push` and `plan`;
	// saying so here keeps the two answers the same answer.
	if _, err := exec.LookPath("bun"); err != nil {
		return fmt.Errorf("bun is not installed, and it is what builds a backend for this runtime " +
			"(https://bun.sh). The stack runs Bun, so the bundle is built by the engine that will run it")
	}

	// A BACKEND IS ITS MODULES, NOT A DIRECTORY NAMED `controllers`.
	//
	// This used to stat `controllers/` and, when it was absent, print "nothing to
	// validate" and return SUCCESS. That is a gate reporting silence: a project
	// that keeps each module in its own folder — the layout the module system
	// exists to allow, and the one Nest developers arrive with — validated
	// NOTHING and said OK. Measured 2026-09-02 on a project moved to
	// `modules/<name>/`: `build OK — 0 route(s)`, with 85 routes in the tree.
	//
	// The real precondition is the one the bundler already uses: at least one
	// `*.module.ts` anywhere. Absence of that is still worth saying out loud,
	// because a tree with no module genuinely has nothing to answer with.
	// NOTHING TO VALIDATE MEANS NOT A BACKEND AT ALL — no schema AND no module.
	//
	// The first attempt returned early whenever there was no module, and that
	// closed a refusal: a `@Controller` no module can name has to REACH the
	// container check to be refused, and skipping past it turned the refusal
	// into a pass. `TestAControllerNoModuleCanNameIsRefused` caught it — the
	// same shape of bug this change set exists to remove, one layer over.
	//
	// Asking for a schema OR a module was still too coarse: a tree with a
	// `@Controller` and neither of those is a backend somebody is in the middle
	// of writing, and it must be REFUSED, not waved through. So the exit asks the
	// smallest honest question — is there any source here at all — and it asks it
	// without reading a directory name.
	if !hasProjectSource(cwd) {
		fmt.Fprintln(out, "no TypeScript sources — nothing to validate")
		if flags := tableFlags(opts); flags != "" {
			fmt.Fprintf(out, "✗ %s did not run — there are no TypeScript sources, so there are no sentences\n", flags)
			return fmt.Errorf("build failed")
		}
		return nil
	}

	// Controllers import @palbase/backend; the bundler keeps it external and the
	// extractor require()s it from node_modules. Install once when absent so a
	// fresh clone can be validated in one step.
	//
	// AND A FAILED INSTALL REFUSES. It used to print a warning and RETURN NIL —
	// exit 0, the word "build" on the screen, and not one controller read. The
	// warning even said so ("cannot validate locally"), which is the whole
	// problem: the sentence a person remembers from a command that exits 0 is
	// that it passed. `palbase push` installs this as a pre-push hook, so the
	// green it printed was the green a deploy went out on.
	//
	// "The deploy will still gate it" was the justification, and it is the same
	// advisory shape as the staging fallback below: a local gate whose answer is
	// known not to measure anything is worse than no local gate, because it
	// spends the person's trust. A build that cannot run says it cannot run.
	if backendDepMissing(cwd) {
		if err := installNodeDeps(cwd); err != nil {
			return fmt.Errorf("%s is not installed and `npm install` failed (%w) — nothing here can be "+
				"validated without the SDK the controllers import", backendPkg, err)
		}
	}
	// Read the installed major BEFORE ensureBuildCheckTools: its `npm install
	// --no-save` can prune a hand-placed @palbase/backend that isn't in
	// package.json (test fixtures), and in general the version we want is the
	// one already on disk, not whatever a tool install leaves behind.
	installed := installedBackendVersion(cwd)
	// The same refusal push and plan make before bundling (palbase-cli#7 §4):
	// a build that validates an SDK the lockfile does not pin validates a tree
	// the deploy will not see.
	if why := lockfileDriftRefusal(cwd); why != "" {
		return errors.New(why)
	}

	// zod-to-json-schema powers the header-rule + schema lowering in the
	// extractor; without it the header checks degrade (best-effort install).
	ensureBuildCheckTools(cwd)

	// VE ŞİMDİ AYNI SORUYU İKİNCİ KEZ SOR (bkz. `sdkPruneRefusal`).
	//
	// Yukarıdaki yorum bu tehlikeyi biliyordu ama yalnız sürümü önce OKUYORDU;
	// okunan doğru sayı, yanlış bir SDK'ya derlenmiş bundle'ın üstünde yazılı
	// kalıyordu. Sıra `stack_sdk.go`'da düzeltildi — bu kapı, düzeltmenin bir
	// gün sessizce geri alınmasını yakalar. Refuse, warn DEĞİL: yanlış SDK'ya
	// derlenen bir bundle sessizce yanlış bir artefakt üretir.
	if why := sdkPruneRefusal(installed, installedBackendVersion(cwd)); why != "" {
		return errors.New(why)
	}

	// The installed SDK version, reported but NOT gated here.
	//
	// This used to fail the build when the installed major differed from npm's
	// `latest`, on the premise that "deploys run the latest major and will reject
	// this tree". That premise stopped being true on 2026-08-04 — but not for the
	// reason recorded here until 2026-09-06, which described a runtime holding
	// several SDK majors at once and choosing between them per tenant lockfile.
	// That behaviour never existed. Measured instead:
	// `v2/runtime/Dockerfile` unpacks ONE tarball into ONE
	// `node_modules/@palbase/backend`, so an image has room for exactly one SDK.
	// What is true sits one level up — the image TAG is the SDK version, so a ^12
	// project keeps deploying because it keeps running the 12.x image: verified
	// live, serving traffic, with the artifact manifest recording sdkVersion
	// 12.0.1.
	//
	// Keeping the check would have been worse than removing it: it turned a
	// PASSING deploy into a FAILING local build, and because `palbase push`
	// installs this as a pre-push hook, it blocked the push before the platform
	// ever saw it. A gate that answers a question the server no longer asks is not
	// a safety net; it is a wrong answer delivered with confidence.
	//
	// Which SDK a deploy lands on is decided by the image the tenant runs, not by
	// anything this process can compute — and the server-side checker this
	// comment used to name as the authority does not exist in this repo either.
	// Printing the installed version is the honest subset available here.
	if installed != "" {
		fmt.Fprintf(out, "✓ %s %s\n", backendPkg, installed)
	}

	// Run the deploy-identical validation via build-check.js.
	tmpDir, err := os.MkdirTemp("", buildTempPrefix+"*")
	if err != nil {
		return err
	}
	defer removeTemp(tmpDir)
	if err := extractFS(buildCheckFS, "devjs", tmpDir); err != nil {
		return fmt.Errorf("extract the build checker: %w", err)
	}

	// Validate the tree the DEPLOY receives, not the one on disk. BuildTarball is
	// the same walk `palbase push` ships, and it strips node_modules — so bundling
	// the working directory resolved bare third-party imports (`import { z } from
	// "zod"`) that the deploy then failed on with `Could not resolve "zod"`, after
	// this command had already printed "build OK". Staging closes that gap at the
	// source: same bytes in, same bundler behaviour out.
	//
	// A STAGING FAILURE REFUSES. It used to warn and validate the working
	// directory instead, on the premise that an environment fault is not the
	// user's fault — but the fallback's own warning admitted what it was doing:
	// "a bare third-party import may still pass here and fail the deploy". That
	// is the exact false green this staging exists to kill, handed back under
	// the word `warning`, and `palbase push` installs this command as a pre-push
	// hook, so the answer it prints is the one a deploy is launched on.
	//
	// It also wrote into the checkout. build-check.js stages its
	// return-binding-injected controllers at `PROJECT_ROOT/.palbase-build-controllers`,
	// and PROJECT_ROOT is whatever is passed here — so the fallback was the one
	// path that put that directory in somebody's repository, cleaned by an exit
	// handler a SIGKILL never runs.
	//
	// Both halves have the same cure: when the tree cannot be staged, say so and
	// stop. A refusal a person can act on beats an answer nobody can trust.
	buildRoot, serr := stageDeployTreeFn(cwd)
	if serr != nil {
		return fmt.Errorf("the deploy tree could not be staged, and validating the working "+
			"directory instead would answer a different question than the deploy asks "+
			"(a bare third-party import resolves here through node_modules and fails there): %w", serr)
	}
	defer removeTemp(buildRoot)

	node := exec.CommandContext(ctx, "node", filepath.Join(tmpDir, "build-check.js"))
	node.Dir = buildRoot
	node.Env = append(os.Environ(),
		fmt.Sprintf("PALBASE_DEV_ROOT=%s", buildRoot),
		// The CLI's pinned TypeScript parser first, then the project's deps —
		// the user's typescript may be 7.x (no compiler API) or absent.
		fmt.Sprintf("NODE_PATH=%s", devNodePath(cwd, out)),
		// The staged tree has no node_modules (that is the point). The metadata
		// extractor still has to require() the real @palbase/backend, exactly as
		// the pod's global install provides it — point it at the project's copy.
		fmt.Sprintf("PALBASE_RUNTIME_MODULES=%s", filepath.Join(cwd, "node_modules")),
	)
	// THE NAMES THE STACK HOLDS, read before the file is rendered.
	//
	// They are the other half of the ONE generated file: `Secrets.get(…)`,
	// `Flags.isEnabled(…)`, `@Upload({ bucket })` and `{ auth: { role } }` are
	// compile-checked against them. `build` works offline (NFR-004), so an
	// unreachable stack — or a checkout never linked to one — is answered by
	// `namesUnavailable` rather than by failing the build; landEnvTypes then
	// keeps whatever stack block the checkout's file already carries.
	names := namesForBuild(ctx, cwd, out)

	// db/ is deploy-fatal too and check mode never looked at it: a schema module
	// that doesn't export a defineSchema() result passed `palbase build` and then
	// failed the deploy. generateEnvTypes runs the SAME bundle + bridge the
	// deploy's extractor does, so reuse it rather than restating the rule. It
	// writes into the staging tree, which is discarded — build validates, it
	// does not mutate.
	//
	// The refusal names the DIRECTORY, not a file: the failure can be the old
	// layout, a missing db/public.ts, or one sibling of several that does not
	// evaluate — and the error carries which.
	if err := generateEnvTypes(ctx, buildRoot, filepath.Join(cwd, "node_modules"), namesOrNil(names)); err != nil {
		fmt.Fprintf(out, "✗ DEPLOY WOULD FAIL: %s/ — %v\n", SchemaDir, err)
		return fmt.Errorf("build failed")
	}
	// …and then it LANDS in the checkout — the ONE generated file, both its
	// blocks. Generating into the staging tree and discarding it validated the
	// schema and left the person with nothing: their editor still typed
	// `Database.tables.*` and `Secrets.get(…)` from whatever was last written,
	// which is the file's whole reason to exist.
	//
	// It is written as soon as the schema is valid, before the controller checks
	// below can fail the build. The types describe db/*.ts and the stack's names
	// and nothing else — a controller with a bad decorator does not make them
	// wrong, and the moment somebody most needs their editor working is while
	// they are fixing one.
	if err := landEnvTypes(buildRoot, cwd, names, out); err != nil {
		return err
	}
	// THE STRING TABLE, right after the types and for the same reason (D-19):
	// it describes the source's sentences, not whether the deploy will pass,
	// and the controller gates below may still fail this build. It reads the
	// STAGED tree — the source the deploy receives — and writes the checkout.
	if err := landStringsTable(ctx, tmpDir, buildRoot, cwd, opts, devNodePath(cwd, out), out); err != nil {
		return err
	}
	// The replacement exists now, so what it supersedes goes now — the sweep at
	// the top of this verb had to leave the root declaration files in place
	// while nothing replaced them (palbase-cli#7 §5), and two copies declaring
	// the same global types do not compile. Everything else it names is already
	// gone; the kept sentences were printed then.
	_ = reapRetiredArtifacts(cwd)

	// AND THE AUGMENTATION HAS TO LAND — measured, not declared (FR-006).
	//
	// A `declare module` written against a specifier TypeScript cannot resolve
	// is a silent no-op in any `.d.ts` (no error, ever), and a file that does
	// not end with `export {};` SHADOWS the package's types instead of merging
	// with them. Both produce a green build and an editor that types nothing.
	//
	// The probe spells one of the names the render used, so a stack block that
	// landed but types nothing is caught too. The refusal already names the file
	// and what TypeScript said about it. TypeScript is loaded the way
	// build-check.js loads it — the CLI's pinned parser first — so a project
	// whose own `typescript` is absent or has no compiler API is still measured
	// rather than refused (D-23).
	if err := verifyAugmentationLands(ctx, cwd, devNodePath(cwd, out), names.Names); err != nil {
		fmt.Fprintf(out, "✗ %v\n", err)
		return fmt.Errorf("build failed")
	}

	node.Stdout = out
	node.Stderr = out
	if err := node.Run(); err != nil {
		// A non-zero exit = a user-code validation failure (build-check.js
		// already printed the per-controller reasons). Anything
		// else (couldn't spawn node) is environment — warn + fail-open.
		if _, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("build failed")
		}
		fmt.Fprintf(out, "warning: could not run local validation (%v) — the deploy will still gate it\n", err)
		return nil
	}
	return nil
}

// namesForBuild answers which names the stack block is rendered from: read the
// stack's four sets when this checkout is linked to one (remembering a
// successful read on this machine, FR-004, and falling back to what it last
// heard when the stack cannot be asked, FR-003); when this checkout is not
// linked to any stack at all, there is nothing to ask and nothing to fall back
// to, and that is said by name rather than silently swallowed.
func namesForBuild(ctx context.Context, cwd string, out io.Writer) checkoutStackNames {
	resolved, err := Resolve(ctx)
	if err != nil {
		return checkoutStackNames{Source: namesUnavailable, Why: errors.New("this checkout is not linked to a stack")}
	}
	names := stackNamesForCheckout(ctx, cwd, resolved.Acting())
	if names.CacheErr != nil {
		fmt.Fprintf(out, "  the stack's names were not remembered on this machine — %v\n", names.CacheErr)
	}
	return names
}

// namesOrNil is what the RENDERER is handed: nil when nothing was read at all,
// so the bridge emits an empty stack block and landEnvTypes is the one that
// decides whether the checkout's existing block survives (FR-003a). Names read
// from the stack OR from this machine's cache are real answers and render.
func namesOrNil(n checkoutStackNames) *StackNames {
	if n.Source == namesUnavailable {
		return nil
	}
	return &n.Names
}

// ── THE BOUNDARIES OF THE GENERATED FILE ────────────────────────────────────
//
// The renderer wraps each of the two blocks it writes — the schema's
// `@palbase/backend/env` block and the stack's `@palbase/backend/stack` block —
// in a fixed marker, and this is the CLI's copy of those four strings.
//
// They live in two repositories with no compiler between them, so
// `TestPreserveStackBlockMarkersAreTheRenderers` reads the SDK's own source and
// fails the moment the two spellings drift. Splicing on a boundary the
// generated file does not have is how a file gets half a declaration.
const (
	palbaseEnvBlockBegin   = "// palbase:env:begin"
	palbaseEnvBlockEnd     = "// palbase:env:end"
	palbaseStackBlockBegin = "// palbase:stack:begin"
	palbaseStackBlockEnd   = "// palbase:stack:end"
)

// markedSpan locates the ONE block text carries between begin and end, markers
// included: [start, stop).
//
// EXACTLY ONE OF EACH, or this is not a block anything may act on. A file with
// two begin markers has been hand-edited or concatenated, and guessing which
// pair is the real one is how a splice eats half a declaration.
func markedSpan(text, begin, end string) (int, int, error) {
	if n := strings.Count(text, begin); n != 1 {
		return 0, 0, fmt.Errorf("%d %q markers, exactly one expected", n, begin)
	}
	if n := strings.Count(text, end); n != 1 {
		return 0, 0, fmt.Errorf("%d %q markers, exactly one expected", n, end)
	}
	start := strings.Index(text, begin)
	stop := strings.Index(text, end)
	if stop < start+len(begin) {
		return 0, 0, fmt.Errorf("%q comes before %q", end, begin)
	}
	return start, stop + len(end), nil
}

// preserveStackBlock rewrites the env block of the file already in the checkout
// and leaves every other byte — the stack block above all — exactly where it
// was (C-8, FR-003a).
//
// `existing` is the file on disk; `envBlock` is the fresh render's env block,
// markers included. THE `.d.ts` IS NOT PARSED: both ends are written by this
// product, so the boundaries are known, and a TypeScript parser here would be a
// second interpreter of a file the generator already knows the shape of.
func preserveStackBlock(existing, envBlock string) (string, error) {
	blockStart, blockStop, err := markedSpan(envBlock, palbaseEnvBlockBegin, palbaseEnvBlockEnd)
	if err != nil {
		return "", fmt.Errorf("the new env block: %w", err)
	}
	if blockStart != 0 || blockStop != len(envBlock) {
		return "", errors.New("the new env block carries text outside its own markers")
	}
	if strings.Contains(envBlock, palbaseStackBlockBegin) || strings.Contains(envBlock, palbaseStackBlockEnd) {
		return "", errors.New("the new env block carries a stack marker — splicing it would leave two stack blocks in one file")
	}
	envStart, envStop, err := markedSpan(existing, palbaseEnvBlockBegin, palbaseEnvBlockEnd)
	if err != nil {
		return "", fmt.Errorf("%s: env block: %w", EnvTypesPath(), err)
	}
	stackStart, stackStop, err := markedSpan(existing, palbaseStackBlockBegin, palbaseStackBlockEnd)
	if err != nil {
		return "", fmt.Errorf("%s: stack block: %w", EnvTypesPath(), err)
	}
	if envStart < stackStop && stackStart < envStop {
		return "", fmt.Errorf("%s: the env and stack blocks overlap", EnvTypesPath())
	}
	return existing[:envStart] + envBlock + existing[envStop:], nil
}

// keepStackBlock lands a fresh render's env block in the file already on disk,
// carrying that file's stack block over BYTE FOR BYTE.
//
// Three shapes reach it, and only one of them splices:
//   - both sides marked → the splice (preserveStackBlock);
//   - neither marked → an older renderer writing what it always wrote, and
//     there is no stack block to keep;
//   - the render unmarked while the file on disk is not → a DOWNGRADED
//     @palbase/backend, whose output carries no stack block at all. Writing it
//     would drop the one this file has, so it is refused by name.
func keepStackBlock(existing, fresh string) (string, error) {
	freshStart, freshStop, freshErr := markedSpan(fresh, palbaseEnvBlockBegin, palbaseEnvBlockEnd)
	existingMarked := strings.Contains(existing, palbaseEnvBlockBegin) ||
		strings.Contains(existing, palbaseStackBlockBegin)

	if freshErr != nil {
		if existingMarked {
			return "", fmt.Errorf("the installed %s renders no single marked env block (%w), and writing its "+
				"output would drop the stack block %s carries", backendPkg, freshErr, EnvTypesPath())
		}
		return fresh, nil
	}
	if !existingMarked {
		// A file written before the single-file renderer: it carries the schema
		// and nothing else, so there is no stack block in it to keep.
		return fresh, nil
	}
	return preserveStackBlock(existing, fresh[freshStart:freshStop])
}

// whyNoStackNames turns the reason no names were read into the half-sentence
// the empty-block line prints. ONE LINE: errors.Join separates with newlines,
// and a person reading a build log gets one line per fact.
func whyNoStackNames(why error) string {
	if why == nil {
		return "this run read no stack names"
	}
	return strings.ReplaceAll(why.Error(), "\n", "; ")
}

// landEnvTypes copies the generated palbase-env.d.ts out of the staging tree and
// into the checkout, where the project's tsconfig can see it.
//
// A no-op in the two cases that are not mistakes: the project declares no
// database (nothing was generated), or staging fell back to the live tree
// (generation already wrote there).
//
// AND IT NEVER NARROWS THE STACK BLOCK. The renderer writes both blocks, so a
// run that could read no names at all would otherwise replace a file typing
// this project's secrets, flags, buckets and roles with one that types none of
// them — every `Secrets.get()` in the codebase turning into a compile error
// that reads "your code is wrong" rather than "the stack could not be asked"
// (FR-003a, NFR-003). With no names, only the bytes between the env markers
// move. With names — from the stack or from this machine's cache — the render
// is the truth, and an empty set it reports is a real answer.
func landEnvTypes(buildRoot, cwd string, names checkoutStackNames, out io.Writer) error {
	if buildRoot == cwd {
		return nil
	}
	// BOTH ENDS COME FROM THE DECLARATION. The file lives under the CLI's own
	// directory now — committed, like everything else there — so a path spelled
	// here would land it at the checkout root, where nothing reads it and a
	// retired ignore rule no longer covers it.
	rel := filepath.FromSlash(EnvTypesPath())
	body, err := os.ReadFile(filepath.Join(buildRoot, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dest := filepath.Join(cwd, rel)
	prev, rerr := os.ReadFile(dest)
	if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", rel, rerr)
	}

	// THE CHECKOUT'S LINE ENDINGS, NOT THE RENDERER'S (review-T010). The file is
	// committed, and git with `core.autocrlf=true` checks it out with CRLF while
	// the renderer writes LF. Spliced as-is, an LF env block landed inside a CRLF
	// file — mixed endings, on disk — and the bytes never compared equal again,
	// so every build rewrote a file whose content had not changed (FR-001a).
	if rerr == nil && bytes.Contains(prev, []byte("\r\n")) {
		body = bytes.ReplaceAll(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	}

	// A RENDER WITH NO STACK BLOCK NEVER REPLACES A FILE THAT HAS ONE, whatever
	// the names' source (review-T010). An @palbase/backend older than the single
	// file ignores the names it is handed; writing its output would drop every
	// secret, flag, bucket and role this project types against, under a "✓".
	if names.Source != namesUnavailable && rerr == nil &&
		!bytes.Contains(body, []byte(palbaseStackBlockBegin)) && bytes.Contains(prev, []byte(palbaseStackBlockBegin)) {
		fmt.Fprintf(out, "  %s not refreshed — the installed %s rendered no stack block, and writing its "+
			"output would drop the one this file carries; upgrade %s to refresh it\n", rel, backendPkg, backendPkg)
		return nil
	}

	if names.Source == namesUnavailable {
		if rerr != nil {
			// NOTHING ON DISK TO KEEP. An empty stack block is the right answer
			// in a checkout that never had one — and the line says why it is
			// empty, because a file that quietly types nothing is worse than a
			// file that says it could not be told what to type.
			fmt.Fprintf(out, "  %s: stack block EMPTY — %s\n", rel, whyNoStackNames(names.Why))
		} else {
			kept, keepErr := keepStackBlock(string(prev), string(body))
			if keepErr != nil {
				// NOT REFRESHED RATHER THAN NARROWED, and never fatal: a build
				// that works offline (NFR-004) must not pay for it with types
				// nobody asked to lose.
				fmt.Fprintf(out, "  %s not refreshed — %v\n", rel, keepErr)
				return nil
			}
			body = []byte(kept)
		}
	}

	if rerr == nil && bytes.Equal(prev, body) {
		fmt.Fprintf(out, "✓ %s (unchanged)\n", rel)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	fmt.Fprintf(out, "✓ %s\n", rel)
	return nil
}

// stageDeployTreeFn is the seam the refusal above is measured through.
//
// Staging fails on a full disk, an unwritable temp directory or a broken
// symlink — none of which a test can arrange on a machine where they are all
// fine. Without a seam the refusal branch would be code nobody has ever run,
// and "it obviously works" is the sentence that precedes every branch that
// did not.
var stageDeployTreeFn = stageDeployTree

// stageDeployTree materialises, in a temp dir, EXACTLY the source tree a deploy
// receives: the `palbase push` tarball, unpacked. It reuses BuildTarball rather
// than re-implementing its ignore rules, so the two can never drift — if push
// starts shipping (or stripping) a path, the local build follows automatically.
//
// The load-bearing effect is what is ABSENT: node_modules. esbuild resolves bare
// imports by walking up from the entry file, so bundling the working directory
// silently satisfied `import { z } from "zod"` from the project's installed deps,
// while the deploy — which bundles from this tarball and never runs npm install —
// could not. Caller removes the returned dir.
func stageDeployTree(cwd string) (string, error) {
	tarball, err := BuildTarball(cwd)
	if err != nil {
		return "", fmt.Errorf("pack the project: %w", err)
	}
	dir, err := os.MkdirTemp("", "palbase-build-stage-*")
	if err != nil {
		return "", err
	}
	if err := extractTarGz(dir, bytes.NewReader(tarball)); err != nil {
		removeTemp(dir)
		return "", fmt.Errorf("unpack the project: %w", err)
	}

	// …and the project's DEPENDENCIES, reachable from it.
	//
	// The tarball carries no node_modules, which is right — that is what a push
	// ships. But the bundle is then produced from a tree that HAS them: `palbase
	// push` runs the bundler in the project directory, and so does the stack when
	// it builds an artifact. A staged tree without them made this command
	// stricter than the thing it models, and the two disagreed out loud —
	// measured on the repository's own fixture, where `palbase build` failed with
	// `Could not resolve "zod"` on a project `palbase push` bundles happily.
	//
	// A symlink rather than a copy: node_modules is the largest thing in a
	// project and nothing here writes to it.
	modules := filepath.Join(cwd, "node_modules")
	if _, err := os.Stat(modules); err == nil {
		if err := os.Symlink(modules, filepath.Join(dir, "node_modules")); err != nil {
			removeTemp(dir)
			return "", fmt.Errorf("make the project's dependencies reachable: %w", err)
		}
	}
	return dir, nil
}

// installedBackendVersion reads node_modules/@palbase/backend/package.json's
// version. "" when absent/unreadable (the caller then skips the skew check).
func installedBackendVersion(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "node_modules", "@palbase", "backend", "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// majorOf returns the leading integer of a semver string ("9.0.1" → 9). 0 when
// unparseable (the caller treats 0 as "can't compare" and skips the gate).
func majorOf(version string) int {
	v := strings.TrimLeft(strings.TrimSpace(version), "^~>=<v ")
	dot := strings.IndexByte(v, '.')
	if dot > 0 {
		v = v[:dot]
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// stackNamesWith asks the linked stack for three of the name sets the generated
// types are rendered from — secrets, flags, buckets — with the credential
// already resolved, so readStackNames (which adds the roles) resolves it once,
// not once per read.
//
// One round trip per set, and a failure in ANY of them fails the whole read:
// a partial answer would generate a file that narrows `Secrets.get()` correctly
// and `Flags.isEnabled()` to nothing, which is worse than not refreshing —
// the compile error would land on code that is right.
func stackNamesWith(ctx context.Context, target Target, cred Credentials) (StackNames, error) {
	secrets, err := secretNames(ctx, target, cred)
	if err != nil {
		return StackNames{}, err
	}
	flags, err := flagKeys(ctx, target, cred)
	if err != nil {
		return StackNames{}, err
	}
	buckets, err := stackBucketsWith(ctx, target, &cred)
	if err != nil {
		return StackNames{}, err
	}
	return StackNames{Secrets: secrets, Flags: flags, Buckets: buckets}, nil
}

// flagKeys asks the stack which feature flags it holds.
func flagKeys(ctx context.Context, target Target, cred Credentials) ([]string, error) {
	status, body, err := managementCall(ctx, target, cred, http.MethodGet, "/v1/management/flags", nil, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d when asked for its flags", target.Describe(), status)
	}
	var answer struct {
		Flags []struct {
			Key string `json:"key"`
		} `json:"flags"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(answer.Flags))
	for _, f := range answer.Flags {
		keys = append(keys, f.Key)
	}
	return keys, nil
}

// hasProjectSource reports whether the tree holds any TypeScript the build could
// be about. It stops at the first hit and skips the directories that are never
// somebody's source.
func hasProjectSource(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (name == "node_modules" || name == "dist" || name == ".git" ||
				strings.HasPrefix(name, ".palbase")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".d.ts") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// stringsScan is what devjs/strings_scan.js prints.
type stringsScan struct {
	Keys    []string             `json:"keys"`
	Dynamic []stringsScanDynamic `json:"dynamic"`
}

type stringsScanDynamic struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// runStringsScanFn is the seam the table step is measured through.
var runStringsScanFn = runStringsScan

// runStringsScan runs the embedded scanner over root with the CLI's pinned
// TypeScript first on NODE_PATH (D-12, NFR-004). Any non-zero exit, and any
// output that is not the scanner's one line, is an error — never an empty key
// set, because an empty key set is a table with every key deleted (FR-030).
func runStringsScan(ctx context.Context, script, root, nodePath string) (stringsScan, error) {
	cmd := exec.CommandContext(ctx, "node", script, root)
	cmd.Env = append(os.Environ(), "NODE_PATH="+nodePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		why := strings.TrimSpace(stderr.String())
		if why == "" {
			why = err.Error()
		}
		return stringsScan{}, errors.New(why)
	}
	var s stringsScan
	if err := json.Unmarshal(bytes.TrimSpace(raw), &s); err != nil {
		return stringsScan{}, fmt.Errorf("unreadable scanner output: %w", err)
	}
	if s.Keys == nil {
		return stringsScan{}, errors.New("the scanner's output carries no key list")
	}
	return s, nil
}

// tableFlags names what was asked of the table beyond keeping it in step —
// the flags whose not happening must fail the build (FR-022).
func tableFlags(opts buildOptions) string {
	switch {
	case len(opts.add) > 0 && opts.translate:
		return "--add/--translate"
	case len(opts.add) > 0:
		return "--add"
	case opts.translate:
		return "--translate"
	}
	return ""
}

// landStringsTable is the build's table step: scan, read, merge, write — in
// the format the project's own stack reads (D-21).
//
// A scanner that cannot run leaves the table exactly as it is and says so in
// one line — a key set nobody measured must never reach the merge, where a
// missing key is a deleted translation. That alone does not fail the build,
// unless --add or --translate asked for something that now cannot happen.
func landStringsTable(ctx context.Context, tmpDir, buildRoot, cwd string, opts buildOptions, nodePath string, out io.Writer) error {
	flags := tableFlags(opts)
	refuse := func(why string) error {
		fmt.Fprintf(out, "✗ %s did not run — %s\n", flags, why)
		return fmt.Errorf("build failed")
	}
	// WHICH TABLE THIS BUILD KEEPS — decided before the scan, because every line
	// it prints names the table's home (D-21, FR-055): palbase/strings/ when the
	// installed SDK declares its stack reads it, or when the directory is already
	// there and the SDK cannot be measured; the old single file otherwise.
	declared, installed := sdkStringsTable(cwd)
	_, metaErr := os.Lstat(filepath.Join(cwd, filepath.FromSlash(StringsDir()), metaFileName))
	dirMode := declared >= tableDirVersion || (installed == "" && metaErr == nil)
	home := StringsPath()
	if dirMode {
		home = StringsDir() + "/"
	}
	scan, err := runStringsScanFn(ctx, filepath.Join(tmpDir, "strings_scan.js"), buildRoot, nodePath)
	if err != nil {
		fmt.Fprintf(out, "warning: %s was not updated in this build — the t() scanner could not run (%s)\n",
			home, strings.ReplaceAll(err.Error(), "\n", " "))
		if flags != "" {
			return refuse("the t() scanner could not run, so the table's sentences are not known")
		}
		return nil
	}
	for _, d := range scan.Dynamic {
		fmt.Fprintf(out, "warning: %s:%d — this use of t() cannot be collected into %s (only a direct call "+
			"whose first argument is a string literal can); it is served in the source language\n",
			d.File, d.Line, home)
	}

	existing, layout, droppedLocales, err := readTable(cwd)
	if errors.Is(err, errNoTableMeta) && opts.source != "" && dirMode {
		existing, err = adoptTableDir(cwd, opts.source)
		layout = layoutDir
	}
	if err != nil {
		fmt.Fprintf(out, "✗ %v\n", err)
		return fmt.Errorf("build failed")
	}
	if layout == layoutDir && !dirMode && installed != "" {
		fmt.Fprintf(out, "✗ %s/ needs @palbase/backend 41.3.0 or later, and this project's is %s — its stack reads only %s; "+
			"upgrade the SDK (npm install @palbase/backend@latest)\n", StringsDir(), installed, StringsPath())
		return fmt.Errorf("build failed")
	}
	if !dirMode {
		if flags != "" {
			v := installed
			if v == "" {
				v = "not installed"
			}
			return refuse(fmt.Sprintf("this project's @palbase/backend is %s; the %s/ table and translation need 41.3.0 or later "+
				"(npm install @palbase/backend@latest)", v, StringsDir()))
		}
		return landLegacyTable(existing, droppedLocales, scan.Keys, opts.source, cwd, out)
	}

	adds := make([]string, 0, len(opts.add))
	for _, raw := range opts.add {
		c, ok := canonicalLocale(raw)
		if !ok {
			fmt.Fprintf(out, "✗ --add %q is not a language tag\n", raw)
			return fmt.Errorf("build failed")
		}
		adds = append(adds, c)
	}
	merged, droppedKeys, err := mergeStringsTable(existing, scan.Keys, opts.source)
	if err != nil {
		fmt.Fprintf(out, "✗ %v\n", err)
		return fmt.Errorf("build failed")
	}
	if merged == nil {
		if flags != "" {
			return refuse("there is no string table — start it with: palbase build --source <language>")
		}
		if len(scan.Keys) > 0 {
			fmt.Fprintf(out, "! %d t() string(s) found and there is no string table — start it with: palbase build --source <language>\n",
				len(scan.Keys))
		}
		return nil
	}
	var added, already []string
	for _, tag := range adds {
		if tag == merged.Source {
			fmt.Fprintf(out, "✗ --add %s: %s is the source language — its text is the key itself and has no file\n", tag, tag)
			return fmt.Errorf("build failed")
		}
		if slices.Contains(merged.Locales, tag) || slices.Contains(added, tag) {
			already = append(already, tag)
			continue
		}
		added = append(added, tag)
	}
	if len(added) > 0 {
		merged.Locales = orderedLocales(merged.Source, append(merged.Locales, added...))
		for key, row := range merged.Strings {
			for _, tag := range added {
				if strings.TrimSpace(key) == "" {
					row[tag] = stringCell{Value: key, State: cellTranslated}
				} else {
					row[tag] = stringCell{State: cellMissing}
				}
			}
		}
	}

	changed := false
	if layout == layoutLegacy {
		if err := migrateLegacyTable(cwd, merged); err != nil {
			fmt.Fprintf(out, "✗ %v\n", err)
			return fmt.Errorf("build failed")
		}
		fmt.Fprintf(out, "✓ moved %s into %s/ — commit the new directory and the deletion\n", StringsPath(), StringsDir())
		changed = true
	} else if changed, err = writeTableDir(cwd, merged); err != nil {
		fmt.Fprintf(out, "✗ %v\n", err)
		return fmt.Errorf("build failed")
	}
	for _, loc := range droppedLocales {
		fmt.Fprintf(out, "  dropped every %s cell — %s is no longer in locales\n", loc, loc)
	}
	for _, k := range droppedKeys {
		fmt.Fprintf(out, "  dropped %q — no t() call uses it any more\n", k)
	}
	for _, tag := range added {
		missing := 0
		for _, row := range merged.Strings {
			if row[tag].State == cellMissing {
				missing++
			}
		}
		fmt.Fprintf(out, "✓ added %s — %s/%s.json, %d string(s) to translate (fill them in, or run: palbase build --translate)\n",
			tag, StringsDir(), tag, missing)
	}
	for _, tag := range already {
		fmt.Fprintf(out, "  %s is already in %s/\n", tag, StringsDir())
	}
	warnPlaceholders(merged, out)
	if changed {
		fmt.Fprintf(out, "✓ wrote %s/ (%d string(s), %d language(s))\n", StringsDir(), len(merged.Strings), len(merged.Locales))
	} else {
		fmt.Fprintf(out, "✓ %s/ unchanged (%d string(s))\n", StringsDir(), len(merged.Strings))
	}
	return nil
}

// warnPlaceholders is FR-016: a person's translation whose {{…}} differ from
// its sentence is named, and the build goes on — the words are theirs.
func warnPlaceholders(t *stringsTable, out io.Writer) {
	keys := make([]string, 0, len(t.Strings))
	for k := range t.Strings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, loc := range t.Locales {
		if loc == t.Source {
			continue
		}
		for _, key := range keys {
			c := t.Strings[key][loc]
			if c.State == cellMissing || c.State == "" {
				continue
			}
			dropped, added := placeholderDiff(key, c.Value)
			if len(dropped) > 0 {
				fmt.Fprintf(out, "warning: %s/%s.json: %q — the translation drops %s\n", StringsDir(), loc, key, braces(dropped))
			}
			if len(added) > 0 {
				fmt.Fprintf(out, "warning: %s/%s.json: %q — the translation adds %s\n", StringsDir(), loc, key, braces(added))
			}
		}
	}
}

// braces renders placeholder names the way a reader wrote them.
func braces(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "{{" + n + "}}"
	}
	return strings.Join(out, ", ")
}

// landLegacyTable is the previous run's table step, VERBATIM in behaviour, for
// a project whose installed SDK's stack reads only palbase/strings.json
// (FR-055, D-21).
func landLegacyTable(existing *stringsTable, droppedLocales, keys []string, source, cwd string, out io.Writer) error {
	path := filepath.Join(cwd, filepath.FromSlash(StringsPath()))
	merged, droppedKeys, err := mergeStringsTable(existing, keys, source)
	if err != nil {
		fmt.Fprintf(out, "✗ %v\n", err)
		return fmt.Errorf("build failed")
	}
	if merged == nil {
		if len(keys) > 0 {
			fmt.Fprintf(out, "! %d t() string(s) found and %s does not exist — start it with: palbase build --source <language>\n",
				len(keys), StringsPath())
		}
		return nil
	}
	before, _ := os.ReadFile(path)
	if err := writeStringsTable(path, merged); err != nil {
		fmt.Fprintf(out, "✗ %v\n", err)
		return fmt.Errorf("build failed")
	}
	for _, loc := range droppedLocales {
		fmt.Fprintf(out, "  dropped every %s cell — %s is no longer in locales\n", loc, loc)
	}
	for _, k := range droppedKeys {
		fmt.Fprintf(out, "  dropped %q — no t() call uses it any more\n", k)
	}
	after, _ := os.ReadFile(path)
	if bytes.Equal(before, after) {
		fmt.Fprintf(out, "✓ %s unchanged (%d string(s))\n", StringsPath(), len(merged.Strings))
	} else {
		fmt.Fprintf(out, "✓ wrote %s (%d string(s), %d language(s))\n", StringsPath(), len(merged.Strings), len(merged.Locales))
	}
	return nil
}
