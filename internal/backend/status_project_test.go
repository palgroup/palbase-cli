package backend

// status_project_test.go — the stale key, and the two contracts that differ.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestAStaleAppKeyIsReported is FR-043. A rotated publishable key leaves every
// installed build authenticating with something the project no longer accepts,
// and the app reports it as a sign-in failure — so the one place that CAN see
// both values says so, and names the command that fixes it.
func TestAStaleAppKeyIsReported(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cROTATED", nil)
	linkedAs(t, srv.URL, "a-credential")

	// The app was linked when the project handed out a different key.
	if err := os.MkdirAll(filepath.Join(nativeArtifactsDir, "ios"), 0o755); err != nil {
		t.Fatal(err)
	}
	slot := appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: projectAppID, BaseURL: srv.URL, APIKey: "pb_project_cOLDKEY"},
		},
	}
	blob, _ := json.MarshalIndent(slot, "", "  ")
	if err := os.WriteFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	reportKeyDrift(context.Background(), Target{URL: srv.URL},
		Credentials{Value: "a-credential", Kind: KindPerson}, &out)

	if !strings.Contains(out.String(), "STALE") {
		t.Errorf("a rotated key was not reported:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "palbase link") {
		t.Errorf("the report does not name the fix:\n%s", out.String())
	}
	// Neither key is printed: two nearly-identical strings invite reading them
	// for the difference instead of running the command.
	if strings.Contains(out.String(), "pb_project_c") {
		t.Errorf("a key was printed:\n%s", out.String())
	}
}

// TestACurrentAppKeySaysSo — the same check, passing, because "no output" and
// "checked and fine" are different answers.
func TestACurrentAppKeySaysSo(t *testing.T) {
	inScratchCheckout(t)
	srv := stackServing(t, "pb_project_cCURRENT", nil)
	linkedAs(t, srv.URL, "a-credential")

	if err := os.MkdirAll(filepath.Join(nativeArtifactsDir, "ios"), 0o755); err != nil {
		t.Fatal(err)
	}
	slot := appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main": {AppID: projectAppID, BaseURL: srv.URL, APIKey: "pb_project_cCURRENT"},
		},
	}
	blob, _ := json.MarshalIndent(slot, "", "  ")
	_ = os.WriteFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"), blob, 0o644)

	var out bytes.Buffer
	reportKeyDrift(context.Background(), Target{URL: srv.URL},
		Credentials{Value: "a-credential", Kind: KindPerson}, &out)
	if !strings.Contains(out.String(), "current") {
		t.Errorf("a current key was not confirmed:\n%s", out.String())
	}
}

// TestAKeyThatCannotBeCheckedSaysThat: "cannot check" is not "checked". Silence
// here would read as "the key is fine".
func TestAKeyThatCannotBeCheckedSaysThat(t *testing.T) {
	inScratchCheckout(t)
	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer unreachable.Close()
	linkedAs(t, unreachable.URL, "a-credential")

	_ = os.MkdirAll(filepath.Join(nativeArtifactsDir, "ios"), 0o755)
	slot := appEnvironments{
		Default:      "main",
		Environments: map[string]appEnvironment{"main": {APIKey: "pb_project_cOLD"}},
	}
	blob, _ := json.MarshalIndent(slot, "", "  ")
	_ = os.WriteFile(filepath.Join(nativeArtifactsDir, "ios", "palbase-config.json"), blob, 0o644)

	var out bytes.Buffer
	reportKeyDrift(context.Background(), Target{URL: unreachable.URL},
		Credentials{Value: "a-credential", Kind: KindPerson}, &out)
	if !strings.Contains(out.String(), "could not be checked") {
		t.Errorf("an unreachable project produced silence:\n%s", out.String())
	}
}

// TestContractDriftNamesTheEndpoints is FR-054: the question an app developer
// asks is "can I build this against production yet", and the answer is otherwise
// a compile error in a configuration they were not building at the time.
func TestContractDriftNamesTheEndpoints(t *testing.T) {
	main := []byte(`{"paths":{"/todos":{"get":{}},"/whoami":{"get":{}}}}`)
	local := []byte(`{"paths":{"/todos":{"get":{}},"/graph/query":{"post":{}}}}`)

	var out bytes.Buffer
	reportContractDrift(map[string][]byte{"main": main, "local": local}, &out)

	got := out.String()
	if !strings.Contains(got, "/graph/query") {
		t.Errorf("an endpoint only the local stack serves was not named:\n%s", got)
	}
	if !strings.Contains(got, "/whoami") {
		t.Errorf("an endpoint the local stack is missing was not named:\n%s", got)
	}
	if strings.Contains(got, "/todos") {
		t.Errorf("an endpoint both serve was reported as a difference:\n%s", got)
	}
}

// TestNoDriftIsSilent: two environments serving the same contract produce no
// report at all, because a line that always appears is a line nobody reads.
func TestNoDriftIsSilent(t *testing.T) {
	same := []byte(`{"paths":{"/todos":{"get":{}}}}`)
	var out bytes.Buffer
	reportContractDrift(map[string][]byte{"main": same, "local": same}, &out)
	if out.Len() != 0 {
		t.Errorf("identical contracts produced a report:\n%s", out.String())
	}
}

