package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultEnvironmentPrefersMainThenFirstByName(t *testing.T) {
	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"canary", "main", "local"}, "main"},
		{[]string{"staging", "canary"}, "canary"},
		{[]string{"Main", "beta"}, "Main"},
		{[]string{"local"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, defaultEnvironment(c.names), "%v", c.names)
	}
}

func TestUnavailableEnvironmentIsFailedOrDeleting(t *testing.T) {
	for _, s := range []string{"Failed", "Deleting", "failed", "DELETING"} {
		assert.True(t, unavailableEnvironment(s), s)
	}
	for _, s := range []string{"Running", "Archived", "Pending", "Provisioning", ""} {
		assert.False(t, unavailableEnvironment(s), s)
	}
}

func TestReadAppEnvironmentsDefaultsToMain(t *testing.T) {
	inScratchCheckout(t)
	for _, env := range []string{"canary", "main"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(ConfigPath(env, "web")), 0o755))
		blob, err := json.Marshal(appEnvironment{AppID: projectAppID, BaseURL: "https://" + env})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(ConfigPath(env, "web"), blob, 0o600))
	}
	envs, err := readAppEnvironments("web")
	require.NoError(t, err)
	assert.Equal(t, "main", envs.Default)
}

func TestLinkRefusesAFromEnvThatIsUnavailable(t *testing.T) {
	envs := []Environment{
		{Name: "main", Ref: "aaaaaaaaa", Status: "Running"},
		{Name: "staging", Ref: "bbbbbbbbb", Status: "Failed"},
	}
	_, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "staging")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "staging")
	assert.Contains(t, err.Error(), "Failed")
	assert.Contains(t, strings.SplitN(err.Error(), "\n", 2)[0], "staging of todoapp is Failed",
		"the refusal itself names neither the environment nor its status")
}

func TestLinkRefusesWhenEveryEnvironmentIsUnavailable(t *testing.T) {
	envs := []Environment{
		{Name: "main", Ref: "aaaaaaaaa", Status: "Deleting"},
		{Name: "staging", Ref: "bbbbbbbbb", Status: "Failed"},
	}
	_, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Deleting")
	assert.Contains(t, err.Error(), "Failed")
	assert.Regexp(t, "main +aaaaaaaaa +Deleting", err.Error())
	assert.Regexp(t, "staging +bbbbbbbbb +Failed", err.Error())
}

func TestLinkDefaultSkipsAnUnavailableMain(t *testing.T) {
	inScratchCheckout(t)
	envs := []Environment{
		{Name: "main", Ref: "aaaaaaaaa", Status: "Failed"},
		{Name: "staging", Ref: "bbbbbbbbb", Status: "Running"},
		{Name: "canary", Ref: "ccccccccc", Status: "Running"},
	}
	ref, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "")
	require.NoError(t, err)
	assert.Equal(t, "ccccccccc", ref, "the default rule must pick the first available name")
}

// ONE ENVIRONMENT, AND IT IS FAILED (FR-079). The shortcut for a project with a
// single environment used to come before the status filter.
func TestLinkRefusesTheOnlyEnvironmentWhenItIsUnavailable(t *testing.T) {
	envs := []Environment{{Name: "main", Ref: "aaaaaaaaa", Status: "Failed"}}
	_, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "")
	require.Error(t, err)
	assert.Contains(t, strings.SplitN(err.Error(), "\n", 2)[0], "nothing to link")
	assert.Regexp(t, "main +aaaaaaaaa +Failed", err.Error())
}

// A REMEMBERED CHOICE OF A FAILED ENVIRONMENT IS NOT A CANDIDATE (D-9). This
// machine chose `main`; `main` has failed since, so the default rule decides
// among the rest.
func TestLinkIgnoresASelectionOfAnUnavailableEnvironment(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteSelection(".", Selection{Project: "prd_a", Env: "main", Ref: "aaaaaaaaa"}))
	envs := []Environment{
		{Name: "main", Ref: "aaaaaaaaa", Status: "Failed"},
		{Name: "staging", Ref: "bbbbbbbbb", Status: "Running"},
		{Name: "canary", Ref: "ccccccccc", Status: "Running"},
	}
	ref, err := linkEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, envs, "")
	require.NoError(t, err)
	assert.Equal(t, "ccccccccc", ref, "the selection of a Failed environment was followed")
}
