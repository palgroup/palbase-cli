package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

func newCmd(t *testing.T, fake *selectiontest.Fake) (*bytes.Buffer, *bytes.Buffer, func(...string) error) {
	t.Helper()
	rest := fake.REST()
	resolver := fake.Resolver()
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return &out, &errOut, func(args ...string) error {
		cmd.SetArgs(args)
		return cmd.Execute()
	}
}

func TestProjectCmd_CanonicalSurface(t *testing.T) {
	cmd := Cmd(Resolvers{})
	var got []string
	for _, c := range cmd.Commands() {
		got = append(got, c.Name())
	}
	sort.Strings(got)
	// spec §7.3: create|list|use|status|delete. Nothing else.
	require.Equal(t, []string{"create", "delete", "list", "status", "use"}, got)
}

func TestProject_HitsTheV2Paths(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		route string
		reply func(f *selectiontest.Fake)
	}{
		{
			name: "list", args: []string{"list", "--json"},
			route: "GET /api/v2/projects",
		},
		{
			name: "status", args: []string{"status", "--json"},
			route: "GET /api/v2/projects/proj_1",
		},
		{
			name: "use", args: []string{"use", "proj_1"},
			route: "GET /api/v2/projects/proj_1/environments",
		},
		{
			name: "delete", args: []string{"delete", "proj_1", "--yes"},
			route: "DELETE /api/v2/projects/proj_1",
			reply: func(f *selectiontest.Fake) {
				f.Handle("DELETE /api/v2/projects/proj_1", func(w http.ResponseWriter, _ *http.Request) {
					selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{"workflowId": "wf_1", "runId": "run_1"})
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
			_, _, exec := newCmd(t, fake)
			require.NoError(t, exec(tc.args...))
			_, ok := fake.Find(tc.route)
			require.True(t, ok, "expected %s, got %v", tc.route, fake.Routes())
			for _, route := range fake.Routes() {
				require.NotContains(t, route, "/api/v1/")
			}
		})
	}
}

// POST /api/v2/projects takes NO organizationId — the server uses the caller's
// default Organization (spec §7.3). Sending one would be a 400 on the STRICT
// body, and it would put Organization back into the CLI, which is the whole
// thing the cutover removed.
func TestProjectCreate_SendsNoOrganizationId(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Projects = nil
	fake.Environments = map[string][]selection.Environment{}
	fake.Handle("POST /api/v2/projects", func(w http.ResponseWriter, _ *http.Request) {
		// The saga "completes": the project + its production environment appear.
		fake.Projects = []selectiontest.Project{{ID: "proj_new", Name: "newapp", Mode: "platform"}}
		fake.Environments["proj_new"] = []selection.Environment{
			selectiontest.Env("env_new", "proj_new", "newappprod", "production", "production", true),
		}
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{
			"projectId": "proj_new", "workflowId": "create-project-newapp-1", "runId": "r",
		})
	})

	projectPollInterval = time.Millisecond
	gitRun = noopGit
	out, _, exec := newCmd(t, fake)
	require.NoError(t, exec("create", "newapp", "--name", "newapp", "--json"))

	req, ok := fake.Find("POST /api/v2/projects")
	require.True(t, ok)
	require.Equal(t, map[string]any{"ref": "newapp", "name": "newapp"}, req.Body)
	require.NotContains(t, req.Body, "organizationId")
	require.NotContains(t, req.Body, "tier")

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "proj_new", got["project_id"])
	require.Equal(t, "env_new", got["environment_id"])
	require.Equal(t, "newappprod", got["environment_ref"])
	require.Equal(t, "palbase", got["repository_provider"])
}

// create SELECTS what it built: config v2 lands in the directory (UAT CLI-001).
func TestProjectCreate_WritesConfigV2(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Handle("POST /api/v2/projects", func(w http.ResponseWriter, _ *http.Request) {
		// The 202 names the Project it is creating; the fake's seeded proj_1 IS it.
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{
			"projectId": "proj_1", "workflowId": "wf", "runId": "r",
		})
	})
	projectPollInterval = time.Millisecond
	gitRun = noopGit

	_, _, exec := newCmd(t, fake)
	require.NoError(t, exec("create", "app1", "--name", "todoapp",
		"--github-account", "personal", "--repo", "todoapp"))

	require.Equal(t, `{
  "version": 2,
  "project_id": "proj_1",
  "environment_id": "env_prod",
  "repository_provider": "github"
}
`, selectiontest.ReadConfig(t, dir))
}

