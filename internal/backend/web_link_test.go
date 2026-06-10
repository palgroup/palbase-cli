package backend

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/stretchr/testify/require"
)

// stubCodegen replaces webLinkCodegen for tests — writes a sentinel file so we
// can verify the seam was called without a real network.
func stubCodegenFunc(sentinelContent string) func(context.Context, Resolvers, string, string, io.Writer) error {
	return func(_ context.Context, _ Resolvers, _ string, outFile string, _ io.Writer) error {
		return os.WriteFile(outFile, []byte(sentinelContent), 0o644)
	}
}

// installStubCodegen replaces the package-level codegen var and restores it on
// test cleanup.
func installStubCodegen(t *testing.T, content string) {
	t.Helper()
	orig := webLinkCodegen
	webLinkCodegen = stubCodegenFunc(content)
	t.Cleanup(func() { webLinkCodegen = orig })
}

// webLinkCmd returns the `web link` subcommand wired with noop resolvers.
func webLinkCmd(t *testing.T) *webCmd {
	t.Helper()
	return &webCmd{r: noopResolvers()}
}

// ── package.json helpers ─────────────────────────────────────────────────────

// minimalPkgJSON returns the smallest valid package.json (no scripts section).
func minimalPkgJSON() string {
	return `{
  "name": "myapp",
  "version": "1.0.0"
}`
}

// pkgJSONWithScripts returns a package.json that already has a scripts section.
func pkgJSONWithScripts(scripts map[string]string) string {
	var sb strings.Builder
	sb.WriteString(`{
  "name": "myapp",
  "version": "1.0.0",
  "scripts": {`)
	first := true
	for k, v := range scripts {
		if !first {
			sb.WriteString(",")
		}
		sb.WriteString("\n    \"")
		sb.WriteString(k)
		sb.WriteString(`": "`)
		sb.WriteString(v)
		sb.WriteString(`"`)
		first = false
	}
	sb.WriteString("\n  },\n  \"dependencies\": {}\n}")
	return sb.String()
}

// writePkgJSON writes content to package.json in the current directory.
func writePkgJSON(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile("package.json", []byte(content), 0o644))
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestWebLink_NoPkgJSON: errors when package.json is absent.
func TestWebLink_NoPkgJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "ref1", DefaultEnv: "main"}))

	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"link", "--ref", "ref1"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "package.json not found")
}

// TestWebLink_HappyPath: link writes config, calls codegen, patches scripts,
// inserts import into the default entry file.
func TestWebLink_HappyPath(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// palbe gen sentinel")
	writePkgJSON(t, minimalPkgJSON())

	// Create a default entry file (app/layout.tsx).
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte(`import React from 'react';
export default function Layout() {}
`), 0o644))

	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"link", "--ref", "testref1"})
	require.NoError(t, cmd.Execute())

	// .palbase/config.json must be written.
	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "testref1", cfg.Ref)

	// gen file must exist (stubbed).
	genContent, err := os.ReadFile("palbe.gen.ts")
	require.NoError(t, err)
	require.Contains(t, string(genContent), "palbe gen sentinel")

	// scripts.predev + scripts.prebuild must be added.
	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody), `"predev": "palbase types --soft"`)
	require.Contains(t, string(pkgBody), `"prebuild": "palbase types --soft"`)

	// import must be inserted into app/layout.tsx.
	entryBody, err := os.ReadFile("app/layout.tsx")
	require.NoError(t, err)
	require.Contains(t, string(entryBody), `import '../palbe.gen'`)
}

