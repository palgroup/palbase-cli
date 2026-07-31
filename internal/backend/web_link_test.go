package backend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// stubArtifactsFunc replaces webLinkArtifacts for tests — writes minimal
// committed artifacts (openapi.json + palbase-config.json) with no network. The
// emitted config names the ENVIRONMENT and carries no branch.
func stubArtifactsFunc() func(context.Context, Resolvers, selection.Selection, io.Writer) error {
	return func(_ context.Context, _ Resolvers, sel selection.Selection, _ io.Writer) error {
		if err := os.MkdirAll(webArtifactsDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(webArtifactsDir, "openapi.json"), []byte(`{"openapi":"3.1.0","paths":{}}`), 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(webArtifactsDir, "palbase-config.json"),
			[]byte(`{"environment_ref":"`+sel.EnvironmentRef()+`","base_url":"https://stub","api_key":"pb_stub"}`+"\n"), 0o600)
	}
}

// installStubCodegen wires the link pipeline's test doubles: the artifact
// seam (no network) plus a fake node_modules/.bin/palbe-gen that writes
// `content` to its --out argument — standing in for @palbase/web's generator,
// so tests verify the whole "fetch artifacts → run the SDK generator" chain
// without npm or a real network.
func installStubCodegen(t *testing.T, content string) {
	t.Helper()
	orig := webLinkArtifacts
	webLinkArtifacts = stubArtifactsFunc()
	t.Cleanup(func() { webLinkArtifacts = orig })

	require.NoError(t, os.MkdirAll(filepath.Dir(palbeGenBin), 0o755))
	script := "#!/bin/sh\nout=palbe.gen.ts\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = \"--out\" ]; then out=\"$2\"; shift; fi\n  shift\ndone\ncat > \"$out\" <<'PALBE_EOF'\n" + content + "\nPALBE_EOF\n"
	require.NoError(t, os.WriteFile(palbeGenBin, []byte(script), 0o755))
}

// minimalPkgJSON returns the smallest valid package.json (no scripts section).
func minimalPkgJSON() string {
	return `{
  "name": "myapp",
  "version": "1.0.0"
}`
}

// `web link` exposes NO --ref: the project + environment come from the selection
// (or the global --project / --environment).
func TestWebLinkCommandFlags(t *testing.T) {
	cmd := newWebCmd(noopResolvers())
	var linkFlags []string
	for _, child := range cmd.Commands() {
		if child.Name() == "link" {
			child.Flags().VisitAll(func(flag *pflag.Flag) { linkFlags = append(linkFlags, flag.Name) })
		}
	}
	require.Equal(t, []string{"entry", "out"}, linkFlags)
}

// webRig selects proj_1 / production IN THE CURRENT DIRECTORY (the callers have
// already chdir'd into their own temp dir and seeded package.json there) and
// returns Resolvers backed by the fake v2 API.
func webRig(t *testing.T) Resolvers {
	t.Helper()
	selectiontest.WriteConfig(t, ".", nil)
	f := selectiontest.New(t)
	rest := f.REST()
	resolver := f.Resolver()
	return Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	}
}

// writePkgJSON writes content to package.json in the current directory.
func writePkgJSON(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile("package.json", []byte(content), 0o644))
}

// runWebLink executes `web link` with the given extra args and returns stdout.
func runWebLink(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newWebCmd(webRig(t))
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
	require.Equal(t, "palbe-gen --soft || exit 0", webTypesCmd)
}

