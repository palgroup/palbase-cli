package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/apps"
	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/palgroup/palbase-cli/internal/transport"
)

func iosTRPCOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

func iosRESTOK(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": "req_x"})
}

func iosREST(t *testing.T, h http.HandlerFunc) *transport.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(srv.URL, key, "pat_test")
}

func iosRESTClientOn(t *testing.T, baseURL string) *transport.Client {
	t.Helper()
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(baseURL, key, "pat_test")
}

func iosUseRig(t *testing.T, h http.HandlerFunc) (*studio.Client, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(context.Context) (string, error) { return "tok", nil }), srv.URL
}

func iosPostBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
	return body
}

func iosStubPullSeams(t *testing.T, wantApp, platform string) (specTargetLookup, remoteSpecFetch, bindingLister, configArtifactFetch) {
	t.Helper()
	return stubTarget("https://generic.example", "pb_generic"),
		func(_ context.Context, specURL, apiKey string, _ io.Writer) ([]byte, error) {
			require.Equal(t, "https://app-bound.example/openapi.json", specURL)
			require.Equal(t, "pb_app_bound", apiKey)
			return []byte(`{"openapi":"3.1.0","paths":{}}`), nil
		},
		func(_ context.Context, appID string) ([]AppBinding, error) {
			require.Equal(t, wantApp, appID)
			return []AppBinding{{ProjectRef: "prodref", EnvPreset: "production"}}, nil
		},
		func(_ context.Context, appID, envRef, branch string) (apps.ConfigArtifact, error) {
			require.Equal(t, wantApp, appID)
			require.Equal(t, "prodref", envRef)
			require.Equal(t, "main", branch)
			return apps.ConfigArtifact{
				AppID: appID, ProjectRef: envRef, Platform: platform,
				EnvPreset: "production", BaseURL: "https://app-bound.example", APIKey: "pb_app_bound",
			}, nil
		}
}

func mustNotPull(t *testing.T) (specTargetLookup, remoteSpecFetch, bindingLister, configArtifactFetch) {
	t.Helper()
	return func(context.Context, string, string) (backendTarget, error) {
			t.Error("spec lookup must not run")
			return backendTarget{}, errors.New("unreachable")
		}, func(context.Context, string, string, io.Writer) ([]byte, error) {
			t.Error("spec fetch must not run")
			return nil, errors.New("unreachable")
		}, func(context.Context, string) ([]AppBinding, error) {
			t.Error("binding list must not run")
			return nil, errors.New("unreachable")
		}, func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			t.Error("config fetch must not run")
			return apps.ConfigArtifact{}, errors.New("unreachable")
		}
}

func TestNativeLinkCommandsExposeOnlyProductSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func(Resolvers) *cobra.Command
	}{
		{"ios", newIOSCmd},
		{"macos", newMacOSCmd},
		{"android", newAndroidCmd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := tc.cmd(noopResolvers())
			var link *cobra.Command
			for _, child := range parent.Commands() {
				if child.Name() == "link" {
					link = child
				}
			}
			require.NotNil(t, link)
			var flags []string
			link.Flags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag.Name) })
			require.Equal(t, []string{"group", "json"}, flags)
		})
	}
}

func TestNativeLink_RequiresCommandPlatform(t *testing.T) {
	lookup, fetch, list, cfgFetch := mustNotPull(t)
	_, err := runNativeLink(context.Background(), nativeLinkDeps{
		rest: fatalRESTDoer{t: t}, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
	}, nativeLinkOpts{branch: "main"}, io.Discard)
	require.ErrorContains(t, err, "must be ios, macos, or android")
}

type fatalRESTDoer struct{ t *testing.T }

func (f fatalRESTDoer) Do(context.Context, string, string, any, any) error {
	f.t.Fatal("must not call management API")
	return nil
}

