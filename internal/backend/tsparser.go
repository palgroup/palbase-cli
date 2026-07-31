package backend

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// parserTSVersion pins the TypeScript the CLI's OWN controller parser runs on.
//
// The return-type binder and the throw analyzer (devjs/return_types.js,
// devjs/throw_analysis.js — shared verbatim with the deploy stager) call the TS
// compiler API (`ts.createSourceFile`, `ts.ScriptTarget`). That API only exists
// in TypeScript 5's CommonJS build: TypeScript 7 is the Go-native rewrite and
// its CJS entry exports `{ version, versionMajorMinor }` and NOTHING else. Since
// npm's `latest` dist-tag moved to 7.x, a plain `npm install typescript` in a new
// project makes the project's typescript unusable as our parser — so the CLI no
// longer borrows it. This parser is the CLI's tool, not the user's compiler:
// their `typescript` stays theirs (their `tsc --noEmit` can be 7.x), and we
// resolve our own, pinned, from the CLI tool cache.
const parserTSVersion = "5.9.3"

// parserTSHome is the CLI-owned tool cache root (~/.palbase/tools). A var only
// so tests can redirect it; production never changes it.
var parserTSHome = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".palbase", "tools"), nil
}

// ensureParserTS returns the node_modules dir holding the pinned TypeScript,
// installing it once per version into the CLI tool cache (never into the user's
// project — a `--no-save` install there would OVERWRITE the typescript their own
// scripts use). Returns "" when it can't be provisioned (no npm, offline first
// run): the caller then falls back to the project's resolution, and if that
// typescript has no parser API the harness prints an actionable error rather
// than `Cannot read properties of undefined (reading 'ES2022')`.
func ensureParserTS(w io.Writer) string {
	root, err := parserTSHome()
	if err != nil {
		return ""
	}
	// The version is IN the dir name, so "is it the right one?" is the same
	// question as "does it exist?" — no version sniffing, no upgrade path.
	prefix := filepath.Join(root, "typescript-"+parserTSVersion)
	mods := filepath.Join(prefix, "node_modules")
	if _, err := os.Stat(filepath.Join(mods, "typescript", "package.json")); err == nil {
		return mods
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return ""
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return ""
	}
	fmt.Fprintf(w, "→ installing typescript@%s (the CLI's controller parser, one-time; NOT added to your project) ...\n", parserTSVersion)
	cmd := exec.Command(npm, "install", "--no-save", "--silent", "--no-audit", "--no-fund",
		"--prefix", prefix, "typescript@"+parserTSVersion)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "warning: could not install typescript@%s (%v) — falling back to the project's typescript\n", parserTSVersion, err)
		return ""
	}
	if _, err := os.Stat(filepath.Join(mods, "typescript", "package.json")); err != nil {
		return ""
	}
	return mods
}

// devNodePath is the NODE_PATH the build checker runs with (`palbase
// serve` and the `palbase build`/push check mode): the CLI's pinned TypeScript
// FIRST, then the project's node_modules (@palbase/backend, esbuild, the user's
// deps).
//
// Order is load-bearing: the harness scripts live in a temp dir OUTSIDE the
// project, so their bare `require('typescript')` never walks into the project's
// node_modules — NODE_PATH is what resolves it, and NODE_PATH entries are tried
// in order. Everything else the harness requires is absent from the parser dir
// and therefore still resolves from the project.
func devNodePath(projectDir string, w io.Writer) string {
	project := filepath.Join(projectDir, "node_modules")
	if ts := ensureParserTS(w); ts != "" {
		return ts + string(os.PathListSeparator) + project
	}
	return project
}
