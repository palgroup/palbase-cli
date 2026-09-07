package sealedclient

// A stack that behaves like the real middleware.
//
// WHY A FAKE AND NOT A MOCK. The defect this package fixes was invisible to
// every test the CLI had, because none of them stood up anything that REFUSED.
// `oauthLinkServer` answers 200 to whatever arrives, so a plaintext GET to a
// sealed path looked exactly like a working one right up until it met a real
// stack and got 415. This fake refuses the way v2/internal/server/sealed.go
// refuses, opens envelopes the way v2/internal/sealed/middleware.go opens them,
// and seals its answers the way v2/internal/sealed/response.go seals them — so
// a CLI change that breaks the wire breaks here first.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
	"golang.org/x/crypto/chacha20poly1305"
)

type fakeStack struct {
	t   *testing.T
	srv *httptest.Server

	rootPub  ed25519.PublicKey
	rootPriv ed25519.PrivateKey
	// stackPriv signs the keyset; the fleet root vouches for its public half.
	stackPub  ed25519.PublicKey
	stackPriv ed25519.PrivateKey

	kemPub  kem.PublicKey
	kemPriv kem.PrivateKey

	stackRef string
	kid      string

	// knobs
	publishKeyset  bool
	keysetVersion  uint64
	keysetExpires  time.Time
	bindingExpires time.Time
	bindingRef     string // defaults to stackRef
	// refuseKid makes the stack answer sealed_unknown_kid until the client
	// comes back with a different one — the rotation the retry exists for.
	refuseKid       string
	forceSkew       bool
	answerPlaintext bool

	// observed
	sealedHits    int
	plaintextHits int
	lastAADHost   string
	keysetFetches int
}

func newFakeStack(t *testing.T) *fakeStack {
	t.Helper()
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	stackPub, stackPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kemPub, kemPriv, err := SuiteKEM.Scheme().GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeStack{
		t: t, rootPub: rootPub, rootPriv: rootPriv,
		stackPub: stackPub, stackPriv: stackPriv,
		kemPub: kemPub, kemPriv: kemPriv,
		stackRef: "teststack", kid: "test-key-1",
		publishKeyset: true, keysetVersion: 1,
		keysetExpires:  time.Now().Add(24 * time.Hour),
		bindingExpires: time.Now().Add(72 * time.Hour),
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeStack) rootB64() string { return base64.StdEncoding.EncodeToString(s.rootPub) }

// document builds the BoundKeyset a stack publishes — the chain the client has
// to walk. Mirrors signedKeysetDocument at v2/internal/server/sealed.go:229.
func (s *fakeStack) document() []byte {
	s.t.Helper()
	ref := s.bindingRef
	if ref == "" {
		ref = s.stackRef
	}
	binding, err := json.Marshal(Binding{
		V:          BindingVersion,
		StackRef:   ref,
		SigningKey: base64.StdEncoding.EncodeToString(s.stackPub),
		Expires:    s.bindingExpires.Unix(),
	})
	if err != nil {
		s.t.Fatal(err)
	}
	pubRaw, err := s.kemPub.MarshalBinary()
	if err != nil {
		s.t.Fatal(err)
	}
	keyset, err := json.Marshal(Keyset{
		V: KeysetDocumentVersion, Version: s.keysetVersion, Expires: s.keysetExpires.Unix(),
		Keys: []PublicKeyEntry{{Kid: s.kid, Alg: SuiteName, Pub: base64.StdEncoding.EncodeToString(pubRaw)}},
	})
	if err != nil {
		s.t.Fatal(err)
	}
	doc, err := json.Marshal(BoundKeyset{
		Binding: &SignedBinding{
			Binding: binding,
			Sig:     base64.StdEncoding.EncodeToString(ed25519.Sign(s.rootPriv, binding)),
			RootKid: FleetRootKid,
			Alg:     "ed25519",
		},
		SignedKeyset: &SignedKeyset{
			Keyset:    keyset,
			Sig:       base64.StdEncoding.EncodeToString(ed25519.Sign(s.stackPriv, keyset)),
			RootKid:   "stack",
			Algorithm: "ed25519",
		},
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return doc
}

func (s *fakeStack) writeErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": code, "error_description": code, "status": status,
	})
}

