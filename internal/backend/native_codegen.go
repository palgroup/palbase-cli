package backend

// native_codegen.go — regenerate the COMMITTED Swift client after a spec fetch.
//
// The generator is NOT in this CLI. It ships with the SDK (palbackend-ios,
// `Sources/palbase-swiftgen`), so the code it emits always matches the SDK
// version this app pins: one generator, one golden suite, no second copy to
// drift. Each SDK release carries it prebuilt (swiftgen_release.go) for the
// version the project pins (swiftgen_pin.go); an app that links the SDK by
// local path gets it compiled from that source instead, because a local package
// has no release.
//
// The output is COMMITTED under Palbase/Generated/ rather than produced at
// build time. Build-time output lands in DerivedData, where it is invisible to
// `git diff`, to the editor before a first build, and to anyone — or anything —
// reading the repo to learn which `pb.<ns>.<op>` calls actually exist. Committed
// output makes a spec change show up as a reviewable diff.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// swiftgenEntryPoint is the one file the generator directory must contain for
// us to recognise it. Everything else compiled is whatever *.swift sits beside
// it: the SDK owns its own file list, and a generator that grows a file (0.28.0
// added Purchases.swift) must not break every app's codegen because this CLI
// hardcoded four names.
const swiftgenEntryPoint = "main.swift"

// swiftgenToolHome is the CLI-owned tool cache root, shared with the pinned
// TypeScript parser (~/.palbase/tools). A var only so tests can redirect it.
var swiftgenToolHome = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".palbase", "tools"), nil
}

// ensureSwiftgenTool is a seam: tests substitute a stub generator so they can
// assert the arguments the CLI passes without a Swift toolchain.
var ensureSwiftgenTool = provideSwiftgen

// provideSwiftgen returns a generator that runs on this machine for the SDK
// this project builds: compiled from the SDK's source when the project links it
// by local path, otherwise the one released with the version it pins.
//
// THERE IS NO THIRD PLACE. The checkout Xcode resolved under DerivedData used to
// be the source for a published SDK; it exists only on a Mac that has already
// built the app, so a Linux shell or a fresh clone could never generate
// (palbase-cli#9), and it could outlive the version the project pinned.
func provideSwiftgen(projectRoot string, w io.Writer) (string, error) {
	if src := localSwiftgenSource(projectRoot); src != "" {
		return compileSwiftgen(src, w)
	}
	v, err := resolveSDKVersion(projectRoot, w)
	if err != nil {
		return "", err
	}
	return downloadSwiftgen(v, w)
}

// discardStaleGenerated handles "spec refreshed, generator unavailable". Leaving
// yesterday's generated code beside today's spec is the one outcome worse than
// having none: it still compiles, so the drift stays invisible until a call
// 404s at runtime. Delete it and fail loudly, including on the first link.
func discardStaleGenerated(cause error, w io.Writer, paths ...string) error {
	var removed []string
	for _, p := range paths {
		if err := os.Remove(p); err == nil {
			removed = append(removed, p)
		}
	}
	if len(removed) == 0 {
		return fmt.Errorf("cannot generate the Swift client and app configuration: %w", cause)
	}
	return fmt.Errorf("removed %s: it no longer matches the spec just fetched, and could not be regenerated: %w",
		strings.Join(removed, ", "), cause)
}

// compileSwiftgen returns a host binary of the generator in src — a local SDK
// package's — compiling it on first use for those sources.
func compileSwiftgen(src string, w io.Writer) (string, error) {
	sum, err := hashFiles(swiftgenSourcePaths(src))
	if err != nil {
		return "", err
	}
	cacheRoot, err := swiftgenToolHome()
	if err != nil {
		return "", err
	}
	// The source hash IS the version, so "is this the right build?" and "does it
	// exist?" are the same question — no version sniffing, no upgrade path.
	dir := filepath.Join(cacheRoot, "swiftgen-"+sum)
	tool := filepath.Join(dir, "palbase-swiftgen")
	if isRegularFile(tool) {
		return tool, nil
	}
	compiler, err := swiftCompiler()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	fmt.Fprintln(w, "→ compiling the local SDK's Swift generator (one-time per source change) ...")
	argv := append(append([]string{}, compiler[1:]...), swiftgenSourcePaths(src)...)
	argv = append(argv, "-o", tool)
	cmd := exec.Command(compiler[0], argv...)
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		removeTemp(dir)
		return "", fmt.Errorf("compile palbase-swiftgen: %w", err)
	}
	return tool, nil
}