func TestNativeLink_FirstRunCreatesAppAndUsesFixedSlot(t *testing.T) {
	for _, platform := range []string{"ios", "macos"} {
		t.Run(platform, func(t *testing.T) {
			t.Chdir(t.TempDir())
			sibling := "ios"
			if platform == "ios" {
				sibling = "macos"
			}
			siblingPath := filepath.Join(".palbase", sibling, "palbase-config.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(siblingPath), 0o755))
			require.NoError(t, os.WriteFile(siblingPath, []byte("sibling"), 0o644))

			var createBody map[string]any
			mutations := 0
			rest := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
					iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "grp_1", "name": "Acme"}})
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/environments":
					iosRESTOK(w, http.StatusOK, []map[string]any{{"ref": "prodref", "env_preset": "production"}})
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
					iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "remote_existing", "platform": platform}})
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
					mutations++
					createBody = iosPostBody(t, r)
					iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_new", "platform": platform, "display_name": "Demo"})
				case r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
					mutations++
					t.Fatalf("link must not mutate bindings or existing apps: %s %s", r.Method, r.URL.Path)
				default:
					t.Fatalf("unexpected call %s %s", r.Method, r.URL.Path)
				}
			})
			lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "app_new", platform)
			summary, err := runNativeLink(context.Background(), nativeLinkDeps{
				rest: rest, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
			}, nativeLinkOpts{platform: platform, branch: "main"}, io.Discard)
			require.NoError(t, err)
			require.Equal(t, 1, mutations, "the only mutation is creating this checkout's app")
			require.Equal(t, map[string]any{
				"platform": platform, "name": filepath.Base(mustGetwd(t)),
			}, createBody)
			require.Equal(t, "app_new", summary.AppID)
			require.Equal(t, filepath.Join(".palbase", platform), summary.ConfigDir)
			_, err = os.Stat(filepath.Join(".palbase", "openapi.json"))
			require.NoError(t, err)
			_, err = os.Stat(filepath.Join(".palbase", platform, "palbase-config.json"))
			require.NoError(t, err)
			siblingRaw, err := os.ReadFile(siblingPath)
			require.NoError(t, err)
			require.Equal(t, "sibling", string(siblingRaw))
		})
	}
}

func TestNativeLink_RerunReusesPersistedAppWithoutMutation(t *testing.T) {
	t.Chdir(t.TempDir())
	mutations := 0
	rest := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"ref": "prodref", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"id": "app_saved", "platform": "ios", "display_name": "Saved"},
				{"id": "app_other", "platform": "ios", "display_name": "Other"},
			})
		case r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
			mutations++
			t.Fatalf("rerun must be read-only: %s %s", r.Method, r.URL.Path)
		default:
			t.Fatalf("unexpected call %s %s", r.Method, r.URL.Path)
		}
	})
	lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "app_saved", "ios")
	summary, err := runNativeLink(context.Background(), nativeLinkDeps{
		rest: rest, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
	}, nativeLinkOpts{platform: "ios", group: "grp_1", branch: "main", appID: "app_saved"}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "app_saved", summary.AppID)
	require.Zero(t, mutations)
}

func TestIOSLinkCommand_FirstRunPersistsAndRerunReuses(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile(".gitignore", []byte(".palbase/\n.env.local\n"), 0o644))
	created := false
	postCalls := 0
	mutationCalls := 0
	rig, restBase := iosUseRig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/apikey.reveal":
			iosTRPCOK(w, map[string]any{"endpointRef": "prodm", "publishableKey": "pb_generic"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "grp_1", "name": "Acme"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"ref": "prod", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			rows := []map[string]any{{"id": "other", "platform": "ios", "display_name": "Other"}}
			if created {
				rows = append(rows, map[string]any{"id": "app_ios", "platform": "ios", "display_name": "Local"})
			}
			iosRESTOK(w, http.StatusOK, rows)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			postCalls++
			mutationCalls++
			body := iosPostBody(t, r)
			require.Equal(t, map[string]any{"platform": "ios", "name": filepath.Base(mustGetwd(t))}, body)
			created = true
			iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_ios", "platform": "ios", "display_name": "Local"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_ios/bindings":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"project_ref": "prod", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_ios/config-artifact":
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_ios", "project_ref": "prod", "api_key": "pb_app",
				"base_url": "https://prodm.dev.palbase.studio", "env_preset": "production", "platform": "ios",
			})
		case r.URL.Path == "/auth/oauth/providers":
			require.Empty(t, r.Header.Get("X-Palbase-Bundle"))
			_, _ = w.Write([]byte(`{"providers":{}}`))
		case r.URL.Path == "/openapi.json":
			require.Empty(t, r.Header.Get("X-Palbase-Bundle"))
			_, _ = w.Write([]byte(`{"openapi":"3.1.0"}`))
		case r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
			mutationCalls++
			t.Fatalf("native link must not mutate bindings or existing apps: %s %s", r.Method, r.URL.Path)
		default:
			t.Fatalf("unexpected call %s %s", r.Method, r.URL.String())
		}
	})
	restore := redirectHostTo(t, "prodm.dev.palbase.studio", rig.BaseURL)
	defer restore()
	resolvers := Resolvers{
		Studio:    func() *studio.Client { return rig },
		REST:      func() REST { return iosRESTClientOn(t, restBase) },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}
	for range 2 {
		cmd := newIOSLinkCmd(resolvers)
		cmd.SetOut(io.Discard)
		require.NoError(t, cmd.Execute())
	}
	require.Equal(t, 1, postCalls)
	require.Equal(t, 1, mutationCalls)
	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "prod", linked.Ref)
	require.Equal(t, "app_ios", linked.IOSAppID)
	gitignore, err := os.ReadFile(".gitignore")
	require.NoError(t, err)
	require.Equal(t, ".palbase/config.json\n.env.local\n", string(gitignore))
}