// TestWebLink_EntryVariants: each auto-detected entry file is tried in order.
func TestWebLink_EntryVariants(t *testing.T) {
	entries := []struct {
		path string
		dir  string
	}{
		{"app/layout.tsx", "app"},
		{"src/app/layout.tsx", "src/app"},
		{"src/main.tsx", "src"},
		{"src/main.ts", "src"},
		{"main.tsx", "."},
	}

	for _, tc := range entries {
		t.Run(tc.path, func(t *testing.T) {
			t.Chdir(t.TempDir())
			installStubCodegen(t, "// gen")
			writePkgJSON(t, minimalPkgJSON())

			require.NoError(t, os.MkdirAll(tc.dir, 0o755))
			require.NoError(t, os.WriteFile(tc.path, []byte("// entry\n"), 0o644))

			cmd := newWebCmd(noopResolvers())
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"link", "--ref", "ref1"})
			require.NoError(t, cmd.Execute())

			body, err := os.ReadFile(tc.path)
			require.NoError(t, err)
			require.Contains(t, string(body), "palbe.gen", "import should reference gen file")
		})
	}
}

// TestWebLink_EntryFlagOverride: --entry overrides the auto-detection.
func TestWebLink_EntryFlagOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())

	// Create a custom entry.
	require.NoError(t, os.MkdirAll("src", 0o755))
	require.NoError(t, os.WriteFile("src/custom-entry.tsx", []byte("// custom\n"), 0o644))

	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"link", "--ref", "ref1", "--entry", "src/custom-entry.tsx"})
	require.NoError(t, cmd.Execute())

	body, err := os.ReadFile("src/custom-entry.tsx")
	require.NoError(t, err)
	require.Contains(t, string(body), "palbe.gen")
}

// TestWebLink_ConflictingScript: warns but does not clobber an existing
// script with a different value.
func TestWebLink_ConflictingScript(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	// predev already set to something else; prebuild absent.
	writePkgJSON(t, `{
  "name": "myapp",
  "scripts": {
    "predev": "my-custom-hook"
  }
}`)

	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"link", "--ref", "ref1"})
	require.NoError(t, cmd.Execute())

	// Warning must be printed.
	outStr := out.String()
	require.Contains(t, outStr, "predev")
	require.Contains(t, outStr, "palbase types --soft")

	// The existing script must NOT be clobbered.
	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody), `"predev": "my-custom-hook"`)
	// prebuild (absent) should be added.
	require.Contains(t, string(pkgBody), `"prebuild": "palbase types --soft"`)
}

// TestWebLink_KeyOrderPreserved: package.json key order is preserved, and keys
// after "scripts" are not moved.
func TestWebLink_KeyOrderPreserved(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")

	// Deliberately non-alphabetical ordering with a key after "scripts".
	original := `{
  "name": "myapp",
  "zebra": "last",
  "alpha": "first",
  "scripts": {
    "dev": "vite"
  },
  "after_scripts": "value"
}`
	writePkgJSON(t, original)

	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"link", "--ref", "ref1"})
	require.NoError(t, cmd.Execute())

	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	content := string(pkgBody)

	// top-level key order: name, zebra, alpha, scripts, after_scripts.
	nameIdx := strings.Index(content, `"name"`)
	zebraIdx := strings.Index(content, `"zebra"`)
	alphaIdx := strings.Index(content, `"alpha"`)
	scriptsIdx := strings.Index(content, `"scripts"`)
	afterIdx := strings.Index(content, `"after_scripts"`)
	require.True(t, nameIdx < zebraIdx, "name before zebra")
	require.True(t, zebraIdx < alphaIdx, "zebra before alpha")
	require.True(t, alphaIdx < scriptsIdx, "alpha before scripts")
	require.True(t, scriptsIdx < afterIdx, "scripts before after_scripts")

	// The new script keys must be present.
	require.Contains(t, content, `"predev": "palbase types --soft"`)
	require.Contains(t, content, `"prebuild": "palbase types --soft"`)

	// Original "dev" script must still be there.
	require.Contains(t, content, `"dev": "vite"`)

	// Unrelated content byte-identical: "zebra": "last" untouched.
	require.Contains(t, content, `"zebra": "last"`)
}

