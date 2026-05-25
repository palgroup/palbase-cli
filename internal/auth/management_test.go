package auth

import (
	"testing"

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

func TestManagementToken_MissingIsActionable(t *testing.T) {
	t.Setenv("PALBASE_ACCESS_TOKEN", "")
	_, err := ManagementToken("dev")
	require.Error(t, err)
	require.Contains(t, err.Error(), "PALBASE_ACCESS_TOKEN")
}
