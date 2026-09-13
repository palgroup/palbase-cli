package backend

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func twoEnvironmentsOf(t *testing.T) {
	t.Helper()
	prev := EnvironmentsOf
	EnvironmentsOf = func(context.Context, string) ([]Environment, error) {
		return []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		}, nil
	}
	t.Cleanup(func() { EnvironmentsOf = prev })
}

// `palbase link` WITH NO TARGET is what `status` tells people to run when a key
// went stale, and in a checkout bound to a project it answered
// "--url is required" (measured on 0.66.0, LV-05). The committed record names
// the project; that is enough to link it again.
func TestLinkWithNoTargetRebindsTheProject(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	twoEnvironmentsOf(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	require.NoError(t, WriteLocalTarget(Target{URL: "http://127.0.0.1:1"}))
	local, err := localPath()
	require.NoError(t, err)
	localBefore, err := os.ReadFile(local)
	require.NoError(t, err)

	o := linkOpts{platforms: []string{"web"}}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())

	record, err := readLinkedProject()
	require.NoError(t, err)
	assert.Equal(t, "prd_a", record.Project)
	assert.Empty(t, record.URL, "the project record gained an address")
	localAfter, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.Equal(t, string(localBefore), string(localAfter), "the machine-local stack record was rewritten")
	require.FileExists(t, ConfigPath("main", webPlatform))
	require.FileExists(t, ConfigPath("staging", webPlatform))
}

func TestLinkOptsForRecordNamesTheProjectAndItsEnvironments(t *testing.T) {
	inScratchCheckout(t)
	routeEnvironments(t, map[string]string{"mainref000": "http://127.0.0.1:9", "stagref000": "http://127.0.0.1:8"})
	twoEnvironmentsOf(t)

	o, err := linkOptsForRecord(context.Background(), Target{Project: "prd_a", Name: "todoapp"}, "staging")
	require.NoError(t, err)
	assert.Equal(t, "prd_a", o.product.ID)
	assert.Equal(t, "todoapp", o.product.Name)
	assert.Len(t, o.environments, 2)
	assert.Equal(t, "staging", o.linkedEnv)
	assert.Equal(t, "http://127.0.0.1:8", o.url)
}

// FR-027: an auth write refreshes the app's config through the same link. It
// used to pass an ADDRESS alone, which wrote the retired record shape and
// fetched one environment. Two environments and no selection make the resolver
// ask for a choice, so this names one the way `--env` does.
func TestRefreshingClientsAfterAnAuthWriteRebindsTheProject(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	twoEnvironmentsOf(t)
	prevFlag, prevHost := SelectedEnvFlag, TenantHost
	SelectedEnvFlag, TenantHost = "main", "palbase.studio"
	t.Cleanup(func() { SelectedEnvFlag, TenantHost = prevFlag, prevHost })
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	// A web config already on disk is what tells the refresh this checkout has
	// a web client (linkedPlatforms, pull_spec.go:19-24).
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", webPlatform),
		[]byte(`{"app_id":"project","base_url":"`+main.URL+`","api_key":"`+linkKeyMain+`"}`+"\n"), 0o600))

	require.NoError(t, RefreshLinkedClients(context.Background(), io.Discard))

	record, err := readLinkedProject()
	require.NoError(t, err)
	assert.Equal(t, "prd_a", record.Project)
	assert.Empty(t, record.URL, "the refresh wrote an address into the project record")
	require.FileExists(t, ConfigPath("staging", webPlatform), "the refresh fetched one environment")
}
