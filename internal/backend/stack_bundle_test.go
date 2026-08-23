package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(entry, `export { __runWithRuntime, __requestALS, __getRuntime } from "@palbase/backend";`) {
		t.Errorf("the entry does not re-export the SDK's request-scope hooks:\n%s", entry)
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
// A package.json written the documented way installs the OLDER major, and the
// bundle then fails deep inside bun with `var controllers = undefined()` — a
// message about neither the version nor the problem. Measured 2026-08-21
// against a real project.
func TestCheckSDKVersionRefusesAMajorTheCLICannotBundle(t *testing.T) {
	dir := t.TempDir()
	writeSDK(t, dir, "17.4.0")

	err := checkSDKVersion(dir)
	if err == nil {
		t.Fatal("an SDK this CLI cannot bundle was accepted")
	}
	for _, want := range []string{"17.4.0", "18", "npm install @palbase/backend@18", "latest"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the message does not say %q:\n%s", want, err)
		}
	}
}

func TestCheckSDKVersionAcceptsTheSupportedMajor(t *testing.T) {
	for _, v := range []string{"18.0.0", "18.0.1", "19.2.3"} {
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
	if len(got) != 2 || got[0] != "avatars" || got[1] != "receipts" {
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
