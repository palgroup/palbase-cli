package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// makeTarGz builds an in-memory tar.gz with the given entries (name → content).
// Passing an entry with a "/" suffix creates a directory entry.
func makeTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range entries {
		var hdr *tar.Header
		if strings.HasSuffix(name, "/") {
			hdr = &tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}
		} else {
			hdr = &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractTarGz_HappyPath(t *testing.T) {
	gz := makeTarGz(t, map[string]string{
		"index.ts":             "export const x = 1",
		"controllers/":         "",
		"controllers/todo.ts":  "// ctrl",
		"nested/deep/file.txt": "deep",
	})
	dst := t.TempDir()
	if err := extractTarGz(dst, bytes.NewReader(gz)); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	for _, rel := range []string{"index.ts", "controllers/todo.ts", "nested/deep/file.txt"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}
	content, err := os.ReadFile(filepath.Join(dst, "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "export const x = 1" {
		t.Fatalf("index.ts content = %q", string(content))
	}
}

// A RELATIVE destination must extract normally. The containment guard compares
// the joined target against dst as a string prefix, so an un-normalized "."
// rejects every entry — filepath.Join(".", ".git") is ".git", which is neither
// prefixed by "./" nor equal to ".". `project create` extracts into the working
// directory, and this is the failure it hit live: the deployed bundle carries a
// .git directory (that is how the "initial template" commit arrives), so the
// FIRST entry aborted the whole extraction.
func TestExtractTarGz_RelativeDestinationExtracts(t *testing.T) {
	gz := makeTarGz(t, map[string]string{
		".git/":                "",
		".git/HEAD":            "ref: refs/heads/main",
		"controllers/hello.ts": "// ctrl",
		"package.json":         "{}",
	})
	dir := t.TempDir()
	t.Chdir(dir)

	if err := extractTarGz(".", bytes.NewReader(gz)); err != nil {
		t.Fatalf("extractTarGz into %q: %v", ".", err)
	}
	for _, rel := range []string{".git/HEAD", "controllers/hello.ts", "package.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}
}

// A pulled bundle carries deploy output next to the sources: the compiled
// bundle under .palbase/ and a <controller>.ts.meta.json beside every
// controller. Both are regenerated on every deploy, so extracting them into a
// checkout just hands the developer files to ignore — the .meta.json sidecar
// was the single untracked file in a freshly created project.
func TestExtractSourceTree_DropsDeployArtifacts(t *testing.T) {
	gz := makeTarGz(t, map[string]string{
		"controllers/hello.controller.ts":           "// ctrl",
		"controllers/hello.controller.ts.meta.json": `{"controller":true}`,
		".palbase/":                     "",
		".palbase/controllers/hello.js": "compiled",
		".palbase/esm/package.json":     "{}",
		"models/hello/greet.ts":         "// zod",
		"package.json":                  "{}",
	})
	dst := t.TempDir()
	if err := extractSourceTree(dst, bytes.NewReader(gz)); err != nil {
		t.Fatalf("extractSourceTree: %v", err)
	}

	for _, rel := range []string{"controllers/hello.controller.ts", "models/hello/greet.ts", "package.json"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			t.Errorf("source %s should have been extracted: %v", rel, err)
		}
	}
	for _, rel := range []string{
		"controllers/hello.controller.ts.meta.json",
		".palbase/controllers/hello.js",
		".palbase/esm/package.json",
	} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err == nil {
			t.Errorf("deploy artifact %s must not land in the checkout", rel)
		}
	}
}

// The bundle carries the PLATFORM's repository. Writing it into a destination
// overwrote the developer's git history on `pull` and planted a nested .git on
// `create` inside a monorepo — the second of which the CLI had just printed "no
// nested .git created" about.
//
// It never actually corrupted a repository, but only by luck: git stores loose
// objects 0444, so the second pull into any directory died with EACCES on the
// first read-only object the first pull had written. The reported symptom was
// "pull is not idempotent"; the defect underneath was writing someone else's
// history over yours.
func TestExtractSourceTree_NeverWritesTheBundlesOwnGit(t *testing.T) {
	gz := makeTarGz(t, map[string]string{
		".git/":                           "",
		".git/HEAD":                       "ref: refs/heads/main",
		".git/objects/ab/cdef":            "loose object",
		".git/palbase-active-version":     "db8420a8",
		"controllers/hello.controller.ts": "// ctrl",
	})
	dst := t.TempDir()
	if err := extractSourceTree(dst, bytes.NewReader(gz)); err != nil {
		t.Fatalf("extractSourceTree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		t.Error("the bundle's .git must never be written into the destination")
	}
	if _, err := os.Stat(filepath.Join(dst, "controllers", "hello.controller.ts")); err != nil {
		t.Errorf("sources must still extract: %v", err)
	}
}

// roGitObjectTarGz builds a bundle whose git object carries git's REAL 0444
// mode. That mode is the whole bug: makeTarGz's 0644 entries re-extract fine, so
// a test built on it would pass with or without the fix.
func roGitObjectTarGz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	entries := []struct {
		name string
		mode int64
		body string
	}{
		{".git/objects/ab/cdef", 0o444, "loose object"}, // git writes these read-only
		{"controllers/hello.controller.ts", 0o644, "// ctrl"},
		{"package.json", 0o644, "{}"},
	}
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: e.name, Typeflag: tar.TypeReg, Mode: e.mode, Size: int64(len(e.body)),
		}))
		_, err := tw.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	return buf.Bytes()
}

