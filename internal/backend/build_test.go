package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	t.Cleanup(func() { os.RemoveAll(staged) })
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

// TestCheckMode_BareThirdPartyImportFails is the build==deploy parity lock.
//
// The deploy bundles the `palbase push` tarball, which strips node_modules, and
// never runs npm install — so a bare `import { z } from "zod"` dies there with
// `Could not resolve "zod"`. Check mode used to bundle the WORKING DIRECTORY,
// where npm has hoisted zod under @palbase/backend, so esbuild resolved it and
// the command printed "build OK" over a push that could not deploy. This is the
// live report: `palbase build` green, deploy red.
//
// Both directions on one fixture: the bare import must FAIL, and the identical
// code taking z from the SDK's re-export (the sanctioned form, kept external by
// the bundler exactly as the pod's global install provides it) must PASS. Flip
// the import, flip the exit — a harness that stopped staging would go green on
// the first case and this test turns red.
func TestCheckMode_BareThirdPartyImportFails(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	// zod must really be resolvable on disk, or the test would pass for the
	// wrong reason (absent dep rather than deploy-shaped staging).
	require.DirExists(t, filepath.Join(dir, "node_modules", "zod"),
		"fixture must have zod hoisted in node_modules — otherwise this test proves nothing")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "models"), 0o755))
	// .parse() keeps the import in a VALUE position: a type-only reference is
	// erased by esbuild and would never reach the resolver at all.
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

	bareZod := `import { z } from "zod";
export const TodoSchema = z.object({ id: z.string(), title: z.string() });
export type TodoSchema = z.infer<typeof TodoSchema>;
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models", "todo.ts"), []byte(bareZod), 0o644))
	out, ok := runCheckMode(t, dir)
	require.False(t, ok, "a bare third-party import must fail check mode — the deploy cannot resolve it:\n%s", out)
	require.Contains(t, out, `Could not resolve "zod"`,
		"must fail with the DEPLOY's own resolver error, not a proxy for it:\n%s", out)

	// Mutation twin: same code, SDK re-export. Must pass.
	sdkZod := `import { z } from "@palbase/backend";
export const TodoSchema = z.object({ id: z.string(), title: z.string() });
export type TodoSchema = z.infer<typeof TodoSchema>;
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models", "todo.ts"), []byte(sdkZod), 0o644))
	out, ok = runCheckMode(t, dir)
	require.True(t, ok, "z re-exported from @palbase/backend is the sanctioned form and must pass:\n%s", out)
}

// TestCheckMode_BrokenSchemaFails locks the db/schema.ts half of build==deploy.
//
// The deploy runs schema_extract.js over db/schema.ts and dies with "schema
// module does not export a defineSchema() result with .tables"; check mode never
// loaded the file at all, so `export {}` printed "build OK" and then failed the
// deploy. Both directions: a schema with no defineSchema() must FAIL, and a real
// one must PASS.
func TestCheckMode_BrokenSchemaFails(t *testing.T) {
	dir := t.TempDir()
	if !npmInstallBackend(t, dir) {
		t.Skip("node/npm unavailable or @palbase/backend install failed")
	}
	useTestParserCache(t)
	writeFixture(t, dir, goodControllerTS)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "db"), 0o755))

	// The reported shape: a schema module that exports nothing usable.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "schema.ts"), []byte("export {};\n"), 0o644))
	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	require.Error(t, err, "a schema with no defineSchema() must fail the build:\n%s", out.String())
	require.Contains(t, out.String(), "db/schema.ts", "the failure must name the file:\n%s", out.String())

	// Mutation twin: a real defineSchema() result must pass.
	// Shape verified against the SDK's real exports + a deployed project's
	// db/schema.ts — not invented: defineSchema({ tables: { <t>: { columns } } }).
	const realSchema = `import { defineSchema, uuid, text } from "@palbase/backend";
export default defineSchema({
  tables: {
    todos: {
      columns: {
        id: uuid().primaryKey().defaultRandom(),
        title: text().notNull(),
      },
    },
  },
});
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db", "schema.ts"), []byte(realSchema), 0o644))
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
