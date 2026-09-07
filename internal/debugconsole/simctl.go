package debugconsole

// Talking to `xcrun simctl`, and the two things that has to get right.
//
// STDERR IS THE DIAGNOSIS. simctl writes its reason to stderr and nothing to
// stdout when it fails, so a caller using `.Output()` — which captures stdout
// and drops stderr — keeps the exit code and throws away the sentence. Measured
// 2026-09-08: the tool said
//
//	An error was encountered processing the command (domain=NSPOSIXErrorDomain, code=2):
//	The operation couldn't be completed. No such file or directory
//
// and `palbase debug tail --app com.centauri.dev` said `exit status 2`.
//
// "BOOTED" IS NOT ONE DEVICE. simctl accepts the literal `booted` and resolves
// it itself, which is fine while one simulator is up and a coin toss the moment
// two are — a toss made inside a tool that will not say which way it landed.
// Measured on the same machine: `iPhone 17 Pro` and `PennySDK0552QA` both
// booted, the app's records on one of them, and the CLI reporting "no Palbe
// console data found on the simulator". A filter that never matched, reporting
// silence. So the set of devices is resolved HERE, where it can be searched and
// where a refusal can name what it looked at.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// runSimctl is this package's only door to the tool.
//
// A var so a test can drive every branch without a simulator on the machine,
// and a single door so there is exactly one place that decides what happens to
// what the tool wrote.
var runSimctl = execSimctl

// execSimctl runs one `xcrun simctl` invocation and returns its stdout.
//
// The two streams are captured SEPARATELY, not combined. CombinedOutput would
// be shorter and wrong here: `get_app_container` prints a filesystem path on
// stdout that this package parses, so a deprecation notice mixed into it
// becomes a path that does not exist. stdout stays clean; stderr becomes the
// error's last sentence.
func execSimctl(args ...string) ([]byte, error) {
	cmd := exec.Command("xcrun", append([]string{"simctl"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// FRAMING FIRST, THE ACTUAL SENTENCE LAST — the caller wraps this with
		// what it was trying to do, and the tool's own words end the line,
		// where a reader looking for the reason will find them.
		if said := strings.TrimSpace(stderr.String()); said != "" {
			return nil, fmt.Errorf("%w: %s", err, said)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// simulator is one device this package may look in.
type simulator struct {
	UDID string
	Name string
}

// String is how a device is named to a person: the name they recognise plus the
// UDID they can paste into --device.
func (s simulator) String() string {
	if s.Name == "" {
		return s.UDID
	}
	return s.Name + " (" + s.UDID + ")"
}

func describeDevices(devices []simulator) string {
	if len(devices) == 0 {
		return "no simulator"
	}
	names := make([]string, 0, len(devices))
	for _, d := range devices {
		names = append(names, d.String())
	}
	return strings.Join(names, ", ")
}

// bootedDevices reads every booted device out of `simctl list devices booted -j`.
//
// EVERY one, not the first. The listing is a map keyed by runtime with no
// defined order, so "the first" was never a device anybody chose — it was
// whichever runtime sorted first that day.
func bootedDevices(payload []byte) ([]simulator, error) {
	var parsed struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			State string `json:"state"`
			Name  string `json:"name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("cannot parse the simulator list: %w", err)
	}
	runtimes := make([]string, 0, len(parsed.Devices))
	for runtime := range parsed.Devices {
		runtimes = append(runtimes, runtime)
	}
	sort.Strings(runtimes)

	var devices []simulator
	for _, runtime := range runtimes {
		for _, device := range parsed.Devices[runtime] {
			// `simctl list devices booted` already filters, but the state is
			// checked anyway: this function is also handed unfiltered listings
			// in tests, and a filter that trusts its input is not a filter.
			if strings.EqualFold(device.State, "Booted") {
				devices = append(devices, simulator{UDID: device.UDID, Name: device.Name})
			}
		}
	}
	return devices, nil
}

// targetDevices resolves which simulators a lookup should search.
//
// `--device` means exactly that device and nothing else — an operator who names
// one is not asking us to search, and it is theirs to name even if it is not
// booted; simctl will say so, in its own words, at the first call.
//
// With no flag, the answer is EVERY booted device rather than one of them. The
// newest session across all of them is the one the developer means, and picking
// silently is what produced a "nothing here" for records that were sitting on
// the other simulator.
func targetDevices(device string) ([]simulator, error) {
	if device != "" {
		return []simulator{{UDID: device}}, nil
	}
	out, err := runSimctl("list", "devices", "booted", "-j")
	if err != nil {
		return nil, fmt.Errorf("cannot list booted simulators: %w", err)
	}
	devices, err := bootedDevices(out)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("no booted simulator. Start one, or pass --device <udid>")
	}
	return devices, nil
}

// appContainer asks simctl where one app's data container is on ONE device.
//
// A concrete UDID, never the literal `booted`: delegating that word to simctl
// while two devices are up hands the choice to a tool that cannot report it
// back. The error is returned unwrapped so the caller — which knows how many
// devices it is asking about — supplies the framing exactly once.
func appContainer(udid, bundleID string) (string, error) {
	out, err := runSimctl("get_app_container", udid, bundleID, "data")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
