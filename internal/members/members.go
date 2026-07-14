// Package members wires `palbase members ...` — PROJECT membership management
// from the terminal: list members + pending invitations, invite by email,
// change a member's role, remove a member, and handle YOUR OWN pending
// invitations (list/accept).
//
// Members live on the PROJECT (the product boundary), not on an Environment and
// not on an Organization: Organization membership is billing/admin territory and
// is deliberately outside the CLI (spec §7.3). Transport: Studio tRPC
// `projectMembers.*` (invite/role/remove are Project admin+ gated server-side;
// accept requires the logged-in user's email to match the invite).
package members

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/palgroup/palbase-cli/internal/selection"
	"github.com/spf13/cobra"
)

// Studio is the tRPC transport subset the members commands need.
type Studio interface {
	Query(ctx context.Context, path string, input any, out any) error
	Mutation(ctx context.Context, path string, input any, out any) error
}

// Resolvers carries the lazily-built Studio client + the shared selection
// resolver (which owns --project).
type Resolvers struct {
	Studio    func() Studio
	Selection func() *selection.Resolver
}

func (r Resolvers) projectID(ctx context.Context) (string, error) {
	return r.Selection().ProjectID(ctx)
}

// memberRow / invitationRow mirror the projectMembers router's MAPPED
// (camelCase) return shapes.
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
	ProjectID   string  `json:"projectId"`
	ProjectName string  `json:"projectName"`
	Role        string  `json:"role"`
	CreatedAt   string  `json:"createdAt"`
	EnvRef      *string `json:"environmentRef"`
}

// Cmd returns the `palbase members` parent command.
func Cmd(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "members",
		Short: "Manage the selected project's members and invitations",
		Long: `Manage the members of the SELECTED project (override with --project).

  palbase members list                          Members + pending invites.
  palbase members invite <email> [--role]       Invite by email.
  palbase members role <userId> <admin|member>  Change a member's role.
  palbase members remove <userId>               Remove a member.
  palbase members invitations                   YOUR pending invitations.
  palbase members accept <invitationId>         Accept one of yours.

Invite/role/remove need Project admin+ (enforced server-side). Organization
membership and billing administration are Studio/API surfaces, not CLI ones.`,
	}
	cmd.AddCommand(listCmd(r), inviteCmd(r), roleCmd(r),
		removeCmd(r), invitationsCmd(r), acceptCmd(r))
	return cmd
}

func listCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's members and pending invitations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			projectID, err := r.projectID(ctx)
			if err != nil {
				return err
			}
			var resp membersResp
			if err := r.Studio().Query(ctx, "projectMembers.members",
				map[string]any{"projectId": projectID}, &resp); err != nil {
				return err
			}
			// Pending invitations are admin+ only — a plain member gets FORBIDDEN.
			// Best-effort: show the section when allowed, skip silently when not.
			pending := []invitationRow{}
			_ = r.Studio().Query(ctx, "projectMembers.pendingInvitations",
				map[string]any{"projectId": projectID}, &pending)

			out := cmd.OutOrStdout()
			if jsonOut {
				return encodeJSON(out, map[string]any{
					"project_id":          projectID,
					"caller_role":         resp.CallerRole,
					"members":             resp.Members,
					"pending_invitations": pending,
				})
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
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
				fmt.Fprintln(out, "\npending invitations:")
				tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tEMAIL\tROLE\tSINCE")
				for _, i := range pending {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", i.ID, i.Email, i.Role, i.CreatedAt)
				}
				_ = tw.Flush()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func inviteCmd(r Resolvers) *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:   "invite <email>",
		Short: "Invite someone to the project by email",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if role != "admin" && role != "member" {
				return fmt.Errorf("--role must be admin or member")
			}
			projectID, err := r.projectID(cmd.Context())
			if err != nil {
				return err
			}
			var out struct {
				ID string `json:"id"`
			}
			if err := r.Studio().Mutation(cmd.Context(), "projectMembers.invite",
				map[string]any{"projectId": projectID, "email": args[0], "role": role}, &out); err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "✓ invited %s as %s (invitation %s)\n", args[0], role, out.ID)
			fmt.Fprintln(w, "  they accept with: palbase members accept "+out.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "member", "Role: member | admin")
	return cmd
}

func roleCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "role <userId> <admin|member>",
		Short: "Change a member's role (project owner only)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] != "admin" && args[1] != "member" {
				return fmt.Errorf("role must be admin or member")
			}
			projectID, err := r.projectID(cmd.Context())
			if err != nil {
				return err
			}
			var out struct {
				OK bool `json:"ok"`
			}
			if err := r.Studio().Mutation(cmd.Context(), "projectMembers.changeMemberRole",
				map[string]any{"projectId": projectID, "userId": args[0], "role": args[1]}, &out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s is now %s\n", args[0], args[1])
			return nil
		},
	}
}

func removeCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <userId>",
		Short: "Remove a member from the project (the owner cannot be removed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, err := r.projectID(cmd.Context())
			if err != nil {
				return err
			}
			var out struct {
				OK bool `json:"ok"`
			}
			if err := r.Studio().Mutation(cmd.Context(), "projectMembers.removeMember",
				map[string]any{"projectId": projectID, "userId": args[0]}, &out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ removed %s\n", args[0])
			return nil
		},
	}
}

func invitationsCmd(r Resolvers) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "invitations",
		Short: "List YOUR pending project invitations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows := []myInvitationRow{}
			if err := r.Studio().Query(cmd.Context(), "projectMembers.listMyInvitations", nil, &rows); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return encodeJSON(out, rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(out, "No pending invitations.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tPROJECT\tROLE\tSINCE")
			for _, i := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", i.ID, i.ProjectName, i.Role, i.CreatedAt)
			}
			_ = tw.Flush()
			fmt.Fprintln(out, "\naccept one: palbase members accept <id>")
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func acceptCmd(r Resolvers) *cobra.Command {
	return &cobra.Command{
		Use:   "accept <invitationId>",
		Short: "Accept one of YOUR pending invitations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				OK bool `json:"ok"`
			}
			if err := r.Studio().Mutation(cmd.Context(), "projectMembers.acceptInvitation",
				map[string]any{"invitationId": args[0]}, &out); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "✓ invitation accepted — see your projects with `palbase project list`")
			return nil
		},
	}
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
