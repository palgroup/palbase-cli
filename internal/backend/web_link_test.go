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

// stubCodegenFunc replaces webLinkCodegen for tests — writes a sentinel file
// so we can verify the seam was called without a real network.
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

// minimalPkgJSON returns the smallest valid package.json (no scripts section).
func minimalPkgJSON() string {
	return `{
  "name": "myapp",
  "version": "1.0.0"
}`
}

// writePkgJSON writes content to package.json in the current directory.
func writePkgJSON(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile("package.json", []byte(content), 0o644))
}

// runWebLink executes `web link` with the given extra args and returns stdout.
func runWebLink(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newWebCmd(noopResolvers())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"link"}, args...))
	require.NoError(t, cmd.Execute())
	return out.String()
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestWebLink_HookLiteral pins the exact predev/prebuild value:
//   - --env remote: codegen always uses the deployed backend, never localhost.
//   - --soft: print a warning and exit 0 on any error (predev/prebuild must
//     never block the build if the CLI is absent or offline).
//   - || exit 0: covers command-not-found (exit 127) which --soft alone can't
//     swallow (--soft handles CLI errors, not missing-binary errors).
func TestWebLink_HookLiteral(t *testing.T) {
	require.Equal(t, "palbase web gen --env remote --soft || exit 0", webTypesCmd)
}

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

	runWebLink(t, "--ref", "testref1")

	// .palbase/config.json must be written.
	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "testref1", cfg.Ref)

	// gen file must exist (stubbed).
	genContent, err := os.ReadFile("palbe.gen.ts")
	require.NoError(t, err)
	require.Contains(t, string(genContent), "palbe gen sentinel")

	// scripts.predev + scripts.prebuild must be added with the exact hook value.
	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody), `"predev": "palbase web gen --env remote --soft || exit 0"`)
	require.Contains(t, string(pkgBody), `"prebuild": "palbase web gen --env remote --soft || exit 0"`)

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

			runWebLink(t, "--ref", "ref1")

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

	runWebLink(t, "--ref", "ref1", "--entry", "src/custom-entry.tsx")

	body, err := os.ReadFile("src/custom-entry.tsx")
	require.NoError(t, err)
	require.Contains(t, string(body), "palbe.gen")
}

// TestWebLink_UseClientDirective: a 'use client' directive must STAY the first
// statement — the generated import lands after the directive prologue.
func TestWebLink_UseClientDirective(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))

	input := `'use client';

export default function Layout() {}
`
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte(input), 0o644))

	runWebLink(t, "--ref", "ref1")

	body, err := os.ReadFile("app/layout.tsx")
	require.NoError(t, err)
	expected := `'use client';

import '../palbe.gen';
export default function Layout() {}
`
	require.Equal(t, expected, string(body))
	require.True(t, strings.HasPrefix(string(body), "'use client';"),
		"file must still START with the directive")
}

// TestWebLink_MultilineImport: a multiline `import { ... } from '...';` must
// never be spliced mid-statement — the generated import lands after the whole
// statement. Exact-file golden.
func TestWebLink_MultilineImport(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("src", 0o755))

	input := `import {
  useState,
} from 'react';

export default function App() {}
`
	require.NoError(t, os.WriteFile("src/main.tsx", []byte(input), 0o644))

	runWebLink(t, "--ref", "ref1")

	body, err := os.ReadFile("src/main.tsx")
	require.NoError(t, err)
	expected := `import {
  useState,
} from 'react';
import '../palbe.gen';

export default function App() {}
`
	require.Equal(t, expected, string(body))
}

