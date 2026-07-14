// Package testuser wires the `palbase test-user ...` subcommand group.
//
// Transport: Studio tRPC (`testData.*`), reached via the same user-JWT
// `internal/studio` client the `apps`/`secret`/`db` commands use. We talk to
// tRPC directly because the test-data feature (0b-3) is exposed ONLY as a tRPC
// router — there are no `/api/v1/...` REST routes for it, so the Management-API
// REST transport (used by `apikey`/`project`) cannot reach it.
//
// The procedures hit:
//
//	testData.testUserCreate  mutation {ref,count,withTokens}  -> {users:[...]}
//	testData.runScenario     mutation {ref,name}              -> {user,inserted}
//
// Studio runs the developer env-role authorization inside the tRPC procedure;
// a below-role / non-member caller surfaces as a FORBIDDEN error here. NO
// secret is generated client-side — palauth mints the passwords + tokens
// server-side; the CLI only displays them.
package testuser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// Studio is the tRPC transport subset the test-user commands need.
// *studio.Client satisfies it; tests substitute a server-backed client.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
	Mutation(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries the lazily-built Studio client + the shared selection
// resolver, populated by PersistentPreRunE before any subcommand fires.
type Resolvers struct {
	Studio    func() Studio
	Selection func() *selection.Resolver
}

// Cmd returns the `test-user` parent command (registered under `auth`).
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test-user",
		Short: "Mint disposable is_test users (optionally populated from a scenario)",
		Long: `Mint disposable test users for the SELECTED environment.

  palbase test-user create                    Mint 1 plain test user.
  palbase test-user create --count 5          Mint 5 plain test users.
  palbase test-user create --scenario demo    Mint 1 user + populate their data
                                              tree from a saved scenario.
  palbase test-user create --json             Emit creds+token as JSON.

Each environment verifies tokens against its OWN auth, so a minted token is only
valid on the environment that minted it. Override the target with --environment.

The minted users are is_test; the server mints their passwords + access tokens.`,
	}
	cmd.AddCommand(createCmd(r))
	return cmd
}

// plainUser mirrors one entry of testData.testUserCreate's {users:[...]} result.
type plainUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	AccessToken string `json:"accessToken"`
}

type plainResult struct {
	Users []plainUser `json:"users"`
}

// scenarioResult mirrors testData.runScenario's return shape: the minted user's
// creds+token + a per-table count of inserted rows.
type scenarioResult struct {
	User struct {
		ID          string `json:"id"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		AccessToken string `json:"access_token"`
	} `json:"user"`
	Inserted map[string]int `json:"inserted"`
}

func createCmd(r Resolvers) *cobra.Command {
	var (
		scenario string
		count    int
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Args:  cobra.NoArgs,
		Short: "Mint test user(s) for the selected environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			sel, err := r.Selection().Resolve(cmd.Context())
			if err != nil {
				return err
			}
			ref := sel.Ref()
			out := cmd.OutOrStdout()

			// --scenario: mint ONE user + populate their data tree from the
			// saved scenario. --count is meaningless here (a scenario mints
			// exactly one user), so reject the combination rather than silently
			// ignore it.
			if scenario != "" {
				if cmd.Flags().Changed("count") {
					return fmt.Errorf("--count cannot be combined with --scenario (a scenario mints exactly one user)")
				}
				var res scenarioResult
				scenarioPayload := map[string]any{
					"ref":  ref,
					"name": scenario,
				}
				if err := r.Studio().Mutation(cmd.Context(), "testData.runScenario", scenarioPayload, &res); err != nil {
					return err
				}
				if jsonOut {
					return encodeJSON(out, res)
				}
				fmt.Fprintf(out, "✓ ran scenario %q — minted 1 test user\n", scenario)
				fmt.Fprintf(out, "  id:       %s\n", res.User.ID)
				fmt.Fprintf(out, "  email:    %s\n", res.User.Email)
				fmt.Fprintf(out, "  password: %s\n", res.User.Password)
				if res.User.AccessToken != "" {
					fmt.Fprintf(out, "  token:    %s\n", res.User.AccessToken)
				}
				if len(res.Inserted) > 0 {
					fmt.Fprintln(out, "  inserted:")
					tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
					for table, n := range res.Inserted {
						fmt.Fprintf(tw, "    %s\t%d\n", table, n)
					}
					_ = tw.Flush()
				}
				fmt.Fprintln(out, "  (creds shown once — store them now)")
				return nil
			}

			// No --scenario: mint `count` plain test users (no data tree).
			if count < 1 {
				return fmt.Errorf("--count must be at least 1")
			}
			var res plainResult
			payload := map[string]any{
				"ref":        ref,
				"count":      count,
				"withTokens": true,
			}
			if err := r.Studio().Mutation(cmd.Context(), "testData.testUserCreate", payload, &res); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(out, res)
			}
			fmt.Fprintf(out, "✓ minted %d test user(s)\n", len(res.Users))
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tEMAIL\tPASSWORD\tTOKEN")
			for _, u := range res.Users {
				token := u.AccessToken
				if token == "" {
					token = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.ID, u.Email, u.Password, token)
			}
			_ = tw.Flush()
			fmt.Fprintln(out, "(creds shown once — store them now)")
			return nil
		},
	}
	cmd.Flags().StringVar(&scenario, "scenario", "", "Populate the minted user's data tree from a saved scenario")
	cmd.Flags().IntVar(&count, "count", 1, "Number of plain test users to mint (ignored with --scenario)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit creds+token as JSON (for scripting)")
	return cmd
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
