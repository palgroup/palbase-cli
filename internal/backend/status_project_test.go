package backend

// status_project_test.go — the stale key, and the two contracts that differ.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
