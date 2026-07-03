package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/stretchr/testify/require"
)

// TestIOSUse_TargetsBranch drives `palbase ios use <branch>` end-to-end against
// an httptest tRPC + tenant rig and locks the three load-bearing behaviours:
//  1. the ios app id is read from the linked .palbase/config.json (no group
//     re-resolution when it is present),
//  2. the branch is threaded into apps.configArtifact (so base_url + key resolve
//     to the branch — not main),
//  3. .palbase/config.json's default_env is rewritten to the branch, so a later
//     `palbase spec` refreshes from the same branch.
func TestIOSUse_TargetsBranch(t *testing.T) {
	t.Chdir(t.TempDir())
	// Linked project with an ios app id already recorded (ios link wrote it).
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "todoappm8p6z", DefaultEnv: "main", IOSAppID: "app_ios1",
	}))

	var configArtifactBranch string
	appListCalled := false
	rig := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/apikey.reveal":
			// runPullSpec's lookupBackendTarget → the branch's tenant host + key.
			iosTRPCOK(w, map[string]any{
				"endpointRef":    "todoappm8p6zd",
				"publishableKey": "pb_todoappm8p6zd_ckey",
			})
		case "/api/trpc/apps.listBindings":
			iosTRPCOK(w, []map[string]any{
				{"project_ref": "todoappm8p6z", "identifier": "com.demo.palbase", "env_preset": "production"},
			})
		case "/api/trpc/apps.configArtifact":
			in := iosQueryInput(t, r)
			if b, ok := in["branchName"].(string); ok {
				configArtifactBranch = b
			}
			iosTRPCOK(w, map[string]any{
				"app_id": "app_ios1", "project_ref": "todoappm8p6z", "endpoint_ref": "todoappm8p6zd",
				"api_key": "pb_todoappm8p6zd_ckey", "base_url": "https://todoappm8p6zd.dev.palbase.studio",
				"env_preset": "production", "platform": "ios", "identifier": "com.demo.palbase",
			})
		case "/api/trpc/apps.list":
			appListCalled = true
			iosTRPCOK(w, []map[string]any{})
		case "/openapi.json":
			// The branch tenant host's spec (redirectHostTo routes it here).
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case "/auth/oauth/providers":
			// Best-effort oauth fetch inside studioConfigArtifactFetch.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	// The branch's tenant host (openapi.json) + palauth oauth providers route to
	// the same httptest server as the tRPC rig.
	restore := redirectHostTo(t, "todoappm8p6zd.dev.palbase.studio", rig.BaseURL)
	defer restore()

	cmd := newIOSUseCmd(Resolvers{
		Studio:    func() *studio.Client { return rig },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"mybranch"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "mybranch", configArtifactBranch,
		"the branch must be threaded into apps.configArtifact")
	require.False(t, appListCalled,
		"apps.list must NOT be called when the ios app id is in .palbase/config.json")

	cfg, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "mybranch", cfg.DefaultEnv,
		"use must record the branch as the active target so `spec` follows it")
	require.Equal(t, "app_ios1", cfg.IOSAppID, "app id must be preserved")

	// The config points at the branch host.
	raw, err := os.ReadFile(filepath.Join("Palbase", "palbase-config.json"))
	require.NoError(t, err)
	var byBundle map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &byBundle))
	require.Contains(t, byBundle, "com.demo.palbase")
	require.Equal(t, "https://todoappm8p6zd.dev.palbase.studio",
		byBundle["com.demo.palbase"]["base_url"], "config base_url must be the branch host")

	require.Contains(t, out.String(), `targets branch "mybranch"`)
	require.Contains(t, out.String(), "archive") // the stale-target warning
	_ = context.Background
}
