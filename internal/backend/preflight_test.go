package backend

import (
	"strings"
	"testing"
)

// preflightImports must fail fast (before upload) when an endpoint imports a
// relative path whose top-level dir is NOT in the bundle allow-list — i.e. a
// dir that will be silently dropped, then break the server-side esbuild with
// "Could not resolve". This is the schema/ drop that left test0r8q3m's
// .palbase/endpoints empty (every endpoint 500ing).
func TestPreflightImports_MissingImportedRootFails(t *testing.T) {
	root := t.TempDir()
	// Endpoint imports schema/, but the project has NO schema/ dir at all.
	mustWrite(t, root, "endpoints/todos/remove/post.ts",
		`import { appSchema } from "../../../schema/index.js";
export default {};`)
	mustWrite(t, root, "package.json", `{"name":"x"}`)

	err := preflightImports(root)
	if err == nil {
		t.Fatal("expected preflight error for missing schema/ import, got nil")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Fatalf("error should name the missing dir 'schema', got: %v", err)
	}
	if !strings.Contains(err.Error(), "remove/post.ts") {
		t.Fatalf("error should name the offending endpoint file, got: %v", err)
	}
}

// When the imported dir exists (and is allow-listed), preflight passes.
func TestPreflightImports_PresentImportPasses(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "endpoints/todos/remove/post.ts",
		`import { appSchema } from "../../../schema/index.js";
export default {};`)
	mustWrite(t, root, "schema/index.ts", "export const appSchema = {};")
	mustWrite(t, root, "package.json", `{"name":"x"}`)

	if err := preflightImports(root); err != nil {
		t.Fatalf("preflight should pass when schema/ exists: %v", err)
	}
}

// A bare-package import (npm dep, no leading ./ or ../) is never a bundle
// concern — the pod installs from package.json — so preflight ignores it.
func TestPreflightImports_IgnoresPackageImports(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "endpoints/todos/list/post.ts",
		`import { defineEndpoint, z } from "@palbase/backend";
export default {};`)
	mustWrite(t, root, "package.json", `{"name":"x"}`)

	if err := preflightImports(root); err != nil {
		t.Fatalf("bare package imports must not trip preflight: %v", err)
	}
}

// An import that escapes the project root entirely (too many ../) is reported,
// not silently ignored.
func TestPreflightImports_EscapingImportFails(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "endpoints/x/post.ts",
		`import { y } from "../../../../outside/y.js";
export default {};`)
	mustWrite(t, root, "package.json", `{"name":"x"}`)

	if err := preflightImports(root); err == nil {
		t.Fatal("expected error for import escaping the project root")
	}
}
