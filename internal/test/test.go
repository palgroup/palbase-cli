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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// Resolvers carries what the command needs from the CLI's composition root:
// where the stack is, and how to mint identities against it.
type Resolvers struct {
	// Target answers the stack this checkout acts on, and its api key.
	Target func(*cobra.Command) (Target, error)
	// Mint creates test identities and returns the `--json` payload verbatim.
	Mint func(cmd *cobra.Command, count int) ([]byte, error)
}

// Target is the address a live test run points at.
type Target struct {
	URL       string
	APIKey    string
	Candidate string
}

// mintedIdentities is the shape `test-user create --json` emits — the same one
// `createTestApi({ identities })` accepts. Decoded here only to count them, so
// the run can say what it prepared.
type mintedIdentities struct {
	Identities map[string]struct {
		Email string `json:"email"`
	} `json:"identities"`
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

The unit layer is your own ` + "`npm test`" + `: services and pure logic, against
` + "`fakeDatabase()`" + ` from @palbase/backend/test. The live layer needs a stack —
this command mints the identities, exports PALBASE_TEST_* and runs the same
` + "`npm test`" + ` with them in the environment, so a test that wants a real request
has one and a test that does not is unaffected.

Identities are minted per run. They expire with the deploy that made them, which
is why a saved file of them stops working and this command does not use one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if unitOnly && liveOnly {
				return fmt.Errorf("--unit and --live are the two halves; pass neither to run both")
			}

			if !liveOnly {
				fmt.Fprintln(out, "▸ unit")
				if err := runNpmTest(cmd, nil, out); err != nil {
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
			raw, err := r.Mint(cmd, count)
			if err != nil {
				return fmt.Errorf("mint test identities: %w", err)
			}
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

			env := []string{
				"PALBASE_TEST_BASE_URL=" + target.URL,
				"PALBASE_TEST_API_KEY=" + target.APIKey,
				"PALBASE_TEST_IDENTITIES=" + string(raw),
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

// runNpmTest runs the project's own `npm test`, with `extra` added to the
// environment. The project's script is the contract — this command supplies the
// environment, it does not decide how tests are invoked.
func runNpmTest(cmd *cobra.Command, extra []string, out io.Writer) error {
	c := exec.CommandContext(context.Background(), "npm", "test")
	c.Env = append(os.Environ(), extra...)
	c.Stdout = out
	c.Stderr = out
	if err := c.Run(); err != nil {
		return fmt.Errorf("npm test: %w", err)
	}
	return nil
}
