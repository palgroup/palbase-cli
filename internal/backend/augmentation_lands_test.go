package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE GATE MEASURES THAT THE AUGMENTATION LANDS, NOT THAT THE FILE EXISTS (FR-006).
//
// `palbase/palbase-env.d.ts` on disk proves nothing: without its trailing
// `export {};` it is an ambient script that REPLACES @palbase/backend's modules
// (FR-005), and with a drifted module name it augments nothing. Both leave a
// green build behind. verifyAugmentationLands compiles a probe through the
// project's own TypeScript and tsconfig — these tests run it against the SDK
// source beside this checkout, with the file rendered by that SDK's own
// makeEnvDts, and break the file the two ways that matter.

// augmentationProject stands up a project whose node_modules resolve to the SDK
// beside this checkout and writes the file its makeEnvDts renders.
func augmentationProject(t *testing.T) (dir string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		requireToolOnCI(t, "node", err)
		t.Skip("node is not on PATH")
	}
	sdk := sdkSourceDir(t)
	if _, err := os.Stat(filepath.Join(sdk, "dist", "index.js")); err != nil {
		t.Skipf("the SDK beside this checkout is not built (%v) — the probe resolves its published types", err)
	}
	dir = t.TempDir()
	link := func(from, to string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(to), 0o755))
		require.NoError(t, os.Symlink(from, to))
	}
	link(sdk, filepath.Join(dir, "node_modules", "@palbase", "backend"))
	for _, dep := range []string{"typescript", "bun-types", filepath.Join("@types", "node")} {
		link(filepath.Join(sdk, "node_modules", dep), filepath.Join(dir, "node_modules", dep))
	}
	tsconfig, err := os.ReadFile(filepath.Join(sdk, "template", "tsconfig.json"))
	require.NoError(t, err)
	mustWrite(t, dir, "tsconfig.json", string(tsconfig))

	render := exec.Command("node", "-e",
		`process.stdout.write(require("@palbase/backend").makeEnvDts([], {secrets:["STRIPE_KEY"],flags:[],buckets:[],roles:["admin"]}))`)
	render.Dir = dir
	body, err := render.Output()
	require.NoError(t, err)
	require.Contains(t, string(body), `declare module "@palbase/backend/stack"`, "the SDK beside this checkout predates the single-file renderer")
	mustWrite(t, dir, EnvTypesPath(), string(body))
	return dir
}

var augmentedNames = StackNames{Secrets: []string{"STRIPE_KEY"}, Roles: []string{"admin"}}

func TestAugmentationLandsForTheRenderedFile(t *testing.T) {
	dir := augmentationProject(t)
	require.NoError(t, verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), augmentedNames))
}

// MUTATION (a): the file loses `export {};` and becomes an ambient script.
func TestAugmentationDoesNotLandWithoutExportBraces(t *testing.T) {
	dir := augmentationProject(t)
	path := filepath.Join(dir, filepath.FromSlash(EnvTypesPath()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "export {};")
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw), "export {};", "", 1)), 0o644))

	err = verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), augmentedNames)
	require.ErrorContains(t, err, "does not apply")
}

// MUTATION (b): the module specifier drifts.
func TestAugmentationDoesNotLandOnAMisnamedModule(t *testing.T) {
	dir := augmentationProject(t)
	path := filepath.Join(dir, filepath.FromSlash(EnvTypesPath()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	broken := strings.Replace(string(raw), `"@palbase/backend/stack"`, `"@palbase/backend/stak"`, 1)
	require.NotEqual(t, string(raw), broken)
	require.NoError(t, os.WriteFile(path, []byte(broken), 0o644))

	err = verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), augmentedNames)
	require.ErrorContains(t, err, "does not apply")
}

