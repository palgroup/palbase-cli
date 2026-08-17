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
