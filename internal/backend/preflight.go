package backend

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// importRe matches static ES import/export-from and require() specifiers.
// We only care about the specifier string; the rest of the statement is
// irrelevant for bundle-reachability.
var importRe = regexp.MustCompile(`(?:import|export)[^'"]*?from\s*['"]([^'"]+)['"]|require\(\s*['"]([^'"]+)['"]\s*\)`)

// preflightImports walks endpoints/ and verifies that every RELATIVE import
// (./ or ../) resolves to a path whose top-level project dir is in the bundle
// allow-list (so it will actually be uploaded). Bare-package imports (npm
// deps, e.g. "@palbase/backend") are ignored — the pod installs those from
// package.json. A relative import that points at a dir which won't ship (or
// escapes the project root) is reported with the offending file + the missing
// dir, BEFORE the upload — turning the old silent-drop-then-server-esbuild
// "Could not resolve" into an actionable local error.
func preflightImports(root string) error {
	endpointsDir := filepath.Join(root, "endpoints")
	if _, err := os.Stat(endpointsDir); os.IsNotExist(err) {
		return nil // no endpoints → nothing to validate
	}

	var problems []string
	err := filepath.Walk(endpointsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".js", ".jsx":
		default:
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fileDir := filepath.Dir(path)
		relFile, _ := filepath.Rel(root, path)
		for _, m := range importRe.FindAllStringSubmatch(string(src), -1) {
			spec := m[1]
			if spec == "" {
				spec = m[2] // require() form
			}
			if !strings.HasPrefix(spec, ".") {
				continue // bare package import — installed from package.json
			}
			if p := checkRelImport(root, fileDir, relFile, spec); p != "" {
				problems = append(problems, p)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("preflight: walk endpoints: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("backend import preflight failed:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// checkRelImport resolves a single relative import and returns a non-empty
// problem string if it won't reach the pod. Empty string = OK.
func checkRelImport(root, fileDir, relFile, spec string) string {
	// Resolve the import target relative to the importing file's dir.
	abs := filepath.Clean(filepath.Join(fileDir, spec))
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Sprintf("%s imports %q which escapes the project root — it cannot be bundled", relFile, spec)
	}

	// Top-level project dir of the resolved target.
	top := rel
	if idx := strings.IndexAny(rel, "/\\"); idx >= 0 {
		top = rel[:idx]
	}
	if _, ok := backendBundleAllowedRoots[top]; !ok {
		return fmt.Sprintf("%s imports %q but %q is not in the bundle allow-list (it will not be uploaded). Move the shared code under one of: endpoints, schema, lib, shared, src, utils, db.", relFile, spec, top)
	}

	// The dir must actually exist (esbuild resolves .ts/.js/.json + index.*).
	if !importTargetExists(abs) {
		return fmt.Sprintf("%s imports %q but no matching file exists (looked for %s with .ts/.tsx/.js/.jsx/.json or an index.* inside it)", relFile, spec, filepath.Join(top, "..."))
	}
	return ""
}

// importTargetExists mirrors esbuild's resolution: the exact path, the path
// with a source extension swapped in for the (possibly .js) spelling, or an
// index.* inside a directory.
func importTargetExists(abs string) bool {
	exts := []string{".ts", ".tsx", ".js", ".jsx", ".json"}
	// 1. Exact path as written.
	if fileExists(abs) {
		return true
	}
	// 2. Strip the written extension and try each source extension
	//    (covers `schema/index.js` → schema/index.ts).
	base := strings.TrimSuffix(abs, filepath.Ext(abs))
	for _, e := range exts {
		if fileExists(base + e) {
			return true
		}
	}
	// 3. Bare path + extension (no extension in the specifier).
	for _, e := range exts {
		if fileExists(abs + e) {
			return true
		}
	}
	// 4. Directory index.*
	for _, e := range exts {
		if fileExists(filepath.Join(abs, "index"+e)) {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
