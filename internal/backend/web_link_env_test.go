package backend

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWireWebProjectGeneratesTheEnvironmentItIsGiven(t *testing.T) {
	t.Chdir(t.TempDir())
	installStubCodegen(t, "// gen") // seeds `main` and the generator
	writePkgJSON(t, minimalPkgJSON())
	require.NoError(t, os.MkdirAll("app", 0o755))
	require.NoError(t, os.WriteFile("app/layout.tsx", []byte("// entry\n"), 0o644))
	for _, env := range []string{"canary", "staging"} {
		require.NoError(t, os.MkdirAll(EnvDir(env), 0o755))
		require.NoError(t, os.WriteFile(ConfigPath(env, webPlatform),
			[]byte(`{"base_url":"https://`+env+`","api_key":"pb_stub"}`+"\n"), 0o600))
		require.NoError(t, os.WriteFile(SpecPath(env), []byte(`{"openapi":"3.1.0","x-palbase-roles":{"roles":[]},"paths":{}}`), 0o644))
	}

	var buf bytes.Buffer
	require.NoError(t, wireWebProject(context.Background(), "", "", "staging", &buf), buf.String())
	client, err := os.ReadFile(filepath.Join("palbase", "client.ts"))
	require.NoError(t, err)
	require.Contains(t, string(client), "./environments/staging/",
		"the web client was generated for the disk default, not the environment the link chose")
}

// THROUGH THE LINK (FR-018). The test above drives the wiring directly, and a
// suite that only did that stayed green when the link's call site passed no
// environment. `--from-env staging`, in a checkout whose disk default is
// `main`, generates the client for staging.
func TestALinkFromStagingGeneratesTheWebClientForStaging(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen")
	main := stackServing(t, linkKeyMain, nil)
	staging := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL, "stagref000": staging.URL})
	twoEnvironmentsOf(t)
	require.NoError(t, WriteLinkedIdentity(Product{ID: "prd_a", Name: "todoapp"}, Target{}))

	o := linkOpts{platforms: []string{"web"}, env: "staging"}
	require.NoError(t, resolveLinkTarget(context.Background(), Resolvers{}, &o))
	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	client, err := os.ReadFile(filepath.Join("palbase", "client.ts"))
	require.NoError(t, err)
	require.Contains(t, string(client), "./environments/staging/",
		"the link generated the web client for the disk default, not the environment it read from")
}

// A WEB CHECKOUT WHOSE DEFAULT ENVIRONMENT HAS NOTHING DEPLOYED — a new
// project's first link (FR-012). The generator refuses without a contract; the
// link failed there, and the stage took every config and the "push" line down
// with it. The configs are written, the line is said, the client waits.
func TestAWebLinkToAnEnvironmentWithNothingDeployedWritesItsConfig(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen")
	require.NoError(t, os.RemoveAll(EnvDir("main")), "the stub's seed is a deployed main")
	main, _ := envServer(t, linkKeyMain, envServerOpts{noContract: true, socialAuth: true})
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		platforms:    []string{"web"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), o, &out), out.String())
	require.FileExists(t, ConfigPath("main", webPlatform), "a link to an environment with nothing deployed wrote no config")
	require.Contains(t, out.String(), "the web client is not generated yet: main has no contract")
	require.NoFileExists(t, filepath.Join("palbase", "client.ts"), "a client was generated with no contract to generate it from")
}

// A GENERATOR THAT REFUSES IS HEARD. Its output went to the stage's buffer, which
// a failed link discards, so the link said only `palbe-gen: exit status 1`.
func TestAGeneratorRefusalReachesTheLinksError(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen")
	require.NoError(t, os.WriteFile(palbeGenBin, []byte("#!/bin/sh\necho 'error: the generator explains itself' >&2\nexit 1\n"), 0o755))
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	o := linkOpts{
		url:          main.URL,
		platforms:    []string{"web"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	err := runLink(context.Background(), o, &out)
	require.ErrorContains(t, err, "error: the generator explains itself")
}

// inGitRepoApp makes the current directory a Next app at apps/web of a git
// repository — the monorepo shape palgroup/palbase#20 was measured in — and
// commits it. rootIgnore is the repository's own ignore file ("" for none).
func inGitRepoApp(t *testing.T, rootIgnore string, files map[string]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed — this measures what a link leaves in a repository")
	}
	inScratchCheckout(t)
	repo, err := os.Getwd()
	require.NoError(t, err)
	gitIn(t, repo, "init", "-q")
	if rootIgnore != "" {
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(rootIgnore), 0o644))
	}
	app := filepath.Join(repo, "apps", "web")
	for rel, body := range files {
		p := filepath.Join(app, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "app")
	require.NoError(t, os.Chdir(app))
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return string(out)
}

