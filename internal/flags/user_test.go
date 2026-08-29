package flags

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// stackCall is one recorded request: its method, path, and the RAW body bytes.
// Raw, not decoded — the point of these tests is the wire, and a decoded map
// cannot tell JSON `true` from the string `"true"` once it has been through an
// `any`.
type stackCall struct {
	Method string
	Path   string
	Body   string
}

// fakeStack is an httptest-backed PROJECT that records every call and answers
// each route from `replies`, keyed "METHOD path". An unexpected route FAILS the
// test rather than being stubbed away — a command that calls the wrong path is
// exactly the silent 404 this exists to catch.
type fakeStack struct {
	t       *testing.T
	replies map[string]any
	calls   []stackCall
}

func newFakeStack(t *testing.T, replies map[string]any) *fakeStack {
	t.Helper()
	return &fakeStack{t: t, replies: replies}
}

// Do satisfies flags.REST — the same seam the definition half already used.
func (f *fakeStack) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	f.calls = append(f.calls, stackCall{Method: method, Path: path, Body: string(body)})
	data, ok := f.replies[method+" "+path]
	if !ok {
		f.t.Errorf("unexpected route %s %s", method, path)
		return http.StatusNotFound, []byte(`{"error":"not_found"}`), nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, raw, nil
}

// only returns the single recorded call, failing when there was not exactly one.
func (f *fakeStack) only() stackCall {
	f.t.Helper()
	require.Len(f.t, f.calls, 1, "expected exactly one call")
	return f.calls[0]
}

