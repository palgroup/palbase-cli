package backend

// init.go — `palbase init`: a project, from the SDK it will compile against.
//
// The scaffold is NOT embedded in this binary, and that is the whole design. An
// embedded copy is a copy: it ages at the speed of CLI releases while the SDK
// moves at its own, and the first thing a new user meets is a decorator that was
// renamed two majors ago. Measured, more than once — the stale-example failure is
// the most expensive kind, because the person hitting it has no way to tell their
// mistake from ours.
//
// So the template ships INSIDE @palbase/backend, and init installs the package
// and copies from it. The scaffold and the SDK that compiles it are then the same
// version by construction, with no gate to keep them in step.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Args:  cobra.NoArgs,
		Short: "Scaffold a backend project here",
		Long: `Create a Palbase backend in this directory.

The scaffold comes from the @palbase/backend package itself, so what you get is
what the SDK you just installed actually compiles — not a copy of it from
whenever this CLI was released.

Then:

    palbase start     run it on this machine
    palbase link      point an app at it`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			version, err := newestPublishedSDK(cmd.Context())
			if err != nil {
				return err
			}
			return runInit(cmd.Context(), dir, backendPkg+"@"+version, cmd.OutOrStdout())
		},
	}
}

// newestPublishedSDK ASKS THE REGISTRY which SDK a new backend starts on.
//
// This binary carries no version, and that is the point: a number here goes
// stale at the next major and makes every SDK release a CLI release. The same
// reasoning already governs the scaffold FILES — they are copied out of the
// package rather than embedded — and the version had no business being the one
// thing still baked in.
//
// It cannot ask for `latest`. That tag is deliberately held on the v1 line,
// because v1 projects carry `"@palbase/backend": "latest"` in their own
// package.json and moving it would jump every one of them a major at their next
// install. So it asks the question `latest` used to answer: what is the newest
// version this package has.
//
// The version it returns decides only WHICH PACKAGE is fetched. What the new
// project then declares is the range in that package's own template — so the
// SDK names its own compatibility and nothing here has to agree with it.
func newestPublishedSDK(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "npm", "view", backendPkg, "versions", "--json")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not ask the registry which %s to install (%w) — `npm view %s versions` is the question, and it needs a network",
			backendPkg, err, backendPkg)
	}
	return pickNewestStable(out)
}

// pickNewestStable is the pure half, so the choosing is testable without a
// network: npm answers with a JSON array, or with a bare string when a package
// has exactly one version.
func pickNewestStable(raw []byte) (string, error) {
	var versions []string
	if err := json.Unmarshal(raw, &versions); err != nil {
		var single string
		if err2 := json.Unmarshal(raw, &single); err2 != nil {
			return "", fmt.Errorf("could not read the registry's version list: %w", err)
		}
		versions = []string{single}
	}

	newest := ""
	for _, v := range versions {
		// A prerelease is not something to hand somebody who typed `init`. npm
		// itself keeps them out of a plain range for the same reason.
		if strings.Contains(v, "-") {
			continue
		}
		if newest == "" || compareSemver(v, newest) > 0 {
			newest = v
		}
	}
	if newest == "" {
		return "", fmt.Errorf("the registry lists no released version of %s", backendPkg)
	}
	return newest, nil
}

