package backend

import (
	"bytes"
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
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/apps"
	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/studio"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// ── tRPC rig (apikey.reveal is still tRPC; apps/groups moved to REST) ──

// iosTRPCOK writes a tRPC success envelope ({result:{data:{json:...}}}).
func iosTRPCOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// iosStudio spins an httptest server and returns a *studio.Client backed by it.
func iosStudio(t *testing.T, h http.HandlerFunc) *studio.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return studio.New(srv.URL, func(context.Context) (string, error) { return "tok", nil })
}

// ── REST rig (mirrors apikey_test.go's restAgainst) — apps + groups ──

// iosRESTOK writes the /api/v1 success envelope ({data, request_id}).
func iosRESTOK(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": "req_x"})
}

// iosREST spins an httptest server and returns a real *transport.Client backed
// by it — so the DPoP Do + {data,request_id} envelope unwrap match production.
func iosREST(t *testing.T, h http.HandlerFunc) *transport.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(srv.URL, key, "pat_test")
}

// iosRESTClientOn returns a *transport.Client pointed at an ALREADY-running
// server URL (used by ios_use_test.go where one server serves both the tRPC
// reveal and the REST apps routes).
func iosRESTClientOn(t *testing.T, baseURL string) *transport.Client {
	t.Helper()
	key, err := auth.NewDPoPKey()
	require.NoError(t, err)
	return transport.New(baseURL, key, "pat_test")
}

// iosUseRig spins ONE httptest server that serves BOTH the tRPC surface
// (apikey.reveal) and the REST surface (apps bindings + config-artifact), and
// returns a *studio.Client bound to it plus its base URL (for a transport.Client
// pointed at the same server). `palbase ios use` mixes both transports —
// reveal is tRPC, apps are REST — so both clients must reach the same handler.
func iosUseRig(t *testing.T, h http.HandlerFunc) (*studio.Client, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sc := studio.New(srv.URL, func(context.Context) (string, error) { return "tok", nil })
	return sc, srv.URL
}

// iosPostBody decodes the JSON request body of a REST POST/PUT.
func iosPostBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
	return body
}

// iosStubPullSeams returns lookup/fetch/list/cfgFetch seams that satisfy
// runPullSpec without a network: two bound envs whose artifacts key the
// palbase-config.json.
func iosStubPullSeams(t *testing.T, wantApp string) (specTargetLookup, remoteSpecFetch, bindingLister, configArtifactFetch) {
	t.Helper()
	ids := map[string]string{"prodref": "com.x.app", "stgref": "com.x.app.stg"}
	return stubTarget("https://abc1m.dev.palbase.studio", "pb_abc1m_ckey"),
		stubFetch(`{"openapi":"3.1.0","paths":{}}`, nil),
		func(_ context.Context, appID string) ([]AppBinding, error) {
			require.Equal(t, wantApp, appID)
			return []AppBinding{
				{ProjectRef: "prodref", Identifier: "com.x.app", EnvPreset: "production"},
				{ProjectRef: "stgref", Identifier: "com.x.app.stg", EnvPreset: "staging"},
			}, nil
		},
		func(_ context.Context, appID, envRef, _ string) (apps.ConfigArtifact, error) {
			return apps.ConfigArtifact{
				AppID: appID, ProjectRef: envRef, Identifier: ids[envRef],
				EnvPreset: "production", BaseURL: "https://x", APIKey: "pb_x",
			}, nil
		}
}

// mustNotPull returns runPullSpec seams that fail the test when reached — for
// scenarios that must error out BEFORE the fetch step.
func mustNotPull(t *testing.T) (specTargetLookup, remoteSpecFetch, bindingLister, configArtifactFetch) {
	t.Helper()
	return func(context.Context, string, string) (backendTarget, error) {
			t.Error("spec lookup must not run")
			return backendTarget{}, errors.New("unreachable")
		},
		func(context.Context, string, string, io.Writer) ([]byte, error) {
			t.Error("spec fetch must not run")
			return nil, errors.New("unreachable")
		},
		func(context.Context, string) ([]AppBinding, error) {
			t.Error("binding list must not run")
			return nil, errors.New("unreachable")
		},
		func(context.Context, string, string, string) (apps.ConfigArtifact, error) {
			t.Error("config fetch must not run")
			return apps.ConfigArtifact{}, errors.New("unreachable")
		}
}

