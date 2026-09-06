package backend

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// PrintTargetFor announces the target on stderr so stdout stays machine-readable.
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