func TestProjectCreate_RejectsHalfGitHubMode(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	_, _, exec := newCmd(t, fake)
	require.ErrorContains(t, exec("create", "app1", "--name", "x", "--repo", "r"), "both --github-account and --repo")
	require.Empty(t, fake.Routes(), "a half-formed GitHub config must never reach the server")
}

// DELETE takes `confirm_name` — the project's NAME, not its id. The CLI reads
// the name itself so a script only has to know the id.
func TestProjectDelete_SendsConfirmName(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Handle("DELETE /api/v2/projects/proj_1", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{"workflowId": "wf", "runId": "r"})
	})
	_, _, exec := newCmd(t, fake)
	require.NoError(t, exec("delete", "proj_1", "--yes"))

	req, ok := fake.Find("DELETE /api/v2/projects/proj_1")
	require.True(t, ok)
	require.Equal(t, map[string]any{"confirm_name": "todoapp"}, req.Body)
}

// Deleting the SELECTED project drops the selection: leaving it behind would
// make every later command 404 against a project that no longer exists.
func TestProjectDelete_DropsTheSelection(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)
	fake := selectiontest.New(t)
	fake.Handle("DELETE /api/v2/projects/proj_1", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{"workflowId": "wf", "runId": "r"})
	})
	_, _, exec := newCmd(t, fake)
	require.NoError(t, exec("delete", "proj_1", "--yes"))

	_, err := selection.Load(dir)
	require.ErrorAs(t, err, &selection.ErrNotSelected{})
}

// The interactive prompt compares against the NAME. A mismatch cancels, and
// nothing is sent.
func TestProjectDelete_PromptMismatchCancels(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)

	rest := fake.REST()
	resolver := fake.Resolver()
	cmd := Cmd(Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(bytes.NewBufferString("wrongname\n"))
	cmd.SetArgs([]string{"delete", "proj_1"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true

	require.ErrorContains(t, cmd.Execute(), "confirmation mismatch")
	_, sent := fake.Find("DELETE /api/v2/projects/proj_1")
	require.False(t, sent)
}

// `use` selects the project AND its production environment, and records the
// repository provider the server reports.
func TestProjectUse_SelectsProductionAndProvider(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Projects[0].Mode = "github"
	fake.Projects[0].GithubRepo = "pal-salih/todoapp"

	_, _, exec := newCmd(t, fake)
	require.NoError(t, exec("use", "proj_1"))

	cfg, err := selection.Load(dir)
	require.NoError(t, err)
	require.Equal(t, "proj_1", cfg.ProjectID)
	require.Equal(t, "env_prod", cfg.EnvironmentID)
	require.Equal(t, selection.ProviderGitHub, cfg.RepositoryProvider)
}

// Switching to a DIFFERENT project drops the old project's app registrations:
// an app id belongs to one product, and carrying it over would point `ios use`
// at an app in someone else's project.
func TestProjectUse_ClearsAppSlotsOnProjectSwitch(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_old", EnvironmentID: "env_old", IOSAppID: "app_old",
	})
	fake := selectiontest.New(t)

	_, _, exec := newCmd(t, fake)
	require.NoError(t, exec("use", "proj_1"))

	cfg, err := selection.Load(dir)
	require.NoError(t, err)
	require.Empty(t, cfg.IOSAppID)
}

func TestProjectUse_SwitchesProjectAndResetsToItsProductionEnvironment(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_stg",
		RepositoryProvider: selection.ProviderPalbase,
	})
	fake := selectiontest.New(t)
	fake.Projects = append(fake.Projects, selectiontest.Project{
		ID: "proj_2", Name: "other", Mode: "github",
	})
	fake.Environments["proj_2"] = []selection.Environment{
		selectiontest.Env("env_2_stg", "proj_2", "app2stg", "staging", "staging", false),
		selectiontest.Env("env_2_prod", "proj_2", "app2prod", "production", "production", true),
	}

	_, _, exec := newCmd(t, fake)
	require.NoError(t, exec("use", "proj_2"))

	cfg, err := selection.Load(dir)
	require.NoError(t, err)
	require.Equal(t, "proj_2", cfg.ProjectID, "the SaaS Project link must be preserved exactly")
	require.Equal(t, "env_2_prod", cfg.EnvironmentID, "a Project switch must select its production Environment")
	require.Equal(t, selection.ProviderGitHub, cfg.RepositoryProvider)
}

