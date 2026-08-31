package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMajorOf pins the semver-major parse used by both skew layers: a range
// prefix (^/~/>=) is stripped, the leading integer is the major, garbage → 0
// (the caller treats 0 as "can't compare" and skips the gate).
func TestMajorOf(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"9.0.1", 9},
		{"^9.0.0", 9},
		{"~9.1", 9},
		{"v10.2.3", 10},
		{">=8", 8},
		{"  9.0.0 ", 9},
		{"latest", 0},
		{"*", 0},
		{"", 0},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, majorOf(c.in), "majorOf(%q)", c.in)
	}
}

// TestInstalledBackendVersion reads node_modules/@palbase/backend/package.json;
// absent/garbage → "" (skew check is skipped, never crashes).
func TestInstalledBackendVersion(t *testing.T) {
	dir := t.TempDir()
	require.Equal(t, "", installedBackendVersion(dir), "no node_modules → empty")

	pkgDir := filepath.Join(dir, "node_modules", "@palbase", "backend")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version":"9.0.1"}`), 0o644))
	require.Equal(t, "9.0.1", installedBackendVersion(dir))

	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`not json`), 0o644))
	require.Equal(t, "", installedBackendVersion(dir), "garbage → empty")
}

// seedInstalledBackend writes a fake node_modules/@palbase/backend so the skew
// check has an installed version to compare (no controllers/ needed for the
// skew branch — but runBuild returns early on missing controllers/, so callers
// that exercise the skew branch also seed a controllers/ dir).
func seedInstalledBackend(t *testing.T, dir, version string) {
	t.Helper()
	pkgDir := filepath.Join(dir, "node_modules", "@palbase", "backend")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"version":"`+version+`"}`), 0o644))
	// Stub zod-to-json-schema so ensureDevServerTools is a no-op (no real npm
	// install → the skew tests stay hermetic and fast).
	ztjs := filepath.Join(dir, "node_modules", "zod-to-json-schema")
	require.NoError(t, os.MkdirAll(ztjs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ztjs, "package.json"), []byte(`{"version":"1.0.0"}`), 0o644))
}

// TestRunBuild_SkewMismatchFailsBeforeExtraction locks Layer A: a local major
// behind the npm latest major fails the build (exit 1) with the fix command,
// WITHOUT reaching the (slow) local extraction. The installed 8.x vs registry
// 9.x is the centauri skew.
// A major that is not the newest must NOT fail the local build.
//
// This test asserted the opposite until 2026-08-04, on the premise that "deploys
// run the latest major and will reject this tree". The runtime now vendors every
// major inside a 12-month window and builds each tenant against the one their
// lockfile resolved — verified live: a ^12.0.0 project deployed against a runtime
// whose newest major is 13, serving traffic, its artifact manifest recording
// sdkVersion 12.0.1.
//
// Keeping the old assertion would keep a real regression: `palbase push` installs
// runBuild as a pre-push hook, so this check blocked the push before the platform
// ever saw it — a PASSING deploy turned into a FAILING local build. The
// authoritative decision belongs to deploy.CheckSDKMajor, which knows the actual
// vendored set; this process does not.
func TestRunBuild_OlderMajorDoesNotFailTheLocalBuild(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	seedInstalledBackend(t, dir, "12.0.1")

	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	require.NoError(t, err, "an older-but-supported major must build locally:\n%s", out.String())
	require.Contains(t, out.String(), "@palbase/backend 12.0.1",
		"the installed version is still reported, just not gated on")
	require.NotContains(t, out.String(), "major behind",
		"the removed premise must not come back")
}

// runBuild no longer contacts the npm registry at all, so an offline developer
// cannot be blocked by it. The previous version of this test locked a
// "registry-down warns but continues" contract; not calling the registry is the
// stronger form of the same guarantee.
func TestRunBuild_DoesNotContactTheRegistry(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	seedInstalledBackend(t, dir, "9.0.0")

	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	require.NoError(t, err, "no network, no failure:\n%s", out.String())
	require.NotContains(t, out.String(), "registry")
}

// TestRunBuild_NoControllersPasses: a project with no controllers/ has nothing
// to validate — exit 0, no skew call.
func TestRunBuild_NoControllersPasses(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, runBuild(context.Background(), t.TempDir(), &out))
	require.Contains(t, out.String(), "nothing to validate")
}

// --- Cross-boundary extraction lock (M1) --------------------------------------
//
// The above tests hold the skew LAYER. The extraction layer — the centauri
// class `@QueryParams("field")` (a string where a zod schema is required) that
// the route-register SILENTLY accepts but the deploy's extract_meta.js
// rejects — can only be proven with the REAL SDK's decorators running. This
// test installs the real published @palbase/backend into a temp fixture and
// runs check mode against it, so a real bundled controller meets the real
// extractor (no synthetic body). Skips when node/npm are unavailable or the
// install fails (offline CI); the skew tests above always run.

// npmInstallProject installs the fixture's deps into dir/node_modules. `deps` is
// the exact spec list (so a test can pick the project's typescript — or omit it
// entirely). Returns false (skip) when node/npm absent or the install fails.
func npmInstallProject(t *testing.T, dir string, deps ...string) bool {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		return false
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return false
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"buildfix","version":"1.0.0"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tsconfig.json"),
		[]byte(`{"compilerOptions":{"experimentalDecorators":true,"emitDecoratorMetadata":true,"target":"ES2022","module":"ESNext","moduleResolution":"Bundler","strict":false}}`), 0o644))
	args := append([]string{"install", "--silent", "--no-audit", "--no-fund"}, deps...)
	cmd := exec.Command(npm, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("npm install failed (skipping cross-boundary extraction test): %v\n%s", err, out)
		return false
	}
	return true
}

// npmInstallBackend installs @palbase/backend + the extractor's transitive dep
// into dir/node_modules, with the typescript a well-behaved project has today.
func npmInstallBackend(t *testing.T, dir string) bool {
	t.Helper()
	return npmInstallProject(t, dir, "@palbase/backend", "typescript@^5", "zod-to-json-schema")
}

// useTestParserCache points the CLI's tool cache (where ensureParserTS installs
// the pinned TypeScript) at a shared temp prefix instead of the developer's
// ~/.palbase — same code path, downloaded once per machine, reused across runs.
func useTestParserCache(t *testing.T) {
	t.Helper()
	home := filepath.Join(os.TempDir(), "palbase-cli-testdeps", "parser-home")
	prev := parserTSHome
	parserTSHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { parserTSHome = prev })
}

// runCheckMode extracts the embedded devjs and runs build-check.js
// against dir, returning (combined output, exitOK). Mirrors runBuild's node
// invocation exactly — including devNodePath, so the shipped parser resolution
// (CLI's pinned typescript first, project's node_modules second) is what the
// tests exercise, not a test-local reimplementation of it.
func runCheckMode(t *testing.T, dir string) (string, bool) {
	t.Helper()
	useTestParserCache(t)
	tmp := t.TempDir()
	require.NoError(t, extractFS(buildCheckFS, "devjs", tmp))
	// Stage exactly as runBuild does — the deploy-shaped tree (no node_modules),
	// with the extractor pointed at the real one. Mirroring runBuild here is the
	// point of this helper: a test that skipped staging would validate a tree no
	// deploy ever sees, which is the false-green this whole layer exists to kill.
	staged, err := stageDeployTree(dir)
	require.NoError(t, err)
	t.Cleanup(func() { removeTemp(staged) })
	cmd := exec.Command("node", filepath.Join(tmp, "build-check.js"))
	cmd.Dir = staged
	cmd.Env = append(os.Environ(),
		"PALBASE_DEV_ROOT="+staged,
		"NODE_PATH="+devNodePath(dir, io.Discard),
		"PALBASE_RUNTIME_MODULES="+filepath.Join(dir, "node_modules"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

const goodModelTS = `import { z } from "@palbase/backend";
export const TodoSchema = z.object({ id: z.string(), title: z.string() });
`

// brokenControllerTS uses @QueryParams("field") — a STRING where a zod schema
// is required (the centauri class). Deploy-fatal at extraction; the route
// register accepts
// it silently.
const brokenControllerTS = `import { Controller, Get, QueryParams, z } from "@palbase/backend";
import { TodoSchema } from "../models/todo";

