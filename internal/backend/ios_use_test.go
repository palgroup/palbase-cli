package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	rig, restBase := iosUseRig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/apikey.reveal":
			// runPullSpec's lookupBackendTarget → the branch's tenant host + key.
			iosTRPCOK(w, map[string]any{
				"endpointRef":    "todoappm8p6zd",
				"publishableKey": "pb_todoappm8p6zd_ckey",
			})
		case r.URL.Path == "/api/v1/apps/app_ios1/bindings":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"project_ref": "todoappm8p6z", "identifier": "com.demo.palbase", "env_preset": "production"},
			})
		case r.URL.Path == "/api/v1/apps/app_ios1/config-artifact":
			configArtifactBranch = r.URL.Query().Get("branch")
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_ios1", "project_ref": "todoappm8p6z", "endpoint_ref": "todoappm8p6zd",
				"api_key": "pb_todoappm8p6zd_ckey", "base_url": "https://todoappm8p6zd.dev.palbase.studio",
				"env_preset": "production", "platform": "ios", "identifier": "com.demo.palbase",
			})
		case strings.HasSuffix(r.URL.Path, "/apps"):
			appListCalled = true
			iosRESTOK(w, http.StatusOK, []map[string]any{})
		case r.URL.Path == "/openapi.json":
			// The branch tenant host's spec (redirectHostTo routes it here).
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case r.URL.Path == "/auth/oauth/providers":
			// Best-effort oauth fetch inside studioConfigArtifactFetch.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	// The branch's tenant host (openapi.json) + palauth oauth providers route to
	// the same httptest server as the rig.
	restore := redirectHostTo(t, "todoappm8p6zd.dev.palbase.studio", rig.BaseURL)
	defer restore()

	cmd := newIOSUseCmd(Resolvers{
		Studio:    func() *studio.Client { return rig },
		REST:      func() REST { return iosRESTClientOn(t, restBase) },
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

	// The config is a FLAT single-env object pointing at the branch host.
	raw, err := os.ReadFile(filepath.Join("Palbase", "palbase-config.json"))
	require.NoError(t, err)
	var cfgFile map[string]any
	require.NoError(t, json.Unmarshal(raw, &cfgFile))
	require.NotContains(t, cfgFile, "identifier", "config no longer carries a bundle identifier (SDK sends it from Bundle.main)")
	require.Equal(t, "https://todoappm8p6zd.dev.palbase.studio",
		cfgFile["base_url"], "config base_url must be the branch host")

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

	// project_ref → branch seen at the config-artifact route.
	branchByRef := map[string]string{}
	rig, restBase := iosUseRig(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/apikey.reveal":
			iosTRPCOK(w, map[string]any{
				"endpointRef":    "todoappm8p6zd",
				"publishableKey": "pb_todoappm8p6zd_ckey",
			})
		case "/api/v1/apps/app_ios1/bindings":
			// Two env-bindings in TWO different projects.
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"project_ref": "todoappm8p6z", "identifier": "com.demo.palbase", "env_preset": "production"},
				{"project_ref": "dev0bvec", "identifier": "com.demo.palbase.dev", "env_preset": "dev"},
			})
		case "/api/v1/apps/app_ios1/config-artifact":
			ref := r.URL.Query().Get("env")
			branchByRef[ref] = r.URL.Query().Get("branch")
			// Reflect the ref back so the two bundles don't collide.
			switch ref {
			case "todoappm8p6z":
				iosRESTOK(w, http.StatusOK, map[string]any{
					"app_id": "app_ios1", "project_ref": "todoappm8p6z", "endpoint_ref": "todoappm8p6zd",
					"api_key": "pb_todoappm8p6zd_ckey", "base_url": "https://todoappm8p6zd.dev.palbase.studio",
					"env_preset": "production", "platform": "ios", "identifier": "com.demo.palbase",
				})
			default:
				iosRESTOK(w, http.StatusOK, map[string]any{
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
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	restore := redirectHostTo(t, "todoappm8p6zd.dev.palbase.studio", rig.BaseURL)
	defer restore()

	cmd := newIOSUseCmd(Resolvers{
		Studio:    func() *studio.Client { return rig },
		REST:      func() REST { return iosRESTClientOn(t, restBase) },
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

// TestIOSUse_ErrorsWhenBranchAppliesToNothing locks the branch-scoping fix, now
// enforced by the SINGLE-env buildPullSpecConfig: `ios use <branch>` re-targets
// the ONE binding whose project_ref == the linked ref. If the ref matches NO
// binding, or the ref binding has an empty identifier, there is nothing to
// re-target — buildPullSpecConfig errors ("not bound to project ref" /
// "no registered bundle id"), so `use` must fail loudly, NOT print success and
// save DefaultEnv.
func TestIOSUse_ErrorsWhenBranchAppliesToNothing(t *testing.T) {
	cases := []struct {
		name     string
		bindings []map[string]any // apps.listBindings response
		wantErr  string
	}{
		{
			// the ref (todoappm8p6z) is NOT among the app's bindings at all.
			name: "ref matches no binding",
			bindings: []map[string]any{
				{"project_ref": "other0ref", "identifier": "com.demo.other", "env_preset": "production"},
			},
			wantErr: "not bound to project ref",
		},
		{
			// the ref binding EXISTS but its identifier is empty → can't config-match.
			name: "ref binding has empty identifier",
			bindings: []map[string]any{
				{"project_ref": "todoappm8p6z", "identifier": "", "env_preset": "production"},
				{"project_ref": "other0ref", "identifier": "com.demo.other", "env_preset": "dev"},
			},
			wantErr: "no registered bundle id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
				Ref: "todoappm8p6z", DefaultEnv: "main", IOSAppID: "app_ios1",
			}))
			rig, restBase := iosUseRig(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/trpc/apikey.reveal":
					iosTRPCOK(w, map[string]any{
						"endpointRef":    "todoappm8p6zd",
						"publishableKey": "pb_todoappm8p6zd_ckey",
					})
				case "/api/v1/apps/app_ios1/bindings":
					iosRESTOK(w, http.StatusOK, tc.bindings)
				case "/api/v1/apps/app_ios1/config-artifact":
					// Any binding that resolves does so at its own default (main).
					ref := r.URL.Query().Get("env")
					iosRESTOK(w, http.StatusOK, map[string]any{
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
					t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", http.StatusInternalServerError)
				}
			})
			restore := redirectHostTo(t, "todoappm8p6zd.dev.palbase.studio", rig.BaseURL)
			defer restore()

			cmd := newIOSUseCmd(Resolvers{
				Studio:    func() *studio.Client { return rig },
				REST:      func() REST { return iosRESTClientOn(t, restBase) },
				Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
			})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"dev"})
			err := cmd.Execute()

			require.Error(t, err, "use must fail when the branch re-targets nothing")
			require.Contains(t, err.Error(), tc.wantErr)
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
