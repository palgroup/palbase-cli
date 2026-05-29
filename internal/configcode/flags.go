package configcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

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
# READ-ONLY MIRROR of server state. ` + "`palbase pull`" + ` overwrites
# this file; edit it and run ` + "`palbase push`" + ` to apply. Editing here
# alone does not change the server.
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
func (flagsSerializer) Pull(ctx context.Context, ref, branch string, sc *studio.Client) ([]byte, error) {
	var rows []systemFlagRow
	if err := sc.Query(ctx, "userFlags.system.list", refPayload(ref, branch), &rows); err != nil {
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

// --- Faz 2: push ----------------------------------------------------
//
// flagsSerializer also implements [ModulePusher]: it parses config/
// flags.toml and upserts each flag to the server via userFlags.system.put.
// It is the only module with push support this phase.

// flagsPutInput mirrors the userFlags.system.put tRPC input shape
// (user-flags.ts:121 — key/valueType/value/description). NOTE: that SET
// procedure does NOT accept `variants`, so push can only sync
// type/value/description. A local flag that declares variants is upserted
// without them and a warning is surfaced — variant push lands when the
// SET API grows the field (Faz 3+).
type flagsPutInput struct {
	Ref         string `json:"ref"`
	Branch      string `json:"branch,omitempty"`
	Key         string `json:"key"`
	ValueType   string `json:"valueType"`
	Value       any    `json:"value"`
	Description string `json:"description,omitempty"`
}

// Push parses tomlBytes (config/flags.toml) and upserts each flag to the
// server. It diffs the parsed local entry against the current server row
// and calls userFlags.system.put ONLY for flags that are new or changed —
// so an unchanged file makes zero mutations (idempotent). Server flags
// absent from the local file are reported as orphans and left untouched
// (upsert-only; delete is Faz 3).
func (flagsSerializer) Push(ctx context.Context, ref, branch string, sc *studio.Client, tomlBytes []byte) (PushApplied, error) {
	var doc flagsDoc
	if err := toml.Unmarshal(tomlBytes, &doc); err != nil {
		return PushApplied{}, fmt.Errorf("parse flags.toml: %w", err)
	}

	var rows []systemFlagRow
	if err := sc.Query(ctx, "userFlags.system.list", refPayload(ref, branch), &rows); err != nil {
		return PushApplied{}, fmt.Errorf("userFlags.system.list: %w", err)
	}
	server := make(map[string]systemFlagRow, len(rows))
	for _, r := range rows {
		if r.DeletedAt != nil {
			continue // tombstone — treat as absent
		}
		server[r.Key] = r
	}

	applied := PushApplied{}
	for key, entry := range doc.Flags {
		// userFlags.system.put has no variants field, so a local flag that
		// declares A/B variants cannot push them. Surface it rather than
		// silently dropping the intent (variant push is Faz 3+).
		if len(entry.Variants) > 0 {
			applied.Ignored = append(applied.Ignored, key)
		}
		changed, err := flagDiffersFromServer(entry, server[key])
		if err != nil {
			return PushApplied{}, fmt.Errorf("flag %q: %w", key, err)
		}
		if !changed {
			continue
		}
		in := flagsPutInput{
			Ref:         ref,
			Branch:      branch,
			Key:         key,
			ValueType:   entry.Type,
			Value:       entry.Default,
			Description: entry.Description,
		}
		if err := sc.Mutation(ctx, "userFlags.system.put", in, nil); err != nil {
			return PushApplied{}, fmt.Errorf("userFlags.system.put %q: %w", key, err)
		}
		applied.Set++
	}

	for key := range server {
		if _, inLocal := doc.Flags[key]; !inLocal {
			applied.Orphans = append(applied.Orphans, key)
		}
	}
	sort.Strings(applied.Orphans)
	sort.Strings(applied.Ignored)
	return applied, nil
}

// flagDiffersFromServer reports whether the local entry needs a SET: true
// if the flag is absent server-side, or if its type/value/description
// differ. Comparison is on the fields userFlags.system.put can write
// (type/value/description); variants are NOT settable via that API so
// they are excluded from the diff. Value equality is by canonical JSON so
// 42 == 42.0 and object key order doesn't cause spurious SETs.
func flagDiffersFromServer(local flagEntry, srv systemFlagRow) (bool, error) {
	if srv.Key == "" {
		return true, nil // not present server-side → must create
	}
	if local.Type != srv.ValueType {
		return true, nil
	}
	if local.Description != srv.Description {
		return true, nil
	}
	localVal, err := canonicalJSON(local.Default)
	if err != nil {
		return false, fmt.Errorf("encode local value: %w", err)
	}
	srvDecoded, err := decodeJSONValue(srv.Value)
	if err != nil {
		return false, fmt.Errorf("decode server value: %w", err)
	}
	srvVal, err := canonicalJSON(srvDecoded)
	if err != nil {
		return false, fmt.Errorf("encode server value: %w", err)
	}
	return localVal != srvVal, nil
}

// canonicalJSON marshals v to JSON. encoding/json sorts object keys, so
// two semantically-equal values produce identical bytes regardless of map
// ordering — the basis for the value diff.
func canonicalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
