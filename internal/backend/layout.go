package backend

// layout.go — THE ONE DECLARATION of what this CLI puts in somebody's project.
//
// Every path the CLI writes or reads inside a customer's checkout comes from
// here. A path spelled in two places drifts, and this repository has already
// paid for that: the ignore list carried `.palbase-serve-controllers/` — a name
// nothing had written for two renames — while the directory `palbase build`
// ACTUALLY created was the one nobody ignored, and it got committed.
//
// THE SHAPE, and why each part of it is the way it is:
//
//	palbase/                         ← ONE visible directory, per repository
//	  client.ts                        web: the stable door to the selected client
//	  environments/<env>/              everything that exists PER environment
//	    openapi.json                   the contract
//	    roles.json                     the role definitions
//	    Palbase-Info.plist             apple config (both ios and macos slots)
//	    android-config.json            android config
//	    web-config.json                web config
//	    PalbaseGenerated.swift         apple generated client
//	    palbe.gen.ts                   web generated client
//
// THE ENVIRONMENT DIRECTORY IS FLAT — the platform is in the FILE NAME, never a
// subdirectory. That is a measured constraint, not a preference. Xcode selects
// which environment compiles with
//
//	EXCLUDED_SOURCE_FILE_NAMES = */palbase/environments/*/*
//	INCLUDED_SOURCE_FILE_NAMES = */palbase/environments/$(PALBASE_ENV)/*
//
// and `*` in those settings does NOT cross a directory boundary (measured on a
// real Xcode 26.6 build: `*/palbase/environments/*` excluded nothing, the
// two-level form excluded correctly, both directions). Adding a platform
// subdirectory would silently make the pattern match nothing — no error, and
// two environments' clients compiled into one app.
//
// Filenames differ per platform for the same reason a subdirectory cannot be
// used: one checkout can serve more than one platform (an RN app has web and
// android side by side), and two `config.json` files in one directory would
// collide.
//
// SEPARATOR IS ALWAYS `/`. These strings reach git, `.gitattributes` and the
// Xcode pattern above — `filepath.Join` would write `\` on Windows and the
// pattern would match nothing there.

import (
	"os"
	"path"
	"path/filepath"
)

const (
	rootDir   = "palbase"
	envSubdir = "environments"
)

// RootDir is the single directory this CLI creates in a checkout.
func RootDir() string { return rootDir }

// EnvDir is where ONE environment's committed artifacts live.
func EnvDir(env string) string { return path.Join(rootDir, envSubdir, env) }

// SpecPath is that environment's contract.
func SpecPath(env string) string { return path.Join(EnvDir(env), "openapi.json") }

// RolesPath is that environment's role definitions, beside its contract — a
// generator handed the spec finds the roles BY RULE rather than by a second
// setting somebody has to keep in step.
func RolesPath(env string) string { return path.Join(EnvDir(env), "roles.json") }

// ConfigPath is what the CLI WRITES for one platform of one environment — the
// generator's INPUT, and the file `readAppEnvironments` reads back on a relink.
//
// One rule for every platform. Apple's generator turns this JSON into the plist
// the app bundles (PlistPath); android's and web's read it directly.
func ConfigPath(env, platform string) string {
	return path.Join(EnvDir(env), platform+"-config.json")
}

// PlistPath is what swiftgen EMITS for one environment: the file an Apple app
// bundles and the SDK reads at first use.
//
// Separate from ConfigPath because on Apple the generator's input and output are
// two different files — the CLI writes JSON, swiftgen writes the plist, and the
// app ships only the second.
func PlistPath(env string) string { return path.Join(EnvDir(env), "Palbase-Info.plist") }

// GeneratedPath is the committed client generated for that environment, or ""
// for a platform whose generator writes outside the checkout.
//
// Android returns "": its Gradle plugin generates into the build directory, per
// variant, and nothing is committed — so there is no path here to look for, and
// returning one would make callers search for a file that never exists.
func GeneratedPath(env, platform string) string {
	switch platform {
	case "ios", "macos":
		return path.Join(EnvDir(env), "PalbaseGenerated.swift")
	case webPlatform:
		return path.Join(EnvDir(env), "palbe.gen.ts")
	default:
		return ""
	}
}

// EnvTypesPath is the declaration file generated from this project's secrets.
//
// It sits under the CLI's own directory and is COMMITTED, like everything else
// there. At the checkout root it was a generated file that had to be ignored
// forever, and an ignored file is one nothing can see drift in — the reason the
// whole of `palbase/` is trackable now (NFR-001).
func EnvTypesPath() string { return path.Join(rootDir, envTypesFile) }

// ClientBarrelPath is the ONE line a web application imports.
//
// Every environment's generated client is committed side by side. If the app
// imported one directly, its import path would carry the environment name and
// switching environments would mean editing the application's own source. The
// barrel keeps the import stable; only its single re-export line changes.
func ClientBarrelPath() string { return path.Join(rootDir, "client.ts") }

// ConfigBarrelPath is the ONE line a Next proxy imports.
//
// It sits beside the client barrel and follows the same PALBASE_ENV switch, but
// it is a SEPARATE file on purpose: the proxy is compiled into its own bundle
// and the client barrel re-exports the generated client, which configures the
// SDK runtime at import time. Reaching that from a request path costs megabytes
// (@palbase/web measures it). This barrel points at an import-free leaf instead.
func ConfigBarrelPath() string { return path.Join(rootDir, "config.ts") }

// LegacyRoots are the directories older CLIs owned that are still THEIR OWN
// directory here.
//
// `Palbase` is deliberately NOT in this list, and its absence is the whole
// lesson: on macOS and Windows the filesystem is case-insensitive, so `palbase`
// and `Palbase` are ONE directory. Measured 11.09.2026 —
// `mkdir palbase && [ -d "Palbase" ]` is true on APFS. A gate that refused a
// checkout "carrying `Palbase/`" would therefore refuse every checkout carrying
// the NEW root, on the platform this product's customers use; and a sweeper
// that deleted it would delete the customer's new directory. The retirement of
// the visible root cannot be measured by its NAME.
func LegacyRoots() []string { return []string{".palbase"} }

// LegacyMarkers are what the OLD visible layout put inside the directory the new
// one now shares with it — files and directories the new layout never creates.
//
// This is how the visible root's retirement is measured: by content, because
// the name cannot tell the two apart. Everything here sat directly under
// `Palbase/`; the new layout writes only `environments/` and `client.ts`.
func LegacyMarkers() []string {
	return []string{"Generated", "Config", "openapi.json", "roles.json", "palbase-config.json"}
}

// CarriesLegacyLayout reports whether this checkout still holds the retired
// layout, by looking for what only that layout produced.
func CarriesLegacyLayout(root string) []string {
	var found []string
	for _, dir := range LegacyRoots() {
		if _, err := os.Stat(filepath.Join(root, dir)); err == nil {
			found = append(found, dir)
		}
	}
	for _, marker := range LegacyMarkers() {
		if _, err := os.Stat(filepath.Join(root, rootDir, marker)); err == nil {
			found = append(found, path.Join(rootDir, marker))
		}
	}
	return found
}
