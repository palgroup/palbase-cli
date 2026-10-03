package backend

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `palbase build` RUN ONE FOLDER TOO HIGH SAYS SO, AND WRITES NOTHING THERE
// (palbase-cli#8).
//
// Measured with 0.79.1, in a workspace whose backend lives in `backend/`: the
// build found the backend's TypeScript below the working directory, ran
// `npm install` in a folder with no package.json, and answered
// "@palbase/backend is not installed and `npm install` failed (exit status
// 254)" — leaving a package-lock.json behind, and naming a `.palbase/` no CLI
// had written as "the hidden root" whose own files "were removed". An AI agent
// read that as "the backend tools are broken" and blocked its task; the same
// command inside `backend/` had passed minutes earlier. What was wrong was the
// folder, so that is what the refusal must say.
func TestBuildOutsideABackendsFolderSaysWhereTheBackendIs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := exec.LookPath("bun"); err != nil {
		requireToolOnCI(t, "bun", err)
		t.Skip("bun is not on PATH — the build refuses before it reaches the folder question")
	}
	const sdkPackageJSON = `{"name":"api","dependencies":{"@palbase/backend":"^42.0.0"}}`

	// The issue's five lines, verbatim: no package.json anywhere, so there is no
	// backend folder to name — only the folder the TypeScript is under.
	t.Run("the issue's own tree", func(t *testing.T) {
		ws := t.TempDir()
		mustWrite(t, ws, "backend/modules/a.ts", "import { Controller } from \"@palbase/backend\";\nexport class A {}\n")
		mustWrite(t, ws, ".palbase/palbase.env", "")

		msg, out := buildRefusal(t, ws)

		mustContain(t, msg, "no package.json here")
		mustContain(t, msg, "backend/")
		mustNotContain(t, msg, "npm install")
		mustNotContain(t, out, "kept")
		wroteNothing(t, ws)
		if _, err := os.Stat(filepath.Join(ws, ".palbase", "palbase.env")); err != nil {
			t.Errorf("a .palbase/ no CLI wrote was touched: %v", err)
		}
	})

	// What the reporter actually had: the backend folder carries its package.json.
	t.Run("the backend has its package.json", func(t *testing.T) {
		ws := t.TempDir()
		mustWrite(t, ws, "backend/package.json", sdkPackageJSON)
		mustWrite(t, ws, "backend/modules/app/app.module.ts", "export class AppModule {}\n")

		msg, _ := buildRefusal(t, ws)

		mustContain(t, msg, "no package.json here")
		mustContain(t, msg, "cd backend && palbase build")
		wroteNothing(t, ws)
	})

	// One folder too LOW is the same mistake from the other side.
	t.Run("run inside the backend's modules/", func(t *testing.T) {
		ws := t.TempDir()
		mustWrite(t, ws, "backend/package.json", sdkPackageJSON)
		mustWrite(t, ws, "backend/modules/app/app.module.ts", "export class AppModule {}\n")
		here := filepath.Join(ws, "backend", "modules")

		msg, _ := buildRefusal(t, here)

		mustContain(t, msg, "cd .. && palbase build")
		wroteNothing(t, here)
	})

	// Two backends below: both are named, neither is picked for the person.
	t.Run("two backends below", func(t *testing.T) {
		ws := t.TempDir()
		mustWrite(t, ws, "services/api/package.json", sdkPackageJSON)
		mustWrite(t, ws, "services/api/modules/a.module.ts", "export class A {}\n")
		mustWrite(t, ws, "admin/package.json", `{"devDependencies":{"@palbase/backend":"42.0.0"}}`)
		// A package.json that is not a backend's is not offered as one.
		mustWrite(t, ws, "web/package.json", `{"dependencies":{"react":"^19.0.0"}}`)

		msg, _ := buildRefusal(t, ws)

		mustContain(t, msg, "admin/, services/api/")
		mustNotContain(t, msg, "web/")
		wroteNothing(t, ws)
	})

	// NEGATIVE CONTROL: the backend's own folder goes on past the question. The
	// SDK is planted, so nothing is installed; what is asserted is only that the
	// folder refusal does not fire where the package.json is.
	t.Run("the backend's own folder is not refused", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir, "package.json", sdkPackageJSON)
		mustWrite(t, dir, "modules/app/app.module.ts", "export class AppModule {}\n")
		seedInstalledBackend(t, dir, "42.0.0")

		var out bytes.Buffer
		err := runBuild(context.Background(), dir, &out)
		if err != nil && strings.Contains(err.Error(), "no package.json here") {
			t.Fatalf("the backend's own folder was refused as not a backend's folder: %v", err)
		}
	})
}

// buildRefusal runs the build in dir and returns its refusal and its output; a
// build that does not refuse fails the test.
func buildRefusal(t *testing.T, dir string) (string, string) {
	t.Helper()
	var out bytes.Buffer
	err := runBuild(context.Background(), dir, &out)
	if err == nil {
		t.Fatalf("the build passed in %s, which has no package.json:\n%s", dir, out.String())
	}
	return err.Error(), out.String()
}

// wroteNothing fails when the build left npm's footprint in dir.
func wroteNothing(t *testing.T, dir string) {
	t.Helper()
	for _, p := range []string{"package-lock.json", "node_modules", "package.json"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err == nil {
			t.Errorf("the build wrote %s into a folder that is not a backend's", p)
		}
	}
}

func mustContain(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Errorf("%q is missing from:\n%s", want, s)
	}
}

func mustNotContain(t *testing.T, s, unwanted string) {
	t.Helper()
	if strings.Contains(s, unwanted) {
		t.Errorf("%q should not be in:\n%s", unwanted, s)
	}
}
