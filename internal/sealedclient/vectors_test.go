package sealedclient

// The cross-binding wire vectors, copied byte for byte from the server that
// produced them (v2/internal/sealed/testdata/wire_vectors.json, emitted by
// v2/internal/sealed/vectors.go).
//
// WHY THE COPY AND NOT A FIXTURE OF OUR OWN. Two independent implementations of
// one wire format can each be internally consistent and still disagree. A CLI
// tested against blobs the CLI produced proves only that the CLI agrees with
// itself; the first live request is then where the disagreement surfaces, as a
// 400 with nothing to read. These bytes came from the process that will open
// ours, so opening them here is the only check that measures the pair.
//
// The DERIVATION vectors in the file are deliberately not exercised: they pin
// how a SERVER derives its keyring from its stack root, and a client never
// derives a key — it fetches a signed keyset and seals to what the fleet
// vouched for. Implementing derivation here to satisfy a vector would be
// shipping server code in a client.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
)

const vectorPath = "testdata/wire_vectors.json"

type vectorFile struct {
	Suite   string `json:"suite"`
	Version int    `json:"version"`
	Vectors []struct {
		Name              string   `json:"name"`
		Host              string   `json:"host"`
		Method            string   `json:"method"`
		Path              string   `json:"path"`
		IdempotencyKey    string   `json:"idempotencyKey"`
		RecipientPrivate  string   `json:"recipientPrivate"`
		RecipientPublic   string   `json:"recipientPublic"`
		Envelope          Envelope `json:"envelope"`
		EnvelopeHeader    string   `json:"envelopeHeader"`
		ExpectedPlaintext string   `json:"expectedPlaintext"`
		ExpectedExporter  string   `json:"expectedExporter"`
	} `json:"vectors"`
	Keysets []struct {
		Name            string `json:"name"`
		RootPublic      string `json:"rootPublic"`
		SignedB64       string `json:"signedB64"`
		ShouldVerify    bool   `json:"shouldVerify"`
		Reason          string `json:"reason"`
		LastSeenVersion uint64 `json:"lastSeenVersion"`
		NowUnix         int64  `json:"nowUnix"`
	} `json:"keysets"`
	Responses []struct {
		Name              string           `json:"name"`
		ExporterB64       string           `json:"exporterB64"`
		Envelope          ResponseEnvelope `json:"envelope"`
		ExpectedPlaintext string           `json:"expectedPlaintext"`
		ExpectedStatus    int              `json:"expectedStatus"`
	} `json:"responses"`
}

func readVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile(vectorPath)
	if err != nil {
		// FATAL, not a skip. The file is committed; the only way this branch is
		// reached is the golden going missing, and a cross-binding lock that
		// reports green because it could not find what it locks is the silent
		// pass it exists to prevent.
		t.Fatalf("the committed vector file is missing (%v)", err)
	}
	var f vectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	if f.Suite != SuiteName {
		t.Fatalf("vector file is for suite %q, this build speaks %q", f.Suite, SuiteName)
	}
	if f.Version != EnvelopeVersion {
		t.Fatalf("vector file is envelope v%d, this build speaks v%d", f.Version, EnvelopeVersion)
	}
	if len(f.Vectors) == 0 || len(f.Keysets) == 0 || len(f.Responses) == 0 {
		t.Fatal("vector file has nothing in it")
	}
	return f
}

// openRequest is the SERVER side of the request direction, and it lives in a
// test file because a client never opens a request. It exists only so the
// vectors can be consumed: an envelope opens with this AAD or it does not, and
// that is the whole cross-binding contract.
func openRequest(privB64 string, e Envelope, host, method, path, idem string) ([]byte, []byte, error) {
	raw, err := base64.StdEncoding.DecodeString(privB64)
	if err != nil {
		return nil, nil, err
	}
	priv, err := SuiteKEM.Scheme().UnmarshalBinaryPrivateKey(raw)
	if err != nil {
		return nil, nil, err
	}
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

func parsePublic(t *testing.T, b64 string) kem.PublicKey {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	pub, err := SuiteKEM.Scheme().UnmarshalBinaryPublicKey(raw)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return pub
}

// TestRecordedEnvelopesOpenWithOurAAD is the AAD contract.
//
// The server bound host, method, path, inner content type, timestamp and
// idempotency key into every one of these ciphertexts with a LENGTH-PREFIXED
// encoding (v2/internal/sealed/sealed.go:153). An implementation that joins the
// fields any other way — a separator, a different order, a dropped field —
// cannot open a single one of them.
func TestRecordedEnvelopesOpenWithOurAAD(t *testing.T) {
	f := readVectors(t)
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			pt, exporter, err := openRequest(v.RecipientPrivate, v.Envelope, v.Host, v.Method, v.Path, v.IdempotencyKey)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if got := base64.StdEncoding.EncodeToString(pt); got != v.ExpectedPlaintext {
				t.Fatalf("plaintext %q, want %q", got, v.ExpectedPlaintext)
			}
			// The exporter is the response key. A binding that agrees about the
			// plaintext and disagrees here would open every request and fail to
			// read a single answer.
			if got := base64.StdEncoding.EncodeToString(exporter); got != v.ExpectedExporter {
				t.Fatalf("exporter %q, want %q — the response direction would diverge", got, v.ExpectedExporter)
			}
		})
	}
}

