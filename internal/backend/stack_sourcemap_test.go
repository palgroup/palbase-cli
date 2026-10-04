package backend

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The controller of palgroup/palbase#27, cut down: line 6 reads `user.id` off a
// null the route let in.
const areasController = `type User = { id: string };

export class AreasController {
  joinWaitlist(email: string, user: User | null): string {
    const who = user as User;
    return email + who.id;
  }
}
`

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
}

// bundleAsPushDoes stages the project the way `palbase push` does — a copy in a
// directory of its own — and bundles it into a fresh bundle root.
func bundleAsPushDoes(t *testing.T, project map[string]string) (projectDir, bundleRoot string) {
	t.Helper()
	projectDir = t.TempDir()
	writeTree(t, projectDir, project)
	staged := t.TempDir()
	writeTree(t, staged, project)
	entry := filepath.Join(staged, ".controllers-entry.ts")
	require.NoError(t, os.WriteFile(entry, []byte(
		`import { AreasController } from "./modules/areas/areas.controller.ts";
export const controller = new AreasController();
`), 0o644))
	bundleRoot = t.TempDir()
	out := filepath.Join(bundleRoot, ".palbase", "esm", "controllers", "controllers.js")
	require.NoError(t, os.MkdirAll(filepath.Dir(out), 0o755))
	require.NoError(t, bundleWithSourceMap(context.Background(), staged, entry, out, bundleRoot, projectDir))
	return projectDir, bundleRoot
}

// AN ERROR A DEPLOYED HANDLER THROWS NAMES THE PROJECT'S FILE AND LINE.
//
// palgroup/palbase#27: the push refused on a TypeError whose only frame was
// `.palbase/esm/controllers/controllers.js:22952:52`. The bundle now carries
// its source map, and this runs the artifact the way the stack does — the
// project's tree at its root, the bundle under `.palbase/esm`, Bun importing it
// — and reads the frame Bun itself builds.
func TestADeployedErrorPointsAtTheProjectsOwnLine(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is what bundles the controllers")
	}
	project := map[string]string{"modules/areas/areas.controller.ts": areasController}
	_, bundleRoot := bundleAsPushDoes(t, project)

	artifact := t.TempDir()
	writeTree(t, artifact, project)
	for _, name := range []string{"controllers.js", "controllers.js.map"} {
		raw, err := os.ReadFile(filepath.Join(bundleRoot, ".palbase", "esm", "controllers", name))
		require.NoError(t, err, "%s was not written where the push collects it", name)
		writeTree(t, artifact, map[string]string{".palbase/esm/controllers/" + name: string(raw)})
	}

	script := `const m = await import(process.argv[1]);
try { m.controller.joinWaitlist("a@b.c", null); } catch (e) { console.log(e.stack); }`
	cmd := exec.Command("bun", "-e", script, filepath.Join(artifact, ".palbase", "esm", "controllers", "controllers.js"))
	stack, err := cmd.CombinedOutput()
	require.NoError(t, err, string(stack))

	real, err := filepath.EvalSymlinks(artifact)
	require.NoError(t, err)
	assert.Contains(t, string(stack), filepath.Join(real, "modules", "areas", "areas.controller.ts")+":6:",
		"the frame does not point at the project's file and line:\n%s", stack)
	assert.NotContains(t, string(stack), "controllers.js:", "a frame still points into the bundle:\n%s", stack)
}

// THE MAP NAMES THE ARTIFACT'S FILES, NOT THE MACHINE THAT BUILT IT.
//
// Bun writes each source relative to the bundle, and the bundle is built from a
// staged copy in a temp directory: left alone, every build would name a
// different directory, and the artifact's digest would change with nothing
// changed. Two builds from two stages must produce the same bytes.
func TestTheSourceMapIsTheSameFromAnyStage(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is what bundles the controllers")
	}
	project := map[string]string{"modules/areas/areas.controller.ts": areasController}
	_, first := bundleAsPushDoes(t, project)
	_, second := bundleAsPushDoes(t, project)

	read := func(root, name string) string {
		raw, err := os.ReadFile(filepath.Join(root, ".palbase", "esm", "controllers", name))
		require.NoError(t, err)
		return string(raw)
	}
	assert.Equal(t, read(first, "controllers.js"), read(second, "controllers.js"))
	assert.Equal(t, read(first, "controllers.js.map"), read(second, "controllers.js.map"))

	var sm struct {
		Sources        []string  `json:"sources"`
		SourcesContent []*string `json:"sourcesContent"`
	}
	require.NoError(t, json.Unmarshal([]byte(read(first, "controllers.js.map")), &sm))
	assert.Contains(t, sm.Sources, "../../../modules/areas/areas.controller.ts")
	for _, s := range sm.Sources {
		assert.False(t, strings.Contains(s, os.TempDir()) || strings.Contains(s, "/var/folders/"),
			"a source names the build machine's temp directory: %q", s)
	}
	// Present, so Bun accepts the map, and null, so it carries no copy of the
	// files the artifact already carries.
	require.Len(t, sm.SourcesContent, len(sm.Sources))
	for _, c := range sm.SourcesContent {
		assert.Nil(t, c, "the map carries a copy of a file the artifact already carries")
	}
}

