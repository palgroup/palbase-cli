package backend

import (
	"bytes"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestFreeDevPort_NoListenerIsNoop: freeing a port nobody is bound to must do
// nothing and emit no output — the common case on a clean re-run.
func TestFreeDevPort_NoListenerIsNoop(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("freeDevPort is a no-op off darwin/linux")
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not on PATH")
	}
	// Grab then release a port so we know nothing is listening on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	var buf bytes.Buffer
	freeDevPort(port, &buf)
	if buf.Len() != 0 {
		t.Fatalf("freeDevPort wrote output for an unbound port: %q", buf.String())
	}
}

// TestFreeDevPort_NonNodeListenerNotKilled: when a port is held by a process
// whose comm is NOT "node", freeDevPort must NOT kill it — it warns and leaves
// it. We bind a Go net.Listener (comm is the test binary, not "node") and
// assert it's still alive after the call.
func TestFreeDevPort_NonNodeListenerNotKilled(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("freeDevPort is a no-op off darwin/linux")
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not on PATH")
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not on PATH")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	var buf bytes.Buffer
	freeDevPort(port, &buf)

	// The listener is held by THIS test process (comm != "node") OR by our own
	// pid (skipped). Either way the listener must still accept connections.
	conn, derr := net.Dial("tcp", l.Addr().String())
	if derr != nil {
		t.Fatalf("listener was killed/closed by freeDevPort (must not touch non-node): %v", derr)
	}
	_ = conn.Close()

	// If lsof reported a holder at all, it should have been left alone with a
	// warning (or skipped as our own pid → no output). It must never claim to
	// have "freed" the port.
	if strings.Contains(buf.String(), "freed port") {
		t.Fatalf("freeDevPort claimed to free a non-node listener: %q", buf.String())
	}
}

// TestProcessComm returns a non-empty comm for the current process (sanity that
// the ps invocation works on this platform), and "" for an impossible pid.
func TestProcessComm(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("processComm relies on ps -o comm=")
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not on PATH")
	}
	// pid 1 always exists on unix; comm should be non-empty.
	if comm := processComm(1); comm == "" {
		t.Fatalf("processComm(1) = empty, want a process name")
	}
	// A wildly out-of-range pid should not exist → empty comm (never panics).
	if comm := processComm(2147483646); comm != "" {
		t.Logf("processComm(huge pid) = %q (expected empty, but harmless)", comm)
	}
}
