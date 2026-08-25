package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// management.go resolves the credential the control plane's management surface
// expects on the Authorization header.
//
// The v2 cloud takes a plain Bearer session token — the one `palbase login`
// stores. There is no separate Personal Access Token to mint and no
// sender-constraining key to keep in step with it: the token the sign-in
// returned IS the management credential, and it refreshes in place.

// EnsureDPoPKey returns the keyring DPoP key for mode, minting and storing one
// if none exists yet. Idempotent, so the thumbprint stays stable across
// invocations.
//
// Sign-in no longer uses it: the v2 flow is the stack's own /auth/login and the
// token it returns is a plain Bearer. The key survives for the REST transport,
// which still signs a proof per request for surfaces that ask for one.
func EnsureDPoPKey() (*DPoPKey, error) {
	key, err := LoadDPoPKey()
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
	if err := StoreDPoPKey(key); err != nil {
		return nil, fmt.Errorf("store dpop key: %w", err)
	}
	return key, nil
}

// AccountTokenEnv, hesap seviyesindeki makine kimliğidir.
//
// Bir hesap token'ı organizasyon ve proje yaratabilir ve bilet basabilir; bir
// deploy bileti bunların hiçbirini yapamaz. İkisi de düzleme aynı şekilde
// (DPoP) sunuluyor, o yüzden burada ayrı bir dal gerekmiyor — fark YETKİDE,
// sunumda değil.
const AccountTokenEnv = "PALBASE_ACCOUNT_TOKEN"

// ManagementToken resolves the credential the control plane expects.
//
// Priority:
//  1. PALBASE_ACCESS_TOKEN — for a headless caller (CI, an agent in a
//     container) with no terminal to type a password into. A machine identity
//     (`pat_…`) travels here too; the transport presents it as DPoP.
//     1b. PALBASE_ACCOUNT_TOKEN — the account-level machine identity, read only
//     when the above is absent, so no existing setup changes.
//  2. The session token from session.json, written by
//     `palbase login`. The access token lives 30 minutes and is refreshed in
//     place, so an interactive user keeps working without signing in again.
//
// Returns an actionable error only when neither path is available.
func (c *Client) ManagementToken(ctx context.Context) (string, error) {
	if v := os.Getenv("PALBASE_ACCESS_TOKEN"); v != "" {
		return v, nil
	}

	// HESAP TOKEN'I VARSA CLI KENDİ BİLETİNİ BASAR.
	//
	// palcore'un backend'i bileti `@palbase/account` ile basıp CLI'a veriyor —
	// ama bir insanın (ya da basit bir CI'ın) elinde yalnız hesap token'ı
	// olabilir. O durumda "önce bir bilet bas" demek, kullanıcıyı iki adımlı
	// bir törene sokmaktır; CLI onu kendisi yapabilir.
	//
	// ÖNCELİK SIRASI DEĞİŞMEDİ: PALBASE_ACCESS_TOKEN hâlâ ÖNCE okunuyor. Bu
	// yol yalnız o YOKKEN devreye giriyor, yani bugünkü hiçbir kurulum
	// etkilenmiyor.
	if v := strings.TrimSpace(os.Getenv(AccountTokenEnv)); v != "" {
		return v, nil
	}
	creds, err := LoadCredentials()
	if err != nil || creds.AccessToken == "" {
		return "", errNotAuthenticated()
	}
	if creds.IsExpired() {
		// If the refresh token is itself dead (expired, rotated past, or the
		// session was revoked), the caller must sign in again — the underlying
		// reason travels verbatim so they see WHY rather than a bare
		// "not authenticated".
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
		"export PALBASE_ACCESS_TOKEN)")
}
