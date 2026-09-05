package backend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchemaPlanShowsWhyPushWillBeRefused(t *testing.T) {
	var out strings.Builder
	renderSchemaPlan(&out, []byte(`{
  "in_sync": false,
  "changes": ["drop column merchants.category_locked (3 rows, 3 non-null)"],
  "destructive": [{"kind":"drop_column","table":"merchants","column":"category_locked","rows":3,"non_null":3}],
  "incompatible": ["merchants.category_locked would be dropped while the running release still declares it"]
}`))
	require.Contains(t, out.String(), "push blocked")
	require.Contains(t, out.String(), "merchants.category_locked would be dropped while the running release still declares it")
	require.Contains(t, out.String(), "--approve does not bypass")
	require.Equal(t, 1, strings.Count(out.String(), "drop column merchants.category_locked"))
	require.NotContains(t, out.String(), "⚠ drop merchants.category_locked")
}

func TestSchemaPlanDoesNotContradictTheServersApprovalDecision(t *testing.T) {
	var out strings.Builder
	renderSchemaPlan(&out, []byte(`{
  "in_sync": false,
  "changes": ["these remove something that holds no values, and ARE applied:", "drop column statements.raw_text (1 rows, 0 non-null)"],
  "destructive": [{"kind":"drop_column","table":"statements","column":"raw_text","rows":1,"non_null":0}]
}`))
	require.Contains(t, out.String(), "ARE applied")
	require.Equal(t, 1, strings.Count(out.String(), "statements.raw_text"))
	require.NotContains(t, out.String(), "--approve")
}

func TestSchemaPlanWithoutFormattedChangesStillDescribesDrops(t *testing.T) {
	for _, tc := range []struct {
		name, drop string
		approval   bool
	}{
		{"empty table", `{"table":"legacy","rows":0}`, false},
		{"populated table", `{"table":"legacy","rows":3}`, true},
		{"empty column in populated table", `{"table":"legacy","column":"note","rows":3,"non_null":0}`, false},
		{"populated column", `{"table":"legacy","column":"note","rows":3,"non_null":2}`, true},
		{"unknown column contents", `{"table":"legacy","column":"note","rows":3}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			renderSchemaPlan(&out, []byte(`{"in_sync":false,"destructive":[`+tc.drop+` ]}`))
			require.Contains(t, out.String(), "legacy")
			require.Equal(t, tc.approval, strings.Contains(out.String(), "--approve"))
		})
	}
}

func TestSchemaPlanInSyncDoesNotHideARefusal(t *testing.T) {
	var out strings.Builder
	renderSchemaPlan(&out, []byte(`{"in_sync":true,"incompatible":["the running release cannot serve this shape"]}`))
	require.Contains(t, out.String(), "the running release cannot serve this shape")
	require.NotContains(t, out.String(), "in sync")
}
