// Package selectiontest is a fake `/api/v2` Management API for the CLI's tests.
//
// It exists so every rewritten command can be tested against the SAME server
// that records the EXACT method + path + query + body + headers it received.
// That recording is what stops a silent 404: a command that uses a non-canonical
// path (or drops the Environment out of the current one) fails the path assertion, not
// some downstream behaviour test that happened to stub the response anyway.
package selectiontest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/palgroup/palbase-cli/internal/transport"
)

// Request is one recorded call.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
	// Header carries the request headers (the Idempotency-Key assertion reads it).
	Header http.Header
}

// Route is a canonical "METHOD /path" key.
func (r Request) Route() string { return r.Method + " " + r.Path }

// Fake is an httptest-backed /api/v2 server plus the seeded Project and
// Environment rows every selection resolves through.
type Fake struct {
	t *testing.T

	Server *httptest.Server

	mu       sync.Mutex
	requests []Request

	// Projects are returned by GET /api/v2/projects and (individually) by
	// GET /api/v2/projects/{projectId}.
	Projects []Project
	// Environments is keyed by project id.
	Environments map[string][]selection.Environment

	// handlers overrides or adds a route: "METHOD /path" -> handler.
	handlers map[string]http.HandlerFunc
}

// Project is a seeded Project (the list row plus the detail-only fields).
type Project struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	CreatedBy      string `json:"created_by"`
	CreatedAt      string `json:"created_at"`
	AppsRequired   bool   `json:"apps_required"`
	Role           string `json:"role"`
	GithubRepo     string `json:"github_repo"`
	Mode           string `json:"mode"`
}

// Env builds an Environment row with the fields the CLI actually reads.
func Env(id, projectID, ref, slug, kind string, production bool) selection.Environment {
	return selection.Environment{
		ID: id, ProjectID: projectID, Ref: ref, Name: slug, Slug: slug,
		Kind: kind, IsProduction: production, Region: "northeurope",
		Status: "active", DesiredState: "running", CreatedAt: "2026-07-01T00:00:00Z",
	}
}

// New starts a fake seeded with ONE project (proj_1, palbase provider) holding
// ONE production environment (env_prod / ref "app1prod"). Tests add or override
// whatever they need.
func New(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{
		t: t,
		Projects: []Project{{
			ID: "proj_1", OrganizationID: "org_1", Name: "todoapp",
			CreatedBy: "usr_1", CreatedAt: "2026-07-01T00:00:00Z",
			Role: "owner", Mode: "platform",
		}},
		Environments: map[string][]selection.Environment{
			"proj_1": {Env("env_prod", "proj_1", "app1prod", "production", "production", true)},
		},
		handlers: map[string]http.HandlerFunc{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// Handle registers (or replaces) a route. key is "METHOD /path".
func (f *Fake) Handle(key string, h http.HandlerFunc) *Fake {
	f.handlers[key] = h
	return f
}

// OK registers a route that answers 200 with `data`.
func (f *Fake) OK(key string, data any) *Fake {
	return f.Handle(key, func(w http.ResponseWriter, _ *http.Request) { WriteOK(w, http.StatusOK, data) })
}

// Accepted registers a route that answers 202 with `data`.
func (f *Fake) Accepted(key string, data any) *Fake {
	return f.Handle(key, func(w http.ResponseWriter, _ *http.Request) { WriteOK(w, http.StatusAccepted, data) })
}

// Requests returns every recorded call, in order.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.requests))
	copy(out, f.requests)
	return out
}

// Routes returns the recorded calls as "METHOD /path" strings.
func (f *Fake) Routes() []string {
	reqs := f.Requests()
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Route()
	}
	return out
}

// Last returns the most recent recorded call.
func (f *Fake) Last() Request {
	reqs := f.Requests()
	if len(reqs) == 0 {
		f.t.Fatal("no request was made")
	}
	return reqs[len(reqs)-1]
}

// Find returns the first recorded call on a "METHOD /path" route.
func (f *Fake) Find(route string) (Request, bool) {
	for _, r := range f.Requests() {
		if r.Route() == route {
			return r, true
		}
	}
	return Request{}, false
}

// REST is a DPoP-signed transport pointed at the fake.
func (f *Fake) REST() *transport.Client {
	key, err := auth.NewDPoPKey()
	if err != nil {
		f.t.Fatalf("dpop key: %v", err)
	}
	return transport.New(f.Server.URL, key, "pat_test")
}

// Resolver builds a selection.Resolver against the fake.
func (f *Fake) Resolver() *selection.Resolver {
	rest := f.REST()
	return &selection.Resolver{
		REST: func() selection.REST { return rest },
	}
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	raw, _ := io.ReadAll(r.Body)
	if len(raw) > 0 {
		// A multipart deploy body is not JSON; record it as empty rather than
		// failing — the deploy test asserts on the path + Idempotency-Key header.
		_ = json.Unmarshal(raw, &body)
	}
	// Recording consumed the stream; hand a fresh one to the route handler, which
	// may legitimately want to read the body it was sent.
	r.Body = io.NopCloser(bytes.NewReader(raw))
	f.mu.Lock()
	f.requests = append(f.requests, Request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Body: body, Header: r.Header.Clone(),
	})
	f.mu.Unlock()

	if h, ok := f.handlers[r.Method+" "+r.URL.Path]; ok {
		h(w, r)
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects":
		WriteOK(w, http.StatusOK, f.Projects)
		return
	// The create-saga poll. The default is a saga that ALREADY finished — a test
	// about something else should not have to model provisioning. Tests that care
	// (still running / died) register their own handler.
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/projects/create-status":
		WriteOK(w, http.StatusOK, map[string]any{
			"workflowId":      r.URL.Query().Get("workflowId"),
			"status":          "COMPLETED",
			"currentActivity": nil,
			"failure":         nil,
		})
		return
	// The ENV-create saga poll — same default as the Project one: a saga that already
	// finished. Tests about a dying / still-running env-create register their own.
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/environments/create-status"):
		WriteOK(w, http.StatusOK, map[string]any{
			"workflowId":      r.URL.Query().Get("workflowId"),
			"status":          "COMPLETED",
			"currentActivity": nil,
			"failure":         nil,
		})
		return
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/environments"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v2/projects/"), "/environments")
		if envs, ok := f.Environments[id]; ok {
			WriteOK(w, http.StatusOK, envs)
			return
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/projects/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/")
		for _, p := range f.Projects {
			if p.ID == id {
				WriteOK(w, http.StatusOK, p)
				return
			}
		}
	}

	// Anything else is UNHANDLED — the 404 is the point. A command that uses a
	// non-canonical path (or omits the Environment) lands here.
	WriteError(w, http.StatusNotFound, "not_found", "no route "+r.Method+" "+r.URL.Path)
}

