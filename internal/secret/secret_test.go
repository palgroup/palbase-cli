package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// trpcOK writes a tRPC success envelope.
func trpcOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{
			"data": map[string]any{
				"json": data,
			},
		},
	})
}

// studioAgainst spins an httptest server and returns a *studio.Client
// backed by it. TokenFn returns a static test token.
func studioAgainst(t *testing.T, h http.HandlerFunc) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(_ context.Context) (string, error) {
		return "test-token", nil
	})
}

// innerInput decodes the inner {"json":{...}} payload from a tRPC
// POST body and returns the inner object.
func innerInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
	return outer.JSON
}

// TestSecretSet_Plain tests `secret set KEY=value` without --secret flag.
func TestSecretSet_Plain(t *testing.T) {
	t.Chdir(t.TempDir())
	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/env.set", r.URL.Path)
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"set", "DATABASE_URL=postgres://localhost/db"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "app1prod", got["ref"])
	require.Equal(t, "DATABASE_URL", got["key"])
	require.Equal(t, "postgres://localhost/db", got["value"])
	require.Equal(t, false, got["isSecret"])
}

// TestSecretSet_Secret tests `secret set KEY=value --secret`.
func TestSecretSet_Secret(t *testing.T) {
	t.Chdir(t.TempDir())
	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"set", "--secret", "API_KEY=super-secret"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "app1prod", got["ref"])
	require.Equal(t, "API_KEY", got["key"])
	require.Equal(t, "super-secret", got["value"])
	require.Equal(t, true, got["isSecret"])
}

// TestSecretSet_ValueContainsEquals ensures KEY=a=b=c parses correctly.
func TestSecretSet_ValueContainsEquals(t *testing.T) {
	t.Chdir(t.TempDir())
	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"set", "KEY=a=b=c"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "KEY", got["key"])
	require.Equal(t, "a=b=c", got["value"])
}

// TestSecretSet_MissingEquals rejects input without `=`.
func TestSecretSet_MissingEquals(t *testing.T) {
	t.Chdir(t.TempDir())
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call API with invalid input")
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"set", "NOEQUALS"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	require.Error(t, cmd.Execute())
}

// TestSecretSet_RefusesReservedKey locks the PB_* reserved-namespace guard:
// `secret set PB_*` must be refused (managed by `palbase notifications add`) and
// must NOT call the API.
func TestSecretSet_RefusesReservedKey(t *testing.T) {
	t.Chdir(t.TempDir())
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call API for a reserved key")
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"set", "--secret", "PB_NOTIFICATIONS_APNS_P8=whatever"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "reserved")
	require.Contains(t, err.Error(), "notifications add")
}

// TestGuardReservedKey is a direct unit test of the namespace check.
func TestGuardReservedKey(t *testing.T) {
	t.Chdir(t.TempDir())
	require.Error(t, guardReservedKey("PB_NOTIFICATIONS_APNS_P8"))
	require.Error(t, guardReservedKey("PB_ANYTHING"))
	require.NoError(t, guardReservedKey("DATABASE_URL"))
	require.NoError(t, guardReservedKey("MY_API_KEY"))
}

// TestSecretList calls env.list and verifies both plain and secret rows.
func TestSecretList(t *testing.T) {
	t.Chdir(t.TempDir())
	secretVal := "hidden"
	rows := []map[string]any{
		{"key": "PORT", "isSecret": false, "value": "8080", "updatedAt": "2026-01-01T00:00:00Z"},
		{"key": "API_KEY", "isSecret": true, "value": secretVal, "updatedAt": "2026-01-02T00:00:00Z"},
	}
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Contains(t, r.URL.Path, "env.list")
		trpcOK(w, rows)
	})

	var out bytes.Buffer
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"list"})
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	output := out.String()
	// Plain var should show value.
	require.Contains(t, output, "PORT")
	require.Contains(t, output, "8080")
	// Secret var should be masked.
	require.Contains(t, output, "API_KEY")
	require.NotContains(t, output, secretVal)
}

// TestSecretRemove calls env.delete with the correct key.
func TestSecretRemove(t *testing.T) {
	t.Chdir(t.TempDir())
	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/env.delete", r.URL.Path)
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"remove", "DATABASE_URL"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "app1prod", got["ref"])
	require.Equal(t, "DATABASE_URL", got["key"])
}

// TestSecretRemove_RequiresKey asserts the remove subcommand is rejected
// when no key is given.
func TestSecretRemove_RequiresKey(t *testing.T) {
	t.Chdir(t.TempDir())
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call API without a key")
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"remove"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	require.Error(t, cmd.Execute())
}

var _ = strings.Contains // keep import used

// TestSecretSet_File uploads a multi-line file (PEM/JSON) as an encrypted secret
// via `secret set KEY --file <path>`. The KEY is bare (no =value) and the value
// is the file contents verbatim, secret-by-default.
func TestSecretSet_File(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	p8 := filepath.Join(dir, "AuthKey_XYZ.p8")
	pem := "-----BEGIN PRIVATE KEY-----\nMIGTAgEA...\nline2\n-----END PRIVATE KEY-----\n"
	require.NoError(t, os.WriteFile(p8, []byte(pem), 0o600))

	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/env.set", r.URL.Path)
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"set", "APNS_P8", "--file", p8})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "APNS_P8", got["key"])
	require.Equal(t, pem, got["value"], "multi-line PEM must survive verbatim")
	require.Equal(t, true, got["isSecret"], "a --file value is a secret by default")
}

// TestSecretSet_File_Plain stores a --file value as a non-secret with --plain.
func TestSecretSet_File_Plain(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	p := filepath.Join(dir, "config.txt")
	require.NoError(t, os.WriteFile(p, []byte("not-sensitive"), 0o600))

	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"set", "CFG", "--file", p, "--plain"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())
	require.Equal(t, false, got["isSecret"])
}

// TestSecretSet_File_RejectsKeyEqualsValue errors when --file is combined with KEY=value.
func TestSecretSet_File_RejectsKeyEqualsValue(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	require.NoError(t, os.WriteFile(p, []byte("v"), 0o600))
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) { trpcOK(w, nil) })
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }, Selection: selectiontest.Selected(t)})
	cmd.SetArgs([]string{"set", "K=v", "--file", p})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SilenceUsage = true
	require.Error(t, cmd.Execute())
}