// changedOutside lists what `git status --porcelain` reports in the repository
// outside prefix, a path relative to the current directory.
func changedOutside(t *testing.T, prefix string) []string {
	t.Helper()
	// Porcelain paths are relative to the repository root, wherever it runs.
	here := strings.TrimSpace(gitIn(t, ".", "rev-parse", "--show-prefix"))
	var outside []string
	for _, line := range strings.Split(strings.TrimRight(gitIn(t, ".", "status", "--porcelain", "-uall"), "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line[2:]), here+prefix) {
			outside = append(outside, line)
		}
	}
	return outside
}

// webLinkOpts is a relink of the app's `main` environment on the stack at url.
func webLinkOpts(url string) linkOpts {
	return linkOpts{
		url:          url,
		platforms:    []string{"web"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}
}

// A RELINK LEAVES AN APP THAT IS ALREADY WIRED AS IT IS (palgroup/palbase#20).
//
// The app is the customer's: a `[locale]` App Router whose OWN provider,
// `src/lib/providers.tsx`, imports `palbase/client` (through `baseUrl`) and
// calls setupPalbeNext() next to its other providers, in a monorepo whose root
// ignore file covers `node_modules/`. Every link used to splice the barrel into
// the root layout, create a second `providers.tsx` and drop an app-level
// `.gitignore` — none of it printed. Linked twice, and the second time against
// a stack whose key changed (the relink the issue was doing), the only changes
// git sees are the environment's own files.
//
// ONE FILE OUTSIDE palbase/ IS THE FIRST LINK'S TO WRITE, and it is not the
// scaffold: `palbase/environments/local/` holds this machine's stack, the root
// ignore file covers `node_modules/` and not that, so the app's own ignore file
// is created with that one rule and the link says so (FR-020, D-014). The
// `node_modules/` + `*.log` scaffold this issue was about still never lands;
// a relink finds the rule committed and adds nothing.
func TestARelinkLeavesAnAlreadyWiredNextAppAlone(t *testing.T) {
	inGitRepoApp(t, "node_modules/\n", map[string]string{
		// Linked before: the hooks an earlier link added are committed.
		"package.json":  `{"name":"web","private":true,"scripts":{"predev":"` + webTypesCmd + `","prebuild":"` + webTypesCmd + `"}}`,
		"tsconfig.json": `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./src/*"]},"jsx":"preserve"}}`,
		"src/app/layout.tsx": "import type { ReactNode } from 'react';\nimport { ApolloClient } from '@apollo/client';\n\n" +
			"export default function RootLayout({ children }: { children: ReactNode }) {\n  return children;\n}\n",
		"src/app/[locale]/layout.tsx": "import type { ReactNode } from 'react';\nimport { Providers } from '@/lib/providers';\n\n" +
			"export default function LocaleLayout({ children }: { children: ReactNode }) {\n  return <Providers>{children}</Providers>;\n}\n",
		"src/lib/providers.tsx": "\"use client\";\nimport \"palbase/client\";\nimport { setupPalbeNext } from \"@palbase/web/next/client\";\n\n" +
			"setupPalbeNext();\n\nexport function Providers({ children }: { children: React.ReactNode }) {\n  return children;\n}\n",
		"src/proxy.ts": "export function proxy() {}\n",
	})
	installStubCodegen(t, "// gen")
	first := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": first.URL})

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), webLinkOpts(first.URL), &out), out.String())
	require.Equal(t, []string{"?? apps/web/.gitignore"}, changedOutside(t, "palbase/"),
		"the first link changed the app's own files beyond the one rule FR-020 adds:\n%s", out.String())
	ignore, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	require.Equal(t, localIgnoreLine+"\n", string(ignore),
		"the app's ignore file carries more than this machine's stack — the root already covers node_modules/")
	require.Contains(t, out.String(), localIgnoreAdded)
	require.Contains(t, out.String(),
		"NOTE: palbase/client.ts is already wired in src/lib/providers.tsx — Palbase left layout and providers untouched.")
	require.NotContains(t, out.String(), "✓ wrote .gitignore",
		"the node_modules/ scaffold was written into a repository whose root already covers it")
	gitIn(t, ".", "add", "-A")
	gitIn(t, ".", "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "link")

	rotated := stackServing(t, linkKeyStaging, nil)
	routeEnvironments(t, map[string]string{"mainref000": rotated.URL})
	for i := 0; i < 2; i++ {
		out.Reset()
		require.NoError(t, runLink(context.Background(), webLinkOpts(rotated.URL), &out), out.String())
	}
	require.Empty(t, changedOutside(t, "palbase/environments/main/"),
		"a relink changed something besides the environment's files:\n%s", out.String())
	require.NotEmpty(t, gitIn(t, ".", "status", "--porcelain", "--", "palbase/environments/main/"),
		"the relink changed nothing at all — this test measures nothing")
}