func (s *fakeStack) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == KeysetPath {
		s.keysetFetches++
		if !s.publishKeyset {
			// A stack with no fleet binding publishes nothing and says so —
			// v2/internal/server/sealed.go:264. This is the condition under
			// which sealing is NOT required of a client.
			s.writeErr(w, http.StatusNotFound, "sealed_unconfigured")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.document())
		return
	}

	sealed := IsSealedRequest(r)
	if !sealed {
		// The whole guarantee: a client that CAN seal never downgrades by
		// simply not sealing. v2/internal/server/sealed.go:433.
		if s.publishKeyset && Required(r.URL.Path) {
			s.plaintextHits++
			s.writeErr(w, http.StatusUnsupportedMediaType, "sealed_required")
			return
		}
		s.plaintextHits++
		s.handle(w, r, nil, nil)
		return
	}

	env, err := decodeEnvelopeHeaderForTest(r.Header.Get(EnvelopeHeaderName))
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, "sealed_invalid")
		return
	}
	if s.refuseKid != "" && env.Kid == s.refuseKid {
		s.writeErr(w, http.StatusBadRequest, "sealed_unknown_kid")
		return
	}
	if env.Kid != s.kid {
		s.writeErr(w, http.StatusBadRequest, "sealed_unknown_kid")
		return
	}
	if s.forceSkew {
		s.writeErr(w, http.StatusBadRequest, "sealed_skew")
		return
	}

	// aadHost(trustProxy=false) is r.Host and nothing else
	// (v2/internal/server/sealed.go:462). Recording it is how the host binding
	// gets measured rather than assumed.
	s.lastAADHost = r.Host
	pt, exporter, err := openRequestWithKey(s.kemPriv, *env, r.Host, r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, "sealed_invalid")
		return
	}
	s.sealedHits++
	if s.answerPlaintext {
		// The downgrade a client must refuse: a sealed request answered in the
		// clear. Real palsvc cannot do this once it has opened an envelope; a
		// stack that has not opened one can, and did, in v0.40.0.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	s.handle(w, r, pt, exporter)
}

// handle is the "application": it answers what the test asked for, and seals
// the answer whenever the request arrived sealed — SealResponses' rule at
// v2/internal/sealed/response.go:145.
func (s *fakeStack) handle(w http.ResponseWriter, r *http.Request, plaintext, exporter []byte) {
	status := http.StatusOK
	if v := r.URL.Query().Get("status"); v != "" {
		status, _ = strconv.Atoi(v)
	}
	body := []byte(`{"path":"` + r.URL.Path + `","sealed":` + strconv.FormatBool(exporter != nil) +
		`,"echo":"` + string(plaintext) + `"}`)

	if exporter == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	env, err := sealResponseForTest(exporter, body, "application/json", status)
	if err != nil {
		s.t.Error(err)
		w.WriteHeader(500)
		return
	}
	raw, err := json.Marshal(env)
	if err != nil {
		s.t.Error(err)
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", ContentType)
	// The status goes out as 200 with the real one sealed inside — leaking it
	// on the response line would hand an observer the outcome of every request.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func decodeEnvelopeHeaderForTest(v string) (*Envelope, error) {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return nil, err
	}
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	if e.V != EnvelopeVersion || e.Enc == "" || e.CT == "" {
		return nil, errTestMalformed
	}
	return &e, nil
}

var errTestMalformed = &testError{"malformed envelope"}

type testError struct{ s string }

func (e *testError) Error() string { return e.s }

func openRequestWithKey(priv kem.PrivateKey, e Envelope, host, method, path, idem string) ([]byte, []byte, error) {
	enc, err := base64.StdEncoding.DecodeString(e.Enc)
	if err != nil {
		return nil, nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(e.CT)
	if err != nil {
		return nil, nil, err
	}
	receiver, err := hpke.NewSuite(SuiteKEM, SuiteKDF, SuiteAEAD).NewReceiver(priv, nil)
	if err != nil {
		return nil, nil, err
	}
	opener, err := receiver.Setup(enc)
	if err != nil {
		return nil, nil, err
	}
	pt, err := opener.Open(ct, AAD(host, method, path, e.ICT, e.TS, idem))
	if err != nil {
		return nil, nil, err
	}
	return pt, opener.Export(ExporterContext, ExporterLen), nil
}

func sealResponseForTest(exporter, plaintext []byte, ict string, status int) (*ResponseEnvelope, error) {
	aead, err := chacha20poly1305.New(exporter)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &ResponseEnvelope{
		V:   EnvelopeVersion,
		N:   base64.StdEncoding.EncodeToString(nonce),
		CT:  base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, plaintext, responseAAD(ict, status))),
		ICT: ict,
		St:  status,
	}, nil
}
