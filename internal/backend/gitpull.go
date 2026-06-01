package backend

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MergeResult reports the outcome of merging a server pull into the local
// working tree.
type MergeResult struct {
	Conflicted      bool
	ConflictedFiles []string
}

// baseRef is the git ref tracking the server tree the local clone last synced
// from — the merge BASE for 3-way merges. remoteRef is the just-pulled server
// tree. Both live under refs/palbase/ so they don't clutter the user's branches
// or show up as normal refs in most tools.
const (
	baseRef   = "refs/palbase/base"
	remoteRef = "refs/palbase/remote"
)

// mergePulledCode lands a server pull (tar.gz) into dir as a git merge, so the
// local working tree is a real git repo (.git at the root) and conflicts surface
// as standard <<<<<<< markers that any git tool (Fork, VS Code, mergetool) can
// resolve. Uses the SYSTEM git (go-git can't do 3-way conflict merges).
//
// Flow:
//   - Fresh dir: git init, extract the server tree, commit on main, set base.
//   - Existing repo: commit local working changes, build the server tree as a
//     commit parented on the stored base (so it's a true 3-way), merge it into
//     main. On a clean merge, advance base to the new server commit.
//
// A conflict is NOT an error: markers are written, the repo is left in merging
// state, and MergeResult.Conflicted is set so the caller can guide the user.
func mergePulledCode(dir string, serverTar []byte, version string) (*MergeResult, error) {
	g := &gitRunner{dir: dir}

	// A dir that's already a git repo is only safe to merge into if it's OUR
	// clone (carries refs/palbase/base). A foreign repo (the user's app/monorepo)
	// must NOT be touched — refuse with guidance instead of clobbering history.
	if dirHasGit(dir) && !g.isPalbaseClone() {
		return nil, fmt.Errorf("this directory is already a git repo but not a palbase clone — "+
			"refusing to merge into it (would touch your history). Pull into a fresh directory, "+
			"or use `palbase pull --raw` to overwrite files without git. (dir: %s)", dir)
	}

	fresh := !dirHasGit(dir)
	if fresh {
		if err := g.run("init", "-q", "-b", "main"); err != nil {
			// Older git without -b: fall back to init + symbolic-ref.
			if err2 := g.run("init", "-q"); err2 != nil {
				return nil, fmt.Errorf("git init: %w", err2)
			}
			_ = g.run("symbolic-ref", "HEAD", "refs/heads/main")
		}
		if err := g.configIdentity(); err != nil {
			return nil, err
		}
	}

	// 1. Commit whatever is currently in the working tree on main, so local
	//    edits are a real commit we can merge against. (No-op if nothing staged.)
	if err := g.commitWorkingTree("palbase: local state before pull"); err != nil {
		return nil, err
	}

	// 2. Materialise the server tree as a commit on remoteRef. On a fresh repo
	//    there's no base yet; the server commit is parentless. Otherwise its
	//    parent is the stored base so the merge is a proper 3-way.
	serverCommit, err := g.commitServerTree(serverTar, version, fresh)
	if err != nil {
		return nil, err
	}

	if fresh {
		// First pull: just check the server tree out as the working tree and
		// set it as both HEAD and base. Nothing to merge.
		if err := g.run("reset", "-q", "--hard", serverCommit); err != nil {
			return nil, fmt.Errorf("seed working tree: %w", err)
		}
		if err := g.run("update-ref", baseRef, serverCommit); err != nil {
			return nil, fmt.Errorf("set base ref: %w", err)
		}
		return &MergeResult{}, nil
	}

	// 3. Merge the server commit into main. --no-edit/--no-ff keeps it scriptable.
	mergeErr := g.run("merge", "--no-edit", "-m",
		fmt.Sprintf("palbase: merge server %s", version), serverCommit)
	if mergeErr == nil {
		// Clean merge → server tree is the new base.
		if err := g.run("update-ref", baseRef, serverCommit); err != nil {
			return nil, fmt.Errorf("advance base ref: %w", err)
		}
		return &MergeResult{}, nil
	}

	// A non-zero merge exit is a conflict (the expected, non-error outcome) —
	// distinguish it from a real git failure by checking for conflicted paths.
	files, lsErr := g.conflictedFiles()
	if lsErr != nil {
		return nil, fmt.Errorf("git merge failed: %w", mergeErr)
	}
	if len(files) == 0 {
		// No conflicts reported but merge errored → a genuine failure.
		return nil, fmt.Errorf("git merge: %w", mergeErr)
	}
	// Leave the repo in merging state with markers for the user to resolve.
	// base is intentionally NOT advanced — it advances on the next clean state.
	return &MergeResult{Conflicted: true, ConflictedFiles: files}, nil
}

