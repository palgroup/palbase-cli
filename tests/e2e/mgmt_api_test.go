//go:build e2e

// Package e2e exercises the Management-API command path end-to-end
// against a real dev stack (api.dev.palbase.studio behind Kong → Studio
// /api/v1 → palauth PAT+DPoP verify). It is gated by the `e2e` build tag
// AND requires live credentials, so it never runs in the unit suite.
//
// Run:
//
//	export PALBASE_ACCESS_TOKEN=pat_…          # DPoP-bound PAT (Dashboard-issued)
//	export PALBASE_E2E_ORG_ID=org_…            # org the PAT's user owns
//	export PALBASE_PLATFORM_URL=https://api.dev.palbase.studio   # optional
//	export PALBASE_NO_KEYRING=1                # use file-backed key in CI
//	go test -tags e2e -race ./tests/e2e/...
//
// The keyring DPoP key MUST be the one the PAT is bound to (the jkt
// `palbase login` printed). In CI, provision it under $HOME/.palbase or
// via the keyring before running.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/stretchr/testify/require"
)

func e2eConfig(t *testing.T) (base, pat, orgID string, key *auth.DPoPKey) {
	t.Helper()
	pat = os.Getenv("PALBASE_ACCESS_TOKEN")
	orgID = os.Getenv("PALBASE_E2E_ORG_ID")
	if pat == "" || orgID == "" {
		t.Skip("set PALBASE_ACCESS_TOKEN + PALBASE_E2E_ORG_ID to run the mgmt-api e2e")
	}
	base = os.Getenv("PALBASE_PLATFORM_URL")
	if base == "" {
		base = "https://api.dev.palbase.studio"
	}
	mode := os.Getenv("PALBASE_MODE")
	if mode == "" {
		mode = "dev"
	}
	k, err := auth.LoadDPoPKey(mode)
	require.NoError(t, err, "load the keyring DPoP key the PAT is bound to (run `palbase login` first)")
	return base, pat, orgID, k
}

// uniqueRef returns a short, contract-valid ref ([a-z0-9]{4,16}).
func uniqueRef() string {
	return fmt.Sprintf("e2e%d", time.Now().UnixNano()%1_000_000_000)
}

func TestE2E_CreatePollReveal_DPoPBound(t *testing.T) {
	base, pat, orgID, key := e2eConfig(t)
	c := transport.New(base, key, pat)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ref := uniqueRef()

	// 1. create — async, expect a workflow handle.
	var handle struct {
		WorkflowID string `json:"workflowId"`
		RunID      string `json:"runId"`
	}
	require.NoError(t, c.Do(ctx, http.MethodPost, "/api/v1/projects",
		map[string]any{"ref": ref, "orgId": orgID, "name": "e2e", "tier": "free"}, &handle))
	require.NotEmpty(t, handle.WorkflowID, "create must return a workflow handle")

	// 2. poll status until the project leaves the provisioning state.
	deadline := time.Now().Add(4 * time.Minute)
	var status string
	for time.Now().Before(deadline) {
		var row struct {
			Ref    string `json:"ref"`
			Status string `json:"status"`
		}
		err := c.Do(ctx, http.MethodGet, "/api/v1/projects/"+ref, nil, &row)
		if err == nil {
			status = row.Status
			if status == "ready" || status == "active" {
				break
			}
		}
		time.Sleep(5 * time.Second)
	}
	require.Contains(t, []string{"ready", "active"}, status, "project never became ready (last status %q)", status)

	// 3. reveal — DPoP-bound write-scoped call returns plaintext.
	var revealed struct {
		PublishableKey *string `json:"publishableKey"`
	}
	require.NoError(t, c.Do(ctx, http.MethodGet, "/api/v1/projects/"+ref+"/api-keys?reveal=true", nil, &revealed))
	require.NotNil(t, revealed.PublishableKey, "reveal must surface the publishable key plaintext")
	require.True(t, strings.HasPrefix(*revealed.PublishableKey, "pb_"), "publishable key has pb_ prefix")
}

// TestE2E_BearerPresentedPAT_Rejected pins the no-downgrade rule: the
// same valid PAT presented as `Authorization: Bearer <pat>` (no DPoP
// proof) MUST be rejected. This is the security invariant from spec §3/§8.
func TestE2E_BearerPresentedPAT_Rejected(t *testing.T) {
	base, pat, _, _ := e2eConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/projects", nil)
	require.NoError(t, err)
	// Deliberately Bearer (not DPoP) and no DPoP proof header.
	req.Header.Set("Authorization", "Bearer "+pat)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	require.GreaterOrEqual(t, resp.StatusCode, 400,
		"a Bearer-presented DPoP-bound PAT must be rejected (no downgrade)")
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
}
