// Package hook centralizes the git pre-push hook the CLI installs into a
// github-mode project: a single v3 body (marker + `palbase build`) plus Ensure(),
// the best-effort retrofit that writes/upgrades it.
//
// v3 dropped the second half, `palbase db check`. It asked whether db/schema.ts
// had changes no migration covered — a question with no meaning once the schema is
// declarative: there are no migration files, and the deploy applies the plan
// itself. `palbase build` stays because it is not part of that rail; it is the
// local mirror of the gate that refuses a deploy collecting zero endpoints, and it
// catches a broken controller in seconds rather than after a push.
//
// THE SCHEMA IS STILL GATED, and by `palbase build` rather than by a test in this
// file. Build runs the same bundle-and-evaluate the deploy runs over `db/` — every
// schema file, not one named path — so the one-file-per-schema cutover reached
// this hook without the hook changing. A `-f <file>` guard here would have been
// the thing that did NOT reach it: it would still be naming db/schema.ts and
// quietly never firing.
//
// The hook is a fast LOCAL feedback loop, not the real gate — `command -v
// palbase || exit 0` makes it a no-op on a CLI-less machine, `--no-verify`
// bypasses it, and the server always gates the actual deploy. Ensure is called
// from the deploy-lifecycle touchpoints (push/clone/serve, github mode only) so
// existing projects self-heal to v2 on first contact; there is no
// `install-hook` command (deployment is fully automatic).
//
// The same body is duplicated in the orchestrator's repo template
// (modules/orchestrator/internal/activities/backend_template/hooks/pre-push) —
// they live in separate submodules with no cross-repo import, so the marker
// version neutralizes any drift: a stale template still gets pulled to v2 by
// the first CLI touchpoint.
package hook

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Version is the embedded hook body's version. Ensure upgrades any lower/known
// version to this; bump it (and Body) together when the body changes.
const Version = 3

// Body is the v2 pre-push hook — marker on line 2 so Ensure can identify it,
// then `palbase build` (the deploy-validation twin) and the db-drift check,
// both no-ops without the CLI on PATH.
const Body = `#!/bin/sh
# palbase-hook: v3
# Managed by palbase — the CLI updates this file automatically on version bumps (push/serve/clone).
# Runs the same validation the deploy runs, so a broken push is caught locally.
# Bypass: git push --no-verify (the server still gates the deploy).
command -v palbase >/dev/null 2>&1 || exit 0

palbase build || {
  echo "" >&2
  echo "✗ palbase build failed — this push would produce a FAILED deploy." >&2
  echo "  Fix the errors above and push again. (bypass: git push --no-verify)" >&2
  exit 1
}
exit 0
`

// knownV1Bodies are the two marker-less v1 hook bodies the platform shipped
// before v2: the CLI's old `db.go` const and the orchestrator template file
// (they differ only in a comment line). Ensure treats an exact match as
// "ours, old" and upgrades it to v2 so the marker-less v1 fleet self-heals.
//
// ‼️ THESE ARE FROZEN BYTES, NOT TEXT. They mention `db/schema.ts` and
// `palbase db check` — both retired — and they must KEEP mentioning them. This
// is not code that runs and not a message anybody reads: it is the fingerprint
// of a file already sitting on other people's machines. Rewriting it to the
// current file name is the one edit that breaks the thing it looks like it is
// fixing — the match stops, Ensure classifies those hooks as "foreign", and the
// fleet keeps a pre-push hook calling a verb that no longer exists, forever.
//
// A rename sweep will want to touch them. TestTheV1FingerprintsAreFrozen pins
// them by hash so the sweep fails loudly instead of succeeding quietly.
var knownV1Bodies = []string{
	// db.go prePushHook const.
	`#!/bin/sh
# palbase pre-push: block a push when db/schema.ts has changes not yet captured
# in a migration. Bypass with ` + "`git push --no-verify`" + ` (the deploy-time
# drift gate is the backstop). Requires the palbase CLI on PATH.
if [ -f db/schema.ts ]; then
  if command -v palbase >/dev/null 2>&1; then
    palbase db check || {
      echo "" >&2
      echo "✗ db/schema.ts has changes not covered by a migration." >&2
      echo "  Run: palbase db diff -f <name>   (generates db/migrations/<ts>_<name>.sql)" >&2
      echo "  Then: git add db/migrations && git commit && git push" >&2
      echo "  (bypass: git push --no-verify — the deploy will still gate it)" >&2
      exit 1
    }
  fi
fi
exit 0
`,
	// orchestrator backend_template/hooks/pre-push (differs only in the header comment).
	`#!/bin/sh
# palbase pre-push: block a push when db/schema.ts has schema changes not yet
# captured in a migration. Bypass with ` + "`git push --no-verify`" + ` (the deploy-time
# drift gate is the backstop). Requires the palbase CLI on PATH.
if [ -f db/schema.ts ]; then
  if command -v palbase >/dev/null 2>&1; then
    palbase db check || {
      echo "" >&2
      echo "✗ db/schema.ts has changes not covered by a migration." >&2
      echo "  Run: palbase db diff -f <name>   (generates db/migrations/<ts>_<name>.sql)" >&2
      echo "  Then: git add db/migrations && git commit && git push" >&2
      echo "  (bypass: git push --no-verify — the deploy will still gate it)" >&2
      exit 1
    }
  fi
fi
exit 0
`,
}

