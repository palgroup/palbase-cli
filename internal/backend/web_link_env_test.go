package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWireWebProjectGeneratesTheEnvironmentItIsGiven(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen") // seeds `main` and the generator
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))
	for _, env := range []string{"canary", "staging"} {
		require.NoError(t, os.MkdirAll(EnvDir(env), 0o755))
		require.NoError(t, os.WriteFile(ConfigPath(env, webPlatform),
			[]byte(`{"base_url":"https://`+env+`","api_key":"pb_stub"}`+"\n"), 0o600))
		require.NoError(t, os.WriteFile(SpecPath(env), []byte(`{"openapi":"3.1.0","paths":{}}`), 0o644))
	}

	var buf bytes.Buffer
	require.NoError(t, wireWebProject(context.Background(), "", "", "staging", &buf), buf.String())
	client, err := os.ReadFile(filepath.Join("palbase", "client.ts"))
	require.NoError(t, err)
	require.Contains(t, string(client), "./environments/staging/",
		"the web client was generated for the disk default, not the environment the link chose")
}

// THROUGH THE LINK (FR-018). The test above drives the wiring directly, and a
// suite that only did that stayed green when the link's call site passed no
// environment. `--from-env staging`, in a checkout whose disk default is
// `main`, generates the client for staging.
func TestALinkFromStagingGeneratesTheWebClientForStaging(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen")
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	twoEnvironmentsOf(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))

	o := linkOpts{platforms: []string{"web"}, env: "staging"}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	client, err := os.ReadFile(filepath.Join("palbase", "client.ts"))
	require.NoError(t, err)
	require.Contains(t, string(client), "./environments/staging/",
		"the link generated the web client for the disk default, not the environment it read from")
}

// A WEB CHECKOUT WHOSE DEFAULT ENVIRONMENT HAS NOTHING DEPLOYED — a new
// project's first link (FR-012). The generator refuses without a contract; the
// link failed there, and the stage took every config and the "push" line down
// with it. The configs are written, the line is said, the client waits.
func TestAWebLinkToAnEnvironmentWithNothingDeployedWritesItsConfig(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen")
	require.NoError(t, os.RemoveAll(EnvDir("main")), "the stub's seed is a deployed main")
	main, _ := envServer(t, linkKeyMain, envServerOpts{noContract: true, socialAuth: true})
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		platforms:    []string{"web"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", webPlatform), "a link to an environment with nothing deployed wrote no config")
	require.Contains(t, out.String(), "the web client is not generated yet: main has no contract")
	require.NoFileExists(t, filepath.Join("palbase", "client.ts"), "a client was generated with no contract to generate it from")
}

// A GENERATOR THAT REFUSES IS HEARD. Its output went to the stage's buffer, which
// a failed link discards, so the link said only `palbe-gen: exit status 1`.
func TestAGeneratorRefusalReachesTheLinksError(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen")
	require.NoError(t, os.WriteFile(palbeGenBin, []byte("#!/bin/sh\necho 'error: the generator explains itself' >&2\nexit 1\n"), 0o755))
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		platforms:    []string{"web"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.ErrorContains(t, err, "error: the generator explains itself")
}
