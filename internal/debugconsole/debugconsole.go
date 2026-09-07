// Package debugconsole wires `palbase debug tail` — watch an app's in-app
// Palbe console (pb.debug) from the terminal.
//
// WHY THIS READS FILES RATHER THAN A SOCKET: when the app runs in a SIMULATOR,
// its container is a directory on this machine. The SDK already appends every
// record to `Library/Application Support/PalbeConsole/sessions/<id>.jsonl`, so
// a tail needs no network, no realtime channel, no tenant credential, and works
// with the device offline. Streaming the same records over the realtime socket
// is how a REAL device will be watched (that is a separate, authenticated
// feature); for the simulator it would be strictly more moving parts for the
// same bytes.
//
// The reason this command exists at all: an agent writing code against a
// Palbase backend cannot see what the app actually did. `palbase logs` shows
// the SERVER's view; this shows the CLIENT's — the request that was never sent,
// the 401 nobody surfaced, the body that came back empty.
package debugconsole

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// consoleDir is the SDK-side path, relative to an app's data container. It is a
// contract with `ConsoleStorage`; changing one without the other silently
// yields "no console data".
const consoleDir = "Library/Application Support/PalbeConsole"

// pollInterval is how often --follow re-reads. A var so tests can shrink it.
var pollInterval = 500 * time.Millisecond

// record mirrors `ConsoleStoredRecord` on the Swift side. Only the fields the
// terminal renders are decoded; the rest of the JSON is ignored on purpose so
// an SDK that adds a field does not break an older CLI.
type record struct {
	SchemaVersion int            `json:"schemaVersion"`
	Message       *messageRecord `json:"message"`
	Network       *networkRecord `json:"network"`
}

