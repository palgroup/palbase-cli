package backend

// schema_sources.go — reading the declaration a project makes about its database.
//
// A project declares its database in `db/`, ONE FILE PER SCHEMA: `db/public.ts`
// is the schema everything is served from, and `db/<name>.ts` adds another.
// There is no `db/schema.ts` any more, and that is not a rename — a single file
// made every schema one file, which is the file nobody could review.
//
// THIS IS THE CLI'S COPY OF A RULE THE STACK ALSO HOLDS.
// The stack's own reader is `internal/deploy.ReadSchemaSources` in the palbase
// repository; this is a separate Go module and cannot import it. So the two
// will drift unless something stops them, and the thing that stops them is that
// the CLI's copy is only ever a PRE-FLIGHT: every refusal below is also made by
// the stack, on the source it actually received. What this side buys is that
// the person hears it on their terminal instead of after a round trip — never
// that it is the only place the rule lives.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// SchemaDir is where a project declares its database.
const SchemaDir = "db"

// PublicSchemaFile is the declaration a project with a database must carry.
// A constant because several refusals quote it, and a refusal that misspells
// the file it asks for is a refusal nobody can act on.
const PublicSchemaFile = "db/public.ts"

// LegacySchemaFile is the layout this replaced. Kept as a NAME — not as a path
// anything reads — so the refusal that retires it cannot drift from the file it
// is about.
const LegacySchemaFile = "db/schema.ts"

// MigrationGuide is where the move off the old layout is written down.
const MigrationGuide = "/docs/backend/schema-migration"

// ErrNoSchema means the project declares no database at all.
//
// Distinct from an empty declaration, because the two have opposite meanings
// and only one of them is safe: an empty declaration says "this project has no
// tables", which — compared against a live database that has them — is a
// request to drop everything.
var ErrNoSchema = errors.New("this project declares no database: expected " + PublicSchemaFile)

// SchemaSource is one schema declaration, with the name its file gives it.
//
// Name comes from the FILE. It is not the identity — `defineSchema("billing", …)`
// is — it is the claim the directory makes about where that identity lives, and
// the stack refuses the two when they disagree.
type SchemaSource struct {
	Name   string
	Source string
}

// Path renders the source as the path its author typed.
func (s SchemaSource) Path() string { return SchemaDir + "/" + s.Name + ".ts" }

// ReadSchemaSources returns every schema projectDir declares, SORTED BY FILE
// NAME.
//
// The sort is load-bearing rather than tidy: readdir order is not specified,
// and an unsorted set would hand the evaluator a different positional answer
// from one run to the next — so the same unchanged schema would diff clean on
// one push and dirty on the next, and nobody would trust either.
func ReadSchemaSources(projectDir string) ([]SchemaSource, error) {
	dir := filepath.Join(projectDir, SchemaDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoSchema
	}
	if err != nil {
		return nil, fmt.Errorf("read %s/: %w", SchemaDir, err)
	}

	out := make([]SchemaSource, 0, len(entries))
	legacy := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".ts")
		if name == "schema" {
			legacy = true
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", SchemaDir, e.Name(), err)
		}
		// A file somebody emptied is not a declaration that there are no
		// tables. Carrying it would ask the database to drop that schema.
		if strings.TrimSpace(string(raw)) == "" {
			continue
		}
		out = append(out, SchemaSource{Name: name, Source: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	hasPublic := slices.ContainsFunc(out, func(s SchemaSource) bool { return s.Name == "public" })
	// Checked BEFORE the missing-public refusal below: for this project the two
	// are the same fact, and only this one names the fix.
	if legacy && !hasPublic {
		return nil, fmt.Errorf(
			"%s is the old layout: rename it to %s and declare each table with "+
				"defineTable(\"name\", {…}) inside defineSchema(\"public\", { tables: [ … ] }) — "+
				"the move is written out at %s",
			LegacySchemaFile, PublicSchemaFile, MigrationGuide)
	}
	if len(out) == 0 {
		return nil, ErrNoSchema
	}
	// `public` is the schema the stack introspects and /v1/db serves. A project
	// that declares only db/billing.ts has none, and every comparison
	// downstream would be made against a schema nobody wrote.
	if !hasPublic {
		return nil, fmt.Errorf(
			"%s/ declares %s but not %s: a project with a database declares its public schema — "+
				"add %s with defineSchema(\"public\", { tables: [ … ] })",
			SchemaDir, strings.Join(SchemaSourcePaths(out), ", "), PublicSchemaFile, PublicSchemaFile)
	}
	return out, nil
}

// SchemaSourcePaths renders the sources as the paths their author typed, for a
// message that has to say which files it read.
func SchemaSourcePaths(sources []SchemaSource) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.Path())
	}
	return out
}

// HasSchemaDeclaration reports whether projectDir declares a database, without
// deciding whether the declaration is well-formed. It is the question
// "is this a backend checkout", and the legacy layout answers NO on purpose:
// the whole point of the cutover is that the old file is not a declaration any
// more, and treating it as one would let a stale project through the door and
// fail later with a message about something else.
func HasSchemaDeclaration(projectDir string) bool {
	info, err := os.Stat(filepath.Join(projectDir, PublicSchemaFile))
	return err == nil && !info.IsDir()
}

// HasLegacySchemaFile reports whether the retired single-file layout is still
// on disk, so a refusal can say THAT rather than "no schema here" — the second
// is true and useless to somebody looking straight at their db/schema.ts.
func HasLegacySchemaFile(projectDir string) bool {
	info, err := os.Stat(filepath.Join(projectDir, LegacySchemaFile))
	return err == nil && !info.IsDir()
}
