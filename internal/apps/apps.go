// Package apps wires `palbase apps ...`: list / create / delete / config /
// enforce / attest.
//
// An app is registered on the PROJECT (the product boundary — `apps.project_id`
// is singular) and BOUND to each Environment: one (app × Environment) binding
// per active Environment, each with its own credentials and config artifact.
// So registration is Project-scoped and configuration is Environment-scoped.
//
// Routes (Management API v2):
//
//	GET    /api/v2/projects/{projectId}/apps                            list
//	POST   /api/v2/projects/{projectId}/apps                            create
//	DELETE /api/v2/apps/{appId}                                         delete
//	GET    /api/v2/apps/{appId}/bindings                                bindings
//	PATCH  /api/v2/apps/{appId}/bindings/{environmentRef}               attest
//	GET    /api/v2/apps/{appId}/config-artifact?environment_ref={ref}   config
//	PATCH  /api/v2/projects/{projectId}       {appsRequired}            enforce
//
// Studio authorizes server-side (member+ to read, Project admin+ to mutate); a
// below-role caller surfaces as a FORBIDDEN *transport.APIError here.
package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// REST is the Management-API transport subset the apps commands need.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers carries the lazily-built REST client + the shared selection resolver.
type Resolvers struct {
	REST      func() REST
	Selection func() *selection.Resolver
}

// Cmd returns the `palbase apps` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Manage the project's apps and their per-environment config",
		Long: `Register and configure the apps of the SELECTED project.

  palbase apps list                             List the project's apps.
  palbase apps create --platform ios --name "My App"
                                                Register a new app.
  palbase apps delete <appId>                   Delete an app.
  palbase apps config --app <appId>             Write a web app's config for the
                                                selected environment.
  palbase apps enforce                          Require registered apps.
  palbase apps attest --app <appId>             Require App Attest on the
                                                selected environment's binding.

iOS/macOS/Android config artifacts are fetched by their platform link commands.
All operations go through the Management API (role-gated server-side).`,
	}
	cmd.AddCommand(
		listCmd(r),
		createCmd(r),
		deleteCmd(r),
		configCmd(r),
		enforceCmd(r),
		attestCmd(r),
	)
	return cmd
}

