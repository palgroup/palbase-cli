package sealedclient

// The response direction.
//
// It costs no second key and no second round trip: the HPKE context the request
// established exports a symmetric secret (RFC 9180 §5.3), both sides derive it
// independently, and the response is a plain AEAD under that secret. This is
// the OHTTP shape (RFC 9458 §4.4). Mirrors v2/internal/sealed/response.go.
//
// TWO THINGS LIVE OUTSIDE THE CIPHERTEXT and are therefore bound into the
// response AAD explicitly:
//
//   - the inner content type, or a JSON body could be re-presented as something
//     the client parses differently;
//   - the HTTP STATUS, which sits on the response line where no amount of body
//     encryption reaches. The server sends 200 on the wire with the real status
//     sealed inside (v2/internal/sealed/response.go:191), so the status this
//     package returns is the one the handler produced — and an attacker who
//     rewrites it cannot make the body open.

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strconv"

	"golang.org/x/crypto/chacha20poly1305"
)

// ResponseEnvelope is the sealed response body. Mirrors
// v2/internal/sealed/response.go:46.
type ResponseEnvelope struct {
	V   int    `json:"v"`
	N   string `json:"n"`   // base64 AEAD nonce
	CT  string `json:"ct"`  // base64 ciphertext
	ICT string `json:"ict"` // inner content type
	St  int    `json:"st"`  // the HTTP status the handler produced
}

var (
	// ErrResponseKey means the exporter secret is the wrong size for the AEAD.
	ErrResponseKey = errors.New("sealed: bad response key")
	// ErrResponseInvalid means the response could not be opened. One error for
	// every cause, exactly as the server does it: telling a caller WHICH part
	// of a forgery failed is telling them how to forge better.
	ErrResponseInvalid = errors.New("sealed: response could not be opened")
)

// ResponseAADPrefix domain-separates the response AAD from the request one, so
// a response can never be accepted where a request is expected.
// v2/internal/sealed/response.go:43.
var ResponseAADPrefix = []byte("palbase-sealed/response/v1")

// responseAAD renders the bytes bound into the response ciphertext.
// v2/internal/sealed/response.go:64 — newline-joined here and length-prefixed
// in the REQUEST direction, which is not an inconsistency to tidy up: these
// bytes are a wire contract three bindings already agree on, and the ambiguity
// the request encoding fixed cannot arise from a fixed prefix, a media type and
// a decimal integer.
func responseAAD(ict string, status int) []byte {
	var b bytes.Buffer
	b.Write(ResponseAADPrefix)
	b.WriteByte('\n')
	b.WriteString(ict)
	b.WriteByte('\n')
	b.WriteString(strconv.Itoa(status))
	return b.Bytes()
}

// OpenResponse decrypts a sealed response under the exporter secret the request
// kept. Mirrors v2/internal/sealed/response.go:105.
func OpenResponse(exporter []byte, e *ResponseEnvelope) ([]byte, error) {
	if e == nil || e.V != EnvelopeVersion {
		return nil, ErrResponseInvalid
	}
	if len(exporter) != chacha20poly1305.KeySize {
		return nil, ErrResponseKey
	}
	aead, err := chacha20poly1305.New(exporter)
	if err != nil {
		return nil, ErrResponseKey
	}
	nonce, err := base64.StdEncoding.DecodeString(e.N)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, ErrResponseInvalid
	}
	ct, err := base64.StdEncoding.DecodeString(e.CT)
	if err != nil {
		return nil, ErrResponseInvalid
	}
	pt, err := aead.Open(nil, nonce, ct, responseAAD(e.ICT, e.St))
	if err != nil {
		return nil, ErrResponseInvalid
	}
	return pt, nil
}
