package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStartServesAndStopCleansUp drives `palbase start` on a scaffolded project
// and asks the STACK whether it is there.
//
// This is the only proof that the compose fix works. The vendored document was
// invalid in v0.52.0 and still invalid in v0.53.0 after a fix commit, and no
// test ran docker against it — the parity gate compared two strings that both
// passed through the same broken function. A stack that answers /.well-known is
// the answer that gate could not give.
func TestStartServesAndStopCleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("brings a real stack up — excluded from -short")
	}
	for _, tool := range []string{"npm", "docker"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is required in CI: %v", tool, err)
			}
			t.Skipf("%s is not on PATH", tool)
		}
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		// THE DAEMON IS PART OF THE TOOLCHAIN. The loop above makes a missing
		// docker BINARY fatal on CI and then this line let a missing DAEMON pass
		// — the same absence, one layer down, excused. A runner whose docker
		// cannot start is a runner where this gate measures nothing.
		requireToolOnCI(t, "the docker daemon", err)
		t.Skip("the docker daemon is not running")
	}

	bin := palbaseBinary(t)
	dir := t.TempDir()

	init := exec.Command(bin, "init")
	init.Dir = dir
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("palbase init: %v\n%s", err, out)
	}

	// Whatever happens below, the stack does not outlive the test.
	t.Cleanup(func() {
		stop := exec.Command(bin, "stop")
		stop.Dir = dir
		_ = stop.Run()
	})

	start := exec.Command(bin, "start")
	start.Dir = dir
	if out, err := start.CombinedOutput(); err != nil {
		// Capture only this test's containers before cleanup removes the cause.
		list := exec.Command("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=palbase-"+sanitiseGroup(filepath.Base(dir)))
		if ids, listErr := list.Output(); listErr == nil {
			for _, id := range strings.Fields(string(ids)) {
				if logs, logErr := exec.Command("docker", "logs", "--tail", "60", id).CombinedOutput(); logErr == nil {
					t.Logf("container %s: %s", id, logs)
				}
			}
		}
		t.Fatalf("palbase start: %v\n%s", err, out)
	}

	local := filepath.Join(dir, ".palbase", "local.json")
	raw, err := os.ReadFile(local)
	if err != nil {
		t.Fatalf("start left no %s: %v", local, err)
	}
	var target struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &target); err != nil || target.URL == "" {
		t.Fatalf("local.json carries no address: %s", raw)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Get(target.URL + "/.well-known/palbase.json")
	if err != nil {
		t.Fatalf("the stack does not answer at %s: %v", target.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s/.well-known/palbase.json answered %d, want 200", target.URL, res.StatusCode)
	}

	stop := exec.Command(bin, "stop")
	stop.Dir = dir
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("palbase stop: %v\n%s", err, out)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("stop left %s behind (err=%v)", local, err)
	}
}
