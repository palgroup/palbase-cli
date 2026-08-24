package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

// ErrDPoPKeyMissing is returned by LoadDPoPKey when neither the keyring
// nor the file fallback has a key. Callers treat it
// as "CLI not logged in" and trigger a fresh login.
var ErrDPoPKeyMissing = errors.New("dpop key not stored")

// StoreDPoPKey persists the private JWK. Prefers the OS keyring; falls
// back to a 0600 file under ~/.palbase/ when the keyring is unavailable
// or PALBASE_NO_KEYRING=1 is explicitly set (CI / headless sandboxes).
//
// When the file fallback is used a warning is emitted to stderr so the
// operator knows the CLI is not using keyring protection for this session.
func StoreDPoPKey(key *DPoPKey) error {
	raw, err := key.marshalPrivateJWK()
	if err != nil {
		return err
	}

	if useFileFallback() {
		return writeKeyFile(raw)
	}

	if err := keyring.Set(dpopKeyService, keyringAccount(), string(raw)); err != nil {
		// Fall through to the file fallback with a visible warning. Some
		// Linux boxes have libsecret but no unlocked keyring; erroring
		// here would make `palbase login` impossible on CI images.
		fmt.Fprintf(os.Stderr,
			"palbase: OS keyring unavailable (%v); falling back to %s permission 0600\n",
			err, fileFallbackHint())
		return writeKeyFile(raw)
	}
	return nil
}

// LoadDPoPKey reads the private JWK back. Checks the keyring first, then
// the file fallback. Returns ErrDPoPKeyMissing if neither has a key.
func LoadDPoPKey() (*DPoPKey, error) {
	if !useFileFallback() {
		// keyring.Get can BLOCK indefinitely when the OS keychain syscall is
		// unavailable (a sandboxed/headless environment, e.g. the Claude Code
		// sandbox, where the macOS Keychain access hangs instead of erroring).
		// Bound it: if the keyring doesn't answer quickly, fall through to the
		// file fallback rather than hanging the whole CLI. The Set path already
		// degrades to the file fallback; this gives Get the same resilience.
		raw, err := keyringGetWithTimeout(dpopKeyService, keyringAccount(), keyringTimeout)
		if err == nil {
			return loadPrivateJWK([]byte(raw))
		}
		if !errors.Is(err, keyring.ErrNotFound) {
			// Keyring present but failed for another reason (locked keyring,
			// timeout in a sandbox) — try the file fallback rather than
			// reporting an error that blocks login. No warning: this branch
			// also fires on locked keyrings during normal startup.
			_ = err
		}
	}

	path, err := fileFallbackPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrDPoPKeyMissing
		}
		return nil, fmt.Errorf("read dpop key file: %w", err)
	}
	return loadPrivateJWK(raw)
}

// DeleteDPoPKey purges the stored key from both the keyring and the file
// fallback. Safe to call when no key is stored. Used by `palbase logout`
// and by key-rotation paths.
func DeleteDPoPKey() error {
	var firstErr error
	if !useFileFallback() {
		if err := keyring.Delete(dpopKeyService, keyringAccount()); err != nil &&
			!errors.Is(err, keyring.ErrNotFound) {
			firstErr = err
		}
	}
	path, err := fileFallbackPath()
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		if firstErr == nil {
			firstErr = fmt.Errorf("remove dpop key file: %w", err)
		}
	}
	return firstErr
}

// useFileFallback returns true when the explicit opt-out env is set.
func useFileFallback() bool {
	return os.Getenv("PALBASE_NO_KEYRING") == "1"
}

func writeKeyFile(raw []byte) error {
	path, err := fileFallbackPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(path, raw, 0o600)
}

func fileFallbackHint() string {
	path, err := fileFallbackPath()
	if err != nil {
		return "~/.palbase/dpop-key.jwk"
	}
	return path
}

// keyringTimeout bounds a single keyring.Get. The keychain answers in
// milliseconds when reachable; a multi-second wait means the syscall is wedged
// (sandbox/headless), so we cut over to the file fallback instead of hanging.
const keyringTimeout = 3 * time.Second

// keyringGetWithTimeout runs keyring.Get on a goroutine and returns either its
// result or a timeout error. The goroutine may outlive the timeout (a truly
// stuck syscall can't be cancelled), but it's a single detached read that does
// not block the caller — the CLI proceeds via the file fallback.
func keyringGetWithTimeout(service, account string, timeout time.Duration) (string, error) {
	return getWithTimeout(func() (string, error) { return keyring.Get(service, account) }, timeout)
}

// getWithTimeout is the testable core: run get() on a goroutine, return its
// result or a timeout error if it doesn't answer within `timeout`.
func getWithTimeout(get func() (string, error), timeout time.Duration) (string, error) {
	type result struct {
		val string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := get()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.val, r.err
	case <-time.After(timeout):
		return "", fmt.Errorf("keyring read timed out after %s (keychain unavailable?)", timeout)
	}
}
