package backend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE REFUSAL'S LIST IS A MENU, so its columns line up.
//
// "The refusal lists the environments" was already measured; whether a person
// can READ it was not. Two rows with names of different lengths printed as
//
//	main   m5rafc8rm
//	staging   4zyxibwwm
//
// which reads as a formatting accident rather than a choice — and this list is
// the single most-read output of the whole change, because it appears exactly
// when a verb stops and asks.
func TestTheRefusalListingLinesUpItsRefs(t *testing.T) {
	got := listing([]Environment{
		{Name: "main", Ref: "m5rafc8rm"},
		{Name: "staging", Ref: "4zyxibwwm"},
		{Name: "a", Ref: "aaaaaaaaa"},
	})

	columns := map[int]bool{}
	for _, line := range strings.Split(got, "\n") {
		fields := strings.Fields(line)
		require.Len(t, fields, 2, "a row is not `name ref`: %q", line)
		columns[strings.Index(line, fields[1])] = true
	}
	require.Len(t, columns, 1,
		"the refs start in %d different columns, so the list reads as ragged text:\n%s",
		len(columns), got)

	// AND THE NAME IS STILL THE FIRST THING ON THE LINE: padding must not
	// become indentation that hides which column is the one to type.
	for _, line := range strings.Split(got, "\n") {
		require.True(t, strings.HasPrefix(line, "  "), "row lost its indent: %q", line)
		require.False(t, strings.HasPrefix(line, "   "), "row is over-indented: %q", line)
	}
}
