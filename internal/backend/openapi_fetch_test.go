package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fetchOpenAPISpec must be wake-aware: a cold/idle backend pod sits behind a
// Kong wake-and-hold that can take up to ~90s and, when scheduling is starved,
// returns an honest 503 backend_unavailable (Retry-After: 5). The fetch needs
// to ride that out instead of failing on the first 503 / 30s timeout.
//
// These tests use a tiny per-attempt timeout + tiny retry budget so they run
// fast; the production caller passes the real (longer) values.

func okSpec(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{},"components":{}}`))
}

// A 503 "wake in progress" on the first attempt must be retried, and the
// eventual 200 must succeed — this is BRANCH B (wake fails transiently).
func TestFetchOpenAPISpec_RetriesOn503ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0") // wake in progress; retry immediately
			http.Error(w, `{"error":"backend_unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		okSpec(w)
	}))
	defer srv.Close()

	body, err := fetchOpenAPISpecOpts(context.Background(), srv.URL+"/admin/openapi.json", "k", fetchOpts{
		attemptTimeout: 5 * time.Second,
		totalBudget:    10 * time.Second,
		minBackoff:     time.Millisecond,
	})
	require.NoError(t, err)
	require.Contains(t, string(body), `"openapi"`)
	require.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(2), "must have retried the 503")
}

// 502 (transient first-byte from a just-woken pod) must also be retried.
func TestFetchOpenAPISpec_RetriesOn502(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		okSpec(w)
	}))
	defer srv.Close()

	body, err := fetchOpenAPISpecOpts(context.Background(), srv.URL+"/admin/openapi.json", "k", fetchOpts{
		attemptTimeout: 5 * time.Second,
		totalBudget:    10 * time.Second,
		minBackoff:     time.Millisecond,
	})
	require.NoError(t, err)
	require.Contains(t, string(body), `"openapi"`)
	require.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(2))
}

// When the budget is exhausted while the backend keeps 503-ing, the final
// error must surface the real status (not a bare context-cancel) so the user
// can tell "backend never woke" from a hard failure.
func TestFetchOpenAPISpec_BudgetExhaustedSurfacesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"error":"backend_unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := fetchOpenAPISpecOpts(context.Background(), srv.URL+"/admin/openapi.json", "k", fetchOpts{
		attemptTimeout: 1 * time.Second,
		totalBudget:    150 * time.Millisecond, // tiny: gives up fast
		minBackoff:     50 * time.Millisecond,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "503", "exhausted error must name the last status")
}

// A hard 4xx (e.g. 401 invalid_apikey) is NOT a wake signal — it must fail
// immediately with no retry. Retrying an auth failure just wastes the budget.
func TestFetchOpenAPISpec_NoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, `{"error":"invalid_apikey"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := fetchOpenAPISpecOpts(context.Background(), srv.URL+"/admin/openapi.json", "k", fetchOpts{
		attemptTimeout: 5 * time.Second,
		totalBudget:    10 * time.Second,
		minBackoff:     time.Millisecond,
	})
	require.Error(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "4xx must not be retried")
	require.Contains(t, err.Error(), "401")
}

// A connection-refused (no server listening — a probe against a dead endpoint
// when serve is down) must return FAST without burning the retry budget, so the
// remote fallback kicks in immediately.
func TestFetchOpenAPISpec_ConnRefusedFailsFast(t *testing.T) {
	start := time.Now()
	_, err := fetchOpenAPISpecOpts(context.Background(), "http://127.0.0.1:1/openapi.json", "", fetchOpts{
		attemptTimeout: 5 * time.Second,
		totalBudget:    30 * time.Second, // big budget, but we must NOT wait it out
		minBackoff:     5 * time.Second,
	})
	require.Error(t, err)
	require.Less(t, time.Since(start), 3*time.Second, "connection-refused must fail fast, not retry the budget")
}

// First-attempt success makes exactly one call — no wasted round-trips on the
// warm/steady-state path (the common case).
func TestFetchOpenAPISpec_WarmPathSingleCall(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		require.Equal(t, "k", r.Header.Get("apikey"))
		okSpec(w)
	}))
	defer srv.Close()

	body, err := fetchOpenAPISpecOpts(context.Background(), srv.URL+"/admin/openapi.json", "k", fetchOpts{
		attemptTimeout: 5 * time.Second,
		totalBudget:    10 * time.Second,
		minBackoff:     time.Millisecond,
	})
	require.NoError(t, err)
	require.Contains(t, string(body), `"openapi"`)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

// A caller-cancelled context must abort promptly and not be masked by retry.
func TestFetchOpenAPISpec_RespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"error":"backend_unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchOpenAPISpecOpts(ctx, srv.URL+"/admin/openapi.json", "k", fetchOpts{
		attemptTimeout: 5 * time.Second,
		totalBudget:    10 * time.Second,
		minBackoff:     time.Millisecond,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled), "cancelled context must surface, got %v", err)
}
