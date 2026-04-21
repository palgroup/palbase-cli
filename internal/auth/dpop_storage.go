package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// ErrDPoPKeyMissing is returned by LoadDPoPKey when neither the keyring
// nor the file fallback has a key for the given mode. Callers treat it
// as "CLI not logged in" and trigger a fresh login.
var ErrDPoPKeyMissing = errors.New("dpop key not stored")

// StoreDPoPKey persists the private JWK. Prefers the OS keyring; falls
// back to a 0600 file under ~/.palbase/ when the keyring is unavailable
// or PALBASE_NO_KEYRING=1 is explicitly set (CI / headless sandboxes).
//
// When the file fallback is used a warning is emitted to stderr so the
// operator knows the CLI is not using keyring protection for this session.
func StoreDPoPKey(mode string, key *DPoPKey) error {
	raw, err := key.marshalPrivateJWK()
	if err != nil {
		return err
	}

	if useFileFallback() {
		return writeKeyFile(mode, raw)
	}

	if err := keyring.Set(dpopKeyService, keyringAccount(mode), string(raw)); err != nil {
		// Fall through to the file fallback with a visible warning. Some
		// Linux boxes have libsecret but no unlocked keyring; erroring
		// here would make `palbase login` impossible on CI images.
		fmt.Fprintf(os.Stderr,
			"palbase: OS keyring unavailable (%v); falling back to %s permission 0600\n",
			err, fileFallbackHint(mode))
		return writeKeyFile(mode, raw)
	}
	return nil
}

// LoadDPoPKey reads the private JWK back. Checks the keyring first, then
// the file fallback. Returns ErrDPoPKeyMissing if neither has a key.
func LoadDPoPKey(mode string) (*DPoPKey, error) {
	if !useFileFallback() {
		raw, err := keyring.Get(dpopKeyService, keyringAccount(mode))
		if err == nil {
			return loadPrivateJWK([]byte(raw))
		}
		if !errors.Is(err, keyring.ErrNotFound) {
			// Keyring present but failed for another reason — try the file
			// fallback rather than reporting an error that blocks login.
			// We don't warn here because this branch also fires on locked
			// keyrings during normal startup; the warning would be noisy.
			_ = err
		}
	}

	path, err := fileFallbackPath(mode)
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
func DeleteDPoPKey(mode string) error {
	var firstErr error
	if !useFileFallback() {
		if err := keyring.Delete(dpopKeyService, keyringAccount(mode)); err != nil &&
			!errors.Is(err, keyring.ErrNotFound) {
			firstErr = err
		}
	}
	path, err := fileFallbackPath(mode)
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

func writeKeyFile(mode string, raw []byte) error {
	path, err := fileFallbackPath(mode)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(path, raw, 0o600)
}

func fileFallbackHint(mode string) string {
	path, err := fileFallbackPath(mode)
	if err != nil {
		return "~/.palbase/dpop-key-" + mode + ".jwk"
	}
	return path
}