// TestOurEnvelopesOpenAgainstTheRecordedKeys is the same contract in the
// direction the CLI actually runs: we SEAL, and the bytes must open under the
// recipient key the server recorded.
func TestOurEnvelopesOpenAgainstTheRecordedKeys(t *testing.T) {
	f := readVectors(t)
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			plaintext, err := base64.StdEncoding.DecodeString(v.ExpectedPlaintext)
			if err != nil {
				t.Fatalf("plaintext: %v", err)
			}
			env, exporter, err := Seal(parsePublic(t, v.RecipientPublic), v.Envelope.Kid, plaintext,
				v.Envelope.ICT, v.Host, v.Method, v.Path, v.Envelope.TS, v.IdempotencyKey)
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			if env.V != EnvelopeVersion || env.Kid != v.Envelope.Kid || env.ICT != v.Envelope.ICT || env.TS != v.Envelope.TS {
				t.Fatalf("envelope fields drifted: %+v", env)
			}
			gotPT, gotExp, err := openRequest(v.RecipientPrivate, *env, v.Host, v.Method, v.Path, v.IdempotencyKey)
			if err != nil {
				t.Fatalf("our envelope does not open under the recorded key: %v", err)
			}
			if !bytes.Equal(gotPT, plaintext) {
				t.Fatalf("round trip plaintext %q, want %q", gotPT, plaintext)
			}
			if !bytes.Equal(gotExp, exporter) {
				t.Fatal("the exporter we kept is not the one the opener derives")
			}
		})
	}
}

// TestTheHeaderCarriageIsByteIdentical pins the one part of a sealed request
// that IS reproducible: the header encoding of a given envelope.
//
// The ciphertext cannot be reproduced (a fresh encapsulation every time), but
// the header is a pure function of the envelope, so the recorded string is an
// exact expectation. This is the check that catches a header codec quietly
// becoming a second wire format for the same object — padding, standard
// base64, a re-ordered JSON encoder.
func TestTheHeaderCarriageIsByteIdentical(t *testing.T) {
	f := readVectors(t)
	seen := 0
	for _, v := range f.Vectors {
		if v.EnvelopeHeader == "" {
			continue
		}
		seen++
		t.Run(v.Name, func(t *testing.T) {
			got, err := EncodeEnvelopeHeader(&v.Envelope)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got != v.EnvelopeHeader {
				t.Fatalf("header value\n got %s\nwant %s", got, v.EnvelopeHeader)
			}
		})
	}
	if seen == 0 {
		t.Fatal("no bodyless vector carries a header — the carriage the CLI uses is untested")
	}
}

// TestRecordedResponsesOpen is the direction a client actually runs.
func TestRecordedResponsesOpen(t *testing.T) {
	f := readVectors(t)
	for _, r := range f.Responses {
		t.Run(r.Name, func(t *testing.T) {
			exporter, err := base64.StdEncoding.DecodeString(r.ExporterB64)
			if err != nil {
				t.Fatalf("exporter: %v", err)
			}
			env := r.Envelope
			pt, err := OpenResponse(exporter, &env)
			if err != nil {
				t.Fatalf("open response: %v", err)
			}
			if got := base64.StdEncoding.EncodeToString(pt); got != r.ExpectedPlaintext {
				t.Fatalf("plaintext %q, want %q", got, r.ExpectedPlaintext)
			}
			if env.St != r.ExpectedStatus {
				t.Fatalf("status %d, want %d", env.St, r.ExpectedStatus)
			}
			// The status is inside the AAD. Moving it must break the open —
			// otherwise anyone who cannot read a 403 can still present it as a
			// 200 with an empty success body.
			moved := r.Envelope
			moved.St = r.ExpectedStatus + 1
			if _, err := OpenResponse(exporter, &moved); err == nil {
				t.Fatal("a rewritten status still opened — the status is not bound")
			}
		})
	}
}

// TestRecordedKeysetsVerifyAndRefuse walks the four cases the server recorded:
// a good document, a substituted key, an expired one, and a rollback.
func TestRecordedKeysetsVerifyAndRefuse(t *testing.T) {
	f := readVectors(t)
	for _, kv := range f.Keysets {
		t.Run(kv.Name, func(t *testing.T) {
			rootRaw, err := base64.StdEncoding.DecodeString(kv.RootPublic)
			if err != nil {
				t.Fatalf("root: %v", err)
			}
			signedRaw, err := base64.StdEncoding.DecodeString(kv.SignedB64)
			if err != nil {
				t.Fatalf("signed document: %v", err)
			}
			var sk SignedKeyset
			if err := json.Unmarshal(signedRaw, &sk); err != nil {
				t.Fatalf("parse signed keyset: %v", err)
			}
			roots := TrustedRoots{"root-vector": ed25519.PublicKey(rootRaw)}
			_, err = VerifyKeyset(roots, &sk, kv.LastSeenVersion, time.Unix(kv.NowUnix, 0))
			if kv.ShouldVerify && err != nil {
				t.Fatalf("expected this keyset to verify, got %v", err)
			}
			if !kv.ShouldVerify && err == nil {
				t.Fatalf("expected refusal (%s) but it verified", kv.Reason)
			}
		})
	}
}
