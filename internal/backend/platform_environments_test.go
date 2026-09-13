package backend

import (
	"context"
	"os"
	"strings"
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

// divergentSnapshotServer answers a social read whose snapshot names another
// environment than the publishable key does, for a checkout whose Xcode project
// does identify the configured iOS client — so the read itself succeeds, and
// the only thing wrong is which environment it is for.
func divergentSnapshotServer(t *testing.T) string {
	t.Helper()
	require.NoError(t, os.Mkdir("Consumer.xcodeproj", 0o755))
	require.NoError(t, os.WriteFile("Consumer.xcodeproj/project.pbxproj", []byte(`PRODUCT_BUNDLE_IDENTIFIER = com.example.app;`), 0o644))
	srv := oauthLinkServer(t, strings.Replace(iosSnapshot, `"environment_ref":"env"`, `"environment_ref":"elsewhere"`, 1))
	return srv.URL
}

// A SNAPSHOT FOR ANOTHER ENVIRONMENT IS A FAILED READ (FR-013). Written, it
// would hand the app a client that signs in against the wrong environment.
func TestANonDefaultEnvironmentWhoseSnapshotDivergesIsDropped(t *testing.T) {
	inScratchCheckout(t)
	main := stackServing(t, "pb_project_cMAIN", nil)
	linkedAs(t, main.URL, "a-credential")
	staging := divergentSnapshotServer(t)
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main":    {AppID: projectAppID, BaseURL: main.URL, APIKey: "pb_project_cMAIN"},
		"staging": {AppID: projectAppID, BaseURL: staging, APIKey: "pb_env_cPUBLIC"},
	}}

	target := Target{URL: main.URL}
	result, dropped, err := platformEnvironments(context.Background(), &target, "ios", source)
	require.NoError(t, err)
	require.Contains(t, dropped, "staging")
	assert.ErrorContains(t, dropped["staging"], "differs from the publishable key")
	assert.NotContains(t, result.Environments, "staging")
	assert.Empty(t, target.OAuth, "a dropped environment's client selection was kept")
}

func TestTheDefaultEnvironmentWhoseSnapshotDivergesIsFatal(t *testing.T) {
	inScratchCheckout(t)
	main := divergentSnapshotServer(t)
	source := appEnvironments{Default: "main", Environments: map[string]appEnvironment{
		"main": {AppID: projectAppID, BaseURL: main, APIKey: "pb_env_cPUBLIC"},
	}}

	target := Target{URL: main}
	_, _, err := platformEnvironments(context.Background(), &target, "ios", source)
	require.ErrorContains(t, err, "differs from the publishable key")
}
