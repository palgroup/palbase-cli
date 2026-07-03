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

// TestIOSUse_BranchAppliesOnlyToRefBinding locks the cross-project branch-leak
// fix: an iOS app registered across MULTIPLE env-projects (one bundle id each)
// must apply `use <branch>` ONLY to the binding whose project IS the linked ref
// — every other env-binding is a separate project with its own branch namespace
// (no branch called "mybranch"), so it must resolve at its own default (empty
// branch → main). Before the fix, `use` threaded the branch into EVERY binding's
// config artifact, and the other project's resolveEndpointRef 404'd (observed
// live: NOT_FOUND: No endpoint_ref for project ref "dev0bvec").
func TestIOSUse_BranchAppliesOnlyToRefBinding(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "todoappm8p6z", DefaultEnv: "main", IOSAppID: "app_ios1",
	}))

	// project_ref → branchName seen at apps.configArtifact.
	branchByRef := map[string]string{}
	rig := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/apikey.reveal":
			iosTRPCOK(w, map[string]any{
				"endpointRef":    "todoappm8p6zd",
				"publishableKey": "pb_todoappm8p6zd_ckey",
			})
		case "/api/trpc/apps.listBindings":
			// Two env-bindings in TWO different projects.
			iosTRPCOK(w, []map[string]any{
				{"project_ref": "todoappm8p6z", "identifier": "com.demo.palbase", "env_preset": "production"},
				{"project_ref": "dev0bvec", "identifier": "com.demo.palbase.dev", "env_preset": "dev"},
			})
		case "/api/trpc/apps.configArtifact":
			in := iosQueryInput(t, r)
			ref, _ := in["projectRef"].(string)
			branch, _ := in["branchName"].(string)
			branchByRef[ref] = branch
			// Reflect the ref back so the two bundles don't collide.
			switch ref {
			case "todoappm8p6z":
				iosTRPCOK(w, map[string]any{
					"app_id": "app_ios1", "project_ref": "todoappm8p6z", "endpoint_ref": "todoappm8p6zd",
					"api_key": "pb_todoappm8p6zd_ckey", "base_url": "https://todoappm8p6zd.dev.palbase.studio",
					"env_preset": "production", "platform": "ios", "identifier": "com.demo.palbase",
				})
			default:
				iosTRPCOK(w, map[string]any{
					"app_id": "app_ios1", "project_ref": "dev0bvec", "endpoint_ref": "dev0bvecm",
					"api_key": "pb_dev0bvecm_ckey", "base_url": "https://dev0bvecm.dev.palbase.studio",
					"env_preset": "dev", "platform": "ios", "identifier": "com.demo.palbase.dev",
				})
			}
		case "/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case "/auth/oauth/providers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	restore := redirectHostTo(t, "todoappm8p6zd.dev.palbase.studio", rig.BaseURL)
	defer restore()

	cmd := newIOSUseCmd(Resolvers{
		Studio:    func() *studio.Client { return rig },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"dev"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "dev", branchByRef["todoappm8p6z"],
		"the ref binding must get the branch")
	require.Equal(t, "", branchByRef["dev0bvec"],
		"a DIFFERENT project's binding must NOT get the branch (it has no such branch) — resolves at its own default")
}

// TestIOSUse_ErrorsWhenBranchAppliesToNothing locks the OPPOSITE edge of the
// branch-scoping fix: `ios use <branch>` is an EXPLICIT re-target, so if the
// linked ref matches NO binding (or the ref's binding has an empty identifier),
// the branch reaches no config entry — openapi.json would point at <branch> while
// palbase-config.json points every env at main, a mismatched pair. That MUST error
// loudly, not print "✓ targets branch" and save DefaultEnv. (ios link / spec take
// requireBranchApplied=false, so their default-branch flow is unaffected — locked
// by TestIOSLink*/TestPullSpec*.)
func TestIOSUse_ErrorsWhenBranchAppliesToNothing(t *testing.T) {
	cases := []struct {
		name     string
		bindings []map[string]any // apps.listBindings response
	}{
		{
			// (b) the ref (todoappm8p6z) is NOT among the app's bindings at all.
			name: "ref matches no binding",
			bindings: []map[string]any{
				{"project_ref": "other0ref", "identifier": "com.demo.other", "env_preset": "production"},
			},
		},
		{
			// (a) the ref binding EXISTS but its identifier is empty → skipped
			// before the branch-scoping block, so the branch is applied to nothing.
			name: "ref binding has empty identifier",
			bindings: []map[string]any{
				{"project_ref": "todoappm8p6z", "identifier": "", "env_preset": "production"},
				{"project_ref": "other0ref", "identifier": "com.demo.other", "env_preset": "dev"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
				Ref: "todoappm8p6z", DefaultEnv: "main", IOSAppID: "app_ios1",
			}))
			rig := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/trpc/apikey.reveal":
					iosTRPCOK(w, map[string]any{
						"endpointRef":    "todoappm8p6zd",
						"publishableKey": "pb_todoappm8p6zd_ckey",
					})
				case "/api/trpc/apps.listBindings":
					iosTRPCOK(w, tc.bindings)
				case "/api/trpc/apps.configArtifact":
					// Any binding that resolves does so at its own default (main).
					in := iosQueryInput(t, r)
					ref, _ := in["projectRef"].(string)
					iosTRPCOK(w, map[string]any{
						"app_id": "app_ios1", "project_ref": ref, "endpoint_ref": ref + "m",
						"api_key": "pb_" + ref + "m_ckey", "base_url": "https://" + ref + "m.dev.palbase.studio",
						"env_preset": "production", "platform": "ios", "identifier": "com.demo.other",
					})
				case "/openapi.json":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
				case "/auth/oauth/providers":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{}`))
				default:
					t.Errorf("unexpected call %s", r.URL.Path)
					http.Error(w, "unexpected", http.StatusInternalServerError)
				}
			})
			restore := redirectHostTo(t, "todoappm8p6zd.dev.palbase.studio", rig.BaseURL)
			defer restore()

			cmd := newIOSUseCmd(Resolvers{
				Studio:    func() *studio.Client { return rig },
				Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
			})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"dev"})
			err := cmd.Execute()

			require.Error(t, err, "use must fail when the branch re-targets nothing")
			require.Contains(t, err.Error(), "nothing to re-target")
			require.NotContains(t, out.String(), "now targets branch",
				"must NOT report success when it retargeted nothing")

			// And DefaultEnv must NOT be advanced to the branch on failure.
			cfg, cfgErr := auth.LoadProjectConfig()
			require.NoError(t, cfgErr)
			require.Equal(t, "main", cfg.DefaultEnv,
				"a failed re-target must not leave the project pointing at the branch")
		})
	}
}
