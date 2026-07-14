//go:build e2e

// Package e2e exercises the Management-API command path end-to-end against a
// real dev stack (api.dev.palbase.studio behind Kong → Studio /api/v2 → palauth
// PAT+DPoP verify). It is gated by the `e2e` build tag AND requires live
// credentials, so it never runs in the unit suite.
//
// There is NO organization id here: `POST /api/v2/projects` targets the caller's
// server-side default Organization (spec §7.3) — Organization is not a CLI
// context, so the e2e cannot pass one either.
//
// Run:
//
//	export PALBASE_ACCESS_TOKEN=pat_…          # DPoP-bound PAT (Dashboard-issued)
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

func e2eConfig(t *testing.T) (base, pat string, key *auth.DPoPKey) {
	t.Helper()
	pat = os.Getenv("PALBASE_ACCESS_TOKEN")
	if pat == "" {
		t.Skip("set PALBASE_ACCESS_TOKEN to run the mgmt-api e2e")
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
	return base, pat, k
}

// uniqueSeed returns a contract-valid ref SEED ([a-z0-9]{4,13}). It seeds the
// production Environment's ref; the Project itself has no ref.
func uniqueSeed() string {
	return fmt.Sprintf("e2e%d", time.Now().UnixNano()%1_000_000)
}

// The canonical create → resolve → reveal path on v2. It asserts the whole
// Project/Environment shape: create takes NO organizationId, the Project has no
// ref, and the KEY hangs off the ENVIRONMENT.
func TestE2E_CreateResolveReveal_DPoPBound(t *testing.T) {
	base, pat, key := e2eConfig(t)
	c := transport.New(base, key, pat)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	seed := uniqueSeed()

	// 1. create — async, in the caller's DEFAULT Organization. No organizationId.
	var handle struct {
		WorkflowID string `json:"workflowId"`
		RunID      string `json:"runId"`
	}
	require.NoError(t, c.Do(ctx, http.MethodPost, "/api/v2/projects",
		map[string]any{"ref": seed, "name": "e2e"}, &handle))
	require.NotEmpty(t, handle.WorkflowID, "create must return a workflow handle")

	// 2. resolve — the saga's production Environment is the one whose ref starts
	// with the seed. That is what identifies the Project it hangs under.
	projectID, envRef := "", ""
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) && envRef == "" {
		var projects []struct {
			ID string `json:"id"`
		}
		if err := c.Do(ctx, http.MethodGet, "/api/v2/projects", nil, &projects); err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		for _, p := range projects {
			var envs []struct {
				Ref          string `json:"ref"`
				Status       string `json:"status"`
				IsProduction bool   `json:"is_production"`
			}
			if err := c.Do(ctx, http.MethodGet, "/api/v2/projects/"+p.ID+"/environments", nil, &envs); err != nil {
				continue
			}
			for _, e := range envs {
				if strings.HasPrefix(e.Ref, seed) && e.IsProduction && e.Status == "active" {
					projectID, envRef = p.ID, e.Ref
				}
			}
		}
		if envRef == "" {
			time.Sleep(5 * time.Second)
		}
	}
	require.NotEmpty(t, envRef, "the project's production environment never became active (seed %q)", seed)

	// 3. reveal — the key hangs off the ENVIRONMENT, under its Project.
	var revealed struct {
		PublishableKey *string `json:"publishableKey"`
	}
	require.NoError(t, c.Do(ctx, http.MethodGet,
		"/api/v2/projects/"+projectID+"/environments/"+envRef+"/api-keys?reveal=true", nil, &revealed))
	require.NotNil(t, revealed.PublishableKey, "reveal must surface the publishable key plaintext")
	require.True(t, strings.HasPrefix(*revealed.PublishableKey, "pb_"), "publishable key has pb_ prefix")
	require.Contains(t, *revealed.PublishableKey, envRef, "the environment ref is embedded in the key")
}

// TestE2E_BearerPresentedPAT_Rejected pins the no-downgrade rule: the
// same valid PAT presented as `Authorization: Bearer <pat>` (no DPoP
// proof) MUST be rejected. This is the security invariant from spec §3/§8.
func TestE2E_BearerPresentedPAT_Rejected(t *testing.T) {
	base, pat, _ := e2eConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v2/projects", nil)
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
