package backend

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// localSpecServer stands in for a local endpoint serving an empty
// OpenAPI spec, so the auto/local spec path takes the LOCAL-spec branch.
func localSpecServer(t *testing.T) {
	t.Helper()
	localServeStub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openapi":"3.1.0","info":{"title":"Palbase Backend","version":"1.0.0"},"paths":{},"components":{}}`))
	}))
}

// localServeStub redirects the fixed local-serve origin (localhost:4003) to an
// ephemeral httptest server running `handler`, via the defaultHTTPClient seam.
// Hermetic: works (and keeps full coverage) even when a real serve holds 4003
// on the dev machine — a bind-based check false-frees against a wildcard
// listener with SO_REUSEADDR, and localhost can resolve to ::1 past a
// 127.0.0.1-only stub.
func localServeStub(t *testing.T, handler http.Handler) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Cleanup(redirectHostTo(t, "localhost:4003", srv.URL))
}

// localServeDown redirects the fixed local-serve origin to a port that is
// guaranteed closed, so the probe connection-refuses deterministically — the
// serve-down / remote-fallback scenarios need that regardless of what really
// listens on 4003.
func localServeDown(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedAddr := ln.Addr().String()
	require.NoError(t, ln.Close())
	t.Cleanup(redirectHostTo(t, "localhost:4003", "http://"+closedAddr))
}

// redirectHostTo installs a temporary RoundTripper on defaultHTTPClient that
// rewrites requests to `host` (the remote tenant host) to `targetURL` (the
// httptest mock), leaving every other request untouched. Returns a restore func.
// Used to exercise the serve-down deployed-spec fetch without a real host.
func redirectHostTo(t *testing.T, host, targetURL string) func() {
	t.Helper()
	u, err := url.Parse(targetURL)
	require.NoError(t, err)
	prev := defaultHTTPClient.Transport
	base := prev
	if base == nil {
		base = http.DefaultTransport
	}
	defaultHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == host {
			req = req.Clone(req.Context())
			req.URL.Scheme = u.Scheme
			req.URL.Host = u.Host
		}
		return base.RoundTrip(req)
	})
	return func() { defaultHTTPClient.Transport = prev }
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
