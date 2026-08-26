package backend

// devjs_flush_test.go — a script that exits before its output is flushed loses
// it, and the loss is silent.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// exitRightAfterWrite matches the shape that drops output: a write to stdout
// whose call does NOT carry a completion callback, followed by process.exit
// within a couple of lines.
// The argument list tolerates ONE level of nesting, because the call this was
// written for is `write(JSON.stringify(result))` — a pattern that stopped at the
// first `)` matched nothing and the pin passed over the very shape it names.
var exitRightAfterWrite = regexp.MustCompile(
	`process\.stdout\.write\((?:[^()]|\([^()]*\))*\)\s*;\s*(?://[^\n]*\n\s*)*process\.exit\(`)

// TestNoEmbeddedScriptExitsBeforeItsOutputIsFlushed.
//
// ‼️ `process.exit` KUYRUKTAKİ YAZIMI DÜŞÜRÜR. stdout bir BORU olduğunda yazım
// asenkrondur ve `exit` o anda çağrılırsa kuyrukta kalan gider.
//
// Ölçüldü 26.08.2026, bu CLI'ın gerçek çağrı şekliyle (`execFileSync`, stdio
// pipe): 128 KiB yazan bir çocuktan TAM 65 536 bayt okunuyor — node 26.7 ve bun
// 1.3.9, ikisinde de. Kesilen JSON `JSON.parse` ile "extractor produced no JSON"
// oluyor, yani BÜYÜK bir controller'ın metadata'sı eşiği aştığı an `palbase
// build` ana dalı reddediyor. Sebep controller'ın İÇERİĞİ değil BOYUTU, ve
// hiçbir mesaj bunu söylemiyor. Bir kullanıcı bunu üretimde buldu.
//
// The pin is on the SHAPE because the failure is invisible below the threshold:
// every test with a small payload passes, and the bug waits for a project big
// enough to trip it.
func TestNoEmbeddedScriptExitsBeforeItsOutputIsFlushed(t *testing.T) {
	entries, err := buildCheckFS.ReadDir("devjs")
	require.NoError(t, err)
	require.NotEmpty(t, entries, "no embedded scripts were scanned — the pin would pass vacuously")

	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		body, err := buildCheckFS.ReadFile(filepath.Join("devjs", e.Name()))
		require.NoError(t, err)
		scanned++
		if m := exitRightAfterWrite.FindString(string(body)); m != "" {
			t.Errorf("%s exits before stdout is flushed — output past the pipe buffer "+
				"(65536 bytes, measured) is dropped:\n%s\n\nPass a completion callback: "+
				"process.stdout.write(payload, () => process.exit(code)) — or set process.exitCode "+
				"and return.", e.Name(), m)
		}
	}
	require.GreaterOrEqual(t, scanned, 3, "fewer scripts scanned than this CLI embeds")
}

// TestTheFlushIdiomActuallyDeliversEverything proves the idiom the pin above
// demands, in the same shape this CLI uses to call these scripts — a pin on a
// rule nobody verified would just be a rule.
func TestTheFlushIdiomActuallyDeliversEverything(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	const size = 128 * 1024

	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		return p
	}
	dropping := write("dropping.js",
		"process.stdout.write('x'.repeat(128*1024));\nprocess.exit(0);\n")
	flushing := write("flushing.js",
		"process.stdout.write('x'.repeat(128*1024), () => process.exit(0));\n")

	out, err := exec.Command(node, dropping).Output()
	require.NoError(t, err)
	require.Less(t, len(out), size,
		"the dropping shape delivered everything — this platform cannot show the fault, "+
			"so the assertion below would prove nothing")

	out, err = exec.Command(node, flushing).Output()
	require.NoError(t, err)
	require.Equal(t, size, len(out), "the flush idiom still lost output")
}
