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
	calls        int
}

func (f *fakeREST) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.calls++
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

// A settings change is typed, not hand-written JSON.
//
// `--json '{"password_min_length":10}'` asks a person to know the module's exact
// field names, which is the thing a CLI exists to hide — and getting one wrong is
// a settings write that silently does nothing, because the module ignores fields
// it does not read. The named flags compose the body; --json stays for anything
// they do not cover, and the two merge rather than one winning silently.
func TestSettingsSetTakesNamedFlagsAndNotOnlyJSON(t *testing.T) {
	rest := &fakeREST{}
	run(t, rest, "settings", "set", "--password-min", "10", "--password-max", "48")
	var sent map[string]any
	if err := json.Unmarshal(rest.body, &sent); err != nil {
		t.Fatalf("the body is not JSON: %s", rest.body)
	}
	if sent["password_min_length"] != float64(10) || sent["password_max_length"] != float64(48) {
		t.Errorf("the named flags did not become the module's fields: %s", rest.body)
	}
	if rest.method != http.MethodPut || rest.path != "/v1/management/auth/settings" {
		t.Errorf("%s %s", rest.method, rest.path)
	}
}

func TestSettingsSetMergesJSONWithTheNamedFlags(t *testing.T) {
	rest := &fakeREST{}
	run(t, rest, "settings", "set", "--password-min", "12", "--json", `{"site_url":"https://example.com"}`)
	var sent map[string]any
	_ = json.Unmarshal(rest.body, &sent)
	if sent["password_min_length"] != float64(12) {
		t.Errorf("the flag was lost: %s", rest.body)
	}
	if sent["site_url"] != "https://example.com" {
		t.Errorf("the JSON was lost: %s", rest.body)
	}
}

func TestSettingsSetWithNothingToSaySaysSo(t *testing.T) {
	rest := &fakeREST{}
	cmd := Cmd(Resolvers{REST: func(*cobra.Command) (REST, error) { return rest, nil }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"settings", "set"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("a settings write with no fields was sent")
	}
	if !strings.Contains(err.Error(), "nothing to send") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// A named change is READ-MODIFY-WRITE, because the module's PUT replaces the
// whole document.
//
// Measured against a live stack: `auth settings set --password-min 13` sent
// {"password_min_length":13} and was refused —
// "password_max_length must be between password_min_length and 64" — because the
// absent maximum arrived as zero. A person saying "make the minimum 13" is not
// saying "and forget everything else".
func TestSettingsSetReadsBeforeItWrites(t *testing.T) {
	rest := &fakeREST{answer: `{"password_min_length":8,"password_max_length":64,"confirm_email_required":false,"site_url":"https://kept.example"}`}
	run(t, rest, "settings", "set", "--password-min", "13")

	if rest.calls < 2 {
		t.Fatalf("the command wrote without reading: %d call(s)", rest.calls)
	}
	var sent map[string]any
	if err := json.Unmarshal(rest.body, &sent); err != nil {
		t.Fatalf("body is not JSON: %s", rest.body)
	}
	if sent["password_min_length"] != float64(13) {
		t.Errorf("the change did not travel: %s", rest.body)
	}
	if sent["password_max_length"] != float64(64) {
		t.Errorf("the untouched maximum was dropped — the module reads that as zero: %s", rest.body)
	}
	if sent["site_url"] != "https://kept.example" {
		t.Errorf("a field nobody mentioned was erased: %s", rest.body)
	}
}
