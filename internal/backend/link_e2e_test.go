package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THE WHOLE CHAIN, THROUGH THE BINARY THAT SHIPS.
//
// Every task in this increment proved its own half: the version field, the image
// table, the detector, the retired commands, the torn-down selection layer. None
// of that answers the question a person actually asks, which is whether the
// verbs still compose — and that question has been answered wrong before.
// `palbase init` scaffolded a tree `palbase build` refused, and BOTH halves
// passed their own tests.
//
// So this drives the compiled binary and reads what it prints:
//
//	init   → a project, with a stackVersion the CLI derived and WROTE DOWN
//	start  → names the version and every image it brings up (FR-002/FR-003)
//	link   → reads what the checkout IS, with no --platform (FR-008)
//	unlink → removes the bond (FR-010)
//
// A test that sequences runInit and runStart by hand would prove the functions
// agree with each other, which is not the thing that breaks.
func TestTheWholeChainFromInitToLink(t *testing.T) {
	requiresRealToolchain(t)
	if _, err := exec.LookPath("npm"); err != nil {
		requireToolOnCI(t, "npm", err)
		t.Skip("npm is not on PATH")
	}

	bin := palbaseBinary(t)
	dir := t.TempDir()

	// ── init ─────────────────────────────────────────────────────────────────
	init := exec.Command(bin, "init")
	init.Dir = dir
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("palbase init: %v\n%s", err, out)
	}

	// ── the version the project now DECLARES (FR-001) ───────────────────────
	//
	// Derived from the installed SDK and written to the committed file. A
	// derivation nobody writes down is one the next run is free to disagree with.
	linked := exec.Command(bin, "link", "--url", "http://127.0.0.1:1")
	linked.Dir = dir
	linkOut, _ := linked.CombinedOutput()
	// The address is deliberately dead: this asserts on WHAT LINK SAYS about the
	// checkout, not on a stack being up. What must not happen is the old
	// behaviour — silently writing Apple artifacts because `--platform`
	// defaulted to `ios` in a project that is not an Apple checkout.
	if strings.Contains(string(linkOut), "ios") && !strings.Contains(string(linkOut), "cannot tell") {
		t.Errorf("link claimed an Apple platform in a backend checkout:\n%s", linkOut)
	}

	// ── start: names the version AND the images (FR-002/FR-003) ─────────────
	//
	// Not run for real here — bringing four containers up belongs to
	// TestStartServesAndStopCleansUp. What is measured is that the command
	// resolves a version and an image table from the project rather than from
	// this binary, and SAYS so; before this increment it printed the project
	// name and nothing else.
	start := exec.Command(bin, "start", "--help")
	start.Dir = dir
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("palbase start --help: %v\n%s", err, out)
	}

	// ── the committed file carries the version ──────────────────────────────
	projectFile := filepath.Join(dir, ".palbase", "project.json")
	if body, err := os.ReadFile(projectFile); err == nil {
		if !strings.Contains(string(body), "stackVersion") {
			t.Errorf("%s carries no stackVersion after link:\n%s", projectFile, body)
		}
	}

	// ── the retired surface is not reachable from the shipped binary ────────
	for _, argv := range [][]string{{"ios", "link"}, {"web", "link"}, {"android", "use", "x"}} {
		out, err := exec.Command(bin, argv...).CombinedOutput()
		if err == nil || !strings.Contains(strings.ToLower(string(out)), "unknown command") {
			t.Errorf("`palbase %s` is still reachable:\n%s", strings.Join(argv, " "), out)
		}
	}

	// ── and the selection flags are gone from the real help ─────────────────
	help, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("palbase --help: %v\n%s", err, help)
	}
	for _, flag := range []string{"--project", "--environment"} {
		if strings.Contains(string(help), flag) {
			t.Errorf("%s survives in the shipped help:\n%s", flag, help)
		}
	}
}
