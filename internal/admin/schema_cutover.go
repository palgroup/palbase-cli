package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// Studio is the tRPC surface this command needs — the same client `palbase db`
// uses. Declared here as an interface so the command is testable without one.
type Studio interface {
	Mutation(ctx context.Context, path string, input any, out any) error
}

// schemaRenderResult is what backend.schemaRender returns.
type schemaRenderResult struct {
	Source string `json:"source"`
}

// schemaCutoverCmd wires `palbase admin schema-cutover --environment <ref>`.
//
// It moves a project off hand-written migration files by generating its
// db/schema.ts FROM the database it already has. That is what makes the cutover
// non-destructive: the first plan compares the database with itself and comes back
// empty. A declaration written by hand instead would produce a first plan full of
// drops, and each one would be real.
//
// Deliberately an operator command rather than part of the public CLI: a tenant
// developer never needs it, and `palbase db` stays plan/apply.
func schemaCutoverCmd(r Resolvers) *cobra.Command {
	var ref string
	var write bool
	var out string

	cmd := &cobra.Command{
		Use:   "schema-cutover",
		Short: "Generate db/schema.ts from an environment's live database",
		Long: `Read an environment's live schema and write the db/schema.ts that declares it.

This is the cutover for a project still carrying hand-written migration files:
because the declaration is generated FROM the database, the project's first
` + "`palbase db plan`" + ` is empty.

Without --write nothing is written; the generated file is printed instead.
With --write the existing file is backed up next to it before being replaced.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(ref) == "" {
				return errors.New("--environment is required")
			}
			if r.Studio == nil {
				return errors.New("no Studio client configured")
			}

			var res schemaRenderResult
			if err := r.Studio().Mutation(cmd.Context(), "backend.schemaRender", map[string]any{"ref": ref}, &res); err != nil {
				return fmt.Errorf("backend.schemaRender: %w", err)
			}
			if strings.TrimSpace(res.Source) == "" {
				return errors.New("the renderer returned nothing; nothing was written")
			}

			if !write {
				fmt.Fprint(cmd.OutOrStdout(), res.Source)
				fmt.Fprintf(cmd.ErrOrStderr(), "\n(nothing written — pass --write to replace %s)\n", out)
				return nil
			}
			return writeSchemaFile(cmd.ErrOrStderr(), out, res.Source)
		},
	}

	cmd.Flags().StringVar(&ref, "environment", "", "environment ref to render")
	cmd.Flags().BoolVar(&write, "write", false, "write the generated schema instead of printing it")
	cmd.Flags().StringVar(&out, "out", filepath.Join("db", "schema.ts"), "where to write it")
	return cmd
}

// namedExports finds what a schema file exports BESIDES its default.
//
// A generated db/schema.ts has exactly one export: the schema. A hand-written one
// often has more — penny's held CATEGORY_VALUES, the list six modules validated
// against — and replacing the file takes those with it. The build failure that
// follows names an import, not a cutover, so the connection is not obvious; and
// even if the exports were carried across once, the NEXT regeneration would drop
// them again. The only stable answer is that they do not live in a generated file.
func namedExports(source string) []string {
	var found []string
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export default") {
			continue
		}
		rest := strings.TrimPrefix(line, "export ")
		// `export { a, b }` and `export * from "..."` re-export without naming a
		// declaration; report them as written.
		if strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, "*") {
			found = append(found, strings.TrimSuffix(line, ";"))
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			continue
		}
		// export <kind> <name>, with `async function` and `declare` taking one more.
		name := fields[1]
		if fields[0] == "async" || fields[0] == "declare" {
			if len(fields) < 3 {
				continue
			}
			name = fields[2]
		}
		if idx := strings.IndexAny(name, "(:=<"); idx > 0 {
			name = name[:idx]
		}
		if name != "" {
			found = append(found, name)
		}
	}
	return found
}

// writeSchemaFile replaces the declaration, keeping the previous one beside it.
//
// The backup is not a convenience: what is being overwritten is the only record of
// what the project MEANT, including the comments its authors wrote. The generated
// file describes what the database IS, which is the same thing only if nothing has
// drifted — and an operator who discovers otherwise needs the original back.
func writeSchemaFile(progress io.Writer, path, source string) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	if existing, err := os.ReadFile(path); err == nil {
		if lost := namedExports(string(existing)); len(lost) > 0 {
			return fmt.Errorf("%s exports %s besides the schema itself, and the generated file would not — "+
				"every module importing them breaks, and it breaks again on the next regeneration.\n"+
				"Move them into their own module and update the imports first, then run this again",
				path, strings.Join(lost, ", "))
		}
		backup := path + ".before-cutover"
		if err := os.WriteFile(backup, existing, 0o644); err != nil {
			return fmt.Errorf("back up %s: %w", path, err)
		}
		fmt.Fprintf(progress, "backed up %s → %s\n", path, backup)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(progress, "wrote %s (%d bytes)\nnext: run `palbase db plan` — it should be empty\n", path, len(source))
	return nil
}