// noopGit stands in for a directory that is already inside a git repository, so
// a create test never forks a real git.
func noopGit(string, ...string) (string, error) { return "/repo", nil }

// ── the local Git invariant (spec §4) ───────────────────────────────────────

// Inside a monorepo the ancestor Git root is REUSED. A nested .git would take
// the backend subtree out of the monorepo's history — the deploy webhook then
// filters against a root the developer never commits to.
func TestEnsureGitRepo_ReusesTheAncestorRoot(t *testing.T) {
	var calls [][]string
	git := func(dir string, args ...string) (string, error) {
		calls = append(calls, args)
		if args[0] == "rev-parse" {
			return "/repo/monorepo", nil
		}
		t.Fatalf("must not run git %v inside an existing repository", args)
		return "", nil
	}
	var w bytes.Buffer
	root, err := ensureGitRepo(git, "/repo/monorepo/services/backend", &w)
	require.NoError(t, err)
	require.Equal(t, "/repo/monorepo", root)
	require.Len(t, calls, 1, "an existing repository must be reused, not re-initialized")
	require.Contains(t, w.String(), "no nested .git created")
}

// A standalone directory gets `git init -b main` — `main` because that is the
// branch production maps to; a repo whose first branch is `master` maps to
// nothing.
func TestEnsureGitRepo_InitializesMainWhenStandalone(t *testing.T) {
	var calls [][]string
	git := func(_ string, args ...string) (string, error) {
		calls = append(calls, args)
		if args[0] == "rev-parse" {
			return "", errors.New("not a git repository")
		}
		return "", nil
	}
	var w bytes.Buffer
	root, err := ensureGitRepo(git, "/tmp/app", &w)
	require.NoError(t, err)
	require.Equal(t, "/tmp/app", root)
	require.Equal(t, [][]string{
		{"rev-parse", "--show-toplevel"},
		{"init", "-b", "main"},
	}, calls)
	require.Contains(t, w.String(), "branch main")
}

// git being unavailable must not lose the project: it is already provisioned
// server-side, so we warn and finish writing the selection.
func TestEnsureGitRepo_MissingGitWarnsButDoesNotFail(t *testing.T) {
	git := func(_ string, args ...string) (string, error) {
		return "", errors.New(`exec: "git": executable file not found in $PATH`)
	}
	var w bytes.Buffer
	root, err := ensureGitRepo(git, "/tmp/app", &w)
	require.NoError(t, err)
	require.Empty(t, root)
	require.Contains(t, w.String(), "warning")
	require.Contains(t, w.String(), "git init -b main")
}

// ── the poll is O(1), not O(projects) ───────────────────────────────────────