// TestWebLink_ImportIdempotencyExactMatch (M1): the skip check is an exact
// module-specifier match — './palbe.gen.extra' must NOT suppress the insert,
// while './palbe.gen' / '../palbe.gen' must.
func TestWebLink_ImportIdempotencyExactMatch(t *testing.T) {
	t.Run("near-miss specifier still gets the import", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())
		require.NoError(t, os.MkdirAll("src", 0o755))
		require.NoError(t, os.WriteFile("src/main.tsx", []byte(`import './palbe.gen.extra';

export const x = 1;
`), 0o644))

		runWebLink(t, "--ref", "ref1")

		body, err := os.ReadFile("src/main.tsx")
		require.NoError(t, err)
		require.Contains(t, string(body), `import './palbe.gen.extra';`)
		require.Equal(t, 1, strings.Count(string(body), `'../palbe.gen'`),
			"the real gen import must be inserted exactly once")
	})

	t.Run("exact specifier suppresses the insert", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())
		require.NoError(t, os.MkdirAll("src", 0o755))
		input := `import '../palbe.gen';

export const x = 1;
`
		require.NoError(t, os.WriteFile("src/main.tsx", []byte(input), 0o644))

		runWebLink(t, "--ref", "ref1")

		body, err := os.ReadFile("src/main.tsx")
		require.NoError(t, err)
		require.Equal(t, input, string(body), "already-imported entry must be untouched")
	})
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

	outStr := runWebLink(t, "--ref", "ref1")

	// Warning must be printed with the suggested value.
	require.Contains(t, outStr, "predev")
	require.Contains(t, outStr, "palbase web gen --env remote --soft || exit 0")

	// The existing script must NOT be clobbered.
	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody), `"predev": "my-custom-hook"`)
	// prebuild (absent) should be added.
	require.Contains(t, string(pkgBody), `"prebuild": "palbase web gen --env remote --soft || exit 0"`)
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

	runWebLink(t, "--ref", "ref1")

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
	require.Contains(t, content, `"predev": "palbase web gen --env remote --soft || exit 0"`)
	require.Contains(t, content, `"prebuild": "palbase web gen --env remote --soft || exit 0"`)

	// Original "dev" script must still be there.
	require.Contains(t, content, `"dev": "vite"`)

	// Unrelated content byte-identical: "zebra": "last" untouched.
	require.Contains(t, content, `"zebra": "last"`)
}

// TestWebPatchPackageJSON_Golden: FULL-FILE golden compares for the
// byte-splice editor — everything outside the spliced range must be
// byte-identical, including nested non-alphabetical objects, `&&` values,
// and deliberately weird indentation.
func TestWebPatchPackageJSON_Golden(t *testing.T) {
	run := func(t *testing.T, input, expected string) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "package.json")
		require.NoError(t, os.WriteFile(path, []byte(input), 0o644))
		var warn strings.Builder
		require.NoError(t, patchPackageJSONScripts(path, &warn))
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, expected, string(body))
	}

	t.Run("nested exports + zzz-first + && — splice only", func(t *testing.T) {
		input := `{
  "name": "myapp",
  "exports": {
    "./z": "./dist/z.js",
    "./a": "./dist/a.js"
  },
  "scripts": {
    "zzz": "echo z",
    "build": "tsc && vite build"
  },
  "version": "1.0.0"
}
`
		expected := `{
  "name": "myapp",
  "exports": {
    "./z": "./dist/z.js",
    "./a": "./dist/a.js"
  },
  "scripts": {
    "zzz": "echo z",
    "build": "tsc && vite build",
    "predev": "palbase web gen --env remote --soft || exit 0",
    "prebuild": "palbase web gen --env remote --soft || exit 0"
  },
  "version": "1.0.0"
}
`
		run(t, input, expected)
	})

	t.Run("weird indentation preserved outside the splice", func(t *testing.T) {
		input := `{
      "name": "x",
  "scripts": {
        "dev":    "vite"
  },
   "odd":  true
}
`
		expected := `{
      "name": "x",
  "scripts": {
        "dev":    "vite",
        "predev": "palbase web gen --env remote --soft || exit 0",
        "prebuild": "palbase web gen --env remote --soft || exit 0"
  },
   "odd":  true
}
`
		run(t, input, expected)
	})

	t.Run("no scripts object — spliced before closing brace", func(t *testing.T) {
		input := `{
  "name": "myapp",
  "version": "1.0.0"
}
`
		expected := `{
  "name": "myapp",
  "version": "1.0.0",
  "scripts": {
    "predev": "palbase web gen --env remote --soft || exit 0",
    "prebuild": "palbase web gen --env remote --soft || exit 0"
  }
}
`
		run(t, input, expected)
	})

	t.Run("already correct — byte identical", func(t *testing.T) {
		input := `{
  "name": "myapp",
  "scripts": {
    "predev": "palbase web gen --env remote --soft || exit 0",
    "prebuild": "palbase web gen --env remote --soft || exit 0"
  }
}
`
		run(t, input, input)
	})
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

	runWebLink(t, "--ref", "ref1")
	runWebLink(t, "--ref", "ref1") // second run

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

