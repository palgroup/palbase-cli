package backend

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// TestSanitizeProjectDir pins the name→directory rule for `palbase pull`
// clone-mode: lowercase, spaces/underscores→"-", non [a-z0-9-] stripped,
// "-" runs collapsed, edges trimmed, and the dangerous ""/"."/".." outputs
// mapped to "" so the caller falls back to the ref.
func TestSanitizeProjectDir(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"simple lowercase", "todos", "todos"},
		{"uppercase folded", "MyApp", "myapp"},
		{"spaces to dash", "My Cool App", "my-cool-app"},
		{"underscores to dash", "my_cool_app", "my-cool-app"},
		{"collapses dash runs", "a   --__  b", "a-b"},
		{"trims leading/trailing", "  --hello--  ", "hello"},
		{"strips punctuation", "café! (v2)#", "caf-v2"},
		{"strips slashes (no traversal)", "a/b/../c", "abc"},
		{"unicode dropped", "日本語app", "app"},
		{"empty input falls back (caller uses ref)", "", ""},
		{"all-punctuation falls back", "!@#$%", ""},
		{"dot maps to empty", ".", ""},
		{"dotdot maps to empty", "..", ""},
		{"dots stripped to empty", "...", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeProjectDir(tc.input); got != tc.want {
				t.Fatalf("sanitizeProjectDir(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestCloneTarget covers the directory-resolution + collision rule:
// new/empty dir is accepted, a non-empty existing dir is rejected, and an
// empty/unsanitizable name falls back to the project ref.
func TestCloneTarget(t *testing.T) {
	t.Run("uses sanitized name when dir absent", func(t *testing.T) {
		t.Chdir(t.TempDir())
		got, err := cloneTarget(&auth.Project{Name: "My App", Ref: "ab12cd"})
		if err != nil {
			t.Fatalf("cloneTarget: %v", err)
		}
		if got != "my-app" {
			t.Fatalf("got %q, want %q", got, "my-app")
		}
	})

	t.Run("falls back to ref when name sanitizes to empty", func(t *testing.T) {
		t.Chdir(t.TempDir())
		got, err := cloneTarget(&auth.Project{Name: "!!!", Ref: "ab12cd"})
		if err != nil {
			t.Fatalf("cloneTarget: %v", err)
		}
		if got != "ab12cd" {
			t.Fatalf("got %q, want ref fallback %q", got, "ab12cd")
		}
	})

	t.Run("accepts an existing but empty dir", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.Mkdir("todos", 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		got, err := cloneTarget(&auth.Project{Name: "todos", Ref: "ab12cd"})
		if err != nil {
			t.Fatalf("cloneTarget on empty existing dir should succeed, got: %v", err)
		}
		if got != "todos" {
			t.Fatalf("got %q, want %q", got, "todos")
		}
	})

	t.Run("rejects an existing non-empty dir", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.MkdirAll(filepath.Join("todos", "sub"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		_, err := cloneTarget(&auth.Project{Name: "todos", Ref: "ab12cd"})
		if err == nil {
			t.Fatal("expected error for non-empty existing dir, got nil")
		}
	})
}

// TestIsCloneMode pins the mode-detection guard. The endpoints/ check is the
// load-bearing safety: a project dir whose .palbase/ was deleted must NOT be
// treated as clone-mode (which would pull into a sibling and orphan the code).
func TestIsCloneMode(t *testing.T) {
	t.Run("empty unlinked dir → clone", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if !isCloneMode() {
			t.Fatal("empty unlinked dir should be clone-mode")
		}
	})

	t.Run("linked dir (has .palbase/config.json) → in-place", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := auth.SaveProjectConfig(&auth.ProjectConfig{Ref: "ab12cd", DefaultEnv: "main"}); err != nil {
			t.Fatalf("save config: %v", err)
		}
		if isCloneMode() {
			t.Fatal("a linked dir must stay in-place, not clone")
		}
	})

	t.Run("unlinked dir WITH endpoints/ → in-place (no clobber)", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := os.Mkdir("endpoints", 0o755); err != nil {
			t.Fatalf("mkdir endpoints: %v", err)
		}
		if isCloneMode() {
			t.Fatal("a dir with endpoints/ but no config must NOT be clone-mode")
		}
	})
}

// TestSaveProjectConfigIn verifies the new dir-targeted config writer lands
// the link under <dir>/.palbase/config.json (clone-mode's load-bearing call),
// while leaving the cwd untouched.
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
