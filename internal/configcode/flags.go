package configcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/BurntSushi/toml"
	"github.com/palgroup/palbase-cli/internal/studio"
)

func init() { Register(flagsSerializer{}) }

// flagsSerializer is the reference ModuleSerializer: it pulls the
// project's system flags and writes config/flags.toml. The
// auth/storage/documents/notifications serializers mirror this shape.
type flagsSerializer struct{}

func (flagsSerializer) Name() string     { return "flags" }
func (flagsSerializer) Filename() string { return "flags.toml" }

// systemFlagRow mirrors the `userFlags.system.list` tRPC response (and
// the palflags SystemFlag model at
// modules/user-flags/internal/model/system_flag.go). value + variants
// are opaque JSON: value is a typed scalar/object, variants is either
// null or an array of {value, weight} per the user-flags spec (line
// 121). deleted_at marks a soft-delete tombstone — filtered out of the
// pull so config/ reflects live state only.
type systemFlagRow struct {
	Key         string          `json:"key"`
	ValueType   string          `json:"value_type"`
	Value       json.RawMessage `json:"value"`
	Variants    json.RawMessage `json:"variants"`
	Description string          `json:"description"`
	DeletedAt   *string         `json:"deleted_at"`
}

// flagsDoc is the root of config/flags.toml. Using map[string]flagEntry
// is safe for determinism: BurntSushi/toml sorts map keys when encoding
// (verified), so flags appear alphabetically and identical runs produce
// byte-identical output.
//
// TOML mapping (documented for round-trip + the parallel module authors):
//
//	[flags.<key>]
//	type = "bool" | "number" | "string" | "object"   ← value_type
//	default = <typed value>                           ← value (decoded JSON)
//	description = "..."                                ← omitted if empty
//
//	# variants (A/B) — positional array per user-flags spec; the server
//	# stores [{value, weight}, ...] with NO names, so we emit an
//	# array-of-tables (NOT the named [flags.X.variants.control] form the
//	# plan sketched — that would invent names that don't round-trip):
//	[[flags.<key>.variants]]
//	value = <typed>
//	weight = 50
//
// CONDITIONS (flags v2): NOT emitted in v1 (palflags has no conditions).
// The format leaves room — v2 will add `[[flags.<key>.conditions]]`
// array-of-tables alongside variants without breaking this v1 layout.
type flagsDoc struct {
	Flags map[string]flagEntry `toml:"flags"`
}

type flagEntry struct {
	Type        string        `toml:"type"`
	Default     any           `toml:"default"`
	Description string        `toml:"description,omitempty"`
	Variants    []flagVariant `toml:"variants,omitempty"`
}

type flagVariant struct {
	Value  any `toml:"value"`
	Weight int `toml:"weight"`
}

// rawVariant decodes a single server-side variant entry. Weight is an
// int per spec ("weight toplamı 100 olmalı"); value is opaque typed JSON.
type rawVariant struct {
	Value  json.RawMessage `json:"value"`
	Weight int             `json:"weight"`
}

const flagsHeader = `# config/flags.toml — system flag definitions (config-as-code, Faz 1).
#
# READ-ONLY MIRROR of server state. ` + "`palbase backend config pull`" + ` overwrites
# this file; there is no push contract yet (Faz 2). Editing here does not
# change the server.
#
# Each [flags.<key>] is a project-wide system flag default. User
# overrides (runtime, per-user) are NOT config — they live in the flags
# service and are not pulled here.

`

// Pull fetches system flags via userFlags.system.list and serializes
// them to TOML. An empty project (no flags) still produces a valid
// header-only document so the file exists for diffing.
//
// The tRPC path is the camelCase key the root router mounts the flags
// router under (platform/studio/src/server/trpc/router.ts:27 —
// `userFlags: userFlagsRouter`), NOT the hyphenated module name. tRPC
// paths are the JS object keys, so this must match the mount key exactly
// or every pull 404s.
func (flagsSerializer) Pull(ctx context.Context, ref string, sc *studio.Client) ([]byte, error) {
	var rows []systemFlagRow
	if err := sc.Query(ctx, "userFlags.system.list", map[string]any{"ref": ref}, &rows); err != nil {
		return nil, fmt.Errorf("userFlags.system.list: %w", err)
	}
	return serializeFlags(rows)
}

// serializeFlags is the pure, testable core: rows → deterministic TOML.
// Split out from Pull so unit tests cover the mapping without a live
// tRPC client.
func serializeFlags(rows []systemFlagRow) ([]byte, error) {
	doc := flagsDoc{Flags: map[string]flagEntry{}}
	for _, row := range rows {
		if row.DeletedAt != nil {
			continue // tombstone — live state only
		}
		entry, err := flagEntryFromRow(row)
		if err != nil {
			return nil, fmt.Errorf("flag %q: %w", row.Key, err)
		}
		doc.Flags[row.Key] = entry
	}

	var buf bytes.Buffer
	buf.WriteString(flagsHeader)
	// Header-only document when there are no flags: skip the encoder so
	// we don't emit a bare `[flags]` table for an empty map.
	if len(doc.Flags) > 0 {
		if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
			return nil, fmt.Errorf("encode toml: %w", err)
		}
	}
	return buf.Bytes(), nil
}

// flagEntryFromRow maps one server flag row to its TOML entry, decoding
// the opaque value/variants JSON into typed Go values BurntSushi/toml
// can encode natively (and deterministically — it sorts object keys).
func flagEntryFromRow(row systemFlagRow) (flagEntry, error) {
	entry := flagEntry{
		Type:        row.ValueType,
		Description: row.Description,
	}

	defaultVal, err := decodeJSONValue(row.Value)
	if err != nil {
		return flagEntry{}, fmt.Errorf("decode value: %w", err)
	}
	entry.Default = defaultVal

	variants, err := decodeVariants(row.Variants)
	if err != nil {
		return flagEntry{}, fmt.Errorf("decode variants: %w", err)
	}
	entry.Variants = variants

	return entry, nil
}

// decodeJSONValue turns an opaque json.RawMessage into a Go value the
// TOML encoder accepts. Empty/null raw → nil (TOML omits via the
// encoder's nil handling is not automatic, so callers relying on a
// present `default` should note a JSON null flag becomes a missing
// default — acceptable: a null-valued system flag is degenerate).
func decodeJSONValue(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// decodeVariants decodes the server's positional variant array. null /
// empty → no variants. Order is preserved as-is from the server (the
// server returns a stable array), giving deterministic output.
func decodeVariants(raw json.RawMessage) ([]flagVariant, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var rawVs []rawVariant
	if err := json.Unmarshal(raw, &rawVs); err != nil {
		return nil, err
	}
	if len(rawVs) == 0 {
		return nil, nil
	}
	out := make([]flagVariant, 0, len(rawVs))
	for _, rv := range rawVs {
		val, err := decodeJSONValue(rv.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, flagVariant{Value: val, Weight: rv.Weight})
	}
	return out, nil
}
