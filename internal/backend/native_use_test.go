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
	"github.com/palgroup/palbase-cli/internal/studio"
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

	// The Studio tRPC client only serves apikey.reveal for the spec target.
	trpc := newTRPCStub(t)

	rest := f.REST()
	return f, Resolvers{
		Studio:    func() *studio.Client { return trpc },
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

func newTRPCStub(t *testing.T) *studio.Client {
	t.Helper()
	url := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			JSON struct {
				Ref string `json:"ref"`
			} `json:"json"`
		}
		require.NoError(t, json.Unmarshal([]byte(r.URL.Query().Get("input")), &input))
		ref := input.JSON.Ref
		trpcOK(w, map[string]any{
			"environment_ref": ref,
			"publishable_key": "pb_" + ref + "_c01234567890123456789",
		})
	})
	return studio.New(
		url,
		func(context.Context) (string, error) { return "tok", nil },
		func(context.Context, string, string, string) (string, error) { return "proof", nil },
	)
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
		{"environment_ref": "app1prod", "kind": "production"},
		{"environment_ref": "app1stg", "kind": "staging"},
	})
	f.OK("GET /api/v2/apps/app_ios/config-artifact", map[string]any{
		"app_id": "app_ios", "environment_ref": "app1stg", "api_key": "pb_app1stg_c01234567890123456789",
		"base_url": "https://app1stg.dev.palbase.studio", "kind": "staging", "platform": "ios",
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

	// The config-artifact was fetched FOR STAGING.
	req, ok := f.Find("GET /api/v2/apps/app_ios/config-artifact")
	require.True(t, ok, "got %v", f.Routes())
	require.Equal(t, "environment_ref=app1stg", req.Query)

	// The emitted codegen config names the environment through the KEY it
	// carries — `pb_app1stg_c…` — and nothing beside it. A field holding a second
	// copy is only a way for the two to disagree, which they did live on
	// 2026-08-16.
	raw, err := os.ReadFile(filepath.Join(".palbase", "ios", "palbase-config.json"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, map[string]any{
		"app_id": "app_ios", "kind": "staging",
		"base_url": "https://app1stg.dev.palbase.studio", "api_key": "pb_app1stg_c01234567890123456789",
	}, got)
	require.NotContains(t, string(raw), "branch")

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
	require.ErrorContains(t, err, "palbase project use proj_2")
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
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
		nil, nil,
		nil, // freshness: these cases predate the check and assert the write path
		"app1prod", "dev.palbase.studio", dir, dir, "", io.Discard)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(dir, "palbase-config.json"))
}

// An app that is NOT bound to the selected environment must fail LOUDLY and
// write nothing — silently emitting another environment's config is how a build
// ends up talking to production from a staging checkout.
func TestRunPullSpec_UnboundEnvironmentWritesNothing(t *testing.T) {
	dir := t.TempDir()
	err := runPullSpec(context.Background(),
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1stg.dev", APIKey: "pb"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			return []byte(`{}`), nil
		},
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{EnvironmentRef: "app1prod"}}, nil
		},
		func(context.Context, string, string) (apps.ConfigArtifact, error) {
			t.Fatal("the artifact must not be fetched for an unbound environment")
			return apps.ConfigArtifact{}, nil
		},
		nil, // freshness: these cases predate the check and assert the write path
		"app1stg", "dev.palbase.studio", dir, dir, "app_ios", io.Discard)
	require.ErrorContains(t, err, "not bound to environment \"app1stg\"")
	require.NoFileExists(t, filepath.Join(dir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(dir, "palbase-config.json"))
}

func TestRunPullSpec_AppConfigSeparatesTheOutputs(t *testing.T) {
	specDir, cfgDir := t.TempDir(), t.TempDir()
	var fetchedURL, fetchedKey string
	err := runPullSpec(context.Background(),
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb_generic"}, nil
		},
		func(_ context.Context, url, key string, _ io.Writer) ([]byte, error) {
			fetchedURL, fetchedKey = url, key
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{EnvironmentRef: "app1prod"}}, nil
		},
		func(_ context.Context, appID, envRef string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{
				AppID: appID, EnvironmentRef: envRef, Kind: "production",
				BaseURL: "https://app1prod.dev.palbase.studio", APIKey: "pb_app1prod_c01234567890123456789", Platform: "ios",
			}, nil
		},
		nil, // freshness: these cases predate the check and assert the write path
		"app1prod", "dev.palbase.studio", specDir, cfgDir, "app_ios", io.Discard)
	require.NoError(t, err)

	// An app link fetches the spec with the APP-BOUND key, not the generic one.
	require.Equal(t, "https://app1prod.dev.palbase.studio/admin/openapi.json", fetchedURL)
	require.Equal(t, "pb_app1prod_c01234567890123456789", fetchedKey)

	require.FileExists(t, filepath.Join(specDir, "openapi.json"))
	raw, err := os.ReadFile(filepath.Join(cfgDir, "palbase-config.json"))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	// The file carries no second copy of the project identity: the key does
	// (`pb_app1prod_c…`), and a field beside it is only a way for the two to
	// disagree — which they did, live, on 2026-08-16.
	require.NotContains(t, got, "environment_ref")
	require.Contains(t, got["api_key"], "app1prod")
	require.NotContains(t, got, "branch")
	require.NotContains(t, got, "env_preset")
}

