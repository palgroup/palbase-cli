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
