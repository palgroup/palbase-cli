package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/apps"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// stubPullSeams replaces the four artifact seams so a link test never needs a
// live tenant host. It asserts that the artifact fetch is for the expected app
// and ENVIRONMENT REF (not a branch, not a bare project ref).
func stubPullSeams(t *testing.T, wantApp, wantEnvRef, platform string) nativeLinkDeps {
	t.Helper()
	return nativeLinkDeps{
		publicHost: "dev.palbase.studio",
		lookup: func(_ context.Context, ref string) (backendTarget, error) {
			require.Equal(t, wantEnvRef, ref)
			return backendTarget{URL: "https://" + ref + ".dev.palbase.studio", APIKey: "pb_generic"}, nil
		},
		fetch: func(context.Context, string, string, io.Writer) ([]byte, error) {
			return []byte(`{"openapi":"3.1.0","paths":{}}`), nil
		},
		list: func(_ context.Context, appID string) ([]AppBinding, error) {
			require.Equal(t, wantApp, appID)
			return []AppBinding{{EnvironmentRef: wantEnvRef, Kind: "production"}}, nil
		},
		cfgFetch: func(_ context.Context, appID, envRef string) (apps.ConfigArtifact, error) {
			require.Equal(t, wantApp, appID)
			require.Equal(t, wantEnvRef, envRef)
			return apps.ConfigArtifact{
				AppID: appID, EnvironmentRef: envRef, Kind: "production",
				BaseURL: "https://" + envRef + ".dev.palbase.studio", APIKey: "pb_" + envRef + "_c01234567890123456789",
				Platform: platform,
			}, nil
		},
	}
}

// appsFake answers the v2 PROJECT-scoped app routes.
func appsFake(t *testing.T, existing []map[string]any, created map[string]any, postBody *map[string]any) *selectiontest.Fake {
	t.Helper()
	f := selectiontest.New(t)
	f.OK("GET /api/v2/projects/proj_1/apps", existing)
	f.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, r *http.Request) {
		if postBody != nil {
			require.NoError(t, json.NewDecoder(r.Body).Decode(postBody))
		}
		selectiontest.WriteOK(w, http.StatusCreated, created)
	})
	return f
}

// The link flow registers the app on the PROJECT and fetches config for the
// SELECTED ENVIRONMENT. It touches no group route (there are none) and no
// branch (there is none).
func TestNativeLink_RegistersOnTheProjectAndConfiguresTheEnvironment(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	var body map[string]any
	f := appsFake(t, nil, map[string]any{"id": "app_ios", "platform": "ios"}, &body)

	deps := stubPullSeams(t, "app_ios", "app1prod", "ios")
	deps.rest = f.REST()

	summary, err := runNativeLink(context.Background(), deps, nativeLinkOpts{
		platform: "ios", projectID: "proj_1", environmentRef: "app1prod",
	}, io.Discard)
	require.NoError(t, err)

	require.Equal(t, "proj_1", summary.ProjectID)
	require.Equal(t, "app1prod", summary.EnvironmentRef)
	require.Equal(t, "app_ios", summary.AppID)
	require.Equal(t, filepath.Join(".palbase", "ios"), summary.ConfigDir)

	require.Equal(t, "ios", body["platform"])
	require.NotContains(t, body, "name", "the v2 body is STRICT: it is displayName, not name")

	require.FileExists(t, filepath.Join(".palbase", "openapi.json"))
	require.FileExists(t, filepath.Join(".palbase", "ios", "palbase-config.json"))
	requireNoV1(t, f)
}

// Only the linked platform's slot is written — iOS, macOS, Android and web each
// own an independent registration, and the SELECTION itself is untouched.
func TestNativeLink_PersistsOnlyItsOwnAppSlot(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod",
		RepositoryProvider: selection.ProviderGitHub,
		IOSAppID:           "app_ios", MacOSAppID: "app_macos", WebAppID: "app_web",
	})

	f := appsFake(t, []map[string]any{{"id": "app_ios", "platform": "ios"}},
		map[string]any{"id": "app_android", "platform": "android"}, nil)

	deps := stubPullSeams(t, "app_android", "app1prod", "android")
	deps.rest = f.REST()

	_, err := runNativeLink(context.Background(), deps, nativeLinkOpts{
		platform: "android", projectID: "proj_1", environmentRef: "app1prod",
		identifier: "com.example.todo",
	}, io.Discard)
	require.NoError(t, err)

	cfg, err := selection.Load("")
	require.NoError(t, err)
	require.Equal(t, "app_android", cfg.AndroidAppID)
	require.Equal(t, "app_ios", cfg.IOSAppID)
	require.Equal(t, "app_macos", cfg.MacOSAppID)
	require.Equal(t, "app_web", cfg.WebAppID)
	// The selection survives: linking an app does not re-target the directory.
	require.Equal(t, "proj_1", cfg.ProjectID)
	require.Equal(t, "env_prod", cfg.EnvironmentID)
	require.Equal(t, selection.ProviderGitHub, cfg.RepositoryProvider)
}

