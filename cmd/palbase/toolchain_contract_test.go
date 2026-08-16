package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// secureGoToolchain is the toolchain this module requires, and the one anything
// that builds it must use. It moved from 1.26.5 to 1.26.6 when go.mod did; a
// constant that lags go.mod turns this contract into a test about a version
// nobody compiles with.
const secureGoToolchain = "1.26.6"

var immutableActionRef = regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*[^\s#]+@([0-9a-f]{40})(?:\s+#.*)?$`)
var anyActionRef = regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*[^\s#]+@[^\s#]+`)

// The CLI ships standalone binaries, so whatever builds them must compile with
// the same patched toolchain go.mod requires. A floating minor silently
// reintroduced GO-2026-5856 when setup-go selected Go 1.26.4.
func TestReleaseToolchainIsSecurityPinned(t *testing.T) {
	goMod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	require.Contains(t, string(goMod), "\ngo "+secureGoToolchain+"\n")
}

// And every workflow that exists agrees with it.
//
// Written over the workflows PRESENT rather than a list of names: this
// repository's pipelines were deleted on 2026-08-14 to be rewritten, and a test
// that reads two files by name became a test about files nobody has, failing for
// a reason that had nothing to do with what it guards. The invariant is about
// every workflow there is — which is the same claim when there are two, and
// still true when there are none.
func TestEveryWorkflowUsesThePinnedToolchain(t *testing.T) {
	workflows, err := filepath.Glob("../../.github/workflows/*.yml")
	require.NoError(t, err)

	for _, workflow := range workflows {
		raw, err := os.ReadFile(workflow)
		require.NoError(t, err)
		contents := string(raw)
		name := filepath.Base(workflow)

		if strings.Contains(contents, "go-version:") {
			require.Contains(t, contents, `go-version: "`+secureGoToolchain+`"`, name)
			require.Equal(t, 1, strings.Count(contents, "go-version:"), name)
		}
		require.Equal(t,
			len(anyActionRef.FindAllString(contents, -1)),
			len(immutableActionRef.FindAllString(contents, -1)),
			name+" must pin every action to a full commit")
		require.NotContains(t, contents, "version: latest", name)
	}
}
