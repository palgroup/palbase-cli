package backend

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

// fakeRegistry points npmRegistryBase at an httptest server for the run and
// restores it after. `latest` is the version the /latest route returns; an
// empty string makes the route 500 (registry-down simulation).
func fakeRegistry(t *testing.T, latest string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if latest == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"version":"` + latest + `"}`))
	}))
	t.Cleanup(srv.Close)
	prev := npmRegistryBase
	npmRegistryBase = srv.URL
	t.Cleanup(func() { npmRegistryBase = prev })
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
//
// Mutation (M5): change the `im != latest` guard in runBuild to always-false
// and this goes RED — the skew passes and the build no longer fails.
func TestRunBuild_SkewMismatchFailsBeforeExtraction(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	seedInstalledBackend(t, dir, "8.2.0")
	fakeRegistry(t, "9.0.1")

	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	require.Error(t, err, "8.x local vs 9.x latest must fail the build")
	require.Contains(t, out.String(), "major behind the latest 9.x")
	require.Contains(t, out.String(), "npm install @palbase/backend@^9")
}

// TestRunBuild_RegistryDownWarnsAndContinues locks the fail-open contract: when
// the registry is unreachable the skew check WARNS but does not fail — the
// build proceeds (and, with no real SDK to extract, passes). Registry-down must
// never block a push.
func TestRunBuild_RegistryDownWarnsAndContinues(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "controllers"), 0o755))
	seedInstalledBackend(t, dir, "9.0.0")
	fakeRegistry(t, "") // 500 → unreachable

	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	require.NoError(t, err, "registry-down must not fail the build")
	require.Contains(t, out.String(), "could not verify @palbase/backend against the registry")
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
// serve's route-register SILENTLY accepts but the deploy's extract_meta.js
// rejects — can only be proven with the REAL SDK's decorators running. This
// test installs the real published @palbase/backend into a temp fixture and
// runs check mode against it, so a real bundled controller meets the real
// extractor (no synthetic body). Skips when node/npm are unavailable or the
// install fails (offline CI); the skew tests above always run.

// npmInstallBackend installs @palbase/backend + the extractor's transitive dep
// into dir/node_modules. Returns false (skip) when node/npm absent or install
// fails.
func npmInstallBackend(t *testing.T, dir string) bool {
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
	cmd := exec.Command(npm, "install", "--silent", "--no-audit", "--no-fund",
		"@palbase/backend", "typescript@^5", "zod-to-json-schema")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("npm install failed (skipping cross-boundary extraction test): %v\n%s", err, out)
		return false
	}
	return true
}

// runCheckMode extracts the embedded devjs and runs PALBASE_CHECK=1 dev-server.js
// against dir, returning (combined output, exitOK). Mirrors runBuild's node
// invocation exactly.
func runCheckMode(t *testing.T, dir string) (string, bool) {
	t.Helper()
	tmp := t.TempDir()
	require.NoError(t, extractFS(devServerFS, "devjs", tmp))
	cmd := exec.Command("node", filepath.Join(tmp, "dev-server.js"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PALBASE_CHECK=1",
		"PALBASE_DEV_ROOT="+dir,
		"NODE_PATH="+filepath.Join(dir, "node_modules"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

const goodModelTS = `import { z } from "@palbase/backend";
export const TodoSchema = z.object({ id: z.string(), title: z.string() });
`

// brokenControllerTS uses @QueryParams("field") — a STRING where a zod schema
// is required (the centauri class). Deploy-fatal at extraction; serve accepts
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