// …AND A FRESH APP STILL GETS ALL THREE, each of them said: the barrel import in
// its root layout, the providers file, and — in a repository that ignores
// nothing — the ignore file that keeps its installed packages out of git.
func TestALinkWiresAFreshNextAppAndSaysSo(t *testing.T) {
	inGitRepoApp(t, "", map[string]string{
		"package.json": `{"name":"web","private":true}`,
		"src/app/layout.tsx": "import type { ReactNode } from 'react';\nimport { ApolloClient } from '@apollo/client';\n\n" +
			"export default function RootLayout({ children }: { children: ReactNode }) {\n  return children;\n}\n",
	})
	installStubCodegen(t, "// gen")
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})

	var out strings.Builder
	require.NoError(t, runLink(context.Background(), webLinkOpts(main.URL), &out), out.String())
	layout, err := os.ReadFile(filepath.Join("src", "app", "layout.tsx"))
	require.NoError(t, err)
	require.Contains(t, string(layout), "import '../../palbase/client';",
		"`@apollo/client` was taken for the generated client and the layout was left unwired")
	require.FileExists(t, filepath.Join("src", "app", "providers.tsx"))
	require.FileExists(t, ".gitignore")
	for _, said := range []string{"~ modified src/app/layout.tsx", "✓ wrote src/app/providers.tsx", "✓ wrote .gitignore"} {
		require.Contains(t, out.String(), said)
	}
	require.NotContains(t, out.String(), "already wired")
}

// A LINK THAT FAILS AFTER BINDING THE PROJECT RELEASES THE STACK, AND SAYS SO.
// The record naming the project is published either way; releasing inside the
// stage left the release unsaid, and before that a release that did not happen
// left the stack linked by address winning over the published project.
func TestAFailedLinkThatBindsTheProjectReleasesTheStackAndSaysSo(t *testing.T) {
	inScratchCheckout(t)
	seedWebCheckout(t)
	installStubCodegen(t, "// gen")
	require.NoError(t, os.WriteFile(palbeGenBin, []byte("#!/bin/sh\necho 'error: the generator explains itself' >&2\nexit 1\n"), 0o755))
	main := stackServing(t, linkKeyMain, nil)
	routeEnvironments(t, map[string]string{"mainref000": main.URL})
	installed := stackServing(t, linkKeyCanary, nil)
	require.NoError(t, WriteSelfHostTarget(Target{URL: installed.URL}))
	o := linkOpts{
		url:          main.URL,
		platforms:    []string{"web"},
		linkedEnv:    "main",
		product:      Product{ID: "prd_a", Name: "todoapp"},
		environments: []Environment{{Name: "main", Ref: "mainref000", Status: "Running"}},
	}

	var out strings.Builder
	require.Error(t, runLink(context.Background(), o, &out))
	record, err := readLinkedProject()
	require.NoError(t, err)
	require.Equal(t, "prd_a", record.Project, "the failed link published no project record — this test measures nothing")
	local, err := localPath()
	require.NoError(t, err)
	assert.NoFileExists(t, local, "the published record names the project while the stack linked by address still wins")
	assert.Contains(t, out.String(), "no longer acts on the stack linked here by address")
}
