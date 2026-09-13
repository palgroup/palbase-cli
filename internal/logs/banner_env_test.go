package logs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/palgroup/palbase-cli/internal/backend"
)

type refusingREST struct{}

func (refusingREST) Do(context.Context, string, string, any, any) error {
	return errors.New("this test reaches no control plane")
}

// `palbase logs` ANNOUNCES THE ENVIRONMENT IT READS (FR-085). The banner was
// the target's own description — the project's name — so `--env staging` read
// staging's logs under a line that did not say so.
func TestLogsAnnounceTheEnvironmentTheyResolved(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PALBASE_ENV", "")
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

	cmd := Cmd(Resolvers{
		REST: func() REST { return refusingREST{} },
		CloudRef: func(url string) (string, bool) {
			return "stagref000", url == "https://stagref000.palbase.test"
		},
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{})
	cmd.SetContext(context.Background())
	_ = cmd.Execute()

	if !strings.Contains(errOut.String(), "▸ todoapp/staging\n") {
		t.Fatalf("the logs banner did not name the environment it read:\n%s", errOut.String())
	}
}