// TestWebLink_NoPkgJSON: errors when package.json is absent.
func TestWebLink_NoPkgJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")

	cmd := newWebCmd(webRig(t))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"link"})
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

	runWebLink(t)

	// The selection is left intact — `web link` reads it, it does not re-target it.
	cfg, err := selection.Load("")
	require.NoError(t, err)
	require.Equal(t, "proj_1", cfg.ProjectID)
	require.Equal(t, "env_prod", cfg.EnvironmentID)

	// gen file must exist (stubbed).
	genContent, err := os.ReadFile("palbe.gen.ts")
	require.NoError(t, err)
	require.Contains(t, string(genContent), "palbe gen sentinel")

	// scripts.predev + scripts.prebuild must be added with the exact hook value.
	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody), `"predev": "palbe-gen --soft || exit 0"`)
	require.Contains(t, string(pkgBody), `"prebuild": "palbe-gen --soft || exit 0"`)

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

			runWebLink(t)

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

	runWebLink(t, "--entry", "src/custom-entry.tsx")

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

	runWebLink(t)

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

	runWebLink(t)

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

		runWebLink(t)

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

		runWebLink(t)

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

	outStr := runWebLink(t)

	// Warning must be printed with the suggested value.
	require.Contains(t, outStr, "predev")
	require.Contains(t, outStr, "palbe-gen --soft || exit 0")

	// The existing script must NOT be clobbered.
	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody), `"predev": "my-custom-hook"`)
	// prebuild (absent) should be added.
	require.Contains(t, string(pkgBody), `"prebuild": "palbe-gen --soft || exit 0"`)
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

	runWebLink(t)

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
	require.Contains(t, content, `"predev": "palbe-gen --soft || exit 0"`)
	require.Contains(t, content, `"prebuild": "palbe-gen --soft || exit 0"`)

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
    "predev": "palbe-gen --soft || exit 0",
    "prebuild": "palbe-gen --soft || exit 0"
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
        "predev": "palbe-gen --soft || exit 0",
        "prebuild": "palbe-gen --soft || exit 0"
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
    "predev": "palbe-gen --soft || exit 0",
    "prebuild": "palbe-gen --soft || exit 0"
  }
}
`
		run(t, input, expected)
	})

	t.Run("already correct — byte identical", func(t *testing.T) {
		input := `{
  "name": "myapp",
  "scripts": {
    "predev": "palbe-gen --soft || exit 0",
    "prebuild": "palbe-gen --soft || exit 0"
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

	runWebLink(t)
	runWebLink(t) // second run

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

// `web link` fetches artifacts for the SELECTED environment, and re-running it
// after `palbase env use staging` re-targets them — there is no --ref to pass
// and no branch to keep.
func TestWebLink_FetchesForTheSelectedEnvironment(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_stg"})

	f := selectiontest.New(t)
	f.Environments["proj_1"] = append(f.Environments["proj_1"],
		selectiontest.Env("env_stg", "proj_1", "app1stg", "staging", "staging", false))
	rest := f.REST()
	resolver := f.Resolver()
	r := Resolvers{
		REST:      func() REST { return rest },
		Selection: func() *selection.Resolver { return resolver },
	}

	var gotSel selection.Selection
	orig := webLinkArtifacts
	webLinkArtifacts = func(ctx context.Context, res Resolvers, sel selection.Selection, w io.Writer) error {
		gotSel = sel
		return stubArtifactsFunc()(ctx, res, sel, w)
	}
	t.Cleanup(func() { webLinkArtifacts = orig })

	require.NoError(t, os.MkdirAll(filepath.Dir(palbeGenBin), 0o755))
	require.NoError(t, os.WriteFile(palbeGenBin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	writePkgJSON(t, minimalPkgJSON())

	cmd := newWebCmd(r)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"link"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "proj_1", gotSel.ProjectID)
	require.Equal(t, "app1stg", gotSel.EnvironmentRef(), "artifacts must be fetched for the SELECTED environment")

	raw, err := os.ReadFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"environment_ref":"app1stg"`)
	require.NotContains(t, string(raw), "branch")
}

// TestWebLink_EnsuresProjectConfigGitignored: link keeps the per-machine
// project selection out of git while generated .palbase inputs stay trackable.
func TestWebLink_EnsuresPalbaseGitignored(t *testing.T) {
	t.Run("creates .gitignore when absent", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())

		runWebLink(t)

		body, err := os.ReadFile(".gitignore")
		require.NoError(t, err)
		require.Equal(t, ".palbase/config.json\n", string(body))
	})

	t.Run("appends to an existing .gitignore", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())
		require.NoError(t, os.WriteFile(".gitignore", []byte("node_modules/\n"), 0o644))

		runWebLink(t)

		body, err := os.ReadFile(".gitignore")
		require.NoError(t, err)
		require.Equal(t, "node_modules/\n.palbase/config.json\n", string(body))
	})

	t.Run("does not duplicate on re-link", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())

		runWebLink(t)
		runWebLink(t)

		body, err := os.ReadFile(".gitignore")
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(body), ".palbase/config.json"))
	})

	t.Run("narrows an existing directory rule", func(t *testing.T) {
		t.Chdir(t.TempDir())
		installStubCodegen(t, "// gen")
		writePkgJSON(t, minimalPkgJSON())
		require.NoError(t, os.WriteFile(".gitignore", []byte("node_modules/\n.palbase/\n"), 0o644))

		runWebLink(t)

		body, err := os.ReadFile(".gitignore")
		require.NoError(t, err)
		require.Equal(t, "node_modules/\n.palbase/config.json\n", string(body))
	})
}

// TestWebLink_GitignoreWarning: prints a loud warning when .gitignore ignores
// the gen file. The offending rule is reported, never edited (the only write
// is the appended .palbase/config.json entry).
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

			outStr := runWebLink(t)

			require.Contains(t, outStr, "WARNING", "should print a loud warning about .gitignore")
			require.Contains(t, outStr, "palbe.gen.ts", "warning should mention the gen file")

			// The offending rule must NOT be rewritten/removed; the only
			// change is the appended .palbase/config.json entry.
			body, err := os.ReadFile(".gitignore")
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(body), tc.content),
				"existing rules must stay byte-identical, got: %q", string(body))
			require.Contains(t, string(body), ".palbase/config.json")
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

	outStr := runWebLink(t)
	require.Contains(t, outStr, "palbe.gen", "manual instruction should mention gen file")
}

// TestWebLink_ResolveAndPersist_ConfiglessDirDoesNotOrphanOrDuplicate exercises
// the two real functions webLinkArtifacts calls before ever touching the
// network for the spec/config — resolveWebApp and persistProjectAppSlot —
// going AROUND the webLinkArtifacts stub every other test in this file
// installs (stubArtifactsFunc never calls either). Every other web-link test
// stubs webLinkArtifacts wholesale, so this exact bug shipped with CI green:
// `palbase web link --project X` in a config-less directory registered the app
// remotely, then persistProjectAppSlot's unconditional selection.Load("")
// returned ErrNotSelected and the whole call failed with "no project
// selected" — orphaning the just-created app. Mutation-evident: restore the
// unconditional `if err != nil { return err }` in persistProjectAppSlot and
// the first block below turns red.
func TestWebLink_ResolveAndPersist_ConfiglessDirDoesNotOrphanOrDuplicate(t *testing.T) {
	dir := selectiontest.Chdir(t)
	// Deliberately no selectiontest.WriteConfig: this is the config-less
	// directory `--project proj_1` resolves without ever touching disk.

	var registered []map[string]any
	var postCount int
	f := selectiontest.New(t)
	f.Handle("GET /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusOK, registered)
	})
	f.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
		postCount++
		row := map[string]any{"id": "app_web", "platform": "web"}
		registered = append(registered, row)
		selectiontest.WriteOK(w, http.StatusCreated, row)
	})
	rest := f.REST()

	sel := selection.Selection{
		ProjectID:          "proj_1",
		Environment:        selection.Environment{ID: "env_prod", Ref: "app1prod"},
		RepositoryProvider: selection.ProviderPalbase,
	}

	// First run: register then persist — must not fail with "no project
	// selected" even though the directory had no config a moment ago.
	firstPersistedID, err := persistedAppIDFor("web", sel)
	require.NoError(t, err)
	appID, err := resolveWebApp(context.Background(), rest, sel.ProjectID, firstPersistedID, io.Discard)
	require.NoError(t, err)
	require.NoError(t, persistProjectAppSlot("web", appID, &sel, false),
		"registering the app remotely must not leave the run failing with 'no project selected'")
	require.Equal(t, 1, postCount)

	cfg, err := selection.Load(dir)
	require.NoError(t, err, "the fresh registration must be persisted, not orphaned")
	require.Equal(t, "app_web", cfg.WebAppID)

	// Second run: mirrors webLinkArtifacts re-reading the persisted app id.
	secondPersistedID, err := persistedAppIDFor("web", sel)
	require.NoError(t, err)
	appID2, err := resolveWebApp(context.Background(), rest, sel.ProjectID, secondPersistedID, io.Discard)
	require.NoError(t, err)
	require.Equal(t, appID, appID2)
	require.NoError(t, persistProjectAppSlot("web", appID2, &sel, false))
	require.Equal(t, 1, postCount, "the second run must reuse the persisted app, not register a duplicate")
}

// TestWebLink_BrokenConfig_AbortsBeforeRegisteringAnyApp is the web-link
// equivalent of the native test of the same shape: a `.palbase/config.json`
// that exists but fails to load (corrupt JSON, an unsupported version) must
// abort the command BEFORE webLinkArtifacts ever calls resolveWebApp. Before
// persistedAppIDFor gated on the error, this Load failure was swallowed into
// "" and resolveWebApp registered an app remotely that persistProjectAppSlot
// then failed to persist against the SAME broken config — an orphan.
// `--project proj_1` is required to reach this: without it, Resolver.Resolve
// reads the same broken file itself and fails first, which would pass this
// test for the wrong reason.
func TestWebLink_BrokenConfig_AbortsBeforeRegisteringAnyApp(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"bad JSON", `{not valid json`},
		{"unsupported version (v1)", `{"version":1,"project_ref":"proj_1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectiontest.Chdir(t)
			selectiontest.WriteRawConfig(t, "", tc.raw)
			writePkgJSON(t, minimalPkgJSON())

			var postCount int
			f := selectiontest.New(t)
			// A real (empty) apps list, so a gate bypass proceeds all the way to a
			// genuine POST instead of failing earlier on an unmodeled route — the
			// postCount assertion below must be locking the real gate, not an
			// incidental 404.
			f.OK("GET /api/v2/projects/proj_1/apps", []map[string]any{})
			f.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
				postCount++
				selectiontest.WriteOK(w, http.StatusCreated, map[string]any{"id": "app_web", "platform": "web"})
			})
			rest := f.REST()
			resolver := &selection.Resolver{
				REST:        func() selection.REST { return rest },
				ProjectFlag: "proj_1", // headless --project: Resolve() never reads the broken config itself
			}
			r := Resolvers{
				REST:      func() REST { return rest },
				Selection: func() *selection.Resolver { return resolver },
				Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
			}

			cmd := newWebCmd(r)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"link"})
			err := cmd.Execute()
			require.Error(t, err, "a broken local config must fail the command")
			require.Equal(t, 0, postCount, "no app may be registered before the broken config is surfaced")
		})
	}
}

