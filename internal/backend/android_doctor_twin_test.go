package backend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// WHICH DISKS HOLD ONE OF TWO NAMES (final review, Minor #5), measured apart
// from any disk: the pairs a Mac or Linux volume can hold are only the ones its
// own rules allow, and the Unicode-form pair no APFS volume can hold at all.
//
// A letter-case pair is one directory on macOS and on Windows. A pair only the
// full fold joins (ß and "ss") is one on macOS alone: Windows folds case one
// letter at a time. Two Unicode forms of one name are one directory on macOS.
func TestTwinProblemNamesTheDisksThatHoldOneOfTheTwo(t *testing.T) {
	for _, c := range []struct{ name, other, want string }{
		{"Main", "main", "differs only in case from palbase/environments/main — a disk that ignores case (macOS, Windows) " +
			"holds one of the two; rename one in the dashboard"},
		{"straße", "STRASSE", "differs only in case from palbase/environments/STRASSE — a disk that ignores case (macOS) " +
			"holds one of the two; rename one in the dashboard"},
		{"café", "café", "is palbase/environments/\"café\" in another Unicode form — a disk that ignores " +
			"Unicode form (macOS) holds one of the two; rename one in the dashboard"},
	} {
		require.Equal(t, c.want, twinProblem(c.name, c.other), c.name)
	}
}
