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

// INHERITED IS STILL SET (review-T013).
//
// A tsconfig that extends a base and writes no moduleResolution of its own
// compiles under the base's value — the `@tsconfig/node*` bases included — and
// reading only the top file answered "absent" for exactly the project this gate
// exists to stop. The refusal names the file the value came from, because that
// is the file somebody has to change (or override).
func TestModuleResolutionInheritedThroughExtendsIsRefused(t *testing.T) {
	cases := []struct {
		name, value, from string
		arrange           func(t *testing.T, dir string)
	}{
		{"relative", "node10", "tsconfig.base.json", func(t *testing.T, dir string) {
			mustWrite(t, dir, "tsconfig.base.json", `{"compilerOptions":{"moduleResolution":"node10"}}`)
			mustWrite(t, dir, "tsconfig.json", `{"extends":"./tsconfig.base.json","compilerOptions":{"strict":true}}`)
		}},
		{"relative, no .json, two levels deep", "Node10", "base.json", func(t *testing.T, dir string) {
			mustWrite(t, dir, "configs/base.json", `{"compilerOptions":{"moduleResolution":"Node10"}}`)
			mustWrite(t, dir, "configs/app.json", `{
  // the second hop is relative to THIS file, not to the project
  "extends": "./base.json"
}`)
			mustWrite(t, dir, "tsconfig.json", `{"extends":"./configs/app"}`)
		}},
		{"package file", "node", "node_modules/@tsconfig/node10/tsconfig.json", func(t *testing.T, dir string) {
			mustWrite(t, dir, "node_modules/@tsconfig/node10/tsconfig.json", `{"compilerOptions":{"moduleResolution":"node"}}`)
			mustWrite(t, dir, "tsconfig.json", `{"extends":"@tsconfig/node10/tsconfig.json"}`)
		}},
		{"package name", "classic", "node_modules/legacy-base/tsconfig.json", func(t *testing.T, dir string) {
			mustWrite(t, dir, "node_modules/legacy-base/tsconfig.json", `{"compilerOptions":{"moduleResolution":"classic"}}`)
			mustWrite(t, dir, "tsconfig.json", `{"extends":"legacy-base"}`)
		}},
		{"array, the last base wins", "node10", "b.json", func(t *testing.T, dir string) {
			mustWrite(t, dir, "a.json", `{"compilerOptions":{"moduleResolution":"bundler"}}`)
			mustWrite(t, dir, "b.json", `{"compilerOptions":{"moduleResolution":"node10"}}`)
			mustWrite(t, dir, "tsconfig.json", `{"extends":["./a.json","./b.json"]}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.arrange(t, dir)
			var out strings.Builder
			require.True(t, reportUnreachableModuleResolution(dir, &out), "an inherited %q reads no exports and was not refused", tc.value)
			require.Contains(t, out.String(), tc.value)
			require.Contains(t, out.String(), tc.from, "the refusal does not name the file the value comes from")
			require.Contains(t, out.String(), `"bundler"`)
		})
	}
}

func TestModuleResolutionThroughExtendsThatReadsExportsPasses(t *testing.T) {
	for name, arrange := range map[string]func(t *testing.T, dir string){
		"the child overrides its base": func(t *testing.T, dir string) {
			mustWrite(t, dir, "tsconfig.base.json", `{"compilerOptions":{"moduleResolution":"node10"}}`)
			mustWrite(t, dir, "tsconfig.json", `{"extends":"./tsconfig.base.json","compilerOptions":{"moduleResolution":"bundler"}}`)
		},
		"array, the last base wins": func(t *testing.T, dir string) {
			mustWrite(t, dir, "a.json", `{"compilerOptions":{"moduleResolution":"node10"}}`)
			mustWrite(t, dir, "b.json", `{"compilerOptions":{"moduleResolution":"bundler"}}`)
			mustWrite(t, dir, "tsconfig.json", `{"extends":["./a.json","./b.json"]}`)
		},
		"a cycle ends": func(t *testing.T, dir string) {
			mustWrite(t, dir, "a.json", `{"extends":"./tsconfig.json"}`)
			mustWrite(t, dir, "tsconfig.json", `{"extends":"./a.json"}`)
		},
		"a base that is not there is the compiler's to report": func(t *testing.T, dir string) {
			mustWrite(t, dir, "tsconfig.json", `{"extends":"./nope.json"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			arrange(t, dir)
			var out strings.Builder
			require.False(t, reportUnreachableModuleResolution(dir, &out), "%s was refused:\n%s", name, out.String())
			require.Empty(t, out.String())
		})
	}
}
