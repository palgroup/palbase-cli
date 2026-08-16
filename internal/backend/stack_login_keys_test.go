package backend

// stack_login_keys_test.go — refreshing a shipped key without wrecking a slot
// that belongs to somebody else.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheKeyRefreshLeavesAnotherTargetsSlotAlone is the failure this rule
// exists for.
//
// A checkout can carry slots for more than one target — an app with a cloud past
// and a local present has both — and the key that ships inside an app is
// invalidated whenever a project is rebuilt from nothing. Refreshing every slot
// on sight fixes the first problem by causing a worse one: measured on
// 2026-08-16, a web slot still naming a cloud environment had its key replaced
// with a local project's, so the config pointed at one host while holding
// another's credential. A stale key fails as "invalid_api_key"; a mixed one
// fails as an argument about whose fault it is.
func TestTheKeyRefreshLeavesAnotherTargetsSlotAlone(t *testing.T) {
	inScratchCheckout(t)

	const (
		project   = "https://127.0.0.1"
		freshKey  = "pb_project_cNEWKEY0000000000000"
		staleKey  = "pb_project_cOLDKEY0000000000000"
		cloudURL  = "https://todoappm8p6zm.dev.palbase.studio"
		cloudKey  = "pb_todoappm8p6zm_cCLOUDKEY000000"
		configArg = "palbase-config.json"
	)

	write := func(dir string, entry pullSpecConfigEntry) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		blob, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, configArg)
		if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	mine := write(filepath.Join(nativeArtifactsDir, "ios"), pullSpecConfigEntry{
		AppID: "project", Kind: "ios", BaseURL: project, APIKey: staleKey,
	})
	theirs := write(webArtifactsDir, pullSpecConfigEntry{
		AppID: "app_cloud", Kind: "production", BaseURL: cloudURL, APIKey: cloudKey,
	})

	var out strings.Builder
	if err := rewriteAppKeys(project, freshKey, &out); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	read := func(path string) pullSpecConfigEntry {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var entry pullSpecConfigEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		return entry
	}

	if got := read(mine).APIKey; got != freshKey {
		t.Errorf("the slot pointing at this project still carries %q — the app would ship a key the project replaced", got)
	}
	if got := read(theirs); got.APIKey != cloudKey || got.BaseURL != cloudURL {
		t.Errorf("another target's slot was rewritten to %q @ %q — it now points at one host with another's key",
			got.APIKey, got.BaseURL)
	}
	if strings.Contains(out.String(), webArtifactsDir) {
		t.Errorf("the run claimed to have updated another target's slot:\n%s", out.String())
	}
}
