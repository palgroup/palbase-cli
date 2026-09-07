package sealedclient

// How the CLI learns which public key to seal to — and why a plain fetch would
// defeat the entire feature.
//
// The adversary this protects against is one who can see inside TLS: a proxy
// with an installed CA, a corporate MDM, a coerced certificate. That adversary
// can serve a substituted public key, and a client that simply fetched a JWKS
// would seal to them. So the keyset is SIGNED and the verification root is
// compiled into this binary (roots.go). Rotating a sealing key then needs no
// CLI release; only a root rotation does, and roots last years.
//
// A signature alone is not enough. Both extra mechanisms come from TUF and both
// are enforced below, mirroring v2/internal/sealed/keyset.go:142:
//
//   - ROLLBACK: `version` is monotonic and a document older than the newest
//     this process has accepted is refused. Otherwise an attacker replays an
//     old signed set — perhaps one naming a leaked key — and the signature is
//     perfectly valid.
//   - FREEZE: `expires` is mandatory. Otherwise an attacker pins the client to
//     a stale set forever by never delivering a newer one.
//
// Every failure leaves the document UNUSED. There is deliberately no "accept it
// anyway": sealing to an unverified key is worse than not sealing, because it
// looks like it worked.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// KeysetPath is where the document lives. NOT under `/.well-known`: the gateway
// routes that entire prefix to the auth surface, so a keyset published there is
// unreachable. Mirrors the `keysetPath` constant at
// v2/internal/server/sealed.go:130 and the iOS store's `path`.
const KeysetPath = "/palbase-sealed-keys.json"

// KeysetDocumentVersion is the document FORMAT version, distinct from
// Keyset.Version which is the monotonic counter. v2/internal/sealed/keyset.go:37.
const KeysetDocumentVersion = 1

// PublicKeyEntry is one advertised key. v2/internal/sealed/keyset.go:45.
type PublicKeyEntry struct {
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Pub string `json:"pub"` // base64 KEM public key
}

// Keyset is the document a stack publishes. v2/internal/sealed/keyset.go:54.
type Keyset struct {
	V       int              `json:"v"`
	Version uint64           `json:"version"` // monotonic; rollback protection
	Expires int64            `json:"expires"` // unix seconds; freeze protection
	Keys    []PublicKeyEntry `json:"keys"`
}

// SignedKeyset is the wire form: the document plus a detached signature over
// its canonical bytes. v2/internal/sealed/keyset.go:63.
type SignedKeyset struct {
	Keyset    json.RawMessage `json:"keyset"`
	Sig       string          `json:"sig"`     // base64 Ed25519 signature
	RootKid   string          `json:"rootKid"` // which key signed it
	Algorithm string          `json:"alg"`     // "ed25519"
}

// rootAlg is the one signature algorithm. An `alg` an attacker chooses is an
// algorithm-confusion bug waiting to happen, so it is checked and never
// negotiated.
const rootAlg = "ed25519"

var (
	// ErrKeysetSignature means the signature did not verify against any key the
	// caller trusts. The move is to KEEP USING what we have — never to accept
	// the document anyway.
	ErrKeysetSignature = errors.New("sealed: keyset signature does not verify")
	// ErrKeysetRollback means the document is older than one already seen.
	ErrKeysetRollback = errors.New("sealed: keyset version went backwards")
	// ErrKeysetExpired means the document is past its expiry.
	ErrKeysetExpired = errors.New("sealed: keyset expired")
	// ErrKeysetMalformed covers everything else.
	ErrKeysetMalformed = errors.New("sealed: keyset malformed")
	// ErrNoUsableKey means the document verified and holds nothing this build
	// can seal to — every key names a suite we do not speak.
	ErrNoUsableKey = errors.New("sealed: the keyset advertises no key for this suite")
)

// TrustedRoots maps a root kid to the Ed25519 public key that may sign under it.
type TrustedRoots map[string]ed25519.PublicKey

// canonicalKeyset renders the bytes that were signed.
//
// The RAW document bytes, never a re-marshal. A verifier that re-serializes
// before checking is trusting its own encoder to agree with the signer's, and
// two JSON encoders in two languages do not agree about key order or
// whitespace. Go's json.RawMessage keeps the original bytes for us, which is
// the whole reason the Swift binding needed a hand-written slicer and this one
// does not. v2/internal/sealed/keyset.go:91.
func canonicalKeyset(raw json.RawMessage) []byte { return []byte(raw) }

// VerifyKeyset checks a document against the given roots and the two TUF
// properties, and returns the keyset only if all of them hold. Mirrors
// v2/internal/sealed/keyset.go:142 check for check, in the same order.
//
// `lastSeenVersion` is the highest version already accepted; 0 for a first
// fetch. `now` is a parameter so a freeze test can control the clock.
func VerifyKeyset(roots TrustedRoots, sk *SignedKeyset, lastSeenVersion uint64, now time.Time) (*Keyset, error) {
	if sk == nil || len(sk.Keyset) == 0 || sk.Sig == "" {
		return nil, ErrKeysetMalformed
	}
	if sk.Algorithm != rootAlg {
		return nil, ErrKeysetMalformed
	}
	root, ok := roots[sk.RootKid]
	if !ok || len(root) != ed25519.PublicKeySize {
		return nil, ErrKeysetSignature
	}
	sig, err := base64.StdEncoding.DecodeString(sk.Sig)
	if err != nil {
		return nil, ErrKeysetMalformed
	}
	// SIGNATURE FIRST, before anything downstream reads a field out of the
	// document. Parsing attacker-controlled JSON and acting on it before
	// checking who wrote it is how a verifier becomes the attack surface.
	if !ed25519.Verify(root, canonicalKeyset(sk.Keyset), sig) {
		return nil, ErrKeysetSignature
	}

	var ks Keyset
	if err := json.Unmarshal(sk.Keyset, &ks); err != nil {
		return nil, ErrKeysetMalformed
	}
	if ks.V != KeysetDocumentVersion || len(ks.Keys) == 0 || ks.Expires == 0 {
		return nil, ErrKeysetMalformed
	}
	// STRICTLY older is the attack. An EQUAL version is the ordinary refresh of
	// an unchanged document, and refusing equality would make every refresh
	// after the first fail — riding an expiring document to a hard stop with no
	// way to replace it. `lastSeenVersion == 0` needs no special case: nothing
	// is strictly less than zero.
	if ks.Version < lastSeenVersion {
		return nil, ErrKeysetRollback
	}
	if now.After(time.Unix(ks.Expires, 0)) {
		return nil, ErrKeysetExpired
	}
	for _, k := range ks.Keys {
		if k.Kid == "" || k.Pub == "" || k.Alg == "" {
			return nil, ErrKeysetMalformed
		}
	}
	return &ks, nil
}

// SealingKey picks the entry this build can actually seal to.
//
// A key advertised for another suite is SKIPPED rather than tried: the alg
// field exists precisely so a client can refuse a key it cannot use here,
// instead of discovering it at the first request as an unreadable AEAD failure.
// v2/internal/sealed/keyset.go:225.
func (k *Keyset) SealingKey() (kid string, pub []byte, err error) {
	for _, e := range k.Keys {
		if e.Alg != SuiteName {
			continue
		}
		raw, derr := base64.StdEncoding.DecodeString(e.Pub)
		if derr != nil {
			return "", nil, fmt.Errorf("%w: key %q is not base64", ErrKeysetMalformed, e.Kid)
		}
		return e.Kid, raw, nil
	}
	return "", nil, ErrNoUsableKey
}
