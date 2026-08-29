package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})

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
	if strings.Contains(bundleEntry(dir, nil), "db/schema.ts") {
		t.Error("a project with no declaration got one imported anyway")
	}

	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "schema.ts"), []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bundleEntry(dir, nil), "export const schema = __schema;") {
		t.Error("a declared schema does not travel with the bundle")
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
	_, err := buildStackArtifact(context.Background(), dir, &strings.Builder{})
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
	_, err := buildStackArtifact(context.Background(), t.TempDir(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "controllers") {
		t.Fatalf("a directory with no controllers/ got %v", err)
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
	if err := os.WriteFile(filepath.Join(dir, "tests", "todos.test.ts"), []byte(
		"import { test } from \"node:test\";\nimport { WHO } from \"./helper.ts\";\ntest(WHO, () => {});\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := bundleTests(context.Background(), dir, &strings.Builder{}); err != nil {
		t.Fatalf("the suite did not bundle: %v", err)
	}

	built := filepath.Join(dir, ".palbase", "esm", "tests", "todos.test.js")
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
	if _, err := os.Stat(filepath.Join(dir, ".palbase", "esm", "tests", "helper.js")); err == nil {
		t.Error("a non-test file was emitted as a suite")
	}
}

func TestAProjectWithNoTestsBundlesNothingAndIsNotRefused(t *testing.T) {
	dir := t.TempDir()
	if err := bundleTests(context.Background(), dir, &strings.Builder{}); err != nil {
		t.Fatalf("a project that declares no tests was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".palbase", "esm", "tests")); err == nil {
		t.Error("an empty tests directory was created for a project with none")
	}
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
func TestWebhooksTravelInTheEntry(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, "webhooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stripe.ts", "shopify.ts", "notes.test.ts"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte("export default class X {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})

	// The NAME is the file's base name, because the file name IS the public URL
	// (the SDK's decorator deliberately offers no `name` option).
	if !strings.Contains(entry, `{ name: "stripe", ctor: __webhook_stripe }`) {
		t.Errorf("stripe webhook missing from the entry:\n%s", entry)
	}
	if !strings.Contains(entry, `{ name: "shopify", ctor: __webhook_shopify }`) {
		t.Errorf("shopify webhook missing from the entry:\n%s", entry)
	}

	// Imported BY NAME, unlike controllers: a webhook class is not registered by
	// its decorator into a global registry, so the entry must carry the ctor.
	if !strings.Contains(entry, `import __webhook_stripe from "`+filepath.Join(hooks, "stripe.ts")+`";`) {
		t.Errorf("the webhook is not imported by name:\n%s", entry)
	}

	// Test files are NOT webhooks. Shipping one would mount a URL nobody meant
	// to publish.
	if strings.Contains(entry, "notes.test.ts") || strings.Contains(entry, `name: "notes.test"`) {
		t.Errorf("a test file was mounted as a webhook:\n%s", entry)
	}
}

// A project with no webhooks/ directory still bundles — and says so explicitly,
// rather than emitting a broken entry.
func TestAProjectWithNoWebhooksStillBundles(t *testing.T) {
	dir := t.TempDir()
	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})
	if !strings.Contains(entry, "export const webhooks = [];") {
		t.Errorf("an empty webhooks export is missing:\n%s", entry)
	}
}

// TestTheBundlerDoesNotGetToNameThePublicAPI.
//
// The dotted operationId's NAMESPACE is derived from the controller class name
// (`extract_meta.js: deriveControllerName` reads `Ctrl.name`), and this entry
// puts every controller into ONE bundle. A bundler must rename duplicate
// top-level identifiers to keep them apart, so a project that declares the same
// class name in two files ships a namespace the BUNDLER chose:
// `PalaiController` and `PalaiController2`.
//
// ÖLÇÜLDÜ 25.08.2026 (palai-cloud, canlı): on bir dosya da `export class
// PalaiController` diyordu ve dağıtılan sözleşme `palai`, `palaiController2`
// … `palaiController11` olarak bölünmüştü. Numaralar bundle SIRASINDAN geliyor,
// yani bir dosya eklemek genel API yüzeyini sessizce yeniden numaralandırır ve
// her istemcinin üretilen kodu kayar. Hiçbir yerde hata görünmüyordu.
//
// Kaynak ne diyorsa yüzey odur: aynı adı taşıyan iki controller AYNI namespace'i
// paylaşır ve gerçek bir çakışma olursa onu zaten operationId kapısı gürültüyle
// reddeder (generator.go).
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
	require.NoError(t, os.WriteFile(entry, []byte(bundleEntry(dir, sources)), 0o644))

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

// TestJobsTravelInTheEntry — the cron half of a deploy, which for a long time
// did not travel at all.
//
// The line this locks used to read `export const jobs = [];`, hardcoded, with
// nothing anywhere filling it. A project could carry four @Job classes, build
// clean, deploy green, and never fire one of them. Measured 2026-08-26 on the
// live tenant `1jhp7jbrm`: `jobs/` held four files, the pushed artifact carried
// `var jobs = []`, `MacScalerJob` was not in the bundle at all, the runtime
// printed no `[runtime] jobs:` line across five boots, and `jobs.job_definitions`
// held ZERO rows while palsvc was running WITH the jobs module mounted. Nothing
// in that chain reported an error — the scheduler simply had nothing to schedule.
//
// The very comment below this line in the generator says the same thing happened
// to `webhooks` and was fixed; `jobs` was left sitting beside it.
//
// SHAPE IS THE CONTRACT: `{ name, job }`, the same pair the palbase repository's
// bundle-controllers.sh emits, because the runtime's collectJobs() reads
// `record.job` and resolves it through getJobConfig.
func TestJobsTravelInTheEntry(t *testing.T) {
	dir := t.TempDir()
	jobs := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mac-scaler.ts", "auto-reload.ts", "notes.test.ts"} {
		if err := os.WriteFile(filepath.Join(jobs, name), []byte("export default class X {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})

	// The NAME is the file's base name: @Job carries no `name` option, and by
	// the time the code is bundled the file is gone.
	if !strings.Contains(entry, `{ name: "mac-scaler", job: __job_mac_scaler }`) {
		t.Errorf("mac-scaler job missing from the entry:\n%s", entry)
	}
	if !strings.Contains(entry, `{ name: "auto-reload", job: __job_auto_reload }`) {
		t.Errorf("auto-reload job missing from the entry:\n%s", entry)
	}
	if !strings.Contains(entry, `import __job_mac_scaler from "`+filepath.Join(jobs, "mac-scaler.ts")+`";`) {
		t.Errorf("the job is not imported by name:\n%s", entry)
	}
	// A test file beside the jobs is not a job. Scheduling one would run a suite
	// on a cron in production.
	if strings.Contains(entry, "notes.test.ts") || strings.Contains(entry, `name: "notes.test"`) {
		t.Errorf("a test file was scheduled as a job:\n%s", entry)
	}
	// The placeholder must be GONE, not merely followed by a real one: two
	// `jobs` exports in one ESM module is a bundle that cannot be imported.
	if strings.Contains(entry, "export const jobs = [];") {
		t.Errorf("the hardcoded empty jobs export survived:\n%s", entry)
	}
}

// A project with no jobs/ directory still bundles, and says so explicitly — the
// runtime tolerates a missing export, but an artifact that never declares the
// shape is one the next reader has to guess about.
func TestAProjectWithNoJobsStillBundles(t *testing.T) {
	dir := t.TempDir()
	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})
	if !strings.Contains(entry, "export const jobs = [];") {
		t.Errorf("an empty jobs export is missing:\n%s", entry)
	}
}

// TestHooksTravelInTheEntry — the same subtraction, one surface over.
//
// The generator emitted NO `hooks` export at all, so `collectHooks(mod)` read
// `undefined` and every @Hook a project declared was dropped by the push that
// claimed to ship it. palbase's own bundle-controllers.sh has emitted them since
// 2026-08-21; this copy of the bundler never learned.
func TestHooksTravelInTheEntry(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"before-signup.ts", "notes.test.ts"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte("export default class X {}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})

	if !strings.Contains(entry, `{ name: "before-signup", ctor: __hook_before_signup }`) {
		t.Errorf("the hook is missing from the entry:\n%s", entry)
	}
	if strings.Contains(entry, "notes.test.ts") {
		t.Errorf("a test file was registered as a hook:\n%s", entry)
	}
}

func TestAProjectWithNoHooksStillBundles(t *testing.T) {
	dir := t.TempDir()
	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})
	if !strings.Contains(entry, "export const hooks = [];") {
		t.Errorf("an empty hooks export is missing:\n%s", entry)
	}
}

// bundleWithDefinitions builds a REAL bun bundle from a project carrying the
// given jobs/webhooks/hooks and returns (projectDir, bundlePath).
//
// A real bundle rather than a hand-written stand-in: the whole defect class this
// file guards is "the generator said one thing and the artifact carried
// another", and only bun can settle that. The stub SDK is the smallest thing
// that answers the entry's imports plus the two resolvers the manifests go
// through.
func bundleWithDefinitions(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not installed")
	}
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}

	write("node_modules/@palbase/backend/package.json",
		`{"name":"@palbase/backend","version":"99.0.0","type":"module","main":"index.js"}`)
	write("node_modules/@palbase/backend/index.js", `
const REG = [];
const JOB = Symbol.for("palbase.backend.jobMeta");
const HOOK = Symbol.for("palbase.backend.hookMeta");
export function Controller() { return (cls) => { REG.push(cls); return cls; }; }
export function getRegisteredControllers() { return REG; }
export function Job(o) { return (cls) => { cls[JOB] = o; return cls; }; }
export function Hook(o) { return (cls) => { cls[HOOK] = o; return cls; }; }
export function getJobConfig(c) {
  const m = c[JOB];
  if (!m) throw new Error("no @Job decorator");
  return { schedule: m.schedule, timeout: m.timeout ?? 60, retry: m.retry ?? 0 };
}
export function getHookConfig(c) {
  const m = c[HOOK];
  if (!m) throw new Error("no @Hook decorator");
  return { blocking: m.blocking ?? {}, listeners: m.listeners ?? {} };
}
export function __runWithRuntime() {}
export function buildModuleClients() { return {}; }
export const __requestALS = null;
export function __getRuntime() {}
`)
	write("controllers/todo.controller.ts",
		`import { Controller } from "@palbase/backend";
@Controller("/todos") export default class TodoController { list() { return []; } }`)
	for rel, body := range files {
		write(rel, body)
	}

	entry := filepath.Join(dir, ".controllers-entry.ts")
	require.NoError(t, os.WriteFile(entry,
		[]byte(bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})), 0o644))

	out := filepath.Join(dir, ".palbase", "esm", "controllers", "controllers.js")
	require.NoError(t, os.MkdirAll(filepath.Dir(out), 0o755))
	build := exec.Command("bun", "build", entry, "--target=bun", "--format=esm", "--outfile="+out)
	build.Dir = dir
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bundle failed: %v\n%s", err, b)
	}
	return dir, out
}

