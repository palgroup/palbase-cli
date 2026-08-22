package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Signing in to the v2 cloud.
//
// The v1 flow is browser OAuth against a pre-registered `palbase-cli` client
// with five loopback redirect URIs seeded into palauth. The v2 control plane is
// a Palbase stack in its own right: it HAS an OIDC provider (discovery even
// advertises a device-authorization endpoint), but no client is registered, so
// that door is shut until one is.
//
// What is open, and proven end-to-end through the public gateway, is the
// stack's own `/auth/login`. Using it keeps a recorded decision intact — the
// management identity comes from the stack's OWN auth module, never a second
// identity system.
//
// Two things make this more than a POST:
//
//   - The anon apikey. `/auth/*` refuses a request without one. Its VALUE is
//     per-environment, so it is fetched from the control plane's bootstrap
//     endpoint rather than compiled in: a baked-in key means one build per
//     environment and a silent lockout the day it rotates.
//   - Proof of work. The first attempt comes back 403 with a challenge; the
//     same request is replayed with the solution. This is the platform's
//     anti-stuffing gate and it is not optional.

// Bootstrap is what a client may know before it has any identity.
type Bootstrap struct {
	AnonKey      string `json:"anonKey"`
	Issuer       string `json:"issuer"`
	TenantDomain string `json:"tenantDomain"`
}

type sessionResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	User         struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

// CloudClient talks to one v2 control plane.
type CloudClient struct {
	BaseURL string
	// HTTP is the INTERFACE, not *http.Client: the auth client injects its own
	// transport, and a CloudClient that quietly built its own would reach the
	// real network from inside a test whose whole point was that it must not.
	HTTP HTTPClient
}

// NewCloudClient builds a client for the control plane at base.
func NewCloudClient(base string) *CloudClient {
	return NewCloudClientWith(base, &http.Client{Timeout: 60 * time.Second})
}

// NewCloudClientWith builds a client that speaks through the given transport.
func NewCloudClientWith(base string, httpClient HTTPClient) *CloudClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &CloudClient{BaseURL: strings.TrimRight(base, "/"), HTTP: httpClient}
}

// Bootstrap fetches what the client needs before it can authenticate.
func (c *CloudClient) Bootstrap(ctx context.Context) (Bootstrap, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/cloud/config", nil)
	if err != nil {
		return Bootstrap{}, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("reach %s: %w", c.BaseURL, err)
	}
	defer func() { _ = res.Body.Close() }()

	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return Bootstrap{}, fmt.Errorf("bootstrap failed (HTTP %d): %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var b Bootstrap
	if err := json.Unmarshal(body, &b); err != nil {
		return Bootstrap{}, fmt.Errorf("bootstrap is not JSON: %w", err)
	}
	if b.AnonKey == "" {
		return Bootstrap{}, fmt.Errorf("bootstrap carried no anon key — sign-in cannot start")
	}
	return b, nil
}

// subjectOf reads the `sub` claim without verifying the signature — the token
// came straight from the server over TLS, and the claim is used only to label
// the stored credential.
func subjectOf(token, fallbackEmail string) (string, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fallbackEmail
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fallbackEmail
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fallbackEmail
	}
	email := claims.Email
	if email == "" {
		email = fallbackEmail
	}
	return claims.Sub, email
}

// describeFailure turns the server's error envelope into one readable line,
// falling back to the raw body when it is not the shape we expect.
func describeFailure(raw []byte) string {
	var env struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Error != "" {
		if env.Description != "" {
			return env.Error + " — " + env.Description
		}
		return env.Error
	}
	return strings.TrimSpace(string(raw))
}

// BeginAuthorization asks the plane to start an Authorization Code flow and
// returns the id of the request a person must now stand behind.
//
// This leg is machine-to-machine on purpose. The authorize endpoint takes the
// anon apikey in a header and a browser navigation cannot carry one, so the CLI
// makes the call itself and hands the person the id instead of the URL. The
// redirect is NOT followed: its Location is the whole answer.
func (c *CloudClient) BeginAuthorization(ctx context.Context, boot Bootstrap, redirectURI, challenge, state string) (string, error) {
	q := url.Values{
		"client_id":             {"palbase-cli"},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {authorizeScopes},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/oauth/authorize?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("apikey", boot.AnonKey)

	res, err := c.doNoRedirect(req)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", c.BaseURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))

	if res.StatusCode != http.StatusFound && res.StatusCode != http.StatusSeeOther {
		return "", fmt.Errorf("the plane would not start a sign-in (HTTP %d): %s",
			res.StatusCode, describeFailure(raw))
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		return "", fmt.Errorf("the plane redirected somewhere unparseable: %w", err)
	}
	id := loc.Query().Get("auth_request_id")
	if id == "" {
		return "", fmt.Errorf("the plane started a sign-in without naming it: %s", loc.String())
	}
	return id, nil
}

// ExchangeCode redeems the authorization code for a session.
//
// The verifier goes here and nowhere else: it is the proof that this process —
// not whatever else saw the code go by — is the one that asked.
func (c *CloudClient) ExchangeCode(ctx context.Context, boot Bootstrap, code, redirectURI, verifier string) (*Credentials, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {"palbase-cli"},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("apikey", boot.AnonKey)

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach %s: %w", c.BaseURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the code could not be exchanged (HTTP %d): %s",
			res.StatusCode, describeFailure(raw))
	}

	var s sessionResponse
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("the response is not a session: %w", err)
	}
	if s.AccessToken == "" {
		return nil, fmt.Errorf("the exchange succeeded but issued no token")
	}
	lifetime := time.Duration(s.ExpiresIn) * time.Second
	if s.ExpiresIn <= 0 {
		lifetime = time.Hour
	}
	id, mail := s.User.ID, s.User.Email
	if id == "" {
		id, mail = subjectOf(s.AccessToken, "")
	}
	return &Credentials{
		AccessToken:  s.AccessToken,
		RefreshToken: s.RefreshToken,
		ExpiresAt:    time.Now().Add(lifetime),
		User:         UserInfo{ID: id, Email: mail},
	}, nil
}

// doNoRedirect sends one request and hands back the redirect instead of
// following it. The injected transport may be a test's fake, so this cannot
// reach for http.Client's own CheckRedirect.
func (c *CloudClient) doNoRedirect(req *http.Request) (*http.Response, error) {
	if hc, ok := c.HTTP.(*http.Client); ok {
		clone := *hc
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return clone.Do(req)
	}
	return c.HTTP.Do(req)
}
