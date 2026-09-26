package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// specRigFor links a scratch checkout to a project whose environments are
// served by one stack that answers every read, and selects `named` with
// --env — so nothing but the name gate keeps its contract off the disk.
func specRigFor(t *testing.T, envs []Environment, named string) {
	t.Helper()
	linkedTo(t, Target{Project: "prd_a", Name: "todoapp"})
	resolverRig(t, envs)
	stack := stackServing(t, linkKeyMain, nil)
	byRef := map[string]string{}
	for _, e := range envs {
		byRef[e.Ref] = stack.URL
	}
	routeEnvironments(t, byRef)
	SelectedEnvFlag = named
}

// `palbase spec` WRITES IN THE REAL CHECKOUT, NOT A STAGE (FR-002), so a name
// that is not one directory went straight where it pointed: measured, `--env`
// on an environment named `../../gradle` wrote gradle/openapi.json.
func TestSpecRefusesAnEnvironmentWhoseNameIsNotOneDirectory(t *testing.T) {
	specRigFor(t, []Environment{
		{Ref: "mainref000", Name: "main", Status: "Running"},
		{Ref: "evilref001", Name: "../../gradle", Status: "Running"},
	}, "../../gradle")
	require.NoError(t, os.MkdirAll("gradle", 0o755))

	var out bytes.Buffer
	err := RefreshSpec(context.Background(), &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment "../../gradle" (evilref001) cannot be written to this checkout: `+
		`the name contains a path separator, so it cannot be a directory under palbase/environments — rename it in the dashboard`)
	require.Empty(t, entriesIn(t, "gradle"), "spec wrote into the checkout's own gradle/")
}

// AND THE PUSH'S REFRESH IS THE SAME ACT: `..` wrote palbase/openapi.json, the
// retired layout's marker, and every later `palbase link` refused the checkout.
func TestPushRefreshRefusesAnEnvironmentWhoseNameIsNotOneDirectory(t *testing.T) {
	specRigFor(t, []Environment{
		{Ref: "mainref000", Name: "main", Status: "Running"},
		{Ref: "evilref002", Name: "..", Status: "Running"},
	}, "..")
	seedWebCheckout(t)

	var out bytes.Buffer
	err := RefreshSpecAfterPush(context.Background(), &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment ".." (evilref002) cannot be written to this checkout: the name is "." or ".."`)
	require.NoFileExists(t, filepath.Join(RootDir(), "openapi.json"))
}

// A CLOUD ENVIRONMENT NAMED `local` DOES NOT GET THIS MACHINE'S DIRECTORY
// (FR-002 → FR-004): `palbase spec --env Local` wrote the cloud contract into
// local/, beside the machine stack's config.
func TestSpecRefusesACloudEnvironmentNamedLocal(t *testing.T) {
	specRigFor(t, []Environment{
		{Ref: "mainref000", Name: "main", Status: "Running"},
		{Ref: "locref0000", Name: "Local", Status: "Running"},
	}, "Local")

	var out bytes.Buffer
	err := RefreshSpec(context.Background(), &out)
	require.Error(t, err, out.String())
	require.Contains(t, err.Error(), `environment "Local" (locref0000) cannot be written to this checkout: `+
		"palbase/environments/local belongs to the stack `palbase start` runs on this machine, never to a cloud environment")
	require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
}
