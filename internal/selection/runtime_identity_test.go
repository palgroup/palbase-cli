package selection_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/palgroup/palbase-cli/internal/selection"
)

func TestCanonicalEnvironmentRefEnvelope(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want bool
	}{
		{name: "minimum", ref: "abcd", want: true},
		{name: "maximum", ref: "abcdefghijklmnopqrstuvwx", want: true},
		{name: "too short", ref: "abc"},
		{name: "too long", ref: "abcdefghijklmnopqrstuvwxy"},
		{name: "uppercase", ref: "ABCD"},
		{name: "underscore", ref: "bad_ref"},
		{name: "hyphen", ref: "bad-ref"},
		{name: "unicode", ref: "tést"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, selection.IsCanonicalEnvironmentRef(tc.ref))
		})
	}
}

func TestValidateRuntimeBinding(t *testing.T) {
	const random = "01234567890123456789"
	for _, ref := range []string{"abcd", "abcdefghijklmnopqrstuvwx"} {
		require.NoError(t, selection.ValidateRuntimeBinding(ref, ref, "pb_"+ref+"_c"+random))
	}

	tests := []struct {
		name     string
		expected string
		returned string
		key      string
	}{
		{name: "invalid expected ref", expected: "bad-ref", returned: "bad-ref", key: "pb_bad-ref_c" + random},
		{name: "invalid returned ref", expected: "app1prod", returned: "bad_ref", key: "pb_app1prod_c" + random},
		{name: "foreign returned ref", expected: "app1prod", returned: "app1stg", key: "pb_app1prod_c" + random},
		{name: "short key ref", expected: "app1prod", returned: "app1prod", key: "pb_abc_c" + random},
		{name: "long key ref", expected: "app1prod", returned: "app1prod", key: "pb_abcdefghijklmnopqrstuvwxy_c" + random},
		{name: "foreign key ref", expected: "app1prod", returned: "app1prod", key: "pb_app1stg_c" + random},
		{name: "service key", expected: "app1prod", returned: "app1prod", key: "pb_app1prod_s" + random},
		{name: "malformed key", expected: "app1prod", returned: "app1prod", key: "pb_app1prod_cshort"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := selection.ValidateRuntimeBinding(tc.expected, tc.returned, tc.key)
			require.Error(t, err)
			require.NotContains(t, err.Error(), random, "validation errors must never leak key material")
		})
	}
}
