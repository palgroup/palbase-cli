package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// CLI-18 regression: bundleCwd switched from a deny-list ("skip .git,
// node_modules, .palbase, …") to an allow-list because the deny-list
// happily packed iOS Xcode projects, Swift sources, *.xcodeproj/
// bundles, Info.plist — anything else the cwd held — and shipped
// them to the backend-runtime pod. The next `palbase pull` re-pulled
// the bloat, so the bug compounded across cycles. These tests pin the
// allow-list contract: only backend artifacts ride, the injected
// palbase.toml lands at the right place, hybrid trees stay safe.

func TestBundleCwd_AllowListOnly(t *testing.T) {
	root := t.TempDir()

	// Layout — mix of allowed (backend) and forbidden (iOS, monorepo) files.
	mustWrite(t, root, "endpoints/hello/get.ts", "export default {};")
	mustWrite(t, root, "endpoints/hello/get.ts.meta.json", "{}")
	mustWrite(t, root, "config/flags.toml", "# flags")
	mustWrite(t, root, "package.json", `{"name":"x"}`)
	mustWrite(t, root, "package-lock.json", "{}")
	mustWrite(t, root, "tsconfig.json", "{}")
	// Forbidden — the whole point of CLI-18.
	mustWrite(t, root, "Info.plist", "<plist/>")
	mustWrite(t, root, "palbase.xcodeproj/project.pbxproj", "// xcode")
	mustWrite(t, root, "palbase/Sources/Palbe/PalBackendClient.swift", "// swift")
	mustWrite(t, root, ".DS_Store", "junk")
	mustWrite(t, root, "node_modules/foo/index.js", "// node modules") // always-skip
	mustWrite(t, root, ".git/HEAD", "ref: refs/heads/main")             // always-skip
	mustWrite(t, root, ".palbase/openapi.json", "{}")                   // always-skip
	mustWrite(t, root, "docs/README.md", "# docs")                      // monorepo doc

	archive, err := bundleCwd(root, "ref0")
	if err != nil {
		t.Fatalf("bundleCwd: %v", err)
	}

	names := listTarball(t, archive)
	// Strip directory entries — only assert on actual files. tar headers
	// for dirs still get emitted, but it's the file payload that matters.
	got := filterFiles(names)
	sort.Strings(got)

	want := []string{
		"config/flags.toml",
		"endpoints/hello/get.ts",
		"endpoints/hello/get.ts.meta.json",
		"package-lock.json",
		"package.json",
		"palbase.toml", // injected at the end
		"tsconfig.json",
	}
	sort.Strings(want)
	if !slicesEqual(got, want) {
		t.Fatalf("bundle contents mismatch\n want: %v\n  got: %v", want, got)
	}

	// Hard invariants — the regression markers.
	for _, forbidden := range []string{
		"Info.plist",
		"palbase.xcodeproj",
		"palbase/Sources",
		"palbase/",
		"Sources/Palbe/PalBackendClient.swift",
		".DS_Store",
		"docs/README.md",
		"node_modules",
		".git",
		".palbase/openapi.json",
	} {
		for _, n := range names {
			if strings.Contains(n, forbidden) {
				t.Errorf("forbidden path leaked into bundle: %q (matched %q)", n, forbidden)
			}
		}
	}
}

func TestBundleCwd_InjectsRuntimeTOML(t *testing.T) {
	// Even when the user supplies a palbase.toml, the injected runtime
	// TOML must end up in the archive (it's appended after the walk —
	// tar lets the last write of the same name win at extract time).
	root := t.TempDir()
	mustWrite(t, root, "endpoints/x/get.ts", "export default {};")
	mustWrite(t, root, "palbase.toml", `# user-supplied — must be overwritten`)

	archive, err := bundleCwd(root, "myproj")
	if err != nil {
		t.Fatalf("bundleCwd: %v", err)
	}

	tomlBodies := readAll(t, archive, "palbase.toml")
	if len(tomlBodies) == 0 {
		t.Fatal("palbase.toml not in archive")
	}
	// The LAST entry is the injected runtime TOML; it must carry the ref.
	last := tomlBodies[len(tomlBodies)-1]
	if !strings.Contains(last, "myproj") {
		t.Fatalf("injected palbase.toml missing ref=myproj; got:\n%s", last)
	}
}

func TestBundleCwd_EmptyProjectFails(t *testing.T) {
	// nothing-to-bundle has always failed loudly so a CI script doesn't
	// silently push an empty archive. Allow-list change must not flip
	// that to a silent zero-file deploy.
	root := t.TempDir()
	mustWrite(t, root, "Info.plist", "<plist/>") // forbidden only — no allowed root
	_, err := bundleCwd(root, "r")
	if err == nil {
		t.Fatal("expected 'nothing to bundle' error, got nil")
	}
	if !strings.Contains(err.Error(), "nothing to bundle") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────

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

func listTarball(t *testing.T, archive []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
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
	return names
}

func readAll(t *testing.T, archive []byte, name string) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var bodies []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != name {
			continue
		}
		var b bytes.Buffer
		if _, err := io.Copy(&b, tr); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, b.String())
	}
	return bodies
}

func filterFiles(names []string) []string {
	// Tar headers carry both directory entries (mode bit) and file
	// entries. tar.FileInfoHeader emits dir entries without a trailing
	// slash in Name, so we can't filter on that — instead we keep only
	// names that have a known file extension or a leaf file basename
	// we know from the test fixture. The simpler trick: drop any name
	// that another name extends (i.e. has a child).
	out := make([]string, 0, len(names))
	for _, n := range names {
		isDir := false
		for _, other := range names {
			if other != n && strings.HasPrefix(other, n+"/") {
				isDir = true
				break
			}
		}
		if isDir {
			continue
		}
		out = append(out, n)
	}
	return out
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