// markedVersion returns the version parsed from a "# palbase-hook: vN" marker
// line in body, and whether such a marker was found.
func markedVersion(body string) (int, bool) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		const prefix = "# palbase-hook: v"
		if strings.HasPrefix(line, prefix) {
			var v int
			if _, err := fmt.Sscanf(line[len(prefix):], "%d", &v); err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

// isKnownV1 reports whether body is byte-exactly one of the marker-less v1 hooks.
func isKnownV1(body string) bool {
	for _, v1 := range knownV1Bodies {
		if body == v1 {
			return true
		}
	}
	return false
}

// Status reports the pre-push hook state of repoDir for `palbase doctor`
// (report-only). It never writes anything. Return values:
//   - "wired-v2":   our v2 hook is present (state describes core.hooksPath).
//   - "outdated":   our older/v1 hook is present (a push/serve upgrades it).
//   - "missing":    no hooks/pre-push.
//   - "foreign":    a hook we didn't write is present.
//
// detail carries a short human suffix (e.g. the core.hooksPath value).
func Status(repoDir string) (state, detail string) {
	body, err := os.ReadFile(filepath.Join(repoDir, "hooks", "pre-push"))
	if err != nil {
		return "missing", ""
	}
	s := string(body)
	if v, ok := markedVersion(s); ok {
		if v >= Version && s != Body {
			// Marked current but byte-different — a drifted copy (e.g. an out-of-date
			// scaffold template). A push/serve rewrites it to the canonical Body.
			return "outdated", fmt.Sprintf("v%d but modified — a push/serve restores it", v)
		}
		if v >= Version {
			cfg := currentHooksPath(repoDir)
			if cfg == "" {
				return "wired-v2", "core.hooksPath not set — run 'palbase push' or 'palbase clone' once"
			}
			return "wired-v2", "core.hooksPath=" + cfg
		}
		return "outdated", fmt.Sprintf("v%d — a push/serve upgrades it to v%d", v, Version)
	}
	if isKnownV1(s) {
		return "outdated", "pre-v2 — a push/serve upgrades it"
	}
	return "foreign", "not palbase's — add 'palbase build' to it"
}

// Ensure installs or upgrades the pre-push hook in repoDir (best-effort). It is
// safe to call unconditionally: no .git → silent no-op; already-current → no-op;
// a foreign hook or a non-`hooks` core.hooksPath (husky etc.) → left untouched
// with a one-line note. Any fs/git error only warns — Ensure NEVER returns an
// error, so a caller (push/clone/serve) never fails because hook wiring did.
func Ensure(repoDir string, out io.Writer) {
	if out == nil {
		out = os.Stdout
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return // not a git repo → nothing to wire
	}

	hookPath := filepath.Join(repoDir, "hooks", "pre-push")
	existing, err := os.ReadFile(hookPath)
	switch {
	case err == nil:
		body := string(existing)
		if v, ok := markedVersion(body); ok {
			if v >= Version && body == Body {
				return // ours + current + byte-identical → no-op
			}
			// Ours, but either an older marker OR a marked-current body that has
			// DRIFTED from Body. The drift case matters because the orchestrator's
			// scaffold template ships its own copy of this hook: without the body
			// compare, a template that diverged would be pinned into every scaffolded
			// repo forever and nothing would ever heal it (the comment saying "keep
			// these two in sync" was the only thing holding the contract). Rewriting
			// makes Body the single source of truth in practice, not just on paper.
		} else if !isKnownV1(body) {
			// A hook we didn't write — never clobber the user's own.
			fmt.Fprintln(out, "note: hooks/pre-push exists but isn't palbase's — add 'palbase build' to it to catch deploy failures before pushing")
			return
		}
		// marked-older or known-v1 → overwrite with v2.
		if werr := writeHook(hookPath); werr != nil {
			fmt.Fprintf(out, "note: could not update hooks/pre-push (%v)\n", werr)
			return
		}
		fmt.Fprintln(out, "✓ updated hooks/pre-push to v2 — commit it to share with your team")

	case os.IsNotExist(err):
		if werr := writeHook(hookPath); werr != nil {
			fmt.Fprintf(out, "note: could not install hooks/pre-push (%v)\n", werr)
			return
		}
		fmt.Fprintln(out, "✓ installed hooks/pre-push (deploy validation gate) — commit it to share with your team")

	default:
		fmt.Fprintf(out, "note: could not read hooks/pre-push (%v)\n", err)
		return
	}

	// Point git at hooks/ — but never override a user's own hooksPath (husky
	// sets .husky). Only set it when unset or already "hooks".
	current := currentHooksPath(repoDir)
	if current == "" {
		if err := setHooksPath(repoDir); err != nil {
			fmt.Fprintf(out, "note: could not set core.hooksPath=hooks (%v)\n", err)
		}
		return
	}
	if current != "hooks" {
		fmt.Fprintf(out, "note: core.hooksPath is %s; add 'palbase build' to your pre-push hook\n", current)
	}
}

// writeHook writes the v2 body to path (0755), creating hooks/ if needed.
func writeHook(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(Body), 0o755)
}

// currentHooksPath returns git's configured core.hooksPath, or "" when unset
// (or git is unavailable — treated as unset, best-effort).
func currentHooksPath(repoDir string) string {
	out, err := exec.Command("git", "-C", repoDir, "config", "--get", "core.hooksPath").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// setHooksPath points git at the repo-local hooks/ dir.
func setHooksPath(repoDir string) error {
	return exec.Command("git", "-C", repoDir, "config", "core.hooksPath", "hooks").Run()
}
