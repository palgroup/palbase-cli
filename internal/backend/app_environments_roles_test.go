package backend

// app_environments_roles_test.go — the spec round brings the stack's ROLE
// DEFINITIONS down beside its contract.
//
// The paths are asserted as LITERALS on purpose. Two generators in two other
// repositories read this artifact — palbase-swiftgen (T010) and palbe-gen
// (T011) — and neither can import a helper from here, so where the file lands
// is a cross-repository contract rather than an implementation detail. A test
// that computed the path from the same function the writer uses would agree
// with itself while the generators looked somewhere else.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The two places a generator looks. `.palbase/openapi/<env>.json` is the
// native contract, so the roles for that environment sit beside it; the web
// SDK reads one contract out of `Palbase/`, so its roles sit beside that one.
const (
	nativeRolesForMain = ".palbase/openapi/main.roles.json"
	webRoles           = "Palbase/roles.json"
)

// specRoundStack stands up a stack that answers the two questions a spec round
// asks it, and links this checkout to it with a web slot present.
func specRoundStack(t *testing.T, roles http.HandlerFunc) *httptest.Server {
	t.Helper()
	isolatedCheckout(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/management/openapi":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{}}`))
		case "/admin/roles":
			roles(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, WriteTarget(Target{URL: srv.URL}))
	require.NoError(t, StoreCredential(srv.URL, Credentials{Kind: KindKey, Value: "pb_secret_test"}))

	// The web slot. `palbe-gen` reads its contract from Palbase/, and
	// linkedPlatforms decides a checkout is a web one by this file existing.
	require.NoError(t, os.MkdirAll(webArtifactsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(webArtifactsDir, "palbase-config.json"),
		[]byte(`{"app_id":"app_1","base_url":"`+srv.URL+`","api_key":"pb_x"}`), 0o600))
	return srv
}

// readRolesDoc decodes an artifact this run wrote.
func readRolesDoc(t *testing.T, path string) stackRoles {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoErrorf(t, err, "no roles artifact at %s", path)
	var doc stackRoles
	require.NoError(t, json.Unmarshal(raw, &doc))
	return doc
}

func servedRoles(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// A spec round against a stack that defines roles writes them where both
// generators read, and writes what a TYPE needs and nothing else.
func TestSpecRoles_LandBesideTheContract(t *testing.T) {
	// Deliberately out of order, and with unsorted permissions: the artifact is
	// COMMITTED, so two runs against an unchanged stack must produce identical
	// bytes rather than a diff that depends on how the rows came back.
	specRoundStack(t, servedRoles(`{"roles":[
		{"name":"support","isDefault":false,"description":"Helpdesk",
		 "permissions":["tickets.close","tickets.read"],"userCount":7},
		{"name":"member","isDefault":true,"description":"",
		 "permissions":["todos.write","todos.read"],"userCount":41}
	]}`))

	require.NoError(t, RefreshSpec(t.Context(), os.Stderr))

	for _, path := range []string{nativeRolesForMain, webRoles} {
		doc := readRolesDoc(t, path)
		require.Len(t, doc.Roles, 2, "%s", path)

		require.Equal(t, "member", doc.Roles[0].Name, "%s: roles are ordered by name", path)
		require.True(t, doc.Roles[0].IsDefault, "%s", path)
		require.Equal(t, []string{"todos.read", "todos.write"}, doc.Roles[0].Permissions,
			"%s: permissions are ordered", path)

		require.Equal(t, "support", doc.Roles[1].Name, "%s", path)
		require.Equal(t, "Helpdesk", doc.Roles[1].Description, "%s", path)
		require.Equal(t, []string{"tickets.close", "tickets.read"}, doc.Roles[1].Permissions, "%s", path)

		// userCount is RUNTIME STATE. A committed artifact that carried it would
		// change every time somebody signed up, and the types it produces would
		// not have changed at all.
		require.NotContains(t, string(mustRead(t, path)), "userCount", "%s", path)
	}
}

// A STACK OLDER THAN THE ROLES SURFACE IS NOT A BROKEN SPEC ROUND. It answers
// 404, which is an answer — "I define none" — so the artifact is written empty
// and the round the person asked for succeeds. Failing here would break
// `palbase spec` in every project that has no RBAC yet.
func TestSpecRoles_OldStackWritesEmptyAndDoesNotFail(t *testing.T) {
	specRoundStack(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	require.NoError(t, RefreshSpec(t.Context(), os.Stderr))

	for _, path := range []string{nativeRolesForMain, webRoles} {
		doc := readRolesDoc(t, path)
		require.NotNil(t, doc.Roles, "%s: an empty list, never null — null reads as \"unknown\"", path)
		require.Empty(t, doc.Roles, "%s", path)
	}
}

// "COULD NOT TELL" IS NOT "THERE ARE NONE". A refusal the stack did not answer
// with a 404 leaves the artifact on disk alone: overwriting it with an empty
// list would delete role definitions this run could not produce, and the next
// build would compile with every permission constant silently gone.
func TestSpecRoles_ARefusalLeavesTheArtifactAlone(t *testing.T) {
	specRoundStack(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	known := []byte(`{"roles":[{"name":"admin","isDefault":false,"permissions":["todos.delete_any"]}]}` + "\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(nativeRolesForMain), 0o755))
	require.NoError(t, os.WriteFile(nativeRolesForMain, known, 0o644))
	require.NoError(t, os.WriteFile(webRoles, known, 0o644))

	require.NoError(t, RefreshSpec(t.Context(), os.Stderr), "the contract was fetched; roles are an addendum")

	for _, path := range []string{nativeRolesForMain, webRoles} {
		require.Equal(t, known, mustRead(t, path), "%s was overwritten with what the stack never said", path)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}
