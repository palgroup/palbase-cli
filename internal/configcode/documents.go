package configcode

import (
	"bytes"
	"context"
	"fmt"

	"github.com/BurntSushi/toml"
	"github.com/palgroup/palbase-cli/internal/studio"
)

func init() { Register(documentsSerializer{}) }

// documentsSerializer pulls the project's collection security rules and
// writes config/documents.toml. Mirrors flags.go: a pure
// serializeDocuments core fed by Pull, deterministic struct-based TOML,
// header-only document when the project has no rules.
type documentsSerializer struct{}

func (documentsSerializer) Name() string     { return "documents" }
func (documentsSerializer) Filename() string { return "documents.toml" }

// documentsRulesResponse mirrors the `documents.rules.list` tRPC
// response (platform/studio/src/server/trpc/routers/documents.ts:307,
// RulesListResponse at line 109): the procedure wraps the rule array in
// `{ rules: [...] }`. NOTE: this is an OBJECT, not a bare array — see the
// shape note in the task report.
type documentsRulesResponse struct {
	Rules []securityRuleRow `json:"rules"`
}

// securityRuleRow mirrors SecurityRuleResponse (documents.ts:101-107).
// id/createdAt/updatedAt are server-managed runtime metadata, not config
// — only collection + the predicate map are pulled.
type securityRuleRow struct {
	Collection string   `json:"collection"`
	Rules      rulesMap `json:"rules"`
}

// rulesMap mirrors RulesMap (documents.ts:93-98): app-level JSON
// predicate expression STRINGS (paldocs JSONB, NOT pg DDL). All four are
// optional — an unset predicate is omitted from the TOML.
type rulesMap struct {
	Read   string `json:"read"`
	Create string `json:"create"`
	Update string `json:"update"`
	Delete string `json:"delete"`
}

// documentsDoc is the root of config/documents.toml.
// map[string]collectionEntry is deterministic: BurntSushi/toml sorts map
// keys when encoding, so collections appear alphabetically and identical
// runs produce byte-identical output.
//
// TOML mapping:
//
//	[collections.<name>]
//	read   = "auth.uid != null"        ← omitted when empty
//	create = "auth.role == 'admin'"    ← omitted when empty
//	update = "auth.uid == resource.owner_id"
//	delete = "auth.role == 'admin'"
type documentsDoc struct {
	Collections map[string]collectionEntry `toml:"collections"`
}

type collectionEntry struct {
	Read   string `toml:"read,omitempty"`
	Create string `toml:"create,omitempty"`
	Update string `toml:"update,omitempty"`
	Delete string `toml:"delete,omitempty"`
}

const documentsHeader = `# config/documents.toml — collection security rules (config-as-code, Faz 1).
#
# READ-ONLY MIRROR of server state. ` + "`palbase pull`" + ` overwrites
# this file; this module has no push contract yet. Editing here does not
# change the server.
#
# Each [collections.<name>] holds the app-level security predicate
# strings {read, create, update, delete} for a paldocs collection. These
# are JSON predicate expressions evaluated by the documents service — NOT
# pg RLS/DDL (which lives in db/migrations). Document data is runtime and
# is never pulled here.

`

// Pull fetches collection security rules via documents.rules.list and
// serializes them to TOML. An empty project (no rules) still produces a
// valid header-only document so the file exists for diffing.
//
// The tRPC path is the camelCase key the root router mounts the
// documents router under (platform/studio/src/server/trpc/router.ts:24 —
// `documents: documentsRouter`), followed by the nested `rules.list`
// procedure. tRPC paths are the JS object keys, so this must match the
// mount key exactly or the pull 404s.
func (documentsSerializer) Pull(ctx context.Context, ref string, sc *studio.Client) ([]byte, error) {
	var resp documentsRulesResponse
	if err := sc.Query(ctx, "documents.rules.list", map[string]any{"ref": ref}, &resp); err != nil {
		return nil, fmt.Errorf("documents.rules.list: %w", err)
	}
	return serializeDocuments(resp.Rules)
}

// serializeDocuments is the pure, testable core: rule rows →
// deterministic TOML. Split out from Pull so unit tests cover the
// mapping without a live tRPC client.
func serializeDocuments(rows []securityRuleRow) ([]byte, error) {
	doc := documentsDoc{Collections: map[string]collectionEntry{}}
	for _, row := range rows {
		doc.Collections[row.Collection] = collectionEntry{
			Read:   row.Rules.Read,
			Create: row.Rules.Create,
			Update: row.Rules.Update,
			Delete: row.Rules.Delete,
		}
	}

	var buf bytes.Buffer
	buf.WriteString(documentsHeader)
	// Header-only document when there are no rules: skip the encoder so
	// we don't emit a bare `[collections]` table for an empty map.
	if len(doc.Collections) > 0 {
		if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
			return nil, fmt.Errorf("encode toml: %w", err)
		}
	}
	return buf.Bytes(), nil
}
