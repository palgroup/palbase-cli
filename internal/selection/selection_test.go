package selection_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// ── config v2 shape ─────────────────────────────────────────────────────────

// The config file is a CONTRACT (spec §4 / UAT CLI-003). Golden-compare the
// exact bytes: a stray key (organization_id, tier, ref, default_env, a branch
// selector, a credential) is a spec violation, not a formatting nit.
func TestSaveConfig_GoldenV2Bytes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, selection.Save(dir, &selection.Config{
		ProjectID:          "proj_01j0",
		EnvironmentID:      "env_01j1",
		RepositoryProvider: selection.ProviderGitHub,
	}))

	const want = `{
  "version": 2,
  "project_id": "proj_01j0",
  "environment_id": "env_01j1",
  "repository_provider": "github"
}
`
	require.Equal(t, want, selectiontest.ReadConfig(t, dir))
}

// The forbidden keys, asserted as ABSENT from what we write. This is the guard
// that catches a well-meaning "just persist the ref too" regression.
func TestSaveConfig_CarriesNoForbiddenKey(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, selection.Save(dir, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_1",
		RepositoryProvider: selection.ProviderPalbase,
		IOSAppID:           "app_ios",
	}))
	raw := selectiontest.ReadConfig(t, dir)
	for _, forbidden := range []string{
		`"organization_id"`, `"organizationId"`, `"tier"`, `"ref"`,
		`"default_env"`, `"branch"`, `"token"`, `"api_key"`,
	} {
		require.NotContains(t, raw, forbidden, "config v2 must not carry %s", forbidden)
	}
}

func TestSave_StampsVersionEvenWhenCallerForgot(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, selection.Save(dir, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_1"}))
	cfg, err := selection.Load(dir)
	require.NoError(t, err)
	require.Equal(t, 2, cfg.Version)
}

func TestLoad_MissingFileIsNotSelected(t *testing.T) {
	_, err := selection.Load(t.TempDir())
	require.ErrorAs(t, err, &selection.ErrNotSelected{})
	require.Contains(t, err.Error(), "palbase project use")
}

func TestLoad_UnknownVersionIsRefused(t *testing.T) {
	dir := t.TempDir()
	selectiontest.WriteRawConfig(t, dir, `{"version":3,"project_id":"proj_1"}`)
	_, err := selection.Load(dir)
	require.ErrorContains(t, err, "version 3")
}

// ── v1 → v2 migration ───────────────────────────────────────────────────────

// The v1 file holds a BARE REF. Migration resolves it to {project_id,
// environment_id} over the API and rewrites the file. It must not 404 silently:
// the whole point is that a user with an old checkout keeps working.
func TestMigrate_V1ConfigInPlace_GoldenBeforeAfter(t *testing.T) {
	dir := t.TempDir()

	const before = `{
  "ref": "app1prod",
  "default_env": "main",
  "mode": "github",
  "github_repo": "pal-salih/todoapp",
  "ios_app_id": "app_ios1"
}
`
	selectiontest.WriteRawConfig(t, dir, before)

	fake := selectiontest.New(t)
	var warn bytes.Buffer
	r := fake.Resolver(&warn)
	r.Dir = dir

	cfg, err := r.Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "proj_1", cfg.ProjectID)
	require.Equal(t, "env_prod", cfg.EnvironmentID)
	require.Equal(t, selection.ProviderGitHub, cfg.RepositoryProvider)
	require.Equal(t, "app_ios1", cfg.IOSAppID, "the platform app slot survives the migration")

	// The file on disk is REWRITTEN — golden.
	const after = `{
  "version": 2,
  "project_id": "proj_1",
  "environment_id": "env_prod",
  "repository_provider": "github",
  "ios_app_id": "app_ios1"
}
`
	require.Equal(t, after, selectiontest.ReadConfig(t, dir))

	// And it SAYS SO, on stderr — a silent rewrite of the user's config is not on.
	require.Contains(t, warn.String(), "migrated")
	require.Contains(t, warn.String(), "app1prod")
	require.Contains(t, warn.String(), "proj_1")
}

func TestMigrate_PlatformModeBecomesPalbaseProvider(t *testing.T) {
	dir := t.TempDir()
	selectiontest.WriteRawConfig(t, dir, `{"ref":"app1prod","default_env":"main","mode":"platform"}`)

	fake := selectiontest.New(t)
	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir

	cfg, err := r.Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, selection.ProviderPalbase, cfg.RepositoryProvider)
}

