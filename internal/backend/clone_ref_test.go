package backend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cloneEnvs() []Environment {
	return []Environment{
		{Name: "main", Ref: "aaaaaaaaa", Status: "Running"},
		{Name: "staging", Ref: "bbbbbbbbb", Status: "Running"},
		{Name: "broken", Ref: "ccccccccc", Status: "Failed"},
	}
}

func TestCloneByRefTakesThatEnvironment(t *testing.T) {
	ref, err := cloneEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, cloneEnvs(), "bbbbbbbbb", "")
	require.NoError(t, err)
	assert.Equal(t, "bbbbbbbbb", ref)
}

func TestCloneByNameFollowsTheDefaultRule(t *testing.T) {
	ref, err := cloneEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, cloneEnvs(), "todoapp", "")
	require.NoError(t, err)
	assert.Equal(t, "aaaaaaaaa", ref)
}

func TestCloneRefusesARefThatContradictsFromEnv(t *testing.T) {
	_, err := cloneEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, cloneEnvs(), "bbbbbbbbb", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "staging")
	assert.Contains(t, err.Error(), "main")
}

func TestCloneAcceptsARefThatAgreesWithFromEnv(t *testing.T) {
	ref, err := cloneEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, cloneEnvs(), "bbbbbbbbb", "staging")
	require.NoError(t, err)
	assert.Equal(t, "bbbbbbbbb", ref)
}

func TestCloneRefusesAnUnavailableEnvironment(t *testing.T) {
	_, err := cloneEnvironmentRef(Product{ID: "prd_a", Name: "todoapp"}, cloneEnvs(), "ccccccccc", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken")
	assert.Contains(t, err.Error(), "Failed")
}
