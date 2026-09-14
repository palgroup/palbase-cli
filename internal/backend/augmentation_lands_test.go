package backend

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// THE GATE MEASURES THAT THE AUGMENTATION LANDS, NOT THAT THE FILE EXISTS (FR-006).
//
// `palbase/palbase-env.d.ts` on disk proves nothing: without its trailing
// `export {};` it is an ambient script that REPLACES @palbase/backend's modules
// (FR-005), and with a drifted module name it augments nothing. Both leave a
// green build behind. verifyAugmentationLands compiles a probe through the
// project's own tsconfig and installed SDK.
//
// THE NEGATIVE CONTROLS NEED NO OTHER REPOSITORY (review-T012 C2). They ran only
// where `sdk/palbase-ts` sat beside this checkout — a private repository this
// module's CI never checks out — so every test proving the probe CATCHES
// something skipped there, and a probe reduced to `return nil` stayed green.
// They stand on a minimal SDK written here and on the CLI's own pinned parser
// TypeScript (ensureParserTS), which CI installs; when that cannot be
// provisioned, CI fails rather than skips. The one test that renders through
// the real SDK still runs wherever the SDK is beside this checkout.

// probeTypeScript provisions the CLI's pinned TypeScript for a fixture project.
func probeTypeScript(t *testing.T) string {
	t.Helper()
	requiresRealToolchain(t)
	if _, err := exec.LookPath("node"); err != nil {
		requireToolOnCI(t, "node", err)
		t.Skip("node is not on PATH")
	}
	useTestParserCache(t)
	mods := ensureParserTS(io.Discard)
	if mods == "" {
		err := errors.New("typescript@" + parserTSVersion + " could not be provisioned into the test parser cache")
		requireToolOnCI(t, "typescript", err)
		t.Skipf("%v", err)
	}
	return filepath.Join(mods, "typescript")
}

// fixtureSDKFiles is a minimal @palbase/backend publishing the two type modules
// the generated file augments, under dist/ and ONLY through package.json
// "exports" — the real package's shape, so a resolution mode that cannot read
// "exports" cannot find them here either.
func writeFixtureSDK(t *testing.T, dir string, importOnly bool) {
	t.Helper()
	cond := func(types, js string) string {
		if importOnly {
			return `{"import":{"types":"` + types + `","default":"` + js + `"}}`
		}
		return `{"types":"` + types + `","default":"` + js + `"}`
	}
	typ := ""
	if importOnly {
		typ = `"type":"module",`
	}
	mustWrite(t, dir, "node_modules/@palbase/backend/package.json", `{"name":"@palbase/backend","version":"40.0.0",`+typ+
		`"exports":{".":`+cond("./dist/index.d.ts", "./dist/index.js")+
		`,"./env":`+cond("./dist/env.d.ts", "./dist/env.js")+
		`,"./stack":`+cond("./dist/stack.d.ts", "./dist/stack.js")+`}}`)
	js := "module.exports = {};\n"
	if importOnly {
		js = "export {};\n"
	}
	for _, f := range []string{"index.js", "env.js", "stack.js"} {
		mustWrite(t, dir, "node_modules/@palbase/backend/dist/"+f, js)
	}
	mustWrite(t, dir, "node_modules/@palbase/backend/dist/index.d.ts", "export {};\n")
	mustWrite(t, dir, "node_modules/@palbase/backend/dist/env.d.ts", "export interface Tables {}\nexport interface Schemas {}\n")
	mustWrite(t, dir, "node_modules/@palbase/backend/dist/stack.d.ts",
		"export interface Secrets {}\nexport type PalbaseSecretName = keyof Secrets & string;\n"+
			"export interface Roles {}\nexport type PalbaseRoleName = keyof Roles & string;\n")
}

// fixtureEnvFile is the one generated file, both blocks, as the renderer lays it out.
const fixtureEnvFile = `// palbase:env:begin
declare module "@palbase/backend/env" {
  interface Tables {
    notes: { row: { id: string }; insert: { id?: string } };
  }
}
// palbase:env:end

// palbase:stack:begin
declare module "@palbase/backend/stack" {
  interface Secrets {
    STRIPE_KEY: true;
  }
  interface Roles {
    admin: true;
  }
}
// palbase:stack:end

export {};
`

const bundlerTSConfig = `{"compilerOptions":{"target":"ES2022","module":"ESNext","moduleResolution":"bundler","strict":true,"skipLibCheck":true,"noEmit":true}}`

// selfContainedProject is a project whose TypeScript, SDK and generated file
// all live in the test — nothing beside this checkout is read.
func selfContainedProject(t *testing.T, tsconfig string, importOnly bool) string {
	t.Helper()
	ts := probeTypeScript(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755))
	require.NoError(t, os.Symlink(ts, filepath.Join(dir, "node_modules", "typescript")))
	writeFixtureSDK(t, dir, importOnly)
	if importOnly {
		mustWrite(t, dir, "package.json", `{"name":"p","private":true,"type":"module"}`)
	}
	mustWrite(t, dir, "tsconfig.json", tsconfig)
	mustWrite(t, dir, EnvTypesPath(), fixtureEnvFile)
	return dir
}

