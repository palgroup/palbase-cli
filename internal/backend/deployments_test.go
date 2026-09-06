package backend

// deployments_test.go — going back to a version this project already has.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// deployingProject answers as a project with a deploy history and records what
// was activated.
func deployingProject(t *testing.T, history []projectDeployment) (*httptest.Server, *string) {
	t.Helper()
	activated := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/management/deployments" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"deployments": history})
		case strings.HasSuffix(r.URL.Path, "/activate") && r.Method == http.MethodPost:
			*activated = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/management/deployments/"), "/activate")
			// The project waits for the runtime and answers with what it
			// OBSERVED serving — which is what makes the count attributable.
			count := 10
			_ = json.NewEncoder(w).Encode(projectDeployment{
				Digest: *activated, ServingDigest: *activated, EndpointCount: &count, Active: true,
			})
		case r.URL.Path == "/v1/management/deployments/current":
			// The project reports what the RUNTIME says it is answering from,
			// separately from the pointer. Here they agree immediately; the
			// interesting case (they disagree for a few seconds) is the one the
			// wait exists for and is covered below.
			count := 10
			_ = json.NewEncoder(w).Encode(projectDeployment{
				Digest: *activated, ServingDigest: *activated, EndpointCount: &count,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, activated
}

func linkedToProject(t *testing.T, url string) {
	t.Helper()
	inScratchCheckout(t)
	if err := WriteTarget(Target{URL: url}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential(url, Credentials{Value: "k", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}
}

func commandIn(t *testing.T, out, errOut *bytes.Buffer) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetContext(context.Background())
	return cmd
}

func history() []projectDeployment {
	newest, older := 37, 30
	return []projectDeployment{
		{Digest: "aaaa111122223333444455556666777788889999aaaabbbbccccddddeeeeffff", EndpointCount: &newest, Active: true, SDKVersion: "18.0.0"},
		{Digest: "bbbb111122223333444455556666777788889999aaaabbbbccccddddeeeeffff", EndpointCount: &older, SDKVersion: "17.4.0"},
	}
}

func TestDeploysJSONContainsFullHistory(t *testing.T) {
	for _, rows := range [][]projectDeployment{history(), {}} {
		srv, _ := deployingProject(t, rows)
		linkedToProject(t, srv.URL)
		cmd := newDeploysCmd()
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs([]string{"--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var got []projectDeployment
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("--json output cannot be parsed: %v\n%s", err, out.String())
		}
		if len(got) != len(rows) {
			t.Fatalf("got %d deployments, want %d", len(got), len(rows))
		}
		for i, row := range got {
			if row.Digest != rows[i].Digest || row.SDKVersion != rows[i].SDKVersion || row.Active != rows[i].Active {
				t.Errorf("deployment was truncated or changed: %+v", row)
			}
		}
		if len(rows) == 0 && strings.TrimSpace(out.String()) != "[]" {
			t.Errorf("an empty history must be a JSON array: %s", out.String())
		}
	}
}

// TestDeploysMarksTheOneThatIsServing: a history without that mark is a list of
// hashes.
func TestDeploysMarksTheOneThatIsServing(t *testing.T) {
	srv, _ := deployingProject(t, history())
	linkedToProject(t, srv.URL)

	var out, errOut bytes.Buffer
	err := deploysOfProject(commandIn(t, &out, &errOut))
	if err != nil {
		t.Fatalf("%v", err)
	}
	body := out.String()
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "aaaa11112222") && !strings.HasPrefix(line, "▸") {
			t.Errorf("the serving version is not marked:\n%s", body)
		}
		if strings.Contains(line, "bbbb11112222") && strings.HasPrefix(line, "▸") {
			t.Errorf("a version that is not serving is marked:\n%s", body)
		}
	}
	if !strings.Contains(body, "bbbb11112222") {
		t.Errorf("the older version is missing:\n%s", body)
	}
	// No ENDPOINTS column: a manifest never recorded a count, so every row was a
	// dash. What the RUNTIME confirms is reported on its own line instead.
	if !strings.Contains(body, "is serving 10 endpoint(s)") {
		t.Errorf("the serving count was not reported:\n%s", body)
	}
}

// TestRollbackTakesTheDigestTheListingPRINTS: making somebody paste 64
// characters they cannot verify by eye is how the wrong version gets activated.
func TestRollbackTakesTheDigestTheListingPRINTS(t *testing.T) {
	srv, activated := deployingProject(t, history())
	linkedToProject(t, srv.URL)

	var out, errOut bytes.Buffer
	err := rollbackOnProject(commandIn(t, &out, &errOut), "bbbb11112222")
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if *activated != history()[1].Digest {
		t.Errorf("activated %q", *activated)
	}
	// The count IS claimed here, because the project confirmed the runtime is
	// answering from this digest before reporting it.
	if !strings.Contains(out.String(), "serving 10 endpoint") {
		t.Errorf("the confirmed count was not reported:\n%s", out.String())
	}
}

// TestAnUnknownVersionListsTheKnownOnes: the answer to "no version starts with
// that" is the set that does.
func TestAnUnknownVersionListsTheKnownOnes(t *testing.T) {
	srv, activated := deployingProject(t, history())
	linkedToProject(t, srv.URL)

	var out, errOut bytes.Buffer
	err := rollbackOnProject(commandIn(t, &out, &errOut), "cccc")
	if err == nil {
		t.Fatal("an unknown version was accepted")
	}
	if !strings.Contains(err.Error(), "aaaa11112222") || !strings.Contains(err.Error(), "bbbb11112222") {
		t.Errorf("the refusal does not list what this project has: %v", err)
	}
	if *activated != "" {
		t.Errorf("something was activated anyway: %q", *activated)
	}
}

// TestAnAmbiguousPrefixIsRefused: two versions that share a prefix is exactly
// when guessing costs the most.
func TestAnAmbiguousPrefixIsRefused(t *testing.T) {
	both := []projectDeployment{
		{Digest: "abcd0000000000000000000000000000000000000000000000000000000000001"},
		{Digest: "abcd0000000000000000000000000000000000000000000000000000000000002"},
	}
	if _, err := resolveDigest("abcd", both); err == nil {
		t.Fatal("an ambiguous prefix activated something")
	} else if !strings.Contains(err.Error(), "matches 2") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// TestNothingDeployedIsAStateNotAnError.
func TestNothingDeployedIsAStateNotAnError(t *testing.T) {
	srv, _ := deployingProject(t, nil)
	linkedToProject(t, srv.URL)

	var out, errOut bytes.Buffer
	err := deploysOfProject(commandIn(t, &out, &errOut))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out.String(), "palbase push") {
		t.Errorf("an empty history does not say what fills it:\n%s", out.String())
	}
}

// TestRollbackRefusesWhenTheRuntimeNeverPicksItUp: the project waits for the
// runtime and answers 422 when it never comes or comes with nothing. Reported as
// a failure rather than as a rollback, because "the pointer moved and nothing
// loaded it" is precisely the state a rollback was trying to escape.
func TestRollbackRefusesWhenTheRuntimeNeverPicksItUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/management/deployments" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"deployments": history()})
		case strings.HasSuffix(r.URL.Path, "/activate"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"not_serving","error_description":"the runtime never reported this digest","status":422}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	linkedToProject(t, srv.URL)

	var out, errOut bytes.Buffer
	err := rollbackOnProject(commandIn(t, &out, &errOut), "bbbb11112222")
	if err == nil {
		t.Fatal("a rollback nothing picked up was reported as success")
	}
	if !strings.Contains(err.Error(), "never reported this digest") {
		t.Errorf("the refusal does not carry the project's reason: %v", err)
	}
}