// TestRunPullSpec_NonWebCheckout_DoesNotCreateAPalbaseDir: the fetch core is
// PLATFORM-AGNOSTIC — it writes the one directory it was handed and nothing
// else. It must never conjure a sibling Palbase/ out of nowhere: that
// directory belongs to `palbase web spec`, which is handed it explicitly.
// (This replaced a heuristic that sniffed for Palbase/palbase-config.json next
// to specOutDir and wrote there too — it guessed wrong whenever --out-dir
// moved. See TestWebSpec_RefreshesOnlyTheWebContract for the new guard.)
func TestRunPullSpec_NonWebCheckout_DoesNotCreateAPalbaseDir(t *testing.T) {
	projectRoot := t.TempDir()
	specDir := filepath.Join(projectRoot, ".palbase")

	err := runPullSpec(context.Background(),
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
		nil, nil,
		nil, // freshness: these cases predate the check and assert the write path
		"app1prod", "dev.palbase.studio", specDir, specDir, "", io.Discard)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(projectRoot, "Palbase"))
	require.True(t, os.IsNotExist(statErr), "a non-web checkout must not get a Palbase/ dir created")
}

func TestRunPullSpec_RejectsForeignHostBeforeTenantNetworkOrWrite(t *testing.T) {
	dir := t.TempDir()
	fetchCalled := false
	err := runPullSpec(context.Background(),
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.example.com", APIKey: "generic"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			fetchCalled = true
			return []byte(`{}`), nil
		},
		func(context.Context, string) ([]AppBinding, error) {
			return []AppBinding{{EnvironmentRef: "app1prod"}}, nil
		},
		func(context.Context, string, string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{
				AppID: "app_ios", EnvironmentRef: "app1prod",
				BaseURL: "https://evil.example",
				APIKey:  "pb_app1prod_c01234567890123456789",
			}, nil
		},
		nil, // freshness: these cases predate the check and assert the write path
		"app1prod", "dev.palbase.studio", dir, dir, "app_ios", io.Discard)

	require.ErrorContains(t, err, "base_url")
	require.False(t, fetchCalled, "an untrusted artifact must not select a tenant network target")
	require.NoFileExists(t, filepath.Join(dir, "openapi.json"))
	require.NoFileExists(t, filepath.Join(dir, "palbase-config.json"))
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
	fetch := func(context.Context, string, string, io.Writer) ([]byte, error) {
		b := []byte(served[min(call, len(served)-1)])
		call++
		return b, nil
	}
	var out bytes.Buffer
	err := runPullSpec(context.Background(),
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb"}, nil
		},
		fetch, nil, nil,
		func(context.Context) (string, error) { return "new", nil },
		"app1prod", "dev.palbase.studio", dir, "", "", &out)
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
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			return []byte(`{"x-palbase-deploy":"old"}`), nil
		},
		nil, nil,
		func(context.Context) (string, error) { return "new", nil },
		"app1prod", "dev.palbase.studio", dir, "", "", io.Discard)
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
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			return []byte(`{"openapi":"3.1.0"}`), nil
		},
		nil, nil,
		func(context.Context) (string, error) { return "new", nil },
		"app1prod", "dev.palbase.studio", dir, "", "", &out))
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.Contains(t, out.String(), "freshness UNVERIFIED")
}

// An environment that has never deployed successfully has nothing to compare
// against — that is not a mismatch, and must not block the first link.
func TestRunPullSpec_NoSuccessfulDeploySkipsTheCheck(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, runPullSpec(context.Background(),
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			return []byte(`{"x-palbase-deploy":"whatever"}`), nil
		},
		nil, nil,
		func(context.Context) (string, error) { return "", nil },
		"app1prod", "dev.palbase.studio", dir, "", "", &out))
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.NotContains(t, out.String(), "waiting for the origin")
}

// A Studio hiccup must not block the fetch — but it must not pass silently as a
// verified one either.
func TestRunPullSpec_FreshnessLookupErrorWarnsAndProceeds(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, runPullSpec(context.Background(),
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			return []byte(`{"x-palbase-deploy":"old"}`), nil
		},
		nil, nil,
		func(context.Context) (string, error) { return "", errors.New("studio unreachable") },
		"app1prod", "dev.palbase.studio", dir, "", "", &out))
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
		func(context.Context, string) (backendTarget, error) {
			return backendTarget{URL: "https://app1prod.dev", APIKey: "pb"}, nil
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			calls++
			return []byte(`{"openapi":"3.1.0","info":{"version":"1.0.0"}}`), nil
		},
		nil, nil,
		func(context.Context) (string, error) { return "8e35fa5f", nil },
		"app1prod", "dev.palbase.studio", dir, "", "", &out)

	require.NoError(t, err, "a contract that cannot prove its freshness must still be written")
	require.Equal(t, 1, calls, "it must not sit in the wait loop for an identity that never arrives")
	require.FileExists(t, filepath.Join(dir, "openapi.json"))
	require.Contains(t, out.String(), "freshness UNVERIFIED")
	require.NotContains(t, out.String(), "waiting for the origin")
	require.NotContains(t, out.String(), "deploy 1.0.0",
		"info.version must never be presented as a deploy identity")
}
