package backend

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModuleResolutionThatCannotReadExportsIsRefused(t *testing.T) {
	for _, value := range []string{"node10", "node", "classic", "Node"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, dir, "tsconfig.json", `{
  // JSONC, as tsconfigs are written
  "compilerOptions": { "moduleResolution": "`+value+`", "experimentalDecorators": true }
}`)
			var out strings.Builder
			require.True(t, reportUnreachableModuleResolution(dir, &out), "%q reads no exports and was not refused", value)
			require.Contains(t, out.String(), `"bundler"`, "the refusal does not name the value that works")
			require.Contains(t, out.String(), value)
		})
	}
}

func TestModuleResolutionThatReadsExportsPasses(t *testing.T) {
	for name, tsconfig := range map[string]string{
		"bundler":  `{"compilerOptions":{"moduleResolution":"bundler"}}`,
		"node16":   `{"compilerOptions":{"moduleResolution":"node16"}}`,
		"nodenext": `{"compilerOptions":{"moduleResolution":"NodeNext"}}`,
		"absent":   `{"compilerOptions":{"module":"esnext"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, dir, "tsconfig.json", tsconfig)
			var out strings.Builder
			require.False(t, reportUnreachableModuleResolution(dir, &out), "%s reads exports and was refused:\n%s", name, out.String())
			require.Empty(t, out.String())
		})
	}
	// No tsconfig at all: nothing to reason about.
	require.False(t, reportUnreachableModuleResolution(t.TempDir(), &strings.Builder{}))
}