@Controller("/todos")
export default class TodosController {
  @Get("/")
  async list(@QueryParams("workout_id") q: any): Promise<z.infer<typeof TodoSchema>> {
    return { id: "1", title: "x" };
  }
}
`

// goodControllerTS is the mutation-fixed twin: @QueryParams given a real zod
// schema. Must pass.
const goodControllerTS = `import { Controller, Get, QueryParams, z } from "@palbase/backend";
import { TodoSchema } from "../models/todo";

@Controller("/todos")
export default class TodosController {
  @Get("/")
  async list(@QueryParams(z.object({ workout_id: z.string() })) q: any): Promise<z.infer<typeof TodoSchema>> {
    return { id: "1", title: "x" };
  }
}
`

func writeFixture(t *testing.T, dir, controllerTS string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "models"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models", "todo.ts"), []byte(goodModelTS), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "controllers", "todos.controller.ts"), []byte(controllerTS), 0o644))
}

// TestCheckMode_CentauriClassIsCaught is the cross-boundary lock (M1): a real
// bundled controller with the centauri `@QueryParams("field")` bug meets the
// real extract_meta.js — check mode must FAIL (exit 1) with the assertZodSchema
// message. The mutation twin (a valid zod schema) must PASS (exit 0). One
// fixture install, both directions asserted — flipping the bug flips the exit.
func TestCheckMode_CentauriClassIsCaught(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed — skew tests cover the rest")
	}

	// BAD: the centauri class → exit 1 with the extraction error.
	writeFixture(t, dir, brokenControllerTS)
	out, ok := runCheckMode(t, dir)
	require.False(t, ok, "broken @QueryParams(string) must fail check mode:\n%s", out)
	require.Contains(t, out, "DEPLOY WOULD FAIL")
	require.Contains(t, out, "must be given a zod schema", "the assertZodSchema message must surface:\n%s", out)

	// MUTATION-FIXED: a real zod schema → exit 0.
	writeFixture(t, dir, goodControllerTS)
	out, ok = runCheckMode(t, dir)
	require.True(t, ok, "valid @QueryParams(zod) must pass check mode:\n%s", out)
	require.Contains(t, out, "build OK")
}

// TestCheckMode_UserTypeScript7StillBuilds is the regression lock for the day
// `npm view typescript version` became 7.x: a brand-new project that ran
// `npm install typescript @palbase/backend` got the REAL TypeScript 7 package —
// the Go-native compiler, whose CommonJS entry exports { version,
// versionMajorMinor } and nothing else — and every `palbase push` died with
// "Cannot read properties of undefined (reading 'ES2022')" because the harness
// borrowed the project's typescript as its parser.
//
// Real byte surface, no stub: typescript@7 is installed from npm into the
// fixture. The build must PASS — the CLI resolves its OWN pinned TypeScript 5
// ahead of the project on NODE_PATH (devNodePath), and the user's 7.x stays
// theirs (their `tsc` keeps working).
//
// Mutation (M5): make devNodePath return only the project's node_modules and
// this goes RED with the parser error.
func TestCheckMode_UserTypeScript7StillBuilds(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallProject(t, dir, "@palbase/backend", "typescript@7", "zod-to-json-schema") {
		t.Skip("node/npm unavailable or install failed")
	}
	// Guard the guard: if npm ever stops serving a 7.x here, the test would
	// silently degrade into "project had a usable TS all along" and prove nothing.
	requireProjectTSMajor(t, dir, "7")

	writeFixture(t, dir, goodControllerTS)
	out, ok := runCheckMode(t, dir)
	require.True(t, ok, "a project whose own typescript is 7.x must still build:\n%s", out)
	require.Contains(t, out, "build OK")
}

// TestCheckMode_NoTypeScriptInProjectStillBuilds: the parser is the CLI's tool,
// not a project dependency — a project that never installs typescript at all
// (nothing forces them to) must still build.
func TestCheckMode_NoTypeScriptInProjectStillBuilds(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallProject(t, dir, "@palbase/backend", "zod-to-json-schema") {
		t.Skip("node/npm unavailable or install failed")
	}
	_, err := os.Stat(filepath.Join(dir, "node_modules", "typescript"))
	require.True(t, os.IsNotExist(err), "fixture must have NO project typescript")

	writeFixture(t, dir, goodControllerTS)
	out, ok := runCheckMode(t, dir)
	require.True(t, ok, "a project with no typescript at all must still build:\n%s", out)
	require.Contains(t, out, "build OK")
}

// requireProjectTSMajor asserts the fixture's project-local typescript is the
// major the test means to simulate (npm resolved it for real).
func requireProjectTSMajor(t *testing.T, dir, major string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "node_modules", "typescript", "package.json"))
	require.NoError(t, err, "fixture must have a project-local typescript")
	var pkg struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(data, &pkg))
	require.Equalf(t, major, string(pkg.Version[0]), "fixture typescript must be %s.x, got %s", major, pkg.Version)
}

// TestCheckMode_BrokenReturnTypeFails locks the return-type gate through check
// mode: an inline (non-named) return type is deploy-fatal and check mode must
// fail. Same real-SDK fixture path.
func TestCheckMode_BrokenReturnTypeFails(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	const inlineReturn = `import { Controller, Get } from "@palbase/backend";

