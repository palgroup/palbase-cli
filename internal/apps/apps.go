// Package apps wires the `palbase apps ...` subcommand group:
// list / create / delete / config. Apps are GROUP-scoped registrations
// (one per platform: ios/android/web) that bind to an env (project ref)
// and yield a per-(app × env) config artifact for the SDKs.
//
// Transport: Studio tRPC (`apps.*`), reached via the same user-JWT
// `internal/studio` client the `secret`/`db`/`notifications` commands use.
// We talk to tRPC directly because the apps feature (Task C4) is exposed
// ONLY as a tRPC router today — there are no `/api/v1/...` REST routes for
// apps, so the Management-API REST transport (used by `apikey`/`project`)
// cannot reach it. The tRPC procedures are already directly reachable:
//
//	apps.list           query    {groupId}                -> AppRow[]
//	apps.create         mutation {groupId,platform,displayName} -> AppRow
//	apps.delete         mutation {appId}                  -> {ok:true}
//	apps.configArtifact query    {appId,projectRef}       -> ConfigArtifact
//
// Studio runs membership/role authorization inside the tRPC service
// (member+ for list/config, admin+ for create/delete); a below-role or
// non-member caller surfaces as a FORBIDDEN error here.
package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// Studio is the tRPC transport subset the apps commands need.
// *studio.Client satisfies it; tests substitute a server-backed client.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
	Mutation(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries the lazily-built Studio client, populated by the root
// command's PersistentPreRunE before any subcommand fires (mirrors
// secret.Resolvers' pattern).
type Resolvers struct {
	Studio func() Studio
}

// Cmd returns the `palbase apps` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Manage a group's apps (ios/android/web) and their per-env config",
		Long: `Register and configure the apps under a group.

  palbase apps list <groupRef>                          List a group's apps.
  palbase apps create <groupRef> --platform ios --name "My App"
                                                        Register a new app.
  palbase apps delete <appId>                           Delete an app.
  palbase apps config --app <appId> --env <ref>         Fetch an (app × env)
                                                        config artifact.

All operations go through Studio (membership/role-gated server-side).`,
	}
	cmd.AddCommand(
		listCmd(r.Studio),
		createCmd(r.Studio),
		deleteCmd(r.Studio),
		configCmd(r.Studio),
	)
	return cmd
}

// appRow mirrors the apps.list / apps.create row shape.
type appRow struct {
	ID          string  `json:"id"`
	Platform    string  `json:"platform"`
	DisplayName string  `json:"display_name"`
	DeletedAt   *string `json:"deleted_at"`
}

func listCmd(studioFn func() Studio) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list <groupRef>",
		Args:  cobra.ExactArgs(1),
		Short: "List a group's apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			groupRef := args[0]
			var rows []appRow
			if err := studioFn().Query(cmd.Context(), "apps.list",
				map[string]any{"groupId": groupRef}, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No apps.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tPLATFORM\tNAME")
			for _, a := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", a.ID, a.Platform, a.DisplayName)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func createCmd(studioFn func() Studio) *cobra.Command {
	var (
		platform string
		name     string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "create <groupRef>",
		Args:  cobra.ExactArgs(1),
		Short: "Register a new app under a group",
		RunE: func(cmd *cobra.Command, args []string) error {
			groupRef := args[0]
			if !isValidPlatform(platform) {
				return fmt.Errorf("--platform must be one of: ios, android, web")
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			var created appRow
			if err := studioFn().Mutation(cmd.Context(), "apps.create", map[string]any{
				"groupId":     groupRef,
				"platform":    platform,
				"displayName": name,
			}, &created); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(created)
			}
			fmt.Fprintf(os.Stdout, "✓ created %s app %s\n", created.Platform, created.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&platform, "platform", "", "App platform: ios | android | web (required)")
	cmd.Flags().StringVar(&name, "name", "", "Display name (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	_ = cmd.MarkFlagRequired("platform")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func deleteCmd(studioFn func() Studio) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "delete <appId>",
		Args:  cobra.ExactArgs(1),
		Short: "Delete an app (starts the server-side DeleteAppWorkflow)",
		RunE: func(cmd *cobra.Command, args []string) error {
			appID := args[0]
			var out struct {
				OK bool `json:"ok"`
			}
			if err := studioFn().Mutation(cmd.Context(), "apps.delete",
				map[string]any{"appId": appID}, &out); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(map[string]any{"ok": out.OK, "appId": appID})
			}
			fmt.Fprintf(os.Stdout, "✓ deleted app %s\n", appID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// configArtifact mirrors the apps.configArtifact return shape — the
// per-(app × env) config the SDKs consume. This REPLACES the old
// PalbaseGenerated.json shape.
type configArtifact struct {
	AppID       string  `json:"app_id"`
	ProjectRef  string  `json:"project_ref"`
	EndpointRef string  `json:"endpoint_ref"`
	APIKey      string  `json:"api_key"`
	BaseURL     string  `json:"base_url"`
	EnvPreset   *string `json:"env_preset"`
	Platform    string  `json:"platform"`
	Identifier  string  `json:"identifier"`
}

func configCmd(studioFn func() Studio) *cobra.Command {
	var (
		appID   string
		env     string
		branch  string
		outPath string
		// jsonOut is accepted for surface compatibility; the artifact is
		// always emitted as JSON, so the flag is a documented no-op.
		jsonOut bool
	)
	_ = jsonOut
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Fetch the per-(app × env) config artifact",
		Long: "Fetch the config artifact for an app in a given env. Writes JSON\n" +
			"to stdout (default) or to a file with -o. The artifact carries\n" +
			"{app_id, project_ref, endpoint_ref, api_key, base_url, env_preset,\n" +
			"platform, identifier}.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// --env is the env's project ref; --branch optionally selects a
			// branch-specific env. The server resolves endpoint_ref from the
			// project ref, so we pass the (optionally branch-composed) ref.
			projectRef := env
			if branch != "" {
				projectRef = env + ":" + branch
			}
			var art configArtifact
			if err := studioFn().Query(cmd.Context(), "apps.configArtifact", map[string]any{
				"appId":      appID,
				"projectRef": projectRef,
			}, &art); err != nil {
				return err
			}
			raw, err := json.MarshalIndent(art, "", "  ")
			if err != nil {
				return fmt.Errorf("encode artifact: %w", err)
			}
			if outPath != "" {
				if err := os.WriteFile(outPath, append(raw, '\n'), 0o600); err != nil {
					return fmt.Errorf("write %s: %w", outPath, err)
				}
				fmt.Fprintf(os.Stdout, "✓ wrote config artifact to %s\n", outPath)
				return nil
			}
			fmt.Fprintln(os.Stdout, string(raw))
			return nil
		},
	}
	cmd.Flags().StringVar(&appID, "app", "", "App id (required)")
	cmd.Flags().StringVar(&env, "env", "", "Env project ref (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch slug (optional)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON (the artifact is always JSON)")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "write the artifact to a file instead of stdout")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("env")
	return cmd
}

func isValidPlatform(p string) bool {
	switch p {
	case "ios", "android", "web":
		return true
	default:
		return false
	}
}

func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