// appRow mirrors the apps list / create row shape.
type appRow struct {
	ID          string  `json:"id"`
	Platform    string  `json:"platform"`
	DisplayName string  `json:"display_name"`
	Identifier  *string `json:"identifier"`
	DeletedAt   *string `json:"deleted_at"`
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List the project's apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, err := r.Selection().ProjectID(cmd.Context())
			if err != nil {
				return err
			}
			var rows []appRow
			if err := r.REST().Do(cmd.Context(), http.MethodGet,
				"/api/v2/projects/"+projectID+"/apps", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No apps.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
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

func createCmd(r Resolvers) *cobra.Command {
	var (
		platform   string
		name       string
		identifier string
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Args:  cobra.NoArgs,
		Short: "Register a new app under the project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !IsValidPlatform(platform) {
				return fmt.Errorf("--platform must be one of: ios, macos, tvos, watchos, android, web")
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			projectID, err := r.Selection().ProjectID(cmd.Context())
			if err != nil {
				return err
			}
			body := map[string]any{"platform": platform, "displayName": name}
			if identifier != "" {
				body["identifier"] = identifier
			}
			var created appRow
			if err := r.REST().Do(cmd.Context(), http.MethodPost,
				"/api/v2/projects/"+projectID+"/apps", body, &created); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), created)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ created %s app %s\n", created.Platform, created.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&platform, "platform", "", "App platform: ios | macos | tvos | watchos | android | web (required)")
	cmd.Flags().StringVar(&name, "name", "", "Display name (required)")
	cmd.Flags().StringVar(&identifier, "identifier", "", "Bundle id / applicationId")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	_ = cmd.MarkFlagRequired("platform")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func deleteCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "delete <appId>",
		Args:  cobra.ExactArgs(1),
		Short: "Delete an app (revokes its keys and bindings)",
		RunE: func(cmd *cobra.Command, args []string) error {
			appID := args[0]
			var out struct {
				ProjectID string `json:"projectId"`
			}
			if err := r.REST().Do(cmd.Context(), http.MethodDelete,
				"/api/v2/apps/"+appID, nil, &out); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), map[string]any{"appId": appID, "projectId": out.ProjectID})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ deleted app %s\n", appID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// enforceCmd wires `palbase apps enforce`: the PROJECT-wide App-Registration
// switch (`projects.apps_required`). Flipping it updates already-minted
// app-bound key blobs. --disable turns it off. Project admin+.
func enforceCmd(r Resolvers) *cobra.Command {
	var (
		disable bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "enforce",
		Args:  cobra.NoArgs,
		Short: "Require registered apps for the project",
		Long: "Toggle the project's App-Registration enforcement.\n" +
			"ON (default): runtime requests must use an app-bound key.\n" +
			"--disable turns it off. Flipping it backfills existing key blobs so the\n" +
			"change applies to already-minted keys. Project admin+, server-side.",
		RunE: func(cmd *cobra.Command, args []string) error {
			on := !disable
			projectID, err := r.Selection().ProjectID(cmd.Context())
			if err != nil {
				return err
			}
			var out struct {
				ID           string `json:"id"`
				AppsRequired bool   `json:"apps_required"`
			}
			if err := r.REST().Do(cmd.Context(), http.MethodPatch,
				"/api/v2/projects/"+projectID, map[string]any{"appsRequired": on}, &out); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), map[string]any{"projectId": projectID, "apps_required": out.AppsRequired})
			}
			state := "enabled"
			if !on {
				state = "disabled"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ app registration enforcement %s for project %s\n", state, projectID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&disable, "disable", false, "turn enforcement OFF (default: turn it ON)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// attestCmd wires `palbase apps attest --app <appId>`: the per-binding
// App-Attest switch. The binding is (app × ENVIRONMENT), so it targets the
// SELECTED environment (or --environment). --disable turns it off.
func attestCmd(r Resolvers) *cobra.Command {
	var (
		appID   string
		disable bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "attest",
		Args:  cobra.NoArgs,
		Short: "Require App Attest on the (app × environment) binding",
		Long: "Toggle the (app × environment) binding's App-Attest enforcement.\n" +
			"ON (default): user-backend calls must carry a valid App Attest assertion\n" +
			"from a real, unmodified app on Apple hardware. Auth/enrollment, Storage\n" +
			"uploads, and module routes remain exempt. --disable turns it off.\n" +
			"It acts on the SELECTED environment (override with --environment).",
		RunE: func(cmd *cobra.Command, args []string) error {
			on := !disable
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			var out struct {
				ProjectID string `json:"projectId"`
			}
			if err := r.REST().Do(cmd.Context(), http.MethodPatch,
				"/api/v2/apps/"+appID+"/bindings/"+sel.EnvironmentRef(),
				map[string]any{"attestEnforce": on}, &out); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), map[string]any{
					"appId": appID, "environment_ref": sel.EnvironmentRef(), "attest_enforce": on,
				})
			}
			state := "enabled"
			if !on {
				state = "disabled"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ App Attest %s for app %s on environment %s\n", state, appID, sel.EnvironmentRef())
			return nil
		},
	}
	cmd.Flags().StringVar(&appID, "app", "", "App id (required)")
	cmd.Flags().BoolVar(&disable, "disable", false, "turn App Attest OFF (default: turn it ON)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	_ = cmd.MarkFlagRequired("app")
	return cmd
}

// ConfigArtifact mirrors the per-(app × Environment) config the Management API
// returns. Exported so the platform link commands can write SDK input.
//
// It is keyed by `environment_ref`; the Environment is the deployment target.
type ConfigArtifact struct {
	AppID          string               `json:"app_id"`
	EnvironmentRef string               `json:"environment_ref"`
	APIKey         string               `json:"api_key"`
	BaseURL        string               `json:"base_url"`
	Kind           string               `json:"kind"`
	Platform       string               `json:"platform"`
	Integrity      *IntegrityConfig     `json:"integrity,omitempty"`
	Notifications  *NotificationsConfig `json:"notifications,omitempty"`
	// OAuth carries the provider-availability map fetched from palauth's public
	// `/auth/oauth/providers` endpoint. Nil means no enabled providers. The
	// Management API artifact does not return this field; the CLI merges it.
	OAuth *OAuthConfig `json:"oauth,omitempty"`
}

var publishableAPIKeyPattern = regexp.MustCompile(`^pb_([a-z0-9]+)_c[A-Za-z0-9]{20}$`)

// ValidateConfigArtifact binds a Management API config response to the exact
// app and Environment requested by the caller before its URL, key, or contents
// can drive another network request or a local file write.
func ValidateConfigArtifact(art ConfigArtifact, expectedAppID, expectedEnvironmentRef string) error {
	if expectedAppID == "" || expectedEnvironmentRef == "" {
		return fmt.Errorf("config artifact validation requires an app_id and environment_ref")
	}
	if art.AppID != expectedAppID {
		return fmt.Errorf("config artifact app_id %q does not match requested app %q", art.AppID, expectedAppID)
	}
	if art.EnvironmentRef != expectedEnvironmentRef {
		return fmt.Errorf(
			"config artifact environment_ref %q does not match selected environment %q",
			art.EnvironmentRef, expectedEnvironmentRef,
		)
	}
	keyParts := publishableAPIKeyPattern.FindStringSubmatch(art.APIKey)
	if len(keyParts) != 2 || keyParts[1] != expectedEnvironmentRef {
		return fmt.Errorf("config artifact api_key is not a canonical publishable key for environment %q", expectedEnvironmentRef)
	}

	baseURL, err := url.Parse(art.BaseURL)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.Hostname() == "" || baseURL.User != nil {
		return fmt.Errorf("config artifact base_url must be an HTTPS URL without user info")
	}
	host := strings.ToLower(baseURL.Hostname())
	if strings.HasSuffix(host, ".palbase.studio") {
		firstLabel, _, _ := strings.Cut(host, ".")
		if firstLabel != expectedEnvironmentRef {
			return fmt.Errorf("config artifact base_url host does not match environment %q", expectedEnvironmentRef)
		}
	}
	return nil
}

// IntegrityConfig carries the Palbase-managed Google Cloud project number used
// by Android Play Integrity. It contains no credential.
type IntegrityConfig struct {
	CloudProjectNumber int64 `json:"cloud_project_number"`
}

// NotificationsConfig contains the public Android client options provisioned by
// Palbase. Customers do not create, connect, or upload a Firebase project.
type NotificationsConfig struct {
	FCM FCMConfig `json:"fcm"`
}

type FCMConfig struct {
	ProjectID     string `json:"project_id"`
	ApplicationID string `json:"application_id"`
	APIKey        string `json:"api_key"`
	SenderID      string `json:"sender_id"`
	PackageName   string `json:"package_name"`
}

// OAuthConfig is the secret-free provider-availability map embedded in the
// per-Environment config artifact. The public provider endpoint returns no
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

// ConfigArtifactPath is the v2 route for one (app × Environment) artifact. The
// Environment is a QUERY parameter (`environment_ref`), not a path segment: the
// artifact hangs off the app, and the Environment selects which binding.
func ConfigArtifactPath(appID, environmentRef string) string {
	return "/api/v2/apps/" + appID + "/config-artifact?environment_ref=" + url.QueryEscape(environmentRef)
}

func configCmd(r Resolvers) *cobra.Command {
	var (
		appID   string
		outPath string
	)
	cmd := &cobra.Command{
		Use:   "config",
		Args:  cobra.NoArgs,
		Short: "Write the web app's config file for the selected environment",
		Long: "Fetch the config artifact for a web app in the SELECTED environment and\n" +
			"write palbase-config.json ({app_id, kind, base_url, api_key}).\n" +
			"Override the environment with the global --environment flag.\n" +
			"Native configs are written by `palbase ios|macos|android link`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			var art ConfigArtifact
			if err := r.REST().Do(cmd.Context(), http.MethodGet,
				ConfigArtifactPath(appID, sel.EnvironmentRef()), nil, &art); err != nil {
				return err
			}
			if err := ValidateConfigArtifact(art, appID, sel.EnvironmentRef()); err != nil {
				return err
			}
			if art.Platform != "web" {
				return fmt.Errorf("apps config writes web config only; app %s is %s — use the native link flow", appID, art.Platform)
			}
			out := outPath
			if out == "" {
				out = "palbase-config.json"
			}
			if err := WriteWebConfig(art, out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ wrote web config to %s (environment %s)\n", out, sel.EnvironmentRef())
			return nil
		},
	}
	cmd.Flags().StringVar(&appID, "app", "", "App id (required)")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "output path (default: palbase-config.json)")
	_ = cmd.MarkFlagRequired("app")
	return cmd
}

// WriteWebConfig writes the per-Environment web config file the SDK reads.
//
// It carries `environment_ref` and NO `branch`: the URL and the API key already
// identify the Environment, so a branch field would be a second, drift-prone
// name for the same thing.
func WriteWebConfig(art ConfigArtifact, path string) error {
	raw, err := json.MarshalIndent(map[string]string{
		"app_id":          art.AppID,
		"environment_ref": art.EnvironmentRef,
		"kind":            art.Kind,
		"base_url":        art.BaseURL,
		"api_key":         art.APIKey,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode web config: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// IsValidPlatform reports whether p is a registrable app platform.
func IsValidPlatform(p string) bool {
	switch p {
	case "ios", "macos", "tvos", "watchos", "android", "web":
		return true
	default:
		return false
	}
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