@Controller("/todos")
export default class TodosController {
  @Get("/")
  async list(): Promise<{ ok: boolean }> {
    return { ok: true };
  }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "controllers", "todos.controller.ts"), []byte(inlineReturn), 0o644))
	out, ok := runCheckMode(t, dir)
	require.False(t, ok, "inline return type must fail check mode:\n%s", out)
	require.Contains(t, out, "DEPLOY WOULD FAIL")
}

// TestCheckMode_AnImportNOTHINGProvidesFails is what remains of the old
// bare-third-party lock, and the change is a measurement rather than a
// relaxation.
//
// The old rule was "the deploy bundles the tarball and never runs npm install,
// so any bare third-party import must fail here". That premise stopped being
// true: `palbase push` builds the artifact LOCALLY, in the project directory
// with node_modules present, and ships the built bundle — so an import the
// project has installed is one the push resolves and deploys. Measured on this
// repository's own fixture on 2026-08-17: `palbase build` failed with
// `Could not resolve "zod"` on a tree `palbase push` bundles and deploys
// happily. Two commands in one CLI, disagreeing about the same tree.
//
// So the staged tree now reaches the project's dependencies, and what is still
// worth failing is an import NOTHING provides — which is the honest form of the
// same guard, and the one a deploy would hit too.
func TestCheckMode_AnImportNOTHINGProvidesFails(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "models"), 0o755))

	const controller = `import { Controller, Get } from "@palbase/backend";
import { TodoSchema } from "../models/todo";

@Controller("/todos")
export default class TodosController {
  @Get("/")
  async list(): Promise<TodoSchema> {
    return TodoSchema.parse({ id: "1", title: "x" });
  }
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "controllers", "todos.controller.ts"), []byte(controller), 0o644))

	// A package nobody installed and nobody declared.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models", "todo.ts"), []byte(`import { z } from "a-package-that-does-not-exist";
