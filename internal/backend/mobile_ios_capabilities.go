package backend

import (
	"context"
	"net/http"

	"github.com/palgroup/palbase-cli/internal/apps"
)

// Per-ENVIRONMENT app-config resolvers for the platform link commands.

// restDoer is the Management-API transport subset the link internals need
// (app bindings + config-artifact). *transport.Client satisfies it; tests
// inject a stub. Kept narrow so the backend package doesn't depend on
// internal/transport and stays testable.
type restDoer interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// configArtifactFetch fetches the per-(app × Environment) config artifact.
// environmentRef is the Environment's ref — the ONE selector. There is no
// branch: the ref already names the endpoint, the database and the keys.
type configArtifactFetch func(ctx context.Context, appID, environmentRef string) (apps.ConfigArtifact, error)

// AppBinding is the subset of an app-bindings row the link commands need to
// verify that an app is bound to the selected Environment.
type AppBinding struct {
	EnvironmentRef  string `json:"environment_ref"`
	EnvironmentName string `json:"environment_name"`
	Kind            string `json:"kind"`
	AttestEnforce   bool   `json:"attest_enforce"`
}

// studioConfigArtifactFetch is the production configArtifactFetch: the v2
// config-artifact route for the (app × Environment) pair — the SAME route
// `palbase apps config` uses.
//
// The artifact does not carry OAuth, so this wrapper also fetches palauth's
// public `/auth/oauth/providers` against the artifact's base_url and api_key.
// Best-effort: a blip leaves OAuth nil.
func studioConfigArtifactFetch(rest restDoer, publicHost string) configArtifactFetch {
	return func(ctx context.Context, appID, environmentRef string) (apps.ConfigArtifact, error) {
		var art apps.ConfigArtifact
		if err := rest.Do(ctx, http.MethodGet, apps.ConfigArtifactPath(appID, environmentRef), nil, &art); err != nil {
			return apps.ConfigArtifact{}, err
		}
		if err := apps.ValidateConfigArtifact(art, appID, environmentRef, publicHost); err != nil {
			return apps.ConfigArtifact{}, err
		}
		oauth, _ := fetchOAuthProviders(ctx, art.BaseURL, art.APIKey)
		art.OAuth = swiftOAuthToApps(oauth)
		return art, nil
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
