package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The CLI embeds its own copy of the analyzers the SDK's stager publishes.
//
// That is not duplication by accident: `go:embed` cannot reach outside the
// module, and the deploy stager loads the copy shipped inside
// `@palbase/backend`. Two copies of a rule that REFUSES BUILDS is a hazard the
// repo has already paid for elsewhere — a stale copy ships a rule that does not
// exist, and every local test stays green because each side tests itself.
//
// Nothing measured the two against each other until a change to `generics.js`
// had to be hand-copied into three places. This is that measurement.
//
// It compares the INTERSECTION rather than a hard-coded list: a file that later
// becomes shared is covered the day it appears in both directories, and a list
// that goes stale cannot report silence.
func TestDevJSMatchesTheSDKStager(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	here := filepath.Dir(thisFile)
	devjs := filepath.Join(here, "devjs")
	// internal/backend → sdk/cli → sdk → sdk/palbase-ts/backend/src/stager
	stager := filepath.Join(here, "..", "..", "..", "palbase-ts", "backend", "src", "stager")

	if _, err := os.Stat(stager); err != nil {
		t.Skipf("the SDK source is not beside this checkout: %v", err)
	}

	entries, err := os.ReadDir(devjs)
	if err != nil {
		t.Fatalf("devjs unreadable: %v", err)
	}

	compared := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".js" {
			continue
		}
		srcPath := filepath.Join(stager, name)
		src, err := os.ReadFile(srcPath)
		if err != nil {
			continue // CLI-only analyzer; the SDK does not publish it.
		}
		mine, err := os.ReadFile(filepath.Join(devjs, name))
		if err != nil {
			t.Fatalf("devjs/%s unreadable: %v", name, err)
		}
		compared++
		if !bytes.Equal(src, mine) {
			t.Errorf("devjs/%s has DRIFTED from src/stager/%s — the CLI would refuse "+
				"builds by a different rule than the deploy stager. Copy the SDK's file over.", name, name)
		}
	}

	// A walk whose filter is wrong finds nothing and calls it a pass. These files
	// are the reason the gate exists; if none was compared, the gate is blind.
	if compared == 0 {
		t.Fatal("compared ZERO files — devjs and src/stager share none, so this gate measured nothing")
	}
	for _, must := range []string{"generics.js", "return_types.js", "throw_analysis.js"} {
		if _, err := os.Stat(filepath.Join(devjs, must)); err != nil {
			t.Errorf("devjs/%s is gone — if it was retired, retire it from this list too: %v", must, err)
		}
	}
}