export const TodoSchema = z.object({ id: z.string() });
export type TodoSchema = z.infer<typeof TodoSchema>;
`), 0o644))
	out, ok := runCheckMode(t, dir)
	require.False(t, ok, "an import nothing provides must fail:\n%s", out)
	require.Contains(t, out, "Could not resolve",
		"must fail with the bundler's own resolver error:\n%s", out)

	// And the INSTALLED one passes, which is the half that used to be wrong: it
	// is what push ships.
	require.DirExists(t, filepath.Join(dir, "node_modules", "zod"),
		"fixture must have zod on disk — otherwise this half proves nothing")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models", "todo.ts"), []byte(`import { z } from "zod";
export const TodoSchema = z.object({ id: z.string(), title: z.string() });
export type TodoSchema = z.infer<typeof TodoSchema>;
`), 0o644))
	out, ok = runCheckMode(t, dir)
	require.True(t, ok, "an import the project HAS installed must pass — push bundles it:\n%s", out)
	require.Contains(t, out, "build OK")
}

// TestCheckMode_BrokenSchemaFails locks the db/ half of build==deploy.
//
// The deploy evaluates every db/*.ts and dies when one exports no
// defineSchema() result; check mode never loaded the files at all, so
// `export {}` printed "build OK" and then failed the deploy. Both directions: a
// schema with no defineSchema() must FAIL, and a real one must PASS.
func TestCheckMode_BrokenSchemaFails(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	useTestParserCache(t)
	writeFixture(t, dir, goodControllerTS)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0o755))

	// The reported shape: a schema module that exports nothing usable.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "public.ts"), []byte("export {};\n"), 0o644))
	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	require.Error(t, err, "a schema with no defineSchema() must fail the build:\n%s", out.String())
	require.Contains(t, out.String(), "db/public.ts", "the failure must name the file:\n%s", out.String())

	// The half above needed no schema DSL at all — a module exporting nothing is
	// rejected before anything is evaluated — so it has already run and passed
	// by the time we get here. The mutation twin below is the half that needs
	// the SDK to be able to express the declaration.
	requireCutoverSDK(t, dir)

	// Mutation twin: a real defineSchema() result must pass. The shape is the
	// one the SDK's OWN scaffold ships (template/db/public.ts) rather than a
	// literal written here: a fixture that agrees only with itself proves
	// nothing about the package this test just installed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "public.ts"), []byte(realPublicSchema), 0o644))
	out.Reset()
	require.NoError(t, runBuild(context.Background(), dir, &out),
		"a valid defineSchema() must pass:\n%s", out.String())
}

// TestBundleSrcDirKeepsNames pins --keep-names on the embedded build checker's
// esbuild invocation — prod parity with the deploy bundler, whose bundler_test
// pins the same flag on its args. The dotted operationId namespace derives from
// the live Ctrl.name, which only survives esbuild scope-hoisting renames under
// --keep-names, so dropping the flag would silently rename a controller class
// that collides with an imported service class (TodosController → TodosController2)
// and emit a different operationId than the deploy does.
func TestBundleSrcDirKeepsNames(t *testing.T) {
	src, err := buildCheckFS.ReadFile("devjs/build-check.js")
	require.NoError(t, err)
	body := string(src)
	start := strings.Index(body, "function bundleSrcDir")
	require.GreaterOrEqual(t, start, 0, "bundleSrcDir not found in embedded build-check.js")
	fn := body[start:]
	if end := strings.Index(fn, "\nfunction "); end >= 0 {
		fn = fn[:end]
	}
	require.Contains(t, fn, "'--keep-names'",
		"bundleSrcDir esbuild args must include --keep-names (dotted-id parity: Ctrl.name must survive bundle scope-hoisting renames)")
}

// TestRunBuild_LandsTheTypesInTheCheckout is the T009 lock, and it runs against
// the REAL SDK: npm-installed @palbase/backend, the real esbuild bundle, the real
// env-gen bridge. `palbase build` validates the tree a deploy receives — a
// staging copy — so the generated declaration file used to be written there and
// thrown away with it. This asserts the artifact reaches the place that makes it
// useful: the checkout the editor reads.
func TestRunBuild_LandsTheTypesInTheCheckout(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	requireCutoverSDK(t, dir)
	useTestParserCache(t)
	writeFixture(t, dir, goodControllerTS)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "public.ts"), []byte(realPublicSchema), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	require.NoError(t, runBuild(ctx, dir, &out), "build:\n%s", out.String())

	body, err := os.ReadFile(filepath.Join(dir, envTypesFile))
	require.NoError(t, err, "the derived types never reached the checkout:\n%s", out.String())
	require.Contains(t, string(body), "todos:", "the landed file does not describe db/public.ts:\n%s", body)
	require.Contains(t, string(body), `declare module "@palbase/backend/env"`)
	require.Contains(t, out.String(), envTypesFile, "the build does not say it wrote the file:\n%s", out.String())

	// Run twice: a build that changes nothing must not rewrite the file, because
	// a checkout that goes dirty on every build is a checkout nobody can read a
	// `git status` in.
	before, err := os.Stat(filepath.Join(dir, envTypesFile))
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, runBuild(ctx, dir, &out))
	after, err := os.Stat(filepath.Join(dir, envTypesFile))
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime(), "the second build rewrote an identical file")
	require.Contains(t, out.String(), "unchanged")
}

// TestBuildIgnoresAConfigDirectoryEntirely is the inverse of a test that used to
// live here.
//
// It asserted that `palbase build` evaluated config/*.ts into .palbase/config.json,
// because push shipped that document and plan read it. Neither is true any more:
// settings and secrets are written directly to the stack by whoever changes them,
// so a copy in the source tree could only disagree with the live one, and a push
// that carried it would silently overwrite what somebody set from the panel.
//
// ‼️ ITS OTHER HALF USED TO SAY "a leftover config/ does not FAIL the build, and
// people have these directories on disk right now" — AND THAT SENTENCE NAMED THE
// DEFECT WHILE CALLING IT A FEATURE. Those people were the ones being harmed:
// measured 2026-08-31 on a customer tree, `config/egress.ts` sat there importing
// `defineEgress` (removed in 23.0.0) while `palbase build` printed
// "build OK — 67 route(s)" and `tsc --noEmit` exited 0, and the owner believed
// five allowed hosts were declared in git. `reportDeadDeclarations` now refuses a
// retired declaration BY NAME; TestBuildRefusesARetiredConfigDeclaration covers it.
//
// What survives here, and is the reason this expensive test is kept: the build
// still does not EVALUATE config/. The fixture is a file with a name no
// declaration ever had, holding text no compiler would accept — if anything read
// the directory, this would fail. Using a RETIRED name instead would make the
// assertions below vacuous: the refusal returns before a config document could be
// produced, and a test that can only pass proves nothing.
func TestBuildIgnoresAConfigDirectoryEntirely(t *testing.T) {
	dir := t.TempDir()
	ctxPack, cancelPack := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelPack()
	sdk := packLocalSDK(t, ctxPack)
	if !npmInstallProject(t, dir, sdk, "typescript@^5", "zod-to-json-schema") {
		t.Skip("node/npm unavailable or the install failed")
	}
	useTestParserCache(t)
	writeFixture(t, dir, goodControllerTS)

	// A project's own module that merely lives under config/, holding text no
	// compiler would accept: nothing reads the directory, so nothing trips over it.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "pricing.ts"),
		[]byte("this is not valid typescript and it does not matter\n"), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	require.NoError(t, runBuild(ctx, dir, &out), "a plain module under config/ failed the build:\n%s", out.String())

	_, err := os.Stat(filepath.Join(dir, ".palbase", "config.json"))
	require.True(t, os.IsNotExist(err), "the build still produces a config document to ship")
	require.NotContains(t, out.String(), "config:", "the build still reports configuration it no longer carries")
}

// TestBuildAcceptsAControllerWithNoExport is one of two defects the CLI harness
// found the first time it ran, against this repository's own fixture.
//
// `@Controller` records the class as it decorates it, so a controller file needs
// no export — the SDK documents that and the runtime reads the registry. The
// local gate did not follow: it insisted on a default export and refused every
// file written the current way, on a project `palbase push` deploys happily. A
// gate that refuses what the deploy accepts is worse than no gate, because it
// teaches people to stop running it.
func TestBuildAcceptsAControllerWithNoExport(t *testing.T) {
	dir := t.TempDir()
	ctxPack, cancelPack := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelPack()
	// The LOCAL SDK: the published 17.4.0 has no controller registry, so a test
	// against it would skip forever and prove nothing about the code that ships.
	sdk := packLocalSDK(t, ctxPack)
	if !npmInstallProject(t, dir, sdk, "typescript@^5", "zod-to-json-schema") {
		t.Skip("node/npm unavailable or the install failed")
	}
	if !sdkHasControllerRegistry(t, dir) {
		t.Skip("the packed SDK exposes no controller registry")
	}
	useTestParserCache(t)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	// The SDK's own scaffold, verbatim in shape: a named zod schema for the
	// response, and NO export on the class — the decorator is the registration.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "controllers", "health.controller.ts"), []byte(`