// TestStatusAsksTheRouteThatEXISTS: `status` asked `/v1/management/deployment`
// (singular), which the stack does not serve, and read the 404 as "nothing is
// deployed" — so it said "nothing yet" about a project with 37 endpoints live,
// in the same second `palbase deploys` listed them. Nothing tested
// statusOfProject at all, which is why it shipped.
func TestStatusAsksTheRouteThatEXISTS(t *testing.T) {
	inScratchCheckout(t)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path == "/v1/management/deployments/current" {
			count := 37
			_ = json.NewEncoder(w).Encode(deploymentState{
				Digest: "7c232f1484db13acc8b083d905df6ac4d8b00ea8", EndpointCount: &count,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := WriteTarget(Target{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential(srv.URL, Credentials{Value: "k", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())

	handled, err := statusOfProject(cmd, Resolvers{}, false)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	for _, path := range asked {
		if path == "/v1/management/deployment" {
			t.Errorf("it asked the singular route, which no stack serves")
		}
	}
	if strings.Contains(out.String(), "nothing yet") {
		t.Errorf("a deployed project was reported as undeployed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "37 endpoint") {
		t.Errorf("the deployment was not reported:\n%s", out.String())
	}
}

// TestSpecNamesTheEnvironmentsItDidNotRefresh is the silent half of a refresh:
// `spec` asks ONE project and regenerates every environment's client from the
// contracts on disk, so for the others the ✓ says "written" where a reader takes
// it for "current". Measured — after a route was added to the local stack and
// `spec` was run, the local client was byte-identical and carried none of it.
func TestSpecNamesTheEnvironmentsItDidNotRefresh(t *testing.T) {
	inScratchCheckout(t)
	envs := appEnvironments{
		Default: "main",
		Environments: map[string]appEnvironment{
			"main":  {BaseURL: "https://main.example"},
			"local": {BaseURL: "http://127.0.0.1:1"},
			"dev":   {BaseURL: "https://dev.example"},
		},
	}
	// One of them has a contract on disk; one never had.
	if err := writeSpec("local", []byte(`{"paths":{}}`)); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	reportStaleContracts("main", envs, &out)
	got := out.String()

	if strings.Contains(got, "only main was refreshed.\n") && !strings.Contains(got, "local") {
		t.Errorf("the others were not named:\n%s", got)
	}
	for _, want := range []string{"local", "dev", "never fetched", "palbase link"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "main (") {
		t.Errorf("the refreshed environment was listed as stale:\n%s", got)
	}

	// One environment: nothing to warn about.
	var single strings.Builder
	reportStaleContracts("main", appEnvironments{
		Default:      "main",
		Environments: map[string]appEnvironment{"main": {}},
	}, &single)
	if single.Len() != 0 {
		t.Errorf("a single-environment checkout was warned about nothing:\n%s", single.String())
	}
}

// `--json` was declared for a year and never wired: the flag existed on the
// command, the renderer existed in the package, and the only path that read
// either was the cloud arm — which a linked checkout never took. So `palbase
// status --json` in a linked project printed the human table and a script
// parsing it got a syntax error, not a status.
func TestStatusJSONIsARealDocument(t *testing.T) {
	inScratchCheckout(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/management/deployments/current" {
			count := 37
			_ = json.NewEncoder(w).Encode(deploymentState{
				Digest:        "7c232f1484db13acc8b083d905df6ac4d8b00ea8",
				EndpointCount: &count,
				SDKVersion:    "21.0.1",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := WriteTarget(Target{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential(srv.URL, Credentials{Value: "k", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())

	if _, err := statusOfProject(cmd, Resolvers{}, true); err != nil {
		t.Fatalf("status --json: %v", err)
	}

	var doc statusJSON
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("--json did not emit JSON (%v):\n%s", err, out.String())
	}
	if doc.Address != srv.URL {
		t.Errorf("address %q, want %q", doc.Address, srv.URL)
	}
	if doc.Deployed == nil || doc.Deployed.SDKVersion != "21.0.1" {
		t.Errorf("the deployment did not travel: %+v", doc.Deployed)
	}
	if doc.Deployed.EndpointCount == nil || *doc.Deployed.EndpointCount != 37 {
		t.Errorf("the endpoint count did not travel: %+v", doc.Deployed)
	}
	// No prose in the document: the human output's advice lines cannot be parsed
	// and their absence is the whole point of the flag.
	if strings.Contains(out.String(), "palbase push") {
		t.Errorf("advice leaked into the JSON:\n%s", out.String())
	}
}

// A project that has never been pushed is a STATE, not an error, and a script
// has to be able to tell it from a deployed one without reading English.
func TestStatusJSONSaysNothingDeployedYet(t *testing.T) {
	inScratchCheckout(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := WriteTarget(Target{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential(srv.URL, Credentials{Value: "k", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetContext(context.Background())

	if _, err := statusOfProject(cmd, Resolvers{}, true); err != nil {
		t.Fatalf("status --json: %v", err)
	}
	var doc statusJSON
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("--json did not emit JSON (%v):\n%s", err, out.String())
	}
	if doc.Deployed != nil {
		t.Errorf("an undeployed project reported a deployment: %+v", doc.Deployed)
	}
	if doc.Reason == "" {
		t.Error("a nil deployment with no reason cannot be told from a failure")
	}
}
