package scaffold

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chdirTemp moves into a fresh temp dir for the duration of a test so init's
// default-cwd path is isolated. Restores cwd on cleanup.
func chdirTemp(t *testing.T) string {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}

// run drives the init command with args, capturing stdout+stderr.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := Cmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

var scaffoldFiles = []string{
	"package.json",
	"tsconfig.json",
	filepath.Join("db", "schema.ts"),
	filepath.Join("models", "hello", "shared.ts"),
	filepath.Join("controllers", "hello.controller.ts"),
	".gitignore",
}

func TestInit_EmptyDir(t *testing.T) {
	chdirTemp(t)
	out, err := run(t, "myapp")
	require.NoError(t, err)
	assert.Contains(t, out, `scaffolding Palbase backend project "myapp"`)
	assert.Contains(t, out, "npm install")
	assert.Contains(t, out, "palbase serve")
	assert.Contains(t, out, "palbase push")

	for _, f := range scaffoldFiles {
		_, statErr := os.Stat(filepath.Join("myapp", f))
		assert.NoError(t, statErr, f)
	}

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join("myapp", name))
		require.NoError(t, err)
		return string(data)
	}
	assert.Contains(t, read("package.json"), `"name": "myapp"`)
	assert.Contains(t, read("package.json"), `"@palbase/backend": "^8.0.0"`)
	assert.Contains(t, read("tsconfig.json"), `"experimentalDecorators": true`)
	assert.Contains(t, read(filepath.Join("db", "schema.ts")), "export default defineSchema({")
	ctrl := read(filepath.Join("controllers", "hello.controller.ts"))
	assert.Contains(t, ctrl, "export default class HelloController")
	assert.Contains(t, ctrl, `@Controller("/hello"`)
	// The runtime REQUIRES a NAMED zod schema as the return type — an inline
	// object type is rejected and the endpoint registers 0 routes (found by
	// the live `palbase serve` smoke). Lock the named-schema shape.
	assert.Contains(t, ctrl, "hello(): HelloSchema")
	assert.Contains(t, ctrl, `import { HelloSchema } from "../models/hello/shared"`)
	assert.NotContains(t, ctrl, "Promise<{")
	assert.NotContains(t, ctrl, "@Returns")
	model := read(filepath.Join("models", "hello", "shared.ts"))
	assert.Contains(t, model, "export const HelloSchema = z.object({")
	assert.Contains(t, model, "export type HelloSchema = z.infer<typeof HelloSchema>")
	assert.Contains(t, read(".gitignore"), "node_modules/")
}

func TestInit_DefaultsToCwd(t *testing.T) {
	dir := chdirTemp(t)
	_, err := run(t)
	require.NoError(t, err)
	for _, f := range scaffoldFiles {
		_, statErr := os.Stat(filepath.Join(dir, f))
		assert.NoError(t, statErr, f)
	}
}

func TestInit_RefusesExistingProject(t *testing.T) {
	dir := chdirTemp(t)
	require.NoError(t, os.WriteFile("package.json", []byte("{}"), 0o644))

	_, err := run(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already a project")

	// Nothing else was written.
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Len(t, entries, 1)
	assert.Equal(t, "package.json", entries[0].Name())
	// And the existing package.json is untouched.
	data, readFileErr := os.ReadFile("package.json")
	require.NoError(t, readFileErr)
	assert.Equal(t, "{}", string(data))
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"lowercase passthrough", "todoapp", "todoapp"},
		{"space to dash", "My App", "my-app"},
		{"multiple invalid chars collapse", "My  Cool App!", "my-cool-app"},
		{"allowed punctuation kept", "my_app.v2", "my_app.v2"},
		{"trims edge separators", "-app-", "app"},
		{"all invalid falls back", "!!!", "palbase-backend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, sanitizeName(tt.input))
		})
	}
}
