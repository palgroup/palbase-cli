package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubSwiftgen stands in for the compiled generator: it records its argv and
// writes whatever --out-swift / --out-plist name, so a test can assert the
// contract the CLI passes across the process boundary without a Swift toolchain.
func stubSwiftgen(t *testing.T, argvLog string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub-swiftgen")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "` + argvLog + `"
while [ $# -gt 0 ]; do
  case "$1" in
    --out-swift|--out-plist) printf 'generated\n' > "$2"; shift 2 ;;
    *) shift ;;
  esac
done
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// linkedProject lays out a checkout as the fetch path leaves it: .palbase with
// ONE CONTRACT PER ENVIRONMENT plus the requested platform slots. It also makes
// that checkout the working directory, because generateForEnvironments emits
// relative to it — the same way every real invocation runs.
func linkedProject(t *testing.T, platforms ...string) string {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	palbaseDir := filepath.Join(root, ".palbase")
	require.NoError(t, os.MkdirAll(palbaseDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(palbaseDir, "openapi"), 0o755))
	require.NoError(t, os.WriteFile(specPath("main"), []byte(`{"openapi":"3.1.0"}`), 0o644))
	for _, p := range platforms {
		dir := filepath.Join(palbaseDir, p)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "palbase-config.json"), []byte(`{}`), 0o644))
	}
	return root
}

// oneEnvironment is the environment set a linkedProject carries.
func oneEnvironment() appEnvironments {
	return appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: "app_ios", BaseURL: "https://app1prod.dev.palbase.studio", APIKey: "pb_project_c0"},
		},
	}
}

// errNoCheckout stands for the real "SDK package not resolved yet" failure.
var errNoCheckout = errors.New("the palbackend-ios checkout is not resolved for this project yet")

// useStub points the generator seam at a stub for one test.
func useStub(t *testing.T, tool string, err error) {
	t.Helper()
	prev := ensureSwiftgenTool
	ensureSwiftgenTool = func(string, io.Writer) (string, error) { return tool, err }
	t.Cleanup(func() { ensureSwiftgenTool = prev })
}

func TestGenerateForEnvironments_NoAppleSlotWritesNoPlist(t *testing.T) {
	root := linkedProject(t, "android")
	useStub(t, stubSwiftgen(t, filepath.Join(t.TempDir(), "argv")), nil)

	var out bytes.Buffer
	require.NoError(t, generateForEnvironments(context.Background(), oneEnvironment(), &out))

	// Android generates its client from the Gradle plugin and has no plist: the
	// CLI must not invent an Apple envelope for it.
	require.NoFileExists(t, filepath.Join(root, "Palbase", "Generated", "Palbase-Info.plist"))
}

// THE TWO HALVES ARE REQUESTED SEPARATELY, and that is the contract the
// generator publishes: one client PER ENVIRONMENT, one plist ONCE for all of
// them. Asking for the plist alongside every client would write the same bytes N
// times and read as though the environment mattered to it.
func TestGenerateForEnvironments_ClientPerEnvironmentAndOnePlist(t *testing.T) {
	root := linkedProject(t, "ios", "macos")
	argvLog := filepath.Join(t.TempDir(), "argv")
	useStub(t, stubSwiftgen(t, argvLog), nil)

	var out bytes.Buffer
	require.NoError(t, generateForEnvironments(context.Background(), oneEnvironment(), &out))

	argv, err := os.ReadFile(argvLog)
	require.NoError(t, err)
	got := strings.Split(strings.TrimSpace(string(argv)), "\n")
	outSwift := filepath.Join(root, "Palbase", "Generated", "main", "PalbaseGenerated.swift")
	outPlist := filepath.Join(root, "Palbase", "Generated", "Palbase-Info.plist")
	require.Equal(t, []string{
		"--openapi", specPath("main"),
		"--out-swift", outSwift,
		"--out-plist", outPlist,
		"--ios-config", filepath.Join(".palbase", "ios", "palbase-config.json"),
		"--macos-config", filepath.Join(".palbase", "macos", "palbase-config.json"),
	}, got)

	// Committed, not DerivedData: the whole point is that this is diffable.
	require.FileExists(t, outSwift)
	require.FileExists(t, outPlist)
}

