package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// writeFile is a tiny test helper.
func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// makeServerTar packs files into a tar.gz the way the server pull returns.
func makeServerTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
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

// TestMergePulledCode_CleanMerge: server changed file A, local changed file B.
// A git-style merge must end with BOTH changes present and no conflict markers.
func TestMergePulledCode_CleanMerge(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir()

	// Initial pull establishes the base: a.txt + b.txt.
	base := makeServerTar(t, map[string]string{"a.txt": "A base\n", "b.txt": "B base\n"})
	res, err := mergePulledCode(dir, base, "v1")
	if err != nil {
		t.Fatalf("initial pull: %v", err)
	}
	if res.Conflicted {
		t.Fatal("initial pull should not conflict")
	}

	// Local edit to b.txt.
	writeFile(t, dir, "b.txt", "B local edit\n")

	// Server advances a.txt (b.txt unchanged on server).
	next := makeServerTar(t, map[string]string{"a.txt": "A server edit\n", "b.txt": "B base\n"})
	res, err = mergePulledCode(dir, next, "v2")
	if err != nil {
		t.Fatalf("second pull: %v", err)
	}
	if res.Conflicted {
		t.Fatalf("non-overlapping changes must merge cleanly, got conflicts: %v", res.ConflictedFiles)
	}
	// Server's a.txt change applied; local b.txt edit preserved.
	if got := readFile(t, dir, "a.txt"); got != "A server edit\n" {
		t.Fatalf("a.txt = %q, want server edit", got)
	}
	if got := readFile(t, dir, "b.txt"); got != "B local edit\n" {
		t.Fatalf("b.txt = %q, want local edit preserved", got)
	}
}

// TestMergePulledCode_Conflict: server and local edit the SAME line of the same
// file → standard git conflict markers must appear in the working tree, and the
// result must report Conflicted with the file listed.
func TestMergePulledCode_Conflict(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir()

	base := makeServerTar(t, map[string]string{"x.txt": "line1\nshared\nline3\n"})
	if _, err := mergePulledCode(dir, base, "v1"); err != nil {
		t.Fatalf("initial: %v", err)
	}

	// Local edits the shared line.
	writeFile(t, dir, "x.txt", "line1\nLOCAL\nline3\n")

	// Server edits the same line differently.
	next := makeServerTar(t, map[string]string{"x.txt": "line1\nSERVER\nline3\n"})
	res, err := mergePulledCode(dir, next, "v2")
	if err != nil {
		t.Fatalf("conflicting pull should not hard-error (markers are the result): %v", err)
	}
	if !res.Conflicted {
		t.Fatal("expected a conflict")
	}
	if len(res.ConflictedFiles) == 0 || res.ConflictedFiles[0] != "x.txt" {
		t.Fatalf("expected x.txt in conflicted files, got %v", res.ConflictedFiles)
	}
	body := readFile(t, dir, "x.txt")
	if !strings.Contains(body, "<<<<<<<") || !strings.Contains(body, "=======") || !strings.Contains(body, ">>>>>>>") {
		t.Fatalf("expected git conflict markers in x.txt, got:\n%s", body)
	}
	if !strings.Contains(body, "LOCAL") || !strings.Contains(body, "SERVER") {
		t.Fatalf("both sides must appear in markers, got:\n%s", body)
	}
}

// TestMergePulledCode_RefusesForeignGitRepo: if the dir is ALREADY a git repo
// that is NOT a palbase clone (e.g. the user's iOS app repo), merging would
// clobber their history. mergePulledCode must refuse rather than touch it.
func TestMergePulledCode_RefusesForeignGitRepo(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir()
	// Make it a foreign git repo with a real commit, no palbase base ref.
	g := &gitRunner{dir: dir}
	if err := g.run("init", "-q", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	_ = g.configIdentity()
	writeFile(t, dir, "App.swift", "// the user's iOS app\n")
	if err := g.commitWorkingTree("user's own commit"); err != nil {
		t.Fatal(err)
	}

	tar := makeServerTar(t, map[string]string{"endpoints/x/post.ts": "// x\n"})
	_, err := mergePulledCode(dir, tar, "v1")
	if err == nil {
		t.Fatal("expected refusal to merge into a foreign (non-palbase) git repo")
	}
	if !strings.Contains(err.Error(), "git repo") && !strings.Contains(strings.ToLower(err.Error()), "not a palbase") {
		t.Fatalf("error should explain the foreign-repo refusal, got: %v", err)
	}
	// The user's file is untouched.
	if got := readFile(t, dir, "App.swift"); got != "// the user's iOS app\n" {
		t.Fatalf("user file was modified: %q", got)
	}
}

// TestMergePulledCode_FreshClone: into an empty dir, the first pull just lands
// the server tree (no conflict, becomes the base).
func TestMergePulledCode_FreshClone(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir()
	tar := makeServerTar(t, map[string]string{"endpoints/todos/list/post.ts": "// list\n"})
	res, err := mergePulledCode(dir, tar, "v1")
	if err != nil {
		t.Fatalf("fresh clone: %v", err)
	}
	if res.Conflicted {
		t.Fatal("fresh clone must not conflict")
	}
	if got := readFile(t, dir, "endpoints/todos/list/post.ts"); got != "// list\n" {
		t.Fatalf("file not landed: %q", got)
	}
	// A real git repo with .git at the root (so Fork/VS Code can open it).
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf(".git must exist at project root: %v", err)
	}
}

// TestMergePulledCode_IgnoresGeneratedAndState: the local clone must NOT track
// derived/local-only files (.palbase/ cache, .env.local, node_modules). They'd
// otherwise show up in git status, get committed, and pollute conflicts.
func TestMergePulledCode_IgnoresGeneratedAndState(t *testing.T) {
	gitAvailable(t)
	dir := t.TempDir()
	tar := makeServerTar(t, map[string]string{"endpoints/x/post.ts": "// x\n"})
	if _, err := mergePulledCode(dir, tar, "v1"); err != nil {
		t.Fatalf("fresh clone: %v", err)
	}
	// Simulate derived/local files appearing after a pull.
	writeFile(t, dir, ".palbase/endpoints/x/post.js", "// compiled\n")
	writeFile(t, dir, ".palbase/deploy-state.json", `{"branch_heads":{}}`)
	writeFile(t, dir, ".env.local", "SECRET=1\n")
	writeFile(t, dir, "node_modules/dep/index.js", "// dep\n")

	g := &gitRunner{dir: dir}
	out, err := g.output("status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{".palbase/", ".env.local", "node_modules/"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%s must be gitignored, but git status shows it:\n%s", bad, out)
		}
	}
}
