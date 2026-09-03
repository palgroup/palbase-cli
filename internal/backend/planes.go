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
	"strings"
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
// A backend is `db/public.ts` AND `controllers/`: either alone is ambiguous —
// an app repo can carry a `controllers` directory of view controllers, and a
// stray schema file is not a backend.
//
// The retired `db/schema.ts` deliberately does NOT answer for the schema half.
// Accepting it would let a project that cannot deploy walk through this door
// and fail further in, with a message about whatever it reached first;
// RequireBackendPlane below names the real problem instead.
func PlaneOf(dir string) Plane {
	// A BACKEND IS A SCHEMA PLUS AT LEAST ONE MODULE — not a directory named
	// `controllers`. That name was the third place discovery still read the file
	// system instead of the declaration, and it was the one a real project hit
	// last: `palbase push` refused a tree whose domains live in
	// `modules/<name>/` with "this is not a backend checkout", after `build`
	// had already validated 85 routes in it. Measured 2026-09-02.
	backend := hasModuleFile(dir) && HasSchemaDeclaration(dir)
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
	// A checkout still carrying the retired layout is not a stranger who wandered
	// in — it is this project, one rename behind. Telling it "no schema here"
	// while db/schema.ts sits in front of the person reading is true and useless.
	if HasLegacySchemaFile(dir) {
		return fmt.Errorf(
			"%s is the old layout: rename it to %s and declare each table with "+
				"defineTable(\"name\", {…}) inside defineSchema(\"public\", { tables: [ … ] }) — "+
				"the move is written out at %s",
			LegacySchemaFile, PublicSchemaFile, MigrationGuide)
	}
	return fmt.Errorf(
		"this is not a backend checkout: no %s and controllers/ here (%s).\n"+
			"Run this where your controllers live, or `palbase init` to start one",
		PublicSchemaFile, dir)
}

// There is deliberately NO RequireAppPlane.
//
// It existed, and nothing called it. The reason is FR-003: a directory can be
// both planes, and every verb that touches an app — `link`, `spec`, `status` —
// is run from backend checkouts too (the harness links its backend fixture with
// --platform ios). A guard with no verb that needs it is a guard somebody
// eventually wires into the wrong place, so it is gone rather than kept for
// symmetry. Five lines to bring back the day a genuinely app-only verb exists.

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

// hasModuleFile reports whether any `*.module.ts` lives under dir — the same
// question `moduleSources` answers, asked cheaply and stopping at the first hit.
func hasModuleFile(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (name == "node_modules" || name == "dist" || name == ".git" ||
				strings.HasPrefix(name, ".palbase")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".module.ts") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
