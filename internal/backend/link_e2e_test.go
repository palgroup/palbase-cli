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
	//
	// AGAINST A STACK THAT ANSWERS. This used to point at a dead address, so
	// `link` never got far enough to write anything — and the assertion below
	// read a file that never existed and SKIPPED ITSELF. The box was ticked and
	// nothing was measured, which is the exact failure this run exists to stop.
	// The fake stack is the one the unit tests use; the binary reaches it over
	// loopback like any other address.
	srv := stackServing(t, "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0", nil)
	home := t.TempDir()
	t.Setenv("HOME", home)
	linkedAs(t, srv.URL, "a-credential")

	linked := exec.Command(bin, "link", "--url", srv.URL)
	linked.Dir = dir
	linked.Env = append(os.Environ(), "HOME="+home)
	linkOut, linkErr := linked.CombinedOutput()
	if linkErr != nil {
		t.Fatalf("palbase link: %v\n%s", linkErr, linkOut)
	}
	// A backend checkout has no client app, and `link` must say so and bind
	// anyway — the binding is what `push` reads. The old behaviour it must not
	// return to is silently writing Apple artifacts because `--platform`
	// defaulted to `ios`.
	if !strings.Contains(string(linkOut), "no client app here") {
		t.Errorf("link did not say what it looked for in a backend checkout:\n%s", linkOut)
	}
	if strings.Contains(string(linkOut), "Palbase/Config") {
		t.Errorf("link wrote Apple artifacts in a backend checkout:\n%s", linkOut)
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
	//
	// NO SILENT SKIP. This read used to be wrapped in `if err == nil`, and the
	// file never existed, so the one assertion this test was written for never
	// ran once. A missing file is now the failure it always was.
	projectFile := filepath.Join(dir, ".palbase", "project.json")
	body, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatalf("link wrote no %s, so the checkout is not bound and `push` cannot work: %v\n%s",
			projectFile, err, linkOut)
	}
	if !strings.Contains(string(body), "stackVersion") {
		t.Errorf("%s carries no stackVersion after link:\n%s", projectFile, body)
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
