package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/apps"
	"github.com/palgroup/palbase-cli/internal/config"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/selectiontest"
)

// useRig wires `palbase <platform> use <environment>` against the fake v2 API +
// a stub tenant host, with production AND staging seeded.
func useRig(t *testing.T, cfg *selection.Config) (*selectiontest.Fake, Resolvers) {
	t.Helper()
	dir := selectiontest.Chdir(t)
	selectiontest.WriteConfig(t, dir, cfg)

	f := selectiontest.New(t)
	f.Environments["proj_1"] = append(f.Environments["proj_1"],
		selectiontest.Env("env_stg", "proj_1", "app1stg", "staging", "staging", false))

	// The tenant host: openapi.json + the OAuth probe.
	tenant := newTenantStub(t)
	t.Cleanup(redirectHostTo(t, "app1stg.dev.palbase.studio", tenant))
	t.Cleanup(redirectHostTo(t, "app1prod.dev.palbase.studio", tenant))

	// ORTAMIN YAYINLANABİLİR ANAHTARI YÖNETİM API'SİNDEN OKUNUYOR.
	//
	// Eskiden Studio'nun tRPC yüzeyinden (`apikey.reveal`) geliyordu ve o yol
	// DPoP'la bağlanmış bir TARAYICI oturumu istiyor; headless bir koşumda öyle
	// bir oturum yok ve komut "refresh tokens: 401" ile ölüyordu. Anahtar iki
	// ortam için de ref'e BAĞLI kayıtlanıyor — istemci bağı doğruluyor.
	for _, ref := range []string{"app1prod", "app1stg"} {
		f.OK("GET /api/v2/environments/"+ref+"/apikey", map[string]any{
			"environment_ref": ref,
			"publishable_key": "pb_" + ref + "_c01234567890123456789",
		})
		// SÖZLEŞME DE YÖNETİM API'SİNDEN. Tenant belgeyi yalnız `service_role`
		// anahtarına sunuyor ve CLI'ın taşıdığı anahtar yayınlanabilir olan.
		f.OK("GET /api/v2/environments/"+ref+"/openapi", map[string]any{
			"openapi": "3.1.0", "paths": map[string]any{},
		})
	}

	// A native checkout is configured for EVERY environment it is bound to, so
	// both routes are part of the standard rig.
	for _, app := range []string{"app_ios", "app_macos", "app_android"} {
		f.OK("GET /api/v2/apps/"+app+"/bindings", []map[string]any{
			{"environment_ref": "app1prod", "environment_name": "main", "kind": "production"},
		})
		f.Handle("GET /api/v2/apps/"+app+"/config-artifact", func(w http.ResponseWriter, r *http.Request) {
			ref := r.URL.Query().Get("environment_ref")
			selectiontest.WriteOK(w, http.StatusOK, map[string]any{
				"app_id": app, "environment_ref": ref,
				"api_key":  "pb_project_c0123456789012345678901234567890",
				"base_url": "https://" + ref + ".dev.palbase.studio", "kind": "primary", "platform": "ios",
			})
		})
	}

	rest := f.REST()
	return f, Resolvers{
		REST:      func() REST { return rest },
		Endpoints: func() config.Endpoints { return config.Endpoints{PublicHost: "dev.palbase.studio"} },
		Selection: func() *selection.Resolver { return f.Resolver() },
	}
}

func newTenantStub(t *testing.T) string {
	t.Helper()
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","paths":{}}`))
		case "/auth/oauth/providers":
			_, _ = w.Write([]byte(`{"providers":{}}`))
		default:
			t.Fatalf("unexpected tenant call %s", r.URL.Path)
		}
	})
	return srv
}

