package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const secureGoToolchain = "1.26.5"

// The CLI ships standalone binaries, so CI and the release job must compile
// them with the same patched toolchain required by go.mod. A floating minor
// silently reintroduced GO-2026-5856 when setup-go selected Go 1.26.4.
func TestReleaseToolchainIsSecurityPinned(t *testing.T) {
	goMod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	require.Contains(t, string(goMod), "\ngo "+secureGoToolchain+"\n")

	for _, workflow := range []string{"ci.yml", "release.yml"} {
		raw, err := os.ReadFile("../../.github/workflows/" + workflow)
		require.NoError(t, err)
		require.Contains(t, string(raw), `go-version: "`+secureGoToolchain+`"`)
		require.Equal(t, 1, strings.Count(string(raw), "go-version:"), workflow)
	}
}
