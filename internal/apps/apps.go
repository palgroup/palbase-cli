// Package apps wires the `palbase apps ...` subcommand group:
// list / create / delete / config. Apps are GROUP-scoped registrations; a
// group may own any number per platform, and each app
// receives a per-environment config artifact for the SDKs.
//
// Transport: Management-API REST (`/api/v1/groups/...`, `/api/v1/apps/...`),
// the same DPoP-bound client the `apikey`/`project`/`groups` commands use.
// Routes:
//
//	GET    /api/v1/groups/{groupRef}/apps                        list
//	POST   /api/v1/groups/{groupRef}/apps  {platform,name}       create
//	DELETE /api/v1/apps/{appId}                                  delete
//	GET    /api/v1/apps/{appId}/config-artifact?env={ref}&branch={name}           config
//
// Studio runs membership/role authorization inside the service (member+ for
// list/config, admin+ for create/delete); a below-role or non-member
// caller surfaces as a FORBIDDEN *transport.APIError here.
package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// REST is the Management-API transport subset the apps commands need.
// *transport.Client satisfies it; tests substitute a stub.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers carries the lazily-built REST client, populated by the root
// command's PersistentPreRunE before any subcommand fires (mirrors
// apikey.Resolvers' pattern).
type Resolvers struct {
	REST func() REST
}

// Cmd returns the `palbase apps` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Manage a group's apps and their per-environment config",
		Long: `Register and configure the apps under a group.

  palbase apps list <groupRef>                          List a group's apps.
  palbase apps create <groupRef> --platform ios --name "My App"
                                                        Register a new app.
  palbase apps delete <appId>                           Delete an app.
  palbase apps config --app <appId> --env <ref>         Write a web app's
                                                        per-env config file.
  palbase apps enforce <groupRef>                       Require registered apps
                                                        for the group.
  palbase apps attest --app <appId> --env <ref>         Require App Attest on
                                                        the binding.

iOS and macOS config artifacts are fetched by their platform link commands and
turned into Palbase-Info.plist by the PalbaseCodegen SPM plugin at build time.

All operations go through the Management API (membership/role-gated server-side).`,
	}
	cmd.AddCommand(
		listCmd(r.REST),
		createCmd(r.REST),
		deleteCmd(r.REST),
		configCmd(r.REST),
		enforceCmd(r.REST),
		attestCmd(r.REST),
	)
	return cmd
}

// appRow mirrors the apps list / create row shape.
type appRow struct {
	ID          string  `json:"id"`
	Platform    string  `json:"platform"`
	DisplayName string  `json:"display_name"`
	DeletedAt   *string `json:"deleted_at"`
}

