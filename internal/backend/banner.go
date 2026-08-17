package backend

// banner.go — every verb that leaves this machine says where it is going.
//
// The failure this prevents is not confusion, it is a correct command run
// against the wrong project: the same `palbase push` is right at 14:00 and
// catastrophic at 14:05 because something else moved the target. Nothing else in
// the output tells you — the diff looks the same, the success line looks the
// same. So the first line of every remote verb is the destination, printed
// before the work rather than after it, while stopping still costs nothing.

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// PrintTargetFor is the cobra-facing form: the banner goes to STDERR.
//
// It is commentary, not output. `palbase status --json | jq` broke the moment a
// human line was written to the same stream as the document — and the fix is not
// to skip the banner in JSON mode, because the run that most needs to say where
// it went is the scripted one.
func PrintTargetFor(cmd *cobra.Command) (Target, error) {
	return PrintTarget(cmd.ErrOrStderr())
}

// PrintTarget resolves where this checkout acts and announces it.
//
// Returns the resolved target so the caller does not read it twice: two reads
// could disagree — `palbase stop` in another pane removes the local file between
// them — and the banner would then name a place the command did not use.
func PrintTarget(w io.Writer) (Target, error) {
	target, err := ReadTarget()
	if err != nil {
		return Target{}, err
	}
	fmt.Fprintf(w, "▸ %s\n", target.Describe())
	return target, nil
}

// refuseCloudSelectionFlags rejects --project/--environment in a checkout that is
// bound to a project.
//
// They resolve a CLOUD project and environment. A checkout with
// .palbase/project.json already answers that question, so accepting them here
// means accepting an instruction and doing something else — and the banner,
// which exists so nobody pushes to the wrong place, would print the place the
// flags did NOT ask for and look like confirmation.
func refuseCloudSelectionFlags(cmd *cobra.Command, target Target) error {
	var named []string
	for _, flag := range []string{"project", "environment"} {
		if f := cmd.Flags().Lookup(flag); f != nil && f.Changed {
			named = append(named, "--"+flag)
		}
		if f := cmd.Root().PersistentFlags().Lookup(flag); f != nil && f.Changed {
			named = append(named, "--"+flag)
		}
	}
	if len(named) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s select a cloud environment, and this checkout is linked to %s.\n"+
			"  palbase env <slug>   switch which environment this checkout acts on\n"+
			"  palbase link <…>     bind it somewhere else",
		strings.Join(dedupe(named), " and "), target.Describe())
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	out := items[:0]
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}
