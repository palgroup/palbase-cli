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
