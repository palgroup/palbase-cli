//go:build e2e

package e2e

// web_layout_test.go — THE WEB CLIENT IS PROVED THROUGH THE PRODUCT, NOT
// THROUGH A FUNCTION CALL.
//
// Everything below runs the real `palbase` binary against a real project, then
// bundles the result with the real bundler and reads the bytes. A test that
// called `wireWebProject` directly would prove that one function works; it
// would not prove that `palbase link` reaches it, that the published
// `palbe-gen` accepts what the CLI writes, or that an application ends up with
// exactly one environment's code in its bundle.
//
//	export PALBASE_E2E_PROJECT=<ref>          # a project this machine may link
//	go test -tags e2e -run TestWebLayout ./tests/e2e/
//
// The `local` environment comes from a stack this test brings up itself, so the
// checkout really does carry two — which is the whole point: selecting one must
// leave the other's code out.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// palbaseBinary builds the CLI from THIS checkout. An installed binary can
// carry a command the product no longer has, so the proof is run with the tool
// built from the source under test.
func palbaseBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "palbase")
	build := exec.Command("go", "build", "-o", bin, "./cmd/palbase")
	build.Dir = repoRoot(t)
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build palbase: %s", out)
	return bin
}

// repoRoot walks up from the test's package to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "no go.mod above %s", dir)
		dir = parent
	}
}

// run executes a command in dir and fails the test with its whole output.
func run(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "%s %s\n%s", name, strings.Join(args, " "), out)
	return string(out)
}

func TestWebLayoutSelectsOneEnvironment(t *testing.T) {
	ref := strings.TrimSpace(os.Getenv("PALBASE_E2E_PROJECT"))
	if ref == "" {
		t.Fatal("PALBASE_E2E_PROJECT is required: this gate measures a real link against a real project")
	}
	bin := palbaseBinary(t)

	// THE CHECKOUT'S DIRECTORY NAME IS ITS GROUP (D-009). `groupOf` falls back
	// to the base name when a target carries no project field, and
	// `LookupLocalStack` reads the machine-wide registry by that group — so an
	// app checkout only sees the local stack when it shares the name of the
	// checkout that started one. The gate accepts the product's rule rather
	// than pretending otherwise.
	group := strings.TrimSpace(os.Getenv("PALBASE_E2E_LOCAL_GROUP"))
	if group == "" {
		t.Fatal("PALBASE_E2E_LOCAL_GROUP is required: name the checkout whose `palbase start` " +
			"registered the local stack — the app checkout must share it to carry a `local` environment")
	}
	dir := filepath.Join(t.TempDir(), group)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	// A web checkout, as a person's looks.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"e2e-web","private":true}`+"\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.ts"),
		[]byte("import '../palbase/client';\n"), 0o644))

	// THE REAL LINK, through the binary.
	run(t, dir, nil, bin, "link", ref, "--platform", "web")

	// THE CONTRACT IS COMMITTED ONCE PER ENVIRONMENT, never twice (NFR-002).
	var specs []string
	require.NoError(t, filepath.Walk(filepath.Join(dir, "palbase"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Base(p) == "openapi.json" {
			specs = append(specs, p)
		}
		return nil
	}))
	require.NotEmpty(t, specs, "the link wrote no contract")
	seen := map[string]bool{}
	for _, p := range specs {
		env := filepath.Base(filepath.Dir(p))
		require.False(t, seen[env], "%s has two contracts: %v", env, specs)
		seen[env] = true
	}

	// AND THE BUNDLE CARRIES THE SELECTED ENVIRONMENT'S CODE, ONLY.
	//
	// Measured in BOTH directions: a one-way measurement cannot tell a working
	// selector from a constant.
	envs := make([]string, 0, len(seen))
	for env := range seen {
		envs = append(envs, env)
	}
	// TWO, NOT ONE. A gate that measures a single environment cannot tell a
	// working selector from a constant — it would pass against a build that
	// always picks the same one. No skip here: an unmet precondition is a
	// failing gate, not a quiet green.
	require.GreaterOrEqual(t, len(envs), 2,
		"this checkout carries one environment (%v); bring a local stack up (`palbase start` "+
			"in a backend checkout) so the link records `local` too", envs)
	for _, selected := range envs {
		// THE REAL FLOW. Switching environments is what `predev`/`prebuild` do:
		// the generator re-runs under the new `PALBASE_ENV` and rewrites the
		// barrel. Bundling without that step would measure the link's choice
		// forever and call it a selector.
		run(t, dir, []string{"PALBASE_ENV=" + selected},
			filepath.Join("node_modules", ".bin", "palbe-gen"))

		out := filepath.Join(dir, "bundle-"+selected+".js")
		run(t, dir, []string{"PALBASE_ENV=" + selected}, "npx", "esbuild",
			"src/main.ts", "--bundle", "--outfile="+out)
		body, err := os.ReadFile(out)
		require.NoError(t, err)
		marker := readMarker(t, filepath.Join(dir, "palbase", "environments", selected, "web-config.json"))
		require.Contains(t, string(body), marker,
			"the bundle for %s does not carry its own base_url", selected)
		for _, other := range envs {
			if other == selected {
				continue
			}
			require.NotContains(t, string(body), readMarker(t, filepath.Join(dir, "palbase", "environments", other, "web-config.json")),
				"the bundle for %s carries %s's address", selected, other)
		}
	}
}

// readMarker returns the base_url an environment is configured with — the one
// value that differs between environments and survives bundling.
func readMarker(t *testing.T, configPath string) string {
	t.Helper()
	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var cfg struct {
		BaseURL string `json:"base_url"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.NotEmpty(t, cfg.BaseURL, "%s carries no base_url", configPath)
	return cfg.BaseURL
}
