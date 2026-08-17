package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What `palbase link <url>` must get right.
//
// It used to be handed both of the stack's keys — through --env-file, or pasted
// — and the dangerous half of that is gone with them: the SECRET key is no
// longer part of linking at all. What remains is the publishable one, which the
// stack serves itself, and which is written into a file that ships inside an
// app. So the assertions here are: ask the stack rather than the operator, put
// the publishable key in the app, and never put anything else there.

// inScratchCheckout runs the test inside an empty directory with its own HOME,
// the way a person runs `link` from a fresh clone — and so one test's session
// never reaches another's.
func inScratchCheckout(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
}

// stackServing answers as a project: the public document, the AUTHENTICATED key
// route, the contract, and whatever else a test wires up.
//
// The key route refuses without a bearer, exactly as the real one does — a
// harness that handed it out unconditionally would let a test pass that a real
// project fails.
func stackServing(t *testing.T, anonKey string, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wellKnownPath:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"hosting":"project","sdk_version":"18.0.0"}`))
			return
		case "/v1/management/keys":
			if r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"publishable":"` + anonKey + `"}`))
			return
		case "/v1/management/openapi":
			if r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.2.0","paths":{}}`))
			return
		}
		if extra != nil {
			extra(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// linkedAs gives this scratch checkout a credential for one target, the way
// `palbase start` or `palbase login` would.
func linkedAs(t *testing.T, url, token string) {
	t.Helper()
	t.Setenv(AccessTokenEnv, "")
	if err := StoreCredential(url, Credentials{Value: token, Kind: KindPerson}); err != nil {
		t.Fatal(err)
	}
}

func TestTheProjectSaysWhatItIsAndNotWhatOpensIt(t *testing.T) {
	// The public document says WHAT answered. It used to hand out the
	// publishable key as well, which meant knowing an address was enough to hold
	// a working client credential — so the key moved behind an authenticated
	// route and this document kept the part that is safe to tell a stranger.
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)

	described, err := describeStack(context.Background(), srv.URL, false)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if described.Hosting != "project" {
		t.Errorf("hosting came back as %q", described.Hosting)
	}
}

func TestSomethingThatIsNotAStackIsSaidToBeOne(t *testing.T) {
	// A mistyped address, or a web server that is not this product. Saying so by
	// name beats the errors that follow from carrying on: a 404 while fetching a
	// contract reads like "nothing is deployed", which is a different problem
	// with a different fix.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := describeStack(context.Background(), srv.URL, false)
	if err == nil {
		t.Fatal("a server that is not a stack was accepted as one")
	}
	if !strings.Contains(err.Error(), "does not look like a Palbase stack") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

func TestAProjectWithNoPublishableKeyIsRefused(t *testing.T) {
	// Its clients could not authenticate at all, so writing that config would
	// produce an app that builds and fails at its first request.
	inScratchCheckout(t)
	srv := stackServing(t, "", nil)
	linkedAs(t, srv.URL, "a-credential")

	_, err := projectPublishableKey(context.Background(), Target{URL: srv.URL})
	if err == nil {
		t.Fatal("a project with no publishable key was linked")
	}
	if !strings.Contains(err.Error(), "publishable key") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

func TestTheAppConfigCarriesThePUBLISHABLEKey(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)
	linkedAs(t, srv.URL, "a-credential")

	opts := linkOpts{url: srv.URL, platforms: []string{"ios"}}
	if err := runLink(context.Background(), opts, &strings.Builder{}); err != nil {
		t.Fatalf("link: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"))
	if err != nil {
		t.Fatalf("no slot file: %v", err)
	}
	var entry pullSpecConfigEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	// THE assertion. This file is committed and ships inside the app, so the key
	// in it must be the one that is safe to ship.
	if entry.APIKey != "pb_project_cPUBLISHABLE" {
		t.Errorf("the app's config carries %q", entry.APIKey)
	}
	if entry.BaseURL != srv.URL {
		t.Errorf("base_url is %q", entry.BaseURL)
	}
	if entry.AppID != projectAppID {
		t.Errorf("the app slot is %q, want %q", entry.AppID, projectAppID)
	}
	// And the file carries NO second copy of the project's identity. It used to
	// write one beside the key, and on 2026-08-16 the two disagreed — "project"
	// in the field, "project" inside the key — so the web generator refused the
	// config outright and the iOS realtime client joined a channel nobody
	// published to. The key is the identity; a copy is only a way to be wrong.
	if strings.Contains(string(raw), "environment_ref") {
		t.Errorf("the app's config still carries a second copy of the project identity:\n%s", raw)
	}
}

func TestLinkingWithoutACredentialWritesNOTHING(t *testing.T) {
	// This is the reverse of what it used to do, and the reversal is the point.
	// Both halves of a link now come from the project over authenticated routes
	// — the key as well as the contract — so a run with no credential has
	// nothing true to write, and writing "most of it" would leave a checkout
	// that looks linked and cannot work.
	inScratchCheckout(t)
	t.Setenv(AccessTokenEnv, "")
	srv := stackServing(t, "pb_project_cPUBLISHABLE", nil)

	var out strings.Builder
	err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out)
	if err == nil {
		t.Fatal("linking succeeded with no credential")
	}
	if _, statErr := os.Stat(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json")); statErr == nil {
		t.Error("a half-linked checkout was written")
	}
	// And the refusal names both ways to fix it.
	for _, want := range []string{"palbase start", "palbase login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestTheContractIsFetchedWithTheSessionAndNoKey(t *testing.T) {
	// THE rule of the management surface: an apikey WINS over a bearer token, so
	// a request carrying both is a request whose person is invisible — the stack
	// mints the identity from the key and answers 403. A client that helpfully
	// attaches the publishable key here would be refused for a reason nobody
	// could see in their own code.
	var sawKey, sawBearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("apikey")
		sawBearer = r.Header.Get("Authorization")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"openapi":"3.2.0","paths":{}}`))
	}))
	defer srv.Close()

	body, err := fetchStackSpec(context.Background(),
		Target{URL: srv.URL}, Credentials{Value: "the-session-token", Kind: KindPerson})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if sawKey != "" {
		t.Errorf("the contract was fetched with an apikey (%q) — the person becomes invisible", sawKey)
	}
	if sawBearer != "Bearer the-session-token" {
		t.Errorf("authorization was %q", sawBearer)
	}
	if !strings.Contains(string(body), "openapi") {
		t.Errorf("body came back as %q", body)
	}
}

func TestAnExpiredSessionIsReportedAsNotSignedIn(t *testing.T) {
	// So `link --email` can sign in and retry. A stack rebuilt since the last
	// link leaves a token behind that verifies as nothing, and that is the case
	// most in need of a fresh sign-in — treating "a token exists" as "signed in"
	// is what made it the one case that could not recover.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := fetchStackSpec(context.Background(), Target{URL: srv.URL}, Credentials{Value: "stale", Kind: KindPerson})
	if !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("an expired session came back as %v", err)
	}
}

func TestNothingDeployedYetIsAStateNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"spec_unavailable"}`))
	}))
	defer srv.Close()

	_, err := fetchStackSpec(context.Background(), Target{URL: srv.URL}, Credentials{Value: "token", Kind: KindPerson})
	if err == nil {
		t.Fatal("an empty stack was treated as a contract")
	}
	// A stack that is up with nothing pushed to it is the normal first state, so
	// the message says what to do next rather than what went wrong.
	if !strings.Contains(err.Error(), "push a backend") {
		t.Errorf("the message does not say what to do: %v", err)
	}
}
