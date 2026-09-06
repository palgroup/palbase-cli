package notifications

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

const statusPath = "/v1/management/notifications/status"

type channelStatus struct {
	Channel string `json:"channel"`
	OK      *bool  `json:"ok"`
	Via     string `json:"via,omitempty"`
	Because string `json:"because,omitempty"`
	Remedy  string `json:"remedy,omitempty"`
	Error   string `json:"error,omitempty"`
}

func statusCmd(r Resolvers) *cobra.Command {
	var channel string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check notification provider configuration on the linked backend",
		Long: `Read the backend's current notification provider configuration.
No message is sent. This does not verify Meta template approval, account
permissions, or delivery. With --channel, an unconfigured channel exits nonzero.

Examples:
  palbase notifications status
  palbase notifications status --channel whatsapp --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if channel != "" && channel != "email" && channel != "sms" && channel != "whatsapp" && channel != "push" {
				return fmt.Errorf("--channel must be email, sms, whatsapp or push")
			}
			raw, err := call(r, cmd, http.MethodGet, statusPath, nil)
			if err != nil {
				return err
			}
			var report struct {
				Channels []channelStatus `json:"channels"`
			}
			if json.Unmarshal(raw, &report) != nil || len(report.Channels) == 0 {
				return fmt.Errorf("this backend did not return a notification status report; update its core version")
			}
			filtered := make([]channelStatus, 0, len(report.Channels))
			for _, item := range report.Channels {
				if item.Channel == "" || item.OK == nil {
					return fmt.Errorf("this backend returned an incomplete notification status report")
				}
				if channel == "" || item.Channel == channel {
					filtered = append(filtered, item)
				}
			}
			if len(filtered) == 0 {
				return fmt.Errorf("this backend does not report the %s channel; update its core version", channel)
			}
			report.Channels = filtered
			out := cmd.OutOrStdout()
			if asJSON {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return err
				}
			} else {
				for _, item := range filtered {
					if item.Error != "" {
						fmt.Fprintf(out, "%s: could not check — %s\n", item.Channel, item.Because)
					} else if *item.OK {
						fmt.Fprintf(out, "%s: configured (%s)\n", item.Channel, item.Via)
					} else {
						fmt.Fprintf(out, "%s: not configured — %s\n", item.Channel, item.Because)
					}
					if (!*item.OK || item.Error != "") && item.Remedy != "" {
						fmt.Fprintln(out, "  "+item.Remedy)
					}
				}
				fmt.Fprintln(out, "Configuration check only; no message was sent.")
			}
			if channel != "" && (!*filtered[0].OK || filtered[0].Error != "") {
				return fmt.Errorf("%s provider configuration is not ready", channel)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "check only email, sms, whatsapp or push")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the configuration report as JSON")
	return cmd
}
