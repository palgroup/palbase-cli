package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testToken(t *testing.T, sub, email string) string {
	t.Helper()
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256"}`))
	claims, err := json.Marshal(map[string]string{"sub": sub, "email": email})
	if err != nil {
		t.Fatal(err)
	}
	return head + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

// A control plane that publishes no anon key cannot be signed in to, and saying
// so here beats a 401 the person cannot explain.
func TestBootstrapRefusesAnEmptyAnonKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Bootstrap{Issuer: "https://plane.test"})
	}))
	t.Cleanup(srv.Close)

	if _, err := NewCloudClient(srv.URL).Bootstrap(context.Background()); err == nil {
		t.Fatal("an empty anon key was accepted")
	}
}

// BeginAuthorization sends what the authorization server needs and reads the
// id out of the redirect WITHOUT following it — following would land on a
// login page and lose the only thing this leg exists to produce.
func TestBeginAuthorizationReadsTheIdFromTheRedirect(t *testing.T) {
	var got url.Values
	var apikey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, apikey = r.URL.Query(), r.Header.Get("apikey")
		http.Redirect(w, r, "/auth/login?auth_request_id=ar_42", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	id, err := NewCloudClient(srv.URL).BeginAuthorization(context.Background(),
		Bootstrap{AnonKey: "pb_project_cANON"}, "http://localhost:54321/callback", "CHAL", "STATE")
	if err != nil {
		t.Fatalf("BeginAuthorization: %v", err)
	}
	if id != "ar_42" {
		t.Fatalf("id: %q", id)
	}
	if apikey == "" {
		t.Fatal("the anon apikey was not sent — /oauth/* refuses without it")
	}
	// PKCE IS NOT OPTIONAL. A missing challenge turns this into a flow where
	// anyone holding the code can redeem it.
	if got.Get("code_challenge") != "CHAL" || got.Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE not sent: %v", got)
	}
	if got.Get("state") != "STATE" {
		t.Fatal("state not sent — the callback could not be told apart from anyone else's")
	}
	if got.Get("redirect_uri") != "http://localhost:54321/callback" {
		t.Fatalf("redirect_uri: %q", got.Get("redirect_uri"))
	}
}

// A plane that answers the authorize leg with anything but a redirect has not
// started a sign-in, and pretending otherwise strands the caller on a wait.
func TestBeginAuthorizationRefusesANonRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"unable to retrieve client by id"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := NewCloudClient(srv.URL).BeginAuthorization(context.Background(),
		Bootstrap{AnonKey: "k"}, "http://localhost:54321/callback", "c", "s")
	if err == nil {
		t.Fatal("a refused authorization reported success")
	}
	if !strings.Contains(err.Error(), "unable to retrieve client by id") {
		t.Fatalf("the plane's reason was dropped: %v", err)
	}
}

// The verifier is the whole point: it proves the process redeeming the code is
// the one that asked for it.
func TestExchangeCodeSendsTheVerifier(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": testToken(t, "usr_1", "who@plane.test"),
			"expires_in":   3600,
		})
	}))
	t.Cleanup(srv.Close)

	creds, err := NewCloudClient(srv.URL).ExchangeCode(context.Background(),
		Bootstrap{AnonKey: "k"}, "CODE", "http://localhost:54321/callback", "VERIFIER")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if creds.User.ID != "usr_1" {
		t.Fatalf("identity not read from the token: %+v", creds.User)
	}
	if creds.IsExpired() {
		t.Fatal("a fresh session is already expired")
	}
	if form.Get("code_verifier") != "VERIFIER" || form.Get("grant_type") != "authorization_code" {
		t.Fatalf("exchange body: %v", form)
	}
	if form.Get("redirect_uri") != "http://localhost:54321/callback" {
		t.Fatal("redirect_uri must match the one the code was issued for")
	}
}

// A session with no expires_in must not be born expired: every later call would
// refuse a token that is in fact valid.
func TestSessionWithoutExpiryIsNotBornExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"` + testToken(t, "usr_x", "x@plane.test") + `"}`))
	}))
	t.Cleanup(srv.Close)

	creds, err := NewCloudClient(srv.URL).ExchangeCode(context.Background(),
		Bootstrap{AnonKey: "k"}, "c", "http://localhost:54321/callback", "v")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if creds.IsExpired() {
		t.Fatal("a session with no expires_in was stored as already expired")
	}
}

// The S256 challenge must be the digest of the verifier, or the server rejects
// the exchange for a reason no message here would explain.
func TestPKCEChallengeIsTheDigestOfTheVerifier(t *testing.T) {
	verifier, challenge, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Fatalf("challenge %q is not S256(verifier)", challenge)
	}
	// And two runs must not agree: a fixed verifier is no verifier.
	v2, _, _ := newPKCE()
	if v2 == verifier {
		t.Fatal("the verifier is not random")
	}
}
