package backend

import (
	"bytes"
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
// the spec plus the requested platform slots.
func linkedProject(t *testing.T, platforms ...string) string {
	t.Helper()
	root := t.TempDir()
	palbaseDir := filepath.Join(root, ".palbase")
	require.NoError(t, os.MkdirAll(palbaseDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(palbaseDir, "openapi.json"), []byte(`{"openapi":"3.1.0"}`), 0o644))
	for _, p := range platforms {
		dir := filepath.Join(palbaseDir, p)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "palbase-config.json"), []byte(`{}`), 0o644))
	}
	return root
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

func TestGenerateAppleClient_NoAppleSlotIsNoop(t *testing.T) {
	root := linkedProject(t, "android")
	useStub(t, "/nonexistent/should-never-run", nil)

	var out bytes.Buffer
	require.NoError(t, generateAppleClient(filepath.Join(root, ".palbase"), &out))

	// Android and web generate from their own toolchains; the CLI must not
	// invent an Apple client for them.
	require.NoDirExists(t, filepath.Join(root, "Palbase"))
	require.Empty(t, out.String())
}

func TestGenerateAppleClient_PassesEverySlotAndWritesCommittedOutput(t *testing.T) {
	root := linkedProject(t, "ios", "macos")
	argvLog := filepath.Join(t.TempDir(), "argv")
	useStub(t, stubSwiftgen(t, argvLog), nil)

	var out bytes.Buffer
	require.NoError(t, generateAppleClient(filepath.Join(root, ".palbase"), &out))

	argv, err := os.ReadFile(argvLog)
	require.NoError(t, err)
	got := strings.Split(strings.TrimSpace(string(argv)), "\n")
	outSwift := filepath.Join(root, "Palbase", "Generated", "PalbaseGenerated.swift")
	outPlist := filepath.Join(root, "Palbase", "Generated", "Palbase-Info.plist")
	require.Equal(t, []string{
		"--openapi", filepath.Join(root, ".palbase", "openapi.json"),
		"--out-swift", outSwift,
		"--out-plist", outPlist,
		"--ios-config", filepath.Join(root, ".palbase", "ios", "palbase-config.json"),
		"--macos-config", filepath.Join(root, ".palbase", "macos", "palbase-config.json"),
	}, got)

	// Committed, not DerivedData: the whole point is that this is diffable.
	require.FileExists(t, outSwift)
	require.FileExists(t, outPlist)
}

func TestGenerateAppleClient_OnlyLinkedSlotIsPassed(t *testing.T) {
	root := linkedProject(t, "ios")
	argvLog := filepath.Join(t.TempDir(), "argv")
	useStub(t, stubSwiftgen(t, argvLog), nil)

	require.NoError(t, generateAppleClient(filepath.Join(root, ".palbase"), &bytes.Buffer{}))

	argv, err := os.ReadFile(argvLog)
	require.NoError(t, err)
	require.Contains(t, string(argv), "--ios-config")
	require.NotContains(t, string(argv), "--macos-config")
}

func TestGenerateAppleClient_StaleOutputIsDeletedAndReported(t *testing.T) {
	root := linkedProject(t, "ios")
	genDir := filepath.Join(root, "Palbase", "Generated")
	require.NoError(t, os.MkdirAll(genDir, 0o755))
	stale := filepath.Join(genDir, "PalbaseGenerated.swift")
	require.NoError(t, os.WriteFile(stale, []byte("// generated from yesterday's spec"), 0o644))
	useStub(t, "", errNoCheckout)

	err := generateAppleClient(filepath.Join(root, ".palbase"), &bytes.Buffer{})

	// Stale generated code still COMPILES, so silently keeping it hides the
	// drift until a call 404s at runtime. It must be gone and the run must fail.
	require.Error(t, err)
	require.NoFileExists(t, stale)
	require.Contains(t, err.Error(), "no longer matches the spec")
}

func TestGenerateAppleClient_FirstLinkWithoutSDKIsNotAnError(t *testing.T) {
	root := linkedProject(t, "ios")
	useStub(t, "", errNoCheckout)

	var out bytes.Buffer
	// The very first `palbase ios link` runs BEFORE the SDK package is added, so
	// there is no checkout yet and nothing generated to go stale. The spec fetch
	// must still succeed.
	require.NoError(t, generateAppleClient(filepath.Join(root, ".palbase"), &out))
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
