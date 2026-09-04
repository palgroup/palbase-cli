package backend

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nodeJUnitCase is one <testcase> from node:test's junit reporter. A case that
// broke carries a <failure> child (assertion) or an <error> child (the case
// threw before asserting); a passing one is a self-closing element.
type nodeJUnitCase struct {
	Name     string `xml:"name,attr"`
	Failures []struct {
		Message string `xml:"message,attr"`
	} `xml:"failure"`
	Errors []struct {
		Message string `xml:"message,attr"`
	} `xml:"error"`
}

// nodeJUnitReport models only the parts of the junit report that decide
// pass/fail. Cases sit directly under <testsuites>; the nested level exists
// because a JS file that groups with describe() emits <testsuite> wrappers,
// and a parse that saw only the top level would report "ran no test cases" for
// a perfectly healthy suite.
type nodeJUnitReport struct {
	Cases  []nodeJUnitCase `xml:"testcase"`
	Suites []struct {
		Cases []nodeJUnitCase `xml:"testcase"`
	} `xml:"testsuite"`
}

func (r nodeJUnitReport) allCases() []nodeJUnitCase {
	cases := append([]nodeJUnitCase(nil), r.Cases...)
	for _, s := range r.Suites {
		cases = append(cases, s.Cases...)
	}
	return cases
}

// runDevJSSuite runs one devjs node:test file inside `go test` and reports
// whatever broke by name.
//
// It pins --test-reporter=junit rather than reading node's default output.
// Node documents that reporter text "may change and should not be relied on
// programmatically", and it duly did: this wrapper used to grep TAP's
// "# fail 0" summary, which became unfindable once node's default reporter
// flipped from tap to spec ("ℹ fail 0"). Both suites stayed green while
// `go test` called them broken. jUnit XML is the one output shape here that is
// a machine contract rather than a human-readable one.
//
// The report is checked for two independent things, because they catch
// different failures: a suite that ran and failed cases, and a suite that
// never ran a case at all (node exits 0 for an empty file, so the exit code
// alone would call that green).
//
// NODE_PATH points at the pinned parser — a bare CI runner has none (no npm
// install step in ci.yml; see tsparser.go on why the project's own typescript,
// when present, isn't safe to borrow). This provisions the SAME parser
// `palbase build` provisions for real users (ensureParserTS → ~/.palbase/tools,
// cached after the first install), so the production codepath is exercised for
// real instead of skipped. Child processes spawned by the suite inherit it.
//
// Skips when node is not on PATH, or when the parser can't be provisioned at
// all (no npm, fully offline) — ensureParserTS's own documented fail-open
// contract, not a new one invented here.
func runDevJSSuite(t *testing.T, jsFile string) {
	t.Helper()

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	testFile := filepath.Join("devjs", jsFile)
	if _, err := os.Stat(testFile); err != nil {
		t.Fatalf("%s missing: %v", testFile, err)
	}
	tsDir := ensureParserTS(io.Discard)
	if tsDir == "" {
		t.Skip("could not provision the pinned typescript parser (no npm / offline) — see tsparser.go ensureParserTS")
	}

	cmd := exec.Command(node, "--test", "--test-reporter=junit", testFile)
	cmd.Env = append(os.Environ(), "NODE_PATH="+tsDir)
	// Keep the streams apart: the junit XML is stdout, and anything the suite
	// writes to stderr would otherwise be spliced into the middle of it.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()

	var report nodeJUnitReport
	if err := xml.Unmarshal(out, &report); err != nil {
		t.Fatalf("node --test %s produced unparseable junit output (run error: %v): %v\nstdout:\n%s\nstderr:\n%s",
			jsFile, runErr, err, out, stderr.String())
	}

	cases := report.allCases()
	if len(cases) == 0 {
		t.Fatalf("node --test %s ran no test cases (run error: %v)\nstderr:\n%s", jsFile, runErr, stderr.String())
	}

	var broken []string
	for _, c := range cases {
		for _, f := range c.Failures {
			broken = append(broken, c.Name+" — "+f.Message)
		}
		for _, e := range c.Errors {
			broken = append(broken, c.Name+" — "+e.Message)
		}
	}
	if len(broken) > 0 {
		t.Fatalf("%s: %d of %d test cases failed:\n  %s", jsFile, len(broken), len(cases), strings.Join(broken, "\n  "))
	}

	// A clean report that still exits non-zero means something broke outside
	// any test case — a top-level throw, an unknown flag — which must not pass
	// as green just because no case was around to record it.
	if runErr != nil {
		t.Fatalf("node --test %s exited non-zero with %d passing cases and no failure recorded: %v\nstderr:\n%s",
			jsFile, len(cases), runErr, stderr.String())
	}
}

// TestBuildCheckNodeSuite runs the build-check.js Node test suite as part of
// `go test`, so the CI unit gate actually exercises the JS-level guards — the
// controllers-bundle externals lock (a controller must share the ONE resource
// module, never inline its own copy), the TypeScript parser guard, and the
// three subprocess integration tests that spawn `node build-check.js` against
// a fixture PROJECT_ROOT. Without this Go wrapper, those tests only ran when a
// human invoked `node --test` by hand.
//
// build-check.test.js's OWN direct calls into return_types.js/throw_analysis.js
// dodge the pinned parser via a plain-JS fixture and withFakeTypescript, but
// its build-check.js subprocess spawns go through scanTxPlanViolations() first,
// which is a real parse — hence the parser provisioning in runDevJSSuite.
func TestBuildCheckNodeSuite(t *testing.T) {
	requiresRealToolchain(t)
	runDevJSSuite(t, "build-check.test.js")
}

// TestTxAnalysisNodeSuite runs tx_analysis.js's own node:test suite (the TxPlan
// Ref-truthiness build gate's 8 positive + 6 negative fixtures), the same
// reason TestBuildCheckNodeSuite exists: without this, the JS-level guard only
// runs when a human invokes `node --test` by hand.
//
// Unlike build-check.test.js's suite, this one NEEDS a real `typescript` parser
// to do anything — it parses 14 real fixtures, not just the withFakeTypescript
// error-path tests.
func TestTxAnalysisNodeSuite(t *testing.T) {
	runDevJSSuite(t, "tx_analysis.test.js")
}
