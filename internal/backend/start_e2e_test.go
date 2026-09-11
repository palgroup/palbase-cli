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
// useRealMachineHome puts this ONE test back on the developer's real home, and
// collects what it leaves there.
//
// `TestMain` moves the package's seam to a throwaway directory, but this test
// spawns the real binary — a child process resolves `os.UserHomeDir()` from its
// own environment and cannot see a package variable. Handing it `HOME` instead
// breaks Docker, which discovers `compose` through `$HOME/.docker/cli-plugins`
// (measured: `unknown shorthand flag: 'f' in -f`). So the child writes to the
// real home, this test reads the same place, and the record goes at the end —
// litter that outlives its checkout is the defect D-010 was about.
func useRealMachineHome(t *testing.T, checkout string) {
	t.Helper()
	prev := machineStateHome
	machineStateHome = os.UserHomeDir
	t.Cleanup(func() {
		if dir, err := machineStateDir(checkout); err == nil {
			_ = os.RemoveAll(dir)
		}
		machineStateHome = prev
	})
}

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

	// THIS MACHINE'S STATE IS NOT IN THE CHECKOUT. `start` records the stack in
	// front of you under the user's own `~/.palbase/checkouts/<hash>/`, so the
	// test asks `LocalStatePath` where that is instead of rebuilding a path the
	// product stopped writing.
	// THE SUBPROCESS HAS ITS OWN HOME. `TestMain` moves this package's seam, and
	// a spawned binary cannot see a package variable — it resolves
	// `os.UserHomeDir()` from its own environment. So the child was pointed at
	// the same throwaway home through `HOME` (see the commands above), and the
	// path is asked for with that same home in effect.
	useRealMachineHome(t, dir)
	local, err := LocalStatePath(dir)
	if err != nil {
		t.Fatal(err)
	}
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
