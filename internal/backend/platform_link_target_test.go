package backend

// platform_link_target_test.go — the platform link commands obey the target.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlatformLinkFollowsTheBoundProject is the trap this rule closes.
//
// Measured on 2026-08-16 in a real app checkout bound to https://127.0.0.1:
// `palbase ios link` resolved a CLOUD project, overwrote the ios slot with that
// environment's URL and key, and regenerated the client from the cloud's
// contract. Nothing failed — the app simply went on talking to a host its owner
// had stopped using. login, push and spec were all target-relative already;
// this was the one verb that was not.
func TestPlatformLinkFollowsTheBoundProject(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)

	if err := WriteTarget(Target{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	// Linking is something you do as somebody: both the key and the contract
	// come from the project over authenticated routes.
	linkedAs(t, srv.URL, "a-credential")

	// The cloud resolver is deliberately absent: if the command reaches for it,
	// this fails as a nil dereference or an auth error rather than passing by
	// accident.
	cmd := newIOSCmd(Resolvers{})
	link, _, err := cmd.Find([]string{"link"})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	link.SetOut(&out)
	link.SetErr(&out)
	link.SetContext(context.Background())
	if err := link.RunE(link, nil); err != nil {
		t.Fatalf("ios link against a bound project: %v\n%s", err, out.String())
	}

	raw, err := os.ReadFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"))
	if err != nil {
		t.Fatalf("no ios slot written: %v\n%s", err, out.String())
	}
	var entry pullSpecConfigEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.BaseURL != srv.URL {
		t.Errorf("the slot names %q, want the project this checkout is bound to (%s)", entry.BaseURL, srv.URL)
	}
	if entry.APIKey != "pb_project_cPUBLISHABLE" {
		t.Errorf("the slot carries %q, want the bound project's publishable key", entry.APIKey)
	}
}

// TestPlatformUseRefusesOnABoundProject: switching environments is a cloud
// verb, and answering it here would point the app somewhere else entirely.
func TestPlatformUseRefusesOnABoundProject(t *testing.T) {
	inScratchCheckout(t)
	if err := WriteTarget(Target{URL: "https://127.0.0.1"}); err != nil {
		t.Fatal(err)
	}

	err := refuseUseOnBoundProject("ios")
	if err == nil {
		t.Fatal("`ios use` was allowed on a checkout bound to a project")
	}
	for _, want := range []string{"https://127.0.0.1", "one environment", "palbase link"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q, so it does not say what to do instead: %v", want, err)
		}
	}
}
