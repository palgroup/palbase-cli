package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureBackendPkg is a self-contained CJS stand-in for the project's installed
// @palbase/backend, faithful to the SDK contract this CLI depends on:
//   - column factories return builders carrying their definition on `._def`
//     (uuid/text/integer/boolean/timestamp/jsonb/enumType + the chainable
//     primaryKey/notNull/nullable/default/defaultRandom/defaultNow modifiers)
//   - defineSchema({ tables }) derives each TableDef.name from the object key and
//     reads the columns out of the table's `columns` key — the shape the real
//     17.4.0 SDK requires. The fixture used to take the table VALUE as the column
//     map, which the real package rejects with "Cannot convert undefined or null
//     to object"; a fixture that accepts a shape the SDK refuses is not a lock,
//     it is two synthetic halves agreeing with each other.
//   - makeEnvDts(schemas) takes an ARRAY and emits the exact `palbase-env.d.ts`
//     text (golden-matched below) — mirrors src/db/env-gen.ts. The arity is not
//     decoration: it is what a bridge regressing to one call per file would
//     break, and the fixture THROWS rather than tolerating it.
//
// The bundled db/*.ts keeps @palbase/* external, so at eval time both the
// bundle's imports AND the env-gen bridge's `require('@palbase/backend')` resolve
// here via NODE_PATH. Keeping this verbatim with the SDK is what makes the test
// a real contract lock without a network install of the actual package.
const fixtureBackendPkg = `
'use strict';
function builder(type, def) {
  const _def = def || { type, nullable: false, primaryKey: false };
  return {
    _def,
    primaryKey() { _def.primaryKey = true; return builder(type, _def); },
    notNull() { _def.nullable = false; return builder(type, _def); },
    nullable() { _def.nullable = true; return builder(type, _def); },
    default(v) { _def.defaultValue = v; return builder(type, _def); },
    defaultRandom() { _def.defaultRandom = true; return builder(type, _def); },
    defaultNow() { _def.defaultNow = true; return builder(type, _def); },
    references(table, column) { _def.references = { table, column }; return builder(type, _def); },
    onDelete(a) { _def.onDeleteAction = a; return builder(type, _def); },
  };
}
const uuid = () => builder('uuid');
const text = () => builder('text');
const integer = () => builder('integer');
const boolean = () => builder('boolean');
const timestamp = () => builder('timestamp');
const jsonb = () => builder('jsonb');
function enumType(name, values) {
  const b = builder('enum');
  b._def.enumName = name;
  b._def.enumValues = values.slice();
  return b;
}
function defineTable(name, table) {
  if (!table || typeof table.columns !== 'object') {
    throw new TypeError('table ' + name + ' has no columns');
  }
  return { name, columns: table.columns, rls: table.rls === true, primaryKey: table.primaryKey };
}
function defineSchema(name, input) {
  if (typeof name !== 'string') {
    throw new TypeError('defineSchema takes the schema NAME first');
  }
  if (!Array.isArray(input.tables)) {
    throw new TypeError('defineSchema takes a tables ARRAY — a table carries its own name');
  }
  const tables = {};
  for (const table of input.tables) tables[table.name] = table;
  return { name, tables };
}
function baseTsType(def) {
  switch (def.type) {
    case 'uuid': case 'text': case 'timestamp': return 'string';
    case 'integer': return 'number';
    case 'boolean': return 'boolean';
    case 'jsonb': return 'unknown';
    case 'enum': {
      const values = def.enumValues || [];
      if (values.length === 0) return 'string';
      return values.map((v) => JSON.stringify(v)).join(' | ');
    }
    default: return 'unknown';
  }
}
function rowType(def) {
  const base = baseTsType(def);
  return def.nullable ? base + ' | null' : base;
}
function optionalOnInsert(def) {
  return def.nullable === true || def.defaultRandom === true || def.defaultNow === true || def.defaultValue !== undefined;
}
function tableBlock(table, indent) {
  const cols = Object.entries(table.columns);
  const rowLines = cols.map(([col, b]) => indent + '    ' + col + ': ' + rowType(b._def) + ';');
  const insertLines = cols.map(([col, b]) => {
    const def = b._def;
    const opt = optionalOnInsert(def) ? '?' : '';
    return indent + '    ' + col + opt + ': ' + rowType(def) + ';';
  });
  return [
    indent + table.name + ': {',
    indent + '  row: {',
    ...rowLines,
    indent + '  };',
    indent + '  insert: {',
    ...insertLines,
    indent + '  };',
    indent + '};',
  ].join('\n');
}
function makeEnvDts(schemas) {
  // ARITY IS THE CONTRACT THIS FIXTURE EXISTS TO LOCK. The real makeEnvDts takes
  // every declaration at once, because relations are named across schemas. A
  // bridge that reverted to one-call-per-file would still "work" against a
  // tolerant fake and then emit half a type; this throws instead.
  if (!Array.isArray(schemas)) {
    throw new TypeError('makeEnvDts takes an ARRAY of schemas, got ' + typeof schemas);
  }
  const publicSchema = schemas.find((s) => s.name === 'public');
  const others = schemas.filter((s) => s.name !== 'public');
  const blocks = Object.keys((publicSchema && publicSchema.tables) || {}).map((name) =>
    tableBlock(publicSchema.tables[name], '    '));
  const body = blocks.length > 0 ? '\n' + blocks.join('\n') + '\n  ' : '';
  const schemaBlocks = others.map((schema) => {
    const inner = Object.keys(schema.tables).map((name) => tableBlock(schema.tables[name], '      '));
    return '    ' + schema.name + ': {\n' + inner.join('\n') + '\n    };';
  });
  const schemasBody = schemaBlocks.length > 0 ? '\n' + schemaBlocks.join('\n') + '\n  ' : '';
  return '// AUTO-GENERATED by @palbase/backend — DO NOT EDIT.\n' +
    '// Regenerated from db/*.ts by ` + "`palbase build`" + ` and by every deploy.\n' +
    '// Augments the @palbase/backend/env ` + "`Tables`" + ` interface so ` + "`Database.tables.*`" + `\n' +
    '// is typed with no import and no generic.\n' +
    '\n' +
    'declare module "@palbase/backend/env" {\n' +
    '  interface Tables {' + body + '}\n' +
    '  interface Schemas {' + schemasBody + '}\n' +
    '}\n' +
    '\n' +
    'export {};\n';
}
module.exports = { uuid, text, integer, boolean, timestamp, jsonb, enumType, defineTable, defineSchema, makeEnvDts };
`

