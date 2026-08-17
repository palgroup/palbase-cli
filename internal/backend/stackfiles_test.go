package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The compose document the binary carries is a COPY of v2/deploy's. Copies
// drift, and this one drifting means `palbase start` brings up a stack that
// stopped resembling the one the repository tests. Held against the original
// whenever the palbase repository is beside this checkout — every development
// machine, and no CI runner, which is why an absence skips rather than fails.
func TestTheVendoredComposeMatchesTheRepository(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	// internal/backend → sdk/cli → sdk → palbase → v2/deploy
	original := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "v2", "deploy", composeFile)
	want, err := os.ReadFile(original)
	if err != nil {
		t.Skipf("the palbase repository is not beside this checkout: %v", err)
	}
	if string(want) != string(stackCompose) {
		t.Errorf("the vendored %s differs from %s — re-vendor it, or the CLI starts a stack the repository does not test",
			composeFile, original)
	}
}

// The images must be PULLABLE by default. A local tag as the default is exactly
// what made `palbase start` require this repository: docker cannot fetch
// `palbase-palsvc` from anywhere, so the command only worked on a machine where
// somebody had already built it.
func TestTheVendoredStackPullsItsImages(t *testing.T) {
	for _, want := range []string{
		"ghcr.io/palgroup/palbase-palsvc:",
		"ghcr.io/palgroup/palbase-runtime-dev:",
	} {
		if !strings.Contains(string(stackCompose), want) {
			t.Errorf("no default image at %s — a stranger cannot pull it", want)
		}
	}
}

func TestARegistryReferenceIsRecognised(t *testing.T) {
	for _, c := range []struct {
		image string
		want  bool
	}{
		{"ghcr.io/palgroup/palbase-palsvc:0.29.0", true},
		{"localhost:5000/palsvc", true},
		// Docker Hub's short form: docker does pull it, but the first segment is
		// an ORG, not a host — and nothing in this stack defaults to one, so
		// treating it as local (and letting the inspect refuse) is the safe way
		// round.
		{"pgvector/pgvector:pg16", false},
		{"palbase-palsvc", false},
		{"palbase-runtime-dev:latest", false},
	} {
		if got := isRegistryImage(c.image); got != c.want {
			t.Errorf("isRegistryImage(%q) = %v, want %v", c.image, got, c.want)
		}
	}
}