func jobSource(schedule string) string {
	return `import { Job } from "@palbase/backend";
@Job({ schedule: "` + schedule + `", timeout: 60 })
export default class J { async run() {} }`
}

// TestTheJobManifestIsWrittenFromTheBundle — the half palsvc actually schedules
// from.
//
// The bundle carries the CODE; this file carries the fact Go cannot compute
// without executing TypeScript: when each job is due. `palbase push` wrote
// neither, so a tenant with four @Job classes had zero rows in
// `jobs.job_definitions` and the scheduler had nothing to fire (measured
// 2026-08-26, tenant `1jhp7jbrm`).
func TestTheJobManifestIsWrittenFromTheBundle(t *testing.T) {
	dir, bundle := bundleWithDefinitions(t, map[string]string{
		"jobs/mac-scaler.ts":  jobSource("* * * * *"),
		"jobs/auto-reload.ts": jobSource("0 3 * * *"),
	})

	require.NoError(t, checkDefinitionsSurvived(context.Background(), dir, bundle))
	require.NoError(t, writeDefinitionManifests(context.Background(), dir, bundle, &strings.Builder{}))

	blob, err := os.ReadFile(filepath.Join(dir, ".palbase", "jobs", "jobs.manifest.json"))
	require.NoError(t, err, "palsvc has no cron to read without this file")

	var manifest struct {
		Jobs []struct {
			Name     string `json:"name"`
			Schedule string `json:"schedule"`
			Timeout  int    `json:"timeout"`
			Retry    int    `json:"retry"`
			File     string `json:"file"`
		} `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(blob, &manifest))
	require.Len(t, manifest.Jobs, 2)

	// Sorted by name, so two builds of one tree produce the same bytes.
	assert.Equal(t, "auto-reload", manifest.Jobs[0].Name)
	assert.Equal(t, "0 3 * * *", manifest.Jobs[0].Schedule)
	assert.Equal(t, "jobs/auto-reload.ts", manifest.Jobs[0].File)
	assert.Equal(t, "mac-scaler", manifest.Jobs[1].Name)
	assert.Equal(t, "* * * * *", manifest.Jobs[1].Schedule)
	assert.Equal(t, 60, manifest.Jobs[1].Timeout)
}

// TestTheHookManifestIsWrittenFromTheBundle: one row per EVENT, carrying
// whether it blocks — that is what palsvc registers from.
func TestTheHookManifestIsWrittenFromTheBundle(t *testing.T) {
	dir, bundle := bundleWithDefinitions(t, map[string]string{
		"hooks/signup.ts": `import { Hook } from "@palbase/backend";
@Hook({ blocking: { "auth.before_signup": async () => {} }, listeners: { "auth.after_login": async () => {} } })
export default class H {}`,
	})

	require.NoError(t, writeDefinitionManifests(context.Background(), dir, bundle, &strings.Builder{}))

	blob, err := os.ReadFile(filepath.Join(dir, ".palbase", "hooks", "hooks.manifest.json"))
	require.NoError(t, err)
	var manifest struct {
		Hooks []struct {
			Event    string `json:"event"`
			Blocking bool   `json:"blocking"`
			File     string `json:"file"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(blob, &manifest))
	require.Len(t, manifest.Hooks, 2)
	assert.Equal(t, "auth.after_login", manifest.Hooks[0].Event)
	assert.False(t, manifest.Hooks[0].Blocking)
	assert.Equal(t, "auth.before_signup", manifest.Hooks[1].Event)
	assert.True(t, manifest.Hooks[1].Blocking)
}

