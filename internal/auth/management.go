package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// refreshMu serialises read → expiry check → refresh → write of the stored
// session. The refresh token is spent by the first caller that uses it, so a
// second concurrent refresh presents a token the server has already rotated
// past and comes back as "sign in again" — with four environments described at
// once, that is the common case rather than the rare one. The lock is held
// across the HTTP call on purpose: the callers behind it want the RESULT of
// this refresh, and the write it performs is what they read.
var refreshMu sync.Mutex

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
//  1. PALBASE_ACCESS_TOKEN — an explicit headless credential. Machine tokens
//     (`pat_…`) use DPoP with the paired PALBASE_DPOP_KEY.
//  2. PALBASE_ACCOUNT_TOKEN — the account-level machine identity.
//  3. The Bearer session in session.json, written by `palbase login` and
//     refreshed in place when expired.
//
// Returns an actionable error only when neither path is available.
func (c *Client) ManagementToken(ctx context.Context) (string, error) {
	if v := os.Getenv("PALBASE_ACCESS_TOKEN"); v != "" {
		return v, nil
	}

	if v := strings.TrimSpace(os.Getenv(AccountTokenEnv)); v != "" {
		return v, nil
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
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
