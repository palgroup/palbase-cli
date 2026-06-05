package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSweepStaleServeTempDirs_RemovesStaleKeepsFresh: the sweep must reclaim
// only OUR-prefixed dirs that are older than the grace window, and must leave a
// fresh OUR-prefixed dir (a concurrently-launching serve) AND any unrelated
// entry strictly alone.
func TestSweepStaleServeTempDirs_RemovesStaleKeepsFresh(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	// A stale serve dir from a crashed run: our prefix, old mtime. It also holds
	// a 0600 owner-token file — removing the dir must take the token with it.
	// Write the token BEFORE stamping the mtime: creating the file bumps the
	// parent dir's mtime, so the age stamp must come last to stick.
	stale := filepath.Join(root, serveTempPrefix+"crashed")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", stale, err)
	}
	if err := os.WriteFile(filepath.Join(stale, "owner-token"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write owner-token: %v", err)
	}
	if err := os.Chtimes(stale, now.Add(-10*time.Minute), now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("chtimes %s: %v", stale, err)
	}

	// A fresh serve dir (another serve launching right now): our prefix, young
	// mtime → must be KEPT.
	fresh := filepath.Join(root, serveTempPrefix+"launching")
	mkDirAt(t, fresh, now.Add(-5*time.Second))

	// An unrelated old dir that does NOT carry our prefix → must be KEPT
	// (proves the sweep never touches anything outside our namespace).
	unrelated := filepath.Join(root, "some-other-tool-cache")
	mkDirAt(t, unrelated, now.Add(-1*time.Hour))

	removed := sweepStaleServeTempDirs(root, serveTempPrefix, now, 1*time.Minute)

	if len(removed) != 1 || removed[0] != stale {
		t.Fatalf("sweep removed %v, want exactly [%s]", removed, stale)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale serve dir (and its owner-token) must be gone, stat err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh serve dir must be kept (concurrent launch), stat err=%v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated non-prefixed dir must never be touched, stat err=%v", err)
	}
}

// TestSweepStaleServeTempDirs_IgnoresNonDirAndMissingRoot: a file (not a dir)
// with our prefix is left alone, and a missing temp root is a safe no-op.
func TestSweepStaleServeTempDirs_IgnoresNonDirAndMissingRoot(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	// A FILE that happens to share our prefix — we only sweep directories.
	file := filepath.Join(root, serveTempPrefix+"not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chtimes(file, now.Add(-1*time.Hour), now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("chtimes file: %v", err)
	}

	removed := sweepStaleServeTempDirs(root, serveTempPrefix, now, 1*time.Minute)
	if len(removed) != 0 {
		t.Fatalf("sweep must ignore non-directories, removed %v", removed)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("prefixed FILE must be left alone, stat err=%v", err)
	}

	// A non-existent temp root must not panic and must remove nothing.
	if got := sweepStaleServeTempDirs(filepath.Join(root, "does-not-exist"), serveTempPrefix, now, time.Minute); got != nil {
		t.Fatalf("missing temp root should be a no-op, got %v", got)
	}
}

// mkDirAt creates dir and stamps its mtime, so the sweep's age check is driven
// by a deterministic timestamp rather than the real clock.
func mkDirAt(t *testing.T, dir string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chtimes(dir, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", dir, err)
	}
}