// `<platform> use <environment>` re-targets the ENVIRONMENT: it rewrites the
// codegen config, records environment_id in the selection, and NEVER writes a
// branch anywhere. This is UAT SDK-003/SDK-004: the generated config selects an
// Environment, not a branch.
func TestNativeUse_RetargetsTheEnvironment(t *testing.T) {
	stubSwiftgenSources(t)
	f, r := useRig(t, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod",
		RepositoryProvider: selection.ProviderPalbase, IOSAppID: "app_ios", MacOSAppID: "app_macos",
	})
	f.OK("GET /api/v2/projects/proj_1/apps", []map[string]any{{"id": "app_ios", "platform": "ios", "display_name": "Phone"}})
	f.OK("GET /api/v2/apps/app_ios/bindings", []map[string]any{
		{"environment_ref": "app1prod", "environment_name": "main", "kind": "production"},
		{"environment_ref": "app1stg", "environment_name": "staging", "kind": "staging"},
	})
	// EVERY environment is configured, so the artifact answers PER ENVIRONMENT.
	f.Handle("GET /api/v2/apps/app_ios/config-artifact", func(w http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("environment_ref")
		selectiontest.WriteOK(w, http.StatusOK, map[string]any{
			"app_id": "app_ios", "environment_ref": ref,
			"api_key":  "pb_project_c0123456789012345678901234567890",
			"base_url": "https://" + ref + ".dev.palbase.studio", "kind": "primary", "platform": "ios",
		})
	})

	// A macOS config already on disk must survive: the platforms are independent.
	macCfg := filepath.Join(".palbase", "macos", "palbase-config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(macCfg), 0o755))
	require.NoError(t, os.WriteFile(macCfg, []byte("mac-sentinel"), 0o644))

	cmd := newNativeUseCmd(r, "ios")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"staging"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.NoError(t, cmd.Execute())

	// The config-artifact was fetched for EVERY environment — the checkout
	// carries them all — and the DEFAULT moved to staging.
	_, ok := f.Find("GET /api/v2/apps/app_ios/config-artifact")
	require.True(t, ok, "got %v", f.Routes())

	raw, err := os.ReadFile(filepath.Join(".palbase", "ios", "palbase-config.json"))
	require.NoError(t, err)
	var got appEnvironments
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "staging", got.Default, "`use` moves which environment a plain build gets")
	require.Equal(t, []string{"main", "staging"}, got.names())
	require.Equal(t, "https://app1stg.dev.palbase.studio", got.Environments["staging"].BaseURL)
	require.Equal(t, "https://app1prod.dev.palbase.studio", got.Environments["main"].BaseURL)
	require.NotContains(t, string(raw), "branch")

	// One build configuration per environment: that is where the choice is made.
	require.FileExists(t, filepath.Join("Palbase", "Config", "Staging.xcconfig"))
	require.FileExists(t, filepath.Join("Palbase", "Config", "Main.xcconfig"))

	// The SELECTION now points at staging — a build made now connects there, so
	// the local selection and the baked config must not disagree.
	cfg, err := selection.Load("")
	require.NoError(t, err)
	require.Equal(t, "env_stg", cfg.EnvironmentID)
	require.Equal(t, "app_ios", cfg.IOSAppID)

	// macOS untouched.
	mac, err := os.ReadFile(macCfg)
	require.NoError(t, err)
	require.Equal(t, "mac-sentinel", string(mac))

	require.Contains(t, out.String(), "targets environment staging")
	requireNoV1(t, f)
}

func TestNativeUse_RequiresALinkedApp(t *testing.T) {
	_, r := useRig(t, &selection.Config{ProjectID: "proj_1", EnvironmentID: "env_prod"})
	cmd := newNativeUseCmd(r, "ios")
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"staging"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.ErrorContains(t, cmd.Execute(), "run `palbase ios link` first")
}

func TestNativeUse_UnknownEnvironmentIsNamed(t *testing.T) {
	_, r := useRig(t, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod", IOSAppID: "app_ios",
	})
	cmd := newNativeUseCmd(r, "ios")
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"qa"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	require.ErrorContains(t, err, `no environment "qa"`)
}

func TestNativeUse_ProjectOverrideCannotSwitchTheLinkedProject(t *testing.T) {
	f, r := useRig(t, &selection.Config{
		ProjectID: "proj_1", EnvironmentID: "env_prod", IOSAppID: "app_ios",
	})
	f.Projects = append(f.Projects, selectiontest.Project{
		ID: "proj_2", Name: "other", Mode: "github",
	})
	f.Environments["proj_2"] = []selection.Environment{
		selectiontest.Env("env_2_stg", "proj_2", "app2stg", "staging", "staging", false),
	}
	resolver := f.Resolver()
	resolver.ProjectFlag = "proj_2"
	r.Selection = func() *selection.Resolver { return resolver }

	cmd := newNativeUseCmd(r, "ios")
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"staging"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	require.ErrorContains(t, err, "cannot switch projects")
	require.ErrorContains(t, err, "palbase link proj_2")
	require.NotContains(t, err.Error(), "project use",
		"`palbase project use` does not exist — advising it sends people nowhere")
	require.Empty(t, f.Routes())

	cfg, loadErr := selection.Load("")
	require.NoError(t, loadErr)
	require.Equal(t, "proj_1", cfg.ProjectID)
	require.Equal(t, "env_prod", cfg.EnvironmentID)
}

