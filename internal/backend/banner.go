package backend

// banner.go — WHAT A VERB SAYS BEFORE IT ACTS.
//
// The line exists so that "this went somewhere I did not mean" is visible at
// the moment it happens rather than afterwards. That makes it the one place
// where being vague costs the most: `todoapp` alone does not distinguish
// staging from production.

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// THERE IS NO PROJECT-ONLY BANNER ANY MORE, and its absence is the fix.
//
// `PrintTargetFor` lived here and read the committed record alone. It was the
// honest banner for a mechanism that pinned a checkout to one address, and
// TEN verbs still called it after the mechanism changed: `plan`, `spec`,
// `secret`, six `test-user` verbs, and `openStackManagement` — which is the
// shared client every management verb is built on. Each of them acts INSIDE one
// tenant, so each of them both ignored `--env` and, in a checkout bound to a
// product, refused outright with "palbase/project.json has no address". The
// suite was green: nothing measured the INDIRECT path, only direct
// `ReadTarget` callers (single_resolver_test.go).
//
// Measured by running the product: `palbase plan` in a freshly linked checkout.
//
// Deleting it is what keeps it fixed. A project-only banner sitting in this
// file is an invitation for the next verb to reach for the wrong one, and a
// gate listing it as an exception would have to be trusted forever.
// `doctor` still prints the committed record — it asks BOTH questions on
// purpose — and it calls `ReadTarget` directly, where the scope gate can see it.

// PrintResolvedFor announces where this verb is about to act — project AND
// environment — and returns it.
//
// THE ENVIRONMENT HALF OF THIS LINE WAS TESTED AND UNREACHABLE FOR MONTHS.
// `Target.Describe` has printed `<project>/<env>` since August and no
// production code ever wrote `Target.Env`, so the suite agreed with itself
// while every real banner showed a bare address. Tested dead code does not look
// dead — the fix is that the string now comes from the only thing that knows
// the answer.
//
// IT ALSO MIGRATES. An old checkout carries a resolved address rather than an
// identity, and this is the first thing every verb calls, so it is where the
// rewrite belongs. Best effort: a migration that cannot resolve leaves the
// checkout exactly as it was (MigrateLegacyTarget).
func PrintResolvedFor(cmd *cobra.Command) (Resolved, error) {
	return PrintResolvedTo(cmd.ErrOrStderr(), cmd)
}

// PrintResolvedTo is PrintResolvedFor with the writer named, for tests and for
// verbs that already hold one.
func PrintResolvedTo(w io.Writer, cmd *cobra.Command) (Resolved, error) {
	ctx := context.Background()
	if cmd != nil && cmd.Context() != nil {
		ctx = cmd.Context()
	}
	// The migration's own output goes to the same place, because a file that
	// changed under somebody deserves the same visibility as the target itself.
	if err := MigrateLegacyTarget(ctx, w); err != nil {
		return Resolved{}, err
	}
	resolved, err := ResolveFor(cmd)
	if err != nil {
		return Resolved{}, err
	}
	fmt.Fprintf(w, "▸ %s\n", resolved.Describe())
	return resolved, nil
}
