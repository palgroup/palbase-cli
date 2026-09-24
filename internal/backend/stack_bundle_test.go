package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The entry this bundler generates is the CONTRACT between what a push ships
// and the runtime that loads it. Two properties in it are load-bearing and
// neither is visible from the outside until something is subtly broken at
// runtime, so they are asserted here.
func TestTheGeneratedEntryKeepsOneSDKInstance(t *testing.T) {
	dir := t.TempDir()
	entry, _ := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})

	// ONE module instance, shared with the runtime. Two instances each get their
	// own AsyncLocalStorage, and the request scope then silently does not reach
	// a handler — no error, no log, just a nil user in production.
	// By SYMBOL, not by the literal line: the seam grows (buildModuleClients was
	// added when the runtime began building module clients from the bundle's own
	// SDK copy), and an exact-match assertion turns every addition into a false
	// failure that says the opposite of what happened.
	for _, sym := range []string{"__runWithRuntime", "__requestALS", "__getRuntime"} {
		if !strings.Contains(entry, sym) {
			t.Errorf("the entry does not re-export %s:\n%s", sym, entry)
		}
	}
	if !strings.Contains(entry, `import * as SDK from "@palbase/backend"; export { SDK };`) {
		t.Errorf("the entry does not share the SDK namespace:\n%s", entry)
	}

	// Controllers are imported for their SIDE EFFECT and never named: @Controller
	// records the class as it decorates it. An entry that imported them by name
	// would refuse every controller written the documented way — with no export.
	if !strings.Contains(entry, `import "`+filepath.Join(dir, "controllers", "todo.controller.ts")+`";`) {
		t.Errorf("the controller is not imported for its side effect:\n%s", entry)
	}

	// And the registry is read AFTER the imports: ESM evaluates every import
	// before the module body, so this line is what makes the decorators visible.
	registry := strings.Index(entry, "SDK.getRegisteredControllers()")
	controller := strings.Index(entry, "todo.controller.ts")
	if registry < controller {
		t.Errorf("the registry is read before the controllers are imported:\n%s", entry)
	}
}

