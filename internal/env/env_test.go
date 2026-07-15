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
	resolver := fake.Resolver()
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
// the SOURCE ENVIRONMENT ref, with no hidden create-time opt-in for production
// rows, secret plaintext, Git mapping, or fake test-user seeding.
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
	// The body carries NO ref: the server allocates it from the Project seed. The
	// client names a SLUG. Sending a ref is the deriveRef bug that minted
	// `e2e140357msta`.
	require.Equal(t, map[string]any{
		"source_environment_ref": "production",
		"name":                   "staging",
	}, req.Body)
	require.NotContains(t, req.Body, "ref", "the client must not compute or send a ref — the server allocates it")
	require.NotContains(t, req.Body, "source_git_branch")
	require.NotContains(t, req.Body, "with_data")
	require.NotContains(t, req.Body, "include_secret_keys")
	require.NotContains(t, req.Body, "test_user_seed_count")

	// The created env is found by the SLUG the client chose; its ref is whatever the
	// server minted (the fake mirrors the canonical staging suffix).
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "app1s", got["ref"])
	require.Equal(t, "staging", got["slug"])
}

// provisions is the fake's "the saga succeeded" handler. It stands in for the SERVER
// side that now ALLOCATES the ref (the client sends no ref): it mints seed "app1" + the
// slug's first 3 chars, stages an ACTIVE environment under it, and returns the workflow
// handle. The create command then polls create-status (COMPLETED by default) and finds
// the new env by the SLUG it chose — never by a ref it computed.
func provisions(f *selectiontest.Fake) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		suffixes := map[string]string{"staging": "s", "dev": "d", "preprod": "pp"}
		suffix := suffixes[body.Name]
		ref := "app1" + suffix
		f.Environments["proj_1"] = append(f.Environments["proj_1"],
			selectiontest.Env("env_"+body.Name, "proj_1", ref, body.Name, body.Name, false))
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{"workflowId": "create-environment-" + ref})
	}
}

func TestEnvCreate_RejectsUnsafeLegacyFlags(t *testing.T) {
	for _, flag := range []string{
		"--with-data", "--include-secret", "--git-branch", "--seed-users",
		"--kind", "--name", "--ref",
	} {
		t.Run(flag, func(t *testing.T) {
			dir := selectiontest.Chdir(t)
			selectiontest.WriteConfig(t, dir, nil)
			fake := selectiontest.New(t)
			_, exec := newCmd(t, fake)
			err := exec("create", "staging", "--from", "production", flag)
			require.ErrorContains(t, err, "unknown flag")
			require.Empty(t, fake.Routes(), "a rejected flag must send no API request")
		})
	}
}

func TestEnvCreate_RequiresFrom(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	fake := selectiontest.New(t)
	_, exec := newCmd(t, fake)
	require.ErrorContains(t, exec("create", "staging"), "--from is required")
}

func TestEnvCreate_RejectsUnsupportedDestinationNames(t *testing.T) {
	for _, name := range []string{"production", "preview", "qa"} {
		t.Run(name, func(t *testing.T) {
			dir := selectiontest.Chdir(t)
			selectiontest.WriteConfig(t, dir, nil)
			fake := selectiontest.New(t)
			_, exec := newCmd(t, fake)
			err := exec("create", name, "--from", "production")
			require.ErrorContains(t, err, "must be staging, dev, or preprod")
			require.Empty(t, fake.Routes())
		})
	}
}

func TestEnvCreate_UnknownSourceIsResolvedByServer(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	fake := selectiontest.New(t)
	fake.Handle("POST /api/v2/projects/proj_1/environments", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteError(w, http.StatusNotFound, "not_found", "Source environment not found")
	})
	_, exec := newCmd(t, fake)
	err := exec("create", "staging", "--from", "prod")
	require.ErrorContains(t, err, "Source environment not found")
	req, ok := fake.Find("POST /api/v2/projects/proj_1/environments")
	require.True(t, ok)
	require.Equal(t, "prod", req.Body["source_environment_ref"])
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
	resolver := fake.Resolver()
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

// A create whose SAGA DIES must surface the saga's OWN reason in SECONDS — not sit for
// the full timeout and then blame the wait. This is the exact bug the ticket names:
// `env create` polled a list where a dead saga and a slow one both look like "not there
// yet", so "source_environment_ref required" never reached the user for five minutes.
//
// MUTATION GATE: make waitForEnvironmentCreate ignore the FAILED status (watch only the
// list) and this test blocks to the 30s timeout and loses the failure text → RED.
func TestEnvCreate_DeadSagaSurfacesTheRealReasonFast(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments"
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	fake := selectiontest.New(t)
	// The POST is accepted (a workflow starts), but the saga DIES: create-status reports
	// FAILED with the reason. The env is NEVER staged — exactly like a rolled-back saga.
	fake.Accepted("POST "+base, map[string]any{"workflowId": "create-environment-proj_1-staging"})
	fake.Handle("GET "+base+"/create-status", func(w http.ResponseWriter, r *http.Request) {
		selectiontest.WriteOK(w, http.StatusOK, map[string]any{
			"workflowId":      r.URL.Query().Get("workflowId"),
			"status":          "FAILED",
			"currentActivity": nil,
			"failure":         "source_environment_ref required",
		})
	})
	// A long timeout: if the wait ignored the FAILED status it would block on this and
	// the test would take 30s instead of returning at once — the fast-fail is the point.
	envPollInterval, envPollTimeout = time.Millisecond, 30*time.Second

	_, exec := newCmd(t, fake)
	err := exec("create", "staging", "--from", "production", "--json")
	require.Error(t, err)
	require.ErrorContains(t, err, "environment create failed")
	require.ErrorContains(t, err, "source_environment_ref required",
		"the saga's OWN reason must reach the user, not a generic 'still provisioning'")
}

// A create that is STILL RUNNING when the wait gives up must say exactly that — never
// dressed up as a failure (the honest-timeout half of the fix).
func TestEnvCreate_StillRunningTimesOutHonestly(t *testing.T) {
	const base = "/api/v2/projects/proj_1/environments"
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	fake := selectiontest.New(t)
	fake.Accepted("POST "+base, map[string]any{"workflowId": "create-environment-proj_1-staging"})
	fake.Handle("GET "+base+"/create-status", func(w http.ResponseWriter, r *http.Request) {
		selectiontest.WriteOK(w, http.StatusOK, map[string]any{
			"workflowId":      r.URL.Query().Get("workflowId"),
			"status":          "RUNNING",
			"currentActivity": "ProvisionEnv",
			"failure":         nil,
		})
	})
	envPollInterval, envPollTimeout = time.Millisecond, 20*time.Millisecond

	_, exec := newCmd(t, fake)
	err := exec("create", "staging", "--from", "production", "--json")
	require.Error(t, err)
	require.ErrorContains(t, err, "STILL provisioning",
		"a running saga must be reported as running, never as a failure")
	require.NotContains(t, err.Error(), "failed",
		"'still provisioning' must not be worded as a failure")
}
