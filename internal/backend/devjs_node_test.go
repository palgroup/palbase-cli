package backend

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildCheckNodeSuite runs the build-check.js Node test suite (node:test) as
// part of `go test`, so the CI unit gate (go test ./... -race) actually
// exercises the JS-level guards — the controllers-bundle externals lock (a
// controller must share the ONE resource module, never inline its own copy),
// the TypeScript parser guard, and (since the TxPlan gate was wired into
// build-check.js's main()) the three subprocess integration tests that spawn
// `node build-check.js` against a fixture PROJECT_ROOT. Without this Go
// wrapper, those tests only ran when a human invoked `node --test` by hand.
//
// NODE_PATH is set to the pinned parser (see TestTxAnalysisNodeSuite's longer
// comment on why) — build-check.test.js's OWN direct calls into
// return_types.js/throw_analysis.js still dodge it via a plain-JS fixture and
// withFakeTypescript, same as before, but its build-check.js SUBPROCESS
// spawns now go through scanTxPlanViolations() first, which is a real parse.
// Those child processes inherit NODE_PATH from this one's os.Environ(), so
// setting it once here covers all of them.
//
// Skips when node is not on PATH, or when the parser can't be provisioned at
// all (no npm, fully offline) — ensureParserTS's own documented fail-open
// contract.
func TestBuildCheckNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	testFile := filepath.Join("devjs", "build-check.test.js")
	if _, err := os.Stat(testFile); err != nil {
		t.Fatalf("build-check test file missing: %v", err)
	}
	tsDir := ensureParserTS(io.Discard)
	if tsDir == "" {
		t.Skip("could not provision the pinned typescript parser (no npm / offline) — see tsparser.go ensureParserTS")
	}
	cmd := exec.Command(node, "--test", testFile)
	cmd.Env = append(os.Environ(), "NODE_PATH="+tsDir)
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

// TestTxAnalysisNodeSuite runs tx_analysis.js's own node:test suite (the
// TxPlan Ref-truthiness build gate's 8 positive + 6 negative fixtures) as part
// of `go test`, the same reason TestBuildCheckNodeSuite exists: without this,
// the JS-level guard only runs when a human invokes `node --test` by hand.
//
// Unlike build-check.test.js's suite, this one NEEDS a real `typescript`
// parser to do anything (it parses 14 real fixtures, not just the
// withFakeTypescript error-path tests) — and a bare CI runner has none (no
// npm install step in ci.yml; see tsparser.go's own doc comment on why the
// project's typescript, when present, isn't safe to borrow either). So this
// provisions the SAME pinned parser `palbase build` provisions for real users
// (ensureParserTS → ~/.palbase/tools, cached after the first install) and
// points the subprocess's NODE_PATH at it — this is not a workaround bolted
// onto the test, it is the production codepath, exercised for real instead of
// skipped.
//
// Skips when node is not on PATH, or when the parser can't be provisioned at
// all (no npm, fully offline) — ensureParserTS's own documented fail-open
// contract, not a new one invented here.
func TestTxAnalysisNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	testFile := filepath.Join("devjs", "tx_analysis.test.js")
	if _, err := os.Stat(testFile); err != nil {
		t.Fatalf("tx_analysis test file missing: %v", err)
	}
	tsDir := ensureParserTS(io.Discard)
	if tsDir == "" {
		t.Skip("could not provision the pinned typescript parser (no npm / offline) — see tsparser.go ensureParserTS")
	}
	cmd := exec.Command(node, "--test", testFile)
	cmd.Env = append(os.Environ(), "NODE_PATH="+tsDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test tx_analysis.test.js failed: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "# fail 0") {
		t.Fatalf("tx_analysis node suite did not report zero failures:\n%s", s)
	}
}
