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
	// --out points into a temp dir even though this must never write: a test that
	// leaves the default relative path in play writes into the repository the
	// moment the code under test changes, which is exactly what one mutation run
	// did (internal/admin/db/schema.ts, 2026-08-11).
	if _, _, err := runCutover(t, &fakeStudio{source: rendered},
		"--out", filepath.Join(t.TempDir(), "schema.ts")); err == nil {
		t.Fatal("a cutover with no environment was accepted")
	}
}

// It reads through the same procedure `palbase db plan` uses — the one place the
// authorization for reading a tenant's schema already lives.
func TestSchemaCutover_CallsTheRenderProcedure(t *testing.T) {
	studio := &fakeStudio{source: rendered}
	if _, _, err := runCutover(t, studio, "--environment", "centauri6toiom",
		"--out", filepath.Join(t.TempDir(), "schema.ts")); err != nil {
		t.Fatalf("schema-cutover: %v", err)
	}
	if len(studio.calls) != 1 || studio.calls[0] != "backend.schemaRender" {
		t.Fatalf("called %v", studio.calls)
	}
}

// A hand-written schema.ts often exports more than the schema. penny's held
// CATEGORY_VALUES, the list six modules validated against; the generated file has
// exactly one export, so replacing it took those with it and the build failed
// naming an import rather than a cutover. Carrying them across once would not help
// either — the next regeneration drops them again.
func TestSchemaCutover_RefusesToSwallowTheFilesOtherExports(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "schema.ts")
	original := `import { defineSchema, enumType } from "@palbase/backend";

export const CATEGORY_VALUES = ["groceries", "dining"] as const;

export default defineSchema({ tables: {} });
`
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := runCutover(t, &fakeStudio{source: rendered},
		"--environment", "pennym", "--out", target, "--write")
	if err == nil {
		t.Fatal("the cutover replaced a file whose other exports would be lost")
	}
	if !strings.Contains(err.Error(), "CATEGORY_VALUES") {
		t.Fatalf("the refusal does not name what would be lost: %v (stderr: %s)", err, stderr)
	}

	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("the file was modified despite the refusal:\n%s", got)
	}
}

// A file whose only export is the schema is exactly what a cutover replaces.
func TestSchemaCutover_ReplacesAFileThatOnlyExportsTheSchema(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "schema.ts")
	if err := os.WriteFile(target, []byte("import { defineSchema } from \"@palbase/backend\";\n\nexport default defineSchema({ tables: {} });\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCutover(t, &fakeStudio{source: rendered},
		"--environment", "pennym", "--out", target, "--write"); err != nil {
		t.Fatalf("a schema-only file was refused: %v", err)
	}
}

func TestNamedExports_FindsEveryFormAndIgnoresTheDefault(t *testing.T) {
	source := `import { defineSchema } from "@palbase/backend";

export const CATEGORY_VALUES = ["a"] as const;
export type Category = (typeof CATEGORY_VALUES)[number];
export function helper() {}
export async function slowHelper() {}
export interface Shape { a: string }
export enum Kind { A }
export class Thing {}
export { helper as alias };
export * from "./other.js";

export default defineSchema({ tables: {} });
`
	got := namedExports(source)
	for _, want := range []string{"CATEGORY_VALUES", "Category", "helper", "slowHelper", "Shape", "Kind", "Thing"} {
		if !contains(got, want) {
			t.Errorf("%q was not reported as an export that would be lost: %v", want, got)
		}
	}
	for _, form := range []string{"export { helper as alias }", "export * from \"./other.js\""} {
		if !contains(got, form) {
			t.Errorf("%q was not reported: %v", form, got)
		}
	}
	for _, entry := range got {
		if strings.HasPrefix(entry, "defineSchema") || entry == "default" {
			t.Errorf("the schema's own default export was reported as a loss: %v", got)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