// TestWebLink_RefRelinkUpdatesConfig (I3): `web link --ref B` in a cwd linked
// to A must update the config's Ref to B (keeping DefaultEnv — the active
// branch is a local choice) and regenerate via the seam with B.
func TestWebLink_RefRelinkUpdatesConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	var gotRef string
	orig := webLinkCodegen
	webLinkCodegen = func(_ context.Context, _ Resolvers, ref, outFile string, _ io.Writer) error {
		gotRef = ref
		return os.WriteFile(outFile, []byte("// gen"), 0o644)
	}
	t.Cleanup(func() { webLinkCodegen = orig })

	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "projA", DefaultEnv: "staging"}))

	outStr := runWebLink(t, "--ref", "projB")

	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "projB", cfg.Ref, "config must be re-linked to the new ref")
	require.Equal(t, "staging", cfg.DefaultEnv, "re-link must keep the active branch")
	require.Equal(t, "projB", gotRef, "codegen must run against the new ref")
	require.Contains(t, outStr, "projB")
}

// TestWebLink_EnsuresPalbaseGitignored (I4): link keeps the per-machine
// .palbase/ link dir out of git, like mobile link does.
func TestWebLink_EnsuresPalbaseGitignored(t *testing.T) {
	t.Run("creates .gitignore when absent", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())

		runWebLink(t, "--ref", "ref1")

		body, err := os.ReadFile(".gitignore")
		require.NoError(t, err)
		require.Contains(t, string(body), ".palbase/")
	})

	t.Run("appends to an existing .gitignore", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())
		require.NoError(t, os.WriteFile(".gitignore", []byte("node_modules/\n"), 0o644))

		runWebLink(t, "--ref", "ref1")

		body, err := os.ReadFile(".gitignore")
		require.NoError(t, err)
		require.Equal(t, "node_modules/\n.palbase/\n", string(body))
	})

	t.Run("does not duplicate on re-link", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())

		runWebLink(t, "--ref", "ref1")
		runWebLink(t, "--ref", "ref1")

		body, err := os.ReadFile(".gitignore")
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(body), ".palbase/"))
	})
}

// TestWebLink_GitignoreWarning: prints a loud warning when .gitignore ignores
// the gen file. The offending rule is reported, never edited (the only write
// is the appended .palbase/ link-dir entry).
func TestWebLink_GitignoreWarning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"exact filename", "palbe.gen.ts\n"},
		{"glob *.gen.ts", "*.gen.ts\n"},
		{"with comment", "# auto-gen\npalbe.gen.ts\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			installStubCodegen(t, "// gen")
			writePkgJSON(t, minimalPkgJSON())
			require.NoError(t, os.WriteFile(".gitignore", []byte(tc.content), 0o644))

			outStr := runWebLink(t, "--ref", "ref1")

			require.Contains(t, outStr, "WARNING", "should print a loud warning about .gitignore")
			require.Contains(t, outStr, "palbe.gen.ts", "warning should mention the gen file")

			// The offending rule must NOT be rewritten/removed; the only
			// change is the appended .palbase/ entry.
			body, err := os.ReadFile(".gitignore")
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(body), tc.content),
				"existing rules must stay byte-identical, got: %q", string(body))
			require.Contains(t, string(body), ".palbase/")
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

	outStr := runWebLink(t, "--ref", "ref1")
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
    "predev": "palbase web gen --env remote --soft || exit 0",
    "prebuild": "palbase web gen --env remote --soft || exit 0"
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

	// Output mentions what was left — generic wording (unlink has no --out
	// knowledge, so it must not hardcode palbe.gen.ts).
	outStr := out.String()
	require.Contains(t, outStr, "generated client file")
	require.NotContains(t, outStr, "palbe.gen.ts")
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

	runWebLink(t, "--ref", "ref1", "--out", "my.custom.gen.ts")

	_, err := os.Stat("my.custom.gen.ts")
	require.NoError(t, err, "custom out file should exist")

	// The import in the entry file should reference the custom out name.
	entryBody, err := os.ReadFile("app/layout.tsx")
	require.NoError(t, err)
	require.Contains(t, string(entryBody), "my.custom.gen")
}

// ── Bug-fix regression tests ──────────────────────────────────────────────────

