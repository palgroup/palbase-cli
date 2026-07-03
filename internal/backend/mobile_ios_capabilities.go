package backend

import (
	"context"
	"net/http"
	"net/url"

	"github.com/palgroup/palbase-cli/internal/apps"
)

// Per-environment app-config resolvers for `palbase spec`.
//
// These resolve an app's per-environment config artifacts + bindings from the
// Management API so `palbase spec` can emit the bundle-id-keyed palbase-config.json
// the PalbaseCodegen SPM plugin turns into Palbase-Info.plist at build time. The
// iOS Swift codegen + Xcode-project wiring (and every Go-side plist emitter)
// that used to live here moved to that plugin.

// restDoer is the Management-API transport subset the mobile/ios internals need
// (apps bindings + config-artifact). *transport.Client satisfies it; tests
// inject a stub. Kept narrow so the backend package doesn't depend on
// internal/transport and stays testable.
type restDoer interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// configArtifactFetch fetches the per-(app × env) config artifact for one env,
// optionally targeting a specific BRANCH of that env (branchName ""→ the env's
// main branch). It abstracts the config-artifact REST route so the codegen emit
// is unit-testable without a live server (tests inject a stub that returns the
// per-env artifacts directly). envRef is the BARE project ref (the binding's
// project_ref); branchName selects which of that env's branches the base_url +
// key resolve to (`palbase ios use <branch>`).
type configArtifactFetch func(ctx context.Context, appID, envRef, branchName string) (apps.ConfigArtifact, error)

// AppBinding is the subset of an app-bindings row the codegen emit needs:
// the env's bare project ref, its registered bundle id (identifier — ” when the
// binding has not declared one yet), and its preset. Field tags match the
// snake_case bindings row shape (project_ref, env_preset, identifier).
type AppBinding struct {
	ProjectRef string `json:"project_ref"`
	EnvPreset  string `json:"env_preset"`
	Identifier string `json:"identifier"`
}

// bindingLister lists an app's (app × env) bindings. Abstracts the bindings
// REST route so the codegen emit is unit-testable without a live server
// (tests inject a stub returning a fixed binding list).
type bindingLister func(ctx context.Context, appID string) ([]AppBinding, error)

// studioConfigArtifactFetch is the production configArtifactFetch the codegen
// command supplies: it runs the config-artifact REST route for the (app × env)
// pair (the SAME route `palbase apps config` uses). The env's project ref is
// passed as the `env` query param; the server resolves the endpoint_ref +
// mints/looks up the env-main key. A non-empty branchName is appended as
// `&branch=...` (mirroring the "only send branch when non-empty" tRPC logic).
//
// The config-artifact route does NOT carry OAuth, so this wrapper ALSO fetches
// palauth's public `/auth/oauth/providers` (the SAME source the legacy
// PalbaseGenerated.json path uses) against the artifact's base_url + api_key and
// merges the result into ConfigArtifact.OAuth — making the per-env plist a true
// superset of the JSON's config role (closes the OAuth regression the config
// cutover would otherwise open). The fetch is best-effort: a blip leaves OAuth
// nil and the plist omits the block, exactly as the JSON path degrades.
func studioConfigArtifactFetch(rest restDoer) configArtifactFetch {
	return func(ctx context.Context, appID, envRef, branchName string) (apps.ConfigArtifact, error) {
		var art apps.ConfigArtifact
		path := "/api/v1/apps/" + appID + "/config-artifact?env=" + url.QueryEscape(envRef)
		if branchName != "" {
			path += "&branch=" + url.QueryEscape(branchName)
		}
		if err := rest.Do(ctx, http.MethodGet, path, nil, &art); err != nil {
			return apps.ConfigArtifact{}, err
		}
		oauth, _ := fetchOAuthProviders(ctx, art.BaseURL, art.APIKey)
		art.OAuth = swiftOAuthToApps(oauth)
		return art, nil
	}
}

// studioBindingLister is the production bindingLister the codegen command
// supplies: it runs the bindings REST route (the SAME route Studio's
// binding-matrix UI uses) for the app and returns every (app × env) binding's
// bare project_ref + registered identifier + preset. The codegen emit then
// fetches each binding's config artifact and keys the plist by identifier.
func studioBindingLister(rest restDoer) bindingLister {
	return func(ctx context.Context, appID string) ([]AppBinding, error) {
		var bindings []AppBinding
		if err := rest.Do(ctx, http.MethodGet, "/api/v1/apps/"+appID+"/bindings", nil, &bindings); err != nil {
			return nil, err
		}
		return bindings, nil
	}
}

// swiftOAuthToApps maps the backend package's swiftOAuthConfig (the shape
// fetchOAuthProviders returns, also embedded in PalbaseGenerated.json) onto
// the apps package's OAuthConfig (embedded in the per-env plist). The two
// shapes are field-identical by design so the iOS SDK decodes the plist's
// `oauth` block the same way it decodes the JSON's. Nil in → nil out.
func swiftOAuthToApps(in *swiftOAuthConfig) *apps.OAuthConfig {
	if in == nil {
		return nil
	}
	out := &apps.OAuthConfig{}
	if in.Apple != nil {
		out.Apple = &apps.OAuthApple{Enabled: in.Apple.Enabled}
	}
	if in.Google != nil {
		out.Google = &apps.OAuthGoogle{
			Enabled:     in.Google.Enabled,
			ClientID:    in.Google.ClientID,
			RedirectURI: in.Google.RedirectURI,
		}
	}
	if out.Apple == nil && out.Google == nil {
		return nil
	}
	return out
}
