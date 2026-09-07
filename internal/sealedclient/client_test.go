package sealedclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func clientFor(t *testing.T, s *fakeStack) *Client {
	t.Helper()
	c, err := New(Config{
		BaseURL:      s.srv.URL,
		APIKey:       "pb_" + s.stackRef + "_cPUBLISHABLE",
		SelfHostRoot: s.rootB64(),
		HTTPClient:   s.srv.Client(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func get(t *testing.T, c *Client, base, path string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// THE DEFECT, as a test. `/auth/oauth/config` is the read `palbase link
// --platform ios` makes, and against a stack that publishes a keyset a
// plaintext GET there is 415 `sealed_required`.
func TestASealedPathIsSealedAndItsAnswerIsOpened(t *testing.T) {
	s := newFakeStack(t)

	plain, err := s.srv.Client().Get(s.srv.URL + "/auth/oauth/config")
	if err != nil {
		t.Fatal(err)
	}
	if plain.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("the fake stack does not model the real refusal: got %d, want 415", plain.StatusCode)
	}
	_ = plain.Body.Close()

	res, err := get(t, clientFor(t, s), s.srv.URL, "/auth/oauth/config?application_key=consumer")
	if err != nil {
		t.Fatalf("sealed request: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if got := body(t, res); !strings.Contains(got, `"sealed":true`) {
		t.Fatalf("the handler did not see a sealed request: %s", got)
	}
	if s.sealedHits != 1 {
		t.Fatalf("stack saw %d sealed requests, want 1", s.sealedHits)
	}
	if s.plaintextHits != 1 {
		t.Fatalf("stack saw %d plaintext requests, want only the one this test sent itself", s.plaintextHits)
	}
	// The inner content type is restored, so a caller that reads Content-Type
	// sees what the handler produced, not the envelope's media type.
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, want the inner type", ct)
	}
}

// The AAD host must be what the server reads — r.Host, which is the Host header
// Go puts on the wire from req.URL.Host. Getting this wrong makes every
// httptest pass and every live request fail, so it is measured here rather than
// reasoned about: the fake stack records the r.Host it opened under, and the
// open only succeeded because the two agreed.
func TestTheAADHostIsTheHostTheStackSees(t *testing.T) {
	s := newFakeStack(t)
	if _, err := get(t, clientFor(t, s), s.srv.URL, "/auth/user"); err != nil {
		t.Fatalf("sealed request: %v", err)
	}
	want := strings.TrimPrefix(s.srv.URL, "http://")
	if s.lastAADHost != want {
		t.Fatalf("the stack opened under host %q, the address was %q", s.lastAADHost, want)
	}
}

// A path the server does not mark sealed-required is not sealed. The rule lives
// in one place and both directions come out of it.
func TestAnUnsealedPathIsNotSealed(t *testing.T) {
	s := newFakeStack(t)
	res, err := get(t, clientFor(t, s), s.srv.URL, "/v1/management/auth/social-auth")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, res); !strings.Contains(got, `"sealed":false`) {
		t.Fatalf("a management path was sealed: %s", got)
	}
	if s.keysetFetches != 0 {
		t.Fatalf("a path that needs no seal fetched the keyset %d times", s.keysetFetches)
	}
}

// A stack that publishes NO keyset does not require sealing — that is the
// server's own condition (`len(cfg.signedKeyset) > 0`,
// v2/internal/server/sealed.go:433). Refusing to talk to it would break every
// stack that predates the chain, which is the failure this whole change exists
// to end rather than to reproduce.
func TestAStackWithNoKeysetIsTalkedToInTheClear(t *testing.T) {
	s := newFakeStack(t)
	s.publishKeyset = false
	res, err := get(t, clientFor(t, s), s.srv.URL, "/auth/oauth/config")
	if err != nil {
		t.Fatalf("plaintext fallback: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if got := body(t, res); !strings.Contains(got, `"sealed":false`) {
		t.Fatalf("unexpected body %s", got)
	}
}

// The substitution attack the signed keyset exists to catch: the stack answers
// with a document signed by a root we do not trust. Nothing may be sent.
func TestAKeysetFromAnotherRootIsRefusedAndNothingIsSent(t *testing.T) {
	s := newFakeStack(t)
	other := newFakeStack(t)
	c, err := New(Config{
		BaseURL: s.srv.URL, APIKey: "pb_" + s.stackRef + "_cKEY",
		SelfHostRoot: other.rootB64(), HTTPClient: s.srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, c, s.srv.URL, "/auth/oauth/config"); !errors.Is(err, ErrBindingSignature) {
		t.Fatalf("error %v, want a binding signature refusal", err)
	}
	if s.sealedHits != 0 || s.plaintextHits != 0 {
		t.Fatal("a request was sent to a stack whose keyset did not verify")
	}
}

// A valid, fleet-signed binding FOR ANOTHER STACK is the replay the subject
// check exists for.
func TestABindingAboutAnotherStackIsRefused(t *testing.T) {
	s := newFakeStack(t)
	s.bindingRef = "someone-elses-stack"
	if _, err := get(t, clientFor(t, s), s.srv.URL, "/auth/oauth/config"); !errors.Is(err, ErrBindingSubject) {
		t.Fatalf("error %v, want a subject refusal", err)
	}
}

// An expired binding, and an expired keyset: freeze protection on both links of
// the chain.
func TestAFrozenChainIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeStack)
		want error
	}{
		{"binding", func(s *fakeStack) { s.bindingExpires = time.Now().Add(-time.Hour) }, ErrBindingExpired},
		{"keyset", func(s *fakeStack) { s.keysetExpires = time.Now().Add(-time.Hour) }, ErrKeysetExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStack(t)
			tc.set(s)
			if _, err := get(t, clientFor(t, s), s.srv.URL, "/auth/oauth/config"); !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
		})
	}
}

// sealed_unknown_kid is the ONE refusal a client acts on: refetch the keyset
// and retry ONCE. The rotation it covers is the stack having moved to a key
// the cached document does not name.
func TestUnknownKidRefetchesTheKeysetAndRetriesOnce(t *testing.T) {
	s := newFakeStack(t)
	c := clientFor(t, s)

	// Prime the cache with the key the stack is about to retire.
	if _, err := get(t, c, s.srv.URL, "/auth/user"); err != nil {
		t.Fatal(err)
	}
	if s.keysetFetches != 1 {
		t.Fatalf("keyset fetched %d times for the first request", s.keysetFetches)
	}

	// Rotate: a new key under a new kid, a newer document version.
	s.refuseKid = s.kid
	s.kid = "test-key-2"
	s.keysetVersion = 2

	res, err := get(t, c, s.srv.URL, "/auth/user")
	if err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d after the retry", res.StatusCode)
	}
	if s.keysetFetches != 2 {
		t.Fatalf("keyset fetched %d times, want exactly one refetch", s.keysetFetches)
	}
}

