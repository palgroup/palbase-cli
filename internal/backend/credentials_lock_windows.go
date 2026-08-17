//go:build windows

package backend

// credentials_lock_windows.go — the same guarantee, through the API Windows has.
//
// See credentials_lock_unix.go for why the lock exists at all. The shape is
// identical: an exclusive lock on a sibling `.lock` file, released when the
// process exits even if it crashed, so nothing has to be cleaned up by hand.
//
// LockFileEx rather than flock, which Windows does not have. The lock is
// MANDATORY here where flock is advisory, which is stricter than we need and
// costs nothing — every writer of this file goes through this function.

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockCredentials takes an exclusive lock covering the credentials file and
// returns the release.
//
// The lock is held on a sibling `.lock` file rather than on the credentials file
// itself, because the write replaces that file by rename — a lock on a handle to
// a file that is about to be replaced protects nothing.
func lockCredentials(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	// Zero length fields mean "the whole file"; the overlapped struct is required
	// even though the handle is not opened for overlapped I/O.
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, ^uint32(0), ^uint32(0),
		&overlapped,
	); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &overlapped)
		_ = f.Close()
	}, nil
}
