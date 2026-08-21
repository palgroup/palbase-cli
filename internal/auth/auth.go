package auth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

// Config holds the auth configuration for CLI.
type Config struct {
	AuthURL  string
	ClientID string
	Mode     string // "prod" or "dev" — determines credentials file
}

// TokenResponse represents the OAuth token endpoint response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// UserInfoResponse represents the OIDC userinfo endpoint response.
type UserInfoResponse struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
}

// HTTPClient abstracts HTTP calls for testing.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client handles CLI authentication flows.
type Client struct {
	Cfg         Config
	HttpClient  HTTPClient
	Output      io.Writer
	OpenBrowser func(url string) error
}

// NewClient creates a new auth client.
func NewClient(cfg Config, output io.Writer) *Client {
	return &Client{
		Cfg:         cfg,
		HttpClient:  &http.Client{Timeout: 30 * time.Second},
		Output:      output,
		OpenBrowser: OpenURL,
	}
}

// Login signs in to the v2 control plane.
//
// The browser flow this replaced (Authorization Code + PKCE, DPoP-bound) spoke
// to a pre-registered `palbase-cli` OAuth client with five loopback redirect
// URIs seeded into the v1 platform. The v2 control plane is a Palbase stack in
// its own right: it HAS an OIDC provider — discovery even advertises a
// device-authorization endpoint — but no client is registered there, so that
// door is shut until one is.
//
// What is open, and proven end-to-end through the public gateway, is the
// stack's own /auth/login. Using it keeps a recorded decision intact: the
// management identity comes from the stack's OWN auth module, never a second
// identity system.
func (c *Client) Login(ctx context.Context) error {
	email, password, err := c.askForCredentials()
	if err != nil {
		return err
	}
	return c.signIn(ctx, email, password, false)
}

// SignUp creates an account on this control plane and signs in with it.
//
// It exists because there is no other door: the v2 cloud has no dashboard yet,
// so a first account can only be born here.
func (c *Client) SignUp(ctx context.Context) error {
	email, password, err := c.askForCredentials()
	if err != nil {
		return err
	}
	return c.signIn(ctx, email, password, true)
}

func (c *Client) signIn(ctx context.Context, email, password string, create bool) error {
	plane := c.plane()

	boot, err := plane.Bootstrap(ctx)
	if err != nil {
		return err
	}

	verb := plane.SignIn
	if create {
		verb = plane.SignUp
	}
	// The gate answers the first attempt with a proof-of-work challenge, so
	// this call can take a moment. Saying so beats a silent pause the person
	// reads as a hang.
	fmt.Fprintf(c.Output, "Signing in to %s…\n", c.Cfg.AuthURL)
	creds, err := verb(ctx, boot, email, password)
	if err != nil {
		return err
	}

	if err := SaveCredentials(c.Cfg.Mode, creds); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	who := creds.User.Email
	if who == "" {
		who = email
	}
	fmt.Fprintf(c.Output, "Signed in as %s\n", who)
	return nil
}

// plane is this CLI's view of the control plane, speaking through the client's
// own transport so tests reach their fake and never the network.
func (c *Client) plane() *CloudClient {
	return NewCloudClientWith(c.Cfg.AuthURL, c.HttpClient)
}

// askForCredentials reads an email and password from the terminal.
//
// PALBASE_EMAIL / PALBASE_PASSWORD skip the prompt for a headless run. Without
// them a non-TTY run fails loudly rather than hanging on a read that will never
// return.
func (c *Client) askForCredentials() (string, string, error) {
	email := strings.TrimSpace(os.Getenv("PALBASE_EMAIL"))
	password := os.Getenv("PALBASE_PASSWORD")
	if email != "" && password != "" {
		return email, password, nil
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", "", fmt.Errorf("stdin is not a terminal — set PALBASE_EMAIL and PALBASE_PASSWORD for a headless sign-in")
	}

	if email == "" {
		fmt.Fprint(c.Output, "Email: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return "", "", fmt.Errorf("read email: %w", err)
		}
		email = strings.TrimSpace(line)
	}
	if email == "" {
		return "", "", fmt.Errorf("no email given — aborting")
	}

	if password == "" {
		fmt.Fprint(c.Output, "Password: ")
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(c.Output)
		if err != nil {
			return "", "", fmt.Errorf("read password: %w", err)
		}
		password = strings.TrimRight(string(raw), "\r\n")
	}
	if password == "" {
		return "", "", fmt.Errorf("no password given — aborting")
	}
	return email, password, nil
}

