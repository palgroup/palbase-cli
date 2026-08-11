// Package db provides the `palbase db` subcommand group: plan / apply / reset.
//
// The authoritative schema lives in db/schema.ts and there are no migration
// files. `db plan` asks Studio (which proxies the orchestrator's
// POST /internal/schema-plan/{ref}) what it would take to make the live database
// match that declaration; `db apply` runs that plan in one transaction.
//
// Both run SERVER-side: only the cluster can reach a tenant's database, so the
// orchestrator runs the tenant's own backend-runtime image as a throwaway Job
// which builds the declared schema in a shadow database and lets Postgres itself
// take the difference. The CLI ships the db/schema.ts SOURCE STRING and never
// touches the database.
//
// `db diff` and `db check` are gone with the migration files they served. A diff
// wrote db/migrations/<ts>_<name>.sql; a check asked whether the schema had
// drifted without one. Neither question exists now: the plan IS the diff, it is
// computed against the database as it is at that moment, and the deploy applies
// it.
package db

import (
	"fmt"
	"os"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// Resolvers carries the lazily-built Studio client, populated by
// PersistentPreRunE on the root command before any subcommand fires
// (mirrors secret.Resolvers).
type Resolvers struct {
	Studio    func() *studio.Client
	Selection func() *selection.Resolver
}

func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Manage the selected environment's database schema",
		Long: `Commands to keep db/schema.ts and the live database in sync.

  palbase db plan    Show what it would take to make the live database match
                     db/schema.ts. Applies nothing.
  palbase db apply   Apply that plan.
  palbase db reset   Drop the schema and rebuild it from db/schema.ts (DESTRUCTIVE).

There are no migration files. The plan is computed server-side against the
database as it is right now: db/schema.ts is built for real in a throwaway
shadow database and Postgres itself takes the difference.`,
	}
	cmd.AddCommand(
		planCmd(r),
		applyCmd(r),
		resetCmd(r),
	)
	return cmd
}

// schemaPath is the project-relative path to the declared schema source.
const schemaPath = "db/schema.ts"

// readSchema reads db/schema.ts (relative to cwd) as a string, returning a
// clear error when it is missing — the diff is meaningless without it.
func readSchema() (string, error) {
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no %s in this directory — run from a project with a declared schema", schemaPath)
		}
		return "", fmt.Errorf("read %s: %w", schemaPath, err)
	}
	return string(data), nil
}
