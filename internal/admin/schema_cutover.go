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
