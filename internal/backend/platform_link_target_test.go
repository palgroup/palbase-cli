package backend

// platform_link_target_test.go — the platform link commands obey the target.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/selection"
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
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// AN iOS CHECKOUT, because `link` now reads what the checkout IS rather than
	// taking the platform from the command name (T006). The old `ios link` said
	// "ios" in its path; the new one looks for an Xcode project.
	if err := os.MkdirAll(filepath.Join(dir, "App.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	cmd := newLinkCmd(Resolvers{})
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
	var slot appEnvironments
	if err := json.Unmarshal(raw, &slot); err != nil {
		t.Fatal(err)
	}
	entry := slot.Environments[slot.Default]
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

// TestWebLinkOnABoundProjectStillDoesTheWebWork.
//
// The bound path replaces ONE step of `palbase web link` — where the artifacts
// come from — and it used to replace the whole command. Everything the web link
// exists to do afterwards (run `palbe-gen`, install the predev/prebuild hooks,
// wire the entry import) was skipped, and the command still exited 0.
//
// ÖLÇÜLDÜ 25.08.2026 (palaicloud): `palbase web link` bağlı bir checkout'ta
// `palbe.gen.ts` ÜRETMEDİ, package.json'a kanca eklemedi ve iOS yolunun
// artefaktlarını bıraktı. Hiçbir adım hata döndürmedi.
func TestWebLinkOnABoundProjectStillDoesTheWebWork(t *testing.T) {
	inScratchCheckout(t)

	const anon = "pb_project_cI1Gf8cAvKPylFE4E4jWVF5FKCT2KmaU0"
	srv := stackServing(t, anon, nil)
	if err := WriteTarget(Target{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	linkedAs(t, srv.URL, "a-credential")

	// The CLOUD artifact seam must not be reached: a bound checkout's artifacts
	// come from the project it is bound to.
	origArtifacts := webLinkArtifacts
	webLinkArtifacts = func(_ context.Context, _ Resolvers, _ selection.Selection, _ bool, _ io.Writer) error {
		t.Error("the cloud artifact fetch ran in a checkout bound to a project")
		return nil
	}
	t.Cleanup(func() { webLinkArtifacts = origArtifacts })
	stubInstall(t)

	// Stand in for @palbase/web's generator.
	if err := os.MkdirAll(filepath.Dir(palbeGenBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(palbeGenBin,
		[]byte("#!/bin/sh\ncat > palbe.gen.ts <<'EOF2'\n// palbe gen sentinel\nEOF2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("package.json", []byte("{\n  \"name\": \"myapp\",\n  \"version\": \"1.0.0\"\n}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("app", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("app/layout.tsx",
		[]byte("import React from 'react';\nexport default function Layout() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Resolvers deliberately empty: reaching for the cloud fails loudly instead
	// of passing by accident.
	cmd := (&webCmd{r: Resolvers{}}).newWebLinkCmd()
	link, _, err := cmd.Find([]string{"link"})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	link.SetOut(&out)
	link.SetErr(&out)
	link.SetContext(context.Background())
	if err := link.RunE(link, nil); err != nil {
		t.Fatalf("web link against a bound project: %v\n%s", err, out.String())
	}

	// 1. The artifacts palbe-gen reads, from the bound project.
	raw, err := os.ReadFile(filepath.Join(webArtifactsDir, "palbase-config.json"))
	if err != nil {
		t.Fatalf("no web config: %v\n%s", err, out.String())
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if got, _ := cfg["api_key"].(string); got != anon {
		t.Errorf("api_key is %q, want the bound project's publishable key", got)
	}
	if got, _ := cfg["base_url"].(string); got != srv.URL {
		t.Errorf("base_url is %q, want %s", got, srv.URL)
	}

	// 2. …and the work the web link exists to do.
	if body, err := os.ReadFile("palbe.gen.ts"); err != nil {
		t.Errorf("palbe.gen.ts was never generated: %v\n%s", err, out.String())
	} else if !strings.Contains(string(body), "palbe gen sentinel") {
		t.Errorf("palbe.gen.ts is not the generator's output:\n%s", body)
	}
	pkg, err := os.ReadFile("package.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pkg), `"predev": "palbe-gen --soft || exit 0"`) {
		t.Errorf("package.json carries no predev hook:\n%s", pkg)
	}
	entry, err := os.ReadFile("app/layout.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), "palbe.gen") {
		t.Errorf("the entry file does not import the generated client:\n%s", entry)
	}
	if _, err := os.Stat(filepath.Join("Palbase", "Config", "Main.xcconfig")); err == nil {
		t.Error("a web link wrote an Xcode build configuration")
	}
}
