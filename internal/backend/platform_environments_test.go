package backend

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A FAILED READ OF ONE ENVIRONMENT IS NOT A FAILED LINK. Every environment of a
// project is described now, and a social-auth read that fails for staging used
// to take down the link of main with it.
func TestANonDefaultEnvironmentWhoseSocialReadFailsIsDropped(t *testing.T) {
	inScratchCheckout(t)
	main := stackServing(t, "pb_project_cMAIN", nil)
	staging, _ := envServer(t, "pb_project_cSTAGING", envServerOpts{}) // its social read answers 404
	linkedAs(t, main.URL, "a-credential")
	linkedAs(t, staging.URL, "a-credential")
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main":    {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_project_cMAIN"},
		"staging": {AppID: projectAppID, BaseURL: staging.URL, APIKey: "pb_project_cSTAGING"},
	}}

	target := Target{URL: main.URL}
	result, dropped, err := platformEnvironments(context.Background(), &target, "web", source)
	require.NoError(t, err)
	require.Len(t, dropped, 1)
	assert.Error(t, dropped["staging"], "the dropped environment carries its reason")
	assert.Contains(t, result.Environments, "main")
	assert.NotContains(t, result.Environments, "staging")
}

func TestTheDefaultEnvironmentWhoseSocialReadFailsIsFatal(t *testing.T) {
	inScratchCheckout(t)
	main, _ := envServer(t, "pb_project_cMAIN", envServerOpts{})
	linkedAs(t, main.URL, "a-credential")
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main": {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_project_cMAIN"},
	}}

	target := Target{URL: main.URL}
	_, _, err := platformEnvironments(context.Background(), &target, "web", source)
	require.Error(t, err)
}