import { Controller, Get, z } from "@palbase/backend";

export const HealthResponse = z.object({ status: z.string() });
export type HealthResponse = z.infer<typeof HealthResponse>;

@Controller("/health", { auth: false })
class HealthController {
  @Get("")
  check(): HealthResponse {
    return { status: "ok" };
  }
}
`), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	require.NoError(t, runBuild(ctx, dir, &out), "an unexported controller was refused:\n%s", out.String())
	require.Contains(t, out.String(), "build OK")
	require.Contains(t, out.String(), "/health")
}

// sdkHasControllerRegistry answers whether the installed SDK exposes the
// registry the runtime reads. The published 17.4.0 does not.
func sdkHasControllerRegistry(t *testing.T, dir string) bool {
	t.Helper()
	cmd := exec.Command("node", "-e",
		`process.stdout.write(String(typeof require("@palbase/backend").getRegisteredControllers))`)
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && string(out) == "function"
}

// fatSchemaControllerTS is one controller whose METADATA — not its source —
// crosses the pipe buffer the extractor's output travels through.
func fatSchemaControllerTS(fields int) string {
	var b strings.Builder
	b.WriteString("import { Controller, Post, z } from \"@palbase/backend\";\n\nconst Fat = z.object({\n")
	for i := 0; i < fields; i++ {
		fmt.Fprintf(&b, "  field%d: z.string().max(200).describe(%q).optional(),\n",
			i, strings.Repeat("d", 180)+fmt.Sprint(i))
	}
	b.WriteString("});\n\n@Controller(\"/fat\")\nexport class FatController {\n" +
		"  @Post(\"/one\")\n  one(input: z.infer<typeof Fat>): z.infer<typeof Fat> { return input; }\n}\n")
	return b.String()
}

// TestCheckMode_ABigSchemaStillBuilds is the regression lock for the truncated
// extractor pipe.
//
// ‼️ `process.exit` KUYRUKTAKİ YAZIMI DÜŞÜRÜR. The extractor writes its metadata
// to stdout — a PIPE — and exited in the same breath, so everything past the
// buffer was dropped. The parent then failed to parse the half-document and
// reported "extractor produced no JSON", which reads as a fault in the
// CONTROLLER. Ölçüldü 26.08.2026, gerçek çağrı şekliyle: 128 KiB yazan bir
// çocuktan tam 65 536 bayt okunuyor (node 26.7 ve bun 1.3.9).
//
// The trigger is SIZE, so nothing smaller can catch it: every fixture in this
// file is comfortably under the buffer and all of them passed while `palbase
// build` refused a real project's main branch. A user found this in production.
func TestCheckMode_ABigSchemaStillBuilds(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	writeFixture(t, dir, fatSchemaControllerTS(400))

	out, ok := runCheckMode(t, dir)
	require.True(t, ok, "a controller whose metadata exceeds the pipe buffer must still build:\n%s", out)
	require.Contains(t, out, "build OK")
	require.NotContains(t, out, "produced no JSON",
		"the extractor's output was truncated — the write is being abandoned by process.exit")
}

