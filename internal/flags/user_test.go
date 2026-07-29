package flags

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
	"github.com/palgroup/palbase-cli/internal/studio"
)

// trpcCall is one recorded request: its method, path, raw query, and the RAW
// body bytes. Raw, not decoded — the point of these tests is the wire, and a
// decoded map cannot tell JSON `true` from the string `"true"` once it has been
// through an `any`.
type trpcCall struct {
	Method string
	Path   string
	Query  string
	Body   string
}

// fakeTRPC is an httptest-backed Studio tRPC server that records every call and
// answers each procedure from `replies`. An unexpected procedure FAILS the test
// rather than being stubbed away — a command that calls the wrong procedure name
// is exactly the silent 404 this exists to catch.
type fakeTRPC struct {
	t       *testing.T
	replies map[string]any
	calls   []trpcCall
	client  Studio
}

func newFakeTRPC(t *testing.T, replies map[string]any) *fakeTRPC {
	t.Helper()
	f := &fakeTRPC{t: t, replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.client = studio.New(
		srv.URL,
		func(context.Context) (string, error) { return "test-token", nil },
		func(context.Context, string, string, string) (string, error) { return "test-proof", nil },
	)
	return f
}

func (f *fakeTRPC) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, trpcCall{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(raw),
	})
	proc := strings.TrimPrefix(r.URL.Path, "/api/trpc/")
	data, ok := f.replies[proc]
	if !ok {
		f.t.Errorf("unexpected tRPC procedure %q", proc)
		http.Error(w, "no such procedure", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// only returns the single recorded call, failing when there was not exactly one.
func (f *fakeTRPC) only() trpcCall {
	f.t.Helper()
	require.Len(f.t, f.calls, 1, "expected exactly one tRPC call")
	return f.calls[0]
}

// runFlags drives `palbase flags <args...>` against the fake tRPC server with
// proj_1 / production selected, and returns stdout.
func runFlags(t *testing.T, f *fakeTRPC, args ...string) (string, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	cmd := Cmd(Resolvers{
		Studio:    func() Studio { return f.client },
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
		body string
	}{
		{
			// A dotted `palbase.` key is the reason these commands exist —
			// `flags add` rejects it, and that rejection must not reach here.
			name: "boolean",
			args: []string{"user", "set", "usr_42", "palbase.debug_console", "--type", "boolean", "--value", "true"},
			body: `{"json":{"key":"palbase.debug_console","ref":"app1prod","userId":"usr_42","value":true}}`,
		},
		{
			name: "boolean false",
			args: []string{"user", "set", "usr_42", "palbase.debug_console", "--type", "boolean", "--value", "false"},
			body: `{"json":{"key":"palbase.debug_console","ref":"app1prod","userId":"usr_42","value":false}}`,
		},
		{
			name: "number",
			args: []string{"user", "set", "usr_42", "max_uploads", "--type", "number", "--value", "50"},
			body: `{"json":{"key":"max_uploads","ref":"app1prod","userId":"usr_42","value":50}}`,
		},
		{
			// The one that proves the types are not interchangeable: the SAME
			// literal `true`, declared a string, must be quoted on the wire.
			name: "string that looks like a boolean",
			args: []string{"user", "set", "usr_42", "theme", "--type", "string", "--value", "true"},
			body: `{"json":{"key":"theme","ref":"app1prod","userId":"usr_42","value":"true"}}`,
		},
		{
			name: "string",
			args: []string{"user", "set", "usr_42", "theme", "--type", "string", "--value", "dark"},
			body: `{"json":{"key":"theme","ref":"app1prod","userId":"usr_42","value":"dark"}}`,
		},
		{
			name: "json object is compacted, not re-quoted",
			args: []string{"user", "set", "usr_42", "limits", "--type", "json", "--value", `{"daily": 10}`},
			body: `{"json":{"key":"limits","ref":"app1prod","userId":"usr_42","value":{"daily":10}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeTRPC(t, map[string]any{
				"userFlags.users.put": map[string]any{"key": "k", "value": true, "source": "override"},
			})
			_, err := runFlags(t, f, tc.args...)
			require.NoError(t, err)

			call := f.only()
			require.Equal(t, http.MethodPost, call.Method)
			require.Equal(t, "/api/trpc/userFlags.users.put", call.Path)
			require.Equal(t, tc.body, strings.TrimSpace(call.Body))
		})
	}
}

// The user id and the key are two opaque positional arguments in a row, so the
// swap is the mistake that will actually happen. It is refused locally, before
// any request goes out, and the message names the order.
func TestFlagsUserSet_RejectsSwappedUserIDAndKey(t *testing.T) {
	f := newFakeTRPC(t, map[string]any{})
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
			f := newFakeTRPC(t, map[string]any{})
			_, err := runFlags(t, f, tc.args...)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
			require.Empty(t, f.calls, "an unencodable value must not reach the server")
		})
	}
}

func TestFlagsUserUnset_Wire(t *testing.T) {
	f := newFakeTRPC(t, map[string]any{
		"userFlags.users.delete": map[string]any{
			"key": "palbase.debug_console", "value": false, "source": "system",
		},
	})
	out, err := runFlags(t, f, "user", "unset", "usr_42", "palbase.debug_console")
	require.NoError(t, err)

	call := f.only()
	require.Equal(t, http.MethodPost, call.Method)
	require.Equal(t, "/api/trpc/userFlags.users.delete", call.Path)
	require.Equal(t,
		`{"json":{"key":"palbase.debug_console","ref":"app1prod","userId":"usr_42"}}`,
		strings.TrimSpace(call.Body))
	// The Environment value that now applies is reported back from the response.
	require.Contains(t, out, "environment value: false")
}

func TestFlagsUserClear_Wire(t *testing.T) {
	f := newFakeTRPC(t, map[string]any{
		"userFlags.users.deleteAll": map[string]any{"deleted": 3},
	})
	out, err := runFlags(t, f, "user", "clear", "usr_42")
	require.NoError(t, err)

	call := f.only()
	require.Equal(t, http.MethodPost, call.Method)
	require.Equal(t, "/api/trpc/userFlags.users.deleteAll", call.Path)
	require.Equal(t, `{"json":{"ref":"app1prod","userId":"usr_42"}}`, strings.TrimSpace(call.Body))
	require.Contains(t, out, "removed 3 override(s)")
}

// list is TWO reads: the merged per-user view, plus the Environment's flag list
// to say which value won. Both are queries (GET, input in the query string).
func TestFlagsUserList_WireAndInferredSource(t *testing.T) {
	f := newFakeTRPC(t, map[string]any{
		"userFlags.users.get": map[string]any{
			"values":     map[string]any{"palbase.debug_console": true, "theme": "dark"},
			"fetched_at": "2026-07-29T00:00:00Z",
		},
		"userFlags.system.list": []any{
			map[string]any{"key": "palbase.debug_console", "value": false, "value_type": "bool"},
			map[string]any{"key": "theme", "value": "dark", "value_type": "string"},
		},
	})
	out, err := runFlags(t, f, "user", "list", "usr_42", "--json")
	require.NoError(t, err)

	require.Len(t, f.calls, 2)
	require.Equal(t, http.MethodGet, f.calls[0].Method)
	require.Equal(t, "/api/trpc/userFlags.users.get", f.calls[0].Path)
	require.Equal(t, `{"json":{"ref":"app1prod","userId":"usr_42"}}`, inputParam(t, f.calls[0].Query))
	require.Equal(t, http.MethodGet, f.calls[1].Method)
	require.Equal(t, "/api/trpc/userFlags.system.list", f.calls[1].Path)
	require.Equal(t, `{"json":{"ref":"app1prod"}}`, inputParam(t, f.calls[1].Query))

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
	f := newFakeTRPC(t, map[string]any{
		"userFlags.users.get": map[string]any{
			"values": map[string]any{"palbase.debug_console": true}, "fetched_at": "x",
		},
		"userFlags.system.list": []any{
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

// The override commands are runtime state. Writing config/flags.ts here would
// put an Environment-specific, per-user value into git.
func TestFlagsUser_NeverTouchesConfigFile(t *testing.T) {
	f := newFakeTRPC(t, map[string]any{
		"userFlags.users.put": map[string]any{"key": "k", "value": true, "source": "override"},
	})
	_, err := runFlags(t, f,
		"user", "set", "usr_42", "palbase.debug_console", "--type", "boolean", "--value", "true")
	require.NoError(t, err)
	_, statErr := os.Stat(configPath)
	require.True(t, os.IsNotExist(statErr), "flags user set must not write %s", configPath)
}

// The counterpart guard: `flags add` still refuses a dotted key, because it
// cannot live in config/flags.ts. Loosening the override key rule must not have
// loosened this one.
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

// inputParam pulls the tRPC `input` query parameter out of a recorded GET.
func inputParam(t *testing.T, rawQuery string) string {
	t.Helper()
	q, err := url.ParseQuery(rawQuery)
	require.NoError(t, err)
	return q.Get("input")
}
