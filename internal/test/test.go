// Package test provides `palbase test`: one command that runs BOTH test layers.
//
// WHY IT EXISTS. The scaffold tells authors to unit-test the service layer with
// node:test, and `@palbase/backend/test` drives HTTP against a running stack —
// two layers, correctly, but the SDK shipped no way to run them together. What
// a project had to write instead was measured on a customer run: a mint script,
// a `pretest:live` hook, and hand-assembled `PALBASE_TEST_*` variables copied
// between files. Every customer writing the same five lines is a command the
// product owed them.
package test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// Resolvers carries what the command needs from the CLI's composition root:
// where the stack is, and how to mint identities against it.
type Resolvers struct {
	// Target answers the stack this checkout acts on, and its api key.
	Target func(*cobra.Command) (Target, error)
	// Mint creates test identities and returns their payload and a cleanup
	// function bound to exactly those users on the stack that minted them.
	Mint func(cmd *cobra.Command, count int) ([]byte, func(context.Context) error, error)
	// Sweep deletes what an older CLI left in the checkout and returns what it
	// would not delete — a path git tracks — for this command to name (FR-009).
	// Nil sweeps nothing: a harness that builds the command without the
	// composition root.
	Sweep func(dir string) []string
}

// Target is the address a live test run points at.
type Target struct {
	URL       string
	APIKey    string
	Candidate string
}

// mintedIdentities is the ENVELOPE `test-user create --json` emits. What the
// tests receive is the map INSIDE it, and the difference is not cosmetic:
// `PALBASE_TEST_IDENTITIES` is a FLAT map keyed by fixture name. Both readers
// in the SDK take it that way — `testRun.minted(name)` asks the parsed object
// for that key, `api.signInAs(name)` indexes it — and the deploy, which is the
// other writer of this variable, already exports the flat shape.
//
// `json.RawMessage` rather than a typed identity: the inner object carries the
// access token, the id, the email and the password, and re-marshalling a
// struct that names only some of those would hand the tests an identity they
// cannot sign in with.
type mintedIdentities struct {
	Identities map[string]json.RawMessage `json:"identities"`
}

