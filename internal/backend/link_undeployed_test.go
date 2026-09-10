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

func TestLinkingAnUndeployedProjectStillLeavesThePushReachable(t *testing.T) {
	inScratchCheckout(t)
	srv := undeployedProject(t)
	linkedAs(t, srv.URL, "person-token")

	var out strings.Builder
	err := runLink(context.Background(), linkOpts{url: srv.URL, platforms: []string{"ios"}}, &out)
	if err == nil {
		t.Fatal("a project with no contract produced a client")
	}
	// THE ADDRESS SURVIVES. Without it `palbase push` — the one act that ends
	// this state — has nothing to push to.
	target, readErr := readLinkedProject()
	if readErr != nil {
		t.Fatalf("the refusal took the project's address with it, so `palbase push` "+
			"cannot be reached: %v (link said: %v)", readErr, err)
	}
	if target.URL != srv.URL {
		t.Fatalf("remembered %q, linked %q", target.URL, srv.URL)
	}
	// AND IT NAMES THE CURE. "cannot describe itself" alone tells a person what
	// failed and not what to do about it.
	if !strings.Contains(err.Error(), "palbase push") {
		t.Errorf("the refusal does not name the act that ends it: %v", err)
	}
}
