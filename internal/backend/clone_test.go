package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// TestSaveProjectConfigIn verifies the dir-targeted config writer lands
// the link under <dir>/.palbase/config.json while leaving the cwd untouched.
func TestSaveProjectConfigIn(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("myproj", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := auth.SaveProjectConfigIn("myproj", &auth.ProjectConfig{Ref: "ab12cd", DefaultEnv: "main"}); err != nil {
		t.Fatalf("SaveProjectConfigIn: %v", err)
	}
	// Config landed inside the target dir.
	if _, err := os.Stat(filepath.Join("myproj", ".palbase", "config.json")); err != nil {
		t.Fatalf("expected config in target dir: %v", err)
	}
	// And the cwd was not touched (no stray .palbase/ at the launch dir).
	if _, err := os.Stat(filepath.Join(".palbase", "config.json")); !os.IsNotExist(err) {
		t.Fatalf("cwd .palbase/config.json should not exist, err=%v", err)
	}
}
