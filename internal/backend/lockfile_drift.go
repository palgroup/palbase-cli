package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// lockfileDriftRefusal answers whether the SDK in this checkout's node_modules
// is the one its npm lockfile pins, and the sentence to refuse with when not.
//
// WHY A REFUSAL (palbase-cli#7 §4). `node_modules` at 39.1.1 while the lockfile
// pins 38.0.15 is an install nobody refreshed after a pull or a branch switch.
// push, plan and build all compiled against whatever was installed, so the
// artifact that shipped was built with an SDK nobody committed and nobody
// tested — and it declared that SDK to the platform, which moved the runtime
// image to it. `sdkPruneRefusal` already refuses drift that happens DURING
// build preparation; nothing compared the install with the lockfile BEFORE it.
// Same rule as there: a wrong-SDK artifact is wrong silently, so this refuses
// rather than warns.
//
// Only the npm lockfile is read, because npm is what installs a backend's
// dependencies here (`installNodeDeps`). No lockfile, an entry it does not
// carry, or an install that cannot be read is no claim at all — silence.
func lockfileDriftRefusal(dir string) string {
	installed := installedBackendVersion(dir)
	locked, lockfile := lockedBackendVersion(dir)
	if installed == "" || locked == "" || installed == locked {
		return ""
	}
	return fmt.Sprintf("%s %s is installed in node_modules, but %s pins %s — building now would "+
		"ship an artifact compiled against an SDK nobody committed. Run `npm ci` (it installs "+
		"exactly what the lockfile pins), or update the lockfile if %s is the one you mean",
		backendPkg, installed, lockfile, locked, installed)
}

// lockedBackendVersion finds the npm lockfile that governs dir — dir's own, or
// an npm workspace root above it, never past the repository root — and returns
// the version it pins for dir/node_modules/@palbase/backend.
func lockedBackendVersion(dir string) (version, lockfile string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	installedAt := filepath.Join(abs, "node_modules", "@palbase", "backend")
	for at := abs; ; at = filepath.Dir(at) {
		candidate := filepath.Join(at, "package-lock.json")
		if raw, err := os.ReadFile(candidate); err == nil {
			// The entry is keyed by the installed copy's path RELATIVE TO THE
			// LOCKFILE: `node_modules/@palbase/backend` beside it, or
			// `backend/node_modules/@palbase/backend` from a workspace root.
			rel, err := filepath.Rel(at, installedAt)
			if err != nil {
				return "", ""
			}
			var lock struct {
				Packages map[string]struct {
					Version string `json:"version"`
				} `json:"packages"`
				// lockfileVersion 1 has no `packages`; its top-level
				// dependencies are the ones installed beside it.
				Dependencies map[string]struct {
					Version string `json:"version"`
				} `json:"dependencies"`
			}
			if json.Unmarshal(raw, &lock) != nil {
				return "", ""
			}
			if entry, ok := lock.Packages[filepath.ToSlash(rel)]; ok {
				return entry.Version, candidate
			}
			if lock.Packages == nil && at == abs {
				return lock.Dependencies[backendPkg].Version, candidate
			}
			return "", ""
		}
		if _, err := os.Stat(filepath.Join(at, ".git")); err == nil || filepath.Dir(at) == at {
			return "", ""
		}
	}
}
