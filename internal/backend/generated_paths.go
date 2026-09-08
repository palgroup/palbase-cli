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
	"path/filepath"
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
	{".palbase/local.json", "the stack running on THIS machine; project.json is committed on purpose", true},
	{envTypesFile, "generated from this project's secrets on every build, like next-env.d.ts", true},
	{"*.log", "logs", false},
}

// retiredProjectPaths is what this CLI USED to write into a checkout and does
// not write any more.
//
// Every one of them now lives in the operating system's temp directory for the
// length of one command. Nothing here can be produced by this binary, so the
// cure for one found on disk is DELETION — an ignore rule would only hide a
// dead file for as long as the repository lives.
//
// `reapRetiredArtifacts` deletes them and `ensurePalbaseGitignored` un-writes
// their rules, which is what makes retiring a producer a complete act rather
// than half of one: fixing the code that produced a file does not remove the
// files it already produced.
var retiredProjectPaths = []struct {
	path, why string
}{
	{".palbase/esm", "the compiled bundle — built into a temp bundle root since 0.61.1"},
	{".palbase/jobs", "the job manifest — same"},
	{".palbase/hooks", "the hook manifest — same"},
	{stagedControllersDir, "`palbase build`'s staging tree; it stages inside the temp deploy tree, never the checkout"},
	{deployStagingDir, "the deploy stager's tree; it stages into a temp directory"},
	{linkStagePrefix + "*", "`palbase link`'s staging tree; it opens in the temp directory"},
}

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

// reapRetiredArtifacts deletes what an older CLI left in this checkout.
//
// Best effort by design: refusing a job somebody asked for because a dead
// directory would not delete trades a doable command for a tidier disk.
//
// `.palbase` ITSELF IS NEVER TOUCHED. `project.json` and `openapi/` are not
// products — they are the contract that lets a colleague clone the repository
// and build without logging in. Only the named subdirectories go.
func reapRetiredArtifacts(dir string) {
	for _, e := range retiredProjectPaths {
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