// TestAProjectThatDroppedItsLastJobStopsDeclaringOne.
//
// The removal matters as much as the write: palsvc prunes every definition when
// an artifact carries NO manifest, so a stale file from an earlier build would
// keep a deleted job firing on a schedule the source no longer contains.
func TestAProjectThatDroppedItsLastJobStopsDeclaringOne(t *testing.T) {
	dir, bundle := bundleWithDefinitions(t, nil)
	stale := filepath.Join(dir, ".palbase", "jobs", "jobs.manifest.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	require.NoError(t, os.WriteFile(stale,
		[]byte(`{"jobs":[{"name":"deleted","schedule":"* * * * *","timeout":60,"retry":0,"file":"jobs/deleted.ts"}]}`), 0o644))

	require.NoError(t, writeDefinitionManifests(context.Background(), dir, bundle, &strings.Builder{}))

	_, err := os.Stat(stale)
	assert.True(t, os.IsNotExist(err),
		"a project with no jobs/ still declared one — the deleted job would keep running")
}

// TestABundleThatDroppedItsJobsIsRefused — THE GATE THAT DID NOT EXIST.
//
// The stack's push-activation gate asks whether ENDPOINTS are served. A job, a
// webhook and a hook are all invisible to it, which is how four @Job classes
// deployed green for weeks and never ran. Here the directory is compared against
// the built bundle, while the author is still watching.
func TestABundleThatDroppedItsJobsIsRefused(t *testing.T) {
	dir, bundle := bundleWithDefinitions(t, nil)
	// The bundle is already built and carries no jobs; the directory appears
	// afterwards, which is exactly the shape of "the generator did not look".
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "jobs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "jobs", "nightly.ts"), []byte(jobSource("0 3 * * *")), 0o644))

	err := checkDefinitionsSurvived(context.Background(), dir, bundle)
	require.Error(t, err, "a bundle that carries none of the project's jobs must not ship")
	assert.Contains(t, err.Error(), "jobs/")
	assert.Contains(t, err.Error(), "ZERO")
}

