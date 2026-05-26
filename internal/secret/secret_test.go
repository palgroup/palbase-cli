package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
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
	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/env.set", r.URL.Path)
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"set", "--ref", "myproj", "DATABASE_URL=postgres://localhost/db"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "myproj", got["ref"])
	require.Equal(t, "DATABASE_URL", got["key"])
	require.Equal(t, "postgres://localhost/db", got["value"])
	require.Equal(t, false, got["isSecret"])
}

// TestSecretSet_Secret tests `secret set KEY=value --secret`.
func TestSecretSet_Secret(t *testing.T) {
	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"set", "--ref", "myproj", "--secret", "API_KEY=super-secret"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "myproj", got["ref"])
	require.Equal(t, "API_KEY", got["key"])
	require.Equal(t, "super-secret", got["value"])
	require.Equal(t, true, got["isSecret"])
}

// TestSecretSet_ValueContainsEquals ensures KEY=a=b=c parses correctly.
func TestSecretSet_ValueContainsEquals(t *testing.T) {
	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"set", "--ref", "myproj", "KEY=a=b=c"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "KEY", got["key"])
	require.Equal(t, "a=b=c", got["value"])
}

// TestSecretSet_MissingEquals rejects input without `=`.
func TestSecretSet_MissingEquals(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call API with invalid input")
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"set", "--ref", "myproj", "NOEQUALS"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	require.Error(t, cmd.Execute())
}

// TestSecretList calls env.list and verifies both plain and secret rows.
func TestSecretList(t *testing.T) {
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
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"list", "--ref", "myproj"})
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
	var got map[string]any
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/trpc/env.delete", r.URL.Path)
		got = innerInput(t, r)
		trpcOK(w, nil)
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"remove", "--ref", "myproj", "DATABASE_URL"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	require.Equal(t, "myproj", got["ref"])
	require.Equal(t, "DATABASE_URL", got["key"])
}

// TestSecretRemove_RequiresKey asserts the remove subcommand is rejected
// when no key is given.
func TestSecretRemove_RequiresKey(t *testing.T) {
	c := studioAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call API without a key")
	})
	cmd := Cmd(Resolvers{Studio: func() *studio.Client { return c }})
	cmd.SetArgs([]string{"remove", "--ref", "myproj"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	require.Error(t, cmd.Execute())
}

var _ = strings.Contains // keep import used
