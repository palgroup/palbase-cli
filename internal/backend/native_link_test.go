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
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// stubPullSeams replaces the four artifact seams so a link test never needs a
// live tenant host. It asserts that the artifact fetch is for the expected app
// and ENVIRONMENT REF (not a branch, not a bare project ref).
func stubPullSeams(t *testing.T, wantApp, wantEnvRef, platform string) nativeLinkDeps {
	t.Helper()
	// The machine's own local-stack register must not leak a `local` environment
	// into a test's expectations.
	t.Setenv("HOME", t.TempDir())
	return nativeLinkDeps{
		publicHost: "dev.palbase.studio",
		// Sözleşme SEÇİLEN ortam için okunur: ref tek seçicidir ve yanlış
		// ortamın belgesini yazmak, istemciyi başka bir arka uca göre üretmektir.
		fetch: func(_ context.Context, ref string, _ io.Writer) ([]byte, error) {
			require.Equal(t, wantEnvRef, ref)
			return []byte(`{"openapi":"3.1.0","paths":{}}`), nil
		},
		list: func(_ context.Context, appID string) ([]AppBinding, error) {
			require.Equal(t, wantApp, appID)
			return []AppBinding{{EnvironmentRef: wantEnvRef, EnvironmentName: "main", Kind: "production"}}, nil
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

	// One contract PER ENVIRONMENT: the checkout carries them all and the build
	// configuration picks one, so there is no single `.palbase/openapi.json`.
	require.FileExists(t, specPath("main"))
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
	deps.fetch = func(context.Context, string, io.Writer) ([]byte, error) {
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

// A config-less directory linked headlessly (`palbase ios link --project X`)
// used to register the app remotely and THEN fail persistProjectAppSlot's
// unconditional selection.Load("") with "no project selected" — the register
// call has no config to check against, so it always orphaned the just-created
// app. Mutation-evident: restore the unconditional `if err != nil { return
// err }` in persistProjectAppSlot and the first runNativeLink call below turns
// red; the second (which would then register a SECOND app because nothing was
// ever persisted) never even runs.
func TestNativeLink_ProjectFlagWithoutConfig_DoesNotOrphanOrDuplicate(t *testing.T) {
	dir := selectiontest.Chdir(t)
	// Deliberately no selectiontest.WriteConfig: this is the config-less
	// directory `--project proj_1` resolves without ever touching disk
	// (selection.Resolver.Resolve skips Config() once ProjectFlag is set).

	var registered []map[string]any
	var postCount int
	f := selectiontest.New(t)
	f.Handle("GET /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
		selectiontest.WriteOK(w, http.StatusOK, registered)
	})
	f.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
		postCount++
		row := map[string]any{"id": "app_ios", "platform": "ios"}
		registered = append(registered, row)
		selectiontest.WriteOK(w, http.StatusCreated, row)
	})

	deps := stubPullSeams(t, "app_ios", "app1prod", "ios")
	deps.rest = f.REST()

	opts := nativeLinkOpts{
		platform: "ios", projectID: "proj_1",
		environmentID: "env_prod", environmentRef: "app1prod",
		repositoryProvider: selection.ProviderPalbase,
	}

	// First run: the app is registered remotely; persisting it must succeed
	// even though the directory had no config a moment ago.
	summary1, err := runNativeLink(context.Background(), deps, opts, io.Discard)
	require.NoError(t, err, "registering the app remotely must not leave the run failing with 'no project selected'")
	require.Equal(t, "app_ios", summary1.AppID)
	require.Equal(t, 1, postCount)

	cfg, err := selection.Load(dir)
	require.NoError(t, err, "the fresh registration must be persisted, not orphaned")
	require.Equal(t, "proj_1", cfg.ProjectID)
	require.Equal(t, "app_ios", cfg.IOSAppID)

	// Second run: mirrors newNativeLinkCmd re-reading the persisted app id
	// from disk (native_link.go's persistedAppIDFor) before calling
	// runNativeLink again.
	opts.appID = cfg.AppID("ios")
	summary2, err := runNativeLink(context.Background(), deps, opts, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "app_ios", summary2.AppID)
	require.Equal(t, 1, postCount, "the second run must reuse the persisted app, not register a duplicate")
}

// A `.palbase/config.json` that exists but fails to load (corrupt JSON, an
// unsupported schema version) must abort the command BEFORE any remote
// registration. Before persistedAppIDFor gated on the error, the command ran
// resolveNativeApp anyway (persistedAppIDFor swallowed every Load error into
// ""), registered an app remotely, and only THEN hit persistProjectAppSlot's
// (correct) refusal to paper over the same broken config — orphaning the
// registration exactly like the config-less case above. `--project proj_1`
// is required to reach this: without it, Resolver.Resolve reads the same
// broken file itself and fails even earlier, which would pass this test for
// the wrong reason.
func TestNativeLink_BrokenConfig_AbortsBeforeRegisteringAnyApp(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"bad JSON", `{not valid json`},
		{"unsupported version (v1)", `{"version":1,"project_ref":"proj_1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectiontest.Chdir(t)
			selectiontest.WriteRawConfig(t, "", tc.raw)

			var postCount int
			f := selectiontest.New(t)
			// A real (empty) apps list, so a gate bypass proceeds all the way to a
			// genuine POST instead of failing earlier on an unmodeled route — the
			// postCount assertion below must be locking the real gate, not an
			// incidental 404.
			f.OK("GET /api/v2/projects/proj_1/apps", []map[string]any{})
			f.Handle("POST /api/v2/projects/proj_1/apps", func(w http.ResponseWriter, _ *http.Request) {
				postCount++
				selectiontest.WriteOK(w, http.StatusCreated, map[string]any{"id": "app_ios", "platform": "ios"})
			})
			rest := f.REST()
			resolver := &selection.Resolver{
				REST:        func() selection.REST { return rest },
				ProjectFlag: "proj_1", // headless --project: Resolve() never reads the broken config itself
			}
			r := Resolvers{
				REST:      func() REST { return rest },
				Selection: func() *selection.Resolver { return resolver },
				Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
			}

			cmd := newNativeLinkCmd(r, "ios")
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			err := cmd.Execute()
			require.Error(t, err, "a broken local config must fail the command")
			require.Equal(t, 0, postCount, "no app may be registered before the broken config is surfaced")
		})
	}
}

// persistedAppIDFor is the shared read both `native link` and `web link` use
// before registering: a slot from a DIFFERENT project (or no config at all)
// must never be handed to resolve*App as if it already belonged to the
// project being linked.
func TestPersistedAppIDFor_OnlyReusesTheSameProjectSlot(t *testing.T) {
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, &selection.Config{
		ProjectID: "proj_a", EnvironmentID: "env_a", WebAppID: "web_a", IOSAppID: "ios_a",
	})

	webID, err := persistedAppIDFor("web", selection.Selection{ProjectID: "proj_a"})
	require.NoError(t, err)
	require.Equal(t, "web_a", webID)

	iosID, err := persistedAppIDFor("ios", selection.Selection{ProjectID: "proj_a"})
	require.NoError(t, err)
	require.Equal(t, "ios_a", iosID)

	otherID, err := persistedAppIDFor("web", selection.Selection{ProjectID: "proj_b"})
	require.NoError(t, err)
	require.Empty(t, otherID, "a slot from a different project must not be reused")
}

func TestPersistedAppIDFor_NoConfigReturnsEmpty(t *testing.T) {
	selectiontest.Chdir(t)
	id, err := persistedAppIDFor("ios", selection.Selection{ProjectID: "proj_1"})
	require.NoError(t, err, "a config-less directory is the ordinary first-link case, not an error")
	require.Empty(t, id)
}

// A `.palbase/config.json` that exists but fails to load (corrupt JSON, an
// unsupported schema version) must surface as an error, not be silently
// treated the same as "nothing selected yet" — a caller that swallowed this
// would go on to register a remote app it can never persist (see
// TestNativeLink_BrokenConfig_AbortsBeforeRegisteringAnyApp below).
func TestPersistedAppIDFor_BrokenConfigReturnsAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"bad JSON", `{not valid json`},
		{"unsupported version (v1)", `{"version":1,"project_ref":"proj_1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := selectiontest.Chdir(t)
			selectiontest.WriteRawConfig(t, dir, tc.raw)

			id, err := persistedAppIDFor("ios", selection.Selection{ProjectID: "proj_1"})
			require.Error(t, err)
			require.Empty(t, id)
		})
	}
}
