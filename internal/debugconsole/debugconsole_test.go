package debugconsole

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The wire this package decodes is written by the Swift SDK's `ConsoleStorage`.
// These fixtures are copied from a REAL session file produced by an app running
// in the simulator — not hand-authored — so a field rename on the SDK side
// shows up here as a failing test rather than as a silently empty tail.
const (
	realMessageLine = `{"schemaVersion":1,"message":{"level":4,"function":"hostLogs()","label":"checkout",` +
		`"metadata":{"sku":"A1","userId":"u_42"},"line":109,"id":"FE72B727-8660-48E8-B195-DD86810F0B77",` +
		`"file":"ConsoleHarness/HarnessApp.swift","message":"cart is empty","createdAt":1785172985075.425}}`

	realNetworkLine = `{"schemaVersion":1,"network":{"kind":0,"method":"POST","label":"palbe",` +
		`"url":"https://todoappm8p6zm.dev.palbase.studio/auth/login","statusCode":401,"state":2,` +
		`"duration":0.877,"createdAt":1785172993060.5,"requestId":"req_019fa49b","responseHeaders":{},` +
		`"requestHeaders":{},"responseBody":{"size":153,"truncated":false,"blobKey":null,"inline":null},` +
		`"id":"5CB683FA-2BFE-438E-A48B-89E18E569726"}}`

	successNetworkLine = `{"schemaVersion":1,"network":{"kind":0,"method":"GET","label":"palbe",` +
		`"url":"https://x.dev.palbase.studio/v1/user-flags/delta","statusCode":304,"state":1,` +
		`"duration":0.117,"createdAt":1785172996745.0,"responseHeaders":{},"requestHeaders":{},` +
		`"id":"AA798F34-3449-44FC-BF75-E53E2999B37E"}}`
)

func writeSession(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func render(t *testing.T, path string, offset int64, limit int, errorsOnly, asJSON bool) (string, int64) {
	t.Helper()
	var buf bytes.Buffer
	next, err := renderFrom(&buf, path, offset, limit, errorsOnly, asJSON)
	if err != nil {
		t.Fatalf("renderFrom: %v", err)
	}
	return buf.String(), next
}

func TestRendersRealSDKRecords(t *testing.T) {
	path := writeSession(t, realMessageLine, realNetworkLine)
	out, _ := render(t, path, 0, 0, false, false)

	// The message line: level name, text, label, sorted metadata.
	if !strings.Contains(out, "warning") || !strings.Contains(out, "cart is empty") {
		t.Errorf("message not rendered:\n%s", out)
	}
	if !strings.Contains(out, "[checkout]") {
		t.Errorf("label missing:\n%s", out)
	}
	if !strings.Contains(out, "{sku=A1 userId=u_42}") {
		t.Errorf("metadata must be sorted and inline:\n%s", out)
	}

	// The network line: method, status, url, duration, size, request id.
	for _, want := range []string{"POST", "401", "/auth/login", "877ms", "153B", "req=req_019fa49b"} {
		if !strings.Contains(out, want) {
			t.Errorf("network line missing %q:\n%s", want, out)
		}
	}
	// A 4xx is a FAILURE and must carry the error marker — the whole point of
	// scanning a wall of output.
	if !strings.Contains(out, "✗") {
		t.Errorf("a 401 must render as failed:\n%s", out)
	}
}

func TestErrorsOnlyDropsSuccesses(t *testing.T) {
	path := writeSession(t, successNetworkLine, realNetworkLine, realMessageLine)
	out, _ := render(t, path, 0, 0, true, false)

	if strings.Contains(out, "user-flags/delta") {
		t.Errorf("--errors must drop a 304:\n%s", out)
	}
	if !strings.Contains(out, "/auth/login") {
		t.Errorf("--errors must keep the 401:\n%s", out)
	}
	// A warning is not an error; only level >= error survives.
	if strings.Contains(out, "cart is empty") {
		t.Errorf("--errors must drop a warning:\n%s", out)
	}
}

func TestLimitKeepsTheNewestRecords(t *testing.T) {
	path := writeSession(t, realMessageLine, successNetworkLine, realNetworkLine)
	out, _ := render(t, path, 0, 1, false, false)

	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("--limit 1 must print exactly one record:\n%s", out)
	}
	if !strings.Contains(out, "/auth/login") {
		t.Errorf("--limit must keep the NEWEST record, not the oldest:\n%s", out)
	}
}

