package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteEnvLocal tests the writeEnvLocal helper directly:
// it writes a .env.local file with KEY=value lines and ensures
// .env.local is present in the project's .gitignore.
func TestWriteEnvLocal(t *testing.T) {
	t.Run("writes KEY=value lines for each var", func(t *testing.T) {
		dir := t.TempDir()

		vars := []envVar{
			{Key: "GOOGLE_MAPS_KEY", Value: "AIzaSy_abc123"},
			{Key: "DATABASE_URL", Value: "postgres://localhost/mydb"},
		}

		if err := writeEnvLocal(dir, vars); err != nil {
			t.Fatalf("writeEnvLocal: %v", err)
		}

		got, err := os.ReadFile(filepath.Join(dir, ".env.local"))
		if err != nil {
			t.Fatalf("read .env.local: %v", err)
		}

		content := string(got)
		if !strings.Contains(content, "GOOGLE_MAPS_KEY=AIzaSy_abc123") {
			t.Errorf(".env.local missing GOOGLE_MAPS_KEY line, got:\n%s", content)
		}
		if !strings.Contains(content, "DATABASE_URL=postgres://localhost/mydb") {
			t.Errorf(".env.local missing DATABASE_URL line, got:\n%s", content)
		}
	})

	t.Run("does not write file when vars is empty", func(t *testing.T) {
		dir := t.TempDir()

		if err := writeEnvLocal(dir, nil); err != nil {
			t.Fatalf("writeEnvLocal: %v", err)
		}

		_, err := os.Stat(filepath.Join(dir, ".env.local"))
		if !os.IsNotExist(err) {
			t.Errorf("expected .env.local to not exist when vars is empty, err=%v", err)
		}
	})

	t.Run("ensures .env.local is in .gitignore", func(t *testing.T) {
		dir := t.TempDir()

		vars := []envVar{{Key: "FOO", Value: "bar"}}
		if err := writeEnvLocal(dir, vars); err != nil {
			t.Fatalf("writeEnvLocal: %v", err)
		}

		gi, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if !strings.Contains(string(gi), ".env.local") {
			t.Errorf(".gitignore missing .env.local entry, got:\n%s", string(gi))
		}
	})

	t.Run("idempotent when .env.local already in .gitignore", func(t *testing.T) {
		dir := t.TempDir()

		// Write .gitignore that already contains .env.local
		existing := "node_modules/\n.env.local\n"
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}

		vars := []envVar{{Key: "FOO", Value: "bar"}}
		if err := writeEnvLocal(dir, vars); err != nil {
			t.Fatalf("writeEnvLocal: %v", err)
		}

		gi, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		// Should not be duplicated
		if strings.Count(string(gi), ".env.local") != 1 {
			t.Errorf(".env.local appears more than once in .gitignore:\n%s", string(gi))
		}
	})

	t.Run("values containing spaces are quoted", func(t *testing.T) {
		dir := t.TempDir()

		vars := []envVar{{Key: "GREETING", Value: "hello world"}}
		if err := writeEnvLocal(dir, vars); err != nil {
			t.Fatalf("writeEnvLocal: %v", err)
		}

		got, err := os.ReadFile(filepath.Join(dir, ".env.local"))
		if err != nil {
			t.Fatalf("read .env.local: %v", err)
		}
		content := string(got)
		if !strings.Contains(content, `GREETING="hello world"`) {
			t.Errorf("expected quoted value, got:\n%s", content)
		}
	})
}
