package sealedclient

// The server's own rule about which paths refuse a plaintext request, mirrored
// HERE and only here.
//
// ONE PLACE. The defect this package exists to fix was a plaintext GET to
// `/auth/oauth/config` — a path the server has required sealing on since the
// ratchet was introduced — and the CLI had nowhere that knew the rule at all.
// A second copy of it somewhere else in this repository is the same defect with
// a longer fuse: two detectors for one question is how a bodyless sealed read
// went unopened in production on the server side (the comment at
// v2/internal/server/sealed.go:392 records that one).

import (
	"fmt"
	"net/http"
	"strings"
)

// RequiredPrefixes is the server's `requiredSealedPrefixes`
// (v2/internal/server/sealed.go:361).
//
// IT ONLY EVER GROWS. A prefix is added when a caller learns to seal; none is
// ever removed, because removing one takes back a protection already given. The
// server holds `TestRequiredSealedPrefixesIsARatchet` over its copy; the CLI's
// copy is held by TestRequiredMirrorsTheServersRule.
//
// The five prefixes queued on the server side — /v1/notifications,
// /v1/user-flags, /v1/docs, /v1/db, /v1/analytics — arrive here when they
// arrive there, and this client seals them the day they do without another
// change.
var RequiredPrefixes = []string{
	"/auth/",
}

// ExemptPaths are the fixed provider ingress paths that accept ordinary OAuth
// GET/form requests, from the switch at v2/internal/server/sealed.go:367.
// Every other /auth request keeps the sealed contract.
var ExemptPaths = []string{
	"/auth/oauth/callback/google",
	"/auth/oauth/callback/apple",
	"/auth/oauth/callback/microsoft",
	"/auth/oauth/callback/github",
}

// Required reports whether the server refuses a plaintext request for this
// path. `path` is the URL PATH with no query string, which is what the server
// compares (`r.URL.Path`).
func Required(path string) bool {
	for _, exempt := range ExemptPaths {
		if path == exempt {
			return false
		}
	}
	for _, prefix := range RequiredPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// Guard wraps a transport so that a request to a path the server refuses
// plaintext on cannot leave this process in the clear.
//
// THIS IS THE GATE, and it sits on the transport rather than in a review
// checklist because the defect it exists for was invisible everywhere else: a
// bare `http.NewRequestWithContext(GET, …/auth/oauth/config)` compiled, every
// test passed, and the only thing that ever objected was a live stack answering
// 415 `sealed_required` — by which point the reader holds an HTTP status and no
// idea which request it was about. Here the same mistake is a local, named
// failure the moment it is written, in whichever package writes it.
//
// The one plaintext escape is a MEASUREMENT and not a preference: the sealing
// Client fetched the stack's keyset and got a 404, which is the server's own
// condition for not requiring a seal (`len(cfg.signedKeyset) > 0`,
// v2/internal/server/sealed.go:433). Only that Client can mark a request —
// StackPublishesNoKeyset reads a value no other package can construct — so this
// cannot be switched off by whatever it is guarding.
//
// Every client that addresses a Palbase stack must be wrapped in it;
// TestEveryStackClientIsGuarded holds that.
func Guard(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return guarded{base}
}

type guarded struct{ base http.RoundTripper }

func (g guarded) RoundTrip(r *http.Request) (*http.Response, error) {
	if Required(r.URL.Path) && !IsSealedRequest(r) && !StackPublishesNoKeyset(r.Context()) {
		return nil, fmt.Errorf(
			"refusing to send %s %s in the clear: the stack requires a sealed request there, "+
				"so it must go through internal/sealedclient",
			r.Method, r.URL.Path)
	}
	return g.base.RoundTrip(r)
}

// IsSealedRequest reports whether a request already carries an envelope — in a
// body with the sealed content type, or in the header for a bodyless one.
//
// This is the exact test the server's middleware uses to decide whether there
// is anything to open (v2/internal/sealed/middleware.go:208), and the CLI's
// outbound guard uses it to decide whether a request that MUST be sealed has
// been. Same question, same answer, one function.
func IsSealedRequest(r *http.Request) bool {
	if r.Header.Get(EnvelopeHeaderName) != "" {
		return true
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	// The media type, ignoring parameters and case, the way a Content-Type is
	// actually allowed to arrive.
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), ContentType)
}
