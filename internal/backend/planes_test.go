package backend

// planes_test.go — a directory says which verbs it has.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
			write(t, d, "modules/notes/notes.module.ts")
			write(t, d, "db/public.ts")
		}, PlaneBackend},

		{"an Xcode app", func(t *testing.T, d string) { mkdir(t, d, "MyApp.xcodeproj") }, PlaneApp},
		{"an xcodegen app before generating", func(t *testing.T, d string) { write(t, d, "project.yml") }, PlaneApp},
		{"an Android app", func(t *testing.T, d string) { write(t, d, "build.gradle.kts") }, PlaneApp},
		{"a web app", func(t *testing.T, d string) {
			write(t, d, "package.json")
			write(t, d, "index.html")
		}, PlaneApp},

		{"a monorepo root that is both", func(t *testing.T, d string) {
			write(t, d, "modules/notes/notes.module.ts")
			write(t, d, "db/public.ts")
			mkdir(t, d, "MyApp.xcodeproj")
		}, PlaneBoth},

		{"an empty directory", func(t *testing.T, d string) {}, PlaneNone},

		// The two that LOOK like a plane. A backend's own package.json must not
		// make it a web app, and an app repo full of view controllers must not
		// make it a backend — the second is now true BY CONSTRUCTION rather than
		// by luck: a backend is a schema plus a module, and `controllers/` on its
		// own says nothing at all.
		{"a backend's package.json alone", func(t *testing.T, d string) {
			write(t, d, "modules/notes/notes.module.ts")
			write(t, d, "db/public.ts")
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
	// The refusal must name BOTH halves of what a backend is — the schema and a
	// module. It used to say `controllers/`, which sent an author looking for a
	// directory the runtime does not use.
	for _, want := range []string{"db/public.ts", "*.module.ts", "palbase init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	backend := t.TempDir()
	write(t, backend, "modules/notes/notes.module.ts")
	write(t, backend, "db/public.ts")
	if err := RequireBackendPlane(backend); err != nil {
		t.Fatalf("a backend verb was refused in a backend checkout: %v", err)
	}
	// The app direction has no guard, on purpose: every verb that touches an app
	// is also run from a backend checkout (the harness links its backend fixture
	// with --platform ios), so the refusal would have no verb to protect. What
	// the plane detector answers is still asserted above.
}

// TestALegacyCheckoutIsToldWhatToRename is NFR-002 on the surface a backend
// verb hits first. A project with controllers/ and the retired db/schema.ts is
// not a stranger — it is this project, one rename behind — and "no schema here"
// is a sentence nobody can act on while the file is on screen in front of them.
func TestALegacyCheckoutIsToldWhatToRename(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, dir, "controllers")
	write(t, dir, "db/schema.ts")

	if got := PlaneOf(dir); got.HasBackend() {
		t.Fatalf("the retired layout still answers for the schema half: PlaneOf → %v", got)
	}
	err := RequireBackendPlane(dir)
	if err == nil {
		t.Fatal("a checkout with no usable declaration was accepted")
	}
	for _, want := range []string{LegacySchemaFile, PublicSchemaFile, MigrationGuide} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// TestATrueBackendIsStillAccepted is the NEGATIVE CONTROL for the gate above:
// it must refuse its target and nothing else. A multi-schema project is a
// legitimate backend and must walk straight through.
func TestATrueBackendIsStillAccepted(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "modules/notes/notes.module.ts")
	write(t, dir, "db/public.ts")
	write(t, dir, "db/billing.ts")

	if got := PlaneOf(dir); got != PlaneBackend {
		t.Fatalf("a multi-schema backend was not recognised: PlaneOf → %v", got)
	}
	if err := RequireBackendPlane(dir); err != nil {
		t.Fatalf("a legitimate multi-schema backend was refused: %v", err)
	}
}

func TestDetectAndroidApplicationID_KotlinAndGroovy(t *testing.T) {
	for _, tc := range []struct{ name, filename, contents string }{
		{"kotlin", "build.gradle.kts", `android { defaultConfig { applicationId = "com.example.kotlin" } }`},
		{"groovy", "build.gradle", `android { defaultConfig { applicationId 'com.example.groovy' } }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			appDir := filepath.Join(root, "app")
			require.NoError(t, os.MkdirAll(appDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(appDir, tc.filename), []byte(tc.contents), 0o644))
			got, err := detectAndroidApplicationID(root)
			require.NoError(t, err)
			require.Contains(t, tc.contents, got)
		})
	}
}

// THE SHAPE NEXT.JS SHIPS BY DEFAULT.
//
// `hasWeb` checked `src/app/` and not `app/`. `src/` is Next's OPT-IN layout;
// `app/` at the root is what `create-next-app` produces unless you ask
// otherwise — so the most common web project in the world answered:
//
//	cannot tell what kind of app this is: looked for an Xcode project or
//	workspace, an Android applicationId in build.gradle[.kts], and a
//	package.json beside an index.html/public/src/app — found none
//
// Measured through the shipped binary in a plain Next checkout. The signal is
// the LAYOUT file rather than the directory: `app/` alone is a folder name
// anybody might use, and treating it as web would classify a backend as one.
func TestWebDetectionFindsNextsDefaultLayout(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"app router at the root", []string{"package.json", "app/layout.tsx"}, true},
		{"app router under src", []string{"package.json", "src/app/layout.tsx"}, true},
		{"javascript, not typescript", []string{"package.json", "app/layout.jsx"}, true},
		{"a plain html entry", []string{"package.json", "index.html"}, true},
		{"a public directory", []string{"package.json", "public/favicon.ico"}, true},
		// The negative half, which is what keeps the rule honest.
		{"an app directory with no layout", []string{"package.json", "app/notes.ts"}, false},
		{"a backend, which is also a package.json", []string{"package.json", "controllers/todo.controller.ts"}, false},
		{"no package.json at all", []string{"app/layout.tsx"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				path := filepath.Join(dir, f)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := hasWeb(dir); got != tc.want {
				t.Errorf("hasWeb(%v) = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}
