package main

// The release ships binaries for platforms nobody develops on.
//
// `credentials_lock.go` called `unix.Flock` with no build tag. It compiled on
// every machine anyone used and could never compile for Windows, so from the
// commit that added it every Windows build failed — and nothing said so for
// three days, because the release workflow had been deleted and no local gate
// crosses a platform boundary. The first tag after restoring it is what found
// out, half an hour into a release.
//
// So the platforms .goreleaser.yml promises are compiled here, from the same
// tree, before a tag can be worth pushing.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// goreleaserTargets reads the OS/arch matrix out of the release config rather
// than repeating it. A platform added there and not here would be exactly the
// gap this file exists to close.
func goreleaserTargets(t *testing.T) (oses, arches []string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", ".goreleaser.yml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yml: %v", err)
	}
	section := regexp.MustCompile(`(?s)\n\s*goos:\n((?:\s+- \w+\n)+)`)
	archSection := regexp.MustCompile(`(?s)\n\s*goarch:\n((?:\s+- \w+\n)+)`)
	item := regexp.MustCompile(`- (\w+)`)

	for _, m := range item.FindAllStringSubmatch(firstGroup(t, section, string(raw), "goos"), -1) {
		oses = append(oses, m[1])
	}
	for _, m := range item.FindAllStringSubmatch(firstGroup(t, archSection, string(raw), "goarch"), -1) {
		arches = append(arches, m[1])
	}
	return oses, arches
}

func firstGroup(t *testing.T, re *regexp.Regexp, s, what string) string {
	t.Helper()
	m := re.FindStringSubmatch(s)
	if m == nil {
		t.Fatalf(".goreleaser.yml declares no %s list", what)
	}
	return m[1]
}

func TestEveryPlatformTheReleaseShipsCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiling every target is the release gate, not the fast one")
	}
	oses, arches := goreleaserTargets(t)
	if len(oses) < 2 || len(arches) < 1 {
		t.Fatalf("read %v / %v out of .goreleaser.yml — that is not a release matrix", oses, arches)
	}

	for _, goos := range oses {
		for _, goarch := range arches {
			t.Run(goos+"/"+goarch, func(t *testing.T) {
				cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), "./...")
				cmd.Dir = ".."
				cmd.Env = append(os.Environ(),
					"GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOWORK=off")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("the release would fail to build %s/%s:\n%s",
						goos, goarch, strings.TrimSpace(string(out)))
				}
			})
		}
	}
}
