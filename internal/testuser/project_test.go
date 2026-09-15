package testuser

// `test-user` was the last verb that could only talk to the cloud. In a checkout
// linked to a stack — which is what `palbase start` produces — every subcommand
// answered "no project selected — run `palbase project use <projectId>`" — advice
// that cannot be followed there and that names a concept the checkout does not
// have. These tests pin the routing: a linked checkout reaches the PROJECT, at
// the route a deploy already uses for the same job.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/backend"
)

func TestMintCleanupDeletesOnlyReturnedUsersOnTheOriginalProject(t *testing.T) {
	for _, tc := range []struct {
		name              string
		firstDeleteStatus int
		wantFailure       bool
	}{
		{"success", http.StatusNoContent, false},
		{"already deleted", http.StatusNotFound, false},
		{"partial cleanup failure", http.StatusForbidden, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch r.Method + " " + r.URL.Path {
				case "GET /v1/management/test-users/templates":
					_, _ = w.Write([]byte(`{"templates":[]}`))
				case "POST /v1/management/test-users":
					_, _ = w.Write([]byte(`{"users":[{"user_id":"usr_new1","email":"one@test.invalid"},{"user_id":"usr_new2","email":"two@test.invalid"},{"user_id":"usr_new1"},{}]}`))
				case "DELETE /v1/management/test-users/usr_new1":
					w.WriteHeader(tc.firstDeleteStatus)
				case "DELETE /v1/management/test-users/usr_new2":
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request (preexisting users must be untouched): %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer srv.Close()
			linkedTo(t, srv.URL)
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			_, cleanup, err := MintIdentities(cmd, 2)
			require.NoError(t, err)
			require.NotNil(t, cleanup)
			// A user's test script may relink its checkout. Cleanup must retain
			// the authority and address used to mint, without resolving again.
			require.NoError(t, backend.WriteLocalTarget(backend.Target{URL: "http://127.0.0.1:1", Local: true}))
			err = cleanup(context.Background())
			if tc.wantFailure {
				require.ErrorContains(t, err, "delete test user usr_new1")
				require.ErrorContains(t, err, "403")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []string{
				"GET /v1/management/test-users/templates",
				"POST /v1/management/test-users",
				"DELETE /v1/management/test-users/usr_new1",
				"DELETE /v1/management/test-users/usr_new2",
			}, calls)
		})
	}
}

func TestMintCleanupHonorsItsDeadlineAndReportsEveryRemainingID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/management/test-users/templates" {
			_, _ = w.Write([]byte(`{"templates":[]}`))
			return
		}
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"users":[{"user_id":"usr_new1"},{"user_id":"usr_new2"}]}`))
			return
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	linkedTo(t, srv.URL)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	_, cleanup, err := MintIdentities(cmd, 2)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = cleanup(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "usr_new1")
	require.ErrorContains(t, err, "usr_new2")
	require.Less(t, time.Since(start), 2*time.Second)
}

// A PROJECT THAT DECLARES ITS FIXTURES GETS THEM BY NAME. `palbase test --live`
// minted `count` anonymous accounts whatever the stack had been told a test user
// can be, so a suite written against `signInAs("author")` found `user1` and
// `user2` instead.
//
// The name is the one ASKED FOR. The stack's mint answer carries none —
// `{email, password, user_id, access_token, inserted}` is the whole credential
// (v2 admin_users_handlers.go) — so a name read from the answer falls back to the
// position, which is exactly the fake below: it answers the way the stack does.
func TestMintIdentitiesProducesEveryDeclaredFixtureByName(t *testing.T) {
	var calls []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/management/test-users/templates":
			_, _ = w.Write([]byte(`{"templates":[{"name":"author","email":"","tables":["posts"]},{"name":"reader","email":"","tables":[]}]}`))
		case "POST /v1/management/test-users":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies = append(bodies, body)
			n := len(bodies)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"users":[{"email":"fixture%d@test.invalid","password":"pw%d","user_id":"usr_%d","access_token":"tok%d","inserted":{"posts":1}}]}`, n, n, n, n)
		case "DELETE /v1/management/test-users/usr_1", "DELETE /v1/management/test-users/usr_2":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	linkedTo(t, srv.URL)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	raw, cleanup, err := MintIdentities(cmd, 2)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	require.Equal(t, []string{
		"GET /v1/management/test-users/templates",
		"POST /v1/management/test-users",
		"POST /v1/management/test-users",
	}, calls, "one mint per declared fixture, after reading what is declared")
	require.Len(t, bodies, 2)
	for i, want := range []string{"author", "reader"} {
		require.Equal(t, want, bodies[i]["template"], "mint %d did not name its fixture: %v", i+1, bodies[i])
		require.Equal(t, float64(1), bodies[i]["count"], "a fixture is one identity: %v", bodies[i])
		require.Equal(t, true, bodies[i]["with_tokens"], "a fixture nobody can sign in as is a row: %v", bodies[i])
	}

	var payload struct {
		Identities map[string]map[string]any `json:"identities"`
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.Len(t, payload.Identities, 2, "identities: %s", raw)
	require.Equal(t, map[string]any{"id": "usr_1", "email": "fixture1@test.invalid", "password": "pw1", "accessToken": "tok1"},
		payload.Identities["author"], "identities: %s", raw)
	require.Equal(t, map[string]any{"id": "usr_2", "email": "fixture2@test.invalid", "password": "pw2", "accessToken": "tok2"},
		payload.Identities["reader"], "identities: %s", raw)

	require.NoError(t, cleanup(context.Background()))
	require.Equal(t, []string{
		"DELETE /v1/management/test-users/usr_1",
		"DELETE /v1/management/test-users/usr_2",
	}, calls[3:], "cleanup must cover every fixture this run minted")
}

