package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// palbaseBinary builds the CLI once and answers with its path.
//
// The tests below drive the BINARY rather than call these functions: a test
// that sequences runInit and runBuild by hand proves the functions agree with
// each other, which is not the thing that broke. `palbase init` produced a tree
// `palbase build` refused, and both halves passed their own tests.
func palbaseBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "palbase")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/palbase")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the CLI under test: %v\n%s", err, out)
	}
	return bin
}

// TestScaffoldBuildsEndToEnd is the regression guard for the shape of P0-2:
// `init` scaffolded a project the build gate refused, and the refusal advised
// running `init` — a circle. Nothing measured the pair.
func TestScaffoldBuildsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("installs the published SDK from the registry — excluded from -short")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("npm is required in CI: %v", err)
		}
		t.Skip("npm is not on PATH")
	}
	bin := palbaseBinary(t)
	dir := t.TempDir()

	init := exec.Command(bin, "init")
	init.Dir = dir
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("palbase init: %v\n%s", err, out)
	}

	build := exec.Command(bin, "build")
	build.Dir = dir
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("palbase build refused the tree palbase init just wrote: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "build OK") {
		t.Fatalf("build did not report success:\n%s", out)
	}
}
