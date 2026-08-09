package auth

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
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
	k1, err := EnsureDPoPKey("dev")
	require.NoError(t, err)
	require.NotEmpty(t, k1.Thumbprint())

	// Second call must reuse the stored key (stable jkt across runs) so a
	// PAT bound to the jkt stays valid between CLI invocations.
	k2, err := EnsureDPoPKey("dev")
	require.NoError(t, err)
	require.Equal(t, k1.Thumbprint(), k2.Thumbprint(),
		"EnsureDPoPKey must reuse the stored key, not mint a new jkt each call")
}

func newManagementTestClient(t *testing.T, authURL string) *Client {
	t.Helper()
	return &Client{
		Cfg:        Config{AuthURL: authURL, ClientID: "palbase-cli", Mode: "dev"},
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
	require.NoError(t, SaveCredentials("dev", creds))

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

func TestManagementToken_ExpiredLogin_RefreshesViaDPoP(t *testing.T) {
	// CLI-12: an expired login token must auto-refresh through
	// /oauth/token rather than force the user to re-login. The refresh
	// must carry a DPoP proof so palauth keeps cnf.jkt on the renewed
	// access token — without it ManagementToken would return an unbound
	// token that the management API rejects at introspection (the exact
	// regression CLI-11 + CLI-12 close together).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")
	t.Setenv("PALBASE_NO_KEYRING", "1")

	// Provision a DPoP key so RefreshTokens can sign a proof.
	_, err := EnsureDPoPKey("dev")
	require.NoError(t, err)

	// Stored creds are expired — ManagementToken must refresh, not error.
	require.NoError(t, SaveCredentials("dev", &Credentials{
		AccessToken:  "stale_at",
		RefreshToken: "rt_alive",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}))

	var sawDPoP bool
	srv := &http.Server{Addr: "127.0.0.1:0"}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "refresh_token", r.PostForm.Get("grant_type"))
		assert.Equal(t, "rt_alive", r.PostForm.Get("refresh_token"))
		// The DPoP header is the whole point of this test — without it the
		// minted access token would silently lose cnf.jkt.
		if r.Header.Get("DPoP") != "" {
			sawDPoP = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "fresh_at_bound",
			RefreshToken: "rt_alive_v2",
			ExpiresIn:    1800,
			TokenType:    "DPoP",
		})
	})
	srv.Handler = mux

	listener, err := newLoopbackListener()
	require.NoError(t, err)
	// Serve always returns an error; ErrServerClosed is the normal path here,
	// produced by the deferred Close below.
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	c := newManagementTestClient(t, "http://"+listener.Addr().String())
	tok, err := c.ManagementToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, "fresh_at_bound", tok)
	assert.True(t, sawDPoP, "refresh on /oauth/token must carry a DPoP header so cnf.jkt is preserved")

	// Verify the refreshed credentials were persisted — next call should not
	// re-refresh.
	stored, err := LoadCredentials("dev")
	require.NoError(t, err)
	assert.Equal(t, "fresh_at_bound", stored.AccessToken)
	assert.Equal(t, "rt_alive_v2", stored.RefreshToken)
	assert.False(t, stored.IsExpired())
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
	_, err := EnsureDPoPKey("dev")
	require.NoError(t, err)

	require.NoError(t, SaveCredentials("dev", &Credentials{
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
