package members

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type stubREST struct {
	method, path string
	body         any
	reply        any
}

func (s *stubREST) Do(_ context.Context, method, path string, body, out any) error {
	s.method, s.path, s.body = method, path, body
	if out == nil || s.reply == nil {
		return nil
	}
	raw, err := json.Marshal(s.reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

type stubTarget struct {
	ref      string
	isCloud  bool
	describe string
}

func (t stubTarget) Ref() (string, bool) { return t.ref, t.isCloud }
func (t stubTarget) Describe() string    { return t.describe }

func run(t *testing.T, rest *stubREST, target stubTarget, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd(Resolvers{
		REST:   func() REST { return rest },
		Target: func() (Target, error) { return target, nil },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestListShowsOwnerAndMembers(t *testing.T) {
	rest := &stubREST{reply: []Member{
		{UserID: "usr_1", Email: "owner@example.test", Role: "owner"},
		{UserID: "usr_2", Email: "dev@example.test", Role: "member"},
	}}
	out, err := run(t, rest, stubTarget{ref: "abc123xyz", isCloud: true}, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rest.path != "/v1/cloud/projects/abc123xyz/members" {
		t.Fatalf("wrong path: %s", rest.path)
	}
	for _, want := range []string{"owner", "owner@example.test", "member", "dev@example.test"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
}

// An id with no recorded address is still a real person on this project;
// a blank column would read as a bug rather than as missing data.
func TestListNamesAMissingAddressRatherThanLeavingItBlank(t *testing.T) {
	rest := &stubREST{reply: []Member{{UserID: "usr_9", Email: "", Role: "member"}}}
	out, err := run(t, rest, stubTarget{ref: "abc123xyz", isCloud: true}, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "not recorded") {
		t.Fatalf("a missing address was printed as emptiness:\n%s", out)
	}
}

func TestAddSendsTheEmail(t *testing.T) {
	rest := &stubREST{reply: Member{UserID: "usr_2", Email: "dev@example.test", Role: "member"}}
	out, err := run(t, rest, stubTarget{ref: "abc123xyz", isCloud: true}, "add", "dev@example.test")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if rest.method != "POST" {
		t.Fatalf("wrong method: %s", rest.method)
	}
	sent, _ := rest.body.(map[string]string)
	if sent["email"] != "dev@example.test" {
		t.Fatalf("the address did not reach the wire: %#v", rest.body)
	}
	if !strings.Contains(out, "Added dev@example.test") {
		t.Fatalf("no confirmation:\n%s", out)
	}
}

func TestRemoveTargetsTheMemberPath(t *testing.T) {
	rest := &stubREST{}
	if _, err := run(t, rest, stubTarget{ref: "abc123xyz", isCloud: true}, "remove", "usr_2"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if rest.method != "DELETE" || rest.path != "/v1/cloud/projects/abc123xyz/members/usr_2" {
		t.Fatalf("wrong call: %s %s", rest.method, rest.path)
	}
}

// A stack on this machine has no membership. Saying so beats a 404 from a
// control plane that has never heard of it.
func TestRefusesANonCloudTarget(t *testing.T) {
	rest := &stubREST{}
	_, err := run(t, rest, stubTarget{isCloud: false, describe: "https://localhost:8443"}, "list")
	if err == nil {
		t.Fatal("a local stack was accepted")
	}
	if !strings.Contains(err.Error(), "no membership") {
		t.Fatalf("unhelpful reason: %v", err)
	}
	if rest.method != "" {
		t.Fatal("it called the cloud anyway")
	}
}

// No `invite` / `accept` / `invitations`: this cloud cannot send an e-mail, and
// a verb whose whole purpose is a message that never arrives is worse than no
// verb at all.
func TestSurfaceHasNoInvitationVerbs(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
	}
	got := fmt.Sprint(names)
	if got != "[add list remove]" {
		t.Fatalf("unexpected surface: %s", got)
	}
}
