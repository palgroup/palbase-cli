package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/config"
)

// cloneEnvs puts `main` neither first nor first among the available ones, so a
// helper that returned a position instead of applying the default rule fails.
func cloneEnvs() []Environment {
	return []Environment{
		{Name: "broken", Ref: "ccccccccc", Status: "Failed"},
		{Name: "canary", Ref: "ddddddddd", Status: "Running"},
		{Name: "main", Ref: "aaaaaaaaa", Status: "Running"},
		{Name: "staging", Ref: "bbbbbbbbb", Status: "Running"},
	}
}

// refusalHeader is the sentence a refusal says before the listing it appends:
// what the refusal names has to be in HERE, not merely somewhere in the list.
func refusalHeader(err error) string {
	return strings.SplitN(err.Error(), "\n", 2)[0]
}

func TestCloneByRefTakesThatEnvironment(t *testing.T) {
	ref, err := cloneEnvironmentRef(Product{ID: "proj_a", Name: "todoapp"}, cloneEnvs(), "bbbbbbbbb", "")
	require.NoError(t, err)
	assert.Equal(t, "bbbbbbbbb", ref)
}

func TestCloneByNameFollowsTheDefaultRule(t *testing.T) {
	inScratchCheckout(t)
	ref, err := cloneEnvironmentRef(Product{ID: "proj_a", Name: "todoapp"}, cloneEnvs(), "todoapp", "")
	require.NoError(t, err)
	assert.Equal(t, "aaaaaaaaa", ref)
}

func TestCloneRefusesARefThatContradictsFromEnv(t *testing.T) {
	_, err := cloneEnvironmentRef(Product{ID: "proj_a", Name: "todoapp"}, cloneEnvs(), "bbbbbbbbb", "main")
	require.Error(t, err)
	assert.Contains(t, refusalHeader(err), "todoapp/staging")
	assert.Contains(t, refusalHeader(err), "--from-env names main")
}

func TestCloneAcceptsAFromEnvThatNamesTheRefsOwnEnvironment(t *testing.T) {
	for _, fromEnv := range []string{"staging", "STAGING", "bbbbbbbbb"} {
		ref, err := cloneEnvironmentRef(Product{ID: "proj_a", Name: "todoapp"}, cloneEnvs(), "bbbbbbbbb", fromEnv)
		require.NoError(t, err, fromEnv)
		assert.Equal(t, "bbbbbbbbb", ref, fromEnv)
	}
}

func TestCloneRefusesAnUnavailableEnvironment(t *testing.T) {
	_, err := cloneEnvironmentRef(Product{ID: "proj_a", Name: "todoapp"}, cloneEnvs(), "ccccccccc", "")
	require.Error(t, err)
	assert.Contains(t, refusalHeader(err), "todoapp/broken is Failed")
}

// A CONTRADICTION THAT MEETS A FAILED ENVIRONMENT NAMES THE STATUS (FR-081): it
// is what tells a person which half of the command to drop.
func TestCloneNamesTheStatusInAContradiction(t *testing.T) {
	_, err := cloneEnvironmentRef(Product{ID: "proj_a", Name: "todoapp"}, cloneEnvs(), "ccccccccc", "main")
	require.Error(t, err)
	assert.Contains(t, refusalHeader(err), "todoapp/broken (Failed)")

	_, err = cloneEnvironmentRef(Product{ID: "proj_a", Name: "todoapp"}, cloneEnvs(), "bbbbbbbbb", "broken")
	require.Error(t, err)
	assert.Contains(t, refusalHeader(err), "--from-env names broken (Failed)")
}

func TestCloneSaysWhenFromEnvNamesNoEnvironment(t *testing.T) {
	_, err := cloneEnvironmentRef(Product{ID: "proj_a", Name: "todoapp"}, cloneEnvs(), "bbbbbbbbb", "prod")
	require.Error(t, err)
	assert.Contains(t, refusalHeader(err), `"prod", which is not one of its environments`)
}

func cloneListing() Resolvers {
	rest := &nameREST{rows: []map[string]any{
		{"id": "proj_a", "name": "todoapp", "environments": []map[string]any{
			{"ref": "aaaaaaaaa", "name": "main", "status": "Running"},
			{"ref": "bbbbbbbbb", "name": "staging", "status": "Running"},
		}},
	}}
	return Resolvers{
		REST:      func() REST { return rest },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "palbase.studio"} },
	}
}

// runClone runs the command the way a person does and returns what it
// announced on stderr. There is no credential for any address here, so every
// clone stops right after the announcement — which is the part under test.
func runClone(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCloneCmd(cloneListing())
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return errOut.String(), err
}

// THROUGH THE COMMAND (FR-076): the call site is what T004 changed, so it is
// what is measured. This machine chose `main` for the project; the ref names
// staging, and staging is what clone announces. The directory it made for the
// download is gone when the download cannot start.
func TestCloneByRefTakesThatEnvironmentThroughTheCommand(t *testing.T) {
	inScratchCheckout(t)
	root, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, WriteSelection(root, Selection{Project: "proj_a", Env: "main", Ref: "aaaaaaaaa"}))

	announced, err := runClone(t, "bbbbbbbbb")
	require.Error(t, err, "there is no credential for the address, so the download cannot start")
	assert.Contains(t, announced, "▸ todoapp/staging")
	assert.NoDirExists(t, filepath.Join(root, "todoapp"))
}

// A PRODUCT ID IS WHAT `project list --json` PRINTS AND WHAT AN AMBIGUOUS NAME
// IS TOLD TO USE. Clone used to refuse anything shaped `proj_…` by its prefix.
func TestCloneTakesAProductID(t *testing.T) {
	inScratchCheckout(t)
	announced, err := runClone(t, "proj_a")
	require.Error(t, err, "there is no credential for the address, so the download cannot start")
	assert.NotContains(t, err.Error(), "management project id")
	assert.Contains(t, announced, "▸ todoapp/main")
}
