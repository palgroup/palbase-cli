package roles

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/palgroup/palbase-cli/internal/backend"
)

// userRolesBody is what the ASSIGNMENT endpoints answer with: the names a user
// holds, not the definitions the environment carries. Two different questions,
// two different shapes — `{"roles":["agent"]}` here, `{"roles":[{name,…}]}` for
// definitions.
type userRolesBody struct {
	Roles []string `json:"roles"`
}

// callUserRoles talks to a user's own door and keeps the stack's sentence.
//
// A refusal here is almost always one of two things a person can fix — the role
// is not defined yet, or the user id is wrong — so the message says which and,
// for the first, what command defines it. "role_not_defined" alone is a code,
// not a direction.
// Kullanıcı ve rol adı da YOLA kaçırılarak giriyor — `rolePath`'in gerekçesi
// birebir burada da geçerli, ve burada iki serbest parça var.
func userRolesPath(uid string) string { return "/admin/users/" + url.PathEscape(uid) + "/roles" }

func userRolePath(uid, role string) string { return userRolesPath(uid) + "/" + url.PathEscape(role) }

func callUserRoles(cmd *cobra.Command, method, path string) (*userRolesBody, error) {
	target, cred, err := resolveProject(cmd)
	if err != nil {
		return nil, err
	}
	status, raw, err := backend.CallProject(cmd.Context(), target, cred, method, path, nil, "")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		msg := describe(raw)
		if strings.Contains(msg, "not defined") || strings.Contains(string(raw), "role_not_defined") {
			return nil, fmt.Errorf("%s — define it first with `palbase roles create <name>`", msg)
		}
		return nil, fmt.Errorf("%s answered %d: %s", target.Describe(), status, msg)
	}
	var out userRolesBody
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("could not read the answer from %s: %w", target.Describe(), err)
		}
	}
	return &out, nil
}

func assignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <userId> <role>",
		Short: "Grant a role to a user",
		Long: "Grant a role to a user.\n\n" +
			"Takes effect on that user's NEXT request — authority is read from the database,\n" +
			"not from their token, so nobody has to sign in again.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid, role := args[0], args[1]
			out, err := callUserRoles(cmd, "PUT", userRolePath(uid, role))
			if err != nil {
				return err
			}
			cmd.Printf("✓ %s now holds: %s\n", uid, strings.Join(out.Roles, ", "))
			return nil
		},
	}
}

func revokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <userId> <role>",
		Short: "Take a role away from a user",
		Long: "Take a role away from a user.\n\n" +
			"Takes effect on that user's NEXT request — there is no token to wait out.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid, role := args[0], args[1]
			out, err := callUserRoles(cmd, "DELETE", userRolePath(uid, role))
			if err != nil {
				return err
			}
			held := strings.Join(out.Roles, ", ")
			if held == "" {
				held = "no roles"
			}
			cmd.Printf("✓ %s now holds: %s\n", uid, held)
			return nil
		},
	}
}

// printUserRoles is `roles list <userId>` — the assignment side of the same verb.
func printUserRoles(cmd *cobra.Command, uid string, asJSON bool) error {
	out, err := callUserRoles(cmd, "GET", userRolesPath(uid))
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if len(out.Roles) == 0 {
		cmd.Printf("%s holds no roles.\n", uid)
		return nil
	}
	for _, r := range out.Roles {
		cmd.Println(r)
	}
	return nil
}