// A job whose @Job is missing or invalid stops the build with the FILE named,
// rather than deploying a class the scheduler will reject at activation.
func TestAJobTheSDKCannotResolveStopsTheBuild(t *testing.T) {
	dir, bundle := bundleWithDefinitions(t, map[string]string{
		"jobs/broken.ts": `export default class J { async run() {} }`,
	})

	err := writeDefinitionManifests(context.Background(), dir, bundle, &strings.Builder{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jobs/broken.ts")
}

// A PROJECT'S CHANNELS HAVE TO REACH THE RUNTIME, and the entry is the only
// thing that can carry them.
//
// `defineChannels` anchors its declaration on a globalThis symbol as it is
// evaluated, and the runtime reads the bundle's `channels` EXPORT first,
// falling back to that symbol. Neither exists unless the entry imports the
// file: nothing else in a project references channels.ts, so a bundle built
// without this line does not contain the declaration at all.
//
// What that costs is not a missing feature. Undeclared channels are REFUSED —
// fail-closed, by design — so a project whose channels.ts never shipped has
// every join answered `unauthorized`, with the file sitting in plain sight and
// the deploy reporting success. This is `jobs = []` again (fixed 2026-08-26),
// and it cost the same silence.
//
// Measured 2026-08-28 on the shipped binary: `channels` appeared nowhere in
// this package.
func TestTheEntryShipsTheProjectsChannels(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "channels.ts"),
		[]byte(`import { defineChannels, ownerOnly } from "@palbase/backend";
export default defineChannels({ "user:{uid}": ownerOnly() });`), 0o644))

	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})

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
// like db/schema.ts. An entry that named it unconditionally would fail to build
// for every project that does not use realtime.
func TestAProjectWithoutChannelsStillBundles(t *testing.T) {
	dir := t.TempDir()
	entry := bundleEntry(dir, []string{filepath.Join(dir, "controllers", "todo.controller.ts")})
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
	entry := bundleEntry(t.TempDir(), nil)
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
