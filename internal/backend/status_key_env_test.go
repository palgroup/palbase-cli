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

	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	require.NoError(t, statusOfProject(cmd, false), errOut.String())
	assert.Contains(t, out.String(), "app key:      current")
	assert.NotContains(t, out.String(), "STALE", "status compared the key of an environment it did not resolve")
}
