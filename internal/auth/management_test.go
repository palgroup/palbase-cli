package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLoopbackListener gives the test an ephemeral 127.0.0.1 port so two
// management-refresh tests can run in parallel (or alongside the rest of
// the auth suite) without colliding on a fixed port.
func newLoopbackListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func TestEnsureDPoPKey_CreatesThenReuses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_NO_KEYRING", "1")

	// First call provisions a fresh key.
	k1, err := EnsureDPoPKey()
	require.NoError(t, err)
	require.NotEmpty(t, k1.Thumbprint())

	// Second call must reuse the stored key (stable jkt across runs) so a
	// PAT bound to the jkt stays valid between CLI invocations.
	k2, err := EnsureDPoPKey()
	require.NoError(t, err)
	require.Equal(t, k1.Thumbprint(), k2.Thumbprint(),
		"EnsureDPoPKey must reuse the stored key, not mint a new jkt each call")
}

func newManagementTestClient(t *testing.T, authURL string) *Client {
	t.Helper()
	return &Client{
		Cfg:        Config{AuthURL: authURL, ClientID: "palbase-cli"},
		HttpClient: &http.Client{Timeout: 5 * time.Second},
		Output:     io.Discard,
	}
}

func TestManagementToken_FromEnv(t *testing.T) {
	// Env-supplied PAT wins over any stored credentials AND skips refresh —
	// headless callers (CI, AI agents) supply the credential explicitly and
	// we must not touch their login state.
	t.Setenv("PALBASE_ACCESS_TOKEN", "pat_headless_123")
	c := newManagementTestClient(t, "http://invalid.example")
	tok, err := c.ManagementToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, "pat_headless_123", tok)
}

func TestManagementToken_LoginFallback(t *testing.T) {
	// Isolate HOME so the test sees only credentials we write here.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")

	// A logged-in user without a PAT must still authenticate to the
	// management API — their DPoP-bound login access token is the
	// credential.
	creds := &Credentials{
		AccessToken: "login_at_xyz",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	require.NoError(t, SaveCredentials(creds))

	c := newManagementTestClient(t, "http://invalid.example")
	tok, err := c.ManagementToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, "login_at_xyz", tok)
}

func TestManagementToken_UnauthenticatedIsActionable(t *testing.T) {
	// Neither env nor stored credentials → actionable error.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")

	c := newManagementTestClient(t, "http://invalid.example")
	_, err := c.ManagementToken(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase login")
}

// An expired session must refresh in place rather than force a fresh sign-in:
// the access token lives 30 minutes, and a person working through an afternoon
// would otherwise be asked for their password every half hour.
func TestManagementTokenRefreshesAnExpiredSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")

	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken:  "stale_at",
		RefreshToken: "rt_alive",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}))

	var refreshPath, sentRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon"})
			return
		}
		// FORM, JSON DEĞİL: yenileme artık girişin kullandığı jeton ucuna
		// gidiyor ve o uç form konuşuyor (RFC 6749 §6).
		_ = r.ParseForm()
		refreshPath, sentRefresh = r.URL.Path, r.PostFormValue("refresh_token")
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "fresh_at",
			RefreshToken: "rt_alive_v2",
			ExpiresIn:    1800,
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	client := NewClient(Config{AuthURL: srv.URL}, &out)

	token, err := client.ManagementToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fresh_at", token)
	assert.Equal(t, "/oauth/token", refreshPath)
	assert.Equal(t, "rt_alive", sentRefresh)

	// And the rotated pair must be on disk: the NEXT command reads it.
	stored, err := LoadCredentials()
	require.NoError(t, err)
	assert.Equal(t, "rt_alive_v2", stored.RefreshToken)
}

func TestManagementToken_ExpiredLogin_RefreshFailureIsActionable(t *testing.T) {
	// When the refresh token itself is dead (30-day expiry, family-revoked,
	// or DPoP key wiped) ManagementToken must surface both signals: the
	// "run palbase login" prompt AND the underlying refresh reason. Silently
	// swallowing the refresh error would mask, e.g., a server outage as a
	// login problem.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")
	t.Setenv("PALBASE_NO_KEYRING", "1")
	_, err := EnsureDPoPKey()
	require.NoError(t, err)

	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken:  "stale",
		RefreshToken: "rt_dead",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}))

	srv := &http.Server{Addr: "127.0.0.1:0"}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	})
	srv.Handler = mux

	listener, err := newLoopbackListener()
	require.NoError(t, err)
	// Serve always returns an error; ErrServerClosed is the normal path here,
	// produced by the deferred Close below.
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	c := newManagementTestClient(t, "http://"+listener.Addr().String())
	_, terr := c.ManagementToken(context.Background())
	require.Error(t, terr)
	msg := terr.Error()
	assert.True(t, strings.Contains(msg, "palbase login"),
		"refresh failure must still tell the user to log in: %s", msg)
	assert.True(t, strings.Contains(msg, "refresh failed"),
		"refresh failure must surface the underlying reason so server outages don't masquerade as login problems: %s", msg)
}

// HESAP TOKEN'I YALNIZ PALBASE_ACCESS_TOKEN YOKKEN devreye girer.
//
// Bu testin varlık sebebi: yeni bir kimlik kaynağı eklerken en kolay yapılan
// hata, var olanın önüne geçirmektir. Bugün PALBASE_ACCESS_TOKEN ihraç etmiş
// her kurulum aynen çalışmaya devam etmeli.
func TestAccountTokenIsReadOnlyWhenAccessTokenIsAbsent(t *testing.T) {
	c := &Client{}

	t.Setenv("PALBASE_ACCESS_TOKEN", "oturum-jetonu")
	t.Setenv(AccountTokenEnv, "pat_hesap")
	got, err := c.ManagementToken(context.Background())
	if err != nil || got != "oturum-jetonu" {
		t.Fatalf("hesap token'ı mevcut kimliğin ÖNÜNE geçti: %q (%v)", got, err)
	}

	t.Setenv("PALBASE_ACCESS_TOKEN", "")
	got, err = c.ManagementToken(context.Background())
	if err != nil || got != "pat_hesap" {
		t.Fatalf("hesap token'ı okunmadı: %q (%v)", got, err)
	}
}
