package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// `apikey` AND `members` ANNOUNCE THE ENVIRONMENT (FR-085). Their target is the
// resolver's answer, and its banner was `Target.Describe()` — the project's name
// and nothing else — while `--env staging` made them act on staging.
func TestTheLinkedTargetNamesTheEnvironmentItResolved(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ENV", "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "palbase"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "palbase", "project.json"),
		[]byte(`{"project":"prd_a","name":"todoapp"}`+"\n"), 0o644))
	prevEnvs, prevHost, prevFlag := backend.EnvironmentsOf, backend.TenantHost, backend.SelectedEnvFlag
	t.Cleanup(func() {
		backend.EnvironmentsOf, backend.TenantHost, backend.SelectedEnvFlag = prevEnvs, prevHost, prevFlag
	})
	backend.EnvironmentsOf = func(context.Context, string) ([]backend.Environment, error) {
		return []backend.Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		}, nil
	}
	backend.TenantHost = "palbase.test"
	backend.SelectedEnvFlag = "staging"

	p, err := linkedTarget()
	require.NoError(t, err)
	require.Equal(t, "todoapp/staging", p.Describe())
}
