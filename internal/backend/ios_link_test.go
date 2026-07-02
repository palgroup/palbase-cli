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
	"github.com/palgroup/palbase-cli/internal/studio"
)

// ── tRPC rig (mirrors apps_test.go's studioAgainst, adapted to this package) ──

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

// iosPostInput decodes the inner {"json":{...}} payload of a mutation POST.
func iosPostInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&outer))
	return outer.JSON
}

// iosQueryInput decodes the ?input={"json":{...}} payload of a query GET.
func iosQueryInput(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var outer struct {
		JSON map[string]any `json:"json"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.URL.Query().Get("input")), &outer))
	return outer.JSON
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
		func(_ context.Context, appID, envRef string) (apps.ConfigArtifact, error) {
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
		func(context.Context, string, string) (apps.ConfigArtifact, error) {
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
}

// ── happy path ───────────────────────────────────────────────────────────────

// TestIOSLink_HappyPath: one group (auto-selected), no ios app (created), two
// envs bound via --bundle-id flags, artifacts fetched into out-dir. Locks the
// exact tRPC input field names (groupId/platform/displayName and
// appId/projectRef/identifier — the same input bindCmd sends).
func TestIOSLink_HappyPath(t *testing.T) {
	var createInput map[string]any
	var bindInputs []map[string]any

	sc := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/groups.mine":
			iosTRPCOK(w, []map[string]any{{"id": "grp_1", "name": "Acme", "plan": "free"}})
		case "/api/trpc/apps.list":
			require.Equal(t, "grp_1", iosQueryInput(t, r)["groupId"])
			iosTRPCOK(w, []map[string]any{})
		case "/api/trpc/apps.create":
			createInput = iosPostInput(t, r)
			iosTRPCOK(w, map[string]any{"id": "app_ios1", "platform": "ios", "display_name": "My App"})
		case "/api/trpc/groups.environments":
			require.Equal(t, "grp_1", iosQueryInput(t, r)["grpId"])
			iosTRPCOK(w, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": "Production", "status": "active"},
				{"ref": "stgref", "env_preset": "staging", "env_display_name": nil, "status": "active"},
			})
		case "/api/trpc/apps.configureBinding":
			bindInputs = append(bindInputs, iosPostInput(t, r))
			iosTRPCOK(w, map[string]any{"ok": true})
		default:
			t.Errorf("unexpected tRPC call %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})

	lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "app_ios1")
	outDir := filepath.Join(t.TempDir(), "Palbase")
	var buf bytes.Buffer
	summary, err := runIOSLink(context.Background(), iosLinkDeps{
		studio: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: strings.NewReader(""), interactive: false,
	}, iosLinkOpts{
		ref: "abc1", branch: "main", name: "My App",
		bundleIDs: []string{"prodref=com.x.app", "stgref=com.x.app.stg"},
		outDir:    outDir,
	}, &buf)
	require.NoError(t, err)

	// apps.create input.
	require.Equal(t, "grp_1", createInput["groupId"])
	require.Equal(t, "ios", createInput["platform"])
	require.Equal(t, "My App", createInput["displayName"])

	// configureBinding inputs, in env order.
	require.Len(t, bindInputs, 2)
	require.Equal(t, "app_ios1", bindInputs[0]["appId"])
	require.Equal(t, "prodref", bindInputs[0]["projectRef"])
	require.Equal(t, "com.x.app", bindInputs[0]["identifier"])
	require.Equal(t, "app_ios1", bindInputs[1]["appId"])
	require.Equal(t, "stgref", bindInputs[1]["projectRef"])
	require.Equal(t, "com.x.app.stg", bindInputs[1]["identifier"])

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
	sc := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/groups.mine":
			iosTRPCOK(w, []map[string]any{{"id": "grp_1", "name": "Acme", "plan": "free"}})
		case "/api/trpc/apps.list":
			iosTRPCOK(w, []map[string]any{
				{"id": "web_1", "platform": "web", "display_name": "Site"},
				{"id": "ios_1", "platform": "ios", "display_name": "My App"},
			})
		case "/api/trpc/apps.create":
			t.Error("apps.create must NOT be called when the group already has an ios app")
			http.Error(w, "unexpected create", http.StatusInternalServerError)
		case "/api/trpc/groups.environments":
			iosTRPCOK(w, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": nil, "status": "active"},
				{"ref": "stgref", "env_preset": "staging", "env_display_name": nil, "status": "active"},
			})
		case "/api/trpc/apps.configureBinding":
			iosTRPCOK(w, map[string]any{"ok": true})
		default:
			t.Errorf("unexpected tRPC call %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})

	lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "ios_1")
	summary, err := runIOSLink(context.Background(), iosLinkDeps{
		studio: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
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
	sc := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/groups.mine", r.URL.Path)
		iosTRPCOK(w, []map[string]any{
			{"id": "grp_a", "name": "Acme", "plan": "free"},
			{"id": "grp_b", "name": "Beta", "plan": "pro"},
		})
	})
	lookup, fetch, list, cfgFetch := mustNotPull(t)
	_, err := runIOSLink(context.Background(), iosLinkDeps{
		studio: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
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
	sc := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/groups.mine":
			iosTRPCOK(w, []map[string]any{{"id": "grp_1", "name": "Acme", "plan": "free"}})
		case "/api/trpc/apps.list":
			iosTRPCOK(w, []map[string]any{{"id": "ios_1", "platform": "ios", "display_name": "My App"}})
		case "/api/trpc/groups.environments":
			iosTRPCOK(w, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": nil, "status": "active"},
			})
		default:
			t.Errorf("unexpected tRPC call %s (bind/fetch must not run)", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	lookup, fetch, list, cfgFetch := mustNotPull(t)
	var buf bytes.Buffer
	_, err := runIOSLink(context.Background(), iosLinkDeps{
		studio: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: strings.NewReader(""), interactive: false,
	}, iosLinkOpts{ref: "abc1", branch: "main", outDir: t.TempDir()}, &buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--bundle-id")
	require.Contains(t, buf.String(), "skipping env prodref", "the skipped env must be surfaced")
}

// TestIOSLink_UnknownEnvFlag: a --bundle-id naming a non-existent env is a
// typo guard error, not a silent skip.
func TestIOSLink_UnknownEnvFlag(t *testing.T) {
	sc := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/groups.mine":
			iosTRPCOK(w, []map[string]any{{"id": "grp_1", "name": "Acme", "plan": "free"}})
		case "/api/trpc/apps.list":
			iosTRPCOK(w, []map[string]any{{"id": "ios_1", "platform": "ios", "display_name": "My App"}})
		case "/api/trpc/groups.environments":
			iosTRPCOK(w, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": nil, "status": "active"},
			})
		default:
			t.Errorf("unexpected tRPC call %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})
	lookup, fetch, list, cfgFetch := mustNotPull(t)
	_, err := runIOSLink(context.Background(), iosLinkDeps{
		studio: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
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
	var bindInputs []map[string]any
	sc := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/groups.mine":
			iosTRPCOK(w, []map[string]any{
				{"id": "grp_a", "name": "Acme", "plan": "free"},
				{"id": "grp_b", "name": "Beta", "plan": "pro"},
			})
		case "/api/trpc/apps.list":
			require.Equal(t, "grp_b", iosQueryInput(t, r)["groupId"], "the picked group must be used")
			iosTRPCOK(w, []map[string]any{{"id": "ios_b", "platform": "ios", "display_name": "Beta App"}})
		case "/api/trpc/groups.environments":
			iosTRPCOK(w, []map[string]any{
				{"ref": "prodref", "env_preset": "production", "env_display_name": "Production", "status": "active"},
				{"ref": "stgref", "env_preset": "staging", "env_display_name": nil, "status": "active"},
			})
		case "/api/trpc/apps.configureBinding":
			bindInputs = append(bindInputs, iosPostInput(t, r))
			iosTRPCOK(w, map[string]any{"ok": true})
		default:
			t.Errorf("unexpected tRPC call %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	})

	lookup, fetch, list, cfgFetch := iosStubPullSeams(t, "ios_b")
	// stdin: group choice "2", then bundle id for env 1, then empty (skip env 2).
	stdin := strings.NewReader("2\ncom.typed.app\n\n")
	var buf bytes.Buffer
	summary, err := runIOSLink(context.Background(), iosLinkDeps{
		studio: sc, lookup: lookup, fetch: fetch, list: list, cfgFetch: cfgFetch,
		stdin: stdin, interactive: true,
	}, iosLinkOpts{
		ref: "abc1", branch: "main",
		outDir:    filepath.Join(t.TempDir(), "Palbase"),
		suggested: []string{"com.acme.Todo"},
	}, &buf)
	require.NoError(t, err)

	require.Equal(t, "grp_b", summary.Group)
	require.Equal(t, []iosLinkBinding{{Env: "prodref", BundleID: "com.typed.app"}}, summary.Bindings)
	require.Len(t, bindInputs, 1)
	require.Equal(t, "com.typed.app", bindInputs[0]["identifier"])
	require.Contains(t, buf.String(), "com.acme.Todo", "the prompt must surface the detected bundle ids")
	require.Contains(t, buf.String(), "skipping env stgref")
}
