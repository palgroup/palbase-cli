package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- PKCE Tests ---

func TestGeneratePKCE(t *testing.T) {
	pkce, err := GeneratePKCE()
	require.NoError(t, err)

	// Verifier: 43 chars (32 bytes base64url encoded)
	assert.Len(t, pkce.Verifier, 43)

	// Challenge must be S256 of verifier
	h := sha256.Sum256([]byte(pkce.Verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])
	assert.Equal(t, expected, pkce.Challenge)
}

func TestGeneratePKCE_Uniqueness(t *testing.T) {
	p1, err := GeneratePKCE()
	require.NoError(t, err)
	p2, err := GeneratePKCE()
	require.NoError(t, err)

	assert.NotEqual(t, p1.Verifier, p2.Verifier)
	assert.NotEqual(t, p1.Challenge, p2.Challenge)
}

func TestGenerateState(t *testing.T) {
	s1, err := GenerateState()
	require.NoError(t, err)
	assert.NotEmpty(t, s1)

	s2, err := GenerateState()
	require.NoError(t, err)
	assert.NotEqual(t, s1, s2)
}

// --- Credentials Tests ---

func TestCredentials_IsExpired(t *testing.T) {
	expired := &Credentials{ExpiresAt: time.Now().Add(-1 * time.Minute)}
	assert.True(t, expired.IsExpired())

	valid := &Credentials{ExpiresAt: time.Now().Add(10 * time.Minute)}
	assert.False(t, valid.IsExpired())
}

func TestSaveAndLoadCredentials(t *testing.T) {
	// Use temp dir as home
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	creds := &Credentials{
		AccessToken:  "test_access_token",
		RefreshToken: "test_refresh_token",
		ExpiresAt:    time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
		User:         UserInfo{ID: "usr_123", Email: "test@example.com"},
	}

	err := SaveCredentials("prod", creds)
	require.NoError(t, err)

	// Check file permissions
	path := filepath.Join(tmpDir, ".palbase", "credentials-prod.json")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	loaded, err := LoadCredentials("prod")
	require.NoError(t, err)

	assert.Equal(t, creds.AccessToken, loaded.AccessToken)
	assert.Equal(t, creds.RefreshToken, loaded.RefreshToken)
	assert.Equal(t, creds.User.Email, loaded.User.Email)
}

func TestLoadCredentials_NotLoggedIn(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	_, err := LoadCredentials("prod")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func TestDeleteCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Save first
	err := SaveCredentials("prod", &Credentials{AccessToken: "x"})
	require.NoError(t, err)

	// Delete
	err = DeleteCredentials("prod")
	require.NoError(t, err)

	// Load should fail
	_, err = LoadCredentials("prod")
	require.Error(t, err)
}

// --- Login Flow Tests ---

