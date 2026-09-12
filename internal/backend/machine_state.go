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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
// ASKING WHERE A FILE GOES MUST NOT CREATE ANYTHING. This used to end in
// `os.MkdirAll`, so every caller that merely wanted the PATH left a directory
// behind — measured live on 11.09.2026: 823 of them under
// `~/.palbase/checkouts/`, one per checkout anything had ever asked about,
// including every temp directory the test suite used and every project since
// deleted. The key is a hash of an absolute path, so a dead one can never be
// named again: it is not garbage that gets reused, it is garbage forever.
//
// The writers create it (`ensureMachineStateDir`), because a write knows it is
// a write.
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
	return filepath.Join(home, ".palbase", "checkouts", hex.EncodeToString(sum[:8])), nil
}

// ensureMachineStateDir creates the directory for a path a writer is about to
// use. 0o700, like the credentials beside it: what it will hold names a stack
// this machine can reach.
func ensureMachineStateDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o700)
}

// checkoutsRoot is where every checkout's private directory lives.
func checkoutsRoot() (string, error) {
	home, err := machineStateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".palbase", "checkouts"), nil
}

// reapDeadCheckoutState removes the records of checkouts that no longer exist.
//
// Best effort, and deliberately: it runs beside work somebody is waiting for,
// and a directory it could not remove is not worth displacing their result. The
// record it deletes belongs to a path that is gone, so there is nothing to lose
// — and nothing to find, since the key is a one-way hash of that path.
//
// Each directory carries its own `origin` file naming the checkout it belongs
// to. Without it the hash could never be reversed and a dead record could never
// be recognised — which is how 823 of them accumulated unnoticed.
// ONCE PER PROCESS, not once per write.
//
// A CLI process runs ONE command, so collecting dead records once is exactly the
// right frequency — and calling it from every `WritePlanFile`/`WriteLocalTarget`
// made the work quadratic in the number of records: measured in this package's
// own suite, which writes state hundreds of times, the run went from ~390s to
// over 600s and tripped the test timeout. The users' directory grows too.
var reapOnce sync.Once

func reapDeadCheckoutState() { reapOnce.Do(reapDeadCheckoutStateNow) }

func reapDeadCheckoutStateNow() {
	root, err := checkoutsRoot()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())

		// AN EMPTY STATE DIRECTORY IS LITTER, whatever wrote it. A write always
		// leaves a file, so an empty one can only have come from a call that
		// merely ASKED for the path — the defect above. This is the other half
		// of that fix: correcting the code that produced them does not remove
		// the ones already on disk. Measured 11.09.2026: 726 of 879.
		if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) == 0 {
			_ = os.Remove(dir)
			continue
		}

		origin, readErr := os.ReadFile(filepath.Join(dir, originFile))
		if readErr != nil {
			continue // written by a CLI that did not record its origin; leave it
		}
		// ONLY "IT IS NOT THERE" IS A REASON TO DELETE. Anything else — a
		// permission error, an unmounted external disk, a network share that is
		// briefly unreachable — says nothing about whether the checkout exists.
		// Treating every stat error as absence deletes the state of a project
		// that is merely on a disk nobody plugged in: the next `push` then says
		// "no plan for this checkout" and `stop` cannot find a running stack.
		if _, statErr := os.Stat(strings.TrimSpace(string(origin))); !errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

// originFile records WHICH checkout a state directory belongs to, so a dead one
// can be recognised. The directory name is a hash and answers nothing.
const originFile = "origin"

// rememberOrigin writes that record beside the state a writer is creating.
func rememberOrigin(dir, checkoutRoot string) {
	abs, err := filepath.Abs(checkoutRoot)
	if err != nil {
		return
	}
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = resolved
	}
	_ = os.WriteFile(filepath.Join(dir, originFile), []byte(abs+"\n"), 0o600)
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

// ── WHICH ENVIRONMENT THIS CHECKOUT ACTS ON ─────────────────────────────────
//
// THE MAP IS THE SERVER'S; THE CHOICE IS THIS MACHINE'S.
//
// What environments a project HAS is a fact the control plane owns, and
// mirroring it into the repository would be a second source of truth that goes
// stale the first time somebody adds one. What this checkout is CURRENTLY
// pointed at is a fact about this machine, and it is deliberately NOT
// committed: Firebase's maintainer wrote the reason down and it is this
// project's scenario exactly — "if it were committed into version control you
// could accidentally switch someone from 'staging' to 'production' instantly".
//
// So it lives beside the other per-checkout state under `~/.palbase/checkouts/`,
// keyed by the hash of the checkout's absolute path, and `palbase env use` is
// the only thing that writes it.

// Selection is this machine's remembered environment for one checkout.
type Selection struct {
	// Project is the product this selection belongs to, and it is the whole
	// defence against the one failure mode a remembered choice has: a checkout
	// relinked to a DIFFERENT project must not inherit an address from the
	// project it left. ResolveFor drops a selection whose project does not
	// match the committed record.
	Project string `json:"project"`
	Env     string `json:"env"`
	// Ref is carried so resolving costs no network call: the address is
	// derived from it directly.
	Ref string `json:"ref"`
}

// SelectionPath is where this checkout's selection lives. It CREATES NOTHING —
// asking where a file goes must not leave a directory behind, a defect this
// package has already paid for once (823 dead directories, measured).
func SelectionPath(checkoutRoot string) (string, error) {
	dir, err := machineStateDir(checkoutRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "selection.json"), nil
}

// ReadSelection answers what this checkout last selected, or an error when
// nothing has been selected. "Nothing selected" is a legal state — it is what
// every fresh clone is in — so the caller falls through to the next rule rather
// than failing.
func ReadSelection(checkoutRoot string) (Selection, error) {
	path, err := SelectionPath(checkoutRoot)
	if err != nil {
		return Selection{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Selection{}, err
	}
	var s Selection
	if err := json.Unmarshal(raw, &s); err != nil {
		return Selection{}, fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

// WriteSelection remembers one environment for this checkout. The WRITER
// creates the directory, because a write knows it is a write.
func WriteSelection(checkoutRoot string, s Selection) error {
	path, err := SelectionPath(checkoutRoot)
	if err != nil {
		return err
	}
	blob, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureMachineStateDir(path); err != nil {
		return err
	}
	rememberOrigin(filepath.Dir(path), checkoutRoot)
	return os.WriteFile(path, append(blob, '\n'), 0o600)
}
