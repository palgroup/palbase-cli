package backend

import (
	"bytes"
	"context"
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
	// A project record is linked again with no target (FR-025); the ref form
	// of this hint belonged to the address model and is retired (FR-077).
	require.ErrorContains(t, err, "names a project, not an address")
	require.ErrorContains(t, err, "`palbase link`")
	require.NotContains(t, err.Error(), "`palbase link <ref>` again", "the retired hint came back")
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

// linkedByAnOlderCLI writes the committed file exactly as an older CLI left it —
// by hand, because the fields it carries no longer exist on Target — and moves
// into that checkout.
func linkedByAnOlderCLI(t *testing.T, raw string) string {
	t.Helper()
	inScratchCheckout(t)
	root, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(RootDir(), 0o755))
	require.NoError(t, os.WriteFile(projectPath(), []byte(raw), 0o644))
	return root
}

// A CHECKOUT LINKED BEFORE THE FIELDS RETIRED STILL READS (FR-1, FR-2).
//
// `1fcefcb` took `env` and `stackVersion` off Target, and the strict decoder
// then refused every file that still carried one — by name, on every verb:
// `read palbase/project.json: json: unknown field "stackVersion"`. Measured on
// a live checkout with the installed 0.67.1. The migration that exists to
// rewrite such a file reads it through the same decoder, so it never reached
// one.
//
// The test above already held an old file and stayed green through all of it:
// it calls `stackVersion()`, which never reads the file. The read path was the
// blind spot, so the read path is what this measures.
func TestACheckoutLinkedBeforeTheFieldsRetiredStillReads(t *testing.T) {
	for _, tc := range []struct {
		name, raw, url, project string
	}{
		{"stackVersion", `{"url":"https://8bbwb2pbm.palbase.studio","stackVersion":"39"}`,
			"https://8bbwb2pbm.palbase.studio", ""},
		{"env", `{"project":"prd_example","env":"main"}`, "", "prd_example"},
		{"both", `{"url":"https://mu0028.palbase.studio","project":"prd_a","env":"staging","stackVersion":"38"}`,
			"https://mu0028.palbase.studio", "prd_a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linkedByAnOlderCLI(t, tc.raw)

			got, err := readLinkedProject()
			require.NoError(t, err, "a file an older CLI wrote is unreadable")
			require.Equal(t, tc.url, got.URL)
			require.Equal(t, tc.project, got.Project)

			// EVERY VERB ALSO READS THROUGH ReadTarget. A project identity has no
			// address and saying so is that function's answer to every such file;
			// what must be gone is the decode failure in front of it.
			_, err = ReadTarget()
			if tc.url != "" {
				require.NoError(t, err, "ReadTarget refused a file an older CLI wrote")
			} else {
				require.ErrorContains(t, err, "names a project, not an address")
			}
		})
	}
}

// THIS MACHINE'S RECORD CARRYING THE RETIRED FIELDS STILL READS, AND IS NEVER
// REWRITTEN (FR-1, FR-2).
//
// `palbase start` and a loopback `link` keep their record under this machine's
// state and read it through the same decoder as the committed file, so an older
// CLI's record failed every verb the same way. Only the committed file is the
// migration's to rewrite: the verb below runs one in this checkout, and this
// machine's record must come out of it byte for byte.
func TestALocalRecordCarryingRetiredFieldsReadsAndIsNotRewritten(t *testing.T) {
	linkedByAnOlderCLI(t, `{"project":"prd_a","stackVersion":"39"}`)
	resolverRig(t, twoEnvs)
	local, err := localPath()
	require.NoError(t, err)
	require.NoError(t, ensureMachineStateDir(local))
	const record = `{"url":"http://127.0.0.1:54321","stackVersion":"39","env":"main"}`
	require.NoError(t, os.WriteFile(local, []byte(record), 0o600))

	got, err := ReadTarget()
	require.NoError(t, err, "a machine-local record an older CLI wrote is unreadable")
	require.Equal(t, "http://127.0.0.1:54321", got.URL)
	require.True(t, got.Local, "the record `palbase start` keeps no longer reads as the stack running here")

	var out bytes.Buffer
	_, err = PrintResolvedTo(&out, nil)
	require.NoError(t, err)
	committed, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	require.NotContains(t, string(committed), "stackVersion",
		"the verb ran no migration, so the assertion below would measure nothing")
	after, err := os.ReadFile(local)
	require.NoError(t, err)
	require.Equal(t, record, string(after), "the migration rewrote this machine's record")
}