func TestTheSchemaTravelsOnlyWhenItExists(t *testing.T) {
	dir := t.TempDir()
	if entry0, _ := bundleEntry(dir, nil); strings.Contains(entry0, "db/public.ts") {
		t.Error("a project with no declaration got one imported anyway")
	}

	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"public.ts", "billing.ts"} {
		if err := os.WriteFile(filepath.Join(dir, "db", name), []byte("export default {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entry, _ := bundleEntry(dir, nil)
	// EVERY declaration travels, not just public's. The runtime resolves a
	// table by its schema-qualified key, and a key it was never handed resolves
	// to nothing — silently, because a missing table def only means "no column
	// metadata", never an error.
	if !strings.Contains(entry, "export const schemas = [__schema0, __schema1];") {
		t.Errorf("the bundle does not carry both declarations:\n%s", entry)
	}
	for _, want := range []string{"db/billing.ts", "db/public.ts"} {
		if !strings.Contains(entry, want) {
			t.Errorf("%s did not travel with the bundle", want)
		}
	}
	// NEGATIVE CONTROL: the retired single-export form must be gone. Leaving it
	// would let a runtime that still reads `schema` look correct on public and
	// be empty everywhere else.
	if strings.Contains(entry, "export const schema =") {
		t.Error("the bundle still emits the retired single-schema export")
	}
}

func TestABackendWithNoControllersIsRefusedBeforeAnythingShips(t *testing.T) {
	// buildStackArtifact checks for bun before it counts controllers, so without
	// it the refusal is about the toolchain and this test would be asserting the
	// wrong sentence.
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not installed — the refusal under test is not the one this machine produces")
	}
	// The silent-404 class: an artifact that activates and answers nothing. The
	// stack refuses it too, but by then a person has watched a "successful" push.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "controllers"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := buildStackArtifact(context.Background(), dir, t.TempDir(), &strings.Builder{})
	if err == nil {
		t.Fatal("a project with no controllers built successfully")
	}
	if !strings.Contains(err.Error(), "nothing would answer") {
		t.Errorf("the refusal does not say what the consequence is: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".palbase", "esm")); statErr == nil {
		t.Error("a refused build left products on disk — the next push would ship them")
	}
}

func TestAProjectThatIsNotABackendSaysSo(t *testing.T) {
	// The refusal names the MODULE, not a directory. It used to say
	// `controllers/`, which sent an author looking for a folder the runtime does
	// not use — and made the layout the module system exists to allow
	// unpushable.
	_, _, err := buildStackArtifact(context.Background(), t.TempDir(), t.TempDir(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "*.module.ts") {
		t.Fatalf("a directory with no module got %v", err)
	}
}

// The SDK-version trap, named where its victim can read it.
//
// npm's `latest` tag still points at the 17 line while the cloud runs 18.x.
// An SDK below the floor fails deep inside bun with `var controllers =
// undefined()` — a message about neither the version nor the problem. Measured
// 2026-08-21 against a real project.
//
// THE REMEDY IS `latest`, and the test says so on purpose. It used to be a
// pinned major, because npm's `latest` pointed at the 17 line and installing it
// made things WORSE. Verified 2026-08-24: latest is 21.0.0 and the live tenant
// runtime resolves 21.0.0, so telling somebody to pin 18 would now hand them an
// SDK three majors behind the runtime.
func TestCheckSDKVersionRefusesAMajorTheCLICannotBundle(t *testing.T) {
	dir := t.TempDir()
	writeSDK(t, dir, "17.4.0")

	err := checkSDKVersion(dir)
	if err == nil {
		t.Fatal("an SDK this CLI cannot bundle was accepted")
	}
	for _, want := range []string{"17.4.0", "18", "npm install @palbase/backend@latest"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the message does not say %q:\n%s", want, err)
		}
	}
}

func TestCheckSDKVersionAcceptsTheSupportedMajor(t *testing.T) {
	for _, v := range []string{"18.0.0", "18.0.1", "19.2.3", "21.0.0"} {
		dir := t.TempDir()
		writeSDK(t, dir, v)
		if err := checkSDKVersion(dir); err != nil {
			t.Fatalf("%s was refused: %v", v, err)
		}
	}
}

// No SDK on disk is NOT this check's business: the bundler's own error about a
// missing module says more than a guess would.
func TestCheckSDKVersionStaysQuietWhenThereIsNothingToRead(t *testing.T) {
	if err := checkSDKVersion(t.TempDir()); err != nil {
		t.Fatalf("a project with no SDK installed was refused: %v", err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "@palbase", "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "@palbase", "backend", "package.json"),
		[]byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkSDKVersion(dir); err != nil {
		t.Fatalf("an unreadable package.json was turned into a version error: %v", err)
	}
}

func writeSDK(t *testing.T, dir, version string) {
	t.Helper()
	p := filepath.Join(dir, "node_modules", "@palbase", "backend")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "package.json"),
		[]byte(`{"name":"@palbase/backend","version":"`+version+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The @Upload gate used to compare against config/storage.ts. That file is gone
// — buckets are created on the stack now — so the gate was pointed at the only
// source that can answer: the stack's own bucket list. It is a STRONGER gate
// than the one it replaces, which compared a name against a second declaration
// of intent rather than against what exists.
func TestAnUploadToABucketTheStackDoesNotHaveIsRefused(t *testing.T) {
	uses := []uploadUse{
		{Where: "Files.avatar", Bucket: "avatars"},
		{Where: "Files.receipt", Bucket: "recipts"}, // the typo this gate exists for
	}
	err := unknownUploadBuckets(uses, []string{"avatars", "receipts"})
	if err == nil {
		t.Fatal("a bucket the stack does not have was accepted")
	}
	// It has to name the ROUTE, not just the bucket: "recipts does not exist" in
	// a project with forty controllers is a search, not an answer.
	for _, want := range []string{"Files.receipt", "recipts", "receipts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "Files.avatar") {
		t.Errorf("a bucket that DOES exist was reported as missing: %v", err)
	}
}

func TestUploadsThatAllExistPassSilently(t *testing.T) {
	err := unknownUploadBuckets([]uploadUse{{Where: "Files.avatar", Bucket: "avatars"}}, []string{"avatars", "other"})
	if err != nil {
		t.Fatalf("a declared bucket was refused: %v", err)
	}
	// No @Upload at all: nothing to check, and NOT an error just because the
	// stack has no buckets.
	if err := unknownUploadBuckets(nil, nil); err != nil {
		t.Fatalf("a project with no uploads was refused: %v", err)
	}
}

func TestTheBucketListComesFromTheManagementSurface(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		_, _ = w.Write([]byte(`{"buckets":[{"name":"avatars"},{"name":"receipts"}]}`))
	}))
	defer srv.Close()

	got, err := stackBuckets(context.Background(), Target{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if asked != "/v1/management/storage/buckets" {
		t.Errorf("the bucket list was read from %q", asked)
	}
	if len(got) != 2 || got[0].Name != "avatars" || got[1].Name != "receipts" {
		t.Errorf("the stack's buckets did not arrive: %v", got)
	}
}

// The tests a deploy runs have to TRAVEL, and they have to travel BUILT.
//
// A tenant's suite imports its own models (`../models/todos/shared.js`) and the
// SDK's test client, and resolving those needs the project's node_modules —
// which live on this machine, not in the stack's container. So the tests are
// bundled by the same engine that bundles the controllers, for the same reason:
// a stack does not build, it collects.
func TestTheProjectsTestsAreBundledSoTheyCanTravel(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is what bundles a suite")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Two files, one of which imports a sibling — the relative import is the
	// whole reason bundling happens here rather than in the container.
	if err := os.WriteFile(filepath.Join(dir, "tests", "helper.ts"),
		[]byte("export const WHO = \"todos\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "todos.e2e.test.ts"), []byte(
		"import { test } from \"node:test\";\nimport { WHO } from \"./helper.ts\";\ntest(WHO, () => {});\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bundleRoot := t.TempDir()
	if err := bundleTests(context.Background(), dir, bundleRoot, deploySelection{}, &strings.Builder{}); err != nil {
		t.Fatalf("the suite did not bundle: %v", err)
	}

	// BUNDLE KOKUNDE, checkout'ta DEGIL. Bu satir eskiden `dir`i okuyordu ve
	// boylece urunun musterinin projesine yazilmasini SABITLIYORDU — 0.61.1'in
	// gocunun kacirdigi tek uretici tam da buydu.
	built := filepath.Join(bundleRoot, ".palbase", "esm", "tests", "todos.e2e.test.js")
	body, err := os.ReadFile(built)
	if err != nil {
		t.Fatalf("the bundled suite is not where the artifact collects it: %v", err)
	}
	// The sibling's contents must be INSIDE it: a bundle that still imports
	// ./helper.ts would resolve to nothing in the container that runs it.
	if !strings.Contains(string(body), "todos") {
		t.Error("the suite's relative import did not travel with it")
	}
	if strings.Contains(string(body), `from "./helper`) {
		t.Error("the suite still reaches for a file the stack does not have")
	}
	// helper.ts is not a suite and must not become one — `bun test` would run it
	// and report zero tests, which reads as a suite that silently does nothing.
	if _, err := os.Stat(filepath.Join(bundleRoot, ".palbase", "esm", "tests", "helper.js")); err == nil {
		t.Error("a non-test file was emitted as a suite")
	}
	if _, err := os.Stat(filepath.Join(dir, ".palbase")); err == nil {
		t.Error("the bundler wrote into the customer's checkout")
	}
}

func TestAProjectWithNoTestsBundlesNothingAndIsNotRefused(t *testing.T) {
	dir := t.TempDir()
	bundleRoot := t.TempDir()
	if err := bundleTests(context.Background(), dir, bundleRoot, deploySelection{}, &strings.Builder{}); err != nil {
		t.Fatalf("a project that declares no tests was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bundleRoot, ".palbase", "esm", "tests")); err == nil {
		t.Error("an empty tests directory was created for a project with none")
	}
}

// FR-017: A DEPLOY SUITE BESIDE THE CODE IT TESTS IS BUNDLED, FROM ANYWHERE IN
// THE PROJECT — not only tests/ (design.md J-17: `modules/notes/notes.e2e.test.ts`,
// the template's own shape, never reached bundleTests before). The neighbouring
// UNIT test stays home: a deploy runs what talks to the release (D-40).
//
// FR-017a: AND TWO SUITES NAMED ALIKE IN DIFFERENT MODULES BOTH SURVIVE. The
// bundler cannot use one output name twice, so without a collision-free
// staging step the second suite entered would silently overwrite the first —
// its failures, and its very existence, would vanish from what a deploy runs.
func TestModuleLocalTestsAreCollectedProjectWideWithoutCollision(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is what bundles a suite")
	}
	dir := t.TempDir()
	mustWrite(t, dir, "modules/a/x.e2e.test.ts", "import { test } from \"node:test\";\ntest(\"a\", () => {});\n")
	mustWrite(t, dir, "modules/b/x.e2e.test.ts", "import { test } from \"node:test\";\ntest(\"b\", () => {});\n")
	// NEGATİF KONTROL (D-40): kodun yanındaki BİRİM testi deploy'a girmez.
	mustWrite(t, dir, "modules/a/x.service.test.ts", "import { test } from \"node:test\";\ntest(\"unit\", () => {});\n")
	// NEGATİF KONTROLLER: node_modules paket yöneticisinindir, ve
	// .palbase-build-controllers bu CLI'ın ÖNCEKİ (ya da hâlâ süren) bir
	// komutun KENDİ staging çıktısıdır — ikisi de projenin kendi testi değil,
	// ve içine girmek bu projenin OLMAYAN bir süitini toplamak olurdu.
	mustWrite(t, dir, "node_modules/dep/dep.e2e.test.ts", "import { test } from \"node:test\";\ntest(\"dep\", () => {});\n")
	mustWrite(t, dir, ".palbase-build-controllers/x.e2e.test.ts", "import { test } from \"node:test\";\ntest(\"stale\", () => {});\n")

	bundleRoot := t.TempDir()
	if err := bundleTests(context.Background(), dir, bundleRoot, deploySelection{}, &strings.Builder{}); err != nil {
		t.Fatalf("the module-local suites did not bundle: %v", err)
	}

	outDir := filepath.Join(bundleRoot, ".palbase", "esm", "tests")
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read %s: %v", outDir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 2 {
		t.Fatalf("want exactly 2 bundled suites (one per module), got %v", names)
	}
	for _, want := range []string{"x.e2e.test.js", "b_x.e2e.test.js"} {
		ok := false
		for _, n := range names {
			if n == want {
				ok = true
			}
		}
		if !ok {
			t.Errorf("missing %q among bundled suites %v — one module-local suite overwrote the other (FR-017a)",
				want, names)
		}
	}
}

// oneTestSuite is the smallest suite bun bundles: one test, no imports of its own.
const oneTestSuite = "import { test } from \"node:test\";\ntest(\"x\", () => {});\n"

// bundledSuiteNames bundles dir's suites into a fresh root and answers the
// output names, sorted — exactly what the runtime's discovery reads.
func bundledSuiteNames(t *testing.T, dir string) []string {
	t.Helper()
	bundleRoot := t.TempDir()
	require.NoError(t, bundleTests(context.Background(), dir, bundleRoot, deploySelection{}, &strings.Builder{}))
	entries, err := os.ReadDir(filepath.Join(bundleRoot, ".palbase", "esm", "tests"))
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		// The runtime discovers `*.test.js` and nothing else
		// (v2/runtime/src/candidate-tests.ts): a name outside that is a suite
		// that ships and never runs.
		require.Truef(t, strings.HasSuffix(e.Name(), ".test.js"), "%s would never be discovered by the runtime", e.Name())
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// THE OUTPUT NAME IS THE IDENTITY (review-T016 CRITICAL-1). Collisions used to be
// looked up by the SOURCE name — `x.test.ts` and `x.test.mts` are two names —
// while the bundle was written under the name without its extension, where both
// are `x.test.js`. Two `bun build`s succeeded and the second silently replaced
// the first: a suite that shipped zero tests while the log said "2 suites".
func TestSuitesThatDifferOnlyByExtensionBothBundle(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is what bundles a suite")
	}
	dir := t.TempDir()
	mustWrite(t, dir, "modules/a/x.e2e.test.ts", oneTestSuite)
	mustWrite(t, dir, "modules/a/x.e2e.test.mts", oneTestSuite)

	require.Equal(t, []string{"a_x.e2e.test.js", "x.e2e.test.js"}, bundledSuiteNames(t, dir),
		"two suites named alike up to their extension landed on one output (FR-017a)")
}

// AND WITHOUT REGARD TO LETTER CASE (review-T016 IMPORTANT-1). On macOS and
// Windows `Login.test.js` and `login.test.js` are ONE file: a lookup that told
// them apart let the second bundle land on the first.
func TestSuitesThatDifferOnlyByLetterCaseBothBundle(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is what bundles a suite")
	}
	dir := t.TempDir()
	mustWrite(t, dir, "modules/a/Login.e2e.test.ts", oneTestSuite)
	mustWrite(t, dir, "modules/b/login.e2e.test.ts", oneTestSuite)

	require.Equal(t, []string{"Login.e2e.test.js", "b_login.e2e.test.js"}, bundledSuiteNames(t, dir))
}

// A PATH THAT RUNS OUT STILL NAMES A SUITE (review-T016 IMPORTANT-2). The prefix
// walk can reach the project root and still collide with a file somebody really
// named that way; refusing the whole push over it traded a working deploy for a
// naming scheme. A number goes BEFORE `.test.js`, where discovery still sees it.
func TestSuiteNamesFallBackToANumberWhenThePathIsExhausted(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is what bundles a suite")
	}
	dir := t.TempDir()
	mustWrite(t, dir, "a/x.e2e.test.ts", oneTestSuite)
	mustWrite(t, dir, "b/x.e2e.test.ts", oneTestSuite)
	mustWrite(t, dir, "b_x.e2e.test.ts", oneTestSuite)

	require.Equal(t, []string{"b_x.e2e.test.js", "b_x.e2e_2.test.js", "x.e2e.test.js"}, bundledSuiteNames(t, dir))
}

// TWO SUITES THAT DIFFER ONLY BY CASE IN ONE DIRECTORY ARE REFUSED (review-T016
// tur 2, deviations.md D-21). Each gets its own output name, but on a
// case-sensitive filesystem `bun build` does not tell `Login.test.ts` from
// `login.test.ts` in one directory: both bundles came out carrying the same
// suite, silently.
func TestSuitesThatDifferOnlyByCaseInOneDirectoryAreRefused(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "modules/a/Login.e2e.test.ts", oneTestSuite)
	// On a case-insensitive filesystem the second name IS the first file, so the
	// fixture cannot exist there. Decided before any assertion, from the
	// filesystem itself.
	if _, err := os.Stat(filepath.Join(dir, "modules", "a", "login.e2e.test.ts")); err == nil {
		t.Skip("this filesystem is case-insensitive: Login.test.ts and login.test.ts are one file here")
	}
	mustWrite(t, dir, "modules/a/login.e2e.test.ts", oneTestSuite)

	_, err := planTestSuites(dir, deploySelection{})
	require.ErrorContains(t, err, "differ only by letter case")
	require.ErrorContains(t, err, "Login.e2e.test.ts")
	require.ErrorContains(t, err, "login.e2e.test.ts")
}

// …AND THE REFUSAL IS ABOUT ONE DIRECTORY. The same pair in two directories is
// what the naming already separates; refusing it would refuse a working tree.
func TestSuitesThatDifferOnlyByCaseInTwoDirectoriesAreNamedApart(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "modules/a/Login.e2e.test.ts", oneTestSuite)
	mustWrite(t, dir, "modules/b/login.e2e.test.ts", oneTestSuite)
	suites, err := planTestSuites(dir, deploySelection{})
	require.NoError(t, err)
	require.Len(t, suites, 2)
}

// EACH SUITE IS BUILT FROM ITS OWN FILE (review-T016 CRITICAL-2). The first shape
// staged a symbolic link per suite, and a symbolic link on Windows needs a
// privilege an ordinary account does not hold — this CLI ships for Windows. A
// per-suite `--outfile` already names the output, so there is nothing to stage:
// the source is the file itself, and its relative imports resolve where it lives.
func TestPlanTestSuitesBuildsEachSuiteFromItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "modules/a/x.e2e.test.ts", oneTestSuite)
	mustWrite(t, dir, "modules/b/x.e2e.test.ts", oneTestSuite)

	suites, err := planTestSuites(dir, deploySelection{})
	require.NoError(t, err)
	require.Len(t, suites, 2)
	for _, s := range suites {
		info, err := os.Lstat(s.Source)
		require.NoError(t, err)
		require.Truef(t, info.Mode().IsRegular(), "%s is not the suite's own file", s.Source)
		require.Truef(t, strings.HasPrefix(s.Source, dir), "%s is outside the project", s.Source)
	}
	require.Equal(t, "x.e2e.test.js", suites[0].Out)
	require.Equal(t, "b_x.e2e.test.js", suites[1].Out)
}

