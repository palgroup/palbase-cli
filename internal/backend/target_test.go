package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BİR HEDEFİN BU MAKİNEDE OLUP OLMADIĞI, ADRESİNDEN OKUNUR.
//
// `Target.URL`'in yorumu "set for a project running on this machine" diyordu ve
// bu, `link` yalnız yerel bir yığını gösterebilirken doğruydu. Artık
// `palbase link https://<ref>.v2.palbase.studio` UZAK bir projeyi yazıyor.
//
// Ayrım olmadan `palbase logs` her linkli hedefte yerel konteyner arıyor ve
// bulut projesinde "No such container: palbase-todoapp-runtime-1" diyor —
// hiç var olmayacak bir konteynerin adını vererek (canlıda ölçüldü 2026-08-21).
func TestATargetKnowsWhetherItIsOnThisMachine(t *testing.T) {
	local := []string{
		"http://localhost:54321",
		"https://127.0.0.1",
		"http://[::1]:8080",
		"https://127.0.0.1:8443/",
	}
	for _, u := range local {
		if !(Target{URL: u}).OnThisMachine() {
			t.Fatalf("%q bu makinede sayılmalı", u)
		}
	}

	remote := []string{
		"https://juvuev3mm.v2.palbase.studio",
		"https://app.dev.palbase.studio",
		// Adı "localhost" İÇEREN ama bu makine OLMAYAN bir adres: alt dizge
		// eşlemesi burada yanlış cevap verirdi.
		"https://localhost.example.com",
	}
	for _, u := range remote {
		if (Target{URL: u}).OnThisMachine() {
			t.Fatalf("%q bu makinede SAYILMAMALI", u)
		}
	}
}

// seedProject writes a .palbase/project.json in dir and chdirs there.
func seedProject(t *testing.T, target Target) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, nativeArtifactsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	blob, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nativeArtifactsDir, "project.json"), append(blob, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

// seedInstalledSDK writes a node_modules/@palbase/backend/package.json.
func seedInstalledSDK(t *testing.T, dir, version string) {
	t.Helper()
	pkg := filepath.Join(dir, "node_modules", "@palbase", "backend")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"@palbase/backend","version":"` + version + `"}`
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// WHICH STACK THIS PROJECT RUNS IS THE PROJECT'S FACT, NOT THE BINARY'S.
//
// The image tags lived in the CLI, so `palbase start` brought up whatever the
// installed binary happened to carry — two colleagues on two CLI versions ran
// two different stacks against the same source, and nothing said so.
//
// Declared once, in the COMMITTED file. Supabase writes the equivalent to a
// gitignored `.temp/`, so a fresh clone and every CI runner silently disagree
// with the machine that linked it (D-036).
func TestStackVersionReadsWhatTheProjectDeclares(t *testing.T) {
	seedProject(t, Target{URL: "http://127.0.0.1:54321", StackVersion: "33"})

	got, err := stackVersion(".")
	if err != nil {
		t.Fatalf("declared version was refused: %v", err)
	}
	if got != "33" {
		t.Errorf("stackVersion = %q, want the declared 33", got)
	}
}

// A PROJECT THAT DECLARES NOTHING GETS THE FIELD WRITTEN, not a silent default.
//
// Deriving and forgetting would leave the next run free to derive something
// else — the same drift, one layer down. The value is persisted so the file
// answers the question from then on.
func TestStackVersionDerivesFromTheSDKAndWritesIt(t *testing.T) {
	dir := seedProject(t, Target{URL: "http://127.0.0.1:54321"})
	seedInstalledSDK(t, dir, "33.0.0")

	got, err := stackVersion(".")
	if err != nil {
		t.Fatalf("derivation failed: %v", err)
	}
	// THE MAJOR, not the full version: a table keyed by every patch release is a
	// table nobody maintains.
	if got != "33" {
		t.Errorf("derived %q from SDK 33.0.0, want the major 33", got)
	}

	// IT MUST BE ON DISK, or the next run derives again.
	onDisk, err := ReadTarget()
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.StackVersion != got {
		t.Errorf("project.json carries %q, want the derived %q — a derivation nobody wrote down "+
			"is a derivation the next run can disagree with", onDisk.StackVersion, got)
	}
}

// NO SDK, NO SILENT FALLBACK. Supabase's probes are best-effort and fall back to
// an embedded default without saying so; the stack you get is then a property of
// the binary again.
func TestStackVersionRefusesWhenNothingCanAnswer(t *testing.T) {
	seedProject(t, Target{URL: "http://127.0.0.1:54321"})

	_, err := stackVersion(".")
	if err == nil {
		t.Fatal("a project with no declaration and no installed SDK got a version anyway")
	}
	if !strings.Contains(err.Error(), backendPkg) {
		t.Errorf("the refusal does not name %s: %v", backendPkg, err)
	}
}