func TestLogin_FullFlow(t *testing.T) {
	// Mock auth server
	var authServer *httptest.Server
	authServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			r.ParseForm()
			assert.Equal(t, "authorization_code", r.FormValue("grant_type"))
			assert.Equal(t, "palbase-cli", r.FormValue("client_id"))
			assert.NotEmpty(t, r.FormValue("code_verifier"))
			assert.Equal(t, "test_code", r.FormValue("code"))
			// CLI-11: exchange must carry a DPoP proof so palauth mints a
			// cnf.jkt-bound access token (matches the authorize-time
			// dpop_jkt). Without this the token would silently come back
			// unbound and every later DPoP-only API call would 401.
			assert.NotEmpty(t, r.Header.Get("DPoP"), "exchange must carry DPoP proof")

			json.NewEncoder(w).Encode(TokenResponse{
				AccessToken:  "access_123",
				RefreshToken: "refresh_456",
				ExpiresIn:    900,
				TokenType:    "Bearer",
			})

		case "/oauth/userinfo":
			assert.Equal(t, "DPoP access_123", r.Header.Get("Authorization"))
			proof := r.Header.Get("DPoP")
			require.NotEmpty(t, proof)
			claims := decodeJWTPayload(t, proof)
			assert.Equal(t, http.MethodGet, claims["htm"])
			assert.Equal(t, authServer.URL+"/oauth/userinfo", claims["htu"])
			assert.Equal(t, accessTokenHash("access_123"), claims["ath"])
			json.NewEncoder(w).Encode(UserInfoResponse{
				Sub:   "usr_abc",
				Email: "test@example.com",
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	// Login provisions a keyring DPoP key — isolate to the 0600 file
	// fallback so the test never touches the host OS keychain (which
	// would block on a Keychain UI prompt under -race).
	t.Setenv("PALBASE_NO_KEYRING", "1")

	var output bytes.Buffer
	client := NewClient(Config{
		AuthURL:  authServer.URL,
		ClientID: "palbase-cli",
	}, &output)

	// Override openBrowser to simulate browser callback. Capture the authorize
	// URL so we can assert the DPoP jkt binding param was attached.
	var capturedAuthURL string
	client.OpenBrowser = func(authURL string) error {
		capturedAuthURL = authURL
		// Parse the auth URL to extract state and redirect_uri
		u, err := parseAuthURL(authURL)
		if err != nil {
			return err
		}

		state := u.Query().Get("state")
		redirectURI := u.Query().Get("redirect_uri")

		// Simulate browser callback
		callbackURL := fmt.Sprintf("%s?code=test_code&state=%s", redirectURI, state)
		resp, err := http.Get(callbackURL)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	err := client.Login(context.Background())
	require.NoError(t, err)

	assert.Contains(t, output.String(), "✓ Logged in as test@example.com")

	// Verify credentials were saved
	creds, err := LoadCredentials("prod")
	require.NoError(t, err)
	assert.Equal(t, "access_123", creds.AccessToken)
	assert.Equal(t, "refresh_456", creds.RefreshToken)
	assert.Equal(t, "test@example.com", creds.User.Email)

	// Login must provision a keyring DPoP key (S5.2) so subsequent
	// Management-API calls can sign a proof.
	key, err := LoadDPoPKey("prod")
	require.NoError(t, err)
	assert.NotEmpty(t, key.Thumbprint())
	assert.Contains(t, output.String(), "DPoP key ready")

	// CLI-8: the authorize request must carry dpop_jkt = the key's
	// thumbprint (RFC 9449 §10) so palauth binds the issued token to it.
	// Without this the token is unbound and the Management API rejects the
	// DPoP-proofed requests the REST client makes.
	au, err := parseAuthURL(capturedAuthURL)
	require.NoError(t, err)
	gotJKT := au.Query().Get("dpop_jkt")
	assert.NotEmpty(t, gotJKT, "authorize URL must carry dpop_jkt")
	assert.Equal(t, key.Thumbprint(), gotJKT, "dpop_jkt must equal the provisioned key's thumbprint")
	// base64url SHA-256: 43 chars, no padding, no +/ chars.
	assert.Len(t, gotJKT, 43, "jkt should be a 43-char base64url SHA-256 thumbprint")
	assert.NotContains(t, gotJKT, "=", "base64url thumbprint must be unpadded")
	assert.NotContains(t, gotJKT, "+", "base64url uses - not +")
	assert.NotContains(t, gotJKT, "/", "base64url uses _ not /")
}

func decodeJWTPayload(t *testing.T, raw string) map[string]any {
	t.Helper()
	parts := strings.Split(raw, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}

func TestLogin_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	// Login now provisions the DPoP key up front (before authorize); pin the
	// file fallback so the test never blocks on a real keychain prompt.
	t.Setenv("PALBASE_NO_KEYRING", "1")

	var output bytes.Buffer
	client := NewClient(Config{
		AuthURL:  "http://localhost:1",
		ClientID: "palbase-cli",
	}, &output)

	// Don't open browser — let it timeout
	client.OpenBrowser = func(u string) error { return nil }

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := client.Login(ctx)
	require.Error(t, err)
}

func TestBindLoopbackSkipsPortOccupiedOnIPv6(t *testing.T) {
	ips, err := net.LookupIP("localhost")
	require.NoError(t, err)
	hasIPv6 := false
	for _, ip := range ips {
		if ip.Equal(net.IPv6loopback) {
			hasIPv6 = true
			break
		}
	}
	if !hasIPv6 {
		t.Skip("localhost has no IPv6 loopback address")
	}

	occupied, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	// The IPv4 side is deliberately free. A callback server that binds only
	// 127.0.0.1 would accept this port even though a browser may resolve
	// localhost to the occupied IPv6 side.
	probe, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("matching IPv4 loopback port is unavailable: %v", err)
	}
	_ = probe.Close()

	listeners, _, err := bindLoopback([]int{port})
	for _, listener := range listeners {
		_ = listener.Close()
	}
	require.Error(t, err)
}

func TestLogin_StateMismatch(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	// Login now provisions the DPoP key up front (before authorize); pin the
	// file fallback so the test never blocks on a real keychain prompt.
	t.Setenv("PALBASE_NO_KEYRING", "1")

	var output bytes.Buffer
	client := NewClient(Config{
		AuthURL:  authServer.URL,
		ClientID: "palbase-cli",
	}, &output)

	client.OpenBrowser = func(authURL string) error {
		u, _ := parseAuthURL(authURL)
		redirectURI := u.Query().Get("redirect_uri")

		// Send wrong state
		callbackURL := fmt.Sprintf("%s?code=test_code&state=wrong_state", redirectURI)
		resp, err := http.Get(callbackURL)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	err := client.Login(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state mismatch")
}

// --- Refresh Token Tests ---

func TestRefreshTokens(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		assert.Equal(t, "refresh_token", r.FormValue("grant_type"))
		assert.Equal(t, "old_refresh", r.FormValue("refresh_token"))
		// CLI-11: refresh must carry a DPoP proof so the new access token
		// stays bound to the keyring key (palauth rebinds cnf.jkt).
		assert.NotEmpty(t, r.Header.Get("DPoP"), "refresh must carry DPoP proof")

		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "new_access",
			RefreshToken: "new_refresh",
			ExpiresIn:    900,
		})
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PALBASE_NO_KEYRING", "1") // force file fallback under HOME

	// Refresh needs a keyring key to sign its DPoP proof.
	_, err := EnsureDPoPKey("prod")
	require.NoError(t, err)

	var output bytes.Buffer
	client := NewClient(Config{
		AuthURL:  authServer.URL,
		ClientID: "palbase-cli",
	}, &output)

	oldCreds := &Credentials{
		AccessToken:  "expired_access",
		RefreshToken: "old_refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		User:         UserInfo{ID: "usr_1", Email: "test@example.com"},
	}

	newCreds, err := client.RefreshTokens(context.Background(), oldCreds)
	require.NoError(t, err)
	assert.Equal(t, "new_access", newCreds.AccessToken)
	assert.Equal(t, "new_refresh", newCreds.RefreshToken)
}

// --- Logout Tests ---

func TestLogout(t *testing.T) {
	type revocationPayload struct {
		Token         string `json:"token"`
		TokenTypeHint string `json:"token_type_hint"`
	}
	type revocationRequest struct {
		ContentType string
		Payload     revocationPayload
		DecodeErr   error
	}
	revocations := make(chan revocationRequest, 1)
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/revoke" {
			var payload revocationPayload
			decodeErr := json.NewDecoder(r.Body).Decode(&payload)
			revocations <- revocationRequest{
				ContentType: r.Header.Get("Content-Type"),
				Payload:     payload,
				DecodeErr:   decodeErr,
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	// Logout purges the keyring DPoP key — isolate to the file fallback
	// so the test never touches the host OS keychain.
	t.Setenv("PALBASE_NO_KEYRING", "1")

	// Save credentials first
	require.NoError(t, SaveCredentials("prod", &Credentials{
		AccessToken:  "access",
		RefreshToken: "refresh",
		User:         UserInfo{Email: "test@example.com"},
	}))

	var output bytes.Buffer
	client := NewClient(Config{
		AuthURL:  authServer.URL,
		ClientID: "palbase-cli",
	}, &output)

	err := client.Logout(context.Background())
	require.NoError(t, err)
	revocation := <-revocations
	require.NoError(t, revocation.DecodeErr)
	assert.Equal(t, "application/json", revocation.ContentType)
	assert.Equal(t, "refresh", revocation.Payload.Token)
	assert.Equal(t, "refresh_token", revocation.Payload.TokenTypeHint)
	assert.Contains(t, output.String(), "✓ Logged out")

	// Credentials should be gone
	_, err = LoadCredentials("prod")
	require.Error(t, err)
}

// --- Whoami Tests ---

func TestWhoami(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	SaveCredentials("prod", &Credentials{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		User:        UserInfo{ID: "usr_xyz", Email: "salih@example.com"},
	})

	var output bytes.Buffer
	client := NewClient(Config{ClientID: "palbase-cli"}, &output)

	err := client.Whoami(context.Background())
	require.NoError(t, err)
	assert.Contains(t, output.String(), "salih@example.com")
	assert.Contains(t, output.String(), "usr_xyz")
}

// TestWhoami_NoEmail pins the empty-email path: palauth's userinfo returns
// only `sub`, so creds.User.Email is usually "". whoami must print just the
// id, never a dangling "User:    (usr_...)" with a leading space. (Live-found.)
func TestWhoami_NoEmail(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	SaveCredentials("prod", &Credentials{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		User:        UserInfo{ID: "usr_noemail"},
	})

	var output bytes.Buffer
	client := NewClient(Config{ClientID: "palbase-cli"}, &output)
	require.NoError(t, client.Whoami(context.Background()))
	assert.Contains(t, output.String(), "User:   usr_noemail\n")
	assert.NotContains(t, output.String(), "(usr_noemail)", "no dangling empty-email parens")
}

// --- GetValidToken Tests ---

func TestGetValidToken_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	SaveCredentials("prod", &Credentials{
		AccessToken: "my_token",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	})

	var output bytes.Buffer
	client := NewClient(Config{ClientID: "palbase-cli"}, &output)

	token, err := client.GetValidToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "my_token", token)
}

func TestGetValidToken_Expired_RefreshesAutomatically(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CLI-11: refresh path must present a DPoP proof so the new token
		// stays bound to the keyring key.
		assert.NotEmpty(t, r.Header.Get("DPoP"), "refresh must carry DPoP proof")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "refreshed_token",
			RefreshToken: "new_refresh",
			ExpiresIn:    900,
		})
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PALBASE_NO_KEYRING", "1")

	// Refresh signs a DPoP proof; provision the keyring key first.
	_, err := EnsureDPoPKey("prod")
	require.NoError(t, err)

	SaveCredentials("prod", &Credentials{
		AccessToken:  "expired",
		RefreshToken: "old_refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
	})

	var output bytes.Buffer
	client := NewClient(Config{
		AuthURL:  authServer.URL,
		ClientID: "palbase-cli",
	}, &output)

	token, err := client.GetValidToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "refreshed_token", token)
}

