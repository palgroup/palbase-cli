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
	// The SERVER shape, not a convenient one: v2 answers with an envelope
	// (ListFlags200JSONResponse{Flags: …}). The fixture used to be a bare array,
	// so the test agreed with the CLI and BOTH were wrong about the stack.
	rest := &fakeREST{answer: `{"flags":[{"key":"a","type":"boolean","value":true,"description":"first"}]}`}

	out, err := runDefs(t, rest, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rest.method != http.MethodGet || rest.path != "/v1/management/flags" {
		t.Fatalf("called %s %s", rest.method, rest.path)
	}
	// The HUMAN TABLE, not "the bytes appeared somewhere". `Contains(out,"a")`
	// passed against a raw JSON dump too, which is why the decode defect
	// survived this test: the assertion agreed with both the fix and the bug.
	if !strings.Contains(out, "flags on this stack:") {
		t.Fatalf("the table header is missing — the answer was not rendered:\n%s", out)
	}
	if !strings.Contains(out, "boolean = true") {
		t.Fatalf("the flag's type and value were not rendered:\n%s", out)
	}
	if strings.Contains(out, `"flags"`) {
		t.Fatalf("the wire shape reached the person instead of a table:\n%s", out)
	}
}

// An empty stack must reach the "no flags" path — it was UNREACHABLE while the
// decode failed, because the failure printed the raw body instead.
func TestListOnAStackWithNoFlags(t *testing.T) {
	t.Chdir(t.TempDir())
	rest := &fakeREST{answer: `{"flags":[]}`}

	out, err := runDefs(t, rest, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "this stack declares no flags") {
		t.Fatalf("an empty stack must say so, not print its wire shape:\n%s", out)
	}
}

// A 200 whose body is not the flags envelope must be REFUSED, not read as an
// empty stack. Decoding into a plain slice made `{"error":"unauthorized"}`
// unmarshal cleanly into len 0 and print "this stack declares no flags" — the
// silent wrong answer, back through a different door.
func TestListRefusesAnAnswerThatIsNotTheFlagsEnvelope(t *testing.T) {
	for _, body := range []string{
		`{"error":"unauthorized"}`,
		`null`,
		`{"items":[{"key":"a"}]}`,
		``,
	} {
		t.Run(body, func(t *testing.T) {
			t.Chdir(t.TempDir())
			out, err := runDefs(t, &fakeREST{answer: body}, "list")
			if err == nil {
				t.Fatalf("body %q was accepted; output was:\n%s", body, out)
			}
			if strings.Contains(out, "declares no flags") {
				t.Fatalf("body %q was read as an empty stack:\n%s", body, out)
			}
		})
	}
}
