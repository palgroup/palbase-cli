package backend

import (
	"context"
	"net/http/httptest"
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
	installStubCodegen(t, "// gen") // no npm install: the generator is the stub
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
	_, selErr := ReadSelection(".")
	assert.Error(t, selErr, "a no-target link wrote a machine-local selection")
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
// fetched one environment. The environment the write resolved — staging here,
// not the default — is the one the link reads from (D-16).
//
// THE RECORD IS MEASURED BY WHAT IS NOT WRITTEN. Every test server is a
// loopback address, and a link that lost the project would write that address
// to this machine's record rather than to project.json — so the test asserts
// that no machine-local record appears and that project.json keeps its bytes.
func TestRefreshingClientsAfterAnAuthWriteRebindsTheProject(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen") // no npm install: the generator is the stub
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	twoEnvironmentsOf(t)
	prevFlag, prevHost := SelectedEnvFlag, TenantHost
	SelectedEnvFlag, TenantHost = "staging", "palbase.studio"
	t.Cleanup(func() { SelectedEnvFlag, TenantHost = prevFlag, prevHost })
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	recordBefore, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	// A web config already on disk is what tells the refresh this checkout has
	// a web client (linkedPlatforms, pull_spec.go:19-24).
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", webPlatform),
		[]byte(`{"app_id":"project","base_url":"`+main.URL+`","api_key":"`+linkKeyMain+`"}`+"\n"), 0o600))

	var out strings.Builder
	require.NoError(t, RefreshLinkedClients(context.Background(), &out), out.String())

	assert.Contains(t, out.String(), "contract read from staging", "the refresh did not read from the environment the auth write resolved")
	recordAfter, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	assert.Equal(t, string(recordBefore), string(recordAfter), "the refresh rewrote the project record")
	local, err := localPath()
	require.NoError(t, err)
	_, statErr := os.Stat(local)
	assert.True(t, os.IsNotExist(statErr), "the refresh wrote an address into this machine's record")
	require.FileExists(t, ConfigPath("staging", webPlatform), "the refresh fetched one environment")
}

// A STACK LINKED ON THIS MACHINE BY ADDRESS COMES FIRST, as it does for every
// verb (Resolve). A checkout bound to a project and then linked to a loopback
// install had its app re-pointed at the cloud by a no-target link or an auth
// refresh, while push and status went on acting on that install.
func loopbackInstallOverAProject(t *testing.T) *httptest.Server {
	t.Helper()
	cloudMain := stackServing(t, linkKeyMain, nil)
	cloudStaging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": cloudMain.URL, "stagref000": cloudStaging.URL})
	twoEnvironmentsOf(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	installed := stackServing(t, linkKeyCanary, nil)
	linkedAs(t, installed.URL, "a-credential")
	require.NoError(t, WriteSelfHostTarget(Target{URL: installed.URL}))
	return installed
}

func TestANoTargetLinkFollowsALoopbackInstallBeforeTheProjectRecord(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen") // no npm install: the generator is the stub
	installed := loopbackInstallOverAProject(t)

	o := linkOpts{platforms: []string{"web"}}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	assert.Equal(t, installed.URL, o.url, "a no-target link bound the project while this checkout acts on the install linked here")
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	raw, err := os.ReadFile(ConfigPath("main", webPlatform))
	require.NoError(t, err)
	assert.Contains(t, string(raw), installed.URL)
	assert.NoFileExists(t, ConfigPath("staging", webPlatform))
}

func TestAnAuthRefreshFollowsALoopbackInstallBeforeTheProjectRecord(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen") // no npm install: the generator is the stub
	installed := loopbackInstallOverAProject(t)
	require.NoError(t, os.MkdirAll(EnvDir("main"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath("main", webPlatform),
		[]byte(`{"app_id":"project","base_url":"`+installed.URL+`","api_key":"`+linkKeyCanary+`"}`+"\n"), 0o600))

	var out strings.Builder
	require.NoError(t, RefreshLinkedClients(context.Background(), &out), out.String())
	raw, err := os.ReadFile(ConfigPath("main", webPlatform))
	require.NoError(t, err)
	assert.Contains(t, string(raw), installed.URL, "the refresh re-pointed the app at the cloud while every verb acts on the install")
	assert.NoFileExists(t, ConfigPath("staging", webPlatform))
}

// BINDING THE PROJECT RELEASES A STACK LINKED HERE BY ADDRESS. The loopback
// link came first and `palbase link <project>` second: the machine-local record
// the first one wrote survived, so the next no-target link or auth refresh
// pointed the app back at the stack the person had just left.
func TestLinkingTheProjectReleasesAStackLinkedHereByAddress(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen")
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	twoEnvironmentsOf(t)
	installed := stackServing(t, linkKeyCanary, nil)
	linkedAs(t, installed.URL, "a-credential")
	require.NoError(t, WriteSelfHostTarget(Target{URL: installed.URL}))

	named := linkOpts{
		url:       main.URL,
		platforms: []string{"web"},
		linkedEnv: "main",
		product:   Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		},
	}
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), named, &out), out.String())
	local, err := localPath()
	require.NoError(t, err)
	assert.NoFileExists(t, local, "linking the project left the stack linked here by address in charge")
	assert.Contains(t, out.String(), "no longer acts on the stack linked here by address")

	again := linkOpts{platforms: []string{"web"}}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &again))
	assert.Equal(t, "prd_a", again.product.ID, "a no-target link after linking the project went back to the stack")
}