// ...and it retries ONCE, not forever. A stack that answers unknown_kid to
// whatever it is handed must end in a named refusal rather than a loop.
func TestUnknownKidGivesUpAfterOneRetry(t *testing.T) {
	s := newFakeStack(t)
	s.refuseKid = s.kid
	_, err := get(t, clientFor(t, s), s.srv.URL, "/auth/user")
	var se *StackError
	if !errors.As(err, &se) || se.Code != "sealed_unknown_kid" {
		t.Fatalf("error %v, want a named sealed_unknown_kid refusal", err)
	}
	if s.keysetFetches != 2 {
		t.Fatalf("keyset fetched %d times — the retry has no cooldown", s.keysetFetches)
	}
}

// A clock problem must SAY it is a clock problem. `sealed_skew` is the one
// refusal an operator can act on directly, and "HTTP 400 sealed_skew" is not
// something anybody acts on.
func TestSkewIsReportedAsAClockProblem(t *testing.T) {
	s := newFakeStack(t)
	s.forceSkew = true
	_, err := get(t, clientFor(t, s), s.srv.URL, "/auth/user")
	var se *StackError
	if !errors.As(err, &se) || se.Code != "sealed_skew" {
		t.Fatalf("error %v, want sealed_skew", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "clock") {
		t.Fatalf("the message never mentions the clock: %s", err)
	}
}

// The status is sealed INSIDE the envelope and the wire carries 200. A client
// that read the wire status would report every refusal as a success.
func TestTheStatusComesFromInsideTheEnvelope(t *testing.T) {
	s := newFakeStack(t)
	res, err := get(t, clientFor(t, s), s.srv.URL, "/auth/oauth/config?status=403")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 403 {
		t.Fatalf("status %d, want the sealed 403", res.StatusCode)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body(t, res)), &doc); err != nil {
		t.Fatalf("the sealed body did not survive: %v", err)
	}
}

// "Sealed request implies sealed response". A stack that opened our envelope
// and then answered 200 in the clear has not honoured that, and accepting the
// answer would hide exactly the state v0.40.0 shipped: palsvc answering 200 to
// a body it never read.
func TestAPlaintextSuccessToASealedRequestIsRefused(t *testing.T) {
	s := newFakeStack(t)
	s.answerPlaintext = true
	_, err := get(t, clientFor(t, s), s.srv.URL, "/auth/user")
	if err == nil || !strings.Contains(err.Error(), "answered in the clear") {
		t.Fatalf("error %v, want a downgrade refusal", err)
	}
}