// WriteOK emits the Management API's success envelope.
func WriteOK(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": "req_test"})
}

// WriteError emits the Management API's error envelope.
func WriteError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": code, "error_description": description,
		"status": status, "request_id": "req_test",
	})
}

// Chdir moves the test into a fresh temp dir and restores the cwd afterwards.
// The commands read/write `.palbase/config.json` relative to the cwd, so every
// selection test needs its own directory.
func Chdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	// t.TempDir() on macOS is a /var symlink to /private/var; return the resolved
	// path so a test comparing os.Getwd() against it does not spuriously differ.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}

// Selected is the one-liner for a test that just needs "proj_1 / production is
// selected here": it writes config v2 into the CURRENT directory, starts the
// fake, and hands back the resolver accessor the command packages take.
//
// The caller must already be in its own directory (`t.Chdir(t.TempDir())` or
// Chdir(t)) — Selected does not chdir, because a test that seeds files (a
// schema, a dotenv) has already chosen where it lives and moving it out from
// under itself is how that test starts asserting against an empty dir.
func Selected(t *testing.T) func() *selection.Resolver {
	t.Helper()
	requireTempCwd(t)
	WriteConfig(t, ".", nil)
	r := New(t).Resolver()
	return func() *selection.Resolver { return r }
}

// requireTempCwd refuses to write a selection into a real working tree — a test
// that forgot to chdir would otherwise drop a .palbase/config.json into the repo.
func requireTempCwd(t *testing.T) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return
	}
	if !strings.HasPrefix(resolved, tmp) {
		t.Fatalf("selectiontest.Selected must run inside a temp cwd — call t.Chdir(t.TempDir()) first (cwd=%s)", cwd)
	}
}

// WriteConfig writes a v2 `.palbase/config.json` selecting proj_1 / env_prod.
func WriteConfig(t *testing.T, dir string, cfg *selection.Config) {
	t.Helper()
	if cfg == nil {
		cfg = &selection.Config{
			ProjectID: "proj_1", EnvironmentID: "env_prod",
			RepositoryProvider: selection.ProviderPalbase,
		}
	}
	if err := selection.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
}

// WriteRawConfig writes arbitrary bytes as `.palbase/config.json`.
func WriteRawConfig(t *testing.T, dir, content string) {
	t.Helper()
	path := selection.ConfigPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ReadConfig reads back `.palbase/config.json` as raw JSON so a golden test can
// compare the EXACT bytes the CLI wrote.
func ReadConfig(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(selection.ConfigPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