// WEBHOOKS TRAVEL. Until this test existed the entry hardcoded `webhooks = []`,
// so a project's `webhooks/stripe.ts` was compiled into the bundle and then
// declared absent: the runtime mounts `/webhooks/<name>` from THIS export, so
// the URL answered 404 and the deploy said "successful".
//
// Measured 2026-08-25 against the live plane: a Stripe receiver was written,
// tested, pushed, and Stripe could never deliver an event to it. The same
// defect was measured on the core side on 2026-08-21 (v2/deploy/verify.sh:1725)
// and fixed there; this copy of the bundler kept it.
func TestTheBundlerDoesNotGetToNameThePublicAPI(t *testing.T) {
	for _, exported := range []bool{true, false} {
		name := "exported"
		if !exported {
			name = "not exported"
		}
		t.Run(name, func(t *testing.T) { assertControllerNamesSurviveTheBundle(t, exported) })
	}
}

func assertControllerNamesSurviveTheBundle(t *testing.T, exported bool) {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not installed")
	}
	dir := t.TempDir()
	ctl := filepath.Join(dir, "controllers")
	require.NoError(t, os.MkdirAll(ctl, 0o755))

	// A stand-in for the SDK: the entry imports these four symbols, and the
	// registry is what the extractor reads the class off.
	sdk := filepath.Join(dir, "node_modules", "@palbase", "backend")
	require.NoError(t, os.MkdirAll(sdk, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sdk, "package.json"),
		[]byte(`{"name":"@palbase/backend","version":"99.0.0","type":"module","main":"index.js"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sdk, "index.js"), []byte(`
const REG = [];
export function Controller() { return (cls) => { REG.push(cls); return cls; }; }
export function getRegisteredControllers() { return REG; }
export function __runWithRuntime() {}
export function buildModuleClients() { return {}; }
export const __requestALS = null;
export function __getRuntime() {}
`), 0o644))

	// TWO files, ONE class name — exactly what the source says.
	//
	// `exported` covers both ways a controller is written: the entry imports the
	// file for its SIDE EFFECT and never names the class, so a controller with no
	// export at all is the documented form and must be covered too. A fix that
	// read the name off the bundle's exports would silently skip it.
	for _, f := range []struct{ file, method string }{
		{"palaiAgents.controller.ts", "agents"},
		{"palaiBots.controller.ts", "bots"},
	} {
		decl := "export class"
		if !exported {
			decl = "class"
		}
		body := `import { Controller } from "@palbase/backend";

@Controller("/palai")
` + decl + ` PalaiController { ` + f.method + `() { return "` + f.method + `"; } }
`
		require.NoError(t, os.WriteFile(filepath.Join(ctl, f.file), []byte(body), 0o644))
	}
	sources := []string{
		filepath.Join(ctl, "palaiAgents.controller.ts"),
		filepath.Join(ctl, "palaiBots.controller.ts"),
	}

	entry := filepath.Join(dir, ".controllers-entry.ts")
	body, err := bundleEntry(dir, sources)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(entry, []byte(body), 0o644))

	out := filepath.Join(dir, "bundle.js")
	build := exec.Command("bun", "build", entry, "--target=bun", "--format=esm", "--outfile="+out)
	build.Dir = dir
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bundle failed: %v\n%s", err, b)
	}

	read := exec.Command("bun", "-e",
		`const m = await import(process.argv[1]); console.log(m.getRegisteredControllers().map(c => c.name).join(","));`,
		out)
	read.Dir = dir
	got, err := read.CombinedOutput()
	if err != nil {
		t.Fatalf("could not read the bundle: %v\n%s", err, got)
	}
	names := strings.TrimSpace(string(got))
	if names != "PalaiController,PalaiController" {
		t.Errorf("the bundle renamed a controller, so the public API namespace is the bundler's: got %q, want both classes to keep the name their SOURCE gives them", names)
	}
}

// The entry has to carry the project's CHANNELS.
//
// (The comment that used to stand here described the job manifest and opened
// with the name of a test — `TestTheJobManifestIsWrittenFromTheBundle` — that no
// longer exists: it went with the `bundleWithDefinitions` helper. A heading left
// over a different test is worse than no heading, because it is read as this
// one's. The job-manifest story is not lost; it lives above
// TestTheJobManifestIsWrittenFromWhatTheContainerHolds, with its measurement.)
func TestTheEntryShipsTheProjectsChannels(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "channels.ts"),
		[]byte(`import { defineChannels, ownerOnly } from "@palbase/backend";
export default defineChannels({ "user:{uid}": ownerOnly() });`), 0o644))

	entry, _ := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})

	channels := filepath.Join(dir, "channels.ts")
	assert.Contains(t, entry,
		"import __channels from "+strconv.Quote(channels)+"; export const channels = __channels;",
		"the entry does not carry the project's channels — every join would be refused")

	// The declaration must be evaluated with the controllers, BEFORE the module
	// body reads anything: the globalThis fallback the runtime keeps is written
	// by that evaluation.
	assert.Less(t, strings.Index(entry, "channels.ts"), strings.Index(entry, "SDK.getRegisteredControllers()"),
		"channels are imported after the registry is read")
}

// A project without a channels.ts still bundles — the file is optional, exactly
// like db/public.ts. An entry that named it unconditionally would fail to build
// for every project that does not use realtime.
func TestAProjectWithoutChannelsStillBundles(t *testing.T) {
	dir := t.TempDir()
	entry, _ := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})
	assert.NotContains(t, entry, "channels.ts")
	assert.NotContains(t, entry, "export const channels")
}

// HER İKİ BUNDLER DA AYNI SEAM'İ YAYMALI.
//
// Bu bundler'ın ikizi palbase deposunda: runtime/scripts/bundle-controllers.sh.
// Runtime, bundle'ın re-export ettigi seam'i ADIYLA ariyor ve eksikse BOOT'U
// REDDEDIYOR — yani seam'i yaymayan bir bundler, acilmayan bir deploy uretir.
//
// Olculdu 2026-08-29: shell bundler `buildModuleClients`'i kazandi, bu kazanmadi,
// ve runtime 0.39.0 yayinlandi. `palbase push` ile uretilen her bundle
// reddedilecekti.
func TestTheBundleEntryCarriesEveryRuntimeSeamSymbol(t *testing.T) {
	entry, _ := bundleEntry(t.TempDir(), nil)
	// Runtime bunlarin HEPSINI ariyor: hook'lar (abi.ts requestScopeHooksOf) ve
	// modul istemci kurucusu (abi.ts moduleClientsBuilderOf).
	for _, sym := range []string{"__runWithRuntime", "__requestALS", "__getRuntime", "buildModuleClients"} {
		if !strings.Contains(entry, sym) {
			t.Errorf("bundle entry %q yaymiyor — runtime bu bundle'i reddeder ve deploy acilmaz", sym)
		}
	}
}

// ZİNCİRİN KIRIK HALKASI, ÖLÇÜLDÜ. `stack-gen.ts` variant birliğini ZATEN
// üretiyor ve girdisi olarak {name, variants} bekliyor (bucketMembers,
// variant'sız kovada `never`). Go tarafı ise kova listesinden yalnız `name`
// ayrıştırıyordu, yani okuyanı olan bir bayrağın yazanı yoktu: yığında
// bildirilen bir rendition üretilen tipe hiç ulaşmıyordu.
func TestStackBucketsCarriesDeclaredVariants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"buckets":[
			{"name":"posts","variants":[{"name":"card"},{"name":"thumb"}]},
			{"name":"docs"}
		]}`))
	}))
	defer srv.Close()

	got, err := stackBuckets(context.Background(), Target{URL: srv.URL})
	if err != nil {
		t.Fatalf("stackBuckets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("iki kova bekleniyordu: %+v", got)
	}
	if got[0].Name != "posts" || len(got[0].Variants) != 2 {
		t.Fatalf("posts'un variant'ları düştü: %+v", got[0])
	}
	if got[0].Variants[0] != "card" || got[0].Variants[1] != "thumb" {
		t.Errorf("variant adları yanlış: %+v", got[0].Variants)
	}
	// NEGATİF KONTROL: variant bildirmeyen kova BOŞ liste taşır, nil değil —
	// üretilen tipte `never` olması gereken şey budur.
	if got[1].Name != "docs" || len(got[1].Variants) != 0 {
		t.Errorf("variant'sız kova temiz gelmedi: %+v", got[1])
	}
}

