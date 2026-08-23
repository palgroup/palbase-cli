package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fakeREST records the calls a verb makes against the stack's fence.
type fakeREST struct {
	calls  []string
	bodies []string
	hosts  []string
}

func (f *fakeREST) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.calls = append(f.calls, method+" "+path)
	f.bodies = append(f.bodies, string(body))
	if method == http.MethodGet {
		raw, _ := json.Marshal(map[string]any{"hosts": f.hosts})
		return http.StatusOK, raw, nil
	}
	var in struct {
		Hosts []string `json:"hosts"`
	}
	_ = json.Unmarshal(body, &in)
	f.hosts = in.Hosts
	return http.StatusOK, body, nil
}

func runEgress(t *testing.T, rest *fakeREST, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("palbase egress %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// TestTheFenceLivesOnTheStackNotInTheSourceTree is the change this task exists
// for. The list used to be config/egress.ts, applied on every push — which meant
// the panel could not change it and the file could silently win over anything
// that did.
func TestTheFenceLivesOnTheStackNotInTheSourceTree(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	rest := &fakeREST{}
	runEgress(t, rest, "add", "api.example.com")

	if _, err := os.Stat(filepath.Join(dir, "config", "egress.ts")); !os.IsNotExist(err) {
		t.Fatal("a file was written: the fence has one home now, and it is the stack")
	}
	if len(rest.calls) != 2 || rest.calls[0] != "GET /v1/management/egress" || rest.calls[1] != "PUT /v1/management/egress" {
		t.Fatalf("calls = %v, want a read then a write", rest.calls)
	}
	if !strings.Contains(rest.bodies[1], "api.example.com") {
		t.Fatalf("the host did not travel: %s", rest.bodies[1])
	}
}

// TestAddRemove_RoundTripsAndSorts keeps the list reviewable: an allowlist a
// person has to scan for a name is one they stop scanning.
func TestAddRemove_RoundTripsAndSorts(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{}

	runEgress(t, rest, "add", "b.example.com")
	runEgress(t, rest, "add", "a.example.com")
	if got := strings.Join(rest.hosts, ","); got != "a.example.com,b.example.com" {
		t.Fatalf("hosts = %s, want them sorted", got)
	}

	runEgress(t, rest, "remove", "b.example.com")
	if got := strings.Join(rest.hosts, ","); got != "a.example.com" {
		t.Fatalf("hosts after remove = %s", got)
	}

	out := runEgress(t, rest, "list")
	if !strings.Contains(out, "a.example.com") {
		t.Fatalf("list did not show what the stack holds:\n%s", out)
	}
}

// TestRemovingTheLastHostSendsAnEmptyLISTNotNull keeps "call nothing" from
// collapsing into "nobody has said". The two mean opposite things to the
// runtime, and a deliberately closed fence must not reopen by omission.
func TestRemovingTheLastHostSendsAnEmptyListNotNull(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{hosts: []string{"only.example.com"}}

	runEgress(t, rest, "remove", "only.example.com")

	last := rest.bodies[len(rest.bodies)-1]
	if !strings.Contains(last, `"hosts":[]`) {
		t.Fatalf("sent %s, want an explicit empty list", last)
	}
}

// TestAdd_RejectsWhatTheStackWouldReject keeps the check local as well as
// remote: an allowlist is never best-effort, and learning the rules from a
// failed deploy is a bad way to learn them.
func TestAdd_RejectsWhatTheStackWouldReject(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, bad := range []string{"https://api.example.com", "api.example.com/v1", "api.example.com:8443", "*.example.com"} {
		rest := &fakeREST{}
		cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"add", bad})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
		if len(rest.calls) != 0 {
			t.Fatalf("%q reached the stack before being refused: %v", bad, rest.calls)
		}
	}
}
