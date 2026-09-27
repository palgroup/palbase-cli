package backend

// stack_spec_test.go — what a project says about its own contract reaches the
// person who asked.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTheProjectsOwnReasonReachesTheCaller.
//
// ÖLÇÜLDÜ 25.08.2026 (palai-cloud): tek bir `z.lazy` şeması bütün OpenAPI
// belgesini düşürdü. Proje `spec_unavailable` + gerçek sebebi veriyordu; bu
// satır ise onu atıp "önce bir backend push et" diyordu — saniyeler önce
// başarıyla deploy edilmiş bir projede. Yani araç, az önce yapılan şeyin
// yapılmasını istedi ve gerçek cümle yalnız pod loglarında kaldı.
func TestTheProjectsOwnReasonReachesTheCaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"spec_unavailable","error_description":` +
			`"controllers/PalaiController.ts cannot be described: Unknown zod object type"}`))
	}))
	defer srv.Close()

	_, err := fetchStackSpec(context.Background(), Target{URL: srv.URL}, Credentials{Value: "k", Kind: KindKey})
	if err == nil {
		t.Fatal("a project that cannot describe itself was reported as fine")
	}
	for _, want := range []string{"PalaiController", "Unknown zod object type"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the project's reason did not survive (%q missing): %v", want, err)
		}
	}
}

// With no envelope there is nothing to repeat, and the friendly sentence is
// right — a project that has never deployed genuinely has nothing to describe.
func TestNothingDeployedStillReadsAsNothingDeployed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchStackSpec(context.Background(), Target{URL: srv.URL}, Credentials{Value: "k", Kind: KindKey})
	if err == nil || !strings.Contains(err.Error(), "nothing to describe yet") {
		t.Errorf("want the first-deploy sentence, got %v", err)
	}
}

// A proxy's HTML page is not an explanation: repeating it would replace an
// unhelpful sentence with a worse one.
func TestAnHTMLErrorPageIsNotTreatedAsAReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body>404 Not Found</body></html>"))
	}))
	defer srv.Close()

	_, err := fetchStackSpec(context.Background(), Target{URL: srv.URL}, Credentials{Value: "k", Kind: KindKey})
	if err == nil || !strings.Contains(err.Error(), "nothing to describe yet") {
		t.Errorf("an HTML page became the message: %v", err)
	}
}

// WHAT ENDS A MISSING CONTRACT DEPENDS ON THE STACK (D-027, T018 fix round 2),
// and a stack somebody hosts keeps the sentence it always had, word for word:
// every test server is a loopback address, so no network test reaches this
// text any more. The stack on this machine — a start record, whatever address
// it announces, or any loopback address — is started, not pushed to.
func TestTheStepThatEndsAMissingContractFitsTheStack(t *testing.T) {
	const reason = "controllers/PalaiController.ts cannot be described: Unknown zod object type"
	for _, c := range []struct {
		target      Target
		description string
		want        string
	}{
		{Target{URL: "https://stack.example.com"}, "",
			"no contract yet: https://stack.example.com has nothing to describe yet — push a backend to it first (palbase push)"},
		{Target{URL: "https://stack.example.com"}, reason,
			"no contract yet: https://stack.example.com cannot describe itself: " + reason +
				" — a backend is what makes a contract, so this ends with `palbase push`"},
		{Target{URL: "http://127.0.0.1:54321"}, "",
			"no contract yet: http://127.0.0.1:54321 has nothing to describe yet — run `palbase start` in the backend " +
				"(its stack serves the contract of the code it runs), then `palbase spec` here"},
		{Target{URL: "http://192.168.1.20:54321", Local: true}, reason,
			"no contract yet: http://192.168.1.20:54321 cannot describe itself: " + reason +
				" — a backend is what makes a contract, and this machine's stack serves the one `palbase start` runs, " +
				"so this ends with `palbase start` in the backend, then `palbase spec` here"},
	} {
		err := noContractFor(c.target, c.description)
		if !errors.Is(err, ErrNoContractYet) {
			t.Errorf("%+v: not a missing contract: %v", c.target, err)
		}
		if err.Error() != c.want {
			t.Errorf("%+v:\n got %s\nwant %s", c.target, err, c.want)
		}
		fact := strings.SplitN(c.want, " — ", 2)[0]
		if got := noContractFact(err); got != fact {
			t.Errorf("%+v: the fact alone:\n got %s\nwant %s", c.target, got, fact)
		}
	}
}
