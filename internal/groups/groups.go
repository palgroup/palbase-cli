// Package groups wires the `palbase groups ...` subcommand group: list the
// caller's umbrellas (project groups) and each umbrella's environments. A
// group is the unit that owns a product's per-environment projects and its
// registered apps — `palbase apps ...` and `palbase ios link` need its id,
// and before this command the ONLY place to discover it was the Studio UI.
//
// Transport: Studio tRPC (`groups.*`), same user-JWT client as `apps`.
// groups.mine resolves off the session user id server-side (a user can only
// ever see their own groups); the detail reads are membership-gated with
// NOT_FOUND collapsing (no existing-vs-missing oracle).
package groups

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// Studio is the tRPC transport subset the groups commands need.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries the lazily-built Studio client (apps.Resolvers pattern).
type Resolvers struct {
	Studio func() Studio
}

type groupRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Plan string `json:"plan"`
}

type envRow struct {
	Ref            string  `json:"ref"`
	EnvPreset      *string `json:"env_preset"`
	EnvDisplayName *string `json:"env_display_name"`
	Status         string  `json:"status"`
}

// Cmd returns the `palbase groups` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "List your groups (umbrellas) and their environments",
		Long: `A group is the umbrella that owns a product's per-environment projects and
its registered apps (ios/android/web).

  palbase groups list             List the groups you belong to.
  palbase groups envs <groupId>   List a group's environments (project refs).

The group id is what 'palbase apps ...' and 'palbase ios link --group' take.`,
	}
	cmd.AddCommand(listCmd(r.Studio), envsCmd(r.Studio))
	return cmd
}

func listCmd(studioFn func() Studio) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the groups you belong to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows := []groupRow{}
			if err := studioFn().Query(cmd.Context(), "groups.mine", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No groups. Create a project first — `palbase project create <ref> --name \"...\"`.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPLAN")
			for _, g := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", g.ID, g.Name, g.Plan)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func envsCmd(studioFn func() Studio) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "envs <groupId>",
		Short: "List a group's environments (project refs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rows := []envRow{}
			if err := studioFn().Query(cmd.Context(), "groups.environments",
				map[string]any{"grpId": args[0]}, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No environments.")
				return nil
			}
			str := func(p *string) string {
				if p == nil {
					return "-"
				}
				return *p
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "REF\tPRESET\tNAME\tSTATUS")
			for _, e := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Ref, str(e.EnvPreset), str(e.EnvDisplayName), e.Status)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