type messageRecord struct {
	Level     int               `json:"level"`
	Label     string            `json:"label"`
	Message   string            `json:"message"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt float64           `json:"createdAt"`
	File      string            `json:"file"`
	Line      int               `json:"line"`
}

type networkRecord struct {
	Method       string   `json:"method"`
	URL          string   `json:"url"`
	StatusCode   *int     `json:"statusCode"`
	State        int      `json:"state"`
	Duration     float64  `json:"duration"`
	CreatedAt    float64  `json:"createdAt"`
	Label        string   `json:"label"`
	RequestID    *string  `json:"requestId"`
	ErrorDesc    *string  `json:"errorDescription"`
	ResponseBody *bodyRef `json:"responseBody"`
	RequestBody  *bodyRef `json:"requestBody"`
}

type bodyRef struct {
	Size      int  `json:"size"`
	Truncated bool `json:"truncated"`
	// Inline carries the bytes when the body was small enough to keep in the
	// record; Swift encodes `Data` as base64, which is what []byte decodes.
	// Otherwise BlobKey (the body's SHA-256) says where the bytes went.
	Inline  []byte `json:"inline"`
	BlobKey string `json:"blobKey"`
}

// Cmd returns the `palbase debug` command group.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Inspect an app's in-app Palbe console",
		Long: "Watch what a Palbe app actually did — every request the SDK made, every\n" +
			"log line, and everything the app pushed in with pb.debug.\n\n" +
			"`palbase logs` shows the server's view of a deployment. This shows the\n" +
			"client's: the request that never left, the 401 nobody surfaced, the body\n" +
			"that came back empty.\n\n" +
			"Two views of the same records: `tail` reads a simulator\x27s own files, and\n" +
			"`attach` watches a real device live with a pairing code it shows.\n\n" +
			"There is no `history`. It read back records days later, and reading them\n" +
			"back requires a plane that RETAINS them — this one keeps aggregates, not\n" +
			"one record per request. The panel says the same thing on the same screen;\n" +
			"a verb that could only ever answer nothing is worse than an absent one.",
	}
	cmd.AddCommand(tailCmd(), attachCmd(r))
	return cmd
}

func tailCmd() *cobra.Command {
	var (
		follow     bool
		bundleID   string
		device     string
		errorsOnly bool
		asJSON     bool
		limit      int
	)

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Tail the in-app console of a simulator app",
		Long: "Reads the console records the SDK writes inside the app's simulator\n" +
			"container. No network and no credentials are involved — the container is\n" +
			"a directory on this machine.\n\n" +
			"Every booted simulator is searched and the newest session wins, because\n" +
			"\"the booted one\" is not a thing once two are up. Pass --device to look in\n" +
			"exactly one.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			session, err := locateSession(device, bundleID)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", session.describe())

			out := cmd.OutOrStdout()
			offset, err := renderFrom(out, session.path, 0, limit, errorsOnly, asJSON)
			if err != nil {
				return err
			}
			if !follow {
				return nil
			}
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(pollInterval):
				}
				// A new launch starts a NEW session file, so re-resolve rather
				// than tailing a file nothing will ever append to again.
				next, err := locateSession(device, bundleID)
				if err == nil && next.path != session.path {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "▸ new session — %s\n", next.describe())
					session, offset = next, 0
				}
				offset, err = renderFrom(out, session.path, offset, 0, errorsOnly, asJSON)
				if err != nil {
					return err
				}
			}
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep watching for new records")
	cmd.Flags().StringVar(&bundleID, "app", "", "bundle identifier, when several apps use the SDK")
	cmd.Flags().StringVar(&device, "device", "", "simulator UDID (default: search every booted simulator)")
	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "only failed requests and error logs")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw records, one JSON object per line")
	cmd.Flags().IntVar(&limit, "limit", 50, "how many existing records to print before following")
	return cmd
}

// MARK: - Locating the session

type sessionFile struct {
	path      string
	container string
	device    simulator
	modified  time.Time
}

// describe names the device as well as the session, because with more than one
// simulator booted "session 8F3A" does not say where it was found — and where
// it was found is the question that sent somebody looking in the first place.
func (s sessionFile) describe() string {
	app := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(s.container))))
	session := strings.TrimSuffix(filepath.Base(s.path), ".jsonl")
	if s.device.UDID == "" {
		return fmt.Sprintf("%s · session %s", app, session)
	}
	return fmt.Sprintf("%s · %s · session %s", s.device, app, session)
}

// locateSession finds the newest console session written by an app on any
// simulator the flags point at.
//
// ACROSS EVERY BOOTED DEVICE, not one of them. The loop below was always a loop
// over roots; the defect was that the set of roots came from a single device
// chosen by map iteration order. Widening the set is what makes the "nothing
// here" answer true when it is given.
func locateSession(device, bundleID string) (sessionFile, error) {
	roots, searched, err := consoleRoots(device, bundleID)
	if err != nil {
		return sessionFile{}, err
	}
	var newest sessionFile
	for _, root := range roots {
		entries, err := os.ReadDir(filepath.Join(root.path, "sessions"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(newest.modified) {
				newest = sessionFile{
					path:      filepath.Join(root.path, "sessions", entry.Name()),
					container: root.path,
					device:    root.device,
					modified:  info.ModTime(),
				}
			}
		}
	}
	if newest.path == "" {
		// NAME WHAT WAS SEARCHED. The old message sent the reader off to "run
		// the app once", which they had just done — on the other simulator.
		return sessionFile{}, fmt.Errorf(
			"no Palbe console data found on %s.\n"+
				"Run the app once so the SDK creates a session, and make sure it links a\n"+
				"Palbe version with pb.debug (the console records from launch; nothing to\n"+
				"switch on). If the app is running on a simulator that is not listed above,\n"+
				"boot it or name it with --device <udid>",
			describeDevices(searched))
	}
	return newest, nil
}

// consoleRoot pairs a PalbeConsole directory with the device it was found on.
type consoleRoot struct {
	path   string
	device simulator
}

// consoleRoots returns every `PalbeConsole` directory across the target
// devices, and the devices it looked at — the second return value exists so a
// refusal can say where it looked instead of saying "the simulator".
func consoleRoots(device, bundleID string) ([]consoleRoot, []simulator, error) {
	devices, err := targetDevices(device)
	if err != nil {
		return nil, nil, err
	}
	if bundleID != "" {
		roots, err := containersFor(devices, bundleID)
		return roots, devices, err
	}

	var roots []consoleRoot
	for _, sim := range devices {
		base, err := deviceDataRoot(sim.UDID)
		if err != nil {
			return nil, devices, err
		}
		apps, err := os.ReadDir(base)
		if err != nil {
			// A device with no app containers at all is the ORDINARY case once
			// more than one simulator is booted — the app was simply never
			// installed there. Refusing on it would make a second booted
			// device break a command that used to work.
			continue
		}
		for _, app := range apps {
			candidate := filepath.Join(base, app.Name(), consoleDir)
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				roots = append(roots, consoleRoot{path: candidate, device: sim})
			}
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].path < roots[j].path })
	return roots, devices, nil
}

// containersFor asks each device where the bundle's container is, and refuses
// only when NO device has one.
//
// The refusal carries every device's answer verbatim, because the answers
// differ and the useful one is whichever device the developer thought they were
// looking at.
func containersFor(devices []simulator, bundleID string) ([]consoleRoot, error) {
	var roots []consoleRoot
	refusals := make([]string, 0, len(devices))
	for _, sim := range devices {
		container, err := appContainer(sim.UDID, bundleID)
		if err != nil {
			refusals = append(refusals, fmt.Sprintf("  %s: %s", sim, err))
			continue
		}
		roots = append(roots, consoleRoot{path: filepath.Join(container, consoleDir), device: sim})
	}
	if len(roots) > 0 {
		return roots, nil
	}
	if len(devices) == 1 {
		return nil, fmt.Errorf("no container for %q on the simulator %s: %s",
			bundleID, devices[0], strings.TrimSpace(strings.TrimPrefix(refusals[0], "  "+devices[0].String()+":")))
	}
	return nil, fmt.Errorf("no container for %q on any booted simulator:\n%s",
		bundleID, strings.Join(refusals, "\n"))
}

// deviceDataRoot is where one simulator keeps its app data containers. The
// layout is Apple's, not ours.
func deviceDataRoot(udid string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library/Developer/CoreSimulator/Devices", udid,
		"data/Containers/Data/Application"), nil
}

// MARK: - Rendering

// renderFrom prints records starting at byte `offset` and returns the new
// offset. When `limit` > 0 only the last `limit` records are printed (the
// initial catch-up); a follow poll passes 0 and prints everything new.
func renderFrom(out io.Writer, path string, offset int64, limit int, errorsOnly, asJSON bool) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		// The file can vanish between locate and read (app reinstalled). Treat
		// it as "nothing new" rather than killing a --follow session.
		if os.IsNotExist(err) {
			return offset, nil
		}
		return offset, err
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}

	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // bodies ride inline
	consumed := offset
	for scanner.Scan() {
		line := scanner.Text()
		consumed += int64(len(scanner.Bytes())) + 1 // + newline
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return consumed, err
	}

	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	for _, line := range lines {
		var rec record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// A torn final line is one lost record, not a lost session.
			continue
		}
		text, keep := format(rec, errorsOnly, asJSON, line)
		if keep {
			_, _ = fmt.Fprintln(out, text)
		}
	}
	return consumed, nil
}

func format(rec record, errorsOnly, asJSON bool, raw string) (string, bool) {
	switch {
	case rec.Network != nil:
		net := rec.Network
		isError := net.State == 2
		if errorsOnly && !isError {
			return "", false
		}
		if asJSON {
			return raw, true
		}
		status := "pending"
		if net.ErrorDesc != nil && *net.ErrorDesc != "" {
			status = *net.ErrorDesc
		} else if net.StatusCode != nil {
			status = fmt.Sprintf("%d", *net.StatusCode)
		}
		parts := []string{
			stamp(net.CreatedAt),
			marker(isError, net.State),
			fmt.Sprintf("%-6s", net.Method),
			status,
			net.URL,
		}
		if net.Duration > 0 {
			parts = append(parts, fmt.Sprintf("(%s)", durationText(net.Duration)))
		}
		if net.ResponseBody != nil && net.ResponseBody.Size > 0 {
			parts = append(parts, fmt.Sprintf("%dB", net.ResponseBody.Size))
		}
		if net.RequestID != nil && *net.RequestID != "" {
			parts = append(parts, "req="+*net.RequestID)
		}
		return strings.Join(parts, " "), true

	case rec.Message != nil:
		msg := rec.Message
		isError := msg.Level >= 5
		if errorsOnly && !isError {
			return "", false
		}
		if asJSON {
			return raw, true
		}
		line := fmt.Sprintf("%s %s %-8s %s", stamp(msg.CreatedAt), marker(isError, -1),
			levelName(msg.Level), msg.Message)
		if msg.Label != "" && msg.Label != "app" {
			line += "  [" + msg.Label + "]"
		}
		if len(msg.Metadata) > 0 {
			keys := make([]string, 0, len(msg.Metadata))
			for key := range msg.Metadata {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			pairs := make([]string, 0, len(keys))
			for _, key := range keys {
				pairs = append(pairs, key+"="+msg.Metadata[key])
			}
			line += "  {" + strings.Join(pairs, " ") + "}"
		}
		return line, true
	}
	return "", false
}

// marker is a one-glyph state column so a wall of output scans at a glance.
func marker(isError bool, state int) string {
	switch {
	case isError:
		return "✗"
	case state == 0:
		return "…"
	default:
		return "✓"
	}
}

func stamp(millis float64) string {
	if millis <= 0 {
		return "--:--:--.---"
	}
	return time.UnixMilli(int64(millis)).Format("15:04:05.000")
}

func durationText(seconds float64) string {
	if seconds < 0.95 {
		return fmt.Sprintf("%.0fms", seconds*1000)
	}
	return fmt.Sprintf("%.1fs", seconds)
}

func levelName(level int) string {
	switch level {
	case 0:
		return "trace"
	case 1:
		return "debug"
	case 2:
		return "info"
	case 3:
		return "notice"
	case 4:
		return "warning"
	case 5:
		return "error"
	case 6:
		return "critical"
	default:
		return "log"
	}
}
