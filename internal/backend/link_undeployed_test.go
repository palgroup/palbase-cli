package backend

// link_undeployed_test.go — A REFUSAL WHOSE CURE IS A PUSH MUST LEAVE THE PUSH
// REACHABLE.
//
// A cloud project that has never been pushed to answers its contract route with
// 404 `spec_unavailable`, and `link` refuses: there is no specification, so
// there is no client to generate. The refusal itself is honest. What it must
// not do is take the ADDRESS with it — `palbase push` reads the remembered
// target, and a link that discards it leaves the person with a project they
// cannot push to and a push they cannot reach.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// undeployedProject answers everything a link asks EXCEPT the contract: this is
// a project whose runtime holds no artifact yet.
func undeployedProject(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wellKnownPath:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"hosting":"project","sdk_version":"38.0.12"}`))
		case "/v1/management/keys":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"publishable":"pb_project_cPUBLISHABLE"}`))
		case "/v1/management/openapi":
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"spec_unavailable","error_description":"nothing is deployed yet","status":404}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A LINK THAT FAILS FOR SOME OTHER REASON STILL LEAVES THE ADDRESS BEHIND.
//
// The missing contract is no longer one of those reasons (see below), but every
// remaining one has the same property: the work happens in a stage the failure
// discards, and the project's identity was learned in that stage. `palbase push`
// reads exactly that identity, so a refusal that takes it along leaves the
// person with no way to reach the act that cures anything.
//
// The failure used here is a real one the harness produces: the app's social
// auth configuration is not served, which stops the iOS client's generation
// AFTER the project has already been identified.
func TestALinkThatFailsStillLeavesThePushReachable(t *testing.T) {
	inScratchCheckout(t)
	srv := undeployedProject(t)
	linkedAs(t, srv.URL, "person-token")

	var out strings.Builder
	err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out)
	if err == nil {
		t.Fatal("the harness serves no social auth config, so this link was expected to fail")
	}
	target, readErr := readLinkedProject()
	if readErr != nil {
		t.Fatalf("the refusal took the project's address with it, so `palbase push` "+
			"cannot be reached: %v (link said: %v)", readErr, err)
	}
	if target.URL != srv.URL {
		t.Fatalf("remembered %q, linked %q", target.URL, srv.URL)
	}
}

// A BACKEND CHECKOUT MUST LINK TO A PROJECT THAT HAS NOTHING DEPLOYED.
//
// This is the loop the refusal closed on itself: `push` reads the binding that
// only `link` writes, so refusing the link because nothing is deployed makes
// the deploy impossible — and the refusal's own advice ("this ends with
// `palbase push`") named an act it had just made unreachable.
func TestLinkingABackendToAProjectWithNoContractSucceeds(t *testing.T) {
	inScratchCheckout(t)
	srv := undeployedProject(t)
	linkedAs(t, srv.URL, "person-token")

	var out strings.Builder
	if err := runLink(context.Background(), linkOpts{url: srv.URL}, &out); err != nil {
		t.Fatalf("a project with nothing deployed could not be linked, so it can never "+
			"BE deployed: %v\n%s", err, out.String())
	}
	target, readErr := readLinkedProject()
	if readErr != nil {
		t.Fatalf("link reported success and bound nothing: %v", readErr)
	}
	if target.URL != srv.URL {
		t.Fatalf("remembered %q, linked %q", target.URL, srv.URL)
	}
	// AND IT SAYS SO. A link that silently produces no contract would leave the
	// person expecting a client that is not there.
	if !strings.Contains(out.String(), "nothing is deployed yet") {
		t.Errorf("the link does not say the contract is missing:\n%s", out.String())
	}
	// NEGATIVE CONTROL: no contract file was invented for an environment that
	// has none — an empty client compiles and then 404s.
	if _, err := os.Stat(SpecPath("main")); err == nil {
		t.Errorf("a contract was written for a project that has none: %s", SpecPath("main"))
	}
}
