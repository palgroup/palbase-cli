package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

func TestLoadDPoPKey_RequiresAP256PrivateKey(t *testing.T) {
	p256, err := loadPrivateJWK([]byte(sdkGeneratedJWK))
	require.NoError(t, err)
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	for name, key := range map[string]jose.JSONWebKey{
		"public key":        p256.publicJWK(),
		"P-384 private key": {Key: p384},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := key.MarshalJSON()
			require.NoError(t, err)
			t.Setenv(DPoPKeyEnv, string(raw))
			got, err := LoadDPoPKey()
			require.Nil(t, got)
			require.ErrorContains(t, err, DPoPKeyEnv)
		})
	}
}

func TestNewProof_RequiresAToken(t *testing.T) {
	k, err := loadPrivateJWK([]byte(sdkGeneratedJWK))
	require.NoError(t, err)
	proof, err := k.NewProof(ProofOptions{
		HTTPMethod: "POST", URL: "https://api.palbase.studio/v1/cloud/projects",
	})
	require.Empty(t, proof)
	require.Error(t, err)
}

func TestNewProof_HeaderAndClaims(t *testing.T) {
	k, err := loadPrivateJWK([]byte(sdkGeneratedJWK))
	require.NoError(t, err)

	fixed := time.Unix(1_700_000_000, 0)
	proof, err := k.NewProof(ProofOptions{
		HTTPMethod:  "post",
		URL:         "https://api.palbase.studio/v1/cloud/projects?limit=10",
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
		"https://api.palbase.studio/v1/cloud/projects",
		payload["htu"],
		"htu must be canonicalised (query stripped)",
	)
	require.Equal(t, float64(fixed.Unix()), payload["iat"])
	require.NotEmpty(t, payload["jti"])
	// ath = SHA-256(access token), base64url
	require.Equal(t, accessTokenHash("access.tok.xyz"), payload["ath"])
}

func TestNewProof_FreshJTIEachCall(t *testing.T) {
	k, err := loadPrivateJWK([]byte(sdkGeneratedJWK))
	require.NoError(t, err)
	seen := make(map[string]struct{}, 32)
	for range 32 {
		p, err := k.NewProof(ProofOptions{
			HTTPMethod:  "GET",
			URL:         "https://api.palbase.studio/v1/cloud/projects",
			AccessToken: "pat_test",
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
