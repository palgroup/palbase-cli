package auth

import (
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

// ManagementToken resolves the DPoP-bound management PAT for mode.
//
// Source: PALBASE_ACCESS_TOKEN — a Dashboard-issued, DPoP-bound PAT. This
// is the supported management credential (headless + interactive) until
// palauth grows an OAuth-token-authed PAT-mint path. A missing env var
// yields an actionable error rather than a silent failure.
func ManagementToken(_ string) (string, error) {
	if v := os.Getenv("PALBASE_ACCESS_TOKEN"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("no management access token — set PALBASE_ACCESS_TOKEN to a " +
		"DPoP-bound Personal Access Token (generate one in the Palbase Dashboard, bound to " +
		"the jkt printed by `palbase login`)")
}
