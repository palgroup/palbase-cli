// `palbase debug history` — what an app already did, days later.
//
// The question this answers, and the only one it is judged against: something
// broke in prod, you know the user, and you need the full request/response
// story around the failure from a terminal. `tail` and `attach` both need the
// problem to be happening right now; this one does not.
//
// Plan: docs/superpowers/specs/2026-07-28-phase6-session-history-plan.md
package debugconsole

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// historyResponse keeps records RAW so one decode serves both paths: --json
// emits the bytes verbatim (a developer's jq works across `attach` and
// `history` because it is the same envelope), and the rendered path unmarshals
// the same bytes through the same formatter.
type historyResponse struct {
	Records []json.RawMessage `json:"records"`
}

func historyCmd(r Resolvers) *cobra.Command {
	var (
		user       string
		session    string
		since      string
		errorsOnly bool
		asJSON     bool
	)

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show what an app did before it broke, days later",
		Long: "Reads back the records an app uploaded when something went wrong — the\n" +
			"requests it made, what went in, what came back, and the log lines around\n" +
			"them. Nothing is live here; this is what already happened.\n\n" +
			"Devices upload on a signal (an error, a crash, a user report), not on a\n" +
			"schedule, so a healthy session leaves nothing behind. That is why an empty\n" +
			"result usually means nothing went wrong — not that the records were lost.\n\n" +
			"  palbase debug history --user u_42 --since 24h\n" +
			"  palbase debug history --session 018f4c2a --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (user == "") == (session == "") {
				return errors.New(
					"pass exactly one of --user or --session.\n" +
						"--user is the end user's id as your app knows them (the same id the SDK\n" +
						"reports); --session is one app launch")
			}
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			ref := sel.EnvironmentRef()

			input := map[string]any{"ref": ref}
			if user != "" {
				input["user"] = user
			} else {
				input["session"] = session
			}
			if since != "" {
				input["since"] = since
			}

			var resp historyResponse
			if err := r.Studio().Query(cmd.Context(), "debug.history", input, &resp); err != nil {
				return fmt.Errorf("debug.history: %w", err)
			}

			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			if len(resp.Records) == 0 {
				_, _ = fmt.Fprintln(errOut, emptyHelp(since))
				return nil
			}
			warned := map[int]bool{}
			for _, raw := range resp.Records {
				printRecord(out, errOut, raw, errorsOnly, asJSON, true, warned)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&user, "user", "", "end user id, as your app identifies them")
	cmd.Flags().StringVar(&session, "session", "", "one app launch")
	cmd.Flags().StringVar(&since, "since", "", "look-back window, e.g. 15m, 24h, 7d")
	cmd.Flags().BoolVar(&errorsOnly, "errors", false, "only failed requests and error logs")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw records, one JSON object per line")
	return cmd
}

// emptyHelp exists because "no records" has several causes with different
// fixes, and a developer who cannot tell "nothing happened" from "we did not
// keep it" spends an hour chasing the wrong one.
func emptyHelp(since string) string {
	window := "the default window"
	if since != "" {
		window = "the last " + since
	}
	return "(no records in " + window + ")\n" +
		"Uploads happen on a signal — an error, a crash, a user report — so this\n" +
		"usually means nothing went wrong. If you expected records: check that the\n" +
		"project has history switched on, that the window reaches back far enough,\n" +
		"and that it is inside the retention period."
}
