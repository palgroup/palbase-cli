package backend

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDatabaseOperatorOptionsReachRuntime(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		requireToolOnCI(t, "docker", err)
		t.Skip("docker is unavailable")
	}
	path := filepath.Join(t.TempDir(), composeFile)
	if err := os.WriteFile(path, stackCompose, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "true"} {
		cmd := exec.Command("docker", "compose", "-f", path, "config", "--format", "json")
		cmd.Env = append(os.Environ(), "PALBASE_HTTP_PORT=1", "PALBASE_PROJECT_DIR=/tmp",
			"PALBASE_DB_COMMANDS="+value, "PALBASE_DB_DIAGNOSTICS="+value)
		for _, img := range stackImages {
			cmd.Env = append(cmd.Env, img.env+"=placeholder")
		}
		raw, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Services map[string]struct {
				Environment map[string]string `json:"environment"`
			} `json:"services"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		want := value
		if want == "" {
			want = "false"
		}
		for _, name := range []string{"PALBASE_DB_COMMANDS", "PALBASE_DB_DIAGNOSTICS"} {
			if got := cfg.Services["runtime"].Environment[name]; got != want {
				t.Errorf("runtime %s = %q, want %q", name, got, want)
			}
		}
	}
}
