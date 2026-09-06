package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// ProofOptions binds a machine-identity proof to a request and its token.
type ProofOptions struct {
	// HTTPMethod is the upper-case request method (GET, POST, ...).
	HTTPMethod string
	// URL is the fully-qualified request URL. Canonicalisation strips
	// the query+fragment to match RFC 9449 §4.3 before signing.
	URL string
	// AccessToken is the machine token the proof is bound to.
	AccessToken string
	// Now is an override for deterministic tests. Production callers
	// leave it unset and time.Now() is used.
	Now func() time.Time
}

// NewProof returns a signed DPoP proof JWT ready for the DPoP request
// header. Every call produces a fresh jti + iat; palauth's replay cache
// ensures single-use semantics server-side.
func (k *DPoPKey) NewProof(opts ProofOptions) (string, error) {
	if opts.HTTPMethod == "" || opts.URL == "" || opts.AccessToken == "" {
		return "", fmt.Errorf("dpop proof: method, url, and access token are required")
	}
	canonicalURL, err := canonicalProofURL(opts.URL)
	if err != nil {
		return "", err
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	jti, err := randomJTI()
	if err != nil {
		return "", fmt.Errorf("dpop proof: jti: %w", err)
	}

	payload := map[string]any{
		"jti": jti,
		"htm": strings.ToUpper(opts.HTTPMethod),
		"htu": canonicalURL,
		"iat": now().Unix(),
		"ath": accessTokenHash(opts.AccessToken),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("dpop proof: marshal payload: %w", err)
	}

	// RFC 9449 §4.2: header MUST carry typ=dpop+jwt and the proof public
	// key as `jwk`. go-jose's Signer attaches EmbedJWK when set on options
	// but the extra header fields (typ) must go through ExtraHeaders.
	signerOpts := (&jose.SignerOptions{}).
		WithType("dpop+jwt").
		WithHeader("jwk", k.publicJWK())
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: k.private},
		signerOpts,
	)
	if err != nil {
		return "", fmt.Errorf("dpop proof: signer: %w", err)
	}
	obj, err := signer.Sign(body)
	if err != nil {
		return "", fmt.Errorf("dpop proof: sign: %w", err)
	}
	return obj.CompactSerialize()
}

// canonicalProofURL strips query+fragment and normalises default ports
// so the `htu` value the proof signs matches what palauth re-derives on
// the server side. RFC 9449 §4.3.
func canonicalProofURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("dpop proof: parse url: %w", err)
	}
	u.RawQuery = ""
	u.Fragment = ""
	if (u.Scheme == "https" && u.Port() == "443") ||
		(u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
	}
	return u.String(), nil
}

// accessTokenHash computes the `ath` claim — base64url(SHA-256(token)).
// RFC 9449 §7.1 requires this whenever a proof is paired with an access
// token; it binds the proof to the specific token it authorises.
func accessTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomJTI returns 16 random bytes base64url-encoded. 128 bits is more
// than enough to keep collisions astronomical across a user's replay
// window (palauth caches for ~310s).
func randomJTI() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