// ── runPullSpec ─────────────────────────────────────────────────────────────

func TestRunPullSpec_WithoutAnApp_WritesOnlyTheContract(t *testing.T) {
	dir := t.TempDir()
	err := runPullSpec(context.Background(),
		func(context.Context, string, io.Writer) ([]byte, error) {
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
		nil, // freshness: these cases predate the check and assert the write path
		"app1prod", dir, io.Discard)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(dir, "palbase-config.json"))
}

// ── gatherCloudEnvironments (the cloud half of the multi-environment model) ──

// isolatedCheckout gives a test its own working directory and its own machine
// state, so `.palbase/…` writes land in a temp dir and the local-stack register
// of the machine running the suite cannot leak a `local` environment in.
func isolatedCheckout(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func artifactFor(base string) configArtifactFetch {
	return func(_ context.Context, appID, envRef string) (apps.ConfigArtifact, error) {
		return apps.ConfigArtifact{
			AppID: appID, EnvironmentRef: envRef, Kind: "primary", Platform: "ios",
			BaseURL: "https://" + envRef + "." + base,
			APIKey:  "pb_project_c0123456789012345678901234567890",
		}, nil
	}
}

// HER ORTAM İNİYOR — sadece seçilen değil.
//
// An app that holds only the environment somebody linked last is an app whose
// address depends on WHEN it was built. The build configuration must be able to
// choose, and it can only choose among what the checkout carries.
func TestGatherCloudEnvironments_CarriesEveryBoundEnvironment(t *testing.T) {
	isolatedCheckout(t)
	var fetched []string
	d := cloudEnvDeps{
		publicHost: "dev.palbase.studio",
		list: func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{
				{EnvironmentRef: "app1prod", EnvironmentName: "main"},
				{EnvironmentRef: "app1stg", EnvironmentName: "staging"},
			}, nil
		},
		cfgFetch: artifactFor("dev.palbase.studio"),
		fetch: func(_ context.Context, ref string, _ io.Writer) ([]byte, error) {
			fetched = append(fetched, ref)
			return []byte(`{"openapi":"3.1.0","paths":{}}`), nil
		},
	}
	envs, specs, err := gatherCloudEnvironments(context.Background(), d, "app_ios", "app1prod", "proj_1", io.Discard)
	require.NoError(t, err)
	require.Equal(t, []string{"main", "staging"}, envs.names())
	require.Equal(t, "main", envs.Default, "the selected environment is what a plain build gets")
	require.ElementsMatch(t, []string{"app1prod", "app1stg"}, fetched)
	require.Len(t, specs, 2)
	require.FileExists(t, specPath("main"))
	require.FileExists(t, specPath("staging"))
	// Keys are per environment, and each entry names its own address.
	require.Equal(t, "https://app1stg.dev.palbase.studio", envs.Environments["staging"].BaseURL)
}

// An app that is NOT bound to the selected environment must fail LOUDLY and
// write nothing — silently emitting another environment's config is how a build
// ends up talking to production from a staging checkout.
func TestGatherCloudEnvironments_UnboundSelectionWritesNothing(t *testing.T) {
	isolatedCheckout(t)
	d := cloudEnvDeps{
		publicHost: "dev.palbase.studio",
		list: func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{EnvironmentRef: "app1prod", EnvironmentName: "main"}}, nil
		},
		cfgFetch: func(context.Context, string, string) (apps.ConfigArtifact, error) {
			t.Fatal("the artifact must not be fetched for an unbound environment")
			return apps.ConfigArtifact{}, nil
		},
		fetch: func(context.Context, string, io.Writer) ([]byte, error) {
			t.Fatal("no contract may be fetched for an unbound environment")
			return nil, nil
		},
	}
	_, _, err := gatherCloudEnvironments(context.Background(), d, "app_ios", "app1stg", "proj_1", io.Discard)
	require.ErrorContains(t, err, "not bound to environment \"app1stg\"")
	require.NoFileExists(t, specPath("main"))
}

