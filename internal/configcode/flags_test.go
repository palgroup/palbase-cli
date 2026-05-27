package configcode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

// row is a small helper to build a systemFlagRow from JSON literals so
// table cases read close to the wire shape.
func row(key, valueType, value, variants, desc string) systemFlagRow {
	r := systemFlagRow{Key: key, ValueType: valueType, Description: desc}
	if value != "" {
		r.Value = json.RawMessage(value)
	}
	if variants != "" {
		r.Variants = json.RawMessage(variants)
	}
	return r
}

// TestSerializeFlags_Golden asserts exact TOML bytes for a representative
// flag set — this is the determinism gate. Any reordering or formatting
// drift (e.g. an upgrade to BurntSushi that changes map key sorting)
// breaks this test.
func TestSerializeFlags_Golden(t *testing.T) {
	rows := []systemFlagRow{
		// Intentionally NOT alphabetical on input — output must sort.
		row("new_checkout", "bool", "false", `[{"value":false,"weight":50},{"value":true,"weight":50}]`, "Checkout UX"),
		row("max_items", "number", "42", "", ""),
		row("welcome_msg", "string", `"hi"`, "null", ""),
	}

	got, err := serializeFlags(rows)
	require.NoError(t, err)

	const want = `# config/flags.toml — system flag definitions (config-as-code, Faz 1).
#
# READ-ONLY MIRROR of server state. ` + "`palbase pull`" + ` overwrites
# this file; edit it and run ` + "`palbase push`" + ` to apply. Editing here
# alone does not change the server.
#
# Each [flags.<key>] is a project-wide system flag default. User
# overrides (runtime, per-user) are NOT config — they live in the flags
# service and are not pulled here.

[flags]
  [flags.max_items]
    type = "number"
    default = 42.0
  [flags.new_checkout]
    type = "bool"
    default = false
    description = "Checkout UX"

    [[flags.new_checkout.variants]]
      value = false
      weight = 50

    [[flags.new_checkout.variants]]
      value = true
      weight = 50
  [flags.welcome_msg]
    type = "string"
    default = "hi"
`
	require.Equal(t, want, string(got))
}

// TestSerializeFlags_Deterministic runs the same input twice and asserts
// byte-identical output (independent of Go map iteration order).
func TestSerializeFlags_Deterministic(t *testing.T) {
	rows := []systemFlagRow{
		row("zeta", "bool", "true", "", ""),
		row("alpha", "string", `"a"`, "", ""),
		row("mid", "number", "1", "", ""),
	}
	a, err := serializeFlags(rows)
	require.NoError(t, err)
	b, err := serializeFlags(rows)
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
}

// TestSerializeFlags_ObjectValueDeterministic guards the riskiest case:
// an object-typed flag value. BurntSushi must sort the object's keys, so
// repeated encodes are stable.
func TestSerializeFlags_ObjectValueDeterministic(t *testing.T) {
	rows := []systemFlagRow{
		row("theme", "object", `{"zebra":1,"apple":2,"middle":{"y":1,"x":2}}`, "", ""),
	}
	a, err := serializeFlags(rows)
	require.NoError(t, err)
	b, err := serializeFlags(rows)
	require.NoError(t, err)
	require.Equal(t, string(a), string(b))
	// Object keys appear sorted.
	require.Less(t, strings.Index(string(a), "apple"), strings.Index(string(a), "zebra"))
	require.Less(t, strings.Index(string(a), "x ="), strings.Index(string(a), "y ="))
}

// TestSerializeFlags_TombstoneFiltered asserts soft-deleted flags are
// excluded from the pull (live state only).
func TestSerializeFlags_TombstoneFiltered(t *testing.T) {
	deleted := "2026-05-01T00:00:00Z"
	rows := []systemFlagRow{
		row("live_flag", "bool", "true", "", ""),
		{Key: "dead_flag", ValueType: "bool", Value: json.RawMessage("false"), DeletedAt: &deleted},
	}
	got, err := serializeFlags(rows)
	require.NoError(t, err)
	require.Contains(t, string(got), "live_flag")
	require.NotContains(t, string(got), "dead_flag")
}

// TestSerializeFlags_Empty asserts a project with no flags produces a
// valid header-only document (no bare [flags] table).
func TestSerializeFlags_Empty(t *testing.T) {
	got, err := serializeFlags(nil)
	require.NoError(t, err)
	require.Contains(t, string(got), "READ-ONLY MIRROR")
	require.NotContains(t, string(got), "[flags]")
}

// TestSerializeFlags_RoundTrip decodes the emitted TOML back and checks
// the values survive the trip — the format is meant to be edited by
// humans and (later) pushed.
func TestSerializeFlags_RoundTrip(t *testing.T) {
	rows := []systemFlagRow{
		row("new_checkout", "bool", "false", `[{"value":false,"weight":50},{"value":true,"weight":50}]`, "Checkout UX"),
		row("max_items", "number", "42", "", ""),
		row("greeting", "string", `"hello"`, "", ""),
	}
	got, err := serializeFlags(rows)
	require.NoError(t, err)

	var doc flagsDoc
	require.NoError(t, toml.Unmarshal(got, &doc))

	require.Len(t, doc.Flags, 3)

	checkout := doc.Flags["new_checkout"]
	require.Equal(t, "bool", checkout.Type)
	require.Equal(t, false, checkout.Default)
	require.Equal(t, "Checkout UX", checkout.Description)
	require.Len(t, checkout.Variants, 2)
	require.Equal(t, false, checkout.Variants[0].Value)
	require.Equal(t, 50, checkout.Variants[0].Weight)
	require.Equal(t, true, checkout.Variants[1].Value)

	maxItems := doc.Flags["max_items"]
	require.Equal(t, "number", maxItems.Type)
	require.EqualValues(t, 42, maxItems.Default)
	require.Empty(t, maxItems.Variants)

	require.Equal(t, "hello", doc.Flags["greeting"].Default)
}

// TestSerializeFlags_NoSecretLeak is a defensive guard: even though flags
// has no secrets, the serializer must never emit a value that looks like
// a raw credential when one is (incorrectly) present in a value. Here we
// just confirm the @secret/ convention helper is wired and the package
// exposes it for the modules that DO have secrets.
func TestSecretRef(t *testing.T) {
	require.Equal(t, "@secret/GOOGLE_OAUTH_SECRET", SecretRef("GOOGLE_OAUTH_SECRET"))
	require.True(t, strings.HasPrefix(SecretRef("X"), SecretRefPrefix))
}

// TestDecodeVariants covers null/empty/array handling.
func TestDecodeVariants(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"nil", "", 0},
		{"null", "null", 0},
		{"empty array", "[]", 0},
		{"two", `[{"value":1,"weight":50},{"value":2,"weight":50}]`, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			vs, err := decodeVariants(raw)
			require.NoError(t, err)
			require.Len(t, vs, tc.want)
		})
	}
}
