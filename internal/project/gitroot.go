package project

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// The LOCAL GIT INVARIANT (spec §4): there is exactly ONE containing Git
// repository.
//
//   - Standalone CLI creation initializes `.git` with `main` when no containing
//     repository exists.
//   - Inside a monorepo, Palbase REUSES the ancestor Git root and never creates a
//     nested `.git`. A nested repo is the failure this exists to prevent: the
//     monorepo's tooling stops seeing the backend subtree, `git push` pushes the
//     wrong thing, and the deploy webhook filters against a root that is not the
//     one the developer commits to.
//
// Environment selection lives in `.palbase/config.json`, never in `.git`.

// gitRunner runs a git command in dir and returns its trimmed stdout. Injected
// so the invariant is testable without forking a real git.
type gitRunner func(dir string, args ...string) (string, error)

func execGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// ensureGitRepo guarantees the local invariant for `dir` and reports what it
// did, on the writer (stderr — this is progress, not command output).
//
// It returns the Git root that now contains dir. A missing `git` binary is NOT
// fatal: the project is already created server-side, and refusing to finish over
// a missing local tool would leave the user with a provisioned project and no
// config. It warns and moves on.
func ensureGitRepo(git gitRunner, dir string, w io.Writer) (string, error) {
	if git == nil {
		git = execGit
	}

	// An ANCESTOR repo (the monorepo case) — reuse it, create nothing.
	if root, err := git(dir, "rev-parse", "--show-toplevel"); err == nil && root != "" {
		fmt.Fprintf(w, "using the existing git repository at %s (no nested .git created)\n", root)
		return root, nil
	}

	// No containing repository — this is a standalone project. Initialize with
	// `main`, because that is the branch the platform maps production to by
	// default and a repo whose first branch is `master` silently maps to nothing.
	if _, err := git(dir, "init", "-b", "main"); err != nil {
		fmt.Fprintf(w, "warning: could not initialize a git repository (%v) — run `git init -b main` yourself\n", err)
		return "", nil
	}
	fmt.Fprintf(w, "initialized a git repository (branch main)\n")
	return dir, nil
}
