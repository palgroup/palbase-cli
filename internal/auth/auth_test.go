package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	err := SaveCredentials(creds)
	require.NoError(t, err)

	// Check file permissions
	path := filepath.Join(tmpDir, ".palbase", "session.json")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	loaded, err := LoadCredentials()
	require.NoError(t, err)

	assert.Equal(t, creds.AccessToken, loaded.AccessToken)
	assert.Equal(t, creds.RefreshToken, loaded.RefreshToken)
	assert.Equal(t, creds.User.Email, loaded.User.Email)
}

func TestLoadCredentials_NotLoggedIn(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	_, err := LoadCredentials()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func TestDeleteCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Save first
	err := SaveCredentials(&Credentials{AccessToken: "x"})
	require.NoError(t, err)

	// Delete
	err = DeleteCredentials()
	require.NoError(t, err)

	// Load should fail
	_, err = LoadCredentials()
	require.Error(t, err)
}

// A SESSION IS RENEWED AT THE DOOR IT WAS OPENED AT.
//
// `palbase login` redeems its code at the OIDC token endpoint, so the refresh
// token it receives lives in the OIDC store. Refreshing used to ask palauth's
// SEPARATE, non-OIDC refresh store instead — which has never heard of that
// token. Measured live 25.08.2026, three times in a row: every command thirty
// minutes after a sign-in answered `refresh tokens: 401 — invalid_token`, and
// the only way back was to open the browser again.
//
// The ROTATED refresh token must still be stored. Keeping the old one would
// make the NEXT refresh present a token the server has already retired: the
// same sign-out, half an hour later, for no reason the person can see.
func TestRefreshTokens(t *testing.T) {
	var gotPath, gotAPIKey, gotGrant, gotRefresh, gotClient string
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon", Issuer: "https://plane.test"})
			return
		}
		_ = r.ParseForm()
		gotPath, gotAPIKey = r.URL.Path, r.Header.Get("apikey")
		gotGrant, gotRefresh, gotClient = r.PostFormValue("grant_type"), r.PostFormValue("refresh_token"), r.PostFormValue("client_id")
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "new_access",
			RefreshToken: "new_refresh",
			ExpiresIn:    900,
		})
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	var output bytes.Buffer
	client := NewClient(Config{AuthURL: authServer.URL, ClientID: "palbase-cli"}, &output)

	newCreds, err := client.RefreshTokens(context.Background(), &Credentials{
		AccessToken:  "expired_access",
		RefreshToken: "old_refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		User:         UserInfo{ID: "usr_1", Email: "test@example.com"},
	})
	require.NoError(t, err)

	assert.Equal(t, "/oauth/token", gotPath, "a browser-minted session is renewed where it was minted")
	assert.Equal(t, "refresh_token", gotGrant)
	assert.Equal(t, "palbase-cli", gotClient)
	assert.Equal(t, "pb_anon", gotAPIKey, "the auth surface refuses a request without the anon apikey")
	assert.Equal(t, "old_refresh", gotRefresh)
	assert.Equal(t, "new_access", newCreds.AccessToken)
	assert.Equal(t, "new_refresh", newCreds.RefreshToken, "the rotated refresh token must replace the old one")
	assert.False(t, newCreds.IsExpired())
}

// A refresh the server rejects must carry the server's own words: "the session
// expired" and "the gateway refused" call for different actions.
func TestRefreshTokensSurfacesTheServersReason(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon"})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"The refresh token has been revoked"}`))
	}))
	defer authServer.Close()
	t.Setenv("HOME", t.TempDir())

	var output bytes.Buffer
	client := NewClient(Config{AuthURL: authServer.URL}, &output)
	_, err := client.RefreshTokens(context.Background(), &Credentials{RefreshToken: "dead"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "The refresh token has been revoked")
}

// --- Logout Tests ---

func TestLogout(t *testing.T) {
	type call struct {
		path   string
		apikey string
		bearer string
	}
	calls := make(chan call, 4)
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon"})
			return
		}
		calls <- call{
			path:   r.URL.Path,
			apikey: r.Header.Get("apikey"),
			bearer: r.Header.Get("Authorization"),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer authServer.Close()

	t.Setenv("HOME", t.TempDir())
	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken:  "access",
		RefreshToken: "refresh",
		User:         UserInfo{Email: "test@example.com"},
	}))

	var output bytes.Buffer
	client := NewClient(Config{AuthURL: authServer.URL, ClientID: "palbase-cli"}, &output)
	require.NoError(t, client.Logout(context.Background()))

	got := <-calls
	assert.Equal(t, "/auth/logout", got.path)
	assert.Equal(t, "pb_anon", got.apikey)
	assert.Equal(t, "Bearer access", got.bearer, "the session is named by its access token")
	assert.Contains(t, output.String(), "✓ Logged out")

	_, err := LoadCredentials()
	require.Error(t, err, "the local credential must be gone")
}

// A server that refuses the sign-out must NOT leave the credential behind: the
// person typed "logout" and would otherwise keep holding a live token they
// believe is gone.
func TestLogoutForgetsLocallyEvenWhenTheServerRefuses(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer authServer.Close()

	t.Setenv("HOME", t.TempDir())
	require.NoError(t, SaveCredentials(&Credentials{AccessToken: "access"}))

	var output bytes.Buffer
	client := NewClient(Config{AuthURL: authServer.URL}, &output)
	require.NoError(t, client.Logout(context.Background()))

	_, err := LoadCredentials()
	require.Error(t, err, "a server error must not strand a live credential on this machine")
}

// --- Whoami Tests ---

func TestWhoami(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		User:        UserInfo{ID: "usr_xyz", Email: "salih@example.com"},
	}))

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

	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		User:        UserInfo{ID: "usr_noemail"},
	}))

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

	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken: "my_token",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	}))

	var output bytes.Buffer
	client := NewClient(Config{ClientID: "palbase-cli"}, &output)

	token, err := client.GetValidToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "my_token", token)
}

func TestGetValidToken_Expired_RefreshesAutomatically(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon"})
			return
		}
		assert.Equal(t, "/oauth/token", r.URL.Path)
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "refreshed_token",
			RefreshToken: "new_refresh",
			ExpiresIn:    900,
		})
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PALBASE_NO_KEYRING", "1")

	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken:  "expired",
		RefreshToken: "old_refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
	}))

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
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon"})
			return
		}
		assert.Equal(t, "/oauth/token", r.URL.Path)
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "ahead_refreshed_token",
			RefreshToken: "new_refresh",
			ExpiresIn:    900,
		})
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PALBASE_NO_KEYRING", "1")

	_, err := EnsureDPoPKey()
	require.NoError(t, err)

	// Token is NOT yet expired (20s left) — GetValidToken would return it as-is.
	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken:  "still_valid_but_soon",
		RefreshToken: "old_refresh",
		ExpiresAt:    time.Now().Add(20 * time.Second),
	}))

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
		_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: "should_not_be_used", ExpiresIn: 900})
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PALBASE_NO_KEYRING", "1")

	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken: "comfortable_token",
		ExpiresAt:   time.Now().Add(30 * time.Minute),
	}))

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
