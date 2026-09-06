package backend

// Shared test fixture deps: the build checker needs two REAL npm packages at
// runtime — `typescript` (the return-type stager parses controllers with it)
// and `esbuild` (controller bundling via `npx --yes esbuild`). Both are seeded
// into a fixture's node_modules from the host's own install (or a cached
// prefix install), so the tests exercise the production resolution path
// without a network round trip per run.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var (
	testDepMu   sync.Mutex
	testDepDirs = map[string]string{}
)

// requireToolOnCI turns a missing external tool into a FAILURE on CI while
// leaving it a skip on a developer's machine.
//
// Measured 2026-09-04: this package takes ~600 s locally and 85 s on CI. A gap
// that size is either a much faster machine or a much thinner run, and the two
// are indistinguishable from a green tick — so the thin run has to be made
// impossible rather than assumed away.
func requireToolOnCI(t *testing.T, tool string, err error) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("%s is not on PATH (%v) — on CI a missing toolchain is a broken gate, "+
			"not an excused test: every real-toolchain test would vanish from a run that still prints ok", tool, err)
	}
}

// hostNodePkgDir returns the package ROOT directory of an installed npm
// package, resolving the host's own install first and falling back to the
// cached test-dep prefix. Results are memoized for the test run.
func hostNodePkgDir(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		// ON CI THIS IS FATAL, NOT A SKIP. A missing tool here silently removes
		// every real-toolchain test from the run, and the suite still prints ok —
		// which is the exact shape of a gate that measures nothing while looking
		// green. Locally a skip is right: somebody without node can still run the
		// rest. On CI, the tool is the runner's job and its absence is a broken
		// gate, not an excused one.
		requireToolOnCI(t, "node", err)
		t.Skip("node not on PATH")
	}
	testDepMu.Lock()
	defer testDepMu.Unlock()
	if dir, ok := testDepDirs[name]; ok {
		return dir
	}
	// 1) Host-resolvable install. Prefer <name>/package.json (gives the pkg
	// root directly); when an exports map blocks that subpath, resolve the main
	// entry and walk up to the directory holding package.json.
	resolve := fmt.Sprintf(`
const path = require('path'), fs = require('fs');
let d;
try { d = path.dirname(require.resolve(%[1]q + '/package.json')); }
catch {
  d = path.dirname(require.resolve(%[1]q));
  while (!fs.existsSync(path.join(d, 'package.json'))) d = path.dirname(d);
}
process.stdout.write(d);`, name)
	// TypeScript's unversioned package is now 7.x, while generated Palbase
	// backends intentionally support ^5. Always seed that dependency from the
	// pinned-major cache; a host-global 7.x install must not make tests drift.
	if name != "typescript" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "node", "-e", resolve).Output(); err == nil && len(out) > 0 {
			testDepDirs[name] = string(out)
			return testDepDirs[name]
		}
	}
	// 2) Cached prefix install — downloads once per machine, reused across runs.
	if _, err := exec.LookPath("npm"); err != nil {
		requireToolOnCI(t, "npm", err)
		t.Skip("npm not on PATH")
	}
	prefix := filepath.Join(os.TempDir(), "palbase-cli-testdeps", name)
	pkgDir := filepath.Join(prefix, "node_modules", name)
	if _, err := os.Stat(filepath.Join(pkgDir, "package.json")); err != nil {
		ictx, icancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer icancel()
		installSpec := name
		if name == "typescript" {
			// The generated backend template supports TypeScript 5 (`^5`). The
			// unversioned npm tag now resolves to TypeScript 7, whose package root
			// intentionally exposes only version metadata instead of the parser API
			// used by return_types.js and throw_analysis.js. Keep this hermetic
			// fixture on the same supported major as real Palbase projects.
			installSpec = "typescript@^5"
		}
		cmd := exec.CommandContext(ictx, "npm", "i", "--no-save", "--prefix", prefix, installSpec)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm i %s into test-dep cache %s: %v\n%s", name, prefix, err, out)
		}
	}
	testDepDirs[name] = pkgDir
	return pkgDir
}

// requiresRealToolchain marks a test that runs the REAL bundler, type-checker or
// npm rather than a stub, and keeps it out of the `-short` budget.
//
// `-short` exists so somebody can ask "did I break anything" in the time it
// takes to read the answer. Measured 2026-09-04 it took 418.75 s in this package
// alone against a 180 s budget, and 366 s of that sat in 23 tests that each shell
// out to a real toolchain. They are not slow by accident — they are slow because
// running the real thing is their whole point, which is exactly what makes them
// the wrong tests for a fast loop.
//
// THEY ARE NOT WEAKENED AND THEY ARE NOT OPTIONAL: CI runs `go test ./... -race`
// with no `-short`, so every one of these still runs on every push. The flag
// splits one suite into a fast question and a thorough one; it does not delete
// the thorough one.
//
// It also buys NFR-003 — no network under `-short`. The real-toolchain tests are
// the ones that can reach for `npm i` when a machine's test-dep cache is cold.
func requiresRealToolchain(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the real toolchain (bundler/tsc/npm) — outside the -short budget")
	}
}

// seedNodePkg symlinks an installed npm package into <root>/node_modules/<name>
// so the build checker (cwd + NODE_PATH resolution) finds it. No-op when the
// fixture already provides one (e.g. a stub written by the test).
func seedNodePkg(t *testing.T, root, name string) string {
	t.Helper()
	dst := filepath.Join(root, "node_modules", name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return dst // already present (e.g. a stub written by the fixture)
	}
	if err := os.Symlink(hostNodePkgDir(t, name), dst); err != nil {
		t.Fatalf("link %s into fixture: %v", name, err)
	}
	return dst
}

// seedEsbuild seeds the real esbuild package AND a node_modules/.bin/esbuild
// launcher into the fixture so the build checker's `npx --yes esbuild` resolves
// the LOCAL install — npx prefers a project-local bin: deterministic, no
// network, and the production resolution path stays exercised. The .bin entry
// points at the package's own bin/esbuild (the native binary after a normal
// npm install, or the JS shim — both runnable); the platform package
// (@esbuild/<os>-<arch>) resolves from the REAL package directory through the
// symlink (Node realpaths it), so it needs no seeding of its own.
func seedEsbuild(t *testing.T, root string) {
	t.Helper()
	pkg := seedNodePkg(t, root, "esbuild")
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir node_modules/.bin: %v", err)
	}
	dst := filepath.Join(binDir, "esbuild")
	if _, err := os.Lstat(dst); err == nil {
		return
	}
	if err := os.Symlink(filepath.Join(pkg, "bin", "esbuild"), dst); err != nil {
		t.Fatalf("link esbuild bin into fixture: %v", err)
	}
}

// mustWrite writes a fixture file, creating parent dirs.
// mustWrite writes body to root/rel, creating parent dirs. Shared by the
// serve fixtures that materialise a temp controllers/ tree.
func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
