//go:build unix

package backend

import "syscall"

// terminatePID sends SIGTERM to pid (Unix). syscall.Kill only exists on
// Unix-like platforms, which is why this lives behind the `unix` build
// tag — the Windows build gets the no-op stub in freeport_other.go so the
// cross-compiled release binary still builds. freeDevPort already gates
// the whole free-on-start path to darwin/linux at runtime; this split is
// purely so the package compiles for GOOS=windows.
func terminatePID(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
