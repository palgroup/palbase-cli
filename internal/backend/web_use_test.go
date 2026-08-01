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

// writeSlot drops the COMMITTED platform slot a link command would have left,
// which is what `spec` reads to decide where the contract belongs.
func writeSlot(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "palbase-config.json"),
		[]byte(`{"environment_ref":"app1prod","base_url":"https://app1prod.dev.palbase.studio","api_key":"pb_stub"}`+"\n"), 0o600))
}

// stubSwiftgenSources makes the Apple preflight pass without a resolved SwiftPM
// checkout on disk. Tests that assert the preflight FIRES must not call it.
func stubSwiftgenSources(t *testing.T) {
	t.Helper()
	orig := locateSwiftgenSources
	locateSwiftgenSources = func(string) (string, error) { return "/stub/palbase-swiftgen", nil }
	t.Cleanup(func() { locateSwiftgenSources = orig })
}

// `spec` writes the directory the LINKED platform's generator reads, and only
// that one. A web checkout must never end up with a refreshed
// .palbase/openapi.json while palbe-gen keeps reading a stale
// Palbase/openapi.json (or the reverse on a native checkout). The old code
// guessed this from a sibling directory relative to --out-dir; this reads the
// committed slot instead.
func TestSpec_WebLinked_WritesOnlyTheWebContract(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})
	writeSlot(t, webArtifactsDir)

	cmd := newSpecCmd(r)
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	require.FileExists(t, filepath.Join(webArtifactsDir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(nativeArtifactsDir, "openapi.json"))
}

func TestSpec_NativeLinked_WritesOnlyTheNativeContract(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})
	stubSwiftgenSources(t)
	writeSlot(t, filepath.Join(nativeArtifactsDir, "ios"))

	cmd := newSpecCmd(r)
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	require.FileExists(t, filepath.Join(nativeArtifactsDir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(webArtifactsDir, "openapi.json"))
}

// The reason ONE command beats four: a checkout linked for both platforms gets
// both contracts from a single run. With per-platform commands you had to know
// to run two, and forgetting one left that SDK generating from a stale spec.
func TestSpec_WebAndNativeLinked_WritesBoth(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})
	stubSwiftgenSources(t)
	writeSlot(t, webArtifactsDir)
	writeSlot(t, filepath.Join(nativeArtifactsDir, "ios"))

	cmd := newSpecCmd(r)
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	require.FileExists(t, filepath.Join(webArtifactsDir, "openapi.json"))
	require.FileExists(t, filepath.Join(nativeArtifactsDir, "openapi.json"))
}

// With no slot at all there is nothing to refresh and no directory to invent:
// say which command to run instead of silently creating an empty tree.
func TestSpec_UnlinkedCheckout_FailsActionably(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})

	cmd := newSpecCmd(r)
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()

	require.ErrorContains(t, err, "not linked")
	require.ErrorContains(t, err, "palbase web link")
	require.NoFileExists(t, filepath.Join(webArtifactsDir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(nativeArtifactsDir, "openapi.json"))
}

// An Apple checkout whose SwiftPM package is not resolved cannot regenerate the
// committed Swift client. Refreshing the spec anyway would leave that client
// stale beside a newer contract — it still compiles, so the drift only surfaces
// as a runtime 404 — which is why the old code deleted the generated files to
// escape. Failing BEFORE the first write removes the dilemma: the contract and
// the generated client both survive untouched, and re-running after an Xcode
// build just works.
func TestSpec_AppleCheckoutWithoutResolvedGenerator_WritesNothing(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})
	writeSlot(t, filepath.Join(nativeArtifactsDir, "ios"))
	// NO stubSwiftgenSources: the real locator runs and finds no checkout here.

	genDir := filepath.Join("Palbase", "Generated")
	require.NoError(t, os.MkdirAll(genDir, 0o755))
	client := filepath.Join(genDir, "PalbaseGenerated.swift")
	require.NoError(t, os.WriteFile(client, []byte("// the app's committed client\n"), 0o644))

	cmd := newSpecCmd(r)
	cmd.SetOut(io.Discard)
	cmd.SetArgs(nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()

	require.ErrorContains(t, err, "not resolved")
	require.ErrorContains(t, err, "nothing was written")
	// The contract was never fetched...
	require.NoFileExists(t, filepath.Join(nativeArtifactsDir, "openapi.json"))
	// ...and the committed client is exactly as it was.
	got, readErr := os.ReadFile(client)
	require.NoError(t, readErr)
	require.Equal(t, "// the app's committed client\n", string(got))
}
