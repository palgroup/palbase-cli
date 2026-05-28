package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// management.go wires the Management-API (REST + DPoP-bound PAT) auth
// material the `internal/transport` REST client needs:
//
//   - a keyring ECDSA P-256 DPoP key (the client half of the binding),
//     provisioned on `palbase login` and reused thereafter so its jkt is
//     stable; and
//   - the DPoP-bound Personal Access Token presented as the
//     `Authorization: DPoP <pat>` credential.
//
// PAT issuance itself is NOT done here. palauth's PAT-create endpoint
// (POST /auth/tokens/personal) is gated by sessionMiddleware, which
// requires a `session_id` claim the CLI's OAuth access token does not
// carry — so the CLI cannot mint a PAT with its login token. The PAT is
// generated from the Dashboard (session-authed, bound to this key's jkt)
// and supplied to the CLI via PALBASE_ACCESS_TOKEN. See
// docs/decisions/2026-05-24-s5-cli-pat-provisioning-and-backend-trpc.md.

// EnsureDPoPKey returns the keyring DPoP key for mode, minting + storing
// one if none exists yet. Idempotent: an existing key is loaded and
// returned unchanged so its jkt thumbprint — the value a Dashboard-issued
// PAT is bound to — stays stable across CLI invocations.
func EnsureDPoPKey(mode string) (*DPoPKey, error) {
	key, err := LoadDPoPKey(mode)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ErrDPoPKeyMissing) {
		return nil, fmt.Errorf("load dpop key: %w", err)
	}
	key, err = NewDPoPKey()
	if err != nil {
		return nil, fmt.Errorf("generate dpop key: %w", err)
	}
	if err := StoreDPoPKey(mode, key); err != nil {
		return nil, fmt.Errorf("store dpop key: %w", err)
	}
	return key, nil
}

// ManagementToken resolves the DPoP-bound credential the management API
// (Studio /api/v1) expects on the Authorization header.
//
// Priority:
//  1. PALBASE_ACCESS_TOKEN — a Dashboard-issued, DPoP-bound PAT. Headless
//     callers (CI, AI agents, no browser) need this; an env-supplied PAT
//     also wins for interactive callers that want a narrowly-scoped
//     credential.
//  2. The login access token from credentials-<mode>.json — the DPoP-bound
//     OAuth token `palbase login` provisions (jkt set in the authorize
//     request, RFC 9449 §10). Studio /api/v1 accepts it on its own; no PAT
//     is required for interactive use. Expired tokens are refreshed in
//     place via /oauth/token (DPoP-bound by CLI-11), so an interactive
//     user keeps working across the 30-min access-token lifetime without
//     re-running `palbase login`.
//
// Returns an actionable error only when neither path is available: the
// caller is unauthenticated AND not configured for headless use.
func (c *Client) ManagementToken(ctx context.Context) (string, error) {
	if v := os.Getenv("PALBASE_ACCESS_TOKEN"); v != "" {
		return v, nil
	}
	creds, err := LoadCredentials(c.Cfg.Mode)
	if err != nil || creds.AccessToken == "" {
		return "", errNotAuthenticated()
	}
	if creds.IsExpired() {
		// Refresh through the same DPoP-bound /oauth/token path login uses
		// — palauth carries cnf.jkt forward from the prior binding (storage.go
		// privateClaims), so the renewed access token stays sender-constrained.
		// If the refresh token is itself dead (30-day lifetime, family-revoked,
		// or DPoP key wiped), the caller still needs to re-login — surface
		// the underlying refresh error verbatim so they see why.
		refreshed, rerr := c.RefreshTokens(ctx, creds)
		if rerr != nil {
			return "", fmt.Errorf("%w (refresh failed: %v)", errNotAuthenticated(), rerr)
		}
		return refreshed.AccessToken, nil
	}
	return creds.AccessToken, nil
}

func errNotAuthenticated() error {
	return fmt.Errorf("not authenticated — run `palbase login` (or, for headless use, " +
		"export PALBASE_ACCESS_TOKEN with a Dashboard-issued DPoP-bound PAT)")
}
