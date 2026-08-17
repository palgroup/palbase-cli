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
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
			return runInit(cmd.Context(), dir, backendPkg+"@latest", cmd.OutOrStdout())
		},
	}
}

// spec is what npm is asked to install — `@palbase/backend@latest` from the
// command, and a locally packed tarball from the test that proves this against a
// real package build rather than against a fixture.
func runInit(ctx context.Context, dir, spec string, out io.Writer) error {
	if err := refuseNonEmpty(dir); err != nil {
		return err
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("npm is not on PATH — a Palbase backend is an npm project, and the scaffold comes from the @palbase/backend package")
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
