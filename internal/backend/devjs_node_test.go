package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildCheckNodeSuite runs the build-check.js Node test suite (node:test) as
// part of `go test`, so the CI unit gate (go test ./... -race) actually
// exercises the JS-level guards — the controllers-bundle externals lock (a
// controller must share the ONE resource module, never inline its own copy) and
// the TypeScript parser guard. Without this, those tests only ran when a human
// invoked `node --test` by hand.
//
// Skips when node is not on PATH. The test file lives beside the embedded
// build-check.js in ./devjs; node:test needs no node_modules for these suites
// (the bundle test skips itself when npx esbuild is unavailable).
func TestBuildCheckNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	testFile := filepath.Join("devjs", "build-check.test.js")
	if _, err := os.Stat(testFile); err != nil {
		t.Fatalf("build-check test file missing: %v", err)
	}
	cmd := exec.Command(node, "--test", testFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test build-check.test.js failed: %v\n%s", err, out)
	}
	// node:test exits 0 only when all tests pass, but assert the summary too so a
	// future harness that swallows the exit code still fails loudly here.
	s := string(out)
	if !strings.Contains(s, "# fail 0") {
		t.Fatalf("build-check node suite did not report zero failures:\n%s", s)
	}
}
