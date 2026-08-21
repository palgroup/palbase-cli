package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// stubREST records what the command asked the control plane for, and answers
// with whatever the test set up.
type stubREST struct {
	method, path string
	body         any
	reply        any
	err          error
}

func (s *stubREST) Do(_ context.Context, method, path string, body, out any) error {
	s.method, s.path, s.body = method, path, body
	if s.err != nil {
		return s.err
	}
	if out == nil || s.reply == nil {
		return nil
	}
	raw, err := json.Marshal(s.reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type stubCloud struct {
	domain string
	err    error
}

func (s stubCloud) TenantDomain(context.Context) (string, error) { return s.domain, s.err }

func resolvers(rest *stubREST, cloud Bootstrapper) Resolvers {
	return Resolvers{
		REST:  func() REST { return rest },
		Cloud: func() Bootstrapper { return cloud },
	}
}

func run(t *testing.T, r Resolvers, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// Creating a project must end with an address the person can act on. Printing
// only a ref would leave them to assemble the host themselves — and to guess
// the domain, which differs per deployment.
func TestCreatePrintsALinkableAddress(t *testing.T) {
	rest := &stubREST{reply: Project{Ref: "abc123xyz", Slot: 1702, Cell: "pbc-cell-01", Phase: "Running"}}
	out, err := run(t, resolvers(rest, stubCloud{domain: "v2.palbase.studio"}), "", "create", "shop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rest.method != "POST" || rest.path != "/v1/cloud/projects" {
		t.Fatalf("wrong call: %s %s", rest.method, rest.path)
	}
	sent, _ := rest.body.(map[string]any)
	if sent["name"] != "shop" || sent["tier"] != "free" {
		t.Fatalf("body did not carry the name and default tier: %#v", rest.body)
	}
	if !strings.Contains(out, "palbase link https://abc123xyz.v2.palbase.studio") {
		t.Fatalf("no linkable address in output:\n%s", out)
	}
}

// The domain comes from the cloud. When it cannot be read, the command still
// succeeds — the project exists — but says plainly that the host is unknown
// rather than printing a confident, wrong address.
func TestCreateStillSucceedsWhenTheDomainIsUnknown(t *testing.T) {
	rest := &stubREST{reply: Project{Ref: "abc123xyz", Phase: "Running"}}
	out, err := run(t, resolvers(rest, stubCloud{err: fmt.Errorf("unreachable")}), "", "create", "shop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.Contains(out, "https://abc123xyz.v2") {
		t.Fatalf("invented a domain it could not read:\n%s", out)
	}
	if !strings.Contains(out, "Created abc123xyz") {
		t.Fatalf("did not report the created project:\n%s", out)
	}
}

func TestListShowsEveryProject(t *testing.T) {
	rest := &stubREST{reply: []Project{
		{Ref: "aaa", Phase: "Running", Cell: "pbc-cell-01"},
		{Ref: "bbb", Phase: "Pending", Cell: "pbc-cell-02"},
	}}
	out, err := run(t, resolvers(rest, stubCloud{}), "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rest.path != "/v1/cloud/projects" {
		t.Fatalf("wrong path: %s", rest.path)
	}
	for _, want := range []string{"aaa", "Running", "bbb", "pbc-cell-02"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
}

func TestListSaysSoWhenThereAreNone(t *testing.T) {
	out, err := run(t, resolvers(&stubREST{reply: []Project{}}, stubCloud{}), "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "No projects yet") {
		t.Fatalf("an empty list printed nothing useful:\n%s", out)
	}
}

// Deleting takes a microVM and its disk with it, so the confirmation must be
// something nobody satisfies by reflex: typing the ref means having read which
// project this is.
func TestDeleteRefusesAMismatchedConfirmation(t *testing.T) {
	rest := &stubREST{}
	_, err := run(t, resolvers(rest, stubCloud{}), "wrong\n", "delete", "abc123xyz")
	if err == nil {
		t.Fatal("a mismatched confirmation was accepted")
	}
	if rest.method != "" {
		t.Fatalf("the delete was sent anyway: %s %s", rest.method, rest.path)
	}
}

func TestDeleteProceedsOnAMatchingConfirmation(t *testing.T) {
	rest := &stubREST{}
	out, err := run(t, resolvers(rest, stubCloud{}), "abc123xyz\n", "delete", "abc123xyz")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rest.method != "DELETE" || rest.path != "/v1/cloud/projects/abc123xyz" {
		t.Fatalf("wrong call: %s %s", rest.method, rest.path)
	}
	if !strings.Contains(out, "Deleted abc123xyz") {
		t.Fatalf("no confirmation printed:\n%s", out)
	}
}

// --yes is for scripts, and must not silently need a terminal.
func TestDeleteWithYesSkipsThePrompt(t *testing.T) {
	rest := &stubREST{}
	if _, err := run(t, resolvers(rest, stubCloud{}), "", "delete", "abc123xyz", "--yes"); err != nil {
		t.Fatalf("delete --yes: %v", err)
	}
	if rest.method != "DELETE" {
		t.Fatalf("the delete never went out: %#v", rest)
	}
}

func TestStatusNamesTheProject(t *testing.T) {
	rest := &stubREST{reply: Project{Ref: "abc123xyz", Slot: 42, Cell: "pbc-cell-01", Phase: "Running"}}
	out, err := run(t, resolvers(rest, stubCloud{}), "", "status", "abc123xyz")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if rest.path != "/v1/cloud/projects/abc123xyz" {
		t.Fatalf("wrong path: %s", rest.path)
	}
	for _, want := range []string{"abc123xyz", "Running", "pbc-cell-01", "42"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
}

// A ref is user input and lands in a URL path. Escaping it here means a ref
// carrying a slash cannot address a different resource.
func TestRefIsEscapedIntoThePath(t *testing.T) {
	rest := &stubREST{reply: Project{}}
	if _, err := run(t, resolvers(rest, stubCloud{}), "", "status", "a/../../admin"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(rest.path, "../") {
		t.Fatalf("an unescaped ref reached the path: %s", rest.path)
	}
}
