package backend

// backend_folder.go — `palbase build` asked somewhere that is not a backend's
// folder (palbase-cli#8).
//
// A backend's folder is the one with its package.json: that is where npm
// installs the SDK the controllers import, and where the deploy reads its test
// selection. `build` never asked. Its source check walks the whole tree, so run
// one folder too high — a workspace whose backend lives in `backend/` — it found
// the backend's TypeScript and went on to `npm install` in a folder npm has
// nothing to read in. npm answered ENOENT (exit 254) and left a
// package-lock.json behind, and the refusal said the SDK was not installed.
// Measured with 0.79.1: an AI agent read that as "the backend tools are broken"
// and blocked its task, while the same command inside `backend/` passed. One
// folder too LOW is the same mistake from the other side, and worse: npm walks
// up to the backend's package.json and installs THERE, then the build fails on
// something unrelated.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// notABackendFolder returns "" when dir holds a package.json, and otherwise the
// refusal `palbase build` answers with — naming the folder the backend is in
// when one is near, and the folder the TypeScript is under when none is.
func notABackendFolder(dir string) string {
	if exists(filepath.Join(dir, "package.json")) {
		return ""
	}
	why := fmt.Sprintf("no package.json here (%s) — this is not a backend's folder, so there is "+
		"nothing to install %s from and nothing to validate", dir, backendPkg)
	near := backendFoldersNear(dir)
	switch {
	case len(near) == 1:
		return fmt.Sprintf("%s.\nThe backend is in %s/: cd %s && palbase build", why, near[0], near[0])
	case len(near) > 1:
		return fmt.Sprintf("%s.\nBackends below this folder: %s/ — run palbase build in one of them "+
			"(cd %s && palbase build)", why, strings.Join(near, "/, "), near[0])
	}
	const remedy = "Run palbase build in the backend's own folder — the one with its package.json — " +
		"or `palbase init` to start one"
	if under := sourceFolder(dir); under != "" {
		return fmt.Sprintf("%s.\nThe TypeScript here is under %s/, and no folder near here has a "+
			"package.json that depends on %s. %s", why, under, backendPkg, remedy)
	}
	return why + ".\n" + remedy
}

// backendFoldersNear finds the backend folders around dir, as slash-separated
// paths relative to it: the nearest one ABOVE, up to the repository root, when
// dir sits inside a backend; otherwise every one up to two levels BELOW, sorted.
//
// A backend folder is one whose package.json depends on @palbase/backend — a
// package.json alone is every JavaScript project, a web app beside the backend
// included, and offering one of those would send the person to the next
// confusing refusal.
func backendFoldersNear(dir string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	repoRoot := func(p string) bool {
		_, err := os.Lstat(filepath.Join(p, ".git"))
		return err == nil
	}
	for at := filepath.Dir(abs); !repoRoot(abs); at = filepath.Dir(at) {
		if dependsOnBackend(at) {
			rel, err := filepath.Rel(abs, at)
			if err != nil {
				return nil
			}
			return []string{filepath.ToSlash(rel)}
		}
		if repoRoot(at) || filepath.Dir(at) == at {
			break
		}
	}
	var below []string
	_ = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == abs {
			return nil
		}
		if name := d.Name(); name == "node_modules" || name == "dist" || strings.HasPrefix(name, ".") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return filepath.SkipDir
		}
		rel = filepath.ToSlash(rel)
		if dependsOnBackend(path) {
			below = append(below, rel)
			return filepath.SkipDir
		}
		if strings.Count(rel, "/") >= 1 {
			return filepath.SkipDir
		}
		return nil
	})
	return below
}

// dependsOnBackend reports whether dir's package.json lists @palbase/backend
// among its dependencies or devDependencies.
func dependsOnBackend(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]any `json:"dependencies"`
		DevDependencies map[string]any `json:"devDependencies"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(string(raw), "\ufeff")), &pkg) != nil {
		return false
	}
	_, dep := pkg.Dependencies[backendPkg]
	_, dev := pkg.DevDependencies[backendPkg]
	return dep || dev
}

// sourceFolder names the top-level folder of dir that the first project source
// sits under, or "" when that source is in dir itself or there is none.
func sourceFolder(dir string) string {
	first := firstProjectSource(dir)
	if first == "" {
		return ""
	}
	rel, err := filepath.Rel(dir, first)
	if err != nil {
		return ""
	}
	top, _, nested := strings.Cut(filepath.ToSlash(rel), "/")
	if !nested {
		return ""
	}
	return top
}