func TestIOSLinkCommand_CrossProductRelinkCreatesAndPersistsReplacement(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "oldprod", DefaultEnv: "main",
		IOSAppID: "app_old_ios", MacOSAppID: "app_old_macos", WebAppID: "app_old_web",
	}))

	postCalls := 0
	rig, restBase := iosUseRig(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/trpc/apikey.reveal":
			iosTRPCOK(w, map[string]any{"endpointRef": "newprodm", "publishableKey": "pb_generic"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_new/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"ref": "newprod", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_new/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "app_other", "platform": "ios"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_new/apps":
			postCalls++
			require.Equal(t, map[string]any{
				"platform": "ios", "name": filepath.Base(mustGetwd(t)),
			}, iosPostBody(t, r))
			iosRESTOK(w, http.StatusCreated, map[string]any{
				"id": "app_new_ios", "platform": "ios", "display_name": "New iOS",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_new_ios/bindings":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"project_ref": "newprod", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/app_new_ios/config-artifact":
			require.Equal(t, "newprod", r.URL.Query().Get("env"))
			iosRESTOK(w, http.StatusOK, map[string]any{
				"app_id": "app_new_ios", "project_ref": "newprod", "api_key": "pb_new_app",
				"base_url": "https://newprodm.dev.palbase.studio", "env_preset": "production", "platform": "ios",
			})
		case r.URL.Path == "/auth/oauth/providers":
			_, _ = w.Write([]byte(`{"providers":{}}`))
		case r.URL.Path == "/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0"}`))
		case r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
			t.Fatalf("relink must only create the replacement app: %s %s", r.Method, r.URL.Path)
		default:
			t.Fatalf("unexpected call %s %s", r.Method, r.URL.String())
		}
	})
	restore := redirectHostTo(t, "newprodm.dev.palbase.studio", rig.BaseURL)
	defer restore()
	resolvers := Resolvers{
		Studio:    func() *studio.Client { return rig },
		REST:      func() REST { return iosRESTClientOn(t, restBase) },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
	}
	cmd := newIOSLinkCmd(resolvers)
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--group", "grp_new"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, 1, postCalls)

	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "newprod", linked.Ref)
	require.Equal(t, "app_new_ios", linked.IOSAppID)
	require.Equal(t, "app_old_macos", linked.MacOSAppID)
	require.Equal(t, "app_old_web", linked.WebAppID)
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return wd
}

func TestNativeLink_StaleOrMismatchedPersistedAppRegistersReplacement(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []map[string]any
	}{
		{"missing from selected product", []map[string]any{{"id": "app_other", "platform": "macos"}}},
		{"wrong platform", []map[string]any{{"id": "app_saved", "platform": "ios"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			postCalls := 0
			var createBody map[string]any
			rest := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/environments":
					iosRESTOK(w, http.StatusOK, []map[string]any{{"ref": "prodref", "env_preset": "production"}})
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
					iosRESTOK(w, http.StatusOK, tc.rows)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
					postCalls++
					createBody = iosPostBody(t, r)
					iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_new", "platform": "macos"})
				case r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete:
					t.Fatalf("replacement must not mutate an existing app: %s %s", r.Method, r.URL.Path)
				default:
					t.Fatalf("unexpected call %s %s", r.Method, r.URL.Path)
				}
			})
			lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "app_new", "macos")
			var out strings.Builder
			summary, err := runNativeLink(context.Background(), nativeLinkDeps{
				rest: rest, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
			}, nativeLinkOpts{
				platform: "macos", group: "grp_1", branch: "main", appID: "app_saved",
			}, &out)
			require.NoError(t, err)
			require.Equal(t, "app_new", summary.AppID)
			require.Equal(t, 1, postCalls)
			require.Equal(t, map[string]any{
				"platform": "macos", "name": filepath.Base(mustGetwd(t)),
			}, createBody)
			require.Contains(t, out.String(), "does not match the selected product and platform")
		})
	}
}

func TestNativeLink_PersistsCreatedAppBeforeArtifactFetchAndRetryReuses(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, auth.SaveProjectConfig(&auth.ProjectConfig{
		Ref: "oldprod", DefaultEnv: "main",
		IOSAppID: "app_stale", MacOSAppID: "app_macos", WebAppID: "app_web",
	}))

	created := false
	postCalls := 0
	rest := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"ref": "prodref", "env_preset": "production"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			rows := []map[string]any{{"id": "app_other", "platform": "ios"}}
			if created {
				rows = append(rows, map[string]any{"id": "app_new", "platform": "ios"})
			}
			iosRESTOK(w, http.StatusOK, rows)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			postCalls++
			if postCalls > 1 {
				t.Fatalf("retry registered a duplicate app")
			}
			created = true
			iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_new", "platform": "ios"})
		default:
			t.Fatalf("unexpected call %s %s", r.Method, r.URL.String())
		}
	})

	configCalls := 0
	lookup := stubTarget("https://generic.example", "pb_generic")
	fetch := func(_ context.Context, specURL, apiKey string, _ io.Writer) ([]byte, error) {
		require.Equal(t, "https://app-bound.example/openapi.json", specURL)
		require.Equal(t, "pb_app", apiKey)
		return []byte(`{"openapi":"3.1.0","paths":{}}`), nil
	}
	list := func(_ context.Context, appID string) ([]AppBinding, error) {
		require.Equal(t, "app_new", appID)
		return []AppBinding{{ProjectRef: "prodref", EnvPreset: "production"}}, nil
	}
	cfgFetch := func(_ context.Context, appID, envRef, branch string) (apps.ConfigArtifact, error) {
		configCalls++
		require.Equal(t, "app_new", appID)
		require.Equal(t, "prodref", envRef)
		require.Equal(t, "main", branch)
		if configCalls == 1 {
			return apps.ConfigArtifact{}, errors.New("temporary config-artifact failure")
		}
		return apps.ConfigArtifact{
			AppID: appID, ProjectRef: envRef, Platform: "ios", EnvPreset: "production",
			BaseURL: "https://app-bound.example", APIKey: "pb_app",
		}, nil
	}
	deps := nativeLinkDeps{rest: rest, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch}

	_, err := runNativeLink(context.Background(), deps, nativeLinkOpts{
		platform: "ios", group: "grp_1", branch: "main", appID: "app_stale",
	}, io.Discard)
	require.ErrorContains(t, err, "temporary config-artifact failure")

	linked, err := auth.LoadProjectConfig()
	require.NoError(t, err)
	require.Equal(t, "prodref", linked.Ref)
	require.Equal(t, "app_new", linked.IOSAppID)
	require.Equal(t, "app_macos", linked.MacOSAppID)
	require.Equal(t, "app_web", linked.WebAppID)

	_, err = runNativeLink(context.Background(), deps, nativeLinkOpts{
		platform: "ios", group: "grp_1", branch: "main", appID: linked.IOSAppID,
	}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, 1, postCalls)
}

func TestNativeLink_MultipleProductsRequiresSelection(t *testing.T) {
	rest := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/groups", r.URL.Path)
		iosRESTOK(w, http.StatusOK, []map[string]any{
			{"id": "grp_a", "name": "A"}, {"id": "grp_b", "name": "B"},
		})
	})
	lookup, fetch, list, cfgFetch := mustNotPull(t)
	_, err := runNativeLink(context.Background(), nativeLinkDeps{
		rest: rest, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: strings.NewReader(""), interactive: false,
	}, nativeLinkOpts{platform: "ios", branch: "main"}, io.Discard)
	require.ErrorContains(t, err, "pass --group")
}
