// Package members wires `palbase members ...` — group (umbrella) membership
// management from the terminal: list members + pending invitations, invite by
// email, change a member's role, remove a member, and handle YOUR OWN pending
// invitations (list/accept). Transport: Studio tRPC `groupMembers.*` (invite/
// role/remove are admin+ gated server-side; accept requires the logged-in
// user's email to match the invite).
package members

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// Studio is the tRPC transport subset the members commands need.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
	Mutation(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries the lazily-built Studio client (apps.Resolvers pattern).
type Resolvers struct {
	Studio func() Studio
}

// memberRow / invitationRow mirror the groupMembers router's MAPPED (camelCase)
// return shapes — the router re-maps its snake_case DB rows before returning.
type memberRow struct {
	UserID      string  `json:"userId"`
	Role        string  `json:"role"`
	JoinedAt    string  `json:"joinedAt"`
	DisplayName *string `json:"displayName"`
}

type membersResp struct {
	CallerRole string      `json:"callerRole"`
	Members    []memberRow `json:"members"`
}

type invitationRow struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	InvitedBy string `json:"invitedBy"`
	CreatedAt string `json:"createdAt"`
}

type myInvitationRow struct {
	ID          string  `json:"id"`
	GroupID     string  `json:"groupId"`
	ProjectRef  *string `json:"projectRef"`
	ProjectName string  `json:"projectName"`
	Role        string  `json:"role"`
	CreatedAt   string  `json:"createdAt"`
}

// Cmd returns the `palbase members` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "members",
		Short: "Manage a group's members and invitations",
		Long: `Manage the members of a group (umbrella) — the unit that owns a product's
environments and apps. Find your group id with 'palbase groups list'.

  palbase members list --group <grpId>                     Members + pending invites.
  palbase members invite <email> --group <grpId> [--role]  Invite by email.
  palbase members role <userId> <admin|member> --group <g> Change a member's role.
  palbase members remove <userId> --group <grpId>          Remove a member.
  palbase members invitations                              YOUR pending invitations.
  palbase members accept <invitationId>                    Accept one of yours.

Invite/role/remove need admin+ on the group (enforced server-side).`,
	}
	cmd.AddCommand(listCmd(r.Studio), inviteCmd(r.Studio), roleCmd(r.Studio),
		removeCmd(r.Studio), invitationsCmd(r.Studio), acceptCmd(r.Studio))
	return cmd
}

func listCmd(studioFn func() Studio) *cobra.Command {
	var grpID string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a group's members and pending invitations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var resp membersResp
			if err := studioFn().Query(cmd.Context(), "groupMembers.members",
				map[string]any{"grpId": grpID}, &resp); err != nil {
				return err
			}
			// Pending invitations are admin+ only — a plain member gets
			// FORBIDDEN. Best-effort: show the section when allowed, skip
			// silently when not.
			pending := []invitationRow{}
			_ = studioFn().Query(cmd.Context(), "groupMembers.pendingInvitations",
				map[string]any{"grpId": grpID}, &pending)
			if jsonOut {
				return encodeJSON(map[string]any{
					"caller_role":         resp.CallerRole,
					"members":             resp.Members,
					"pending_invitations": pending,
				})
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "USER\tROLE\tNAME\tJOINED")
			for _, m := range resp.Members {
				name := "-"
				if m.DisplayName != nil {
					name = *m.DisplayName
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.UserID, m.Role, name, m.JoinedAt)
			}
			_ = tw.Flush()
			if len(pending) > 0 {
				fmt.Fprintln(os.Stdout, "\npending invitations:")
				tw = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tEMAIL\tROLE\tSINCE")
				for _, i := range pending {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", i.ID, i.Email, i.Role, i.CreatedAt)
				}
				_ = tw.Flush()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&grpID, "group", "", "Group id (see `palbase groups list`; required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	_ = cmd.MarkFlagRequired("group")
	return cmd
}

func inviteCmd(studioFn func() Studio) *cobra.Command {
	var grpID, role string
	cmd := &cobra.Command{
		Use:   "invite <email>",
		Short: "Invite someone to the group by email",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if role != "admin" && role != "member" {
				return fmt.Errorf("--role must be admin or member")
			}
			var out struct {
				ID string `json:"id"`
			}
			if err := studioFn().Mutation(cmd.Context(), "groupMembers.invite",
				map[string]any{"grpId": grpID, "email": args[0], "role": role}, &out); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "✓ invited %s as %s (invitation %s)\n", args[0], role, out.ID)
			fmt.Fprintln(os.Stdout, "  they accept with: palbase members accept "+out.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&grpID, "group", "", "Group id (required)")
	cmd.Flags().StringVar(&role, "role", "member", "Role: member | admin")
	_ = cmd.MarkFlagRequired("group")
	return cmd
}

func roleCmd(studioFn func() Studio) *cobra.Command {
	var grpID string
	cmd := &cobra.Command{
		Use:   "role <userId> <admin|member>",
		Short: "Change a member's role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] != "admin" && args[1] != "member" {
				return fmt.Errorf("role must be admin or member")
			}
			var out struct {
				OK bool `json:"ok"`
			}
			if err := studioFn().Mutation(cmd.Context(), "groupMembers.changeMemberRole",
				map[string]any{"grpId": grpID, "userId": args[0], "role": args[1]}, &out); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "✓ %s is now %s\n", args[0], args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&grpID, "group", "", "Group id (required)")
	_ = cmd.MarkFlagRequired("group")
	return cmd
}

func removeCmd(studioFn func() Studio) *cobra.Command {
	var grpID string
	cmd := &cobra.Command{
		Use:   "remove <userId>",
		Short: "Remove a member from the group (owner cannot be removed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				OK bool `json:"ok"`
			}
			if err := studioFn().Mutation(cmd.Context(), "groupMembers.removeMember",
				map[string]any{"grpId": grpID, "userId": args[0]}, &out); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "✓ removed %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&grpID, "group", "", "Group id (required)")
	_ = cmd.MarkFlagRequired("group")
	return cmd
}

func invitationsCmd(studioFn func() Studio) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "invitations",
		Short: "List YOUR pending group invitations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows := []myInvitationRow{}
			if err := studioFn().Query(cmd.Context(), "groupMembers.listMyInvitations", nil, &rows); err != nil {
				return err
			}
			if jsonOut {
				return encodeJSON(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(os.Stdout, "No pending invitations.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tGROUP\tROLE\tSINCE")
			for _, i := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", i.ID, i.ProjectName, i.Role, i.CreatedAt)
			}
			_ = tw.Flush()
			fmt.Fprintln(os.Stdout, "\naccept one: palbase members accept <id>")
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func acceptCmd(studioFn func() Studio) *cobra.Command {
	return &cobra.Command{
		Use:   "accept <invitationId>",
		Short: "Accept one of YOUR pending invitations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				OK bool `json:"ok"`
			}
			if err := studioFn().Mutation(cmd.Context(), "groupMembers.acceptInvitation",
				map[string]any{"invitationId": args[0]}, &out); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "✓ invitation accepted — see your groups with `palbase groups list`\n")
			return nil
		},
	}
}

func encodeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
