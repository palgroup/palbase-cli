//go:build !unix

package backend

// terminatePID is a no-op on non-Unix platforms (Windows): the
// free-on-start path in freeDevPort is already gated to darwin/linux at
// runtime, so this is never reached there. It exists only so the package
// compiles for GOOS=windows, where syscall.Kill is undefined.
func terminatePID(pid int) error {
	return nil
}
