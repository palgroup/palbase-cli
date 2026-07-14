package apikey

import (
	"bytes"
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
	resolver := fake.Resolver(&bytes.Buffer{})
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return out.String(), func() error { err := cmd.Execute(); return err }()
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
				f.OK("GET "+base, map[string]any{"environmentRef": "app1prod", "publishableKey": "pb_app1prod_cx", "keys": []any{}})
			},
		},
		{
			name: "create", args: []string{"create", "--name", "ci", "--json"},
			route: "POST " + base,
			reply: func(f *selectiontest.Fake) {
				f.Handle("POST "+base, func(w http.ResponseWriter, _ *http.Request) {
					selectiontest.WriteOK(w, http.StatusCreated, map[string]any{"id": "key_2", "plaintext": "pb_app1prod_cnew"})
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
		selectiontest.WriteOK(w, http.StatusCreated, map[string]any{"id": "key_2", "plaintext": "pb_x"})
	})

	_, err := run(t, fake, "create", "--name", "ci", "--json")
	require.NoError(t, err)

	req, ok := fake.Find("POST " + base)
	require.True(t, ok)
	require.Equal(t, map[string]any{"name": "ci"}, req.Body)
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
	resolver := fake.Resolver(&bytes.Buffer{})
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
