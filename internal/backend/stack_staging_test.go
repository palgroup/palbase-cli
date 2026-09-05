package backend

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControllerStagingStaysOutsideTheProject(t *testing.T) {
	requiresRealToolchain(t)
	useTestParserCache(t)
	dir := t.TempDir()
	mustWrite(t, dir, "modules/health.controller.ts", `
import { Controller, Get, z } from "@palbase/backend";
export const Health = z.object({ status: z.string() });
export type Health = z.infer<typeof Health>;
@Controller("/health")
export class HealthController {
  @Get("/")
  async check(): Promise<Health> { return { status: "ok" }; }
}
`)
	mustWrite(t, dir, "modules/value.ts", `export const value = "relative";`)
	mustWrite(t, dir, "probe.ts", `
import { dependency } from "fixture-dependency";
import { value } from "./modules/value";
console.log(dependency + ":" + value);
`)
	mustWrite(t, dir, "node_modules/fixture-dependency/package.json",
		`{"name":"fixture-dependency","main":"index.js"}`)
	mustWrite(t, dir, "node_modules/fixture-dependency/index.js",
		`exports.dependency = "installed";`)
	before, err := os.ReadDir(dir)
	require.NoError(t, err)
	sourcePath := filepath.Join(dir, "modules", "health.controller.ts")
	source, err := os.ReadFile(sourcePath)
	require.NoError(t, err)

	// Keep both builds alive: neither may replace the other's staged sources.
	first, err := stageControllers(context.Background(), dir, io.Discard)
	require.NoError(t, err)
	t.Cleanup(func() { removeTemp(first) })
	second, err := stageControllers(context.Background(), dir, io.Discard)
	require.NoError(t, err)
	t.Cleanup(func() { removeTemp(second) })
	require.NotEqual(t, first, second)
	for _, staged := range []string{first, second} {
		rel, err := filepath.Rel(dir, staged)
		require.NoError(t, err)
		require.False(t, filepath.IsLocal(rel), "staging must stay outside the checkout even while building")
		injected, err := os.ReadFile(filepath.Join(staged, "modules", "health.controller.ts"))
		require.NoError(t, err)
		require.Contains(t, string(injected), "palbase.backend.returnBuffer")
		bundle := filepath.Join(t.TempDir(), "probe.js")
		require.NoError(t, run(context.Background(), dir, "bun", "build",
			filepath.Join(staged, "probe.ts"), "--target=bun", "--outfile="+bundle))
		got, err := output(context.Background(), dir, "bun", bundle)
		require.NoError(t, err)
		require.Equal(t, "installed:relative\n", got)
	}
	after, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Equal(t, before, after, "no generated directory may appear while staging exists")
	unchanged, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	require.Equal(t, source, unchanged)
}

func TestControllerStagingCleansUpAfterARefusal(t *testing.T) {
	requiresRealToolchain(t)
	useTestParserCache(t)
	dir := t.TempDir()
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	mustWrite(t, dir, "broken.controller.ts", `
import { Controller, Get } from "@palbase/backend";
@Controller("/broken")
export class BrokenController {
  @Get("/")
  async check() { return { status: "missing return type" }; }
}
`)
	staged, err := stageControllers(context.Background(), dir, io.Discard)
	require.Error(t, err)
	require.Empty(t, staged)
	left, err := os.ReadDir(scratch)
	require.NoError(t, err)
	require.Empty(t, left, "a refused stage must remove its temporary sources and tools")
	require.NoDirExists(t, filepath.Join(dir, deployStagingDir))
}

func TestModuleDiscoveryContinuesPastLinkedDependencies(t *testing.T) {
	dir := t.TempDir()
	dependencies := t.TempDir()
	mustWrite(t, dependencies, "vendor.module.ts", "export class VendorModule {}")
	mustWrite(t, dir, "z.module.ts", "export class ProjectModule {}")
	require.NoError(t, os.Symlink(dependencies, filepath.Join(dir, "node_modules")))
	found, err := moduleSources(dir)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(dir, "z.module.ts")}, found)
}

func TestStackBundleDoesNotDependOnTemporaryDirectory(t *testing.T) {
	requiresRealToolchain(t)
	dir := t.TempDir()
	buildableBackend(t, dir)
	t.Cleanup(func() { removeBundleOutput(dir) })
	var previous []byte
	for i := 0; i < 2; i++ {
		_, _, err := buildStackArtifact(context.Background(), dir, io.Discard)
		require.NoError(t, err)
		bundle, err := os.ReadFile(filepath.Join(dir, ".palbase", "esm", "controllers", "controllers.js"))
		require.NoError(t, err)
		if i > 0 {
			require.True(t, string(previous) == string(bundle), "temporary directory names must not change the deployment artifact")
		}
		previous = bundle
	}
}
