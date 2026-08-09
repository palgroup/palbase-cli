package auth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The provider controls error_description and the CLI reflects it into a page
// it serves itself, so this is the one place in the login flow where remote
// text becomes local markup. The assertion is on the real handler — not on
// writeCallbackPage — because the boundary that has to hold is query string →
// browser tab, and an earlier revision printed it with fmt.Fprintf(%s).
func TestLogin_CallbackPageEscapesProviderErrorDescription(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer authServer.Close()

	t.Setenv("HOME", t.TempDir())
	// Login provisions a DPoP key; pin the 0600 file fallback so the test never
	// blocks on a host keychain prompt.
	t.Setenv("PALBASE_NO_KEYRING", "1")

	var output bytes.Buffer
	client := NewClient(Config{
		AuthURL:  authServer.URL,
		ClientID: "palbase-cli",
		Mode:     "dev",
	}, &output)

	const payload = `<script>alert(1)</script>`
	var page string
	client.OpenBrowser = func(authURL string) error {
		u, err := parseAuthURL(authURL)
		if err != nil {
			return err
		}
		resp, err := http.Get(u.Query().Get("redirect_uri") + "?" + url.Values{
			"error":             {"access_denied"},
			"error_description": {payload},
			"state":             {u.Query().Get("state")},
		}.Encode())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		page = string(raw)
		return nil
	}

	require.Error(t, client.Login(context.Background()))

	assert.NotContains(t, page, payload, "provider text must not reach the tab as live markup")
	assert.Contains(t, page, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestWriteCallbackPage(t *testing.T) {
	client := NewClient(Config{Mode: "dev"}, io.Discard)

	render := func(t *testing.T, view callbackView) string {
		t.Helper()
		rec := httptest.NewRecorder()
		client.writeCallbackPage(rec, view)
		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
		return rec.Body.String()
	}

	t.Run("success", func(t *testing.T) {
		page := render(t, callbackView{
			Status:   "Login complete",
			Headline: "You're logged in.",
			Lede:     "Your terminal has the session. You can close this tab.",
		})

		assert.Contains(t, page, "<title>Palbase CLI — Login complete</title>")
		assert.Contains(t, page, "Your terminal has the session.")
		assert.NotContains(t, page, `class="failed"`, "success must not carry the red accent")
		assert.NotContains(t, page, `class="reason"`, "success has nothing to explain")
	})

	t.Run("failure", func(t *testing.T) {
		page := render(t, failedCallback("The provider redirected without an authorization code."))

		assert.Contains(t, page, `<html lang="en" class="failed">`)
		assert.Contains(t, page, "<title>Palbase CLI — Login failed</title>")
		assert.Contains(t, page, "The provider redirected without an authorization code.")
	})

	// The footer names the credential set the CLI just filled — the one fact
	// the tab cannot infer — so it must survive on every outcome.
	t.Run("footer names the mode", func(t *testing.T) {
		for name, view := range map[string]callbackView{
			"success": {Status: "Login complete"},
			"failure": failedCallback("boom"),
		} {
			t.Run(name, func(t *testing.T) {
				assert.Contains(t, render(t, view), "<dt>mode</dt><dd>dev</dd>")
			})
		}
	})
}