// TestWebLink_CodegenEnvIsRemote (Bug-1): the production webLinkCodegen seam
// must use "remote" — never "auto" or "local" — so a linked platform project
// always generates against the deployed backend, not whatever happens to be
// at localhost:4003.
func TestWebLink_CodegenEnvIsRemote(t *testing.T) {
	require.Equal(t, "remote", webLinkCodegenEnv,
		"codegen env must be remote: auto/localhost emits wrong or empty clients for platform-linked projects")
}

// TestWebLink_HookContainsEnvRemote (Bug-2): the injected predev/prebuild hook
// must pin --env remote so that repeated `npm run build` / `npm run dev` runs
// do not regress the generated client back to a localhost URL.
func TestWebLink_HookContainsEnvRemote(t *testing.T) {
	require.Contains(t, webTypesCmd, "--env remote",
		"hook must pin --env remote so builds never regress to localhost")
	require.Contains(t, webTypesCmd, "--soft",
		"hook must be --soft so offline/no-CLI machines don't break the build")
	require.Contains(t, webTypesCmd, "|| exit 0",
		"|| exit 0 is required: --soft swallows CLI errors but not command-not-found (exit 127)")
}

// TestWebLink_ProvidersCreatedForNextAppRouter (Bug-3): for an App Router
// layout (app/layout.tsx), web link must create app/providers.tsx so the
// CLIENT bundle also imports and configures the generated pb client.
// Without this, every client component throws "Palbe is not configured".
func TestWebLink_ProvidersCreatedForNextAppRouter(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte(`import React from 'react';
export default function Layout() {}
`), 0o644))

	runWebLink(t, "--ref", "ref1")

	body, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err, "app/providers.tsx must be created")
	s := string(body)
	require.Contains(t, s, "'use client'", "providers.tsx must be a client component")
	require.Contains(t, s, "palbe.gen", "providers.tsx must import the generated client")
	require.Contains(t, s, "setupPalbeNext", "providers.tsx must call setupPalbeNext()")
	require.Contains(t, s, "@palbase/web/next/client", "providers.tsx must import from @palbase/web/next/client")
	require.Contains(t, s, "Providers", "providers.tsx must export a Providers component")
}

// TestWebLink_ProvidersCreatedForSrcAppRouter: same as above but for the
// src/app/layout.tsx App Router variant.
func TestWebLink_ProvidersCreatedForSrcAppRouter(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("src/app", 0o755))
	require.NoError(t, os.WriteFile("src/app/layout.tsx", []byte(`import React from 'react';
export default function Layout() {}
`), 0o644))

	runWebLink(t, "--ref", "ref1")

	body, err := os.ReadFile("src/app/providers.tsx")
	require.NoError(t, err, "src/app/providers.tsx must be created")
	s := string(body)
	require.Contains(t, s, "'use client'")
	require.Contains(t, s, "palbe.gen")
	require.Contains(t, s, "setupPalbeNext")
}

// TestWebLink_ProvidersIdempotent: running web link twice does not duplicate
// or overwrite providers.tsx when it already imports the gen file.
func TestWebLink_ProvidersIdempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte(`import React from 'react';
export default function Layout() {}
`), 0o644))

	runWebLink(t, "--ref", "ref1")
	first, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err)

	runWebLink(t, "--ref", "ref1") // second run
	second, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err)

	require.Equal(t, string(first), string(second), "providers.tsx must be byte-identical after re-link")
}

// TestWebLink_ProvidersNotCreatedForNonNextLayouts: providers.tsx is only
// written for Next.js App Router layouts. For src/main.tsx (Vite/CRA),
// no providers.tsx is created.
func TestWebLink_ProvidersNotCreatedForNonNextLayouts(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("src", 0o755))
	require.NoError(t, os.WriteFile("src/main.tsx", []byte("// entry\n"), 0o644))

	runWebLink(t, "--ref", "ref1")

	// No providers.tsx should be created for non-Next layouts.
	_, err := os.Stat("src/providers.tsx")
	require.True(t, os.IsNotExist(err), "providers.tsx must NOT be created for non-Next entries")
}

// TestWebLink_ProvidersGenRelPath: providers.tsx must import the gen file
// with a correct relative path (default: ../palbe.gen from app/).
func TestWebLink_ProvidersGenRelPath(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))

	runWebLink(t, "--ref", "ref1")

	body, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err)
	// Default gen file is palbe.gen.ts in root; from app/ that's ../palbe.gen.
	require.Contains(t, string(body), "../palbe.gen",
		"providers.tsx import path must be relative to its own directory")
}
