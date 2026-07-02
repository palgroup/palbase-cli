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
  palbase apps config --app <appId> --env <ref>         Write a web app's
                                                        per-env config file.
  palbase apps bind --app <appId> --env <ref> --identifier <bundleId>
                                                        Set the (app × env)
                                                        binding's identifier.

iOS config artifacts are fetched by 'palbase spec --app <appId>' and turned
into Palbase-Info.plist by the PalbaseCodegen SPM plugin at build time.

All operations go through Studio (membership/role-gated server-side).`,
	}
	cmd.AddCommand(
		listCmd(r.Studio),
		createCmd(r.Studio),
		deleteCmd(r.Studio),
		configCmd(r.Studio),
		bindCmd(r.Studio),
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

// bindCmd wires `palbase apps bind`: it sets the (app × env) binding's
// identifier (bundle id / package name / web origin) plus optional iOS
// attestation material, by calling the apps.configureBinding tRPC mutation —
// the SAME UPDATE Studio's binding-matrix UI runs (admin+ gated server-side).
//
// --env is the env's BARE project ref (control-pg projects.ref), NOT a branch
// endpoint ref: the binding row is keyed by project_ref = projects.ref (the
// service resolves the env's branch endpoint_ref itself when it later mints
// keys). This is exactly the ref `apps config --env` and listBindings already
// use, so the binding round-trips with `palbase spec`.
func bindCmd(studioFn func() Studio) *cobra.Command {
	var (
		appID      string
		env        string
		identifier string
		teamID     string
		apns       string
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:   "bind",
		Short: "Set an (app × env) binding's identifier (bundle id / origin)",
		Long: "Configure the (app × env) binding the SDK config-match enforces.\n" +
			"Sets the env's --identifier (the app's bundle id / package name / web\n" +
			"origin) so the env's config artifact resolves and `palbase spec` emits it.\n" +
			"--env is the env's BARE project ref (the same ref `apps config --env`\n" +
			"takes), never a branch endpoint ref.\n" +
			"--team-id and --apns are optional iOS App Attest material.\n" +
			"Runs through Studio (admin+ on the app's group, server-side).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apns != "" && apns != "sandbox" && apns != "production" {
				return fmt.Errorf("--apns must be one of: sandbox, production")
			}
			input := map[string]any{
				"appId":      appID,
				"projectRef": env,
				"identifier": identifier,
			}
			if teamID != "" {
				input["teamId"] = teamID
			}
			if apns != "" {
				input["apnsEnvironment"] = apns
			}
			var out struct {
				OK bool `json:"ok"`
			}
			if err := studioFn().Mutation(cmd.Context(), "apps.configureBinding", input, &out); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(map[string]any{"ok": out.OK, "appId": appID, "projectRef": env, "identifier": identifier})
			}
			fmt.Fprintf(os.Stdout, "✓ bound app %s to env %s as %s\n", appID, env, identifier)
			return nil
		},
	}
	cmd.Flags().StringVar(&appID, "app", "", "App id (required)")
	cmd.Flags().StringVar(&env, "env", "", "Env project ref (required)")
	cmd.Flags().StringVar(&identifier, "identifier", "", "Bundle id / package name / web origin (required)")
	cmd.Flags().StringVar(&teamID, "team-id", "", "Apple Developer Team id (iOS App Attest, optional)")
	cmd.Flags().StringVar(&apns, "apns", "", "APNs environment: sandbox | production (optional)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("env")
	_ = cmd.MarkFlagRequired("identifier")
	return cmd
}

// ConfigArtifact mirrors the apps.configArtifact return shape — the
// per-(app × env) config the SDKs consume. This REPLACES the old
// PalbaseGenerated.json shape. Exported so `palbase spec` (backend
// package) can resolve the same artifact per binding.
type ConfigArtifact struct {
	AppID       string `json:"app_id"`
	ProjectRef  string `json:"project_ref"`
	EndpointRef string `json:"endpoint_ref"`
	APIKey      string `json:"api_key"`
	BaseURL     string `json:"base_url"`
	EnvPreset   string `json:"env_preset"`
	Platform    string `json:"platform"`
	Identifier  string `json:"identifier"`
	// OAuth carries the provider-availability map fetched from palauth's
	// public `/auth/oauth/providers` endpoint, mirroring the field the
	// legacy PalbaseGenerated.json path embeds. Nil means the env has no
	// enabled+configured providers; the per-env plist then omits the
	// `oauth` sub-dict and the iOS SDK's zero-arg
	// `pb.auth.signInWithGoogle()` overload throws. Making the plist a
	// true SUPERSET of the JSON's config role closes the OAuth regression
	// the config cutover would otherwise open. The tRPC apps.configArtifact
	// query does NOT return this — the CLI fetches providers separately and
	// merges it in (see backend.withOAuth), exactly as the JSON path does.
	OAuth *OAuthConfig `json:"oauth,omitempty"`
}

// OAuthConfig is the secret-free provider-availability map embedded in the
// per-env config artifact (plist). Shape mirrors PalbaseGenerated.json's
// `oauth` block so the iOS SDK decodes it identically. palauth's public
// `/auth/oauth/providers` endpoint never returns secrets, so there is
// nothing to filter here.
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

func configCmd(studioFn func() Studio) *cobra.Command {
	var (
		appID   string
		env     string
		outPath string
	)
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Write the per-(app × env) web config file the SDK reads",
		Long: "Fetch the config artifact for a WEB app in a given env and write the\n" +
			"per-env palbase-config.json the SDK loads to enforce config-match\n" +
			"({app_id, identifier, env_preset, base_url, api_key}).\n" +
			"--env is the env's BARE project ref; the server resolves the\n" +
			"endpoint_ref from it (the env's main branch).\n" +
			"An unconfigured binding (empty identifier — the app has not declared\n" +
			"its web origin yet) is REFUSED: no partial file is written, because a\n" +
			"config without an identifier cannot enforce config-match.\n" +
			"iOS config is NOT emitted here: the PalbaseCodegen SPM plugin\n" +
			"generates Palbase-Info.plist on every Xcode build from the\n" +
			"palbase-config.json fetched by 'palbase spec --app'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(appID) >= 4 && appID[:4] == "ios_" {
				return fmt.Errorf("apps config no longer emits iOS plists — run 'palbase spec --app %s' and let the PalbaseCodegen SPM plugin generate Palbase-Info.plist at build time", appID)
			}
			// --env is the env's BARE project ref. The server resolves the
			// endpoint_ref from the bare ref (the env's main branch); there is
			// no branch-composed ref.
			var art ConfigArtifact
			if err := studioFn().Query(cmd.Context(), "apps.configArtifact", map[string]any{
				"appId":      appID,
				"projectRef": env,
			}, &art); err != nil {
				return err
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
// reads — exactly {app_id, identifier, env_preset, base_url, api_key}.
//
// REFUSES (returns an error, writes NOTHING) when the artifact has no
// identifier: an empty identifier means the binding is unconfigured (the app
// has not declared its web origin), and a config file without an identifier
// cannot enforce config-match — writing it would be a footgun that mirrors
// the orchestrator's unconfigured-binding rule.
func emitConfig(art ConfigArtifact, path string) error {
	if art.Identifier == "" {
		return fmt.Errorf("refusing to write %s: app %q has an unconfigured binding (no identifier) — declare the web origin before emitting a config", path, art.AppID)
	}
	return writeWebConfig(art, path)
}

// writeWebConfig marshals the five config-match fields to JSON.
func writeWebConfig(art ConfigArtifact, path string) error {
	raw, err := json.MarshalIndent(map[string]string{
		"app_id":     art.AppID,
		"identifier": art.Identifier,
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
