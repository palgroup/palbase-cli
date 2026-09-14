package backend

// generated_paths.go — WHAT THIS CLI PUTS IN SOMEBODY'S PROJECT, in one place.
//
// Two lists, and the difference between them is the whole point. One names what
// a command writes into a checkout today, so the ignore file can cover it. The
// other names what a command USED to write, so the leftovers can be REMOVED —
// from the disk and from the ignore file that still speaks of them.
//
// The second list exists because the first one could only grow. `link` appends
// every `ours` entry to the customer's `.gitignore` on every run and has no way
// to take one back, so retiring a producer left its rule standing: a file
// telling every reader that this tool writes a directory it can no longer
// write. The ignore list's own history says the same thing from the other side —
// it named `.palbase-serve-controllers/`, "a name nothing has written for two
// renames", while the directory `build` actually created was the one nobody
// ignored.
//
// A rule for a path nothing produces is not harmless. It is the record claiming
// a mechanism still runs, and the next person to read it believes the record.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// generatedProjectPaths is EVERY path this CLI writes into someone's project
// that is not meant to be committed — one declaration, read by the scaffolder
// and by the rule that repairs an existing checkout.
//
// `ours` says whether the repair path adds this entry to a checkout that
// already has a .gitignore. `node_modules/` and `*.log` are the ecosystem's,
// not ours — every project already handles them, and appending them to
// somebody's curated file is noise. The distinction is DECLARED rather than
// derived from the string: a rule whose subject is guessed is a rule that
// measures something else.
var generatedProjectPaths = []struct {
	path, why string
	ours      bool
}{
	{"node_modules/", "dependencies", false},
	{"*.log", "logs", false},
}

// retiredProjectPaths is what this CLI USED to write into a checkout and does
// not write any more.
//
// Nothing here can be produced by this binary, so the cure for one found on
// disk is DELETION — an ignore rule would only hide a dead file for as long as
// the repository lives. The working trees open in the operating system's temp
// directory for the length of one command, the machine state moved to
// ~/.palbase/checkouts/<hash>/, and what was worth committing moved under the
// visible root.
//
// `reapRetiredArtifacts` deletes them and `takeBackRetiredIgnoreRules` un-writes
// their rules, which is what makes retiring a producer a complete act rather
// than half of one: fixing the code that produced a file does not remove the
// files it already produced.
//
// `keepIfTracked` marks the one entry that is not purely a product: the hidden
// root itself. Everything else goes whatever git thinks of it — a compiled
// bundle somebody committed by accident is still a compiled bundle — but
// `.palbase` once held the project's contract, and a person may have committed
// it. The distinction is DECLARED rather than derived from the name.
var retiredProjectPaths = []struct {
	path, why     string
	keepIfTracked bool
}{
	{path: ".palbase/esm", why: "the compiled bundle — built into a temp bundle root since 0.61.1"},
	{path: ".palbase/jobs", why: "the job manifest — same"},
	{path: ".palbase/hooks", why: "the hook manifest — same"},
	{path: stagedControllersDir, why: "`palbase build`'s staging tree; it stages inside the temp deploy tree, never the checkout"},
	{path: deployStagingDir, why: "the deploy stager's tree; it stages into a temp directory"},
	{path: linkStagePrefix + "*", why: "`palbase link`'s staging tree; it opens in the temp directory"},
	{path: ".palbase/local.json", why: "the stack in front of you — machine state, moved to ~/.palbase/checkouts/<hash>/"},
	{path: ".palbase/plan.json", why: "`palbase plan`'s measurement of THIS machine — same move"},
	{path: envTypesFile, why: "the generated declaration file; it is written under " + rootDir + "/ and committed"},
	{path: ".palbase", why: "the retired hidden root — " + rootDir + "/ replaced it; swept unless git tracks a file under it", keepIfTracked: true},
}

// THE VISIBLE ROOT IS NOT IN THIS LIST, AND THE ABSENCE IS THE POINT (D-008).
// `reapRetiredArtifacts` DELETES what it names, and on macOS and Windows
// `palbase` and `Palbase` are ONE directory — sweeping the retired spelling
// would delete the directory this CLI just filled. What the OLD visible layout
// left inside it is recognised by content (`LegacyMarkers`) and refused by
// `link`, never swept.
//
// THE HIDDEN ROOT IS, and that reverses an older rule on purpose. `.palbase` is a
// real second directory on every filesystem, nothing writes it any more, and
// leaving it for a person to find was measured as 36 directories / ~30 MB on one
// disk that no verb collected. It used to be untouchable because `project.json`
// lived in there; the rule is now narrowed to what that was protecting — a
// directory git TRACKS a file under is kept and named, an untracked one is
// deleted like any other product.