var augmentedNames = StackNames{Secrets: []string{"STRIPE_KEY"}, Roles: []string{"admin"}}

func verifyFixture(dir string, names StackNames) error {
	return verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), names)
}

func breakFixtureFile(t *testing.T, dir, old, new string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(EnvTypesPath()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	broken := strings.Replace(string(raw), old, new, 1)
	require.NotEqual(t, string(raw), broken, "the mutation did not land in the fixture")
	require.NoError(t, os.WriteFile(path, []byte(broken), 0o644))
}

// POSITIVE: the correct file lands, with names spelled — the baseline every
// negative control below is measured against.
func TestAugmentationLandsForASelfContainedProject(t *testing.T) {
	dir := selfContainedProject(t, bundlerTSConfig, false)
	require.NoError(t, verifyFixture(dir, augmentedNames))
}

// MUTATION (a): the file loses `export {};` and becomes an ambient script.
func TestAugmentationDoesNotLandWithoutExportBraces(t *testing.T) {
	dir := selfContainedProject(t, bundlerTSConfig, false)
	breakFixtureFile(t, dir, "export {};", "")
	err := verifyFixture(dir, augmentedNames)
	require.ErrorContains(t, err, "does not apply")
	require.ErrorContains(t, err, "is not a module")
}

// MUTATION (b): the module specifier drifts, with a name to spell.
func TestAugmentationDoesNotLandOnAMisnamedModule(t *testing.T) {
	dir := selfContainedProject(t, bundlerTSConfig, false)
	breakFixtureFile(t, dir, `"@palbase/backend/stack"`, `"@palbase/backend/stak"`)
	require.ErrorContains(t, verifyFixture(dir, augmentedNames), "does not apply")
}

// A DRIFTED MODULE NAME IS CAUGHT WITHOUT ANY NAME TO SPELL. A checkout that is
// not linked to a stack renders no names.
func TestAugmentationDoesNotLandOnAMisnamedModuleWithoutNames(t *testing.T) {
	dir := selfContainedProject(t, bundlerTSConfig, false)
	breakFixtureFile(t, dir, `"@palbase/backend/stack"`, `"@palbase/backend/stak"`)
	err := verifyFixture(dir, StackNames{})
	require.ErrorContains(t, err, "does not apply")
	require.ErrorContains(t, err, `"@palbase/backend/stak"`)
}

// A MODULE RESOLUTION TYPESCRIPT DERIVES IS STILL A MODULE RESOLUTION.
//
// A tsconfig that sets neither `module` nor `moduleResolution` compiles under
// node10, where the "exports" subpaths the file augments do not resolve — so the
// file types nothing, and this gate is the one that can say so.
func TestAugmentationDoesNotLandUnderADerivedNode10Resolution(t *testing.T) {
	dir := selfContainedProject(t, `{"compilerOptions":{"strict":true}}`, false)
	err := verifyFixture(dir, StackNames{})
	require.ErrorContains(t, err, "does not apply")
	require.ErrorContains(t, err, `augments "@palbase/backend/env", which TypeScript cannot resolve`)
}

// NODENEXT READS THE IMPORT CONDITION FOR AN ESM FILE (review-T012 C1).
//
// FR-007 allows node16/nodenext. Asked with no resolution mode, TypeScript's
// resolver falls back to the "require" condition under them, so a package that
// exports its types only under "import" — a valid, increasingly common shape —
// was reported unresolvable while `tsc -p` compiled the same project clean. The
// file's own implied module format is the mode the compiler resolves its
// augmentations in.
func TestAugmentationLandsUnderNodeNextOnAnImportOnlySDK(t *testing.T) {
	dir := selfContainedProject(t,
		`{"compilerOptions":{"target":"ES2022","module":"NodeNext","moduleResolution":"NodeNext","strict":true,"skipLibCheck":true,"noEmit":true}}`, true)
	require.NoError(t, verifyFixture(dir, augmentedNames))
}

// A GATE THAT CANNOT MEASURE DOES NOT SAY GREEN.
func TestAugmentationProbeRefusesWithoutTypeScript(t *testing.T) {
	dir := selfContainedProject(t, bundlerTSConfig, false)
	require.NoError(t, os.Remove(filepath.Join(dir, "node_modules", "typescript")))
	require.ErrorContains(t, verifyFixture(dir, augmentedNames), "typescript is not installed")
}

// …NOR WHEN THE TYPESCRIPT IT FINDS HAS NO COMPILER API (review-T012 I1).
//
// TypeScript 7's CJS entry exports `{ version, versionMajorMinor }` and nothing
// else (tsparser.go). `require` succeeds, and the probe died on `ts.sys` with an
// internal TypeError instead of saying why it could not measure.
func TestAugmentationProbeRefusesATypeScriptWithoutACompilerAPI(t *testing.T) {
	dir := selfContainedProject(t, bundlerTSConfig, false)
	require.NoError(t, os.Remove(filepath.Join(dir, "node_modules", "typescript")))
	mustWrite(t, dir, "node_modules/typescript/package.json", `{"name":"typescript","version":"7.0.0","main":"index.js"}`)
	mustWrite(t, dir, "node_modules/typescript/index.js", `module.exports = { version: "7.0.0", versionMajorMinor: "7.0" };`+"\n")
	err := verifyFixture(dir, augmentedNames)
	require.ErrorContains(t, err, "typescript is not installed where the probe can load it")
	require.NotContains(t, err.Error(), "Cannot read properties")
}

// NOTHING GENERATED, NOTHING TO MEASURE.
func TestAugmentationProbeSkipsAProjectWithNoGeneratedFile(t *testing.T) {
	require.NoError(t, verifyAugmentationLands(context.Background(), t.TempDir(), "", StackNames{}))
}

// THE REAL RENDERER'S FILE LANDS — fidelity against the SDK beside this
// checkout, where there is one: the self-contained fixture above stands in for
// its layout, and this is where the two are held together.
func TestAugmentationLandsForTheRenderedFile(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		requireToolOnCI(t, "node", err)
		t.Skip("node is not on PATH")
	}
	sdk := sdkSourceDir(t)
	if _, err := os.Stat(filepath.Join(sdk, "dist", "index.js")); err != nil {
		t.Skipf("the SDK beside this checkout is not built (%v) — the self-contained tests above still run", err)
	}
	dir := t.TempDir()
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
	require.NoError(t, verifyFixture(dir, augmentedNames))
}