// --- ExpiresSoon (refresh-ahead threshold) Tests ---

func TestCredentials_ExpiresSoon(t *testing.T) {
	cases := []struct {
		name   string
		until  time.Duration // time left until ExpiresAt (negative = already expired)
		within time.Duration
		want   bool
	}{
		{"already expired", -1 * time.Minute, 5 * time.Minute, true},
		{"inside window", 20 * time.Second, 5 * time.Minute, true},
		{"on the far side", 30 * time.Minute, 5 * time.Minute, false},
		{"comfortable margin", 6 * time.Minute, 5 * time.Minute, false},
		// A non-positive window collapses to IsExpired: only a past ExpiresAt counts.
		{"zero window, valid", 10 * time.Second, 0, false},
		{"zero window, expired", -10 * time.Second, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creds := &Credentials{ExpiresAt: time.Now().Add(tc.until)}
			assert.Equal(t, tc.want, creds.ExpiresSoon(tc.within))
		})
	}
}

// TestGetFreshToken_RefreshesAhead is the refresh-AHEAD counterpart to
// TestGetValidToken_Expired_RefreshesAutomatically: the token still has life
// left (so GetValidToken would NOT refresh and would hand back the stale token),
// but it's inside the minRemaining window, so GetFreshToken refreshes proactively.
func TestGetFreshToken_RefreshesAhead(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Refresh path must present a DPoP proof so the new token stays bound.
		assert.NotEmpty(t, r.Header.Get("DPoP"), "refresh must carry DPoP proof")
		assert.Equal(t, "refresh_token", r.FormValue("grant_type"))
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "ahead_refreshed_token",
			RefreshToken: "new_refresh",
			ExpiresIn:    900,
		})
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PALBASE_NO_KEYRING", "1")

	_, err := EnsureDPoPKey("prod")
	require.NoError(t, err)

	// Token is NOT yet expired (20s left) — GetValidToken would return it as-is.
	SaveCredentials("prod", &Credentials{
		AccessToken:  "still_valid_but_soon",
		RefreshToken: "old_refresh",
		ExpiresAt:    time.Now().Add(20 * time.Second),
	})

	var output bytes.Buffer
	client := NewClient(Config{
		AuthURL:  authServer.URL,
		ClientID: "palbase-cli",
	}, &output)

	// Sanity: GetValidToken sees a non-expired token and does NOT refresh.
	stale, err := client.GetValidToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "still_valid_but_soon", stale, "GetValidToken must not refresh a non-expired token")

	// GetFreshToken with a 5m floor refreshes ahead because 20s < 5m.
	fresh, err := client.GetFreshToken(context.Background(), 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "ahead_refreshed_token", fresh, "GetFreshToken must refresh a soon-to-expire token")
}

