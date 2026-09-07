package debugconsole

// Two defects, both measured on a real machine on 2026-09-08.
//
//  1. `.Output()` throws away stderr, which is the only place simctl writes its
//     diagnosis. `xcrun simctl get_app_container booted com.centauri.dev data`
//     says "An error was encountered processing the command
//     (domain=NSPOSIXErrorDomain, code=2): The operation couldn't be completed.
//     No such file or directory"; `palbase debug tail --app com.centauri.dev`
//     said "exit status 2".
//
//  2. "The booted one" is not a thing when two are booted. With `iPhone 17 Pro`
//     and `PennySDK0552QA` both up, the CLI inspected whichever came first and
//     reported "no Palbe console data found on the simulator" — a filter that
//     never matched, reporting silence, and then telling the developer to run
//     the app they had just run.
//
// Nothing here shells out to a real simctl. The command runner is a seam
// (runSimctl), and the one test that must exercise the REAL runner puts a fake
// `xcrun` on PATH — because a stub of the runner cannot prove that the runner
// captures stderr.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	deviceA = "D51D94F3-6B2C-4C1E-9A0F-1111AAAA2222"
	deviceB = "4BDE8FCA-77E1-4D3B-8C55-3333BBBB4444"

	// What simctl really printed, copied from the terminal.
	simctlNoSuchContainer = "An error was encountered processing the command " +
		"(domain=NSPOSIXErrorDomain, code=2):\nThe operation couldn't be completed. " +
		"No such file or directory"
)

func twoBootedPayload() []byte {
	return []byte(`{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-26-0":[
		{"udid":"` + deviceA + `","state":"Booted","name":"iPhone 17 Pro"},
		{"udid":"` + deviceB + `","state":"Booted","name":"PennySDK0552QA"}]}}`)
}

// stubSimctl replaces the command runner for one test.
func stubSimctl(t *testing.T, fn func(args ...string) ([]byte, error)) {
	t.Helper()
	original := runSimctl
	runSimctl = fn
	t.Cleanup(func() { runSimctl = original })
}

// bootedListing answers the device listing and delegates everything else.
func bootedListing(rest func(args ...string) ([]byte, error)) func(args ...string) ([]byte, error) {
	return func(args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "list" && args[1] == "devices" {
			return twoBootedPayload(), nil
		}
		if rest == nil {
			return nil, fmt.Errorf("unexpected simctl call: %v", args)
		}
		return rest(args...)
	}
}

func simulatorHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func deviceAppRoot(home, udid, appDir string) string {
	return filepath.Join(home, "Library/Developer/CoreSimulator/Devices", udid,
		"data/Containers/Data/Application", appDir)
}

// writeSessionOn plants a console session inside one device's app container,
// the way the SDK's ConsoleStorage does.
func writeSessionOn(t *testing.T, home, udid, appDir, sessionID string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(deviceAppRoot(home, udid, appDir), consoleDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(realMessageLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

// DEFECT 2, without --app. The records are on the SECOND booted device, which
// is exactly the case that reported silence.
func TestASessionOnAnotherBootedDeviceIsFound(t *testing.T) {
	home := simulatorHome(t)
	want := writeSessionOn(t, home, deviceB, "APP-ON-B", "session-b", time.Minute)
	stubSimctl(t, bootedListing(nil))

	got, err := locateSession("", "")
	if err != nil {
		t.Fatalf("locateSession: %v", err)
	}
	if got.path != want {
		t.Fatalf("found %q, want the session on the second booted device %q", got.path, want)
	}
}

// With records on BOTH, the newest is the one the developer means — and the
// header line has to say which device it came from, or the answer is ambiguous
// in a different way.
func TestTheNewestSessionAcrossBootedDevicesWins(t *testing.T) {
	home := simulatorHome(t)
	writeSessionOn(t, home, deviceA, "APP-ON-A", "session-a", time.Hour)
	want := writeSessionOn(t, home, deviceB, "APP-ON-B", "session-b", time.Minute)
	stubSimctl(t, bootedListing(nil))

	got, err := locateSession("", "")
	if err != nil {
		t.Fatalf("locateSession: %v", err)
	}
	if got.path != want {
		t.Fatalf("found %q, want the newest session %q", got.path, want)
	}
	if !strings.Contains(got.describe(), "PennySDK0552QA") {
		t.Fatalf("the header does not name the device the records came from: %q", got.describe())
	}
}

// --device still means exactly that device, even when others are booted: an
// operator who names one is not asking us to search.
func TestAnExplicitDeviceIsTheOnlyOneSearched(t *testing.T) {
	home := simulatorHome(t)
	writeSessionOn(t, home, deviceB, "APP-ON-B", "session-b", time.Minute)
	stubSimctl(t, func(args ...string) ([]byte, error) {
		t.Fatalf("--device must not need a device listing, got simctl %v", args)
		return nil, nil
	})

	if _, err := locateSession(deviceA, ""); err == nil {
		t.Fatal("searching the named device found records that live on another one")
	}
	got, err := locateSession(deviceB, "")
	if err != nil {
		t.Fatalf("locateSession on the named device: %v", err)
	}
	if !strings.HasPrefix(got.path, deviceAppRoot(home, deviceB, "APP-ON-B")) {
		t.Fatalf("found %q, want the named device's session", got.path)
	}
}

// DEFECT 2, with --app. simctl is asked per DEVICE, never handed the literal
// "booted" — which is ambiguous to simctl itself when two are up.
func TestABundleOnlyOnTheSecondBootedDeviceIsFound(t *testing.T) {
	home := simulatorHome(t)
	want := writeSessionOn(t, home, deviceB, "APP-ON-B", "session-b", time.Minute)
	var asked []string
	stubSimctl(t, bootedListing(func(args ...string) ([]byte, error) {
		if args[0] != "get_app_container" {
			return nil, fmt.Errorf("unexpected simctl call: %v", args)
		}
		asked = append(asked, args[1])
		if args[1] == deviceA {
			return nil, fmt.Errorf("exit status 2: %s", simctlNoSuchContainer)
		}
		return []byte(deviceAppRoot(home, deviceB, "APP-ON-B") + "\n"), nil
	}))

	got, err := locateSession("", "com.centauri.dev")
	if err != nil {
		t.Fatalf("locateSession: %v", err)
	}
	if got.path != want {
		t.Fatalf("found %q, want %q", got.path, want)
	}
	for _, udid := range asked {
		if udid == "booted" {
			t.Fatal("simctl was handed the literal \"booted\" while two devices are up — the ambiguity was delegated, not resolved")
		}
	}
	if len(asked) != 2 {
		t.Fatalf("asked simctl about %v, want both booted devices", asked)
	}
}

// DEFECT 1. When the container is nowhere, the error must carry the CLI's
// framing AND the sentence the tool wrote — framing first, the actual diagnosis
// last — plus the devices that were searched.
func TestNoContainerAnywhereRepeatsWhatSimctlSaid(t *testing.T) {
	simulatorHome(t)
	stubSimctl(t, bootedListing(func(args ...string) ([]byte, error) {
		return nil, fmt.Errorf("exit status 2: %s", simctlNoSuchContainer)
	}))

	_, err := locateSession("", "com.centauri.dev")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	for _, want := range []string{
		"com.centauri.dev",
		"No such file or directory",
		"iPhone 17 Pro",
		"PennySDK0552QA",
		deviceA,
		deviceB,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error never mentions %q:\n%s", want, msg)
		}
	}
	if strings.HasPrefix(msg, "exit status") {
		t.Errorf("the CLI's framing was dropped:\n%s", msg)
	}
}

// The "nothing here" message must not send somebody to run an app they already
// ran without saying WHERE it looked.
func TestNoConsoleDataNamesTheDevicesSearched(t *testing.T) {
	simulatorHome(t)
	stubSimctl(t, bootedListing(nil))

	_, err := locateSession("", "")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"iPhone 17 Pro", "PennySDK0552QA", deviceA, deviceB} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error never says it searched %q:\n%s", want, err)
		}
	}
}