// THE APPLICATION'S DEFAULT AUTH TRAVELS THE SAME WAY, and for the same reason
// as channels: nothing else in a project imports the file that declares it.
//
// `defineDefaultAuth` is a function called at MODULE SCOPE; it writes the
// application ring of the auth cascade (route → controller → application →
// `true`) onto a globalThis symbol as its file is evaluated. A file no import
// reaches is not in the bundle, is never evaluated, and the declaration is
// SILENTLY not there — the cascade falls through to its terminal `true` and the
// project runs on a default it believes it replaced.
//
// Which way that fails depends on what was declared, and one direction is the
// dangerous one: `defineDefaultAuth({ verifiedEmail: true })` TIGHTENS the
// terminal default, so losing it leaves every route open to any signed-in user
// while the declaration sits in plain sight and the deploy reports success.
// Same silence as `jobs = []` and as the channels table that shipped empty.
//
// `auth.ts` at the project root is the well-known name, chosen to match
// `channels.ts` — one flat file the bundler knows by path.
func TestTheBundleEntryShipsTheProjectsDefaultAuth(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.ts"),
		[]byte(`import { defineDefaultAuth } from "@palbase/backend";
defineDefaultAuth({ verifiedEmail: true });`), 0o644))

	entry, _ := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})

	auth := filepath.Join(dir, "auth.ts")
	assert.Contains(t, entry, "import "+strconv.Quote(auth)+";",
		"the entry does not carry the project's default auth — the cascade would fall through to its terminal default")

	// Imported for its SIDE EFFECT and never named: the declaration is a call,
	// not a value, so there is nothing to export. It must still be evaluated
	// with the controllers, BEFORE the module body reads the registry — the
	// route table and the emitted spec both read the slot that evaluation fills.
	assert.Less(t, strings.Index(entry, "auth.ts"), strings.Index(entry, "SDK.getRegisteredControllers()"),
		"the default auth is imported after the registry is read")
}

// A project without an auth.ts still bundles — the file is optional, exactly
// like channels.ts and db/public.ts. An entry that named it unconditionally
// would fail to build for every project that never declared an application
// default, which is most of them.
func TestAProjectWithoutADefaultAuthStillBundles(t *testing.T) {
	dir := t.TempDir()
	entry, _ := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})
	assert.NotContains(t, entry, "auth.ts")
}

// A BUNDLE WITH NO SCHEMA MUST BE A DECISION, NEVER AN ACCIDENT.
//
// The schema block used to be guarded by `err == nil`, which swallowed the
// retired layout, a db/ with no public file, and an unreadable directory alike
// — and produced a bundle carrying NO schema, silently. At runtime that means
// `setSchema([])`: the project's typed `.tables` surface comes back empty with
// nothing anywhere naming the cause.
func TestAnUnreadableDeclarationStopsTheBundle(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The retired single-file layout: ReadSchemaSources refuses it by name.
	if err := os.WriteFile(filepath.Join(dir, "db", "schema.ts"), []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := bundleEntry(dir, nil); err == nil {
		t.Fatal("a refused declaration produced a bundle instead of an error")
	}

	// NEGATIVE CONTROL: no db/ at all is still legitimate and still bundles.
	empty := t.TempDir()
	entry, err := bundleEntry(empty, nil)
	if err != nil {
		t.Fatalf("a project with no declaration must still bundle: %v", err)
	}
	if strings.Contains(entry, "export const schemas") {
		t.Error("a project with no declaration got a schemas export anyway")
	}
}

// The entry carries MODULES and nothing that names a surface by its file.
//
// It used to emit `jobs`, `webhooks` and `hooks` arrays built from directory
// globs, with each entry's NAME taken from its file. Both halves were a second
// declaration the module system never saw: the directory decided what existed,
// and the file name decided its identity. Measured before the change — @Job,
// @Webhook and @Hook register NOTHING (only @Controller does), so a class
// carrying @Job outside jobs/ never ran, and one inside it ran whether or not
// any module listed it.
func TestTheEntryCarriesModulesAndNoByNameArrays(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "billing"), 0o755); err != nil {
		t.Fatal(err)
	}
	mods := []string{
		filepath.Join(dir, "app.module.ts"),
		filepath.Join(dir, "billing", "billing.module.ts"),
	}
	for _, m := range mods {
		if err := os.WriteFile(m, []byte("export class M {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entry, err := bundleEntry(dir, mods)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range mods {
		if !strings.Contains(entry, `import "`+m+`";`) {
			t.Errorf("module %s is not imported by the entry:\n%s", m, entry)
		}
	}
	// The three arrays are gone. A bundle that still declared them would be
	// declaring a second source of truth for what a project contains.
	for _, gone := range []string{"export const jobs", "export const webhooks", "export const hooks"} {
		if strings.Contains(entry, gone) {
			t.Errorf("the entry still emits %q — discovery is the module's now:\n%s", gone, entry)
		}
	}
	// Controllers stay: @Controller genuinely registers itself, and the engine
	// reconciles that list against the modules (a controller in no module is
	// REFUSED at boot rather than served).
	if !strings.Contains(entry, "export const controllers = SDK.getRegisteredControllers();") {
		t.Errorf("the entry no longer exports the registered controllers:\n%s", entry)
	}
}

// A module file lives beside the domain it owns, so discovery is recursive.
func TestModuleSourcesWalksNestedDirectoriesAndSkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"billing", filepath.Join("node_modules", "pkg"), ".palbase"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("export class M {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app.module.ts")
	write(filepath.Join("billing", "billing.module.ts"))
	write(filepath.Join("node_modules", "pkg", "vendor.module.ts"))
	write(filepath.Join(".palbase", "stale.module.ts"))

	found, err := moduleSources(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("expected the two project modules, got %v", found)
	}
	for _, f := range found {
		if strings.Contains(f, "node_modules") || strings.Contains(f, ".palbase") {
			t.Errorf("a non-source module leaked into discovery: %s", f)
		}
	}
}

// The manifest is written from WHAT THE CONTAINER HOLDS, and that is pure Go
// logic worth testing without a bundle.
//
// The half palsvc actually schedules from: the bundle carries the CODE, this
// file carries the fact Go cannot compute without executing TypeScript — when
// each job is due. `palbase push` wrote neither once, so a tenant with four
// @Job classes had zero rows in `jobs.job_definitions` and the scheduler had
// nothing to fire (measured 2026-08-26, tenant `1jhp7jbrm`).
func TestTheJobManifestIsWrittenFromWhatTheContainerHolds(t *testing.T) {
	dir := t.TempDir()
	surfaces := &bundleSurfaces{
		Controllers: 1,
		Jobs: []jobDef{
			{Name: "mac-scaler", Schedule: "* * * * *", Timeout: 60, Retry: 0},
			{Name: "auto-reload", Schedule: "0 3 * * *", Timeout: 60, Retry: 5},
		},
	}
	require.NoError(t, writeDefinitionManifests(context.Background(), dir, surfaces, &strings.Builder{}))

	blob, err := os.ReadFile(filepath.Join(dir, ".palbase", "jobs", "jobs.manifest.json"))
	require.NoError(t, err, "palsvc has no cron to read without this file")

	var manifest struct {
		Jobs []struct {
			Name     string `json:"name"`
			Schedule string `json:"schedule"`
			Timeout  int    `json:"timeout"`
			Retry    int    `json:"retry"`
		} `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(blob, &manifest))
	require.Len(t, manifest.Jobs, 2)
	require.Equal(t, "mac-scaler", manifest.Jobs[0].Name)
	require.Equal(t, "* * * * *", manifest.Jobs[0].Schedule)
	require.Equal(t, 60, manifest.Jobs[0].Timeout)
	require.Equal(t, 5, manifest.Jobs[1].Retry)
}

// Two jobs under one name is a scheduler that fires one of them and drops the
// other, silently. The name is DECLARED now, so it can be renamed away — the
// message has to say that, because the old one said the opposite ("a job takes
// its name from its file, so this cannot be renamed away").
func TestTwoJobsUnderOneNameIsRefused(t *testing.T) {
	err := writeDefinitionManifests(context.Background(), t.TempDir(), &bundleSurfaces{
		Jobs: []jobDef{
			{Name: "sweep", Schedule: "0 1 * * *"},
			{Name: "sweep", Schedule: "0 2 * * *"},
		},
	}, &strings.Builder{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sweep")
	require.Contains(t, err.Error(), "rename")
}

// A build that removed the last job must remove the manifest with it, or the
// scheduler keeps firing something the bundle no longer carries.
func TestAStaleJobManifestIsRemovedWhenTheLastJobGoes(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".palbase", "jobs")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "jobs.manifest.json"), []byte(`{"jobs":[{"name":"gone"}]}`), 0o644))

	require.NoError(t, writeDefinitionManifests(context.Background(), dir, &bundleSurfaces{}, &strings.Builder{}))

	_, err := os.Stat(filepath.Join(stale, "jobs.manifest.json"))
	require.True(t, os.IsNotExist(err), "a stale manifest survived a build with no jobs")
}

// HOOK MANIFESTI TASINMALI — yoksa @Hook/@On beyanlari SESSIZCE hicbir sey yapmaz.
//
// NE KIRILMISTI. `writeDefinitionManifests`'in adi cogul ama yalniz
// `jobs.manifest.json` yaziyordu. `.palbase/hooks/` dizini tam olarak bu dosya
// icin ayrilmis (bundleOutputDirs yorumu: "the compiled controllers and the two
// manifests") ve hep bos kaliyordu. Sunucu onu okuyor
// (palbase/v2 hooksmanifest/manifest.go:185), artifact varsa pakete koyuyor
// (artifact.go:599), archive.go'nun kendi yorumu "the only way" diyor — ve
// uretici yoktu.
//
// OLCULDU, palbase/v2 CI 35153323636: runtime acilista bes hook basiyor
// (document.created, file.uploaded, file.deleted, before.user.create,
// after.session.revoke) ama palsvc "count":0 ve "events":[] yaziyor. O kosunun
// kapisinda bes dusus: 15-14, 16-4 ve ardillari 16-6/16-8/16-11.
func TestHookManifestIsWritten(t *testing.T) {
	dir := t.TempDir()
	surfaces := &bundleSurfaces{
		Controllers: 1,
		Hooks: []hookDef{
			{Event: "before.user.create", Blocking: true, File: "SignupGate"},
			{Event: "document.created", File: "Listeners"},
			{Event: "file.uploaded", File: "Listeners"},
		},
	}
	require.NoError(t, writeDefinitionManifests(context.Background(), dir, surfaces, &strings.Builder{}))

	blob, err := os.ReadFile(filepath.Join(dir, ".palbase", "hooks", "hooks.manifest.json"))
	require.NoError(t, err, "palsvc has no hooks to register without this file")

	var manifest struct {
		Hooks []struct {
			Event    string `json:"event"`
			Blocking bool   `json:"blocking"`
			File     string `json:"file"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(blob, &manifest))
	require.Len(t, manifest.Hooks, 3)
	require.Equal(t, "before.user.create", manifest.Hooks[0].Event)
	require.True(t, manifest.Hooks[0].Blocking,
		"@Hook bloklar; blocking dusurse kapi kapanmaz ve kayit sessizce gecer")
	require.Equal(t, "document.created", manifest.Hooks[1].Event)
	require.False(t, manifest.Hooks[1].Blocking,
		"@On dinler; blocking:true bildirilirse sunucu MANIFESTIN TAMAMINI reddeder")
	require.Equal(t, "Listeners", manifest.Hooks[1].File)
}

