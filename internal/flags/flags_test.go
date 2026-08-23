package flags

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

// fakeREST records what a verb asked the stack for.
type fakeREST struct {
	method, path string
	body         string
	status       int
	answer       string
}

func (f *fakeREST) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.method, f.path, f.body = method, path, string(body)
	st := f.status
	if st == 0 {
		st = http.StatusOK
	}
	ans := f.answer
	if ans == "" {
		ans = "{}"
	}
	return st, []byte(ans), nil
}

func runDefs(t *testing.T, rest *fakeREST, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestFlagsLiveOnTheStackNotInAFile is the change this task exists for. The
// definitions used to be config/flags.ts, upserted on every push — so a flag
// changed in the panel went back to the file's value on the next deploy, with
// nothing reporting it. One writer now.
func TestFlagsLiveOnTheStackNotInAFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	rest := &fakeREST{}
	if _, err := runDefs(t, rest, "add", "new_dashboard", "--type", "boolean", "--default", "false"); err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "config", "flags.ts")); !os.IsNotExist(err) {
		t.Fatal("a file was written: flag definitions have one home now, and it is the stack")
	}
	if rest.method != http.MethodPut || rest.path != "/v1/management/flags/new_dashboard" {
		t.Fatalf("called %s %s, want PUT /v1/management/flags/new_dashboard", rest.method, rest.path)
	}
}

// TestAddSendsTheWholeDefinition is FR-021: the courier wrote type, default,
// variants and description, so the endpoint that replaced it has to carry all
// four or the capability shrank while looking replaced.
func TestAddSendsTheWholeDefinition(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{}

	if _, err := runDefs(t, rest, "add", "theme",
		"--type", "string", "--default", `"system"`,
		"--variants", "light,dark,system", "--description", "Which theme wins"); err != nil {
		t.Fatalf("add: %v", err)
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(rest.body), &sent); err != nil {
		t.Fatalf("body is not JSON: %s", rest.body)
	}
	for _, want := range []string{"type", "value", "variants", "description"} {
		if _, ok := sent[want]; !ok {
			t.Fatalf("the definition lost %q on the way out: %s", want, rest.body)
		}
	}
}

// TestRemoveActuallyRemoves. It used to edit the file and leave the live flag in
// place, so "removed" and "still there" were both true at once.
func TestRemoveActuallyRemoves(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{status: http.StatusNoContent}

	out, err := runDefs(t, rest, "remove", "old_flag")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if rest.method != http.MethodDelete || rest.path != "/v1/management/flags/old_flag" {
		t.Fatalf("called %s %s, want a DELETE", rest.method, rest.path)
	}
	if strings.Contains(out, "NOT deleted") {
		t.Fatal("the command still hedges about the live flag surviving")
	}
}

// TestAddRefusesADefaultThatIsNotJSON keeps the check local as well as remote: a
// string default has to be quoted, and learning that from a stack round-trip is
// slower than learning it here.
func TestAddRefusesADefaultThatIsNotJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{}

	if _, err := runDefs(t, rest, "add", "theme", "--type", "string", "--default", "system"); err == nil {
		t.Fatal("an unquoted string default was accepted")
	}
	if rest.method != "" {
		t.Fatalf("it reached the stack before being refused: %s %s", rest.method, rest.path)
	}
}

// TestListReadsTheStack.
func TestListReadsTheStack(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{answer: `[{"key":"a","type":"boolean","value":true,"description":"first"}]`}

	out, err := runDefs(t, rest, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rest.method != http.MethodGet || rest.path != "/v1/management/flags" {
		t.Fatalf("called %s %s", rest.method, rest.path)
	}
	if !strings.Contains(out, "a") || !strings.Contains(out, "first") {
		t.Fatalf("the stack's answer did not reach the person:\n%s", out)
	}
}
