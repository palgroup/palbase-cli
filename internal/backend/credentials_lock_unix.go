//go:build !windows

package backend

// credentials_lock_unix.go — one writer at a time on a shared machine.
//
// A developer box runs several of these at once: two agents in two panes, a
// `start` finishing while a `link` writes, a CI step and a shell. They share one
// HOME, so they share one credentials file, and a read-modify-write without a
// lock loses whichever write finished first — signing you out of a project you
// just linked, for no visible reason.
//
// flock, not a lock FILE somebody has to clean up: the advisory lock is released
// by the kernel when the process exits, so a crashed run leaves nothing behind
// for the next one to wonder about.
//
// Split by platform because `unix.Flock` does not exist on Windows and this file
// carried no build tag — so every Windows build has failed since it was added,
// and the first release attempt in three days is what said so.

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockCredentials takes an exclusive lock covering the credentials file and
// returns the release.
//
// The lock is held on a sibling `.lock` file rather than on the credentials file
// itself, because the write replaces that file by rename — a lock on an inode
// that is about to be unlinked protects nothing.
func lockCredentials(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