// Ayni olayi iki kayit islerse BUILD durur. Sunucu bunu zaten reddediyor
// (ParseManifest: "olay basina bir handler"), ama orada reddetmek deploy'u
// yarida birakmak demek — manifest toptan reddedilir ve ESKI kayitlar calismaya
// devam eder, yani yeni hook sessizce yoktur.
func TestTwoHooksOnOneEventIsRefused(t *testing.T) {
	err := writeDefinitionManifests(context.Background(), t.TempDir(), &bundleSurfaces{
		Hooks: []hookDef{
			{Event: "before.user.create", Blocking: true, File: "GateA"},
			{Event: "before.user.create", Blocking: true, File: "GateB"},
		},
	}, &strings.Builder{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "before.user.create")
	require.Contains(t, err.Error(), "GateA")
	require.Contains(t, err.Error(), "GateB")
}

// Son hook da silindiyse manifest onunla gitmeli — jobs'ta oldugu gibi ve ayni
// sebeple: bayat bir manifest, kaldirilmis bir hook'u kayitli tutardi ve
// bloklayan bir kapi, kodu artik bulunmayan bir olayi reddetmeye devam ederdi.
func TestAStaleHookManifestIsRemovedWhenTheLastHookGoes(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".palbase", "hooks")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "hooks.manifest.json"),
		[]byte(`{"hooks":[{"event":"before.user.create"}]}`), 0o644))

	require.NoError(t, writeDefinitionManifests(context.Background(), dir, &bundleSurfaces{}, &strings.Builder{}))

	_, err := os.Stat(filepath.Join(stale, "hooks.manifest.json"))
	require.True(t, os.IsNotExist(err), "a stale hook manifest survived a build with no hooks")
}

// TestDIGenerationCeilingRefusal is FR-048, and the task that claimed it was
// ticked with NOTHING behind it: `go test -run TestDIGeneration` answered "no
// tests to run" and no commit in this repository mentions the FR. The refusal
// was built on the RUNTIME side (abi.ts aboveCeilingRefusal) and the plan was
// never corrected to say so.
//
// What was genuinely missing is the knowledge arriving before the push. The
// runtime published its ceiling; palsvc dropped the field on the floor; so no
// caller up the chain could read it and the admission gate stayed one-way.
func TestDIGenerationCeilingRefusal(t *testing.T) {
	two, three := 2, 3

	t.Run("a bundle above the stack's ceiling is refused, naming both numbers", func(t *testing.T) {
		why := pushCeilingRefusal(&three, &two)
		require.NotEmpty(t, why)
		require.Contains(t, why, "generation 3")
		require.Contains(t, why, "at most 2")
		// The fix, not just the fault: a refusal that does not say what to type
		// next costs the reader the next hour.
		require.Contains(t, why, "matching runtime version")
		// And WHY it matters — an operator weighing whether to force it needs to
		// know the failure is silent, not loud.
		require.Contains(t, why, "undefined")
	})

	t.Run("a bundle at or under the ceiling passes", func(t *testing.T) {
		require.Empty(t, pushCeilingRefusal(&two, &two))
		require.Empty(t, pushCeilingRefusal(&two, &three))
		require.Empty(t, pushCeilingRefusal(&three, &three))
	})

	// ABSENCE IS AN ANSWER, and reading it as "unknown, refuse" would refuse
	// every push to every image in the fleet today — not a gate but an outage.
	// The field arrived with rung 3, so an image that does not publish it was
	// built before rung 3 existed.
	t.Run("a stack that publishes no ceiling is read as rung 2", func(t *testing.T) {
		require.Empty(t, pushCeilingRefusal(&two, nil), "rung 2 fits an old image")
		require.NotEmpty(t, pushCeilingRefusal(&three, nil), "rung 3 does not")
	})

	// A bundle that reaches NO rung is a FLOOR problem, and the floor check at
	// boot owns it by name. Blaming the ceiling would send the reader to the
	// wrong fix.
	t.Run("a bundle that reaches no rung is not this gate's fault to report", func(t *testing.T) {
		require.Empty(t, pushCeilingRefusal(nil, &two))
		require.Empty(t, pushCeilingRefusal(nil, nil))
	})
}

// TEŞHİSİ SAKLAYAN TEŞHİS KAPISI — canlıda ölçüldü 03.09.2026.
//
// `output` yerel bir aletin stderr'ini `trimBody` ile 300 karaktere kırpıyordu.
// 300 karakter bir HTTP gövdesi için doğru sınır; bir derleyici hatası için
// yanlış: bun'ın çıktısı kod ÇERÇEVESİYLE başlar ve asıl cümle sonda gelir, yani
// kırpma tam olarak okunması gereken yeri atıyordu. `palbase push` günlerce
// "the bundle could not be inspected:" deyip ardına anlamsız bir parça
// yapıştırdı; kontrol düzleminin push'u 02.09'da dört kez bununla düştü ve İKİ
// teşhis birden yanlış çıktı.
func TestToolDiagnosticIsNotTruncatedToAnHTTPBody(t *testing.T) {
	long := strings.Repeat("x", 1200) + "SON-CÜMLE-BURADA"

	kept := trimDiagnostic([]byte(long))
	require.Contains(t, kept, "SON-CÜMLE-BURADA",
		"aletin asıl hata cümlesi kırpılmış — çerçeve kalıp teşhis gitmiş")

	// SINIRSIZ DEĞİL: kaçak bir alet megabaytlarca dökebilir ve terminali
	// doldurmak da bir tür saklamaktır.
	require.LessOrEqual(t, len(trimDiagnostic([]byte(strings.Repeat("y", 100000)))), 8200)
}

// Ve kapı, bir gün birinin onu tekrar HTTP kırpıcısına bağlamasını tutar.
func TestBundleInspectionDoesNotUseTheHTTPTrimmer(t *testing.T) {
	src, err := os.ReadFile("stack_bundle.go")
	require.NoError(t, err)
	body := string(src)
	start := strings.Index(body, "func output(")
	require.NotEqual(t, -1, start)
	fn := body[start : start+600]
	require.NotContains(t, fn, "trimBody(",
		"output yine HTTP gövde kırpıcısını kullanıyor — alet hatası 300 karakterde kesilir")
}

// TestAPushEvaluatesTheSchemaItShips is the 2026-09-04 outage, pinned at the
// door it came through.
//
// `db/` was READ by the bundler and never RUN: a schema whose two foreign keys
// to one table resolve to the same reverse relation name shipped, applied its
// DDL, and then refused at BOOT inside `setSchema` — where the cure is a push
// and the exit takes down palsvc, the process that accepts one. The gate
// existed the whole time, in `palbase build`, a command nobody is required to
// run before pushing.
//
// Live precedent 2026-08-30: exactly this schema pushed successfully and
// reported `created table uat_two_fks`.
//
// It asserts the COLUMNS are named. A refusal that says "your schema is wrong"
// sends somebody hunting through every table; this one has to point at the two
// it means.
func TestAPushEvaluatesTheSchemaItShips(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	buildableBackend(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Two FKs to one table with no `reverseAs` — the reverse of both is "trips".
	ambiguous := `import { defineSchema, defineTable, uuid, text } from "@palbase/backend";

const airports = defineTable("airports", {
  columns: { id: uuid(), name: text() },
  primaryKey: ["id"],
  rls: true,
});

const trips = defineTable("trips", {
  columns: {
    id: uuid(),
    from_airport: uuid().references(() => airports.id),
    to_airport: uuid().references(() => airports.id),
  },
  primaryKey: ["id"],
  rls: true,
});

export default defineSchema("public", { tables: [airports, trips] });
`
	if err := os.WriteFile(filepath.Join(dir, "db", "public.ts"), []byte(ambiguous), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	_, _, err := buildStackArtifact(context.Background(), dir, t.TempDir(), &out)
	if err == nil {
		t.Fatal("a schema that cannot form its relation graph was BUNDLED — it would refuse at boot, " +
			"and the pod that refuses cannot accept the push that fixes it")
	}
	for _, want := range []string{"from_airport", "to_airport", "reverseAs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q — a person cannot act on it:\n%v", want, err)
		}
	}
}

