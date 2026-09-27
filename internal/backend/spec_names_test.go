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

// AND `palbase spec` SAYS THE SAME READ ERROR WITHOUT THE RAW NAME (FR-006):
// an Apple checkout regenerates every environment's client from the configs on
// disk, and a config it cannot read ends the command with that error.
func TestSpecPrintsNoControlCharacterFromAnEnvironmentDirectory(t *testing.T) {
	specRigFor(t, []Environment{{Ref: "mainref000", Name: "main", Status: "Running"}}, "main")
	require.NoError(t, os.MkdirAll("App.xcodeproj", 0o755))
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", "ios"), []byte(`{"base_url":"https://main.example"}`), 0o600))
	require.NoError(t, os.MkdirAll(ConfigPath("evil\x1b[2J", "ios"), 0o755))

	var out bytes.Buffer
	err := RefreshSpec(context.Background(), &out)
	require.EqualError(t, err, `read palbase/environments/"evil\x1b[2J"/ios-config.json: is a directory`, out.String())
	require.NotContains(t, out.String(), "\x1b")
}

// TWO LISTED NAMES THAT ARE ONE DIRECTORY ON A MAC (FR-003) ARE REFUSED BY
// `palbase spec` AS BY `link` (final review, Minor #6). Link refused the pair;
// spec asked only whether the one name was a directory, so with `Main` and
// `main` both listed, `spec --env Main` wrote `Main/openapi.json` — which on a
// Mac is `main/openapi.json`, and main built against Main's contract.
func TestSpecRefusesAnEnvironmentThatSharesItsDirectoryWithAnother(t *testing.T) {
	for _, named := range []string{"Main", "mainref001"} {
		t.Run(named, func(t *testing.T) {
			specRigFor(t, []Environment{
				{Ref: "mainref001", Name: "Main", Status: "Running"},
				{Ref: "mainref000", Name: "main", Status: "Running"},
			}, named)

			var out bytes.Buffer
			err := RefreshSpec(context.Background(), &out)
			require.EqualError(t, err, `environments "Main" (mainref001) and "main" (mainref000) match when letter case `+
				`and Unicode form are ignored, so they would share one directory, and "Main" is the one this refresh `+
				`writes — rename one in the dashboard`, out.String())
			require.NoDirExists(t, filepath.Join(RootDir(), envSubdir))
		})
	}
}
