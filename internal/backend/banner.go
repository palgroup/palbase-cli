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
	"context"
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
	if err := RefuseSelectionFlagsWhenLinked(cmd); err != nil {
		return Target{}, err
	}
	return PrintTarget(cmd.Context(), cmd.ErrOrStderr())
}

// ResolveTargetFor is the same resolution for a verb that prints its own banner.
//
// `deploys`, `rollback`, `status` and `debug attach` format the destination into
// a longer line of their own, so they call ResolveTarget directly — and that is
// how they came to accept --project/--environment and drop them. The refusal
// belongs to the RESOLUTION, not to the printing, or the next verb that formats
// its own line inherits the bug.
func ResolveTargetFor(cmd *cobra.Command) (Target, error) {
	if err := RefuseSelectionFlagsWhenLinked(cmd); err != nil {
		return Target{}, err
	}
	return ResolveTarget(cmd.Context())
}

// RefuseSelectionFlagsWhenLinked is the gate the whole family shares.
//
// It asks ReadTarget, not ResolveTarget, and the difference is the point: the
// flags are only wrong when the LINK FILE is what answered. In a checkout with
// no link they are the only way to name a project, and refusing them there would
// trade a silent ignore for a refusal of the one thing that works.
//
// An unreadable link file is not a refusal — the verb below reports it, with the
// wording that fixes it. Two messages about the same missing file is one too
// many.
//
// EXPORTED because one stack verb does not come through PrintTargetFor:
// `palbase logs` reads the link itself and, for a cloud project, fetches THAT
// environment's lines from the control plane. A verb that resolves its own
// target has to ask for the gate, or it inherits exactly the bug this closes.
func RefuseSelectionFlagsWhenLinked(cmd *cobra.Command) error {
	linked, err := ReadTarget()
	if err != nil {
		return nil
	}
	return refuseCloudSelectionFlags(cmd, linked)
}

// PrintTarget resolves where this checkout acts and announces it.
//
// Returns the resolved target so the caller does not read it twice: two reads
// could disagree — `palbase stop` in another pane removes the local file between
// them — and the banner would then name a place the command did not use.
//
// IT RESOLVES THE SAME WAY THE VERB DOES. It used to call ReadTarget, which
// knows only the LINK; the verbs around it moved to ResolveTarget, which also
// knows the SELECTION. So in a checkout with a selection and no link the verb
// resolved fine and the banner did not — and because the banner runs first and
// returns its error, the whole command failed with "this checkout is not linked
// to a project" about a project it had just resolved. Measured 2026-08-24:
// `palbase test-user list` refused in centauri-ios while `palbase status`, one
// line different, printed that project's live deployment.
func PrintTarget(ctx context.Context, w io.Writer) (Target, error) {
	target, err := ResolveTarget(ctx)
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
	// BOTH LINES NAME A COMMAND THIS BINARY ANSWERS. The first offered
	// `palbase env <slug>`, which was retired at the v2 cutover — so the refusal
	// that exists to stop somebody acting on the wrong project handed them a
	// command that fails, at the moment they were already looking for one that
	// works. A refusal is only as good as its way out.
	return fmt.Errorf(
		"%s select a cloud environment, and this checkout is linked to %s.\n"+
			"  palbase link <ref>   point this checkout at another project — these verbs read the link\n"+
			"  palbase status       show which one it is pointed at now",
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

// unlinkedOrCloudError turns "no project selected" into the refusal FR-008 asks
// for.
//
// The resolver's message used to advise a command that DOES NOT EXIST. Verified
// 2026-08-24 against the built binary: `palbase project` carries create, delete,
// list and status — there is no `use`. Fourteen places across this CLI sent
// people to it, and the one line offered to somebody with a cloud project was
// the only line they could not follow. The four below are the four that work.
//
// AND THE FOURTH USED TO BE A FLAG. `--environment <ref>   act on one without
// linking` was the last line here, which is the line a stuck reader types — and
// it selects nothing on its own: the resolver wants a project id from
// `.palbase/selection.json` before the environment flag matters, and this error
// is only reached when there is no such file. Same correction as
// readLinkedProject's, for the same reason, and `palbase link <ref>` is what
// replaces it there too.
func unlinkedOrCloudError(cause error) error {
	return fmt.Errorf(
		"this checkout is not linked to a project, and no cloud project is selected either.\n"+
			"  palbase link <project>   a project in the cloud\n"+
			"  palbase link <ref>       one environment of it, by ref\n"+
			"  palbase link <url>       a project running on this machine\n"+
			"  palbase start            bring one up here and link to it (%v)",
		cause)
}
