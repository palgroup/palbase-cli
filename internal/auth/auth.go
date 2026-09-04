package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Config holds the auth configuration for CLI.
type Config struct {
	AuthURL string
	// StudioURL is the PANEL's origin, and it is not AuthURL. The gateway
	// serves the API on api.* and the app on app.*, and a person signing in
	// goes to the app: the authorize endpoint is a machine surface behind an
	// apikey header, which a browser navigation cannot carry.
	StudioURL string
	ClientID  string
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

// Login signs in to the cloud, through the browser.
//
// The email-and-password prompt this replaces was a stopgap: the plane's OIDC
// provider was mounted but no `palbase-cli` client was registered, so the
// browser door was shut. It is open now — the client is seeded by the script
// that builds the installation — and a CLI that never handles a password is
// the better shape by a distance.
//
// A headless run does not come through here at all: CI sets
// PALBASE_ACCESS_TOKEN and every verb resolves it without a sign-in.
//
// This is a CLOUD sign-in. A stack you host yourself is a different question
// with a different answer: `palbase link <url> --token-stdin`.
func (c *Client) Login(ctx context.Context) error {
	return c.finish(ctx, false)
}

// SignUp opens a new account, through the same browser flow.
func (c *Client) SignUp(ctx context.Context) error {
	return c.finish(ctx, true)
}

func (c *Client) finish(ctx context.Context, create bool) error {
	creds, err := c.BrowserLogin(ctx, create)
	if err != nil {
		return err
	}
	if err := SaveCredentials(creds); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	who := creds.User.Email
	if who == "" {
		who = creds.User.ID
	}
	fmt.Fprintf(c.Output, "Signed in as %s\n", who)
	return nil
}

// plane is this CLI's view of the control plane, speaking through the client's
// own transport so tests reach their fake and never the network.
func (c *Client) plane() *CloudClient {
	return NewCloudClientWith(c.Cfg.AuthURL, c.HttpClient)
}

// RefreshTokens trades the stored refresh token for a fresh session.
//
// A SESSION IS RENEWED AT THE DOOR IT WAS OPENED AT. `palbase login` redeems
// its authorization code at the OIDC token endpoint (see ExchangeCode), so the
// refresh token it receives lives in the OIDC store. This used to ask palauth's
// SEPARATE, non-OIDC refresh store instead — the plane's own comment names them
// as two ("palauth's standalone refresh-token store is what the non-OIDC
// /auth/token/refresh flow uses") — and that store has never heard of a token
// the browser flow minted.
//
// Measured live 25.08.2026, three times in a row: every command thirty minutes
// after a sign-in answered `refresh tokens: 401 — invalid_token`, and the only
// way back was to open the browser again. Nothing said which of the two stores
// was being asked, which is why one endpoint answering for two flows is the
// shape to avoid, not the wording.
//
// The plane rotates: the response carries a NEW refresh token and the old one
// stops working. Keeping the previous one would mean the NEXT refresh presents
// a token the server has already retired — the same sign-out, half an hour
// later, for no reason the person can see.
func (c *Client) RefreshTokens(ctx context.Context, creds *Credentials) (*Credentials, error) {
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token stored — run: palbase login")
	}

	boot, err := c.plane().Bootstrap(ctx)
	if err != nil {
		return nil, err
	}

	// FORM ENCODING, because that is what the token endpoint speaks (RFC 6749
	// §6) and what ExchangeCode already sends. A JSON body here would be read
	// as an empty form and refused as a missing grant.
	clientID := c.Cfg.ClientID
	if clientID == "" {
		clientID = "palbase-cli"
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {clientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Cfg.AuthURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

	if err := SaveCredentials(creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// Logout ends the session on the server and forgets it here.
func (c *Client) Logout(ctx context.Context) error {
	creds, err := LoadCredentials()
	if err == nil && creds.AccessToken != "" {
		if err := c.revokeSession(ctx, creds.AccessToken); err != nil {
			// Local credentials are deleted regardless of what the server
			// says: a person who typed "logout" and got an error would
			// otherwise be left holding a live token they believe is gone.
			fmt.Fprintf(c.Output, "  ! the server did not confirm the sign-out: %v\n", err)
		}
	}

	if err := DeleteCredentials(); err != nil {
		return err
	}

	fmt.Fprintln(c.Output, "✓ Logged out")
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
	// HEADLESS KİMLİK DE BİR KİMLİKTİR.
	//
	// `ManagementToken` PALBASE_ACCESS_TOKEN'ı BİRİNCİ sırada okuyor ve
	// `status`, `link`, `push`, `project list` onunla çalışıyor. `whoami` ise
	// yalnız tarayıcı oturumuna bakıyordu ve jeton ayarlıyken bile
	// "refresh tokens: 401 — invalid_token" diyordu (ölçüldü 25.08.2026):
	// işi "ben kimim" olan komut, belgelenmiş kimliği görmezden geliyordu.
	//
	// Jeton varsa kimliği DÜZLEME sorulur — jetonun içini okuyup ondan bir ad
	// çıkarmak, imzasını doğrulamadığımız bir gövdeye inanmak olurdu.
	if tok := strings.TrimSpace(os.Getenv("PALBASE_ACCESS_TOKEN")); tok != "" {
		id, err := c.identityOf(ctx, tok)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Output, "User:   %s\n", id)
		fmt.Fprintf(c.Output, "Auth:   %s (PALBASE_ACCESS_TOKEN)\n", c.Cfg.AuthURL)
		return nil
	}

	creds, err := LoadCredentials()
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
	fmt.Fprintf(c.Output, "Auth:   %s\n", c.Cfg.AuthURL)
	return nil
}

// identityOf, bir jetonun kime ait olduğunu DÜZLEME sorar.
//
// `/v1/cloud/me` çağıranın kimliğini döndürür. Jetonun yükünü yerel olarak
// çözmek daha ucuz olurdu ama imzasını doğrulamadığımız bir gövdeye inanmak
// demekti — ve `whoami`'nin cevabı, insanın yıkıcı bir fiilden önce baktığı
// şeydir.
func (c *Client) identityOf(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Cfg.AuthURL+"/v1/cloud/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.HttpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", c.Cfg.AuthURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PALBASE_ACCESS_TOKEN kabul edilmedi (HTTP %d): %s",
			res.StatusCode, strings.TrimSpace(string(body)))
	}
	var me struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return "", fmt.Errorf("the identity response was not JSON: %w", err)
	}
	if me.Email != "" {
		return fmt.Sprintf("%s (%s)", me.Email, me.ID), nil
	}
	return me.ID, nil
}

// GetValidToken returns a valid access token, refreshing if needed.
func (c *Client) GetValidToken(ctx context.Context) (string, error) {
	creds, err := LoadCredentials()
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
	creds, err := LoadCredentials()
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