func TestPersistProjectAppSlot_CrossProjectSwitchIsAtomic(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_a", EnvironmentID: "env_a",
		RepositoryProvider: selection.ProviderPalbase,
		IOSAppID:           "ios_a", WebAppID: "web_a",
	})
	sel := selection.Selection{
		ProjectID: "proj_b",
		Environment: selection.Environment{
			ID: "env_b", Ref: "envbref", Slug: "staging",
		},
		RepositoryProvider: selection.ProviderGitHub,
	}

	require.NoError(t, persistProjectAppSlot("android", "android_b", &sel, false))
	cfg, err := selection.Load(dir)
	require.NoError(t, err)
	require.Equal(t, "proj_b", cfg.ProjectID)
	require.Equal(t, "env_b", cfg.EnvironmentID)
	require.Equal(t, selection.ProviderGitHub, cfg.RepositoryProvider)
	require.Equal(t, "android_b", cfg.AndroidAppID)
	require.Empty(t, cfg.IOSAppID)
	require.Empty(t, cfg.WebAppID)
}

func TestPersistProjectAppSlot_SameProjectLinkPreservesEnvironment(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod",
		RepositoryProvider: selection.ProviderPalbase,
	})
	sel := selection.Selection{
		ProjectID: "proj_1",
		Environment: selection.Environment{
			ID: "env_stg", Ref: "stgref", Slug: "staging",
		},
		RepositoryProvider: selection.ProviderGitHub,
	}

	require.NoError(t, persistProjectAppSlot("web", "web_1", &sel, false))
	cfg, err := selection.Load(dir)
	require.NoError(t, err)
	require.Equal(t, "env_prod", cfg.EnvironmentID, "link must not retarget within one Project")
	require.Equal(t, selection.ProviderGitHub, cfg.RepositoryProvider)
	require.Equal(t, "web_1", cfg.WebAppID)
}

// A persisted app id that no longer belongs to the selected project (or is the
// wrong platform) is REPLACED — never reused, never guessed at.
func TestNativeLink_StalePersistedAppIsReplaced(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod", IOSAppID: "app_stale",
	})

	f := appsFake(t, []map[string]any{{"id": "app_other", "platform": "ios"}},
		map[string]any{"id": "app_new", "platform": "ios"}, nil)

	deps := stubPullSeams(t, "app_new", "app1prod", "ios")
	deps.rest = f.REST()

	summary, err := runNativeLink(context.Background(), deps, nativeLinkOpts{
		platform: "ios", projectID: "proj_1", environmentRef: "app1prod",
		appID: "app_stale",
	}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "app_new", summary.AppID)

	cfg, _ := selection.Load("")
	require.Equal(t, "app_new", cfg.IOSAppID)
}

// A live registration is REUSED — a re-link must not mint a second app.
func TestNativeLink_RerunReusesTheLiveApp(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod", IOSAppID: "app_ios",
	})

	f := selectiontest.New(t)
	f.OK("GET /api/v2/projects/proj_1/apps", []map[string]any{{"id": "app_ios", "platform": "ios", "display_name": "Phone"}})
	f.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("a live registration must be reused, not duplicated")
	})

	deps := stubPullSeams(t, "app_ios", "app1prod", "ios")
	deps.rest = f.REST()

	summary, err := runNativeLink(context.Background(), deps, nativeLinkOpts{
		platform: "ios", projectID: "proj_1", environmentRef: "app1prod", appID: "app_ios",
	}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "app_ios", summary.AppID)
}

