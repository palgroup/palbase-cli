package selection_test

import (
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

func TestSave_RequiresProjectAndEnvironmentIDs(t *testing.T) {
	tests := []struct {
		name string
		cfg  *selection.Config
	}{
		{name: "missing project", cfg: &selection.Config{EnvironmentID: "env_1"}},
		{name: "missing environment", cfg: &selection.Config{ProjectID: "proj_1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			err := selection.Save(dir, tc.cfg)
			require.ErrorContains(t, err, "must contain project_id and environment_id")
			require.NoFileExists(t, selection.ConfigPath(dir))
		})
	}
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

func TestLoad_RequiresProjectAndEnvironmentIDs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing project", raw: `{"version":2,"environment_id":"env_1"}`},
		{name: "missing environment", raw: `{"version":2,"project_id":"proj_1"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			selectiontest.WriteRawConfig(t, dir, tc.raw)

			_, err := selection.Load(dir)
			require.ErrorContains(t, err, "must contain project_id and environment_id")
			require.Equal(t, tc.raw, selectiontest.ReadConfig(t, dir))
		})
	}
}

func TestLoad_RejectsUnknownFieldsWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	const raw = `{"version":2,"project_id":"proj_1","environment_id":"env_1","ref":"unused"}`
	selectiontest.WriteRawConfig(t, dir, raw)

	_, err := selection.Load(dir)
	require.ErrorContains(t, err, `unknown field "ref"`)
	require.Equal(t, raw, selectiontest.ReadConfig(t, dir))
}

func TestResolverConfig_RejectsUnsupportedShapeWithoutNetworkOrRewrite(t *testing.T) {
	dir := t.TempDir()
	const raw = `{"ref":"app1prod","default_env":"main"}`
	selectiontest.WriteRawConfig(t, dir, raw)

	fake := selectiontest.New(t)
	r := fake.Resolver()
	r.Dir = dir

	_, err := r.Config()
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase project use")
	require.Empty(t, fake.Routes())
	require.Equal(t, raw, selectiontest.ReadConfig(t, dir))
}

// ── environment picking ─────────────────────────────────────────────────────

func TestResolve_EnvironmentFlagMatchesSlugRefOrName(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	fake.Environments["proj_1"] = append(fake.Environments["proj_1"],
		selectiontest.Env("env_stg", "proj_1", "app1stg", "staging", "staging", false))
	selectiontest.WriteConfig(t, dir, nil)

	for _, want := range []string{"staging", "app1stg"} {
		r := fake.Resolver()
		r.Dir = dir
		r.EnvironmentFlag = want
		sel, err := r.Resolve(context.Background())
		require.NoError(t, err, want)
		require.Equal(t, "app1stg", sel.EnvironmentRef(), want)
	}
}

func TestResolve_UnknownEnvironmentListsTheRealOnes(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	selectiontest.WriteConfig(t, dir, nil)

	r := fake.Resolver()
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

	r := fake.Resolver()
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

	r := fake.Resolver()
	r.Dir = dir
	r.ProjectFlag = "proj_2"
	sel, err := r.Resolve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "proj_2", sel.ProjectID)
	require.Equal(t, "app2prod", sel.EnvironmentRef())
}

// The resolver hits ONE environments listing even when a command asks twice.
func TestResolve_IsCached(t *testing.T) {
	dir := selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	selectiontest.WriteConfig(t, dir, nil)

	r := fake.Resolver()
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