// ── pbxproj parsing ──────────────────────────────────────────────────────────

func TestParsePBXBundleIDs(t *testing.T) {
	const multiConfig = `
	/* Debug */ = {
		buildSettings = {
			PRODUCT_BUNDLE_IDENTIFIER = "com.acme.Todo.dev";
			SWIFT_VERSION = 6.0;
		};
	};
	/* Release */ = {
		buildSettings = {
			PRODUCT_BUNDLE_IDENTIFIER = com.acme.Todo;
		};
	};
	/* Tests — build-setting ref must be dropped */ = {
		buildSettings = {
			PRODUCT_BUNDLE_IDENTIFIER = "$(BUNDLE_PREFIX).Todo";
		};
	};
	/* duplicate of Release */ = {
		buildSettings = {
			PRODUCT_BUNDLE_IDENTIFIER = com.acme.Todo;
		};
	};`

	for _, tc := range []struct {
		name    string
		content string
		want    []string
	}{
		{"empty", "", nil},
		{"no assignments", "SWIFT_VERSION = 6.0;", nil},
		{"debug/release + $(VAR) dropped + dedupe + sorted", multiConfig, []string{"com.acme.Todo", "com.acme.Todo.dev"}},
		{"bare value", `PRODUCT_BUNDLE_IDENTIFIER = com.q.App;`, []string{"com.q.App"}},
		{"quoted value", `PRODUCT_BUNDLE_IDENTIFIER = "com.q.App";`, []string{"com.q.App"}},
		{"only build-setting refs", `PRODUCT_BUNDLE_IDENTIFIER = "$(X)";`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, parsePBXBundleIDs(tc.content))
		})
	}
}

func TestDetectXcodeBundleIDs(t *testing.T) {
	dir := t.TempDir()
	require.Nil(t, detectXcodeBundleIDs(dir), "no .xcodeproj → nil (best-effort)")

	proj := filepath.Join(dir, "Todo.xcodeproj")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "project.pbxproj"),
		[]byte(`PRODUCT_BUNDLE_IDENTIFIER = "com.acme.Todo";`), 0o644))
	require.Equal(t, []string{"com.acme.Todo"}, detectXcodeBundleIDs(dir))
}

// ── flag parsing ─────────────────────────────────────────────────────────────

func TestParseBundleIDFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      []string
		want    map[string]string
		wantErr string
	}{
		{"empty", nil, map[string]string{}, ""},
		{"two envs", []string{"prod=com.a", "stg=com.a.stg"}, map[string]string{"prod": "com.a", "stg": "com.a.stg"}, ""},
		{"missing =", []string{"prodcom.a"}, nil, "expected <envRef>=<bundleId>"},
		{"empty env", []string{"=com.a"}, nil, "expected <envRef>=<bundleId>"},
		{"empty id", []string{"prod="}, nil, "expected <envRef>=<bundleId>"},
		{"conflicting duplicate", []string{"prod=com.a", "prod=com.b"}, nil, "twice"},
		{"agreeing duplicate ok", []string{"prod=com.a", "prod=com.a"}, map[string]string{"prod": "com.a"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBundleIDFlags(tc.in)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// ── command tree ─────────────────────────────────────────────────────────────

// TestIOSCmd_Tree pins the `palbase ios link` surface: the parent group, the
// link child, and the documented flag defaults. Constructing with zero-value
// Resolvers must not panic (clients resolve at RunE time).
func TestIOSCmd_Tree(t *testing.T) {
	cmd := newIOSCmd(noopResolvers())
	require.Equal(t, "ios", cmd.Name())

	var link *cobra.Command
	for _, c := range cmd.Commands() {
		if c.Name() == "link" {
			link = c
		}
	}
	require.NotNil(t, link, "ios must have a `link` subcommand")

	for _, tc := range []struct{ name, def string }{
		{"ref", ""},
		{"group", ""},
		{"app", ""},
		{"name", ""},
		{"out-dir", "./Palbase"},
		{"json", "false"},
	} {
		f := link.Flags().Lookup(tc.name)
		require.NotNilf(t, f, "missing --%s flag", tc.name)
		require.Equalf(t, tc.def, f.DefValue, "--%s default", tc.name)
	}
	require.NotNil(t, link.Flags().Lookup("bundle-id"), "missing repeatable --bundle-id flag")

	// ios also has a `use <branch>` child (re-target an existing wiring).
	var use *cobra.Command
	for _, c := range cmd.Commands() {
		if c.Name() == "use" {
			use = c
		}
	}
	require.NotNil(t, use, "ios must have a `use` subcommand")
	require.NotNil(t, use.Flags().Lookup("ref"))
	require.NotNil(t, use.Flags().Lookup("app"))
	require.NotNil(t, use.Flags().Lookup("out-dir"))
}

// ── happy path ───────────────────────────────────────────────────────────────

// TestIOSLink_HappyPath: one group (auto-selected), no ios app (created), two
// envs bound via --bundle-id flags, artifacts fetched into out-dir. Locks the
// exact tRPC input field names (groupId/platform/displayName and
// appId/projectRef/identifier — the same input bindCmd sends).
func TestIOSLink_HappyPath(t *testing.T) {
	var createBody map[string]any
	var createPath string
	type bindCall struct {
		path string
		body map[string]any
	}
	var binds []bindCall

	sc := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "grp_1", "name": "Acme", "plan": "free"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			createPath = r.URL.Path
			createBody = iosPostBody(t, r)
			iosRESTOK(w, http.StatusCreated, map[string]any{"id": "app_ios1", "platform": "ios", "display_name": "My App"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": "Production", "status": "active"},
				{"ref": "stgref", "env_preset": "staging", "env_display_name": nil, "status": "active"},
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/apps/app_ios1/bindings/"):
			binds = append(binds, bindCall{path: r.URL.Path, body: iosPostBody(t, r)})
			iosRESTOK(w, http.StatusOK, map[string]any{"ok": true})
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})

	lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "app_ios1")
	outDir := filepath.Join(t.TempDir(), "Palbase")
	var buf bytes.Buffer
	summary, err := runIOSLink(context.Background(), iosLinkDeps{
		rest: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: strings.NewReader(""), interactive: false,
	}, iosLinkOpts{
		ref: "abc1", branch: "main", name: "My App",
		bundleIDs: []string{"prodref=com.x.app", "stgref=com.x.app.stg"},
		outDir:    outDir,
	}, &buf)
	require.NoError(t, err)

	// create app: POST to the group's apps route, {platform,name} body.
	require.Equal(t, "/api/v1/groups/grp_1/apps", createPath)
	require.Equal(t, "ios", createBody["platform"])
	require.Equal(t, "My App", createBody["name"])
	require.NotContains(t, createBody, "groupId", "group id rides in the PATH")
	require.NotContains(t, createBody, "displayName", "the field is `name`")

	// bind calls, in env order: appId + projectRef in the PATH, {identifier} body.
	require.Len(t, binds, 2)
	require.Equal(t, "/api/v1/apps/app_ios1/bindings/prodref", binds[0].path)
	require.Equal(t, "com.x.app", binds[0].body["identifier"])
	require.Equal(t, "/api/v1/apps/app_ios1/bindings/stgref", binds[1].path)
	require.Equal(t, "com.x.app.stg", binds[1].body["identifier"])

	// Artifacts landed in out-dir (spec + bundle-id-keyed config).
	for _, f := range []string{"openapi.json", "palbase-config.json"} {
		_, statErr := os.Stat(filepath.Join(outDir, f))
		require.NoErrorf(t, statErr, "%s must be written to out-dir", f)
	}

	// Summary (the --json shape).
	require.Equal(t, "grp_1", summary.Group)
	require.Equal(t, "app_ios1", summary.AppID)
	require.Equal(t, outDir, summary.OutDir)
	require.Equal(t, []iosLinkBinding{
		{Env: "prodref", BundleID: "com.x.app"},
		{Env: "stgref", BundleID: "com.x.app.stg"},
	}, summary.Bindings)
}

// ── idempotent second run ────────────────────────────────────────────────────

// TestIOSLink_SecondRunReusesExistingApp: apps.list already returns an ios app
// → apps.create must NOT be called; the existing app id is reused for binding
// and fetch.
func TestIOSLink_SecondRunReusesExistingApp(t *testing.T) {
	sc := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "grp_1", "name": "Acme", "plan": "free"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"id": "web_1", "platform": "web", "display_name": "Site"},
				{"id": "ios_1", "platform": "ios", "display_name": "My App"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/grp_1/apps":
			t.Error("create app must NOT be called when the group already has an ios app")
			http.Error(w, "unexpected create", http.StatusInternalServerError)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/grp_1/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": nil, "status": "active"},
				{"ref": "stgref", "env_preset": "staging", "env_display_name": nil, "status": "active"},
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/apps/ios_1/bindings/"):
			iosRESTOK(w, http.StatusOK, map[string]any{"ok": true})
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})

	lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "ios_1")
	summary, err := runIOSLink(context.Background(), iosLinkDeps{
		rest: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: strings.NewReader(""), interactive: false,
	}, iosLinkOpts{
		ref: "abc1", branch: "main",
		bundleIDs: []string{"prodref=com.x.app", "stgref=com.x.app.stg"},
		outDir:    filepath.Join(t.TempDir(), "Palbase"),
	}, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "ios_1", summary.AppID, "the existing ios app must be reused")
}

