package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// module_resolution.go — A TSCONFIG THAT CANNOT SEE `exports` CANNOT SEE THE TYPES (FR-007).
//
// @palbase/backend publishes its typed surfaces as package.json "exports"
// subpaths — `@palbase/backend/env`, `/stack`, `/test`, `/engine` — and
// `palbase/palbase-env.d.ts` augments two of them. TypeScript reads "exports"
// only under `bundler`, `node16` and `nodenext`. Under `node` (alias `node10`)
// and `classic` the subpath does not resolve, the augmentation targets a module
// the compiler never finds, and every `Secrets.get("…")` quietly types as
// nothing. Nothing else says so: the file exists, the build is green.

// unreachableModuleResolution answers a moduleResolution the tsconfig compiles
// under that does not read "exports" — its own, or one it inherits through
// `extends` — in the spelling found and the file it was found in (relative to
// dir, slash-separated), or "", "" when there is none.
//
// ABSENT IS NOT REFUSED: TypeScript then derives the mode from `module`, and
// guessing that derivation here would refuse projects that are fine. A
// tsconfig this cannot parse, and a base it cannot find, are left to the
// compiler, like includeBlindSpots.
//
// INHERITED IS SET (review-T013). includeBlindSpots can leave `extends`
// unfollowed because a base's `include` only ever widens what compiles; a
// base's moduleResolution is simply the one the compiler uses when the file
// names none, and not following it answered "absent" for exactly the project
// this gate exists to stop — `@tsconfig/node*` bases included.
func unreachableModuleResolution(dir string) (value, from string) {
	value, from = effectiveModuleResolution(filepath.Join(dir, "tsconfig.json"), map[string]bool{})
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "node", "node10", "classic":
		if rel, err := filepath.Rel(dir, from); err == nil {
			from = filepath.ToSlash(rel)
		}
		return value, from
	}
	return "", ""
}

// effectiveModuleResolution reads compilerOptions.moduleResolution from path,
// or — when path sets none — from what it extends, the LAST base first:
// TypeScript applies an `extends` array in order, so a later base wins.
//
// A file already visited answers nothing, which ends a cycle and cannot change
// an answer: a base reached twice was fully asked the first time.
func effectiveModuleResolution(path string, seen map[string]bool) (value, from string) {
	if seen[path] {
		return "", ""
	}
	seen[path] = true
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var cfg struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions struct {
			ModuleResolution string `json:"moduleResolution"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(stripJSONComments(raw), &cfg) != nil {
		return "", ""
	}
	if strings.TrimSpace(cfg.CompilerOptions.ModuleResolution) != "" {
		return cfg.CompilerOptions.ModuleResolution, path
	}
	var bases []string
	var one string
	if json.Unmarshal(cfg.Extends, &one) == nil && one != "" {
		bases = []string{one}
	} else if json.Unmarshal(cfg.Extends, &bases) != nil {
		return "", ""
	}
	for i := len(bases) - 1; i >= 0; i-- {
		base := resolveTSConfigExtends(filepath.Dir(path), bases[i])
		if base == "" {
			continue
		}
		if value, from = effectiveModuleResolution(base, seen); value != "" {
			return value, from
		}
	}
	return "", ""
}

// resolveTSConfigExtends finds the file an `extends` entry names, for the shapes
// projects write: a path (relative to the file that names it, or absolute;
// `.json` optional), or a package — `@tsconfig/node18/tsconfig.json`, or a bare
// name meaning its tsconfig.json — looked up in node_modules from fromDir
// upward. A base published only through package.json "exports" is not
// followed; it answers "" and the compiler reads it (FR-006 still measures the
// result).
func resolveTSConfigExtends(fromDir, spec string) string {
	candidates := func(p string) []string {
		if strings.HasSuffix(p, ".json") {
			return []string{p}
		}
		return []string{p, p + ".json", filepath.Join(p, "tsconfig.json")}
	}
	find := func(p string) string {
		for _, c := range candidates(p) {
			if info, err := os.Stat(c); err == nil && !info.IsDir() {
				return c
			}
		}
		return ""
	}
	if filepath.IsAbs(spec) {
		return find(spec)
	}
	if spec == "." || spec == ".." || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return find(filepath.Join(fromDir, filepath.FromSlash(spec)))
	}
	for d := fromDir; ; d = filepath.Dir(d) {
		if found := find(filepath.Join(d, "node_modules", filepath.FromSlash(spec))); found != "" {
			return found
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// reportUnreachableModuleResolution prints the refusal, names the file the value
// comes from and the value that works. Returns true when there was one — the
// same shape as reportIncludeBlindSpots, so runBuild can gather both before
// refusing.
func reportUnreachableModuleResolution(dir string, out io.Writer) bool {
	value, from := unreachableModuleResolution(dir)
	if value == "" {
		return false
	}
	if from == "tsconfig.json" {
		fmt.Fprintf(out, "✗ tsconfig.json sets \"moduleResolution\": %q, which does not read package.json \"exports\".\n", value)
	} else {
		fmt.Fprintf(out, "✗ tsconfig.json inherits \"moduleResolution\": %q from %s, which does not read package.json \"exports\".\n", value, from)
	}
	fmt.Fprintln(out, "  @palbase/backend publishes /env, /stack, /test and /engine as export subpaths, so under this")
	fmt.Fprintln(out, "  mode the generated types augment a module the compiler never finds.")
	fmt.Fprintln(out, "\n  Set \"moduleResolution\": \"bundler\" (or \"node16\" / \"nodenext\") in tsconfig.json.")
	return true
}