// gitignoreScaffold is the whole ignore file for a project that has none.
//
// EVERY entry, not just `ours`. The `ours` flag decides what may be APPENDED to
// a file somebody curated — `node_modules/` there would be noise, because a
// project with a curated ignore file already handles it. A checkout with NO
// ignore file has curated nothing, and the two were treated as the same case:
// `link` created a `.gitignore` for a JavaScript project that did not ignore
// `node_modules`, so the next `git add -A` staged every installed dependency.
// Measured on a fresh web checkout, 08.09.2026: 500+ files under
// `node_modules/@bufbuild` staged by a repository the CLI had just set up.
func gitignoreScaffold() string {
	var b strings.Builder
	for _, e := range generatedProjectPaths {
		b.WriteString(e.path)
		b.WriteString("\n")
	}
	return b.String()
}

// reapRetiredArtifacts deletes what an older CLI left in this checkout and
// returns what it would NOT delete: every `keepIfTracked` entry that exists and
// has a git-tracked file under it, by its declared path.
//
// Best effort by design: refusing a job somebody asked for because a dead
// directory would not delete trades a doable command for a tidier disk.
//
// `.palbase` ITSELF IS SWEPT NOW. It used to be spared because `project.json`
// and `openapi/` lived in there and had to survive; neither has been written
// there since the visible root replaced it. What still deserves to survive is
// what that rule was really about — a file somebody COMMITTED. A tracked
// `.palbase` is returned, never deleted, so its removal is a commit a person
// makes and reviews rather than a side effect behind a progress line. The
// caller names it.
func reapRetiredArtifacts(dir string) []string {
	var kept []string
	for _, e := range retiredProjectPaths {
		if e.keepIfTracked {
			if _, err := os.Lstat(filepath.Join(dir, e.path)); err != nil {
				continue
			}
			if gitTracks(dir, e.path) {
				kept = append(kept, e.path)
				continue
			}
		}
		if !strings.ContainsAny(e.path, "*?[") {
			_ = os.RemoveAll(filepath.Join(dir, e.path))
			continue
		}
		matches, err := filepath.Glob(filepath.Join(dir, e.path))
		if err != nil {
			continue
		}
		for _, m := range matches {
			_ = os.RemoveAll(m)
		}
	}
	return kept
}

// gitTracks reports whether git tracks at least one file at or under rel,
// asked from dir.
//
// NO ANSWER READS AS "NOT TRACKED". Not a repository, and no git on PATH, both
// mean nothing here can have been committed, so the entry is an ordinary
// product.
//
// The repository-LOCATING variables are removed from the child's environment.
// A git hook exports them relative to the repository root (`GIT_DIR=.git`,
// `GIT_INDEX_FILE`), and `palbase push` installs `palbase build` as a pre-push
// hook. For a backend living in a SUBDIRECTORY of its repository, `-C dir` then
// resolves them against the wrong directory, git answers "not a git
// repository", and a committed directory would read as untracked and be deleted.
//
// `:(icase)` because `.palbase` and `.Palbase` are one directory on macOS and
// Windows: a case-sensitive pathspec misses a tracked `.Palbase/project.json`
// that `os.RemoveAll(".palbase")` deletes. On a case-sensitive filesystem the
// wider match can only KEEP something, never delete it.
func gitTracks(dir, rel string) bool {
	cmd := exec.Command("git", "-C", dir, "ls-files", "-z", "--", ":(icase)"+rel)
	cmd.Env = withoutGitLocation(os.Environ())
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}

// gitLocationEnv are the variables that point git at a repository other than
// the one it would discover from its working directory.
var gitLocationEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_PREFIX",
}

func withoutGitLocation(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(gitLocationEnv, name) {
			kept = append(kept, kv)
		}
	}
	return kept
}

// isRetiredIgnoreRule reports whether a `.gitignore` line is one this CLI wrote
// for a producer that no longer exists.
//
// The rules were written with a trailing slash (`.palbase/esm/`) and the paths
// are declared without one, so the comparison is made on the trimmed form —
// otherwise the retirement would silently match nothing, which is the same
// defect as never having written it.
func isRetiredIgnoreRule(line string) bool {
	t := strings.TrimSuffix(strings.TrimSpace(line), "/")
	if t == "" {
		return false
	}
	for _, e := range retiredProjectPaths {
		if t == strings.TrimSuffix(e.path, "/") {
			return true
		}
	}
	return false
}