// TestCheckMode_AControllerThatRegistersNothingIsNamed.
//
// The route count is a SUM, and a sum hides a subtraction. A controller that
// loads but registers nothing takes its endpoints off the air while the report
// still ends in "build OK" — ölçüldü 26.08.2026: bir dosya rotalarını
// kaybettiğinde çıktı "build OK — 64 route(s)" dedi, ne dosyayı andı ne düşen
// üç rotayı. O çıktıda okunması gereken tek satır toplam sayıydı, ve onu ezbere
// bilmek gerekiyordu.
func TestCheckMode_AControllerThatRegistersNothingIsNamed(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	writeFixture(t, dir, goodControllerTS)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "controllers", "quiet.controller.ts"),
		[]byte("import { Controller } from \"@palbase/backend\";\n\n"+
			"@Controller(\"/quiet\")\nexport class QuietController {\n"+
			"  // its routes were lost in an edit\n}\n"), 0o644))

	out, ok := runCheckMode(t, dir)
	require.False(t, ok, "a controller that registers nothing must fail the build:\n%s", out)
	require.Contains(t, out, "quiet.controller.ts", "the silent file is not named:\n%s", out)
	require.Contains(t, out, "registered no routes")
}

// realPublicSchema is the declaration shape the SDK's own scaffold ships
// (@palbase/backend template/db/public.ts). Written in the DSL the installed
// package actually exports — `defineTable("name", …)` collected by
// `defineSchema("public", { tables: [ … ] })` — because these tests npm-install
// the real package and a fixture in a retired shape would prove only that the
// two halves of this file agree with each other.
const realPublicSchema = `import { defineSchema, defineTable, uuid, text, boolean } from "@palbase/backend";

const todos = defineTable("todos", {
  columns: {
    id: uuid().primaryKey().defaultRandom(),
    title: text().notNull(),
    done: boolean().default(false),
  },
});

export default defineSchema("public", { tables: [todos] });
`

