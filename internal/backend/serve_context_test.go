package backend

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

func TestServe_RequiresASelectedEnvironmentBeforeFilesystemWork(t *testing.T) {
	selectiontest.Chdir(t)
	fake := selectiontest.New(t)
	resolver := fake.Resolver()
	cmd := newDevCmd(Resolvers{
		Selection: func() *selection.Resolver { return resolver },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true

	err := cmd.Execute()
	require.ErrorContains(t, err, "no project selected")
	require.NotContains(t, err.Error(), "controllers/", "selection must be checked before local files or dependencies")
	require.Empty(t, fake.Routes())
}