// THE regression this fix exists to prevent.
//
// `create` used to be handed a 202 with no project id, so it SEARCHED for what it
// had just built: list every Project on the account, then fetch the environments of
// EACH, looking for a ref that starts with the seed. On a real account (77 projects)
// that is 78 requests per poll tick, and slow enough that the access token expired
// mid-wait — `create` died with a 401 while its project provisioned fine.
//
// The 202 now NAMES the project and the wait polls the SAGA — ONE request per tick,
// whatever the account holds. The counts below are the lock: they must not move when
// the account grows.
func TestProjectCreate_PollsOnlyTheReturnedProject_OneRequestPerTick(t *testing.T) {
	tests := []struct {
		name          string
		otherProjects int
		ticksUntilWon int
	}{
		{name: "empty account, ready on the first tick", otherProjects: 0, ticksUntilWon: 1},
		{name: "3 other projects, ready on the third tick", otherProjects: 3, ticksUntilWon: 3},
		{name: "77 other projects (the live account)", otherProjects: 77, ticksUntilWon: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selectiontest.Chdir(t)
			fake := selectiontest.New(t)
			fake.Projects = nil
			fake.Environments = map[string][]selection.Environment{}
			for i := range tc.otherProjects {
				id := fmt.Sprintf("proj_other%d", i)
				fake.Projects = append(fake.Projects, selectiontest.Project{ID: id, Name: id, Mode: "platform"})
				fake.Environments[id] = []selection.Environment{
					selectiontest.Env("env_"+id, id, fmt.Sprintf("other%dm", i), "production", "production", true),
				}
			}

			fake.Handle("POST /api/v2/projects", func(w http.ResponseWriter, _ *http.Request) {
				selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{
					"projectId":      "proj_new",
					"workflowId":     "create-project-newapp-1",
					"runId":          "r",
					"organizationId": "org_1",
				})
			})
			// The saga runs, then completes on the tick'th poll.
			polls := 0
			fake.Handle("GET /api/v2/projects/create-status", func(w http.ResponseWriter, _ *http.Request) {
				polls++
				status := "RUNNING"
				if polls >= tc.ticksUntilWon {
					status = "COMPLETED"
				}
				selectiontest.WriteOK(w, http.StatusOK, map[string]any{
					"workflowId": "create-project-newapp-1", "status": status,
					"currentActivity": "ProvisionDatabase", "failure": nil,
				})
			})
			fake.Handle("GET /api/v2/projects/proj_new/environments", func(w http.ResponseWriter, _ *http.Request) {
				selectiontest.WriteOK(w, http.StatusOK, []selection.Environment{
					selectiontest.Env("env_new", "proj_new", "newappm", "production", "production", true),
				})
			})

			projectPollInterval = time.Millisecond
			gitRun = noopGit
			out, _, exec := newCmd(t, fake)
			require.NoError(t, exec("create", "newapp", "--name", "newapp", "--json"))

			var got map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			require.Equal(t, "proj_new", got["project_id"], "the id comes from the 202, not from a search")
			require.Equal(t, "env_new", got["environment_id"])

			// The COUNT is the guard: POST + one status call per tick + ONE environments
			// read once the saga is done. Nothing here scales with the account.
			counts := map[string]int{}
			for _, route := range fake.Routes() {
				counts[route]++
			}
			require.Equal(t, 1, counts["POST /api/v2/projects"])
			require.Equal(t, tc.ticksUntilWon, counts["GET /api/v2/projects/create-status"],
				"exactly ONE status call per poll tick")
			require.Equal(t, 1, counts["GET /api/v2/projects/proj_new/environments"],
				"the environments read happens ONCE, after the saga completes")
			require.Len(t, fake.Routes(), 2+tc.ticksUntilWon,
				"the wait must make NO other request — its cost cannot depend on how many projects the account holds")

			// The status poll is bound to THIS create: the ref seed and the workflow id
			// from the 202, nothing derived from a search.
			req, ok := fake.Find("GET /api/v2/projects/create-status")
			require.True(t, ok)
			require.Contains(t, req.Query, "ref=newapp")
			require.Contains(t, req.Query, "workflowId=create-project-newapp-1")

			// And it NEVER lists the account's projects: that listing (and the
			// per-project environments fan-out behind it) IS the O(projects) bug.
			require.NotContains(t, fake.Routes(), "GET /api/v2/projects")
			for _, route := range fake.Routes() {
				require.NotContains(t, route, "proj_other", "no other project may be touched")
			}
		})
	}
}

// THE bug this fix exists for.
//
// The create saga can die NON-RETRYABLY in its first second — AllocateRef refusing a
// quota ("project quota exceeded: 77/20") — and it SAYS so. `create` used to watch the
// environments list, where a dead saga and a slow one look identical (an empty list),
// so it sat there for the full 10-minute timeout and then blamed the wait. It must stop
// on the FIRST failing tick and print the saga's own reason.
func TestProjectCreate_StopsAtOnceAndPrintsWhyTheSagaDied(t *testing.T) {
	for _, state := range []string{"FAILED", "TERMINATED", "TIMED_OUT", "CANCELED"} {
		t.Run(state, func(t *testing.T) {
			selectiontest.Chdir(t)
			fake := selectiontest.New(t)
			fake.Handle("POST /api/v2/projects", func(w http.ResponseWriter, _ *http.Request) {
				selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{
					"projectId": "proj_new", "workflowId": "create-project-newapp-1", "runId": "r",
				})
			})
			fake.Handle("GET /api/v2/projects/create-status", func(w http.ResponseWriter, _ *http.Request) {
				selectiontest.WriteOK(w, http.StatusOK, map[string]any{
					"workflowId": "create-project-newapp-1", "status": state,
					"currentActivity": nil, "failure": "project quota exceeded: 77/20",
				})
			})

			projectPollInterval = time.Millisecond
			projectPollTimeout = 10 * time.Minute // the real one: the fix is NOT a shorter wait
			gitRun = noopGit
			_, _, exec := newCmd(t, fake)

			start := time.Now()
			err := exec("create", "newapp", "--name", "newapp")

			require.ErrorContains(t, err, "project quota exceeded: 77/20")
			require.Less(t, time.Since(start), 5*time.Second, "it must not wait out the timeout on a DEAD saga")
			// One status call: it stopped on the first failing tick, and it never
			// went looking for environments of a project that was never created.
			require.Equal(t, []string{
				"POST /api/v2/projects",
				"GET /api/v2/projects/create-status",
			}, fake.Routes())
		})
	}
}