// UNLINK RELEASES A STACK LINKED HERE BY ADDRESS. It removed only
// project.json, said "not linked", and every verb went on acting on the stack.
func TestUnlinkReleasesAStackLinkedHereByAddress(t *testing.T) {
	inScratchCheckout(t)
	installed := stackServing(t, linkKeyCanary, nil)
	require.NoError(t, WriteSelfHostTarget(Target{URL: installed.URL}))

	var out strings.Builder
	require.NoError(t, runUnlink(&out))
	local, err := localPath()
	require.NoError(t, err)
	assert.NoFileExists(t, local, "unlink left the stack linked here by address in charge")
	assert.Contains(t, out.String(), "re-link with `palbase link <url>`")
	assert.NotContains(t, out.String(), "was not linked")
}

// --from-env names one of a project's environments; while a stack linked here by
// address is what this checkout acts on there is nothing to choose between, and
// ignoring the flag linked the stack as if it had not been given.
func TestFromEnvIsRefusedWhileAStackLinkedHereByAddressIsInCharge(t *testing.T) {
	inScratchCheckout(t)
	_ = loopbackInstallOverAProject(t)

	o := linkOpts{env: "staging"}
	require.ErrorContains(t, resolveLinkTarget(context.Background(), Resolvers{}, &o), "--from-env")
}

// UNLINK LEAVES THE STACK `palbase start` RUNS HERE. That record is not a
// binding somebody made with `link`; `palbase stop` is what ends it (FR-084).
func TestUnlinkLeavesTheStackPalbaseStartRuns(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	require.NoError(t, WriteLocalTarget(Target{URL: "http://127.0.0.1:54321"}))
	local, err := localPath()
	require.NoError(t, err)
	before, err := os.ReadFile(local)
	require.NoError(t, err)

	var out strings.Builder
	require.NoError(t, runUnlink(&out))
	after, err := os.ReadFile(local)
	require.NoError(t, err, "unlink removed the record `palbase start` keeps")
	assert.Equal(t, string(before), string(after))
	assert.NoFileExists(t, projectPath())
	assert.NotContains(t, out.String(), "no longer acts")
}

// AN UNREADABLE MACHINE RECORD DOES NOT STOP UNLINK. Nothing in it can be read
// as a stack linked by address, and `Resolve` passes it over too; refusing made
// the committed binding impossible to remove.
func TestAnUnreadableMachineRecordDoesNotStopUnlink(t *testing.T) {
	inScratchCheckout(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))
	require.NoError(t, WriteSelfHostTarget(Target{URL: "http://127.0.0.1:54321"}))
	local, err := localPath()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(local, 0o000))
	t.Cleanup(func() { _ = os.Chmod(local, 0o600) })
	if _, readErr := os.ReadFile(local); readErr == nil {
		t.Skip("this user reads a mode-000 file, so the record is not unreadable here")
	}

	var out strings.Builder
	require.NoError(t, runUnlink(&out), out.String())
	assert.NoFileExists(t, projectPath())
}