// The consequence that matters: extracting the same bundle twice into the same
// directory now succeeds. It did not before — the 0444 git objects the first
// extraction wrote made every later pull die with EACCES.
func TestExtractSourceTree_IsIdempotent(t *testing.T) {
	dst := t.TempDir()
	for i := 1; i <= 2; i++ {
		if err := extractSourceTree(dst, bytes.NewReader(roGitObjectTarGz(t))); err != nil {
			t.Fatalf("extraction %d into the same directory failed: %v", i, err)
		}
	}
}

func TestExtractTarGz_PathTraversalRejected(t *testing.T) {
	cases := []string{
		"../evil",
		"../../etc/passwd",
		"foo/../../evil",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			gz := makeTarGz(t, map[string]string{name: "boom"})
			dst := t.TempDir()
			err := extractTarGz(dst, bytes.NewReader(gz))
			if err == nil {
				t.Fatalf("extractTarGz with entry %q should have errored (path traversal)", name)
			}
			if !strings.Contains(err.Error(), "path traversal") {
				t.Fatalf("expected 'path traversal' in error, got: %v", err)
			}
		})
	}
}

// TestExtractTarGz_AbsolutePathSanitised verifies that a tar entry with an
// absolute path (e.g. "/etc/passwd") is sanitised by filepath.Join and lands
// UNDER dst rather than at the absolute filesystem path. Go's filepath.Join
// strips the leading slash from subsequent args, so this is inherently safe;
// the test pins that contract so a future refactor can't accidentally break it.
func TestExtractTarGz_AbsolutePathSanitised(t *testing.T) {
	gz := makeTarGz(t, map[string]string{"/etc/passwd": "root:x"})
	dst := t.TempDir()
	if err := extractTarGz(dst, bytes.NewReader(gz)); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	// File MUST land under dst, never at /etc/passwd.
	under := filepath.Join(dst, "etc", "passwd")
	if _, err := os.Stat(under); err != nil {
		t.Fatalf("expected file at %s (under dst): %v", under, err)
	}
	// Confirm the real /etc/passwd was not overwritten (it should exist and NOT
	// contain our payload).
	real, err := os.ReadFile("/etc/passwd")
	if err == nil && strings.Contains(string(real), "root:x") && string(real) == "root:x" {
		t.Fatal("/etc/passwd was overwritten — absolute path escaped dst")
	}
}

func tarEntries(t *testing.T, gz []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}

func TestBuildTarball_IncludesFilesExcludesIgnored(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.ts", "export const x = 1")
	write("controllers/todo.controller.ts", "// ctrl")
	write(".git/HEAD", "ref: refs/heads/main")
	write(".palbase/selection.json", `{"ref":"x"}`)
	write("node_modules/dep/index.js", "module.exports={}")
	write(".palignore", "secrets.txt\n")
	write("secrets.txt", "TOPSECRET")

	gz, err := BuildTarball(dir)
	if err != nil {
		t.Fatalf("BuildTarball: %v", err)
	}

	got := tarEntries(t, gz)
	want := []string{".palignore", "controllers/todo.controller.ts", "index.ts"}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries = %v, want %v", got, want)
		}
	}
}

