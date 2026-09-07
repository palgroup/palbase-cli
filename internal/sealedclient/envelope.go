// Package sealedclient is this CLI's half of Palbase's sealed request wire
// format: HPKE (RFC 9180) around the body — or, for a bodyless read, around
// nothing at all — opened by the service that handles the request rather than
// by the gateway in front of it.
//
// WHY THE CLI NEEDS ONE AT ALL. The server refuses a plaintext request under
// `/auth/` with 415 `sealed_required` (v2/internal/server/sealed.go:361-380 for
// the rule, :433-436 for the refusal). `palbase link --platform ios/android`
// reads `/auth/oauth/config`, so until this package existed that read simply
// could not succeed against any stack that publishes a keyset. The iOS and web
// SDKs each carry their own sealing client for the same reason; this is the
// third.
//
// WHAT IT IS FOR, said the way the server's own package says it: TLS terminates
// at the edge and the body crosses the cluster in the clear from there. Sealing
// means the gateway and the hops behind it see ciphertext while the handler
// sees exactly the bytes we sent. It is NOT end-to-end — Palbase holds the
// private key and decrypts.
//
// THE REFERENCE IMPLEMENTATION is v2/internal/sealed. Every wire decision in
// this package names the file and line it mirrors, because the two are one
// format and a comment is the only thing that keeps them one format.
package sealedclient

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
)

// ContentType is the media type a sealed BODY carries — the envelope is the
// body and the real content type is inside it. Mirrors
// v2/internal/sealed/sealed.go:47.
const ContentType = "application/palbase-sealed+json"

// EnvelopeHeaderName carries the envelope for a request that has no body.
// Mirrors v2/internal/sealed/envelope_header.go:19.
//
// A GET cannot hold a body — CFNetwork refuses one outright, which is why the
// header form exists at all — and `/auth/oauth/config` is a GET. This is the
// carriage `palbase link` actually uses.
const EnvelopeHeaderName = "X-Palbase-Sealed"

// EnvelopeVersion is the only version this package emits. Version 1's
// newline-joined AAD is refused by the server, not accepted with a warning
// (v2/internal/sealed/sealed.go:103), so there is nothing to negotiate.
const EnvelopeVersion = 2

// The one ciphersuite. There is no negotiation surface: suite selection rides
// the `kid`, so a post-quantum KEM later means a new kid rather than a
// handshake. Mirrors v2/internal/sealed/sealed.go:63-67.
//
// ChaCha20Poly1305 and NOT AES-GCM because CryptoKit — which the iOS binding of
// this same format runs on — has no `Curve25519_SHA256_AES_GCM_256`.
var (
	SuiteKEM  = hpke.KEM_X25519_HKDF_SHA256
	SuiteKDF  = hpke.KDF_HKDF_SHA256
	SuiteAEAD = hpke.AEAD_ChaCha20Poly1305
)

// SuiteName is what a keyset entry names its key's suite as. A key advertised
// for anything else is refused rather than tried. Mirrors
// v2/internal/sealed/keyset.go:222.
const SuiteName = "DHKEM-X25519-HKDF-SHA256/HKDF-SHA256/ChaCha20Poly1305"

// Envelope is the JSON object a sealed request carries, in its body or —
// bodyless — in EnvelopeHeaderName. Mirrors v2/internal/sealed/sealed.go:85.
//
// `ts` rides OUTSIDE the ciphertext because the server checks skew before it
// can afford to open anything. It is bound into the AAD, so carrying it in the
// clear does not let anyone move it.
type Envelope struct {
	V   int    `json:"v"`
	Kid string `json:"kid"`
	Enc string `json:"enc"` // base64 KEM encapsulation
	CT  string `json:"ct"`  // base64 ciphertext
	ICT string `json:"ict"` // inner content type, e.g. application/json
	TS  int64  `json:"ts"`  // our unix seconds
}

// AAD binds an envelope to the exact request it was minted for. Byte-for-byte
// the encoding at v2/internal/sealed/sealed.go:153.
//
// LENGTH-PREFIXED, never delimited: each variable field is a big-endian uint32
// length followed by its bytes, `ts` is a fixed big-endian uint64, and the
// idempotency key comes last. There is no separator, so no field can be shifted
// into its neighbour — the server's own comment records the two triples that
// rendered one AAD under the old newline encoding.
//
// Getting this wrong does not produce a readable failure. It produces a 400
// `sealed_invalid` on every request with nothing on either side saying which
// field disagreed, which is why the recorded vectors — not a fixture of our own
// — are what this is tested against.
func AAD(host, method, path, ict string, ts int64, idempotencyKey string) []byte {
	var b bytes.Buffer
	field := func(s string) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(s)))
		b.Write(l[:])
		b.WriteString(s)
	}
	field(host)
	field(method)
	field(path)
	field(ict)
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], uint64(ts))
	b.Write(t[:])
	field(idempotencyKey)
	return b.Bytes()
}

// ExporterContext is the label the RESPONSE key is derived under, so a request
// key and a response key can never be the same bytes even though they come from
// one encapsulation. Mirrors v2/internal/sealed/sealed.go:175.
var ExporterContext = []byte("palbase-sealed/response/v1")

// ExporterLen is the response key length in bytes.
const ExporterLen uint = 32

// Seal encrypts plaintext to the recipient public key and returns the envelope
// plus the exporter secret the RESPONSE will be sealed under.
//
// `ts` is a parameter rather than time.Now() for the same reason the server
// takes one: a caller that cannot control the clock cannot reproduce a recorded
// envelope, and the recorded envelopes are the only cross-binding check there
// is. Production callers pass time.Now().Unix().
func Seal(pub kem.PublicKey, kid string, plaintext []byte, ict, host, method, path string, ts int64, idempotencyKey string) (*Envelope, []byte, error) {
	sender, err := hpke.NewSuite(SuiteKEM, SuiteKDF, SuiteAEAD).NewSender(pub, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("sealed: new sender: %w", err)
	}
	enc, sealer, err := sender.Setup(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("sealed: setup: %w", err)
	}
	ct, err := sealer.Seal(plaintext, AAD(host, method, path, ict, ts, idempotencyKey))
	if err != nil {
		return nil, nil, fmt.Errorf("sealed: seal: %w", err)
	}
	return &Envelope{
		V:   EnvelopeVersion,
		Kid: kid,
		Enc: base64.StdEncoding.EncodeToString(enc),
		CT:  base64.StdEncoding.EncodeToString(ct),
		ICT: ict,
		TS:  ts,
	}, sealer.Export(ExporterContext, ExporterLen), nil
}

// EncodeEnvelopeHeader renders an envelope as one header value: base64url with
// NO padding of the same JSON the body would carry. Mirrors
// v2/internal/sealed/envelope_header.go:26.
//
// base64url-no-padding because a header value must not carry '=' ambiguity, CR,
// LF or spaces — and because the alternative, a bespoke field encoding, would
// be a SECOND wire format for one object, which is how two bindings drift.
func EncodeEnvelopeHeader(e *Envelope) (string, error) {
	if e == nil {
		return "", fmt.Errorf("sealed: nil envelope")
	}
	b, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("sealed: marshal envelope: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
