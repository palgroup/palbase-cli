package configcode

import (
	"bytes"
	"context"
	"fmt"

	"github.com/BurntSushi/toml"
	"github.com/palgroup/palbase-cli/internal/studio"
)

func init() { Register(storageSerializer{}) }

// storageSerializer pulls the project's storage buckets and writes
// config/storage.toml. Mirrors flags.go: a pure serializeStorage core
// fed by Pull, deterministic struct-based TOML, header-only document
// when the project has no buckets.
type storageSerializer struct{}

func (storageSerializer) Name() string     { return "storage" }
func (storageSerializer) Filename() string { return "storage.toml" }

// storageListResponse mirrors the `storage.buckets.list` tRPC response
// (platform/studio/src/server/trpc/routers/storage.ts:63-68): the
// procedure wraps the bucket array in `{ buckets: [...] }`.
type storageListResponse struct {
	Buckets []storageBucketRow `json:"buckets"`
}

// storageBucketRow mirrors the StorageBucket wire type
// (storage.ts:13-22). Only the META fields below are config — RLS
// policies live in db/migrations (Supabase model, plan §3), and the
// owner/timestamps are server-managed runtime state, not config.
//
// FileSizeLimit is a *int64 (TS `number | null`): a nil pointer means
// "no limit set", which must be OMITTED rather than emitted as
// `file_size_limit = 0` (a real, different meaning).
type storageBucketRow struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Public           bool     `json:"public"`
	FileSizeLimit    *int64   `json:"file_size_limit"`
	AllowedMimeTypes []string `json:"allowed_mime_types"`
}

// storageDoc is the root of config/storage.toml. map[string]bucketEntry
// is deterministic: BurntSushi/toml sorts map keys when encoding, so
// buckets appear alphabetically and identical runs produce byte-identical
// output.
//
// TOML mapping:
//
//	[buckets.<name>]
//	public = true                              ← always present
//	file_size_limit = 5242880                  ← omitted when null
//	allowed_mime_types = ["image/png", ...]    ← omitted when null/empty
type storageDoc struct {
	Buckets map[string]bucketEntry `toml:"buckets"`
}

type bucketEntry struct {
	Public           bool     `toml:"public"`
	FileSizeLimit    *int64   `toml:"file_size_limit,omitempty"`
	AllowedMimeTypes []string `toml:"allowed_mime_types,omitempty"`
}

const storageHeader = `# config/storage.toml — storage bucket metadata (config-as-code, Faz 1).
#
# READ-ONLY MIRROR of server state. ` + "`palbase pull`" + ` overwrites
# this file; this module has no push contract yet. Editing here does not
# change the server.
#
# Each [buckets.<name>] is a storage bucket's metadata. RLS policies are
# NOT config — they live in db/migrations as SQL (Supabase model). File
# contents are runtime data and are never pulled here.

`

// Pull fetches storage buckets via storage.buckets.list and serializes
// their metadata to TOML. An empty project (no buckets) still produces a
// valid header-only document so the file exists for diffing.
//
// The tRPC path is the camelCase key the root router mounts the storage
// router under (platform/studio/src/server/trpc/router.ts:26 —
// `storage: storageRouter`), followed by the nested `buckets.list`
// procedure. tRPC paths are the JS object keys, so this must match the
// mount key exactly or the pull 404s.
func (storageSerializer) Pull(ctx context.Context, ref, branch string, sc *studio.Client) ([]byte, error) {
	var resp storageListResponse
	if err := sc.Query(ctx, "storage.buckets.list", refPayload(ref, branch), &resp); err != nil {
		return nil, fmt.Errorf("storage.buckets.list: %w", err)
	}
	return serializeStorage(resp.Buckets)
}

// serializeStorage is the pure, testable core: bucket rows →
// deterministic TOML. Split out from Pull so unit tests cover the
// mapping without a live tRPC client.
//
// The TOML key is the bucket's name (matching the [buckets.<name>] plan
// example). Supabase usually keeps id == name, but name is the
// human-recognizable identifier and matches the documented shape.
func serializeStorage(rows []storageBucketRow) ([]byte, error) {
	doc := storageDoc{Buckets: map[string]bucketEntry{}}
	for _, row := range rows {
		doc.Buckets[row.Name] = bucketEntry{
			Public:           row.Public,
			FileSizeLimit:    row.FileSizeLimit,
			AllowedMimeTypes: row.AllowedMimeTypes,
		}
	}

	var buf bytes.Buffer
	buf.WriteString(storageHeader)
	// Header-only document when there are no buckets: skip the encoder so
	// we don't emit a bare `[buckets]` table for an empty map.
	if len(doc.Buckets) > 0 {
		if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
			return nil, fmt.Errorf("encode toml: %w", err)
		}
	}
	return buf.Bytes(), nil
}
