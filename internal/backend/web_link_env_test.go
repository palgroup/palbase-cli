package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
