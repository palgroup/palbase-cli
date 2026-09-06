//go:build e2e

// Package e2e exercises the cloud API with a live account-level machine token.
// The project test creates a free project and deletes it during cleanup.
//
// Run with an account token that can create and delete projects:
//
//	export PALBASE_ACCOUNT_TOKEN=pat_…
//	export PALBASE_DPOP_KEY='<the private P-256 JWK paired with the token>'
//	export PALBASE_PLATFORM_URL=https://api.palbase.studio # optional
//	go test -tags e2e -race ./tests/e2e/...
//
// PALBASE_ACCESS_TOKEN takes priority over PALBASE_ACCOUNT_TOKEN, as in the CLI.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/palgroup/palbase-cli/internal/apikey"
	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/project"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

func e2eClient(t *testing.T) *transport.Client {
	t.Helper()
	token := os.Getenv("PALBASE_ACCESS_TOKEN")
	if token == "" {
		token = strings.TrimSpace(os.Getenv(auth.AccountTokenEnv))
	}
	if token == "" {
		t.Skip("set PALBASE_ACCOUNT_TOKEN or PALBASE_ACCESS_TOKEN to run the cloud API e2e")
	}
	require.True(t, strings.HasPrefix(token, "pat_"), "the cloud API e2e requires an account-level machine token")
	base := os.Getenv("PALBASE_PLATFORM_URL")
	if base == "" {
		base = "https://api.palbase.studio"
	}
	key, err := auth.LoadDPoPKey()
	require.NoError(t, err, "supply the token's paired private JWK in PALBASE_DPOP_KEY")

	signer := transport.DPoPSigner
	t.Cleanup(func() { transport.DPoPSigner = signer })
	transport.DPoPSigner = func(method, address, accessToken string) (string, error) {
		return key.NewProof(auth.ProofOptions{
			HTTPMethod: method, URL: address, AccessToken: accessToken,
		})
	}
	return transport.New(base, token)
}

func TestE2E_CreateReadReveal_DPoPBound(t *testing.T) {
	c := e2eClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	name := fmt.Sprintf("cli-e2e-%d", time.Now().UnixNano())
	var created project.Project
	require.NoError(t, c.Do(ctx, http.MethodPost, "/v1/cloud/projects",
		map[string]string{"name": name, "tier": "free"}, &created))
	require.NotEmpty(t, created.Ref, "create must return the new project's ref")
	path := "/v1/cloud/projects/" + url.PathEscape(created.Ref)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cleanupCancel()
		require.NoError(t, c.Do(cleanupCtx, http.MethodDelete, path, nil, nil),
			"delete the project created by this test: %s", created.Ref)
	})
	require.NotNil(t, created.Name)
	require.Equal(t, name, *created.Name)
	require.Equal(t, "Running", created.Phase, "provisioning must complete before create returns")

	var status project.Project
	require.NoError(t, c.Do(ctx, http.MethodGet, path, nil, &status))
	require.Equal(t, created, status)

	var projects []project.Project
	require.NoError(t, c.Do(ctx, http.MethodGet, "/v1/cloud/projects", nil, &projects))
	require.Contains(t, projects, created, "the created project must appear in the caller's list")

	var keys apikey.Keys
	require.NoError(t, c.Do(ctx, http.MethodGet, path+"/keys", nil, &keys))
	// Assert key shape without exposing either key in test output.
	require.True(t, strings.HasPrefix(keys.AnonKey, "pb_project_c"), "missing publishable key")
	require.True(t, strings.HasPrefix(keys.ServiceRoleKey, "pb_project_s"), "missing service-role key")
}

func TestE2E_BearerPresentedPAT_Rejected(t *testing.T) {
	c := e2eClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Establish that this token and route work with the required DPoP proof.
	// A missing route or invalid token must not make the rejection test pass.
	require.NoError(t, c.Do(ctx, http.MethodGet, "/v1/cloud/projects", nil, nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/cloud/projects", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.HTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, resp.StatusCode,
		"a valid machine token presented without its DPoP proof must be rejected")
}
