package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-jose/go-jose/v4"
)

// DPoPKeyEnv supplies the private JWK bound to the caller's machine token.
const DPoPKeyEnv = "PALBASE_DPOP_KEY"

var ErrDPoPKeyMissing = errors.New("PALBASE_DPOP_KEY is required for a machine identity; supply the private JWK paired with the token")

// DPoPKey signs requests with the caller's ECDSA P-256 private key.
type DPoPKey struct {
	private *ecdsa.PrivateKey
}

// LoadDPoPKey reads the key supplied with the machine identity. Browser sessions
// do not use DPoP, and the CLI never generates or selects a different key.
func LoadDPoPKey() (*DPoPKey, error) {
	raw := strings.TrimSpace(os.Getenv(DPoPKeyEnv))
	if raw == "" {
		return nil, ErrDPoPKeyMissing
	}
	key, err := loadPrivateJWK([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", DPoPKeyEnv, err)
	}
	return key, nil
}

func loadPrivateJWK(raw []byte) (*DPoPKey, error) {
	var jwk jose.JSONWebKey
	if err := jwk.UnmarshalJSON(raw); err != nil {
		return nil, fmt.Errorf("parse jwk: %w", err)
	}
	priv, ok := jwk.Key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("jwk is not an ECDSA private key (got %T)", jwk.Key)
	}
	if priv.Curve != elliptic.P256() {
		return nil, fmt.Errorf("unsupported curve (want P-256)")
	}
	return &DPoPKey{private: priv}, nil
}

// publicJWK is the public half embedded in each request proof.
func (k *DPoPKey) publicJWK() jose.JSONWebKey {
	return jose.JSONWebKey{Key: &k.private.PublicKey, Algorithm: string(jose.ES256)}
}
