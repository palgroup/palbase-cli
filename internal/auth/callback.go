package auth

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/palgroup/palbase-cli/internal/config"
)

// THE CALLBACK TAB IS THE ONLY BROWSER SURFACE THIS CLI OWNS.
//
// Everything else a person sees during sign-in belongs to the panel. This one
// page is served by a loopback listener that lives for a single request, so it
// carries the Studio design tokens inline — moss brand on the warm charcoal
// ladder, serif display over a mono utility face — and reads as the same
// product as the dashboard rather than as a debug response.
//
//go:embed callback.html
var callbackPageHTML string

// callbackTmpl renders that tab. It is html/template rather than fmt on
// purpose: callbackView.Reason carries the identity provider's
// error_description straight off the query string, and contextual escaping is
// what stops a crafted redirect from turning this process's own listener into
// a script host.
var callbackTmpl = template.Must(template.New("callback").Parse(callbackPageHTML))

// callbackView is the entire browser-facing vocabulary of the callback. The
// wording tracks the CLI's own — the terminal says "Signing in", so this page
// says "Signed in" — so the tab and the terminal name one action, not two.
type callbackView struct {
	Status   string // eyebrow and tab title, e.g. "Signed in"
	Headline string
	Lede     string
	Reason   string // what went wrong; omitted on success
	Meta     []callbackMetaRow
	Failed   bool // switches the accent from moss to red
}

type callbackMetaRow struct{ Label, Value string }

// signedInCallback is what the browser shows once the terminal has the code.
func signedInCallback() callbackView {
	return callbackView{
		Status:   "Signed in",
		Headline: "You're signed in.",
		Lede:     "Your terminal has the session. You can close this tab.",
	}
}

// failedCallback builds the shared failure view. Only the reason differs
// between the ways a redirect can go wrong; the instruction is always the same,
// so it is written once here.
func failedCallback(reason string) callbackView {
	return callbackView{
		Failed:   true,
		Status:   "Sign-in failed",
		Headline: "Sign-in didn't complete.",
		Lede:     "Nothing was saved. Return to your terminal and run palbase login again.",
		Reason:   reason,
	}
}

// writeCallbackPage renders view into the redirected browser tab.
//
// The status code is the HTTP truth — a refused redirect is a 400 and stays one
// — while the body is the page a person reads; a browser renders both.
func (c *Client) writeCallbackPage(w http.ResponseWriter, status int, view callbackView) {
	// EVERY OUTCOME CARRIES THE CLOUD IT WAS AGAINST, when that is not the one
	// this binary is built for. Signing in to the wrong cloud looks identical
	// afterwards, and the tab cannot see the line the terminal printed.
	if row, ok := cloudMetaRow(c.Cfg.AuthURL); ok {
		view.Meta = append(view.Meta, row)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// A render failure leaves the person on a blank tab with no idea whether the
	// CLI got the session, so it belongs in the terminal rather than in /dev/null.
	if err := callbackTmpl.Execute(w, view); err != nil {
		fmt.Fprintf(c.output(), "(could not render callback page: %s)\n", err)
	}
}

// cloudMetaRow names a non-default control plane, and stays silent for the
// default one. The same question printDeployment answers in the terminal: a
// line every person sees on every sign-in teaches them to stop reading.
func cloudMetaRow(cloud string) (callbackMetaRow, bool) {
	if cloud == "" || strings.TrimRight(cloud, "/") == strings.TrimRight(config.DefaultPlatformAPI(), "/") {
		return callbackMetaRow{}, false
	}
	return callbackMetaRow{Label: "cloud", Value: cloud}, true
}

// output is the terminal, or nothing when a caller built a Client without one.
func (c *Client) output() io.Writer {
	if c.Output == nil {
		return io.Discard
	}
	return c.Output
}
