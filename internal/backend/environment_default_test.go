package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
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