// gitRunner runs git in a fixed directory.
type gitRunner struct{ dir string }

func (g *gitRunner) run(args ...string) error {
	_, err := g.output(args...)
	return err
}

func (g *gitRunner) output(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.dir
	// Deterministic identity/env so commits succeed in CI and don't read the
	// user's global hooks/config in surprising ways.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=palbase", "GIT_AUTHOR_EMAIL=cli@palbase.dev",
		"GIT_COMMITTER_NAME=palbase", "GIT_COMMITTER_EMAIL=cli@palbase.dev",
	)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (g *gitRunner) configIdentity() error {
	if err := g.run("config", "user.name", "palbase"); err != nil {
		return err
	}
	return g.run("config", "user.email", "cli@palbase.dev")
}

// commitWorkingTree stages everything and commits if there's anything to
// commit. A no-op (clean tree, or nothing tracked yet) is fine.
func (g *gitRunner) commitWorkingTree(msg string) error {
	if err := g.run("add", "-A"); err != nil {
		return err
	}
	// `git diff --cached --quiet` exits 1 when there ARE staged changes.
	if err := g.run("diff", "--cached", "--quiet"); err == nil {
		return nil // nothing staged
	}
	return g.run("commit", "-q", "-m", msg)
}

// commitServerTree writes serverTar into a temp index + work area and commits
// it on remoteRef. When not fresh, the commit's parent is baseRef so a merge
// of remoteRef into main is a true 3-way against the common ancestor.
func (g *gitRunner) commitServerTree(serverTar []byte, version string, fresh bool) (string, error) {
	tmp, err := os.MkdirTemp("", "palbase-server-tree-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := extractTarGzReplace(serverTar, tmp); err != nil {
		return "", fmt.Errorf("extract server tree: %w", err)
	}

	// Build a tree object from the extracted files using a temp index that
	// points at the SAME object database as dir/.git (so the resulting tree/
	// commit are reachable for the merge).
	gitDir := filepath.Join(g.dir, ".git")
	idx := filepath.Join(tmp, ".palbase-index")
	env := append(os.Environ(),
		"GIT_DIR="+gitDir,
		"GIT_WORK_TREE="+tmp,
		"GIT_INDEX_FILE="+idx,
		"GIT_AUTHOR_NAME=palbase", "GIT_AUTHOR_EMAIL=cli@palbase.dev",
		"GIT_COMMITTER_NAME=palbase", "GIT_COMMITTER_EMAIL=cli@palbase.dev",
	)
	runEnv := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		cmd.Env = env
		var out, errb bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return strings.TrimSpace(out.String()), nil
	}

	if _, err := runEnv("add", "-A"); err != nil {
		return "", err
	}
	tree, err := runEnv("write-tree")
	if err != nil {
		return "", err
	}
	commitArgs := []string{"commit-tree", tree, "-m", "palbase: server " + version}
	if !fresh {
		if base, e := g.output("rev-parse", "--verify", "-q", baseRef); e == nil && strings.TrimSpace(base) != "" {
			commitArgs = append(commitArgs, "-p", strings.TrimSpace(base))
		}
	}
	commit, err := runEnv(commitArgs...)
	if err != nil {
		return "", err
	}
	if _, err := runEnv("update-ref", remoteRef, commit); err != nil {
		return "", err
	}
	return commit, nil
}

// conflictedFiles returns the working-tree paths with unresolved conflicts.
func (g *gitRunner) conflictedFiles() ([]string, error) {
	out, err := g.output("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

func dirHasGit(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// isPalbaseClone reports whether dir's git repo is one WE created — identified
// by the presence of our base ref. Distinguishes our clones from a user's own
// app/monorepo repo that merely happens to be the pull target.
func (g *gitRunner) isPalbaseClone() bool {
	out, err := g.output("rev-parse", "--verify", "-q", baseRef)
	return err == nil && strings.TrimSpace(out) != ""
}