// "Still provisioning" and "dead" are DIFFERENT statements. A wait that runs out
// while the saga is genuinely still running must say so — and must not claim a
// failure that did not happen.
func TestProjectCreate_TimeoutSaysStillProvisioning_NotFailed(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Handle("POST /api/v2/projects", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{
			"projectId": "proj_new", "workflowId": "create-project-newapp-1", "runId": "r",
		})
	})
	fake.Handle("GET /api/v2/projects/create-status", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusOK, map[string]any{
			"workflowId": "create-project-newapp-1", "status": "RUNNING",
			"currentActivity": "ProvisionDatabase", "failure": nil,
		})
	})

	projectPollInterval = time.Millisecond
	projectPollTimeout = 5 * time.Millisecond
	t.Cleanup(func() { projectPollTimeout = 10 * time.Minute })
	gitRun = noopGit
	_, _, exec := newCmd(t, fake)

	err := exec("create", "newapp", "--name", "newapp")

	require.ErrorContains(t, err, "STILL provisioning")
	require.ErrorContains(t, err, "ProvisionDatabase") // where it actually is
	require.ErrorContains(t, err, "proj_new")          // how to pick it up again
	require.NotContains(t, err.Error(), "failed", "a running saga must never be reported as a failure")
}

// The saga's last step is MarkActive, so a COMPLETED workflow means the rows are
// there — but a read that lands before they are visible must be retried, not turned
// into a failed create.
func TestProjectCreate_WaitsThroughTheNotFoundWindow(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Handle("POST /api/v2/projects", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{
			"projectId": "proj_new", "workflowId": "create-project-newapp-1", "runId": "r",
		})
	})
	fake.Handle("GET /api/v2/projects/create-status", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusOK, map[string]any{
			"workflowId": "create-project-newapp-1", "status": "COMPLETED",
			"currentActivity": nil, "failure": nil,
		})
	})
	polls := 0
	fake.Handle("GET /api/v2/projects/proj_new/environments", func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls < 3 {
			selectiontest.WriteError(w, http.StatusNotFound, "not_found", "Project not found")
			return
		}
		selectiontest.WriteOK(w, http.StatusOK, []selection.Environment{
			selectiontest.Env("env_new", "proj_new", "newappm", "production", "production", true),
		})
	})

	projectPollInterval = time.Millisecond
	gitRun = noopGit
	out, _, exec := newCmd(t, fake)
	require.NoError(t, exec("create", "newapp", "--name", "newapp", "--json"))

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "env_new", got["environment_id"])
	require.Equal(t, 3, polls)
}

// --async returns the handle without waiting — and now it can NAME what it started.
func TestProjectCreate_AsyncPrintsTheProjectId(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Handle("POST /api/v2/projects", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{
			"projectId": "proj_new", "workflowId": "wf_1", "runId": "r",
		})
	})

	out, _, exec := newCmd(t, fake)
	require.NoError(t, exec("create", "newapp", "--name", "newapp", "--async"))

	require.Contains(t, out.String(), "proj_new")
	require.Contains(t, out.String(), "wf_1")
	require.Equal(t, []string{"POST /api/v2/projects"}, fake.Routes(), "--async must not poll at all")
}

// A 202 with no project id is a broken contract, not a cue to go hunting for the
// project: the search is what this command stopped doing.
func TestProjectCreate_FailsLoudlyWhenTheServerReturnsNoProjectId(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Handle("POST /api/v2/projects", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusAccepted, map[string]any{"workflowId": "wf", "runId": "r"})
	})

	gitRun = noopGit
	_, _, exec := newCmd(t, fake)
	require.ErrorContains(t, exec("create", "newapp", "--name", "newapp"), "returned no project id")
	require.Equal(t, []string{"POST /api/v2/projects"}, fake.Routes())
}
