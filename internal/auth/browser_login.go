package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SIGNING IN OPENS A BROWSER. It does not ask for a password.
//
// A CLI that reads a password has to be trusted with it — by the person typing,
// by whatever is recording the terminal, and by every later version of itself.
// The Authorization Code flow with PKCE removes that: the password is typed
// into the panel, over TLS, on a page whose address the person can read, and
// what comes back here is a code that is worthless without a secret this
// process generated and never sent.
//
// The chain, all four legs measured live (2026-08-22):
//
//	/oauth/authorize          → an auth_request_id
//	panel /login?auth_request_id=…  → a person stands behind it
//	/oauth/authorize/callback → 302 to this process's loopback, with a code
//	/oauth/token              → the session, exchanged with the verifier
//
// The CLI never sees the credential, and the panel never sees the verifier.

// authorizeScopes is what the CLI asks for. `offline_access` is what makes a
// refresh token come back — without it every session dies in an hour and the
// person signs in again mid-deploy.
const authorizeScopes = "openid profile email offline_access"

// browserLoginTimeout bounds the wait for somebody to finish in the browser.
// Generous on purpose: a first sign-in may include creating the account.
var browserLoginTimeout = 5 * time.Minute

// BrowserLogin runs the Authorization Code + PKCE flow and returns the session.
func (c *Client) BrowserLogin(ctx context.Context, create bool) (*Credentials, error) {
	plane := c.plane()
	boot, err := plane.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}

	verifier, challenge, err := newPKCE()
	if err != nil {
		return nil, err
	}
	state, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}

	// THE LISTENER COMES FIRST. Asking the plane to start an authorization that
	// redirects to a port nothing is listening on produces a code delivered to
	// a closed door — and the person watching the browser sees a refused
	// connection with no idea which side failed.
	ln, redirectURI, err := listenLoopback()
	if err != nil {
		return nil, err
	}
	defer func() { _ = ln.Close() }()

	authRequestID, err := plane.BeginAuthorization(ctx, boot, redirectURI, challenge, state)
	if err != nil {
		return nil, err
	}

	// The person goes to the PANEL, not to the API: the authorize endpoint is a
	// machine surface behind an apikey header, and a browser sends no such
	// header. The panel is where a human signs in, and it carries the id back.
	handoff := handoffURL(c.Cfg.StudioURL, create, authRequestID)
	fmt.Fprintf(c.Output, "Opening %s\n", handoff)
	fmt.Fprintln(c.Output, "Waiting for you to finish in the browser…")
	if err := c.OpenBrowser(handoff); err != nil {
		// A headless machine has no browser and that is not a failure: print
		// the URL so it can be opened somewhere that does.
		fmt.Fprintf(c.Output, "Could not open a browser (%v). Open the link above.\n", err)
	}

	code, err := awaitCallback(ctx, ln, state)
	if err != nil {
		return nil, err
	}
	return plane.ExchangeCode(ctx, boot, code, redirectURI, verifier)
}

// newPKCE returns a verifier and its S256 challenge. The verifier never leaves
// this process; the challenge is public by construction.
func newPKCE() (verifier, challenge string, err error) {
	verifier, err = randomURLSafe(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	// SECURITY VALUE: crypto/rand, never math/rand. A guessable verifier is a
	// verifier that proves nothing.
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// listenLoopback binds the first free port the client is registered for. The
// ports are not arbitrary: they are the redirect URIs seeded into the
// `palbase-cli` OAuth client, and a port outside that list is refused by the
// authorization server rather than silently accepted.
func listenLoopback() (net.Listener, string, error) {
	var lastErr error
	for _, port := range LoopbackCallbackPorts {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			lastErr = err
			continue
		}
		return ln, fmt.Sprintf("http://localhost:%d/callback", port), nil
	}
	return nil, "", fmt.Errorf(
		"every callback port this client is registered for is busy (%v): close what is holding one and try again: %w",
		LoopbackCallbackPorts, lastErr)
}

// awaitCallback serves exactly one request and returns the code it carried.
func awaitCallback(ctx context.Context, ln net.Listener, state string) (string, error) {
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)

	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			// STATE IS CHECKED BEFORE THE CODE IS TOUCHED. Without it any page
			// on the machine could drive this listener to redeem a code the
			// person never asked for.
			if q.Get("state") != state {
				http.Error(w, "this callback did not come from the sign-in that started here", http.StatusBadRequest)
				done <- result{err: errors.New("the callback carried the wrong state — sign-in abandoned")}
				return
			}
			if e := q.Get("error"); e != "" {
				reason := e
				if d := q.Get("error_description"); d != "" {
					reason = e + " — " + d
				}
				http.Error(w, reason, http.StatusBadRequest)
				done <- result{err: fmt.Errorf("sign-in refused: %s", reason)}
				return
			}
			code := q.Get("code")
			if code == "" {
				http.Error(w, "no code", http.StatusBadRequest)
				done <- result{err: errors.New("the callback carried no code")}
				return
			}
			w.Header().Set("content-type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(callbackPage))
			done <- result{code: code}
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	select {
	case r := <-done:
		return r.code, r.err
	case <-time.After(browserLoginTimeout):
		return "", fmt.Errorf("gave up waiting for the browser after %s", browserLoginTimeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// callbackPage is what the browser shows when it lands. Self-contained: this
// server exists for one request and cannot serve a stylesheet.
const callbackPage = `<!doctype html><meta charset="utf-8"><title>Signed in</title>
<body style="font:16px/1.5 ui-sans-serif,system-ui;background:#0b0b0d;color:#e7e7ea;display:grid;place-items:center;height:100vh;margin:0">
<div style="text-align:center"><p style="font-size:20px;margin:0 0 8px">Signed in.</p>
<p style="opacity:.6;margin:0">You can close this tab and go back to the terminal.</p></div>`

// handoffURL, kişinin gönderileceği panel sayfası.
//
// `/auth/login` — "Sign in to authorize" — auth_request_id'yi OKUYAN tek
// sayfadır. Sıradan `/login` parametreyi görmezden gelir: kişi giriş yapar,
// proje listesine düşer ve CLI zaman aşımına uğrayana kadar döngüsel adreste
// bekler. Hiçbir yerde hata görünmez; giriş yalnızca hiç bitmez.
func handoffURL(studioURL string, create bool, authRequestID string) string {
	page := "/auth/login"
	if create {
		page = "/signup"
	}
	return strings.TrimRight(studioURL, "/") + page + "?auth_request_id=" + url.QueryEscape(authRequestID)
}