// A stack that declares NO fixtures keeps today's counted mint — one request, no
// template — and the person is told why the identities are `user1`, `user2`
// rather than the names their suite might expect.
func TestMintIdentitiesWithoutDeclaredFixturesKeepsTheCountedMint(t *testing.T) {
	var calls []string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/management/test-users/templates":
			_, _ = w.Write([]byte(`{"templates":[]}`))
		case "POST /v1/management/test-users":
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"users":[{"user_id":"usr_1","email":"a@test.invalid","password":"p"},{"user_id":"usr_2","email":"b@test.invalid","password":"p"}]}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	linkedTo(t, srv.URL)
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&stderr)
	cmd.SetContext(context.Background())

	raw, _, err := MintIdentities(cmd, 2)
	require.NoError(t, err)
	require.Equal(t, []string{
		"GET /v1/management/test-users/templates",
		"POST /v1/management/test-users",
	}, calls)
	_, named := body["template"]
	require.False(t, named, "a counted mint names no template: %v", body)
	require.Equal(t, float64(2), body["count"])

	var payload struct {
		Identities map[string]any `json:"identities"`
	}
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.Contains(t, payload.Identities, "user1")
	require.Contains(t, payload.Identities, "user2")
	require.Contains(t, stderr.String(), "declares no fixture accounts",
		"the person is not told why the identities have no names")
	require.Contains(t, stderr.String(), "palbase test-user templates set")
}

// linkedTo points a scratch checkout at srv and gives it a credential, the way
// `palbase start` leaves one.
func linkedTo(t *testing.T, url string) (backend.Target, backend.Credentials) {
	t.Helper()
	dir := t.TempDir()
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	target := backend.Target{URL: url, Local: true}
	if err := backend.WriteLocalTarget(target); err != nil {
		t.Fatal(err)
	}
	cred := backend.Credentials{Value: "service-role-key", Kind: backend.KindKey}
	if err := backend.StoreCredential(url, cred); err != nil {
		t.Fatal(err)
	}
	got, _, err := resolveProject(&cobra.Command{})
	if err != nil {
		t.Fatalf("a checkout with a local target and a credential did not resolve: %v", err)
	}
	if got.URL != url {
		t.Fatalf("resolved %q, want %q", got.URL, url)
	}
	return got, cred
}

func TestCreateMintsAtTheProjectsOwnDoor(t *testing.T) {
	var seen struct {
		method string
		path   string
		body   map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&seen.body)
		_, _ = w.Write([]byte(`{"users":[{"user_id":"usr_1","email":"t@test.invalid","password":"pw","access_token":"tok"}]}`))
	}))
	defer srv.Close()
	target, cred := linkedTo(t, srv.URL)

	var out bytes.Buffer
	if err := createOnProject(context.Background(), target, cred, 1, "", false, &out); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The SAME route a deploy calls to materialise config/test-users.ts. Two
	// doors would be two answers to "what is a test user".
	if seen.method != http.MethodPost || seen.path != "/v1/management/test-users" {
		t.Errorf("asked %s %s, want POST /v1/management/test-users", seen.method, seen.path)
	}
	// with_tokens, because a minted user nobody can sign in as is a row, not a
	// fixture — and the stack returns the credential exactly once.
	if seen.body["with_tokens"] != true {
		t.Errorf("did not ask for a token: %v", seen.body)
	}
	for _, want := range []string{"usr_1", "t@test.invalid", "pw", "tok", "shown once"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output does not carry %q:\n%s", want, out.String())
		}
	}
}

