package backend

// planes.go — which verbs a directory has.
//
// There are two kinds of checkout and they need different things. A BACKEND
// checkout is code that gets deployed: it has controllers and a schema, and it
// pushes. An APP checkout consumes a backend: it has an Xcode project or a
// Gradle build or a web package, and it wants a contract and a generated client.
// A single surface that offers every verb everywhere is how `palbase push` ends
// up run in an iOS directory and answers something confusing about a missing
// file three layers down.
//
// The CLI works this out by LOOKING, not by being told. There is no `kind` field
// to keep in step with reality, and a directory that is genuinely both — a
// monorepo root — gets both sets.

import (
	"fmt"
	"os"
	"path/filepath"
)

// Plane is what a directory is.
type Plane int

const (
	// PlaneNone is a directory that is neither — an empty one, or somebody's
	// home. `init` is the verb that changes that.
	PlaneNone Plane = iota
	PlaneBackend
	PlaneApp
	PlaneBoth
)

func (p Plane) String() string {
	switch p {
	case PlaneBackend:
		return "backend"
	case PlaneApp:
		return "app"
	case PlaneBoth:
		return "backend and app"
	default:
		return "neither"
	}
}

// HasBackend reports whether backend verbs apply here.
func (p Plane) HasBackend() bool { return p == PlaneBackend || p == PlaneBoth }

// HasApp reports whether app verbs apply here.
func (p Plane) HasApp() bool { return p == PlaneApp || p == PlaneBoth }

// PlaneOf inspects a directory.
//
// A backend is `db/schema.ts` AND `controllers/`: either alone is ambiguous —
// an app repo can carry a `controllers` directory of view controllers, and a
// stray schema file is not a backend.
func PlaneOf(dir string) Plane {
	backend := isDir(filepath.Join(dir, "controllers")) && exists(filepath.Join(dir, "db", "schema.ts"))
	app := hasApple(dir) || exists(filepath.Join(dir, "build.gradle")) ||
		exists(filepath.Join(dir, "build.gradle.kts")) || hasWeb(dir)

	switch {
	case backend && app:
		return PlaneBoth
	case backend:
		return PlaneBackend
	case app:
		return PlaneApp
	default:
		return PlaneNone
	}
}

// RequireBackendPlane refuses a backend verb outside a backend checkout, and
// says what was looked for rather than what was missing in the abstract.
func RequireBackendPlane(dir string) error {
	if PlaneOf(dir).HasBackend() {
		return nil
	}
	return fmt.Errorf(
		"this is not a backend checkout: no db/schema.ts and controllers/ here (%s).\n"+
			"Run this where your controllers live, or `palbase init` to start one", dir)
}

// RequireAppPlane refuses an app verb outside an app checkout.
func RequireAppPlane(dir string) error {
	if PlaneOf(dir).HasApp() {
		return nil
	}
	return fmt.Errorf(
		"this is not an app checkout: no Xcode project, Gradle build or web package here (%s).\n"+
			"Run this where your app lives", dir)
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// hasApple looks for an Xcode project or workspace, which is a DIRECTORY with a
// known suffix rather than a file — the one shape a plain `os.Stat` on a name
// would miss.
func hasApple(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".xcodeproj" || filepath.Ext(name) == ".xcworkspace" {
			return true
		}
		// xcodegen projects are a spec file until the project is generated, and
		// a checkout that has not generated one yet is still an app checkout.
		if name == "project.yml" && exists(filepath.Join(dir, "project.yml")) {
			return true
		}
	}
	return false
}

// hasWeb is package.json AND an html entry: a package.json alone is every
// JavaScript project in the world, including a backend's own.
func hasWeb(dir string) bool {
	if !exists(filepath.Join(dir, "package.json")) {
		return false
	}
	return exists(filepath.Join(dir, "index.html")) ||
		isDir(filepath.Join(dir, "public")) ||
		isDir(filepath.Join(dir, "src", "app")) // next.js app router
}
