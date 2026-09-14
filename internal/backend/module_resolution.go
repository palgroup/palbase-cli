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

// unreachableModuleResolution answers the tsconfig's own spelling of a
// moduleResolution that does not read "exports", or "" when there is none.
//
// ABSENT IS NOT REFUSED: TypeScript then derives the mode from `module`, and
// guessing that derivation here would refuse projects that are fine. A
// tsconfig this cannot parse is left to the compiler, like includeBlindSpots.
func unreachableModuleResolution(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "tsconfig.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CompilerOptions struct {
			ModuleResolution string `json:"moduleResolution"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(stripJSONComments(raw), &cfg) != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(cfg.CompilerOptions.ModuleResolution)) {
	case "node", "node10", "classic":
		return cfg.CompilerOptions.ModuleResolution
	}
	return ""
}

// reportUnreachableModuleResolution prints the refusal and names the value that
// works. Returns true when there was one — the same shape as
// reportIncludeBlindSpots, so runBuild can gather both before refusing.
func reportUnreachableModuleResolution(dir string, out io.Writer) bool {
	value := unreachableModuleResolution(dir)
	if value == "" {
		return false
	}
	fmt.Fprintf(out, "✗ tsconfig.json sets \"moduleResolution\": %q, which does not read package.json \"exports\".\n", value)
	fmt.Fprintln(out, "  @palbase/backend publishes /env, /stack, /test and /engine as export subpaths, so under this")
	fmt.Fprintln(out, "  mode the generated types augment a module the compiler never finds.")
	fmt.Fprintln(out, "\n  Set \"moduleResolution\": \"bundler\" (or \"node16\" / \"nodenext\").")
	return true
}