func TestGenerateForEnvironments_OnlyLinkedSlotIsPassed(t *testing.T) {
	linkedProject(t, "ios")
	argvLog := filepath.Join(t.TempDir(), "argv")
	useStub(t, stubSwiftgen(t, argvLog), nil)

	require.NoError(t, generateForEnvironments(context.Background(), oneEnvironment(), &bytes.Buffer{}))

	argv, err := os.ReadFile(argvLog)
	require.NoError(t, err)
	require.Contains(t, string(argv), "--ios-config")
	require.NotContains(t, string(argv), "--macos-config")
}

func TestGenerateForEnvironments_StaleOutputIsDeletedAndReported(t *testing.T) {
	root := linkedProject(t, "ios")
	genDir := filepath.Join(root, "Palbase", "Generated", "main")
	require.NoError(t, os.MkdirAll(genDir, 0o755))
	stale := filepath.Join(genDir, "PalbaseGenerated.swift")
	require.NoError(t, os.WriteFile(stale, []byte("// generated from yesterday's spec"), 0o644))
	useStub(t, "", errNoCheckout)

	err := generateForEnvironments(context.Background(), oneEnvironment(), &bytes.Buffer{})

	// Stale generated code still COMPILES, so silently keeping it hides the
	// drift until a call 404s at runtime. It must be gone and the run must fail.
	require.Error(t, err)
	require.NoFileExists(t, stale)
	require.Contains(t, err.Error(), "no longer matches the spec")
}

func TestGenerateForEnvironments_FirstLinkWithoutSDKIsNotAnError(t *testing.T) {
	linkedProject(t, "ios")
	useStub(t, "", errNoCheckout)

	var out bytes.Buffer
	// The very first `palbase ios link` runs BEFORE the SDK package is added, so
	// there is no checkout yet and nothing generated to go stale. The spec fetch
	// must still succeed.
	require.NoError(t, generateForEnvironments(context.Background(), oneEnvironment(), &out))
	require.Contains(t, out.String(), "no Swift client generated yet")
}

func TestFindSwiftgenSources(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, ".build", "checkouts", "palbackend-ios", "Sources", "palbase-swiftgen")
	require.NoError(t, os.MkdirAll(src, 0o755))

	_, err := findSwiftgenSources(root)
	require.Error(t, err, "an empty checkout dir is not a generator")
	require.Contains(t, err.Error(), "resolvePackageDependencies")

	// A directory is the generator when it has the entry point; every other
	// *.swift beside it comes along, so an SDK that adds a file (0.28.0 added
	// Purchases.swift) still compiles instead of failing on a stale name list.
	for _, name := range []string{"main.swift", "Parse.swift", "Emit.swift", "Plist.swift", "Purchases.swift"} {
		require.NoError(t, os.WriteFile(filepath.Join(src, name), []byte("// swift"), 0o644))
	}
	found, err := findSwiftgenSources(root)
	require.NoError(t, err)
	require.Equal(t, src, found)
	require.Len(t, swiftgenSourcePaths(found), 5, "every generator source is compiled, not a fixed four")
}

func TestHashFilesTracksContent(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "a.swift"), filepath.Join(dir, "b.swift")}
	require.NoError(t, os.WriteFile(paths[0], []byte("one"), 0o644))
	require.NoError(t, os.WriteFile(paths[1], []byte("two"), 0o644))
	first, err := hashFiles(paths)
	require.NoError(t, err)

	// A different SDK version must compile to a different cache entry, or the
	// CLI keeps running yesterday's generator forever.
	require.NoError(t, os.WriteFile(paths[1], []byte("three"), 0o644))
	second, err := hashFiles(paths)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}

