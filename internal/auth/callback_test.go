package auth

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/palgroup/palbase-cli/internal/config"
)

// THE TAB A PERSON LANDS ON IS PART OF THE PRODUCT.
//
// The redirect used to land on a bare sentence in the browser's default face on
// a black page — the one browser surface the CLI owns, looking like nothing
// else it ships. These are the marks that say it is the same product as the
// dashboard: the Studio tokens, the Palbase wordmark, the display face.
func TestTheCallbackTabIsTheProductNotADebugResponse(t *testing.T) {
	page := renderCallback(t, NewClient(Config{}, io.Discard), http.StatusOK, signedInCallback())

	for what, want := range map[string]string{
		"the Studio charcoal ground": "oklch(0.198 0.003 270)",
		"the Studio moss accent":     "oklch(0.74 0.16 155)",
		"the display face":           "--serif",
		"the Palbase mark":           "<svg",
		"the product's name":         "Palbase CLI",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the tab is missing %s (%q)", what, want)
		}
	}
	// It has to render with the network unavailable: a loopback listener that
	// lives for one request has no asset host behind it.
	for _, forbidden := range []string{"http://", "https://", "<link", "<script"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("the tab reaches outside itself: %q", forbidden)
		}
	}
}

// Success and failure are one page with one switch. The dot in the mark carries
// the outcome — moss when the terminal got its session, red when it didn't.
func TestTheCallbackTabSaysWhichOutcomeItIs(t *testing.T) {
	client := NewClient(Config{}, io.Discard)

	ok := renderCallback(t, client, http.StatusOK, signedInCallback())
	if !strings.Contains(ok, "<title>Palbase CLI — Signed in</title>") {
		t.Errorf("the success tab is not titled for a person:\n%s", firstLines(ok))
	}
	// The apostrophe arrives as &#39;: contextual escaping is the point of
	// html/template here, and the browser renders it as the headline reads.
	if !strings.Contains(ok, "You&#39;re signed in.") ||
		!strings.Contains(ok, "You can close this tab") {
		t.Errorf("the success tab does not say the terminal has the session:\n%s", ok)
	}
	if strings.Contains(ok, `class="failed"`) {
		t.Error("a completed sign-in carries the red accent")
	}
	if strings.Contains(ok, `class="reason"`) {
		t.Error("a completed sign-in has something to explain")
	}

	bad := renderCallback(t, client, http.StatusBadRequest,
		failedCallback("The provider redirected without an authorization code."))
	if !strings.Contains(bad, `<html lang="en" class="failed">`) {
		t.Error("a failed sign-in does not switch the accent to red")
	}
	if !strings.Contains(bad, "<title>Palbase CLI — Sign-in failed</title>") {
		t.Errorf("the failure tab is not titled for a person:\n%s", firstLines(bad))
	}
	if !strings.Contains(bad, "The provider redirected without an authorization code.") {
		t.Error("the failure tab does not carry the reason")
	}
	// A failure that does not say what to do next leaves a person switching
	// windows to find a terminal that is no longer waiting.
	if !strings.Contains(bad, "palbase login") {
		t.Error("the failure tab does not name the way out")
	}
}

// The provider controls error_description and the CLI reflects it into a page
// it serves itself, so this is the one place in the sign-in where remote text
// becomes local markup. The assertion drives the REAL listener — not
// writeCallbackPage — because the boundary that has to hold is query string →
// browser tab, and the revision this replaces printed it with fmt/%s.
func TestTheCallbackTabEscapesTheProvidersErrorDescription(t *testing.T) {
	const payload = `<script>alert(1)</script>`

	body, _ := driveCallback(t, "state=ours&error=access_denied&error_description="+
		"%3Cscript%3Ealert(1)%3C%2Fscript%3E")

	if strings.Contains(body, payload) {
		t.Fatalf("provider text reached the tab as live markup:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("the reason was not shown at all:\n%s", body)
	}
}

// EVERY WAY THE REDIRECT CAN GO WRONG LANDS ON THE PAGE. A refusal, a stolen
// state and a missing code used to answer with http.Error — a plain-text line
// in the browser's default face, which is precisely what this page exists to
// replace. The status code stays the HTTP truth; only the body changed.
func TestEveryFailedRedirectGetsThePageNotAPlainTextError(t *testing.T) {
	for name, query := range map[string]string{
		"a refusal the person made": "state=ours&error=access_denied&error_description=you+said+no",
		"a state that is not ours":  "state=someone-elses&code=STOLEN",
		"a redirect with no code":   "state=ours",
	} {
		t.Run(name, func(t *testing.T) {
			body, status := driveCallback(t, query)

			if status != http.StatusBadRequest {
				t.Errorf("status = %d, 400 bekleniyordu", status)
			}
			if !strings.Contains(body, `<html lang="en" class="failed">`) {
				t.Errorf("the browser got a bare error instead of the page:\n%s", body)
			}
		})
	}
}

// A non-default cloud is named in the tab as well as in the terminal: the tab
// cannot see the terminal, and signing in to the wrong cloud looks identical
// afterwards. The default one stays quiet.
func TestTheCallbackTabNamesANonDefaultCloud(t *testing.T) {
	named := renderCallback(t,
		NewClient(Config{AuthURL: "http://localhost:8888"}, io.Discard),
		http.StatusOK, signedInCallback())
	if !strings.Contains(named, "<dt>cloud</dt><dd>http://localhost:8888</dd>") {
		t.Errorf("a non-default cloud was not named in the tab:\n%s", named)
	}

	quiet := renderCallback(t,
		NewClient(Config{AuthURL: config.DefaultAuth()}, io.Discard),
		http.StatusOK, signedInCallback())
	if strings.Contains(quiet, "<dt>cloud</dt>") {
		t.Errorf("the configured cloud announced itself:\n%s", quiet)
	}
}

func renderCallback(t *testing.T, client *Client, status int, view callbackView) string {
	t.Helper()
	rec := httptest.NewRecorder()
	client.writeCallbackPage(rec, status, view)
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if rec.Code != status {
		t.Fatalf("status = %d, %d bekleniyordu", rec.Code, status)
	}
	return rec.Body.String()
}

// driveCallback runs the real loopback listener over one redirect and returns
// what the browser was handed.
func driveCallback(t *testing.T, query string) (body string, status int) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		body   string
		status int
	}
	got := make(chan answer, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		res, err := http.Get("http://" + ln.Addr().String() + "/callback?" + query)
		if err != nil {
			got <- answer{}
			return
		}
		defer func() { _ = res.Body.Close() }()
		raw, _ := io.ReadAll(res.Body)
		got <- answer{body: string(raw), status: res.StatusCode}
	}()

	client := NewClient(Config{}, io.Discard)
	_, _ = client.awaitCallback(context.Background(), ln, "ours")

	select {
	case a := <-got:
		return a.body, a.status
	case <-time.After(2 * time.Second):
		t.Fatal("the browser never got a response")
		return "", 0
	}
}

func firstLines(page string) string {
	lines := strings.SplitN(page, "\n", 9)
	if len(lines) > 8 {
		lines = lines[:8]
	}
	return strings.Join(lines, "\n")
}
