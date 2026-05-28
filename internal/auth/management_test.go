package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnsureDPoPKey_CreatesThenReuses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_NO_KEYRING", "1")

	// First call provisions a fresh key.
	k1, err := EnsureDPoPKey("dev")
	require.NoError(t, err)
	require.NotEmpty(t, k1.Thumbprint())

	// Second call must reuse the stored key (stable jkt across runs) so a
	// PAT bound to the jkt stays valid between CLI invocations.
	k2, err := EnsureDPoPKey("dev")
	require.NoError(t, err)
	require.Equal(t, k1.Thumbprint(), k2.Thumbprint(),
		"EnsureDPoPKey must reuse the stored key, not mint a new jkt each call")
}

func TestManagementToken_FromEnv(t *testing.T) {
	t.Setenv("PALBASE_ACCESS_TOKEN", "pat_headless_123")
	tok, err := ManagementToken("dev")
	require.NoError(t, err)
	require.Equal(t, "pat_headless_123", tok)
}

func TestManagementToken_LoginFallback(t *testing.T) {
	// Isolate HOME so the test sees only credentials we write here.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")

	// A logged-in user without a PAT must still authenticate to the
	// management API — their DPoP-bound login access token is the
	// credential.
	creds := &Credentials{
		AccessToken: "login_at_xyz",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	require.NoError(t, SaveCredentials("dev", creds))

	tok, err := ManagementToken("dev")
	require.NoError(t, err)
	require.Equal(t, "login_at_xyz", tok)
}

func TestManagementToken_UnauthenticatedIsActionable(t *testing.T) {
	// Neither env nor stored credentials → actionable error.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ACCESS_TOKEN", "")

	_, err := ManagementToken("dev")
	require.Error(t, err)
	require.Contains(t, err.Error(), "palbase login")
}
