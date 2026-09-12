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

// PrintTargetFor announces the target on stderr so stdout stays machine-readable.
//
// IT NAMES THE PROJECT, NOT THE ENVIRONMENT, and it cannot do better: it reads
// the committed record alone. Verbs that ACT on an environment use
// PrintResolvedFor, which asks the resolver.
func PrintTargetFor(cmd *cobra.Command) (Target, error) {
	return PrintTarget(cmd.ErrOrStderr())
}

// PrintTarget returns the same target it announces.
func PrintTarget(w io.Writer) (Target, error) {
	target, err := ReadTarget()
	if err != nil {
		return Target{}, err
	}
	fmt.Fprintf(w, "▸ %s\n", target.Describe())
	return target, nil
}

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