// TestCompileSwiftgen_RealToolchain crosses the real CLI→swiftc→binary boundary:
// it compiles actual Swift sources with the same argv production uses, runs the
// result, and checks the CLI's flags arrived. A stub cannot catch a broken
// compile invocation (the SDKROOT trap) or a cache that never hits.
func TestCompileSwiftgen_RealToolchain(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles Swift; -short skips it")
	}
	if _, err := exec.LookPath("xcrun"); err != nil {
		t.Skip("no xcrun on this host")
	}
	root := t.TempDir()
	src := filepath.Join(root, ".build", "checkouts", "palbackend-ios", "Sources", "palbase-swiftgen")
	require.NoError(t, os.MkdirAll(src, 0o755))
	// A miniature stand-in for the SDK generator: same file names, same flag,
	// real Swift.
	bodies := map[string]string{
		"main.swift":  "import Foundation\nprint(marker() + CommandLine.arguments.dropFirst().joined(separator: \" \"))\n",
		"Parse.swift": "func marker() -> String { \"swiftgen:\" }\n",
		"Emit.swift":  "// unused\n",
		"Plist.swift": "// unused\n",
	}
	for name, body := range bodies {
		require.NoError(t, os.WriteFile(filepath.Join(src, name), []byte(body), 0o644))
	}
	cache := t.TempDir()
	prev := swiftgenToolHome
	swiftgenToolHome = func() (string, error) { return cache, nil }
	t.Cleanup(func() { swiftgenToolHome = prev })

	var out bytes.Buffer
	tool, err := compileSwiftgen(root, &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), "compiling the SDK's Swift generator")

	got, err := exec.Command(tool, "--openapi", "spec.json").CombinedOutput()
	require.NoError(t, err)
	require.Equal(t, "swiftgen:--openapi spec.json", strings.TrimSpace(string(got)))

	// Second call must hit the cache: same path, no recompile message.
	out.Reset()
	again, err := compileSwiftgen(root, &out)
	require.NoError(t, err)
	require.Equal(t, tool, again)
	require.Empty(t, out.String())
}

func TestALocalSDKPackageIsFoundBeforeAStaleCheckout(t *testing.T) {
	// The failure this locks out: a project switches from the published SDK to
	// the SDK source, and keeps generating from the checkout DerivedData still
	// holds. The emitted client then matches a version the app no longer links —
	// silently, because both directories contain a working generator.
	root := t.TempDir()
	sdk := filepath.Join(root, "sdk-source")
	generator := filepath.Join(sdk, "Sources", "palbase-swiftgen")
	require.NoError(t, os.MkdirAll(generator, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(generator, "main.swift"), []byte("// generator"), 0o644))

	project := filepath.Join(root, "App.xcodeproj")
	require.NoError(t, os.MkdirAll(project, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, "project.pbxproj"),
		[]byte(`isa = XCLocalSwiftPackageReference;
			relativePath = "sdk-source";`), 0o644))

	found, err := findSwiftgenSources(root)
	require.NoError(t, err)
	require.Equal(t, generator, found)
}

func TestAPackageSwiftLocalDependencyIsFoundToo(t *testing.T) {
	root := t.TempDir()
	sdk := filepath.Join(root, "..", "sdk-src")
	generator := filepath.Join(sdk, "Sources", "palbase-swiftgen")
	require.NoError(t, os.MkdirAll(generator, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(generator, "main.swift"), []byte("// generator"), 0o644))
	t.Cleanup(func() { _ = os.RemoveAll(sdk) })

	require.NoError(t, os.WriteFile(filepath.Join(root, "Package.swift"),
		[]byte(`.package(path: "../sdk-src"),`), 0o644))

	found, err := findSwiftgenSources(root)
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(generator), filepath.Clean(found))
}

func TestAnUnquotedPackagePathIsFoundToo(t *testing.T) {
	// Xcode writes the quotes only when the path needs them. A pattern that
	// matched the quoted form alone found nothing in exactly the projects that
	// had been pointed at a local SDK by hand — and then `palbase spec` DELETED
	// the committed client, because it could not regenerate what it had just
	// invalidated.
	root := t.TempDir()
	generator := filepath.Join(root, "sdk", "Sources", "palbase-swiftgen")
	require.NoError(t, os.MkdirAll(generator, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(generator, "main.swift"), []byte("// generator"), 0o644))

	project := filepath.Join(root, "App.xcodeproj")
	require.NoError(t, os.MkdirAll(project, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, "project.pbxproj"),
		[]byte("isa = XCLocalSwiftPackageReference;\n\t\t\trelativePath = sdk;\n"), 0o644))

	found, err := findSwiftgenSources(root)
	require.NoError(t, err)
	require.Equal(t, generator, found)
}
