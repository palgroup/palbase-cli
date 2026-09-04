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
	_, _, err := buildStackArtifact(context.Background(), dir, &strings.Builder{})
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
	_, _, err := buildStackArtifact(context.Background(), t.TempDir(), &strings.Builder{})
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

// TestTheJobManifestIsWrittenFromTheBundle — the half palsvc actually schedules
// from.
//
// The bundle carries the CODE; this file carries the fact Go cannot compute
// without executing TypeScript: when each job is due. `palbase push` wrote
// neither, so a tenant with four @Job classes had zero rows in
// `jobs.job_definitions` and the scheduler had nothing to fire (measured
// 2026-08-26, tenant `1jhp7jbrm`).
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
		require.Contains(t, why, "palbase upgrade")
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
