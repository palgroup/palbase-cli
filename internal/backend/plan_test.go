package backend

// plan_test.go — the change set, and the two things it must never do: write
// during a plan, and replace a secret nobody named.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildableBackend turns dir into a project a PUSH would accept: the local SDK
// installed, one controller, and the secret declaration in the form the build
// actually reads.
//
// config/secrets.ts rather than a hand-written .palbase/config.json, because
// that file is an OUTPUT — `buildStackArtifact` regenerates it from config/*.ts
// on every run, so a fixture that writes it directly is writing something the
// code under test is about to overwrite. The published scaffold declares
// SENTRY_DSN optional; this mirrors it.
//
// The local SDK, not the published one: 17.4.0 has no controller registry, so a
// build against it would prove nothing about the code that ships.
func buildableBackend(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not installed — it is the engine a stack push builds with")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sdk := packLocalSDK(t, ctx)
	if !npmInstallProject(t, dir, sdk, "typescript@^5", "zod-to-json-schema") {
		t.Skip("node/npm unavailable or the install failed")
	}
	if !sdkHasControllerRegistry(t, dir) {
		t.Skip("the packed SDK exposes no controller registry")
	}
	useTestParserCache(t)

	if err := os.MkdirAll(filepath.Join(dir, "controllers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "controllers", "health.controller.ts"), []byte(`
import { Controller, Get, z } from "@palbase/backend";

export const HealthResponse = z.object({ status: z.string() });
export type HealthResponse = z.infer<typeof HealthResponse>;

@Controller("/health", { auth: false })
class HealthController {
  @Get("/")
  async check(): Promise<HealthResponse> {
    return { status: "ok" };
  }
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "secrets.ts"), []byte(`
import { defineSecrets, secret } from "@palbase/backend";

export default defineSecrets({
  secrets: [secret("SENTRY_DSN", { required: false })],
});
`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// projectServer answers as a management surface and records every write.
type projectServer struct {
	*httptest.Server
	mu      sync.Mutex
	writes  []string
	secrets map[string]string
}

func newProjectServer(t *testing.T, secrets map[string]string) *projectServer {
	t.Helper()
	p := &projectServer{secrets: secrets}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			p.mu.Lock()
			p.writes = append(p.writes, r.Method+" "+r.URL.Path)
			p.mu.Unlock()
		}
		switch {
		case r.URL.Path == "/v1/management/secrets" && r.Method == http.MethodGet:
			list := struct {
				Secrets []struct {
					Name string `json:"name"`
				} `json:"secrets"`
			}{}
			p.mu.Lock()
			for name := range p.secrets {
				list.Secrets = append(list.Secrets, struct {
					Name string `json:"name"`
				}{name})
			}
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(list)

		case strings.HasSuffix(r.URL.Path, "/value") && r.Method == http.MethodGet:
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/management/secrets/"), "/value")
			p.mu.Lock()
			value, ok := p.secrets[name]
			p.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(value))

		case strings.HasPrefix(r.URL.Path, "/v1/management/secrets/") && r.Method == http.MethodPut:
			var body struct {
				Value string `json:"value"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			p.mu.Lock()
			p.secrets[strings.TrimPrefix(r.URL.Path, "/v1/management/secrets/")] = body.Value
			p.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case r.URL.Path == "/v1/management/schema/plan":
			_, _ = w.Write([]byte(`{"in_sync":false,"changes":["create table notes"],"destructive":[]}`))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *projectServer) writesSeen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.writes...)
}

// declaring writes an evaluated config with the given secret declarations, the
// way `palbase build` produces it.
func declaring(t *testing.T, dir string, required []string, optional []string) {
	t.Helper()
	type decl struct {
		Name     string `json:"name"`
		Required bool   `json:"required"`
	}
	var all []decl
	for _, name := range required {
		all = append(all, decl{name, true})
	}
	for _, name := range optional {
		all = append(all, decl{name, false})
	}
	body, err := json.Marshal(map[string]any{"secrets": map[string]any{"secrets": all}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".palbase"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".palbase", "config.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// runningLocalStack points this checkout at a local stack holding `secrets` —
// the state `palbase start` leaves, and the only source a push has for a value.
func runningLocalStack(t *testing.T, secrets map[string]string) *projectServer {
	t.Helper()
	local := newProjectServer(t, secrets)
	if err := WriteLocalTarget(Target{URL: local.URL}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential(local.URL, Credentials{Value: "local-key", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}
	return local
}

// TestPlanWritesNOTHING is FR-035. The schema half is computed by the project,
// which means a POST — so "writes nothing" cannot be checked by looking at the
// method. It is checked by what the server was asked to CHANGE.
//
// The fixture is a REAL backend, installed against the local SDK, because
// `plan`'s code bucket now runs buildStackArtifact — the same function `push`
// runs — and that refuses a directory with no controllers exactly as a push
// would. It used to be a bare temp dir with a stub config.json, and it passed
// only because the esbuild path plan used to take said "no controllers/ —
// nothing to validate" and carried on. A fixture that no push would accept was
// proving something about a command whose whole job is to predict a push.
func TestPlanWritesNOTHING(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	buildableBackend(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", "schema.ts"), []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := newProjectServer(t, map[string]string{})
	cred := Credentials{Value: "k", Kind: KindKey}

	var out strings.Builder
	if err := runPlan(context.Background(), dir, Target{URL: target.URL}, cred, &out); err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, write := range target.writesSeen() {
		if strings.HasPrefix(write, "PUT") || strings.HasPrefix(write, "DELETE") ||
			strings.Contains(write, "/schema/apply") || strings.Contains(write, "/push") {
			t.Errorf("plan changed something: %s", write)
		}
	}
	// It says the two things a push actually carries.
	for _, bucket := range []string{"code", "schema"} {
		if !strings.Contains(out.String(), bucket) {
			t.Errorf("the plan does not mention %s:\n%s", bucket, out.String())
		}
	}
	// And NOT the two it used to. The config line could never tell the truth —
	// the target had no read-back, so it said what would be SENT rather than what
	// would change — and settings are written directly now, so a plan has nothing
	// to say about them. A line that claims otherwise is the old lie returning.
	for _, gone := range []string{"config", "secrets"} {
		if strings.Contains(out.String(), gone+"\n") || strings.Contains(out.String(), "  would send") {
			t.Errorf("the plan still has a %s section:\n%s", gone, out.String())
		}
	}
}

// THE SECRET-CARRIER TESTS ARE GONE WITH THE CARRIER.
//
// Four tests pinned `carrySecrets`: a name already set on the target is skipped
// (FR-037 — the value there is usually production's), --approve replaces it, a
// missing REQUIRED name stops the push, and a missing optional one does not.
// All four read `config/secrets.ts` for "what the code declares".
//
// That file is gone (2026-08-29) and the push carries no secrets. The stop it
// provided moved EARLIER and got stricter: the names a controller may spell come
// from `palbase-stack.d.ts`, generated off the stack, so code reading a secret
// nobody set does not compile — a keystroke rather than a push. Values are
// written with `palbase secret set NAME --stdin`.

// TestPushRefusesWhileALocalStackIsRunning: the dev runtime serves the DIRECTORY
// it has mounted and never follows the deploy pointer, so a push there activates
// a version nothing loads. "I pushed" would be true and "it shipped" would not,
// which is the kind of failure that has to be refused rather than warned about.
func TestPushRefusesWhileALocalStackIsRunning(t *testing.T) {
	inScratchCheckout(t)
	local := newProjectServer(t, map[string]string{})

	err := runStackPush(context.Background(), Target{URL: local.URL, Local: true},
		Credentials{Value: "k", Kind: KindKey}, false, &strings.Builder{})
	if err == nil {
		t.Fatal("a push to the local stack was accepted")
	}
	for _, want := range []string{"palbase stop", "palbase db apply"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not offer %q: %v", want, err)
		}
	}
	if len(local.writesSeen()) != 0 {
		t.Errorf("it wrote something before refusing: %v", local.writesSeen())
	}
}

// TestPushInAnAppCheckoutWritesNOTHING: the plane check has to come before the
// SDK install, not after it. Run in an app directory this used to download and
// install the project's SDK — node_modules and all — and only then notice there
// were no controllers to send. A refusal that arrives after a side effect is not
// a refusal.
func TestPushInAnAppCheckoutWritesNOTHING(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	// An app checkout: something Xcode would open, and no controllers.
	if err := os.MkdirAll(filepath.Join(dir, "MyApp.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}

	project := newProjectServer(t, map[string]string{})
	err := runStackPush(context.Background(), Target{URL: project.URL},
		Credentials{Value: "k", Kind: KindKey}, false, &strings.Builder{})
	if err == nil {
		t.Fatal("a push from an app checkout was accepted")
	}
	if !strings.Contains(err.Error(), "not a backend checkout") {
		t.Errorf("the refusal does not name the plane: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "node_modules")); statErr == nil {
		t.Error("it installed the SDK into an app checkout before refusing")
	}
	if len(project.writesSeen()) != 0 {
		t.Errorf("it reached the project before refusing: %v", project.writesSeen())
	}
}
