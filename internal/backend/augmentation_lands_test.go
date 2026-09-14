package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE GATE MEASURES THAT THE AUGMENTATION LANDS, NOT THAT THE FILE EXISTS (FR-006).
//
// `palbase/palbase-env.d.ts` on disk proves nothing: without its trailing
// `export {};` it is an ambient script that REPLACES @palbase/backend's modules
// (FR-005), and with a drifted module name it augments nothing. Both leave a
// green build behind. verifyAugmentationLands compiles a probe through the
// project's own TypeScript and tsconfig — these tests run it against the SDK
// source beside this checkout, with the file rendered by that SDK's own
// makeEnvDts, and break the file the two ways that matter.

// augmentationProject stands up a project whose node_modules resolve to the SDK
// beside this checkout and writes the file its makeEnvDts renders.
func augmentationProject(t *testing.T) (dir string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		requireToolOnCI(t, "node", err)
		t.Skip("node is not on PATH")
	}
	sdk := sdkSourceDir(t)
	if _, err := os.Stat(filepath.Join(sdk, "dist", "index.js")); err != nil {
		t.Skipf("the SDK beside this checkout is not built (%v) — the probe resolves its published types", err)
	}
	dir = t.TempDir()
	link := func(from, to string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(to), 0o755))
		require.NoError(t, os.Symlink(from, to))
	}
	link(sdk, filepath.Join(dir, "node_modules", "@palbase", "backend"))
	for _, dep := range []string{"typescript", "bun-types", filepath.Join("@types", "node")} {
		link(filepath.Join(sdk, "node_modules", dep), filepath.Join(dir, "node_modules", dep))
	}
	tsconfig, err := os.ReadFile(filepath.Join(sdk, "template", "tsconfig.json"))
	require.NoError(t, err)
	mustWrite(t, dir, "tsconfig.json", string(tsconfig))

	render := exec.Command("node", "-e",
		`process.stdout.write(require("@palbase/backend").makeEnvDts([], {secrets:["STRIPE_KEY"],flags:[],buckets:[],roles:["admin"]}))`)
	render.Dir = dir
	body, err := render.Output()
	require.NoError(t, err)
	require.Contains(t, string(body), `declare module "@palbase/backend/stack"`, "the SDK beside this checkout predates the single-file renderer")
	mustWrite(t, dir, EnvTypesPath(), string(body))
	return dir
}

var augmentedNames = StackNames{Secrets: []string{"STRIPE_KEY"}, Roles: []string{"admin"}}

func TestAugmentationLandsForTheRenderedFile(t *testing.T) {
	dir := augmentationProject(t)
	require.NoError(t, verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), augmentedNames))
}

// MUTATION (a): the file loses `export {};` and becomes an ambient script.
func TestAugmentationDoesNotLandWithoutExportBraces(t *testing.T) {
	dir := augmentationProject(t)
	path := filepath.Join(dir, filepath.FromSlash(EnvTypesPath()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "export {};")
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw), "export {};", "", 1)), 0o644))

	err = verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), augmentedNames)
	require.ErrorContains(t, err, "does not apply")
}

// MUTATION (b): the module specifier drifts.
func TestAugmentationDoesNotLandOnAMisnamedModule(t *testing.T) {
	dir := augmentationProject(t)
	path := filepath.Join(dir, filepath.FromSlash(EnvTypesPath()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	broken := strings.Replace(string(raw), `"@palbase/backend/stack"`, `"@palbase/backend/stak"`, 1)
	require.NotEqual(t, string(raw), broken)
	require.NoError(t, os.WriteFile(path, []byte(broken), 0o644))

	err = verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), augmentedNames)
	require.ErrorContains(t, err, "does not apply")
}

// A GATE THAT CANNOT MEASURE DOES NOT SAY GREEN.
func TestAugmentationProbeRefusesWithoutTypeScript(t *testing.T) {
	dir := augmentationProject(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "node_modules", "typescript")))

	err := verifyAugmentationLands(context.Background(), dir, filepath.Join(dir, "node_modules"), augmentedNames)
	require.ErrorContains(t, err, "typescript is not installed")
}

// NOTHING GENERATED, NOTHING TO MEASURE.
func TestAugmentationProbeSkipsAProjectWithNoGeneratedFile(t *testing.T) {
	require.NoError(t, verifyAugmentationLands(context.Background(), t.TempDir(), "", StackNames{}))
}
