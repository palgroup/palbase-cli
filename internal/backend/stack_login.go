package backend

// stack_login.go — how this CLI talks to one project.
//
// What used to live here was a second way to authenticate: sign in to a project
// with an email and password, keep the access token, throw the refresh away.
// Measured on 2026-08-16 that token lives 1800 seconds, which is why `palbase
// push` kept answering "that stack no longer accepts this session" half an hour
// into an afternoon. Identity now has ONE resolver (credentials.go) and that
// path is gone rather than patched.
//
// The key-rewriter that lived beside it is gone too: it had no caller, and it
// wrote the single-environment app config that `link` stopped producing when an
// app started holding every environment at once.

import (
	"crypto/tls"
	"net/http"
	"time"
)

// stackClient talks to one project, trusting a self-signed certificate only
// when the link said to.
// HTTPClient is how anything talks to a project: `db` and `secret` live in their
// own packages and must not each decide TLS policy — one target, one client.
func HTTPClient(t Target) *http.Client { return stackClient(t) }

func stackClient(t Target) *http.Client {
	c := &http.Client{Timeout: 5 * time.Minute}
	if t.Insecure {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in at link time
	}
	return c
}
