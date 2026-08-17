package backend

// A stack started on this machine serves the directory it has mounted and never
// follows the deploy pointer, so /v1/management/deployments/current answers 404
// forever — that is the correct steady state, not a state to get out of.
//
// `status` read that 404 the same way it reads it for a project in the cloud and
// printed "nothing yet — `palbase push`". Running that command on the very same
// checkout gets a refusal, in the same terminal, a second later. A tool that
// recommends a command its own CLI rejects teaches people to distrust its
// advice, so the invariant is asserted here rather than the wording: IF push
// refuses this target, status must not name push.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestStatusDoesNotAdviseACommandThatRefusesThisTarget(t *testing.T) {
	inScratchCheckout(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A dev stack: no artifact was ever activated, and none ever will be.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	local := Target{URL: srv.URL, Local: true}
	if err := WriteLocalTarget(local); err != nil {
		t.Fatal(err)
	}
	// The real resolver reads a running stack's own key out of its state
	// directory; there is no stack here, so the store's fallback stands in. What
	// is under test is the ADVICE, not where the key came from.
	if err := StoreCredential(srv.URL, Credentials{Value: "k", Kind: KindKey}); err != nil {
		t.Fatal(err)
	}

	// The other half of the invariant, measured rather than assumed: this really
	// is a target a push refuses. Without this the assertion below would keep
	// passing if push ever started accepting local targets, and the advice would
	// silently become correct-to-omit.
	pushErr := runStackPush(context.Background(), local, Credentials{Value: "k", Kind: KindKey}, false, &bytes.Buffer{})
	if pushErr == nil {
		t.Fatal("push accepted a local target — this test's premise no longer holds, revisit the advice in status")
	}

	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())

	handled, err := statusOfProject(cmd)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}

	if strings.Contains(out.String(), "palbase push") {
		t.Errorf("status recommends a command that refuses this target (%v):\n%s", pushErr, out.String())
	}
	// And it still SAYS something about the deployment line, rather than leaving
	// a person wondering whether the question was even asked.
	if !strings.Contains(out.String(), "deployed:") {
		t.Errorf("status went quiet about the deployment:\n%s", out.String())
	}
}
