package testuser

// `test-user` was the last verb that could only talk to the cloud. In a checkout
// linked to a stack — which is what `palbase start` produces — every subcommand
// answered "no project selected — run `palbase project use <projectId>`", advice
// that cannot be followed there and that names a concept the checkout does not
// have. These tests pin the routing: a linked checkout reaches the PROJECT, at
// the route a deploy already uses for the same job.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/backend"
)

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
	got, _, ok := linkedProject()
	if !ok {
		t.Fatal("a checkout with a local target and a credential did not resolve as linked")
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
	if seen.method != http.MethodPost || seen.path != "/admin/test-users" {
		t.Errorf("asked %s %s, want POST /admin/test-users", seen.method, seen.path)
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
	want := []string{"GET /admin/test-users", "DELETE /admin/test-users/usr_1"}
	for i, w := range want {
		if i >= len(paths) || paths[i] != w {
			t.Fatalf("asked %v, want %v", paths, want)
		}
	}
}

// A stack cannot seed a template's rows — that needs the project's schema, and
// the apply path says so when it skips them. Refusing by name beats minting an
// account and leaving somebody to discover the empty app later.
func TestATemplateIsRefusedWithTheReasonRatherThanHalfApplied(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	target, cred := linkedTo(t, srv.URL)

	err := createOnProject(context.Background(), target, cred, 1, "demo", false, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a template was accepted; its rows cannot be seeded here")
	}
	if called {
		t.Error("it minted the account anyway — a half-applied fixture is the failure being avoided")
	}
	if !strings.Contains(err.Error(), "demo") || !strings.Contains(err.Error(), "schema") {
		t.Errorf("the refusal does not say which template or why: %v", err)
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
