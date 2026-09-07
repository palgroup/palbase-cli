package sealedclient

// A BINDING IS THE FLEET SAYING "THIS KEY IS THAT STACK'S".
//
// This is the half the task brief did not name and the wire cannot do without.
// A stack does NOT publish a bare SignedKeyset: it publishes a BoundKeyset —
// the fleet's statement about the stack's signing key, plus the keyset that key
// signed (v2/internal/server/sealed.go:229 `signedKeysetDocument`, which
// marshals `sealed.BoundKeyset{Binding: statement, SignedKeyset: signed}`). A
// client that verified only the inner SignedKeyset against an embedded root
// would refuse every stack in the fleet, which is exactly what was measured
// live on 2026-08-24 before the chain existed.
//
// The problem the chain solves: the client ships ONE root, every tenant derives
// its own signing key from its own secret, so one embedded root can verify no
// tenant. The obvious repair — one signing key for the whole fleet — is the
// wrong one and not marginally: a tenant pod runs CUSTOMER CODE in the same
// process as palsvc, so a fleet-wide signing private key on that disk lets any
// one customer mint a keyset every other customer's client would accept.
//
// So the fleet key stays in the control plane and signs a short statement
// instead: stack X's sealing signing key is P, until T. The tenant signs its
// own keyset with P. We verify the statement against the embedded root, then
// the keyset against P. Mirrors v2/internal/sealed/binding.go.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Binding is the fleet's statement. v2/internal/sealed/binding.go:40.
type Binding struct {
	V uint64 `json:"v"`
	// StackRef is the stack this statement is about. WITHOUT IT a binding
	// issued for one tenant is a valid binding for ANY tenant: whoever can
	// answer for b.example replays a.example's statement beside a keyset of
	// their own choosing and the chain closes.
	StackRef string `json:"stackRef"`
	// SigningKey is that stack's Ed25519 public key, base64 (std).
	SigningKey string `json:"signingKey"`
	// Expires is the unix second after which the statement means nothing — the
	// same freeze argument the keyset makes for itself.
	Expires int64 `json:"expires"`
}

// BindingVersion is the only binding shape this build accepts.
// v2/internal/sealed/binding.go:53.
const BindingVersion = 1

// SignedBinding is the statement plus the fleet's signature over it.
type SignedBinding struct {
	Binding json.RawMessage `json:"binding"`
	Sig     string          `json:"sig"`
	RootKid string          `json:"rootKid"`
	Alg     string          `json:"alg"`
}

// BoundKeyset is the whole document a stack publishes. The two halves travel
// together because they are useless apart. v2/internal/sealed/binding.go:174.
type BoundKeyset struct {
	Binding *SignedBinding `json:"binding,omitempty"`
	*SignedKeyset
}

var (
	// ErrBindingSignature means the fleet's signature did not verify against
	// any root this build trusts.
	ErrBindingSignature = errors.New("sealed: binding signature does not verify")
	// ErrBindingSubject means the statement is about a DIFFERENT stack. A valid
	// signature over someone else's stack is exactly the replay this catches.
	ErrBindingSubject = errors.New("sealed: binding names another stack")
	// ErrBindingExpired means the statement is past its end.
	ErrBindingExpired = errors.New("sealed: binding has expired")
	// ErrBindingMalformed means it could not be read at all — including the
	// case of a document that carries no binding, which is refused rather than
	// verified directly against an embedded root.
	ErrBindingMalformed = errors.New("sealed: binding is malformed")
)

// VerifyBinding checks a statement against the embedded roots and returns the
// signing key it names. SIGNATURE FIRST, then subject, then expiry — the same
// order and the same reason as VerifyKeyset. v2/internal/sealed/binding.go:119.
//
// `wantRef` is the stack the CALLER believes it is talking to, derived from
// local configuration (the API key and the address). Taking it out of the
// document instead would make the subject check a tautology.
func VerifyBinding(roots TrustedRoots, sb *SignedBinding, wantRef string, now time.Time) (ed25519.PublicKey, error) {
	if sb == nil || len(sb.Binding) == 0 || sb.Sig == "" {
		return nil, ErrBindingMalformed
	}
	if sb.Alg != rootAlg {
		return nil, ErrBindingMalformed
	}
	root, ok := roots[sb.RootKid]
	if !ok || len(root) != ed25519.PublicKeySize {
		return nil, ErrBindingSignature
	}
	sig, err := base64.StdEncoding.DecodeString(sb.Sig)
	if err != nil {
		return nil, ErrBindingMalformed
	}
	if !ed25519.Verify(root, sb.Binding, sig) {
		return nil, ErrBindingSignature
	}

	var b Binding
	if err := json.Unmarshal(sb.Binding, &b); err != nil {
		return nil, ErrBindingMalformed
	}
	if b.V != BindingVersion {
		return nil, ErrBindingMalformed
	}
	if strings.TrimSpace(wantRef) == "" {
		// No subject to check against is not "check nothing" — it is the one
		// state in which the chain proves nothing at all.
		return nil, ErrBindingSubject
	}
	if !strings.EqualFold(strings.TrimSpace(b.StackRef), strings.TrimSpace(wantRef)) {
		return nil, ErrBindingSubject
	}
	if b.Expires == 0 {
		return nil, ErrBindingMalformed
	}
	if now.Unix() >= b.Expires {
		return nil, ErrBindingExpired
	}
	return decodeSigningKey(b.SigningKey)
}

func decodeSigningKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrBindingMalformed
	}
	return ed25519.PublicKey(raw), nil
}

// VerifyBoundKeyset walks the chain: the embedded root vouches for the binding,
// the binding vouches for the stack's key, that key signed the keyset.
// v2/internal/sealed/binding.go:188.
func VerifyBoundKeyset(roots TrustedRoots, doc *BoundKeyset, wantRef string, lastSeenVersion uint64, now time.Time) (*Keyset, error) {
	if doc == nil || doc.SignedKeyset == nil {
		return nil, ErrKeysetMalformed
	}
	if doc.Binding == nil {
		// NO CHAIN, NO TRUST. There is deliberately no path that accepts a
		// keyset because it is "signed by something": the signer would be
		// whoever answered, which is precisely who the embedded root exists to
		// distrust.
		return nil, ErrBindingMalformed
	}
	stackKey, err := VerifyBinding(roots, doc.Binding, wantRef, now)
	if err != nil {
		return nil, err
	}
	// The keyset is verified against the key the FLEET vouched for, under the
	// kid the document claims — so a document naming a different signer cannot
	// verify.
	return VerifyKeyset(TrustedRoots{doc.RootKid: stackKey}, doc.SignedKeyset, lastSeenVersion, now)
}
