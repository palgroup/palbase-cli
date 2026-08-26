package backend

// stack_spec_test.go — what a project says about its own contract reaches the
// person who asked.

import (
	"context"
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
