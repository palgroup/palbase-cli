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
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// backendPkg is the SDK a controller imports. Its major must be one the deploy
// runtime VENDORS — not necessarily the newest: the runtime keeps every major
// inside a 12-month window and builds each tenant against the one their lockfile
// resolved. Which majors those are is server-side knowledge, so the
// authoritative check is deploy.CheckSDKMajor; this command validates the code,
// not the version.
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
func newBuildCmd(_ Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Validate the backend locally the way a deploy would (catches broken pushes before they ship)",
		Long: `Run the same validation the deploy runs — stage, bundle, and extract
controller metadata — against your local tree, so a push that would produce a
FAILED deploy is caught here first. Exits non-zero on a user-code error
(bad decorator, return-type, version skew); exits 0 when it passes or when the
local environment can't run it (a warning is printed and the server still
gates the real deploy).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return runBuild(cmd.Context(), cwd, cmd.OutOrStdout())
		},
	}
}

// runBuild is the `palbase build` body, factored out so `palbase push`
// (platform arm) can gate on it inline. Returns an error ONLY for a user-code
// validation failure (exit 1); environment problems (no controllers, npm
// install failed) warn and return nil (fail-open — the server gate is the
// authoritative backstop).
func runBuild(ctx context.Context, cwd string, out io.Writer) error {
	// THE SHAPE OF THE CHECKOUT, FIRST — AND BEFORE THE controllers/ EARLY RETURN.
	//
	// Both checks are pure filesystem: no node, no bun, no network, microseconds.
	// They run ahead of everything because a tree can carry either defect while
	// having no controllers/ at all, and because the answer is more useful before
	// sixty lines of route listing than after them.
	//
	// NOT SHORT-CIRCUITED. A checkout that has been through one upgrade tends to
	// have both, and reporting one at a time turns a single fix into two builds.
	deadDecl := reportDeadDeclarations(cwd, out)
	blindSpots := reportIncludeBlindSpots(cwd, out)
	if deadDecl || blindSpots {
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
		return nil
	}

	// Controllers import @palbase/backend; the bundler keeps it external and the
	// extractor require()s it from node_modules. Install once when absent so a
	// fresh clone can be validated in one step. Install failure = environment
	// problem, not a user-code error → warn + continue (server gate backstops).
	if backendDepMissing(cwd) {
		if err := installNodeDeps(cwd); err != nil {
			fmt.Fprintf(out, "warning: %s is not installed and `npm install` failed (%v) — cannot validate locally; the deploy will still gate it\n", backendPkg, err)
			return nil
		}
	}
	// Read the installed major BEFORE ensureBuildCheckTools: its `npm install
	// --no-save` can prune a hand-placed @palbase/backend that isn't in
	// package.json (test fixtures), and in general the version we want is the
	// one already on disk, not whatever a tool install leaves behind.
	installed := installedBackendVersion(cwd)

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
	// this tree". That premise stopped being true on 2026-08-04: the runtime now
	// vendors every SDK major inside a 12-month window and builds each tenant
	// against the one their lockfile resolved. A ^12 project deploys perfectly
	// well against a runtime whose newest major is 13 — verified live, serving
	// traffic, with the artifact manifest recording sdkVersion 12.0.1.
	//
	// Keeping the check would have been worse than removing it: it turned a
	// PASSING deploy into a FAILING local build, and because `palbase push`
	// installs this as a pre-push hook, it blocked the push before the platform
	// ever saw it. A gate that answers a question the server no longer asks is not
	// a safety net; it is a wrong answer delivered with confidence.
	//
	// The authoritative check remains server-side (deploy.CheckSDKMajor), which
	// knows the actual vendored set — something this process cannot know without
	// asking the platform. Surfacing that set locally is worth doing and is
	// tracked separately; printing the installed version is the honest subset of
	// it available here.
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
	// source: same bytes in, same bundler behaviour out. Staging failure is an
	// environment problem, not user code → warn + fall back to the live tree.
	buildRoot := cwd
	if staged, serr := stageDeployTree(cwd); serr != nil {
		fmt.Fprintf(out, "warning: could not stage the deploy tree (%v) — validating the working directory instead; a bare third-party import may still pass here and fail the deploy\n", serr)
	} else {
		defer removeTemp(staged)
		buildRoot = staged
	}

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
	// db/ is deploy-fatal too and check mode never looked at it: a schema module
	// that doesn't export a defineSchema() result passed `palbase build` and then
	// failed the deploy. generateEnvTypes runs the SAME bundle + bridge the
	// deploy's extractor does (and is a clean no-op when the project declares no
	// database), so reuse it rather than restating the rule. It writes into the
	// staging tree, which is discarded — build validates, it does not mutate.
	//
	// The refusal names the DIRECTORY, not a file: the failure can be the old
	// layout, a missing db/public.ts, or one sibling of several that does not
	// evaluate — and the error carries which.
	if err := generateEnvTypes(ctx, buildRoot, filepath.Join(cwd, "node_modules")); err != nil {
		fmt.Fprintf(out, "✗ DEPLOY WOULD FAIL: %s/ — %v\n", SchemaDir, err)
		return fmt.Errorf("build failed")
	}
	// …and then it LANDS in the checkout. Generating into the staging tree and
	// discarding it validated the schema and left the person with nothing: their
	// editor still typed `Database.tables.*` from whatever `palbase-env.d.ts` was
	// last written, which is the file's whole reason to exist.
	//
	// It is written as soon as the schema is valid, before the controller checks
	// below can fail the build. The types describe db/*.ts and nothing else —
	// a controller with a bad decorator does not make them wrong, and the moment
	// somebody most needs their editor working is while they are fixing one.
	if err := landEnvTypes(buildRoot, cwd, out); err != nil {
		return err
	}

	// There is no config to evaluate. A build produces what a push ships, and a
	// push ships code and schema: settings reach the stack directly, from
	// whoever changes them, and a copy of them in the source tree could only
	// disagree with the live one.
	//
	// But the NAMES the stack holds are what makes `Secrets.get("…")`,
	// `Flags.isEnabled("…")` and `@Upload({ bucket })` compile-checked, and
	// something has to ask for them. `build` is where `palbase-env.d.ts` is
	// already regenerated, so it is where the other generated file belongs too:
	// one verb regenerates a project's types, not two.
	//
	// BEST-EFFORT ON PURPOSE. `build` works offline — that is its whole shape —
	// so an unreachable stack is reported and skipped, never fatal, and the
	// existing file is left exactly as it was. Narrowing a project's types to
	// nothing because a network call failed would turn every `Secrets.get()` in
	// the codebase into a compile error reading "your code is wrong".
	landStackTypes(ctx, cwd, out)

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

// landStackTypes regenerates palbase-stack.d.ts from the names the linked stack
// holds. Best-effort: a checkout with no stack, or a stack that cannot be
// reached, leaves the existing file alone and says so in one line.
func landStackTypes(ctx context.Context, cwd string, out io.Writer) {
	target, err := ReadTarget()
	if err != nil {
		// Not linked to anything. Nothing to ask, and nothing is wrong.
		return
	}
	names, err := stackNamesFor(ctx, target)
	if err != nil {
		fmt.Fprintf(out, "  %s not refreshed — %v\n", stackTypesFile, err)
		return
	}
	if err := generateStackTypes(ctx, cwd, filepath.Join(cwd, "node_modules"), names); err != nil {
		fmt.Fprintf(out, "  %s not refreshed — %v\n", stackTypesFile, err)
		return
	}
	fmt.Fprintf(out, "✓ %s (%d secret(s), %d flag(s), %d bucket(s))\n",
		stackTypesFile, len(names.Secrets), len(names.Flags), len(names.Buckets))
}

// landEnvTypes copies the generated palbase-env.d.ts out of the staging tree and
// into the checkout, where the project's tsconfig can see it.
//
// A no-op in the two cases that are not mistakes: the project declares no
// database (nothing was generated), or staging fell back to the live tree
// (generation already wrote there).
func landEnvTypes(buildRoot, cwd string, out io.Writer) error {
	if buildRoot == cwd {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(buildRoot, envTypesFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dest := filepath.Join(cwd, envTypesFile)
	if prev, rerr := os.ReadFile(dest); rerr == nil && bytes.Equal(prev, body) {
		fmt.Fprintf(out, "✓ %s (unchanged)\n", envTypesFile)
		return nil
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", envTypesFile, err)
	}
	fmt.Fprintf(out, "✓ %s\n", envTypesFile)
	return nil
}

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

// stackNamesFor asks the linked stack for the three name sets the generated
// types are rendered from.
//
// One round trip per set, and a failure in ANY of them fails the whole read:
// a partial answer would generate a file that narrows `Secrets.get()` correctly
// and `Flags.isEnabled()` to nothing, which is worse than not refreshing —
// the compile error would land on code that is right.
func stackNamesFor(ctx context.Context, target Target) (StackNames, error) {
	cred, _, err := Credential(target.URL)
	if err != nil {
		return StackNames{}, fmt.Errorf("no credential for %s", target.Describe())
	}
	secrets, err := secretNames(ctx, target, cred)
	if err != nil {
		return StackNames{}, err
	}
	flags, err := flagKeys(ctx, target, cred)
	if err != nil {
		return StackNames{}, err
	}
	buckets, err := stackBuckets(ctx, target)
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
