package test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func run(t *testing.T, r Resolvers, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := Cmd(r)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// Use a real subprocess without depending on the repository's npm scripts.
func stubNpm(t *testing.T, exitCode string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("npm stub requires a POSIX shell")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "npm-environment")
	path := filepath.Join(dir, "npm")
	require.NoError(t, os.WriteFile(path, []byte(`#!/bin/sh
printf '%s\n' "$PALBASE_TEST_BASE_URL" "$PALBASE_TEST_API_KEY" "$PALBASE_TEST_IDENTITIES" "$PALBASE_TEST_CANDIDATE" > "$PALBASE_NPM_MARKER"
exit `+exitCode+"\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PALBASE_NPM_MARKER", marker)
	return marker
}

const identities = `{"identities":{"user1":{"id":"u1","email":"a@e.f","password":"p"}}}`

func liveResolvers(mint func(*cobra.Command, int) ([]byte, func(context.Context) error, error)) Resolvers {
	return Resolvers{
		Target: func(*cobra.Command) (Target, error) {
			return Target{URL: "http://127.0.0.1:63638", APIKey: "pb_project_x", Candidate: "candidate"}, nil
		},
		Mint: mint,
	}
}

func TestLiveLayerExportsIdentitiesAndCleansAfterSuccess(t *testing.T) {
	marker := stubNpm(t, "0")
	cleaned := false
	r := liveResolvers(func(_ *cobra.Command, count int) ([]byte, func(context.Context) error, error) {
		require.Equal(t, 3, count)
		return []byte(identities), func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "cleanup must be bounded")
			require.InDelta(t, 30, time.Until(deadline).Seconds(), 2)
			raw, err := os.ReadFile(marker)
			require.NoError(t, err, "cleanup must run after npm finishes")
			require.Equal(t, "http://127.0.0.1:63638\npb_project_x\n"+identities+"\ncandidate\n", string(raw))
			cleaned = true
			return nil
		}, nil
	})
	out, err := run(t, r, "--live", "--identities", "3")
	require.NoError(t, err)
	require.True(t, cleaned)
	require.Contains(t, out, "minted 1 identit")
	require.Contains(t, out, "removed this run's test identities")
}

func TestLiveFailureStillCleansAndPreservesBothErrors(t *testing.T) {
	stubNpm(t, "7")
	cleanupFailure := errors.New("user u1 remains: cleanup denied")
	for _, tc := range []struct {
		name       string
		cleanupErr error
	}{
		{"cleanup succeeds", nil}, {"cleanup fails", cleanupFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleaned := false
			r := liveResolvers(func(*cobra.Command, int) ([]byte, func(context.Context) error, error) {
				return []byte(identities), func(context.Context) error { cleaned = true; return tc.cleanupErr }, nil
			})
			_, err := run(t, r, "--live")
			require.True(t, cleaned)
			require.ErrorContains(t, err, "live tests failed")
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 7, exitErr.ExitCode())
			if tc.cleanupErr != nil {
				require.ErrorIs(t, err, tc.cleanupErr)
				require.ErrorContains(t, err, "clean up this run's test identities")
			}
		})
	}
}

func TestSuccessfulTestsFailTheCommandIfCleanupFails(t *testing.T) {
	stubNpm(t, "0")
	failure := errors.New("cleanup denied")
	r := liveResolvers(func(*cobra.Command, int) ([]byte, func(context.Context) error, error) {
		return []byte(identities), func(context.Context) error { return failure }, nil
	})
	out, err := run(t, r, "--live")
	require.ErrorIs(t, err, failure)
	require.NotContains(t, out, "removed this run's test identities")
}

func TestMalformedMintIsRejectedButItsKnownUsersAreCleaned(t *testing.T) {
	for _, raw := range []string{`{"users":[{"user_id":"u1"}]}`, `{"identities":`} {
		t.Run(raw, func(t *testing.T) {
			cleaned := false
			r := liveResolvers(func(*cobra.Command, int) ([]byte, func(context.Context) error, error) {
				return []byte(raw), func(context.Context) error { cleaned = true; return nil }, nil
			})
			_, err := run(t, r, "--live")
			require.ErrorContains(t, err, "identities")
			require.True(t, cleaned)
		})
	}
}

