package backend

// native_codegen.go — regenerate the COMMITTED Swift client after a spec fetch.
//
// The generator is NOT in this CLI. It ships with the SDK
// (palbackend-ios, `Sources/palbase-swiftgen`), so the code it emits always
// matches the SDK version this app pins: the CLI only locates the checkout
// SwiftPM already resolved, compiles it for the host once (cached by source
// hash), and runs it. One generator, one golden suite, no second copy to drift.
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
var ensureSwiftgenTool = compileSwiftgen

// locateSwiftgenSources is a seam over findSwiftgenSources so the preflight can
// be exercised without a resolved SwiftPM checkout on disk.
var locateSwiftgenSources = findSwiftgenSources

// preflightAppleGenerator reports whether an Apple checkout can regenerate,
// BEFORE anything on disk is touched.
//
// Order matters here. A refresh that writes the spec first and only then
// discovers it cannot regenerate has already broken the checkout: the committed
// Swift client no longer matches the spec beside it, and because that client
// still COMPILES the drift stays invisible until a call 404s at runtime. The
// only safe recoveries from that point are to delete the generated code (what
// discardStaleGenerated does — loud, but it leaves the app unbuildable) or to
// leave the drift in place (silent, and worse).
//
// Failing first avoids the choice entirely: nothing is written, so there is
// nothing to reconcile, and re-running after `xcodebuild -resolvePackageDependencies`
// (or one Xcode build) picks up exactly where it left off.
func preflightAppleGenerator(palbaseDir string) error {
	hasIOS := isRegularFile(filepath.Join(palbaseDir, "ios", "palbase-config.json"))
	hasMacOS := isRegularFile(filepath.Join(palbaseDir, "macos", "palbase-config.json"))
	if !hasIOS && !hasMacOS {
		return nil
	}
	if _, err := locateSwiftgenSources(filepath.Dir(palbaseDir)); err != nil {
		return fmt.Errorf("%w\n"+
			"       (nothing was written — the committed contract and Palbase/Generated/ are untouched)", err)
	}
	return nil
}

// generateAppleClient regenerates Palbase/Generated from the spec and platform
// slots under palbaseDir. It is a no-op for checkouts with no Apple slot —
// Android generates from its Gradle plugin and @palbase/web from palbe-gen.
func generateAppleClient(palbaseDir string, w io.Writer) error {
	iosCfg := filepath.Join(palbaseDir, "ios", "palbase-config.json")
	macCfg := filepath.Join(palbaseDir, "macos", "palbase-config.json")
	hasIOS, hasMacOS := isRegularFile(iosCfg), isRegularFile(macCfg)
	if !hasIOS && !hasMacOS {
		return nil
	}

	projectRoot := filepath.Dir(palbaseDir)
	outDir := filepath.Join(projectRoot, "Palbase", "Generated")
	outSwift := filepath.Join(outDir, "PalbaseGenerated.swift")
	outPlist := filepath.Join(outDir, "Palbase-Info.plist")

	tool, err := ensureSwiftgenTool(projectRoot, w)
	if err != nil {
		return discardStaleGenerated(err, w, outSwift, outPlist)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	args := []string{
		"--openapi", filepath.Join(palbaseDir, "openapi.json"),
		"--out-swift", outSwift,
		"--out-plist", outPlist,
	}
	// Every available slot is passed: one plist envelope carries both, and the
	// compiled SDK selects its own platform at runtime.
	if hasIOS {
		args = append(args, "--ios-config", iosCfg)
	}
	if hasMacOS {
		args = append(args, "--macos-config", macCfg)
	}
	cmd := exec.Command(tool, args...)
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("palbase-swiftgen: %w", err)
	}
	fmt.Fprintf(w, "✓ wrote %s\n", outSwift)
	fmt.Fprintf(w, "✓ wrote %s\n", outPlist)
	return nil
}

// discardStaleGenerated handles "spec refreshed, generator unavailable". Leaving
// yesterday's generated code beside today's spec is the one outcome worse than
// having none: it still compiles, so the drift stays invisible until a call
// 404s at runtime. Delete it and fail loudly. With nothing generated yet (the
// first link, before the SDK package is added) there is no drift to report.
func discardStaleGenerated(cause error, w io.Writer, paths ...string) error {
	var removed []string
	for _, p := range paths {
		if err := os.Remove(p); err == nil {
			removed = append(removed, p)
		}
	}
	if len(removed) == 0 {
		fmt.Fprintf(w, "note: no Swift client generated yet — %v\n", cause)
		return nil
	}
	return fmt.Errorf("removed %s: it no longer matches the spec just fetched, and could not be regenerated: %w",
		strings.Join(removed, ", "), cause)
}

// compileSwiftgen returns a host binary of the SDK's generator, compiling it on
// first use for this SDK version.
func compileSwiftgen(projectRoot string, w io.Writer) (string, error) {
	src, err := findSwiftgenSources(projectRoot)
	if err != nil {
		return "", err
	}
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	fmt.Fprintln(w, "→ compiling the SDK's Swift generator (one-time per SDK version) ...")
	// -u SDKROOT: under Xcode an inherited SDKROOT points at the device or
	// simulator SDK, and the resulting binary cannot run on the build host.
	argv := append([]string{"-u", "SDKROOT", "/usr/bin/xcrun", "swiftc"}, swiftgenSourcePaths(src)...)
	argv = append(argv, "-o", tool)
	cmd := exec.Command("/usr/bin/env", argv...)
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		removeTemp(dir)
		return "", fmt.Errorf("compile palbase-swiftgen: %w", err)
	}
	return tool, nil
}

// findSwiftgenSources locates the generator in the palbackend-ios checkout
// SwiftPM already resolved for THIS project — that is what ties the emitted
// code to the SDK version the app pins.
func findSwiftgenSources(projectRoot string) (string, error) {
	rel := filepath.Join("checkouts", "palbackend-ios", "Sources", "palbase-swiftgen")
	candidates := []string{filepath.Join(projectRoot, ".build", rel)}

	if home, err := os.UserHomeDir(); err == nil {
		derived := filepath.Join(home, "Library", "Developer", "Xcode", "DerivedData")
		for _, name := range derivedDataNames(projectRoot) {
			matches, _ := filepath.Glob(filepath.Join(derived, name+"-*", "SourcePackages", rel))
			candidates = append(candidates, newestFirst(matches)...)
		}
	}
	for _, dir := range candidates {
		if hasSwiftgenSources(dir) {
			return dir, nil
		}
	}
	return "", fmt.Errorf("the palbackend-ios checkout is not resolved for this project yet — " +
		"build once in Xcode, or run `xcodebuild -resolvePackageDependencies`")
}

// derivedDataNames lists the names Xcode may have used for this project's
// DerivedData directory: its workspace, its project, then the directory itself.
func derivedDataNames(projectRoot string) []string {
	var names []string
	for _, ext := range []string{"*.xcworkspace", "*.xcodeproj"} {
		matches, _ := filepath.Glob(filepath.Join(projectRoot, ext))
		for _, m := range matches {
			names = append(names, strings.TrimSuffix(filepath.Base(m), filepath.Ext(m)))
		}
	}
	return append(names, filepath.Base(projectRoot))
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

// newestFirst orders checkout candidates by mtime so the most recently resolved
// DerivedData directory wins when a project has several.
func newestFirst(paths []string) []string {
	sort.SliceStable(paths, func(i, j int) bool {
		a, errA := os.Stat(paths[i])
		b, errB := os.Stat(paths[j])
		if errA != nil || errB != nil {
			return errA == nil
		}
		return a.ModTime().After(b.ModTime())
	})
	return paths
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
