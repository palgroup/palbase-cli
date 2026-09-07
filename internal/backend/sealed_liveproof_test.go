package backend

// Live proof for the CLI's sealing client.
//
// WHY THIS EXISTS ON TOP OF THE VECTORS. The cross-binding vectors prove this
// implementation agrees with a RECORDING of the server. They cannot prove it
// agrees with a stack that is running, and the two differ in exactly the place
// that is hardest to see from here: the AAD binds the host, and the host the
// server binds is whatever `aadHost` reads off the live request
// (v2/internal/server/sealed.go:462). Get that wrong and every fixture passes
// while every real request comes back 400. So this sends a real envelope to a
// real tenant.
//
// Skipped unless PALBASE_SEALED_LIVE_URL names a stack, the same way
// v2/internal/sealed/liveproof_test.go is gated on SEALED_LIVE_PROOF — an
// ordinary `go test -short ./...` must never need the network.
//
// The credential is resolved the way the PRODUCT resolves it (projectKeys, over
// the credential store), never hardcoded, and no key value is ever logged.

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/sealedclient"
)

const liveProofEnv = "PALBASE_SEALED_LIVE_URL"

func liveTarget(t *testing.T) Target {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(liveProofEnv))
	if raw == "" {
		t.Skipf("set %s=https://<ref>.palbase.studio to prove this against a running stack", liveProofEnv)
	}
	return Target{URL: strings.TrimRight(raw, "/")}
}

// liveOAuthQuery names an application key that does not exist. The proof is
// about the ENVELOPE, not about the configuration: a 400 or a 404 saying the
// application key is unknown means a handler ran, which means the seal was
// opened. Only 415 means it never got that far.
const liveOAuthQuery = "?application_key=palbase-live-proof&platform=ios&variant=release"

// wireProbe records what actually crossed the wire.
//
// By the time Do returns, a sealed answer and a plaintext one look the same —
// the whole point of the client is that the caller cannot tell. The facts this
// proof needs live on the wire and nowhere else, so they are read there.
type wireProbe struct {
	base        http.RoundTripper
	sentSealed  bool
	gotSealed   bool
	outerStatus int
}

func (p *wireProbe) RoundTrip(r *http.Request) (*http.Response, error) {
	if sealedclient.Required(r.URL.Path) {
		p.sentSealed = sealedclient.IsSealedRequest(r)
	}
	res, err := p.base.RoundTrip(r)
	if err != nil || !sealedclient.Required(r.URL.Path) {
		return res, err
	}
	p.outerStatus = res.StatusCode
	ct := res.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	p.gotSealed = strings.EqualFold(strings.TrimSpace(ct), sealedclient.ContentType)
	return res, nil
}

// probedClient is the CLI's own stack client — same TLS policy, same outbound
// guard — with the wire recorder in front of it.
func probedClient(target Target) (*http.Client, *wireProbe) {
	client := *HTTPClient(target)
	probe := &wireProbe{base: client.Transport}
	client.Transport = probe
	return &client, probe
}

