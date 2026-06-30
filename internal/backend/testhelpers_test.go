package backend

import (
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// localSpecServer stands up a stub `palbase serve` on localhost:4003 (the fixed
// local-codegen port) serving an empty OpenAPI spec, so the auto/local spec path
// takes the LOCAL-spec branch. Skips the test if 4003 is already bound (a real
// serve running) — we don't want to fight it.
func localSpecServer(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:4003")
	if err != nil {
		t.Skipf("localhost:4003 unavailable (%v) — skipping local-spec path test", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openapi":"3.1.0","info":{"title":"Palbase Backend","version":"1.0.0"},"paths":{},"components":{}}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
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
