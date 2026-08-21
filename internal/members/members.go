// Package members wires `palbase members` over the v2 cloud.
//
// A project has ONE owner and a set of members. The owner is not a membership
// row — it lives on the project itself — which is why `remove` cannot take the
// last person off a project and leave it unreachable.
//
// THERE IS NO INVITATION. The control plane has no e-mail provider configured
// and says so in its own logs; accepting an invitation that cannot be delivered
// would leave somebody waiting for a message that never arrives. A member is an
// account that already exists, added by the address it signed up with.
package members

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// REST is the control-plane transport subset these commands use.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Target names the cloud project this directory acts on.
type Target interface {
	Ref() (string, bool)
	Describe() string
}

// Resolvers carries the lazily-built dependencies.
type Resolvers struct {
	REST   func() REST
	Target func() (Target, error)
}

// Member is one person on a project.
type Member struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// Cmd returns the `palbase members` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "members",
		Short: "See who can reach this project, and change it",
	}
	cmd.AddCommand(listCmd(r), addCmd(r), removeCmd(r))
	return cmd
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Args:  cobra.NoArgs,
		Short: "List this project's owner and members",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := cloudRef(r)
			if err != nil {
				return err
			}
			var rows []Member
			if err := r.REST().Do(cmd.Context(), http.MethodGet, membersPath(ref), nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(cmd.OutOrStdout(), rows)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ROLE\tEMAIL\tID")
			for _, m := range rows {
				email := m.Email
				if email == "" {
					// An id with no address is still a real person on this
					// project; printing a blank column would read as a bug.
					email = "(address not recorded yet)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Role, email, m.UserID)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func addCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <email>",
		Args:  cobra.ExactArgs(1),
		Short: "Add an existing account to this project",
		Long: `Add someone to this project by the address they signed up with.

The account must already exist: there is no invitation to accept, because this
cloud cannot send one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := cloudRef(r)
			if err != nil {
				return err
			}
			var added Member
			body := map[string]string{"email": args[0]}
			if err := r.REST().Do(cmd.Context(), http.MethodPost, membersPath(ref), body, &added); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s to %s\n", args[0], ref)
			return nil
		},
	}
	return cmd
}

func removeCmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <user-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Remove a member (never the owner)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := cloudRef(r)
			if err != nil {
				return err
			}
			path := membersPath(ref) + "/" + url.PathEscape(args[0])
			if err := r.REST().Do(cmd.Context(), http.MethodDelete, path, nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from %s\n", args[0], ref)
			return nil
		},
	}
	return cmd
}

func membersPath(ref string) string {
	return "/v1/cloud/projects/" + url.PathEscape(ref) + "/members"
}

func cloudRef(r Resolvers) (string, error) {
	target, err := r.Target()
	if err != nil {
		return "", err
	}
	ref, ok := target.Ref()
	if !ok {
		return "", fmt.Errorf("%s is not a project on this cloud — it has no membership", target.Describe())
	}
	return ref, nil
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
