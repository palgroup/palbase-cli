package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDevServerNodeSuite runs the dev-server.js Node test suite (node:test) as
// part of `go test`, so the CI unit gate (go test ./... -race) actually
// exercises the JS-level guards — most importantly the per-request identity
// ISOLATION test (concurrent requests must not cross RLS/auth context across an
// await; the AsyncLocalStorage switch). Without this, dev-server.js's JS tests
// only ran when a human invoked `node --test` by hand, so an ALS regression
// (back to a shared module-global slot → cross-tenant leak) would pass CI.
//
// Skips when node is not on PATH (matches the other serve-path tests). The test
// file lives beside the embedded dev-server.js in ./devjs; node:test needs no
// node_modules for these suites (they exercise pure in-process helpers +
// AsyncLocalStorage, no @palbase/backend import).
func TestDevServerNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	testFile := filepath.Join("devjs", "dev-server.test.js")
	if _, err := os.Stat(testFile); err != nil {
		t.Fatalf("dev-server test file missing: %v", err)
	}
	cmd := exec.Command(node, "--test", testFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test dev-server.test.js failed: %v\n%s", err, out)
	}
	// node:test exits 0 only when all tests pass, but assert the summary too so a
	// future harness that swallows the exit code still fails loudly here.
	s := string(out)
	if !strings.Contains(s, "# fail 0") {
		t.Fatalf("dev-server node suite did not report zero failures:\n%s", s)
	}
}
