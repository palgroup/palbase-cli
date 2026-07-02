// Package github wires `palbase github ...` — the GitHub App connection the
// github-mode deploy flow rides on (`palbase project create --github-account
// <installationId> --repo <name>`).
//
// The App-install handshake itself is CSRF-cookie-bound to the Studio origin
// (installUrl mints a signed state cookie the browser must echo back to
// Studio's /api/github/callback), so the CLI can NOT drive it headless.
// `github connect` therefore opens Studio's own connect page in the browser
// and polls `github.status` until the connection lands — the same pattern as
// `palbase login`. `status`/`accounts` are plain reads.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/auth"
)

// Studio is the tRPC transport subset the github commands need.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries the lazily-built Studio client + the Studio origin (for
// the browser connect page).
type Resolvers struct {
	Studio    func() Studio
	StudioURL func() string
}

type statusResp struct {
	Connected   bool   `json:"connected"`
	GithubLogin string `json:"githubLogin"`
}

type account struct {
	InstallationID int64  `json:"installationId"`
	AccountLogin   string `json:"accountLogin"`
	AccountType    string `json:"accountType"`
}

// pollInterval/pollBudget pace the connect wait. Vars so tests can shrink them.
var (
	pollInterval = 3 * time.Second
	pollBudget   = 3 * time.Minute
)

// Cmd returns the `palbase github` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Connect GitHub and list installations (for github-mode projects)",
		Long: `Manage the GitHub App connection github-mode deploys ride on.

  palbase github status     Are you connected, and as whom?
  palbase github accounts   Installations you can pass to 'project create --github-account'.
  palbase github connect    Open the browser handshake and wait for it to land.`,
	}
	cmd.AddCommand(statusCmd(r), accountsCmd(r), connectCmd(r))
	return cmd
}

func fetchStatus(ctx context.Context, s Studio) (statusResp, error) {
	var st statusResp
	err := s.Query(ctx, "github.status", nil, &st)
	return st, err
}

func printAccounts(ctx context.Context, s Studio, jsonOut bool) error {
	accounts := []account{}
	if err := s.Query(ctx, "github.listAccounts", nil, &accounts); err != nil {
		// listAccounts refreshes the GitHub user token; a dead refresh token
		// surfaces as github_reauth_required even though status says
		// "connected". Point at the fix instead of dumping the raw error.
		if strings.Contains(err.Error(), "github_reauth_required") {
			return fmt.Errorf("your GitHub token needs re-authorization — run `palbase github connect` and complete the browser flow")
		}
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(accounts)
	}
	if len(accounts) == 0 {
		fmt.Fprintln(os.Stdout, "No installations — run `palbase github connect` (and install the App on your account/org).")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "INSTALLATION\tACCOUNT\tTYPE")
	for _, a := range accounts {
		fmt.Fprintf(tw, "%d\t%s\t%s\n", a.InstallationID, a.AccountLogin, a.AccountType)
	}
	_ = tw.Flush()
	fmt.Fprintln(os.Stdout, "\nuse one: palbase project create <ref> --name \"...\" --github-account <INSTALLATION> --repo <name>")
	return nil
}

func statusCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the GitHub connection state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := fetchStatus(cmd.Context(), r.Studio())
			if err != nil {
				return err
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}
			if !st.Connected {
				fmt.Fprintln(os.Stdout, "not connected — run `palbase github connect`")
				return nil
			}
			fmt.Fprintf(os.Stdout, "✓ connected as %s\n", st.GithubLogin)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func accountsCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "List GitHub installations you can create projects under",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return printAccounts(cmd.Context(), r.Studio(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func connectCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "connect",
		Short: "Connect your GitHub account via the browser, then wait for it to land",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := fetchStatus(cmd.Context(), r.Studio())
			if err != nil {
				return err
			}
			if st.Connected {
				fmt.Fprintf(os.Stdout, "✓ already connected as %s\n", st.GithubLogin)
				return printAccounts(cmd.Context(), r.Studio(), false)
			}

			connectURL := r.StudioURL() + "/settings/github"
			fmt.Fprintf(os.Stdout, "Opening %s — complete the GitHub App install there …\n", connectURL)
			if err := auth.OpenURL(connectURL); err != nil {
				fmt.Fprintf(os.Stdout, "could not open a browser (%v) — open the URL yourself\n", err)
			}

			deadline := time.Now().Add(pollBudget)
			for time.Now().Before(deadline) {
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(pollInterval):
				}
				st, err := fetchStatus(cmd.Context(), r.Studio())
				if err != nil {
					continue // transient — keep waiting
				}
				if st.Connected {
					fmt.Fprintf(os.Stdout, "✓ connected as %s\n", st.GithubLogin)
					return printAccounts(cmd.Context(), r.Studio(), false)
				}
			}
			return fmt.Errorf("timed out waiting for the GitHub connection — finish the browser flow and check `palbase github status`")
		},
	}
}
