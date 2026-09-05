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
	"fmt"
	"net/url"
	"strings"

	"github.com/palgroup/palbase-cli/internal/selection"
)

// REST is the Management-API transport subset the apps commands need.
// WHAT SURVIVED, AND WHY THE REST DID NOT (T011).
//
// This package carried a whole `palbase apps` command group — list, create,
// delete, enforce, attest, identifier, team-id, config — and NOTHING registered
// it: `grep -rn apps.Cmd` over the tree returns no call site. A command group
// nobody mounts is 380 lines the next reader has to understand before learning
// it never ran.
//
// What stayed is the part with live callers: the config artifact shape and its
// route, read by mobile_ios_capabilities.go and cloud_environments.go. The
// design said "delete internal/apps (617 lines)"; measuring it found six
// symbols in use, so the cut follows the callers rather than the line count.

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

// ValidateConfigArtifact binds a Management API config response to the exact
// app and Environment requested by the caller before its URL, key, or contents
// can drive another network request or a local file write.
func ValidateConfigArtifact(
	art ConfigArtifact,
	expectedAppID, expectedEnvironmentRef, publicHost string,
) error {
	publicHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(publicHost)), ".")
	if expectedAppID == "" || expectedEnvironmentRef == "" || publicHost == "" {
		return fmt.Errorf("config artifact validation requires an app_id, environment_ref, and public host")
	}
	if strings.ContainsAny(publicHost, "/:@?#") {
		return fmt.Errorf("config artifact validation requires a hostname-only public host")
	}
	if art.AppID != expectedAppID {
		return fmt.Errorf("config artifact app_id %q does not match requested app %q", art.AppID, expectedAppID)
	}
	if err := selection.ValidateRuntimeBinding(expectedEnvironmentRef, art.EnvironmentRef, art.APIKey); err != nil {
		return fmt.Errorf("config artifact environment_ref/api_key binding is invalid: %w", err)
	}

	baseURL, err := url.Parse(art.BaseURL)
	expectedHost := strings.ToLower(expectedEnvironmentRef) + "." + publicHost
	rootPath := baseURL != nil && (baseURL.EscapedPath() == "" || baseURL.EscapedPath() == "/")
	if err != nil || baseURL.Scheme != "https" || baseURL.Opaque != "" || baseURL.Host == "" ||
		baseURL.User != nil || baseURL.Port() != "" || strings.ToLower(baseURL.Hostname()) != expectedHost ||
		!rootPath || baseURL.RawQuery != "" || baseURL.ForceQuery ||
		baseURL.Fragment != "" || strings.Contains(art.BaseURL, "#") {
		return fmt.Errorf("config artifact base_url must be the root HTTPS URL for environment host %q", expectedHost)
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

// defaultWebConfigPath is the ONE directory @palbase/web's `palbe-gen` reads
// from by default (its own `--dir` default — see sdk/palbase-ts
// palbe/src/gen/generate.ts USAGE) and `palbase web link` already writes to
// (web_link.go's webArtifactsDir). `apps config` writing anywhere else by
// default — it used to write bare `./palbase-config.json` — meant the file it
// just wrote was invisible to palbe-gen unless the caller remembered to pass
// `--out Palbase/palbase-config.json` by hand.
