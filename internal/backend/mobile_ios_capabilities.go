package backend

import (
	"context"
	"net/http"
	"net/url"

	"github.com/palgroup/palbase-cli/internal/apps"
)

// Per-environment app-config resolvers for platform link commands.

// restDoer is the Management-API transport subset the mobile/ios internals need
// (app environments + config-artifact). *transport.Client satisfies it; tests
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

// AppBinding is the subset of an app-bindings row needed to verify that an app
// belongs to the selected environment.
type AppBinding struct {
	ProjectRef string `json:"project_ref"`
	EnvPreset  string `json:"env_preset"`
}

// bindingLister lists an app's (app × env) bindings. Abstracts the bindings
// REST route so artifact generation is testable without a live server.
type bindingLister func(ctx context.Context, appID string) ([]AppBinding, error)

// studioConfigArtifactFetch is the production configArtifactFetch the codegen
// command supplies: it runs the config-artifact REST route for the (app × env)
// pair (the SAME route `palbase apps config` uses). The env's project ref is
// passed as the `env` query param; the server resolves the endpoint_ref +
// mints/looks up the env-main key. A non-empty branchName is appended as
// `&branch=...` (mirroring the "only send branch when non-empty" tRPC logic).
//
// The config-artifact route does not carry OAuth, so this wrapper also fetches
// palauth's public `/auth/oauth/providers` against the artifact's base_url and
// api_key. The fetch is best-effort: a blip leaves OAuth nil.
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
// supplies: it runs the bindings REST route and returns the app's environments.
func studioBindingLister(rest restDoer) bindingLister {
	return func(ctx context.Context, appID string) ([]AppBinding, error) {
		var bindings []AppBinding
		if err := rest.Do(ctx, http.MethodGet, "/api/v1/apps/"+appID+"/bindings", nil, &bindings); err != nil {
			return nil, err
		}
		return bindings, nil
	}
}

// swiftOAuthToApps maps the provider response onto the config artifact shape.
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