// ── non-interactive missing info ─────────────────────────────────────────────

// TestIOSLink_MultipleGroupsNonInteractive: >1 group, no --group, no TTY →
// actionable error listing the groups.
func TestIOSLink_MultipleGroupsNonInteractive(t *testing.T) {
	sc := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/groups", r.URL.Path)
		iosRESTOK(w, http.StatusOK, []map[string]any{
			{"id": "grp_a", "name": "Acme", "plan": "free"},
			{"id": "grp_b", "name": "Beta", "plan": "pro"},
		})
	})
	lookup, fetch, list, cfgFetch := mustNotPull(t)
	_, err := runIOSLink(context.Background(), iosLinkDeps{
		rest: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: strings.NewReader(""), interactive: false,
	}, iosLinkOpts{ref: "abc1", branch: "main", outDir: t.TempDir()}, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pass --group")
	require.Contains(t, err.Error(), "grp_a")
	require.Contains(t, err.Error(), "grp_b")
}

// TestIOSLink_NoBindingsNonInteractive: envs exist but no --bundle-id flags on
// a non-TTY → every env is skipped and the run fails with actionable guidance
// (before any bind or fetch).
func TestIOSLink_NoBindingsNonInteractive(t *testing.T) {
	sc := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/groups":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "grp_1", "name": "Acme", "plan": "free"}})
		case r.URL.Path == "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "ios_1", "platform": "ios", "display_name": "My App"}})
		case r.URL.Path == "/api/v1/groups/grp_1/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": nil, "status": "active"},
			})
		default:
			t.Errorf("unexpected call %s %s (bind/fetch must not run)", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	lookup, fetch, list, cfgFetch := mustNotPull(t)
	var buf bytes.Buffer
	_, err := runIOSLink(context.Background(), iosLinkDeps{
		rest: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: strings.NewReader(""), interactive: false,
	}, iosLinkOpts{ref: "abc1", branch: "main", outDir: t.TempDir()}, &buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--bundle-id")
	require.Contains(t, buf.String(), "skipping env prodref", "the skipped env must be surfaced")
}

// TestIOSLink_UnknownEnvFlag: a --bundle-id naming a non-existent env is a
// typo guard error, not a silent skip.
func TestIOSLink_UnknownEnvFlag(t *testing.T) {
	sc := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/groups":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "grp_1", "name": "Acme", "plan": "free"}})
		case "/api/v1/groups/grp_1/apps":
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "ios_1", "platform": "ios", "display_name": "My App"}})
		case "/api/v1/groups/grp_1/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": nil, "status": "active"},
			})
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	lookup, fetch, list, cfgFetch := mustNotPull(t)
	_, err := runIOSLink(context.Background(), iosLinkDeps{
		rest: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: strings.NewReader(""), interactive: false,
	}, iosLinkOpts{
		ref: "abc1", branch: "main", outDir: t.TempDir(),
		bundleIDs: []string{"tpyoref=com.x.app"},
	}, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown environment "tpyoref"`)
	require.Contains(t, err.Error(), "prodref", "the error must list the real environments")
}

// ── interactive path ─────────────────────────────────────────────────────────

// TestIOSLink_InteractivePickerAndPrompts: two groups → numbered picker
// (choice 2); two envs → first gets a typed bundle id, second skipped by empty
// input. The prompt shows the pbxproj-detected suggestions.
func TestIOSLink_InteractivePickerAndPrompts(t *testing.T) {
	type bindCall struct {
		path string
		body map[string]any
	}
	var binds []bindCall
	sc := iosREST(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/groups":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"id": "grp_a", "name": "Acme", "plan": "free"},
				{"id": "grp_b", "name": "Beta", "plan": "pro"},
			})
		case r.URL.Path == "/api/v1/groups/grp_b/apps" && r.Method == http.MethodGet:
			iosRESTOK(w, http.StatusOK, []map[string]any{{"id": "ios_b", "platform": "ios", "display_name": "Beta App"}})
		case r.URL.Path == "/api/v1/groups/grp_b/environments":
			iosRESTOK(w, http.StatusOK, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": "Production", "status": "active"},
				{"ref": "stgref", "env_preset": "staging", "env_display_name": nil, "status": "active"},
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/apps/ios_b/bindings/"):
			binds = append(binds, bindCall{path: r.URL.Path, body: iosPostBody(t, r)})
			iosRESTOK(w, http.StatusOK, map[string]any{"ok": true})
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})

	lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "ios_b")
	// stdin: group choice "2", then bundle id for env 1, then empty (skip env 2).
	stdin := strings.NewReader("2\ncom.typed.app\n\n")
	var buf bytes.Buffer
	summary, err := runIOSLink(context.Background(), iosLinkDeps{
		rest: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: stdin, interactive: true,
	}, iosLinkOpts{
		ref: "abc1", branch: "main",
		outDir:    filepath.Join(t.TempDir(), "Palbase"),
		suggested: []string{"com.acme.Todo"},
	}, &buf)
	require.NoError(t, err)

	require.Equal(t, "grp_b", summary.Group)
	require.Equal(t, []iosLinkBinding{{Env: "prodref", BundleID: "com.typed.app"}}, summary.Bindings)
	require.Len(t, binds, 1)
	require.Equal(t, "/api/v1/apps/ios_b/bindings/prodref", binds[0].path)
	require.Equal(t, "com.typed.app", binds[0].body["identifier"])
	require.Contains(t, buf.String(), "com.acme.Todo", "the prompt must surface the detected bundle ids")
	require.Contains(t, buf.String(), "skipping env stgref")
}
