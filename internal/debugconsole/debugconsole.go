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
	"os/exec"
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
}

// Cmd returns the `palbase debug` command group.
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Inspect an app's in-app Palbe console",
		Long: "Watch what a Palbe app actually did — every request the SDK made, every\n" +
			"log line, and everything the app pushed in with pb.debug.\n\n" +
			"`palbase logs` shows the server's view of a deployment. This shows the\n" +
			"client's: the request that never left, the 401 nobody surfaced, the body\n" +
			"that came back empty.",
	}
	cmd.AddCommand(tailCmd())
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
			"a directory on this machine.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			session, err := locateSession(device, bundleID)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", session.describe())

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
					fmt.Fprintf(cmd.ErrOrStderr(), "▸ new session — %s\n", next.describe())
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
	cmd.Flags().StringVar(&device, "device", "", "simulator UDID (default: the booted one)")
	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "only failed requests and error logs")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw records, one JSON object per line")
	cmd.Flags().IntVar(&limit, "limit", 50, "how many existing records to print before following")
	return cmd
}

// MARK: - Locating the session

type sessionFile struct {
	path      string
	container string
	modified  time.Time
}

func (s sessionFile) describe() string {
	return fmt.Sprintf("%s · session %s",
		filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(s.container)))),
		strings.TrimSuffix(filepath.Base(s.path), ".jsonl"))
}

// locateSession finds the newest console session written by an app on a booted
// simulator.
func locateSession(device, bundleID string) (sessionFile, error) {
	roots, err := consoleRoots(device, bundleID)
	if err != nil {
		return sessionFile{}, err
	}
	var newest sessionFile
	for _, root := range roots {
		entries, err := os.ReadDir(filepath.Join(root, "sessions"))
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
					path:      filepath.Join(root, "sessions", entry.Name()),
					container: root,
					modified:  info.ModTime(),
				}
			}
		}
	}
	if newest.path == "" {
		return sessionFile{}, fmt.Errorf(
			"no Palbe console data found on the simulator.\n" +
				"Run the app once so the SDK creates a session, and make sure it links a\n" +
				"Palbe version with pb.debug (the console records from launch; nothing to\n" +
				"switch on).")
	}
	return newest, nil
}

// consoleRoots returns every `PalbeConsole` directory under the simulator's app
// containers.
func consoleRoots(device, bundleID string) ([]string, error) {
	if bundleID != "" {
		container, err := appContainer(device, bundleID)
		if err != nil {
			return nil, err
		}
		return []string{filepath.Join(container, consoleDir)}, nil
	}

	base, err := simulatorDataRoot(device)
	if err != nil {
		return nil, err
	}
	apps, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("cannot read the simulator's app containers: %w", err)
	}
	var roots []string
	for _, app := range apps {
		candidate := filepath.Join(base, app.Name(), consoleDir)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			roots = append(roots, candidate)
		}
	}
	sort.Strings(roots)
	return roots, nil
}

func appContainer(device, bundleID string) (string, error) {
	args := []string{"simctl", "get_app_container"}
	if device != "" {
		args = append(args, device)
	} else {
		args = append(args, "booted")
	}
	args = append(args, bundleID, "data")
	out, err := exec.Command("xcrun", args...).Output()
	if err != nil {
		return "", fmt.Errorf("no container for %q on the simulator: %w", bundleID, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// simulatorDataRoot resolves the Containers/Data/Application directory of the
// target simulator. It asks `simctl` for any app's container rather than
// guessing the path, then walks up — the layout is Apple's, not ours.
func simulatorDataRoot(device string) (string, error) {
	udid := device
	if udid == "" {
		out, err := exec.Command("xcrun", "simctl", "list", "devices", "booted", "-j").Output()
		if err != nil {
			return "", fmt.Errorf("cannot list booted simulators: %w", err)
		}
		udid, err = firstBootedUDID(out)
		if err != nil {
			return "", err
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library/Developer/CoreSimulator/Devices", udid,
		"data/Containers/Data/Application"), nil
}

// firstBootedUDID pulls a UDID out of `simctl list devices booted -j`.
func firstBootedUDID(payload []byte) (string, error) {
	var parsed struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			State string `json:"state"`
			Name  string `json:"name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("cannot parse the simulator list: %w", err)
	}
	names := make([]string, 0, len(parsed.Devices))
	for runtime := range parsed.Devices {
		names = append(names, runtime)
	}
	sort.Strings(names)
	for _, runtime := range names {
		for _, device := range parsed.Devices[runtime] {
			if strings.EqualFold(device.State, "Booted") {
				return device.UDID, nil
			}
		}
	}
	return "", fmt.Errorf("no booted simulator. Start one, or pass --device <udid>")
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
			fmt.Fprintln(out, text)
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
