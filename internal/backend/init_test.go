package backend

// init_test.go — the scaffold comes from the package, and lands in an empty
// directory or not at all.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// packLocalSDK runs `npm pack` on the SDK source in this monorepo and returns
// the tarball path. Skips when the source is not beside this checkout.
func packLocalSDK(t *testing.T, ctx context.Context) string {
	t.Helper()
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/backend → sdk/cli → sdk → sdk/palbase-ts/backend
	sdk := filepath.Join(here, "..", "..", "..", "palbase-ts", "backend")
	if _, err := os.Stat(filepath.Join(sdk, "package.json")); err != nil {
		t.Skipf("the SDK source is not beside this checkout: %v", err)
	}
	out := t.TempDir()
	cmd := exec.CommandContext(ctx, "npm", "pack", "--pack-destination", out)
	cmd.Dir = sdk
	body, err := cmd.Output()
	if err != nil {
		t.Skipf("npm pack failed: %v", err)
	}
	name := strings.TrimSpace(string(body))
	if i := strings.LastIndex(name, "\n"); i >= 0 {
		name = name[i+1:]
	}
	return filepath.Join(out, name)
}

// TestInitRefusesADirectoryWithWorkInIt: `palbase init` writes files, so the one
// thing it must never do is write over somebody's project.
func TestInitRefusesADirectoryWithWorkInIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runInit(context.Background(), dir, backendPkg+"@latest", &strings.Builder{})
	if err == nil {
		t.Fatal("init wrote into a directory that already had work in it")
	}
	if !strings.Contains(err.Error(), "main.go") {
		t.Errorf("the refusal does not name what is in the way: %v", err)
	}
}

// TestAFreshGitCheckoutIsStillEmpty: cloning an empty repository and scaffolding
// into it is the ordinary way to start, and refusing it would send people to a
// temporary directory and a `mv`.
func TestAFreshGitCheckoutIsStillEmpty(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".git", ".gitignore", "README.md"} {
		path := filepath.Join(dir, name)
		if name == ".git" {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := refuseNonEmpty(dir); err != nil {
		t.Errorf("a fresh checkout was refused: %v", err)
	}
}

// TestTheScaffoldComesFromTheInstalledPackage is FR-045, and it is the whole
// point: an embedded copy ages at the speed of CLI releases while the SDK moves
// at its own, and the first thing a new user meets is a decorator that was
// renamed two majors ago.
//
// The package here is a REAL npm install of @palbase/backend, so what is copied
// is what the published SDK actually ships. Skipped offline.
func TestTheScaffoldComesFromTheInstalledPackage(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The LOCAL package, packed the way npm publishes it. The published 17.4.0
	// carries no template/ yet, and testing against the registry would prove
	// only that a release has not happened — this proves the mechanism against
	// the bytes a release will ship.
	tarball := packLocalSDK(t, ctx)

	var out strings.Builder
	if err := runInit(ctx, dir, tarball, &out); err != nil {
		if strings.Contains(err.Error(), "npm is not on PATH") || strings.Contains(err.Error(), "install") {
			t.Skipf("npm unavailable or the install failed: %v", err)
		}
		t.Fatalf("init: %v\n%s", err, out.String())
	}

	// The four files a backend needs to be one.
	for _, path := range []string{
		"package.json", "tsconfig.json",
		filepath.Join("db", "schema.ts"),
		filepath.Join("controllers", "health.controller.ts"),
		filepath.Join("config", "secrets.ts"),
	} {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Errorf("the scaffold has no %s: %v", path, err)
		}
	}

	// It came from the PACKAGE: the copied controller is byte-identical to the
	// one the package ships, which is what "not a copy of our own" means.
	//
	// Compared against the packed SOURCE rather than against node_modules,
	// because the second install — the one that resolves what the template
	// declares — replaces node_modules with whatever `latest` is on the
	// registry. That is correct for a real user, where both installs are the
	// same version, and it is why this assertion reads the source.
	here, _ := os.Getwd()
	fromPkg, err := os.ReadFile(filepath.Join(here, "..", "..", "..", "palbase-ts", "backend",
		"template", "controllers", "health.controller.ts"))
	if err != nil {
		t.Fatalf("the SDK source has no template: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "controllers", "health.controller.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(fromPkg) {
		t.Error("the scaffolded controller differs from the package's — something other than the package wrote it")
	}

	// The dependency the project declares is the SDK, unpinned: a pinned version
	// goes stale at the next major, and the lockfile is what records what was
	// actually resolved.
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Dependencies[backendPkg] == "" {
		t.Errorf("the scaffold does not depend on %s: %v", backendPkg, pkg.Dependencies)
	}
	if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err != nil {
		t.Errorf("no lockfile was written, so nothing pins what was resolved: %v", err)
	}

	// npm renames a packed .gitignore to .npmignore, so the template cannot
	// carry one and init writes it.
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("no .gitignore: %v", err)
	}
	for _, want := range []string{"node_modules/", ".palbase/local.json"} {
		if !strings.Contains(string(ignore), want) {
			t.Errorf(".gitignore does not carry %q:\n%s", want, ignore)
		}
	}
	// …and NOT the whole .palbase directory: project.json is committed on
	// purpose, so a colleague who clones this reaches the same project.
	for _, line := range strings.Split(string(ignore), "\n") {
		if strings.TrimSpace(line) == ".palbase" || strings.TrimSpace(line) == ".palbase/" {
			t.Error("the whole .palbase directory is ignored — project.json is committed on purpose")
		}
	}

	// And the result is a BACKEND plane, which is what every verb after this
	// one checks for.
	if err := RequireBackendPlane(dir); err != nil {
		t.Errorf("the scaffold is not recognised as a backend: %v", err)
	}
}

// TestInitInsideATreeWithAnAncestorPackageJSON is the trap npm sets: it walks UP
// looking for a package.json and installs into whatever it finds. Measured on
// 2026-08-17 in /tmp — which has one — where `npm install` answered "up to date",
// wrote nothing, and init then refused with "ships no template", a true sentence
// pointing at entirely the wrong thing.
func TestInitInsideATreeWithAnAncestorPackageJSON(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "package.json"),
		[]byte(`{"name":"an-unrelated-project","private":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "child")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tarball := packLocalSDK(t, ctx)

	var out strings.Builder
	if err := runInit(ctx, dir, tarball, &out); err != nil {
		t.Fatalf("init: %v\n%s", err, out.String())
	}

	// The install landed HERE, not in the ancestor.
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@palbase", "backend", "package.json")); err != nil {
		t.Errorf("the SDK was not installed in the project directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "node_modules")); err == nil {
		t.Error("npm installed into the ancestor project instead")
	}
	if _, err := os.Stat(filepath.Join(dir, "controllers", "health.controller.ts")); err != nil {
		t.Errorf("no scaffold was written: %v\n%s", err, out.String())
	}
}
