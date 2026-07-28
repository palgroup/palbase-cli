// Package auth — DPoP (RFC 9449) keypair + proof generation for CLI.
//
// The CLI is an OAuth 2.1 public client. Palauth issues its access and
// refresh tokens sender-constrained via cnf.jkt (JWK thumbprint), meaning
// every API request must carry a DPoP proof JWT signed by the private
// key whose public thumbprint matches the binding. A leaked access token
// without the paired private key is useless.
//
// Key lifecycle:
//
//  1. `palbase login` generates an ephemeral ECDSA P-256 keypair and
//     stores the private JWK in the OS keyring (macOS Keychain, Linux
//     Secret Service, Windows Credential Manager). The public thumbprint
//     is included in the /oauth/authorize request as `dpop_jkt` so
//     palauth pre-binds the access token at issue time.
//
//  2. Every HTTP call built via AttachProof signs a fresh proof JWT with
//     the stored private key. The proof is unique per request (htm, htu,
//     iat, jti, ath) and single-use — palauth's Redis jti dedup ensures
//     replay fails server-side even if the proof is intercepted.
//
//  3. `palbase logout` (or any key rotation path) deletes the keyring
//     entry; the next login mints a new keypair.
//
// The mode name ("prod", "dev") is part of the keyring slot so the two
// sessions can coexist without clobbering each other.
package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-jose/go-jose/v4"
)

// DPoPKey bundles an ECDSA P-256 keypair with its JWK thumbprint,
// cached so /oauth/authorize + proof construction do not recompute.
type DPoPKey struct {
	private    *ecdsa.PrivateKey
	thumbprint string
}

// NewDPoPKey mints a fresh ECDSA P-256 keypair. crypto/rand is used for
// key generation and for all per-proof jti values.
func NewDPoPKey() (*DPoPKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate dpop key: %w", err)
	}
	thumb, err := thumbprint(priv)
	if err != nil {
		return nil, fmt.Errorf("compute thumbprint: %w", err)
	}
	return &DPoPKey{private: priv, thumbprint: thumb}, nil
}

// Thumbprint returns the base64url SHA-256 JWK thumbprint (RFC 7638) of
// the public key. Palauth's access tokens carry this in cnf.jkt; every
// proof we sign advertises a JWK whose thumbprint must match.
func (k *DPoPKey) Thumbprint() string { return k.thumbprint }

// publicJWK returns the public half as a jose JWK suitable for embedding
// in a DPoP proof's protected header.
func (k *DPoPKey) publicJWK() jose.JSONWebKey {
	return jose.JSONWebKey{
		Key:       &k.private.PublicKey,
		Algorithm: string(jose.ES256),
	}
}

// privateJoseJWK returns the private half for signing.
func (k *DPoPKey) privateJoseJWK() jose.JSONWebKey {
	return jose.JSONWebKey{
		Key:       k.private,
		Algorithm: string(jose.ES256),
	}
}

// thumbprint delegates to go-jose's RFC 7638 implementation so the
// canonicalisation exactly matches palauth + RFC-conformant verifiers.
func thumbprint(priv *ecdsa.PrivateKey) (string, error) {
	jwk := jose.JSONWebKey{Key: &priv.PublicKey, Algorithm: string(jose.ES256)}
	sum, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

// marshalPrivateJWK serialises the private key for keyring storage. Uses
// go-jose's MarshalJSON which produces a stable, RFC 7517-compliant form.
func (k *DPoPKey) marshalPrivateJWK() ([]byte, error) {
	jwk := jose.JSONWebKey{Key: k.private, Algorithm: string(jose.ES256)}
	return jwk.MarshalJSON()
}

// loadPrivateJWK inverts marshalPrivateJWK + re-derives the thumbprint.
func loadPrivateJWK(raw []byte) (*DPoPKey, error) {
	var jwk jose.JSONWebKey
	if err := jwk.UnmarshalJSON(raw); err != nil {
		return nil, fmt.Errorf("parse jwk: %w", err)
	}
	priv, ok := jwk.Key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("stored jwk is not an ECDSA private key (got %T)", jwk.Key)
	}
	if priv.Curve != elliptic.P256() {
		return nil, fmt.Errorf("unsupported curve (want P-256)")
	}
	thumb, err := thumbprint(priv)
	if err != nil {
		return nil, err
	}
	return &DPoPKey{private: priv, thumbprint: thumb}, nil
}

// --- Keyring storage --------------------------------------------------------

// dpopKeyService is the service name used for every keyring entry. The mode
// (prod/dev) is baked into the account slot so the two CLI modes don't
// clobber each other.
const dpopKeyService = "palbase-cli"

func keyringAccount(mode string) string {
	if mode == "" {
		mode = "prod"
	}
	return "dpop-key-" + mode
}

// fileFallbackPath returns the 0600 file the CLI falls back to when the
// OS keyring is not available. Mirrors SaveCredentials' convention so
// both files sit alongside each other and share a purge.
func fileFallbackPath(mode string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	if mode == "" {
		mode = "prod"
	}
	return filepath.Join(home, ".palbase", fmt.Sprintf("dpop-key-%s.jwk", mode)), nil
}
