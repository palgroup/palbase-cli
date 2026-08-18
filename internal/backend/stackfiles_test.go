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

// The images must be NAMED as registry references by default. A local tag as
// the default is exactly what made `palbase start` require this repository:
// docker cannot fetch `palbase-palsvc` from anywhere, so the command only worked
// on a machine where somebody had already built it.
//
// It reads the compose file and nothing else, so it says the default POINTS at a
// registry — not that the registry will serve it. Worth stating because the two
// came apart on 2026-08-18: the edge package was created private by ghcr, this
// test was green, and an anonymous `docker manifest inspect` answered
// `unauthorized`. Whether a stranger can actually pull is a property of the
// registry, and the only honest place to assert it is against the registry.
func TestTheVendoredStackPullsItsImages(t *testing.T) {
	for _, want := range []string{
		"ghcr.io/palgroup/palbase/palsvc:",
		"ghcr.io/palgroup/palbase/runtime-dev:",
		// The edge is a third image now, and it carries the route table — a
		// stack that cannot pull it has no door.
		"ghcr.io/palgroup/palbase/edge:",
	} {
		if !strings.Contains(string(stackCompose), want) {
			t.Errorf("no default image at %s — the default must name a registry, or `palbase start` needs this repository", want)
		}
	}
}

func TestARegistryReferenceIsRecognised(t *testing.T) {
	for _, c := range []struct {
		image string
		want  bool
	}{
		{"ghcr.io/palgroup/palbase/palsvc:0.29.1", true},
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

// `--lan` widens ONE port, and there is only one to widen.
//
// A local stack holds a real database password and a service_role key. It used
// to publish two ports — palsvc's and postgres' — and this test's job was to
// keep `--lan` away from the second. Now nothing but the edge is published at
// all, so the test asserts the stronger thing: exactly ONE publish line exists,
// it is the edge's, it reads the bind variable, and no line anywhere in the
// file puts the database on the host.
func TestOnlyTheEdgeIsPublished(t *testing.T) {
	var publishes []string
	for _, line := range strings.Split(string(stackCompose), "\n") {
		trimmed := strings.TrimSpace(line)
		// A PUBLISH line, not a mention: the header names both variables in
		// prose long before anything is published, and a search that stops at
		// the first match reads the documentation instead of the file.
		if strings.HasPrefix(trimmed, "- \"") && strings.Contains(trimmed, ":") &&
			strings.Contains(trimmed, "${PALBASE_") {
			publishes = append(publishes, trimmed)
		}
	}

	if len(publishes) != 1 {
		t.Fatalf("the stack publishes %d port(s), want exactly one (the edge):\n%s",
			len(publishes), strings.Join(publishes, "\n"))
	}
	edge := publishes[0]
	if !strings.Contains(edge, "${"+BindEnv) {
		t.Errorf("the published port does not read %s — `--lan` would change nothing: %s", BindEnv, edge)
	}
	if !strings.HasSuffix(edge, ":8080\"") {
		t.Errorf("the published port does not reach the edge's 8080: %s", edge)
	}
	if strings.Contains(string(stackCompose), ":5432\"") {
		t.Error("something publishes the database port — a stack's password would be on the host, and `--lan` would put it on the network")
	}
}