// TestWebUnlink_RemovesConfig: `web unlink` removes .palbase/config.json and
// the .palbase/ dir (when empty), leaves gen file and scripts.
func TestWebUnlink_RemovesConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("palbe.gen.ts", []byte("// gen"), 0o644))
	writePkgJSON(t, `{
  "name": "myapp",
  "scripts": {
    "predev": "palbe-gen --soft || exit 0",
    "prebuild": "palbe-gen --soft || exit 0"
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

	runWebLink(t, "--out", "my.custom.gen.ts")

	_, err := os.Stat("my.custom.gen.ts")
	require.NoError(t, err, "custom out file should exist")

	// The import in the entry file should reference the custom out name.
	entryBody, err := os.ReadFile("app/layout.tsx")
	require.NoError(t, err)
	require.Contains(t, string(entryBody), "my.custom.gen")

	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody),
		`"predev": "palbe-gen --out 'my.custom.gen.ts' --soft || exit 0"`,
		"automatic regeneration must preserve the custom output path")
	require.Contains(t, string(pkgBody),
		`"prebuild": "palbe-gen --out 'my.custom.gen.ts' --soft || exit 0"`,
		"automatic regeneration must preserve the custom output path")
}

// ── Bug-fix regression tests ──────────────────────────────────────────────────

// TestWebLink_ArtifactsWritten (Bug-1 successor): link must leave the two
// COMMITTED SDK-generator inputs in Palbase/ — palbe-gen (in @palbase/web)
// generates offline from these, so a missing artifact means every later
// build regenerates from nothing.
func TestWebLink_ArtifactsWritten(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())

	runWebLink(t)

	for _, f := range []string{"openapi.json", "palbase-config.json"} {
		_, err := os.Stat(filepath.Join(webArtifactsDir, f))
		require.NoError(t, err, "web link must write Palbase/%s", f)
	}
}

// TestWebLink_HookIsSDKGenerator (Bug-2 successor): the injected predev/
// prebuild hook must run the SDK's OWN generator (palbe-gen) — never a
// palbase CLI codegen verb (client codegen is the SDKs' job) — and stay
// build-safe on machines without the SDK installed.
func TestWebLink_HookIsSDKGenerator(t *testing.T) {
	require.Contains(t, webTypesCmd, "palbe-gen",
		"hook must run @palbase/web's generator, not a CLI codegen verb")
	require.NotContains(t, webTypesCmd, "palbase ",
		"the hook must not depend on the palbase CLI being installed")
	require.Contains(t, webTypesCmd, "--soft",
		"hook must be --soft so a broken spec doesn't break the build")
	require.Contains(t, webTypesCmd, "|| exit 0",
		"|| exit 0 is required: --soft swallows generator errors but not command-not-found (exit 127)")
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

	runWebLink(t)

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

	runWebLink(t)

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

	runWebLink(t)
	first, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err)

	runWebLink(t) // second run
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

	runWebLink(t)

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

	runWebLink(t)

	body, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err)
	// Default gen file is palbe.gen.ts in root; from app/ that's ../palbe.gen.
	require.Contains(t, string(body), "../palbe.gen",
		"providers.tsx import path must be relative to its own directory")
}

// ── Task 3 regression tests: providers clobber, wrong path, dangling import ──

// TestWebLink_ProvidersNeverOverwritesExisting is the EZME (overwrite) lock:
// an existing providers.tsx whose first line is NOT 'use client' (so Palbase
// cannot safely reason about splicing an import into it — see
// firstLineIsUseClientDirective) must survive link BYTE-IDENTICAL. Before
// this fix, wireNextProviders's idempotency check only skipped writing when
// the file ALREADY imported the gen stem; anything else — a project's own
// React Query / theme / i18n provider — was silently clobbered by
// os.WriteFile with the generated template. Irrecoverable data loss.
func TestWebLink_ProvidersNeverOverwritesExisting(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))

	existing := "import { QueryClientProvider } from '@tanstack/react-query';\n\n" +
		"export function Providers({ children }) {\n" +
		"  return <QueryClientProvider>{children}</QueryClientProvider>;\n}\n"
	require.NoError(t, os.WriteFile("app/providers.tsx", []byte(existing), 0o644))

	out := runWebLink(t)

	body, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err)
	require.Equal(t, existing, string(body),
		"an existing providers.tsx that isn't a 'use client' component must survive byte-identical")
	require.Contains(t, out, "Palbase left it untouched",
		"the CLI must tell the user it skipped the file and why")
}

// TestWebLink_ProvidersSplicedIntoExistingClientComponent: an existing
// providers.tsx that IS a 'use client' component (the only case where a
// splice is provably safe) gets a single import line added, not overwritten
// — the rest of the file, including its own providers, must survive intact.
func TestWebLink_ProvidersSplicedIntoExistingClientComponent(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))

	existing := "'use client';\nimport { ThemeProvider } from './theme';\n\n" +
		"export function Providers({ children }) {\n" +
		"  return <ThemeProvider>{children}</ThemeProvider>;\n}\n"
	require.NoError(t, os.WriteFile("app/providers.tsx", []byte(existing), 0o644))

	runWebLink(t)

	body, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err)
	s := string(body)
	require.Contains(t, s, "import '../palbe.gen'", "the gen import must be spliced in")
	require.Contains(t, s, "ThemeProvider", "the existing provider content must survive")
	require.Contains(t, s, "'use client';", "the existing directive must survive")

	// Re-running must not duplicate the spliced import (idempotent on the
	// gen-import check, same as the fresh-create path).
	runWebLink(t)
	second, err := os.ReadFile("app/providers.tsx")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(second), "palbe.gen"), "the spliced import must not be duplicated on re-link")
}

// TestWebLink_ProvidersJSXAppRouter (.jsx silent-skip fix): a plain-JS App
// Router project (app/layout.jsx) must get providers.jsx wired up too —
// before this, nextAppLayouts only recognized .tsx keys, so a .jsx layout
// got its entry import wired but silently NO providers file at all, and
// autoEntryPaths didn't even auto-detect a .jsx layout to begin with.
func TestWebLink_ProvidersJSXAppRouter(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.jsx", []byte("export default function Layout() {}\n"), 0o644))

	runWebLink(t)

	entryBody, err := os.ReadFile("app/layout.jsx")
	require.NoError(t, err)
	require.Contains(t, string(entryBody), "palbe.gen", "the .jsx entry must still be auto-detected and wired")

	body, err := os.ReadFile("app/providers.jsx")
	require.NoError(t, err, "app/providers.jsx must be created for a .jsx App Router layout")
	s := string(body)
	require.Contains(t, s, "'use client'")
	require.Contains(t, s, "palbe.gen")
	require.Contains(t, s, "setupPalbeNext")
	require.NotContains(t, s, "React.ReactNode", "a .jsx project must not get TypeScript type syntax it can't parse")

	_, tsxErr := os.Stat("app/providers.tsx")
	require.True(t, os.IsNotExist(tsxErr), "must not also create a .tsx sibling")

	// middleware.ts wiring (Task 6) follows the same extension-matching rule.
	mwBody, err := os.ReadFile("middleware.js")
	require.NoError(t, err, "middleware.js must be created for a .jsx App Router layout")
	mws := string(mwBody)
	require.Contains(t, mws, "palbeMiddleware")
	require.NotContains(t, mws, "NextRequest", "a .jsx project must not get the TypeScript type import/annotation")
	_, mwTsErr := os.Stat("middleware.ts")
	require.True(t, os.IsNotExist(mwTsErr), "must not also create a middleware.ts sibling")
}

// TestWebLink_NoDanglingImportWhenSDKNotInstalled: when @palbase/web's
// palbe-gen binary isn't installed, outFile never gets created — the entry
// file (and providers.tsx) must NOT get an import wired to it, or the
// command "succeeds" while leaving a project that fails to resolve the
// import until install. package.json's predev/prebuild hooks still get set
// up so a later install + build regenerates and this can be re-linked.
func TestWebLink_NoDanglingImportWhenSDKNotInstalled(t *testing.T) {
	t.Chdir(t.TempDir())
	orig := webLinkArtifacts
	webLinkArtifacts = stubArtifactsFunc()
	t.Cleanup(func() { webLinkArtifacts = orig })
	// Deliberately do NOT create node_modules/.bin/palbe-gen.

	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))

	out := runWebLink(t)
	require.Contains(t, out, "not installed yet")

	entryBody, err := os.ReadFile("app/layout.tsx")
	require.NoError(t, err)
	require.NotContains(t, string(entryBody), "palbe.gen",
		"no import may be wired to a gen file that was never generated")

	_, providersErr := os.Stat("app/providers.tsx")
	require.True(t, os.IsNotExist(providersErr), "providers.tsx must not be created either — it would also dangle")

	_, mwErr := os.Stat("middleware.ts")
	require.True(t, os.IsNotExist(mwErr), "middleware.ts must not be created either — it would also dangle")

	pkgBody, err := os.ReadFile("package.json")
	require.NoError(t, err)
	require.Contains(t, string(pkgBody), `"predev": "palbe-gen --soft || exit 0"`,
		"predev/prebuild must still be wired so a later install regenerates")
}

// TestWebLink_EntryImportRelPath locks filepath.Rel(filepath.Dir(entryPath),
// outFile)'s output against REAL directory-tree fixtures, verified against
// Go's actual filepath.Rel semantics (checked by hand, not assumed) rather
// than any hand-written expectation. Includes the exact (entry, out) pair
// that reproduces the user-reported `../../palbe.gen`: with the DEFAULT out
// (no --out, so out=palbe.gen.ts at the repo root) a src/app/layout.tsx
// entry is TWO directories below the root — "../../palbe.gen" IS the
// mathematically correct relative import for that pair, not a formula bug.
// The pair the report's prose named explicitly (--out src/palbe.gen.ts with
// a src/app/layout.tsx entry) already computes correctly today: "../palbe.gen".
func TestWebLink_EntryImportRelPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		entry, out string
		want       string
	}{
		{"default out, root App Router", "app/layout.tsx", "palbe.gen.ts", "../palbe.gen"},
		{"default out, src App Router", "src/app/layout.tsx", "palbe.gen.ts", "../../palbe.gen"},
		{"--out under src, src App Router entry", "src/app/layout.tsx", "src/palbe.gen.ts", "../palbe.gen"},
		{"--out co-located with a non-Next src entry", "src/main.tsx", "src/palbe.gen.ts", "./palbe.gen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, os.MkdirAll(filepath.Dir(tc.entry), 0o755))
			require.NoError(t, os.WriteFile(tc.entry, []byte("// entry\n"), 0o644))

			require.NoError(t, wireEntryImport("", tc.out, io.Discard))

			body, err := os.ReadFile(tc.entry)
			require.NoError(t, err)
			wantLine := "import '" + tc.want + "';"
			// Exact LINE match, not Contains: "../palbe.gen" is itself a
			// substring of "../../palbe.gen", so a plain substring check
			// would not catch a formula regression between these two cases.
			require.Contains(t, strings.Split(string(body), "\n"), wantLine)
		})
	}
}

// ── Task 6 regression tests: middleware.ts generation ─────────────────────────

// TestWebLink_MiddlewareCreatedForAppRouter: `web link` writes middleware.ts
// for a Next.js App Router project so the session cookie refreshes BEFORE
// the RSC tree renders. Server Components can't write cookies — without
// this, every RSC render re-refreshes from the same stale cookie token, and
// once two such refreshes land more than palauth's 30s rotation grace apart
// the reuse detector revokes the WHOLE token family (force logout
// everywhere; modules/auth/internal/token/refresh.go:25,282-287). Content
// matches @palbase/web/next/middleware's own documented usage exactly.
func TestWebLink_MiddlewareCreatedForAppRouter(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))

	runWebLink(t)

	body, err := os.ReadFile("middleware.ts")
	require.NoError(t, err, "middleware.ts must be created at the project root")
	s := string(body)
	require.Contains(t, s, "import { palbeMiddleware } from '@palbase/web/next/middleware';")
	require.Contains(t, s, "import type { NextRequest } from 'next/server';")
	require.Contains(t, s, "export function middleware(request: NextRequest)")
	require.Contains(t, s, "palbeMiddleware(request, {")
	// stubArtifactsFunc commits base_url:"https://stub" / api_key:"pb_stub" —
	// the SAME artifact palbe.gen.ts itself was configured from.
	require.Contains(t, s, `url: "https://stub"`, "must carry the artifact's own url")
	require.Contains(t, s, `apiKey: "pb_stub"`, "must carry the artifact's own (publishable) api key")
	require.Contains(t, s, "export const config = {",
		"config MUST be declared in this file — Next reads it off this file's own AST, a re-export is invisible")
	require.Contains(t, s, "matcher:")
}

// TestWebLink_MiddlewareNeverOverwritesExisting: an existing middleware.ts
// (the user's own auth/redirect logic, or one from a prior manual
// integration) must survive link BYTE-IDENTICAL. Unlike providers.tsx there
// is no safe splice here — Next reads exactly ONE middleware/default export
// per file, so guessing how to combine two response values would be unsafe;
// the file is left untouched and the user is told to wire it in by hand.
func TestWebLink_MiddlewareNeverOverwritesExisting(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen")
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))

	existing := "import { NextResponse } from 'next/server';\n\n" +
		"export function middleware(request) {\n  // custom auth check\n  return NextResponse.next();\n}\n\n" +
		"export const config = { matcher: '/dashboard/:path*' };\n"
	require.NoError(t, os.WriteFile("middleware.ts", []byte(existing), 0o644))

	out := runWebLink(t)

	body, err := os.ReadFile("middleware.ts")
	require.NoError(t, err)
	require.Equal(t, existing, string(body), "an existing middleware.ts must survive byte-identical")
	require.Contains(t, out, "already exists", "the CLI must tell the user it skipped the file")
}

// TestWebLink_MiddlewarePathDerivedFromSrcLayout: middleware.ts must land at
// the project ROOT for a root App Router (app/layout.tsx), or inside src/
// for a src/ App Router (src/app/layout.tsx) — Next only recognizes
// middleware at those exact two convention levels (never inside app/ or
// src/app/; confirmed against Next 16.2.9's own isAtConventionLevel check —
// normalizedFileDir === '/' || === '/src' — via Context7, not assumed).
func TestWebLink_MiddlewarePathDerivedFromSrcLayout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		entryDir  string
		entryFile string
		wantPath  string
	}{
		{"root App Router", "app", "app/layout.tsx", "middleware.ts"},
		{"src App Router", "src/app", "src/app/layout.tsx", filepath.Join("src", "middleware.ts")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			installStubCodegen(t, "// gen")
			writePkgJSON(t, minimalPkgJSON())
			require.NoError(t, os.MkdirAll(tc.entryDir, 0o755))
			require.NoError(t, os.WriteFile(tc.entryFile, []byte("// entry\n"), 0o644))

			runWebLink(t)

			_, err := os.Stat(tc.wantPath)
			require.NoError(t, err, "middleware must land at %s", tc.wantPath)

			// It must NOT also land at the OTHER convention level.
			other := "middleware.ts"
			if tc.wantPath == "middleware.ts" {
				other = filepath.Join("src", "middleware.ts")
			}
			_, otherErr := os.Stat(other)
			require.True(t, os.IsNotExist(otherErr), "must not also write %s", other)
		})
	}
}
