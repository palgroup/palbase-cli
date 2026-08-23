package authadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fakeREST records the one call a verb makes, because that is the property that
// actually breaks: a command that reaches the wrong path fails in production
// against a real stack and passes every test that only checks its flags.
type fakeREST struct {
	method, path string
	body         []byte
	status       int
	answer       string
}

func (f *fakeREST) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.method, f.path, f.body = method, path, body
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

func run(t *testing.T, rest *fakeREST, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("palbase auth %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// TestVerbsReachTheEndpointsTheContractPublishes is the whole test worth having
// here. These commands hold no logic of their own — they name a path and print
// what came back — so the only way they can be wrong is by naming the wrong one.
func TestVerbsReachTheEndpointsTheContractPublishes(t *testing.T) {
	cases := []struct {
		args         []string
		wantMethod   string
		wantPath     string
		wantBodyPart string
	}{
		{[]string{"settings", "get"}, http.MethodGet, "/v1/management/auth/settings", ""},
		{[]string{"providers", "list"}, http.MethodGet, "/v1/management/auth/providers", ""},
		{[]string{"providers", "enable", "google"}, http.MethodPost, "/v1/management/auth/providers/google", `"enabled":true`},
		{[]string{"providers", "disable", "google"}, http.MethodPost, "/v1/management/auth/providers/google", `"enabled":false`},
		{[]string{"providers", "config", "clear", "google"}, http.MethodDelete, "/v1/management/auth/providers/google/config", ""},
		{[]string{"sessions", "list"}, http.MethodGet, "/v1/management/auth/sessions", ""},
		{[]string{"sessions", "revoke", "s-1"}, http.MethodDelete, "/v1/management/auth/sessions/s-1", ""},
		{[]string{"sessions", "revoke-all", "u-1"}, http.MethodPost, "/v1/management/auth/users/u-1/sessions/revoke-all", ""},
		{[]string{"audit"}, http.MethodGet, "/v1/management/auth/audit", ""},
		{[]string{"templates", "list"}, http.MethodGet, "/v1/management/auth/templates", ""},
		{[]string{"templates", "get", "confirm"}, http.MethodGet, "/v1/management/auth/templates/confirm", ""},
		{[]string{"mfa", "get", "u-1"}, http.MethodGet, "/v1/management/auth/users/u-1/mfa", ""},
		{[]string{"mfa", "reset", "u-1"}, http.MethodDelete, "/v1/management/auth/users/u-1/mfa", ""},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			rest := &fakeREST{answer: `{"ok":true}`}
			run(t, rest, tc.args...)
			if rest.method != tc.wantMethod || rest.path != tc.wantPath {
				t.Fatalf("called %s %s, want %s %s", rest.method, rest.path, tc.wantMethod, tc.wantPath)
			}
			if tc.wantBodyPart != "" && !strings.Contains(string(rest.body), tc.wantBodyPart) {
				t.Fatalf("body = %s, want it to carry %s", rest.body, tc.wantBodyPart)
			}
		})
	}
}

// TestAnswersAreMachineReadable closes FR-026 on the CLI side: the same surface
// serves a panel and a script, so what comes out of a script's end of it has to
// be parseable rather than prose with a JSON body somewhere inside.
func TestAnswersAreMachineReadable(t *testing.T) {
	rest := &fakeREST{answer: `{"site_url":"https://x","password":{"min_length":10}}`}
	out := run(t, rest, "settings", "get")

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("output is not JSON a script can read: %v\n%s", err, out)
	}
	if got["site_url"] != "https://x" {
		t.Fatalf("the module's answer did not survive the print: %v", got)
	}
}

// TestARefusalIsReportedNotSwallowed keeps a failed verb from reading as a
// successful one — the shape of failure that makes a person believe a setting
// changed when it did not.
func TestARefusalIsReportedNotSwallowed(t *testing.T) {
	rest := &fakeREST{status: http.StatusBadRequest, answer: `{"error":"egress_invalid","error_description":"nope"}`}
	var out bytes.Buffer
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"settings", "get"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("a 400 exited 0: a script would treat the refusal as a change")
	}
	if !strings.Contains(err.Error(), "nope") && !strings.Contains(out.String(), "nope") {
		t.Fatalf("the module's own reason was dropped: %v / %s", err, out.String())
	}
}