// TestWebLink_IdempotentRelink: running link a second time doesn't duplicate
// imports or scripts.
func TestWebLink_IdempotentRelink(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte(`import React from 'react';
`), 0o644))

	run := func() {
		cmd := newWebCmd(noopResolvers())
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"link", "--ref", "ref1"})
		require.NoError(t, cmd.Execute())
	}

	run()
	run() // second run

	// Only ONE import line referencing palbe.gen.
	entryBody, err := os.ReadFile("app/layout.tsx")
	require.NoError(t, err)
	count := strings.Count(string(entryBody), "palbe.gen")
	require.Equal(t, 1, count, "import must appear exactly once after idempotent re-link")

	// scripts appear exactly once.
	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(pkgBody), `"predev"`))
	require.Equal(t, 1, strings.Count(string(pkgBody), `"prebuild"`))
}

// TestWebLink_GitignoreWarning: prints a loud warning when .gitignore ignores
// the gen file, but does not modify .gitignore.
func TestWebLink_GitignoreWarning(t *testing.T) {
	for _, ignoreContent := range []string{
		"palbe.gen.ts\n",
		"*.gen.ts\n",
		"# auto-gen\npalbe.gen.ts\n",
	} {
		t.Run(ignoreContent[:min(len(ignoreContent), 20)], func(t *testing.T) {
			t.Chdir(t.TempDir())
			installStubCodegen(t, "// gen")
			writePkgJSON(t, minimalPkgJSON())
			require.NoError(t, os.WriteFile(".gitignore", []byte(ignoreContent), 0o644))

			cmd := newWebCmd(noopResolvers())
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"link", "--ref", "ref1"})
			require.NoError(t, cmd.Execute())

			outStr := out.String()
			require.Contains(t, outStr, "WARNING", "should print a loud warning about .gitignore")
			require.Contains(t, outStr, "palbe.gen.ts", "warning should mention the gen file")

			// .gitignore must not be modified.
			body, err := os.ReadFile(".gitignore")
			require.NoError(t, err)
			require.Equal(t, ignoreContent, string(body))
		})
	}
}

// TestWebLink_UnknownLayout: when no entry file is found, prints manual
// instruction and exits 0.
func TestWebLink_UnknownLayout(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	// No entry file created.

	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"link", "--ref", "ref1"})
	require.NoError(t, cmd.Execute(), "unknown layout must exit 0")

	outStr := out.String()
	require.Contains(t, outStr, "palbe.gen", "manual instruction should mention gen file")
}

// TestWebUnlink_RemovesConfig: `web unlink` removes .palbase/config.json and
// the .palbase/ dir (when empty), leaves gen file and scripts.
func TestWebUnlink_RemovesConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "ref1", DefaultEnv: "main"}))
	require.NoError(t, os.WriteFile("palbe.gen.ts", []byte("// gen"), 0o644))
	writePkgJSON(t, `{
  "name": "myapp",
  "scripts": {
    "predev": "palbase types --soft",
    "prebuild": "palbase types --soft"
  }
}`)

	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"unlink"})
	require.NoError(t, cmd.Execute())

	// config.json removed.
	_, err := os.Stat(filepath.Join(".palbase", "config.json"))
	require.True(t, os.IsNotExist(err), "config.json should be gone")

	// gen file untouched.
	_, err = os.Stat("palbe.gen.ts")
	require.NoError(t, err, "gen file should remain")

	// package.json untouched (scripts NOT removed by unlink).
	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody), `"predev"`)

	// Output mentions what was left.
	outStr := out.String()
	require.Contains(t, outStr, "palbe.gen.ts")
}

// TestWebUnlink_Idempotent: running unlink twice is a no-op on the second run.
func TestWebUnlink_Idempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	// No config.json at all.
	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"unlink"})
	require.NoError(t, cmd.Execute(), "unlink with no config must exit 0")
}

// TestWebLink_CustomOut: --out flag changes the gen file name.
func TestWebLink_CustomOut(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// custom gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))

	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"link", "--ref", "ref1", "--out", "my.custom.gen.ts"})
	require.NoError(t, cmd.Execute())

	_, err := os.Stat("my.custom.gen.ts")
	require.NoError(t, err, "custom out file should exist")

	// The import in the entry file should reference the custom out name.
	entryBody, err := os.ReadFile("app/layout.tsx")
	require.NoError(t, err)
	require.Contains(t, string(entryBody), "my.custom.gen")
}