// An unresolvable ref is a HARD, NAMED error — never a silent 404 later.
func TestMigrate_UnknownRefFailsLoudlyNamingTheRef(t *testing.T) {
	dir := t.TempDir()
	selectiontest.WriteRawConfig(t, dir, `{"ref":"ghostref","default_env":"main"}`)

	fake := selectiontest.New(t)
	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir

	_, err := r.Config(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "ghostref")
	require.Contains(t, err.Error(), "palbase project use")
	// The broken v1 file is LEFT ALONE: we do not destroy state we could not read.
	require.Contains(t, selectiontest.ReadConfig(t, dir), `"ghostref"`)
}

// ── environment picking ─────────────────────────────────────────────────────

func TestResolve_DefaultsToProduction(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	// A config with a project but no environment: production is the answer.
	selectiontest.WriteConfig(t, dir, &selection.Config{ProjectID: "proj_1"})

	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir
	sel, err := r.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "app1prod", sel.Ref())
}

func TestResolve_EnvironmentFlagMatchesSlugRefOrName(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Environments["proj_1"] = append(fake.Environments["proj_1"],
		selectiontest.Env("env_stg", "proj_1", "app1stg", "staging", "staging", false))
	selectiontest.WriteConfig(t, dir, nil)

	for _, want := range []string{"staging", "app1stg"} {
		r := fake.Resolver(&bytes.Buffer{})
		r.Dir = dir
		r.EnvironmentFlag = want
		sel, err := r.Resolve(context.Background())
		require.NoError(t, err, want)
		require.Equal(t, "app1stg", sel.Ref(), want)
	}
}

func TestResolve_UnknownEnvironmentListsTheRealOnes(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	selectiontest.WriteConfig(t, dir, nil)

	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir
	r.EnvironmentFlag = "nope"
	_, err := r.Resolve(context.Background())
	require.ErrorContains(t, err, `no environment "nope"`)
	require.ErrorContains(t, err, "production")
}

// A config pointing at an environment that has since been deleted must say so —
// not fall back to production and silently act on the wrong runtime.
func TestResolve_DeletedEnvironmentIsNamed_NotSilentlyReplaced(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_gone"})

	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir
	_, err := r.Resolve(context.Background())
	require.ErrorContains(t, err, "env_gone")
	require.ErrorContains(t, err, "palbase env use")
}

func TestResolve_ProjectFlagOverridesConfig(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Projects = append(fake.Projects, selectiontest.Project{ID: "proj_2", Name: "other", Mode: "platform"})
	fake.Environments["proj_2"] = []selection.Environment{
		selectiontest.Env("env_p2", "proj_2", "app2prod", "production", "production", true),
	}
	selectiontest.WriteConfig(t, dir, nil)

	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir
	r.ProjectFlag = "proj_2"
	sel, err := r.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "proj_2", sel.ProjectID)
	require.Equal(t, "app2prod", sel.Ref())
}

// The resolver hits ONE environments listing even when a command asks twice.
func TestResolve_IsCached(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	selectiontest.WriteConfig(t, dir, nil)

	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir
	_, err := r.Resolve(context.Background())
	require.NoError(t, err)
	_, err = r.Resolve(context.Background())
	require.NoError(t, err)

	n := 0
	for _, route := range fake.Routes() {
		if route == "GET /api/v2/projects/proj_1/environments" {
			n++
		}
	}
	require.Equal(t, 1, n)
}

// ── gitignore ───────────────────────────────────────────────────────────────

func TestEnsureGitignored_NarrowsADirectoryWideRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("node_modules\n.palbase\n"), 0o644))
	require.NoError(t, selection.EnsureGitignored(path))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Equal(t, []string{"node_modules", ".palbase/config.json"}, lines,
		"generated artifacts under .palbase must stay trackable")
}

func TestErrNotSelected_IsMatchable(t *testing.T) {
	var target selection.ErrNotSelected
	require.True(t, errors.As(selection.ErrNotSelected{}, &target))
}

