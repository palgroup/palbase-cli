package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// refreshMu serialises read → expiry check → refresh → write of the stored
// session for ManagementToken's callers. The refresh token is spent by the
// first caller that uses it, so a second concurrent refresh presents a token
// the server has already rotated past and comes back as "sign in again" — with
// four environments described at once, that is the common case rather than
// the rare one. The lock is held across the HTTP call on purpose: the callers
// behind it want the RESULT of this refresh, and the write it performs is what
// they read. The session's other readers (GetValidToken, Whoami) do not take
// it; none of them runs concurrently.
var refreshMu sync.Mutex

// A FAILED REFRESH IS A RESULT TOO. The callers queued behind a refresh that
// failed used to present the same refresh token again, one after another —
// four requests for one dead session, and a rotated token presented three more
// times when the server had rotated it and the write failed. The failure is
// kept, keyed by the refresh token it spent, while anybody is still waiting;
// the first caller after the queue drains tries again. The two values are
// guarded by refreshMu.
var (
	refreshWaiters     atomic.Int32
	refreshFailedToken string
	refreshFailure     error
)

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
	refreshWaiters.Add(1)
	refreshMu.Lock()
	defer func() {
		if refreshWaiters.Add(-1) == 0 {
			refreshFailedToken, refreshFailure = "", nil
		}
		refreshMu.Unlock()
	}()
	creds, err := LoadCredentials()
	if err != nil || creds.AccessToken == "" {
		return "", errNotAuthenticated()
	}
	if creds.IsExpired() {
		if refreshFailure != nil && creds.RefreshToken == refreshFailedToken {
			return "", refreshFailure
		}
		// If the refresh token is itself dead (expired, rotated past, or the
		// session was revoked), the caller must sign in again — the underlying
		// reason travels verbatim so they see WHY rather than a bare
		// "not authenticated".
		refreshed, rerr := c.RefreshTokens(ctx, creds)
		if rerr != nil {
			refreshFailedToken = creds.RefreshToken
			refreshFailure = fmt.Errorf("%w (refresh failed: %v)", errNotAuthenticated(), rerr)
			return "", refreshFailure
		}
		return refreshed.AccessToken, nil
	}
	return creds.AccessToken, nil
}

func errNotAuthenticated() error {
	return fmt.Errorf("not authenticated — run `palbase login` (or, for headless use, " +
		"export PALBASE_ACCESS_TOKEN)")
}
