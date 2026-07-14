package env

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

func newCmd(t *testing.T, fake *selectiontest.Fake) (*bytes.Buffer, func(...string) error) {
	t.Helper()
	rest := fake.REST()
	resolver := fake.Resolver(&bytes.Buffer{})
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(bytes.NewBufferString("y\n"))
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return &out, func(args ...string) error {
		cmd.SetArgs(args)
		return cmd.Execute()
	}
}

// withStaging seeds a second environment so `use`/`archive`/`delete` have a
// non-production target.
func withStaging(f *selectiontest.Fake) *selectiontest.Fake {
	f.Environments["proj_1"] = append(f.Environments["proj_1"],
		selectiontest.Env("env_stg", "proj_1", "app1stg", "staging", "staging", false))
	return f
}

func TestEnvCmd_CanonicalSurface(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var got []string
	for _, c := range cmd.Commands() {
		got = append(got, c.Name())
	}
	sort.Strings(got)
	// spec §7.3: create|list|use|status|archive|wake|delete. Nothing else — and
	// crucially no `switch`, which is what the retired `branch` command had.
	require.Equal(t, []string{"archive", "create", "delete", "list", "status", "use", "wake"}, got)
}

func TestEnv_HitsTheV2Paths(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments"

	tests := []struct {
		name  string
		args  []string
		route string
		reply func(f *selectiontest.Fake)
	}{
		{
			name: "list", args: []string{"list", "--json"},
			route: "GET " + base,
		},
		{
			name: "status", args: []string{"status", "--json"},
			route: "GET " + base + "/app1prod",
			reply: func(f *selectiontest.Fake) {
				f.OK("GET "+base+"/app1prod", map[string]any{
					"id": "env_prod", "ref": "app1prod", "slug": "production", "kind": "production",
					"status": "active", "organization_id": "org_1", "tier": "free",
					"url": "https://app1prod.dev.palbase.studio",
				})
			},
		},
		{
			name: "archive", args: []string{"archive", "staging", "--json"},
			route: "POST " + base + "/app1stg/archive",
			reply: func(f *selectiontest.Fake) {
				withStaging(f).Accepted("POST "+base+"/app1stg/archive", map[string]any{"workflowId": "wf", "runId": "r"})
			},
		},
		{
			name: "wake", args: []string{"wake", "staging", "--json"},
			route: "POST " + base + "/app1stg/wake",
			reply: func(f *selectiontest.Fake) {
				withStaging(f).Accepted("POST "+base+"/app1stg/wake", map[string]any{"workflowId": "wf", "runId": "r"})
			},
		},
		{
			name: "delete", args: []string{"delete", "staging", "--yes", "--json"},
			route: "DELETE " + base + "/app1stg",
			reply: func(f *selectiontest.Fake) {
				withStaging(f).Handle("DELETE "+base+"/app1stg", func(w http.ResponseWriter, _ *http.Request) {
					selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{"workflowId": "wf", "runId": "r"})
				})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := selectiontest.Chdir(t)
			selectiontest.WriteConfig(t, dir, nil)
			fake := selectiontest.New(t)
			if tc.reply != nil {
				tc.reply(fake)
			}
			_, exec := newCmd(t, fake)
			require.NoError(t, exec(tc.args...))

			_, ok := fake.Find(tc.route)
			require.True(t, ok, "expected %s, got %v", tc.route, fake.Routes())
			for _, route := range fake.Routes() {
				require.NotContains(t, route, "/api/v1/")
				require.NotContains(t, route, "/branches", "the Palbase branch is gone as a resource")
			}
		})
	}
}

// `env create staging --from production` is the SAFE COPY. The body must carry
// the SOURCE ENVIRONMENT ref, and it must NOT opt into production rows or secret
// plaintext by default — that default is the whole security property.
func TestEnvCreate_SafeCopyByDefault(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments"
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	fake := selectiontest.New(t)
	// The saga lands: the new environment appears at the ref the CLI asked for.
	fake.Handle("POST "+base, provisions(fake))
	envPollInterval, envPollTimeout = time.Millisecond, 5*time.Second

	out, exec := newCmd(t, fake)
	require.NoError(t, exec("create", "staging", "--from", "production", "--json"))

	req, ok := fake.Find("POST " + base)
	require.True(t, ok)
	require.Equal(t, map[string]any{
		"sourceEnvironmentRef": "app1prod",
		// `<seed><envSlug>`: the source's ref minus its own slug, plus this slug.
		"ref":               "app1sta",
		"name":              "staging",
		"slug":              "staging",
		"kind":              "staging",
		"withData":          false,
		"includeSecretKeys": []any{},
		"testUserSeedCount": float64(0),
	}, req.Body)
	// No git branch is mapped unless asked for: a Git branch is never a selector.
	require.NotContains(t, req.Body, "sourceGitBranch")

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "app1sta", got["ref"])
	require.Equal(t, "staging", got["slug"])
}

