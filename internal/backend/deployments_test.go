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
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1/management/deployments/current":
			count := 37
			_ = json.NewEncoder(w).Encode(projectDeployment{Digest: *activated, EndpointCount: &count})
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

// TestDeploysMarksTheOneThatIsServing: a history without that mark is a list of
// hashes.
func TestDeploysMarksTheOneThatIsServing(t *testing.T) {
	srv, _ := deployingProject(t, history())
	linkedToProject(t, srv.URL)

	var out, errOut bytes.Buffer
	handled, err := deploysOfProject(commandIn(t, &out, &errOut))
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
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
	if !strings.Contains(body, "37") {
		t.Errorf("the endpoint count is missing:\n%s", body)
	}
}

// TestRollbackTakesTheDigestTheListingPRINTS: making somebody paste 64
// characters they cannot verify by eye is how the wrong version gets activated.
func TestRollbackTakesTheDigestTheListingPRINTS(t *testing.T) {
	srv, activated := deployingProject(t, history())
	linkedToProject(t, srv.URL)

	var out, errOut bytes.Buffer
	handled, err := rollbackOnProject(commandIn(t, &out, &errOut), "bbbb11112222")
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v\n%s", handled, err, out.String())
	}
	if *activated != history()[1].Digest {
		t.Errorf("activated %q", *activated)
	}
	// The count is deliberately NOT claimed here: the runtime re-reads the
	// pointer on its own clock, so a number printed at this instant belongs to
	// the artifact that is on its way out.
	if strings.Contains(out.String(), "serving 37 endpoint") {
		t.Errorf("a count from the outgoing artifact was reported as confirmation:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "palbase status") {
		t.Errorf("the output does not say where to see what is serving:\n%s", out.String())
	}
}

// TestAnUnknownVersionListsTheKnownOnes: the answer to "no version starts with
// that" is the set that does.
func TestAnUnknownVersionListsTheKnownOnes(t *testing.T) {
	srv, activated := deployingProject(t, history())
	linkedToProject(t, srv.URL)

	var out, errOut bytes.Buffer
	_, err := rollbackOnProject(commandIn(t, &out, &errOut), "cccc")
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
	handled, err := deploysOfProject(commandIn(t, &out, &errOut))
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if !strings.Contains(out.String(), "palbase push") {
		t.Errorf("an empty history does not say what fills it:\n%s", out.String())
	}
}
