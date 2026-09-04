package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// schemaProject writes a db/ directory with the given files (name → source).
func schemaProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, "db", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestReadSchemaSourcesScansTheDirectory is the cutover: the declaration is a
// DIRECTORY, one file per schema, and the order is the file name's — a set that
// came back in readdir order would re-diff the same schema on every push.
func TestReadSchemaSourcesScansTheDirectory(t *testing.T) {
	dir := schemaProject(t, map[string]string{
		"public.ts":  `export default defineSchema("public", { tables: [] });`,
		"billing.ts": `export default defineSchema("billing", { tables: [] });`,
	})

	got, err := ReadSchemaSources(dir)
	if err != nil {
		t.Fatalf("ReadSchemaSources: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sources, got %d: %v", len(got), got)
	}
	if got[0].Name != "billing" || got[1].Name != "public" {
		t.Fatalf("sources must be sorted by name for a stable diff, got %q, %q", got[0].Name, got[1].Name)
	}
	if !strings.Contains(got[1].Source, `defineSchema("public"`) {
		t.Errorf("the source did not travel verbatim: %q", got[1].Source)
	}
}

// TestReadSchemaSourcesRejectsTheLegacyLayout is NFR-002. "invalid schema" is
// not a disposition: the refusal has to name the file to write and where the
// move is written down, or the person reading it has to go and find both.
func TestReadSchemaSourcesRejectsTheLegacyLayout(t *testing.T) {
	dir := schemaProject(t, map[string]string{
		"schema.ts": `export default defineSchema({ tables: {} });`,
	})

	_, err := ReadSchemaSources(dir)
	if err == nil {
		t.Fatal("the old single-file layout was accepted")
	}
	for _, want := range []string{LegacySchemaFile, PublicSchemaFile, MigrationGuide} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// TestReadSchemaSourcesRequiresThePublicSchema: `public` is the schema the rail
// introspects and /v1/db serves. A project declaring only db/billing.ts has
// none, and every comparison downstream would be against a schema nobody wrote.
func TestReadSchemaSourcesRequiresThePublicSchema(t *testing.T) {
	dir := schemaProject(t, map[string]string{
		"billing.ts": `export default defineSchema("billing", { tables: [] });`,
	})

	_, err := ReadSchemaSources(dir)
	if err == nil {
		t.Fatal("a project with no public schema was accepted")
	}
	if !strings.Contains(err.Error(), PublicSchemaFile) {
		t.Errorf("the refusal does not name the file to add: %v", err)
	}
	if !strings.Contains(err.Error(), "db/billing.ts") {
		t.Errorf("the refusal does not say what it DID read: %v", err)
	}
}

// TestReadSchemaSourcesSkipsAnEmptyFile: a file somebody emptied is not a
// declaration that there are no tables. Sending it would ask the database to
// drop everything in that schema.
func TestReadSchemaSourcesSkipsAnEmptyFile(t *testing.T) {
	dir := schemaProject(t, map[string]string{
		"public.ts":  `export default defineSchema("public", { tables: [] });`,
		"billing.ts": "   \n\t\n",
	})

	got, err := ReadSchemaSources(dir)
	if err != nil {
		t.Fatalf("ReadSchemaSources: %v", err)
	}
	if len(got) != 1 || got[0].Name != "public" {
		t.Fatalf("an emptied file was carried as a declaration: %v", got)
	}
}

// TestReadSchemaSourcesRefusesAProjectWithNoDatabase: distinct from an empty
// declaration, because the two have opposite meanings and only one is safe.
func TestReadSchemaSourcesRefusesAProjectWithNoDatabase(t *testing.T) {
	if _, err := ReadSchemaSources(t.TempDir()); err == nil {
		t.Fatal("a directory with no db/ was accepted")
	}
}

// TestReadSchemaSourcesIgnoresNonSchemaEntries — a stray .sql or a nested
// directory under db/ is not a schema declaration.
func TestReadSchemaSourcesIgnoresNonSchemaEntries(t *testing.T) {
	dir := schemaProject(t, map[string]string{
		"public.ts": `export default defineSchema("public", { tables: [] });`,
		"notes.sql": "select 1;",
		"README.md": "# db",
	})
	if err := os.MkdirAll(filepath.Join(dir, "db", "seeds"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ReadSchemaSources(dir)
	if err != nil {
		t.Fatalf("ReadSchemaSources: %v", err)
	}
	if len(got) != 1 || got[0].Name != "public" {
		t.Fatalf("a non-schema entry was read as a declaration: %v", got)
	}
}

// A COLOCATED TEST IS NOT A SCHEMA DECLARATION — and neither is a `.d.ts`.
//
// This package's OWN bundler builds every `*.test.ts` in the project into its
// own test module (stack_bundle.go, bundleTests). So the product invites a test
// to sit next to the thing it tests — in db/ like anywhere else — and this
// reader then called that test a schema named `public.test` and handed the
// bundler an `import __schemaN from "db/public.test.ts"` that cannot resolve.
//
// Measured 2026-08-31 pushing the control plane: `No matching export in
// "db/public.test.ts" for import "default"`. The build error is the LUCKY case.
// A test file that does export a default would have been planned as a schema
// and CREATED IN THE DATABASE.
func TestReadSchemaSourcesSkipsTestsAndTypeDeclarations(t *testing.T) {
	dir := schemaProject(t, map[string]string{
		"public.ts":      `export default defineSchema("public", { tables: [] })`,
		"public.test.ts": `import { test } from "vitest"; test("t", () => {})`,
		"billing.ts":     `export default defineSchema("billing", { tables: [] })`,
		"billing.d.ts":   `export declare const x: number;`,
	})
	got, err := ReadSchemaSources(dir)
	if err != nil {
		t.Fatalf("ReadSchemaSources: %v", err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "billing,public" {
		t.Fatalf("schema names = %v, want [billing public]", names)
	}
}

// THE WIRE FORMAT IS A CONTRACT, and `wireSource(s)` is a conversion that keeps
// no record of it.
//
// SchemaSourcesBody projects SchemaSource into a local struct with json tags.
// The conversion is safe in ONE direction: if only one of the two types gains a
// field, the compiler refuses it. If BOTH gain one — the likely shape, since a
// new field usually wants to travel — the conversion keeps compiling and the
// new field goes out on the wire without anybody deciding that it should.
//
// So the keys are asserted here rather than inferred from the types.
func TestTheSchemaWireFormatSendsExactlyTheseKeys(t *testing.T) {
	raw, err := SchemaSourcesBody([]SchemaSource{{Name: "public", Source: "export const x = 1"}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(decoded); !slices.Equal(got, []string{"sources"}) {
		t.Errorf("the body's top-level keys are %v, want [sources]", got)
	}
	list, ok := decoded["sources"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("sources is not a one-element list: %v", decoded["sources"])
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("a source is not an object: %v", list[0])
	}
	if got := keysOf(entry); !slices.Equal(got, []string{"name", "source"}) {
		t.Errorf("a source carries %v, want [name source] — a field reached the wire "+
			"without anybody choosing to send it", got)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