// TestBuildTarball_ExcludesNestedIgnoredDirs locks the fix for the silent
// deploy-hang: a nested web app's node_modules/.next (web/node_modules,
// web/.next) MUST be excluded too, not just the top-level ones. Before the fix
// the walk matched only the first path segment, so web/node_modules (357MB in a
// real project) shipped → the base64 tarball blew past Temporal's 4MB gRPC arg
// cap → opaque RESOURCE_EXHAUSTED deploy failure.
func TestBuildTarball_ExcludesNestedIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.ts", "export const x = 1")
	write("controllers/todo.controller.ts", "// ctrl")
	write("node_modules/dep/index.js", "top-level")          // top-level (already excluded)
	write("web/node_modules/react/index.js", "NESTED BLOAT") // nested — the bug
	write("web/.next/build-manifest.json", "{}")             // nested build output
	write("web/app/page.tsx", "export default () => null")   // a real nested file → kept

	gz, err := BuildTarball(dir)
	if err != nil {
		t.Fatalf("BuildTarball: %v", err)
	}
	got := tarEntries(t, gz)
	for _, n := range got {
		if strings.Contains(n, "node_modules") || strings.Contains(n, "/.next/") || strings.HasPrefix(n, ".next/") {
			t.Fatalf("ignored dir leaked into bundle: %q (all: %v)", n, got)
		}
	}
	// The non-ignored nested file must still be packed.
	if !contains(got, "web/app/page.tsx") {
		t.Fatalf("expected web/app/page.tsx to be included; got %v", got)
	}
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// V10 (CWE-61): a symlink whose target is OUTSIDE the project tree must NOT be
// followed and packed — that would exfiltrate the developer's local secrets
// (e.g. ~/.palbase/credentials) into the deploy bundle.
func TestBuildTarball_DoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A secret file OUTSIDE the project tree, and a symlink inside the tree
	// pointing at it (the malicious-starter-template shape).
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "credentials.json")
	if err := os.WriteFile(secretPath, []byte("LOCAL_OAUTH_TOKEN_topsecret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(dir, "logo.png")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	gz, err := BuildTarball(dir)
	if err != nil {
		t.Fatalf("BuildTarball: %v", err)
	}
	got := tarEntries(t, gz)

	if contains(got, "logo.png") {
		t.Fatalf("symlink 'logo.png' was packed (target bytes leaked): entries=%v", got)
	}
	// Sanity: the real file is still included.
	if !contains(got, "index.ts") {
		t.Fatalf("index.ts missing: entries=%v", got)
	}
}

// V10 (CWE-538): secret-bearing files (.env.local etc.) are excluded by default,
// independent of .palignore — `palbase secret pull` writes decrypted secrets to
// .env.local, which must never ride along in the deploy bundle.
func TestBuildTarball_ExcludesSecretFilesByDefault(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.ts", "x")
	write(".env", "A=1")
	write(".env.local", "DB_PASS=decrypted")
	write("apns.p8", "-----BEGIN PRIVATE KEY-----")
	write("server.key", "-----BEGIN EC PRIVATE KEY-----")
	write("cert.pem", "-----BEGIN CERTIFICATE-----")
	// NO .palignore — exclusion must be by default.

	gz, err := BuildTarball(dir)
	if err != nil {
		t.Fatalf("BuildTarball: %v", err)
	}
	got := tarEntries(t, gz)

	for _, leaked := range []string{".env", ".env.local", "apns.p8", "server.key", "cert.pem"} {
		if contains(got, leaked) {
			t.Fatalf("secret file %q was packed into the bundle: entries=%v", leaked, got)
		}
	}
	if !contains(got, "index.ts") {
		t.Fatalf("index.ts missing: entries=%v", got)
	}
}

// TestBuildTarball_ExcludesStagedControllers locks the staging tree out of the
// deploy bundle. `palbase build` writes .palbase-build-controllers/ beside
// controllers/ (return bindings injected) and removes it on exit — but a
// SIGKILLed run leaves it in the project root, and shipping it would put a
// second, shadow copy of every controller into the tarball the deploy stages.
// The JS side owns the real name; this test pins the Go constant to the same
// spelling, so renaming one without the other fails here.
func TestBuildTarball_ExcludesStagedControllers(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("controllers/todos.controller.ts", "export default class T {}")
	write(stagedControllersDir+"/todos.controller.ts", "STAGED_COPY_CANARY")
	write(stagedControllersDir+"/nested/deep.ts", "STAGED_COPY_CANARY")
	write(deployStagingDir+"/modules/todos.controller.ts", "STAGED_COPY_CANARY")
	write("nested/"+deployStagingDir+"/modules/todos.controller.ts", "STAGED_COPY_CANARY")

	tarball, err := BuildTarball(dir)
	require.NoError(t, err)
	names := tarEntries(t, tarball)

	require.Contains(t, names, "controllers/todos.controller.ts", "the real controller must ship")
	for _, n := range names {
		require.NotContains(t, n, stagedControllersDir,
			"the build staging tree must never enter the deploy tarball (got %q)", n)
		require.NotContains(t, n, deployStagingDir,
			"an old deploy staging tree must never enter the deploy tarball (got %q)", n)
	}
}

// stagedControllersDir must match build-check.js's STAGED_CONTROLLERS_DIR.
func TestStagedControllersDirMatchesBuildCheck(t *testing.T) {
	src, err := buildCheckFS.ReadFile("devjs/build-check.js")
	require.NoError(t, err)
	require.Contains(t, string(src), "'"+stagedControllersDir+"'",
		"build-check.js must stage into %s — the tarball walk skips exactly that name", stagedControllersDir)
}
