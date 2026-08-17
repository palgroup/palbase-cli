package backend

// pull_guard.go — `palbase pull` never lands on top of work you have not saved.
//
// Pull overwrites the backend in this directory with the one the project is
// serving. That is exactly what it is for, and exactly why it has to look first:
// the case where somebody runs it is "let me get the deployed version", and the
// case where it hurts is "…and I had unsaved edits". Git already knows the
// difference, so this asks it.
//
// Refusing is the whole feature. There is no --force here: the escape hatch is
// `git stash` or a commit, and both are things the person can undo.

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// refuseDirtyTree reports the paths that would be overwritten, or nil when the
// tree is clean.
//
// A checkout that is not a git repository at all is not refused: there is
// nothing to protect and nothing to stash, and demanding a repository would make
// `pull` unusable in the one place it is most useful — a directory somebody just
// created to look at what is deployed.
func refuseDirtyTree(dir string) error {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// "Not a repository" is the one failure that means there is nothing to
		// protect. Everything else — a held index.lock, a permission error, a
		// broken object store — means this could not LOOK, and answering "clean"
		// to that is how a pull overwrites the work it exists to guard.
		message := stderr.String()
		switch {
		case strings.Contains(message, "not a git repository"),
			errors.Is(err, exec.ErrNotFound):
			return nil
		}
		return fmt.Errorf(
			"could not check this checkout for unsaved changes (%s) — refusing to overwrite it.\n"+
				"Fix that, or move the directory aside and pull into a fresh one",
			strings.TrimSpace(firstLine(message+err.Error())))
	}
	dirty := make([]string, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "??") {
			// Untracked files are not overwritten by a pull that writes the
			// project's own files, and refusing on them would mean refusing in
			// any directory with a stray note in it.
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 2 {
			dirty = append(dirty, fields[len(fields)-1])
		}
	}
	if len(dirty) == 0 {
		return nil
	}

	shown := dirty
	if len(shown) > 10 {
		shown = shown[:10]
	}
	more := ""
	if len(dirty) > len(shown) {
		more = fmt.Sprintf("\n  …and %d more", len(dirty)-len(shown))
	}
	return fmt.Errorf(
		"this checkout has changes that a pull would overwrite:\n  %s%s\n"+
			"Commit them, or `git stash`, then pull",
		strings.Join(shown, "\n  "), more)
}
