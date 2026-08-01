package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
)

// stubPalbeGen installs a fake node_modules/.bin/palbe-gen that writes a marker
// to its --out target — standing in for @palbase/web's generator, so tests can
// assert the client WAS regenerated without npm or a real network.
func stubPalbeGen(t *testing.T) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(palbeGenBin), 0o755))
	script := "#!/bin/sh\nout=palbe.gen.ts\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = \"--out\" ]; then out=\"$2\"; shift; fi\n  shift\ndone\nprintf 'regenerated\\n' > \"$out\"\n"
	require.NoError(t, os.WriteFile(palbeGenBin, []byte(script), 0o755))
}

// webUseRig seeds the app + config-artifact reads `web use` makes on top of the
// shared use rig.
func webUseRig(t *testing.T, cfg *selection.Config) Resolvers {
	t.Helper()
	f, r := useRig(t, cfg)
	f.OK("GET /api/v2/projects/proj_1/apps", []map[string]any{
		{"id": "app_web", "platform": "web", "display_name": "Web"},
	})
	f.OK("GET /api/v2/apps/app_web/config-artifact", map[string]any{
		"app_id": "app_web", "environment_ref": "app1stg", "platform": "web",
		"api_key":  "pb_app1stg_c01234567890123456789",
		"base_url": "https://app1stg.dev.palbase.studio", "kind": "staging",
	})
	return r
}

// `web use <environment>` is the web counterpart of `ios use`: it re-targets the
// ENVIRONMENT — rewriting the committed runtime config, recording the selection,
// and regenerating the client that EMBEDS that config. Skipping the last step is
// the silent failure this locks out: an app whose palbe.gen.ts still talks to the
// previous environment while every file on disk says otherwise.
func TestWebUse_RetargetsTheEnvironment(t *testing.T) {
	r := webUseRig(t, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod",
		RepositoryProvider: selection.ProviderPalbase, WebAppID: "app_web",
	})
	stubPalbeGen(t)
	require.NoError(t, os.WriteFile("palbe.gen.ts", []byte("stale: production\n"), 0o644))

	wc := &webCmd{r: r}
	cmd := wc.newWebUseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"staging"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	// The committed runtime config names staging and carries no branch.
	raw, err := os.ReadFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "app1stg", got["environment_ref"])
	require.Equal(t, "https://app1stg.dev.palbase.studio", got["base_url"])
	require.NotContains(t, string(raw), "branch")

	// Web artifacts land in Palbase/ — never in the native .palbase/ tree.
	require.FileExists(t, filepath.Join(webArtifactsDir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(nativeArtifactsDir, "openapi.json"))

	// The SELECTION followed the config: a build made now connects to staging,
	// so the two must not disagree.
	cfg, err := selection.Load("")
	require.NoError(t, err)
	require.Equal(t, "env_stg", cfg.EnvironmentID)
	require.Equal(t, "app_web", cfg.WebAppID)

	// The generated client was re-emitted from the new config.
	gen, err := os.ReadFile("palbe.gen.ts")
	require.NoError(t, err)
	require.Equal(t, "regenerated\n", string(gen))

	require.Contains(t, out.String(), "targets environment staging")
}

// Without @palbase/web installed there is no generator to run, so the gen file
// still points at the PREVIOUS environment. That must be said out loud rather
// than reported as a clean switch.
func TestWebUse_WarnsWhenTheClientCannotBeRegenerated(t *testing.T) {
	r := webUseRig(t, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod",
		RepositoryProvider: selection.ProviderPalbase, WebAppID: "app_web",
	})
	// Deliberately no node_modules/.bin/palbe-gen.
	require.NoError(t, os.WriteFile("palbe.gen.ts", []byte("stale: production\n"), 0o644))

	wc := &webCmd{r: r}
	cmd := wc.newWebUseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"staging"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	require.Contains(t, out.String(), "still points at the previous environment")
	gen, err := os.ReadFile("palbe.gen.ts")
	require.NoError(t, err)
	require.Equal(t, "stale: production\n", string(gen))
}

func TestWebUse_RequiresALinkedApp(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})
	wc := &webCmd{r: r}
	cmd := wc.newWebUseCmd()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"staging"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.ErrorContains(t, cmd.Execute(), "run `palbase web link` first")
}

// The point of splitting `spec` per platform: each command writes the directory
// ITS generator reads, and only that one. A web checkout must never end up with
// a refreshed .palbase/openapi.json while palbe-gen keeps reading a stale
// Palbase/openapi.json (or the reverse on a native checkout).
func TestWebSpec_RefreshesOnlyTheWebContract(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})

	cmd := newWebSpecCmd(r)
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	require.FileExists(t, filepath.Join(webArtifactsDir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(nativeArtifactsDir, "openapi.json"))
	// spec is contract-only: runtime config comes from link/use.
	require.NoFileExists(t, filepath.Join(webArtifactsDir, "palbase-config.json"))
}

func TestNativeSpec_RefreshesOnlyTheNativeContract(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})

	cmd := newNativeSpecCmd(r, "ios")
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	require.FileExists(t, filepath.Join(nativeArtifactsDir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(webArtifactsDir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"))
}
