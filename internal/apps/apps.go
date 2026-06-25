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
	"strings"
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

// ConfigArtifact mirrors the apps.configArtifact return shape — the
// per-(app × env) config the SDKs consume. This REPLACES the old
// PalbaseGenerated.json shape. Exported so the mobile codegen path
// (Track A2) can reuse emitConfig with the same artifact.
type ConfigArtifact struct {
	AppID       string `json:"app_id"`
	ProjectRef  string `json:"project_ref"`
	EndpointRef string `json:"endpoint_ref"`
	APIKey      string `json:"api_key"`
	BaseURL     string `json:"base_url"`
	EnvPreset   string `json:"env_preset"`
	Platform    string `json:"platform"`
	Identifier  string `json:"identifier"`
}

func configCmd(studioFn func() Studio) *cobra.Command {
	var (
		appID    string
		env      string
		branch   string
		platform string
		outPath  string
	)
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Write the per-(app × env) config file the SDK reads",
		Long: "Fetch the config artifact for an app in a given env and write the\n" +
			"per-env config file the SDK loads to enforce config-match:\n" +
			"  --platform ios → Palbase-Info.plist (an Apple plist)\n" +
			"  --platform web → palbase-config.json\n" +
			"Both carry {app_id, identifier, env_preset, base_url, api_key}.\n" +
			"An unconfigured binding (empty identifier — the app has not declared\n" +
			"its bundle id / web origin yet) is REFUSED: no partial file is\n" +
			"written, because a config without an identifier cannot enforce\n" +
			"config-match.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if platform == "" {
				platform = derivePlatform(appID)
			}
			if !isValidPlatform(platform) {
				return fmt.Errorf("--platform must be one of: ios, web")
			}
			if platform == "android" {
				return fmt.Errorf("--platform android is not yet supported by apps config")
			}
			// --env is the env's project ref; --branch optionally selects a
			// branch-specific env. The server resolves endpoint_ref from the
			// project ref, so we pass the (optionally branch-composed) ref.
			projectRef := env
			if branch != "" {
				projectRef = env + ":" + branch
			}
			var art ConfigArtifact
			if err := studioFn().Query(cmd.Context(), "apps.configArtifact", map[string]any{
				"appId":      appID,
				"projectRef": projectRef,
			}, &art); err != nil {
				return err
			}
			out := outPath
			if out == "" {
				out = defaultConfigFilename(platform)
			}
			if err := emitConfig(art, platform, out); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "✓ wrote %s config to %s\n", platform, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&appID, "app", "", "App id (required)")
	cmd.Flags().StringVar(&env, "env", "", "Env project ref (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch slug (optional)")
	cmd.Flags().StringVar(&platform, "platform", "", "Target platform: ios | web (defaults from the app id prefix)")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "output path (defaults: Palbase-Info.plist | palbase-config.json)")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("env")
	return cmd
}

// defaultConfigFilename is the conventional output name per platform when -o
// is omitted.
func defaultConfigFilename(platform string) string {
	if platform == "ios" {
		return "Palbase-Info.plist"
	}
	return "palbase-config.json"
}

// derivePlatform best-effort infers the platform from common app-id prefixes
// so `--platform` can be omitted in the simple case. Empty when unknown; the
// caller then surfaces the validation error.
func derivePlatform(appID string) string {
	switch {
	case len(appID) >= 4 && appID[:4] == "ios_":
		return "ios"
	case len(appID) >= 4 && appID[:4] == "web_":
		return "web"
	default:
		return ""
	}
}

// emitConfig writes the per-env config file the SDK reads. platform "ios"
// writes a minimal Apple plist (Palbase-Info.plist); "web" writes JSON
// (palbase-config.json). Both carry exactly {app_id, identifier, env_preset,
// base_url, api_key}.
//
// REFUSES (returns an error, writes NOTHING) when the artifact has no
// identifier: an empty identifier means the binding is unconfigured (the app
// has not declared its bundle id / web origin), and a config file without an
// identifier cannot enforce config-match — writing it would be a footgun that
// mirrors the orchestrator's unconfigured-binding rule.
func emitConfig(art ConfigArtifact, platform, path string) error {
	if art.Identifier == "" {
		return fmt.Errorf("refusing to write %s: app %q has an unconfigured binding (no identifier) — declare the bundle id (ios) / origin (web) before emitting a config", path, art.AppID)
	}
	switch platform {
	case "web":
		return writeWebConfig(art, path)
	case "ios":
		return writeIOSPlist(art, path)
	default:
		return fmt.Errorf("emitConfig: unsupported platform %q", platform)
	}
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

// writeIOSPlist writes a minimal, valid Apple plist carrying the five
// config-match fields as a flat <key>/<string> dict.
func writeIOSPlist(art ConfigArtifact, path string) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	for _, kv := range []struct{ key, val string }{
		{"app_id", art.AppID},
		{"identifier", art.Identifier},
		{"env_preset", art.EnvPreset},
		{"base_url", art.BaseURL},
		{"api_key", art.APIKey},
	} {
		b.WriteString("\t<key>" + plistEscape(kv.key) + "</key>\n")
		b.WriteString("\t<string>" + plistEscape(kv.val) + "</string>\n")
	}
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// plistEscape escapes the XML metacharacters that can appear in plist text
// nodes so the emitted plist stays valid for arbitrary identifiers/URLs/keys.
func plistEscape(s string) string {
	return xmlReplacer.Replace(s)
}

var xmlReplacer = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

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