// realBillingSchema is a SECOND schema, the thing the whole cutover exists for.
const realBillingSchema = `import { defineSchema, defineTable, uuid, integer } from "@palbase/backend";

const invoices = defineTable("invoices", {
  columns: {
    id: uuid().primaryKey().defaultRandom(),
    amount_cents: integer().notNull(),
  },
});

export default defineSchema("billing", { tables: [invoices] });
`

// TestRunBuild_AMultiSchemaProjectBuilds is the NEGATIVE CONTROL for every
// refusal this cutover added: the gates must reject the OLD layout and nothing
// else. A project declaring db/public.ts AND db/billing.ts is the legitimate
// shape the change was made for, and it has to walk all the way through the
// real SDK — read, bundled, evaluated, typed — not merely past the Go reader.
//
// It also pins the reason both files go in ONE makeEnvDts call: the generated
// file has to carry the second schema, and a per-file generator would emit two
// files that each think the other's tables do not exist.
func TestRunBuild_AMultiSchemaProjectBuilds(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	requireCutoverSDK(t, dir)
	useTestParserCache(t)
	writeFixture(t, dir, goodControllerTS)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "public.ts"), []byte(realPublicSchema), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "billing.ts"), []byte(realBillingSchema), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	require.NoError(t, runBuild(ctx, dir, &out),
		"a legitimate multi-schema project was refused:\n%s", out.String())

	body, err := os.ReadFile(filepath.Join(dir, envTypesFile))
	require.NoError(t, err, "the derived types never reached the checkout:\n%s", out.String())
	require.Contains(t, string(body), "todos:", "the public schema is missing from the types:\n%s", body)
	require.Contains(t, string(body), "billing", "the SECOND schema never reached the types:\n%s", body)
	require.Contains(t, string(body), "invoices:", "the second schema's table is missing:\n%s", body)
}

// TestRunBuild_TheLegacyLayoutIsRefusedByName is NFR-002 where a person meets
// it: `palbase build`. "Invalid schema" would send them looking; this names the
// file to write and where the move is written down.
func TestRunBuild_TheLegacyLayoutIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	useTestParserCache(t)
	writeFixture(t, dir, goodControllerTS)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "schema.ts"), []byte(realPublicSchema), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	require.Error(t, runBuild(ctx, dir, &out), "the retired layout built:\n%s", out.String())
	for _, want := range []string{LegacySchemaFile, PublicSchemaFile, MigrationGuide} {
		require.Contains(t, out.String(), want, "the refusal does not name %q:\n%s", want, out.String())
	}
}

