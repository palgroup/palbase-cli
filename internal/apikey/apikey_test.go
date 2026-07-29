package apikey

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// run executes `palbase apikey <args...>` against the fake v2 API with proj_1 /
// production selected, and returns stdout.
func run(t *testing.T, fake *selectiontest.Fake, args ...string) (string, error) {
	t.Helper()
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	rest := fake.REST()
	resolver := fake.Resolver()
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestApikeyCmd_Subcommands(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var got []string
	for _, c := range cmd.Commands() {
		got = append(got, c.Name())
	}
	sort.Strings(got)
	require.Equal(t, []string{"create", "list", "reveal", "revoke"}, got)
}

// THE path contract. Every apikey verb must address the ENVIRONMENT under its
// PROJECT. A stale v1 path (/api/v1/projects/{ref}/api-keys) 404s on the fake,
// which is exactly the silent failure this table exists to catch.
func TestApikey_HitsTheV2EnvironmentScopedPath(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments/app1prod/api-keys"

	tests := []struct {
		name  string
		args  []string
		route string
		reply func(f *selectiontest.Fake)
		query string
	}{
		{
			name: "list", args: []string{"list", "--json"},
			route: "GET " + base,
			reply: func(f *selectiontest.Fake) {
				f.OK("GET "+base, []map[string]any{{"id": "key_1", "scope": "c", "lookup_prefix": "pb_abc123"}})
			},
		},
		{
			name: "reveal", args: []string{"reveal", "--json"},
			route: "GET " + base, query: "reveal=true",
			reply: func(f *selectiontest.Fake) {
				f.OK("GET "+base, map[string]any{"environment_ref": "app1prod", "publishable_key": "pb_app1prod_c01234567890123456789", "keys": []any{}})
			},
		},
		{
			name: "create", args: []string{"create", "--name", "ci", "--json"},
			route: "POST " + base,
			reply: func(f *selectiontest.Fake) {
				f.Handle("POST "+base, func(w http.ResponseWriter, _ *http.Request) {
					selectiontest.WriteOK(w, http.StatusCreated, map[string]any{
						"id": "key_2", "environment_ref": "app1prod", "plaintext": "pb_app1prod_c01234567890123456789",
					})
				})
			},
		},
		{
			name: "revoke", args: []string{"revoke", "key_2", "--json"},
			route: "DELETE " + base + "/key_2",
			reply: func(f *selectiontest.Fake) { f.OK("DELETE "+base+"/key_2", map[string]any{"ok": true}) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := selectiontest.New(t)
			tc.reply(fake)

			_, err := run(t, fake, tc.args...)
			require.NoError(t, err)

			req, ok := fake.Find(tc.route)
			require.True(t, ok, "expected %s, got %v", tc.route, fake.Routes())
			require.Equal(t, tc.query, req.Query)
			for _, route := range fake.Routes() {
				require.NotContains(t, route, "/api/v1/", "no apikey verb may ride v1")
			}
		})
	}
}

func TestApikeyCreate_SendsOnlyTheName(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments/app1prod/api-keys"
	fake := selectiontest.New(t)
	fake.Handle("POST "+base, func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusCreated, map[string]any{
			"id": "key_2", "environment_ref": "app1prod", "plaintext": "pb_app1prod_c01234567890123456789",
		})
	})

	out, err := run(t, fake, "create", "--name", "ci", "--json")
	require.NoError(t, err)

	req, ok := fake.Find("POST " + base)
	require.True(t, ok)
	require.Equal(t, map[string]any{"name": "ci"}, req.Body)
	// The v2 route REJECTS key creation without this header — 400, every time,
	// which made `palbase apikey create` unusable on v0.13.0. Asserted on the
	// wire rather than on the call site, because the call site compiled fine
	// while sending nothing.
	require.NotEmpty(t, req.Header.Get("Idempotency-Key"),
		"apikey create MUST carry an Idempotency-Key")
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, "app1prod", got["environment_ref"])
	require.NotContains(t, got, "environmentRef")
}

func TestApikeyReveal_JSONUsesCanonicalFieldNames(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments/app1prod/api-keys"
	fake := selectiontest.New(t)
	fake.OK("GET "+base, map[string]any{
		"environment_ref": "app1prod",
		"publishable_key": "pb_app1prod_c01234567890123456789",
		"keys":            []any{},
	})

	out, err := run(t, fake, "reveal", "--json")
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, "app1prod", got["environment_ref"])
	require.Equal(t, "pb_app1prod_c01234567890123456789", got["publishable_key"])
	require.NotContains(t, got, "environmentRef")
	require.NotContains(t, got, "publishableKey")
}

