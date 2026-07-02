package storage

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chdirTemp moves into a fresh temp dir for the duration of a test so each test
// gets its own config/storage.ts. Restores cwd on cleanup.
func chdirTemp(t *testing.T) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// run drives the storage command with args, capturing stdout.
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

func readFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	return string(data)
}

func TestAdd_WritesConfigFile(t *testing.T) {
	chdirTemp(t)
	out, err := run(t, "add", "avatars", "--public", "--max-size", "5MB", "--mime", "image/png,image/jpeg")
	require.NoError(t, err)
	assert.Contains(t, out, "added bucket \"avatars\"")

	src := readFile(t)
	// Valid, importable, typed config.
	assert.Contains(t, src, `import { defineStorage, bucket } from "@palbase/backend";`)
	assert.Contains(t, src, "export default defineStorage({")
	// 5MB -> 5242880 bytes; public + mimes present.
	assert.Contains(t, src, "avatars: bucket({ public: true, fileSizeLimit: 5242880, allowedMimeTypes: [\"image/png\", \"image/jpeg\"] }),")

	// Re-parsing the generated file yields the same bucket.
	buckets, err := readConfig()
	require.NoError(t, err)
	require.Contains(t, buckets, "avatars")
	assert.True(t, buckets["avatars"].Public)
	require.NotNil(t, buckets["avatars"].FileSizeLimit)
	assert.Equal(t, int64(5242880), *buckets["avatars"].FileSizeLimit)
	assert.Equal(t, []string{"image/png", "image/jpeg"}, buckets["avatars"].AllowedMimeTypes)
}

func TestAdd_BareBucketHasMinimalOpts(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "docs")
	require.NoError(t, err)
	src := readFile(t)
	// All defaults → compact `bucket({})`.
	assert.Contains(t, src, "docs: bucket({}),")
}

func TestAdd_SecondAddUpdatesNotDuplicates(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "avatars", "--public")
	require.NoError(t, err)
	out, err := run(t, "add", "avatars", "--max-size", "10MB")
	require.NoError(t, err)
	assert.Contains(t, out, "updated bucket \"avatars\"")

	src := readFile(t)
	// Exactly ONE avatars entry (no duplicate).
	assert.Equal(t, 1, strings.Count(src, "avatars: bucket("), "second add must update, not duplicate")
	// The update replaced the entry: public flag is gone (not passed this time),
	// the new size is present.
	buckets, err := readConfig()
	require.NoError(t, err)
	assert.False(t, buckets["avatars"].Public, "re-add without --public clears it (full replace)")
	require.NotNil(t, buckets["avatars"].FileSizeLimit)
	assert.Equal(t, int64(10*1024*1024), *buckets["avatars"].FileSizeLimit)
}

func TestAdd_MultipleBucketsCoexist(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "avatars", "--public")
	require.NoError(t, err)
	_, err = run(t, "add", "invoices", "--max-size", "20MB", "--mime", "application/pdf")
	require.NoError(t, err)

	buckets, err := readConfig()
	require.NoError(t, err)
	require.Len(t, buckets, 2)
	assert.True(t, buckets["avatars"].Public)
	assert.Equal(t, []string{"application/pdf"}, buckets["invoices"].AllowedMimeTypes)
}

func TestRemove_DropsEntry(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "avatars", "--public")
	require.NoError(t, err)
	_, err = run(t, "add", "invoices")
	require.NoError(t, err)

	out, err := run(t, "remove", "avatars")
	require.NoError(t, err)
	assert.Contains(t, out, "removed bucket \"avatars\"")
	assert.Contains(t, out, "NOT deleted")

	buckets, err := readConfig()
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	assert.NotContains(t, buckets, "avatars")
	assert.Contains(t, buckets, "invoices")
}

func TestRemove_UnknownBucketErrors(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "remove", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no bucket")
}

func TestList_EmptyAndPopulated(t *testing.T) {
	chdirTemp(t)
	// No file yet.
	out, err := run(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no buckets declared")

	_, err = run(t, "add", "avatars", "--public", "--max-size", "5MB")
	require.NoError(t, err)
	out, err = run(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "avatars")
	assert.Contains(t, out, "public")
	assert.Contains(t, out, "5MB")
}

func TestAdd_RejectsBadName(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "Bad Name!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bucket name")
}

func TestAdd_RejectsBadMime(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "x", "--mime", "notamime")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid MIME type")
}

func TestAdd_RejectsBadSize(t *testing.T) {
	chdirTemp(t)
	_, err := run(t, "add", "x", "--max-size", "5PB")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown --max-size unit")
}

func TestParseSize_BinaryUnits(t *testing.T) {
	cases := map[string]int64{
		"5MB":   5 * 1024 * 1024,
		"20MB":  20 * 1024 * 1024,
		"1GB":   1024 * 1024 * 1024,
		"500kb": 500 * 1024,
		"1024":  1024,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

func TestRoundTrip_QuotedKeyName(t *testing.T) {
	chdirTemp(t)
	// A name needing quoting as an object key (contains a dash).
	_, err := run(t, "add", "user-uploads", "--public")
	require.NoError(t, err)
	src := readFile(t)
	assert.Contains(t, src, `"user-uploads": bucket({ public: true }),`)
	buckets, err := readConfig()
	require.NoError(t, err)
	require.Contains(t, buckets, "user-uploads")
}

func TestReadConfig_RefusesUnrelatedFile(t *testing.T) {
	chdirTemp(t)
	require.NoError(t, os.MkdirAll("config", 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte("export const x = 1;\n"), 0o644))
	_, err := readConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not look like a defineStorage")
}