func TestCancelledRunCanStillCleanItsUsers(t *testing.T) {
	stubNpm(t, "0")
	cleaned := false
	r := liveResolvers(func(cmd *cobra.Command, _ int) ([]byte, func(context.Context) error, error) {
		ctx, cancel := context.WithCancel(cmd.Context())
		cancel()
		cmd.SetContext(ctx)
		return []byte(identities), func(ctx context.Context) error {
			require.NoError(t, ctx.Err())
			_, ok := ctx.Deadline()
			require.True(t, ok)
			cleaned = true
			return nil
		}, nil
	})
	_, err := run(t, r, "--live")
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, cleaned)
}

func TestMintFailureDoesNotTryDeletingUnknownUsers(t *testing.T) {
	cleaned := false
	r := liveResolvers(func(*cobra.Command, int) ([]byte, func(context.Context) error, error) {
		return nil, func(context.Context) error { cleaned = true; return nil }, errors.New("rate limited")
	})
	_, err := run(t, r, "--live")
	require.ErrorContains(t, err, "rate limited")
	require.False(t, cleaned)
}

func TestUnitRunNeverMintsOrDeletesIdentities(t *testing.T) {
	stubBun(t, passingSummary, "0")
	_, err := run(t, Resolvers{}, "--unit")
	require.NoError(t, err)
}

// stubBun stands in for the unit layer's runner with what `bun test` really
// prints: a banner on stdout and the summary on STDERR (bun 1.3.9, measured —
// a check that reads only stdout sees no summary on a green run).
func stubBun(t *testing.T, summary, exitCode string) {
	t.Helper()
	stubBunStreams(t, "", summary, exitCode)
}

// stubBunStreams stubs `bun` with what it prints on EACH stream: the unit
// layer's verdict depends on which one a summary-shaped line arrives on.
func stubBunStreams(t *testing.T, stdout, stderr, exitCode string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("bun stub requires a POSIX shell")
	}
	dir := t.TempDir()
	body := "#!/bin/sh\nprintf 'bun test v1.3.9 (stub)\\n'\n"
	if stdout != "" {
		body += "cat <<'STDOUT'\n" + stdout + "\nSTDOUT\n"
	}
	if stderr != "" {
		body += "cat >&2 <<'SUMMARY'\n" + stderr + "\nSUMMARY\n"
	}
	body += "exit " + exitCode + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bun"), []byte(body), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const passingSummary = " 3 pass\n 0 fail\n 5 expect() calls\nRan 3 tests across 1 file. [12.00ms]"

// A SUMMARY THE SUITE PRINTS IS NOT BUN'S (review-T004).
//
// Measured against a real bun 1.3.9: a test that `console.log`s
// "Ran 3 tests across 1 file." and then calls `process.exit(0)` leaves exactly
// that line on STDOUT, nothing on stderr — and the unit layer read it as a pass
// while two of the three tests never ran. Bun writes its summary to STDERR;
// stdout is the code under test's.
func TestUnitRunIgnoresASummaryTheSuitePrintedToStdout(t *testing.T) {
	stubBunStreams(t, "sample.test.ts:\nRan 3 tests across 1 file.", "", "0")
	out, err := run(t, Resolvers{}, "--unit")
	require.Error(t, err, "a summary printed by the suite on stdout passed the gate:\n%s", out)
	require.Contains(t, err.Error(), "printed no summary")
}

// THE LAST SUMMARY ON STDERR IS THE RUN'S. Bun ends a completed run with it; a
// summary-shaped line earlier in the stream was printed by the code under test
// (`console.error`) and must not decide how many tests ran.
func TestUnitRunReadsTheLastSummaryOnStderr(t *testing.T) {
	stubBunStreams(t, "", "Ran 5 tests across 2 files.\n 0 pass\n 0 fail\nRan 0 tests across 0 files. [2.00ms]", "0")
	out, err := run(t, Resolvers{}, "--unit")
	require.Error(t, err, "an earlier summary-shaped line outvoted bun's own:\n%s", out)
	require.Contains(t, err.Error(), "ran 0 tests")
}

