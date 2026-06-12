package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

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
	write(".palbase/config.json", `{"ref":"x"}`)
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
