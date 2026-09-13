package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedIOSKeys(t *testing.T, url string, keys map[string]string) {
	t.Helper()
	for env, key := range keys {
		require.NoError(t, os.MkdirAll(EnvDir(env), 0o755))
		blob, err := json.MarshalIndent(appEnvironment{AppID: projectAppID, BaseURL: url, APIKey: key}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(ConfigPath(env, "ios"), blob, 0o644))
	}
}

// runStatus runs `palbase status`, as text or as --json, the way the command does.
func runStatus(t *testing.T, jsonOut bool) string {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	require.NoError(t, statusOfProject(cmd, jsonOut), errOut.String())
	return out.String()
}

// EVERY ENVIRONMENT IS ON DISK NOW, so "the" committed key is a question with
// one answer per environment — and the one to compare is the environment this
// command resolved, not whichever sorts first.
func TestKeyDriftComparesTheResolvedEnvironment(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cSTAGINGNOW", nil)
	linkedAs(t, srv.URL, "a-credential")
	seedIOSKeys(t, srv.URL, map[string]string{"main": "pb_project_cMAINOLD", "staging": "pb_project_cSTAGINGNOW"})

	var out bytes.Buffer
	reportKeyDrift(context.Background(), Target{URL: srv.URL}, "staging",
		Credentials{Value: "a-credential", Kind: KindPerson}, &out)
	assert.Contains(t, out.String(), "current")
	assert.NotContains(t, out.String(), "STALE")
	assert.Equal(t, "current", appKeyState(context.Background(), Target{URL: srv.URL}, "staging"))
}

func TestKeyDriftWithNoEnvironmentFallsBackToTheDiskDefault(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cSTAGINGNOW", nil)
	linkedAs(t, srv.URL, "a-credential")
	seedIOSKeys(t, srv.URL, map[string]string{"main": "pb_project_cMAINOLD", "staging": "pb_project_cSTAGINGNOW"})

	var out bytes.Buffer
	reportKeyDrift(context.Background(), Target{URL: srv.URL}, "",
		Credentials{Value: "a-credential", Kind: KindPerson}, &out)
	assert.Contains(t, out.String(), "STALE", "with no resolved environment the disk default (main) is compared")
}

// A RESOLVED ENVIRONMENT WITH NO COMMITTED CONFIG IS SAID, NOT PASSED OVER.
// One link writes every environment, so a checkout can carry main and not the
// staging this command acts on — and saying nothing would read as "the key is
// fine".
func TestKeyDriftSaysWhenTheResolvedEnvironmentHasNoConfig(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cSTAGINGNOW", nil)
	linkedAs(t, srv.URL, "a-credential")
	seedIOSKeys(t, srv.URL, map[string]string{"main": "pb_project_cMAINOLD"})

	var out bytes.Buffer
	reportKeyDrift(context.Background(), Target{URL: srv.URL}, "staging",
		Credentials{Value: "a-credential", Kind: KindPerson}, &out)
	assert.Contains(t, out.String(), "app key:      unchecked — staging has no committed config")
	assert.Equal(t, "unchecked", appKeyState(context.Background(), Target{URL: srv.URL}, "staging"))
}

// THROUGH THE COMMAND. A stack running here is `local` on disk, and its key is
// the one `palbase status` compares — not whichever environment sorts first.
// The two functions above take the environment; this measures that the
// command hands them the right one.
func TestStatusComparesTheKeyOfTheStackRunningHere(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cLOCALNOW", nil)
	require.NoError(t, WriteLocalTarget(Target{URL: srv.URL, Local: true}))
	require.NoError(t, StoreCredential(srv.URL, Credentials{Value: "k", Kind: KindKey}))
	seedIOSKeys(t, srv.URL, map[string]string{"local": "pb_project_cLOCALNOW", "main": "pb_project_cMAINOLD"})

	out := runStatus(t, false)
	assert.Contains(t, out, "app key:      current")
	assert.NotContains(t, out, "STALE", "status compared the key of an environment it did not resolve")
}

// THROUGH THE COMMAND, FOR A CLOUD ENVIRONMENT (FR-073, FR-074). `--env
// staging` resolves staging, and the text and the --json output both compare
// staging's committed key — not main's, which sorts first and is stale here.
func TestStatusComparesTheKeyOfTheNamedCloudEnvironment(t *testing.T) {
	inScratchCheckout(t)
	staging := stackServing(t, "pb_project_cSTAGINGNOW", nil)
	routeEnvironments(t, map[string]string{"stagref000": staging.URL})
	twoEnvironmentsOf(t)
	prev := SelectedEnvFlag
	SelectedEnvFlag = "staging"
	t.Cleanup(func() { SelectedEnvFlag = prev })
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	seedIOSKeys(t, staging.URL, map[string]string{"main": "pb_project_cMAINOLD", "staging": "pb_project_cSTAGINGNOW"})

	text := runStatus(t, false)
	assert.Contains(t, text, "app key:      current")
	assert.NotContains(t, text, "STALE", "the text output compared an environment the command did not resolve")

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(runStatus(t, true)), &doc))
	assert.Equal(t, "current", doc["app_key"], "the --json output compared an environment the command did not resolve")
}