// Cmd returns `palbase test`.
func Cmd(r Resolvers) *cobra.Command {
	var (
		unitOnly bool
		liveOnly bool
		count    int
	)
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run this project's tests — unit and live — in one command",
		Long: `Run the project's tests.

  palbase test              both layers
  palbase test --unit       the service layer only (no stack needed)
  palbase test --live       the HTTP layer only

The unit layer runs ` + "`bun test`" + ` directly — the scaffold's test script and the
runner a deploy grades your suites with, whatever package.json's test script
says — over services and pure logic, against ` + "`fakeDatabase()`" + ` from
@palbase/backend/test. A run that ran no test, or that exited without bun
printing its summary, is refused rather than reported as a pass. The live layer needs a stack —
this command mints the identities, exports PALBASE_TEST_* and runs the same
` + "`npm test`" + ` with them in the environment, so a test that wants a real request
has one and a test that does not is unaffected.

Identities are minted per run. After the live tests finish, this command deletes
those identities and their data, including when tests fail. Cleanup failures
are reported; an interrupted process may leave users for test-user delete.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			out := cmd.OutOrStdout()
			// THE SWEEP FIRST (FR-009): what an older CLI left in the checkout goes
			// before this command refuses or runs anything. A path git tracks, and a
			// file no CLI wrote, is kept and named — each arrives as its sentence.
			if r.Sweep != nil {
				dir, wdErr := os.Getwd()
				if wdErr != nil {
					return fmt.Errorf("resolve the checkout directory: %w", wdErr)
				}
				for _, kept := range r.Sweep(dir) {
					fmt.Fprintf(cmd.ErrOrStderr(), "  kept %s\n", kept)
				}
			}
			if unitOnly && liveOnly {
				return fmt.Errorf("--unit and --live are the two halves; pass neither to run both")
			}

			if !liveOnly {
				fmt.Fprintln(out, "▸ unit")
				if err := runUnitTests(cmd, out); err != nil {
					return fmt.Errorf("unit tests failed: %w", err)
				}
			}
			if unitOnly {
				return nil
			}

			fmt.Fprintln(out, "▸ live")
			target, err := r.Target(cmd)
			if err != nil {
				return err
			}
			raw, cleanup, err := r.Mint(cmd, count)
			if err != nil {
				return fmt.Errorf("mint test identities: %w", err)
			}
			if cleanup == nil {
				return fmt.Errorf("the mint did not provide cleanup for this run's test identities")
			}
			defer func() {
				// A cancelled test must still get a bounded chance to remove its
				// own users. Preserve its original failure if cleanup fails too.
				ctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 30*time.Second)
				defer cancel()
				if err := cleanup(ctx); err != nil {
					runErr = errors.Join(runErr, fmt.Errorf("clean up this run's test identities: %w", err))
					return
				}
				fmt.Fprintln(out, "  removed this run's test identities and their data")
			}()
			var minted mintedIdentities
			if jErr := json.Unmarshal(raw, &minted); jErr != nil {
				return fmt.Errorf("the mint did not answer in the identities shape: %w", jErr)
			}
			// AN ABSENT KEY IS NOT AN EMPTY SET. A payload in some other shape
			// decodes cleanly into a nil map, and the run would then export an
			// environment with no identities in it and fail every signInAs with
			// "no test identity named …" — pointing the author at their own test
			// instead of at the mint that answered in the wrong shape.
			if minted.Identities == nil {
				return fmt.Errorf(
					"the mint answered without an `identities` object — this command reads the shape "+
						"`palbase test-user create --json` emits, and got: %s",
					firstBytes(raw, 200),
				)
			}
			fmt.Fprintf(out, "  minted %d identit(ies) for this run\n", len(minted.Identities))

			// THE ENVELOPE IS UNWRAPPED HERE. Passing `raw` through made the
			// count above true and the run useless: every `signInAs` and every
			// `testRun.minted(name)` looked for the fixture name and found one
			// key called "identities", so the signed-in half of the scaffold's
			// e2e test SKIPPED while the command reported a mint.
			exported, mErr := json.Marshal(minted.Identities)
			if mErr != nil {
				return fmt.Errorf("re-encode this run's test identities: %w", mErr)
			}

			env := []string{
				"PALBASE_TEST_BASE_URL=" + target.URL,
				"PALBASE_TEST_API_KEY=" + target.APIKey,
				"PALBASE_TEST_IDENTITIES=" + string(exported),
			}
			// A local stack serves one version, so there is no candidate to select
			// and the harness does not ask for one.
			if target.Candidate != "" {
				env = append(env, "PALBASE_TEST_CANDIDATE="+target.Candidate)
			}
			if err := runNpmTest(cmd, env, out); err != nil {
				return fmt.Errorf("live tests failed: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&unitOnly, "unit", false, "run only the service-layer tests")
	cmd.Flags().BoolVar(&liveOnly, "live", false, "run only the tests that call the stack")
	cmd.Flags().IntVar(&count, "identities", 2, "how many test identities to mint for the live layer")
	return cmd
}

// firstBytes keeps an unexpected payload short enough to read in an error.
func firstBytes(raw []byte, n int) string {
	s := strings.TrimSpace(string(raw))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ranSummary is the line `bun test` ends every completed run with.
var ranSummary = regexp.MustCompile(`(?m)^Ran (\d+) tests? across (\d+) files?\.`)

// runUnitTests runs `bun test` DIRECTLY — the scaffold's test script (FR-014)
// and the runner a deploy grades a candidate's suites with — and refuses a run
// that cannot say it tested anything (FR-015). It does not run package.json's
// `test` script: a project that points that script elsewhere is still graded by
// bun at deploy, and this layer measures what the deploy will.
//
// THE SUMMARY IS READ, NOT ONLY THE EXIT CODE. A project with no test file, and
// a test file that ends the process with `process.exit(0)` half-way through,
// both exit 0; only the summary tells them from a pass. The guard used to be a
// shell script in the scaffold, which any project could edit away, and it lives
// here now (D-7).
//
// ONLY BUN'S STREAM, AND ONLY ITS LAST SUMMARY (review-T004). Bun writes the
// summary to STDERR, as the last thing a completed run prints. Stdout belongs to
// the code under test: a suite that `console.log`ged a summary-shaped line and
// then called `process.exit(0)` left exactly that there — measured against bun
// 1.3.9, and read as a pass while two of its three tests never ran. A
// summary-shaped line EARLIER on stderr is the suite's `console.error`, so the
// last one is the run's. Out of reach stays a suite written to forge bun's final
// line on purpose: its author controls the tests themselves, and no reading of a
// stream they can write to outvotes that.
func runUnitTests(cmd *cobra.Command, out io.Writer) error {
	var stderr bytes.Buffer
	// TWO STREAMS, ONE DESTINATION. With Stdout and Stderr set to different
	// writers, os/exec copies each pipe on its own goroutine, and both reach
	// `out` — a writer that need not be safe for concurrent use. Measured under
	// -race: two goroutines in bytes.(*Buffer).Write, and bun's summary missing
	// from the output. One lock in front of `out` serialises them.
	shared := &lockedWriter{w: out}
	c := exec.CommandContext(cmd.Context(), "bun", "test")
	c.Env = os.Environ()
	c.Stdout = shared
	c.Stderr = io.MultiWriter(shared, &stderr)
	if err := c.Run(); err != nil {
		// A RUNNER THAT FOUND NO TEST FILE IS THE SAME REFUSAL, NOT A CRASH (D-28).
		// Bun 1.3.9 answers a project with no test file with
		// `error: 0 test files matching …` on stderr and exit 1 — before any
		// summary — so reading the summary alone reported `exit status 1` and never
		// said that nothing ran.
		if noTestFiles.Match(stderr.Bytes()) {
			return ranNothing("0")
		}
		return fmt.Errorf("bun test: %w", err)
	}
	var m [][]byte
	if all := ranSummary.FindAllSubmatch(stderr.Bytes(), -1); len(all) > 0 {
		m = all[len(all)-1]
	}
	if m == nil {
		return fmt.Errorf("bun test exited 0 but printed no summary, so it cannot say how many tests ran — " +
			"a test file that ends the process early looks exactly like this, and it is not a pass")
	}
	if n, err := strconv.Atoi(string(m[1])); err != nil || n == 0 {
		return ranNothing(string(m[1]))
	}
	return nil
}

// noTestFiles is bun's own answer when it discovers no test file at all.
var noTestFiles = regexp.MustCompile(`(?m)^error: 0 test files matching`)

// ranNothing is the one refusal for a unit run that tested nothing, however the
// runner said so (FR-015).
func ranNothing(count string) error {
	return fmt.Errorf("bun test ran %s tests — a run that tested nothing is not a pass; "+
		"put a *.test.ts beside the code it covers", count)
}

// lockedWriter serialises writes to one writer shared by two copy goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// runNpmTest runs the project's own `npm test`, with `extra` added to the
// environment. The project's script is the contract — this command supplies the
// environment, it does not decide how tests are invoked.
func runNpmTest(cmd *cobra.Command, extra []string, out io.Writer) error {
	c := exec.CommandContext(cmd.Context(), "npm", "test")
	c.Env = append(os.Environ(), extra...)
	c.Stdout = out
	c.Stderr = out
	if err := c.Run(); err != nil {
		return fmt.Errorf("npm test: %w", err)
	}
	return nil
}
