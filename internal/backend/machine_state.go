package backend

// machine_state.go — WHERE THIS MACHINE'S OWN STATE LIVES, and it is not in the
// customer's repository.
//
// Two files used to sit in the checkout, gitignored:
//
//	.palbase/local.json  `palbase start`'s "right now, work here" switch
//	.palbase/plan.json   `palbase plan`'s measurement, read by the push that follows
//
// Both belong to THIS machine. The reason recorded for keeping them in the
// checkout was "every verb reads it" — and that is not a reason: every verb
// already knows the checkout root. What it cost was a repository that could
// never be clean: two files the CLI wrote, ignored forever, and one of them
// (`plan.json`) covered by no rule at all, so `link`'s closing line —
// "commit palbase/" — would have put a machine's plan into the shared history.
//
// The CLI already has a home for this, and it is documented in start.go:
//
//	~/.palbase/credentials.json  MACHINE-WIDE …
//	~/.palbase/stacks/<group>/   … NOT in the checkout: they are secrets, they
//	                             are per-machine, and a .env in a repository is
//	                             a .env somebody commits.
//
// The same sentence covers these two, so they moved next to them.
//
// Keyed by the checkout's ABSOLUTE path, hashed: the key must not depend on how
// the directory was spelled (`.` and the full path are the same project), and
// the directory name must not leak into a path somebody screenshots.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// machineStateHome is the seam the tests point somewhere else.
//
// It exists because the obvious alternative — `t.Setenv("HOME", …)` — is not
// local to one test. Measured: with a single such test in this package the
// suite stopped finishing (600s timeout, hung in
// `TestBuildIgnoresAConfigDirectoryEntirely`), while the same suite without it
// passes in 380s. Something downstream resolves a home-derived path once and
// keeps it, so a later test inherits a directory that no longer exists — the
// class this repository already names: a test that writes machine-global state
// leaves it to whatever runs next.
var machineStateHome = os.UserHomeDir

// machineStateDir returns (and creates) this checkout's private directory under
// the user's home.
//
// 0o700, like the credentials beside it: the file it will hold names a stack
// this machine can reach.
func machineStateDir(checkoutRoot string) (string, error) {
	home, err := machineStateHome()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(checkoutRoot)
	if err != nil {
		return "", err
	}
	// EvalSymlinks so a checkout reached through a symlink is the same project
	// as the one reached directly — `/tmp` is a symlink to `/private/tmp` on
	// macOS, and without this a `palbase start` in one spelling would be
	// invisible to a `palbase stop` in the other.
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = resolved
	}
	sum := sha256.Sum256([]byte(abs))
	dir := filepath.Join(home, ".palbase", "checkouts", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// LocalStatePath is where `palbase start` records the stack in front of you.
func LocalStatePath(checkoutRoot string) (string, error) {
	dir, err := machineStateDir(checkoutRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "local.json"), nil
}

// PlanStatePath is where `palbase plan` leaves the measurement `palbase push`
// reads back.
func PlanStatePath(checkoutRoot string) (string, error) {
	dir, err := machineStateDir(checkoutRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plan.json"), nil
}