func TestFollowOffsetPrintsOnlyNewRecords(t *testing.T) {
	// The --follow contract: re-reading from the returned offset must not
	// reprint what was already shown. Getting this wrong floods the terminal
	// with the same lines every poll.
	path := writeSession(t, realMessageLine)
	first, offset := render(t, path, 0, 0, false, false)
	if !strings.Contains(first, "cart is empty") {
		t.Fatalf("first pass missing its record:\n%s", first)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(realNetworkLine + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	second, _ := render(t, path, offset, 0, false, false)
	if strings.Contains(second, "cart is empty") {
		t.Errorf("a follow poll reprinted an old record:\n%s", second)
	}
	if !strings.Contains(second, "/auth/login") {
		t.Errorf("a follow poll missed the appended record:\n%s", second)
	}
}

func TestATornFinalLineCostsOneRecord(t *testing.T) {
	// The app can be killed mid-append. JSONL is chosen so that costs exactly
	// one record; the tail must not abort the whole session.
	path := writeSession(t, realMessageLine, realNetworkLine)
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = file.WriteString(`{"schemaVersion":1,"netw`)
	_ = file.Close()

	out, _ := render(t, path, 0, 0, false, false)
	if !strings.Contains(out, "cart is empty") || !strings.Contains(out, "/auth/login") {
		t.Errorf("complete records must survive a torn tail:\n%s", out)
	}
}

func TestJSONModeEmitsTheRawLine(t *testing.T) {
	path := writeSession(t, realNetworkLine)
	out, _ := render(t, path, 0, 0, false, true)
	if strings.TrimSpace(out) != realNetworkLine {
		t.Errorf("--json must pass the record through byte-for-byte:\n%s", out)
	}
}

func TestAMissingFileIsNotAFollowKiller(t *testing.T) {
	// An app reinstall deletes the container between polls.
	_, err := renderFrom(&bytes.Buffer{}, filepath.Join(t.TempDir(), "gone.jsonl"), 0, 0, false, false)
	if err != nil {
		t.Errorf("a vanished session file must not end a --follow run: %v", err)
	}
}

func TestFirstBootedUDID(t *testing.T) {
	payload := []byte(`{"devices":{
		"com.apple.CoreSimulator.SimRuntime.iOS-18-0":[
			{"udid":"AAA","state":"Shutdown","name":"iPhone 15"},
			{"udid":"BBB","state":"Booted","name":"iPhone 17 Pro"}]}}`)
	udid, err := firstBootedUDID(payload)
	if err != nil {
		t.Fatal(err)
	}
	if udid != "BBB" {
		t.Errorf("picked %q, want the BOOTED device", udid)
	}
}

func TestNoBootedSimulatorSaysWhatToDo(t *testing.T) {
	_, err := firstBootedUDID([]byte(`{"devices":{"rt":[{"udid":"AAA","state":"Shutdown"}]}}`))
	if err == nil {
		t.Fatal("expected an error when nothing is booted")
	}
	if !strings.Contains(err.Error(), "--device") {
		t.Errorf("the error must tell the user the way out, got: %v", err)
	}
}

func TestDurationAndLevelFormatting(t *testing.T) {
	cases := []struct {
		seconds float64
		want    string
	}{
		{0.117, "117ms"},
		{0.949, "949ms"},
		{2.4, "2.4s"},
	}
	for _, c := range cases {
		if got := durationText(c.seconds); got != c.want {
			t.Errorf("durationText(%v) = %q, want %q", c.seconds, got, c.want)
		}
	}
	if levelName(5) != "error" || levelName(2) != "info" || levelName(99) != "log" {
		t.Errorf("level names drifted from the SDK's PBLogLevel ordering")
	}
}
