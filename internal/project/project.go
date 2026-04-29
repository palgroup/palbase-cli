// Package project wires `palbase project ...` subcommands. We talk to
// Studio's tRPC layer with the same Bearer flow `palbase backend ...`
// uses, so org-membership + tier checks stay server-side.
package project

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/spf13/cobra"
)

// Resolvers lets the cobra wiring read the lazily-built Studio client
// from main.go's PersistentPreRunE — see backend.Resolvers for the
// same pattern.
type Resolvers struct {
	Studio func() *studio.Client
}

// Cmd returns the `palbase project` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "List and inspect Palbase projects",
	}
	cmd.AddCommand(listCmd(r))
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
	OrgID     string    `json:"org_id"`
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects you have access to",
		RunE: func(cmd *cobra.Command, args []string) error {
			var rows []projectRow
			if err := r.Studio().Query(cmd.Context(), "project.list", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No projects yet — create one at the dashboard.")
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

	// satisfy unused-context lint when the command runs without args
	_ = context.Background
	return cmd
}
