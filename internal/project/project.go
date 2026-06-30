// Package project wires `palbase project ...` subcommands over the
// Management API REST transport (Authorization: DPoP <pat> + per-request
// proof). The legacy tRPC path is gone for project — only the REST
// client is used here (Spec 1, S5.4).
// project.delete is tRPC-only (no REST route) and uses the Studio client.
package project

import (
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"fmt"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// Resolvers lets the cobra wiring read the lazily-built REST client from
// main.go's PersistentPreRunE — mirrors backend.Resolvers' pattern.
type Resolvers struct {
	REST   func() REST
	Studio func() *studio.Client
}

// Cmd returns the `palbase project` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Create, list, inspect, and delete Palbase projects",
	}
	cmd.AddCommand(
		listCmd(r.REST),
		createCmd(r.REST),
		statusCmd(r.REST),
		deleteCmd(r.Studio),
	)
	return cmd
}

type projectRow struct {
	ID        string    `json:"id"`
	Ref       string    `json:"ref"`
	Name      string    `json:"name"`
	Tier      string    `json:"tier"`
	Region    string    `json:"region"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func listCmd(rest func() REST) *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects you have access to",
		RunE: func(cmd *cobra.Command, args []string) error {
			var rows []projectRow
			if err := rest().Do(cmd.Context(), http.MethodGet, "/api/v1/projects", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No projects yet — create one with `palbase project create`.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "REF\tNAME\tTIER\tREGION\tSTATUS\tCREATED")
			for _, p := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					p.Ref, p.Name, p.Tier, p.Region, p.Status,
					p.CreatedAt.Format("2006-01-02"))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}