// requireCutoverSDK skips when the @palbase/backend npm just installed predates
// the one-file-per-schema cutover.
//
// WHY A PROBE RATHER THAN A CONSTANT. These tests install from the REGISTRY, on
// purpose: their whole value is that they run the CLI against the SDK a real
// user resolves, not against a fixture this repo wrote. That makes them hostage
// to release order — the CLI moved to the new DSL (`defineTable`, and a
// `makeEnvDts` that takes every schema at once) before the package carrying it
// was published, so today the registry answers with a build that cannot express
// what the test is asking. Measured 2026-08-31: @palbase/backend@24.3.0 on npm
// exports no `defineTable`, and `makeEnvDts([schema])` throws "Cannot convert
// undefined or null to object".
//
// Skipping is right and pinning a version is not: the day the release lands,
// this probe passes and the tests run again with no edit. A hardcoded "skip
// until 25.x" would still be skipping a year later.
//
// The BEHAVIOUR is not unproven meanwhile — TestGenerateEnvTypes exercises the
// same bundle-and-bridge path against a pinned fixture package that mirrors the
// new contract and THROWS on the wrong arity. What waits on the release is the
// proof against the shipped artifact, which is a different claim and is why
// this says so out loud instead of quietly passing.
func requireCutoverSDK(t *testing.T, dir string) {
	t.Helper()
	const probe = `try { const m = require("@palbase/backend");
	  process.stdout.write(typeof m.defineTable === "function" ? "yes" : "no"); }
	catch (e) { process.stdout.write("no"); }`
	cmd := exec.Command("node", "-e", probe)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "yes" {
		t.Skipf("the @palbase/backend on the registry predates the cutover (no `defineTable`) — "+
			"this test proves the CLI against the SHIPPED SDK and runs again once that release lands; "+
			"probe said %q (err: %v)", strings.TrimSpace(string(out)), err)
	}
}

// --- the retired-declaration gate ---------------------------------------

// A `config/` declaration the SDK deleted is refused, by NAME, with its door.
//
// THE FAILURE THIS EXISTS FOR, MEASURED 2026-08-31 on a customer tree: a checkout
// written against 21.0.1 was upgraded, and `config/egress.ts` kept importing
// `defineEgress` — an export 23.0.0 removed. `palbase build` printed
// "build OK — 67 route(s)" and `tsc --noEmit` exited 0, because `config/` is in
// NEITHER include list (not the project's, not the template's) and the bundler
// never reads that directory. The person went on believing five allowed hosts
// were declared in git.
func TestBuildRefusesARetiredConfigDeclaration(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "egress.ts"),
		[]byte("import { defineEgress } from \"@palbase/backend\";\nexport default defineEgress({hosts:[]});\n"), 0o644))

	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	require.Error(t, err, "a dead declaration must fail the build, not warn")
	require.Contains(t, out.String(), "config/egress.ts")
	require.Contains(t, out.String(), "palbase egress add",
		"the refusal has to name the door, or it is a dead end")
}

// ‼️ AND A PLAIN MODULE THAT MERELY LIVES UNDER config/ IS NOT A DECLARATION.
// The same customer tree carries `config/pricing.ts` — its own price table,
// imported by `webhooks/stripe.ts`, nothing to do with palbase. A gate keyed on
// the DIRECTORY would have refused a correct repository.
func TestBuildAcceptsAPlainModuleUnderConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "pricing.ts"),
		[]byte("export const RATE = 3;\n"), 0o644))

	var out bytes.Buffer
	require.NoError(t, runBuild(context.Background(), dir, &out))
	require.NotContains(t, out.String(), "pricing.ts")
}

// --- the tsconfig coverage gate -----------------------------------------

// A directory the deploy COMPILES but the tsconfig does not INCLUDE is refused.
//
// `jobs/`, `webhooks/` and `hooks/` are read off disk by the bundler — no
// controller imports them, so `include` is the only thing that can put them in
// front of tsc. Measured on the same tree: the include list named neither, and two
// jobs read `meta.name` — a field `JobMeta` has never had — logging
// `job: undefined` on their error path for months, green through every gate.
func TestBuildRefusesATsconfigThatSkipsACompiledDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "jobs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "jobs", "sweep.ts"), []byte("export default class {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tsconfig.json"),
		[]byte(`{"include":["controllers/**/*.ts","services/**/*.ts"]}`), 0o644))

	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	require.Error(t, err)
	require.Contains(t, out.String(), "jobs/**/*.ts", "the refusal has to print the line to add")
}

// ‼️ NO `include` AT ALL IS NOT A HOLE — tsc then takes the whole directory, which
// is MORE coverage than any list. Judging that as a gap would refuse the one shape
// that cannot have this defect, and it is why the gate reads the key rather than
// the file's existence. It also sidesteps `extends`: a tsconfig that inherits its
// include has none of its own, and resolving the chain to say so would be a
// compiler's job, not a gate's.
func TestBuildAcceptsATsconfigWithNoIncludeList(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "jobs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "jobs", "sweep.ts"), []byte("export default class {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tsconfig.json"),
		[]byte(`{"compilerOptions":{"strict":true}}`), 0o644))

	var out bytes.Buffer
	require.NoError(t, runBuild(context.Background(), dir, &out))
}
