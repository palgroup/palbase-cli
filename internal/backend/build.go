package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// backendPkg is the SDK a controller imports. Its major must be one the deploy
// runtime VENDORS — not necessarily the newest: the runtime keeps every major
// inside a 12-month window and builds each tenant against the one their lockfile
// resolved. Which majors those are is server-side knowledge, so the
// authoritative check is deploy.CheckSDKMajor; this command validates the code,
// not the version.
const backendPkg = "@palbase/backend"

// buildTempPrefix is the os.MkdirTemp prefix for the extracted build-check
// runner. `palbase build` is one-shot and removes its own dir on return, so
// nothing sweeps this prefix.
const buildTempPrefix = "palbase-build-"

// npmRegistryBase is the npm registry root the Layer-A skew check queries. A
// package var (not a const) only so tests can point it at an httptest server;
// production never changes it.
var npmRegistryBase = "https://registry.npmjs.org"

// newBuildCmd wires `palbase build` — the local pre-deploy validator. It runs
// the SAME stage + bundle + extract_meta.js the deploy runs (via build-check.js),
// so a broken push (e.g. a `@Query("field")` string where a zod schema is
// required) is caught before it produces a FAILED deploy. Non-interactive, no
// Studio auth, NO network call. Exit 0 = PASSED (or environment couldn't run it —
// warned); exit 1 = user-code validation error.
func newBuildCmd(_ Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Validate the backend locally the way a deploy would (catches broken pushes before they ship)",
		Long: `Run the same validation the deploy runs — stage, bundle, and extract
controller metadata — against your local tree, so a push that would produce a
FAILED deploy is caught here first. Exits non-zero on a user-code error
(bad decorator, return-type, version skew); exits 0 when it passes or when the
local environment can't run it (a warning is printed and the server still
gates the real deploy).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return runBuild(cmd.Context(), cwd, cmd.OutOrStdout())
		},
	}
}

// runBuild is the `palbase build` body, factored out so `palbase push`
// (platform arm) can gate on it inline. Returns an error ONLY for a user-code
// validation failure (exit 1); environment problems (no controllers, npm
// install failed) warn and return nil (fail-open — the server gate is the
// authoritative backstop).
func runBuild(ctx context.Context, cwd string, out io.Writer) error {
	controllersDir := filepath.Join(cwd, "controllers")
	if _, err := os.Stat(controllersDir); err != nil {
		fmt.Fprintln(out, "no controllers/ — nothing to validate")
		return nil
	}

	// Controllers import @palbase/backend; the bundler keeps it external and the
	// extractor require()s it from node_modules. Install once when absent so a
	// fresh clone can be validated in one step. Install failure = environment
	// problem, not a user-code error → warn + continue (server gate backstops).
	if backendDepMissing(cwd) {
		if err := installNodeDeps(cwd); err != nil {
			fmt.Fprintf(out, "warning: %s is not installed and `npm install` failed (%v) — cannot validate locally; the deploy will still gate it\n", backendPkg, err)
			return nil
		}
	}
	// Read the installed major BEFORE ensureBuildCheckTools: its `npm install
	// --no-save` can prune a hand-placed @palbase/backend that isn't in
	// package.json (test fixtures), and in general the version we want is the
	// one already on disk, not whatever a tool install leaves behind.
	installed := installedBackendVersion(cwd)

	// zod-to-json-schema powers the header-rule + schema lowering in the
	// extractor; without it the header checks degrade (best-effort install).
	ensureBuildCheckTools(cwd)

	// The installed SDK version, reported but NOT gated here.
	//
	// This used to fail the build when the installed major differed from npm's
	// `latest`, on the premise that "deploys run the latest major and will reject
	// this tree". That premise stopped being true on 2026-08-04: the runtime now
	// vendors every SDK major inside a 12-month window and builds each tenant
	// against the one their lockfile resolved. A ^12 project deploys perfectly
	// well against a runtime whose newest major is 13 — verified live, serving
	// traffic, with the artifact manifest recording sdkVersion 12.0.1.
	//
	// Keeping the check would have been worse than removing it: it turned a
	// PASSING deploy into a FAILING local build, and because `palbase push`
	// installs this as a pre-push hook, it blocked the push before the platform
	// ever saw it. A gate that answers a question the server no longer asks is not
	// a safety net; it is a wrong answer delivered with confidence.
	//
	// The authoritative check remains server-side (deploy.CheckSDKMajor), which
	// knows the actual vendored set — something this process cannot know without
	// asking the platform. Surfacing that set locally is worth doing and is
	// tracked separately; printing the installed version is the honest subset of
	// it available here.
	if installed != "" {
		fmt.Fprintf(out, "✓ %s %s\n", backendPkg, installed)
	}

	// Run the deploy-identical validation via build-check.js.
	tmpDir, err := os.MkdirTemp("", buildTempPrefix+"*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if err := extractFS(buildCheckFS, "devjs", tmpDir); err != nil {
		return fmt.Errorf("extract the build checker: %w", err)
	}

	// Validate the tree the DEPLOY receives, not the one on disk. BuildTarball is
	// the same walk `palbase push` ships, and it strips node_modules — so bundling
	// the working directory resolved bare third-party imports (`import { z } from
	// "zod"`) that the deploy then failed on with `Could not resolve "zod"`, after
	// this command had already printed "build OK". Staging closes that gap at the
	// source: same bytes in, same bundler behaviour out. Staging failure is an
	// environment problem, not user code → warn + fall back to the live tree.
	buildRoot := cwd
	if staged, serr := stageDeployTree(cwd); serr != nil {
		fmt.Fprintf(out, "warning: could not stage the deploy tree (%v) — validating the working directory instead; a bare third-party import may still pass here and fail the deploy\n", serr)
	} else {
		defer os.RemoveAll(staged)
		buildRoot = staged
	}

	node := exec.CommandContext(ctx, "node", filepath.Join(tmpDir, "build-check.js"))
	node.Dir = buildRoot
	node.Env = append(os.Environ(),
		fmt.Sprintf("PALBASE_DEV_ROOT=%s", buildRoot),
		// The CLI's pinned TypeScript parser first, then the project's deps —
		// the user's typescript may be 7.x (no compiler API) or absent.
		fmt.Sprintf("NODE_PATH=%s", devNodePath(cwd, out)),
		// The staged tree has no node_modules (that is the point). The metadata
		// extractor still has to require() the real @palbase/backend, exactly as
		// the pod's global install provides it — point it at the project's copy.
		fmt.Sprintf("PALBASE_RUNTIME_MODULES=%s", filepath.Join(cwd, "node_modules")),
	)
	// db/schema.ts is deploy-fatal too and check mode never looked at it: a schema
	// module that doesn't export a defineSchema() result passed `palbase build`
	// and then failed the deploy with `schema module does not export a
	// defineSchema() result with .tables`. generateEnvTypes runs the SAME bundle +
	// bridge the deploy's extractor does (and is a clean no-op when there is no
	// db/schema.ts), so reuse it rather than restating the rule. It writes into the
	// staging tree, which is discarded — build validates, it does not mutate.
	if err := generateEnvTypes(ctx, buildRoot, filepath.Join(cwd, "node_modules")); err != nil {
		fmt.Fprintf(out, "✗ DEPLOY WOULD FAIL: db/schema.ts — %v\n", err)
		return fmt.Errorf("build failed")
	}

	node.Stdout = out
	node.Stderr = out
	if err := node.Run(); err != nil {
		// A non-zero exit = a user-code validation failure (build-check.js
		// already printed the per-controller reasons). Anything
		// else (couldn't spawn node) is environment — warn + fail-open.
		if _, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("build failed")
		}
		fmt.Fprintf(out, "warning: could not run local validation (%v) — the deploy will still gate it\n", err)
		return nil
	}
	return nil
}

