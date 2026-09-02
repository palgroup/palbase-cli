package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// THE SCAFFOLD MUST COMPILE AGAINST THE SDK IT SHIPS WITH.
//
// `template.test.ts` in the SDK already reads every scaffold file — but it only
// PARSES them (`ts.createSourceFile`), which answers "is this syntactically
// TypeScript" and nothing else. A call with the wrong argument SHAPE parses
// perfectly.
//
// That hole shipped. `DbNoteRepo.findMany` passed a bare filter to
// `Database.public.notes.findMany`, which SDK 25 accepted and 26 does not — the
// scaffold's own comment promised "build once and the whole surface below is
// typed", and after a build it was one type error. Measured 2026-09-02 in a
// project scaffolded from the packed package.
//
// A scaffold teaches by BEING COPIED, so a scaffold that does not compile
// teaches a call that does not compile. This gate is the compiler's answer, in
// the one place where the package can be packed, installed, built and typed:
// the CLI, which already does the first two.
func TestTheScaffoldTypechecksAgainstItsOwnSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("packs, installs, builds and typechecks a project — not a -short gate")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm is not on PATH")
	}

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	tarball := packLocalSDK(t, ctx)

	var out strings.Builder
	if err := runInit(ctx, dir, tarball, &out); err != nil {
		t.Skipf("init could not run here: %v\n%s", err, out.String())
	}

	// `runInit`'s SECOND install resolves what the template DECLARES, so
	// node_modules ends up holding whatever the registry serves — an older
	// major, until this one is published. Typing the new scaffold against an old
	// package would fail for a reason that is not the question. Install the
	// packed bytes over it: the question is whether THIS template compiles
	// against THIS SDK.
	install := exec.CommandContext(ctx, "npm", "install", "--no-audit", "--no-fund", tarball)
	install.Dir = dir
	if body, err := install.CombinedOutput(); err != nil {
		t.Skipf("installing the packed SDK failed: %v\n%s", err, body)
	}

	// `palbase-env.d.ts` is what types `Database.public.<table>`, and only a
	// build writes it. Without this step the typecheck would report the tables
	// as unknown and say nothing about the calls — a red gate that is right for
	// the wrong reason.
	var buildOut strings.Builder
	if err := runBuild(ctx, dir, &buildOut); err != nil {
		t.Fatalf("the scaffold does not even build: %v\n%s", err, buildOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "palbase-env.d.ts")); err != nil {
		t.Fatalf("the build wrote no palbase-env.d.ts, so the typecheck below would "+
			"measure the wrong thing: %v\n%s", err, buildOut.String())
	}

	tsc := exec.CommandContext(ctx, "npx", "--no-install", "tsc", "--noEmit")
	tsc.Dir = dir
	body, err := tsc.CombinedOutput()
	if err == nil {
		return
	}
	// `npx --no-install` fails differently when tsc is simply absent; that is an
	// environment gap, not a scaffold defect, and must not read as one.
	if strings.Contains(string(body), "could not determine executable") ||
		strings.Contains(string(body), "npm ERR") {
		t.Skipf("tsc is not installed in the scaffold: %s", body)
	}
	t.Errorf("the scaffold does NOT typecheck against the SDK it ships with — "+
		"every project copied from it starts red:\n%s", body)
}