// A FIELD NO TARGET EVER HAD IS STILL REFUSED BY NAME (FR-3).
//
// Tolerating the two retired fields must not become tolerating everything: a
// misspelt key in a committed file is a finding, and a reader that swallows it
// links nothing while saying nothing. The second case is the one that matters —
// a file that DOES carry a retired field goes down the path that sets those
// aside, and that path must not relax the rule for whatever is left.
//
// THE NAMES ARE EXACT. encoding/json matches keys without regard to case, and
// by Unicode's folding rather than ASCII's — `ſ` (U+017F) is an `s` to it — so
// every spelling below would land in a retired field. No CLI ever wrote one.
func TestAFieldNoTargetEverHadIsStillRefused(t *testing.T) {
	for _, tc := range []struct{ name, raw, refusal string }{
		{"alone", `{"url":"https://mu0028.palbase.studio","bogus":1}`, `unknown field "bogus"`},
		{"beside a retired field", `{"url":"https://mu0028.palbase.studio","stackVersion":"39","bogus":1}`,
			`unknown field "bogus"`},
		// Every CLI that wrote these wrote a string; any other shape was never a
		// Target either.
		{"a retired field of another shape", `{"url":"https://mu0028.palbase.studio","stackVersion":39}`,
			"stackVersion"},
		{"a retired name in capitals", `{"url":"https://mu0028.palbase.studio","STACKVERSION":"39"}`,
			`unknown field "STACKVERSION"`},
		{"a retired name in another case", `{"url":"https://mu0028.palbase.studio","Env":"main"}`,
			`unknown field "Env"`},
		{"a retired name under Unicode folding", `{"url":"https://mu0028.palbase.studio","ſtackVersion":"39"}`,
			"unknown field \"ſtackVersion\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linkedByAnOlderCLI(t, tc.raw)
			_, err := readLinkedProject()
			require.ErrorContains(t, err, tc.refusal)
		})
	}
}

// THE STRUCTURAL RULES READ THE FILE AS WRITTEN (FR-8).
//
// Tolerating the retired fields must not open a way round the rules every
// committed file is held to: a duplicate `env` would decode as its last value, a
// null as absent, an oversized value as a file like any other. Every case
// carries a retired field, so every one goes down the path that tolerates them,
// and every one is still refused with today's structural error.
func TestTheStructuralRulesReadTheFileAsWritten(t *testing.T) {
	oversized := `{"url":"https://mu0028.palbase.studio","stackVersion":"` + strings.Repeat("9", 257*1024) + `"}`
	nested := `{"project":"prd_a","env":` + strings.Repeat("[", 40) + strings.Repeat("]", 40) + `}`
	for _, tc := range []struct{ name, raw, refusal string }{
		{"a duplicate retired field", `{"project":"prd_a","env":"main","env":"staging"}`, "/env: duplicate field"},
		{"a null retired field", `{"project":"prd_a","env":null}`, "/env: null is not accepted"},
		{"a retired value past the size limit", oversized, "exceeds 256 KiB"},
		{"a retired value nested too deep", nested, "JSON nesting is too deep"},
		{"trailing JSON after a retired field", `{"project":"prd_a","stackVersion":"39"} {}`, "contains trailing JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linkedByAnOlderCLI(t, tc.raw)
			_, err := readLinkedProject()
			require.ErrorContains(t, err, tc.refusal)
		})
	}
}

// A RETIRED FIELD DOES NOT CHANGE HOW THE REST OF THE FILE DECODES (FR-9).
//
// Round 1 set the retired keys aside and decoded a RE-SERIALISED copy of what
// was left, and that copy was not the file: its keys came back sorted, so which
// of `url` and `URL` won changed, and every `<` came back as `<` — six
// bytes for one — so a file inside the size limit could land outside it. The
// same file with and without a retired field decodes to the same Target,
// compared as it would be written.
func TestARetiredFieldDoesNotChangeHowTheRestDecodes(t *testing.T) {
	nearTheLimit := strings.Repeat("<", 64*1024) + strings.Repeat("a", 256*1024-64*1024-200)
	for _, tc := range []struct{ name, bare, withRetired string }{
		{"keys that differ only in case",
			`{"url":"https://first.palbase.studio","URL":"https://second.palbase.studio"}`,
			`{"url":"https://first.palbase.studio","URL":"https://second.palbase.studio","stackVersion":"39"}`},
		{"a name near the size limit that escaping would grow",
			`{"url":"https://mu0028.palbase.studio","name":"` + nearTheLimit + `"}`,
			`{"url":"https://mu0028.palbase.studio","name":"` + nearTheLimit + `","stackVersion":"39"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linkedByAnOlderCLI(t, tc.bare)
			bare, err := readLinkedProject()
			require.NoError(t, err, "the file without a retired field is unreadable")

			linkedByAnOlderCLI(t, tc.withRetired)
			withRetired, err := readLinkedProject()
			require.NoError(t, err, "a retired field changed whether the rest of the file reads")

			bareJSON, err := json.Marshal(bare)
			require.NoError(t, err)
			retiredJSON, err := json.Marshal(withRetired)
			require.NoError(t, err)
			require.Equal(t, string(bareJSON), string(retiredJSON),
				"a retired field changed what the rest of the file decodes to")
		})
	}
}

// A REWRITTEN FILE CARRIES NO RETIRED FIELD (FR-7).
//
// The address migration writes Target back out, and the retired values ride on
// the Target it read. They are held where serialisation cannot see them; this
// measures the bytes that land in the repository rather than that intention.
func TestTheAddressMigrationWritesNoRetiredFieldBack(t *testing.T) {
	linkedByAnOlderCLI(t, `{"url":"https://mu0028.palbase.studio","env":"staging","stackVersion":"39"}`)
	resolverRig(t, twoEnvs)
	cloudAddresses(t, true)

	var out bytes.Buffer
	require.NoError(t, MigrateLegacyTarget(context.Background(), &out))

	written, err := os.ReadFile(projectPath())
	require.NoError(t, err)
	require.NotContains(t, string(written), "stackVersion", "the rewritten file still carries a retired field")
	require.NotContains(t, string(written), `"env"`, "the rewritten file still carries a retired field")
	require.Contains(t, string(written), `"project": "prd_a"`, "the address migration did not rewrite the file")
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