// The artifact is UNTRUSTED here: a base_url on a foreign host must be refused
// BEFORE it can select a network target or reach a file.
func TestGatherCloudEnvironments_RejectsForeignHostBeforeTenantNetworkOrWrite(t *testing.T) {
	isolatedCheckout(t)
	fetchCalled := false
	d := cloudEnvDeps{
		publicHost: "dev.palbase.studio",
		list: func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{EnvironmentRef: "app1prod", EnvironmentName: "main"}}, nil
		},
		cfgFetch: func(context.Context, string, string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{
				AppID: "app_ios", EnvironmentRef: "app1prod",
				BaseURL: "https://evil.example",
				APIKey:  "pb_project_c0123456789012345678901234567890",
			}, nil
		},
		fetch: func(context.Context, string, io.Writer) ([]byte, error) {
			fetchCalled = true
			return []byte(`{}`), nil
		},
	}
	_, _, err := gatherCloudEnvironments(context.Background(), d, "app_ios", "app1prod", "proj_1", io.Discard)
	require.ErrorContains(t, err, "base_url")
	require.False(t, fetchCalled, "an untrusted artifact must not select a tenant network target")
	require.NoFileExists(t, specPath("main"))
}

// A NON-selected environment whose backend is down still gets its config entry
// and its build configuration: a configuration that vanishes because a pod was
// restarting is a build that breaks for a reason nobody connects to the pod.
func TestGatherCloudEnvironments_ADownSiblingKeepsItsConfiguration(t *testing.T) {
	isolatedCheckout(t)
	d := cloudEnvDeps{
		publicHost: "dev.palbase.studio",
		list: func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{
				{EnvironmentRef: "app1prod", EnvironmentName: "main"},
				{EnvironmentRef: "app1stg", EnvironmentName: "staging"},
			}, nil
		},
		cfgFetch: artifactFor("dev.palbase.studio"),
		fetch: func(_ context.Context, ref string, _ io.Writer) ([]byte, error) {
			if ref == "app1stg" {
				return nil, errors.New("backend did not wake")
			}
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
	}
	envs, specs, err := gatherCloudEnvironments(context.Background(), d, "app_ios", "app1prod", "proj_1", io.Discard)
	require.NoError(t, err)
	require.Contains(t, envs.Environments, "staging", "the entry survives a backend that is down")
	require.NotContains(t, specs, "staging", "but no contract is invented for it")
	require.FileExists(t, specPath("main"))
	require.NoFileExists(t, specPath("staging"))
}

// The SELECTED environment is different: linking a checkout it cannot build is
// not a link.
func TestGatherCloudEnvironments_ADownSelectionFails(t *testing.T) {
	isolatedCheckout(t)
	d := cloudEnvDeps{
		publicHost: "dev.palbase.studio",
		list: func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{EnvironmentRef: "app1prod", EnvironmentName: "main"}}, nil
		},
		cfgFetch: artifactFor("dev.palbase.studio"),
		fetch: func(context.Context, string, io.Writer) ([]byte, error) {
			return nil, errors.New("backend did not wake")
		},
	}
	_, _, err := gatherCloudEnvironments(context.Background(), d, "app_ios", "app1prod", "proj_1", io.Discard)
	require.ErrorContains(t, err, "did not wake")
}

// ── spec freshness (the stale-contract gate) ────────────────────────────────

func TestSpecDocVersion(t *testing.T) {
	require.Equal(t, "9f1c2ab", specDocVersion([]byte(`{"x-palbase-deploy":"9f1c2ab"}`)))
	require.Equal(t, "", specDocVersion([]byte(`{"openapi":"3.1.0"}`)),
		"an omitted extension is no identity — and needs no placeholder to say so")
	require.Equal(t, "", specDocVersion([]byte(`{"info":{"version":"1.0.0"}}`)),
		"info.version is NOT the identity; a document carrying only it names no deploy")
	require.Equal(t, "", specDocVersion([]byte(`not json`)), "an unreadable body must not throw")
}