// RefreshTokens trades the stored refresh token for a fresh session.
//
// The v2 control plane rotates: POST /auth/token/refresh returns a new access
// token AND a new refresh token, and the old one stops working. Keeping the
// previous refresh token on a response that carried a new one would mean the
// NEXT refresh presents a token the server has already retired — a sign-out
// that arrives half an hour later for no reason the person can see.
func (c *Client) RefreshTokens(ctx context.Context, creds *Credentials) (*Credentials, error) {
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token stored — run: palbase login")
	}

	boot, err := c.plane().Bootstrap(ctx)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]string{"refresh_token": creds.RefreshToken})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Cfg.AuthURL+"/auth/token/refresh", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", boot.AnonKey)

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh tokens: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		// The server's own reason travels: "the session expired" and "the
		// gateway refused" call for different actions, and collapsing them
		// into one message costs the person the difference.
		return nil, fmt.Errorf("refresh tokens: %d — %s", resp.StatusCode, describeFailure(body))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("the refresh succeeded but returned no token")
	}

	creds.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		creds.RefreshToken = tokenResp.RefreshToken
	}
	lifetime := time.Duration(tokenResp.ExpiresIn) * time.Second
	if tokenResp.ExpiresIn <= 0 {
		lifetime = time.Hour
	}
	creds.ExpiresAt = time.Now().Add(lifetime)

	if err := SaveCredentials(c.Cfg.Mode, creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// Logout ends the session on the server and forgets it here.
func (c *Client) Logout(ctx context.Context) error {
	creds, err := LoadCredentials(c.Cfg.Mode)
	if err == nil && creds.AccessToken != "" {
		if err := c.revokeSession(ctx, creds.AccessToken); err != nil {
			// Local credentials are deleted regardless of what the server
			// says: a person who typed "logout" and got an error would
			// otherwise be left holding a live token they believe is gone.
			fmt.Fprintf(c.Output, "  ! the server did not confirm the sign-out: %v\n", err)
		}
	}

	if err := DeleteCredentials(c.Cfg.Mode); err != nil {
		return err
	}

	fmt.Fprintf(c.Output, "✓ Logged out (mode=%s)\n", c.Cfg.Mode)
	return nil
}

// revokeSession asks the control plane to end this session server-side.
func (c *Client) revokeSession(ctx context.Context, accessToken string) (retErr error) {
	boot, err := c.plane().Bootstrap(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Cfg.AuthURL+"/auth/logout", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", boot.AnonKey)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return fmt.Errorf("reach the control plane: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%d — %s", resp.StatusCode, describeFailure(body))
	}
	return nil
}

// Whoami prints the current logged-in user info.
func (c *Client) Whoami(ctx context.Context) error {
	creds, err := LoadCredentials(c.Cfg.Mode)
	if err != nil {
		return err
	}

	if creds.IsExpired() {
		creds, err = c.RefreshTokens(ctx, creds)
		if err != nil {
			return err
		}
	}

	// palauth's /oauth/userinfo currently returns only `sub` (no email claim
	// on the CLI token), so creds.User.Email is usually empty. Print whatever
	// we have without a dangling "  ()".
	if creds.User.Email != "" {
		fmt.Fprintf(c.Output, "User:   %s (%s)\n", creds.User.Email, creds.User.ID)
	} else {
		fmt.Fprintf(c.Output, "User:   %s\n", creds.User.ID)
	}
	fmt.Fprintf(c.Output, "Mode:   %s\n", c.Cfg.Mode)
	fmt.Fprintf(c.Output, "Auth:   %s\n", c.Cfg.AuthURL)
	return nil
}

// GetValidToken returns a valid access token, refreshing if needed.
func (c *Client) GetValidToken(ctx context.Context) (string, error) {
	creds, err := LoadCredentials(c.Cfg.Mode)
	if err != nil {
		return "", err
	}

	if creds.IsExpired() {
		creds, err = c.RefreshTokens(ctx, creds)
		if err != nil {
			return "", err
		}
	}
	return creds.AccessToken, nil
}

// GetFreshToken returns an access token guaranteed to have at least
// minRemaining of life left, refreshing AHEAD of expiry when it doesn't.
//
// GetValidToken only refreshes once the token is ALREADY expired
// (creds.IsExpired()): a caller that polls on a fixed tick can therefore write
// out a token with only a few seconds left, which then expires before the next
// tick fires — leaving a stale-token window (e.g. a long-running command's
// silently returns empty until the next refresh actually triggers).
// GetFreshToken closes that window: it refreshes when the token has LESS THAN
// minRemaining left, so the returned token always has a comfortable margin.
//
// This is deliberately a SEPARATE method from GetValidToken (and does NOT touch
// Credentials.IsExpired, which every other caller relies on) so the
// refresh-ahead policy is opt-in per call site.
func (c *Client) GetFreshToken(ctx context.Context, minRemaining time.Duration) (string, error) {
	creds, err := LoadCredentials(c.Cfg.Mode)
	if err != nil {
		return "", err
	}

	if creds.ExpiresSoon(minRemaining) {
		creds, err = c.RefreshTokens(ctx, creds)
		if err != nil {
			return "", err
		}
	}
	return creds.AccessToken, nil
}

// OpenURL opens u in the platform browser (exported for `palbase open`).
func OpenURL(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "linux":
		return exec.Command("xdg-open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return fmt.Errorf("unsupported platform")
	}
}

// LoopbackCallbackPorts are the ports the CLI tries, in order, when it
// needs a loopback HTTP server to receive the OAuth redirect. OAuth 2.1
// (RFC 8252 §7.3 as implemented here) requires exact-match redirect URIs
// on the server, so every port listed here must also appear in palauth's
// palbase-cli client redirect_uris config.
var LoopbackCallbackPorts = []int{54321, 54322, 54323, 54324, 54325}