// AN SDK FROM BEFORE THE STACK MODULE STILL BUILDS (D-23).
//
// `@palbase/backend/stack` arrived in 22.1.0; measured, 12.0.1 exports ".",
// "./db", "./env", "./test" and "./purchases" and nothing else — and a ^12
// project is live, deploying on the 12.x image. The probe measures the blocks
// the file DECLARES, not the ones the newest SDK would render.
func staleSDKProject(t *testing.T) (dir string) {
	t.Helper()
	ts := probeTypeScript(t)
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755))
	require.NoError(t, os.Symlink(ts, filepath.Join(dir, "node_modules", "typescript")))
	mustWrite(t, dir, "node_modules/@palbase/backend/package.json", `{"name":"@palbase/backend","version":"12.0.1",
  "exports":{".":{"types":"./index.d.ts","default":"./index.js"},"./env":{"types":"./env.d.ts","default":"./env.js"}}}`)
	mustWrite(t, dir, "node_modules/@palbase/backend/index.js", "module.exports = {};\n")
	mustWrite(t, dir, "node_modules/@palbase/backend/index.d.ts", "export {};\n")
	mustWrite(t, dir, "node_modules/@palbase/backend/env.js", "module.exports = {};\n")
	mustWrite(t, dir, "node_modules/@palbase/backend/env.d.ts", "export interface Tables {}\nexport interface Schemas {}\n")
	mustWrite(t, dir, "tsconfig.json", bundlerTSConfig)
	mustWrite(t, dir, EnvTypesPath(), "declare module \"@palbase/backend/env\" {\n  interface Tables {\n    notes: { row: { id: string }; insert: { id?: string } };\n  }\n}\n\nexport {};\n")
	return dir
}

func TestAugmentationLandsOnAnSDKThatHasNoStackModule(t *testing.T) {
	dir := staleSDKProject(t)
	require.NoError(t, verifyFixture(dir, StackNames{}))
}

// …and the shadowing it exists to catch is still caught there, by name.
func TestAugmentationWithoutExportBracesIsCaughtOnAnSDKThatHasNoStackModule(t *testing.T) {
	dir := staleSDKProject(t)
	breakFixtureFile(t, dir, "export {};", "")
	err := verifyFixture(dir, StackNames{})
	require.ErrorContains(t, err, "does not apply")
	require.ErrorContains(t, err, "is not a module")
}

// A SPENT BUDGET SAYS SO (review-T012 M1). The probe is killed at NFR-002's
// ceiling, and the refusal read `signal: killed ()` — a crash, not a budget.
func TestAugmentationProbeNamesItsBudgetWhenItRunsOut(t *testing.T) {
	dir := selfContainedProject(t, bundlerTSConfig, false)
	prev := augmentationProbeBudget
	augmentationProbeBudget = time.Millisecond
	t.Cleanup(func() { augmentationProbeBudget = prev })
	err := verifyFixture(dir, augmentedNames)
	require.ErrorContains(t, err, "budget")
	require.ErrorContains(t, err, "NFR-002")
}
