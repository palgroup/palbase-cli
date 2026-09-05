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

// AN UNLINKED CHECKOUT IS THE NORMAL CASE FOR `start`.
//
// Caught by UAT, not by a unit test: reading the target first made `palbase
// start` refuse a fresh `palbase init` with "this checkout is not linked" —
// advice for the command the reader had just run. Every unit test passed,
// because each half agreed with itself.
func TestStackVersionWorksInAnUnlinkedCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	seedInstalledSDK(t, dir, "33.0.2")

	got, err := stackVersion(".")
	if err != nil {
		t.Fatalf("an unlinked checkout was refused: %v", err)
	}
	if got != "33" {
		t.Errorf("stackVersion = %q, want the derived 33", got)
	}
	// AND IT WRITES NOTHING: `start` creates the project file itself once the
	// stack is up, and a half-formed target on disk names an address that does
	// not exist yet.
	if _, err := os.Stat(filepath.Join(dir, nativeArtifactsDir, "project.json")); !os.IsNotExist(err) {
		t.Error("a project file was written before there was a project to name")
	}
}

// A RUNNING DEV STACK MUST NOT EAT THE PROJECT BOND.
//
// ReadTarget PREFERS .palbase/local.json; WriteTarget writes .palbase/project.json.
// Reading through the first and writing through the second replaces a colleague's
// committed project with a localhost address — the exact thing WriteLocalTarget's
// comment twelve lines above warns about: "Writing one through the other is how a
// `palbase start` ends up committing a localhost address into a colleague's
// checkout."
//
// Not a rare edge: WriteLocalTarget only ever writes Target{URL}, so the local
// target NEVER carries a StackVersion — every repeat `palbase start` on a running
// stack re-derives and re-writes.
func TestStackVersionDoesNotClobberTheProjectWithALocalStack(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(nativeArtifactsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const committed = `{"project":"myproj","env":"prod"}`
	if err := os.WriteFile(filepath.Join(nativeArtifactsDir, "project.json"), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeArtifactsDir, "local.json"),
		[]byte(`{"url":"http://127.0.0.1:54321"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	seedInstalledSDK(t, dir, "33.0.0")

	if _, err := stackVersion("."); err != nil {
		t.Fatalf("stackVersion failed: %v", err)
	}

	after, err := ReadProjectTargetForTest()
	if err != nil {
		t.Fatalf("the committed project file is unreadable after stackVersion: %v", err)
	}
	if after.Project != "myproj" || after.Env != "prod" {
		t.Errorf("the project bond was overwritten: project=%q env=%q url=%q — a running dev stack "+
			"just committed a localhost address into this checkout", after.Project, after.Env, after.URL)
	}
	if after.URL != "" {
		t.Errorf("the local stack's address leaked into the committed file: %q", after.URL)
	}
}
