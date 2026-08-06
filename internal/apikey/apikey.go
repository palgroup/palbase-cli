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
//	POST   .../api-keys/{keyId}/rotate                                                     rotate
//
// The Environment is the SELECTED one (`palbase env use`), overridable headlessly
// with the global --project / --environment.
package apikey

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/palgroup/palbase-cli/internal/transport"
	"io"
	"net/http"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// REST is the transport subset the apikey commands need.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
	// DoIdempotent carries an Idempotency-Key. Key creation REQUIRES one
	// server-side; Do alone gets a 400.
	DoIdempotent(ctx context.Context, method, path string, body, out any, idempotencyKey string) error
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
		rotateCmd(r),
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
				keysPath(sel.ProjectID, sel.EnvironmentRef(), ""), nil, &rows); err != nil {
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
			// The v2 route REQUIRES an Idempotency-Key on key creation — without
			// one it answers 400 and the command is simply unusable. Minted per
			// invocation: a create is one logical mutation and the CLI does not
			// retry it.
			if err := r.REST().DoIdempotent(cmd.Context(), http.MethodPost,
				keysPath(sel.ProjectID, sel.EnvironmentRef(), ""),
				map[string]any{"name": name}, &created,
				transport.NewIdempotencyKey()); err != nil {
				return err
			}
			if err := validateEnvironmentRef("create", sel.EnvironmentRef(), created.EnvironmentRef); err != nil {
				return err
			}
			if err := selection.ValidateRuntimeBinding(sel.EnvironmentRef(), created.EnvironmentRef, created.Plaintext); err != nil {
				return fmt.Errorf("apikey create returned an invalid publishable credential: %w", err)
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
				EnvironmentRef string  `json:"environment_ref"`
				PublishableKey *string `json:"publishable_key"`
				Keys           []struct {
					ID    string `json:"id"`
					Scope string `json:"scope"`
				} `json:"keys"`
			}
			if err := r.REST().Do(cmd.Context(), http.MethodGet,
				keysPath(sel.ProjectID, sel.EnvironmentRef(), "")+"?reveal=true", nil, &revealed); err != nil {
				return err
			}
			if err := validateEnvironmentRef("reveal", sel.EnvironmentRef(), revealed.EnvironmentRef); err != nil {
				return err
			}
			if revealed.PublishableKey == nil {
				return fmt.Errorf("apikey reveal response is missing the publishable credential")
			}
			if err := selection.ValidateRuntimeBinding(sel.EnvironmentRef(), revealed.EnvironmentRef, *revealed.PublishableKey); err != nil {
				return fmt.Errorf("apikey reveal returned an invalid publishable credential: %w", err)
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
	var (
		force   bool
		jsonOut bool
	)
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
				keysPath(sel.ProjectID, sel.EnvironmentRef(), keyID),
				map[string]any{"force": force}, nil); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "id": keyID})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ revoked %s\n", keyID)
			return nil
		},
	}
	// The server refuses to revoke an app-bound key unless force is set, and its
	// refusal says so in those words: "Pass force to revoke anyway (do this for a
	// leaked key…)". The flag did not exist, and neither did a released `rotate`
	// — so at the one moment the instruction matters, following it was impossible.
	// Prefer `rotate` when a replacement is wanted; this is the deliberate
	// no-replacement path.
	cmd.Flags().BoolVar(&force, "force", false,
		"revoke even when the key is bound to an app — breaks already-installed builds, which cannot re-fetch it")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// rotateCmd is the incident-response command: a leaked key is answered by
// rotating it, not by revoking it. Until this existed the only way to rotate was
// the web UI, so the terminal — the fastest surface at the moment a key leaks —
// could not reach it.
//
// What rotate buys you over revoke, verified against the orchestrator: ONE
// durable operation mints the replacement (same name, scope and app binding) and
// revokes the source in the SAME transaction, so the replacement exists at the
// instant the old secret dies. Revoke leaves you with nothing and a second
// command to run. It does NOT buy you a grace window — there is none, see below.
func rotateCmd(r Resolvers) *cobra.Command {
	var (
		force   bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "rotate <keyId>",
		Args:  cobra.ExactArgs(1),
		Short: "Rotate an API key: mint a fresh secret and revoke the old one atomically",
		RunE: func(cmd *cobra.Command, args []string) error {
			keyID := args[0]
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			var rotated struct {
				ID             string `json:"id"`
				EnvironmentRef string `json:"environment_ref"`
				Name           string `json:"name"`
				Scope          string `json:"scope"`
				IsDefault      bool   `json:"is_default"`
				Plaintext      string `json:"plaintext"`
				LookupPrefix   string `json:"lookup_prefix"`
			}
			// Rotation REQUIRES an Idempotency-Key server-side: the durable
			// mutation id is derived from it, so a retry joins the same rotation
			// instead of minting a second key. Minted per invocation — one
			// command is one logical rotation and the CLI does not retry it.
			if err := r.REST().DoIdempotent(cmd.Context(), http.MethodPost,
				keysPath(sel.ProjectID, sel.EnvironmentRef(), keyID)+"/rotate",
				map[string]any{"force": force}, &rotated,
				transport.NewIdempotencyKey()); err != nil {
				return err
			}
			if err := validateEnvironmentRef("rotate", sel.EnvironmentRef(), rotated.EnvironmentRef); err != nil {
				return err
			}
			// The new secret must belong to the environment we asked about. A
			// rotate that hands back a credential bound elsewhere is the same
			// silent cross-environment hazard create/reveal already guard.
			if err := selection.ValidateRuntimeBinding(sel.EnvironmentRef(), rotated.EnvironmentRef, rotated.Plaintext); err != nil {
				return fmt.Errorf("apikey rotate returned an invalid credential: %w", err)
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), rotated)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ rotated %s → %s\n", keyID, rotated.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", rotated.Plaintext)
			fmt.Fprintln(cmd.OutOrStdout(), "  (shown once — store it now)")
			// Measured, not assumed: the rotate commit runs
			// `UPDATE api_keys SET revoked_at=now()` on the source in the same
			// transaction that inserts the replacement, and the cleanup deletes
			// the source's gateway blob right after publishing the new one. The
			// only overlap is however long that cleanup takes — an implementation
			// window, NOT a grace period, and nothing may be built on it.
			fmt.Fprintln(cmd.OutOrStdout(), "  The previous secret is revoked — roll your clients over now.")
			return nil
		},
	}
	// An app-bound key is compiled into shipped builds, so rotating it
	// invalidates the secret those builds carry. The server refuses without
	// this; the flag exists so the caller says it out loud.
	cmd.Flags().BoolVar(&force, "force", false, "Rotate even when the key is app-bound (breaks shipped builds)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func keyType(scope string) string {
	if scope == "c" {
		return "publishable"
	}
	return scope
}

func validateEnvironmentRef(operation, selected, returned string) error {
	if !selection.IsCanonicalEnvironmentRef(selected) {
		return fmt.Errorf("apikey %s selected environment_ref %q is non-canonical", operation, selected)
	}
	if !selection.IsCanonicalEnvironmentRef(returned) {
		return fmt.Errorf("apikey %s response environment_ref %q is missing or non-canonical", operation, returned)
	}
	if returned != selected {
		return fmt.Errorf(
			"apikey %s response environment_ref %q does not match selected environment %q",
			operation, returned, selected,
		)
	}
	return nil
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
