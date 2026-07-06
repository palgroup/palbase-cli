package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// backendPkg is the SDK a controller imports; its major must match the deploy
// runtime's vendored major or a deploy of this tree fails at extraction with a
// cryptic decorator error (the centauri class). `palbase build` catches that
// skew locally, before the push.
const backendPkg = "@palbase/backend"

// npmRegistryBase is the npm registry root the Layer-A skew check queries. A
// package var (not a const) only so tests can point it at an httptest server;
// production never changes it.
var npmRegistryBase = "https://registry.npmjs.org"

// newBuildCmd wires `palbase build` — the local pre-deploy validator. It runs
// the SAME stage + bundle + extract_meta.js the deploy runs (via dev-server.js
// PALBASE_CHECK=1), plus a fast npm-registry major skew check, so a broken push
// (e.g. a `@Query("field")` string where a zod schema is required) is caught
// before it produces a FAILED deploy. Non-interactive, no Studio auth, one
// best-effort network call (npm GET). Exit 0 = PASSED (or environment couldn't
// run it — warned); exit 1 = user-code validation error.
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
// install failed, registry unreachable) warn and return nil (fail-open — the
// server gate is the authoritative backstop).
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
	// Read the installed major BEFORE ensureDevServerTools: its `npm install
	// --no-save` can prune a hand-placed @palbase/backend that isn't in
	// package.json (test fixtures), and in general the version we want is the
	// one already on disk, not whatever a tool install leaves behind.
	installed := installedBackendVersion(cwd)

	// zod-to-json-schema powers the header-rule + schema lowering in the
	// extractor; without it the header checks degrade (best-effort install).
	ensureDevServerTools(cwd)

	// Layer A skew: local installed major vs npm latest major. A user can only
	// install a major that npm published, so "local major < npm latest major"
	// catches every real stale-user case (centauri included). Registry
	// unreachable → warn + continue (Layer B server gate closes the window).
	if installed != "" {
		latest, lerr := npmLatestMajor(ctx, backendPkg)
		if lerr != nil {
			fmt.Fprintf(out, "warning: could not verify %s against the registry (%v) — result may not match the deploy runtime\n", backendPkg, lerr)
		} else if im := majorOf(installed); im > 0 && im != latest {
			fmt.Fprintf(out, "✗ %s %s is a major behind the latest %d.x — deploys run the latest major and will reject this tree\n", backendPkg, installed, latest)
			fmt.Fprintf(out, "  fix: npm install %s@^%d  (then update your code for the new major)\n", backendPkg, latest)
			return fmt.Errorf("build failed: %s major skew (installed %s, latest %d)", backendPkg, installed, latest)
		} else {
			fmt.Fprintf(out, "✓ %s %s (matches latest major %d)\n", backendPkg, installed, latest)
		}
	}

	// Run the deploy-identical validation via dev-server.js in check mode.
	tmpDir, err := os.MkdirTemp("", serveTempPrefix+"*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	if err := extractFS(devServerFS, "devjs", tmpDir); err != nil {
		return fmt.Errorf("extract dev server: %w", err)
	}

	node := exec.CommandContext(ctx, "node", filepath.Join(tmpDir, "dev-server.js"))
	node.Dir = cwd
	node.Env = append(os.Environ(),
		"PALBASE_CHECK=1",
		fmt.Sprintf("PALBASE_DEV_ROOT=%s", cwd),
		fmt.Sprintf("NODE_PATH=%s", filepath.Join(cwd, "node_modules")),
	)
	node.Stdout = out
	node.Stderr = out
	if err := node.Run(); err != nil {
		// A non-zero exit from check mode = a user-code validation failure
		// (dev-server.js already printed the per-controller reasons). Anything
		// else (couldn't spawn node) is environment — warn + fail-open.
		if _, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("build failed")
		}
		fmt.Fprintf(out, "warning: could not run local validation (%v) — the deploy will still gate it\n", err)
		return nil
	}
	return nil
}

// warnBackendSkew prints a warn-only Layer A skew notice for `palbase serve`
// (which runs the local SDK on purpose). Silent when the version can't be read
// or the registry is unreachable — a warning must never block or noise up dev.
func warnBackendSkew(ctx context.Context, cwd string, w io.Writer) {
	installed := installedBackendVersion(cwd)
	if installed == "" {
		return
	}
	latest, err := npmLatestMajor(ctx, backendPkg)
	if err != nil {
		return
	}
	if im := majorOf(installed); im > 0 && im != latest {
		fmt.Fprintf(w, "warning: local %s %s is a major behind the latest %d.x — deploys will fail; run: npm install %s@^%d\n",
			backendPkg, installed, latest, backendPkg, latest)
	}
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

// npmLatestMajor GETs the `latest` dist-tag major of pkg from the public npm
// registry — the user's install ceiling. 2s timeout, no auth, no wake. Returns
// an error the caller downgrades to a non-fatal warning (registry down must not
// fail a build).
func npmLatestMajor(ctx context.Context, pkg string) (int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	url := strings.TrimRight(npmRegistryBase, "/") + "/" + pkg + "/latest"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("registry returned %d", resp.StatusCode)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	m := majorOf(body.Version)
	if m == 0 {
		return 0, fmt.Errorf("registry returned an unparseable version %q", body.Version)
	}
	return m, nil
}
