package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
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
		OpenBrowser: openURL,
	}
}

// Login performs browser-based OAuth login with Authorization Code + PKCE.
func (c *Client) Login(ctx context.Context) error {
	pkce, err := GeneratePKCE()
	if err != nil {
		return fmt.Errorf("generate PKCE: %w", err)
	}

	state, err := GenerateState()
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}

	// OAuth 2.1 requires exact-match redirect URIs, so the server-side
	// client must have every loopback port we might bind to pre-registered.
	// Palauth's palbase-cli client is seeded with 54321..54325; try them
	// in order and fail the login if all five are in use (which realistically
	// means five concurrent login flows on one machine — rare).
	listener, port, err := bindLoopback(LoopbackCallbackPorts)
	if err != nil {
		return fmt.Errorf("start callback server: %w", err)
	}
	defer listener.Close()

	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)

	authURL := fmt.Sprintf("%s/oauth/authorize?%s",
		c.Cfg.AuthURL,
		url.Values{
			"response_type":         {"code"},
			"client_id":             {c.Cfg.ClientID},
			"redirect_uri":          {redirectURI},
			"code_challenge":        {pkce.Challenge},
			"code_challenge_method": {"S256"},
			"state":                 {state},
			"scope":                 {"openid profile email"},
		}.Encode(),
	)

	type callbackResult struct {
		code string
		err  error
	}
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if errParam := q.Get("error"); errParam != "" {
			desc := q.Get("error_description")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<html><body><h2>Login failed</h2><p>%s</p></body></html>", desc)
			resultCh <- callbackResult{err: fmt.Errorf("auth error: %s — %s", errParam, desc)}
			return
		}

		if q.Get("state") != state {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>Invalid state parameter</p></body></html>")
			resultCh <- callbackResult{err: fmt.Errorf("state mismatch")}
			return
		}

		code := q.Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<html><body><h2>Login failed</h2><p>No authorization code received</p></body></html>")
			resultCh <- callbackResult{err: fmt.Errorf("no authorization code")}
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><h2>Login successful!</h2><p>You can close this tab.</p></body></html>")
		resultCh <- callbackResult{code: code}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(listener)
	defer srv.Close()

	fmt.Fprintln(c.Output, "Opening browser for login...")
	if err := c.OpenBrowser(authURL); err != nil {
		fmt.Fprintf(c.Output, "Could not open browser. Please visit:\n%s\n", authURL)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			return result.err
		}
		creds, err := c.exchangeCode(ctx, result.code, redirectURI, pkce.Verifier)
		if err != nil {
			return err
		}
		if err := SaveCredentials(c.Cfg.Mode, creds); err != nil {
			return err
		}
		fmt.Fprintf(c.Output, "✓ Logged in as %s (mode=%s)\n", creds.User.Email, c.Cfg.Mode)
		return nil

	case <-time.After(120 * time.Second):
		return fmt.Errorf("login timed out — no response from browser within 120 seconds")

	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) exchangeCode(ctx context.Context, code, redirectURI, codeVerifier string) (*Credentials, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {c.Cfg.ClientID},
		"code_verifier": {codeVerifier},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Cfg.AuthURL+"/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	userInfo, err := c.fetchUserInfo(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, err
	}

	return &Credentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		User:         UserInfo{ID: userInfo.Sub, Email: userInfo.Email},
	}, nil
}

func (c *Client) fetchUserInfo(ctx context.Context, accessToken string) (*UserInfoResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Cfg.AuthURL+"/oauth/userinfo", nil)
	if err != nil {
		return nil, fmt.Errorf("create userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch userinfo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo failed (%d)", resp.StatusCode)
	}

	var info UserInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("parse userinfo: %w", err)
	}
	return &info, nil
}

// RefreshTokens refreshes an expired access token.
func (c *Client) RefreshTokens(ctx context.Context, creds *Credentials) (*Credentials, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.RefreshToken},
		"client_id":     {c.Cfg.ClientID},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Cfg.AuthURL+"/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh tokens: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("session expired — run: palbase login")
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	creds.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		creds.RefreshToken = tokenResp.RefreshToken
	}
	creds.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	if err := SaveCredentials(c.Cfg.Mode, creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// Logout deletes local credentials and revokes the token server-side.
func (c *Client) Logout(ctx context.Context) error {
	creds, err := LoadCredentials(c.Cfg.Mode)
	if err == nil && creds.RefreshToken != "" {
		c.revokeToken(ctx, creds.RefreshToken)
	}

	if err := DeleteCredentials(c.Cfg.Mode); err != nil {
		return err
	}

	fmt.Fprintf(c.Output, "✓ Logged out (mode=%s)\n", c.Cfg.Mode)
	return nil
}

func (c *Client) revokeToken(ctx context.Context, token string) {
	data := url.Values{
		"token":           {token},
		"token_type_hint": {"refresh_token"},
		"client_id":       {c.Cfg.ClientID},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Cfg.AuthURL+"/oauth/revoke", strings.NewReader(data.Encode()))
	if req != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.HttpClient.Do(req)
	}
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

	fmt.Fprintf(c.Output, "User:    %s (%s)\n", creds.User.Email, creds.User.ID)
	fmt.Fprintf(c.Output, "Mode:    %s\n", c.Cfg.Mode)
	fmt.Fprintf(c.Output, "Auth:    %s\n", c.Cfg.AuthURL)
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

func openURL(u string) error {
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

// bindLoopback tries each port in turn and returns the first listener it
// can open. When every port is busy — e.g. five parallel login flows on
// the same machine — the error surfaces so the user can retry.
func bindLoopback(ports []int) (net.Listener, int, error) {
	var lastErr error
	for _, p := range ports {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			return l, p, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("no free loopback port in %v: %w", ports, lastErr)
}
