package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE PUSH GATE AND THE RELEASE GATE MUST RUN THE SAME THING.
//
// Measured 2026-09-02: this repository had ONE workflow, `release.yml`, and its
// only trigger was `push: tags: v*`. So nothing was ever tested until somebody
// cut a version — and the person cutting it was not the person who wrote the
// break. It happened exactly that way: `v0.51.0` was tagged to ship the
// `palbase admin` removal, the Release run failed on
// `TestBuildCheckNodeSuite` (a `typescript` module the runner does not have),
// and the four commits that introduced it had never been through CI at all.
// The tag was deleted; nothing shipped.
//
// A push gate fixes that only if it is the SAME gate. A cheaper one — no
// `-race`, a different Go, no bun — would go green on the very things the
// release gate rejects, and its green would be worse than no gate: it would
// say "tested" about a commit the release will refuse.
//
// So this test binds them. It does not assert what the command should BE; it
// asserts the two files AGREE. Change the release gate and the push gate must
// follow, in the same commit, or this fails.

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	require.NoError(t, err, "workflow %s is missing", name)
	return string(b)
}

// testCommand returns the `go test …` line a workflow runs, whitespace-collapsed.
func testCommand(t *testing.T, yaml string, workflow string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*run:\s*(go test [^\n]*)$`)
	m := re.FindStringSubmatch(yaml)
	require.Len(t, m, 2, "%s has no `run: go test …` step — a gate that runs no tests is not a gate", workflow)
	return strings.Join(strings.Fields(m[1]), " ")
}

// pinnedVersion returns the version pinned for a `with: <key>: "x"` block.
func pinnedVersion(t *testing.T, yaml, key, workflow string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(yaml)
	require.Len(t, m, 2, "%s does not pin %s", workflow, key)
	return m[1]
}

func TestPushGateMatchesReleaseGate(t *testing.T) {
	release := readWorkflow(t, "release.yml")
	ci := readWorkflow(t, "ci.yml")

	// (1) THE PUSH GATE ACTUALLY RUNS ON PUSHES. A workflow that only answers
	// `workflow_dispatch` is a button nobody presses.
	require.Regexp(t, `(?s)on:.*push:.*branches:`, ci,
		"ci.yml does not trigger on branch pushes — that was the whole defect")

	// (2) SAME TEST COMMAND. `-race` and `-count=1` are the parts most likely
	// to be dropped "to make CI faster", and both are the reason the gate
	// catches anything.
	require.Equal(t,
		testCommand(t, release, "release.yml"),
		testCommand(t, ci, "ci.yml"),
		"the push gate runs a DIFFERENT test command than the release gate — "+
			"its green would say 'tested' about a commit the release will refuse")

	// (3) SAME TOOLCHAIN. A different Go or bun is a different answer. bun
	// especially: without it the backend-build tests SKIP, and a gate that
	// skips the thing it exists to measure reports silence as success.
	for _, key := range []string{"go-version", "bun-version"} {
		require.Equal(t,
			pinnedVersion(t, release, key, "release.yml"),
			pinnedVersion(t, ci, key, "ci.yml"),
			"%s differs between the push gate and the release gate", key)
	}
}
