package apikey

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
	reply        any
	err          error
}

func (s *stubREST) Do(_ context.Context, method, path string, _, out any) error {
	s.method, s.path = method, path
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

type stubTarget struct {
	ref      string
	isCloud  bool
	describe string
	gotPath  string
	reply    any
	err      error
}

func (t *stubTarget) Ref() (string, bool) { return t.ref, t.isCloud }
func (t *stubTarget) Describe() string    { return t.describe }
func (t *stubTarget) GetJSON(_ context.Context, path string, out any) error {
	t.gotPath = path
	if t.err != nil {
		return t.err
	}
	raw, err := json.Marshal(t.reply)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func run(t *testing.T, rest *stubREST, target *stubTarget, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd(Resolvers{
		REST:   func() REST { return rest },
		Target: func() (Target, error) { return target, nil },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// `list` asks the PROJECT about itself: the publishable key is not a secret and
// the project's own surface is the nearest authority for it.
func TestListReadsTheProjectsOwnSurface(t *testing.T) {
	target := &stubTarget{
		ref: "abc123xyz", isCloud: true, describe: "https://abc123xyz.v2.example",
		reply: map[string]string{"publishable": "pb_project_cPUB"},
	}
	out, err := run(t, &stubREST{}, target, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if target.gotPath != "/v1/management/keys" {
		t.Fatalf("wrong path: %s", target.gotPath)
	}
	if !strings.Contains(out, "pb_project_cPUB") {
		t.Fatalf("the publishable key is missing:\n%s", out)
	}
}

// The secret must NOT appear in a verb people run to see what exists: a
// terminal, a screen share and a scrollback buffer all keep what it prints.
func TestListNeverPrintsTheSecret(t *testing.T) {
	target := &stubTarget{
		ref: "abc123xyz", isCloud: true, describe: "https://abc123xyz.v2.example",
		reply: map[string]string{
			"publishable":  "pb_project_cPUB",
			"serviceRole":  "pb_project_sSECRET",
			"service_role": "pb_project_sSECRET",
		},
	}
	out, err := run(t, &stubREST{}, target, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(out, "sSECRET") {
		t.Fatalf("the service-role key leaked into `list`:\n%s", out)
	}
}

// `reveal` goes to the CLOUD: the project will not hand out its own secret.
func TestRevealAsksTheCloud(t *testing.T) {
	rest := &stubREST{reply: Keys{AnonKey: "pb_project_cPUB", ServiceRoleKey: "pb_project_sSEC"}}
	target := &stubTarget{ref: "abc123xyz", isCloud: true}
	out, err := run(t, rest, target, "", "reveal")
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if rest.method != "GET" || rest.path != "/v1/cloud/projects/abc123xyz/keys" {
		t.Fatalf("wrong call: %s %s", rest.method, rest.path)
	}
	if !strings.Contains(out, "pb_project_sSEC") {
		t.Fatalf("reveal printed no secret:\n%s", out)
	}
}

// A stack on this machine has no cloud ref. Asking the control plane about it
// would be asking the wrong authority, so the refusal names the reason.
func TestCloudVerbsRefuseANonCloudTarget(t *testing.T) {
	rest := &stubREST{}
	target := &stubTarget{isCloud: false, describe: "https://localhost:8443"}
	for _, args := range [][]string{{"reveal"}, {"rotate", "--yes"}} {
		verb := args[0]
		_, err := run(t, rest, target, "", args...)
		if err == nil {
			t.Fatalf("%s accepted a target that is not on this cloud", verb)
		}
		if !strings.Contains(err.Error(), "not a project on this cloud") {
			t.Fatalf("%s gave an unhelpful reason: %v", verb, err)
		}
		if rest.method != "" {
			t.Fatalf("%s called the cloud anyway", verb)
		}
	}
}

// Rotation stops every existing key. The confirmation must be something nobody
// satisfies by reflex.
func TestRotateRefusesAMismatchedConfirmation(t *testing.T) {
	rest := &stubREST{}
	target := &stubTarget{ref: "abc123xyz", isCloud: true}
	if _, err := run(t, rest, target, "nope\n", "rotate"); err == nil {
		t.Fatal("a mismatched confirmation was accepted")
	}
	if rest.method != "" {
		t.Fatalf("the rotation was sent anyway: %s %s", rest.method, rest.path)
	}
}

func TestRotateProceedsAndPrintsBothKeys(t *testing.T) {
	rest := &stubREST{reply: Keys{AnonKey: "pb_project_cNEW", ServiceRoleKey: "pb_project_sNEW"}}
	target := &stubTarget{ref: "abc123xyz", isCloud: true}
	out, err := run(t, rest, target, "abc123xyz\n", "rotate")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rest.method != "POST" || rest.path != "/v1/cloud/projects/abc123xyz/keys/rotate" {
		t.Fatalf("wrong call: %s %s", rest.method, rest.path)
	}
	for _, want := range []string{"pb_project_cNEW", "pb_project_sNEW", "restarting"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
}

// The surface carries only the verbs a two-key project can honour: there is no
// arbitrary set of named keys here, so `create` and `revoke` would be verbs for
// a shape that does not exist.
func TestSurfaceIsListRevealRotate(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
	}
	got := fmt.Sprint(names)
	if got != "[list reveal rotate]" {
		t.Fatalf("unexpected surface: %s", got)
	}
}
