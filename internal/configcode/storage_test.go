package configcode

import (
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

// sizePtr is a small helper for the *int64 file_size_limit field.
func sizePtr(v int64) *int64 { return &v }

// TestSerializeStorage_Golden asserts exact TOML bytes for a
// representative bucket set — the determinism gate. Buckets are given in
// non-alphabetical order; output must sort by name.
func TestSerializeStorage_Golden(t *testing.T) {
	rows := []storageBucketRow{
		{ID: "uploads", Name: "uploads", Public: false},
		{
			ID:               "avatars",
			Name:             "avatars",
			Public:           true,
			FileSizeLimit:    sizePtr(5242880),
			AllowedMimeTypes: []string{"image/png", "image/jpeg"},
		},
	}

	got, err := serializeStorage(rows)
	require.NoError(t, err)

	const want = `# config/storage.toml — storage bucket metadata (config-as-code, Faz 1).
#
# READ-ONLY MIRROR of server state. ` + "`palbase pull`" + ` overwrites
# this file; this module has no push contract yet. Editing here does not
# change the server.
#
# Each [buckets.<name>] is a storage bucket's metadata. RLS policies are
# NOT config — they live in db/migrations as SQL (Supabase model). File
# contents are runtime data and are never pulled here.

[buckets]
  [buckets.avatars]
    public = true
    file_size_limit = 5242880
    allowed_mime_types = ["image/png", "image/jpeg"]
  [buckets.uploads]
    public = false
`
	require.Equal(t, want, string(got))
}

// TestSerializeStorage_Deterministic runs the same input twice and
// asserts byte-identical output (independent of Go map iteration order).
func TestSerializeStorage_Deterministic(t *testing.T) {
	rows := []storageBucketRow{
		{ID: "zeta", Name: "zeta", Public: true},
		{ID: "alpha", Name: "alpha", Public: false, FileSizeLimit: sizePtr(1024)},
		{ID: "mid", Name: "mid", Public: true, AllowedMimeTypes: []string{"text/plain"}},
	}
	a, err := serializeStorage(rows)
	require.NoError(t, err)
	b, err := serializeStorage(rows)
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
}

// TestSerializeStorage_NullableFieldsOmitted asserts that an unset
// file_size_limit / allowed_mime_types are OMITTED, not emitted as
// `= 0` / `= []`. A nil limit means "no limit", which differs from 0.
func TestSerializeStorage_NullableFieldsOmitted(t *testing.T) {
	got, err := serializeStorage([]storageBucketRow{
		{ID: "plain", Name: "plain", Public: false},
	})
	require.NoError(t, err)
	require.Contains(t, string(got), "public = false")
	require.NotContains(t, string(got), "file_size_limit")
	require.NotContains(t, string(got), "allowed_mime_types")
}

// TestSerializeStorage_Empty asserts a project with no buckets produces a
// valid header-only document (no bare [buckets] table).
func TestSerializeStorage_Empty(t *testing.T) {
	got, err := serializeStorage(nil)
	require.NoError(t, err)
	require.Contains(t, string(got), "READ-ONLY MIRROR")
	require.NotContains(t, string(got), "[buckets]")
}

// TestSerializeStorage_RoundTrip decodes the emitted TOML back and checks
// the values survive the trip — the format is meant to be reviewed and
// (later) pushed.
func TestSerializeStorage_RoundTrip(t *testing.T) {
	rows := []storageBucketRow{
		{
			ID:               "avatars",
			Name:             "avatars",
			Public:           true,
			FileSizeLimit:    sizePtr(5242880),
			AllowedMimeTypes: []string{"image/png", "image/jpeg"},
		},
		{ID: "uploads", Name: "uploads", Public: false},
	}
	got, err := serializeStorage(rows)
	require.NoError(t, err)

	var doc storageDoc
	require.NoError(t, toml.Unmarshal(got, &doc))

	require.Len(t, doc.Buckets, 2)

	avatars := doc.Buckets["avatars"]
	require.True(t, avatars.Public)
	require.NotNil(t, avatars.FileSizeLimit)
	require.Equal(t, int64(5242880), *avatars.FileSizeLimit)
	require.Equal(t, []string{"image/png", "image/jpeg"}, avatars.AllowedMimeTypes)

	uploads := doc.Buckets["uploads"]
	require.False(t, uploads.Public)
	require.Nil(t, uploads.FileSizeLimit)
	require.Empty(t, uploads.AllowedMimeTypes)
}
