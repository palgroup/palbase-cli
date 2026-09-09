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

import "path"

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

// ConfigPath is the app configuration that environment's platform reads.
//
// Apple's is a plist because the app BUNDLES it and the SDK reads it out of the
// bundle at first use; the others are JSON read by their generator.
func ConfigPath(env, platform string) string {
	switch platform {
	case "ios", "macos":
		return path.Join(EnvDir(env), "Palbase-Info.plist")
	default:
		return path.Join(EnvDir(env), platform+"-config.json")
	}
}

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

// ClientBarrelPath is the ONE line a web application imports.
//
// Every environment's generated client is committed side by side. If the app
// imported one directly, its import path would carry the environment name and
// switching environments would mean editing the application's own source. The
// barrel keeps the import stable; only its single re-export line changes.
func ClientBarrelPath() string { return path.Join(rootDir, "client.ts") }

// LegacyRoots are the directories older CLIs owned in a checkout.
//
// They are named so `link` can REFUSE a checkout that still carries one. There
// is no reader for them and no migration: a half-old, half-new tree is the one
// outcome that makes "I will clean the old ones up myself" impossible.
func LegacyRoots() []string { return []string{".palbase", "Palbase"} }