// A self-host root that cannot be parsed is an ERROR, never a quiet fall back
// to the fleet root: an operator who configured their own root and silently got
// the fleet's would be told a chain verified that never did.
func TestAnUnusableSelfHostRootIsNeverTheFleetRoot(t *testing.T) {
	_, err := New(Config{BaseURL: "https://example.test", APIKey: "pb_env_cKEY", SelfHostRoot: "not base64!!"})
	if !errors.Is(err, ErrRootUnusable) {
		t.Fatalf("error %v, want ErrRootUnusable", err)
	}
}

// The fleet root is a fact about the fleet, not a value to be edited: it is the
// same constant the iOS binding ships, and a client whose root drifts from the
// fleet's verifies nothing.
func TestTheFleetRootIsTheOneTheFleetSigns(t *testing.T) {
	roots, selfHosted, err := rootsFor("")
	if err != nil {
		t.Fatal(err)
	}
	if selfHosted {
		t.Fatal("an empty self-host root produced a self-hosted client")
	}
	if len(roots) != 1 || len(roots[FleetRootKid]) != 32 {
		t.Fatalf("roots %v", roots)
	}
	// A self-host root REPLACES it under the same kid, so there is no map in
	// which both are reachable.
	self, selfHosted, err := rootsFor("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil || !selfHosted {
		t.Fatalf("self host root: %v", err)
	}
	if len(self) != 1 {
		t.Fatalf("a self-hosted client trusts %d roots", len(self))
	}
	if string(self[SelfHostRootKid]) == string(roots[FleetRootKid]) {
		t.Fatal("the self-host root resolved to the fleet root")
	}
}

// Which stack we believe we are talking to. Mirrors the iOS binding's
// expectedStackRef; a disagreement there means one of the two refuses documents
// the other accepts.
func TestExpectedStackRefMirrorsTheIOSBinding(t *testing.T) {
	for _, tc := range []struct {
		name, key, url string
		selfHosted     bool
		want           string
		wantErr        bool
	}{
		{name: "a concrete ref in the key wins", key: "pb_penny7_cKEY", url: "https://anything.example", want: "penny7"},
		{name: "hosted key takes the ref from the managed host", key: "pb_project_cKEY",
			url: "https://centauri.palbase.studio", want: "centauri"},
		{name: "a managed subzone routes on the first label", key: "pb_project_cKEY",
			url: "https://todoapp.dev.palbase.studio", want: "todoapp"},
		{name: "a lookalike suffix carries no ref", key: "pb_project_cKEY",
			url: "https://centauri.palbase.studio.evil.test", wantErr: true},
		{name: "self host keeps the key's own subject", key: "pb_project_cKEY",
			url: "https://stack.example", selfHosted: true, want: "project"},
		{name: "a key that is not a publishable key identifies nothing", key: "operator-token",
			url: "https://centauri.palbase.studio", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expectedStackRef(tc.key, tc.url, tc.selfHosted)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want a refusal", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The rule, mirrored from v2/internal/server/sealed.go:361-380. If this table
// and the server's switch ever disagree, the CLI either sends something the
// stack refuses or seals something the stack cannot open.
func TestRequiredMirrorsTheServersRule(t *testing.T) {
	for path, want := range map[string]bool{
		"/auth/oauth/config":              true,
		"/auth/login":                     true,
		"/auth/user":                      true,
		"/auth/":                          true,
		"/auth/oauth/callback/google":     false,
		"/auth/oauth/callback/apple":      false,
		"/auth/oauth/callback/microsoft":  false,
		"/auth/oauth/callback/github":     false,
		"/auth/oauth/callback/gitlab":     true,
		"/v1/management/auth/social-auth": false,
		"/v1/management/keys":             false,
		"/palbase-sealed-keys.json":       false,
		"/authentication/not-a-prefix":    false,
	} {
		if got := Required(path); got != want {
			t.Errorf("Required(%q) = %v, want %v", path, got, want)
		}
	}
	// The ratchet: the prefix list only ever grows, so a change that shortens
	// it has to be seen by whoever makes it.
	if len(RequiredPrefixes) != 1 || RequiredPrefixes[0] != "/auth/" {
		t.Fatalf("the prefix list changed to %v — the server's list is the authority and this mirror must move with it, never behind it", RequiredPrefixes)
	}
	if len(ExemptPaths) != 4 {
		t.Fatalf("the exemptions changed to %v", ExemptPaths)
	}
}