// stageDeployTree materialises, in a temp dir, EXACTLY the source tree a deploy
// receives: the `palbase push` tarball, unpacked. It reuses BuildTarball rather
// than re-implementing its ignore rules, so the two can never drift — if push
// starts shipping (or stripping) a path, the local build follows automatically.
//
// The load-bearing effect is what is ABSENT: node_modules. esbuild resolves bare
// imports by walking up from the entry file, so bundling the working directory
// silently satisfied `import { z } from "zod"` from the project's installed deps,
// while the deploy — which bundles from this tarball and never runs npm install —
// could not. Caller removes the returned dir.
func stageDeployTree(cwd string) (string, error) {
	tarball, err := BuildTarball(cwd)
	if err != nil {
		return "", fmt.Errorf("pack the project: %w", err)
	}
	dir, err := os.MkdirTemp("", "palbase-build-stage-*")
	if err != nil {
		return "", err
	}
	if err := extractTarGz(dir, bytes.NewReader(tarball)); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("unpack the project: %w", err)
	}
	return dir, nil
}

// installedBackendVersion reads node_modules/@palbase/backend/package.json's
// version. "" when absent/unreadable (the caller then skips the skew check).
func installedBackendVersion(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "node_modules", "@palbase", "backend", "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// majorOf returns the leading integer of a semver string ("9.0.1" → 9). 0 when
// unparseable (the caller treats 0 as "can't compare" and skips the gate).
func majorOf(version string) int {
	v := strings.TrimLeft(strings.TrimSpace(version), "^~>=<v ")
	dot := strings.IndexByte(v, '.')
	if dot > 0 {
		v = v[:dot]
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