func TestApikeyCreateAndReveal_RejectWrongResponseEnvironment(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments/app1prod/api-keys"
	tests := []struct {
		name  string
		args  []string
		value string
	}{
		{name: "create missing", args: []string{"create", "--name", "ci"}},
		{name: "create mismatch", args: []string{"create", "--name", "ci"}, value: "app1stg"},
		{name: "reveal missing", args: []string{"reveal"}},
		{name: "reveal mismatch", args: []string{"reveal"}, value: "app1stg"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := selectiontest.New(t)
			method := http.MethodGet
			response := map[string]any{"environment_ref": tc.value, "publishable_key": "pb_app1prod_c01234567890123456789"}
			if tc.args[0] == "create" {
				method = http.MethodPost
				response = map[string]any{"environment_ref": tc.value, "plaintext": "pb_app1prod_c01234567890123456789"}
			}
			fake.OK(method+" "+base, response)

			out, err := run(t, fake, tc.args...)
			require.ErrorContains(t, err, "environment_ref")
			require.Empty(t, out, "a response for the wrong environment must not emit credentials")
		})
	}
}

func TestApikeyCreateAndReveal_RejectMalformedOrForeignPublishableKeys(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments/app1prod/api-keys"
	tests := []struct {
		name      string
		operation string
		key       string
	}{
		{name: "create malformed", operation: "create", key: "pb_app1prod_cshort"},
		{name: "create foreign", operation: "create", key: "pb_app1stg_c01234567890123456789"},
		{name: "reveal malformed", operation: "reveal", key: "pb_app1prod_cshort"},
		{name: "reveal foreign", operation: "reveal", key: "pb_app1stg_c01234567890123456789"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := selectiontest.New(t)
			if tc.operation == "create" {
				fake.OK("POST "+base, map[string]any{
					"id": "key_2", "environment_ref": "app1prod", "plaintext": tc.key,
				})
			} else {
				fake.OK("GET "+base, map[string]any{
					"environment_ref": "app1prod", "publishable_key": tc.key,
				})
			}

			args := []string{tc.operation, "--json"}
			if tc.operation == "create" {
				args = append(args, "--name", "mobile")
			}
			out, err := run(t, fake, args...)
			require.ErrorContains(t, err, "publishable")
			require.Empty(t, out, "an unbound credential must not be emitted")
		})
	}
}

func TestApikeyReveal_RejectsAMissingPublishableKey(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments/app1prod/api-keys"
	fake := selectiontest.New(t)
	fake.OK("GET "+base, map[string]any{"environment_ref": "app1prod"})

	out, err := run(t, fake, "reveal", "--json")
	require.ErrorContains(t, err, "publishable")
	require.Empty(t, out, "a missing credential must not look like a successful reveal")
}

func TestApikeyCreate_RequiresName(t *testing.T) {
	fake := selectiontest.New(t)
	_, err := run(t, fake, "create")
	require.ErrorContains(t, err, "--name is required")
}

// The --environment override targets ANOTHER environment of the same project —
// this is the headless path (UAT CLI-005: keys are per-environment).
func TestApikeyList_EnvironmentOverrideRetargetsTheKeys(t *testing.T) {
	fake := selectiontest.New(t)
	fake.Environments["proj_1"] = append(fake.Environments["proj_1"],
		selectiontest.Env("env_stg", "proj_1", "app1stg", "staging", "staging", false))
	const stagingKeys = "/api/v2/projects/proj_1/environments/app1stg/api-keys"
	fake.OK("GET "+stagingKeys, []map[string]any{{"id": "key_stg", "scope": "c"}})

	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	rest := fake.REST()
	resolver := fake.Resolver()
	resolver.EnvironmentFlag = "staging"
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"list", "--json"})
	require.NoError(t, cmd.Execute())

	_, ok := fake.Find("GET " + stagingKeys)
	require.True(t, ok, "expected the STAGING keys, got %v", fake.Routes())
	_, prod := fake.Find("GET /api/v2/projects/proj_1/environments/app1prod/api-keys")
	require.False(t, prod, "production keys must not be touched")
}