// TestAPushAcceptsANAMEDPairOfForeignKeys is the negative control, in the same
// conditions: the gate must refuse AMBIGUITY, not two foreign keys.
//
// Without this, narrowing the gate to "any table with two FKs" would look
// identical from the test above and would break every legitimate schema.
func TestAPushAcceptsANAMEDPairOfForeignKeys(t *testing.T) {
	requiresRealToolchain(t)
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	buildableBackend(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	named := `import { defineSchema, defineTable, uuid, text } from "@palbase/backend";

const airports = defineTable("airports", {
  columns: { id: uuid(), name: text() },
  primaryKey: ["id"],
  rls: true,
});

const trips = defineTable("trips", {
  columns: {
    id: uuid(),
    from_airport: uuid().references(() => airports.id, { reverseAs: "departures" }),
    to_airport: uuid().references(() => airports.id, { reverseAs: "arrivals" }),
  },
  primaryKey: ["id"],
  rls: true,
});

export default defineSchema("public", { tables: [airports, trips] });
`
	if err := os.WriteFile(filepath.Join(dir, "db", "public.ts"), []byte(named), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if _, _, err := buildStackArtifact(context.Background(), dir, t.TempDir(), &out); err != nil {
		t.Fatalf("a LEGITIMATE pair of named foreign keys was refused: %v", err)
	}
}

// ÜRÜN MÜŞTERİNİN CHECKOUT'UNA YAZILMAZ — TEST PAKETLERİ DE DAHİL.
//
// 0.61.1'de her derleme ürünü geçici bir bundle köküne taşındı ve 0.61.4 bunu
// "CLI artık müşterinin projesine yazmıyor" diye duyurdu. BİR üretici geride
// kaldı: `bundleTests` çıktısını `dir` (checkout) altına yazıyordu, oysa aynı
// fonksiyonun içindeki diğer her çıktı `bundleRoot`a gidiyor.
//
// ÖLÇÜLDÜ 11.09.2026, centauri-backdoor: `.palbase/` silinip commit'lendi
// (22:01), `palbase link` ondan sonra GEÇTİ, ve `palbase plan` 22:34'te
// `.palbase/esm/tests` altına 13 MB geri yazdı.
//
// Bedeli iki katlı. Müşteri istemediği bir derleme ağacını geri alıyor — ve
// daha kötüsü, `palbase link` `.palbase` taşıyan bir checkout'u REDDEDİYOR
// ("this checkout still carries the retired layout"). Yani `push`, `link`'in
// engel saydığı şeyi ÜRETİYOR: alet kendi kendisiyle kavga ediyor.
func TestTheTestBundlerWritesToTheBundleRootNotTheCheckout(t *testing.T) {
	requiresRealToolchain(t)

	dir := t.TempDir()
	bundleRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	suite := "export function noop() { return 1 }\n"
	if err := os.WriteFile(filepath.Join(dir, "tests", "a.e2e.test.ts"), []byte(suite), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := bundleTests(context.Background(), dir, bundleRoot, deploySelection{}, &strings.Builder{}); err != nil {
		t.Fatalf("bundleTests: %v", err)
	}

	// (1) Ürün BUNDLE KÖKÜNDE — yoksa süit artifact'la seyahat etmez ve
	// deploy'un koştuğu testler sessizce hiç olmaz.
	if _, err := os.Stat(filepath.Join(bundleRoot, ".palbase", "esm", "tests", "a.e2e.test.js")); err != nil {
		t.Errorf("suite did not land in the bundle root: %v", err)
	}

	// (2) Ve checkout'ta HİÇBİR ŞEY yok. `link`in engel saydığı dizin burada
	// oluşmamalı.
	if _, err := os.Stat(filepath.Join(dir, ".palbase")); err == nil {
		t.Error("the bundler wrote .palbase into the customer's checkout — " +
			"`palbase link` refuses a checkout that carries it, so push would " +
			"create the blocker link rejects")
	}
}

// DEPLOY'A AİT SÜİTLER, KİRACININ BÜTÜN TEST KORPUSU DEĞİL (FR-017, D-40).
//
// İlk yazımı projedeki HER `*.test.*`'ı topluyordu ve ölçüldü: kontrol düzlemi
// sunucusunun 160 CI süiti deploy'da runtime'ın 384Mi'lik bellek zarfını
// doldurup konteyneri `OOMKilled` ile düşürdü (tek başına en ağır süit 425 MB);
// o kiracı hiç deploy edemez oldu. Deploy'un koştuğu test ile yerelde koşan
// birim testi ayrı kümelerdir — şablonun kendi ayrımı da budur
// (`notes.e2e.test.ts` yayına çıkacak release'e karşı HTTP konuşur,
// `note.service.test.ts` bir stand-in ile mantığı ölçer).
//
// tests/ is no longer sent whole (FR-001, palgroup/palbase#13): a unit test kept
// there ran in the deploy like any other, and 86 of one tenant's 87 were not
// deploy suites.
func TestCollectTakesOnlyTheDeploysOwnSuites(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{
		"modules/notes/notes.e2e.test.ts",
		"modules/notes/note.service.test.ts",
		"modules/shared/canon.test.ts",
		"tests/health.test.ts",
		"tests/deep/isolation.test.ts",
		"db/public.test.ts",
	} {
		mustWrite(t, dir, rel, oneTestSuite)
	}

	got, err := collectTestSources(dir, deploySelection{})
	require.NoError(t, err)
	var rels []string
	for _, p := range got {
		rel, relErr := filepath.Rel(dir, p)
		require.NoError(t, relErr)
		rels = append(rels, filepath.ToSlash(rel))
	}
	sort.Strings(rels)
	require.Equal(t, []string{
		"modules/notes/notes.e2e.test.ts",
	}, rels, "deploy kiracının birim testlerini de topladı (FR-001)")
}

// ONARIM TABLOSU BOŞ OLAMAZ (#151).
//
// Giriş modülü, bundler'ın tekilleştirme sayacının yayımlanan operation id'lere
// sızmasını engelleyen bir onarım taşıyor: kaynaktaki sınıf adlarını gömüyor ve
// bundle'ın verdiği `Foo2` gibi adları geri düzeltiyor. Blok yazılmıştı ve
// FAIL-CLOSED'dı — ama HER AĞAÇTA ÖLÜYDÜ.
//
// Sebep: girdisi `sources`tı ve `sources` bir noktada `*.module.ts` listesi
// oldu. Module dosyası `@Controller` sınıfı BİLDİRMEZ — sınıf kendi
// `*.controller.ts` dosyasında yaşar. Ölçüldü 2026-09-16: bu depoda 28
// `*.module.ts` var, `@Controller` taşıyan SIFIR. Yani tablo boş dönüyor, blok
// hiç emit edilmiyor ve hiçbir test kırmızıya dönmüyordu — çünkü kapının
// konusu boş bir listeye dönüşmüştü ve boş liste hiçbir şeyle eşleşmez.
//
// Bu kapı tam olarak o sessizliği ölçüyor: VARLIK değil, DOLULUK.
func TestTheNameRepairTableIsNeverEmptyOnATreeWithControllers(t *testing.T) {
	dir := writeRealModuleTree(t, "PalaiController", "PalaiController")

	sources, err := moduleSources(dir)
	require.NoError(t, err)
	require.NotEmpty(t, sources, "fikstür bir *.module.ts yazmadı — test kendi öncülünü kaybetti")

	entry, err := bundleEntry(dir, sources)
	require.NoError(t, err)

	require.Contains(t, entry, "__want",
		"giriş modülü ad onarımını hiç emit etmiyor — bundler'ın sayacı sözleşmeye sızar")
	require.Contains(t, entry, "PalaiController",
		"onarım tablosu BOŞ: kaynakta bildirilen sınıf adı tabloya girmemiş. "+
			"Tablo `*.module.ts` listesinden doldurulursa her ağaçta boş kalır — "+
			"module dosyaları @Controller sınıfı bildirmez.")
}

// İKİ SINIF TEK AD, GERÇEK MODULE YÜRÜYÜŞÜNDEN.
//
// `TestTheBundlerDoesNotGetToNameThePublicAPI` de iki-sınıf-tek-ad kuruyor ama
// `sources`a controller dosyalarını DOĞRUDAN veriyor — yani üretimde hiç
// olmayan bir şekil. O yüzden bugün yeşil ve aradığımız kırmızı o değil. Bu
// test ağacı üretimdeki gibi kuruyor: `*.module.ts` dosyaları ve onların
// import ettiği `*.controller.ts` dosyaları.
//
// `--keep-names` AÇIKKEN koşuyor. Ölçüldü (bun 1.3.9): bayrak bu çakışmayı
// KAPSAMIYOR — bayraklı ve bayraksız çıktı byte-identical ve ikisinde de
// `PalaiController2` çıkıyor. Testin bayrakla koşması, bunu bir yorum iddiası
// olmaktan çıkarıp süitin kendi çıktısı yapıyor.
func TestTwoClassesOneNameSurviveTheRealModuleWalk(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not installed")
	}
	dir := writeRealModuleTree(t, "PalaiController", "PalaiController")

	sources, err := moduleSources(dir)
	require.NoError(t, err)

	body, err := bundleEntry(dir, sources)
	require.NoError(t, err)
	entry := filepath.Join(dir, ".controllers-entry.ts")
	require.NoError(t, os.WriteFile(entry, []byte(body), 0o644))

	out := filepath.Join(dir, "bundle.js")
	build := exec.Command("bun", "build", entry, "--target=bun", "--format=esm", "--keep-names", "--outfile="+out)
	build.Dir = dir
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bundle failed: %v\n%s", err, b)
	}

	read := exec.Command("bun", "-e",
		`const m = await import(process.argv[1]); console.log(m.getRegisteredControllers().map(c => c.name).join(","));`,
		out)
	read.Dir = dir
	got, err := read.CombinedOutput()
	if err != nil {
		t.Fatalf("could not read the bundle: %v\n%s", err, got)
	}
	names := strings.TrimSpace(string(got))
	if names != "PalaiController,PalaiController" {
		t.Errorf("gerçek module yürüyüşünden geçen ağaçta bundler adı DEĞİŞTİRDİ: %q. "+
			"Yayımlanan namespace bundler'ın tekilleştirme sayacından gelirse, bir dosya "+
			"eklemek her istemcinin üretilen kodunu kaydırır.", names)
	}
}

// KAYNAKTA GERÇEKTEN RAKAMLA BİTEN BİR AD VARSA ONA DOKUNULMAZ.
//
// Onarım "sondaki rakamları at, kaynakta var mı bak" kuralıyla çalışıyor.
// `Api2Controller` diye bir sınıfı kaynakta yazan bir proje, onun `Api`ye
// çevrilmesini görmemeli — o rakam bundler'ın değil, yazarın.
func TestARealDigitSuffixedNameIsLeftAlone(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not installed")
	}
	dir := writeRealModuleTree(t, "ApiController", "Api2Controller")

	sources, err := moduleSources(dir)
	require.NoError(t, err)
	body, err := bundleEntry(dir, sources)
	require.NoError(t, err)
	entry := filepath.Join(dir, ".controllers-entry.ts")
	require.NoError(t, os.WriteFile(entry, []byte(body), 0o644))

	out := filepath.Join(dir, "bundle.js")
	build := exec.Command("bun", "build", entry, "--target=bun", "--format=esm", "--keep-names", "--outfile="+out)
	build.Dir = dir
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bundle failed: %v\n%s", err, b)
	}
	read := exec.Command("bun", "-e",
		`const m = await import(process.argv[1]); console.log(m.getRegisteredControllers().map(c => c.name).sort().join(","));`,
		out)
	read.Dir = dir
	got, err := read.CombinedOutput()
	if err != nil {
		t.Fatalf("could not read the bundle: %v\n%s", err, got)
	}
	if names := strings.TrimSpace(string(got)); names != "Api2Controller,ApiController" {
		t.Errorf("kaynakta yazılmış bir rakam onarım tarafından silindi: %q — "+
			"beklenen \"Api2Controller,ApiController\"", names)
	}
}

