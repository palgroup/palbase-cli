package configcode

import (
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

// TestSerializeDocuments_Golden asserts exact TOML bytes for a
// representative rule set — the determinism gate. Collections are given
// in non-alphabetical order; output must sort by name.
func TestSerializeDocuments_Golden(t *testing.T) {
	rows := []securityRuleRow{
		{
			Collection: "rooms",
			Rules: rulesMap{
				Read:   "auth.uid != null",
				Create: "auth.role == 'admin'",
				Update: "auth.uid == resource.owner_id",
				Delete: "auth.role == 'admin'",
			},
		},
		{
			Collection: "posts",
			Rules:      rulesMap{Read: "true"},
		},
	}

	got, err := serializeDocuments(rows)
	require.NoError(t, err)

	const want = `# config/documents.toml — collection security rules (config-as-code, Faz 1).
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

[collections]
  [collections.posts]
    read = "true"
  [collections.rooms]
    read = "auth.uid != null"
    create = "auth.role == 'admin'"
    update = "auth.uid == resource.owner_id"
    delete = "auth.role == 'admin'"
`
	require.Equal(t, want, string(got))
}

// TestSerializeDocuments_Deterministic runs the same input twice and
// asserts byte-identical output (independent of Go map iteration order).
func TestSerializeDocuments_Deterministic(t *testing.T) {
	rows := []securityRuleRow{
		{Collection: "zeta", Rules: rulesMap{Read: "true"}},
		{Collection: "alpha", Rules: rulesMap{Create: "auth.uid != null"}},
		{Collection: "mid", Rules: rulesMap{Delete: "false"}},
	}
	a, err := serializeDocuments(rows)
	require.NoError(t, err)
	b, err := serializeDocuments(rows)
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
}

// TestSerializeDocuments_EmptyPredicatesOmitted asserts unset predicate
// strings are dropped rather than emitted as `read = ""`.
func TestSerializeDocuments_EmptyPredicatesOmitted(t *testing.T) {
	got, err := serializeDocuments([]securityRuleRow{
		{Collection: "sparse", Rules: rulesMap{Update: "auth.uid == resource.owner_id"}},
	})
	require.NoError(t, err)
	require.Contains(t, string(got), `update = "auth.uid == resource.owner_id"`)
	require.NotContains(t, string(got), "read =")
	require.NotContains(t, string(got), "create =")
	require.NotContains(t, string(got), "delete =")
}

// TestSerializeDocuments_Empty asserts a project with no rules produces a
// valid header-only document (no bare [collections] table).
func TestSerializeDocuments_Empty(t *testing.T) {
	got, err := serializeDocuments(nil)
	require.NoError(t, err)
	require.Contains(t, string(got), "READ-ONLY MIRROR")
	require.NotContains(t, string(got), "[collections]")
}

// TestSerializeDocuments_RoundTrip decodes the emitted TOML back and
// checks predicate strings survive the trip — the format is meant to be
// reviewed and (later) pushed.
func TestSerializeDocuments_RoundTrip(t *testing.T) {
	rows := []securityRuleRow{
		{
			Collection: "rooms",
			Rules: rulesMap{
				Read:   "auth.uid != null",
				Create: "auth.role == 'admin'",
				Update: "auth.uid == resource.owner_id",
				Delete: "auth.role == 'admin'",
			},
		},
		{Collection: "posts", Rules: rulesMap{Read: "true"}},
	}
	got, err := serializeDocuments(rows)
	require.NoError(t, err)

	var doc documentsDoc
	require.NoError(t, toml.Unmarshal(got, &doc))

	require.Len(t, doc.Collections, 2)

	rooms := doc.Collections["rooms"]
	require.Equal(t, "auth.uid != null", rooms.Read)
	require.Equal(t, "auth.role == 'admin'", rooms.Create)
	require.Equal(t, "auth.uid == resource.owner_id", rooms.Update)
	require.Equal(t, "auth.role == 'admin'", rooms.Delete)

	posts := doc.Collections["posts"]
	require.Equal(t, "true", posts.Read)
	require.Empty(t, posts.Create)
	require.Empty(t, posts.Update)
	require.Empty(t, posts.Delete)
}