// THE USER'S DEFECT, END TO END, AGAINST A REAL TENANT.
func TestLiveTheSealedAuthReadIsAccepted(t *testing.T) {
	target := liveTarget(t)
	ctx := context.Background()

	// Where `palbase link` gets both: the key an app ships, and the root a
	// self-hosted stack's chain hangs from (omitted by a managed one, which
	// then verifies against the compiled-in fleet root).
	publishable, sealedRoot, err := projectKeys(ctx, target)
	if err != nil {
		t.Fatalf("projectKeys: %v", err)
	}
	t.Logf("CREDENTIAL publishable=%d bytes  sealed_root=%s  target=%s",
		len(publishable), rootPresence(sealedRoot), target.URL)

	// 1. THE SYMPTOM. A plaintext read of the path `palbase link` needs. The
	//    CLI's own client would refuse to send this — that is the guard doing
	//    its job — so the probe is a bare client, which is what the CLI WAS
	//    before this change.
	plainReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		target.URL+"/auth/oauth/config"+liveOAuthQuery, nil)
	if err != nil {
		t.Fatal(err)
	}
	Credentials{Kind: KindKey, Value: publishable}.Apply(plainReq)
	plainRes, err := http.DefaultClient.Do(plainReq)
	if err != nil {
		t.Fatalf("plaintext probe: %v", err)
	}
	plainBody, _ := io.ReadAll(io.LimitReader(plainRes.Body, 4096))
	_ = plainRes.Body.Close()
	t.Logf("PLAINTEXT  status=%d body=%s", plainRes.StatusCode, strings.TrimSpace(string(plainBody)))
	stackRequiresSealing := plainRes.StatusCode == http.StatusUnsupportedMediaType
	if !stackRequiresSealing {
		t.Logf("NOTE       this stack does not refuse plaintext here (status %d); "+
			"the sealed assertion below is still the one that matters", plainRes.StatusCode)
	}

	// 2. THE FIX. The same read through the sealing client: it fetches the
	//    published keyset, walks the fleet binding chain, seals, and opens the
	//    answer. A verification failure fails HERE, before anything is sent.
	client, probe := probedClient(target)
	sealer, err := sealedclient.New(sealedclient.Config{
		BaseURL: target.URL, APIKey: publishable, SelfHostRoot: sealedRoot, HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("sealing client: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		target.URL+"/auth/oauth/config"+liveOAuthQuery, nil)
	if err != nil {
		t.Fatal(err)
	}
	Credentials{Kind: KindKey, Value: publishable}.Apply(req)
	req.Header.Set("Palbase-Auth-Contract", "1")

	res, err := sealer.Do(req)
	if err != nil {
		t.Fatalf("the sealed read failed: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	_ = res.Body.Close()

	t.Logf("SEALED     sent-sealed=%v wire-status=%d wire-sealed=%v",
		probe.sentSealed, probe.outerStatus, probe.gotSealed)
	t.Logf("OPENED     status=%d content-type=%s body=%s",
		res.StatusCode, res.Header.Get("Content-Type"), strings.TrimSpace(string(body)))

	if !probe.sentSealed {
		t.Fatal("the request left this process unsealed — the client did not seal a path the server requires sealing on")
	}
	// THE ASSERTION THE WHOLE FILE IS FOR. 415 means the stack refused the
	// request as plaintext, which is the user's original failure. Any other
	// status — 400, 404, whatever the handler thinks of an application key that
	// does not exist — means the envelope was OPENED and a handler ran, which
	// is precisely what had to be proven.
	if res.StatusCode == http.StatusUnsupportedMediaType || probe.outerStatus == http.StatusUnsupportedMediaType {
		t.Fatalf("the stack still answered 415 sealed_required — the seal was NOT accepted:\n%s", body)
	}
	if strings.Contains(string(body), "sealed_required") {
		t.Fatalf("the answer still names sealed_required:\n%s", body)
	}

	// 3. "Sealed request implies sealed response." When the stack sealed its
	//    answer, the client opened it — the wire carried 200 with the real
	//    status inside, so a differing pair is the proof it was opened.
	//
	//    A sealed answer is also the only POSITIVE evidence that the envelope
	//    was opened: the exporter the response is sealed under exists solely on
	//    the server's request context after a successful Open. So on a stack
	//    that refuses plaintext, a plaintext answer leaves the open unproven and
	//    breaks the guarantee at the same time — not something to log and pass.
	if probe.gotSealed {
		if probe.outerStatus != http.StatusOK {
			t.Errorf("a sealed answer must ride on a 200, wire said %d", probe.outerStatus)
		}
		t.Logf("RESPONSE   sealed on the wire, opened here: inner status %d — the envelope was opened", res.StatusCode)
	} else if stackRequiresSealing {
		t.Fatalf("this stack refuses plaintext yet answered our sealed request in the clear (%d): "+
			"\"sealed request implies sealed response\" is broken, and the open is unproven", res.StatusCode)
	} else {
		t.Logf("RESPONSE   plaintext (%d) — this stack seals nothing, so the open is not observable here", res.StatusCode)
	}
}

// The chain is REAL, live: the same read against the same tenant with a root
// the fleet never signed must be refused before a byte is sent. Without this,
// a client that skipped verification entirely would pass the test above.
func TestLiveAnUnvouchedRootRefusesBeforeSending(t *testing.T) {
	target := liveTarget(t)
	ctx := context.Background()

	publishable, _, err := projectKeys(ctx, target)
	if err != nil {
		t.Fatalf("projectKeys: %v", err)
	}
	client, probe := probedClient(target)
	// A well-formed Ed25519 public key that signed nothing.
	stranger := base64.StdEncoding.EncodeToString(make([]byte, 32))
	sealer, err := sealedclient.New(sealedclient.Config{
		BaseURL: target.URL, APIKey: publishable, SelfHostRoot: stranger, HTTPClient: client,
	})
	if err != nil {
		t.Fatalf("sealing client: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		target.URL+"/auth/oauth/config"+liveOAuthQuery, nil)
	if err != nil {
		t.Fatal(err)
	}
	Credentials{Kind: KindKey, Value: publishable}.Apply(req)

	res, err := sealer.Do(req)
	if err == nil {
		_ = res.Body.Close()
		t.Fatal("a keyset signed by nobody we trust was accepted")
	}
	t.Logf("STRANGER   refused: %v", err)
	if probe.outerStatus != 0 {
		t.Fatalf("a request was sent (%d) despite the keyset not verifying", probe.outerStatus)
	}
}

func rootPresence(root string) string {
	if strings.TrimSpace(root) == "" {
		return "absent (fleet root)"
	}
	return "present (self-hosted)"
}
