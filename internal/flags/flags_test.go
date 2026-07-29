package flags

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chdirTemp moves into a fresh temp dir for the duration of a test so each test
// gets its own config/flags.ts. Restores cwd on cleanup.
func chdirTemp(t *testing.T) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// run drives the flags command with args, capturing stdout.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// Zero Resolvers: list/add/remove are pure local file authoring and touch
	// neither Studio nor the selection resolver.
	cmd := Cmd(Resolvers{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func readFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	return string(data)
}

func TestAdd_WritesConfigFile_Boolean(t *testing.T) {
	chdirTemp(t)
	out, err := run(t, "add", "new_dashboard", "--type", "boolean", "--default", "false", "--description", "Roll out")
	require.NoError(t, err)
	assert.Contains(t, out, "added flag \"new_dashboard\"")

	src := readFile(t)
	// Valid, importable, typed config.
	assert.Contains(t, src, `import { defineFlags, flag } from "@palbase/backend";`)
	assert.Contains(t, src, "export default defineFlags({")
	assert.Contains(t, src, `new_dashboard: flag({ type: "boolean", default: false, description: "Roll out" }),`)

	// Re-parsing the generated file yields the same flag.
	declared, err := readConfig()
	require.NoError(t, err)
	require.Contains(t, declared, "new_dashboard")
	assert.Equal(t, "boolean", declared["new_dashboard"].Type)
	assert.Equal(t, "false", declared["new_dashboard"].DefaultLiteral)
	assert.Equal(t, "Roll out", declared["new_dashboard"].Description)
}

func TestAdd_Number(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "max_uploads", "--type", "number", "--default", "10")
	require.NoError(t, err)
	src := readFile(t)
	assert.Contains(t, src, `max_uploads: flag({ type: "number", default: 10 }),`)

	declared, err := readConfig()
	require.NoError(t, err)
	assert.Equal(t, "10", declared["max_uploads"].DefaultLiteral)
}

func TestAdd_StringWithVariants(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "theme", "--type", "string", "--default", "light", "--variants", "light,dark,system")
	require.NoError(t, err)
	src := readFile(t)
	assert.Contains(t, src, `theme: flag({ type: "string", default: "light", variants: ["light", "dark", "system"] }),`)

	declared, err := readConfig()
	require.NoError(t, err)
	assert.Equal(t, `"light"`, declared["theme"].DefaultLiteral)
	assert.Equal(t, []string{"light", "dark", "system"}, declared["theme"].Variants)
}

func TestAdd_SecondAddUpdatesNotDuplicates(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "flag_x", "--type", "boolean", "--default", "true")
	require.NoError(t, err)
	out, err := run(t, "add", "flag_x", "--type", "number", "--default", "42")
	require.NoError(t, err)
	assert.Contains(t, out, "updated flag \"flag_x\"")

	src := readFile(t)
	// Exactly ONE flag_x entry (no duplicate).
	assert.Equal(t, 1, strings.Count(src, "flag_x: flag("), "second add must update, not duplicate")

	declared, err := readConfig()
	require.NoError(t, err)
	// The update fully replaced the entry: now a number=42.
	assert.Equal(t, "number", declared["flag_x"].Type)
	assert.Equal(t, "42", declared["flag_x"].DefaultLiteral)
}

func TestAdd_MultipleFlagsCoexist(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "a", "--type", "boolean", "--default", "true")
	require.NoError(t, err)
	_, err = run(t, "add", "b", "--type", "string", "--default", "x")
	require.NoError(t, err)

	declared, err := readConfig()
	require.NoError(t, err)
	require.Len(t, declared, 2)
	assert.Equal(t, "boolean", declared["a"].Type)
	assert.Equal(t, "string", declared["b"].Type)
}

func TestRemove_DropsEntry(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "a", "--type", "boolean", "--default", "true")
	require.NoError(t, err)
	_, err = run(t, "add", "b", "--type", "boolean", "--default", "false")
	require.NoError(t, err)

	out, err := run(t, "remove", "a")
	require.NoError(t, err)
	assert.Contains(t, out, "removed flag \"a\"")
	assert.Contains(t, out, "NOT deleted")

	declared, err := readConfig()
	require.NoError(t, err)
	require.Len(t, declared, 1)
	assert.NotContains(t, declared, "a")
	assert.Contains(t, declared, "b")
}

func TestRemove_UnknownFlagErrors(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "remove", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no flag")
}

func TestList_EmptyAndPopulated(t *testing.T) {
	chdirTemp(t)
	out, err := run(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no flags declared")

	_, err = run(t, "add", "theme", "--type", "string", "--default", "dark", "--variants", "light,dark")
	require.NoError(t, err)
	out, err = run(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "theme")
	assert.Contains(t, out, "string")
	assert.Contains(t, out, "light, dark")
}

func TestAdd_RejectsBadKey(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "bad-key", "--type", "boolean", "--default", "true")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid flag key")
}

func TestAdd_RejectsUnknownType(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "x", "--type", "object", "--default", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --type")
}

func TestAdd_RejectsBooleanDefaultMismatch(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "x", "--type", "boolean", "--default", "yes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be true or false")
}

func TestAdd_RejectsNumberDefaultMismatch(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "x", "--type", "number", "--default", "ten")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a number")
}

func TestAdd_RejectsVariantsOnNonString(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "x", "--type", "boolean", "--default", "true", "--variants", "a,b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only valid for --type string")
}

func TestAdd_RejectsDefaultNotInVariants(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "theme", "--type", "string", "--default", "blue", "--variants", "light,dark")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not one of --variants")
}

func TestAdd_RequiresTypeAndDefault(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--type is required")

	_, err = run(t, "add", "x", "--type", "boolean")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--default is required")
}

func TestRoundTrip_QuotedKeyName(t *testing.T) {
	chdirTemp(t)
	// A string default containing a comma/quote must round-trip losslessly.
	_, err := run(t, "add", "greeting", "--type", "string", "--default", `hi, "you"`)
	require.NoError(t, err)
	declared, err := readConfig()
	require.NoError(t, err)
	require.Contains(t, declared, "greeting")
	assert.Equal(t, strconvQuote(`hi, "you"`), declared["greeting"].DefaultLiteral)
}

func TestReadConfig_RefusesUnrelatedFile(t *testing.T) {
	chdirTemp(t)
	require.NoError(t, os.MkdirAll("config", 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte("export const x = 1;\n"), 0o644))
	_, err := readConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not look like a defineFlags")
}

// strconvQuote mirrors strconv.Quote for the expected literal in the round-trip
// test without importing strconv into the test (keeps the assertion explicit).
func strconvQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