// writeRealModuleTree, ÜRETİMDEKİ şekli kurar: bir `*.module.ts` ve onun
// import ettiği iki `*.controller.ts`. Verilen iki sınıf adı aynı olabilir —
// çakışma fikstürü tam budur.
func writeRealModuleTree(t *testing.T, classA, classB string) string {
	t.Helper()
	dir := t.TempDir()

	sdk := filepath.Join(dir, "node_modules", "@palbase", "backend")
	require.NoError(t, os.MkdirAll(sdk, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sdk, "package.json"),
		[]byte(`{"name":"@palbase/backend","version":"99.0.0","type":"module","main":"index.js"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sdk, "index.js"), []byte(`
const REG = [];
export function Controller() { return (cls) => { REG.push(cls); return cls; }; }
export function Module() { return (cls) => cls; }
export function getRegisteredControllers() { return REG; }
export function __runWithRuntime() {}
export function buildModuleClients() { return {}; }
export const __requestALS = null;
export function __getRuntime() {}
`), 0o644))

	ctl := filepath.Join(dir, "controllers")
	require.NoError(t, os.MkdirAll(ctl, 0o755))
	files := []struct{ file, class, method string }{
		{"a.controller.ts", classA, "alpha"},
		{"b.controller.ts", classB, "beta"},
	}
	for _, f := range files {
		body := `import { Controller } from "@palbase/backend";

@Controller("/palai")
export class ` + f.class + ` { ` + f.method + `() { return "` + f.method + `"; } }
`
		require.NoError(t, os.WriteFile(filepath.Join(ctl, f.file), []byte(body), 0o644))
	}

	// MODULE DOSYASI: üretimde giriş noktası budur ve controller'lar buraya
	// import edilerek bundle'a girer. `@Controller` sınıfı BİLDİRMEZ — onarım
	// tablosunun neden `*.module.ts` listesinden doldurulamayacağının sebebi.
	mods := filepath.Join(dir, "modules")
	require.NoError(t, os.MkdirAll(mods, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "palai.module.ts"), []byte(
		`import { Module } from "@palbase/backend";
import "../controllers/a.controller";
import "../controllers/b.controller";

@Module()
export class PalaiModule {}
`), 0o644))

	return dir
}

// THE GOLDEN FIXTURE (D-6). The same tree, the same package.json variants and
// the same expected names are asserted in palbase's v2/runtime/src/dev.test.ts
// against runtime/scripts/bundle-controllers.sh — the other bundler. A test in
// one repository cannot call the other's selector, so the fixture is the
// contract: change it here, change it there, in the same round.
var deployGoldenTree = map[string]string{
	"modules/notes/notes.e2e.test.ts":               oneTestSuite,
	"modules/notes/note.service.test.ts":            oneTestSuite,
	"modules/billing/notes.e2e.test.ts":             oneTestSuite,
	"tests/health.e2e.test.ts":                      oneTestSuite,
	"tests/health.test.ts":                          oneTestSuite,
	"tests/deep/isolation.test.ts":                  oneTestSuite,
	"tests/deep/helper.ts":                          "export const HELPER = 1;\n",
	"node_modules/dep/dep.e2e.test.ts":              oneTestSuite,
	"dist/old.e2e.test.ts":                          oneTestSuite,
	".palbase-staged-controllers/stale.e2e.test.ts": oneTestSuite,
}

const (
	goldenD2   = `2 test file(s) under tests/ are not deploy suites and were not sent: tests/deep/isolation.test.ts, tests/health.test.ts — name one *.e2e.test.* to send it, or list what the deploy runs in package.json "palbase.deployTests"`
	goldenS1   = `1 test file(s) under tests/ are not in package.json "palbase.deployTests" and were not sent: tests/deep/isolation.test.ts — list them there to send them`
	goldenS2a  = `2 test file(s) under tests/ are not in package.json "palbase.deployTests" and were not sent: tests/deep/isolation.test.ts, tests/health.e2e.test.ts — list them there to send them`
	goldenS2b  = `2 test file(s) under tests/ are not in package.json "palbase.deployTests" and were not sent: tests/deep/isolation.test.ts, tests/health.test.ts — list them there to send them`
	goldenS3   = `3 test file(s) under tests/ are not in package.json "palbase.deployTests" and were not sent: tests/deep/isolation.test.ts, tests/health.e2e.test.ts, tests/health.test.ts — list them there to send them`
	goldenNone = `bundled 0 deploy suite(s): "palbase.deployTests" in package.json matched no test file`
)

var deployGoldenCases = []struct {
	name, pkg string
	suites    []string
	zero      bool
	note      string
}{
	{"V0 no package.json", "", []string{"health.e2e.test.js", "notes.e2e.test.js", "notes_notes.e2e.test.js"}, false, goldenD2},
	{"V1 no palbase key", `{"name":"golden","private":true,"type":"module"}`, []string{"health.e2e.test.js", "notes.e2e.test.js", "notes_notes.e2e.test.js"}, false, goldenD2},
	{"V2 a directory", `{"palbase":{"deployTests":["tests/"]}}`, []string{"health.e2e.test.js", "health.test.js", "isolation.test.js"}, false, ""},
	{"V3 a star and a path", `{"palbase":{"deployTests":["modules/*/notes.e2e.test.ts","tests/health.test.ts"]}}`, []string{"health.test.js", "notes.e2e.test.js", "notes_notes.e2e.test.js"}, false, goldenS2a},
	{"V4 a question mark", `{"palbase":{"deployTests":["tests/health.e?e.test.ts"]}}`, []string{"health.e2e.test.js"}, false, goldenS2b},
	{"V5 a bare name is not a path", `{"palbase":{"deployTests":["health.e2e.test.ts"]}}`, nil, true, goldenS3},
	{"V6 an empty list sends nothing", `{"palbase":{"deployTests":[]}}`, nil, true, goldenS3},
	{"V7 a star stops at a slash", `{"palbase":{"deployTests":["tests/*.test.ts"]}}`, []string{"health.e2e.test.js", "health.test.js"}, false, goldenS1},
	{"V8 palbase that is not an object", `{"palbase":"x"}`, []string{"health.e2e.test.js", "notes.e2e.test.js", "notes_notes.e2e.test.js"}, false, goldenD2},
	{"V9 a byte-order mark", "\ufeff" + `{"palbase":{"deployTests":["tests/"]}}`, []string{"health.e2e.test.js", "health.test.js", "isolation.test.js"}, false, ""},
}

var deployGoldenRefusals = []struct{ name, pkg, says string }{
	{"R1 unparseable", `{`, `package.json could not be read as JSON`},
	{"R2 a string", `{"palbase":{"deployTests":"tests/"}}`, `package.json: "palbase.deployTests" must be an array of paths`},
	{"R3 null", `{"palbase":{"deployTests":null}}`, `package.json: "palbase.deployTests" must be an array of paths`},
	{"R4 a number entry", `{"palbase":{"deployTests":["tests/",3]}}`, `package.json: "palbase.deployTests"[1] (3) must be a non-empty string`},
	{"R5 an empty entry", `{"palbase":{"deployTests":[""]}}`, `package.json: "palbase.deployTests"[0] ("") must be a non-empty string`},
	{"R6 double star", `{"palbase":{"deployTests":["tests/**"]}}`, `package.json: "palbase.deployTests"[0] ("tests/**") uses "**"`},
	{"R7 brackets", `{"palbase":{"deployTests":["tests/[ab].test.ts"]}}`, `package.json: "palbase.deployTests"[0] ("tests/[ab].test.ts") uses "["`},
	{"R8 braces", `{"palbase":{"deployTests":["{a,b}.e2e.test.ts"]}}`, `package.json: "palbase.deployTests"[0] ("{a,b}.e2e.test.ts") uses "{"`},
	{"R9 a backslash", `{"palbase":{"deployTests":["tests\\health.test.ts"]}}`, `package.json: "palbase.deployTests"[0] ("tests\\health.test.ts") uses "\\"`},
	{"R10 a dot segment", `{"palbase":{"deployTests":["./tests/"]}}`, `package.json: "palbase.deployTests"[0] ("./tests/") is not a plain path from the project root`},
	{"R11 a leading slash", `{"palbase":{"deployTests":["/tests/"]}}`, `package.json: "palbase.deployTests"[0] ("/tests/") is not a plain path from the project root`},
	{"R12 an empty segment", `{"palbase":{"deployTests":["tests//"]}}`, `package.json: "palbase.deployTests"[0] ("tests//") is not a plain path from the project root`},
	{"R13 a pattern in a directory entry", `{"palbase":{"deployTests":["modules/*/"]}}`, `package.json: "palbase.deployTests"[0] ("modules/*/") ends with "/", so it names a directory as written`},
}

func writeDeployGolden(t *testing.T, pkg string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range deployGoldenTree {
		mustWrite(t, dir, rel, body)
	}
	if pkg != "" {
		mustWrite(t, dir, "package.json", pkg)
	}
	return dir
}

// FR-001, FR-002, FR-004 — the selection, its names, and what it says it left behind.
func TestDeploySelectionGoldenFixture(t *testing.T) {
	for _, c := range deployGoldenCases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeDeployGolden(t, c.pkg)
			sel, err := readDeploySelection(dir)
			require.NoError(t, err)
			suites, err := planTestSuites(dir, sel)
			require.NoError(t, err)
			names := make([]string, 0, len(suites))
			for _, s := range suites {
				names = append(names, s.Out)
			}
			sort.Strings(names)
			if len(c.suites) == 0 {
				require.Empty(t, names)
			} else {
				require.Equal(t, c.suites, names)
			}
			unsent, err := unsentUnderTests(dir, sel)
			require.NoError(t, err)
			want := ""
			if c.note != "" {
				want = "note: " + c.note
			}
			require.Equal(t, want, deployNote(unsent, sel))
		})
	}
}

// …and the lines a push prints, through the real bundler (bun builds each suite).
func TestDeploySelectionGoldenFixtureBundles(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is what bundles a suite")
	}
	for _, c := range deployGoldenCases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeDeployGolden(t, c.pkg)
			sel, err := readDeploySelection(dir)
			require.NoError(t, err)
			bundleRoot := t.TempDir()
			var out strings.Builder
			require.NoError(t, bundleTests(context.Background(), dir, bundleRoot, sel, &out))
			entries, _ := os.ReadDir(filepath.Join(bundleRoot, ".palbase", "esm", "tests"))
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			if len(c.suites) == 0 {
				require.Empty(t, names)
			} else {
				require.Equal(t, c.suites, names)
				require.Contains(t, out.String(), fmt.Sprintf("bundled %d test suite(s)\n", len(c.suites)))
			}
			if c.zero {
				require.Contains(t, out.String(), goldenNone+"\n")
			} else {
				require.NotContains(t, out.String(), "0 deploy suite(s)")
			}
			if c.note != "" {
				require.Contains(t, out.String(), "note: "+c.note+"\n")
			} else {
				require.NotContains(t, out.String(), "note:")
			}
		})
	}
}

// FR-003 — a selection this bundler cannot read is refused, naming file, field and entry.
func TestDeploySelectionRefusals(t *testing.T) {
	for _, r := range deployGoldenRefusals {
		t.Run(r.name, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, dir, "package.json", r.pkg)
			_, err := readDeploySelection(dir)
			require.Error(t, err)
			require.Contains(t, err.Error(), r.says)
		})
	}
}

// FR-003 — "before bundling starts": the refusal arrives before anything is compiled or written.
func TestABadSelectionIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "package.json", `{"palbase":{"deployTests":["tests/**"]}}`)
	bundleRoot := t.TempDir()
	_, _, err := buildStackArtifact(context.Background(), dir, bundleRoot, &strings.Builder{})
	require.Error(t, err)
	require.Contains(t, err.Error(), `"palbase.deployTests"[0] ("tests/**") uses "**"`)
	entries, readErr := os.ReadDir(bundleRoot)
	require.NoError(t, readErr)
	require.Empty(t, entries, "a refused selection left build products behind")
}

// FR-004 — the first three paths, then a count of the rest.
func TestDeployNoteNamesTheFirstThreeAndCountsTheRest(t *testing.T) {
	unsent := []string{"tests/a.test.ts", "tests/b.test.ts", "tests/c.test.ts", "tests/d.test.ts", "tests/e.test.ts"}
	require.Equal(t,
		`note: 5 test file(s) under tests/ are not deploy suites and were not sent: tests/a.test.ts, tests/b.test.ts, tests/c.test.ts, … — name one *.e2e.test.* to send it, or list what the deploy runs in package.json "palbase.deployTests"`,
		deployNote(unsent, deploySelection{}))
	require.Equal(t, "", deployNote(nil, deploySelection{}))
}

// D-7 — the small dialect: `*` and `?` never cross `/`, and a pattern is a WHOLE path.
func TestMatchDeployPattern(t *testing.T) {
	for _, c := range []struct {
		pattern, rel string
		want         bool
	}{
		{"tests/*.test.ts", "tests/a.test.ts", true},
		{"tests/*.test.ts", "tests/deep/a.test.ts", false},
		{"tests/?.test.ts", "tests/a.test.ts", true},
		{"tests/?.test.ts", "tests/ab.test.ts", false},
		{"tests/?.test.ts", "tests//.test.ts", false},
		{"a/*/c.test.ts", "a/b/c.test.ts", true},
		{"*", "a/b", false},
		{"tests/health.test.ts", "health.test.ts", false},
		{"tests/health.test.ts", "tests/health.test.ts", true},
		{"tests/*", "tests/", true},
	} {
		require.Equalf(t, c.want, matchDeployPattern(c.pattern, c.rel), "%q ~ %q", c.pattern, c.rel)
	}
}

// FR-002 THROUGH THE PRODUCTION PATH (review rv-w1-t002 I-1). The golden tests
// call the selector and the bundler directly; this one goes through
// buildStackArtifact, so a build that read "palbase.deployTests" and then threw
// it away — bundling the default instead — fails here. The fixture is chosen so
// the two answers differ: the declared selection sends only tests/unit.test.ts,
// the default would send only the module's e2e suite.
func TestAPushBundlesTheDeclaredSelection(t *testing.T) {
	requiresRealToolchain(t)
	dir := t.TempDir()
	buildableBackend(t, dir)
	pkg := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(pkg)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	doc["palbase"] = map[string]any{"deployTests": []string{"tests/"}}
	edited, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pkg, edited, 0o644))
	mustWrite(t, dir, "tests/unit.test.ts", oneTestSuite)
	mustWrite(t, dir, "modules/x/x.e2e.test.ts", oneTestSuite)

	bundleRoot := t.TempDir()
	var log strings.Builder
	_, _, err = buildStackArtifact(context.Background(), dir, bundleRoot, &log)
	require.NoError(t, err, log.String())

	entries, err := os.ReadDir(filepath.Join(bundleRoot, ".palbase", "esm", "tests"))
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.Equal(t, []string{"unit.test.js"}, names, "the push did not bundle what package.json declared")
	require.Contains(t, log.String(), "bundled 1 test suite(s)\n")
	require.NotContains(t, log.String(), "note:")
}

// FR-018, J-15 — a name that differs from another only by a non-ASCII case is
// ONE name to both bundlers. The collision key is lowered code point by code
// point (Go's strings.ToLower); runtime/scripts/bundle-controllers.sh lowers the
// same way — its JS maps "İ" to "i", which String.prototype.toLowerCase does
// not ("i̇", two code points), so the plain JS call named these two apart.
// Asserted with the same tree in v2/runtime/src/dev.test.ts.
func TestSuiteNamesLowerNonASCIILikeTheOtherBundler(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "modules/a/İade.e2e.test.ts", oneTestSuite)
	mustWrite(t, dir, "modules/b/iade.e2e.test.ts", oneTestSuite)
	suites, err := planTestSuites(dir, deploySelection{})
	require.NoError(t, err)
	var names []string
	for _, s := range suites {
		names = append(names, s.Out)
	}
	sort.Strings(names)
	require.Equal(t, []string{"b_iade.e2e.test.js", "İade.e2e.test.js"}, names)
}
