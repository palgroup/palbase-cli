// Package apikey wires `palbase apikey ...` subcommands over the
// Management API REST transport. Routes (Spec 1, S3 OpenAPI):
//
//	GET    /api/v1/projects/{ref}/api-keys             list (api-keys:read)
//	GET    /api/v1/projects/{ref}/api-keys?reveal=true reveal (api-keys:write)
//	POST   /api/v1/projects/{ref}/api-keys             create (api-keys:write)
//	DELETE /api/v1/projects/{ref}/api-keys/{id}        revoke (api-keys:write)
package apikey

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// REST is the transport subset the apikey commands need.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers carries the lazily-built REST client.
type Resolvers struct {
	REST func() REST
}

// Cmd returns the `palbase apikey` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apikey",
		Short: "Manage a project's API keys",
	}
	cmd.AddCommand(
		listCmd(r.REST),
		createCmd(r.REST),
		revealCmd(r.REST),
		revokeCmd(r.REST),
	)
	return cmd
}

// listRow mirrors the api-keys list metadata (never plaintext).
type listRow struct {
	ID           string  `json:"id"`
	Name         *string `json:"name"`
	Scope        string  `json:"scope"`
	LookupPrefix string  `json:"lookup_prefix"`
	IsDefault    bool    `json:"is_default"`
	CreatedAt    string  `json:"created_at"`
	RevokedAt    *string `json:"revoked_at"`
	LastUsedAt   *string `json:"last_used_at"`
}

func listCmd(rest func() REST) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list <ref>",
		Args:  cobra.ExactArgs(1),
		Short: "List a project's API keys (metadata only — no secrets)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			var rows []listRow
			if err := rest().Do(cmd.Context(), http.MethodGet,
				"/api/v1/projects/"+ref+"/api-keys", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No API keys.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tTYPE\tPREFIX\tDEFAULT\tREVOKED")
			for _, k := range rows {
				name := ""
				if k.Name != nil {
					name = *k.Name
				}
				revoked := "no"
				if k.RevokedAt != nil {
					revoked = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%v\t%s\n",
					k.ID, name, keyType(k.Scope), k.LookupPrefix, k.IsDefault, revoked)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func createCmd(rest func() REST) *cobra.Command {
	var (
		name    string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "create <ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Create a new API key (plaintext shown once)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			var created struct {
				ID           string `json:"id"`
				EndpointRef  string `json:"endpoint_ref"`
				Name         string `json:"name"`
				Scope        string `json:"scope"`
				IsDefault    bool   `json:"is_default"`
				Plaintext    string `json:"plaintext"`
				LookupPrefix string `json:"lookup_prefix"`
			}
			if err := rest().Do(cmd.Context(), http.MethodPost,
				"/api/v1/projects/"+ref+"/api-keys",
				map[string]any{"name": name}, &created); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(created)
			}
			fmt.Fprintf(os.Stdout, "✓ created publishable key %s\n", created.ID)
			fmt.Fprintf(os.Stdout, "  %s\n", created.Plaintext)
			fmt.Fprintln(os.Stdout, "  (shown once — store it now)")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Key name (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func revealCmd(rest func() REST) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "reveal <ref>",
		Args:  cobra.ExactArgs(1),
		Short: "Reveal the project's default publishable key plaintext (needs api-keys:write)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			var revealed struct {
				PublishableKey *string `json:"publishableKey"`
				Keys           []struct {
					ID    string `json:"id"`
					Scope string `json:"scope"`
				} `json:"keys"`
			}
			if err := rest().Do(cmd.Context(), http.MethodGet,
				"/api/v1/projects/"+ref+"/api-keys?reveal=true", nil, &revealed); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(revealed)
			}
			if revealed.PublishableKey != nil {
				fmt.Fprintf(os.Stdout, "publishable:  %s\n", *revealed.PublishableKey)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func revokeCmd(rest func() REST) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "revoke <ref> <id>",
		Args:  cobra.ExactArgs(2),
		Short: "Revoke an API key by id",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, id := args[0], args[1]
			if err := rest().Do(cmd.Context(), http.MethodDelete,
				"/api/v1/projects/"+ref+"/api-keys/"+id, nil, nil); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(map[string]any{"ok": true, "id": id})
			}
			fmt.Fprintf(os.Stdout, "✓ revoked %s\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func keyType(scope string) string {
	if scope == "c" {
		return "publishable"
	}
	return scope
}

func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