func TestBootedDevicesReturnsEveryBootedOne(t *testing.T) {
	devices, err := bootedDevices(twoBootedPayload())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d booted devices, want 2", len(devices))
	}
	if devices[0].UDID != deviceA || devices[1].UDID != deviceB {
		t.Fatalf("devices came back as %+v", devices)
	}
	if devices[0].Name != "iPhone 17 Pro" {
		t.Fatalf("the name is what a person recognises, got %q", devices[0].Name)
	}

	// Shutdown devices are not booted, and none booted still says the way out.
	only, err := bootedDevices([]byte(`{"devices":{"rt":[{"udid":"AAA","state":"Shutdown","name":"iPhone 15"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 0 {
		t.Fatalf("a shutdown device was reported as booted: %+v", only)
	}
	stubSimctl(t, func(args ...string) ([]byte, error) {
		return []byte(`{"devices":{"rt":[{"udid":"AAA","state":"Shutdown","name":"iPhone 15"}]}}`), nil
	})
	if _, err := targetDevices(""); err == nil || !strings.Contains(err.Error(), "--device") {
		t.Errorf("with nothing booted the error must name the way out, got: %v", err)
	}
}

// The runner itself. A STUB of runSimctl cannot prove that the real runner
// keeps stderr, so this one drives the real exec path against a fake `xcrun` on
// PATH — the same shape as the tool, none of the simulator.
func TestTheRealRunnerKeepsWhatTheToolWroteToStderr(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf 'An error was encountered processing the command (domain=NSPOSIXErrorDomain, code=2):\\n' >&2\n" +
		"printf \"The operation couldn't be completed. No such file or directory\\n\" >&2\n" +
		"exit 2\n"
	if err := os.WriteFile(filepath.Join(bin, "xcrun"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := execSimctl("get_app_container", deviceA, "com.centauri.dev", "data")
	if err == nil {
		t.Fatal("expected the fake xcrun's failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "No such file or directory") {
		t.Errorf("the tool's own sentence was discarded — this is the defect:\n%s", msg)
	}
	if !strings.Contains(msg, "exit status 2") {
		t.Errorf("the exit status is still worth carrying:\n%s", msg)
	}
	if strings.HasSuffix(msg, "\n") || strings.HasSuffix(msg, " ") {
		t.Errorf("trailing whitespace survived into the error: %q", msg)
	}
}

// And a successful run returns stdout, with nothing from stderr mixed in: the
// container path is PARSED, so a tool that warns on stderr must not corrupt it.
func TestTheRealRunnerReturnsOnlyStdout(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf 'a deprecation warning\\n' >&2\n" +
		"printf '/Users/x/Containers/Data/Application/ABC\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "xcrun"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := execSimctl("get_app_container", deviceA, "com.centauri.dev", "data")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "/Users/x/Containers/Data/Application/ABC" {
		t.Fatalf("stdout came back as %q", got)
	}
}
