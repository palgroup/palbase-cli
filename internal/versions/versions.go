// Package versions provides `palbase versions` — which app versions are
// actually out there, and how fast the newest one is spreading.
//
// WHY THIS EXISTS IN THE CLI AND NOT ONLY IN THE PANEL: an agent writing code
// against a Palbase backend has a terminal, not a browser. The same question a
// human answers by opening the panel — "did anyone actually take the update I
// shipped?" — has to be answerable from here, through the same management
// endpoint, or the two surfaces drift.
//
// COUNTS INSTALLATIONS, NOT USERS. An app version is a property of a device:
// the same person can run 2.0 on a phone and 1.9 on a tablet, and anonymous
// sessions carry no durable user identity to count instead. The output says
// "installations" everywhere for that reason.
package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

const (
	currentPath = "/v1/management/version-adoption/current"
	dailyPath   = "/v1/management/version-adoption/daily"
)

// REST reaches the linked stack's management surface.
type REST interface {
	Do(ctx context.Context, method, path string, body []byte) (int, []byte, error)
}

// Resolvers is what the host binary hands this package.
type Resolvers struct {
	REST func(cmd *cobra.Command) (REST, error)
}

type bucket struct {
	Platform      string `json:"platform"`
	AppVersion    string `json:"app_version"`
	Installations int    `json:"installations"`
}

type distribution struct {
	Buckets      []bucket `json:"buckets"`
	Unidentified int      `json:"unidentified"`
}

type dailyRow struct {
	Day           string `json:"day"`
	Platform      string `json:"platform"`
	AppVersion    string `json:"app_version"`
	Installations int    `json:"installations"`
}

// call performs one read and reports the stack's own refusal rather than a
// generic one.
func call(r Resolvers, cmd *cobra.Command, path string) ([]byte, error) {
	rest, err := r.REST(cmd)
	if err != nil {
		return nil, err
	}
	status, raw, err := rest.Do(cmd.Context(), http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotImplemented {
		return nil, fmt.Errorf("this stack runs no module that counts installations")
	}
	if status >= 400 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Description != "" {
			return nil, fmt.Errorf("%s: %s", e.Error, e.Description)
		}
		return nil, fmt.Errorf("the stack answered %d: %s", status, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// Cmd builds `palbase versions`.
func Cmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	var days int

	cmd := &cobra.Command{
		Use:   "versions",
		Short: "Which app versions are out there, and how fast the newest is spreading",
		Long: `Show how many INSTALLATIONS are on each app version, and the adoption curve.

  palbase versions              Current distribution (last 24 hours).
  palbase versions --days 30    Also print the daily curve.
  palbase versions --json       Raw JSON, for scripts and agents.

Installations, not users: an app version is a property of a device, and the same
person may run two versions on two devices. These numbers will not match App
Store or Play — a store counts downloads, this counts installations that
actually reached this stack.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := call(r, cmd, currentPath)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if jsonOut && days == 0 {
				fmt.Fprintln(out, strings.TrimSpace(string(raw)))
				return nil
			}

			var dist distribution
			if err := json.Unmarshal(raw, &dist); err != nil {
				return fmt.Errorf("the stack answered something this CLI cannot read: %w", err)
			}

			if len(dist.Buckets) == 0 {
				fmt.Fprintln(out, "No installation has reported a version yet.")
				fmt.Fprintln(out, "An SDK sends this on every request, so it fills in as soon as an app talks to the stack.")
			} else {
				total := 0
				for _, b := range dist.Buckets {
					total += b.Installations
				}
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "PLATFORM\tVERSION\tINSTALLATIONS\tSHARE")
				for _, b := range dist.Buckets {
					share := "—"
					if total > 0 {
						share = fmt.Sprintf("%d%%", b.Installations*100/total)
					}
					fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", b.Platform, b.AppVersion, b.Installations, share)
				}
				_ = tw.Flush()
			}

			// Reported, never folded into a version: an incomplete number you
			// can see beats a wrong one you cannot.
			if dist.Unidentified > 0 {
				fmt.Fprintf(out, "\n%d request(s) came from an SDK too old to send an installation id.\n", dist.Unidentified)
			}

			if days == 0 {
				return nil
			}

			curveRaw, err := call(r, cmd, fmt.Sprintf("%s?days=%d", dailyPath, days))
			if err != nil {
				return err
			}
			if jsonOut {
				fmt.Fprintln(out, strings.TrimSpace(string(curveRaw)))
				return nil
			}
			var curve []dailyRow
			if err := json.Unmarshal(curveRaw, &curve); err != nil {
				return fmt.Errorf("the stack answered something this CLI cannot read: %w", err)
			}
			if len(curve) == 0 {
				fmt.Fprintln(out, "\nNo day has been rolled up yet.")
				return nil
			}
			fmt.Fprintf(out, "\nADOPTION, LAST %d DAYS\n", days)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "DAY\tPLATFORM\tVERSION\tINSTALLATIONS")
			for _, d := range curve {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", d.Day, d.Platform, d.AppVersion, d.Installations)
			}
			_ = tw.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print raw JSON")
	cmd.Flags().IntVar(&days, "days", 0, "Also print the adoption curve for this many days (max 90)")
	return cmd
}