// TestGenerateEnvTypes drives the real Go bridge end-to-end: a fixture project
// declaring TWO schemas in db/ (importing @palbase/backend) is esbuild-bundled
// through one generated entry, the embedded env-gen.js bridge evaluates it and
// calls makeEnvDts ONCE with both declarations, and palbase-env.d.ts lands at
// the project root with the expected typed tables.
//
// Two schemas rather than one because one proves nothing the cutover changed:
// the single-file path also passes an array of length 1. The second file is
// what catches a bridge that reads only the first, or reads them in readdir
// order — `billing` sorts before `public`, so an unsorted reader gets a
// different answer here than on a machine whose readdir disagrees.
func TestGenerateEnvTypes(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not on PATH")
	}

	root := t.TempDir()

	// Self-sufficient fixture: bundleSchemaTS shells out to `npx --yes esbuild`
	// from the project root — seed esbuild locally so npx resolves the fixture's
	// own bin (same trap as the build tests: a fixture node_modules without
	// esbuild makes npx resolution host-dependent).
	seedEsbuild(t, root)

	// The project's installed @palbase/backend (resolved via NODE_PATH).
	mustWrite(t, root, "node_modules/@palbase/backend/package.json",
		`{"name":"@palbase/backend","version":"0.0.0-test","main":"index.js"}`)
	mustWrite(t, root, "node_modules/@palbase/backend/index.js", fixtureBackendPkg)

	// db/public.ts — the documented `export default defineSchema(name, …)` form.
	mustWrite(t, root, "db/public.ts", `
import { defineSchema, defineTable, uuid, text, boolean, timestamp } from "@palbase/backend";

const todos = defineTable("todos", {
  columns: {
    id: uuid().defaultRandom(),
    title: text().notNull(),
    done: boolean().default(false),
    created_at: timestamp().defaultNow(),
  },
  primaryKey: ["id"],
  rls: true,
});

export default defineSchema("public", { tables: [todos] });
`)

	// db/billing.ts — the second schema. It sorts BEFORE public, which is the
	// point: the bridge must hand them over in file-name order regardless.
	mustWrite(t, root, "db/billing.ts", `
import { defineSchema, defineTable, uuid, integer } from "@palbase/backend";

const invoices = defineTable("invoices", {
  columns: {
    id: uuid().defaultRandom(),
    amount_cents: integer().notNull(),
  },
  primaryKey: ["id"],
});

export default defineSchema("billing", { tables: [invoices] });
`)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := generateEnvTypes(ctx, root, filepath.Join(root, "node_modules"), nil); err != nil {
		t.Fatalf("generateEnvTypes: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(EnvTypesPath())))
	if err != nil {
		t.Fatalf("read palbase-env.d.ts: %v", err)
	}

	want := "// AUTO-GENERATED by @palbase/backend — DO NOT EDIT.\n" +
		"// Regenerated from db/*.ts by `palbase build` and by every deploy.\n" +
		"// Augments the @palbase/backend/env `Tables` interface so `Database.tables.*`\n" +
		"// is typed with no import and no generic.\n" +
		"\n" +
		"declare module \"@palbase/backend/env\" {\n" +
		"  interface Tables {\n" +
		"    todos: {\n" +
		"      row: {\n" +
		"        id: string;\n" +
		"        title: string;\n" +
		"        done: boolean;\n" +
		"        created_at: string;\n" +
		"      };\n" +
		"      insert: {\n" +
		"        id?: string;\n" +
		"        title: string;\n" +
		"        done?: boolean;\n" +
		"        created_at?: string;\n" +
		"      };\n" +
		"    };\n" +
		"  }\n" +
		"  interface Schemas {\n" +
		"    billing: {\n" +
		"      invoices: {\n" +
		"        row: {\n" +
		"          id: string;\n" +
		"          amount_cents: number;\n" +
		"        };\n" +
		"        insert: {\n" +
		"          id?: string;\n" +
		"          amount_cents: number;\n" +
		"        };\n" +
		"      };\n" +
		"    };\n" +
		"  }\n" +
		"}\n" +
		"\n" +
		"export {};\n"

	if string(got) != want {
		t.Fatalf("palbase-env.d.ts mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The generated output must never leak the phantom ColumnBuilder type.
	if strings.Contains(string(got), "ColumnBuilder") {
		t.Fatalf("generated palbase-env.d.ts leaked ColumnBuilder:\n%s", got)
	}
}

// A PROJECT WITH NO DATABASE STILL GETS THE ONE FILE.
//
// This asserted the opposite — no db/, no `palbase-env.d.ts` — and that was
// right while the stack's names had a file of their own. They do not any
// more: `palbase-stack.d.ts` is retired (FR-008) and its block lives here, so
// skipping the render would leave a linked project with no `Secrets`, no
// `Flags`, no `Buckets` and no `Roles` — for the one reason that has nothing
// to do with any of them. The `Tables` block is simply empty, and rendering
// it needs no schema bundle at all — `bundleSchemaTS`/npx never run on this
// path any more.
//
// PUSH KEEPS THE NO-OP: it asks whether a declaration would refuse to boot,
// and a project with nothing declared has nothing that can.
func TestGenerateEnvTypesNoSchema(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	root := t.TempDir()
	// A plain v1 project: endpoints/ but no db/ at all.
	mustWrite(t, root, "endpoints/hello/get.ts", "export default { handler: async () => ({}) };\n")

	if err := validateSchemaDeclaration(context.Background(), root, filepath.Join(root, "node_modules")); err != nil {
		t.Fatalf("a push of a project with no db/ must evaluate nothing, got: %v", err)
	}

	mustWrite(t, root, "node_modules/@palbase/backend/package.json",
		`{"name":"@palbase/backend","version":"0.0.0-test","main":"index.js"}`)
	mustWrite(t, root, "node_modules/@palbase/backend/index.js", fixtureBackendPkg)

	if err := generateEnvTypes(context.Background(), root, filepath.Join(root, "node_modules"), nil); err != nil {
		t.Fatalf("generateEnvTypes (no schema): %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(EnvTypesPath())))
	if err != nil {
		t.Fatalf("a project with no db/ got no generated file: %v", err)
	}
	if got := string(raw); !strings.Contains(got, "interface Tables {}") ||
		!strings.HasSuffix(strings.TrimRight(got, "\n"), "export {};") {
		t.Fatalf("the file is not an empty-tables module:\n%s", got)
	}
}
