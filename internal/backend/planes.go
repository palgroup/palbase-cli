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
	"regexp"
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
	app := len(applePlatforms(dir)) > 0 || exists(filepath.Join(dir, "build.gradle")) ||
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
		"this is not a backend checkout: no %s and no *.module.ts here (%s).\n"+
			"Run this where your modules live, or `palbase init` to start one",
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

// applePlatforms reports which Apple platforms this checkout targets, and it
// answers three things the old `hasApple` could not.
//
//  1. IT CAN SAY macos. The detector returned a bool and the caller turned true
//     into "ios", so a macOS-only app was handed an iOS slot and an iOS
//     xcconfig — silently, because the two look identical until something reads
//     the platform.
//
//  2. IT LOOKS WHERE CROSS-PLATFORM PROJECTS KEEP IT. React Native, Expo and
//     Flutter all put the Xcode project in `ios/` (and `macos/`), never at the
//     root — so `palbase link` in the root of any of them found no Apple
//     platform at all and said "cannot tell what kind of app this is".
//
//  3. IT READS THE PROJECT INSTEAD OF GUESSING. The platform comes from the
//     pbxproj's own SDKROOT / SUPPORTED_PLATFORMS. A `project.yml` used to count
//     as Apple on the strength of its FILENAME, which is a common enough name
//     that any repository carrying one was classified as an app checkout; now
//     the file has to actually look like an XcodeGen spec.
func applePlatforms(dir string) []string {
	seen := map[string]bool{}
	// The root first, then the two directories the cross-platform toolchains
	// use. `dir` itself is checked with an empty prefix so a plain Xcode app is
	// found exactly as before.
	for _, sub := range []string{"", "ios", "macos", "apple"} {
		root := dir
		if sub != "" {
			root = filepath.Join(dir, sub)
			if !isDir(root) {
				continue
			}
		}
		for _, platform := range applePlatformsIn(root) {
			seen[platform] = true
		}
	}
	var out []string
	for _, platform := range []string{"ios", "macos"} { // deterministic order
		if seen[platform] {
			out = append(out, platform)
		}
	}
	return out
}

// applePlatformsIn inspects ONE directory for an Xcode project and reports what
// it builds for.
func applePlatformsIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		switch {
		case filepath.Ext(name) == ".xcodeproj":
			for _, p := range platformsFromPBXProj(filepath.Join(dir, name, "project.pbxproj")) {
				seen[p] = true
			}
		case filepath.Ext(name) == ".xcworkspace":
			// A workspace names its projects rather than its platforms; the
			// projects beside it are read on their own pass above. Treat the
			// workspace as an Apple signal whose platform is unknown.
			seen["ios"] = true
		case name == "project.yml":
			for _, p := range platformsFromXcodeGenSpec(filepath.Join(dir, name)) {
				seen[p] = true
			}
		}
	}
	var out []string
	for _, platform := range []string{"ios", "macos"} {
		if seen[platform] {
			out = append(out, platform)
		}
	}
	return out
}

// platformsFromPBXProj reads the SDK the project builds against. Xcode writes
// `SDKROOT = iphoneos;` / `macosx`, and a multiplatform target lists both in
// SUPPORTED_PLATFORMS.
//
// A project that cannot be read, or that names neither, is reported as `ios`:
// that is what every such checkout got before this function existed, and
// answering "no Apple platform" for a directory holding an .xcodeproj would
// turn a wrong slot into a refused link.
func platformsFromPBXProj(path string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return []string{"ios"}
	}
	text := string(body)
	var out []string
	if strings.Contains(text, "iphoneos") || strings.Contains(text, "iphonesimulator") {
		out = append(out, "ios")
	}
	if strings.Contains(text, "macosx") {
		out = append(out, "macos")
	}
	if len(out) == 0 {
		return []string{"ios"}
	}
	return out
}

// platformsFromXcodeGenSpec reads an XcodeGen spec — which is a project that has
// not been generated yet, and still an app checkout.
//
// The FILENAME is not the signal: `project.yml` belongs to several unrelated
// tools. An XcodeGen spec declares `targets:`, and each target names its
// platform.
func platformsFromXcodeGenSpec(path string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := string(body)
	if !strings.Contains(text, "targets:") {
		return nil // some other tool's project.yml
	}
	var out []string
	if strings.Contains(text, "platform: iOS") || strings.Contains(text, "platform: [iOS") {
		out = append(out, "ios")
	}
	if strings.Contains(text, "platform: macOS") || strings.Contains(text, "macOS") {
		out = append(out, "macos")
	}
	if len(out) == 0 {
		return []string{"ios"}
	}
	return out
}

// hasWeb is package.json AND an html entry: a package.json alone is every
// JavaScript project in the world, including a backend's own.
func hasWeb(dir string) bool {
	if !exists(filepath.Join(dir, "package.json")) {
		return false
	}
	if exists(filepath.Join(dir, "index.html")) || isDir(filepath.Join(dir, "public")) {
		return true
	}
	// NEXT'S APP ROUTER LIVES AT `app/` BY DEFAULT — `src/app/` is the opt-in
	// variant, and only that one was checked. Measured in a plain Next checkout:
	// `palbase link` answered "cannot tell what kind of app this is" for the
	// most common shape the framework ships.
	//
	// The LAYOUT file is what makes it a router, not the directory name: `app/`
	// is a common enough folder that treating it alone as a web signal would
	// classify somebody's backend as a web app.
	for _, root := range []string{"app", filepath.Join("src", "app")} {
		for _, ext := range []string{".tsx", ".jsx", ".ts", ".js"} {
			if exists(filepath.Join(dir, root, "layout"+ext)) {
				return true
			}
		}
	}
	return false
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

// detectPlatforms answers what this checkout IS, so `link` stops asking the
// reader to repeat it.
//
// `--platform` defaulted to `ios`, which meant `palbase link` in a web-only
// checkout wrote Apple artifacts and nothing else — silently, because a default
// that is wrong looks exactly like a default that is right. Every piece of
// material needed to answer the question was already in this package; it was
// simply never asked.
//
// AN EMPTY RESULT IS AN ANSWER. The caller has to tell "this is a web app" from
// "I could not tell", because only the second one deserves a sentence.
func detectPlatforms(dir string) []string {
	var found []string
	// EVERY Apple platform this checkout targets, not just the first. Returning
	// a bool here is what made `macos` unreachable: true became "ios", and a
	// macOS-only app was given an iOS slot.
	found = append(found, applePlatforms(dir)...)
	if _, err := detectAndroidApplicationID(dir); err == nil {
		found = append(found, "android")
	}
	if hasWeb(dir) {
		found = append(found, webPlatform)
	}
	return found
}

var androidApplicationIDPattern = regexp.MustCompile(`(?m)applicationId\s*(?:=\s*)?["']([^"']+)["']`)

func detectAndroidApplicationID(root string) (string, error) {
	candidates := []string{
		filepath.Join(root, "app", "build.gradle.kts"),
		filepath.Join(root, "app", "build.gradle"),
		filepath.Join(root, "build.gradle.kts"),
		filepath.Join(root, "build.gradle"),
	}
	for _, candidate := range candidates {
		contents, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		match := androidApplicationIDPattern.FindSubmatch(contents)
		if len(match) == 2 {
			return string(match[1]), nil
		}
	}
	return "", fmt.Errorf("applicationId not found in the Android Gradle files; pass --package-name")
}