// runFlags drives `palbase flags <args...>` against the fake project with
// proj_1 / production selected, and returns stdout.
func runFlags(t *testing.T, f *fakeStack, args ...string) (string, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	cmd := Cmd(Resolvers{
		REST:      func(*cobra.Command) (REST, error) { return f, nil },
		Selection: selectiontest.Selected(t),
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	// Two statements on purpose: `return out.String(), cmd.Execute()` evaluates
	// the operands left to right, so it would snapshot an empty buffer.
	err := cmd.Execute()
	return out.String(), err
}

func TestFlagsUser_Surface(t *testing.T) {
	parent := Cmd(Resolvers{})
	subs := map[string]*cobra.Command{}
	for _, c := range parent.Commands() {
		subs[c.Name()] = c
	}
	// The config-as-code half must survive the addition.
	for _, name := range []string{"list", "add", "remove", "user"} {
		require.Contains(t, subs, name)
	}

	userSubs := map[string]*cobra.Command{}
	for _, c := range subs["user"].Commands() {
		userSubs[c.Name()] = c
	}
	for _, name := range []string{"set", "unset", "list", "clear"} {
		require.Contains(t, userSubs, name, "flags user must have a `%s` subcommand", name)
	}
	for _, flag := range []string{"type", "value"} {
		require.NotNil(t, userSubs["set"].Flags().Lookup(flag), "set must define --%s", flag)
	}
	require.NotNil(t, userSubs["list"].Flags().Lookup("json"))
}

// THE wire contract for `set`, one case per value type. The body is compared as
// an EXACT string: `--value true` must land on the wire as JSON `true`, and a
// serialiser that quoted it (`"true"`) would pass a decoded-map assertion while
// writing the wrong type into the flag.
func TestFlagsUserSet_WireBodyPerValueType(t *testing.T) {
	tests := []struct {
		name string
		args []string
		key  string
		body string
	}{
		{
			// A dotted `palbase.` key is the reason these commands exist —
			// `flags add` rejects it, and that rejection must not reach here.
			name: "boolean",
			args: []string{"user", "set", "usr_42", "palbase.debug_console", "--type", "boolean", "--value", "true"},
			key:  "palbase.debug_console",
			body: `{"value":true}`,
		},
		{
			name: "boolean false",
			args: []string{"user", "set", "usr_42", "palbase.debug_console", "--type", "boolean", "--value", "false"},
			key:  "palbase.debug_console",
			body: `{"value":false}`,
		},
		{
			name: "number",
			args: []string{"user", "set", "usr_42", "max_uploads", "--type", "number", "--value", "50"},
			key:  "max_uploads",
			body: `{"value":50}`,
		},
		{
			// The one that proves the types are not interchangeable: the SAME
			// literal `true`, declared a string, must be quoted on the wire.
			name: "string that looks like a boolean",
			args: []string{"user", "set", "usr_42", "theme", "--type", "string", "--value", "true"},
			key:  "theme",
			body: `{"value":"true"}`,
		},
		{
			name: "string",
			args: []string{"user", "set", "usr_42", "theme", "--type", "string", "--value", "dark"},
			key:  "theme",
			body: `{"value":"dark"}`,
		},
		{
			name: "json object is compacted, not re-quoted",
			args: []string{"user", "set", "usr_42", "limits", "--type", "json", "--value", `{"daily": 10}`},
			key:  "limits",
			body: `{"value":{"daily":10}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := "/v1/user-flags/users/usr_42/" + tc.key
			f := newFakeStack(t, map[string]any{
				"PUT " + path: map[string]any{"key": "k", "value": true, "source": "override"},
			})
			_, err := runFlags(t, f, tc.args...)
			require.NoError(t, err)

			call := f.only()
			require.Equal(t, http.MethodPut, call.Method)
			require.Equal(t, path, call.Path)
			require.Equal(t, tc.body, strings.TrimSpace(call.Body))
		})
	}
}

// The user id and the key are two opaque positional arguments in a row, so the
// swap is the mistake that will actually happen. It is refused locally, before
// any request goes out, and the message names the order.
func TestFlagsUserSet_RejectsSwappedUserIDAndKey(t *testing.T) {
	f := newFakeStack(t, map[string]any{})
	_, err := runFlags(t, f,
		"user", "set", "palbase.debug_console", "550e8400-e29b-41d4-a716-446655440000",
		"--type", "boolean", "--value", "true")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid flag key")
	require.Contains(t, err.Error(), "<user-id> <key>")
	require.Empty(t, f.calls, "a rejected key must not reach the server")
}

// A wrong shape is an error, never a coercion: nothing is guessed into a
// different JSON type behind the caller's back.
func TestFlagsUserSet_RejectsWrongShape(t *testing.T) {
	tests := []struct {
		name, wantErr string
		args          []string
	}{
		{
			name: "boolean given a yes", wantErr: "must be true or false",
			args: []string{"user", "set", "usr_42", "k", "--type", "boolean", "--value", "yes"},
		},
		{
			name: "number given a word", wantErr: "must be a number",
			args: []string{"user", "set", "usr_42", "k", "--type", "number", "--value", "ten"},
		},
		{
			name: "json given a fragment", wantErr: "must be valid JSON",
			args: []string{"user", "set", "usr_42", "k", "--type", "json", "--value", `{"daily":`},
		},
		{
			name: "unknown type", wantErr: "invalid --type",
			args: []string{"user", "set", "usr_42", "k", "--type", "bool", "--value", "true"},
		},
		{
			name: "type omitted", wantErr: "--type is required",
			args: []string{"user", "set", "usr_42", "k", "--value", "true"},
		},
		{
			name: "value omitted", wantErr: "--value is required",
			args: []string{"user", "set", "usr_42", "k", "--type", "boolean"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeStack(t, map[string]any{})
			_, err := runFlags(t, f, tc.args...)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
			require.Empty(t, f.calls, "an unencodable value must not reach the server")
		})
	}
}

func TestFlagsUserUnset_Wire(t *testing.T) {
	f := newFakeStack(t, map[string]any{
		"DELETE /v1/user-flags/users/usr_42/palbase.debug_console": map[string]any{
			"key": "palbase.debug_console", "value": false, "source": "system",
		},
	})
	out, err := runFlags(t, f, "user", "unset", "usr_42", "palbase.debug_console")
	require.NoError(t, err)

	call := f.only()
	require.Equal(t, http.MethodDelete, call.Method)
	require.Equal(t, "/v1/user-flags/users/usr_42/palbase.debug_console", call.Path)
	require.Empty(t, strings.TrimSpace(call.Body), "a delete carries no body")
	// The Environment value that now applies is reported back from the response.
	require.Contains(t, out, "environment value: false")
}

func TestFlagsUserClear_Wire(t *testing.T) {
	f := newFakeStack(t, map[string]any{
		"DELETE /v1/user-flags/users/usr_42": map[string]any{"deleted": 3},
	})
	out, err := runFlags(t, f, "user", "clear", "usr_42")
	require.NoError(t, err)

	call := f.only()
	require.Equal(t, http.MethodDelete, call.Method)
	require.Equal(t, "/v1/user-flags/users/usr_42", call.Path)
	require.Contains(t, out, "removed 3 override(s)")
}

// list is TWO reads: the merged per-user view, plus the Environment's flag list
// to say which value won. Both are queries (GET, input in the query string).
func TestFlagsUserList_WireAndInferredSource(t *testing.T) {
	f := newFakeStack(t, map[string]any{
		"GET /v1/user-flags/users/usr_42": map[string]any{
			"values":     map[string]any{"palbase.debug_console": true, "theme": "dark"},
			"fetched_at": "2026-07-29T00:00:00Z",
		},
		"GET /v1/user-flags/system": []any{
			map[string]any{"key": "palbase.debug_console", "value": false, "value_type": "bool"},
			map[string]any{"key": "theme", "value": "dark", "value_type": "string"},
		},
	})
	out, err := runFlags(t, f, "user", "list", "usr_42", "--json")
	require.NoError(t, err)

	require.Len(t, f.calls, 2)
	require.Equal(t, http.MethodGet, f.calls[0].Method)
	require.Equal(t, "/v1/user-flags/users/usr_42", f.calls[0].Path)
	require.Equal(t, http.MethodGet, f.calls[1].Method)
	require.Equal(t, "/v1/user-flags/system", f.calls[1].Path)

	var rows []userFlagRow
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	require.Len(t, rows, 2)
	// Sorted by key; the effective value differs from the Environment value, so
	// the managed flag reads as overridden.
	require.Equal(t, "palbase.debug_console", rows[0].Key)
	require.JSONEq(t, `true`, string(rows[0].Value))
	require.JSONEq(t, `false`, string(rows[0].EnvironmentValue))
	require.True(t, rows[0].Overridden)
	// Equal values: no override is claimed.
	require.Equal(t, "theme", rows[1].Key)
	require.False(t, rows[1].Overridden)
}

func TestFlagsUserList_HumanOutputMarksTheWinner(t *testing.T) {
	f := newFakeStack(t, map[string]any{
		"GET /v1/user-flags/users/usr_42": map[string]any{
			"values": map[string]any{"palbase.debug_console": true}, "fetched_at": "x",
		},
		"GET /v1/user-flags/system": []any{
			map[string]any{"key": "palbase.debug_console", "value": false},
		},
	})
	out, err := runFlags(t, f, "user", "list", "usr_42")
	require.NoError(t, err)
	require.Contains(t, out, "palbase.debug_console")
	require.Contains(t, out, "override")
	// The inference caveat is stated, not implied.
	require.Contains(t, out, "source is inferred")
}

// legacyConfigPath is the file `palbase flags` USED to write. Production no
// longer knows this name — the constant lives here, in the guard, because the
// only remaining question about it is "did we start writing it again?".
//
// The guard outlived its original reason and got a stronger one. It began as
// "override commands are runtime state; writing config/flags.ts would put an
// Environment-specific, per-user value into git". Now NO flags command may write
// it: the declaration was applied to nothing after the applier was retired, so
// the file could only ever mislead — a project declaring flags that the stack
// never held.
const legacyConfigPath = "config/flags.ts"

func TestFlagsUser_NeverTouchesConfigFile(t *testing.T) {
	f := newFakeStack(t, map[string]any{
		"PUT /v1/user-flags/users/usr_42/palbase.debug_console": map[string]any{
			"key": "k", "value": true, "source": "override",
		},
	})
	_, err := runFlags(t, f,
		"user", "set", "usr_42", "palbase.debug_console", "--type", "boolean", "--value", "true")
	require.NoError(t, err)
	_, statErr := os.Stat(legacyConfigPath)
	require.True(t, os.IsNotExist(statErr), "no flags command may write %s — it is gone", legacyConfigPath)
}

// The counterpart guard: `flags add` still refuses a dotted key. The reason
// moved with the file — it used to be "a dotted key cannot live in
// config/flags.ts"; it is now the stack's own key shape, which `flags add`
// checks locally so the mistake is caught before the round trip. Loosening the
// override key rule must not have loosened this one.
func TestFlagsAdd_StillRejectsDottedKey(t *testing.T) {
	t.Chdir(t.TempDir())
	cmd := Cmd(Resolvers{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "palbase.debug_console", "--type", "boolean", "--default", "true"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid flag key")
}