// The reported defect: the origin kept serving the PREVIOUS deploy for ~8s after
// the deploy reported success (a warm isolate re-reads ACTIVE.json once per
// ACTIVE_RECHECK_MS window), and `palbase spec` wrote that stale contract to
// disk without a word — codegen then emitted a client for a deploy ago and said
// "✓ success". It must wait for the origin to catch up.
func TestRunPullSpec_WaitsForTheExpectedDeployThenWrites(t *testing.T) {
	dir := t.TempDir()
	served := []string{`{"x-palbase-deploy":"old"}`, `{"x-palbase-deploy":"old"}`, `{"x-palbase-deploy":"new"}`}
	call := 0
	fetch := func(context.Context, string, io.Writer) ([]byte, error) {
		b := []byte(served[min(call, len(served)-1)])
		call++
		return b, nil
	}
	var out bytes.Buffer
	err := runPullSpec(context.Background(),
		fetch,
		func(context.Context) (string, error) { return "new", nil },
		"app1prod", dir, &out)
	require.NoError(t, err)
	require.Equal(t, 3, call, "it kept fetching until the origin served the expected deploy")

	b, readErr := os.ReadFile(filepath.Join(dir, "openapi.json"))
	require.NoError(t, readErr)
	require.Contains(t, string(b), `"new"`, "only the fresh contract reaches disk")
	require.Contains(t, out.String(), "deploy new", "the success line names what it wrote")
}

// A contract nobody can trust is worse ON DISK than absent: on disk it becomes a
// committed client. So the wait ends in an error, not in a write.
func TestRunPullSpec_FailsRatherThanWriteAStaleContract(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := runPullSpec(ctx,
		func(context.Context, string, io.Writer) ([]byte, error) {
			return []byte(`{"x-palbase-deploy":"old"}`), nil
		},
		func(context.Context) (string, error) { return "new", nil },
		"app1prod", dir, io.Discard)
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(dir, "openapi.json"),
		"a stale contract must never reach disk")
}

// Absence is not evidence: an artifact built before the deploy stamp existed
// cannot be checked, and the user must be TOLD that rather than left to assume
// the check passed.
func TestRunPullSpec_UnverifiableSpecIsWrittenWithALoudNote(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, runPullSpec(context.Background(),
		func(context.Context, string, io.Writer) ([]byte, error) {
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
		func(context.Context) (string, error) { return "new", nil },
		"app1prod", dir, &out))
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.Contains(t, out.String(), "freshness UNVERIFIED")
}

// An environment that has never deployed successfully has nothing to compare
// against — that is not a mismatch, and must not block the first link.
func TestRunPullSpec_NoSuccessfulDeploySkipsTheCheck(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, runPullSpec(context.Background(),
		func(context.Context, string, io.Writer) ([]byte, error) {
			return []byte(`{"x-palbase-deploy":"whatever"}`), nil
		},
		func(context.Context) (string, error) { return "", nil },
		"app1prod", dir, &out))
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.NotContains(t, out.String(), "waiting for the origin")
}

// A Studio hiccup must not block the fetch — but it must not pass silently as a
// verified one either.
func TestRunPullSpec_FreshnessLookupErrorWarnsAndProceeds(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, runPullSpec(context.Background(),
		func(context.Context, string, io.Writer) ([]byte, error) {
			return []byte(`{"x-palbase-deploy":"old"}`), nil
		},
		func(context.Context) (string, error) { return "", errors.New("studio unreachable") },
		"app1prod", dir, &out))
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.Contains(t, out.String(), "could not verify spec freshness")
}

// TestRunPullSpec_UnstampedContractIsUnverifiableNotStale is the regression a
// live run found. A runtime older than the deploy-identity stamp serves a
// document with no x-palbase-deploy at all, and reading that as a MISMATCH
// blocked `palbase spec` outright — observed against a real Environment:
//
//	waiting for the origin to serve deploy 8e35fa5f (it is still on 1.0.0)…
//	… is still serving deploy 1.0.0 but the latest successful deploy is 8e35fa5f
//
// (that run predates the move to the extension, when the placeholder in
// info.version was mistaken for a deploy). The contract was current; the origin
// simply could not prove it. That is the UNVERIFIED path — write it, say so —
// never the refuse-to-write one, or every tenant on an unstamped artifact loses
// the command entirely.
func TestRunPullSpec_UnstampedContractIsUnverifiableNotStale(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	calls := 0
	err := runPullSpec(context.Background(),
		func(context.Context, string, io.Writer) ([]byte, error) {
			calls++
			return []byte(`{"openapi":"3.1.0","info":{"version":"1.0.0"}}`), nil
		},
		func(context.Context) (string, error) { return "8e35fa5f", nil },
		"app1prod", dir, &out)

	require.NoError(t, err, "a contract that cannot prove its freshness must still be written")
	require.Equal(t, 1, calls, "it must not sit in the wait loop for an identity that never arrives")
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.Contains(t, out.String(), "freshness UNVERIFIED")
	require.NotContains(t, out.String(), "waiting for the origin")
	require.NotContains(t, out.String(), "deploy 1.0.0",
		"info.version must never be presented as a deploy identity")
}

