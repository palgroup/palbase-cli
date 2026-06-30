package auth

import (
	"errors"
	"testing"
	"time"
)

// getWithTimeout must NOT hang when the getter blocks (the sandbox/locked-
// keychain case that wedged `palbase` until dangerouslyDisableSandbox) — it
// returns a timeout error so LoadDPoPKey falls through to the file fallback.
func TestGetWithTimeout(t *testing.T) {
	t.Run("returns the value when the getter answers in time", func(t *testing.T) {
		v, err := getWithTimeout(func() (string, error) { return "jwk", nil }, time.Second)
		if err != nil || v != "jwk" {
			t.Fatalf("got (%q, %v), want (\"jwk\", nil)", v, err)
		}
	})

	t.Run("propagates the getter's error", func(t *testing.T) {
		sentinel := errors.New("not found")
		_, err := getWithTimeout(func() (string, error) { return "", sentinel }, time.Second)
		if !errors.Is(err, sentinel) {
			t.Fatalf("got %v, want the getter's error", err)
		}
	})

	t.Run("times out instead of hanging when the getter blocks", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			// 20ms timeout against a getter that never returns — must time out,
			// not block the test.
			_, err := getWithTimeout(func() (string, error) {
				select {} // block forever (the wedged-syscall case)
			}, 20*time.Millisecond)
			if err == nil {
				t.Error("expected a timeout error, got nil")
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("getWithTimeout hung — it must return on timeout, not block")
		}
	})
}