// A v1 `ref` is the OLD env-CONTAINER ref, NOT an environment ref: migration 129
// folded container+branch into one environment whose ref is the old ENDPOINT ref,
// `<ref><branchSlug>`. This is the real todoapp config, and it is what every
// existing user has on disk — before the fix it resolved to nothing and `palbase
// status` died with "no environment with ref "todoappm8p6z" is visible to you".
func TestMigrate_V1RefIsTheBaseOfTheEnvironmentRef(t *testing.T) {
	dir := t.TempDir()
	selectiontest.WriteRawConfig(t, dir, `{"ref":"todoappm8p6z","default_env":"main"}`)

	fake := selectiontest.New(t)
	fake.Projects = []selectiontest.Project{{ID: "proj_todo", Name: "todoapp", Mode: "platform"}}
	fake.Environments = map[string][]selection.Environment{
		"proj_todo": {selectiontest.Env("env_todo", "proj_todo", "todoappm8p6zm", "production", "production", true)},
	}
	var warn bytes.Buffer
	r := fake.Resolver(&warn)
	r.Dir = dir

	cfg, err := r.Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "proj_todo", cfg.ProjectID)
	require.Equal(t, "env_todo", cfg.EnvironmentID)
	require.Contains(t, warn.String(), "migrated")
}

// Several environments share the base ref (the old branches). `default_env` says
// which one this directory meant: v1 `main` is the branch the cutover folded into
// the PRODUCTION environment.
func TestMigrate_AmbiguousBaseRefIsBrokenByDefaultEnv(t *testing.T) {
	tests := []struct {
		name       string
		defaultEnv string
		wantEnv    string
	}{
		{name: "main is the production environment", defaultEnv: "main", wantEnv: "env_prod"},
		{name: "a named branch matches its environment", defaultEnv: "staging", wantEnv: "env_stg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			selectiontest.WriteRawConfig(t, dir,
				`{"ref":"abc1","default_env":"`+tc.defaultEnv+`"}`)

			fake := selectiontest.New(t)
			fake.Projects = []selectiontest.Project{{ID: "proj_a", Name: "a", Mode: "platform"}}
			fake.Environments = map[string][]selection.Environment{
				"proj_a": {
					selectiontest.Env("env_prod", "proj_a", "abc1m", "production", "production", true),
					selectiontest.Env("env_stg", "proj_a", "abc1s", "staging", "staging", false),
				},
			}
			r := fake.Resolver(&bytes.Buffer{})
			r.Dir = dir

			cfg, err := r.Config(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.wantEnv, cfg.EnvironmentID)
		})
	}
}

// An UNRESOLVABLE tie is an error that LISTS the candidates. Guessing would point
// `palbase push` at the wrong environment — silently deploying to staging (or
// production) is worse than refusing.
func TestMigrate_UnresolvableAmbiguityErrorsWithCandidates(t *testing.T) {
	dir := t.TempDir()
	selectiontest.WriteRawConfig(t, dir, `{"ref":"abc1","default_env":"feature-x"}`)

	fake := selectiontest.New(t)
	fake.Projects = []selectiontest.Project{{ID: "proj_a", Name: "a", Mode: "platform"}}
	fake.Environments = map[string][]selection.Environment{
		"proj_a": {
			selectiontest.Env("env_prod", "proj_a", "abc1m", "production", "production", true),
			selectiontest.Env("env_stg", "proj_a", "abc1s", "staging", "staging", false),
		},
	}
	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir

	_, err := r.Config(context.Background())
	require.ErrorContains(t, err, "matches 2 environments")
	require.ErrorContains(t, err, "abc1m")
	require.ErrorContains(t, err, "abc1s")
	require.ErrorContains(t, err, "palbase project use")
}

// The base ref must match on a SLUG BOUNDARY, never on a bare prefix: with ref
// "app1", the NEIGHBOURING project "app12"'s production environment is "app12m".
// A prefix scan would migrate this directory onto a stranger's project and the next
// `palbase push` would deploy there.
func TestMigrate_NeighbouringProjectRefIsNotAMatch(t *testing.T) {
	dir := t.TempDir()
	selectiontest.WriteRawConfig(t, dir, `{"ref":"app1","default_env":"main"}`)

	fake := selectiontest.New(t)
	fake.Projects = []selectiontest.Project{{ID: "proj_neighbour", Name: "app12", Mode: "platform"}}
	fake.Environments = map[string][]selection.Environment{
		"proj_neighbour": {selectiontest.Env("env_n", "proj_neighbour", "app12m", "production", "production", true)},
	}
	r := fake.Resolver(&bytes.Buffer{})
	r.Dir = dir

	_, err := r.Config(context.Background())
	require.ErrorContains(t, err, `no environment with ref "app1"`)
}
