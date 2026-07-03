package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// trpcList writes a tRPC query success envelope carrying a JSON array.
func trpcList(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{"data": map[string]any{"json": data}},
	})
}

// TestAutoResolveSpecApp_SingleGroupExistingApp: the happy path `spec` relies
// on — one group, one ios app → the app id is resolved WITHOUT --app and
// WITHOUT registering anything (spec must never call apps.create).
func TestAutoResolveSpecApp_SingleGroupExistingApp(t *testing.T) {
	c := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/groups.mine":
			trpcList(w, []iosGroupRow{{ID: "grp_1", Name: "My App", Plan: "free"}})
		case "/api/trpc/apps.list":
			require.Equal(t, "grp_1", iosQueryInput(t, r)["groupId"])
			trpcList(w, []iosAppRow{{ID: "app_ios1", Platform: "ios", DisplayName: "iOS"}})
		default:
			t.Fatalf("unexpected path %s (spec must NOT register an app)", r.URL.Path)
		}
	})
	var w bytes.Buffer
	got := autoResolveSpecApp(context.Background(),
		iosLinkDeps{studio: c, stdin: strings.NewReader(""), interactive: false}, "", &w)
	require.Equal(t, "app_ios1", got)
	require.Contains(t, w.String(), "using ios app iOS (app_ios1)")
}

// TestAutoResolveSpecApp_NoIOSApp: a group with no ios app degrades to ""
// (openapi-only) with an actionable pointer — NOT an error.
func TestAutoResolveSpecApp_NoIOSApp(t *testing.T) {
	c := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/groups.mine":
			trpcList(w, []iosGroupRow{{ID: "grp_1", Name: "My App", Plan: "free"}})
		case "/api/trpc/apps.list":
			trpcList(w, []iosAppRow{{ID: "app_web", Platform: "web", DisplayName: "Web"}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
	var w bytes.Buffer
	got := autoResolveSpecApp(context.Background(),
		iosLinkDeps{studio: c, stdin: strings.NewReader(""), interactive: false}, "", &w)
	require.Equal(t, "", got, "no ios app → openapi-only, not an error")
	require.Contains(t, w.String(), "palbase ios link")
}

// TestAutoResolveSpecApp_MultiGroupNonInteractive: >1 group in a non-TTY shell
// can't pick → degrade to "" (openapi-only) with a "pass --app" hint, NOT a
// hard failure. spec must still write openapi.json.
func TestAutoResolveSpecApp_MultiGroupNonInteractive(t *testing.T) {
	c := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/trpc/groups.mine", r.URL.Path)
		trpcList(w, []iosGroupRow{
			{ID: "grp_1", Name: "A", Plan: "free"},
			{ID: "grp_2", Name: "B", Plan: "free"},
		})
	})
	var w bytes.Buffer
	got := autoResolveSpecApp(context.Background(),
		iosLinkDeps{studio: c, stdin: strings.NewReader(""), interactive: false}, "", &w)
	require.Equal(t, "", got)
	require.Contains(t, w.String(), "skipping palbase-config.json")
	require.Contains(t, w.String(), "--app")
}

// TestAutoResolveSpecApp_GroupFlagBypassesPicker: --group short-circuits the
// group resolution even with multiple groups.
func TestAutoResolveSpecApp_GroupFlagBypassesPicker(t *testing.T) {
	c := iosStudio(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/trpc/apps.list":
			require.Equal(t, "grp_2", iosQueryInput(t, r)["groupId"])
			trpcList(w, []iosAppRow{{ID: "app_ios2", Platform: "ios", DisplayName: "iOS2"}})
		default:
			t.Fatalf("groups.mine must not be queried when --group is set (got %s)", r.URL.Path)
		}
	})
	var w bytes.Buffer
	got := autoResolveSpecApp(context.Background(),
		iosLinkDeps{studio: c, stdin: strings.NewReader(""), interactive: false}, "grp_2", &w)
	require.Equal(t, "app_ios2", got)
}
