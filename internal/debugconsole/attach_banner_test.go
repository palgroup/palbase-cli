package debugconsole

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// toServer sends every request, whatever host it names, to one test server.
type toServer struct {
	target *url.URL
	base   http.RoundTripper
}

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return t.base.RoundTrip(r)
}

// `palbase debug attach` NAMES THE ENVIRONMENT IT ATTACHES TO (FR-085). Its
// banner printed the address, which says nothing a person recognises about
// which of the project's environments armed the session.
func TestAttachAnnouncesTheEnvironmentItResolved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rt/v1/debug/sessions/resolve" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"session_id":"018f4c2a-6b1d-7a3e-9c55-2b7f0e1d4a88"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	prevTransport := http.DefaultTransport
	http.DefaultTransport = toServer{target: target, base: prevTransport}
	t.Cleanup(func() { http.DefaultTransport = prevTransport })

	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ENV", "")
	t.Setenv("PALBASE_ACCESS_TOKEN", "pb_project_cSTAGINGKEY")
	if err := os.MkdirAll(filepath.Join(dir, "palbase"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "palbase", "project.json"),
		[]byte(`{"project":"prd_a","name":"todoapp"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevEnvs, prevHost, prevFlag := backend.EnvironmentsOf, backend.TenantHost, backend.SelectedEnvFlag
	t.Cleanup(func() {
		backend.EnvironmentsOf, backend.TenantHost, backend.SelectedEnvFlag = prevEnvs, prevHost, prevFlag
	})
	backend.EnvironmentsOf = func(context.Context, string) ([]backend.Environment, error) {
		return []backend.Environment{
			{Name: "main", Ref: "mainref000", Status: "Running"},
			{Name: "staging", Ref: "stagref000", Status: "Running"},
		}, nil
	}
	backend.TenantHost = "palbase.test"
	backend.SelectedEnvFlag = "staging"

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(ctx)
	_, _ = attachToProject(cmd, "K7M4P2QX", false, false)

	if !strings.Contains(errOut.String(), "▸ todoapp/staging · attaching to ") {
		t.Fatalf("the attach banner did not name the environment:\n%s", errOut.String())
	}
}
