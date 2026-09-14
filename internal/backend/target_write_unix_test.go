//go:build unix

package backend

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// underAFileSizeLimit is set in the child binary that runs one case below.
const underAFileSizeLimit = "PALBASE_TEST_UNDER_A_FILE_SIZE_LIMIT"

// fileSizeLimit is smaller than either rewrite below and larger than the
// selection the address migration writes ahead of its file, so what fails is
// the committed file's write — never the selection's.
const fileSizeLimit = 512

// A REWRITE CUT SHORT LEAVES THE FILE AS IT WAS (FR-6).
//
// A read-only file refuses the write when it is opened. A full disk, a quota or
// an I/O error lets the open succeed and fails the write part way — and that is
// the case nothing measured: `os.WriteFile` had already truncated
// `project.json`, so the file was left cut, every later verb refused it as
// invalid JSON, and the migration that cut it said nothing. A file-size limit
// makes exactly that failure (the open succeeds, the write stops with EFBIG) on
// each path's rewrite: the address migration's identity, and the retired-field
// cleanup's file. The identity is too big for the limit and the cleanup of the
// same file is not, so a cleanup given a second go at the file after the
// address migration's write failed would show here too.
//
// Then the limit is lifted and the same migration runs again and rewrites the
// file: the limit was the only thing in its way, so "nothing changed" above is a
// measurement and not a migration that never ran.
func TestARewriteCutShortLeavesTheFileAsItWas(t *testing.T) {
	longName := strings.Repeat("n", 700)
	for _, tc := range []struct{ name, raw, product string }{
		{"the address migration's", `{"url":"https://mu0028.palbase.studio","stackVersion":"39"}`,
			strings.Repeat("m", 2000)},
		{"the retired-field cleanup's", `{"project":"prd_a","name":"` + longName + `","env":"staging"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Getenv(underAFileSizeLimit) == "" {
				runAloneUnderAFileSizeLimit(t)
				return
			}
			linkedByAnOlderCLI(t, tc.raw)
			resolverRig(t, twoEnvs)
			cloudAddresses(t, true)
			if tc.product != "" {
				product := tc.product
				ProductOfRef = func(context.Context, string) (Product, error) {
					return Product{ID: "prd_a", Name: product}, nil
				}
			}

			var out bytes.Buffer
			migrated := withFileSizeLimit(t, func() error { return MigrateLegacyTarget(context.Background(), &out) })
			require.NoError(t, migrated, "a rewrite cut short failed the verb")

			after, err := os.ReadFile(projectPath())
			require.NoError(t, err)
			require.Equal(t, tc.raw, string(after), "a rewrite cut short changed the committed file")
			require.Empty(t, out.String(), "a rewrite cut short announced itself")
			_, err = readLinkedProject()
			require.NoError(t, err, "a rewrite cut short left a file no verb can read")
			entries, err := os.ReadDir(RootDir())
			require.NoError(t, err)
			require.Len(t, entries, 1, "a rewrite cut short left its temporary file in the committed directory")

			require.NoError(t, MigrateLegacyTarget(context.Background(), &out))
			rewritten, err := os.ReadFile(projectPath())
			require.NoError(t, err)
			require.NotEqual(t, tc.raw, string(rewritten),
				"without the limit the migration does not rewrite this file either, so the case measured nothing")
			require.Contains(t, out.String(), projectPath(), "without the limit the migration did not say what it rewrote")
		})
	}
}

// runAloneUnderAFileSizeLimit runs the calling test again in a child of this
// binary, where the limit can be lowered without reaching anything else.
//
// RLIMIT_FSIZE belongs to the process, and a test binary writes more than its
// tests do: `go test` hands a cacheable run `-test.testlogfile`, which the
// testing package logs through a bufio.Writer. A flush that failed inside the
// window would stick, and be reported only at exit — "testing: can't write …",
// exit 2 — failing the package long after this test passed. The child is
// started without that flag and runs this one case and nothing else.
func runAloneUnderAFileSizeLimit(t *testing.T) {
	t.Helper()
	levels := strings.Split(t.Name(), "/")
	for i, level := range levels {
		levels[i] = "^" + regexp.QuoteMeta(level) + "$"
	}
	child := exec.Command(os.Args[0], "-test.run="+strings.Join(levels, "/"), "-test.v")
	child.Env = append(os.Environ(), underAFileSizeLimit+"=1")
	output, err := child.CombinedOutput()
	require.NoError(t, err, "the case failed in its child binary:\n%s", output)
	require.Contains(t, string(output), "--- PASS: "+t.Name(), "the child binary did not run the case:\n%s", output)
}

// withFileSizeLimit runs fn with this process's file-size limit lowered to
// fileSizeLimit, and puts the limit back before it returns, whatever fn did.
// The Go runtime catches SIGXFSZ and, with nobody notified, drops it, so a write
// past the limit returns EFBIG rather than ending the binary.
func withFileSizeLimit(t *testing.T, fn func() error) error {
	t.Helper()
	var prev syscall.Rlimit
	require.NoError(t, syscall.Getrlimit(syscall.RLIMIT_FSIZE, &prev))
	require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: fileSizeLimit, Max: prev.Max}))
	defer func() {
		require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &prev), "the file-size limit could not be put back")
	}()
	return fn()
}
