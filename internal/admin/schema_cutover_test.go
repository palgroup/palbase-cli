package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeStudio struct {
	source string
	err    error
	calls  []string
}

func (f *fakeStudio) Mutation(_ context.Context, path string, input any, out any) error {
	f.calls = append(f.calls, path)
	if f.err != nil {
		return f.err
	}
	body, _ := json.Marshal(map[string]any{"source": f.source})
	_ = input
	return json.Unmarshal(body, out)
}

func runCutover(t *testing.T, studio Studio, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := schemaCutoverCmd(Resolvers{Studio: func() Studio { return studio }})
	var so, se bytes.Buffer
	cmd.SetOut(&so)
	cmd.SetErr(&se)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return so.String(), se.String(), err
}

const rendered = "import { defineSchema, text } from \"@palbase/backend\";\n\nexport default defineSchema({ tables: {} });\n"

// Without --write the command must be a dry run. An operator inspecting what a
// cutover WOULD produce, and finding it had already replaced the project's
// declaration, has lost the only record of what the project meant.
func TestSchemaCutover_WritesNothingWithoutWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "db", "schema.ts")

	stdout, _, err := runCutover(t, &fakeStudio{source: rendered},
		"--environment", "centauri6toiom", "--out", target)
	if err != nil {
		t.Fatalf("schema-cutover: %v", err)
	}
	if !strings.Contains(stdout, "defineSchema") {
		t.Fatalf("the generated schema was not printed:\n%s", stdout)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a dry run wrote %s", target)
	}
}

// With --write the previous declaration is kept beside the new one. The generated
// file describes what the database IS; the original recorded what the project
// MEANT, comments included, and an operator who finds a difference needs it back.
func TestSchemaCutover_BacksUpTheDeclarationItReplaces(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "db", "schema.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "// hand-written, with the reasons in the comments\nexport default defineSchema({});\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCutover(t, &fakeStudio{source: rendered},
		"--environment", "centauri6toiom", "--out", target, "--write"); err != nil {
		t.Fatalf("schema-cutover --write: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != rendered {
		t.Fatalf("the generated schema was not written:\n%s", got)
	}
	backup, err := os.ReadFile(target + ".before-cutover")
	if err != nil {
		t.Fatalf("the replaced declaration was not kept: %v", err)
	}
	if string(backup) != original {
		t.Fatalf("the backup does not hold the original:\n%s", backup)
	}
}

// A first cutover into a project with no declaration yet must still work, and must
// not invent a backup of a file that never existed.
func TestSchemaCutover_WritesWhereThereWasNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "db", "schema.ts")

	if _, _, err := runCutover(t, &fakeStudio{source: rendered},
		"--environment", "centauri6toiom", "--out", target, "--write"); err != nil {
		t.Fatalf("schema-cutover --write: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	if _, err := os.Stat(target + ".before-cutover"); !errors.Is(err, os.ErrNotExist) {
		t.Error("a backup was invented for a file that did not exist")
	}
}

// An empty render must never reach the file. Written over a project's declaration
// it would turn the next plan into a proposal to drop every table it has.
func TestSchemaCutover_RefusesAnEmptyRender(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "schema.ts")
	if err := os.WriteFile(target, []byte("export default defineSchema({});\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := runCutover(t, &fakeStudio{source: "   "},
		"--environment", "centauri6toiom", "--out", target, "--write")
	if err == nil {
		t.Fatal("an empty render was accepted")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(got), "defineSchema") {
		t.Fatalf("the declaration was overwritten by an empty render:\n%s", got)
	}
}

// The environment is what decides whose schema is read; without it the command
// has no target and must not guess one.
func TestSchemaCutover_RequiresAnEnvironment(t *testing.T) {
	if _, _, err := runCutover(t, &fakeStudio{source: rendered}); err == nil {
		t.Fatal("a cutover with no environment was accepted")
	}
}

// It reads through the same procedure `palbase db plan` uses — the one place the
// authorization for reading a tenant's schema already lives.
func TestSchemaCutover_CallsTheRenderProcedure(t *testing.T) {
	studio := &fakeStudio{source: rendered}
	if _, _, err := runCutover(t, studio, "--environment", "centauri6toiom"); err != nil {
		t.Fatalf("schema-cutover: %v", err)
	}
	if len(studio.calls) != 1 || studio.calls[0] != "backend.schemaRender" {
		t.Fatalf("called %v", studio.calls)
	}
}