// swiftCompiler is the host's Swift compiler command. A local package has no
// release to download a generator from, so its source has to be compiled here.
func swiftCompiler() ([]string, error) {
	if runtime.GOOS == "darwin" {
		// -u SDKROOT: under Xcode an inherited SDKROOT points at the device or
		// simulator SDK, and the resulting binary cannot run on the build host.
		return []string{"/usr/bin/env", "-u", "SDKROOT", "/usr/bin/xcrun", "swiftc"}, nil
	}
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		return nil, fmt.Errorf("this project links palbackend-ios by local path, so its Swift generator is compiled "+
			"from that source, and that needs a Swift toolchain (swiftc) on PATH: %w", err)
	}
	return []string{swiftc}, nil
}

// localSwiftgenSources returns the generator directory of every SDK package
// this project links BY PATH.
//
// Read from the project's own manifest, because that is the only place a local
// package is recorded: SwiftPM's workspace state lists what it RESOLVED — remote
// packages it had to fetch — and a package already on disk is never in it. That
// absence is easy to mistake for "no local package", which is how a project that
// had switched to the SDK source kept generating from a leftover checkout of the
// published one.
func localSwiftgenSources(projectRoot string) []string {
	var out []string
	for _, ref := range localPackageRefs(projectRoot) {
		out = append(out, resolveSwiftgenDir(projectRoot, ref))
	}
	return out
}

// localSwiftgenSource is the generator of the SDK package this project links by
// path, or "" when it links none (or the path holds no generator on this
// machine).
func localSwiftgenSource(projectRoot string) string {
	for _, src := range localSwiftgenSources(projectRoot) {
		if hasSwiftgenSources(src) {
			return src
		}
	}
	return ""
}

// localPackageRefs lists every package path the project links BY PATH: an Xcode
// project's XCLocalSwiftPackageReference, a Swift package's .package(path:).
func localPackageRefs(projectRoot string) []string {
	var out []string
	projects, _ := filepath.Glob(filepath.Join(projectRoot, "*.xcodeproj", "project.pbxproj"))
	for _, pbxproj := range projects {
		blob, err := os.ReadFile(pbxproj)
		if err != nil {
			continue
		}
		for _, m := range localPackagePath.FindAllStringSubmatch(string(blob), -1) {
			out = append(out, m[1])
		}
	}
	if blob, err := os.ReadFile(filepath.Join(projectRoot, "Package.swift")); err == nil {
		for _, m := range packageByPath.FindAllStringSubmatch(string(blob), -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// localPackagePath matches a pbxproj's `relativePath = …;`, quoted or not.
//
// Xcode omits the quotes when a path needs none, and a pattern that only
// matched the quoted form found nothing in exactly the projects that had been
// pointed at a local SDK by hand — which is when this lookup matters most.
var localPackagePath = regexp.MustCompile(`relativePath = "?([^";
]+)"?;`)

// packageByPath matches `.package(path: "../somewhere")` in a Package.swift.
var packageByPath = regexp.MustCompile(`\.package\(\s*path:\s*"([^"]+)"`)

// resolveSwiftgenDir turns a manifest's package path into the generator inside
// it. Paths in a manifest are relative to the project, so they are resolved
// against it rather than against whatever directory the CLI was run from.
func resolveSwiftgenDir(projectRoot, packagePath string) string {
	if !filepath.IsAbs(packagePath) {
		packagePath = filepath.Join(projectRoot, packagePath)
	}
	return filepath.Join(packagePath, "Sources", "palbase-swiftgen")
}

// swiftgenSourcePaths is every Swift file in the generator directory, sorted so
// the compile command — and the cache hash built from it — are deterministic.
func swiftgenSourcePaths(dir string) []string {
	paths, err := filepath.Glob(filepath.Join(dir, "*.swift"))
	if err != nil {
		return nil
	}
	sort.Strings(paths)
	return paths
}

func hasSwiftgenSources(dir string) bool {
	return isRegularFile(filepath.Join(dir, swiftgenEntryPoint)) && len(swiftgenSourcePaths(dir)) > 0
}

// hashFiles digests the generator sources so a changed SDK compiles to a new
// cache entry. Names are hashed too, so reordering or renaming is not a
// collision.
func hashFiles(paths []string) (string, error) {
	h := sha256.New()
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s:%d:", filepath.Base(p), len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