func listCmd(rest func() REST) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list <groupRef>",
		Args:  cobra.ExactArgs(1),
		Short: "List a group's apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			groupRef := args[0]
			var rows []appRow
			if err := rest().Do(cmd.Context(), http.MethodGet,
				"/api/v1/groups/"+groupRef+"/apps", nil, &rows); err != nil {
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

func createCmd(rest func() REST) *cobra.Command {
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
				return fmt.Errorf("--platform must be one of: ios, macos, tvos, watchos, android, web")
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			body := map[string]any{
				"platform": platform,
				"name":     name,
			}
			var created appRow
			if err := rest().Do(cmd.Context(), http.MethodPost,
				"/api/v1/groups/"+groupRef+"/apps", body, &created); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(created)
			}
			fmt.Fprintf(os.Stdout, "✓ created %s app %s\n", created.Platform, created.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&platform, "platform", "", "App platform: ios | macos | tvos | watchos | android | web (required)")
	cmd.Flags().StringVar(&name, "name", "", "Display name (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	_ = cmd.MarkFlagRequired("platform")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func deleteCmd(rest func() REST) *cobra.Command {
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
			if err := rest().Do(cmd.Context(), http.MethodDelete,
				"/api/v1/apps/"+appID, nil, &out); err != nil {
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

// enforceCmd wires `palbase apps enforce <groupRef>`: the umbrella-wide
// App-Registration switch (project_groups.apps_required). Flipping it updates
// already-minted app-bound key blobs. --disable turns it off. admin+ on the
// group.
func enforceCmd(rest func() REST) *cobra.Command {
	var (
		disable bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "enforce <groupRef>",
		Args:  cobra.ExactArgs(1),
		Short: "Require registered apps for the group",
		Long: "Toggle the group's App-Registration enforcement.\n" +
			"ON (default): runtime requests must use an app-bound key.\n" +
			"--disable turns it off. Flipping it fleet-backfills\n" +
			"existing key blobs so the change applies to already-minted keys.\n" +
			"Runs through the Management API (admin+ on the group, server-side).",
		RunE: func(cmd *cobra.Command, args []string) error {
			groupID := args[0]
			on := !disable
			var out struct {
				OK bool `json:"ok"`
			}
			if err := rest().Do(cmd.Context(), http.MethodPatch,
				"/api/v1/groups/"+groupID, map[string]any{"apps_required": on}, &out); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(map[string]any{"ok": out.OK, "groupId": groupID, "apps_required": on})
			}
			state := "enabled"
			if !on {
				state = "disabled"
			}
			fmt.Fprintf(os.Stdout, "✓ app registration enforcement %s for group %s\n", state, groupID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&disable, "disable", false, "turn enforcement OFF (default: turn it ON)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// attestCmd wires `palbase apps attest --app <appId> --env <ref>`: the
// per-binding App-Attest switch (app_env_bindings.attest_enforce). When ON, the
// user backend additionally requires a valid App Attest assertion. The binding
// is environment-level and branch-invariant; non-backend routes stay exempt.
// --disable turns it off. admin+ on the app's group.
func attestCmd(rest func() REST) *cobra.Command {
	var (
		appID   string
		env     string
		disable bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "attest",
		Short: "Require App Attest on an (app × env) binding (hardware attestation)",
		Long: "Toggle the (app × env) binding's App-Attest enforcement.\n" +
			"ON (default): user-backend calls must carry a valid App Attest assertion\n" +
			"from a real, unmodified app on Apple hardware. The environment-level\n" +
			"setting applies to all branches. Auth/enrollment, Storage uploads, and\n" +
			"module routes remain exempt. --disable turns it off. --env is the env's\n" +
			"BARE project ref.\n" +
			"Runs through the Management API (admin+ on the app's group, server-side).",
		RunE: func(cmd *cobra.Command, args []string) error {
			on := !disable
			var out struct {
				OK bool `json:"ok"`
			}
			if err := rest().Do(cmd.Context(), http.MethodPatch,
				"/api/v1/apps/"+appID+"/bindings/"+env, map[string]any{"attest_enforce": on}, &out); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(map[string]any{"ok": out.OK, "appId": appID, "projectRef": env, "attest_enforce": on})
			}
			state := "enabled"
			if !on {
				state = "disabled"
			}
			fmt.Fprintf(os.Stdout, "✓ App Attest %s for app %s on env %s\n", state, appID, env)
			return nil
		},
	}
	cmd.Flags().StringVar(&appID, "app", "", "App id (required)")
	cmd.Flags().StringVar(&env, "env", "", "Env project ref (required)")
	cmd.Flags().BoolVar(&disable, "disable", false, "turn App Attest OFF (default: turn it ON)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("env")
	return cmd
}

// ConfigArtifact mirrors the per-(app × env) config returned by the
// Management API. Exported so the platform link commands can write SDK input.
type ConfigArtifact struct {
	AppID       string `json:"app_id"`
	ProjectRef  string `json:"project_ref"`
	EndpointRef string `json:"endpoint_ref"`
	APIKey      string `json:"api_key"`
	BaseURL     string `json:"base_url"`
	EnvPreset   string `json:"env_preset"`
	Platform    string `json:"platform"`
	// OAuth carries the provider-availability map fetched from palauth's
	// public `/auth/oauth/providers` endpoint. Nil means the environment has no
	// enabled providers. The Management API artifact does not return this field;
	// the CLI fetches and merges it.
	OAuth *OAuthConfig `json:"oauth,omitempty"`
}

// OAuthConfig is the secret-free provider-availability map embedded in the
// per-environment config artifact. The public provider endpoint returns no
// secrets.
type OAuthConfig struct {
	Apple  *OAuthApple  `json:"apple,omitempty"`
	Google *OAuthGoogle `json:"google,omitempty"`
}

type OAuthApple struct {
	Enabled bool `json:"enabled"`
}

type OAuthGoogle struct {
	Enabled     bool   `json:"enabled"`
	ClientID    string `json:"client_id"`
	RedirectURI string `json:"redirect_uri"`
}

func configCmd(rest func() REST) *cobra.Command {
	var (
		appID   string
		env     string
		outPath string
	)
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Write the per-(app × env) web config file the SDK reads",
		Long: "Fetch the config artifact for a web app in a given env and write\n" +
			"palbase-config.json ({app_id, env_preset, base_url, api_key}).\n" +
			"--env is the env's BARE project ref; the server resolves the\n" +
			"endpoint_ref from it (the env's main branch). Native configs are\n" +
			"written by `palbase ios link` and `palbase macos link`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// --env is the env's BARE project ref, passed as the `env` query
			// param. The server resolves the endpoint_ref from it (the env's main
			// branch); there is no branch-composed ref.
			var art ConfigArtifact
			if err := rest().Do(cmd.Context(), http.MethodGet,
				"/api/v1/apps/"+appID+"/config-artifact?env="+url.QueryEscape(env), nil, &art); err != nil {
				return err
			}
			if art.Platform != "web" {
				return fmt.Errorf("apps config writes web config only; app %s is %s — use the native codegen flow", appID, art.Platform)
			}
			out := outPath
			if out == "" {
				out = "palbase-config.json"
			}
			if err := emitConfig(art, out); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "✓ wrote web config to %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVar(&appID, "app", "", "App id (required)")
	cmd.Flags().StringVar(&env, "env", "", "Env project ref (required)")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "output path (default: palbase-config.json)")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("env")
	return cmd
}

// emitConfig writes the per-env web config file (palbase-config.json) the SDK
// reads — exactly {app_id, env_preset, base_url, api_key}.
func emitConfig(art ConfigArtifact, path string) error {
	return writeWebConfig(art, path)
}

// writeWebConfig marshals the canonical client config fields to JSON.
func writeWebConfig(art ConfigArtifact, path string) error {
	raw, err := json.MarshalIndent(map[string]string{
		"app_id":     art.AppID,
		"env_preset": art.EnvPreset,
		"base_url":   art.BaseURL,
		"api_key":    art.APIKey,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode web config: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func isValidPlatform(p string) bool {
	switch p {
	case "ios", "macos", "tvos", "watchos", "android", "web":
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
