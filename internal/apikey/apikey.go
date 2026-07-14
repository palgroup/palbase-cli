// Package apikey wires `palbase apikey ...` over the Management API v2.
//
// API keys belong to an ENVIRONMENT, not a Project: the ref embedded in every
// key (`pb_<environmentRef>_c<random>`) IS the Environment's ref, so a key can
// never straddle two runtimes. Routes:
//
//	GET    /api/v2/projects/{projectId}/environments/{environmentRef}/api-keys             list
//	GET    /api/v2/projects/{projectId}/environments/{environmentRef}/api-keys?reveal=true reveal
//	POST   /api/v2/projects/{projectId}/environments/{environmentRef}/api-keys             create
//	DELETE /api/v2/projects/{projectId}/environments/{environmentRef}/api-keys/{keyId}     revoke
//
// The Environment is the SELECTED one (`palbase env use`), overridable headlessly
// with the global --project / --environment.
package apikey

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// REST is the transport subset the apikey commands need.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers carries the lazily-built REST client + the shared selection resolver.
type Resolvers struct {
	REST      func() REST
	Selection func() *selection.Resolver
}

// Cmd returns the `palbase apikey` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apikey",
		Short: "Manage the selected environment's API keys",
	}
	cmd.AddCommand(
		listCmd(r),
		createCmd(r),
		revealCmd(r),
		revokeCmd(r),
	)
	return cmd
}

// keysPath is `.../environments/{ref}/api-keys[/{keyId}]`.
func keysPath(projectID, ref, keyID string) string {
	p := "/api/v2/projects/" + projectID + "/environments/" + ref + "/api-keys"
	if keyID != "" {
		p += "/" + keyID
	}
	return p
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

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List the environment's API keys (metadata only — no secrets)",
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			var rows []listRow
			if err := r.REST().Do(cmd.Context(), http.MethodGet,
				keysPath(sel.ProjectID, sel.Ref(), ""), nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No API keys.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
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

func createCmd(r Resolvers) *cobra.Command {
	var (
		name    string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Args:  cobra.NoArgs,
		Short: "Mint a publishable key for the environment (plaintext shown once)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			var created struct {
				ID             string `json:"id"`
				EnvironmentRef string `json:"environment_ref"`
				Name           string `json:"name"`
				Scope          string `json:"scope"`
				IsDefault      bool   `json:"is_default"`
				Plaintext      string `json:"plaintext"`
				LookupPrefix   string `json:"lookup_prefix"`
			}
			if err := r.REST().Do(cmd.Context(), http.MethodPost,
				keysPath(sel.ProjectID, sel.Ref(), ""),
				map[string]any{"name": name}, &created); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), created)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ created publishable key %s\n", created.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", created.Plaintext)
			fmt.Fprintln(cmd.OutOrStdout(), "  (shown once — store it now)")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Key name (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func revealCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "reveal",
		Args:  cobra.NoArgs,
		Short: "Reveal the environment's default publishable key (needs api-keys:write)",
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			var revealed struct {
				EnvironmentRef string  `json:"environmentRef"`
				PublishableKey *string `json:"publishableKey"`
				Keys           []struct {
					ID    string `json:"id"`
					Scope string `json:"scope"`
				} `json:"keys"`
			}
			if err := r.REST().Do(cmd.Context(), http.MethodGet,
				keysPath(sel.ProjectID, sel.Ref(), "")+"?reveal=true", nil, &revealed); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), revealed)
			}
			if revealed.PublishableKey != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "publishable:  %s\n", *revealed.PublishableKey)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func revokeCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "revoke <keyId>",
		Args:  cobra.ExactArgs(1),
		Short: "Revoke an API key by id",
		RunE: func(cmd *cobra.Command, args []string) error {
			keyID := args[0]
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			if err := r.REST().Do(cmd.Context(), http.MethodDelete,
				keysPath(sel.ProjectID, sel.Ref(), keyID), nil, nil); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "id": keyID})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ revoked %s\n", keyID)
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

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
