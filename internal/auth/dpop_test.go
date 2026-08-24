package auth

import (
	"crypto"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

func TestNewDPoPKey_ThumbprintStable(t *testing.T) {
	// Two separate key instances must yield the RFC 7638 thumbprint the
	// same way jose computes it for the public half. This pins the
	// canonicalisation we rely on for palauth's cnf.jkt binding.
	k, err := NewDPoPKey()
	require.NoError(t, err)

	jwk := jose.JSONWebKey{Key: &k.private.PublicKey, Algorithm: string(jose.ES256)}
	raw, err := jwk.Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	want := base64.RawURLEncoding.EncodeToString(raw)
	require.Equal(t, want, k.Thumbprint())
}

func TestDPoPKey_MarshalRoundTrip(t *testing.T) {
	k, err := NewDPoPKey()
	require.NoError(t, err)
	raw, err := k.marshalPrivateJWK()
	require.NoError(t, err)

	back, err := loadPrivateJWK(raw)
	require.NoError(t, err)
	require.Equal(t, k.Thumbprint(), back.Thumbprint())
}

func TestDPoPKey_RejectsNonP256(t *testing.T) {
	// A hand-crafted JWK with wrong kty/crv must not load — otherwise a
	// corrupted keyring blob could coerce the CLI into using whatever
	// curve happens to fit, bypassing palauth's alg validation on the
	// server side.
	raw := []byte(`{"kty":"RSA","n":"abc","e":"AQAB"}`)
	_, err := loadPrivateJWK(raw)
	require.Error(t, err)
}

func TestNewProof_HeaderAndClaims(t *testing.T) {
	k, err := NewDPoPKey()
	require.NoError(t, err)

	fixed := time.Unix(1_700_000_000, 0)
	proof, err := k.NewProof(ProofOptions{
		HTTPMethod:  "post",
		URL:         "https://dev.palbase.studio/api/trpc/project.list?batch=1",
		AccessToken: "access.tok.xyz",
		Now:         func() time.Time { return fixed },
	})
	require.NoError(t, err)

	parts := strings.Split(proof, ".")
	require.Len(t, parts, 3, "compact JWT has three parts")

	// Header
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var hdr map[string]any
	require.NoError(t, json.Unmarshal(hdrBytes, &hdr))
	require.Equal(t, "dpop+jwt", hdr["typ"])
	require.Equal(t, "ES256", hdr["alg"])
	require.NotNil(t, hdr["jwk"], "proof must embed public jwk")
	jwkBlob, err := json.Marshal(hdr["jwk"])
	require.NoError(t, err)
	// Private material must never appear in the proof header.
	require.NotContains(t, string(jwkBlob), `"d":`, "private key leaked into proof header")

	// Payload
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadBytes, &payload))
	require.Equal(t, "POST", payload["htm"], "htm must be upper-case")
	require.Equal(
		t,
		"https://dev.palbase.studio/api/trpc/project.list",
		payload["htu"],
		"htu must be canonicalised (query stripped)",
	)
	require.Equal(t, float64(fixed.Unix()), payload["iat"])
	require.NotEmpty(t, payload["jti"])
	// ath = SHA-256(access token), base64url
	require.Equal(t, accessTokenHash("access.tok.xyz"), payload["ath"])
}

func TestNewProof_OmitsAthWhenNoAccessToken(t *testing.T) {
	// First /oauth/token call has no access token yet. `ath` must be
	// absent, not an empty string, so palauth doesn't treat it as a
	// zero-valued binding.
	k, err := NewDPoPKey()
	require.NoError(t, err)

	proof, err := k.NewProof(ProofOptions{
		HTTPMethod: "POST",
		URL:        "https://dev.palbase.studio/oauth/token",
	})
	require.NoError(t, err)

	parts := strings.Split(proof, ".")
	require.Len(t, parts, 3)
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadBytes, &payload))
	_, has := payload["ath"]
	require.False(t, has, "ath must be absent when no access token is bound")
}

func TestNewProof_IncludesNonce(t *testing.T) {
	k, err := NewDPoPKey()
	require.NoError(t, err)
	proof, err := k.NewProof(ProofOptions{
		HTTPMethod: "POST",
		URL:        "https://dev.palbase.studio/oauth/token",
		Nonce:      "srv-nonce-123",
	})
	require.NoError(t, err)

	parts := strings.Split(proof, ".")
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadBytes, &payload))
	require.Equal(t, "srv-nonce-123", payload["nonce"])
}

func TestNewProof_FreshJTIEachCall(t *testing.T) {
	k, err := NewDPoPKey()
	require.NoError(t, err)
	seen := make(map[string]struct{}, 32)
	for range 32 {
		p, err := k.NewProof(ProofOptions{
			HTTPMethod: "GET",
			URL:        "https://dev.palbase.studio/api/trpc/x",
		})
		require.NoError(t, err)
		parts := strings.Split(p, ".")
		payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(payloadBytes, &payload))
		jti, _ := payload["jti"].(string)
		require.NotEmpty(t, jti)
		_, dup := seen[jti]
		require.False(t, dup, "jti collision on iteration")
		seen[jti] = struct{}{}
	}
}

func TestCanonicalProofURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://dev.palbase.studio/a", "https://dev.palbase.studio/a"},
		{"https://dev.palbase.studio/a?x=1", "https://dev.palbase.studio/a"},
		{"https://dev.palbase.studio/a?x=1#frag", "https://dev.palbase.studio/a"},
		{"https://dev.palbase.studio:443/a", "https://dev.palbase.studio/a"},
		{"http://localhost:80/cb", "http://localhost/cb"},
		{"https://dev.palbase.studio:8443/a", "https://dev.palbase.studio:8443/a"},
	}
	for _, tc := range tests {
		got, err := canonicalProofURL(tc.in)
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "input %q", tc.in)
	}
}

func TestStoreLoadDeleteDPoPKey_FileFallback(t *testing.T) {
	// Force the file fallback regardless of the host's keyring state
	// (CI doesn't have libsecret) and redirect $HOME to a temp dir so
	// the real user's ~/.palbase is untouched.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_NO_KEYRING", "1")

	k, err := NewDPoPKey()
	require.NoError(t, err)
	require.NoError(t, StoreDPoPKey(k))

	// File must exist and be 0600.
	path, err := fileFallbackPath()
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"stored key file must be 0600 (caller-only readable)")

	back, err := LoadDPoPKey()
	require.NoError(t, err)
	require.Equal(t, k.Thumbprint(), back.Thumbprint())

	require.NoError(t, DeleteDPoPKey())
	_, err = os.Stat(path)
	require.Error(t, err, "file fallback must be removed on delete")

	_, err = LoadDPoPKey()
	require.ErrorIs(t, err, ErrDPoPKeyMissing)
}

func TestLoadDPoPKey_MissingReturnsErrMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_NO_KEYRING", "1")
	_, err := LoadDPoPKey()
	require.ErrorIs(t, err, ErrDPoPKeyMissing)
}
