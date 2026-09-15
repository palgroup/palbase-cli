package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A REFUSAL MUST NOT SEND A PERSON BACK TO A DIRECTORY NOTHING READS.
//
// The include gate was written when the deploy walked four directories for entry
// points, so its cure was to widen `include` until the compiler saw them. Neither
// half survives: `moduleSources` reads ONE glob, `*.module.ts`, and a class under
// `jobs/` reaches the deploy only when a module imports it and names it. Telling
// a person to add `jobs/**/*.ts` to `include` buys them a type-checked file that
// still never runs — the gate hands out the retired layout as the fix.
//
// Measured against the OUTPUT rather than the source, because the sentence is the
// product here: `message_shape_test.go` can see the literal, only this can see
// what a person actually reads.
func TestIncludeRefusalDoesNotRecommendARetiredDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs", "nightly.ts"),
		[]byte("export class NightlySweep {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A NARROWED include is what trips the gate at all — an absent one compiles
	// the whole tree and is deliberately not a finding (see includeBlindSpots).
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"),
		[]byte(`{"include": ["modules/**/*.ts"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if !reportIncludeBlindSpots(dir, &out) {
		t.Fatalf("the gate stayed silent on a tree carrying jobs/ outside include; it printed %q", out.String())
	}
	got := out.String()

	if strings.Contains(got, `Add to "include"`) {
		t.Errorf("the refusal still prescribes widening \"include\":\n%s", got)
	}
	for _, want := range []string{"modules/<domain>/", "providers"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal never says %q — it has to name where the class goes:\n%s", want, got)
		}
	}
}

// NEGATIVE CONTROL: a tree whose include already covers the directory produces no
// refusal at all, so the assertions above are measuring a message that fires for
// a reason rather than one that always fires.
func TestIncludeRefusalStaysSilentWhenTheDirectoryIsCovered(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"),
		[]byte(`{"include": ["jobs/**/*.ts"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if reportIncludeBlindSpots(dir, &out) {
		t.Errorf("refused a tree whose include already covers the directory:\n%s", out.String())
	}
}
