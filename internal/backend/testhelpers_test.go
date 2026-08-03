package backend

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file used to also carry localSpecServer / localServeStub / localServeDown
// — stubs that stood in for `palbase serve`'s local spec server on
// localhost:4003. serve is retired and nothing probes that address any more
// (the codegen --watch default that did was removed too), so the stubs went
// with it rather than sitting here as scaffolding for a scenario that can no
// longer occur.

// redirectHostTo installs a temporary RoundTripper on defaultHTTPClient that
// rewrites requests to `host` (the remote tenant host) to `targetURL` (the
// httptest mock), leaving every other request untouched. Returns a restore func.
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
