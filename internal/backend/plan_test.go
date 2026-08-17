package backend

// plan_test.go — the change set, and the two things it must never do: write
// during a plan, and replace a secret nobody named.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

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
func TestPlanWritesNOTHING(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	declaring(t, dir, nil, []string{"SENTRY_DSN"})
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
	// And it says all four things.
	for _, bucket := range []string{"code", "schema", "config", "secrets"} {
		if !strings.Contains(out.String(), bucket) {
			t.Errorf("the plan does not mention %s:\n%s", bucket, out.String())
		}
	}
}

// TestASecretAlreadySetThereIsSkipped is FR-037, and it is the one that protects
// a production credential: the value on the target is usually production's and
// the value here is usually not.
func TestASecretAlreadySetThereIsSkipped(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	declaring(t, dir, nil, []string{"SENTRY_DSN", "STRIPE_KEY"})

	runningLocalStack(t, map[string]string{
		"SENTRY_DSN": "https://dev@sentry.io/dev",
		"STRIPE_KEY": "sk_test_local",
	})
	target := newProjectServer(t, map[string]string{"STRIPE_KEY": "sk_live_PRODUCTION"})
	cred := Credentials{Value: "k", Kind: KindKey}

	var out strings.Builder
	if err := carrySecrets(context.Background(), dir, Target{URL: target.URL}, cred, false, &out); err != nil {
		t.Fatalf("carry: %v", err)
	}

	if got := target.secrets["STRIPE_KEY"]; got != "sk_live_PRODUCTION" {
		t.Errorf("the production key was replaced with %q", got)
	}
	if got := target.secrets["SENTRY_DSN"]; got != "https://dev@sentry.io/dev" {
		t.Errorf("the gap was not filled: %q", got)
	}
	if !strings.Contains(out.String(), "STRIPE_KEY: already set there, left alone") {
		t.Errorf("the skip was not reported:\n%s", out.String())
	}
	if strings.Contains(out.String(), "sk_") {
		t.Errorf("a value reached the output:\n%s", out.String())
	}
}

// TestApproveReplacesIt is FR-038 — the same run, with the decision made.
func TestApproveReplacesIt(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	declaring(t, dir, nil, []string{"STRIPE_KEY"})

	runningLocalStack(t, map[string]string{"STRIPE_KEY": "sk_test_local"})
	target := newProjectServer(t, map[string]string{"STRIPE_KEY": "sk_live_PRODUCTION"})

	var out strings.Builder
	if err := carrySecrets(context.Background(), dir, Target{URL: target.URL},
		Credentials{Value: "k", Kind: KindKey}, true, &out); err != nil {
		t.Fatalf("carry: %v", err)
	}
	if got := target.secrets["STRIPE_KEY"]; got != "sk_test_local" {
		t.Errorf("--approve did not replace it: %q", got)
	}
}

// TestAMissingRequiredSecretStopsThePush is FR-039, and it stops it BEFORE the
// code ships: code that reads a credential nobody set deploys green and fails on
// its first request.
func TestAMissingRequiredSecretStopsThePush(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	declaring(t, dir, []string{"NEO4J_PASSWORD"}, nil)

	runningLocalStack(t, map[string]string{})
	target := newProjectServer(t, map[string]string{})

	var out strings.Builder
	err := carrySecrets(context.Background(), dir, Target{URL: target.URL},
		Credentials{Value: "k", Kind: KindKey}, false, &out)
	if err == nil {
		t.Fatal("a push went ahead without a required secret")
	}
	if !strings.Contains(err.Error(), "NEO4J_PASSWORD") {
		t.Errorf("the refusal does not name it: %v", err)
	}
	if !strings.Contains(err.Error(), "palbase secret set") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
	if len(target.writesSeen()) != 0 {
		t.Errorf("something was written before the refusal: %v", target.writesSeen())
	}
}

// TestAnOptionalSecretNobodySetIsNotAnError: a project can declare something it
// does not always need, and a fresh environment must still deploy.
func TestAnOptionalSecretNobodySetIsNotAnError(t *testing.T) {
	inScratchCheckout(t)
	dir, _ := os.Getwd()
	declaring(t, dir, nil, []string{"SENTRY_DSN"})

	runningLocalStack(t, map[string]string{})
	target := newProjectServer(t, map[string]string{})

	var out strings.Builder
	if err := carrySecrets(context.Background(), dir, Target{URL: target.URL},
		Credentials{Value: "k", Kind: KindKey}, false, &out); err != nil {
		t.Fatalf("an optional secret blocked the push: %v", err)
	}
}

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
