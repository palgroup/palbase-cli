// Package admin wires `palbase admin` over the v2 cloud's operator surface.
//
// Two verbs, because the v2 control plane has two operator fiats: roll the
// fleet onto a new stack image, and delete tenants the ledger does not know
// about. The v1 verbs this replaced (`migrate-all-tenants`, `schema-cutover`,
// `set-module-image`, `rotate-key`) belonged to a plane of many modules with
// per-module images; v2 runs ONE stack image per tenant, so there is nothing
// per-module left to point anywhere.
//
// BOTH ARE GATED SERVER-SIDE and fail closed: the control plane refuses anyone
// not on its operator list, and refuses EVERYONE when that list is empty. The
// confirmation prompts here are for the person, not the gate — a verb that
// replaces every pod in the fleet, or deletes a tenant's disk, should be hard
// to run by accident even when you are allowed to.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// REST is the control-plane transport subset these commands use.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers carries the lazily-built REST client.
type Resolvers struct {
	REST func() REST
}

// UpgradeAccepted is what the plane reports when a roll starts: a job id, and
// nothing else.
//
// It once carried Total and Skipped too, and printed them — but the plane never
// sends either, so they decoded to zero and the CLI told the operator
// "Rolling 0 tenant(s) (0 already there)" while a fleet of fourteen sat there.
// Numbers the server did not send are not a summary; they are a lie with a
// tabwriter.
type UpgradeAccepted struct {
	JobID string `json:"jobId"`
}

// SweepEntry is one tenant the sweeper considered.
type SweepEntry struct {
	Cell   string `json:"cell"`
	Ref    string `json:"ref"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// Cmd returns the `palbase admin` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Fleet operations (operator only)",
	}
	cmd.AddCommand(fleetCmd(r), sweepCmd(r))
	return cmd
}

func fleetCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{Use: "fleet", Short: "Operate the tenant fleet"}
	cmd.AddCommand(fleetUpgradeCmd(r))
	return cmd
}

func fleetUpgradeCmd(r Resolvers) *cobra.Command {
	var parallel int
	var yes bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "upgrade <image>",
		Args:  cobra.ExactArgs(1),
		Short: "Roll every tenant onto a new stack image",
		Long: `Roll the fleet onto a new stack image.

One tenant goes first and has to come back healthy before the rest follow; the
rest roll in batches. Each tenant's pod is replaced — its disk and its identity
stay — so a tenant is briefly unavailable while its own pod restarts.

The image must come from this fleet's own registry. The plane refuses any other,
which is the point: a fleet-wide roll is the widest blast radius there is.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			image := args[0]
			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(),
					"This replaces the pod of EVERY tenant in the fleet with:\n  %s\nType the image to confirm: ", image)
				var typed string
				if _, err := fmt.Fscanln(cmd.InOrStdin(), &typed); err != nil {
					return fmt.Errorf("aborted")
				}
				if typed != image {
					return fmt.Errorf("aborted — %q does not match %q", typed, image)
				}
			}
			var out UpgradeAccepted
			body := map[string]any{"image": image, "parallel": parallel}
			if err := r.REST().Do(cmd.Context(), http.MethodPost, "/v1/cloud/fleet/upgrade", body, &out); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), out)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Rolling the fleet onto %s\njob %s\n\nOne tenant goes first and has to come back healthy before the rest follow.\n",
				image, out.JobID)
			return nil
		},
	}
	cmd.Flags().IntVar(&parallel, "parallel", 4, "how many tenants roll at once after the canary")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func sweepCmd(r Resolvers) *cobra.Command {
	var yes bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "sweep",
		Args:  cobra.NoArgs,
		Short: "Delete tenants the ledger does not know about",
		Long: `Delete cell tenants that no project in the ledger claims.

These are leftovers: a create that failed after the cell had already been told,
or a delete that never finished. Sweeping removes the pod AND its disk, so it
re-checks the ledger for each one immediately before deleting — a project
created while the sweep was running must not be mistaken for a leftover.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Fprint(cmd.OutOrStdout(),
					"This deletes unclaimed tenants and their disks. Type 'sweep' to confirm: ")
				var typed string
				if _, err := fmt.Fscanln(cmd.InOrStdin(), &typed); err != nil {
					return fmt.Errorf("aborted")
				}
				if typed != "sweep" {
					return fmt.Errorf("aborted")
				}
			}
			var entries []SweepEntry
			if err := r.REST().Do(cmd.Context(), http.MethodPost, "/v1/cloud/sweep", nil, &entries); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing to sweep — every tenant in every cell is in the ledger.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "CELL\tREF\tACTION\tREASON")
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Cell, e.Ref, e.Action, e.Reason)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
