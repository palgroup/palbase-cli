package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

// powChallenge is the server's demand: find a nonce whose digest starts with
// `Difficulty` zero bits.
type powChallenge struct {
	ID         string `json:"id"`
	Prefix     string `json:"prefix"`
	Difficulty int    `json:"difficulty"`
}

type powRejection struct {
	Error     string       `json:"error"`
	Challenge powChallenge `json:"challenge"`
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

// SignIn exchanges an email and password for a session, solving the
// proof-of-work challenge the gate demands.
func (c *CloudClient) SignIn(ctx context.Context, boot Bootstrap, email, password string) (*Credentials, error) {
	return c.authenticate(ctx, "/auth/login", boot, email, password)
}

// SignUp creates an account and returns its first session.
func (c *CloudClient) SignUp(ctx context.Context, boot Bootstrap, email, password string) (*Credentials, error) {
	return c.authenticate(ctx, "/auth/signup", boot, email, password)
}

func (c *CloudClient) authenticate(ctx context.Context, path string, boot Bootstrap, email, password string) (*Credentials, error) {
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return nil, err
	}

	status, raw, err := c.post(ctx, path, body, boot.AnonKey, nil)
	if err != nil {
		return nil, err
	}

	// The gate answers the FIRST attempt with a challenge. Replaying the same
	// request with the solution is the protocol, not a retry: a fresh body
	// would earn a fresh challenge and the loop would never close.
	if status == http.StatusForbidden {
		var rej powRejection
		if err := json.Unmarshal(raw, &rej); err == nil && rej.Challenge.Prefix != "" {
			id, nonce := solveProofOfWork(rej.Challenge)
			status, raw, err = c.post(ctx, path, body, boot.AnonKey, map[string]string{
				"X-PoW-Challenge-ID": id,
				"X-PoW-Nonce":        strconv.FormatUint(nonce, 10),
			})
			if err != nil {
				return nil, err
			}
		}
	}

	if status != http.StatusOK && status != http.StatusCreated {
		return nil, fmt.Errorf("sign-in failed (HTTP %d): %s", status, describeFailure(raw))
	}

	var s sessionResponse
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("the response is not a session: %w", err)
	}
	if s.AccessToken == "" {
		return nil, fmt.Errorf("the server accepted the sign-in but issued no token")
	}

	// A missing expires_in must not read as "expired at the epoch": every later
	// call would refuse to use a token that is in fact valid.
	lifetime := time.Duration(s.ExpiresIn) * time.Second
	if s.ExpiresIn <= 0 {
		lifetime = time.Hour
	}
	id, mail := s.User.ID, s.User.Email
	if id == "" {
		id, mail = subjectOf(s.AccessToken, email)
	}
	return &Credentials{
		AccessToken:  s.AccessToken,
		RefreshToken: s.RefreshToken,
		ExpiresAt:    time.Now().Add(lifetime),
		User:         UserInfo{ID: id, Email: mail},
	}, nil
}

func (c *CloudClient) post(ctx context.Context, path string, body []byte, anonKey string, extra map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("reach %s: %w", c.BaseURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw, nil
}

// solveProofOfWork finds a nonce whose sha256(prefix+nonce) begins with
// `Difficulty` zero bits.
//
// The comparison reads the first eight bytes as a big-endian integer and shifts
// away everything below the required prefix. Comparing hex characters instead
// would only ever express difficulties that are multiples of four — and would
// silently accept a digest the server rejects for every other value.
func solveProofOfWork(ch powChallenge) (string, uint64) {
	if ch.Difficulty <= 0 || ch.Difficulty > 64 {
		return ch.ID, 0
	}
	shift := uint(64 - ch.Difficulty)
	for nonce := uint64(0); ; nonce++ {
		sum := sha256.Sum256([]byte(ch.Prefix + strconv.FormatUint(nonce, 10)))
		if binary.BigEndian.Uint64(sum[:8])>>shift == 0 {
			return ch.ID, nonce
		}
	}
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
