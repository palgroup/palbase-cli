package backend

import (
	"context"
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
	err := buildStackArtifact(context.Background(), dir, &strings.Builder{})
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
	err := buildStackArtifact(context.Background(), t.TempDir(), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "controllers") {
		t.Fatalf("a directory with no controllers/ got %v", err)
	}
}