// YEREL YIĞIN DA BİR ORTAMDIR.
//
// The stack on this machine is an environment every app checkout gets for free:
// a developer must be able to build against their own laptop and against
// production by switching a build configuration, not by re-linking. The entry is
// written even when the stack is registered but NOT answering — a build
// configuration that disappears because a container was stopped is an app that
// stops compiling for a reason nobody connects to the container.
func TestGatherCloudEnvironments_CarriesTheLocalStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".palbase"), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, ".palbase", "local-stacks.json"),
		[]byte(`{"stacks":{"proj_1":{"url":"http://127.0.0.1:54321"}}}`), 0o600))

	d := cloudEnvDeps{
		publicHost: "dev.palbase.studio",
		list: func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{EnvironmentRef: "app1prod", EnvironmentName: "main"}}, nil
		},
		cfgFetch: artifactFor("dev.palbase.studio"),
		fetch: func(context.Context, string, io.Writer) ([]byte, error) {
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
	}
	envs, _, err := gatherCloudEnvironments(context.Background(), d, "app_ios", "app1prod", "proj_1", io.Discard)
	require.NoError(t, err)
	require.Equal(t, []string{"local", "main"}, envs.names())
	require.Equal(t, "http://127.0.0.1:54321", envs.Environments["local"].BaseURL)
	require.Empty(t, envs.Environments["local"].APIKey,
		"a stack this machine holds no credential for is listed keyless, never invented")
	require.Equal(t, "main", envs.Default, "local is available, but a plain build still gets the cloud")
}

// SİLİNEN BİR ORTAMIN İSTEMCİSİ HÂLÂ DERLENİR — ve bu yüzden kalamaz.
//
// The xcconfig excludes the OTHER KNOWN environments by name, so a directory
// left behind by an environment that was renamed is excluded by nothing: Xcode
// compiles it, and the app links a client for an address that is gone. Measured
// on the centauri checkout, where an environment named by its ref left
// `Palbase/Generated/8bbwb2pbm/` beside `main/`.
func TestPruneRemovedEnvironments_DeletesOnlyOurOrphans(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	orphan := filepath.Join(root, generatedDir, "8bbwb2pbm")
	keep := filepath.Join(root, generatedDir, "main")
	handwritten := filepath.Join(root, generatedDir, "Helpers")
	for _, d := range []string{orphan, keep, handwritten} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(orphan, "PalbaseGenerated.swift"), []byte("//"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(keep, "PalbaseGenerated.swift"), []byte("//"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(handwritten, "Extras.swift"), []byte("//"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".palbase", "openapi"), 0o755))
	require.NoError(t, os.WriteFile(specPath("8bbwb2pbm"), []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Palbase", "Config"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "Palbase", "Config", xcconfigName("8bbwb2pbm")), []byte("//"), 0o644))

	envs := appEnvironments{Default: "main", Environments: map[string]appEnvironment{"main": {}}}
	require.NoError(t, pruneRemovedEnvironments(root, envs, io.Discard))

	require.NoDirExists(t, orphan)
	require.NoFileExists(t, specPath("8bbwb2pbm"))
	require.NoFileExists(t, filepath.Join(root, "Palbase", "Config", xcconfigName("8bbwb2pbm")))
	require.FileExists(t, filepath.Join(keep, "PalbaseGenerated.swift"))
	// A directory that is not ours is not ours: nothing a person wrote is removed.
	require.FileExists(t, filepath.Join(handwritten, "Extras.swift"))
}
