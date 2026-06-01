package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeployState_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Unknown base before any pull.
	if got := baseSHAFor(dir, "main"); got != "" {
		t.Fatalf("expected empty base before pull, got %q", got)
	}

	// Record a pull for main, read it back.
	if err := recordPulledVersion(dir, "main", "abc12345", "2026-06-01T00:00:00Z"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := baseSHAFor(dir, "main"); got != "abc12345" {
		t.Fatalf("main base = %q, want abc12345", got)
	}

	// A different branch is tracked independently.
	if err := recordPulledVersion(dir, "qa", "def67890", "2026-06-01T00:01:00Z"); err != nil {
		t.Fatalf("record qa: %v", err)
	}
	if got := baseSHAFor(dir, "qa"); got != "def67890" {
		t.Fatalf("qa base = %q, want def67890", got)
	}
	// main unchanged.
	if got := baseSHAFor(dir, "main"); got != "abc12345" {
		t.Fatalf("main base after qa write = %q, want abc12345", got)
	}
}

// Empty branch ("") normalises to "main" so the default-branch base is shared.
func TestDeployState_EmptyBranchIsMain(t *testing.T) {
	dir := t.TempDir()
	if err := recordPulledVersion(dir, "", "aaa11111", "t"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := baseSHAFor(dir, "main"); got != "aaa11111" {
		t.Fatalf(`baseSHAFor(main) = %q, want aaa11111 (""→main)`, got)
	}
	if got := baseSHAFor(dir, ""); got != "aaa11111" {
		t.Fatalf(`baseSHAFor("") = %q, want aaa11111`, got)
	}
}

// deploy-state.json must NOT clobber the config-as-code state.json.
func TestDeployState_SeparateFromConfigState(t *testing.T) {
	dir := t.TempDir()
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(os.MkdirAll(filepath.Join(dir, ".palbase"), 0o755))
	// Pre-existing config-as-code state.
	require(os.WriteFile(filepath.Join(dir, ".palbase/state.json"), []byte(`{"state_version":3}`), 0o644))

	require(recordPulledVersion(dir, "main", "abc12345", "t"))

	// state.json untouched.
	got, err := os.ReadFile(filepath.Join(dir, ".palbase/state.json"))
	require(err)
	if string(got) != `{"state_version":3}` {
		t.Fatalf("config state.json was modified: %s", got)
	}
	// deploy-state.json is a separate file.
	if _, err := os.Stat(filepath.Join(dir, deployStateFile)); err != nil {
		t.Fatalf("deploy-state.json not written: %v", err)
	}
}
