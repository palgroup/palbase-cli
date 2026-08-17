package backend

// planes_test.go — a directory says which verbs it has.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, rel string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("//\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, dir, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, rel), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestADirectorySaysWhatItIs covers every shape the CLI will meet, including the
// two that look like a plane and are not.
func TestADirectorySaysWhatItIs(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(t *testing.T, dir string)
		want  Plane
	}{
		{"a backend", func(t *testing.T, d string) {
			mkdir(t, d, "controllers")
			write(t, d, "db/schema.ts")
		}, PlaneBackend},

		{"an Xcode app", func(t *testing.T, d string) { mkdir(t, d, "MyApp.xcodeproj") }, PlaneApp},
		{"an xcodegen app before generating", func(t *testing.T, d string) { write(t, d, "project.yml") }, PlaneApp},
		{"an Android app", func(t *testing.T, d string) { write(t, d, "build.gradle.kts") }, PlaneApp},
		{"a web app", func(t *testing.T, d string) {
			write(t, d, "package.json")
			write(t, d, "index.html")
		}, PlaneApp},

		{"a monorepo root that is both", func(t *testing.T, d string) {
			mkdir(t, d, "controllers")
			write(t, d, "db/schema.ts")
			mkdir(t, d, "MyApp.xcodeproj")
		}, PlaneBoth},

		{"an empty directory", func(t *testing.T, d string) {}, PlaneNone},

		// The two that LOOK like a plane. A backend's own package.json must not
		// make it a web app, and an app repo full of view controllers must not
		// make it a backend.
		{"a backend's package.json alone", func(t *testing.T, d string) {
			mkdir(t, d, "controllers")
			write(t, d, "db/schema.ts")
			write(t, d, "package.json")
		}, PlaneBackend},
		{"an app's controllers directory alone", func(t *testing.T, d string) {
			mkdir(t, d, "controllers")
			mkdir(t, d, "MyApp.xcodeproj")
		}, PlaneApp},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			c.build(t, dir)
			if got := PlaneOf(dir); got != c.want {
				t.Fatalf("PlaneOf → %v, want %v", got, c.want)
			}
		})
	}
}

// TestAWrongPlaneVerbSaysWhatItLookedFor: the refusal has to be actionable, or
// somebody goes hunting for a bug that is a wrong directory.
func TestAWrongPlaneVerbSaysWhatItLookedFor(t *testing.T) {
	app := t.TempDir()
	mkdir(t, app, "MyApp.xcodeproj")

	err := RequireBackendPlane(app)
	if err == nil {
		t.Fatal("a backend verb was allowed in an app checkout")
	}
	for _, want := range []string{"db/schema.ts", "controllers/", "palbase init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	backend := t.TempDir()
	mkdir(t, backend, "controllers")
	write(t, backend, "db/schema.ts")
	if err := RequireBackendPlane(backend); err != nil {
		t.Fatalf("a backend verb was refused in a backend checkout: %v", err)
	}
	// The app direction has no guard, on purpose: every verb that touches an app
	// is also run from a backend checkout (the harness links its backend fixture
	// with --platform ios), so the refusal would have no verb to protect. What
	// the plane detector answers is still asserted above.
}