// AN SDK FROM BEFORE THE STACK MODULE STILL BUILDS (D-23).
//
// `@palbase/backend/stack` arrived in 22.1.0; measured, 12.0.1 exports ".",
// "./db", "./env", "./test" and "./purchases" and nothing else — and a ^12
// project is live, deploying on the 12.x image. A probe that imported a stack
// type unconditionally refused that project's `palbase build` with
// `TS2307: Cannot find module '@palbase/backend/stack'`, on a file that
// augments `@palbase/backend/env` correctly. The probe measures the blocks the
// file DECLARES, not the ones the newest SDK would render.
func staleSDKProject(t *testing.T) (dir string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		requireToolOnCI(t, "node", err)
		t.Skip("node is not on PATH")
	}
	ts := filepath.Join(sdkSourceDir(t), "node_modules", "typescript")
	if _, err := os.Stat(filepath.Join(ts, "package.json")); err != nil {
		t.Skipf("no typescript beside this checkout to compile the probe with (%v)", err)
	}
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755))
	require.NoError(t, os.Symlink(ts, filepath.Join(dir, "node_modules", "typescript")))
	mustWrite(t, dir, "node_modules/@palbase/backend/package.json", `{"name":"@palbase/backend","version":"12.0.1",
  "exports":{".":{"types":"./index.d.ts","default":"./index.js"},"./env":{"types":"./env.d.ts","default":"./env.js"}}}`)
	mustWrite(t, dir, "node_modules/@palbase/backend/index.js", "module.exports = {};\n")
	mustWrite(t, dir, "node_modules/@palbase/backend/index.d.ts", "export {};\n")
	mustWrite(t, dir, "node_modules/@palbase/backend/env.js", "module.exports = {};\n")
	mustWrite(t, dir, "node_modules/@palbase/backend/env.d.ts", "export interface Tables {}\nexport interface Schemas {}\n")
	mustWrite(t, dir, "tsconfig.json", `{"compilerOptions":{"target":"ES2022","module":"ESNext","moduleResolution":"bundler","strict":true,"skipLibCheck":true,"noEmit":true}}`)
	mustWrite(t, dir, EnvTypesPath(), "declare module \"@palbase/backend/env\" {\n  interface Tables {\n    notes: { row: { id: string }; insert: { id?: string } };\n  }\n}\n\nexport {};\n")
	return dir
}

func TestAugmentationLandsOnAnSDKThatHasNoStackModule(t *testing.T) {
	dir := staleSDKProject(t)
	require.NoError(t, verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), StackNames{}))
}

// …and the shadowing it exists to catch is still caught there, by name: no
// stack type to lose does not mean no way to tell a script from a module.
func TestAugmentationWithoutExportBracesIsCaughtOnAnSDKThatHasNoStackModule(t *testing.T) {
	dir := staleSDKProject(t)
	path := filepath.Join(dir, filepath.FromSlash(EnvTypesPath()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw), "export {};", "", 1)), 0o644))

	err = verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), StackNames{})
	require.ErrorContains(t, err, "does not apply")
	require.ErrorContains(t, err, "is not a module")
}

// A DRIFTED MODULE NAME IS CAUGHT WITHOUT ANY NAME TO SPELL. A checkout that is
// not linked to a stack renders no names, and mutation (b) above only went red
// because it spelled one.
func TestAugmentationDoesNotLandOnAMisnamedModuleWithoutNames(t *testing.T) {
	dir := augmentationProject(t)
	path := filepath.Join(dir, filepath.FromSlash(EnvTypesPath()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	broken := strings.Replace(string(raw), `"@palbase/backend/stack"`, `"@palbase/backend/stak"`, 1)
	require.NotEqual(t, string(raw), broken)
	require.NoError(t, os.WriteFile(path, []byte(broken), 0o644))

	err = verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), StackNames{})
	require.ErrorContains(t, err, "does not apply")
	require.ErrorContains(t, err, `"@palbase/backend/stak"`)
}

// A MODULE RESOLUTION TYPESCRIPT DERIVES IS STILL A MODULE RESOLUTION.
//
// FR-007 names only a tsconfig that SETS node/node10/classic, and leaves an
// absent value alone on purpose (module_resolution.go). But a tsconfig that sets
// neither `module` nor `moduleResolution` compiles under node10, where the
// "exports" subpaths the file augments do not resolve — so the file types
// nothing, and this gate is the one that can say so. Measured when every build
// began to render the file: a fixture with `{"compilerOptions":{"strict":true}}`
// was refused here, by name, and rightly.
func TestAugmentationDoesNotLandUnderADerivedNode10Resolution(t *testing.T) {
	dir := augmentationProject(t)
	mustWrite(t, dir, "tsconfig.json", `{"compilerOptions":{"strict":true}}`)

	err := verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), StackNames{})
	require.ErrorContains(t, err, "does not apply")
	require.ErrorContains(t, err, `augments "@palbase/backend/env", which TypeScript cannot resolve`)
}

// A GATE THAT CANNOT MEASURE DOES NOT SAY GREEN.
func TestAugmentationProbeRefusesWithoutTypeScript(t *testing.T) {
	dir := augmentationProject(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "node_modules", "typescript")))

	err := verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), augmentedNames)
	require.ErrorContains(t, err, "typescript is not installed")
}

// NOTHING GENERATED, NOTHING TO MEASURE.
func TestAugmentationProbeSkipsAProjectWithNoGeneratedFile(t *testing.T) {
	require.NoError(t, verifyAugmentationLands(context.Background(), t.TempDir(), "", StackNames{}))
}
