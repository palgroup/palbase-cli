package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FOUR ENVIRONMENTS, ONE SESSION. Linking describes every environment of a
// project concurrently, and every description asks for a management token. A
// refresh token is spent when it is used: two callers refreshing the same
// expired session race, and the loser is told to sign in again.
func TestManagementTokenRefreshesOnceForConcurrentCallers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")
	t.Setenv(AccountTokenEnv, "")
	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken:  "stale_at",
		RefreshToken: "rt_alive",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}))

	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon"})
			return
		}
		refreshes.Add(1)
		time.Sleep(50 * time.Millisecond) // hold the window open
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "fresh_at", RefreshToken: "rt_alive_v2", ExpiresIn: 1800,
		})
	}))
	defer srv.Close()

	client := NewClient(Config{AuthURL: srv.URL}, io.Discard)
	tokens := make([]string, 4)
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = client.ManagementToken(context.Background())
		}(i)
	}
	wg.Wait()

	for i := range tokens {
		require.NoError(t, errs[i])
		assert.Equal(t, "fresh_at", tokens[i])
	}
	assert.Equal(t, int32(1), refreshes.Load(), "the expired session was refreshed more than once")
}

// A FAILED REFRESH IS SHARED WITH THE CALLERS BEHIND IT. Each of them used to
// present the same refresh token again: four requests for one dead session.
// And it is not remembered past them — the first caller after the queue has
// drained tries again.
func TestManagementTokenSharesAFailedRefreshWithTheCallersBehindIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")
	t.Setenv(AccountTokenEnv, "")
	require.NoError(t, SaveCredentials(&Credentials{
		AccessToken:  "stale_at",
		RefreshToken: "rt_dead",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}))

	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/cloud/config" {
			_ = json.NewEncoder(w).Encode(Bootstrap{AnonKey: "pb_anon"})
			return
		}
		// The first refresh answers only once all four callers are queued, so
		// the measurement does not depend on how fast goroutines start.
		if refreshes.Add(1) == 1 {
			deadline := time.Now().Add(5 * time.Second)
			for refreshWaiters.Load() < 4 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	client := NewClient(Config{AuthURL: srv.URL}, io.Discard)
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = client.ManagementToken(context.Background())
		}(i)
	}
	wg.Wait()

	for i := range errs {
		require.Error(t, errs[i])
	}
	assert.Equal(t, int32(1), refreshes.Load(), "the callers behind a failed refresh each presented the refresh token again")

	_, err := client.ManagementToken(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(2), refreshes.Load(), "a caller after the queue drained did not try again")
}
