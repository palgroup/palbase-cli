package backend

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE BANNER NAMES THE ENVIRONMENT (FR-085). The environment is not part of a
// target — only the resolver knows it — so a verb that announced
// `resolved.Acting().Describe()` printed `▸ todoapp` while it acted on staging.
func stagingSelected(t *testing.T) {
	t.Helper()
	inScratchCheckout(t)
	staging := stackServing(t, "pb_project_cSTAGINGNOW", nil)
	routeEnvironments(t, map[string]string{"stagref000": staging.URL})
	twoEnvironmentsOf(t)
	prev := SelectedEnvFlag
	SelectedEnvFlag = "staging"
	t.Cleanup(func() { SelectedEnvFlag = prev })
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
}

func TestStatusAnnouncesTheEnvironmentItResolved(t *testing.T) {
	stagingSelected(t)
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())

	require.NoError(t, statusOfProject(cmd, false), errOut.String())
	assert.Contains(t, errOut.String(), "▸ todoapp/staging\n")
}

func TestDeploysAnnounceTheEnvironmentTheyResolved(t *testing.T) {
	stagingSelected(t)
	var errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())

	_, _, err := openLinked(cmd)
	require.NoError(t, err)
	assert.Equal(t, "▸ todoapp/staging\n", errOut.String())
}
