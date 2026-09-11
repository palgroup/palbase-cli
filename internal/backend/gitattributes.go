package backend

// gitattributes.go — GENERATED CODE SHOULD NOT BE READ AS A CHANGE.
//
// Everything under `palbase/environments/` is emitted by a generator from a
// contract somebody else approved: a spec fetch can move thousands of lines
// that nobody wrote and nobody should review. Marked `-diff` it collapses to one
// line in a pull request; marked `linguist-generated=true` it stops counting
// toward the repository's language statistics.
//
// THE FILE LIVES INSIDE THE DIRECTORY THIS CLI OWNS. Writing into the
// checkout's root `.gitattributes` would be this tool editing somebody else's
// configuration — the same mistake the retired xcconfig mechanism made, and the
// reason `palbase link` no longer touches a build system at all. Patterns in
// `palbase/.gitattributes` are resolved relative to `palbase/`, so they can
// never reach the application's own source.

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// generatedAttributes is what this CLI declares about its own products.
//
// The patterns are DERIVED from the layout rather than restated: a generated
// file is named by `GeneratedPath`, `SpecPath`, `RolesPath` and `PlistPath`, and
// a pattern list written by hand would go stale the first time one of them
// moved. Only the BASE names are used — a `**/` pattern matches them under
// whichever environment directory they land in.
func generatedAttributes() []string {
	seen := map[string]bool{}
	var lines []string
	add := func(p string) {
		base := path.Base(p)
		if base == "" || base == "." || seen[base] {
			return
		}
		seen[base] = true
		lines = append(lines, "**/"+base+" linguist-generated=true -diff")
	}
	const probe = "any"
	add(SpecPath(probe))
	add(RolesPath(probe))
	add(PlistPath(probe))
	for _, platform := range []string{"ios", "macos", webPlatform} {
		if p := GeneratedPath(probe, platform); p != "" {
			add(p)
		}
	}
	// The environment config leaf: generated beside the web client, and just as
	// much noise in a review diff.
	add(EnvDir(probe) + "/palbe.config.ts")
	// The barrels sit at the root of this directory, not under an environment.
	add(ClientBarrelPath())
	add(ConfigBarrelPath())
	return lines
}

// gitattributesHeader says who wrote the file and why, so the next person does
// not have to guess whether they may edit it.
const gitattributesHeader = "# Written by `palbase link`. These files are generated from your project's\n" +
	"# contract; marking them keeps them out of review diffs and language stats.\n"

// writeGitattributes writes the rules for this CLI's own directory.
//
// Idempotent by construction: the whole file is this CLI's, so it is written
// rather than appended to, and a re-link produces identical bytes.
func writeGitattributes(root string) error {
	dir := filepath.Join(root, RootDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := gitattributesHeader + strings.Join(generatedAttributes(), "\n") + "\n"
	return os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte(body), 0o644)
}
