package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
	"github.com/palgroup/palbase-cli/internal/logs"
)

// PanelREST is the plane's panel transport — the one `palbase logs` reads with.
type PanelREST interface {
	Do(ctx context.Context, method, path string, body any, out any) error
}

// delivery is one message as the plane reports it. There is no recipient: the
// plane's ledger does not keep the project's end users, and the id, the
// template and the provider's answer are what "what happened to this mail"
// needs.
type delivery struct {
	MessageID  string  `json:"message_id"`
	Channel    string  `json:"channel"`
	Template   *string `json:"template"`
	SentAt     string  `json:"sent_at"`
	Status     *string `json:"status"`
	Detail     *string `json:"detail"`
	ReportedAt *string `json:"reported_at"`
}

type deliveriesResponse struct {
	Deliveries    []delivery `json:"deliveries"`
	WindowSeconds int        `json:"window_seconds"`
}

// deliveriesPath asks for one environment's window; a message id narrows it to
// that message ON THE PLANE — filtering here would only search the newest
// `limit` rows and call an older message missing.
func deliveriesPath(ref string, windowSec, limit int, messageID string) string {
	q := url.Values{}
	q.Set("window_seconds", strconv.Itoa(windowSec))
	q.Set("limit", strconv.Itoa(limit))
	if messageID != "" {
		q.Set("message_id", messageID)
	}
	return "/v1/panel/environments/" + url.PathEscape(ref) + "/messages/deliveries?" + q.Encode()
}

// deliveriesCmd reads what the mail provider said about each message this
// environment sent on the platform rail (palgroup/palbase#25).
//
// Before it, a project whose auth mail went missing had nothing to look at: the
// API had answered 200, the logs said nothing, and "never sent" could not be
// told from "sent and lost after the recipient's server took it". The provider
// reports every message; this is where that report is read.
func deliveriesCmd(r Resolvers) *cobra.Command {
	var (
		since     string
		limit     int
		messageID string
		jsonOut   bool
	)
	c := &cobra.Command{
		Use:   "deliveries",
		Short: "Show what the mail provider reported for each message this environment sent",
		Long: `Each message this environment sent on the platform mail rail, newest first,
with the provider's last word about it:

  Delivered     the recipient's mail server accepted it — if it is not in the
                inbox, look in that mailbox's spam/quarantine, not here
  Bounced       the recipient's server refused it; DETAIL carries its SMTP answer
  Suppressed    the provider did not send it (an address that bounced before)
  Quarantined / FilteredSpam   the recipient's system took it as spam
  (pending)     sent; the provider has not reported yet (usually seconds)
  Untracked     sent, but under no id a report can be matched to

  palbase notifications deliveries                        the last 24h
  palbase notifications deliveries --since 2h
  palbase notifications deliveries --message-id <id>      one message (send-test prints the id)

Only a cloud environment sends on the platform rail. A project that configured
its own provider reads delivery in that provider's console.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := backend.ResolveFor(cmd)
			target := resolved.Acting()
			if err != nil {
				return err
			}
			ref, ok := "", false
			if r.CloudRef != nil {
				ref, ok = r.CloudRef(target.URL)
			}
			if !ok || r.Panel == nil {
				return fmt.Errorf("%s is not a cloud environment, and delivery reports come from the "+
					"platform mail rail, which only cloud environments send through. A stack with its own "+
					"mail provider reports delivery in that provider's console", target.Describe())
			}
			windowSec, err := logs.ParseWindow(since)
			if err != nil {
				return err
			}
			if since == "" {
				windowSec = 24 * 3600
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "▸ %s\n", resolved.Describe())

			var resp deliveriesResponse
			if err := r.Panel().Do(cmd.Context(), http.MethodGet, deliveriesPath(ref, windowSec, limit, messageID), nil, &resp); err != nil {
				return err
			}
			rows := resp.Deliveries
			if messageID != "" && len(rows) == 0 {
				return fmt.Errorf("no message %s in the last %s — widen --since, or check the id", messageID, humanWindow(windowSec))
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintf(out, "(no mail sent on the platform rail in the last %s)\n", humanWindow(windowSec))
				return nil
			}
			printDeliveries(out, rows)
			return nil
		},
	}
	c.Flags().StringVar(&since, "since", "", "look-back window, e.g. 30m, 2h, 3d (default 24h)")
	c.Flags().IntVar(&limit, "limit", 50, "max messages (1-500)")
	c.Flags().StringVar(&messageID, "message-id", "", "show only this message")
	c.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return c
}

func printDeliveries(w interface{ Write([]byte) (int, error) }, rows []delivery) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SENT\tSTATUS\tTEMPLATE\tMESSAGE ID\tDETAIL")
	for _, d := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			d.SentAt, statusWord(d.Status), orDash(d.Template), d.MessageID, orDash(d.Detail))
	}
	_ = tw.Flush()
}

func statusWord(s *string) string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return "(pending)"
	}
	return *s
}

func orDash(s *string) string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return "-"
	}
	return *s
}

func humanWindow(sec int) string {
	switch {
	case sec%86400 == 0:
		return strconv.Itoa(sec/86400) + "d"
	case sec%3600 == 0:
		return strconv.Itoa(sec/3600) + "h"
	case sec%60 == 0:
		return strconv.Itoa(sec/60) + "m"
	default:
		return strconv.Itoa(sec) + "s"
	}
}