// compareSemver orders two released versions. Prereleases are filtered out
// before this runs, so numeric major.minor.patch is the whole comparison —
// and a lexical sort is not it: "9.0.0" sorts above "18.0.0".
func compareSemver(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// spec is what npm is asked to install — `@palbase/backend@<newest released>`
// from the command, and a locally packed tarball from the test that proves this
// against a real package build rather than against a fixture.
func runInit(ctx context.Context, dir, spec string, out io.Writer) error {
	if err := refuseNonEmpty(dir); err != nil {
		return err
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("npm is not on PATH — a Palbase backend is an npm project, and the scaffold comes from the @palbase/backend package")
	}

	// A package.json FIRST, before npm is asked for anything.
	//
	// npm walks UP looking for one and installs into whatever it finds, so in a
	// directory with an ancestor package.json — a monorepo, or /tmp on this
	// machine — `npm install` answered "up to date" and wrote nothing here.
	// Measured 2026-08-17: init then refused with "ships no template", which is
	// true and points at the wrong thing entirely. Writing one first makes this
	// directory the project root, and the template's own package.json replaces
	// it a moment later.
	if err := seedPackageJSON(dir); err != nil {
		return err
	}

	fmt.Fprintf(out, "▸ installing %s\n", spec)
	install := exec.CommandContext(ctx, "npm", "install", "--no-audit", "--no-fund", spec)
	install.Dir = dir
	install.Stdout = io.Discard
	install.Stderr = &prefixed{w: out, prefix: "  "}
	if err := install.Run(); err != nil {
		return fmt.Errorf("install %s: %w", spec, err)
	}

	templateDir := filepath.Join(dir, "node_modules", "@palbase", "backend", "template")
	if _, err := os.Stat(templateDir); err != nil {
		version := installedBackendVersion(dir)
		return fmt.Errorf(
			"%s %s ships no template/ — this CLI scaffolds from the package rather than from a copy of its own, "+
				"so there is nothing to write. Install a newer SDK", backendPkg, orNone(version))
	}

	written, err := copyTemplate(templateDir, dir)
	if err != nil {
		return err
	}
	for _, path := range written {
		fmt.Fprintf(out, "  %s\n", path)
	}

	// The template carries no .gitignore, and it cannot: npm RENAMES a packed
	// .gitignore to .npmignore, which would then start excluding files from
	// inside the published template. So it is written here.
	if err := writeGitignore(dir); err != nil {
		return err
	}
	fmt.Fprintln(out, "  .gitignore")

	// A second install, now that the template's package.json is the project's:
	// the first one existed to fetch the template, this one resolves what the
	// template declares and writes the lockfile that pins it.
	fmt.Fprintln(out, "▸ resolving the project's dependencies")
	resolve := exec.CommandContext(ctx, "npm", "install", "--no-audit", "--no-fund")
	resolve.Dir = dir
	resolve.Stdout = io.Discard
	resolve.Stderr = &prefixed{w: out, prefix: "  "}
	if err := resolve.Run(); err != nil {
		return fmt.Errorf("resolve the project's dependencies: %w", err)
	}

	fmt.Fprintf(out, "\n▸ %s %s\n", backendPkg, orNone(installedBackendVersion(dir)))
	fmt.Fprintln(out, "  palbase start   run it on this machine")
	return nil
}

// refuseNonEmpty keeps init from writing into somebody's project.
//
// A fresh `git init` does not count as content: cloning an empty repository and
// scaffolding into it is the ordinary way to start, and refusing it would send
// people to a temporary directory and a `mv`.
func refuseNonEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var occupied []string
	for _, e := range entries {
		switch e.Name() {
		case ".git", ".gitignore", ".DS_Store", "README.md", "LICENSE":
			continue
		}
		occupied = append(occupied, e.Name())
	}
	if len(occupied) == 0 {
		return nil
	}
	shown := occupied
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return fmt.Errorf(
		"this directory already has %s in it — `palbase init` scaffolds into an empty one so it cannot overwrite your work",
		strings.Join(shown, ", "))
}

// copyTemplate copies the package's template/ into the project, returning the
// paths written in the order they were created.
func copyTemplate(from, to string) ([]string, error) {
	var written []string
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dest := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return err
		}
		written = append(written, rel)
		return nil
	})
	return written, err
}

// writeGitignore covers the three things a backend project must not commit: the
// dependencies, the CLI's local state, and logs.
//
// `.palbase/local.json` rather than `.palbase/` — project.json is committed on
// purpose, so a colleague who clones this reaches the same project.
func writeGitignore(dir string) error {
	const body = `node_modules/
.palbase/local.json
.palbase-staged-controllers/
*.log
`
	path := filepath.Join(dir, ".gitignore")
	if existing, err := os.ReadFile(path); err == nil {
		if strings.Contains(string(existing), "node_modules") {
			return nil
		}
		return os.WriteFile(path, append(existing, []byte("\n"+body)...), 0o644)
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// seedPackageJSON makes this directory a project root for npm's benefit.
func seedPackageJSON(dir string) error {
	path := filepath.Join(dir, "package.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	body := fmt.Sprintf("{\n  \"name\": %q,\n  \"private\": true\n}\n", filepath.Base(dir))
	return os.WriteFile(path, []byte(body), 0o644)
}
