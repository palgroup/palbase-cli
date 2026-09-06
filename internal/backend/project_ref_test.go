package backend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalProjectRefEnvelope(t *testing.T) {
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
			require.Equal(t, tc.want, isCanonicalProjectRef(tc.ref))
		})
	}
}