// A RUN THAT RAN NOTHING IS NOT A PASS (FR-015).
//
// The guard used to live in the scaffold's own `scripts/test.sh`, which every
// project could edit away; it lives in the command now (D-7), so a project that
// has no tests — or whose runner found none — is told so instead of green.
func TestUnitRunRefusesARunThatDiscoveredNoTests(t *testing.T) {
	stubBun(t, " 0 pass\n 0 fail\nRan 0 tests across 0 files. [2.00ms]", "0")
	out, err := run(t, Resolvers{}, "--unit")
	require.Error(t, err, "a unit run that ran no test passed:\n%s", out)
	require.ErrorContains(t, err, "0 tests")
}

// AND BUN'S OWN "NO TEST FILES" IS THAT SAME REFUSAL, WITH ITS COUNT (FR-015).
//
// The stub above prints a summary and exits 0. Real bun does not: measured with
// bun 1.3.9 in a project with no test file, it writes
// `error: 0 test files matching **{.test,.spec,_test_,_spec_}.{js,ts,jsx,tsx} in --cwd=…`
// to stderr, prints no summary, and exits 1 — and the command answered
// `unit tests failed: bun test: exit status 1`, a refusal that did not say how
// many tests ran. A fixture that never modelled the runner's real answer never
// measured this path.
func TestUnitRunNamesTheCountWhenBunFindsNoTestFile(t *testing.T) {
	stubBunStreams(t, "", "bun test v1.3.9 (cf6cdbbb)\nerror: 0 test files matching **{.test,.spec,_test_,_spec_}.{js,ts,jsx,tsx} in --cwd=\"/p\"\n", "1")
	out, err := run(t, Resolvers{}, "--unit")
	require.Error(t, err, "a unit run with no test file passed:\n%s", out)
	require.ErrorContains(t, err, "ran 0 tests")
	require.NotContains(t, err.Error(), "exit status", "the refusal still reads as a crash rather than as what ran")
}

// AN EXIT 0 WITH NO SUMMARY IS NOT A PASS EITHER (FR-015).
//
// A test file that calls `process.exit(0)` half-way through ends the runner
// before it prints what ran — exit code 0, and nothing that says how many tests
// that was. The scaffold's retired guard caught exactly this with a sabotage
// file; the command has to.
func TestUnitRunRefusesAnExitZeroThatPrintedNoSummary(t *testing.T) {
	stubBun(t, "", "0")
	out, err := run(t, Resolvers{}, "--unit")
	require.Error(t, err, "a unit run that printed no summary passed:\n%s", out)
	require.ErrorContains(t, err, "summary")
}

// NEGATIVE CONTROL: a run that ran tests and passed is a pass, and the runner's
// own words reach the person.
func TestUnitRunPassesARunThatRanTests(t *testing.T) {
	stubBun(t, passingSummary, "0")
	out, err := run(t, Resolvers{}, "--unit")
	require.NoError(t, err)
	require.Contains(t, out, "Ran 3 tests")
}

func TestUnitRunThatFailsStillFails(t *testing.T) {
	stubBun(t, " 2 pass\n 1 fail\nRan 3 tests across 1 file. [9.00ms]", "1")
	_, err := run(t, Resolvers{}, "--unit")
	require.ErrorContains(t, err, "unit tests failed")
}

// THE SWEEP RUNS BEFORE ANY REFUSAL (FR-009).
//
// `palbase test` is one of the five verbs that collect what an older CLI left
// in the checkout, and it does so before it has a chance to refuse anything —
// a refused command that leaves the litter is the gap FR-009 closes. What the
// sweep would not delete (git tracks it) is named, never silently kept.
func TestTheCommandSweepsBeforeAnyRefusal(t *testing.T) {
	var swept []string
	r := Resolvers{Sweep: func(dir string) []string {
		swept = append(swept, dir)
		return []string{".palbase"}
	}}
	out, err := run(t, r, "--unit", "--live")
	require.Error(t, err, "conflicting flags were accepted")
	wd, wdErr := os.Getwd()
	require.NoError(t, wdErr)
	require.Equal(t, []string{wd}, swept, "the sweep did not run, once, on the checkout, before the refusal")
	require.Contains(t, out, ".palbase", "a kept path was not named")
}

func TestUnitAndLiveTogetherIsRefused(t *testing.T) {
	_, err := run(t, Resolvers{}, "--unit", "--live")
	require.Error(t, err)
}

func TestHelpDoesNotClaimDeployExpiry(t *testing.T) {
	require.False(t, strings.Contains(Cmd(Resolvers{}).Long, "expire with the deploy"))
}