// THE STAGED COPY BEGINS WITH THE AUTHOR'S FILE, BYTE FOR BYTE.
//
// The bundle's map is built from the staged copy and then labelled as the
// author's file, so anything the staging adds ABOVE the author's first line
// moves every mapped frame down. Measured (review of palgroup/palbase#27): a
// controller that does not import `z` got `import { z } …` prepended, and its
// line 8 was reported as line 9. Driven through the real `stageControllers`.
func TestStagingKeepsTheAuthorsLinesWhereTheyWere(t *testing.T) {
	requiresRealToolchain(t)
	dir := t.TempDir()
	ctxPack, cancelPack := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelPack()
	sdk := packLocalSDK(t, ctxPack)
	if !npmInstallProject(t, dir, sdk, "typescript@^5", "zod-to-json-schema") {
		t.Skip("node/npm unavailable or the install failed")
	}
	// The scaffold's shape: the schema lives in `dto/`, so the controller itself
	// imports no `z` — and the staging writes a binding that needs one.
	source := `import { Controller, Get } from "@palbase/backend";

import { Health } from "./dto/health";

@Controller("/health", { auth: false })
export class HealthController {
  @Get("")
  check(): Health {
    return { status: "ok" };
  }
}
`
	writeTree(t, dir, map[string]string{
		"modules/health/health.controller.ts": source,
		"modules/health/dto/health.ts": `import { z } from "@palbase/backend";

export const Health = z.object({ status: z.string() });
export type Health = z.infer<typeof Health>;
`,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var out strings.Builder
	staged, err := stageControllers(ctx, dir, &out)
	require.NoError(t, err, out.String())
	defer func() { _ = os.RemoveAll(staged) }()

	raw, err := os.ReadFile(filepath.Join(staged, "modules", "health", "health.controller.ts"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(raw), source),
		"the staged controller does not begin with the author's file — every mapped line would move:\n%s", raw)
	require.Contains(t, string(raw)[len(source):], "import { z }",
		"the staging no longer adds the `z` import this controller needs — the measurement is empty")
}

// A SOURCE OUTSIDE THE PROJECT IS STILL WRITTEN RELATIVE TO IT.
//
// A monorepo hoists `node_modules` above the backend's folder, so the bundler
// reads files that are in neither the stage nor the project. Left alone, the
// map would carry `../../../../Users/<name>/…` — the machine's layout, and a
// digest that differs per machine.
func TestASourceOutsideTheProjectNamesNoMachinePath(t *testing.T) {
	repo := t.TempDir()
	project := filepath.Join(repo, "backend")
	staged := t.TempDir()
	bundleRoot := t.TempDir()
	writeTree(t, repo, map[string]string{"node_modules/zod/index.js": "export const z = 1;\n"})
	writeTree(t, staged, map[string]string{"modules/a.ts": "export const a = 1;\n"})
	require.NoError(t, os.MkdirAll(project, 0o755))
	mapDir := filepath.Join(bundleRoot, ".palbase", "esm", "controllers")
	require.NoError(t, os.MkdirAll(mapDir, 0o755))
	rel := func(target string) string {
		r, err := filepath.Rel(mapDir, target)
		require.NoError(t, err)
		return filepath.ToSlash(r)
	}
	raw, err := json.Marshal(map[string]any{
		"version":  3,
		"sources":  []string{rel(filepath.Join(staged, "modules", "a.ts")), rel(filepath.Join(repo, "node_modules", "zod", "index.js"))},
		"mappings": "",
		"names":    []string{},
	})
	require.NoError(t, err)
	mapPath := filepath.Join(mapDir, "controllers.js.map")
	require.NoError(t, os.WriteFile(mapPath, raw, 0o644))

	require.NoError(t, rewriteSourceMap(mapPath, bundleRoot, staged, project))

	var sm struct {
		Sources []string `json:"sources"`
	}
	body, err := os.ReadFile(mapPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &sm))
	require.Equal(t, []string{"../../../modules/a.ts", "../../../../node_modules/zod/index.js"}, sm.Sources)
}