// The created app is persisted BEFORE the artifact fetch, so a fetch failure
// does not orphan a server-side registration the retry would duplicate.
func TestNativeLink_PersistsTheNewAppBeforeFetching(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, nil)

	f := appsFake(t, nil, map[string]any{"id": "app_ios", "platform": "ios"}, nil)
	deps := stubPullSeams(t, "app_ios", "app1prod", "ios")
	deps.rest = f.REST()
	deps.fetch = func(context.Context, string, string, io.Writer) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}

	_, err := runNativeLink(context.Background(), deps, nativeLinkOpts{
		platform: "ios", projectID: "proj_1", environmentRef: "app1prod",
	}, io.Discard)
	require.Error(t, err)

	cfg, cfgErr := selection.Load("")
	require.NoError(t, cfgErr)
	require.Equal(t, "app_ios", cfg.IOSAppID, "the registration must survive a failed fetch")
}

func TestNativeLink_RequiresAndroidApplicationID(t *testing.T) {
	_, err := runNativeLink(context.Background(), nativeLinkDeps{}, nativeLinkOpts{
		platform: "android", projectID: "proj_1", environmentRef: "app1prod",
	}, io.Discard)
	require.ErrorContains(t, err, "applicationId is required")
}

func TestNativeLink_RejectsAnUnknownPlatform(t *testing.T) {
	_, err := runNativeLink(context.Background(), nativeLinkDeps{}, nativeLinkOpts{
		platform: "web", projectID: "proj_1", environmentRef: "app1prod",
	}, io.Discard)
	require.ErrorContains(t, err, "must be ios, macos, or android")
}

// The link commands expose NO --group and NO --ref: the project comes from the
// selection (or the global --project), which is the whole point of the cutover.
func TestNativeLinkCommands_ExposeNoGroupOrRefFlag(t *testing.T) {
	for _, platform := range []string{"ios", "macos", "android"} {
		var names []string
		newNativeLinkCmd(Resolvers{}, platform).Flags().VisitAll(func(f *pflag.Flag) {
			names = append(names, f.Name)
		})
		require.NotContains(t, names, "group", platform)
		require.NotContains(t, names, "ref", platform)
		require.NotContains(t, names, "branch", platform)
	}
}

func TestDetectAndroidApplicationID_KotlinAndGroovy(t *testing.T) {
	for _, tc := range []struct{ name, filename, contents string }{
		{"kotlin", "build.gradle.kts", `android { defaultConfig { applicationId = "com.example.kotlin" } }`},
		{"groovy", "build.gradle", `android { defaultConfig { applicationId 'com.example.groovy' } }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			appDir := filepath.Join(root, "app")
			require.NoError(t, os.MkdirAll(appDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(appDir, tc.filename), []byte(tc.contents), 0o644))
			got, err := detectAndroidApplicationID(root)
			require.NoError(t, err)
			require.Contains(t, tc.contents, got)
		})
	}
}

func TestAndroidLink_PrintsGradleNextSteps(t *testing.T) {
	var out bytes.Buffer
	printNativeNextSteps(&out, "android", filepath.Join(".palbase", "android"))
	for _, want := range []string{
		`implementation("io.palbase:palbe:<version>")`,
		`plugin id("io.palbase.codegen")`,
		`.palbase/android/palbase-config.json`,
		`Palbase.initialize(this)`,
	} {
		require.Contains(t, out.String(), want)
	}
}

// A teammate's fresh clone has no `.palbase/config.json` (it is gitignored CLI
// state) but DOES have the committed platform slot, which names the app this
// checkout is already linked to. Linking there must reuse that app — before
// this, it registered a second app for the same project and rewrote the
// committed api_key. Mutation-evident: drop the slot fallback and the POST
// guard below fires.
func TestNativeLink_FreshCloneReusesTheCommittedAppSlot(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod",
	})
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".palbase", "ios"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".palbase", "ios", "palbase-config.json"),
		[]byte(`{"app_id":"app_ios","environment_ref":"app1prod","base_url":"https://app1prod.dev.palbase.studio","api_key":"pb_app1prod_c01234567890123456789"}`),
		0o644,
	))

	f := selectiontest.New(t)
	f.OK("GET /api/v2/projects/proj_1/apps", []map[string]any{{"id": "app_ios", "platform": "ios", "display_name": "Phone"}})
	f.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("the committed app slot must be reused, not duplicated")
	})

	deps := stubPullSeams(t, "app_ios", "app1prod", "ios")
	deps.rest = f.REST()

	summary, err := runNativeLink(context.Background(), deps, nativeLinkOpts{
		platform: "ios", projectID: "proj_1", environmentRef: "app1prod",
	}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "app_ios", summary.AppID)
}
