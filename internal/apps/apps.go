// Package apps wires the `palbase apps ...` subcommand group:
// list / create / delete / config. Apps are GROUP-scoped registrations
// (one per platform: ios/android/web) that bind to an env (project ref)
// and yield a per-(app × env) config artifact for the SDKs.
//
// Transport: Management-API REST (`/api/v1/groups/...`, `/api/v1/apps/...`),
// the same DPoP-bound client the `apikey`/`project`/`groups` commands use.
// Routes:
//
//	GET    /api/v1/groups/{groupRef}/apps                        list
//	POST   /api/v1/groups/{groupRef}/apps  {platform,name}       create
//	DELETE /api/v1/apps/{appId}                                  delete
//	PUT    /api/v1/apps/{appId}/bindings/{projectRef} {identifier,teamId?,apns?}  bind
//	GET    /api/v1/apps/{appId}/config-artifact?env={ref}&branch={name}           config
//
// Studio runs membership/role authorization inside the service (member+ for
// list/config, admin+ for create/delete/bind); a below-role or non-member
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
  palbase apps enforce <groupRef>                       Require registered apps
                                                        (config-match on).
  palbase apps attest --app <appId> --env <ref>         Require App Attest on
                                                        the binding.

iOS config artifacts are fetched by 'palbase spec --app <appId>' and turned
into Palbase-Info.plist by the PalbaseCodegen SPM plugin at build time.

All operations go through the Management API (membership/role-gated server-side).`,
	}
	cmd.AddCommand(
		listCmd(r.REST),
		createCmd(r.REST),
		deleteCmd(r.REST),
		configCmd(r.REST),
		bindCmd(r.REST),
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
				return fmt.Errorf("--platform must be one of: ios, android, web")
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			var created appRow
			if err := rest().Do(cmd.Context(), http.MethodPost,
				"/api/v1/groups/"+groupRef+"/apps", map[string]any{
					"platform": platform,
					"name":     name,
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

// bindCmd wires `palbase apps bind`: it sets the (app × env) binding's
// identifier (bundle id / package name / web origin) plus optional iOS
// attestation material, by calling the PUT bindings route — the SAME UPDATE
// Studio's binding-matrix UI runs (admin+ gated server-side).
//
// --env is the env's BARE project ref (control-pg projects.ref), NOT a branch
// endpoint ref: the binding row is keyed by project_ref = projects.ref (the
// service resolves the env's branch endpoint_ref itself when it later mints
// keys). This is exactly the ref `apps config --env` and listBindings already
// use, so the binding round-trips with `palbase spec`.
func bindCmd(rest func() REST) *cobra.Command {
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
			"Runs through the Management API (admin+ on the app's group, server-side).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apns != "" && apns != "sandbox" && apns != "production" {
				return fmt.Errorf("--apns must be one of: sandbox, production")
			}
			// appId + projectRef ride in the PATH; the body is just the binding
			// attributes (identifier + optional iOS App Attest material).
			body := map[string]any{"identifier": identifier}
			if teamID != "" {
				body["teamId"] = teamID
			}
			if apns != "" {
				body["apns"] = apns
			}
			var out struct {
				OK bool `json:"ok"`
			}
			if err := rest().Do(cmd.Context(), http.MethodPut,
				"/api/v1/apps/"+appID+"/bindings/"+env, body, &out); err != nil {
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

// enforceCmd wires `palbase apps enforce <groupRef>`: the umbrella-wide
// App-Registration switch (project_groups.apps_required). When ON, the gateway
// 403s any request whose key doesn't config-match its registered bundle id /
// origin — a leaked key is useless from an unregistered app. Flipping it also
// fleet-backfills every already-minted app-bound key blob so the change takes
// effect on existing keys. --disable turns it off. admin+ on the group.
func enforceCmd(rest func() REST) *cobra.Command {
	var (
		disable bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "enforce <groupRef>",
		Args:  cobra.ExactArgs(1),
		Short: "Require registered apps for the group (config-match enforcement)",
		Long: "Toggle the group's App-Registration enforcement (config-match).\n" +
			"ON (default): the gateway rejects any key that doesn't match its\n" +
			"registered bundle id / web origin, so a leaked key can't be used from\n" +
			"an unregistered app. --disable turns it off. Flipping it fleet-backfills\n" +
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
			fmt.Fprintf(os.Stdout, "✓ config-match enforcement %s for group %s\n", state, groupID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&disable, "disable", false, "turn enforcement OFF (default: turn it ON)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// attestCmd wires `palbase apps attest --app <appId> --env <ref>`: the
// per-binding App-Attest switch (app_env_bindings.attest_enforce). When ON, the
// backend additionally requires a valid App Attest assertion on that env's
// requests (hardware attestation — layer 2, above config-match). --disable turns
// it off. admin+ on the app's group.
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
			"ON (default): requests to that env must carry a valid App Attest\n" +
			"assertion (a real, unmodified app on Apple hardware) — layer 2, above\n" +
			"config-match. --disable turns it off. --env is the env's BARE project\n" +
			"ref (the same ref `apps bind --env` takes).\n" +
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

// ConfigArtifact mirrors the config-artifact return shape — the
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
	// the config cutover would otherwise open. The config-artifact route
	// does NOT return this — the CLI fetches providers separately and
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

func configCmd(rest func() REST) *cobra.Command {
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
			// --env is the env's BARE project ref, passed as the `env` query
			// param. The server resolves the endpoint_ref from it (the env's main
			// branch); there is no branch-composed ref.
			var art ConfigArtifact
			if err := rest().Do(cmd.Context(), http.MethodGet,
				"/api/v1/apps/"+appID+"/config-artifact?env="+url.QueryEscape(env), nil, &art); err != nil {
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