// TestGetFreshToken_KeepsComfortableToken verifies GetFreshToken does NOT
// refresh when the token has more than minRemaining left (no needless churn).
func TestGetFreshToken_KeepsComfortableToken(t *testing.T) {
	refreshCalled := false
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalled = true
		json.NewEncoder(w).Encode(TokenResponse{AccessToken: "should_not_be_used", ExpiresIn: 900})
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PALBASE_NO_KEYRING", "1")

	SaveCredentials("prod", &Credentials{
		AccessToken: "comfortable_token",
		ExpiresAt:   time.Now().Add(30 * time.Minute),
	})

	var output bytes.Buffer
	client := NewClient(Config{AuthURL: authServer.URL, ClientID: "palbase-cli"}, &output)

	token, err := client.GetFreshToken(context.Background(), 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "comfortable_token", token)
	assert.False(t, refreshCalled, "GetFreshToken must not refresh a token with comfortable margin")
}

// --- Helpers ---

func parseAuthURL(rawURL string) (*httpURL, error) {
	// Simple URL parser for test
	parts := strings.SplitN(rawURL, "?", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("no query params in URL")
	}

	vals := make(map[string]string)
	for _, param := range strings.Split(parts[1], "&") {
		kv := strings.SplitN(param, "=", 2)
		if len(kv) == 2 {
			decoded, _ := decodePercent(kv[1])
			vals[kv[0]] = decoded
		}
	}

	return &httpURL{query: vals}, nil
}

type httpURL struct {
	query map[string]string
}

func (u *httpURL) Query() httpURLQuery {
	return httpURLQuery(u.query)
}

type httpURLQuery map[string]string

func (q httpURLQuery) Get(key string) string {
	return q[key]
}

func decodePercent(s string) (string, error) {
	// Use stdlib
	result := strings.Builder{}
	i := 0
	for i < len(s) {
		if s[i] == '%' && i+2 < len(s) {
			// Parse hex
			hi := unhex(s[i+1])
			lo := unhex(s[i+2])
			if hi >= 0 && lo >= 0 {
				result.WriteByte(byte(hi<<4 | lo))
				i += 3
				continue
			}
		}
		if s[i] == '+' {
			result.WriteByte(' ')
		} else {
			result.WriteByte(s[i])
		}
		i++
	}
	return result.String(), nil
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	}
	return -1
}

// Ensure io is imported (used by httptest setup)
var _ = io.Discard