func TestListAndDeleteReachTheProject(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"users":[{"id":"usr_1","email":"a@test.invalid","email_verified":true}]}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	target, cred := linkedTo(t, srv.URL)

	var out bytes.Buffer
	if err := listOnProject(context.Background(), target, cred, false, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "a@test.invalid") {
		t.Errorf("list did not show the user:\n%s", out.String())
	}

	out.Reset()
	if err := deleteOnProject(context.Background(), target, cred, "usr_1", &out); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := []string{"GET /v1/management/test-users", "DELETE /v1/management/test-users/usr_1"}
	for i, w := range want {
		if i >= len(paths) || paths[i] != w {
			t.Fatalf("asked %v, want %v", paths, want)
		}
	}
}

// A stack cannot seed a template's rows — that needs the project's schema, and
// the apply path says so when it skips them. Refusing by name beats minting an
// account and leaving somebody to discover the empty app later.
// A template is now MINTED here rather than refused. It used to answer with a
// paragraph explaining that the stack could create the account but not the rows
// it was declared to own; the stack writes both, so the paragraph is gone and
// what replaces it is the request that carries the name.
func TestATemplateIsMintedAtTheProjectsOwnDoor(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"users":[{"user_id":"usr_1","email":"t@test.invalid","password":"p",
			"inserted":{"accounts":2,"transactions":3}}]}`))
	}))
	defer srv.Close()
	target, cred := linkedTo(t, srv.URL)

	var out bytes.Buffer
	if err := createOnProject(context.Background(), target, cred, 2, "banking", false, &out); err != nil {
		t.Fatalf("mint from a template: %v", err)
	}
	if body["template"] != "banking" {
		t.Errorf("the request did not carry the template name: %v", body)
	}
	if body["count"] != float64(2) {
		t.Errorf("--count did not reach the stack: %v — several instances of one template is an ordinary thing to want", body)
	}
	// The ROWS are why a template exists, so they are printed beside the login.
	if !strings.Contains(out.String(), "2 accounts, 3 transactions") {
		t.Errorf("the output does not say what the user arrived holding:\n%s", out.String())
	}
}

func TestTheTemplateListComesFromTheStack(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"templates":[{"name":"banking","email":"","tables":["accounts","profiles"]}]}`))
	}))
	defer srv.Close()
	target, cred := linkedTo(t, srv.URL)

	var out bytes.Buffer
	if err := templatesOnProject(context.Background(), target, cred, false, &out); err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if path != "/v1/management/test-users/templates" {
		t.Errorf("it asked %q", path)
	}
	for _, want := range []string{"banking", "accounts, profiles"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the list does not show %q:\n%s", want, out.String())
		}
	}
}

func TestCloneReachesTheProjectWithItsOverrides(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"users":[{"user_id":"usr_2","email":"c@test.invalid","password":"p","inserted":{"profiles":1}}]}`))
	}))
	defer srv.Close()
	target, cred := linkedTo(t, srv.URL)

	var out bytes.Buffer
	overrides := map[string]map[string]any{"profiles": {"display_name": "Copy"}}
	if err := cloneOnProject(context.Background(), target, cred, "usr_1", "", "", overrides, false, &out); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if path != "/v1/management/test-users/clone" {
		t.Errorf("it asked %q", path)
	}
	if body["source_user_id"] != "usr_1" {
		t.Errorf("the source did not travel: %v", body)
	}
	set, ok := body["set"].(map[string]any)
	if !ok || set["profiles"] == nil {
		t.Errorf("--set did not reach the stack: %v", body)
	}
	if !strings.Contains(out.String(), "1 profiles") {
		t.Errorf("the output does not say what was copied:\n%s", out.String())
	}
}

// The stack's refusals are worth relaying: a 429 over the per-project cap and a
// 409 for an e-mail already taken both explain themselves, and "request failed"
// would throw that away.
func TestAStacksRefusalReachesThePerson(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"test_user_cap","error_description":"This project allows at most 50 test users."}`))
	}))
	defer srv.Close()
	target, cred := linkedTo(t, srv.URL)

	err := createOnProject(context.Background(), target, cred, 1, "", false, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a 429 was reported as success")
	}
	if !strings.Contains(err.Error(), "at most 50 test users") {
		t.Errorf("the stack's own explanation was dropped: %v", err)
	}
}

// THE SHELL'S PALBASE_ENV IS NOT THE TESTS'. The web SDK teaches people to keep
// it set, and the resolver reads it like --env: on such a machine a dozen tests
// here failed that CI never saw. A test that wants it sets it.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("PALBASE_ENV")
	os.Exit(m.Run())
}
