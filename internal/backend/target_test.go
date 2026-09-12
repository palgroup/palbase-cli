package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBrokenLocalTargetNeverFallsBackToCloud(t *testing.T) {
	for _, raw := range []string{`{"url":`, `{}`, `{"url":" "}`, "directory"} {
		t.Run(raw, func(t *testing.T) {
			seedProject(t, Target{URL: "https://project.palbase.studio"})
			p, err := localPath()
			require.NoError(t, err)
			// The rig creates the directory it is about to write into: asking
			// for the path creates nothing, for the product and for the test
			// alike.
			require.NoError(t, ensureMachineStateDir(p))
			if raw == "directory" {
				require.NoError(t, os.Mkdir(p, 0o755))
			} else {
				require.NoError(t, os.WriteFile(p, []byte(raw), 0o644))
			}
			target, err := ReadTarget()
			// THE REFUSAL NAMES THE FILE IT COULD NOT READ, and the file is the
			// one `localPath()` declares — this machine's own state under
			// `~/.palbase/checkouts/<hash>/`, not a path spelled again here. A
			// literal would keep passing after the state moved, which is the
			// move this test exists to guard.
			p, pathErr := localPath()
			require.NoError(t, pathErr)
			require.ErrorContains(t, err, p)
			require.Empty(t, target.URL, "a broken local target must not send an operation to the cloud")
		})
	}
}

func TestTargetWithoutAddressRequiresRelinking(t *testing.T) {
	seedProject(t, Target{Project: "prd_old", Name: "old-project"})
	_, err := ReadTarget()
	require.ErrorContains(t, err, "palbase link <ref>")
}

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
	if err := os.MkdirAll(filepath.Join(dir, "palbase"), 0o755); err != nil {
		t.Fatal(err)
	}
	blob, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "palbase", "project.json"), append(blob, '\n'), 0o644); err != nil {
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
// THE STACK VERSION IS DERIVED, NEVER DECLARED — and that is a correction.
//
// The committed file used to carry `stackVersion`, and the field's own comment
// said it "decides which images `palbase start` brings up". MEASURED, that is
// no longer true: every decision in `start` reads `installedSDKVersion` —
// `img.ref(sdkVersion)` (:281), `imagesPresent` (:284), `ensureBootValues`
// (:293), `recordStackImages` (:297). The declared value survived in exactly
// ONE place: the banner string.
//
// And there it could LIE. `stackVersion()` returned the committed value BEFORE
// falling back to the installed package, so bumping @palbase/backend 38 → 39
// brought the 39 images up while the banner still printed "stack 38" — and the
// banner exists precisely to make "the wrong stack came up" visible
// (`start.go:265`). A field that decides nothing and can lie about the one
// thing it is printed for is not a field; it is a defect with a schema.
//
// So: derived from the installed SDK, every time, and written nowhere.

func TestStackVersionDerivesFromTheInstalledSDK(t *testing.T) {
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
}

// A COMMITTED `stackVersion` NO LONGER WINS — this is the defect, stated as a
// test. An old checkout carries the field; the installed SDK must still decide.
func TestAnOldDeclaredStackVersionDoesNotOverrideTheInstalledSDK(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("palbase", 0o755); err != nil {
		t.Fatal(err)
	}
	// Written by hand: the field no longer exists on Target, and an old file is
	// exactly what this test is about.
	const old = `{"url":"http://127.0.0.1:54321","stackVersion":"33"}`
	if err := os.WriteFile(filepath.Join("palbase", "project.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	seedInstalledSDK(t, dir, "39.1.6")

	got, err := stackVersion(".")
	if err != nil {
		t.Fatalf("an old file made the derivation fail: %v", err)
	}
	if got != "39" {
		t.Errorf("stackVersion = %q, want 39 from the INSTALLED SDK — a committed field "+
			"that outranks the installed package is how the banner learned to lie", got)
	}
}

// NOTHING IS EVER WRITTEN. The old implementation persisted what it derived, so
// the next run would agree with itself; now there is nothing to disagree about.
func TestStackVersionWritesNothing(t *testing.T) {
	dir := seedProject(t, Target{URL: "http://127.0.0.1:54321"})
	seedInstalledSDK(t, dir, "33.0.0")
	before, err := os.ReadFile(filepath.Join(dir, "palbase", "project.json"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stackVersion("."); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(filepath.Join(dir, "palbase", "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("stackVersion rewrote the committed file:\nbefore %s\nafter  %s", before, after)
	}
	if strings.Contains(string(after), "stackVersion") {
		t.Error("the retired field was written back into the customer's repository")
	}
}

// NO SDK, NO SILENT FALLBACK. Supabase's probes are best-effort and fall back to
// an embedded default without saying so; the stack you get is then a property of
// the binary again.
func TestStackVersionRefusesWhenNothingCanAnswer(t *testing.T) {
	seedProject(t, Target{URL: "http://127.0.0.1:54321"})

	_, err := stackVersion(".")
	if err == nil {
		t.Fatal("a project with no installed SDK got a version anyway")
	}
	if !strings.Contains(err.Error(), backendPkg) {
		t.Errorf("the refusal does not name %s: %v", backendPkg, err)
	}
}

// AN UNLINKED CHECKOUT IS THE NORMAL CASE FOR `start`.
//
// Caught by UAT, not by a unit test: reading the target first made `palbase
// start` refuse a fresh `palbase init` with "this checkout is not linked" —
// advice for the command the reader had just run.
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
	if _, err := os.Stat(filepath.Join(dir, "palbase", "project.json")); !os.IsNotExist(err) {
		t.Error("a project file was written before there was a project to name")
	}
}

// A RUNNING DEV STACK MUST NOT EAT THE PROJECT BOND.
//
// The original defect: ReadTarget PREFERS the machine-local record while
// WriteTarget writes the committed one, so reading through the first and
// writing through the second replaced a colleague's project with a localhost
// address. `stackVersion` no longer writes at all, which removes the mechanism
// — and this test keeps measuring the outcome, because a future writer could
// reintroduce it.
func TestStackVersionDoesNotClobberTheProjectWithALocalStack(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll("palbase", 0o755); err != nil {
		t.Fatal(err)
	}
	const committed = `{"project":"prd_a","name":"myproj"}`
	if err := os.WriteFile(filepath.Join("palbase", "project.json"), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	useTempMachineHome(t)
	if err := WriteLocalTarget(Target{URL: "http://127.0.0.1:54321"}); err != nil {
		t.Fatal(err)
	}
	seedInstalledSDK(t, dir, "33.0.0")

	if _, err := stackVersion("."); err != nil {
		t.Fatalf("stackVersion failed: %v", err)
	}

	after, err := readLinkedProject()
	if err != nil {
		t.Fatalf("the committed project file is unreadable after stackVersion: %v", err)
	}
	if after.Project != "prd_a" || after.Name != "myproj" {
		t.Errorf("the committed project was replaced: %+v", after)
	}
}