// provisions is the fake's "the saga succeeded" handler: it inserts an ACTIVE
// environment at exactly the ref the request asked for, which is what the
// create command polls for.
func provisions(f *selectiontest.Fake) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ref  string `json:"ref"`
			Slug string `json:"slug"`
			Kind string `json:"kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.Environments["proj_1"] = append(f.Environments["proj_1"],
			selectiontest.Env("env_"+body.Slug, "proj_1", body.Ref, body.Slug, body.Kind, false))
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{"workflowId": "create-environment-" + body.Ref})
	}
}

func TestEnvCreate_OptInsAreExplicit(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments"
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	fake := selectiontest.New(t)
	fake.Handle("POST "+base, provisions(fake))
	envPollInterval, envPollTimeout = time.Millisecond, 5*time.Second

	_, exec := newCmd(t, fake)
	require.NoError(t, exec("create", "staging", "--from", "production",
		"--with-data", "--include-secret", "STRIPE_KEY",
		"--git-branch", "staging", "--ref", "app1stg", "--json"))

	req, _ := fake.Find("POST " + base)
	require.Equal(t, true, req.Body["withData"])
	require.Equal(t, []any{"STRIPE_KEY"}, req.Body["includeSecretKeys"])
	require.Equal(t, "staging", req.Body["sourceGitBranch"])
	require.Equal(t, "app1stg", req.Body["ref"])
}

func TestEnvCreate_RequiresFrom(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	fake := selectiontest.New(t)
	_, exec := newCmd(t, fake)
	require.ErrorContains(t, exec("create", "staging"), "--from is required")
}

func TestEnvCreate_UnknownSourceNamesTheRealOnes(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	fake := selectiontest.New(t)
	_, exec := newCmd(t, fake)
	err := exec("create", "staging", "--from", "prod")
	require.ErrorContains(t, err, `no source environment "prod"`)
	require.ErrorContains(t, err, "production")
}

func TestEnvUse_RewritesOnlyTheEnvironmentId(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod",
		RepositoryProvider: selection.ProviderGitHub, IOSAppID: "app_ios",
	})
	fake := withStaging(selectiontest.New(t))

	_, exec := newCmd(t, fake)
	require.NoError(t, exec("use", "staging"))

	cfg, err := selection.Load(dir)
	require.NoError(t, err)
	require.Equal(t, "env_stg", cfg.EnvironmentID)
	require.Equal(t, "proj_1", cfg.ProjectID)
	require.Equal(t, selection.ProviderGitHub, cfg.RepositoryProvider)
	require.Equal(t, "app_ios", cfg.IOSAppID)

	// `use` is LOCAL: it must not mutate anything server-side.
	for _, route := range fake.Routes() {
		require.True(t, route == "GET /api/v2/projects/proj_1/environments", route)
	}
}

// archive/wake with NO argument act on the SELECTED environment.
func TestEnvLifecycle_DefaultsToTheSelectedEnvironment(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments"
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_stg"})

	fake := withStaging(selectiontest.New(t))
	fake.Accepted("POST "+base+"/app1stg/archive", map[string]any{"workflowId": "wf", "runId": "r"})

	_, exec := newCmd(t, fake)
	require.NoError(t, exec("archive", "--json"))

	_, ok := fake.Find("POST " + base + "/app1stg/archive")
	require.True(t, ok, "got %v", fake.Routes())
}

func TestEnvDelete_DeclinedPromptSendsNothing(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	fake := withStaging(selectiontest.New(t))

	rest := fake.REST()
	resolver := fake.Resolver(&bytes.Buffer{})
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(bytes.NewBufferString("n\n"))
	cmd.SetArgs([]string{"delete", "staging"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	_, sent := fake.Find("DELETE /api/v2/projects/proj_1/environments/app1stg")
	require.False(t, sent)
}

// deriveRef is pure: `<seed><envSlug>`. The seed is the source's ref minus its
// own slug suffix.
func TestDeriveRef(t *testing.T) {
	tests := []struct {
		sourceRef, sourceSlug, slug, want string
	}{
		{"app1prod", "production", "staging", "app1sta"},
		{"app1prod", "production", "dev", "app1dev"},
		{"todoappm", "main", "staging", "todoappsta"},
		{"abcd", "production", "staging", "abcdsta"},
		{"aaaaaaaaaaaaaprod", "production", "staging", "aaaaaaaaaaaaasta"},
	}
	for _, tc := range tests {
		got := deriveRef(tc.sourceRef, tc.sourceSlug, tc.slug)
		require.Equal(t, tc.want, got, "%s/%s + %s", tc.sourceRef, tc.sourceSlug, tc.slug)
		require.LessOrEqual(t, len(got), 16, "refs must fit the 16-char k8s/pg-meta/storage ceiling")
	}
}
